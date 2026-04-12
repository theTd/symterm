package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkScanLocalWorkspaceColdStart20000Files(b *testing.B) {
	root := b.TempDir()
	for idx := 0; idx < 20_000; idx++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg-%03d", idx%200))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatalf("MkdirAll(%q) error = %v", dir, err)
		}
		path := filepath.Join(dir, fmt.Sprintf("file-%05d.txt", idx))
		if err := os.WriteFile(path, []byte("benchmark-data"), 0o644); err != nil {
			b.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}

	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		snapshot, err := ScanLocalWorkspace(root, nil, false)
		if err != nil {
			b.Fatalf("ScanLocalWorkspace() error = %v", err)
		}
		if len(snapshot.Files) != 20_000 {
			b.Fatalf("snapshot file count = %d", len(snapshot.Files))
		}
	}
}

func BenchmarkHashWorkspaceSnapshotWarmPersistentCache20000Files(b *testing.B) {
	root := b.TempDir()
	for idx := 0; idx < 20_000; idx++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg-%03d", idx%200))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatalf("MkdirAll(%q) error = %v", dir, err)
		}
		path := filepath.Join(dir, fmt.Sprintf("file-%05d.txt", idx))
		if err := os.WriteFile(path, []byte("benchmark-data"), 0o644); err != nil {
			b.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}

	initial, err := ScanLocalWorkspace(root, nil, false)
	if err != nil {
		b.Fatalf("ScanLocalWorkspace(initial) error = %v", err)
	}
	initial.WorkspaceInstanceID = "benchmark-warm-persistent-cache"
	guard := StartSyncGuard(context.Background(), root, initial, nil)
	allPaths := make([]string, 0, len(initial.Files))
	for path := range initial.Files {
		allPaths = append(allPaths, path)
	}
	seeded, err := guard.HashPaths(initial, allPaths)
	if err != nil {
		b.Fatalf("HashPaths(seed) error = %v", err)
	}
	if len(seeded.HashedFiles) != len(initial.Files) {
		b.Fatalf("seeded hash count = %d, want %d", len(seeded.HashedFiles), len(initial.Files))
	}

	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		snapshot, err := ScanLocalWorkspace(root, nil, false)
		if err != nil {
			b.Fatalf("ScanLocalWorkspace() error = %v", err)
		}
		snapshot.WorkspaceInstanceID = initial.WorkspaceInstanceID
		hashed, err := guard.HashPaths(snapshot, allPaths)
		if err != nil {
			b.Fatalf("HashPaths() error = %v", err)
		}
		if len(hashed.HashedFiles) != 20_000 {
			b.Fatalf("hashed file count = %d", len(hashed.HashedFiles))
		}
	}
}
