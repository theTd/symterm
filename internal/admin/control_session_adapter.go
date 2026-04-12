package admin

import (
	"errors"

	"symterm/internal/control"
)

type SessionCatalog interface {
	ListSessions() []SessionSnapshot
	GetSession(string) (SessionSnapshot, bool)
	TerminateSession(string, string) error
}

type controlSessionCatalog struct {
	source control.AdminSessionService
}

type controlSessionObserver struct {
	hub *EventHub
}

func NewControlSessionCatalog(source control.AdminSessionService) SessionCatalog {
	return controlSessionCatalog{source: source}
}

func NewControlSessionObserver(hub *EventHub) control.LiveSessionObserver {
	return controlSessionObserver{hub: hub}
}

func (c controlSessionCatalog) ListSessions() []SessionSnapshot {
	if c.source == nil {
		return nil
	}
	snapshots := c.source.ListSessions()
	result := make([]SessionSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		result = append(result, sessionSnapshotFromControl(snapshot))
	}
	return result
}

func (c controlSessionCatalog) GetSession(sessionID string) (SessionSnapshot, bool) {
	if c.source == nil {
		return SessionSnapshot{}, false
	}
	snapshot, ok := c.source.SessionSnapshot(sessionID)
	if !ok {
		return SessionSnapshot{}, false
	}
	return sessionSnapshotFromControl(snapshot), true
}

func (c controlSessionCatalog) TerminateSession(sessionID string, reason string) error {
	if c.source == nil {
		return errors.New("session catalog is unavailable")
	}
	return c.source.TerminateSession(sessionID, reason)
}

func (o controlSessionObserver) UpsertSession(snapshot control.LiveSessionSnapshot) {
	if o.hub == nil {
		return
	}
	o.hub.UpsertSession(sessionSnapshotFromControl(snapshot))
}

func (o controlSessionObserver) CloseSession(snapshot control.LiveSessionSnapshot) {
	if o.hub == nil {
		return
	}
	o.hub.CloseSession(sessionSnapshotFromControl(snapshot))
}

func sessionSnapshotFromControl(snapshot control.LiveSessionSnapshot) SessionSnapshot {
	result := SessionSnapshot{
		SessionID:            snapshot.SessionID,
		ClientID:             snapshot.ClientID,
		ProjectID:            snapshot.ProjectID,
		WorkspaceRoot:        snapshot.WorkspaceRoot,
		WorkspaceDigest:      snapshot.WorkspaceDigest,
		ConnectedAt:          snapshot.ConnectedAt,
		LastActivityAt:       snapshot.LastActivityAt,
		CloseReason:          snapshot.CloseReason,
		Role:                 snapshot.Role,
		ProjectState:         snapshot.ProjectState,
		SyncEpoch:            snapshot.SyncEpoch,
		NeedsConfirmation:    snapshot.NeedsConfirmation,
		ControlBytesIn:       snapshot.ControlBytesIn,
		ControlBytesOut:      snapshot.ControlBytesOut,
		StdioBytesIn:         snapshot.StdioBytesIn,
		StdioBytesOut:        snapshot.StdioBytesOut,
		OwnerFSBytesIn:       snapshot.OwnerFSBytesIn,
		OwnerFSBytesOut:      snapshot.OwnerFSBytesOut,
		AttachedCommandCount: snapshot.AttachedCommandCount,
		Principal: SessionPrincipal{
			Username:        snapshot.Principal.Username,
			UserDisabled:    snapshot.Principal.UserDisabled,
			TokenID:         snapshot.Principal.TokenID,
			TokenSource:     string(snapshot.Principal.TokenSource),
			AuthenticatedAt: snapshot.Principal.AuthenticatedAt,
		},
	}
	if snapshot.Control != nil {
		channel := sessionChannelFromControl(*snapshot.Control)
		result.Control = &channel
	}
	result.Channels = make([]SessionChannelSnapshot, 0, len(snapshot.Channels))
	for _, channel := range snapshot.Channels {
		result.Channels = append(result.Channels, sessionChannelFromControl(channel))
	}
	return result
}

func sessionChannelFromControl(snapshot control.SessionChannelSnapshot) SessionChannelSnapshot {
	return SessionChannelSnapshot{
		ChannelID: snapshot.ChannelID,
		Meta: SessionChannelMeta{
			TransportKind: string(snapshot.Meta.TransportKind),
			RemoteAddr:    snapshot.Meta.RemoteAddr,
			LocalAddr:     snapshot.Meta.LocalAddr,
			ConnectedAt:   snapshot.Meta.ConnectedAt,
			ChannelKind:   string(snapshot.Meta.ChannelKind),
		},
		BytesIn:  snapshot.BytesIn,
		BytesOut: snapshot.BytesOut,
	}
}
