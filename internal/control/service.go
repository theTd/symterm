package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"symterm/internal/diagnostic"
	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

type Authenticator interface {
	Authenticate(ctx context.Context, token string) (AuthenticatedPrincipal, error)
}

type StaticTokenAuthenticator map[string]string

func (a StaticTokenAuthenticator) Authenticate(_ context.Context, token string) (AuthenticatedPrincipal, error) {
	username, ok := a[token]
	if !ok {
		return AuthenticatedPrincipal{}, proto.NewError(proto.ErrAuthenticationFailed, "token authentication failed")
	}
	return AuthenticatedPrincipal{
		Username:        username,
		TokenID:         "static-" + tokenFingerprint(token),
		TokenSource:     TokenSourceManaged,
		AuthenticatedAt: time.Now().UTC(),
	}, nil
}

type Clock func() time.Time

type ProjectBootstrapper interface {
	PrepareProject(proto.ProjectKey) error
}

type SyncBackend interface {
	BeginSync(proto.ProjectKey, proto.BeginSyncRequest) error
	ScanManifest(proto.ProjectKey, proto.ScanManifestRequest) error
	PlanManifestHashes(proto.ProjectKey) (proto.PlanManifestHashesResponse, error)
	PlanSyncActions(proto.ProjectKey) (proto.PlanSyncActionsResponse, error)
	BeginFile(proto.ProjectKey, proto.BeginFileRequest) (proto.BeginFileResponse, error)
	ApplyChunk(proto.ProjectKey, proto.ApplyChunkRequest) error
	CommitFile(proto.ProjectKey, proto.CommitFileRequest) error
	AbortFile(proto.ProjectKey, proto.AbortFileRequest) error
	DeletePath(proto.ProjectKey, proto.DeletePathRequest) error
	FinalizeSync(proto.ProjectKey, proto.FinalizeSyncRequest) error
}

type FilesystemBackend interface {
	FsReadContext(context.Context, proto.ProjectKey, proto.FsOperation, proto.FsRequest) (proto.FsReply, error)
	FsMutationContext(context.Context, proto.ProjectKey, proto.FsOperation, proto.FsRequest, []proto.MutationPrecondition) (proto.FsReply, error)
	ApplyOwnerInvalidationsContext(context.Context, proto.ProjectKey, []proto.InvalidateChange) error
	EnterConservativeModeContext(context.Context, proto.ProjectKey, string) ([]proto.InvalidateChange, error)
	SetAuthorityState(proto.ProjectKey, proto.AuthorityState) error
	SetAuthoritativeRoot(proto.ProjectKey, string) error
	SetAuthoritativeClient(proto.ProjectKey, ownerfs.Client) error
	ClearAuthoritativeRoot(proto.ProjectKey) error
}

type CommandBackend interface {
	Launch(CommandLaunch)
	ReadOutput(proto.ProjectKey, proto.AttachStdioRequest) (proto.AttachStdioResponse, error)
	WaitOutput(context.Context, proto.ProjectKey, proto.AttachStdioRequest) error
	ResizeTTY(proto.ProjectKey, string, int, int) error
	SendSignal(proto.ProjectKey, string, string) error
	WriteInput(proto.ProjectKey, string, []byte) error
	CloseInput(proto.ProjectKey, string) error
	StopProject(proto.ProjectKey) error
}

type RuntimeBackend interface {
	ProjectBootstrapper
	SyncBackend
	FilesystemBackend
	CommandBackend
}

type ServiceDependencies struct {
	Runtime     RuntimeBackend
	Diagnostics diagnostic.Reporter
	Now         Clock
	Sessions    LiveSessionObserver
	Tracef      func(string, ...any)
}

type Service struct {
	auth            Authenticator
	runtime         RuntimeBackend
	diagnostics     diagnostic.Reporter
	now             Clock
	sessions        *SessionRegistry
	uploads         *UploadTracker
	commands        *CommandController
	invalidates     *InvalidateHub
	projects        *ProjectCoordinator
	projectSessions *ProjectSessionUseCase
	sessionObserver LiveSessionObserver
	tracef          func(string, ...any)
}

type CommandLaunch struct {
	ProjectKey proto.ProjectKey
	Command    proto.CommandSnapshot
}

type HelloResponse struct {
	ClientID  string
	SessionID string
	Username  string
	ProjectID string
}

func NewService(auth Authenticator) (*Service, error) {
	return NewServiceWithDependencies(auth, ServiceDependencies{})
}

func NewServiceWithDependencies(auth Authenticator, deps ServiceDependencies) (*Service, error) {
	if auth == nil {
		return nil, errors.New("authenticator is required")
	}

	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	reporter := deps.Diagnostics
	if reporter == nil {
		reporter = diagnostic.Default()
	}
	runtime := deps.Runtime

	sessions := newSessionRegistry()
	projects := newProjectCoordinator(runtime, deps.Tracef)
	commands, err := newCommandController(runtime, now)
	if err != nil {
		return nil, err
	}
	invalidates, err := newInvalidateHub(now)
	if err != nil {
		return nil, err
	}
	service := &Service{
		auth:            auth,
		runtime:         runtime,
		diagnostics:     reporter,
		now:             now,
		sessions:        sessions,
		uploads:         newUploadTracker(),
		commands:        commands,
		invalidates:     invalidates,
		projects:        projects,
		sessionObserver: deps.Sessions,
		tracef:          deps.Tracef,
	}
	service.projectSessions = newProjectSessionUseCase(sessions, projects, service.EnsureProjectRequest, service.ConfirmReconcile, service.StartCommand)

	return service, nil
}

func (s *Service) Hello(ctx context.Context, request proto.HelloRequest) (HelloResponse, error) {
	return HelloResponse{}, proto.NewError(proto.ErrAuthenticationFailed, "hello requires an authenticated SSH control channel")
}

func (s *Service) HelloAuthenticated(ctx context.Context, principal AuthenticatedPrincipal, request proto.HelloRequest) (HelloResponse, error) {
	s.trace(
		"hello start project=%q transport=%q workspace_root=%q workspace_instance_id=%q session_kind=%q digest=%s:%s",
		request.ProjectID,
		request.TransportKind,
		request.LocalWorkspaceRoot,
		request.WorkspaceInstanceID,
		proto.NormalizeSessionKind(request.SessionKind),
		request.WorkspaceDigest.Algorithm,
		request.WorkspaceDigest.Value,
	)
	if strings.TrimSpace(request.ProjectID) == "" {
		return HelloResponse{}, proto.NewError(proto.ErrInvalidArgument, "project id is required")
	}
	if kind := TransportKind(strings.TrimSpace(request.TransportKind)); kind != "" && kind != TransportKindSSH {
		return HelloResponse{}, proto.NewError(proto.ErrInvalidArgument, "transport kind must be ssh")
	}
	switch strings.TrimSpace(string(request.SessionKind)) {
	case "", string(proto.SessionKindInteractive), string(proto.SessionKindAuthority):
	default:
		return HelloResponse{}, proto.NewError(proto.ErrInvalidArgument, "session kind must be interactive or authority")
	}
	if principal.AuthenticatedAt.IsZero() {
		principal.AuthenticatedAt = s.now()
	}
	s.trace("hello authenticated project=%q username=%q token_id=%q token_source=%q", request.ProjectID, principal.Username, principal.TokenID, principal.TokenSource)

	response := s.sessions.HelloPrincipal(principal, request, s.now())
	s.trace("hello complete project=%q client_id=%q session_id=%q username=%q", request.ProjectID, response.ClientID, response.SessionID, response.Username)
	return response, nil
}

func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func (s *Service) trace(format string, args ...any) {
	if s == nil || s.tracef == nil {
		return
	}
	s.tracef(format, args...)
}
