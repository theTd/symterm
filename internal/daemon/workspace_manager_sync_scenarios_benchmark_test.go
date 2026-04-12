package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"symterm/internal/proto"
)

func BenchmarkWorkspaceManagerInitialSyncScenarios(b *testing.B) {
	type scenario struct {
		name       string
		prepare    func(*testing.B, string) ([]proto.ManifestEntry, map[string][]byte)
		applyPlans bool
	}

	scenarios := []scenario{
		{
			name: "no-diff-large-workspace",
			prepare: func(tb *testing.B, root string) ([]proto.ManifestEntry, map[string][]byte) {
				return benchmarkSeedWorkspace(tb, root, 256, 1024, false)
			},
		},
		{
			name: "low-diff-large-workspace",
			prepare: func(tb *testing.B, root string) ([]proto.ManifestEntry, map[string][]byte) {
				entries, uploads := benchmarkSeedWorkspace(tb, root, 256, 1024, false)
				entries[0].ContentHash = sha256Hex([]byte("changed-file-000"))
				entries[0].Metadata.Size = int64(len("changed-file-000"))
				entries[0].Metadata.MTime = time.Unix(1_700_001_100, 0).UTC()
				entries[0].StatFingerprint = "changed-000"
				uploads[entries[0].Path] = []byte("changed-file-000")
				return entries, uploads
			},
			applyPlans: true,
		},
		{
			name: "delete-only",
			prepare: func(tb *testing.B, root string) ([]proto.ManifestEntry, map[string][]byte) {
				entries, _ := benchmarkSeedWorkspace(tb, root, 64, 512, false)
				for idx := 0; idx < 8; idx++ {
					path := filepath.Join(root, "extra", benchmarkFileName(idx))
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						tb.Fatalf("MkdirAll(extra) error = %v", err)
					}
					if err := os.WriteFile(path, []byte("extra"), 0o644); err != nil {
						tb.Fatalf("WriteFile(extra) error = %v", err)
					}
				}
				return entries, map[string][]byte{}
			},
			applyPlans: true,
		},
		{
			name: "many-small-files",
			prepare: func(tb *testing.B, root string) ([]proto.ManifestEntry, map[string][]byte) {
				return benchmarkPrepareUploads(tb, 512, 128)
			},
			applyPlans: true,
		},
		{
			name: "few-large-files",
			prepare: func(tb *testing.B, root string) ([]proto.ManifestEntry, map[string][]byte) {
				return benchmarkPrepareUploads(tb, 4, 2*1024*1024)
			},
			applyPlans: true,
		},
	}

	for _, scenario := range scenarios {
		scenario := scenario
		b.Run(scenario.name, func(b *testing.B) {
			key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
			b.ResetTimer()
			for idx := 0; idx < b.N; idx++ {
				projectsRoot := b.TempDir()
				manager := NewWorkspaceManager(projectsRoot)
				layout, err := ResolveProjectLayout(projectsRoot, key)
				if err != nil {
					b.Fatalf("ResolveProjectLayout() error = %v", err)
				}
				if err := layout.EnsureDirectories(); err != nil {
					b.Fatalf("EnsureDirectories() error = %v", err)
				}

				entries, uploads := scenario.prepare(b, layout.WorkspaceDir)
				if err := manager.BeginSync(key, proto.BeginSyncRequest{
					SyncEpoch:       uint64(idx + 1),
					AttemptID:       1,
					RootFingerprint: "benchmark-root",
				}); err != nil {
					b.Fatalf("BeginSync() error = %v", err)
				}
				if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: entries}); err != nil {
					b.Fatalf("ScanManifest() error = %v", err)
				}
				hashPlan, err := manager.PlanManifestHashes(key)
				if err != nil {
					b.Fatalf("PlanManifestHashes() error = %v", err)
				}
				if len(hashPlan.Paths) != 0 {
					b.Fatalf("PlanManifestHashes() returned unexpected paths: %#v", hashPlan.Paths)
				}
				actions, err := manager.PlanSyncActions(key)
				if err != nil {
					b.Fatalf("PlanSyncActions() error = %v", err)
				}
				if scenario.applyPlans {
					for _, path := range actions.DeletePaths {
						if err := manager.DeletePath(key, proto.DeletePathRequest{
							SyncEpoch: uint64(idx + 1),
							Path:      path,
						}); err != nil {
							b.Fatalf("DeletePath(%s) error = %v", path, err)
						}
					}
					for _, path := range actions.UploadPaths {
						content := uploads[path]
						entry := benchmarkEntryByPath(entries, path)
						started, err := manager.BeginFile(key, proto.BeginFileRequest{
							SyncEpoch:       uint64(idx + 1),
							Path:            path,
							Metadata:        entry.Metadata,
							ExpectedSize:    entry.Metadata.Size,
							StatFingerprint: entry.StatFingerprint,
						})
						if err != nil {
							b.Fatalf("BeginFile(%s) error = %v", path, err)
						}
						if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
							FileID: started.FileID,
							Offset: 0,
							Data:   content,
						}); err != nil {
							b.Fatalf("ApplyChunk(%s) error = %v", path, err)
						}
						if err := manager.CommitFile(key, proto.CommitFileRequest{
							FileID:          started.FileID,
							FinalHash:       entry.ContentHash,
							FinalSize:       entry.Metadata.Size,
							MTime:           entry.Metadata.MTime,
							Mode:            entry.Metadata.Mode,
							StatFingerprint: entry.StatFingerprint,
						}); err != nil {
							b.Fatalf("CommitFile(%s) error = %v", path, err)
						}
					}
				}
				if err := manager.FinalizeSync(key, proto.FinalizeSyncRequest{
					SyncEpoch:   uint64(idx + 1),
					AttemptID:   1,
					GuardStable: true,
				}); err != nil {
					b.Fatalf("FinalizeSync() error = %v", err)
				}
			}
		})
	}
}

func benchmarkSeedWorkspace(tb *testing.B, workspaceRoot string, count int, size int, nested bool) ([]proto.ManifestEntry, map[string][]byte) {
	tb.Helper()
	entries := make([]proto.ManifestEntry, 0, count)
	for idx := 0; idx < count; idx++ {
		name := benchmarkFileName(idx)
		if nested {
			name = filepath.ToSlash(filepath.Join("nested", name))
		}
		content := bytes.Repeat([]byte{byte('a' + idx%26)}, size)
		path := filepath.Join(workspaceRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatalf("MkdirAll(%s) error = %v", name, err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			tb.Fatalf("WriteFile(%s) error = %v", name, err)
		}
		mtime := time.Unix(1_700_001_000+int64(idx), 0).UTC()
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			tb.Fatalf("Chtimes(%s) error = %v", name, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			tb.Fatalf("Stat(%s) error = %v", name, err)
		}
		entries = append(entries, proto.ManifestEntry{
			Path:            name,
			Metadata:        fileInfoMetadata(info),
			StatFingerprint: benchmarkFingerprint(name),
			ContentHash:     sha256Hex(content),
		})
	}
	return entries, map[string][]byte{}
}

func benchmarkPrepareUploads(tb *testing.B, count int, size int) ([]proto.ManifestEntry, map[string][]byte) {
	tb.Helper()
	entries := make([]proto.ManifestEntry, 0, count)
	uploads := make(map[string][]byte, count)
	for idx := 0; idx < count; idx++ {
		name := benchmarkFileName(idx)
		content := bytes.Repeat([]byte{byte('a' + idx%26)}, size)
		entries = append(entries, proto.ManifestEntry{
			Path: name,
			Metadata: proto.FileMetadata{
				Mode:  0o644,
				MTime: time.Unix(1_700_002_000+int64(idx), 0).UTC(),
				Size:  int64(len(content)),
			},
			StatFingerprint: benchmarkFingerprint(name),
			ContentHash:     sha256Hex(content),
		})
		uploads[name] = content
	}
	return entries, uploads
}

func benchmarkEntryByPath(entries []proto.ManifestEntry, path string) proto.ManifestEntry {
	for _, entry := range entries {
		if entry.Path == path {
			return entry
		}
	}
	return proto.ManifestEntry{}
}

func benchmarkFileName(idx int) string {
	return filepath.ToSlash(filepath.Join("bench", "file-"+time.Unix(int64(idx), 0).UTC().Format("150405")+".txt"))
}

func benchmarkFingerprint(path string) string {
	return "bench-" + filepath.Base(path)
}
