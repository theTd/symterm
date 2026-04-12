package control

import (
	"testing"

	"symterm/internal/proto"
)

func TestUploadTrackerLifecycleAndCleanup(t *testing.T) {
	t.Parallel()

	tracker := newUploadTracker()
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	otherKey := proto.ProjectKey{Username: "alice", ProjectID: "other"}

	tracker.Begin(key, "file-1", "docs/a.txt")
	if path := tracker.Commit(key, "file-1"); path != "docs/a.txt" {
		t.Fatalf("Commit() path = %q, want docs/a.txt", path)
	}
	if path := tracker.Commit(key, "file-1"); path != "" {
		t.Fatalf("Commit() after removal = %q, want empty", path)
	}

	tracker.Begin(key, "file-2", "docs/b.txt")
	tracker.Begin(otherKey, "file-3", "docs/c.txt")
	tracker.Abort(key, "file-2")
	if path := tracker.Commit(key, "file-2"); path != "" {
		t.Fatalf("Commit() after Abort() = %q, want empty", path)
	}

	tracker.Begin(key, "file-4", "docs/d.txt")
	tracker.CleanupProject(key)
	if path := tracker.Commit(key, "file-4"); path != "" {
		t.Fatalf("Commit() after CleanupProject() = %q, want empty", path)
	}
	if path := tracker.Commit(otherKey, "file-3"); path != "docs/c.txt" {
		t.Fatalf("Commit() for other project = %q, want docs/c.txt", path)
	}
}
