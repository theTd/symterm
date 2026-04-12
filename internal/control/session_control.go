package control

import (
	"symterm/internal/ownerfs"
)

func (s *Service) DisconnectClient(clientID string) {
	s.reportCleanup("release client "+clientID, s.ReleaseClient(clientID))
}

func (s *Service) RetainClient(clientID string) error {
	return s.sessions.RetainClient(clientID)
}

func (s *Service) ReleaseClient(clientID string) error {
	clientSession, sessionErr := s.sessions.Session(clientID)
	err := s.projects.ReleaseClient(
		s.sessions,
		s.runtime,
		s.commands,
		s.uploads,
		s.invalidates,
		clientID,
		s.now,
		s.reportCleanup,
	)
	if err != nil || sessionErr != nil {
		return err
	}
	s.publishSessionByID(clientSession.SessionID)
	s.publishProjectSessions(projectKeyForSession(clientSession))
	return nil
}

func (s *Service) RegisterOwnerFileClient(clientID string, client ownerfs.Client) error {
	registration, err := s.sessions.RegisterOwnerFileClient(clientID, client)
	if err != nil {
		return err
	}
	if registration.Previous != nil && registration.Previous != client {
		s.reportCleanup("close replaced owner file client for "+clientID, registration.Previous.Close())
	}

	if s.runtime != nil && sessionUsesOwnerFileAuthority(registration.Session) {
		if instance, _, err := s.projects.InstanceForClient(s.sessions, clientID); err == nil {
			if instance.IsActiveAuthorityClient(clientID) {
				if err := s.runtime.SetAuthoritativeClient(projectKeyForSession(registration.Session), client); err != nil {
					return err
				}
			}
		}
	}

	go s.watchOwnerFileClient(clientID, client)
	s.publishSessionByClientID(clientID)
	return nil
}

func (s *Service) watchOwnerFileClient(clientID string, client ownerfs.Client) {
	clientSession, _ := s.sessions.Session(clientID)
	done := client.Done()
	if done == nil {
		return
	}
	<-done
	s.projects.HandleOwnerFileDisconnect(s.sessions, s.runtime, s.commands, clientID, client, s.now, s.reportCleanup)
	if _, sessionErr := s.sessions.Session(clientID); sessionErr == nil {
		s.publishSessionByClientID(clientID)
	}
	if clientSession.SessionID != "" {
		s.publishProjectSessions(projectKeyForSession(clientSession))
	}
}
