package control

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

func ensureProjectAsync(
	coordinator *ProjectCoordinator,
	registry *SessionRegistry,
	filesystem FilesystemBackend,
	clientID string,
	projectID string,
	now Clock,
) <-chan error {
	done := make(chan error, 1)
	go func() {
		var reportedErr error
		_, err := coordinator.EnsureProject(registry, filesystem, clientID, projectID, now, func(action string, err error) {
			if err != nil && reportedErr == nil {
				reportedErr = fmt.Errorf("%s error = %w", action, err)
			}
		})
		if reportedErr != nil {
			done <- reportedErr
			return
		}
		done <- err
	}()
	return done
}

func TestProjectCoordinatorEnsureProjectAssignsAuthoritativeSources(t *testing.T) {
	t.Parallel()

	now := fixedClock()

	t.Run("workspace root", func(t *testing.T) {
		registry := newSessionRegistry()
		hello := registry.Hello("alice", proto.HelloRequest{
			ProjectID:          "demo",
			LocalWorkspaceRoot: "/workspace/demo",
			SessionKind:        proto.SessionKindAuthority,
			WorkspaceDigest:    testWorkspaceDigest(1, "root-a"),
		})
		coordinator := newProjectCoordinator(nil)

		var root string
		snapshot, err := coordinator.EnsureProject(registry, filesystemBackendStub{
			setAuthoritativeRoot: func(key proto.ProjectKey, receivedRoot string) error {
				if key != (proto.ProjectKey{Username: "alice", ProjectID: "demo"}) {
					t.Fatalf("SetAuthoritativeRoot() key = %#v", key)
				}
				root = receivedRoot
				return nil
			},
		}, hello.ClientID, "demo", now, func(action string, err error) {
			if err != nil {
				t.Fatalf("%s error = %v", action, err)
			}
		})
		if err != nil {
			t.Fatalf("EnsureProject() error = %v", err)
		}
		if snapshot.Role != proto.RoleOwner {
			t.Fatalf("EnsureProject() role = %q, want owner", snapshot.Role)
		}
		if root != "/workspace/demo" {
			t.Fatalf("SetAuthoritativeRoot() root = %q, want /workspace/demo", root)
		}
	})

	t.Run("owner file client", func(t *testing.T) {
		registry := newSessionRegistry()
		hello := registry.Hello("alice", proto.HelloRequest{
			ProjectID:       "demo",
			SessionKind:     proto.SessionKindAuthority,
			WorkspaceDigest: testWorkspaceDigest(1, "root-a"),
		})
		client := &fakeOwnerFileClient{}
		if _, err := registry.RegisterOwnerFileClient(hello.ClientID, client); err != nil {
			t.Fatalf("RegisterOwnerFileClient() error = %v", err)
		}
		coordinator := newProjectCoordinator(nil)

		var matched bool
		_, err := coordinator.EnsureProject(registry, filesystemBackendStub{
			setAuthoritativeClient: func(key proto.ProjectKey, receivedClient ownerfs.Client) error {
				ownerClient, ok := receivedClient.(*fakeOwnerFileClient)
				matched = ok && ownerClient == client && key == (proto.ProjectKey{Username: "alice", ProjectID: "demo"})
				return nil
			},
		}, hello.ClientID, "demo", now, func(action string, err error) {
			if err != nil {
				t.Fatalf("%s error = %v", action, err)
			}
		})
		if err != nil {
			t.Fatalf("EnsureProject() error = %v", err)
		}
		if !matched {
			t.Fatal("SetAuthoritativeClient() did not receive registered owner file client")
		}
	})

	t.Run("ssh workspace root prefers owner file client", func(t *testing.T) {
		registry := newSessionRegistry()
		hello := registry.Hello("alice", proto.HelloRequest{
			ProjectID:          "demo",
			TransportKind:      string(TransportKindSSH),
			LocalWorkspaceRoot: `C:\Users\cui\standalone\symterm`,
			SessionKind:        proto.SessionKindAuthority,
			WorkspaceDigest:    testWorkspaceDigest(1, "root-a"),
		})
		client := &fakeOwnerFileClient{}
		if _, err := registry.RegisterOwnerFileClient(hello.ClientID, client); err != nil {
			t.Fatalf("RegisterOwnerFileClient() error = %v", err)
		}
		coordinator := newProjectCoordinator(nil)

		var rootCalls int
		var matched bool
		_, err := coordinator.EnsureProject(registry, filesystemBackendStub{
			setAuthoritativeRoot: func(proto.ProjectKey, string) error {
				rootCalls++
				return nil
			},
			setAuthoritativeClient: func(key proto.ProjectKey, receivedClient ownerfs.Client) error {
				ownerClient, ok := receivedClient.(*fakeOwnerFileClient)
				matched = ok && ownerClient == client && key == (proto.ProjectKey{Username: "alice", ProjectID: "demo"})
				return nil
			},
		}, hello.ClientID, "demo", now, func(action string, err error) {
			if err != nil {
				t.Fatalf("%s error = %v", action, err)
			}
		})
		if err != nil {
			t.Fatalf("EnsureProject() error = %v", err)
		}
		if rootCalls != 0 {
			t.Fatalf("SetAuthoritativeRoot() call count = %d, want 0", rootCalls)
		}
		if !matched {
			t.Fatal("SetAuthoritativeClient() did not receive registered owner file client for ssh session")
		}
	})
}

func TestProjectCoordinatorBootstrapCanAccessInstanceForKey(t *testing.T) {
	t.Parallel()

	now := fixedClock()
	projectKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	registry := newSessionRegistry()
	hello := registry.Hello("alice", proto.HelloRequest{
		ProjectID:       projectKey.ProjectID,
		SessionKind:     proto.SessionKindAuthority,
		WorkspaceDigest: testWorkspaceDigest(1, "root-a"),
	})

	var coordinator *ProjectCoordinator
	coordinator = newProjectCoordinator(bootstrapperStub{
		prepareProject: func(receivedKey proto.ProjectKey) error {
			if receivedKey != projectKey {
				return fmt.Errorf("PrepareProject() key = %#v, want %#v", receivedKey, projectKey)
			}
			instance, err := coordinator.InstanceForKey(receivedKey)
			if err != nil {
				return fmt.Errorf("InstanceForKey() during bootstrap error = %v", err)
			}
			if instance == nil {
				return fmt.Errorf("InstanceForKey() returned nil instance during bootstrap")
			}
			return nil
		},
	})

	done := ensureProjectAsync(coordinator, registry, filesystemBackendStub{}, hello.ClientID, projectKey.ProjectID, now)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureProject() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("EnsureProject() blocked while bootstrap accessed InstanceForKey()")
	}
}

func TestProjectCoordinatorConcurrentEnsureProjectWaitsForBootstrap(t *testing.T) {
	t.Parallel()

	now := fixedClock()
	registry := newSessionRegistry()
	owner := registry.Hello("alice", proto.HelloRequest{
		ProjectID:       "demo",
		SessionKind:     proto.SessionKindAuthority,
		WorkspaceDigest: testWorkspaceDigest(1, "root-a"),
	})
	follower := registry.Hello("alice", proto.HelloRequest{
		ProjectID:       "demo",
		WorkspaceDigest: testWorkspaceDigest(1, "root-b"),
	})

	bootstrapStarted := make(chan struct{})
	releaseBootstrap := make(chan struct{})
	var bootstrapCalls int32
	coordinator := newProjectCoordinator(bootstrapperStub{
		prepareProject: func(proto.ProjectKey) error {
			if atomic.AddInt32(&bootstrapCalls, 1) != 1 {
				return fmt.Errorf("PrepareProject() ran more than once for the same project")
			}
			close(bootstrapStarted)
			<-releaseBootstrap
			return nil
		},
	})

	firstDone := ensureProjectAsync(coordinator, registry, filesystemBackendStub{}, owner.ClientID, "demo", now)

	select {
	case <-bootstrapStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PrepareProject() did not start")
	}

	secondDone := ensureProjectAsync(coordinator, registry, filesystemBackendStub{}, follower.ClientID, "demo", now)

	select {
	case err := <-secondDone:
		t.Fatalf("second EnsureProject() returned before bootstrap completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseBootstrap)

	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first EnsureProject() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first EnsureProject() did not complete after bootstrap release")
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second EnsureProject() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second EnsureProject() did not complete after bootstrap release")
	}
	if atomic.LoadInt32(&bootstrapCalls) != 1 {
		t.Fatalf("PrepareProject() call count = %d, want 1", bootstrapCalls)
	}
}

func TestProjectCoordinatorReleaseClientCleansProjectState(t *testing.T) {
	t.Parallel()

	now := fixedClock()
	projectKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	registry := newSessionRegistry()
	hello := registry.Hello("alice", proto.HelloRequest{
		ProjectID:          "demo",
		LocalWorkspaceRoot: "/workspace/demo",
		SessionKind:        proto.SessionKindAuthority,
		WorkspaceDigest:    testWorkspaceDigest(1, "root-a"),
	})
	coordinator := newProjectCoordinator(nil)

	snapshot, err := coordinator.EnsureProject(registry, filesystemBackendStub{}, hello.ClientID, "demo", now, func(action string, err error) {
		if err != nil {
			t.Fatalf("%s error = %v", action, err)
		}
	})
	if err != nil {
		t.Fatalf("EnsureProject() error = %v", err)
	}
	if _, err := coordinator.CompleteInitialSync(registry, hello.ClientID, snapshot.SyncEpoch, now); err != nil {
		t.Fatalf("CompleteInitialSync() error = %v", err)
	}

	uploads := newUploadTracker()
	uploads.Begin(projectKey, "file-1", "docs/a.txt")

	stopped := make(chan proto.ProjectKey, 1)
	commands, err := newCommandController(commandBackendStub{
		stopProject: func(key proto.ProjectKey) error {
			stopped <- key
			return nil
		},
	}, now)
	if err != nil {
		t.Fatalf("newCommandController() error = %v", err)
	}
	if err := commands.Start(projectKey, proto.CommandSnapshot{CommandID: "cmd-1", StartedAt: now()}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	_, _, commandWatch, unsubscribeCommand, err := commands.Subscribe("cmd-1", 0)
	if err != nil {
		t.Fatalf("Subscribe(command) error = %v", err)
	}
	defer unsubscribeCommand()

	invalidates, err := newInvalidateHub(now)
	if err != nil {
		t.Fatalf("newInvalidateHub() error = %v", err)
	}
	_, _, invalidateWatch, _, err := invalidates.Subscribe(projectKey, 0)
	if err != nil {
		t.Fatalf("Subscribe(invalidate) error = %v", err)
	}

	var cleared []proto.ProjectKey
	err = coordinator.ReleaseClient(registry, filesystemBackendStub{
		clearAuthoritativeRoot: func(key proto.ProjectKey) error {
			cleared = append(cleared, key)
			return nil
		},
	}, commands, uploads, invalidates, hello.ClientID, now, func(action string, cleanupErr error) {
		if cleanupErr != nil {
			t.Fatalf("%s error = %v", action, cleanupErr)
		}
	})
	if err != nil {
		t.Fatalf("ReleaseClient() error = %v", err)
	}

	select {
	case key := <-stopped:
		if key != projectKey {
			t.Fatalf("StopProject() key = %#v, want %#v", key, projectKey)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("StopProject() was not invoked")
	}
	if len(cleared) != 1 || cleared[0] != projectKey {
		t.Fatalf("ClearAuthoritativeRoot() = %#v, want [%#v]", cleared, projectKey)
	}
	if _, err := coordinator.InstanceForKey(projectKey); err == nil {
		t.Fatal("InstanceForKey() succeeded after owner release cleanup")
	}
	if path := uploads.Commit(projectKey, "file-1"); path != "" {
		t.Fatalf("UploadTracker cleanup retained %q", path)
	}

	select {
	case _, ok := <-commandWatch:
		if ok {
			t.Fatal("command watcher remained open after cleanup")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("command watcher was not closed")
	}
	select {
	case _, ok := <-invalidateWatch:
		if ok {
			t.Fatal("invalidate watcher remained open after cleanup")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("invalidate watcher was not closed")
	}
}

func TestProjectCoordinatorReleaseInteractiveClientKeepsAuthorityProjectAlive(t *testing.T) {
	t.Parallel()

	now := fixedClock()
	projectKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	registry := newSessionRegistry()
	authority := registry.Hello("alice", proto.HelloRequest{
		ProjectID:           "demo",
		WorkspaceInstanceID: "wsi-demo",
		SessionKind:         proto.SessionKindAuthority,
		WorkspaceDigest:     testWorkspaceDigest(1, "root-a"),
	})
	interactive := registry.Hello("alice", proto.HelloRequest{
		ProjectID:           "demo",
		WorkspaceInstanceID: "wsi-demo",
		SessionKind:         proto.SessionKindInteractive,
		WorkspaceDigest:     testWorkspaceDigest(1, "root-a"),
	})

	coordinator := newProjectCoordinator(nil)
	snapshot, err := coordinator.EnsureProject(registry, filesystemBackendStub{}, authority.ClientID, "demo", now, func(action string, err error) {
		if err != nil {
			t.Fatalf("%s error = %v", action, err)
		}
	})
	if err != nil {
		t.Fatalf("EnsureProject(authority) error = %v", err)
	}
	if _, err := coordinator.CompleteInitialSync(registry, authority.ClientID, snapshot.SyncEpoch, now); err != nil {
		t.Fatalf("CompleteInitialSync() error = %v", err)
	}
	if _, err := coordinator.EnsureProject(registry, filesystemBackendStub{}, interactive.ClientID, "demo", now, func(action string, err error) {
		if err != nil {
			t.Fatalf("%s error = %v", action, err)
		}
	}); err != nil {
		t.Fatalf("EnsureProject(interactive) error = %v", err)
	}

	stopped := make(chan proto.ProjectKey, 1)
	commands, err := newCommandController(commandBackendStub{
		stopProject: func(key proto.ProjectKey) error {
			stopped <- key
			return nil
		},
	}, now)
	if err != nil {
		t.Fatalf("newCommandController() error = %v", err)
	}

	var cleared []proto.ProjectKey
	if err := coordinator.ReleaseClient(registry, filesystemBackendStub{
		clearAuthoritativeRoot: func(key proto.ProjectKey) error {
			cleared = append(cleared, key)
			return nil
		},
		setAuthorityState: func(key proto.ProjectKey, state proto.AuthorityState) error {
			t.Fatalf("SetAuthorityState() key=%#v state=%q, want no authority change", key, state)
			return nil
		},
	}, commands, newUploadTracker(), nil, interactive.ClientID, now, func(action string, cleanupErr error) {
		if cleanupErr != nil {
			t.Fatalf("%s error = %v", action, cleanupErr)
		}
	}); err != nil {
		t.Fatalf("ReleaseClient(interactive) error = %v", err)
	}

	instance, err := coordinator.InstanceForKey(projectKey)
	if err != nil {
		t.Fatalf("InstanceForKey() error = %v", err)
	}
	if state := instance.CurrentState(); state != proto.ProjectStateActive {
		t.Fatalf("CurrentState() = %q, want active", state)
	}
	authoritySnapshot, err := instance.Snapshot(authority.ClientID)
	if err != nil {
		t.Fatalf("Snapshot(authority) error = %v", err)
	}
	if authoritySnapshot.AuthorityState != proto.AuthorityStateStable {
		t.Fatalf("AuthorityState = %q, want stable", authoritySnapshot.AuthorityState)
	}
	if len(cleared) != 0 {
		t.Fatalf("ClearAuthoritativeRoot() = %#v, want no cleanup", cleared)
	}

	select {
	case key := <-stopped:
		t.Fatalf("StopProject() key = %#v, want project to stay alive", key)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProjectCoordinatorHandleOwnerFileDisconnectMarksOwnerFileProjectsRebinding(t *testing.T) {
	t.Parallel()

	now := fixedClock()
	projectKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	registry := newSessionRegistry()
	hello := registry.Hello("alice", proto.HelloRequest{
		ProjectID:       "demo",
		SessionKind:     proto.SessionKindAuthority,
		WorkspaceDigest: testWorkspaceDigest(1, "root-a"),
	})
	client := &fakeOwnerFileClient{}
	if _, err := registry.RegisterOwnerFileClient(hello.ClientID, client); err != nil {
		t.Fatalf("RegisterOwnerFileClient() error = %v", err)
	}

	coordinator := newProjectCoordinator(nil)
	snapshot, err := coordinator.EnsureProject(registry, filesystemBackendStub{}, hello.ClientID, "demo", now, func(action string, err error) {
		if err != nil {
			t.Fatalf("%s error = %v", action, err)
		}
	})
	if err != nil {
		t.Fatalf("EnsureProject() error = %v", err)
	}
	if _, err := coordinator.CompleteInitialSync(registry, hello.ClientID, snapshot.SyncEpoch, now); err != nil {
		t.Fatalf("CompleteInitialSync() error = %v", err)
	}

	stopped := make(chan proto.ProjectKey, 1)
	commands, err := newCommandController(commandBackendStub{
		stopProject: func(key proto.ProjectKey) error {
			stopped <- key
			return nil
		},
	}, now)
	if err != nil {
		t.Fatalf("newCommandController() error = %v", err)
	}

	var states []proto.AuthorityState
	coordinator.HandleOwnerFileDisconnect(registry, filesystemBackendStub{
		setAuthorityState: func(key proto.ProjectKey, state proto.AuthorityState) error {
			if key != projectKey {
				t.Fatalf("SetAuthorityState() key = %#v, want %#v", key, projectKey)
			}
			states = append(states, state)
			return nil
		},
	}, commands, hello.ClientID, client, now, func(action string, cleanupErr error) {
		if cleanupErr != nil {
			t.Fatalf("%s error = %v", action, cleanupErr)
		}
	})

	instance, err := coordinator.InstanceForKey(projectKey)
	if err != nil {
		t.Fatalf("InstanceForKey() error = %v", err)
	}
	if state := instance.CurrentState(); state != proto.ProjectStateActive {
		t.Fatalf("CurrentState() = %q, want active", state)
	}
	snapshot, err = instance.Snapshot(hello.ClientID)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.AuthorityState != proto.AuthorityStateRebinding {
		t.Fatalf("AuthorityState = %q, want rebinding", snapshot.AuthorityState)
	}
	if len(states) != 1 || states[0] != proto.AuthorityStateRebinding {
		t.Fatalf("SetAuthorityState() = %#v, want [rebinding]", states)
	}

	select {
	case key := <-stopped:
		t.Fatalf("StopProject() key = %#v, want no stop during rebinding", key)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestProjectCoordinatorHandleOwnerFileDisconnectMarksSSHWorkspaceRootProjectsRebinding(t *testing.T) {
	t.Parallel()

	now := fixedClock()
	projectKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	registry := newSessionRegistry()
	hello := registry.Hello("alice", proto.HelloRequest{
		ProjectID:          "demo",
		TransportKind:      string(TransportKindSSH),
		LocalWorkspaceRoot: `C:\Users\cui\standalone\symterm`,
		SessionKind:        proto.SessionKindAuthority,
		WorkspaceDigest:    testWorkspaceDigest(1, "root-a"),
	})
	client := &fakeOwnerFileClient{}
	if _, err := registry.RegisterOwnerFileClient(hello.ClientID, client); err != nil {
		t.Fatalf("RegisterOwnerFileClient() error = %v", err)
	}

	coordinator := newProjectCoordinator(nil)
	snapshot, err := coordinator.EnsureProject(registry, filesystemBackendStub{}, hello.ClientID, "demo", now, func(action string, err error) {
		if err != nil {
			t.Fatalf("%s error = %v", action, err)
		}
	})
	if err != nil {
		t.Fatalf("EnsureProject() error = %v", err)
	}
	if _, err := coordinator.CompleteInitialSync(registry, hello.ClientID, snapshot.SyncEpoch, now); err != nil {
		t.Fatalf("CompleteInitialSync() error = %v", err)
	}

	stopped := make(chan proto.ProjectKey, 1)
	commands, err := newCommandController(commandBackendStub{
		stopProject: func(key proto.ProjectKey) error {
			stopped <- key
			return nil
		},
	}, now)
	if err != nil {
		t.Fatalf("newCommandController() error = %v", err)
	}

	var states []proto.AuthorityState
	coordinator.HandleOwnerFileDisconnect(registry, filesystemBackendStub{
		setAuthorityState: func(key proto.ProjectKey, state proto.AuthorityState) error {
			if key != projectKey {
				t.Fatalf("SetAuthorityState() key = %#v, want %#v", key, projectKey)
			}
			states = append(states, state)
			return nil
		},
	}, commands, hello.ClientID, client, now, func(action string, cleanupErr error) {
		if cleanupErr != nil {
			t.Fatalf("%s error = %v", action, cleanupErr)
		}
	})

	instance, err := coordinator.InstanceForKey(projectKey)
	if err != nil {
		t.Fatalf("InstanceForKey() error = %v", err)
	}
	if state := instance.CurrentState(); state != proto.ProjectStateActive {
		t.Fatalf("CurrentState() = %q, want active", state)
	}
	snapshot, err = instance.Snapshot(hello.ClientID)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.AuthorityState != proto.AuthorityStateRebinding {
		t.Fatalf("AuthorityState = %q, want rebinding", snapshot.AuthorityState)
	}
	if len(states) != 1 || states[0] != proto.AuthorityStateRebinding {
		t.Fatalf("SetAuthorityState() = %#v, want [rebinding]", states)
	}

	select {
	case key := <-stopped:
		t.Fatalf("StopProject() key = %#v, want no stop during rebinding", key)
	case <-time.After(200 * time.Millisecond):
	}
}
