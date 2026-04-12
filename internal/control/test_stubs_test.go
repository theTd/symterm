package control

import (
	"context"
	"testing"

	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

func newTestService(t testing.TB, auth Authenticator, deps ServiceDependencies) *Service {
	t.Helper()

	service, err := NewServiceWithDependencies(auth, deps)
	if err != nil {
		t.Fatalf("NewServiceWithDependencies() error = %v", err)
	}
	return service
}

type bootstrapperStub struct {
	prepareProject func(proto.ProjectKey) error
}

func (s bootstrapperStub) PrepareProject(key proto.ProjectKey) error {
	if s.prepareProject == nil {
		return nil
	}
	return s.prepareProject(key)
}

type runtimeBackendStub struct {
	bootstrapperStub
	syncBackendStub
	filesystemBackendStub
	commandBackendStub
}

type syncBackendStub struct {
	beginSync          func(proto.ProjectKey, proto.BeginSyncRequest) error
	scanManifest       func(proto.ProjectKey, proto.ScanManifestRequest) error
	planManifestHashes func(proto.ProjectKey) (proto.PlanManifestHashesResponse, error)
	planSyncActions    func(proto.ProjectKey) (proto.PlanSyncActionsResponse, error)
	beginFile          func(proto.ProjectKey, proto.BeginFileRequest) (proto.BeginFileResponse, error)
	applyChunk         func(proto.ProjectKey, proto.ApplyChunkRequest) error
	commitFile         func(proto.ProjectKey, proto.CommitFileRequest) error
	abortFile          func(proto.ProjectKey, proto.AbortFileRequest) error
	deletePath         func(proto.ProjectKey, proto.DeletePathRequest) error
	finalizeSync       func(proto.ProjectKey, proto.FinalizeSyncRequest) error
}

func (s syncBackendStub) BeginSync(key proto.ProjectKey, request proto.BeginSyncRequest) error {
	if s.beginSync == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.beginSync(key, request)
}

func (s syncBackendStub) ScanManifest(key proto.ProjectKey, request proto.ScanManifestRequest) error {
	if s.scanManifest == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.scanManifest(key, request)
}

func (s syncBackendStub) PlanManifestHashes(key proto.ProjectKey) (proto.PlanManifestHashesResponse, error) {
	if s.planManifestHashes == nil {
		return proto.PlanManifestHashesResponse{}, proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.planManifestHashes(key)
}

func (s syncBackendStub) PlanSyncActions(key proto.ProjectKey) (proto.PlanSyncActionsResponse, error) {
	if s.planSyncActions == nil {
		return proto.PlanSyncActionsResponse{}, proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.planSyncActions(key)
}

func (s syncBackendStub) BeginFile(key proto.ProjectKey, request proto.BeginFileRequest) (proto.BeginFileResponse, error) {
	if s.beginFile == nil {
		return proto.BeginFileResponse{}, proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.beginFile(key, request)
}

func (s syncBackendStub) ApplyChunk(key proto.ProjectKey, request proto.ApplyChunkRequest) error {
	if s.applyChunk == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.applyChunk(key, request)
}

func (s syncBackendStub) CommitFile(key proto.ProjectKey, request proto.CommitFileRequest) error {
	if s.commitFile == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.commitFile(key, request)
}

func (s syncBackendStub) AbortFile(key proto.ProjectKey, request proto.AbortFileRequest) error {
	if s.abortFile == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.abortFile(key, request)
}

func (s syncBackendStub) DeletePath(key proto.ProjectKey, request proto.DeletePathRequest) error {
	if s.deletePath == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.deletePath(key, request)
}

func (s syncBackendStub) FinalizeSync(key proto.ProjectKey, request proto.FinalizeSyncRequest) error {
	if s.finalizeSync == nil {
		return proto.NewError(proto.ErrProjectNotReady, "sync backend is unavailable")
	}
	return s.finalizeSync(key, request)
}

type filesystemBackendStub struct {
	fsReadContext                  func(context.Context, proto.ProjectKey, proto.FsOperation, proto.FsRequest) (proto.FsReply, error)
	fsMutationContext              func(context.Context, proto.ProjectKey, proto.FsOperation, proto.FsRequest, []proto.MutationPrecondition) (proto.FsReply, error)
	applyOwnerInvalidationsContext func(context.Context, proto.ProjectKey, []proto.InvalidateChange) error
	enterConservativeModeContext   func(context.Context, proto.ProjectKey, string) ([]proto.InvalidateChange, error)
	setAuthorityState              func(proto.ProjectKey, proto.AuthorityState) error
	setAuthoritativeRoot           func(proto.ProjectKey, string) error
	setAuthoritativeClient         func(proto.ProjectKey, ownerfs.Client) error
	clearAuthoritativeRoot         func(proto.ProjectKey) error
}

func (s filesystemBackendStub) FsReadContext(ctx context.Context, key proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	if s.fsReadContext == nil {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectNotReady, "filesystem backend is unavailable")
	}
	return s.fsReadContext(ctx, key, op, request)
}

func (s filesystemBackendStub) FsMutationContext(ctx context.Context, key proto.ProjectKey, op proto.FsOperation, request proto.FsRequest, preconditions []proto.MutationPrecondition) (proto.FsReply, error) {
	if s.fsMutationContext == nil {
		return proto.FsReply{}, proto.NewError(proto.ErrProjectNotReady, "filesystem backend is unavailable")
	}
	return s.fsMutationContext(ctx, key, op, request, preconditions)
}

func (s filesystemBackendStub) ApplyOwnerInvalidationsContext(ctx context.Context, key proto.ProjectKey, changes []proto.InvalidateChange) error {
	if s.applyOwnerInvalidationsContext == nil {
		return nil
	}
	return s.applyOwnerInvalidationsContext(ctx, key, changes)
}

func (s filesystemBackendStub) EnterConservativeModeContext(ctx context.Context, key proto.ProjectKey, reason string) ([]proto.InvalidateChange, error) {
	if s.enterConservativeModeContext == nil {
		return nil, nil
	}
	return s.enterConservativeModeContext(ctx, key, reason)
}

func (s filesystemBackendStub) SetAuthorityState(key proto.ProjectKey, state proto.AuthorityState) error {
	if s.setAuthorityState == nil {
		return nil
	}
	return s.setAuthorityState(key, state)
}

func (s filesystemBackendStub) SetAuthoritativeRoot(key proto.ProjectKey, root string) error {
	if s.setAuthoritativeRoot == nil {
		return nil
	}
	return s.setAuthoritativeRoot(key, root)
}

func (s filesystemBackendStub) SetAuthoritativeClient(key proto.ProjectKey, client ownerfs.Client) error {
	if s.setAuthoritativeClient == nil {
		return nil
	}
	return s.setAuthoritativeClient(key, client)
}

func (s filesystemBackendStub) ClearAuthoritativeRoot(key proto.ProjectKey) error {
	if s.clearAuthoritativeRoot == nil {
		return nil
	}
	return s.clearAuthoritativeRoot(key)
}

type commandBackendStub struct {
	launch      func(CommandLaunch)
	readOutput  func(proto.ProjectKey, proto.AttachStdioRequest) (proto.AttachStdioResponse, error)
	waitOutput  func(context.Context, proto.ProjectKey, proto.AttachStdioRequest) error
	resizeTTY   func(proto.ProjectKey, string, int, int) error
	sendSignal  func(proto.ProjectKey, string, string) error
	writeInput  func(proto.ProjectKey, string, []byte) error
	closeInput  func(proto.ProjectKey, string) error
	stopProject func(proto.ProjectKey) error
}

func (s commandBackendStub) Launch(launch CommandLaunch) {
	if s.launch != nil {
		s.launch(launch)
	}
}

func (s commandBackendStub) ReadOutput(key proto.ProjectKey, request proto.AttachStdioRequest) (proto.AttachStdioResponse, error) {
	if s.readOutput == nil {
		return proto.AttachStdioResponse{}, proto.NewError(proto.ErrInvalidArgument, "command output is unavailable")
	}
	return s.readOutput(key, request)
}

func (s commandBackendStub) WaitOutput(ctx context.Context, key proto.ProjectKey, request proto.AttachStdioRequest) error {
	if s.waitOutput == nil {
		return nil
	}
	return s.waitOutput(ctx, key, request)
}

func (s commandBackendStub) ResizeTTY(key proto.ProjectKey, commandID string, columns int, rows int) error {
	if s.resizeTTY == nil {
		return nil
	}
	return s.resizeTTY(key, commandID, columns, rows)
}

func (s commandBackendStub) SendSignal(key proto.ProjectKey, commandID string, name string) error {
	if s.sendSignal == nil {
		return proto.NewError(proto.ErrInvalidArgument, "command signal is unavailable")
	}
	return s.sendSignal(key, commandID, name)
}

func (s commandBackendStub) WriteInput(key proto.ProjectKey, commandID string, data []byte) error {
	if s.writeInput == nil {
		return proto.NewError(proto.ErrInvalidArgument, "command input is unavailable")
	}
	return s.writeInput(key, commandID, data)
}

func (s commandBackendStub) CloseInput(key proto.ProjectKey, commandID string) error {
	if s.closeInput == nil {
		return proto.NewError(proto.ErrInvalidArgument, "command input is unavailable")
	}
	return s.closeInput(key, commandID)
}

func (s commandBackendStub) StopProject(key proto.ProjectKey) error {
	if s.stopProject == nil {
		return nil
	}
	return s.stopProject(key)
}
