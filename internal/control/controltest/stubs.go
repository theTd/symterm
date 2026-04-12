package controltest

import (
	"context"
	"testing"

	"symterm/internal/control"
	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

func NewService(tb testing.TB, auth control.Authenticator, deps control.ServiceDependencies) *control.Service {
	tb.Helper()

	service, err := control.NewServiceWithDependencies(auth, deps)
	if err != nil {
		tb.Fatalf("NewServiceWithDependencies() error = %v", err)
	}
	return service
}

type BootstrapperStub struct {
	PrepareProjectFunc func(proto.ProjectKey) error
}

func (s BootstrapperStub) PrepareProject(key proto.ProjectKey) error {
	if s.PrepareProjectFunc == nil {
		return nil
	}
	return s.PrepareProjectFunc(key)
}

type RuntimeBackendStub struct {
	BootstrapperStub
	SyncBackendStub
	FilesystemBackendStub
	CommandBackendStub
}

type SyncBackendStub struct {
	BeginSyncFunc          func(proto.ProjectKey, proto.BeginSyncRequest) error
	ScanManifestFunc       func(proto.ProjectKey, proto.ScanManifestRequest) error
	PlanManifestHashesFunc func(proto.ProjectKey) (proto.PlanManifestHashesResponse, error)
	PlanSyncActionsFunc    func(proto.ProjectKey) (proto.PlanSyncActionsResponse, error)
	BeginFileFunc          func(proto.ProjectKey, proto.BeginFileRequest) (proto.BeginFileResponse, error)
	ApplyChunkFunc         func(proto.ProjectKey, proto.ApplyChunkRequest) error
	CommitFileFunc         func(proto.ProjectKey, proto.CommitFileRequest) error
	AbortFileFunc          func(proto.ProjectKey, proto.AbortFileRequest) error
	DeletePathFunc         func(proto.ProjectKey, proto.DeletePathRequest) error
	FinalizeSyncFunc       func(proto.ProjectKey, proto.FinalizeSyncRequest) error
}

func (s SyncBackendStub) BeginSync(key proto.ProjectKey, request proto.BeginSyncRequest) error {
	if s.BeginSyncFunc == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.BeginSyncFunc(key, request)
}

func (s SyncBackendStub) ScanManifest(key proto.ProjectKey, request proto.ScanManifestRequest) error {
	if s.ScanManifestFunc == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.ScanManifestFunc(key, request)
}

func (s SyncBackendStub) PlanManifestHashes(key proto.ProjectKey) (proto.PlanManifestHashesResponse, error) {
	if s.PlanManifestHashesFunc == nil {
		return proto.PlanManifestHashesResponse{}, proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.PlanManifestHashesFunc(key)
}

func (s SyncBackendStub) PlanSyncActions(key proto.ProjectKey) (proto.PlanSyncActionsResponse, error) {
	if s.PlanSyncActionsFunc == nil {
		return proto.PlanSyncActionsResponse{}, proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.PlanSyncActionsFunc(key)
}

func (s SyncBackendStub) BeginFile(key proto.ProjectKey, request proto.BeginFileRequest) (proto.BeginFileResponse, error) {
	if s.BeginFileFunc == nil {
		return proto.BeginFileResponse{}, proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.BeginFileFunc(key, request)
}

func (s SyncBackendStub) ApplyChunk(key proto.ProjectKey, request proto.ApplyChunkRequest) error {
	if s.ApplyChunkFunc == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.ApplyChunkFunc(key, request)
}

func (s SyncBackendStub) CommitFile(key proto.ProjectKey, request proto.CommitFileRequest) error {
	if s.CommitFileFunc == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.CommitFileFunc(key, request)
}

func (s SyncBackendStub) AbortFile(key proto.ProjectKey, request proto.AbortFileRequest) error {
	if s.AbortFileFunc == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.AbortFileFunc(key, request)
}

func (s SyncBackendStub) DeletePath(key proto.ProjectKey, request proto.DeletePathRequest) error {
	if s.DeletePathFunc == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.DeletePathFunc(key, request)
}

func (s SyncBackendStub) FinalizeSync(key proto.ProjectKey, request proto.FinalizeSyncRequest) error {
	if s.FinalizeSyncFunc == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.FinalizeSyncFunc(key, request)
}

type FilesystemBackendStub struct {
	FsReadContextFunc                  func(context.Context, proto.ProjectKey, proto.FsOperation, proto.FsRequest) (proto.FsReply, error)
	FsMutationContextFunc              func(context.Context, proto.ProjectKey, proto.FsOperation, proto.FsRequest, []proto.MutationPrecondition) (proto.FsReply, error)
	ApplyOwnerInvalidationsContextFunc func(context.Context, proto.ProjectKey, []proto.InvalidateChange) error
	EnterConservativeModeContextFunc   func(context.Context, proto.ProjectKey, string) ([]proto.InvalidateChange, error)
	SetAuthorityStateFunc              func(proto.ProjectKey, proto.AuthorityState) error
	SetAuthoritativeRootFunc           func(proto.ProjectKey, string) error
	SetAuthoritativeClientFunc         func(proto.ProjectKey, ownerfs.Client) error
	ClearAuthoritativeRootFunc         func(proto.ProjectKey) error
}

func (s FilesystemBackendStub) FsReadContext(ctx context.Context, key proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	if s.FsReadContextFunc == nil {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectNotReady, "filesystem backend is unavailable")
	}
	return s.FsReadContextFunc(ctx, key, op, request)
}

func (s FilesystemBackendStub) FsMutationContext(ctx context.Context, key proto.ProjectKey, op proto.FsOperation, request proto.FsRequest, preconditions []proto.MutationPrecondition) (proto.FsReply, error) {
	if s.FsMutationContextFunc == nil {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectNotReady, "filesystem backend is unavailable")
	}
	return s.FsMutationContextFunc(ctx, key, op, request, preconditions)
}

func (s FilesystemBackendStub) ApplyOwnerInvalidationsContext(ctx context.Context, key proto.ProjectKey, changes []proto.InvalidateChange) error {
	if s.ApplyOwnerInvalidationsContextFunc == nil {
		return nil
	}
	return s.ApplyOwnerInvalidationsContextFunc(ctx, key, changes)
}

func (s FilesystemBackendStub) EnterConservativeModeContext(ctx context.Context, key proto.ProjectKey, reason string) ([]proto.InvalidateChange, error) {
	if s.EnterConservativeModeContextFunc == nil {
		return nil, nil
	}
	return s.EnterConservativeModeContextFunc(ctx, key, reason)
}

func (s FilesystemBackendStub) SetAuthorityState(key proto.ProjectKey, state proto.AuthorityState) error {
	if s.SetAuthorityStateFunc == nil {
		return nil
	}
	return s.SetAuthorityStateFunc(key, state)
}

func (s FilesystemBackendStub) SetAuthoritativeRoot(key proto.ProjectKey, root string) error {
	if s.SetAuthoritativeRootFunc == nil {
		return nil
	}
	return s.SetAuthoritativeRootFunc(key, root)
}

func (s FilesystemBackendStub) SetAuthoritativeClient(key proto.ProjectKey, client ownerfs.Client) error {
	if s.SetAuthoritativeClientFunc == nil {
		return nil
	}
	return s.SetAuthoritativeClientFunc(key, client)
}

func (s FilesystemBackendStub) ClearAuthoritativeRoot(key proto.ProjectKey) error {
	if s.ClearAuthoritativeRootFunc == nil {
		return nil
	}
	return s.ClearAuthoritativeRootFunc(key)
}

type CommandBackendStub struct {
	LaunchFunc      func(control.CommandLaunch)
	ReadOutputFunc  func(proto.ProjectKey, proto.AttachStdioRequest) (proto.AttachStdioResponse, error)
	WaitOutputFunc  func(context.Context, proto.ProjectKey, proto.AttachStdioRequest) error
	ResizeTTYFunc   func(proto.ProjectKey, string, int, int) error
	SendSignalFunc  func(proto.ProjectKey, string, string) error
	WriteInputFunc  func(proto.ProjectKey, string, []byte) error
	CloseInputFunc  func(proto.ProjectKey, string) error
	StopProjectFunc func(proto.ProjectKey) error
}

func (s CommandBackendStub) Launch(launch control.CommandLaunch) {
	if s.LaunchFunc != nil {
		s.LaunchFunc(launch)
	}
}

func (s CommandBackendStub) ReadOutput(key proto.ProjectKey, request proto.AttachStdioRequest) (proto.AttachStdioResponse, error) {
	if s.ReadOutputFunc == nil {
		return proto.AttachStdioResponse{}, proto.NewError(proto.ErrInvalidArgument, "command output is unavailable")
	}
	return s.ReadOutputFunc(key, request)
}

func (s CommandBackendStub) WaitOutput(ctx context.Context, key proto.ProjectKey, request proto.AttachStdioRequest) error {
	if s.WaitOutputFunc == nil {
		return nil
	}
	return s.WaitOutputFunc(ctx, key, request)
}

func (s CommandBackendStub) ResizeTTY(key proto.ProjectKey, commandID string, columns int, rows int) error {
	if s.ResizeTTYFunc == nil {
		return nil
	}
	return s.ResizeTTYFunc(key, commandID, columns, rows)
}

func (s CommandBackendStub) SendSignal(key proto.ProjectKey, commandID string, name string) error {
	if s.SendSignalFunc == nil {
		return proto.NewError(proto.ErrInvalidArgument, "command signal is unavailable")
	}
	return s.SendSignalFunc(key, commandID, name)
}

func (s CommandBackendStub) WriteInput(key proto.ProjectKey, commandID string, data []byte) error {
	if s.WriteInputFunc == nil {
		return proto.NewError(proto.ErrInvalidArgument, "command input is unavailable")
	}
	return s.WriteInputFunc(key, commandID, data)
}

func (s CommandBackendStub) CloseInput(key proto.ProjectKey, commandID string) error {
	if s.CloseInputFunc == nil {
		return proto.NewError(proto.ErrInvalidArgument, "command input is unavailable")
	}
	return s.CloseInputFunc(key, commandID)
}

func (s CommandBackendStub) StopProject(key proto.ProjectKey) error {
	if s.StopProjectFunc == nil {
		return nil
	}
	return s.StopProjectFunc(key)
}
