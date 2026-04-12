//go:build unix

package sync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"symterm/internal/proto"
)

func TestScanLocalWorkspaceRejectsNonRegularSpecialFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "named-pipe")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}

	_, err := ScanLocalWorkspace(root, nil, true)
	if err == nil {
		t.Fatal("scanLocalWorkspace() succeeded with FIFO")
	}
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) || protoErr.Code != proto.ErrUnsupportedPath {
		t.Fatalf("scanLocalWorkspace() error = %v, want unsupported-path", err)
	}
	if !strings.Contains(protoErr.Message, "only regular files") {
		t.Fatalf("scanLocalWorkspace() message = %q", protoErr.Message)
	}
}

func TestScanLocalWorkspaceRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	_, err := ScanLocalWorkspace(root, nil, true)
	if err == nil {
		t.Fatal("scanLocalWorkspace() succeeded with symlink")
	}
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) || protoErr.Code != proto.ErrUnsupportedPath {
		t.Fatalf("scanLocalWorkspace() error = %v, want unsupported-path", err)
	}
	if !strings.Contains(protoErr.Message, "symbolic links") {
		t.Fatalf("scanLocalWorkspace() message = %q", protoErr.Message)
	}
}
