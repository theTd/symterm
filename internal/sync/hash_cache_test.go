package sync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentHashCacheRoundTrip(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("cached"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	mtime := time.Unix(1_700_003_000, 0).UTC()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	snapshot, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		t.Fatalf("ScanLocalWorkspace() error = %v", err)
	}
	snapshot.WorkspaceInstanceID = "workspace-cache-test-" + filepath.Base(root)
	file := snapshot.Files["note.txt"]

	cache, err := loadPersistentHashCache(snapshot.WorkspaceInstanceID)
	if err != nil {
		t.Fatalf("loadPersistentHashCache(first) error = %v", err)
	}
	cache.Store(file)
	if err := cache.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	reloaded, err := loadPersistentHashCache(snapshot.WorkspaceInstanceID)
	if err != nil {
		t.Fatalf("loadPersistentHashCache(second) error = %v", err)
	}
	hashValue, ok := reloaded.Lookup(file)
	if !ok {
		t.Fatal("Lookup() = miss, want hit")
	}
	if hashValue != file.Entry.ContentHash {
		t.Fatalf("Lookup() hash = %q, want %q", hashValue, file.Entry.ContentHash)
	}
	hits, misses := reloaded.Stats()
	if hits != 1 || misses != 0 {
		t.Fatalf("Stats() = hits:%d misses:%d, want hits:1 misses:0", hits, misses)
	}
}
