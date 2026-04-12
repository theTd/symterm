package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestWorkspaceManagerSyncV2BundleUploadPublishesFiles(t *testing.T) {
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

	started, err := manager.StartSyncSession(key, proto.StartSyncSessionRequest{
		SyncEpoch:       1,
		AttemptID:       1,
		RootFingerprint: "root-v2",
	})
	if err != nil {
		t.Fatalf("StartSyncSession() error = %v", err)
	}

	files := []struct {
		path    string
		content []byte
		mtime   time.Time
	}{
		{path: "docs/a.txt", content: []byte("alpha"), mtime: time.Unix(1_700_004_000, 0).UTC()},
		{path: "docs/b.txt", content: []byte("beta"), mtime: time.Unix(1_700_004_100, 0).UTC()},
	}
	entries := make([]proto.ManifestEntry, 0, len(files))
	for _, file := range files {
		entries = append(entries, proto.ManifestEntry{
			Path: file.path,
			Metadata: proto.FileMetadata{
				Mode:  0o644,
				MTime: file.mtime,
				Size:  int64(len(file.content)),
			},
			StatFingerprint: "v2-" + filepath.Base(file.path),
			ContentHash:     sha256Hex(file.content),
		})
	}
	if err := manager.SyncManifestBatch(key, proto.SyncManifestBatchRequest{
		SessionID: started.SessionID,
		Entries:   entries,
		Final:     true,
	}); err != nil {
		t.Fatalf("SyncManifestBatch() error = %v", err)
	}

	plan, err := manager.PlanSyncV2(key, proto.PlanSyncV2Request{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("PlanSyncV2() error = %v", err)
	}
	if len(plan.HashPaths) != 0 {
		t.Fatalf("PlanSyncV2() hash paths = %#v, want none", plan.HashPaths)
	}
	if len(plan.UploadPaths) != len(files) {
		t.Fatalf("PlanSyncV2() upload paths = %#v, want %d files", plan.UploadPaths, len(files))
	}

	bundle, err := manager.UploadBundleBegin(key, proto.UploadBundleBeginRequest{
		SessionID: started.SessionID,
		SyncEpoch: 1,
	})
	if err != nil {
		t.Fatalf("UploadBundleBegin() error = %v", err)
	}
	uploadFiles := make([]proto.UploadBundleFile, 0, len(files))
	for _, file := range files {
		uploadFiles = append(uploadFiles, proto.UploadBundleFile{
			Path:            file.path,
			Metadata:        proto.FileMetadata{Mode: 0o644, MTime: file.mtime, Size: int64(len(file.content))},
			StatFingerprint: "v2-" + filepath.Base(file.path),
			ContentHash:     sha256Hex(file.content),
			Data:            file.content,
		})
	}
	if err := manager.UploadBundleCommit(key, proto.UploadBundleCommitRequest{
		SessionID: started.SessionID,
		BundleID:  bundle.BundleID,
		Files:     uploadFiles,
	}); err != nil {
		t.Fatalf("UploadBundleCommit() error = %v", err)
	}
	if err := manager.FinalizeSyncV2(key, proto.FinalizeSyncV2Request{
		SessionID:   started.SessionID,
		SyncEpoch:   1,
		AttemptID:   1,
		GuardStable: true,
	}); err != nil {
		t.Fatalf("FinalizeSyncV2() error = %v", err)
	}

	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(layout.WorkspaceDir, filepath.FromSlash(file.path)))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", file.path, err)
		}
		if string(data) != string(file.content) {
			t.Fatalf("ReadFile(%s) = %q, want %q", file.path, data, file.content)
		}
	}
}

func TestWorkspaceManagerSyncV2DeletePathsBatchRemovesStaleEntries(t *testing.T) {
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

	keepPath := filepath.Join(layout.WorkspaceDir, "keep.txt")
	if err := os.WriteFile(keepPath, []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile(keep.txt) error = %v", err)
	}
	stalePath := filepath.Join(layout.WorkspaceDir, "stale.txt")
	if err := os.WriteFile(stalePath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(stale.txt) error = %v", err)
	}
	mtime := time.Unix(1_700_004_200, 0).UTC()
	if err := os.Chtimes(keepPath, mtime, mtime); err != nil {
		t.Fatalf("Chtimes(keep.txt) error = %v", err)
	}
	info, err := os.Stat(keepPath)
	if err != nil {
		t.Fatalf("Stat(keep.txt) error = %v", err)
	}

	started, err := manager.StartSyncSession(key, proto.StartSyncSessionRequest{
		SyncEpoch:       1,
		AttemptID:       1,
		RootFingerprint: "root-v2-delete",
	})
	if err != nil {
		t.Fatalf("StartSyncSession() error = %v", err)
	}
	if err := manager.SyncManifestBatch(key, proto.SyncManifestBatchRequest{
		SessionID: started.SessionID,
		Entries: []proto.ManifestEntry{{
			Path:            "keep.txt",
			Metadata:        fileInfoMetadata(info),
			StatFingerprint: "keep-file",
			ContentHash:     sha256Hex([]byte("keep")),
		}},
		Final: true,
	}); err != nil {
		t.Fatalf("SyncManifestBatch() error = %v", err)
	}

	plan, err := manager.PlanSyncV2(key, proto.PlanSyncV2Request{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("PlanSyncV2() error = %v", err)
	}
	if !slices.Contains(plan.DeletePaths, "stale.txt") {
		t.Fatalf("PlanSyncV2() delete paths = %#v, want stale.txt", plan.DeletePaths)
	}
	if err := manager.DeletePathsBatch(key, proto.DeletePathsBatchRequest{
		SessionID: started.SessionID,
		SyncEpoch: 1,
		Paths:     plan.DeletePaths,
	}); err != nil {
		t.Fatalf("DeletePathsBatch() error = %v", err)
	}
	if err := manager.FinalizeSyncV2(key, proto.FinalizeSyncV2Request{
		SessionID:   started.SessionID,
		SyncEpoch:   1,
		AttemptID:   1,
		GuardStable: true,
	}); err != nil {
		t.Fatalf("FinalizeSyncV2() error = %v", err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("Stat(stale.txt) error = %v, want not exist", err)
	}
}
