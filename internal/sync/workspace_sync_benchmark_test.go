package sync

import (
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
