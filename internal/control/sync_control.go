package control

import (
	"symterm/internal/invalidation"
	"symterm/internal/project"
	"symterm/internal/proto"
)

type syncInvocationContext struct {
	instance   *project.Instance
	projectKey proto.ProjectKey
}

func resolveSyncInvocation(s *Service, clientID string, expectedEpoch uint64) (SyncBackend, syncInvocationContext, error) {
	instance, session, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return nil, syncInvocationContext{}, err
	}
	if err := ensureOwnerSyncAccess(instance, clientID, expectedEpoch); err != nil {
		return nil, syncInvocationContext{}, err
	}

	backend := s.runtime
	if backend == nil {
		return nil, syncInvocationContext{}, proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}

	projectKey := projectKeyForSession(session)
	return backend, syncInvocationContext{
		instance:   instance,
		projectKey: projectKey,
	}, nil
}

func (s *Service) BeginSync(clientID string, request proto.BeginSyncRequest) error {
	backend, ctx, err := resolveSyncInvocation(s, clientID, request.SyncEpoch)
	if err != nil {
		return err
	}
	if err := backend.BeginSync(ctx.projectKey, request); err != nil {
		s.projects.FailInitialSync(s.sessions, s.runtime, s.commands, s.uploads, s.invalidates, ctx.projectKey, ctx.instance, err, s.now, s.reportCleanup)
		return err
	}
	return ctx.instance.ReportSyncProgress(clientID, proto.SyncProgress{
		Phase:     proto.SyncProgressPhaseBegin,
		Completed: 1,
		Total:     1,
	}, request.SyncEpoch, s.now())
}

func (s *Service) ScanManifest(clientID string, request proto.ScanManifestRequest) error {
	backend, ctx, err := resolveSyncInvocation(s, clientID, 0)
	if err != nil {
		return err
	}
	if err := backend.ScanManifest(ctx.projectKey, request); err != nil {
		s.projects.FailInitialSync(s.sessions, s.runtime, s.commands, s.uploads, s.invalidates, ctx.projectKey, ctx.instance, err, s.now, s.reportCleanup)
		return err
	}
	return nil
}

func (s *Service) PlanManifestHashes(clientID string) (proto.PlanManifestHashesResponse, error) {
	backend, ctx, err := resolveSyncInvocation(s, clientID, 0)
	if err != nil {
		return proto.PlanManifestHashesResponse{}, err
	}
	response, err := backend.PlanManifestHashes(ctx.projectKey)
	if err != nil {
		s.projects.FailInitialSync(s.sessions, s.runtime, s.commands, s.uploads, s.invalidates, ctx.projectKey, ctx.instance, err, s.now, s.reportCleanup)
		return proto.PlanManifestHashesResponse{}, err
	}
	return response, nil
}

func (s *Service) PlanSyncActions(clientID string) (proto.PlanSyncActionsResponse, error) {
	backend, ctx, err := resolveSyncInvocation(s, clientID, 0)
	if err != nil {
		return proto.PlanSyncActionsResponse{}, err
	}
	response, err := backend.PlanSyncActions(ctx.projectKey)
	if err != nil {
		s.projects.FailInitialSync(s.sessions, s.runtime, s.commands, s.uploads, s.invalidates, ctx.projectKey, ctx.instance, err, s.now, s.reportCleanup)
		return proto.PlanSyncActionsResponse{}, err
	}
	return response, nil
}

func (s *Service) BeginFile(clientID string, request proto.BeginFileRequest) (proto.BeginFileResponse, error) {
	backend, ctx, err := resolveSyncInvocation(s, clientID, request.SyncEpoch)
	if err != nil {
		return proto.BeginFileResponse{}, err
	}
	response, err := backend.BeginFile(ctx.projectKey, request)
	if err != nil {
		s.projects.FailInitialSync(s.sessions, s.runtime, s.commands, s.uploads, s.invalidates, ctx.projectKey, ctx.instance, err, s.now, s.reportCleanup)
		return proto.BeginFileResponse{}, err
	}
	s.uploads.Begin(ctx.projectKey, response.FileID, request.Path)
	return response, nil
}

func (s *Service) ApplyChunk(clientID string, request proto.ApplyChunkRequest) error {
	backend, ctx, err := resolveSyncInvocation(s, clientID, 0)
	if err != nil {
		return err
	}
	if err := backend.ApplyChunk(ctx.projectKey, request); err != nil {
		s.projects.FailInitialSync(s.sessions, s.runtime, s.commands, s.uploads, s.invalidates, ctx.projectKey, ctx.instance, err, s.now, s.reportCleanup)
		return err
	}
	return nil
}

func (s *Service) CommitFile(clientID string, request proto.CommitFileRequest) error {
	backend, ctx, err := resolveSyncInvocation(s, clientID, 0)
	if err != nil {
		return err
	}
	if err := backend.CommitFile(ctx.projectKey, request); err != nil {
		s.projects.FailInitialSync(s.sessions, s.runtime, s.commands, s.uploads, s.invalidates, ctx.projectKey, ctx.instance, err, s.now, s.reportCleanup)
		return err
	}
	path := s.uploads.Commit(ctx.projectKey, request.FileID)
	if path != "" {
		if err := s.invalidates.Append(ctx.projectKey, invalidation.DataPath(path)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) AbortFile(clientID string, request proto.AbortFileRequest) error {
	backend, ctx, err := resolveSyncInvocation(s, clientID, 0)
	if err == nil {
		err = backend.AbortFile(ctx.projectKey, request)
	}
	s.uploads.Abort(ctx.projectKey, request.FileID)
	return err
}

func (s *Service) DeletePath(clientID string, request proto.DeletePathRequest) error {
	backend, ctx, err := resolveSyncInvocation(s, clientID, request.SyncEpoch)
	if err != nil {
		return err
	}
	if err := backend.DeletePath(ctx.projectKey, request); err != nil {
		s.projects.FailInitialSync(s.sessions, s.runtime, s.commands, s.uploads, s.invalidates, ctx.projectKey, ctx.instance, err, s.now, s.reportCleanup)
		return err
	}
	return s.invalidates.Append(ctx.projectKey, invalidation.DeletePath(request.Path))
}

func (s *Service) FinalizeSync(clientID string, request proto.FinalizeSyncRequest) (proto.ProjectSnapshot, error) {
	backend, ctx, err := resolveSyncInvocation(s, clientID, request.SyncEpoch)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if err := backend.FinalizeSync(ctx.projectKey, request); err != nil {
		s.projects.FailInitialSync(s.sessions, s.runtime, s.commands, s.uploads, s.invalidates, ctx.projectKey, ctx.instance, err, s.now, s.reportCleanup)
		return proto.ProjectSnapshot{}, err
	}
	if err := ctx.instance.ReportSyncProgress(clientID, proto.SyncProgress{
		Phase:     proto.SyncProgressPhaseFinalize,
		Completed: 1,
		Total:     1,
	}, request.SyncEpoch, s.now()); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	snapshot, err := ctx.instance.CompleteInitialSync(clientID, request.SyncEpoch, s.now())
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	s.publishProjectSessions(ctx.projectKey)
	return snapshot, nil
}

func (s *Service) ReportSyncProgress(clientID string, request proto.ReportSyncProgressRequest) error {
	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return err
	}
	if err := ensureOwnerSyncAccess(instance, clientID, request.SyncEpoch); err != nil {
		return err
	}
	if err := instance.ReportSyncProgress(clientID, request.Progress, request.SyncEpoch, s.now()); err != nil {
		return err
	}
	if clientSession, sessionErr := s.sessions.Session(clientID); sessionErr == nil {
		s.publishProjectSessions(projectKeyForSession(clientSession))
	}
	return nil
}

func ensureOwnerSyncAccess(instance *project.Instance, clientID string, expectedEpoch uint64) error {
	state, activeEpoch, err := instance.SyncState(clientID)
	if err != nil {
		return err
	}
	if state == proto.ProjectStateNeedsConfirmation {
		return proto.NewError(proto.ErrNeedsConfirmation, "project is locked pending reconcile confirmation")
	}
	if state == proto.ProjectStateTerminating || state == proto.ProjectStateTerminated {
		return proto.NewError(proto.ErrProjectTerminated, "project instance has already terminated")
	}
	if expectedEpoch != 0 && expectedEpoch != activeEpoch {
		return proto.NewError(proto.ErrSyncEpochMismatch, "sync epoch does not match the active owner epoch")
	}
	if err := instance.RequireAuthority(clientID); err != nil {
		if protoErr, ok := err.(*proto.Error); ok && protoErr.Code == proto.ErrPermissionDenied {
			return proto.NewError(proto.ErrFollowerSyncDenied, "sync operation requires the active authority session")
		}
		return err
	}
	return nil
}
