package control

import (
	"context"

	"symterm/internal/proto"
)

func (s *Service) WatchInvalidate(clientID string, request proto.WatchInvalidateRequest) ([]proto.InvalidateEvent, error) {
	_, clientSession, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return nil, err
	}
	if request.ProjectID != clientSession.ProjectID {
		return nil, proto.NewError(proto.ErrInvalidArgument, "project id does not match the authenticated session")
	}
	return s.invalidates.Watch(projectKeyForSession(clientSession), request.SinceCursor)
}

func (s *Service) Invalidate(clientID string, request proto.InvalidateRequest) error {
	return s.InvalidateContext(context.Background(), clientID, request)
}

func (s *Service) InvalidateContext(ctx context.Context, clientID string, request proto.InvalidateRequest) error {
	instance, clientSession, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return err
	}
	if err := ensureOwnerSyncAccess(instance, clientID, 0); err != nil {
		return err
	}

	projectKey := projectKeyForSession(clientSession)
	if s.runtime != nil {
		opCtx, cancel := bindProjectContext(ctx, instance)
		defer cancel()
		err = s.runtime.ApplyOwnerInvalidationsContext(opCtx, projectKey, request.Changes)
		if err != nil {
			return err
		}
	}

	return s.invalidates.Append(projectKey, request.Changes)
}

func (s *Service) OwnerWatcherFailed(clientID string, request proto.OwnerWatcherFailureRequest) error {
	return s.OwnerWatcherFailedContext(context.Background(), clientID, request)
}

func (s *Service) OwnerWatcherFailedContext(ctx context.Context, clientID string, request proto.OwnerWatcherFailureRequest) error {
	instance, clientSession, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return err
	}
	if err := ensureOwnerSyncAccess(instance, clientID, 0); err != nil {
		return err
	}
	if s.runtime == nil {
		return nil
	}

	projectKey := projectKeyForSession(clientSession)
	opCtx, cancel := bindProjectContext(ctx, instance)
	defer cancel()

	changes, err := s.runtime.EnterConservativeModeContext(opCtx, projectKey, request.Reason)
	if err != nil {
		return err
	}

	return s.invalidates.Append(projectKey, changes)
}

func (s *Service) SubscribeInvalidateEvents(clientID string, request proto.WatchInvalidateRequest) ([]proto.InvalidateEvent, uint64, <-chan struct{}, func(), error) {
	_, clientSession, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	if request.ProjectID != clientSession.ProjectID {
		return nil, 0, nil, nil, proto.NewError(proto.ErrInvalidArgument, "project id does not match the authenticated session")
	}
	return s.invalidates.Subscribe(projectKeyForSession(clientSession), request.SinceCursor)
}

func (s *Service) ProjectWatchInvalidate(projectKey proto.ProjectKey, sinceCursor uint64) ([]proto.InvalidateEvent, error) {
	if _, err := s.projects.InstanceForKey(projectKey); err != nil {
		return nil, err
	}
	return s.invalidates.Watch(projectKey, sinceCursor)
}

func (s *Service) SubscribeProjectInvalidate(projectKey proto.ProjectKey, sinceCursor uint64) ([]proto.InvalidateEvent, <-chan struct{}, func(), error) {
	if _, err := s.projects.InstanceForKey(projectKey); err != nil {
		return nil, nil, nil, err
	}
	events, _, ch, unsubscribe, err := s.invalidates.Subscribe(projectKey, sinceCursor)
	if err != nil {
		return nil, nil, nil, err
	}
	return events, ch, unsubscribe, nil
}
