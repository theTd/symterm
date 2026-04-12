package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestWorkspaceManagerFsReadFiltersAuthoritativeRootByWorkspaceIgnore(t *testing.T) {
	t.Parallel()

	projectsRoot := t.TempDir()
	manager := NewWorkspaceManager(projectsRoot)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	authorityRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(authorityRoot, "dist", "include"), 0o755); err != nil {
		t.Fatalf("MkdirAll(dist/include) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(authorityRoot, ".stignore"), []byte("secret.txt\ndist/\n!dist/include/**\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.stignore) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(authorityRoot, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile(secret.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(authorityRoot, "dist", "drop.txt"), []byte("drop"), 0o644); err != nil {
		t.Fatalf("WriteFile(dist/drop.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(authorityRoot, "dist", "include", "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile(dist/include/keep.txt) error = %v", err)
	}
	if err := manager.SetAuthoritativeRoot(key, authorityRoot); err != nil {
		t.Fatalf("SetAuthoritativeRoot() error = %v", err)
	}

	rootReply, err := manager.FsRead(key, proto.FsOpReadDir, proto.FsRequest{})
	if err != nil {
		t.Fatalf("FsRead(root readdir) error = %v", err)
	}
	if string(rootReply.Data) != ".stignore\ndist" {
		t.Fatalf("root readdir = %q", string(rootReply.Data))
	}

	keepReply, err := manager.FsRead(key, proto.FsOpRead, proto.FsRequest{Path: "dist/include/keep.txt", Size: 16})
	if err != nil {
		t.Fatalf("FsRead(keep.txt) error = %v", err)
	}
	if string(keepReply.Data) != "keep" {
		t.Fatalf("keep.txt data = %q", string(keepReply.Data))
	}

	_, err = manager.FsRead(key, proto.FsOpRead, proto.FsRequest{Path: "secret.txt", Size: 16})
	if err == nil {
		t.Fatal("FsRead(secret.txt) succeeded for excluded path")
	}
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) || protoErr.Code != proto.ErrUnknownFile {
		t.Fatalf("FsRead(secret.txt) error = %v, want unknown-file", err)
	}
}

func TestObserveConservativePathOnDiskFiltersWorkspaceIgnore(t *testing.T) {
	t.Parallel()

	projectsRoot := t.TempDir()
	manager := NewWorkspaceManager(projectsRoot)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(projectsRoot, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}

	authorityRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(authorityRoot, "dist", "include"), 0o755); err != nil {
		t.Fatalf("MkdirAll(dist/include) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(authorityRoot, ".stignore"), []byte("secret.txt\ndist/\n!dist/include/**\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.stignore) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(authorityRoot, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile(secret.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(authorityRoot, "dist", "include", "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile(dist/include/keep.txt) error = %v", err)
	}

	hiddenObservation, err := manager.observeConservativePathOnDisk(workspaceAuthority{root: authorityRoot}, layout, "secret.txt")
	if err != nil {
		t.Fatalf("observeConservativePathOnDisk(secret.txt) error = %v", err)
	}
	if hiddenObservation.objectFingerprint != conservativeMissingFingerprint {
		t.Fatalf("secret observation = %#v", hiddenObservation)
	}

	rootObservation, err := manager.observeConservativePathOnDisk(workspaceAuthority{root: authorityRoot}, layout, "")
	if err != nil {
		t.Fatalf("observeConservativePathOnDisk(root) error = %v", err)
	}
	if strings.Contains(rootObservation.directoryView, "secret.txt") {
		t.Fatalf("root directory view leaked excluded file: %q", rootObservation.directoryView)
	}
	if !strings.Contains(rootObservation.directoryView, ".stignore") || !strings.Contains(rootObservation.directoryView, "dist") {
		t.Fatalf("root directory view = %q", rootObservation.directoryView)
	}
}

func TestWorkspaceManagerPlanSyncActionsUsesCommittedWorkspaceWithoutStageMirror(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte("same"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace note.txt) error = %v", err)
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		AttemptID:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	stageRoot := activeSyncStageRoot(t, manager, key, 1)
	if names, err := os.ReadDir(stageRoot); err != nil {
		t.Fatalf("ReadDir(stageRoot) error = %v", err)
	} else if len(names) != 0 {
		t.Fatalf("stageRoot unexpectedly pre-mirrored: %d entries", len(names))
	}

	entry := proto.ManifestEntry{
		Path: "note.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_200, 0).UTC(),
			Size:  int64(len("same")),
		},
		StatFingerprint: "same-note",
		ContentHash:     sha256Hex([]byte("same")),
	}
	if err := os.Chtimes(filepath.Join(layout.WorkspaceDir, "note.txt"), entry.Metadata.MTime, entry.Metadata.MTime); err != nil {
		t.Fatalf("Chtimes(workspace note.txt) error = %v", err)
	}
	info, err := os.Stat(filepath.Join(layout.WorkspaceDir, "note.txt"))
	if err != nil {
		t.Fatalf("Stat(workspace note.txt) error = %v", err)
	}
	entry.Metadata = fileInfoMetadata(info)
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest() error = %v", err)
	}
	actions, err := manager.PlanSyncActions(key)
	if err != nil {
		t.Fatalf("PlanSyncActions() error = %v", err)
	}
	if len(actions.UploadPaths) != 0 || len(actions.DeletePaths) != 0 {
		t.Fatalf("PlanSyncActions() = uploads:%#v deletes:%#v, want no changes when committed workspace already matches", actions.UploadPaths, actions.DeletePaths)
	}
}
