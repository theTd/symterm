//go:build linux

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"symterm/internal/proto"
)

type dummyProjectFS struct{}

func (d *dummyProjectFS) FsRead(ctx context.Context, pk proto.ProjectKey, op proto.FsOperation, req proto.FsRequest) (proto.FsReply, error) {
	return proto.FsReply{}, nil
}
func (d *dummyProjectFS) FsMutation(ctx context.Context, pk proto.ProjectKey, req proto.FsMutationRequest) (proto.FsReply, error) {
	return proto.FsReply{}, nil
}
func (d *dummyProjectFS) WatchInvalidate(projectKey proto.ProjectKey, sinceCursor uint64) (ProjectInvalidateWatch, error) {
	ch := make(chan struct{})
	return ProjectInvalidateWatch{Notify: ch, Close: func() { close(ch) }}, nil
}

func TestFuseMountSessionLifecycle(t *testing.T) {
	mountDir, err := os.MkdirTemp("", "fuse-mount-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(mountDir)

	layout := ProjectLayout{MountDir: mountDir}
	fs := &dummyProjectFS{}
	key := proto.ProjectKey{Username: "test", ProjectID: "test"}

	session, err := startFuseMount(key, layout, fs)
	if err != nil {
		t.Fatalf("startFuseMount failed: %v", err)
	}

	if err := session.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	done := make(chan struct{})
	var stopErr error
	go func() {
		stopErr = session.Stop()
		close(done)
	}()
	select {
	case <-done:
		if stopErr != nil {
			t.Logf("Stop returned error: %v", stopErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop timed out")
	}
}

// TestFuseMountNoDStateOnSIGKILL verifies that killing a process with an
// active FUSE mount does not leave D-state processes.
func TestFuseMountNoDStateOnSIGKILL(t *testing.T) {
	if os.Getenv("BE_TEST_CHILD") == "1" {
		runChildFuseServer(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestFuseMountNoDStateOnSIGKILL", "-test.v")
	cmd.Env = append(os.Environ(), "BE_TEST_CHILD=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	childPid := cmd.Process.Pid
	t.Logf("Child PID: %d", childPid)

	// Wait for child to create mount
	time.Sleep(2 * time.Second)

	mountDir := fmt.Sprintf("/tmp/fuse-sigkill-test-%d", childPid)
	_, _ = os.Stat(mountDir)

	// SIGKILL the child
	t.Logf("Sending SIGKILL to %d", childPid)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		t.Log("Child exited")
	case <-time.After(5 * time.Second):
		t.Fatal("Child did not exit after SIGKILL")
	}

	checkCmd := exec.Command("sh", "-c", fmt.Sprintf("ps aux | awk '$8 ~ /D/ {print}' | grep %d || echo 'No D-state'", childPid))
	out, _ := checkCmd.CombinedOutput()
	t.Logf("D-state check: %s", string(out))
	if string(out) != "No D-state\n" {
		t.Fatalf("Child process entered D-state: %s", string(out))
	}
}

// TestFuseMountNoDStateWithActiveClient verifies that killing the FUSE server
// while a client is actively reading the mount does not cause D-state.
func TestFuseMountNoDStateWithActiveClient(t *testing.T) {
	if os.Getenv("BE_TEST_CHILD") == "1" {
		runChildFuseServerWithClient(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestFuseMountNoDStateWithActiveClient", "-test.v")
	cmd.Env = append(os.Environ(), "BE_TEST_CHILD=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	childPid := cmd.Process.Pid
	t.Logf("Child PID: %d", childPid)

	// Wait for child to create mount and spawn client
	time.Sleep(3 * time.Second)

	mountDir := fmt.Sprintf("/tmp/fuse-sigkill-test2-%d", childPid)

	// Start a client that continuously accesses the mount
	clientCmd := exec.Command("sh", "-c", fmt.Sprintf("while true; do ls %s >/dev/null 2>&1; done", mountDir))
	if err := clientCmd.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	clientPid := clientCmd.Process.Pid
	t.Logf("Client PID: %d", clientPid)

	// Let client run for a bit
	time.Sleep(1 * time.Second)

	// SIGKILL the child (FUSE server)
	t.Logf("Sending SIGKILL to server %d", childPid)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		t.Log("Server exited")
	case <-time.After(5 * time.Second):
		t.Fatal("Server did not exit after SIGKILL")
	}

	// Kill the client too
	_ = clientCmd.Process.Kill()
	_ = clientCmd.Wait()

	// Check for D-state on either process
	checkCmd := exec.Command("sh", "-c", fmt.Sprintf("ps aux | awk '$8 ~ /D/ {print}' | grep -E '%d|%d' || echo 'No D-state'", childPid, clientPid))
	out, _ := checkCmd.CombinedOutput()
	t.Logf("D-state check: %s", string(out))
	if string(out) != "No D-state\n" {
		t.Fatalf("Process entered D-state: %s", string(out))
	}
}

func runChildFuseServer(t *testing.T) {
	mountDir := fmt.Sprintf("/tmp/fuse-sigkill-test-%d", os.Getpid())
	_ = os.MkdirAll(mountDir, 0o755)

	layout := ProjectLayout{MountDir: mountDir}
	fs := &dummyProjectFS{}
	key := proto.ProjectKey{Username: "test", ProjectID: "test"}

	session, err := startFuseMount(key, layout, fs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mount failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("MOUNT_READY %s\n", session.WorkDir())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	<-sigCh

	_ = session.Stop()
}

func runChildFuseServerWithClient(t *testing.T) {
	mountDir := fmt.Sprintf("/tmp/fuse-sigkill-test2-%d", os.Getpid())
	_ = os.MkdirAll(mountDir, 0o755)

	layout := ProjectLayout{MountDir: mountDir}
	fs := &dummyProjectFS{}
	key := proto.ProjectKey{Username: "test", ProjectID: "test"}

	session, err := startFuseMount(key, layout, fs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mount failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("MOUNT_READY %s\n", session.WorkDir())

	// Wait for parent to kill us
	select {}
}
