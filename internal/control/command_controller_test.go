package control

import (
	"context"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestCommandControllerRoutesBackendAndRecordsNonTerminalEvents(t *testing.T) {
	t.Parallel()

	now := fixedClock()
	projectKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	command := proto.CommandSnapshot{
		CommandID: "cmd-1",
		StartedAt: now(),
	}

	var launched CommandLaunch
	var resized [2]int
	var signaled string
	var written []byte
	var closed bool
	controller, err := newCommandController(commandBackendStub{
		launch: func(received CommandLaunch) {
			launched = received
		},
		readOutput: func(key proto.ProjectKey, request proto.AttachStdioRequest) (proto.AttachStdioResponse, error) {
			if key != projectKey || request.CommandID != command.CommandID {
				t.Fatalf("ReadOutput() key=%#v request=%#v", key, request)
			}
			return proto.AttachStdioResponse{Stdout: []byte("stdout")}, nil
		},
		waitOutput: func(ctx context.Context, key proto.ProjectKey, request proto.AttachStdioRequest) error {
			if ctx == nil || key != projectKey || request.CommandID != command.CommandID {
				t.Fatalf("WaitOutput() key=%#v request=%#v", key, request)
			}
			return nil
		},
		resizeTTY: func(key proto.ProjectKey, commandID string, columns int, rows int) error {
			if key != projectKey || commandID != command.CommandID {
				t.Fatalf("ResizeTTY() key=%#v commandID=%q", key, commandID)
			}
			resized = [2]int{columns, rows}
			return nil
		},
		sendSignal: func(key proto.ProjectKey, commandID string, name string) error {
			if key != projectKey || commandID != command.CommandID {
				t.Fatalf("SendSignal() key=%#v commandID=%q", key, commandID)
			}
			signaled = name
			return nil
		},
		writeInput: func(key proto.ProjectKey, commandID string, data []byte) error {
			if key != projectKey || commandID != command.CommandID {
				t.Fatalf("WriteInput() key=%#v commandID=%q", key, commandID)
			}
			written = append([]byte(nil), data...)
			return nil
		},
		closeInput: func(key proto.ProjectKey, commandID string) error {
			if key != projectKey || commandID != command.CommandID {
				t.Fatalf("CloseInput() key=%#v commandID=%q", key, commandID)
			}
			closed = true
			return nil
		},
	}, now)
	if err != nil {
		t.Fatalf("newCommandController() error = %v", err)
	}

	if err := controller.Start(projectKey, command); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if launched.ProjectKey != projectKey || launched.Command.CommandID != command.CommandID {
		t.Fatalf("Launch() = %#v", launched)
	}

	reply, err := controller.ReadOutput(command.CommandID, proto.AttachStdioRequest{CommandID: command.CommandID})
	if err != nil {
		t.Fatalf("ReadOutput() error = %v", err)
	}
	if string(reply.Stdout) != "stdout" {
		t.Fatalf("ReadOutput() stdout = %q, want stdout", string(reply.Stdout))
	}
	if err := controller.WaitOutput(context.Background(), command.CommandID, proto.AttachStdioRequest{CommandID: command.CommandID}); err != nil {
		t.Fatalf("WaitOutput() error = %v", err)
	}
	if err := controller.ResizeTTY(command.CommandID, 80, 24); err != nil {
		t.Fatalf("ResizeTTY() error = %v", err)
	}
	if err := controller.SendSignal(command.CommandID, "TERM"); err != nil {
		t.Fatalf("SendSignal() error = %v", err)
	}
	if err := controller.WriteInput(command.CommandID, []byte("stdin")); err != nil {
		t.Fatalf("WriteInput() error = %v", err)
	}
	if err := controller.CloseInput(command.CommandID); err != nil {
		t.Fatalf("CloseInput() error = %v", err)
	}
	if resized != [2]int{80, 24} {
		t.Fatalf("ResizeTTY() = %#v", resized)
	}
	if signaled != "TERM" {
		t.Fatalf("SendSignal() = %q, want TERM", signaled)
	}
	if string(written) != "stdin" {
		t.Fatalf("WriteInput() = %q, want stdin", string(written))
	}
	if !closed {
		t.Fatal("CloseInput() was not invoked")
	}

	events, err := controller.Watch(command.CommandID, 0)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("Watch() len = %d, want 4; events=%#v", len(events), events)
	}
	if events[0].Type != proto.CommandEventExecStarted {
		t.Fatalf("event[0] = %q, want exec-started", events[0].Type)
	}
	if events[1].Type != proto.CommandEventTTYResized || events[1].Message != "80x24" {
		t.Fatalf("event[1] = %#v", events[1])
	}
	if events[2].Type != proto.CommandEventSignalSent || events[2].Message != "TERM" {
		t.Fatalf("event[2] = %#v", events[2])
	}
	if events[3].Type != proto.CommandEventIOClosed || events[3].Message != "stdin closed" {
		t.Fatalf("event[3] = %#v", events[3])
	}
}

func TestCommandControllerCleanupProjectClosesWatchersAndDropsOwnership(t *testing.T) {
	t.Parallel()

	now := fixedClock()
	projectKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	controller, err := newCommandController(commandBackendStub{}, now)
	if err != nil {
		t.Fatalf("newCommandController() error = %v", err)
	}
	if err := controller.Start(projectKey, proto.CommandSnapshot{
		CommandID: "cmd-1",
		StartedAt: now(),
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	_, _, ch, unsubscribe, err := controller.Subscribe("cmd-1", 0)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer unsubscribe()

	controller.CleanupProject(projectKey)

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("Subscribe() channel remained open after CleanupProject()")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Subscribe() channel was not closed by CleanupProject()")
	}

	events, err := controller.Watch("cmd-1", 0)
	if err != nil {
		t.Fatalf("Watch() after cleanup error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Watch() after cleanup = %#v, want empty recreated stream", events)
	}
	if err := controller.SendSignal("cmd-1", "TERM"); err == nil {
		t.Fatal("SendSignal() succeeded after CleanupProject()")
	}
}
