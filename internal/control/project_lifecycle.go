package control

import "symterm/internal/proto"

func (s *Service) EnsureProject(clientID string) (proto.ProjectSnapshot, error) {
	clientSession, err := s.sessions.Session(clientID)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	snapshot, err := s.projects.EnsureProject(s.sessions, s.runtime, clientID, clientSession.ProjectID, s.now, s.reportError)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	s.publishProjectSessions(projectKeyForSession(clientSession))
	return snapshot, nil
}

func (s *Service) EnsureProjectRequest(clientID string, request proto.EnsureProjectRequest) (proto.ProjectSnapshot, error) {
	projectID := request.ProjectID
	if projectID == "" {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrInvalidArgument, "project id is required")
	}
	snapshot, err := s.projects.EnsureProject(s.sessions, s.runtime, clientID, projectID, s.now, s.reportError)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if clientSession, sessionErr := s.sessions.Session(clientID); sessionErr == nil {
		s.publishProjectSessions(projectKeyForSession(clientSession))
	}
	return snapshot, nil
}

func (s *Service) ConfirmReconcile(clientID string, request proto.ConfirmReconcileRequest) (proto.ProjectSnapshot, error) {
	snapshot, err := s.projects.ConfirmReconcile(s.sessions, s.runtime, clientID, request, s.now, s.reportError)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if clientSession, sessionErr := s.sessions.Session(clientID); sessionErr == nil {
		s.publishProjectSessions(projectKeyForSession(clientSession))
	}
	return snapshot, nil
}

func (s *Service) CompleteInitialSync(clientID string, syncEpoch uint64) (proto.ProjectSnapshot, error) {
	snapshot, err := s.projects.CompleteInitialSync(s.sessions, clientID, syncEpoch, s.now)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if clientSession, sessionErr := s.sessions.Session(clientID); sessionErr == nil {
		s.publishProjectSessions(projectKeyForSession(clientSession))
	}
	return snapshot, nil
}

func (s *Service) WatchProject(clientID string, request proto.WatchProjectRequest) ([]proto.ProjectEvent, error) {
	instance, clientSession, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return nil, err
	}
	if request.ProjectID != clientSession.ProjectID {
		return nil, proto.NewError(proto.ErrInvalidArgument, "project id does not match the authenticated session")
	}
	return instance.EventsSince(request.SinceCursor)
}

func (s *Service) SubscribeProjectEvents(clientID string, request proto.WatchProjectRequest) ([]proto.ProjectEvent, uint64, <-chan struct{}, func(), error) {
	instance, clientSession, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	if request.ProjectID != clientSession.ProjectID {
		return nil, 0, nil, nil, proto.NewError(proto.ErrInvalidArgument, "project id does not match the authenticated session")
	}

	events, watcherID, ch, err := instance.SubscribeProjectEvents(request.SinceCursor)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	return events, watcherID, ch, func() {
		instance.UnsubscribeProjectEvents(watcherID)
	}, nil
}

func (s *Service) TerminateProject(key proto.ProjectKey, reason string) error {
	if err := s.projects.TerminateProject(
		s.sessions,
		s.runtime,
		s.commands,
		s.uploads,
		s.invalidates,
		key,
		reason,
		s.now,
		s.reportCleanup,
	); err != nil {
		return err
	}
	s.publishProjectSessions(key)
	return nil
}
