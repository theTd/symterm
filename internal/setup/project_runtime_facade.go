package setup

import (
	"context"
	"errors"
	"fmt"

	"symterm/internal/control"
	"symterm/internal/daemon"
	"symterm/internal/diagnostic"
	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

type projectRuntimeService interface {
	control.ProjectRuntimeControl
}

type projectRuntimeServiceResolver func() projectRuntimeService

type ProjectRuntimeFacade struct {
	ctx       context.Context
	workspace *daemon.WorkspaceManager
	mounts    *daemon.MountManager
	runtime   *daemon.RuntimeManager
	service   projectRuntimeServiceResolver
}

type projectFilesystemBridge struct {
	facade *ProjectRuntimeFacade
}

func NewProjectRuntimeFacade(
	ctx context.Context,
	projectsRoot string,
	allowUnsafeNoFuse bool,
	remoteEntrypoint []string,
	resolveEntrypoint func(string, []string) []string,
	resolveService projectRuntimeServiceResolver,
	tracef func(string, ...any),
) *ProjectRuntimeFacade {
	facade := &ProjectRuntimeFacade{
		ctx:       ctx,
		workspace: daemon.NewWorkspaceManager(projectsRoot),
		service:   resolveService,
	}
	facade.mounts = daemon.NewMountManagerWithDependencies(projectsRoot, allowUnsafeNoFuse, daemon.MountManagerDependencies{
		ProjectFilesystem:     projectFilesystemBridge{facade: facade},
		SessionFailureHandler: facade.handleSessionFailure,
		Tracef:                tracef,
	})
	facade.runtime = daemon.NewRuntimeManager(projectsRoot, remoteEntrypoint, facade.mounts)
	facade.runtime.SetEntrypointResolver(resolveEntrypoint)
	return facade
}

func (f *ProjectRuntimeFacade) PrepareProject(key proto.ProjectKey) error {
	return f.mounts.PrepareProject(key)
}

func (f *ProjectRuntimeFacade) BeginSync(key proto.ProjectKey, request proto.BeginSyncRequest) error {
	return f.workspace.BeginSync(key, request)
}

func (f *ProjectRuntimeFacade) ScanManifest(key proto.ProjectKey, request proto.ScanManifestRequest) error {
	return f.workspace.ScanManifest(key, request)
}

func (f *ProjectRuntimeFacade) PlanManifestHashes(key proto.ProjectKey) (proto.PlanManifestHashesResponse, error) {
	return f.workspace.PlanManifestHashes(key)
}

func (f *ProjectRuntimeFacade) PlanSyncActions(key proto.ProjectKey) (proto.PlanSyncActionsResponse, error) {
	return f.workspace.PlanSyncActions(key)
}

func (f *ProjectRuntimeFacade) BeginFile(key proto.ProjectKey, request proto.BeginFileRequest) (proto.BeginFileResponse, error) {
	return f.workspace.BeginFile(key, request)
}

func (f *ProjectRuntimeFacade) ApplyChunk(key proto.ProjectKey, request proto.ApplyChunkRequest) error {
	return f.workspace.ApplyChunk(key, request)
}

func (f *ProjectRuntimeFacade) CommitFile(key proto.ProjectKey, request proto.CommitFileRequest) error {
	return f.workspace.CommitFile(key, request)
}

func (f *ProjectRuntimeFacade) AbortFile(key proto.ProjectKey, request proto.AbortFileRequest) error {
	return f.workspace.AbortFile(key, request)
}

func (f *ProjectRuntimeFacade) DeletePath(key proto.ProjectKey, request proto.DeletePathRequest) error {
	return f.workspace.DeletePath(key, request)
}

func (f *ProjectRuntimeFacade) FinalizeSync(key proto.ProjectKey, request proto.FinalizeSyncRequest) error {
	return f.workspace.FinalizeSync(key, request)
}

func (f *ProjectRuntimeFacade) FsRead(key proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	return f.workspace.FsRead(key, op, request)
}

func (f *ProjectRuntimeFacade) FsReadContext(ctx context.Context, key proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	return f.workspace.FsReadContext(ctx, key, op, request)
}

func (f *ProjectRuntimeFacade) FsMutation(key proto.ProjectKey, op proto.FsOperation, request proto.FsRequest, preconditions []proto.MutationPrecondition) (proto.FsReply, error) {
	return f.workspace.FsMutation(key, op, request, preconditions)
}

func (f *ProjectRuntimeFacade) FsMutationContext(ctx context.Context, key proto.ProjectKey, op proto.FsOperation, request proto.FsRequest, preconditions []proto.MutationPrecondition) (proto.FsReply, error) {
	return f.workspace.FsMutationContext(ctx, key, op, request, preconditions)
}

func (f *ProjectRuntimeFacade) ApplyOwnerInvalidations(key proto.ProjectKey, changes []proto.InvalidateChange) error {
	return f.workspace.ApplyOwnerInvalidations(key, changes)
}

func (f *ProjectRuntimeFacade) ApplyOwnerInvalidationsContext(ctx context.Context, key proto.ProjectKey, changes []proto.InvalidateChange) error {
	return f.workspace.ApplyOwnerInvalidationsContext(ctx, key, changes)
}

func (f *ProjectRuntimeFacade) EnterConservativeMode(key proto.ProjectKey, reason string) ([]proto.InvalidateChange, error) {
	return f.workspace.EnterConservativeMode(key, reason)
}

func (f *ProjectRuntimeFacade) EnterConservativeModeContext(ctx context.Context, key proto.ProjectKey, reason string) ([]proto.InvalidateChange, error) {
	return f.workspace.EnterConservativeModeContext(ctx, key, reason)
}

func (f *ProjectRuntimeFacade) SetAuthorityState(key proto.ProjectKey, state proto.AuthorityState) error {
	return f.workspace.SetAuthorityState(key, state)
}

func (f *ProjectRuntimeFacade) SetAuthoritativeRoot(key proto.ProjectKey, root string) error {
	return f.workspace.SetAuthoritativeRoot(key, root)
}

func (f *ProjectRuntimeFacade) SetAuthoritativeClient(key proto.ProjectKey, client ownerfs.Client) error {
	return f.workspace.SetAuthoritativeClient(key, client)
}

func (f *ProjectRuntimeFacade) ClearAuthoritativeRoot(key proto.ProjectKey) error {
	return f.workspace.ClearAuthoritativeRoot(key)
}

func (f *ProjectRuntimeFacade) Launch(launch control.CommandLaunch) {
	f.runtime.Launch(
		f.ctx,
		daemon.LaunchRequest{ProjectKey: launch.ProjectKey, Command: launch.Command},
		func(exitCode int) {
			service := f.runtimeService()
			if service == nil {
				return
			}
			diagnostic.Error(service.Diagnostics(), "record command completion for "+launch.Command.CommandID, service.CompleteCommand(launch.ProjectKey, launch.Command.CommandID, exitCode))
		},
		func(reason string) {
			service := f.runtimeService()
			if service == nil {
				return
			}
			diagnostic.Error(service.Diagnostics(), "record command failure for "+launch.Command.CommandID, service.FailCommand(launch.ProjectKey, launch.Command.CommandID, reason))
		},
	)
}

func (f *ProjectRuntimeFacade) ReadOutput(projectKey proto.ProjectKey, request proto.AttachStdioRequest) (proto.AttachStdioResponse, error) {
	return f.runtime.AttachStdio(projectKey, request)
}

func (f *ProjectRuntimeFacade) WaitOutput(ctx context.Context, projectKey proto.ProjectKey, request proto.AttachStdioRequest) error {
	return f.runtime.WaitOutput(ctx, projectKey, request)
}

func (f *ProjectRuntimeFacade) ResizeTTY(projectKey proto.ProjectKey, commandID string, columns int, rows int) error {
	return f.runtime.ResizeTTY(projectKey, commandID, columns, rows)
}

func (f *ProjectRuntimeFacade) SendSignal(projectKey proto.ProjectKey, commandID string, name string) error {
	return f.runtime.SendSignal(projectKey, commandID, name)
}

func (f *ProjectRuntimeFacade) WriteInput(projectKey proto.ProjectKey, commandID string, data []byte) error {
	return f.runtime.WriteStdin(projectKey, commandID, data)
}

func (f *ProjectRuntimeFacade) CloseInput(projectKey proto.ProjectKey, commandID string) error {
	return f.runtime.CloseStdin(projectKey, commandID)
}

func (f *ProjectRuntimeFacade) StopProject(key proto.ProjectKey) error {
	if err := f.runtime.StopProject(key); err != nil {
		return err
	}
	return f.mounts.StopProject(key)
}

func (f *ProjectRuntimeFacade) StopAllProjects() error {
	if f == nil {
		return nil
	}

	var errs []error
	if f.runtime != nil {
		if err := f.runtime.StopAllProjects(); err != nil {
			errs = append(errs, fmt.Errorf("stop runtime commands: %w", err))
		}
	}
	if f.mounts != nil {
		if err := f.mounts.StopAll(); err != nil {
			errs = append(errs, fmt.Errorf("stop mount sessions: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (f *ProjectRuntimeFacade) WatchInvalidate(projectKey proto.ProjectKey, sinceCursor uint64) (daemon.ProjectInvalidateWatch, error) {
	service := f.runtimeService()
	if service == nil {
		return daemon.ProjectInvalidateWatch{}, proto.NewError(proto.ErrProjectNotReady, "project runtime service is unavailable")
	}
	events, ch, unsubscribe, err := service.SubscribeProjectInvalidate(projectKey, sinceCursor)
	if err != nil {
		return daemon.ProjectInvalidateWatch{}, err
	}
	return daemon.ProjectInvalidateWatch{Events: events, Notify: ch, Close: unsubscribe}, nil
}

func (f *ProjectRuntimeFacade) handleSessionFailure(key proto.ProjectKey, err error) {
	service := f.runtimeService()
	if service == nil {
		return
	}
	reason := "fuse mount failed"
	if err != nil {
		reason = "fuse mount failed: " + err.Error()
	}
	diagnostic.Error(service.Diagnostics(), "terminate project "+key.String()+" after mount failure", service.TerminateProject(key, reason))
}

func (f *ProjectRuntimeFacade) ProjectFsRead(ctx context.Context, projectKey proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	service := f.runtimeService()
	if service == nil {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectNotReady, "project runtime service is unavailable")
	}
	return service.ProjectFsReadContext(ctx, projectKey, op, request)
}

func (f *ProjectRuntimeFacade) ProjectFsMutation(ctx context.Context, projectKey proto.ProjectKey, request proto.FsMutationRequest) (proto.FsReply, error) {
	service := f.runtimeService()
	if service == nil {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectNotReady, "project runtime service is unavailable")
	}
	return service.ProjectFsMutationContext(ctx, projectKey, request)
}

func (f *ProjectRuntimeFacade) MountManager() *daemon.MountManager {
	if f == nil {
		return nil
	}
	return f.mounts
}

func (f *ProjectRuntimeFacade) RuntimeManager() *daemon.RuntimeManager {
	if f == nil {
		return nil
	}
	return f.runtime
}

func (f *ProjectRuntimeFacade) WorkspaceManager() *daemon.WorkspaceManager {
	if f == nil {
		return nil
	}
	return f.workspace
}

func (f *ProjectRuntimeFacade) runtimeService() projectRuntimeService {
	if f == nil || f.service == nil {
		return nil
	}
	return f.service()
}

func (b projectFilesystemBridge) FsRead(ctx context.Context, projectKey proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	return b.facade.ProjectFsRead(ctx, projectKey, op, request)
}

func (b projectFilesystemBridge) FsMutation(ctx context.Context, projectKey proto.ProjectKey, request proto.FsMutationRequest) (proto.FsReply, error) {
	return b.facade.ProjectFsMutation(ctx, projectKey, request)
}

func (b projectFilesystemBridge) WatchInvalidate(projectKey proto.ProjectKey, sinceCursor uint64) (daemon.ProjectInvalidateWatch, error) {
	return b.facade.WatchInvalidate(projectKey, sinceCursor)
}
