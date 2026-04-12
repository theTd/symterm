package app

import (
	"context"
	"io"
	"net"
	"testing"

	"symterm/internal/config"
	"symterm/internal/proto"
	workspacesync "symterm/internal/sync"
	"symterm/internal/transport"
)

type testLifecycle struct {
	conn net.Conn
}

func (l *testLifecycle) HasDedicatedStdio() bool {
	return false
}

func (l *testLifecycle) OpenDedicatedPipe(context.Context) (*transport.StdioPipeClient, io.Closer, error) {
	return nil, nil, nil
}

func (l *testLifecycle) OpenOwnerFileChannel(context.Context, string) (io.ReadWriteCloser, error) {
	return l.conn, nil
}

func (l *testLifecycle) Close() {}

type testInitialSyncer struct {
	calls func(context.Context, string, workspacesync.LocalWorkspaceSnapshot, uint64) (proto.ProjectSnapshot, error)
}

func (s testInitialSyncer) SyncProjectWorkspace(
	ctx context.Context,
	_ *transport.Client,
	clientID string,
	snapshot workspacesync.LocalWorkspaceSnapshot,
	syncEpoch uint64,
	_ *workspacesync.InitialSyncObserver,
) (proto.ProjectSnapshot, error) {
	return s.calls(ctx, clientID, snapshot, syncEpoch)
}

func TestStartOwnerFileServiceKeepsRuntimeAvailableWhileSyncingAndDelaysWatcher(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	controlReader, controlReaderPeer := io.Pipe()
	controlWriterPeer, controlWriter := io.Pipe()
	defer controlReaderPeer.Close()
	defer controlWriterPeer.Close()
	defer controlReader.Close()
	defer controlWriter.Close()

	session := ConnectedProjectSession{
		Snapshot: proto.ProjectSnapshot{
			Role:         proto.RoleOwner,
			ProjectState: proto.ProjectStateSyncing,
		},
		ClientID: "client-1",
	}
	useCase := ProjectSessionUseCase{
		Config: config.ClientConfig{
			Workdir: root,
		},
		ControlClient: transport.NewClient(controlReader, controlWriter),
		Lifecycle:     &testLifecycle{conn: clientConn},
		SessionKind:   proto.SessionKindAuthority,
	}

	if err := useCase.startOwnerFileService(context.Background(), &session); err != nil {
		t.Fatalf("startOwnerFileService() error = %v", err)
	}
	defer session.Close()

	if session.ownerRuntime == nil {
		t.Fatal("owner runtime was not created while syncing")
	}
	session.ensureOwnerRuntime(session.Snapshot)
	if session.ownerRuntime.stopWatcher != nil {
		t.Fatal("owner watcher started before initial sync completed")
	}

	session.ensureOwnerRuntime(proto.ProjectSnapshot{
		Role:         proto.RoleOwner,
		ProjectState: proto.ProjectStateActive,
	})
	if session.ownerRuntime.stopWatcher == nil {
		t.Fatal("owner watcher did not start after project became active")
	}
}

func TestPrepareSessionStartsOwnerRuntimeAndRunsInitialSync(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	controlReader, controlReaderPeer := io.Pipe()
	controlWriterPeer, controlWriter := io.Pipe()
	defer controlReaderPeer.Close()
	defer controlWriterPeer.Close()
	defer controlReader.Close()
	defer controlWriter.Close()

	useCase := ProjectSessionUseCase{
		Config: config.ClientConfig{
			Workdir: root,
		},
		ControlClient: transport.NewClient(controlReader, controlWriter),
		Lifecycle:     &testLifecycle{conn: clientConn},
		SessionKind:   proto.SessionKindAuthority,
		InitialSyncer: testInitialSyncer{
			calls: func(_ context.Context, clientID string, _ workspacesync.LocalWorkspaceSnapshot, syncEpoch uint64) (proto.ProjectSnapshot, error) {
				if clientID != "client-1" {
					t.Fatalf("SyncProjectWorkspace() clientID = %q, want client-1", clientID)
				}
				if syncEpoch != 7 {
					t.Fatalf("SyncProjectWorkspace() syncEpoch = %d, want 7", syncEpoch)
				}
				return proto.ProjectSnapshot{
					Role:         proto.RoleOwner,
					ProjectState: proto.ProjectStateActive,
					SyncEpoch:    syncEpoch,
				}, nil
			},
		},
	}

	session, err := useCase.prepareSession(context.Background(), ConnectedProjectSession{
		ClientID:      "client-1",
		LocalSnapshot: workspacesync.LocalWorkspaceSnapshot{},
		Snapshot: proto.ProjectSnapshot{
			Role:         proto.RoleOwner,
			ProjectState: proto.ProjectStateSyncing,
			SyncEpoch:    7,
		},
	}, "initial sync")
	if err != nil {
		t.Fatalf("prepareSession() error = %v", err)
	}
	defer session.Close()

	if session.ownerRuntime == nil {
		t.Fatal("prepareSession() did not start owner runtime")
	}
	if session.Snapshot.ProjectState != proto.ProjectStateActive {
		t.Fatalf("prepareSession() state = %q, want active", session.Snapshot.ProjectState)
	}
	if session.ownerRuntime.stopWatcher == nil {
		t.Fatal("prepareSession() did not start owner watcher after sync completed")
	}
}
