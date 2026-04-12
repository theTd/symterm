package control

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

type session struct {
	ClientID            string
	SessionID           string
	Principal           AuthenticatedPrincipal
	Username            string
	ProjectID           string
	WorkspaceRoot       string
	WorkspaceInstanceID string
	WorkspaceDigest     proto.WorkspaceDigest
	SessionKind         proto.SessionKind
	TransportHint       TransportKind
	RefCount            int
	ConnectedAt         time.Time
	LastActivityAt      time.Time
}

type liveSessionRecord struct {
	session   session
	control   *liveChannelBinding
	channels  map[string]*liveChannelBinding
	ownerFile ownerfs.Client
	closed    bool
}

type SessionRegistry struct {
	mu            sync.Mutex
	nextClientID  uint64
	nextSessionID uint64
	nextChannelID uint64
	sessions      map[string]*liveSessionRecord
	bySessionID   map[string]*liveSessionRecord
	ownerFiles    map[string]ownerfs.Client
	closed        map[string]LiveSessionSnapshot
}

type ownerFileRegistration struct {
	Session  session
	Previous ownerfs.Client
}

type sessionRelease struct {
	Session         session
	OwnerFileClient ownerfs.Client
	Released        bool
}

type ownerFileDisconnect struct {
	Session session
	Matched bool
}

func newSessionRegistry() *SessionRegistry {
	return &SessionRegistry{
		sessions:    make(map[string]*liveSessionRecord),
		bySessionID: make(map[string]*liveSessionRecord),
		ownerFiles:  make(map[string]ownerfs.Client),
		closed:      make(map[string]LiveSessionSnapshot),
	}
}

func (r *SessionRegistry) Hello(username string, request proto.HelloRequest) HelloResponse {
	return r.HelloPrincipal(AuthenticatedPrincipal{
		Username:    username,
		TokenSource: TokenSourceManaged,
	}, request, time.Now().UTC())
}

func (r *SessionRegistry) HelloPrincipal(principal AuthenticatedPrincipal, request proto.HelloRequest, now time.Time) HelloResponse {
	r.mu.Lock()
	defer r.mu.Unlock()

	if principal.AuthenticatedAt.IsZero() {
		principal.AuthenticatedAt = now
	}
	r.nextClientID++
	clientID := fmt.Sprintf("client-%04d", r.nextClientID)
	r.nextSessionID++
	sessionID := fmt.Sprintf("session-%04d", r.nextSessionID)
	record := &liveSessionRecord{
		session: session{
			ClientID:            clientID,
			SessionID:           sessionID,
			Principal:           principal,
			Username:            principal.Username,
			ProjectID:           request.ProjectID,
			WorkspaceRoot:       request.LocalWorkspaceRoot,
			WorkspaceInstanceID: strings.TrimSpace(request.WorkspaceInstanceID),
			WorkspaceDigest:     request.WorkspaceDigest,
			SessionKind:         proto.NormalizeSessionKind(request.SessionKind),
			TransportHint:       TransportKind(strings.TrimSpace(request.TransportKind)),
			RefCount:            1,
			ConnectedAt:         now,
			LastActivityAt:      now,
		},
		channels: make(map[string]*liveChannelBinding),
	}
	r.sessions[clientID] = record
	r.bySessionID[sessionID] = record
	delete(r.closed, sessionID)

	return HelloResponse{
		ClientID:         clientID,
		SessionID:        sessionID,
		Username:         principal.Username,
		ProjectID:        request.ProjectID,
		SyncCapabilities: defaultSyncCapabilities(),
	}
}

func (r *SessionRegistry) Session(clientID string) (session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionLocked(clientID)
}

func (r *SessionRegistry) sessionLocked(clientID string) (session, error) {
	record, ok := r.sessions[clientID]
	if !ok {
		return session{}, proto.NewError(proto.ErrUnknownClient, "client session does not exist")
	}
	return record.session, nil
}

func (r *SessionRegistry) RetainClient(clientID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, err := r.recordLocked(clientID)
	if err != nil {
		return err
	}
	record.session.RefCount++
	record.session.LastActivityAt = time.Now().UTC()
	return nil
}

func (r *SessionRegistry) ReleaseClient(clientID string) (sessionRelease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, err := r.recordLocked(clientID)
	if err != nil {
		return sessionRelease{}, err
	}
	record.session.RefCount--
	record.session.LastActivityAt = time.Now().UTC()
	if record.session.RefCount > 0 {
		return sessionRelease{}, nil
	}

	delete(r.sessions, clientID)
	delete(r.bySessionID, record.session.SessionID)
	delete(r.ownerFiles, clientID)
	record.closed = true
	closedSnapshot := r.snapshotLocked(record)
	if existing, ok := r.closed[record.session.SessionID]; ok && strings.TrimSpace(existing.CloseReason) != "" {
		closedSnapshot.CloseReason = existing.CloseReason
	} else {
		closedSnapshot.CloseReason = "client disconnected"
	}
	r.closed[record.session.SessionID] = closedSnapshot

	return sessionRelease{
		Session:         record.session,
		OwnerFileClient: record.ownerFile,
		Released:        true,
	}, nil
}

func (r *SessionRegistry) RegisterOwnerFileClient(clientID string, client ownerfs.Client) (ownerFileRegistration, error) {
	if client == nil {
		return ownerFileRegistration{}, proto.NewError(proto.ErrInvalidArgument, "owner file client is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	record, err := r.recordLocked(clientID)
	if err != nil {
		return ownerFileRegistration{}, err
	}
	previous := r.ownerFiles[clientID]
	record.ownerFile = client
	record.session.LastActivityAt = time.Now().UTC()
	r.ownerFiles[clientID] = client
	return ownerFileRegistration{
		Session:  record.session,
		Previous: previous,
	}, nil
}

func (r *SessionRegistry) OwnerFileClient(clientID string) ownerfs.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ownerFiles[clientID]
}

func (r *SessionRegistry) OwnerFileDisconnected(clientID string, client ownerfs.Client) (ownerFileDisconnect, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.ownerFiles[clientID]
	if !ok || current != client {
		return ownerFileDisconnect{}, nil
	}
	delete(r.ownerFiles, clientID)

	record, ok := r.sessions[clientID]
	if !ok {
		return ownerFileDisconnect{Matched: true}, nil
	}
	record.ownerFile = nil
	record.session.LastActivityAt = time.Now().UTC()
	return ownerFileDisconnect{
		Session: record.session,
		Matched: true,
	}, nil
}

func (r *SessionRegistry) RemoveProjectOwnerFileClients(key proto.ProjectKey, excludeClientID string) []ownerfs.Client {
	r.mu.Lock()
	defer r.mu.Unlock()

	var clients []ownerfs.Client
	for clientID, current := range r.ownerFiles {
		if clientID == excludeClientID {
			continue
		}
		record, ok := r.sessions[clientID]
		if !ok || projectKeyForSession(record.session) != key {
			continue
		}
		clients = append(clients, current)
		record.ownerFile = nil
		delete(r.ownerFiles, clientID)
	}
	return clients
}

func (r *SessionRegistry) ProjectSessionCount(key proto.ProjectKey) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	count := 0
	for _, record := range r.sessions {
		if projectKeyForSession(record.session) == key {
			count++
		}
	}
	return count
}

func (r *SessionRegistry) BindControl(clientID string, meta ConnMeta, counters *TrafficCounters, closer io.Closer) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, err := r.recordLocked(clientID)
	if err != nil {
		return err
	}
	meta.ChannelKind = ChannelKindControl
	if meta.TransportKind == TransportKindUnknown && record.session.TransportHint != "" {
		meta.TransportKind = record.session.TransportHint
	}
	if meta.ConnectedAt.IsZero() {
		meta.ConnectedAt = record.session.ConnectedAt
	}
	record.control = &liveChannelBinding{
		channelID: "control",
		meta:      normalizeConnMeta(meta),
		counters:  counters,
		closer:    closer,
	}
	record.session.LastActivityAt = time.Now().UTC()
	return nil
}

func (r *SessionRegistry) AttachChannel(clientID string, meta ConnMeta, counters *TrafficCounters, closer io.Closer) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, err := r.recordLocked(clientID)
	if err != nil {
		return "", err
	}
	r.nextChannelID++
	channelID := fmt.Sprintf("channel-%04d", r.nextChannelID)
	if meta.ConnectedAt.IsZero() {
		meta.ConnectedAt = time.Now().UTC()
	}
	record.channels[channelID] = &liveChannelBinding{
		channelID: channelID,
		meta:      normalizeConnMeta(meta),
		counters:  counters,
		closer:    closer,
	}
	record.session.LastActivityAt = meta.ConnectedAt
	return channelID, nil
}

func (r *SessionRegistry) DetachChannel(clientID string, channelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.sessions[clientID]
	if !ok {
		return
	}
	delete(record.channels, channelID)
	record.session.LastActivityAt = time.Now().UTC()
}

func (r *SessionRegistry) MarkActivity(clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.sessions[clientID]
	if !ok {
		return
	}
	record.session.LastActivityAt = time.Now().UTC()
}

func (r *SessionRegistry) ListLiveSessions() []LiveSessionSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	snapshots := make([]LiveSessionSnapshot, 0, len(r.sessions))
	for _, record := range r.sessions {
		snapshots = append(snapshots, r.snapshotLocked(record))
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].ConnectedAt.Equal(snapshots[j].ConnectedAt) {
			return snapshots[i].SessionID < snapshots[j].SessionID
		}
		return snapshots[i].ConnectedAt.Before(snapshots[j].ConnectedAt)
	})
	return snapshots
}

func (r *SessionRegistry) SessionSnapshot(sessionID string) (LiveSessionSnapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if record := r.bySessionID[sessionID]; record != nil {
		return r.snapshotLocked(record), true
	}
	snapshot, ok := r.closed[sessionID]
	return snapshot, ok
}

func (r *SessionRegistry) ClientSnapshot(clientID string) (LiveSessionSnapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record := r.sessions[clientID]
	if record == nil {
		return LiveSessionSnapshot{}, false
	}
	return r.snapshotLocked(record), true
}

func (r *SessionRegistry) TerminateSession(sessionID string, reason string) error {
	r.mu.Lock()
	record := r.bySessionID[sessionID]
	if record == nil {
		r.mu.Unlock()
		return proto.NewError(proto.ErrInvalidArgument, "session does not exist")
	}
	control := record.control
	channels := make([]*liveChannelBinding, 0, len(record.channels))
	for _, channel := range record.channels {
		channels = append(channels, channel)
	}
	r.mu.Unlock()

	if control != nil && control.closer != nil {
		_ = control.closer.Close()
	}
	for _, channel := range channels {
		if channel.closer != nil {
			_ = channel.closer.Close()
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if record := r.bySessionID[sessionID]; record != nil {
		snapshot := r.snapshotLocked(record)
		snapshot.CloseReason = strings.TrimSpace(reason)
		r.closed[sessionID] = snapshot
	}
	return nil
}

func (r *SessionRegistry) recordLocked(clientID string) (*liveSessionRecord, error) {
	record, ok := r.sessions[clientID]
	if !ok {
		return nil, proto.NewError(proto.ErrUnknownClient, "client session does not exist")
	}
	return record, nil
}

func (r *SessionRegistry) snapshotLocked(record *liveSessionRecord) LiveSessionSnapshot {
	snapshot := LiveSessionSnapshot{
		SessionID:           record.session.SessionID,
		ClientID:            record.session.ClientID,
		ProjectID:           record.session.ProjectID,
		WorkspaceRoot:       record.session.WorkspaceRoot,
		WorkspaceInstanceID: record.session.WorkspaceInstanceID,
		WorkspaceDigest:     record.session.WorkspaceDigest.Value,
		SessionKind:         record.session.SessionKind,
		Principal:           record.session.Principal,
		ConnectedAt:         record.session.ConnectedAt,
		LastActivityAt:      record.session.LastActivityAt,
	}
	if record.control != nil {
		in, out := record.control.counters.Snapshot()
		snapshot.Control = &SessionChannelSnapshot{
			ChannelID: record.control.channelID,
			Meta:      record.control.meta,
			BytesIn:   in,
			BytesOut:  out,
		}
		snapshot.ControlBytesIn = in
		snapshot.ControlBytesOut = out
	}
	for _, channel := range record.channels {
		in, out := channel.counters.Snapshot()
		item := SessionChannelSnapshot{
			ChannelID: channel.channelID,
			Meta:      channel.meta,
			BytesIn:   in,
			BytesOut:  out,
		}
		snapshot.Channels = append(snapshot.Channels, item)
		switch channel.meta.ChannelKind {
		case ChannelKindStdio:
			snapshot.StdioBytesIn += in
			snapshot.StdioBytesOut += out
		case ChannelKindOwnerFS:
			snapshot.OwnerFSBytesIn += in
			snapshot.OwnerFSBytesOut += out
		}
	}
	sort.Slice(snapshot.Channels, func(i, j int) bool {
		return snapshot.Channels[i].ChannelID < snapshot.Channels[j].ChannelID
	})
	return snapshot
}

func normalizeConnMeta(meta ConnMeta) ConnMeta {
	if meta.TransportKind == "" {
		meta.TransportKind = TransportKindUnknown
	}
	meta.RemoteAddr = normalizeConnAddr(meta.RemoteAddr)
	meta.LocalAddr = normalizeConnAddr(meta.LocalAddr)
	return meta
}

func normalizeConnAddr(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func projectKeyForSession(session session) proto.ProjectKey {
	return proto.ProjectKey{
		Username:  session.Username,
		ProjectID: session.ProjectID,
	}
}
