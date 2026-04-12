package daemon

import (
	"bytes"
	"testing"
	"time"

	"symterm/internal/proto"
)

func BenchmarkWorkspaceManagerUploadLargeFile256MB(b *testing.B) {
	content := bytes.Repeat([]byte("0123456789abcdef"), 16*1024*1024)
	contentHash := sha256Hex(content)
	entry := proto.ManifestEntry{
		Path: "large.bin",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_400, 0).UTC(),
			Size:  int64(len(content)),
		},
		StatFingerprint: "benchmark-large-upload",
		ContentHash:     contentHash,
	}
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		projectsRoot := b.TempDir()
		manager := NewWorkspaceManager(projectsRoot)
		if err := manager.BeginSync(key, proto.BeginSyncRequest{
			SyncEpoch:       uint64(idx + 1),
			AttemptID:       1,
			RootFingerprint: "benchmark-root",
		}); err != nil {
			b.Fatalf("BeginSync() error = %v", err)
		}
		if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
			b.Fatalf("ScanManifest() error = %v", err)
		}
		started, err := manager.BeginFile(key, proto.BeginFileRequest{
			SyncEpoch:       uint64(idx + 1),
			Path:            entry.Path,
			Metadata:        entry.Metadata,
			ExpectedSize:    entry.Metadata.Size,
			StatFingerprint: entry.StatFingerprint,
		})
		if err != nil {
			b.Fatalf("BeginFile() error = %v", err)
		}
		var offset int64
		for start := 0; start < len(content); start += 512 * 1024 {
			end := start + 512*1024
			if end > len(content) {
				end = len(content)
			}
			if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
				FileID: started.FileID,
				Offset: offset,
				Data:   content[start:end],
			}); err != nil {
				b.Fatalf("ApplyChunk() error = %v", err)
			}
			offset += int64(end - start)
		}
		if err := manager.CommitFile(key, proto.CommitFileRequest{
			FileID:          started.FileID,
			FinalHash:       entry.ContentHash,
			FinalSize:       entry.Metadata.Size,
			MTime:           entry.Metadata.MTime,
			Mode:            entry.Metadata.Mode,
			StatFingerprint: entry.StatFingerprint,
		}); err != nil {
			b.Fatalf("CommitFile() error = %v", err)
		}
		if err := manager.FinalizeSync(key, proto.FinalizeSyncRequest{
			SyncEpoch:   uint64(idx + 1),
			AttemptID:   1,
			GuardStable: true,
		}); err != nil {
			b.Fatalf("FinalizeSync() error = %v", err)
		}
	}
}
