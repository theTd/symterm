package control

import "symterm/internal/proto"

type LiveSessionObserver interface {
	UpsertSession(LiveSessionSnapshot)
	CloseSession(LiveSessionSnapshot)
}

func (s *Service) publishSessionByClientID(clientID string) {
	if s == nil || s.sessionObserver == nil || clientID == "" {
		return
	}
	s.trace("publish session by client begin client_id=%q", clientID)
	session, err := s.sessions.Session(clientID)
	if err != nil {
		s.trace("publish session by client skipped client_id=%q error=%v", clientID, err)
		return
	}
	s.publishSessionByID(session.SessionID)
	s.trace("publish session by client end client_id=%q session_id=%q", clientID, session.SessionID)
}

func (s *Service) publishSessionByID(sessionID string) {
	if s == nil || s.sessionObserver == nil || sessionID == "" {
		return
	}
	s.trace("publish session by id begin session_id=%q", sessionID)
	snapshot, ok := s.SessionSnapshot(sessionID)
	if !ok {
		s.trace("publish session by id skipped session_id=%q", sessionID)
		return
	}
	if snapshot.CloseReason != "" {
		s.trace("publish session close session_id=%q reason=%q", sessionID, snapshot.CloseReason)
		s.sessionObserver.CloseSession(snapshot)
		return
	}
	s.sessionObserver.UpsertSession(snapshot)
	s.trace("publish session upsert session_id=%q role=%q state=%q", sessionID, snapshot.Role, snapshot.ProjectState)
}

func (s *Service) publishProjectSessions(projectKey proto.ProjectKey) {
	if s == nil || s.sessionObserver == nil {
		return
	}
	for _, snapshot := range s.ListSessions() {
		if snapshot.ProjectID != projectKey.ProjectID || snapshot.Principal.Username != projectKey.Username {
			continue
		}
		s.sessionObserver.UpsertSession(snapshot)
	}
}
