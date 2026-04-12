package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestWorkspaceManagerPlanManifestHashesRequestsUncertainSameMetadataFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	path := filepath.Join(layout.WorkspaceDir, "note.txt")
	if err := os.WriteFile(path, []byte("same"), 0o644); err != nil {
		t.Fatalf("WriteFile(note.txt) error = %v", err)
	}
	wantMTime := time.Unix(1_700_000_800, 0).UTC()
	if err := os.Chtimes(path, wantMTime, wantMTime); err != nil {
		t.Fatalf("Chtimes(note.txt) error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(note.txt) error = %v", err)
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		AttemptID:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}

	entry := proto.ManifestEntry{
		Path:            "note.txt",
		Metadata:        fileInfoMetadata(info),
		StatFingerprint: "note-same",
	}
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest(no hash) error = %v", err)
	}

	hashPlan, err := manager.PlanManifestHashes(key)
	if err != nil {
		t.Fatalf("PlanManifestHashes() error = %v", err)
	}
	if len(hashPlan.Paths) != 1 || hashPlan.Paths[0] != "note.txt" {
		t.Fatalf("PlanManifestHashes() = %#v, want note.txt", hashPlan.Paths)
	}

	entry.ContentHash = sha256Hex([]byte("same"))
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest(with hash) error = %v", err)
	}
	actions, err := manager.PlanSyncActions(key)
	if err != nil {
		t.Fatalf("PlanSyncActions() error = %v", err)
	}
	if len(actions.UploadPaths) != 0 || len(actions.DeletePaths) != 0 {
		t.Fatalf("PlanSyncActions() = %#v, want no changes", actions)
	}
}

func TestWorkspaceManagerPlanSyncActionsReusesRemoteHashMemo(t *testing.T) {
	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	path := filepath.Join(layout.WorkspaceDir, "cached.txt")
	if err := os.WriteFile(path, []byte("cached"), 0o644); err != nil {
		t.Fatalf("WriteFile(cached.txt) error = %v", err)
	}
	wantMTime := time.Unix(1_700_000_900, 0).UTC()
	if err := os.Chtimes(path, wantMTime, wantMTime); err != nil {
		t.Fatalf("Chtimes(cached.txt) error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(cached.txt) error = %v", err)
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		AttemptID:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}

	entry := proto.ManifestEntry{
		Path:            "cached.txt",
		Metadata:        fileInfoMetadata(info),
		StatFingerprint: "cached-file",
		ContentHash:     sha256Hex([]byte("cached")),
	}
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest() error = %v", err)
	}

	original := syncHashFileFn
	t.Cleanup(func() {
		syncHashFileFn = original
	})
	var calls int
	syncHashFileFn = func(path string) (string, error) {
		calls++
		return original(path)
	}

	for idx := 0; idx < 2; idx++ {
		actions, err := manager.PlanSyncActions(key)
		if err != nil {
			t.Fatalf("PlanSyncActions(%d) error = %v", idx, err)
		}
		if len(actions.UploadPaths) != 0 || len(actions.DeletePaths) != 0 {
			t.Fatalf("PlanSyncActions(%d) = %#v, want no changes", idx, actions)
		}
	}

	if calls != 1 {
		t.Fatalf("syncHashFileFn calls = %d, want 1", calls)
	}
}
