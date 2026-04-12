package control

import (
	"errors"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestServiceCommitFileAppendsInvalidation(t *testing.T) {
	t.Parallel()

	expectedKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	service, clientID, projectKey, snapshot := newAuthorityProjectService(t, ServiceDependencies{
		Runtime: runtimeBackendStub{
			syncBackendStub: syncBackendStub{
				beginFile: func(key proto.ProjectKey, request proto.BeginFileRequest) (proto.BeginFileResponse, error) {
					if key != expectedKey {
						t.Fatalf("BeginFile() key = %#v, want %#v", key, expectedKey)
					}
					if request.Path != "docs/a.txt" {
						t.Fatalf("BeginFile() path = %q", request.Path)
					}
					return proto.BeginFileResponse{FileID: "file-1"}, nil
				},
				commitFile: func(key proto.ProjectKey, request proto.CommitFileRequest) error {
					if key != expectedKey {
						t.Fatalf("CommitFile() key = %#v, want %#v", key, expectedKey)
					}
					if request.FileID != "file-1" {
						t.Fatalf("CommitFile() file_id = %q", request.FileID)
					}
					return nil
				},
			},
		},
		Now: fixedClock(),
	})

	response, err := service.BeginFile(clientID, proto.BeginFileRequest{
		SyncEpoch: snapshot.SyncEpoch,
		Path:      "docs/a.txt",
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}
	if response.FileID != "file-1" {
		t.Fatalf("BeginFile().FileID = %q, want file-1", response.FileID)
	}

	if err := service.CommitFile(clientID, proto.CommitFileRequest{FileID: "file-1"}); err != nil {
		t.Fatalf("CommitFile() error = %v", err)
	}

	if path := service.uploads.Commit(projectKey, "file-1"); path != "" {
		t.Fatalf("UploadTracker retained %q after commit", path)
	}

	events, err := service.invalidates.Watch(projectKey, 0)
	if err != nil {
		t.Fatalf("Watch(invalidate) error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("invalidate events = %#v, want one event", events)
	}
	if len(events[0].Changes) != 2 || events[0].Changes[0].Kind != proto.InvalidateData || events[0].Changes[0].Path != "docs/a.txt" || events[0].Changes[1].Kind != proto.InvalidateDentry || events[0].Changes[1].Path != "docs" {
		t.Fatalf("invalidate changes = %#v", events[0].Changes)
	}
}

func TestServiceSyncFailureCleansProjectStateOnce(t *testing.T) {
	t.Parallel()

	backendErr := errors.New("commit failed")
	var cleared int
	var stopped int
	expectedKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	service, clientID, projectKey, snapshot := newAuthorityProjectService(t, ServiceDependencies{
		Runtime: runtimeBackendStub{
			syncBackendStub: syncBackendStub{
				beginFile: func(proto.ProjectKey, proto.BeginFileRequest) (proto.BeginFileResponse, error) {
					return proto.BeginFileResponse{FileID: "file-1"}, nil
				},
				commitFile: func(proto.ProjectKey, proto.CommitFileRequest) error {
					return backendErr
				},
			},
			filesystemBackendStub: filesystemBackendStub{
				clearAuthoritativeRoot: func(key proto.ProjectKey) error {
					if key != expectedKey {
						t.Fatalf("ClearAuthoritativeRoot() key = %#v, want %#v", key, expectedKey)
					}
					cleared++
					return nil
				},
			},
			commandBackendStub: commandBackendStub{
				stopProject: func(key proto.ProjectKey) error {
					if key != expectedKey {
						t.Fatalf("StopProject() key = %#v, want %#v", key, expectedKey)
					}
					stopped++
					return nil
				},
			},
		},
		Now: fixedClock(),
	})

	if _, err := service.BeginFile(clientID, proto.BeginFileRequest{
		SyncEpoch: snapshot.SyncEpoch,
		Path:      "docs/a.txt",
	}); err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}

	_, _, notify, unsubscribe, err := service.invalidates.Subscribe(projectKey, 0)
	if err != nil {
		t.Fatalf("Subscribe(invalidate) error = %v", err)
	}
	defer unsubscribe()

	err = service.CommitFile(clientID, proto.CommitFileRequest{FileID: "file-1"})
	if !errors.Is(err, backendErr) {
		t.Fatalf("CommitFile() error = %v, want %v", err, backendErr)
	}
	if cleared != 1 {
		t.Fatalf("ClearAuthoritativeRoot() call count = %d, want 1", cleared)
	}
	if stopped != 1 {
		t.Fatalf("StopProject() call count = %d, want 1", stopped)
	}
	instance, err := service.projects.InstanceForKey(projectKey)
	if err != nil {
		t.Fatalf("InstanceForKey() error = %v", err)
	}
	if state := instance.CurrentState(); state != proto.ProjectStateTerminated {
		t.Fatalf("CurrentState() = %q, want terminated", state)
	}
	if path := service.uploads.Commit(projectKey, "file-1"); path != "" {
		t.Fatalf("UploadTracker retained %q after sync failure cleanup", path)
	}

	select {
	case _, ok := <-notify:
		if ok {
			t.Fatal("invalidate watcher remained open after sync failure cleanup")
		}
	case <-time.After(time.Second):
		t.Fatal("invalidate watcher was not closed after sync failure cleanup")
	}
}
