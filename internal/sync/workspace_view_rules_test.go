package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"symterm/internal/proto"
)

func TestScanLocalWorkspaceAppliesWorkspaceIgnoreRules(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "build", "include"), 0o755); err != nil {
		t.Fatalf("MkdirAll(build/include) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatalf("MkdirAll(src) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceIgnoreFileName), []byte("build/\n!build/include/**\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.stignore) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "build", "drop.txt"), []byte("drop"), 0o644); err != nil {
		t.Fatalf("WriteFile(build/drop.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "build", "include", "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile(build/include/keep.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.txt"), []byte("main"), 0o644); err != nil {
		t.Fatalf("WriteFile(src/main.txt) error = %v", err)
	}

	snapshot, err := ScanLocalWorkspace(root, nil, false)
	if err != nil {
		t.Fatalf("ScanLocalWorkspace() error = %v", err)
	}

	got := make([]string, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		got = append(got, entry.Path)
	}
	want := []string{
		".stignore",
		"build",
		"build/include",
		"build/include/keep.txt",
		"src",
		"src/main.txt",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("snapshot paths = %#v, want %#v", got, want)
	}
	if _, ok := snapshot.Files["build/drop.txt"]; ok {
		t.Fatal("build/drop.txt unexpectedly included in snapshot")
	}
}

func TestLocalOwnerFileServiceFiltersWorkspaceView(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist", "include"), 0o755); err != nil {
		t.Fatalf("MkdirAll(dist/include) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceIgnoreFileName), []byte("secret.txt\ndist/\n!dist/include/**\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.stignore) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile(secret.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "drop.txt"), []byte("drop"), 0o644); err != nil {
		t.Fatalf("WriteFile(dist/drop.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "include", "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile(dist/include/keep.txt) error = %v", err)
	}

	service := newLocalOwnerFileService(root)

	reply, err := service.FsRead(context.Background(), proto.FsOpReadDir, proto.FsRequest{})
	if err != nil {
		t.Fatalf("FsRead(root readdir) error = %v", err)
	}
	if string(reply.Data) != ".stignore\ndist" {
		t.Fatalf("root readdir = %q", string(reply.Data))
	}

	distReply, err := service.FsRead(context.Background(), proto.FsOpReadDir, proto.FsRequest{Path: "dist"})
	if err != nil {
		t.Fatalf("FsRead(dist readdir) error = %v", err)
	}
	if string(distReply.Data) != "include" {
		t.Fatalf("dist readdir = %q", string(distReply.Data))
	}

	fileReply, err := service.FsRead(context.Background(), proto.FsOpRead, proto.FsRequest{Path: "dist/include/keep.txt", Size: 16})
	if err != nil {
		t.Fatalf("FsRead(keep.txt) error = %v", err)
	}
	if string(fileReply.Data) != "keep" {
		t.Fatalf("keep.txt data = %q", string(fileReply.Data))
	}

	_, err = service.FsRead(context.Background(), proto.FsOpRead, proto.FsRequest{Path: "secret.txt", Size: 16})
	if err == nil {
		t.Fatal("FsRead(secret.txt) succeeded for excluded path")
	}
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) || protoErr.Code != proto.ErrUnknownFile {
		t.Fatalf("FsRead(secret.txt) error = %v, want unknown-file", err)
	}
}

func TestVisibleDirectoryNamesOnDiskSkipsExcludedEmptyDirs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "logs"), 0o755); err != nil {
		t.Fatalf("MkdirAll(logs) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceIgnoreFileName), []byte("logs/\n!build/include/**\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.stignore) error = %v", err)
	}

	rules, err := LoadWorkspaceViewRules(root)
	if err != nil {
		t.Fatalf("LoadWorkspaceViewRules() error = %v", err)
	}
	names, err := VisibleDirectoryNamesOnDisk(root, rules, "")
	if err != nil {
		t.Fatalf("VisibleDirectoryNamesOnDisk() error = %v", err)
	}
	sort.Strings(names)
	if strings.Join(names, "\n") != ".stignore" {
		t.Fatalf("visible names = %#v", names)
	}
}
