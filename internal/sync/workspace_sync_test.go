package sync

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestFingerprintEntriesSeparatesMetadataAndContentFingerprints(t *testing.T) {
	t.Parallel()

	entriesWithoutHash := []proto.ManifestEntry{{
		Path: "demo.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1712476800, 0).UTC(),
			Size:  4,
		},
		StatFingerprint: "4:420:1712476800",
	}}
	entriesWithHash := []proto.ManifestEntry{{
		Path: "demo.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1712476800, 0).UTC(),
			Size:  4,
		},
		StatFingerprint: "4:420:1712476800",
		ContentHash:     "deadbeef",
	}}

	left, err := fingerprintEntries(entriesWithoutHash, false)
	if err != nil {
		t.Fatalf("fingerprintEntries() error = %v", err)
	}
	right, err := fingerprintEntries(entriesWithHash, false)
	if err != nil {
		t.Fatalf("fingerprintEntries() error = %v", err)
	}
	if left != right {
		t.Fatalf("fingerprints differ: %q != %q", left, right)
	}

	left, err = fingerprintEntries(entriesWithoutHash, true)
	if err != nil {
		t.Fatalf("fingerprintEntries(content) error = %v", err)
	}
	right, err = fingerprintEntries(entriesWithHash, true)
	if err != nil {
		t.Fatalf("fingerprintEntries(content) error = %v", err)
	}
	if left == right {
		t.Fatalf("content fingerprints matched unexpectedly: %q", left)
	}
}

func TestScanLocalWorkspaceContentFingerprintDetectsSameSecondRewrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "demo.txt")
	timestamp := time.Unix(1712476800, 0).UTC()

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

	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("metadata fingerprint changed unexpectedly: %q != %q", first.Fingerprint, second.Fingerprint)
	}
	if first.ContentFingerprint == second.ContentFingerprint {
		t.Fatalf("content fingerprint did not change: %q", first.ContentFingerprint)
	}
}

func TestScanLocalWorkspaceRejectsPortableNameCollisions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	firstPath := filepath.Join(root, "Dir", "\u00E9.txt")
	secondPath := filepath.Join(root, "dir", "e\u0301.txt")
	if err := os.MkdirAll(filepath.Dir(firstPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(first) error = %v", err)
	}
	if err := os.WriteFile(firstPath, []byte("first"), 0o644); err != nil {
		t.Fatalf("WriteFile(first) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(secondPath), 0o755); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("host filesystem merged normalized path variants: %v", err)
		}
		t.Fatalf("MkdirAll(second) error = %v", err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o644); err != nil {
		if runtime.GOOS == "windows" || errors.Is(err, os.ErrExist) {
			t.Skipf("host filesystem merged normalized path variants: %v", err)
		}
		t.Fatalf("WriteFile(second) error = %v", err)
	}

	_, err := ScanLocalWorkspace(root, nil, true)
	if err == nil {
		t.Fatal("scanLocalWorkspace() succeeded with portable path collision")
	}
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) || protoErr.Code != proto.ErrUnsupportedPath {
		t.Fatalf("scanLocalWorkspace() error = %v, want unsupported-path", err)
	}
	if !strings.Contains(protoErr.Message, "cross-platform normalization") {
		t.Fatalf("scanLocalWorkspace() message = %q", protoErr.Message)
	}
}

func TestScanLocalWorkspaceRejectsDirectoryNameCollisions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	firstPath := filepath.Join(root, "Dir", "\u00E9", "first.txt")
	secondPath := filepath.Join(root, "dir", "e\u0301", "second.txt")
	if err := os.MkdirAll(filepath.Dir(firstPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(first) error = %v", err)
	}
	if err := os.WriteFile(firstPath, []byte("first"), 0o644); err != nil {
		t.Fatalf("WriteFile(first) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(secondPath), 0o755); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("host filesystem merged normalized path variants: %v", err)
		}
		t.Fatalf("MkdirAll(second) error = %v", err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o644); err != nil {
		if runtime.GOOS == "windows" || errors.Is(err, os.ErrExist) {
			t.Skipf("host filesystem merged normalized path variants: %v", err)
		}
		t.Fatalf("WriteFile(second) error = %v", err)
	}

	_, err := ScanLocalWorkspace(root, nil, true)
	if err == nil {
		t.Fatal("scanLocalWorkspace() succeeded with directory collision")
	}
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) || protoErr.Code != proto.ErrUnsupportedPath {
		t.Fatalf("scanLocalWorkspace() error = %v, want unsupported-path", err)
	}
	if !strings.Contains(protoErr.Message, "cross-platform normalization") {
		t.Fatalf("scanLocalWorkspace() message = %q", protoErr.Message)
	}
}

func TestScanLocalWorkspaceRejectsWindowsReservedName(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("host filesystem does not allow creating Windows-reserved names")
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CON.txt"), []byte("reserved"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := ScanLocalWorkspace(root, nil, true)
	if err == nil {
		t.Fatal("scanLocalWorkspace() succeeded with Windows-reserved name")
	}
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) || protoErr.Code != proto.ErrUnsupportedPath {
		t.Fatalf("scanLocalWorkspace() error = %v, want unsupported-path", err)
	}
	if !strings.Contains(protoErr.Message, "Windows-reserved") {
		t.Fatalf("scanLocalWorkspace() message = %q", protoErr.Message)
	}
}
