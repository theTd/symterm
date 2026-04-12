package sync

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestLocalOwnerFileServiceStreamsFileUploadToTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service := NewLocalOwnerFileService(root).(*localOwnerFileService)
	content := strings.Repeat("abcdef0123456789", 8192)
	mtime := time.Unix(1_700_000_500, 0).UTC()

	started, err := service.BeginFileUpload(context.Background(), proto.OwnerFileBeginRequest{
		Op:   proto.FsOpCreate,
		Path: "docs/large.bin",
		Metadata: proto.FileMetadata{
			Mode:  0o640,
			MTime: mtime,
			Size:  int64(len(content)),
		},
		ExpectedSize: int64(len(content)),
	})
	if err != nil {
		t.Fatalf("BeginFileUpload() error = %v", err)
	}

	var offset int64
	for start := 0; start < len(content); start += 17 * 1024 {
		end := start + 17*1024
		if end > len(content) {
			end = len(content)
		}
		if err := service.ApplyFileChunk(context.Background(), proto.OwnerFileApplyChunkRequest{
			UploadID: started.UploadID,
			Offset:   offset,
			Data:     []byte(content[start:end]),
		}); err != nil {
			t.Fatalf("ApplyFileChunk(offset=%d) error = %v", offset, err)
		}
		offset += int64(end - start)
	}

	if err := service.CommitFileUpload(context.Background(), proto.OwnerFileCommitRequest{
		UploadID: started.UploadID,
	}); err != nil {
		t.Fatalf("CommitFileUpload() error = %v", err)
	}

	target := filepath.Join(root, "docs", "large.bin")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != content {
		t.Fatalf("streamed file length = %d, want %d", len(data), len(content))
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("file mode = %o, want 640", info.Mode().Perm())
	}
	if !info.ModTime().UTC().Equal(mtime) {
		t.Fatalf("file mtime = %s, want %s", info.ModTime().UTC(), mtime)
	}
}

func TestLocalOwnerFileServiceAbortFileUploadRemovesHandle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service := NewLocalOwnerFileService(root).(*localOwnerFileService)

	started, err := service.BeginFileUpload(context.Background(), proto.OwnerFileBeginRequest{
		Op:   proto.FsOpCreate,
		Path: "note.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_501, 0).UTC(),
			Size:  4,
		},
		ExpectedSize: 4,
	})
	if err != nil {
		t.Fatalf("BeginFileUpload() error = %v", err)
	}
	if err := service.ApplyFileChunk(context.Background(), proto.OwnerFileApplyChunkRequest{
		UploadID: started.UploadID,
		Offset:   0,
		Data:     []byte("data"),
	}); err != nil {
		t.Fatalf("ApplyFileChunk() error = %v", err)
	}
	if err := service.AbortFileUpload(context.Background(), proto.OwnerFileAbortRequest{
		UploadID: started.UploadID,
		Reason:   "test abort",
	}); err != nil {
		t.Fatalf("AbortFileUpload() error = %v", err)
	}

	service.mu.Lock()
	remaining := len(service.uploads)
	service.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("remaining uploads = %d, want 0", remaining)
	}
	if _, err := os.Stat(filepath.Join(root, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("target exists after abort, err = %v", err)
	}
}
