package sync

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestOwnerWorkspaceChangesDetectsSameSecondRewrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "demo.txt")
	timestamp := time.Unix(1_712_476_800, 0).UTC()

	if err := os.WriteFile(path, []byte("aaaa"), 0o644); err != nil {
		t.Fatalf("WriteFile(first) error = %v", err)
	}
	if err := os.Chtimes(path, timestamp, timestamp); err != nil {
		t.Fatalf("Chtimes(first) error = %v", err)
	}
	first, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		t.Fatalf("scanLocalWorkspace(first) error = %v", err)
	}

	if err := os.WriteFile(path, []byte("bbbb"), 0o644); err != nil {
		t.Fatalf("WriteFile(second) error = %v", err)
	}
	if err := os.Chtimes(path, timestamp, timestamp); err != nil {
		t.Fatalf("Chtimes(second) error = %v", err)
	}
	second, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		t.Fatalf("scanLocalWorkspace(second) error = %v", err)
	}

	changes := ownerWorkspaceChanges(first, second)
	if len(changes) == 0 {
		t.Fatal("ownerWorkspaceChanges() returned no invalidations")
	}
	if changes[0] != (proto.InvalidateChange{Path: "demo.txt", Kind: proto.InvalidateData}) {
		t.Fatalf("first change = %#v", changes[0])
	}
}

func TestOwnerWorkspaceChangesDetectsDelete(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "demo.txt")

	if err := os.WriteFile(path, []byte("aaaa"), 0o644); err != nil {
		t.Fatalf("WriteFile(first) error = %v", err)
	}
	first, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		t.Fatalf("scanLocalWorkspace(first) error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	second, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		t.Fatalf("scanLocalWorkspace(second) error = %v", err)
	}

	changes := ownerWorkspaceChanges(first, second)
	if len(changes) == 0 {
		t.Fatal("ownerWorkspaceChanges() returned no invalidations")
	}
	if changes[0] != (proto.InvalidateChange{Path: "demo.txt", Kind: proto.InvalidateDelete}) {
		t.Fatalf("first change = %#v", changes[0])
	}
}

func TestOwnerWorkspaceChangesDetectsRename(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	oldPath := filepath.Join(root, "before.txt")
	newPath := filepath.Join(root, "after.txt")
	timestamp := time.Unix(1_712_476_800, 0).UTC()

	if err := os.WriteFile(oldPath, []byte("same-content"), 0o644); err != nil {
		t.Fatalf("WriteFile(first) error = %v", err)
	}
	if err := os.Chtimes(oldPath, timestamp, timestamp); err != nil {
		t.Fatalf("Chtimes(first) error = %v", err)
	}
	first, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		t.Fatalf("scanLocalWorkspace(first) error = %v", err)
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	second, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		t.Fatalf("scanLocalWorkspace(second) error = %v", err)
	}

	changes := ownerWorkspaceChanges(first, second)
	if len(changes) == 0 {
		t.Fatal("ownerWorkspaceChanges() returned no invalidations")
	}
	if changes[0] != (proto.InvalidateChange{Path: "before.txt", NewPath: "after.txt", Kind: proto.InvalidateRename}) {
		t.Fatalf("first change = %#v", changes[0])
	}
}

func TestOwnerWorkspaceChangesDetectsCrossDirectoryRename(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "from"), 0o755); err != nil {
		t.Fatalf("MkdirAll(from) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "to"), 0o755); err != nil {
		t.Fatalf("MkdirAll(to) error = %v", err)
	}
	oldPath := filepath.Join(root, "from", "demo.txt")
	newPath := filepath.Join(root, "to", "demo.txt")
	timestamp := time.Unix(1_712_476_800, 0).UTC()

	if err := os.WriteFile(oldPath, []byte("same-content"), 0o644); err != nil {
		t.Fatalf("WriteFile(first) error = %v", err)
	}
	if err := os.Chtimes(oldPath, timestamp, timestamp); err != nil {
		t.Fatalf("Chtimes(first) error = %v", err)
	}
	first, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		t.Fatalf("scanLocalWorkspace(first) error = %v", err)
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	second, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		t.Fatalf("scanLocalWorkspace(second) error = %v", err)
	}

	changes := ownerWorkspaceChanges(first, second)
	if len(changes) < 3 {
		t.Fatalf("ownerWorkspaceChanges() len = %d, want at least 3", len(changes))
	}
	if changes[0] != (proto.InvalidateChange{Path: "from/demo.txt", NewPath: "to/demo.txt", Kind: proto.InvalidateRename}) {
		t.Fatalf("first change = %#v", changes[0])
	}
	foundFromDentry := false
	foundToDentry := false
	for _, change := range changes[1:] {
		if change == (proto.InvalidateChange{Path: "from", Kind: proto.InvalidateDentry}) {
			foundFromDentry = true
		}
		if change == (proto.InvalidateChange{Path: "to", Kind: proto.InvalidateDentry}) {
			foundToDentry = true
		}
	}
	if !foundFromDentry || !foundToDentry {
		t.Fatalf("rename dentry invalidations = %#v", changes)
	}
}

func TestOwnerWorkspaceWatchDirectoriesIncludesRootAndNestedDirs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	snapshot, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		t.Fatalf("scanLocalWorkspace() error = %v", err)
	}

	dirs := ownerWorkspaceWatchDirectories(snapshot)
	var got []string
	for path := range dirs {
		got = append(got, path)
	}
	sort.Strings(got)

	want := []string{
		root,
		filepath.Join(root, "docs"),
		filepath.Join(root, "docs", "nested"),
	}
	if len(got) != len(want) {
		t.Fatalf("watch dirs = %#v, want %#v", got, want)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("watch dirs = %#v, want %#v", got, want)
		}
	}
}
