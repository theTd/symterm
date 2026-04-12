package control

import (
	"context"
	"testing"

	"symterm/internal/proto"
)

func TestServiceCompleteCommandRecordsTerminalEvents(t *testing.T) {
	t.Parallel()

	service, clientID, projectKey := newActiveCommandService(t)
	started, err := service.StartCommand(clientID, proto.StartCommandRequest{
		ProjectID: projectKey.ProjectID,
		ArgvTail:  []string{"echo", "hi"},
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}

	if err := service.CompleteCommand(projectKey, started.CommandID, 23); err != nil {
		t.Fatalf("CompleteCommand() error = %v", err)
	}

	command, err := service.projects.InstanceForKey(projectKey)
	if err != nil {
		t.Fatalf("InstanceForKey() error = %v", err)
	}
	snapshot, err := command.Command(started.CommandID)
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	if snapshot.State != proto.CommandStateExited {
		t.Fatalf("Command().State = %q, want %q", snapshot.State, proto.CommandStateExited)
	}
	if snapshot.ExitCode == nil || *snapshot.ExitCode != 23 {
		t.Fatalf("Command().ExitCode = %#v, want 23", snapshot.ExitCode)
	}

	events, err := service.WatchCommand(clientID, proto.WatchCommandRequest{CommandID: started.CommandID})
	if err != nil {
		t.Fatalf("WatchCommand() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("WatchCommand() len = %d, want 3; events=%#v", len(events), events)
	}
	if events[0].Type != proto.CommandEventExecStarted {
		t.Fatalf("event[0] = %#v", events[0])
	}
	if events[1].Type != proto.CommandEventExited || events[1].ExitCode == nil || *events[1].ExitCode != 23 {
		t.Fatalf("event[1] = %#v", events[1])
	}
	if events[2].Type != proto.CommandEventIOClosed || events[2].Message != "command exited" {
		t.Fatalf("event[2] = %#v", events[2])
	}
}

func TestServiceFailCommandRecordsTerminalEvents(t *testing.T) {
	t.Parallel()

	service, clientID, projectKey := newActiveCommandService(t)
	started, err := service.StartCommand(clientID, proto.StartCommandRequest{
		ProjectID: projectKey.ProjectID,
		ArgvTail:  []string{"echo", "hi"},
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}

	if err := service.FailCommand(projectKey, started.CommandID, "exec failed for test"); err != nil {
		t.Fatalf("FailCommand() error = %v", err)
	}

	command, err := service.projects.InstanceForKey(projectKey)
	if err != nil {
		t.Fatalf("InstanceForKey() error = %v", err)
	}
	snapshot, err := command.Command(started.CommandID)
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	if snapshot.State != proto.CommandStateFailed {
		t.Fatalf("Command().State = %q, want %q", snapshot.State, proto.CommandStateFailed)
	}
	if snapshot.FailureReason != "exec failed for test" {
		t.Fatalf("Command().FailureReason = %q", snapshot.FailureReason)
	}

	events, err := service.WatchCommand(clientID, proto.WatchCommandRequest{CommandID: started.CommandID})
	if err != nil {
		t.Fatalf("WatchCommand() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("WatchCommand() len = %d, want 3; events=%#v", len(events), events)
	}
	if events[0].Type != proto.CommandEventExecStarted {
		t.Fatalf("event[0] = %#v", events[0])
	}
	if events[1].Type != proto.CommandEventExecFailed || events[1].Message != "exec failed for test" {
		t.Fatalf("event[1] = %#v", events[1])
	}
	if events[2].Type != proto.CommandEventIOClosed || events[2].Message != "exec failed" {
		t.Fatalf("event[2] = %#v", events[2])
	}
}

func newActiveCommandService(t testing.TB) (*Service, string, proto.ProjectKey) {
	t.Helper()

	service := newTestService(t, StaticTokenAuthenticator{"token-a": "alice"}, ServiceDependencies{
		Runtime: runtimeBackendStub{
			commandBackendStub: commandBackendStub{},
		},
		Now: fixedClock(),
	})
	hello, err := service.HelloAuthenticated(context.Background(), AuthenticatedPrincipal{
		Username:    "alice",
		TokenSource: TokenSourceBootstrap,
	}, proto.HelloRequest{
		ProjectID:          "demo",
		TransportKind:      string(TransportKindSSH),
		LocalWorkspaceRoot: `C:\workspace\symterm`,
		SessionKind:        proto.SessionKindAuthority,
		WorkspaceDigest:    testWorkspaceDigest(1, "root-a"),
	})
	if err != nil {
		t.Fatalf("HelloAuthenticated() error = %v", err)
	}

	snapshot, err := service.EnsureProjectRequest(hello.ClientID, proto.EnsureProjectRequest{ProjectID: "demo"})
	if err != nil {
		t.Fatalf("EnsureProjectRequest() error = %v", err)
	}
	if _, err := service.CompleteInitialSync(hello.ClientID, snapshot.SyncEpoch); err != nil {
		t.Fatalf("CompleteInitialSync() error = %v", err)
	}

	return service, hello.ClientID, proto.ProjectKey{Username: "alice", ProjectID: "demo"}
}
