package app

import (
	"context"
	"io"
	"strings"
	"time"

	"symterm/internal/config"
	"symterm/internal/control"
	"symterm/internal/proto"
	workspacesync "symterm/internal/sync"
	"symterm/internal/transport"
	"symterm/internal/workspaceidentity"
)

type ProjectSessionResult struct {
	Hello    control.HelloResponse
	Snapshot proto.ProjectSnapshot
	Command  *proto.StartCommandResponse
	Events   []proto.CommandEvent
	Output   *proto.AttachStdioResponse
}

type SessionIO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// ProjectSessionLifecycle is a pure endpoint resource adapter.
// The project session use case owns business timing such as when owner runtime starts.
type ProjectSessionLifecycle interface {
	HasDedicatedStdio() bool
	OpenDedicatedPipe(context.Context) (*transport.StdioPipeClient, io.Closer, error)
	OpenOwnerFileChannel(context.Context, string) (io.ReadWriteCloser, error)
	Close()
}

type ConnectedProjectSession struct {
	Hello         control.HelloResponse
	Snapshot      proto.ProjectSnapshot
	ClientID      string
	LocalSnapshot workspacesync.LocalWorkspaceSnapshot
	ownerRuntime  *ownerProjectRuntime
}

func (s *ConnectedProjectSession) Close() {
	if s == nil || s.ownerRuntime == nil {
		return
	}
	s.ownerRuntime.Close()
}

func (s *ConnectedProjectSession) Done() <-chan struct{} {
	if s == nil || s.ownerRuntime == nil {
		return nil
	}
	return s.ownerRuntime.Done()
}

func (s *ConnectedProjectSession) ensureOwnerRuntime(snapshot proto.ProjectSnapshot) {
	if s == nil || s.ownerRuntime == nil {
		return
	}
	s.ownerRuntime.Ensure(snapshot)
}

type LocalWorkspaceSnapshotter interface {
	Snapshot(root string, hashPaths map[string]bool, hashAll bool) (workspacesync.LocalWorkspaceSnapshot, error)
}

type ProjectInitialSyncer interface {
	SyncProjectWorkspace(
		ctx context.Context,
		client *transport.Client,
		clientID string,
		snapshot workspacesync.LocalWorkspaceSnapshot,
		syncEpoch uint64,
		observer *workspacesync.InitialSyncObserver,
	) (proto.ProjectSnapshot, error)
}

type SyncFeedback interface {
	InitialSyncObserver() *workspacesync.InitialSyncObserver
	FinishInitialSync()
}

type ProjectSessionUseCase struct {
	ControlClient        *transport.Client
	Config               config.ClientConfig
	Lifecycle            ProjectSessionLifecycle
	SessionKind          proto.SessionKind
	WorkspaceSnapshotter LocalWorkspaceSnapshotter
	InitialSyncer        ProjectInitialSyncer
	SyncFeedback         SyncFeedback
	Tracef               func(string, ...any)
}

func (u ProjectSessionUseCase) ConnectProjectSession(ctx context.Context) (ConnectedProjectSession, error) {
	u.tracef("scan local workspace root=%q", u.Config.Workdir)
	scanStarted := time.Now()
	localSnapshot, err := u.workspaceSnapshotter().Snapshot(u.Config.Workdir, nil, false)
	if err != nil {
		return ConnectedProjectSession{}, err
	}
	u.tracef("scan local workspace finished in %s", time.Since(scanStarted).Round(time.Millisecond))
	u.tracef("local workspace digest=%s:%s", localSnapshot.Digest.Algorithm, localSnapshot.Digest.Value)
	workspaceInstanceID, err := workspaceidentity.DefaultWorkspaceInstanceID(u.Config.Workdir)
	if err != nil {
		return ConnectedProjectSession{}, err
	}
	localSnapshot.WorkspaceInstanceID = workspaceInstanceID
	u.tracef("workspace instance id=%s", workspaceInstanceID)

	var hello control.HelloResponse
	u.tracef(
		"hello request project=%s transport=%s workspace_root=%q",
		u.Config.ProjectID,
		endpointTransportKind(u.Config.Endpoint.Kind),
		ClientWorkspaceRoot(u.Config),
	)
	if err := u.ControlClient.Call(ctx, "hello", "", proto.HelloRequest{
		Version:             "v1alpha1",
		ProjectID:           u.Config.ProjectID,
		TransportKind:       string(endpointTransportKind(u.Config.Endpoint.Kind)),
		LocalWorkspaceRoot:  ClientWorkspaceRoot(u.Config),
		WorkspaceInstanceID: workspaceInstanceID,
		SessionKind:         u.sessionKind(),
		WorkspaceDigest:     localSnapshot.Digest,
	}, &hello); err != nil {
		return ConnectedProjectSession{}, err
	}
	u.tracef(
		"hello response client_id=%s username=%s sync_protocol=%d manifest_batch=%t delete_batch=%t upload_bundle=%t hash_cache=%t",
		hello.ClientID,
		hello.Username,
		hello.SyncCapabilities.ProtocolVersion,
		hello.SyncCapabilities.ManifestBatch,
		hello.SyncCapabilities.DeleteBatch,
		hello.SyncCapabilities.UploadBundle,
		hello.SyncCapabilities.PersistentHashCache,
	)

	var sessionResponse proto.ProjectSessionResponse
	u.tracef("open_project_session project=%s", u.Config.ProjectID)
	if err := u.ControlClient.Call(ctx, "open_project_session", hello.ClientID, proto.OpenProjectSessionRequest{
		ProjectID: u.Config.ProjectID,
	}, &sessionResponse); err != nil {
		return ConnectedProjectSession{}, err
	}
	snapshot := sessionResponse.Snapshot
	u.tracef(
		"project snapshot role=%s state=%s sync_epoch=%d can_start=%t needs_confirmation=%t commands=%d cursor=%d",
		snapshot.Role,
		snapshot.ProjectState,
		snapshot.SyncEpoch,
		snapshot.CanStartCommands,
		snapshot.NeedsConfirmation,
		len(snapshot.CommandSnapshots),
		snapshot.CurrentCursor,
	)
	session := ConnectedProjectSession{
		Hello:         hello,
		Snapshot:      snapshot,
		ClientID:      hello.ClientID,
		LocalSnapshot: localSnapshot,
	}
	return u.prepareSession(ctx, session, "initial sync")
}

func (u ProjectSessionUseCase) ConfirmAndResumeProjectSession(ctx context.Context, session ConnectedProjectSession) (ConnectedProjectSession, error) {
	if !u.Config.ConfirmReconcile {
		u.tracef("confirm reconcile skipped")
		return session, nil
	}
	if !session.Snapshot.NeedsConfirmation {
		u.tracef("confirm reconcile no-op project is already ready")
		return session, nil
	}

	var sessionResponse proto.ProjectSessionResponse
	u.tracef(
		"resume_project_session project=%s expected_cursor=%d workspace_digest=%s:%s",
		u.Config.ProjectID,
		session.Snapshot.CurrentCursor,
		session.LocalSnapshot.Digest.Algorithm,
		session.LocalSnapshot.Digest.Value,
	)
	if err := u.ControlClient.Call(ctx, "resume_project_session", session.ClientID, proto.ResumeProjectSessionRequest{
		ProjectID:       u.Config.ProjectID,
		ExpectedCursor:  session.Snapshot.CurrentCursor,
		WorkspaceDigest: session.LocalSnapshot.Digest,
	}, &sessionResponse); err != nil {
		return ConnectedProjectSession{}, err
	}
	session.Snapshot = sessionResponse.Snapshot
	u.tracef(
		"resume snapshot role=%s state=%s sync_epoch=%d can_start=%t needs_confirmation=%t cursor=%d",
		session.Snapshot.Role,
		session.Snapshot.ProjectState,
		session.Snapshot.SyncEpoch,
		session.Snapshot.CanStartCommands,
		session.Snapshot.NeedsConfirmation,
		session.Snapshot.CurrentCursor,
	)
	return u.prepareSession(ctx, session, "post-resume initial sync")
}

func (u ProjectSessionUseCase) prepareSession(ctx context.Context, session ConnectedProjectSession, syncLabel string) (ConnectedProjectSession, error) {
	if err := u.startOwnerFileService(ctx, &session); err != nil {
		return ConnectedProjectSession{}, err
	}
	session.ensureOwnerRuntime(session.Snapshot)
	if !u.needsInitialSync(session.Snapshot) {
		return session, nil
	}

	syncFeedback := u.syncFeedback()
	if syncFeedback != nil {
		defer syncFeedback.FinishInitialSync()
	}
	u.tracef("%s start sync_epoch=%d", syncLabel, session.Snapshot.SyncEpoch)
	snapshot, err := u.initialSyncerForSession(session).SyncProjectWorkspace(
		ctx,
		u.ControlClient,
		session.ClientID,
		session.LocalSnapshot,
		session.Snapshot.SyncEpoch,
		u.initialSyncObserver(),
	)
	if err != nil {
		session.Close()
		return ConnectedProjectSession{}, err
	}
	session.Snapshot = snapshot
	u.tracef("%s complete state=%s cursor=%d", syncLabel, snapshot.ProjectState, snapshot.CurrentCursor)
	session.ensureOwnerRuntime(session.Snapshot)
	return session, nil
}

func (u ProjectSessionUseCase) needsInitialSync(snapshot proto.ProjectSnapshot) bool {
	return u.sessionKind() == proto.SessionKindAuthority &&
		snapshot.Role == proto.RoleOwner &&
		snapshot.ProjectState == proto.ProjectStateSyncing
}

func (u ProjectSessionUseCase) startOwnerFileService(ctx context.Context, session *ConnectedProjectSession) error {
	if session == nil || session.ownerRuntime != nil || u.Lifecycle == nil {
		u.tracef("owner file service skipped ready=%t lifecycle=%t", session != nil && session.ownerRuntime != nil, u.Lifecycle != nil)
		return nil
	}
	if u.sessionKind() != proto.SessionKindAuthority {
		u.tracef("owner file service not started session_kind=%s", u.sessionKind())
		return nil
	}
	if session.Snapshot.Role != proto.RoleOwner || strings.TrimSpace(u.Config.Workdir) == "" {
		u.tracef("owner file service not started role=%s workdir=%q", session.Snapshot.Role, u.Config.Workdir)
		return nil
	}
	conn, err := u.Lifecycle.OpenOwnerFileChannel(ctx, session.ClientID)
	if err != nil {
		return err
	}
	u.tracef("owner file service started client_id=%s", session.ClientID)
	session.ownerRuntime = StartOwnerProjectRuntime(
		ctx,
		conn,
		u.ControlClient,
		session.ClientID,
		workspacesync.NewOwnerWorkspaceRuntime(u.Config.Workdir),
	)
	return nil
}

func projectSessionResult(session ConnectedProjectSession) ProjectSessionResult {
	return ProjectSessionResult{
		Hello:    session.Hello,
		Snapshot: session.Snapshot,
	}
}

func ClientWorkspaceRoot(cfg config.ClientConfig) string {
	return cfg.Workdir
}

func (u ProjectSessionUseCase) workspaceSnapshotter() LocalWorkspaceSnapshotter {
	if u.WorkspaceSnapshotter != nil {
		return u.WorkspaceSnapshotter
	}
	return workspacesync.WorkspaceSnapshotScanner{}
}

func (u ProjectSessionUseCase) initialSyncerForSession(session ConnectedProjectSession) ProjectInitialSyncer {
	if u.InitialSyncer != nil {
		return u.InitialSyncer
	}
	return workspacesync.InitialSyncSessionRunner{
		Capabilities: session.Hello.SyncCapabilities,
		Tracef:       u.Tracef,
	}
}

func (u ProjectSessionUseCase) initialSyncObserver() *workspacesync.InitialSyncObserver {
	if u.SyncFeedback == nil {
		return nil
	}
	return u.SyncFeedback.InitialSyncObserver()
}

func (u ProjectSessionUseCase) syncFeedback() SyncFeedback {
	return u.SyncFeedback
}

func (u ProjectSessionUseCase) sessionKind() proto.SessionKind {
	return proto.NormalizeSessionKind(u.SessionKind)
}

func (u ProjectSessionUseCase) tracef(format string, args ...any) {
	if u.Tracef == nil {
		return
	}
	u.Tracef(format, args...)
}

func endpointTransportKind(kind config.EndpointKind) control.TransportKind {
	if kind != config.EndpointSSH {
		return control.TransportKindUnknown
	}
	return control.TransportKindSSH
}
