package control

import "io"

func (s *Service) BindControlConnection(clientID string, meta ConnMeta, counters *TrafficCounters, closer io.Closer) error {
	s.trace("bind control begin client_id=%q transport=%q remote=%q local=%q", clientID, meta.TransportKind, meta.RemoteAddr, meta.LocalAddr)
	if err := s.sessions.BindControl(clientID, meta, counters, closer); err != nil {
		s.trace("bind control failed client_id=%q error=%v", clientID, err)
		return err
	}
	s.publishSessionByClientID(clientID)
	s.trace("bind control end client_id=%q", clientID)
	return nil
}

func (s *Service) AttachSessionChannel(clientID string, meta ConnMeta, counters *TrafficCounters, closer io.Closer) (string, error) {
	channelID, err := s.sessions.AttachChannel(clientID, meta, counters, closer)
	if err != nil {
		return "", err
	}
	s.publishSessionByClientID(clientID)
	return channelID, nil
}

func (s *Service) DetachSessionChannel(clientID string, channelID string) {
	s.sessions.DetachChannel(clientID, channelID)
	s.publishSessionByClientID(clientID)
}

func (s *Service) NoteSessionActivity(clientID string) {
	s.trace("note session activity begin client_id=%q", clientID)
	s.sessions.MarkActivity(clientID)
	s.publishSessionByClientID(clientID)
	s.trace("note session activity end client_id=%q", clientID)
}

func (s *Service) ListSessions() []LiveSessionSnapshot {
	snapshots := s.sessions.ListLiveSessions()
	for idx := range snapshots {
		snapshots[idx] = s.enrichSessionSnapshot(snapshots[idx])
	}
	return snapshots
}

func (s *Service) SessionSnapshot(sessionID string) (LiveSessionSnapshot, bool) {
	snapshot, ok := s.sessions.SessionSnapshot(sessionID)
	if !ok {
		return LiveSessionSnapshot{}, false
	}
	return s.enrichSessionSnapshot(snapshot), true
}

func (s *Service) TerminateSession(sessionID string, reason string) error {
	return s.sessions.TerminateSession(sessionID, reason)
}

func (s *Service) enrichSessionSnapshot(snapshot LiveSessionSnapshot) LiveSessionSnapshot {
	instance, _, err := s.projects.InstanceForClient(s.sessions, snapshot.ClientID)
	if err != nil {
		return snapshot
	}
	projectSnapshot, err := instance.Snapshot(snapshot.ClientID)
	if err != nil {
		return snapshot
	}
	snapshot.Role = projectSnapshot.Role
	snapshot.ProjectState = projectSnapshot.ProjectState
	snapshot.SyncEpoch = projectSnapshot.SyncEpoch
	snapshot.NeedsConfirmation = projectSnapshot.NeedsConfirmation
	snapshot.AttachedCommandCount = len(projectSnapshot.CommandSnapshots)
	return snapshot
}
