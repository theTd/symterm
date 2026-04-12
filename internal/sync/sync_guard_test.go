package sync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestSyncGuardHashPathsReusesLocalHashCache(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	snapshot, err := ScanLocalWorkspace(root, nil, false)
	if err != nil {
		t.Fatalf("ScanLocalWorkspace() error = %v", err)
	}

	guard := &SyncGuard{
		root:           root,
		watchHealthy:   true,
		quiescentSince: time.Now().Add(-time.Second).UTC(),
		localHashCache: make(map[string]LocalWorkspaceFile),
	}

	original := hashLocalFileFn
	t.Cleanup(func() {
		hashLocalFileFn = original
	})

	var calls int
	hashLocalFileFn = func(path string) (string, error) {
		calls++
		return original(path)
	}

	first, err := guard.HashPaths(snapshot, []string{"note.txt"})
	if err != nil {
		t.Fatalf("HashPaths(first) error = %v", err)
	}
	callsAfterFirst := calls
	second, err := guard.HashPaths(snapshot, []string{"note.txt"})
	if err != nil {
		t.Fatalf("HashPaths(second) error = %v", err)
	}

	if callsAfterFirst == 0 {
		t.Fatal("first HashPaths() did not hash the requested path")
	}
	if calls != callsAfterFirst {
		t.Fatalf("hashLocalFileFn calls after cache reuse = %d, want %d", calls, callsAfterFirst)
	}
	if first.HashedFiles["note.txt"].Entry.ContentHash == "" {
		t.Fatal("first hash result is empty")
	}
	if second.HashedFiles["note.txt"].Entry.ContentHash != first.HashedFiles["note.txt"].Entry.ContentHash {
		t.Fatalf("cached hash = %q, want %q", second.HashedFiles["note.txt"].Entry.ContentHash, first.HashedFiles["note.txt"].Entry.ContentHash)
	}
}

func TestSyncGuardValidateRejectsDirtyOrDegradedState(t *testing.T) {
	t.Parallel()

	t.Run("dirty-epoch", func(t *testing.T) {
		guard := &SyncGuard{
			watchHealthy:   true,
			quiescentSince: time.Now().Add(-time.Second).UTC(),
		}
		attempt := guard.AttemptStart()
		guard.markDirty()

		err := guard.Validate(attempt)
		if err == nil {
			t.Fatal("Validate() succeeded after dirty epoch changed")
		}
		var protoErr *proto.Error
		if !errors.As(err, &protoErr) || protoErr.Code != proto.ErrSyncRescanMismatch {
			t.Fatalf("Validate() error = %v, want sync-rescan-mismatch", err)
		}
	})

	t.Run("watcher-degraded", func(t *testing.T) {
		guard := &SyncGuard{
			watchHealthy:   false,
			failureReason:  "watch failed",
			quiescentSince: time.Now().Add(-time.Second).UTC(),
		}
		err := guard.Validate(guard.AttemptStart())
		if err == nil {
			t.Fatal("Validate() succeeded with degraded watcher")
		}
		var protoErr *proto.Error
		if !errors.As(err, &protoErr) || protoErr.Code != proto.ErrSyncRescanMismatch {
			t.Fatalf("Validate() error = %v, want sync-rescan-mismatch", err)
		}
	})
}
