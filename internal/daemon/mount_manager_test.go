package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"symterm/internal/proto"
)

type fakeMountSession struct {
	workdir  string
	failures chan error
	stopped  chan struct{}
	validate func() error
}

func (s *fakeMountSession) WorkDir() string {
	return s.workdir
}

func (s *fakeMountSession) Failure() <-chan error {
	return s.failures
}

func (s *fakeMountSession) Stop() error {
	if s.stopped != nil {
		select {
		case s.stopped <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *fakeMountSession) Validate() error {
	if s.validate != nil {
		return s.validate()
	}
	return nil
}

func TestMountManagerAllowsUnsafePassthroughOnUnsupportedPlatform(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewMountManager(root, true, nil)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	if err := manager.PrepareProject(key); err != nil {
		t.Fatalf("PrepareProject() error = %v", err)
	}
	workdir, err := manager.WorkDir(key)
	if err != nil {
		t.Fatalf("WorkDir() error = %v", err)
	}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if workdir != layout.MountDir {
		t.Fatalf("workdir = %q, want %q", workdir, layout.MountDir)
	}
}

func TestMountManagerRejectsUnsupportedPlatformWithoutUnsafeFlag(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "linux" {
		t.Skip("unsupported-platform guard is only meaningful off linux")
	}

	manager := NewMountManager(t.TempDir(), false, nil)
	err := manager.PrepareProject(proto.ProjectKey{Username: "alice", ProjectID: "demo"})
	if err == nil {
		t.Fatal("PrepareProject() succeeded without unsafe passthrough")
	}
}

func TestMountManagerPrepareProjectRefreshesPublishedWorkspace(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewMountManager(root, true, nil)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte("workspace"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount stale) error = %v", err)
	}

	if err := manager.PrepareProject(key); err != nil {
		t.Fatalf("PrepareProject() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(layout.MountDir, "note.txt"))
	if err != nil {
		t.Fatalf("ReadFile(mount note.txt) error = %v", err)
	}
	if string(data) != "workspace" {
		t.Fatalf("mount note.txt = %q", string(data))
	}
	if _, err := os.Stat(filepath.Join(layout.MountDir, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("mount stale.txt still exists after prepare, err = %v", err)
	}
}

func TestMountManagerForwardsSessionFailure(t *testing.T) {
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

	failures := make(chan error, 1)
	manager := NewMountManager(root, true, nil)
	manager.sessionStarter = func(proto.ProjectKey, ProjectLayout) (projectMountSession, error) {
		return &fakeMountSession{workdir: layout.MountDir, failures: failures}, nil
	}

	reported := make(chan error, 1)
	manager.SetSessionFailureHandler(func(receivedKey proto.ProjectKey, err error) {
		if receivedKey != key {
			t.Errorf("failure key = %#v, want %#v", receivedKey, key)
		}
		reported <- err
	})

	if err := manager.PrepareProject(key); err != nil {
		t.Fatalf("PrepareProject() error = %v", err)
	}

	failErr := errors.New("invalidate pump failed")
	failures <- failErr
	close(failures)

	select {
	case err := <-reported:
		if !errors.Is(err, failErr) {
			t.Fatalf("reported error = %v, want %v", err, failErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected mount session failure to be forwarded")
	}
}

func TestMountManagerStopProjectStopsSessionAndClearsCaches(t *testing.T) {
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

	stopped := make(chan struct{}, 1)
	manager := NewMountManager(root, true, nil)
	manager.sessionStarter = func(proto.ProjectKey, ProjectLayout) (projectMountSession, error) {
		return &fakeMountSession{workdir: layout.MountDir, stopped: stopped}, nil
	}

	if err := manager.PrepareProject(key); err != nil {
		t.Fatalf("PrepareProject() error = %v", err)
	}
	if err := manager.StopProject(key); err != nil {
		t.Fatalf("StopProject() error = %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected mount session Stop() to be invoked")
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, ok := manager.sessions[key.String()]; ok {
		t.Fatal("mount session was retained after StopProject()")
	}
	if _, ok := manager.workdirs[key.String()]; ok {
		t.Fatal("workdir cache was retained after StopProject()")
	}
	if _, ok := manager.sessionFailures[key.String()]; ok {
		t.Fatal("session failure watcher was retained after StopProject()")
	}
}

func TestMountManagerStopAllStopsSessionsAndClearsCaches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	keyOne := proto.ProjectKey{Username: "alice", ProjectID: "demo-one"}
	keyTwo := proto.ProjectKey{Username: "bob", ProjectID: "demo-two"}
	layouts := map[string]ProjectLayout{}
	for _, key := range []proto.ProjectKey{keyOne, keyTwo} {
		layout, err := ResolveProjectLayout(root, key)
		if err != nil {
			t.Fatalf("ResolveProjectLayout(%#v) error = %v", key, err)
		}
		if err := layout.EnsureDirectories(); err != nil {
			t.Fatalf("EnsureDirectories(%#v) error = %v", key, err)
		}
		layouts[key.String()] = layout
	}

	stoppedOne := make(chan struct{}, 1)
	stoppedTwo := make(chan struct{}, 1)
	manager := NewMountManager(root, true, nil)
	manager.sessionStarter = func(key proto.ProjectKey, _ ProjectLayout) (projectMountSession, error) {
		session := &fakeMountSession{workdir: layouts[key.String()].MountDir}
		switch key {
		case keyOne:
			session.stopped = stoppedOne
		case keyTwo:
			session.stopped = stoppedTwo
		default:
			t.Fatalf("unexpected project key: %#v", key)
		}
		return session, nil
	}

	if err := manager.PrepareProject(keyOne); err != nil {
		t.Fatalf("PrepareProject(keyOne) error = %v", err)
	}
	if err := manager.PrepareProject(keyTwo); err != nil {
		t.Fatalf("PrepareProject(keyTwo) error = %v", err)
	}

	if err := manager.StopAll(); err != nil {
		t.Fatalf("StopAll() error = %v", err)
	}

	for name, ch := range map[string]chan struct{}{"keyOne": stoppedOne, "keyTwo": stoppedTwo} {
		select {
		case <-ch:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("%s Stop() was not invoked", name)
		}
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.sessions) != 0 {
		t.Fatalf("sessions retained after StopAll(): %#v", manager.sessions)
	}
	if len(manager.workdirs) != 0 {
		t.Fatalf("workdirs retained after StopAll(): %#v", manager.workdirs)
	}
	if len(manager.sessionFailures) != 0 {
		t.Fatalf("sessionFailures retained after StopAll(): %#v", manager.sessionFailures)
	}
}

func TestMountManagerWorkDirRepairsInvalidCachedSession(t *testing.T) {
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

	var firstSessionValid = true
	starts := 0
	stopped := make(chan struct{}, 2)
	manager := NewMountManager(root, true, nil)
	manager.sessionStarter = func(proto.ProjectKey, ProjectLayout) (projectMountSession, error) {
		starts++
		validate := func() error { return nil }
		if starts == 1 {
			validate = func() error {
				if firstSessionValid {
					return nil
				}
				return errors.New("mount not ready")
			}
		}
		return &fakeMountSession{
			workdir:  layout.MountDir,
			stopped:  stopped,
			validate: validate,
		}, nil
	}

	if err := manager.PrepareProject(key); err != nil {
		t.Fatalf("PrepareProject() error = %v", err)
	}

	firstSessionValid = false
	workdir, err := manager.WorkDir(key)
	if err != nil {
		t.Fatalf("WorkDir() error = %v", err)
	}
	if workdir != layout.MountDir {
		t.Fatalf("workdir = %q, want %q", workdir, layout.MountDir)
	}
	if starts != 2 {
		t.Fatalf("sessionStarter calls = %d, want 2", starts)
	}

	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected invalid cached session to be stopped")
	}
}

func TestMountManagerWorkDirFailsWhenRemountValidationFails(t *testing.T) {
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

	failErr := errors.New("mount not ready")
	stopped := make(chan struct{}, 2)
	manager := NewMountManager(root, true, nil)
	manager.sessionStarter = func(proto.ProjectKey, ProjectLayout) (projectMountSession, error) {
		return &fakeMountSession{
			workdir: layout.MountDir,
			stopped: stopped,
			validate: func() error {
				return failErr
			},
		}, nil
	}

	err = manager.PrepareProject(key)
	if !errors.Is(err, failErr) {
		t.Fatalf("PrepareProject() error = %v, want %v", err, failErr)
	}

	manager.mu.Lock()
	_, sessionCached := manager.sessions[key.String()]
	_, workdirCached := manager.workdirs[key.String()]
	manager.mu.Unlock()
	if sessionCached || workdirCached {
		t.Fatal("failed mount validation should not leave cached session state")
	}

	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected failed mount session to be stopped")
	}
}
