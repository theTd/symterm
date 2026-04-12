//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMountInfoPoint(t *testing.T) {
	t.Parallel()

	line := `151 29 0:56 / /srv/symterm/demo/mount rw,nosuid,nodev,relatime - fuse.symterm symterm rw,user_id=1000,group_id=1000`
	mountPoint, ok := parseMountInfoPoint(line)
	if !ok {
		t.Fatal("parseMountInfoPoint() = false")
	}
	if mountPoint != "/srv/symterm/demo/mount" {
		t.Fatalf("mountPoint = %q", mountPoint)
	}
}

func TestParseMountInfoPointUnescapesSpaces(t *testing.T) {
	t.Parallel()

	line := `151 29 0:56 / /srv/with\040space/project\040mount rw,nosuid,nodev,relatime - fuse.symterm symterm rw`
	mountPoint, ok := parseMountInfoPoint(line)
	if !ok {
		t.Fatal("parseMountInfoPoint() = false")
	}
	if mountPoint != "/srv/with space/project mount" {
		t.Fatalf("mountPoint = %q", mountPoint)
	}
}

func TestEnsureMountDirectoryReadyReplacesFileLeaf(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mountDir := filepath.Join(root, "project", "mount")
	if err := os.MkdirAll(filepath.Dir(mountDir), 0o755); err != nil {
		t.Fatalf("MkdirAll(parent) error = %v", err)
	}
	if err := os.WriteFile(mountDir, []byte("blocker"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount) error = %v", err)
	}

	if err := ensureMountDirectoryReady(mountDir); err != nil {
		t.Fatalf("ensureMountDirectoryReady() error = %v", err)
	}

	info, err := os.Lstat(mountDir)
	if err != nil {
		t.Fatalf("Lstat(mount) error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("mountDir mode = %v, want directory", info.Mode())
	}
}

func TestEnsureMountDirectoryReadyReplacesSymlinkLeaf(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mountDir := filepath.Join(root, "project", "mount")
	if err := os.MkdirAll(filepath.Dir(mountDir), 0o755); err != nil {
		t.Fatalf("MkdirAll(parent) error = %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "missing-target"), mountDir); err != nil {
		t.Fatalf("Symlink(mount) error = %v", err)
	}

	if err := ensureMountDirectoryReady(mountDir); err != nil {
		t.Fatalf("ensureMountDirectoryReady() error = %v", err)
	}

	info, err := os.Lstat(mountDir)
	if err != nil {
		t.Fatalf("Lstat(mount) error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("mountDir mode = %v, want directory", info.Mode())
	}
}
