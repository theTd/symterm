package control

import (
	"context"

	"symterm/internal/project"
	"symterm/internal/proto"
)

func (s *Service) FsRead(clientID string, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	return s.FsReadContext(context.Background(), clientID, op, request)
}

func (s *Service) FsReadContext(ctx context.Context, clientID string, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	instance, session, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return proto.FsReply{}, err
	}
	state, _, err := instance.SyncState(clientID)
	if err != nil {
		return proto.FsReply{}, err
	}
	if state == proto.ProjectStateTerminating || state == proto.ProjectStateTerminated {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectTerminated, "project instance has already terminated")
	}

	backend := s.runtime
	if backend == nil {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectNotReady, "filesystem backend is unavailable")
	}

	opCtx, cancel := bindProjectContext(ctx, instance)
	defer cancel()
	return backend.FsReadContext(opCtx, projectKeyForSession(session), op, request)
}

func (s *Service) FsMutation(clientID string, request proto.FsMutationRequest) (proto.FsReply, error) {
	return s.FsMutationContext(context.Background(), clientID, request)
}

func (s *Service) FsMutationContext(ctx context.Context, clientID string, request proto.FsMutationRequest) (proto.FsReply, error) {
	instance, session, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return proto.FsReply{}, err
	}
	state, _, err := instance.SyncState(clientID)
	if err != nil {
		return proto.FsReply{}, err
	}
	readOnly, err := instance.ReadOnly(clientID)
	if err != nil {
		return proto.FsReply{}, err
	}
	if readOnly && !allowsMutationCleanup(request.Op) {
		if state == proto.ProjectStateTerminating || state == proto.ProjectStateTerminated {
			return proto.FsReply{}, proto.NewError(proto.ErrProjectTerminated, "project instance has already terminated")
		}
		return proto.FsReply{ReadOnly: true}, proto.NewError(proto.ErrReadOnlyProject, "project is currently read-only")
	}

	backend := s.runtime
	if backend == nil {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectNotReady, "filesystem backend is unavailable")
	}

	projectKey := projectKeyForSession(session)
	opCtx, cancel := bindProjectContext(ctx, instance)
	defer cancel()

	reply, err := backend.FsMutationContext(opCtx, projectKey, request.Op, request.Request, request.Preconditions)
	if err != nil {
		return reply, err
	}
	if err := s.invalidates.Append(projectKey, reply.Invalidations); err != nil {
		return proto.FsReply{}, err
	}
	return reply, nil
}

func (s *Service) ProjectFsRead(projectKey proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	return s.ProjectFsReadContext(context.Background(), projectKey, op, request)
}

func (s *Service) ProjectFsReadContext(ctx context.Context, projectKey proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	instance, err := s.projects.InstanceForKey(projectKey)
	if err != nil {
		return proto.FsReply{}, err
	}
	switch instance.CurrentState() {
	case proto.ProjectStateTerminating, proto.ProjectStateTerminated:
		return proto.FsReply{}, proto.NewError(proto.ErrProjectTerminated, "project instance has already terminated")
	}

	backend := s.runtime
	if backend == nil {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectNotReady, "filesystem backend is unavailable")
	}

	opCtx, cancel := bindProjectContext(ctx, instance)
	defer cancel()
	return backend.FsReadContext(opCtx, projectKey, op, request)
}

func (s *Service) ProjectFsMutation(projectKey proto.ProjectKey, request proto.FsMutationRequest) (proto.FsReply, error) {
	return s.ProjectFsMutationContext(context.Background(), projectKey, request)
}

func (s *Service) ProjectFsMutationContext(ctx context.Context, projectKey proto.ProjectKey, request proto.FsMutationRequest) (proto.FsReply, error) {
	instance, err := s.projects.InstanceForKey(projectKey)
	if err != nil {
		return proto.FsReply{}, err
	}
	switch instance.CurrentState() {
	case proto.ProjectStateNeedsConfirmation:
		if !allowsMutationCleanup(request.Op) {
			return proto.FsReply{ReadOnly: true}, proto.NewError(proto.ErrReadOnlyProject, "project is currently read-only")
		}
	case proto.ProjectStateTerminating, proto.ProjectStateTerminated:
		if !allowsMutationCleanup(request.Op) {
			return proto.FsReply{}, proto.NewError(proto.ErrProjectTerminated, "project instance has already terminated")
		}
	}

	backend := s.runtime
	if backend == nil {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectNotReady, "filesystem backend is unavailable")
	}

	opCtx, cancel := bindProjectContext(ctx, instance)
	defer cancel()

	reply, err := backend.FsMutationContext(opCtx, projectKey, request.Op, request.Request, request.Preconditions)
	if err != nil {
		return reply, err
	}
	if err := s.invalidates.Append(projectKey, reply.Invalidations); err != nil {
		return proto.FsReply{}, err
	}
	return reply, nil
}

func bindProjectContext(parent context.Context, instance *project.Instance) (context.Context, context.CancelFunc) {
	if instance == nil {
		if parent == nil {
			parent = context.Background()
		}
		return context.WithCancel(parent)
	}
	return instance.BindContext(parent)
}

func allowsMutationCleanup(op proto.FsOperation) bool {
	return op == proto.FsOpRelease
}
