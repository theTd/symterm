package admin

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"symterm/internal/proto"
)

type DaemonInfo struct {
	Version         string    `json:"version"`
	StartedAt       time.Time `json:"started_at"`
	ListenAddr      string    `json:"listen_addr"`
	AdminSocketPath string    `json:"admin_socket_path"`
	AdminWebAddr    string    `json:"admin_web_addr"`
}

type Snapshot struct {
	Cursor   uint64            `json:"cursor"`
	Daemon   DaemonInfo        `json:"daemon"`
	Sessions []SessionSnapshot `json:"sessions"`
	Users    []UserRecord      `json:"users"`
}

type UserDetail struct {
	User   UserRecord        `json:"user"`
	Tokens []UserTokenRecord `json:"tokens"`
}

type SessionListFilter struct {
	Search        string
	Username      string
	Role          proto.Role
	ProjectState  proto.ProjectState
	IncludeClosed bool
}

type Overview struct {
	Daemon                 DaemonInfo    `json:"daemon"`
	ActiveSessionCount     int           `json:"active_session_count"`
	ClosedSessionCount     int           `json:"closed_session_count"`
	DisabledUserCount      int           `json:"disabled_user_count"`
	NeedsConfirmationCount int           `json:"needs_confirmation_count"`
	RecentEvents           []Event       `json:"recent_events"`
	RecentAudit            []AuditRecord `json:"recent_audit"`
}

type Service struct {
	sessions SessionCatalog
	events   *EventHub
	store    *Store
	now      func() time.Time
	tmux     TmuxStatusSource

	daemonMu sync.RWMutex
	daemon   DaemonInfo
}

type TmuxStatusSource interface {
	TmuxStatus(string, string) (proto.TmuxStatusSnapshot, error)
}

func NewService(sessionCatalog SessionCatalog, events *EventHub, store *Store, daemonInfo DaemonInfo, now func() time.Time) (*Service, error) {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if events == nil {
		var err error
		events, err = NewEventHub(defaultEventRetention)
		if err != nil {
			return nil, err
		}
	}
	service := &Service{
		sessions: sessionCatalog,
		events:   events,
		store:    store,
		now:      now,
		daemon:   daemonInfo,
	}
	service.events.RecordDaemon(daemonInfo)
	return service, nil
}

func (s *Service) SetListenAddr(addr string) {
	s.daemonMu.Lock()
	s.daemon.ListenAddr = addr
	s.daemonMu.Unlock()
	s.events.RecordDaemon(s.DaemonInfo())
}

func (s *Service) SetAdminWebAddr(addr string) {
	s.daemonMu.Lock()
	s.daemon.AdminWebAddr = addr
	s.daemonMu.Unlock()
	s.events.RecordDaemon(s.DaemonInfo())
}

func (s *Service) DaemonInfo() DaemonInfo {
	s.daemonMu.RLock()
	defer s.daemonMu.RUnlock()
	return s.daemon
}

func (s *Service) SetTmuxStatusSource(source TmuxStatusSource) {
	s.tmux = source
}

func (s *Service) Snapshot() Snapshot {
	return Snapshot{
		Cursor:   s.currentCursor(),
		Daemon:   s.DaemonInfo(),
		Sessions: s.ListSessions(),
		Users:    s.store.ListUsers(),
	}
}

func (s *Service) GetSession(sessionID string) (SessionSnapshot, bool) {
	if s.events != nil {
		if snapshot, ok := s.events.GetSession(sessionID); ok {
			return snapshot, true
		}
	}
	if s.sessions == nil {
		return SessionSnapshot{}, false
	}
	return s.sessions.GetSession(sessionID)
}

func (s *Service) TerminateSession(actor string, sessionID string) error {
	if s.sessions == nil {
		err := errors.New("session catalog is unavailable")
		s.recordAudit("terminate_session", actor, sessionID, "error:"+err.Error())
		return err
	}
	if err := s.sessions.TerminateSession(sessionID, "terminated by admin"); err != nil {
		s.recordAudit("terminate_session", actor, sessionID, "error:"+err.Error())
		return err
	}
	s.recordAudit("terminate_session", actor, sessionID, "ok")
	return nil
}

func (s *Service) ListSessions() []SessionSnapshot {
	return s.ListSessionsFiltered(SessionListFilter{})
}

func (s *Service) ListSessionsFiltered(filter SessionListFilter) []SessionSnapshot {
	if s.events == nil {
		if s.sessions == nil {
			return []SessionSnapshot{}
		}
		return filterSessions(s.sessions.ListSessions(), filter)
	}
	snapshots := s.events.ListSessions(filter.IncludeClosed)
	if len(snapshots) == 0 && s.sessions != nil {
		return filterSessions(s.sessions.ListSessions(), filter)
	}
	return filterSessions(snapshots, filter)
}

func (s *Service) GetTmuxStatus(clientID string, commandID string) (proto.TmuxStatusSnapshot, error) {
	if s.tmux == nil {
		return proto.TmuxStatusSnapshot{}, errors.New("tmux status source is unavailable")
	}
	return s.tmux.TmuxStatus(clientID, commandID)
}

func (s *Service) SubscribeEvents(since uint64) ([]Event, uint64, <-chan struct{}, error) {
	return s.events.Subscribe(since)
}

func (s *Service) EventsSince(since uint64) ([]Event, error) {
	return s.events.EventsSince(since)
}

func (s *Service) UnsubscribeEvents(subscriberID uint64) {
	s.events.Unsubscribe(subscriberID)
}

func (s *Service) ListUsers() []UserRecord {
	return s.store.ListUsers()
}

func (s *Service) GetUserDetail(username string) (UserDetail, bool) {
	user, ok := s.store.GetUser(username)
	if !ok {
		return UserDetail{}, false
	}
	return UserDetail{
		User:   user,
		Tokens: s.store.ListTokens(username),
	}, true
}

func (s *Service) CreateUser(actor string, username string, note string) (UserRecord, error) {
	record, err := s.store.CreateUser(username, note, s.now())
	if err != nil {
		s.recordAudit("create_user", actor, username, "error:"+err.Error())
		return UserRecord{}, err
	}
	s.recordAudit("create_user", actor, username, "ok")
	s.events.RecordUser(record)
	return record, nil
}

func (s *Service) DisableUser(actor string, username string) (UserRecord, error) {
	record, err := s.store.DisableUser(username, s.now())
	if err != nil {
		s.recordAudit("disable_user", actor, username, "error:"+err.Error())
		return UserRecord{}, err
	}
	s.recordAudit("disable_user", actor, username, "ok")
	s.events.RecordUser(record)
	return record, nil
}

func (s *Service) IssueUserToken(actor string, username string, description string) (IssuedToken, error) {
	token, err := s.store.IssueToken(username, description, s.now())
	if err != nil {
		s.recordAudit("issue_managed_token", actor, username, "error:"+err.Error())
		return IssuedToken{}, err
	}
	s.recordAudit("issue_managed_token", actor, fmt.Sprintf("%s:%s", username, token.Record.TokenID), "ok")
	if user, ok := s.store.GetUser(username); ok {
		s.events.RecordUser(user)
	}
	s.events.RecordToken(EventKindTokenIssued, token.Record)
	return token, nil
}

func (s *Service) RevokeUserToken(actor string, tokenID string) (UserTokenRecord, error) {
	record, err := s.store.RevokeToken(tokenID, s.now())
	if err != nil {
		s.recordAudit("revoke_managed_token", actor, tokenID, "error:"+err.Error())
		return UserTokenRecord{}, err
	}
	s.recordAudit("revoke_managed_token", actor, tokenID, "ok")
	s.events.RecordToken(EventKindTokenRevoked, record)
	return record, nil
}

func (s *Service) GetUserEntrypoint(username string) ([]string, error) {
	return s.store.GetUserEntrypoint(username)
}

func (s *Service) SetUserEntrypoint(actor string, username string, argv []string) (UserRecord, error) {
	record, err := s.store.SetUserEntrypoint(username, argv, s.now())
	if err != nil {
		s.recordAudit("set_entrypoint", actor, username, "error:"+err.Error())
		return UserRecord{}, err
	}
	s.recordAudit("set_entrypoint", actor, username, "ok")
	s.events.RecordUser(record)
	return record, nil
}

func (s *Service) Overview() (Overview, error) {
	users := s.store.ListUsers()
	disabledUserCount := 0
	for _, user := range users {
		if user.Disabled {
			disabledUserCount++
		}
	}

	sessions := s.ListSessionsFiltered(SessionListFilter{IncludeClosed: true})
	activeSessionCount := 0
	closedSessionCount := 0
	needsConfirmationCount := 0
	for _, session := range sessions {
		if strings.TrimSpace(session.CloseReason) == "" {
			activeSessionCount++
		} else {
			closedSessionCount++
		}
		if session.NeedsConfirmation || session.ProjectState == proto.ProjectStateNeedsConfirmation {
			needsConfirmationCount++
		}
	}

	auditPage, err := s.store.ListAudit(AuditQuery{Page: 1, PageSize: 20})
	if err != nil {
		return Overview{}, err
	}
	return Overview{
		Daemon:                 s.DaemonInfo(),
		ActiveSessionCount:     activeSessionCount,
		ClosedSessionCount:     closedSessionCount,
		DisabledUserCount:      disabledUserCount,
		NeedsConfirmationCount: needsConfirmationCount,
		RecentEvents:           s.recentEvents(20),
		RecentAudit:            auditPage.Items,
	}, nil
}

func (s *Service) ListAudit(query AuditQuery) (AuditPage, error) {
	return s.store.ListAudit(query)
}

func (s *Service) currentCursor() uint64 {
	if s.events == nil {
		return 0
	}
	return s.events.CurrentCursor()
}

func (s *Service) recentEvents(limit int) []Event {
	if s.events == nil || limit <= 0 {
		return []Event{}
	}
	events, err := s.events.EventsSince(0)
	if err != nil {
		return []Event{}
	}
	return summarizeRecentEvents(events, limit)
}

func summarizeRecentEvents(events []Event, limit int) []Event {
	if len(events) == 0 || limit <= 0 {
		return []Event{}
	}

	recent := make([]Event, 0, minInt(limit, len(events)))
	seenSessionUpserts := make(map[string]struct{})
	for idx := len(events) - 1; idx >= 0 && len(recent) < limit; idx-- {
		event := cloneEvent(events[idx])
		if shouldSkipRecentEvent(event, seenSessionUpserts) {
			continue
		}
		recent = append(recent, event)
	}

	for left, right := 0, len(recent)-1; left < right; left, right = left+1, right-1 {
		recent[left], recent[right] = recent[right], recent[left]
	}
	return nonNilSlice(recent)
}

func shouldSkipRecentEvent(event Event, seenSessionUpserts map[string]struct{}) bool {
	if event.Kind != EventKindSessionUpsert {
		return false
	}

	sessionID := strings.TrimSpace(event.SessionID)
	if event.Session != nil && strings.TrimSpace(event.Session.SessionID) != "" {
		sessionID = strings.TrimSpace(event.Session.SessionID)
	}
	if sessionID == "" {
		return false
	}
	if _, exists := seenSessionUpserts[sessionID]; exists {
		return true
	}
	seenSessionUpserts[sessionID] = struct{}{}
	return false
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func (s *Service) recordAudit(action string, actor string, target string, result string) {
	if s.store == nil {
		return
	}
	record, err := s.store.AppendAudit(action, actor, target, result, s.now())
	if err != nil {
		return
	}
	if s.events != nil {
		s.events.RecordAudit(record)
	}
}

func filterSessions(items []SessionSnapshot, filter SessionListFilter) []SessionSnapshot {
	var filtered []SessionSnapshot
	for _, item := range items {
		if !filter.IncludeClosed && strings.TrimSpace(item.CloseReason) != "" {
			continue
		}
		if filter.Username != "" && !strings.EqualFold(item.Principal.Username, strings.TrimSpace(filter.Username)) {
			continue
		}
		if filter.Role != "" && item.Role != filter.Role {
			continue
		}
		if filter.ProjectState != "" && item.ProjectState != filter.ProjectState {
			continue
		}
		if !sessionMatchesSearch(item, filter.Search) {
			continue
		}
		filtered = append(filtered, cloneSessionSnapshot(item))
	}
	return nonNilSlice(filtered)
}

func nonNilSlice[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func sessionMatchesSearch(item SessionSnapshot, raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return true
	}
	candidates := []string{
		item.SessionID,
		item.ClientID,
		item.ProjectID,
		item.WorkspaceRoot,
		item.WorkspaceDigest,
		item.Principal.Username,
		item.Principal.TokenID,
	}
	for _, candidate := range candidates {
		if strings.Contains(strings.ToLower(candidate), raw) {
			return true
		}
	}
	return false
}
