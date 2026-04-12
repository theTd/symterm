package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestRuntimeHelperProcess(t *testing.T) {
	mode, args, ok := runtimeHelperModeArg()
	if !ok {
		t.Skip("helper process only")
	}

	switch mode {
	case "echo-stdin-line":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			os.Exit(2)
		}
		_, _ = io.WriteString(os.Stdout, strings.TrimRight(line, "\r\n"))
		os.Exit(0)
	case "print-argv-json":
		raw, _ := json.Marshal(args)
		_, _ = os.Stdout.Write(raw)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func waitForRuntimeActive(t *testing.T, manager *RuntimeManager, key proto.ProjectKey, commandID string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := manager.waitForActive(ctx, key, commandID); err != nil {
		t.Fatalf("waitForActive() error = %v", err)
	}
}

func TestRuntimeManagerWriteStdin(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewRuntimeManager(root, []string{os.Args[0], "-test.run=TestRuntimeHelperProcess", "--"}, NewMountManager(root, true, nil))
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	exitCh := make(chan int, 1)
	failCh := make(chan string, 1)
	manager.Launch(context.Background(), LaunchRequest{
		ProjectKey: key,
		Command: proto.CommandSnapshot{
			CommandID: "cmd-0001",
			ArgvTail:  []string{"echo-stdin-line"},
		},
	}, func(exitCode int) {
		exitCh <- exitCode
	}, func(reason string) {
		failCh <- reason
	})

	waitForRuntimeActive(t, manager, key, "cmd-0001")

	if err := manager.WriteStdin(key, "cmd-0001", []byte("hello-runtime\n")); err != nil {
		t.Fatalf("WriteStdin() error = %v", err)
	}

	select {
	case reason := <-failCh:
		t.Fatalf("launch failure = %s", reason)
	case exitCode := <-exitCh:
		if exitCode != 0 {
			t.Fatalf("exit code = %d", exitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for command exit")
	}

	output, err := manager.AttachStdio(key, proto.AttachStdioRequest{CommandID: "cmd-0001"})
	if err != nil {
		t.Fatalf("AttachStdio() error = %v", err)
	}
	if !strings.Contains(string(output.Stdout), "hello-runtime") {
		t.Fatalf("stdout = %q", string(output.Stdout))
	}
}

func TestRuntimeManagerStopProjectKillsActiveCommands(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewRuntimeManager(root, []string{os.Args[0], "-test.run=TestRuntimeHelperProcess", "--"}, NewMountManager(root, true, nil))
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	exitCh := make(chan int, 1)
	failCh := make(chan string, 1)
	manager.Launch(context.Background(), LaunchRequest{
		ProjectKey: key,
		Command: proto.CommandSnapshot{
			CommandID: "cmd-0002",
			ArgvTail:  []string{"echo-stdin-line"},
		},
	}, func(exitCode int) {
		exitCh <- exitCode
	}, func(reason string) {
		failCh <- reason
	})

	waitForRuntimeActive(t, manager, key, "cmd-0002")

	if err := manager.StopProject(key); err != nil {
		t.Fatalf("StopProject() error = %v", err)
	}

	select {
	case <-exitCh:
	case <-failCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for command termination after StopProject()")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := manager.lookupActive(key, "cmd-0002"); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command remained active after StopProject()")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestRuntimeManagerStopAllProjectsKillsCommandsAndReleasesWaiters(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewRuntimeManager(root, []string{os.Args[0], "-test.run=TestRuntimeHelperProcess", "--"}, NewMountManager(root, true, nil))
	keyOne := proto.ProjectKey{Username: "alice", ProjectID: "demo-one"}
	keyTwo := proto.ProjectKey{Username: "bob", ProjectID: "demo-two"}
	for _, key := range []proto.ProjectKey{keyOne, keyTwo} {
		layout, err := ResolveProjectLayout(root, key)
		if err != nil {
			t.Fatalf("ResolveProjectLayout(%#v) error = %v", key, err)
		}
		if err := layout.EnsureDirectories(); err != nil {
			t.Fatalf("EnsureDirectories(%#v) error = %v", key, err)
		}
	}

	exitCh := make(chan string, 2)
	failCh := make(chan string, 2)
	launch := func(key proto.ProjectKey, commandID string) {
		manager.Launch(context.Background(), LaunchRequest{
			ProjectKey: key,
			Command: proto.CommandSnapshot{
				CommandID: commandID,
				ArgvTail:  []string{"echo-stdin-line"},
			},
		}, func(int) {
			exitCh <- commandID
		}, func(string) {
			failCh <- commandID
		})
	}
	launch(keyOne, "cmd-0001")
	launch(keyTwo, "cmd-0002")

	waitForRuntimeActive(t, manager, keyOne, "cmd-0001")
	waitForRuntimeActive(t, manager, keyTwo, "cmd-0002")

	startupWait := manager.beginStartupWait(runtimeKey(keyOne, "cmd-pending"))
	outputWait, _ := manager.subscribeOutputWaiter(runtimeKey(keyTwo, "cmd-0002"))

	if err := manager.StopAllProjects(); err != nil {
		t.Fatalf("StopAllProjects() error = %v", err)
	}

	select {
	case <-startupWait:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("startup waiter was not released by StopAllProjects()")
	}

	select {
	case <-outputWait:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("output waiter was not signaled by StopAllProjects()")
	}

	for remaining := 2; remaining > 0; remaining-- {
		select {
		case <-exitCh:
		case <-failCh:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for command termination after StopAllProjects()")
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, errOne := manager.lookupActive(keyOne, "cmd-0001")
		_, errTwo := manager.lookupActive(keyTwo, "cmd-0002")
		if errOne != nil && errTwo != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("commands remained active after StopAllProjects()")
		}
		time.Sleep(25 * time.Millisecond)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.startupWaiters) != 0 {
		t.Fatalf("startupWaiters retained entries after StopAllProjects(): %#v", manager.startupWaiters)
	}
	if len(manager.outputWaiters) != 0 {
		t.Fatalf("outputWaiters retained entries after StopAllProjects(): %#v", manager.outputWaiters)
	}
}

func TestRuntimeManagerIgnoresStaleExitFileForNewCommand(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewRuntimeManager(root, []string{os.Args[0], "-test.run=TestRuntimeHelperProcess", "--"}, NewMountManager(root, true, nil))
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	commandLayout, err := layout.ResolveCommandLayout("cmd-0001")
	if err != nil {
		t.Fatalf("ResolveCommandLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.MkdirAll(commandLayout.Dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(command dir) error = %v", err)
	}
	if err := os.WriteFile(commandLayout.ExitCodePath, []byte("17"), 0o644); err != nil {
		t.Fatalf("WriteFile(exit code) error = %v", err)
	}

	exitCh := make(chan int, 1)
	failCh := make(chan string, 1)
	manager.Launch(context.Background(), LaunchRequest{
		ProjectKey: key,
		Command: proto.CommandSnapshot{
			CommandID: "cmd-0001",
			ArgvTail:  []string{"echo-stdin-line"},
		},
	}, func(exitCode int) {
		exitCh <- exitCode
	}, func(reason string) {
		failCh <- reason
	})

	waitForRuntimeActive(t, manager, key, "cmd-0001")

	output, err := manager.AttachStdio(key, proto.AttachStdioRequest{CommandID: "cmd-0001"})
	if err != nil {
		t.Fatalf("AttachStdio() error = %v", err)
	}
	if output.Complete {
		t.Fatalf("AttachStdio().Complete = true with stale exit file; output=%#v", output)
	}

	if err := manager.WriteStdin(key, "cmd-0001", []byte("hello-runtime\n")); err != nil {
		t.Fatalf("WriteStdin() error = %v", err)
	}

	select {
	case reason := <-failCh:
		t.Fatalf("launch failure after stdin = %s", reason)
	case exitCode := <-exitCh:
		if exitCode != 0 {
			t.Fatalf("exit code = %d", exitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for command exit")
	}
}

func TestRuntimeManagerFailsLaunchWhenMountIsUnavailable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	mounts := NewMountManager(root, true, nil)
	mounts.sessionStarter = func(proto.ProjectKey, ProjectLayout) (projectMountSession, error) {
		return &fakeMountSession{
			workdir: layout.MountDir,
			validate: func() error {
				return errors.New("mount not ready")
			},
		}, nil
	}
	manager := NewRuntimeManager(root, []string{os.Args[0], "-test.run=TestRuntimeHelperProcess", "--"}, mounts)

	exitCh := make(chan int, 1)
	failCh := make(chan string, 1)
	manager.Launch(context.Background(), LaunchRequest{
		ProjectKey: key,
		Command: proto.CommandSnapshot{
			CommandID: "cmd-mount-fail",
			ArgvTail:  []string{"echo-stdin-line"},
		},
	}, func(exitCode int) {
		exitCh <- exitCode
	}, func(reason string) {
		failCh <- reason
	})

	select {
	case reason := <-failCh:
		if !strings.Contains(reason, "mount not ready") {
			t.Fatalf("launch failure = %q, want mount error", reason)
		}
	case exitCode := <-exitCh:
		t.Fatalf("exit code = %d, want launch failure", exitCode)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for launch failure")
	}
}

func TestRuntimeManagerPreservesEntrypointArgvBoundaries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	entrypoint := []string{
		os.Args[0],
		"-test.run=TestRuntimeHelperProcess",
		"--",
		"print-argv-json",
		"entry arg with spaces",
		`quoted"value`,
	}
	manager := NewRuntimeManager(root, entrypoint, NewMountManager(root, true, nil))
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	exitCh := make(chan int, 1)
	failCh := make(chan string, 1)
	manager.Launch(context.Background(), LaunchRequest{
		ProjectKey: key,
		Command: proto.CommandSnapshot{
			CommandID: "cmd-argv",
			ArgvTail: []string{
				"tail arg with spaces",
				`tail"quote`,
			},
		},
	}, func(exitCode int) {
		exitCh <- exitCode
	}, func(reason string) {
		failCh <- reason
	})

	select {
	case reason := <-failCh:
		t.Fatalf("launch failure = %s", reason)
	case exitCode := <-exitCh:
		if exitCode != 0 {
			t.Fatalf("exit code = %d", exitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for command exit")
	}

	output, err := manager.AttachStdio(key, proto.AttachStdioRequest{CommandID: "cmd-argv"})
	if err != nil {
		t.Fatalf("AttachStdio() error = %v", err)
	}

	var argv []string
	if err := json.Unmarshal(output.Stdout, &argv); err != nil {
		t.Fatalf("stdout json = %q, unmarshal error = %v", string(output.Stdout), err)
	}
	want := []string{
		"entry arg with spaces",
		`quoted"value`,
		"tail arg with spaces",
		`tail"quote`,
	}
	if len(argv) != len(want) {
		t.Fatalf("argv = %#v, want %#v", argv, want)
	}
	for idx := range want {
		if argv[idx] != want[idx] {
			t.Fatalf("argv[%d] = %q, want %q (full argv %#v)", idx, argv[idx], want[idx], argv)
		}
	}
}

func TestCommandEnvironmentAddsInteractiveTerminalDefaults(t *testing.T) {
	t.Parallel()

	env := commandEnvironment([]string{"PATH=/usr/bin"}, proto.TTYSpec{
		Interactive: true,
		Columns:     132,
		Rows:        40,
	})

	expected := map[string]string{
		"PATH":      "/usr/bin",
		"TERM":      "xterm-256color",
		"COLORTERM": "truecolor",
		"LINES":     "40",
		"COLUMNS":   "132",
	}
	for key, want := range expected {
		prefix := key + "="
		found := ""
		for _, item := range env {
			if strings.HasPrefix(item, prefix) {
				found = strings.TrimPrefix(item, prefix)
				break
			}
		}
		if found != want {
			t.Fatalf("%s = %q, want %q; env=%#v", key, found, want, env)
		}
	}
}

func TestCommandEnvironmentLeavesNonInteractiveEnvUnchanged(t *testing.T) {
	t.Parallel()

	base := []string{"TERM=screen", "PATH=/usr/bin"}
	env := commandEnvironment(base, proto.TTYSpec{})
	if len(env) != len(base) {
		t.Fatalf("len(env) = %d, want %d", len(env), len(base))
	}
	for idx := range base {
		if env[idx] != base[idx] {
			t.Fatalf("env[%d] = %q, want %q", idx, env[idx], base[idx])
		}
	}
}

func runtimeHelperModeArg() (string, []string, bool) {
	for idx := 0; idx < len(os.Args); idx++ {
		if os.Args[idx] == "--" {
			if idx+1 >= len(os.Args) {
				return "", nil, false
			}
			return os.Args[idx+1], os.Args[idx+2:], true
		}
	}
	return "", nil, false
}
