package control

import (
	"context"
	"io"

	"symterm/internal/diagnostic"
	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

type DiagnosticsProvider interface {
	Diagnostics() diagnostic.Reporter
}

type ClientService interface {
	DiagnosticsProvider
	Hello(context.Context, proto.HelloRequest) (HelloResponse, error)
	HelloAuthenticated(context.Context, AuthenticatedPrincipal, proto.HelloRequest) (HelloResponse, error)
	DisconnectClient(string)
	RetainClient(string) error
	ReleaseClient(string) error
	RegisterOwnerFileClient(string, ownerfs.Client) error
	BindControlConnection(string, ConnMeta, *TrafficCounters, io.Closer) error
	AttachSessionChannel(string, ConnMeta, *TrafficCounters, io.Closer) (string, error)
	DetachSessionChannel(string, string)
	NoteSessionActivity(string)
	OpenProjectSession(string, proto.OpenProjectSessionRequest) (proto.ProjectSessionResponse, error)
	EnsureProjectRequest(string, proto.EnsureProjectRequest) (proto.ProjectSnapshot, error)
	CompleteInitialSync(string, uint64) (proto.ProjectSnapshot, error)
	ConfirmReconcile(string, proto.ConfirmReconcileRequest) (proto.ProjectSnapshot, error)
	ResumeProjectSession(string, proto.ResumeProjectSessionRequest) (proto.ProjectSessionResponse, error)
	WatchProject(string, proto.WatchProjectRequest) ([]proto.ProjectEvent, error)
	SubscribeProjectEvents(string, proto.WatchProjectRequest) ([]proto.ProjectEvent, uint64, <-chan struct{}, func(), error)
	ReportSyncProgress(string, proto.ReportSyncProgressRequest) error
	BeginSync(string, proto.BeginSyncRequest) error
	ScanManifest(string, proto.ScanManifestRequest) error
	PlanManifestHashes(string) (proto.PlanManifestHashesResponse, error)
	PlanSyncActions(string) (proto.PlanSyncActionsResponse, error)
	BeginFile(string, proto.BeginFileRequest) (proto.BeginFileResponse, error)
	ApplyChunk(string, proto.ApplyChunkRequest) error
	CommitFile(string, proto.CommitFileRequest) error
	AbortFile(string, proto.AbortFileRequest) error
	DeletePath(string, proto.DeletePathRequest) error
	FinalizeSync(string, proto.FinalizeSyncRequest) (proto.ProjectSnapshot, error)
	FsReadContext(context.Context, string, proto.FsOperation, proto.FsRequest) (proto.FsReply, error)
	FsMutationContext(context.Context, string, proto.FsMutationRequest) (proto.FsReply, error)
	WatchInvalidate(string, proto.WatchInvalidateRequest) ([]proto.InvalidateEvent, error)
	SubscribeInvalidateEvents(string, proto.WatchInvalidateRequest) ([]proto.InvalidateEvent, uint64, <-chan struct{}, func(), error)
	InvalidateContext(context.Context, string, proto.InvalidateRequest) error
	OwnerWatcherFailedContext(context.Context, string, proto.OwnerWatcherFailureRequest) error
	StartCommand(string, proto.StartCommandRequest) (proto.StartCommandResponse, error)
	StartProjectCommandSession(context.Context, string, proto.StartProjectCommandSessionRequest) (proto.StartProjectCommandSessionResponse, error)
	AttachStdio(string, proto.AttachStdioRequest) (proto.AttachStdioResponse, error)
	WaitCommandOutput(context.Context, string, proto.AttachStdioRequest) error
	OpenStdio(string, string) error
	DetachStdio(string, string) error
	WatchCommand(string, proto.WatchCommandRequest) ([]proto.CommandEvent, error)
	SubscribeCommandEvents(string, proto.WatchCommandRequest) ([]proto.CommandEvent, uint64, <-chan struct{}, func(), error)
	ResizeTTY(string, proto.ResizeTTYRequest) (proto.ResizeTTYResponse, error)
	SendSignal(string, proto.SendSignalRequest) (proto.SendSignalResponse, error)
	WriteCommandInput(string, string, []byte) error
	CloseCommandInput(string, string) error
}

type AdminSessionService interface {
	ListSessions() []LiveSessionSnapshot
	SessionSnapshot(string) (LiveSessionSnapshot, bool)
	TerminateSession(string, string) error
}

type ProjectRuntimeControl interface {
	DiagnosticsProvider
	ProjectFsReadContext(context.Context, proto.ProjectKey, proto.FsOperation, proto.FsRequest) (proto.FsReply, error)
	ProjectFsMutationContext(context.Context, proto.ProjectKey, proto.FsMutationRequest) (proto.FsReply, error)
	SubscribeProjectInvalidate(proto.ProjectKey, uint64) ([]proto.InvalidateEvent, <-chan struct{}, func(), error)
	CompleteCommand(proto.ProjectKey, string, int) error
	FailCommand(proto.ProjectKey, string, string) error
	TerminateProject(proto.ProjectKey, string) error
}
