package control

import (
	"context"
	"strings"

	"symterm/internal/project"
	"symterm/internal/projectready"
	"symterm/internal/proto"
)

type projectCommandStarter func(string, proto.StartCommandRequest) (proto.StartCommandResponse, error)
type ensureProjectSessionFunc func(string, proto.EnsureProjectRequest) (proto.ProjectSnapshot, error)
type confirmProjectSessionFunc func(string, proto.ConfirmReconcileRequest) (proto.ProjectSnapshot, error)

type ProjectSessionUseCase struct {
	sessions     *SessionRegistry
	projects     *ProjectCoordinator
	ensure       ensureProjectSessionFunc
	confirm      confirmProjectSessionFunc
	startCommand projectCommandStarter
}

func newProjectSessionUseCase(
	sessions *SessionRegistry,
	projects *ProjectCoordinator,
	ensure ensureProjectSessionFunc,
	confirm confirmProjectSessionFunc,
	startCommand projectCommandStarter,
) *ProjectSessionUseCase {
	return &ProjectSessionUseCase{
		sessions:     sessions,
		projects:     projects,
		ensure:       ensure,
		confirm:      confirm,
		startCommand: startCommand,
	}
}

func (u *ProjectSessionUseCase) OpenProjectSession(clientID string, request proto.OpenProjectSessionRequest) (proto.ProjectSessionResponse, error) {
	snapshot, err := u.EnsureProjectRequest(clientID, proto.EnsureProjectRequest{
		ProjectID: request.ProjectID,
	})
	if err != nil {
		return proto.ProjectSessionResponse{}, err
	}
	return proto.ProjectSessionResponse{Snapshot: snapshot}, nil
}

func (u *ProjectSessionUseCase) ResumeProjectSession(clientID string, request proto.ResumeProjectSessionRequest) (proto.ProjectSessionResponse, error) {
	snapshot, err := u.ConfirmReconcile(clientID, proto.ConfirmReconcileRequest{
		ProjectID:       request.ProjectID,
		ExpectedCursor:  request.ExpectedCursor,
		WorkspaceDigest: request.WorkspaceDigest,
	})
	if err != nil {
		return proto.ProjectSessionResponse{}, err
	}
	return proto.ProjectSessionResponse{Snapshot: snapshot}, nil
}

func (u *ProjectSessionUseCase) StartProjectCommandSession(
	ctx context.Context,
	clientID string,
	request proto.StartProjectCommandSessionRequest,
) (proto.StartProjectCommandSessionResponse, error) {
	instance, session, err := u.projects.InstanceForClient(u.sessions, clientID)
	if err != nil {
		return proto.StartProjectCommandSessionResponse{}, err
	}
	if strings.TrimSpace(request.ProjectID) == "" {
		return proto.StartProjectCommandSessionResponse{}, proto.NewError(proto.ErrInvalidArgument, "project id is required")
	}
	if request.ProjectID != session.ProjectID {
		return proto.StartProjectCommandSessionResponse{}, proto.NewError(proto.ErrInvalidArgument, "project id does not match the authenticated session")
	}

	snapshot, err := instance.Snapshot(clientID)
	if err != nil {
		return proto.StartProjectCommandSessionResponse{}, err
	}
	snapshot, err = u.waitForProjectCommandReady(ctx, instance, clientID, snapshot)
	if err != nil {
		return proto.StartProjectCommandSessionResponse{}, err
	}

	response := proto.StartProjectCommandSessionResponse{
		Snapshot: snapshot,
	}
	if !snapshot.CanStartCommands {
		return response, nil
	}

	started, err := u.startCommand(clientID, proto.StartCommandRequest{
		ProjectID:  request.ProjectID,
		ArgvTail:   append([]string(nil), request.ArgvTail...),
		TTY:        request.TTY,
		TmuxStatus: request.TmuxStatus,
	})
	if err != nil {
		return proto.StartProjectCommandSessionResponse{}, err
	}
	response.Command = &started

	updatedSnapshot, err := instance.Snapshot(clientID)
	if err != nil {
		return proto.StartProjectCommandSessionResponse{}, err
	}
	response.Snapshot = updatedSnapshot
	return response, nil
}

func (u *ProjectSessionUseCase) waitForProjectCommandReady(
	ctx context.Context,
	instance *project.Instance,
	clientID string,
	snapshot proto.ProjectSnapshot,
) (proto.ProjectSnapshot, error) {
	if instance == nil {
		return snapshot, nil
	}
	return projectready.Wait(ctx, snapshot,
		func(waitCtx context.Context, sinceCursor uint64, onEvent func(proto.ProjectEvent) error) error {
			return streamProjectEvents(waitCtx, instance, sinceCursor, onEvent)
		},
		func(context.Context) (proto.ProjectSnapshot, error) {
			return instance.Snapshot(clientID)
		},
	)
}

func (u *ProjectSessionUseCase) EnsureProjectRequest(clientID string, request proto.EnsureProjectRequest) (proto.ProjectSnapshot, error) {
	if u.ensure == nil {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrProjectNotReady, "project session use case is unavailable")
	}
	return u.ensure(clientID, request)
}

func (u *ProjectSessionUseCase) ConfirmReconcile(clientID string, request proto.ConfirmReconcileRequest) (proto.ProjectSnapshot, error) {
	if u.confirm == nil {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrProjectNotReady, "project session use case is unavailable")
	}
	return u.confirm(clientID, request)
}

func (s *Service) OpenProjectSession(clientID string, request proto.OpenProjectSessionRequest) (proto.ProjectSessionResponse, error) {
	return s.projectSessions.OpenProjectSession(clientID, request)
}

func (s *Service) ResumeProjectSession(clientID string, request proto.ResumeProjectSessionRequest) (proto.ProjectSessionResponse, error) {
	return s.projectSessions.ResumeProjectSession(clientID, request)
}

func (s *Service) StartProjectCommandSession(
	ctx context.Context,
	clientID string,
	request proto.StartProjectCommandSessionRequest,
) (proto.StartProjectCommandSessionResponse, error) {
	return s.projectSessions.StartProjectCommandSession(ctx, clientID, request)
}

func streamProjectEvents(
	ctx context.Context,
	instance *project.Instance,
	sinceCursor uint64,
	onEvent func(proto.ProjectEvent) error,
) error {
	events, watcherID, ch, err := instance.SubscribeProjectEvents(sinceCursor)
	if err != nil {
		return err
	}
	defer instance.UnsubscribeProjectEvents(watcherID)

	emit := func(items []proto.ProjectEvent) error {
		for _, event := range items {
			if err := onEvent(event); err != nil {
				return err
			}
		}
		return nil
	}
	if err := emit(events); err != nil {
		return err
	}

	currentCursor := sinceCursor
	if len(events) > 0 {
		currentCursor = events[len(events)-1].Cursor
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			events, err = instance.EventsSince(currentCursor)
			if err != nil {
				return err
			}
			if err := emit(events); err != nil {
				return err
			}
			if len(events) > 0 {
				currentCursor = events[len(events)-1].Cursor
			}
		}
	}
}
