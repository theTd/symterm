package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

type fakeOwnerFileClient struct {
	readFn       func(context.Context, proto.FsOperation, proto.FsRequest) (proto.FsReply, error)
	applyFn      func(context.Context, proto.OwnerFileApplyRequest) error
	beginFn      func(context.Context, proto.OwnerFileBeginRequest) (proto.OwnerFileBeginResponse, error)
	applyChunkFn func(context.Context, proto.OwnerFileApplyChunkRequest) error
	commitFn     func(context.Context, proto.OwnerFileCommitRequest) error
	abortFn      func(context.Context, proto.OwnerFileAbortRequest) error
	done         chan struct{}
	mu           sync.Mutex
	nextUploadID uint64
	uploads      map[string]*fakeOwnerUpload
}

type fakeOwnerUpload struct {
	request proto.OwnerFileBeginRequest
	data    []byte
}

var _ ownerfs.Client = (*fakeOwnerFileClient)(nil)

func TestPrepareMirrorDirectoryRootPreservesDirectoryAndClearsContents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(target, "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll(target) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(stale) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "nested", "child.txt"), []byte("child"), 0o644); err != nil {
		t.Fatalf("WriteFile(child) error = %v", err)
	}

	if err := prepareMirrorDirectoryRoot(target); err != nil {
		t.Fatalf("prepareMirrorDirectoryRoot() error = %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat(target) error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("target mode = %v, want directory", info.Mode())
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("ReadDir(target) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(entries))
	}
}

func (c *fakeOwnerFileClient) FsRead(ctx context.Context, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	if c.readFn == nil {
		return proto.FsReply{}, nil
	}
	return c.readFn(ctx, op, request)
}

func (c *fakeOwnerFileClient) Apply(ctx context.Context, request proto.OwnerFileApplyRequest) error {
	if c.applyFn == nil {
		return nil
	}
	return c.applyFn(ctx, request)
}

func (c *fakeOwnerFileClient) BeginFileUpload(ctx context.Context, request proto.OwnerFileBeginRequest) (proto.OwnerFileBeginResponse, error) {
	if c.beginFn != nil {
		return c.beginFn(ctx, request)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextUploadID++
	uploadID := "upload-" + time.Unix(0, int64(c.nextUploadID)).UTC().Format("150405.000000000")
	if c.uploads == nil {
		c.uploads = make(map[string]*fakeOwnerUpload)
	}
	c.uploads[uploadID] = &fakeOwnerUpload{request: request}
	return proto.OwnerFileBeginResponse{UploadID: uploadID}, nil
}

func (c *fakeOwnerFileClient) ApplyFileChunk(ctx context.Context, request proto.OwnerFileApplyChunkRequest) error {
	if c.applyChunkFn != nil {
		return c.applyChunkFn(ctx, request)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	upload, ok := c.uploads[request.UploadID]
	if !ok {
		return proto.NewError(proto.ErrUnknownFile, "fake owner upload handle does not exist")
	}
	if request.Offset != int64(len(upload.data)) {
		return proto.NewError(proto.ErrFileCommitFailed, "chunk offset is not contiguous")
	}
	upload.data = append(upload.data, request.Data...)
	return nil
}

func (c *fakeOwnerFileClient) CommitFileUpload(ctx context.Context, request proto.OwnerFileCommitRequest) error {
	if c.commitFn != nil {
		return c.commitFn(ctx, request)
	}

	c.mu.Lock()
	upload, ok := c.uploads[request.UploadID]
	if ok {
		delete(c.uploads, request.UploadID)
	}
	c.mu.Unlock()
	if !ok {
		return proto.NewError(proto.ErrUnknownFile, "fake owner upload handle does not exist")
	}
	if upload.request.ExpectedSize != int64(len(upload.data)) {
		return proto.NewError(proto.ErrFileCommitFailed, "uploaded file size does not match the expected size")
	}
	if c.applyFn == nil {
		return nil
	}
	return c.applyFn(ctx, proto.OwnerFileApplyRequest{
		Op:       upload.request.Op,
		Path:     upload.request.Path,
		Metadata: upload.request.Metadata,
		Data:     append([]byte(nil), upload.data...),
	})
}

func (c *fakeOwnerFileClient) AbortFileUpload(ctx context.Context, request proto.OwnerFileAbortRequest) error {
	if c.abortFn != nil {
		return c.abortFn(ctx, request)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.uploads[request.UploadID]; !ok {
		return proto.NewError(proto.ErrUnknownFile, "fake owner upload handle does not exist")
	}
	delete(c.uploads, request.UploadID)
	return nil
}

func (c *fakeOwnerFileClient) Done() <-chan struct{} {
	if c.done == nil {
		c.done = make(chan struct{})
	}
	return c.done
}

func (c *fakeOwnerFileClient) Close() error {
	return nil
}

func TestWorkspaceManagerSyncRoundTrip(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		AttemptID:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	entry := proto.ManifestEntry{
		Path: "hello.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_000, 0).UTC(),
			Size:  int64(len("hello workspace")),
		},
		StatFingerprint: "fp-1",
	}
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest() error = %v", err)
	}
	emptyDir := proto.ManifestEntry{
		Path:  "empty",
		IsDir: true,
		Metadata: proto.FileMetadata{
			Mode:  0o755,
			MTime: time.Unix(1_700_000_000, 0).UTC(),
			Size:  0,
		},
		StatFingerprint: "dir-1",
	}
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{emptyDir}}); err != nil {
		t.Fatalf("ScanManifest(dir) error = %v", err)
	}
	hashPlan, err := manager.PlanManifestHashes(key)
	if err != nil {
		t.Fatalf("PlanManifestHashes() error = %v", err)
	}
	if len(hashPlan.Paths) != 0 {
		t.Fatalf("PlanManifestHashes() = %#v, want no hashes for a missing remote file", hashPlan.Paths)
	}

	entry.ContentHash = "c8bfeab31e8dc628cc7f96b7ecd26bd1dd4264229a400702833e4e6a0af51f9f"
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest(with hash) error = %v", err)
	}
	needUpload, err := manager.PlanSyncActions(key)
	if err != nil {
		t.Fatalf("PlanSyncActions(after hash) error = %v", err)
	}
	if len(needUpload.UploadPaths) != 1 || needUpload.UploadPaths[0] != "hello.txt" || len(needUpload.DeletePaths) != 0 {
		t.Fatalf("PlanSyncActions(after hash) = %#v", needUpload)
	}

	started, err := manager.BeginFile(key, proto.BeginFileRequest{
		SyncEpoch:       1,
		Path:            "hello.txt",
		Metadata:        entry.Metadata,
		ExpectedSize:    entry.Metadata.Size,
		StatFingerprint: entry.StatFingerprint,
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}
	if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
		FileID: started.FileID,
		Offset: 0,
		Data:   []byte("hello workspace"),
	}); err != nil {
		t.Fatalf("ApplyChunk() error = %v", err)
	}
	if err := manager.CommitFile(key, proto.CommitFileRequest{
		FileID:          started.FileID,
		FinalHash:       entry.ContentHash,
		FinalSize:       entry.Metadata.Size,
		MTime:           entry.Metadata.MTime,
		Mode:            entry.Metadata.Mode,
		StatFingerprint: entry.StatFingerprint,
	}); err != nil {
		t.Fatalf("CommitFile() error = %v", err)
	}
	if err := manager.FinalizeSync(key, proto.FinalizeSyncRequest{
		SyncEpoch:   1,
		AttemptID:   1,
		GuardStable: true,
	}); err != nil {
		t.Fatalf("FinalizeSync() error = %v", err)
	}

	canonicalPath := filepath.Join(root, "alice", "demo", "workspace", "hello.txt")
	data, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "hello workspace" {
		t.Fatalf("file content = %q", string(data))
	}
	if info, err := os.Stat(filepath.Join(root, "alice", "demo", "workspace", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("expected empty dir to exist, err = %v", err)
	}
	mountPath := filepath.Join(root, "alice", "demo", "mount", "hello.txt")
	mountData, err := os.ReadFile(mountPath)
	if err != nil {
		t.Fatalf("ReadFile(mount) error = %v", err)
	}
	if string(mountData) != "hello workspace" {
		t.Fatalf("mount file content = %q", string(mountData))
	}
	if info, err := os.Stat(filepath.Join(root, "alice", "demo", "mount", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("expected mount empty dir to exist, err = %v", err)
	}
}

func TestWorkspaceManagerPlanSyncActionsIncludesStalePathsAndFinalizeSyncDoesNotDeleteThem(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "stale.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile(stale) error = %v", err)
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		AttemptID:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}

	actions, err := manager.PlanSyncActions(key)
	if err != nil {
		t.Fatalf("PlanSyncActions() error = %v", err)
	}
	if len(actions.DeletePaths) != 1 || actions.DeletePaths[0] != "stale.txt" || len(actions.UploadPaths) != 0 {
		t.Fatalf("PlanSyncActions() = %#v, want delete stale.txt", actions)
	}

	if err := manager.FinalizeSync(key, proto.FinalizeSyncRequest{
		SyncEpoch:   1,
		AttemptID:   1,
		GuardStable: true,
	}); err != nil {
		t.Fatalf("FinalizeSync() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(layout.WorkspaceDir, "stale.txt")); err != nil {
		t.Fatalf("stale path removed without explicit DeletePath: %v", err)
	}
}

func TestWorkspaceManagerFinalizeSyncSkipsMountMirrorWhenMountDoesNotRequireIt(t *testing.T) {
	original := publishedMountRequiresMirror
	publishedMountRequiresMirror = func(string) (bool, error) {
		return false, nil
	}
	defer func() {
		publishedMountRequiresMirror = original
	}()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount stale.txt) error = %v", err)
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		AttemptID:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	entry := proto.ManifestEntry{
		Path: "hello.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_000, 0).UTC(),
			Size:  int64(len("hello workspace")),
		},
		StatFingerprint: "fp-1",
		ContentHash:     "c8bfeab31e8dc628cc7f96b7ecd26bd1dd4264229a400702833e4e6a0af51f9f",
	}
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest() error = %v", err)
	}
	started, err := manager.BeginFile(key, proto.BeginFileRequest{
		SyncEpoch:       1,
		Path:            entry.Path,
		Metadata:        entry.Metadata,
		ExpectedSize:    entry.Metadata.Size,
		StatFingerprint: entry.StatFingerprint,
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}
	if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
		FileID: started.FileID,
		Offset: 0,
		Data:   []byte("hello workspace"),
	}); err != nil {
		t.Fatalf("ApplyChunk() error = %v", err)
	}
	if err := manager.CommitFile(key, proto.CommitFileRequest{
		FileID:          started.FileID,
		FinalHash:       entry.ContentHash,
		FinalSize:       entry.Metadata.Size,
		MTime:           entry.Metadata.MTime,
		Mode:            entry.Metadata.Mode,
		StatFingerprint: entry.StatFingerprint,
	}); err != nil {
		t.Fatalf("CommitFile() error = %v", err)
	}
	if err := manager.FinalizeSync(key, proto.FinalizeSyncRequest{
		SyncEpoch:   1,
		AttemptID:   1,
		GuardStable: true,
	}); err != nil {
		t.Fatalf("FinalizeSync() error = %v", err)
	}

	assertCommittedFileContent(t, layout.WorkspaceDir, "hello.txt", "hello workspace")
	if _, err := os.Stat(filepath.Join(layout.MountDir, "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("mount hello.txt exists after skipped mirror, err = %v", err)
	}
	assertCommittedFileContent(t, layout.MountDir, "stale.txt", "stale")
}

func TestWorkspaceManagerCreateSkipsMountMirrorWhenMountDoesNotRequireIt(t *testing.T) {
	original := publishedMountRequiresMirror
	publishedMountRequiresMirror = func(string) (bool, error) {
		return false, nil
	}
	defer func() {
		publishedMountRequiresMirror = original
	}()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	created, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "remote.txt",
		Mode: 0o644,
		Data: []byte("payload"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "remote.txt",
		HandleID: created.HandleID,
	}, nil); err != nil {
		t.Fatalf("FsMutation(flush create) error = %v", err)
	}

	assertCommittedFileContent(t, layout.WorkspaceDir, "remote.txt", "payload")
	if _, err := os.Stat(filepath.Join(layout.MountDir, "remote.txt")); !os.IsNotExist(err) {
		t.Fatalf("mount remote.txt exists after skipped mirror, err = %v", err)
	}
}

func TestWorkspaceManagerPlanSyncActionsIgnoresRuntimeAndCommands(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.RuntimeDir, "runtime.tmp"), []byte("runtime"), 0o644); err != nil {
		t.Fatalf("WriteFile(runtime.tmp) error = %v", err)
	}
	commandLayout, err := layout.ResolveCommandLayout("cmd-1")
	if err != nil {
		t.Fatalf("ResolveCommandLayout() error = %v", err)
	}
	if err := os.MkdirAll(commandLayout.Dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(command dir) error = %v", err)
	}
	if err := os.WriteFile(commandLayout.StdoutPath, []byte("stdout"), 0o644); err != nil {
		t.Fatalf("WriteFile(stdout.log) error = %v", err)
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		AttemptID:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}

	actions, err := manager.PlanSyncActions(key)
	if err != nil {
		t.Fatalf("PlanSyncActions() error = %v", err)
	}
	if len(actions.UploadPaths) != 0 || len(actions.DeletePaths) != 0 {
		t.Fatalf("PlanSyncActions() reported non-workspace paths: %#v", actions)
	}
}

func TestWorkspaceManagerDeletePathStaysWithinWorkspaceRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	workspaceRuntimeFile := filepath.Join(layout.WorkspaceDir, "runtime", "cache.txt")
	if err := os.MkdirAll(filepath.Dir(workspaceRuntimeFile), 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace runtime) error = %v", err)
	}
	if err := os.WriteFile(workspaceRuntimeFile, []byte("workspace-runtime"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace runtime) error = %v", err)
	}
	workspaceCommandsFile := filepath.Join(layout.WorkspaceDir, "commands", "stdout.log")
	if err := os.MkdirAll(filepath.Dir(workspaceCommandsFile), 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace commands) error = %v", err)
	}
	if err := os.WriteFile(workspaceCommandsFile, []byte("workspace-commands"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace commands) error = %v", err)
	}
	runtimeFile := filepath.Join(layout.RuntimeDir, "cache.txt")
	if err := os.WriteFile(runtimeFile, []byte("real-runtime"), 0o644); err != nil {
		t.Fatalf("WriteFile(runtime cache) error = %v", err)
	}
	commandLayout, err := layout.ResolveCommandLayout("cmd-1")
	if err != nil {
		t.Fatalf("ResolveCommandLayout() error = %v", err)
	}
	if err := os.MkdirAll(commandLayout.Dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(command dir) error = %v", err)
	}
	if err := os.WriteFile(commandLayout.StdoutPath, []byte("real-stdout"), 0o644); err != nil {
		t.Fatalf("WriteFile(stdout.log) error = %v", err)
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}

	if err := manager.DeletePath(key, proto.DeletePathRequest{
		SyncEpoch: 1,
		Path:      "runtime",
	}); err != nil {
		t.Fatalf("DeletePath(runtime) error = %v", err)
	}
	if err := manager.DeletePath(key, proto.DeletePathRequest{
		SyncEpoch: 1,
		Path:      "commands",
	}); err != nil {
		t.Fatalf("DeletePath(commands) error = %v", err)
	}
	if err := manager.FinalizeSync(key, proto.FinalizeSyncRequest{
		SyncEpoch:   1,
		GuardStable: true,
	}); err != nil {
		t.Fatalf("FinalizeSync() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(layout.WorkspaceDir, "runtime")); !os.IsNotExist(err) {
		t.Fatalf("workspace runtime path still exists, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.WorkspaceDir, "commands")); !os.IsNotExist(err) {
		t.Fatalf("workspace commands path still exists, err = %v", err)
	}
	if data, err := os.ReadFile(runtimeFile); err != nil || string(data) != "real-runtime" {
		t.Fatalf("runtime file mutated by DeletePath: %q, err = %v", string(data), err)
	}
	if data, err := os.ReadFile(commandLayout.StdoutPath); err != nil || string(data) != "real-stdout" {
		t.Fatalf("commands file mutated by DeletePath: %q, err = %v", string(data), err)
	}
}

func TestWorkspaceManagerCommitFileSupportsChunkedLargeUpload(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	content := strings.Repeat("0123456789abcdef", 8*1024)
	mtime := time.Unix(1_700_000_300, 0).UTC()
	entry := proto.ManifestEntry{
		Path: "large.bin",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: mtime,
			Size:  int64(len(content)),
		},
		StatFingerprint: "large-file",
		ContentHash:     sha256Hex([]byte(content)),
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest() error = %v", err)
	}

	started, err := manager.BeginFile(key, proto.BeginFileRequest{
		SyncEpoch:       1,
		Path:            entry.Path,
		Metadata:        entry.Metadata,
		ExpectedSize:    entry.Metadata.Size,
		StatFingerprint: entry.StatFingerprint,
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}

	offset := int64(0)
	for start := 0; start < len(content); start += 17 * 1024 {
		end := start + 17*1024
		if end > len(content) {
			end = len(content)
		}
		chunk := content[start:end]
		if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
			FileID: started.FileID,
			Offset: offset,
			Data:   []byte(chunk),
		}); err != nil {
			t.Fatalf("ApplyChunk(offset=%d) error = %v", offset, err)
		}
		offset += int64(len(chunk))
	}

	if err := manager.CommitFile(key, proto.CommitFileRequest{
		FileID:          started.FileID,
		FinalHash:       entry.ContentHash,
		FinalSize:       entry.Metadata.Size,
		MTime:           entry.Metadata.MTime,
		Mode:            entry.Metadata.Mode,
		StatFingerprint: entry.StatFingerprint,
	}); err != nil {
		t.Fatalf("CommitFile() error = %v", err)
	}
	if err := manager.FinalizeSync(key, proto.FinalizeSyncRequest{
		SyncEpoch:   1,
		GuardStable: true,
	}); err != nil {
		t.Fatalf("FinalizeSync() error = %v", err)
	}

	workspaceData, err := os.ReadFile(filepath.Join(layout.WorkspaceDir, entry.Path))
	if err != nil {
		t.Fatalf("ReadFile(workspace) error = %v", err)
	}
	if string(workspaceData) != content {
		t.Fatalf("workspace content length = %d, want %d", len(workspaceData), len(content))
	}
	mountData, err := os.ReadFile(filepath.Join(layout.MountDir, entry.Path))
	if err != nil {
		t.Fatalf("ReadFile(mount) error = %v", err)
	}
	if string(mountData) != content {
		t.Fatalf("mount content length = %d, want %d", len(mountData), len(content))
	}
}

func TestWorkspaceManagerCommitFileShorterUploadReplacesLongerFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	longContent := "this content is definitely longer than the replacement"
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte(longContent), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace note.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "note.txt"), []byte(longContent), 0o644); err != nil {
		t.Fatalf("WriteFile(mount note.txt) error = %v", err)
	}

	shortContent := "short"
	mtime := time.Unix(1_700_000_301, 0).UTC()
	entry := proto.ManifestEntry{
		Path: "note.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: mtime,
			Size:  int64(len(shortContent)),
		},
		StatFingerprint: "short-file",
		ContentHash:     sha256Hex([]byte(shortContent)),
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest() error = %v", err)
	}
	started, err := manager.BeginFile(key, proto.BeginFileRequest{
		SyncEpoch:       1,
		Path:            entry.Path,
		Metadata:        entry.Metadata,
		ExpectedSize:    entry.Metadata.Size,
		StatFingerprint: entry.StatFingerprint,
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}
	if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
		FileID: started.FileID,
		Offset: 0,
		Data:   []byte(shortContent),
	}); err != nil {
		t.Fatalf("ApplyChunk() error = %v", err)
	}
	if err := manager.CommitFile(key, proto.CommitFileRequest{
		FileID:          started.FileID,
		FinalHash:       entry.ContentHash,
		FinalSize:       entry.Metadata.Size,
		MTime:           entry.Metadata.MTime,
		Mode:            entry.Metadata.Mode,
		StatFingerprint: entry.StatFingerprint,
	}); err != nil {
		t.Fatalf("CommitFile() error = %v", err)
	}
	if err := manager.FinalizeSync(key, proto.FinalizeSyncRequest{
		SyncEpoch:   1,
		GuardStable: true,
	}); err != nil {
		t.Fatalf("FinalizeSync() error = %v", err)
	}

	workspaceData, err := os.ReadFile(filepath.Join(layout.WorkspaceDir, "note.txt"))
	if err != nil {
		t.Fatalf("ReadFile(workspace note.txt) error = %v", err)
	}
	if string(workspaceData) != shortContent {
		t.Fatalf("workspace note.txt = %q, want %q", string(workspaceData), shortContent)
	}
	mountData, err := os.ReadFile(filepath.Join(layout.MountDir, "note.txt"))
	if err != nil {
		t.Fatalf("ReadFile(mount note.txt) error = %v", err)
	}
	if string(mountData) != shortContent {
		t.Fatalf("mount note.txt = %q, want %q", string(mountData), shortContent)
	}
}

func TestWorkspaceManagerApplyChunkRejectsMissingChunkGap(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	started, err := manager.BeginFile(key, proto.BeginFileRequest{
		SyncEpoch:    1,
		Path:         "gap.txt",
		Metadata:     proto.FileMetadata{Mode: 0o644, MTime: time.Unix(1_700_000_302, 0).UTC(), Size: 6},
		ExpectedSize: 6,
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}

	err = manager.ApplyChunk(key, proto.ApplyChunkRequest{
		FileID: started.FileID,
		Offset: 3,
		Data:   []byte("def"),
	})
	if err == nil {
		t.Fatal("ApplyChunk() succeeded with missing leading chunk")
	}
	protoErr, ok := err.(*proto.Error)
	if !ok {
		t.Fatalf("ApplyChunk() error = %T, want *proto.Error", err)
	}
	if protoErr.Code != proto.ErrFileCommitFailed {
		t.Fatalf("ApplyChunk() error code = %q, want %q", protoErr.Code, proto.ErrFileCommitFailed)
	}
}

func TestWorkspaceManagerApplyChunkRejectsDuplicateChunkOffset(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	started, err := manager.BeginFile(key, proto.BeginFileRequest{
		SyncEpoch:    1,
		Path:         "dup.txt",
		Metadata:     proto.FileMetadata{Mode: 0o644, MTime: time.Unix(1_700_000_303, 0).UTC(), Size: 6},
		ExpectedSize: 6,
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}
	if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
		FileID: started.FileID,
		Offset: 0,
		Data:   []byte("abc"),
	}); err != nil {
		t.Fatalf("ApplyChunk(first) error = %v", err)
	}

	err = manager.ApplyChunk(key, proto.ApplyChunkRequest{
		FileID: started.FileID,
		Offset: 0,
		Data:   []byte("abc"),
	})
	if err == nil {
		t.Fatal("ApplyChunk() succeeded with duplicate chunk offset")
	}
	protoErr, ok := err.(*proto.Error)
	if !ok {
		t.Fatalf("ApplyChunk() error = %T, want *proto.Error", err)
	}
	if protoErr.Code != proto.ErrFileCommitFailed {
		t.Fatalf("ApplyChunk() error code = %q, want %q", protoErr.Code, proto.ErrFileCommitFailed)
	}
}

func TestWorkspaceManagerFinalizeSyncRejectsUnstableGuard(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}

	err := manager.FinalizeSync(key, proto.FinalizeSyncRequest{
		SyncEpoch:   1,
		GuardStable: false,
	})
	if err == nil {
		t.Fatal("FinalizeSync() succeeded with unstable guard state")
	}
	protoErr, ok := err.(*proto.Error)
	if !ok {
		t.Fatalf("FinalizeSync() error = %T, want *proto.Error", err)
	}
	if protoErr.Code != proto.ErrSyncRescanMismatch {
		t.Fatalf("FinalizeSync() error code = %q, want %q", protoErr.Code, proto.ErrSyncRescanMismatch)
	}
}

func TestWorkspaceManagerCommitFileDoesNotPublishCommittedViewBeforeFinalizeSync(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	seedManifestState(t, manager, key, layout, []proto.ManifestEntry{{
		Path: "note.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_050, 0).UTC(),
			Size:  int64(len("note")),
		},
		StatFingerprint: "note-v1",
	}})

	manager.mu.Lock()
	beforeState := manager.stateLocked(key)
	beforeGeneration := beforeState.committed.objectGen["note.txt"]
	beforeIdentity := beforeState.committed.objectIdentity["note.txt"]
	manager.mu.Unlock()

	content := "next"
	entry := proto.ManifestEntry{
		Path: "note.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_051, 0).UTC(),
			Size:  int64(len(content)),
		},
		StatFingerprint: "note-v2",
		ContentHash:     sha256Hex([]byte(content)),
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       2,
		RootFingerprint: "root-2",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest() error = %v", err)
	}
	started, err := manager.BeginFile(key, proto.BeginFileRequest{
		SyncEpoch:       2,
		Path:            entry.Path,
		Metadata:        entry.Metadata,
		ExpectedSize:    entry.Metadata.Size,
		StatFingerprint: entry.StatFingerprint,
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}
	if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
		FileID: started.FileID,
		Offset: 0,
		Data:   []byte(content),
	}); err != nil {
		t.Fatalf("ApplyChunk() error = %v", err)
	}
	if err := manager.CommitFile(key, proto.CommitFileRequest{
		FileID:          started.FileID,
		FinalHash:       entry.ContentHash,
		FinalSize:       entry.Metadata.Size,
		MTime:           entry.Metadata.MTime,
		Mode:            entry.Metadata.Mode,
		StatFingerprint: entry.StatFingerprint,
	}); err != nil {
		t.Fatalf("CommitFile() error = %v", err)
	}

	if data, err := os.ReadFile(filepath.Join(layout.WorkspaceDir, "note.txt")); err != nil || string(data) != "note" {
		t.Fatalf("workspace note.txt before finalize = %q, err = %v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(layout.MountDir, "note.txt")); err != nil || string(data) != "note" {
		t.Fatalf("mount note.txt before finalize = %q, err = %v", string(data), err)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.stateLocked(key)
	if state.committed.objectGen["note.txt"] != beforeGeneration {
		t.Fatalf("object generation before finalize = %d, want %d", state.committed.objectGen["note.txt"], beforeGeneration)
	}
	if state.committed.objectIdentity["note.txt"] != beforeIdentity {
		t.Fatalf("object identity before finalize = %q, want %q", state.committed.objectIdentity["note.txt"], beforeIdentity)
	}
}

func TestWorkspaceManagerDeletePathDoesNotPublishCommittedViewBeforeFinalizeSync(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	seedManifestState(t, manager, key, layout, []proto.ManifestEntry{{
		Path: "note.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_060, 0).UTC(),
			Size:  int64(len("note")),
		},
		StatFingerprint: "note-v1",
	}})

	manager.mu.Lock()
	beforeState := manager.stateLocked(key)
	beforeGeneration := beforeState.committed.objectGen["note.txt"]
	beforeIdentity := beforeState.committed.objectIdentity["note.txt"]
	manager.mu.Unlock()

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       2,
		RootFingerprint: "root-2",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	if err := manager.DeletePath(key, proto.DeletePathRequest{
		SyncEpoch: 2,
		Path:      "note.txt",
	}); err != nil {
		t.Fatalf("DeletePath() error = %v", err)
	}

	if data, err := os.ReadFile(filepath.Join(layout.WorkspaceDir, "note.txt")); err != nil || string(data) != "note" {
		t.Fatalf("workspace note.txt before finalize delete = %q, err = %v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(layout.MountDir, "note.txt")); err != nil || string(data) != "note" {
		t.Fatalf("mount note.txt before finalize delete = %q, err = %v", string(data), err)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.stateLocked(key)
	if state.committed.objectGen["note.txt"] != beforeGeneration {
		t.Fatalf("object generation before finalize delete = %d, want %d", state.committed.objectGen["note.txt"], beforeGeneration)
	}
	if state.committed.objectIdentity["note.txt"] != beforeIdentity {
		t.Fatalf("object identity before finalize delete = %q, want %q", state.committed.objectIdentity["note.txt"], beforeIdentity)
	}
}

func TestWorkspaceManagerFinalizeSyncFailureKeepsCommittedViewStable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	seedManifestState(t, manager, key, layout, []proto.ManifestEntry{{
		Path: "note.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_070, 0).UTC(),
			Size:  int64(len("note")),
		},
		StatFingerprint: "note-v1",
	}})

	manager.mu.Lock()
	beforeState := manager.stateLocked(key)
	beforeGeneration := beforeState.committed.objectGen["note.txt"]
	beforeIdentity := beforeState.committed.objectIdentity["note.txt"]
	manager.mu.Unlock()

	content := "next"
	entry := proto.ManifestEntry{
		Path: "note.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_071, 0).UTC(),
			Size:  int64(len(content)),
		},
		StatFingerprint: "note-v2",
		ContentHash:     sha256Hex([]byte(content)),
	}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       2,
		RootFingerprint: "root-2",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
		t.Fatalf("ScanManifest() error = %v", err)
	}
	started, err := manager.BeginFile(key, proto.BeginFileRequest{
		SyncEpoch:       2,
		Path:            entry.Path,
		Metadata:        entry.Metadata,
		ExpectedSize:    entry.Metadata.Size,
		StatFingerprint: entry.StatFingerprint,
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}
	if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
		FileID: started.FileID,
		Offset: 0,
		Data:   []byte(content),
	}); err != nil {
		t.Fatalf("ApplyChunk() error = %v", err)
	}
	if err := manager.CommitFile(key, proto.CommitFileRequest{
		FileID:          started.FileID,
		FinalHash:       entry.ContentHash,
		FinalSize:       entry.Metadata.Size,
		MTime:           entry.Metadata.MTime,
		Mode:            entry.Metadata.Mode,
		StatFingerprint: entry.StatFingerprint,
	}); err != nil {
		t.Fatalf("CommitFile() error = %v", err)
	}

	err = manager.FinalizeSync(key, proto.FinalizeSyncRequest{
		SyncEpoch:   2,
		GuardStable: false,
	})
	if err == nil {
		t.Fatal("FinalizeSync() succeeded with unstable guard state")
	}

	if data, err := os.ReadFile(filepath.Join(layout.WorkspaceDir, "note.txt")); err != nil || string(data) != "note" {
		t.Fatalf("workspace note.txt after failed finalize = %q, err = %v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(layout.MountDir, "note.txt")); err != nil || string(data) != "note" {
		t.Fatalf("mount note.txt after failed finalize = %q, err = %v", string(data), err)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.stateLocked(key)
	if state.committed.objectGen["note.txt"] != beforeGeneration {
		t.Fatalf("object generation after failed finalize = %d, want %d", state.committed.objectGen["note.txt"], beforeGeneration)
	}
	if state.committed.objectIdentity["note.txt"] != beforeIdentity {
		t.Fatalf("object identity after failed finalize = %q, want %q", state.committed.objectIdentity["note.txt"], beforeIdentity)
	}
	if state.sync.activeEpoch != 2 {
		t.Fatalf("active sync epoch after failed finalize = %d, want 2", state.sync.activeEpoch)
	}
	if _, ok := state.sync.sessions[2]; !ok {
		t.Fatal("sync session was discarded after failed finalize")
	}
}

func TestWorkspaceManagerAbortFileCleansUploadSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}

	started, err := manager.BeginFile(key, proto.BeginFileRequest{
		SyncEpoch:    1,
		Path:         "hello.txt",
		Metadata:     proto.FileMetadata{Mode: 0o644, MTime: time.Unix(1_700_000_000, 0).UTC(), Size: 5},
		ExpectedSize: 5,
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}
	if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
		FileID: started.FileID,
		Offset: 0,
		Data:   []byte("hello"),
	}); err != nil {
		t.Fatalf("ApplyChunk() error = %v", err)
	}

	manager.mu.Lock()
	upload := manager.stateLocked(key).sync.sessions[1].uploads[started.FileID]
	tempPath := upload.tempPath
	manager.mu.Unlock()

	if err := manager.AbortFile(key, proto.AbortFileRequest{
		FileID: started.FileID,
		Reason: "client canceled",
	}); err != nil {
		t.Fatalf("AbortFile() error = %v", err)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("temp upload still exists after AbortFile(), err = %v", err)
	}

	manager.mu.Lock()
	_, ok := manager.stateLocked(key).sync.sessions[1].uploads[started.FileID]
	manager.mu.Unlock()
	if ok {
		t.Fatal("upload handle was retained after AbortFile()")
	}
}

func TestWorkspaceManagerCommitFileHashMismatchCleansUploadSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		RootFingerprint: "root-1",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}

	started, err := manager.BeginFile(key, proto.BeginFileRequest{
		SyncEpoch:    1,
		Path:         "hello.txt",
		Metadata:     proto.FileMetadata{Mode: 0o644, MTime: time.Unix(1_700_000_000, 0).UTC(), Size: 5},
		ExpectedSize: 5,
	})
	if err != nil {
		t.Fatalf("BeginFile() error = %v", err)
	}
	if err := manager.ApplyChunk(key, proto.ApplyChunkRequest{
		FileID: started.FileID,
		Offset: 0,
		Data:   []byte("hello"),
	}); err != nil {
		t.Fatalf("ApplyChunk() error = %v", err)
	}

	manager.mu.Lock()
	upload := manager.stateLocked(key).sync.sessions[1].uploads[started.FileID]
	tempPath := upload.tempPath
	manager.mu.Unlock()

	err = manager.CommitFile(key, proto.CommitFileRequest{
		FileID:    started.FileID,
		FinalHash: strings.Repeat("0", 64),
		FinalSize: 5,
		MTime:     time.Unix(1_700_000_000, 0).UTC(),
		Mode:      0o644,
	})
	if err == nil {
		t.Fatal("CommitFile() succeeded with mismatched hash")
	}
	protoErr, ok := err.(*proto.Error)
	if !ok {
		t.Fatalf("CommitFile() error = %T, want *proto.Error", err)
	}
	if protoErr.Code != proto.ErrFileCommitFailed {
		t.Fatalf("CommitFile() error code = %q, want %q", protoErr.Code, proto.ErrFileCommitFailed)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("temp upload still exists after CommitFile() hash mismatch, err = %v", err)
	}

	manager.mu.Lock()
	_, ok = manager.stateLocked(key).sync.sessions[1].uploads[started.FileID]
	manager.mu.Unlock()
	if ok {
		t.Fatal("upload handle was retained after hash mismatch")
	}
}

func TestWorkspaceManagerFsMutationStagesAndCommitsOnFlush(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	reply, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}
	if reply.HandleID == "" {
		t.Fatal("HandleID is empty")
	}

	stagedRead, err := manager.FsRead(key, proto.FsOpRead, proto.FsRequest{
		Path:     "note.txt",
		HandleID: reply.HandleID,
		Size:     16,
	})
	if err != nil {
		t.Fatalf("FsRead(staged) error = %v", err)
	}
	if string(stagedRead.Data) != "v1" {
		t.Fatalf("staged read = %q", string(stagedRead.Data))
	}
	if _, err := manager.FsRead(key, proto.FsOpRead, proto.FsRequest{
		Path: "note.txt",
		Size: 16,
	}); err == nil {
		t.Fatal("committed read succeeded before flush")
	}

	flushed, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: reply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(flush) error = %v", err)
	}
	if flushed.ObjectGeneration == 0 {
		t.Fatal("ObjectGeneration = 0 after flush")
	}
	if len(flushed.Invalidations) != 2 || flushed.Invalidations[0].Kind != proto.InvalidateData {
		t.Fatalf("flush invalidations = %#v", flushed.Invalidations)
	}
	assertInvalidationKinds(t, flushed.Invalidations,
		proto.InvalidateChange{Path: "note.txt", Kind: proto.InvalidateData},
		proto.InvalidateChange{Path: "", Kind: proto.InvalidateDentry},
	)

	readReply, err := manager.FsRead(key, proto.FsOpRead, proto.FsRequest{
		Path: "note.txt",
		Size: 16,
	})
	if err != nil {
		t.Fatalf("FsRead(committed) error = %v", err)
	}
	if string(readReply.Data) != "v1" {
		t.Fatalf("committed read = %q", string(readReply.Data))
	}
}

func TestWorkspaceManagerOpenReturnsHandleAndFsyncsSubsequentWrites(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace note.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "note.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount note.txt) error = %v", err)
	}

	opened, err := manager.FsMutation(key, proto.FsOpOpen, proto.FsRequest{
		Path: "note.txt",
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(open) error = %v", err)
	}
	if opened.HandleID == "" {
		t.Fatal("open HandleID is empty")
	}
	if state := handleState(t, manager, key, opened.HandleID); state != stagedHandleClean {
		t.Fatalf("handle state after open = %q, want %q", state, stagedHandleClean)
	}

	readReply, err := manager.FsRead(key, proto.FsOpRead, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
		Size:     16,
	})
	if err != nil {
		t.Fatalf("FsRead(open handle) error = %v", err)
	}
	if string(readReply.Data) != "v1" {
		t.Fatalf("open-handle read = %q", string(readReply.Data))
	}

	written, err := manager.FsMutation(key, proto.FsOpWrite, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
		Offset:   0,
		Data:     []byte("v2"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(write after open) error = %v", err)
	}
	if written.HandleID != opened.HandleID {
		t.Fatalf("write HandleID = %q, want %q", written.HandleID, opened.HandleID)
	}
	if state := handleState(t, manager, key, opened.HandleID); state != stagedHandleDirty {
		t.Fatalf("handle state after write = %q, want %q", state, stagedHandleDirty)
	}

	if data, err := os.ReadFile(filepath.Join(layout.WorkspaceDir, "note.txt")); err != nil || string(data) != "v1" {
		t.Fatalf("workspace note.txt before fsync = %q, err = %v", string(data), err)
	}

	fsynced, err := manager.FsMutation(key, proto.FsOpFSync, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(fsync) error = %v", err)
	}
	if fsynced.HandleID != opened.HandleID {
		t.Fatalf("fsync HandleID = %q, want %q", fsynced.HandleID, opened.HandleID)
	}
	if state := handleState(t, manager, key, opened.HandleID); state != stagedHandleCommitted {
		t.Fatalf("handle state after fsync = %q, want %q", state, stagedHandleCommitted)
	}

	if data, err := os.ReadFile(filepath.Join(layout.WorkspaceDir, "note.txt")); err != nil || string(data) != "v2" {
		t.Fatalf("workspace note.txt after fsync = %q, err = %v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(layout.MountDir, "note.txt")); err != nil || string(data) != "v2" {
		t.Fatalf("mount note.txt after fsync = %q, err = %v", string(data), err)
	}

	released, err := manager.FsMutation(key, proto.FsOpRelease, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(release) error = %v", err)
	}
	if released.HandleID != opened.HandleID {
		t.Fatalf("release HandleID = %q, want %q", released.HandleID, opened.HandleID)
	}
	if _, err := manager.FsMutation(key, proto.FsOpRelease, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
	}, nil); err != nil {
		t.Fatalf("FsMutation(release repeat) error = %v", err)
	}
}

func TestWorkspaceManagerFsReadSupportsRootDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(note.txt) error = %v", err)
	}
	if err := syncPublishedWorkspace(layout); err != nil {
		t.Fatalf("syncPublishedWorkspace() error = %v", err)
	}

	reply, err := manager.FsRead(key, proto.FsOpReadDir, proto.FsRequest{})
	if err != nil {
		t.Fatalf("FsRead(root readdir) error = %v", err)
	}
	if string(reply.Data) != "note.txt" {
		t.Fatalf("root readdir = %q", string(reply.Data))
	}

	attrReply, err := manager.FsRead(key, proto.FsOpGetAttr, proto.FsRequest{})
	if err != nil {
		t.Fatalf("FsRead(root getattr) error = %v", err)
	}
	if !attrReply.IsDir {
		t.Fatal("root getattr did not report directory")
	}
}

func TestWorkspaceManagerFsReadUsesAuthoritativeClient(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace note.txt) error = %v", err)
	}

	client := &fakeOwnerFileClient{
		readFn: func(_ context.Context, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
			if op != proto.FsOpRead || request.Path != "note.txt" {
				t.Fatalf("FsRead(op=%q, path=%q)", op, request.Path)
			}
			return proto.FsReply{Data: []byte("owner-view")}, nil
		},
	}
	if err := manager.SetAuthoritativeClient(key, client); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	reply, err := manager.FsRead(key, proto.FsOpRead, proto.FsRequest{
		Path: "note.txt",
		Size: 32,
	})
	if err != nil {
		t.Fatalf("FsRead() error = %v", err)
	}
	if string(reply.Data) != "owner-view" {
		t.Fatalf("FsRead().Data = %q", string(reply.Data))
	}
}

func TestWorkspaceManagerFsReadContextCancelsAuthoritativeClient(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	enterRead := make(chan struct{}, 1)
	client := &fakeOwnerFileClient{
		readFn: func(ctx context.Context, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
			if op != proto.FsOpRead || request.Path != "note.txt" {
				t.Fatalf("FsRead(op=%q, path=%q)", op, request.Path)
			}
			enterRead <- struct{}{}
			<-ctx.Done()
			return proto.FsReply{}, ctx.Err()
		},
	}
	if err := manager.SetAuthoritativeClient(key, client); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := manager.FsReadContext(ctx, key, proto.FsOpRead, proto.FsRequest{
			Path: "note.txt",
			Size: 32,
		})
		errCh <- err
	}()

	select {
	case <-enterRead:
	case <-time.After(time.Second):
		t.Fatal("authoritative read did not start")
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FsReadContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FsReadContext() did not return after cancellation")
	}
}

func TestWorkspaceManagerOpenSeedsWritableHandleFromAuthoritativeClient(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte("stale-daemon-copy"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace note.txt) error = %v", err)
	}

	var (
		ownerMu    sync.Mutex
		ownerData  = []byte("owner-current")
		ownerMTime = time.Unix(1_700_100_000, 0).UTC()
	)
	client := &fakeOwnerFileClient{
		readFn: func(_ context.Context, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
			ownerMu.Lock()
			defer ownerMu.Unlock()

			switch op {
			case proto.FsOpGetAttr:
				return proto.FsReply{
					Metadata: proto.FileMetadata{
						Mode:  0o644,
						MTime: ownerMTime,
						Size:  int64(len(ownerData)),
					},
				}, nil
			case proto.FsOpRead:
				if request.Offset >= int64(len(ownerData)) {
					return proto.FsReply{}, nil
				}
				size := int(request.Size)
				if size <= 0 {
					size = len(ownerData)
				}
				start := int(request.Offset)
				end := start + size
				if end > len(ownerData) {
					end = len(ownerData)
				}
				return proto.FsReply{Data: append([]byte(nil), ownerData[start:end]...)}, nil
			default:
				t.Fatalf("FsRead(op=%q, path=%q)", op, request.Path)
				return proto.FsReply{}, nil
			}
		},
	}
	if err := manager.SetAuthoritativeClient(key, client); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	opened, err := manager.FsMutation(key, proto.FsOpOpen, proto.FsRequest{Path: "note.txt"}, nil)
	if err != nil {
		t.Fatalf("FsMutation(open) error = %v", err)
	}
	if backing := handleBackingKind(t, manager, key, opened.HandleID); backing != stagedHandleBackingStagedTemp {
		t.Fatalf("handle backing = %q, want %q", backing, stagedHandleBackingStagedTemp)
	}

	reply, err := manager.FsRead(key, proto.FsOpRead, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
		Size:     64,
	})
	if err != nil {
		t.Fatalf("FsRead(open handle) error = %v", err)
	}
	if string(reply.Data) != "owner-current" {
		t.Fatalf("FsRead(open handle).Data = %q, want owner-current", string(reply.Data))
	}
}

func TestWorkspaceManagerReadOnlyOwnerHandleSeesOwnerUpdates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	var (
		ownerMu    sync.Mutex
		ownerData  = []byte("owner-v1")
		ownerMTime = time.Unix(1_700_200_000, 0).UTC()
	)
	client := &fakeOwnerFileClient{
		readFn: func(_ context.Context, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
			ownerMu.Lock()
			defer ownerMu.Unlock()

			switch op {
			case proto.FsOpGetAttr:
				return proto.FsReply{
					Metadata: proto.FileMetadata{
						Mode:  0o644,
						MTime: ownerMTime,
						Size:  int64(len(ownerData)),
					},
				}, nil
			case proto.FsOpRead:
				if request.Offset >= int64(len(ownerData)) {
					return proto.FsReply{}, nil
				}
				size := int(request.Size)
				if size <= 0 {
					size = len(ownerData)
				}
				start := int(request.Offset)
				end := start + size
				if end > len(ownerData) {
					end = len(ownerData)
				}
				return proto.FsReply{Data: append([]byte(nil), ownerData[start:end]...)}, nil
			default:
				t.Fatalf("FsRead(op=%q, path=%q)", op, request.Path)
				return proto.FsReply{}, nil
			}
		},
	}
	if err := manager.SetAuthoritativeClient(key, client); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	opened, err := manager.FsMutation(key, proto.FsOpOpen, proto.FsRequest{
		Path:       "note.txt",
		OpenIntent: proto.FsOpenIntentReadOnly,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(open readonly) error = %v", err)
	}
	if backing := handleBackingKind(t, manager, key, opened.HandleID); backing != stagedHandleBackingOwnerReadThrough {
		t.Fatalf("handle backing = %q, want %q", backing, stagedHandleBackingOwnerReadThrough)
	}

	read := func() string {
		reply, err := manager.FsRead(key, proto.FsOpRead, proto.FsRequest{
			Path:     "note.txt",
			HandleID: opened.HandleID,
			Size:     64,
		})
		if err != nil {
			t.Fatalf("FsRead(open readonly handle) error = %v", err)
		}
		return string(reply.Data)
	}

	if got := read(); got != "owner-v1" {
		t.Fatalf("initial readonly-handle read = %q, want owner-v1", got)
	}

	ownerMu.Lock()
	ownerData = []byte("owner-v2-expanded")
	ownerMTime = ownerMTime.Add(2 * time.Second)
	ownerMu.Unlock()

	if got := read(); got != "owner-v2-expanded" {
		t.Fatalf("updated readonly-handle read = %q, want owner-v2-expanded", got)
	}

	attrReply, err := manager.FsRead(key, proto.FsOpGetAttr, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
	})
	if err != nil {
		t.Fatalf("FsRead(getattr readonly handle) error = %v", err)
	}
	if attrReply.Metadata.Size != int64(len("owner-v2-expanded")) {
		t.Fatalf("FsRead(getattr readonly handle).Metadata.Size = %d, want %d", attrReply.Metadata.Size, len("owner-v2-expanded"))
	}
	if !attrReply.Metadata.MTime.Equal(ownerMTime) {
		t.Fatalf("FsRead(getattr readonly handle).Metadata.MTime = %v, want %v", attrReply.Metadata.MTime, ownerMTime)
	}
}

func TestWorkspaceManagerReadOnlyOwnerHandlePromotesToStagedWrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte("stale-daemon-copy"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace note.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "note.txt"), []byte("stale-daemon-copy"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount note.txt) error = %v", err)
	}

	var (
		ownerMu    sync.Mutex
		ownerData  = []byte("owner-v1")
		ownerMTime = time.Unix(1_700_300_000, 0).UTC()
	)
	client := &fakeOwnerFileClient{
		readFn: func(_ context.Context, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
			ownerMu.Lock()
			defer ownerMu.Unlock()

			switch op {
			case proto.FsOpGetAttr:
				return proto.FsReply{
					Metadata: proto.FileMetadata{
						Mode:  0o644,
						MTime: ownerMTime,
						Size:  int64(len(ownerData)),
					},
				}, nil
			case proto.FsOpRead:
				if request.Offset >= int64(len(ownerData)) {
					return proto.FsReply{}, nil
				}
				size := int(request.Size)
				if size <= 0 {
					size = len(ownerData)
				}
				start := int(request.Offset)
				end := start + size
				if end > len(ownerData) {
					end = len(ownerData)
				}
				return proto.FsReply{Data: append([]byte(nil), ownerData[start:end]...)}, nil
			default:
				t.Fatalf("FsRead(op=%q, path=%q)", op, request.Path)
				return proto.FsReply{}, nil
			}
		},
		applyFn: func(_ context.Context, request proto.OwnerFileApplyRequest) error {
			ownerMu.Lock()
			defer ownerMu.Unlock()

			ownerData = append([]byte(nil), request.Data...)
			ownerMTime = request.Metadata.MTime
			return nil
		},
	}
	if err := manager.SetAuthoritativeClient(key, client); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	opened, err := manager.FsMutation(key, proto.FsOpOpen, proto.FsRequest{
		Path:       "note.txt",
		OpenIntent: proto.FsOpenIntentReadOnly,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(open readonly) error = %v", err)
	}

	ownerMu.Lock()
	ownerData = []byte("owner-v2")
	ownerMTime = ownerMTime.Add(2 * time.Second)
	ownerMu.Unlock()

	if _, err := manager.FsMutation(key, proto.FsOpWrite, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
		Offset:   int64(len("owner-v2")),
		Data:     []byte("-tail"),
	}, nil); err != nil {
		t.Fatalf("FsMutation(write) error = %v", err)
	}
	if backing := handleBackingKind(t, manager, key, opened.HandleID); backing != stagedHandleBackingStagedTemp {
		t.Fatalf("handle backing after write = %q, want %q", backing, stagedHandleBackingStagedTemp)
	}

	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
	}, nil); err != nil {
		t.Fatalf("FsMutation(flush) error = %v", err)
	}

	assertCommittedFileContent(t, layout.WorkspaceDir, "note.txt", "owner-v2-tail")
	assertCommittedFileContent(t, layout.MountDir, "note.txt", "owner-v2-tail")

	ownerMu.Lock()
	defer ownerMu.Unlock()
	if string(ownerData) != "owner-v2-tail" {
		t.Fatalf("owner data after flush = %q, want owner-v2-tail", string(ownerData))
	}
}

func TestWorkspaceManagerFlushWrapsAuthoritativeApplyFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := manager.SetAuthoritativeClient(key, &fakeOwnerFileClient{
		applyFn: func(context.Context, proto.OwnerFileApplyRequest) error {
			return errors.New("disk full")
		},
	}); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	reply, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}

	_, err = manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: reply.HandleID,
	}, nil)
	if err == nil {
		t.Fatal("FsMutation(flush) succeeded unexpectedly")
	}
	protoErr, ok := err.(*proto.Error)
	if !ok {
		t.Fatalf("FsMutation(flush) error = %T, want *proto.Error", err)
	}
	if protoErr.Code != proto.ErrOwnerWriteFailed {
		t.Fatalf("FsMutation(flush) error code = %q, want %q", protoErr.Code, proto.ErrOwnerWriteFailed)
	}
	if _, err := os.Stat(filepath.Join(layout.WorkspaceDir, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("workspace note.txt changed after owner apply failure, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.MountDir, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("mount note.txt changed after owner apply failure, err = %v", err)
	}
	manager.mu.Lock()
	generation := manager.stateLocked(key).committed.objectGen["note.txt"]
	manager.mu.Unlock()
	if generation != 0 {
		t.Fatalf("object generation after owner apply failure = %d, want 0", generation)
	}
}

func TestWorkspaceManagerFlushContextCancelsOwnerApply(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	enterApply := make(chan struct{}, 1)
	if err := manager.SetAuthoritativeClient(key, &fakeOwnerFileClient{
		beginFn: func(ctx context.Context, request proto.OwnerFileBeginRequest) (proto.OwnerFileBeginResponse, error) {
			if request.Path != "note.txt" {
				t.Fatalf("BeginFileUpload().Path = %q, want note.txt", request.Path)
			}
			enterApply <- struct{}{}
			<-ctx.Done()
			return proto.OwnerFileBeginResponse{}, ctx.Err()
		},
	}); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	reply, err := manager.FsMutationContext(context.Background(), key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutationContext(create) error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, flushErr := manager.FsMutationContext(ctx, key, proto.FsOpFlush, proto.FsRequest{
			Path:     "note.txt",
			HandleID: reply.HandleID,
		}, nil)
		errCh <- flushErr
	}()

	select {
	case <-enterApply:
	case <-time.After(time.Second):
		t.Fatal("owner apply did not start")
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FsMutationContext(flush) error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FsMutationContext(flush) did not return after cancellation")
	}
}

func TestWorkspaceManagerEnterConservativeModeKeepsCommittedGenerationsStable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	manager.mu.Lock()
	state := manager.stateLocked(key)
	state.syncManifestStateLocked(map[string]proto.ManifestEntry{
		"docs": {
			Path:  "docs",
			IsDir: true,
		},
		"docs/readme.txt": {
			Path: "docs/readme.txt",
		},
	})
	state.committed.dirGen["docs"] = 4
	state.committed.objectGen["docs/readme.txt"] = 7
	beforeObject := state.committed.objectGen["docs/readme.txt"]
	beforeDir := state.committed.dirGen["docs"]
	manager.mu.Unlock()

	changes, err := manager.EnterConservativeMode(key, "watch failed")
	if err != nil {
		t.Fatalf("EnterConservativeMode() error = %v", err)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state = manager.stateLocked(key)
	if !state.validation.conservative {
		t.Fatal("conservative = false, want true")
	}
	if state.committed.objectGen["docs/readme.txt"] != beforeObject {
		t.Fatalf("object generation = %d, want %d", state.committed.objectGen["docs/readme.txt"], beforeObject)
	}
	if state.committed.dirGen["docs"] != beforeDir {
		t.Fatalf("dir generation = %d, want %d", state.committed.dirGen["docs"], beforeDir)
	}
	if len(changes) == 0 {
		t.Fatal("EnterConservativeMode() returned no invalidations")
	}
}

func TestWorkspaceManagerEnterConservativeModeContextCancelsOwnerObservation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	enterRead := make(chan struct{}, 1)

	manager.mu.Lock()
	state := manager.stateLocked(key)
	state.committed.objectGen["note.txt"] = 1
	manager.mu.Unlock()

	if err := manager.SetAuthoritativeClient(key, &fakeOwnerFileClient{
		readFn: func(ctx context.Context, _ proto.FsOperation, _ proto.FsRequest) (proto.FsReply, error) {
			enterRead <- struct{}{}
			<-ctx.Done()
			return proto.FsReply{}, ctx.Err()
		},
	}); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := manager.EnterConservativeModeContext(ctx, key, "watch failed")
		errCh <- err
	}()

	select {
	case <-enterRead:
	case <-time.After(time.Second):
		t.Fatal("owner observation did not start")
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("EnterConservativeModeContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("EnterConservativeModeContext() did not return after cancellation")
	}
}

func TestWorkspaceManagerConservativeModeRevalidatesReadContentHash(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	mtime := time.Unix(1_700_000_200, 0).UTC()
	target := filepath.Join(layout.WorkspaceDir, "note.txt")
	if err := os.WriteFile(target, []byte("one"), 0o644); err != nil {
		t.Fatalf("WriteFile(note.txt initial) error = %v", err)
	}
	if err := os.Chtimes(target, mtime, mtime); err != nil {
		t.Fatalf("Chtimes(note.txt initial) error = %v", err)
	}

	manager.mu.Lock()
	state := manager.stateLocked(key)
	state.committed.objectGen["note.txt"] = 5
	state.committed.dirGen[""] = 2
	state.committed.objectIdentity["note.txt"] = "obj-1"
	state.committed.nextObjectID = 1
	manager.mu.Unlock()

	if _, err := manager.EnterConservativeMode(key, "watch failed"); err != nil {
		t.Fatalf("EnterConservativeMode() error = %v", err)
	}

	manager.mu.Lock()
	beforeRead := manager.stateLocked(key).committed.objectGen["note.txt"]
	manager.mu.Unlock()

	if err := os.WriteFile(target, []byte("two"), 0o644); err != nil {
		t.Fatalf("WriteFile(note.txt updated) error = %v", err)
	}
	if err := os.Chtimes(target, mtime, mtime); err != nil {
		t.Fatalf("Chtimes(note.txt updated) error = %v", err)
	}

	reply, err := manager.FsRead(key, proto.FsOpRead, proto.FsRequest{
		Path: "note.txt",
		Size: 16,
	})
	if err != nil {
		t.Fatalf("FsRead() error = %v", err)
	}
	if string(reply.Data) != "two" {
		t.Fatalf("FsRead().Data = %q", string(reply.Data))
	}
	if reply.ObjectGeneration <= beforeRead {
		t.Fatalf("ObjectGeneration = %d, want > %d", reply.ObjectGeneration, beforeRead)
	}
}

func TestWorkspaceManagerConservativeModeRevalidatesMutationPreconditions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	mtime := time.Unix(1_700_000_300, 0).UTC()
	target := filepath.Join(layout.WorkspaceDir, "note.txt")
	if err := os.WriteFile(target, []byte("one"), 0o644); err != nil {
		t.Fatalf("WriteFile(note.txt initial) error = %v", err)
	}
	if err := os.Chtimes(target, mtime, mtime); err != nil {
		t.Fatalf("Chtimes(note.txt initial) error = %v", err)
	}

	manager.mu.Lock()
	state := manager.stateLocked(key)
	state.committed.objectGen["note.txt"] = 9
	state.committed.dirGen[""] = 4
	state.committed.objectIdentity["note.txt"] = "obj-1"
	state.committed.nextObjectID = 1
	manager.mu.Unlock()

	if _, err := manager.EnterConservativeMode(key, "watch failed"); err != nil {
		t.Fatalf("EnterConservativeMode() error = %v", err)
	}

	manager.mu.Lock()
	staleGeneration := manager.stateLocked(key).committed.objectGen["note.txt"]
	manager.mu.Unlock()

	if err := os.WriteFile(target, []byte("two"), 0o644); err != nil {
		t.Fatalf("WriteFile(note.txt updated) error = %v", err)
	}
	if err := os.Chtimes(target, mtime, mtime); err != nil {
		t.Fatalf("Chtimes(note.txt updated) error = %v", err)
	}

	reply, err := manager.FsMutation(key, proto.FsOpWrite, proto.FsRequest{
		Path:   "note.txt",
		Offset: 0,
		Data:   []byte("new"),
	}, []proto.MutationPrecondition{{
		Path:             "note.txt",
		ObjectGeneration: staleGeneration,
	}})
	if err == nil {
		t.Fatal("FsMutation() succeeded with stale conservative generation")
	}
	if !reply.Conflict {
		t.Fatalf("FsMutation() reply = %#v, want conflict", reply)
	}
}

func TestWorkspaceManagerConservativeModeRevalidatesDirectoryListing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	docsDir := filepath.Join(layout.WorkspaceDir, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(docs) error = %v", err)
	}

	manager.mu.Lock()
	state := manager.stateLocked(key)
	state.committed.objectGen["docs"] = 3
	state.committed.dirGen[""] = 2
	manager.mu.Unlock()

	if _, err := manager.EnterConservativeMode(key, "watch failed"); err != nil {
		t.Fatalf("EnterConservativeMode() error = %v", err)
	}

	manager.mu.Lock()
	beforeDir := manager.stateLocked(key).committed.dirGen["docs"]
	manager.mu.Unlock()

	if err := os.WriteFile(filepath.Join(docsDir, "readme.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile(readme) error = %v", err)
	}

	reply, err := manager.FsRead(key, proto.FsOpReadDir, proto.FsRequest{Path: "docs"})
	if err != nil {
		t.Fatalf("FsRead(readdir) error = %v", err)
	}
	if string(reply.Data) != "readme.txt" {
		t.Fatalf("FsRead(readdir).Data = %q", string(reply.Data))
	}
	if reply.DirGeneration <= beforeDir {
		t.Fatalf("DirGeneration = %d, want > %d", reply.DirGeneration, beforeDir)
	}
}

func TestWorkspaceManagerApplyOwnerInvalidationsBumpsConflictMetadata(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	seedManifestState(t, manager, key, layout, []proto.ManifestEntry{{
		Path: "note.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_100, 0).UTC(),
			Size:  4,
		},
		StatFingerprint: "note-file",
	}})

	manager.mu.Lock()
	state := manager.stateLocked(key)
	beforeObject := state.committed.objectGen["note.txt"]
	beforeDir := state.committed.dirGen[""]
	identity := state.committed.objectIdentity["note.txt"]
	manager.mu.Unlock()

	if err := manager.ApplyOwnerInvalidations(key, []proto.InvalidateChange{
		{Path: "note.txt", Kind: proto.InvalidateData},
		{Path: "", Kind: proto.InvalidateDentry},
	}); err != nil {
		t.Fatalf("ApplyOwnerInvalidations() error = %v", err)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state = manager.stateLocked(key)
	if state.committed.objectGen["note.txt"] <= beforeObject {
		t.Fatalf("object generation = %d, want > %d", state.committed.objectGen["note.txt"], beforeObject)
	}
	if state.committed.dirGen[""] <= beforeDir {
		t.Fatalf("dir generation = %d, want > %d", state.committed.dirGen[""], beforeDir)
	}
	if state.committed.objectIdentity["note.txt"] != identity {
		t.Fatalf("object identity changed unexpectedly: %q -> %q", identity, state.committed.objectIdentity["note.txt"])
	}
}

func TestWorkspaceManagerApplyOwnerInvalidationsMovesRenameIdentity(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	seedManifestState(t, manager, key, layout, []proto.ManifestEntry{{
		Path: "old.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_100, 0).UTC(),
			Size:  3,
		},
		StatFingerprint: "old-file",
	}})

	manager.mu.Lock()
	state := manager.stateLocked(key)
	oldIdentity := state.committed.objectIdentity["old.txt"]
	beforeRootDir := state.committed.dirGen[""]
	manager.mu.Unlock()

	if err := manager.ApplyOwnerInvalidations(key, []proto.InvalidateChange{
		{Path: "old.txt", NewPath: "new.txt", Kind: proto.InvalidateRename},
		{Path: "", Kind: proto.InvalidateDentry},
	}); err != nil {
		t.Fatalf("ApplyOwnerInvalidations(rename) error = %v", err)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state = manager.stateLocked(key)
	if state.committed.objectIdentity["old.txt"] != "" {
		t.Fatalf("old identity still present: %q", state.committed.objectIdentity["old.txt"])
	}
	if state.committed.objectIdentity["new.txt"] != oldIdentity {
		t.Fatalf("new identity = %q, want %q", state.committed.objectIdentity["new.txt"], oldIdentity)
	}
	if state.committed.objectGen["new.txt"] == 0 || state.committed.objectGen["old.txt"] == 0 {
		t.Fatalf("rename generations not bumped: old=%d new=%d", state.committed.objectGen["old.txt"], state.committed.objectGen["new.txt"])
	}
	if state.committed.dirGen[""] <= beforeRootDir {
		t.Fatalf("root dir generation = %d, want > %d", state.committed.dirGen[""], beforeRootDir)
	}
}

func TestWorkspaceManagerApplyOwnerInvalidationsKeepsCommittedStateStableInConservativeMode(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	seedManifestState(t, manager, key, layout, []proto.ManifestEntry{{
		Path: "note.txt",
		Metadata: proto.FileMetadata{
			Mode:  0o644,
			MTime: time.Unix(1_700_000_100, 0).UTC(),
			Size:  4,
		},
		StatFingerprint: "note-file",
	}})

	if _, err := manager.EnterConservativeMode(key, "watch failed"); err != nil {
		t.Fatalf("EnterConservativeMode() error = %v", err)
	}

	manager.mu.Lock()
	state := manager.stateLocked(key)
	beforeObject := state.committed.objectGen["note.txt"]
	beforeDir := state.committed.dirGen[""]
	beforeIdentity := state.committed.objectIdentity["note.txt"]
	manager.mu.Unlock()

	if err := manager.ApplyOwnerInvalidations(key, []proto.InvalidateChange{
		{Path: "note.txt", Kind: proto.InvalidateData},
		{Path: "", Kind: proto.InvalidateDentry},
	}); err != nil {
		t.Fatalf("ApplyOwnerInvalidations() error = %v", err)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state = manager.stateLocked(key)
	if state.committed.objectGen["note.txt"] != beforeObject {
		t.Fatalf("object generation = %d, want %d", state.committed.objectGen["note.txt"], beforeObject)
	}
	if state.committed.dirGen[""] != beforeDir {
		t.Fatalf("dir generation = %d, want %d", state.committed.dirGen[""], beforeDir)
	}
	if state.committed.objectIdentity["note.txt"] != beforeIdentity {
		t.Fatalf("object identity changed unexpectedly: %q -> %q", beforeIdentity, state.committed.objectIdentity["note.txt"])
	}
}

func TestWorkspaceManagerRemoteFlushPublishesAfterOwnerApply(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	var began proto.OwnerFileBeginRequest
	var streamed []byte
	var chunkCount int
	client := &fakeOwnerFileClient{
		applyFn: func(context.Context, proto.OwnerFileApplyRequest) error {
			t.Fatal("Apply() should not be used for streamed file flush")
			return nil
		},
		beginFn: func(_ context.Context, request proto.OwnerFileBeginRequest) (proto.OwnerFileBeginResponse, error) {
			began = request
			return proto.OwnerFileBeginResponse{UploadID: "upload-0001"}, nil
		},
		applyChunkFn: func(_ context.Context, request proto.OwnerFileApplyChunkRequest) error {
			chunkCount++
			streamed = append(streamed, request.Data...)
			return nil
		},
		commitFn: func(_ context.Context, request proto.OwnerFileCommitRequest) error {
			if request.UploadID != "upload-0001" {
				t.Fatalf("CommitFileUpload().UploadID = %q, want upload-0001", request.UploadID)
			}
			return nil
		},
	}
	if err := manager.SetAuthoritativeClient(key, client); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	created, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("remote-owner"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: created.HandleID,
	}, nil); err != nil {
		t.Fatalf("FsMutation(flush) error = %v", err)
	}

	if began.Op != proto.FsOpCreate || began.Path != "note.txt" || began.ExpectedSize != int64(len("remote-owner")) {
		t.Fatalf("owner begin upload request = %#v", began)
	}
	if string(streamed) != "remote-owner" {
		t.Fatalf("streamed owner content = %q", string(streamed))
	}
	if chunkCount != 1 {
		t.Fatalf("chunk count = %d, want 1", chunkCount)
	}
	data, err := os.ReadFile(filepath.Join(layout.WorkspaceDir, "note.txt"))
	if err != nil {
		t.Fatalf("ReadFile(workspace note.txt) error = %v", err)
	}
	if string(data) != "remote-owner" {
		t.Fatalf("workspace note.txt = %q", string(data))
	}
	mountData, err := os.ReadFile(filepath.Join(layout.MountDir, "note.txt"))
	if err != nil {
		t.Fatalf("ReadFile(mount note.txt) error = %v", err)
	}
	if string(mountData) != "remote-owner" {
		t.Fatalf("mount note.txt = %q", string(mountData))
	}
}

func TestWorkspaceManagerRemoteFlushStreamsLargeFileToOwner(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	content := strings.Repeat("0123456789abcdef", ownerFileUploadChunkSize/16+9)
	var began proto.OwnerFileBeginRequest
	var chunkCount int
	var streamed []byte

	if err := manager.SetAuthoritativeClient(key, &fakeOwnerFileClient{
		applyFn: func(context.Context, proto.OwnerFileApplyRequest) error {
			t.Fatal("Apply() should not be used for large streamed file flush")
			return nil
		},
		beginFn: func(_ context.Context, request proto.OwnerFileBeginRequest) (proto.OwnerFileBeginResponse, error) {
			began = request
			return proto.OwnerFileBeginResponse{UploadID: "upload-large"}, nil
		},
		applyChunkFn: func(_ context.Context, request proto.OwnerFileApplyChunkRequest) error {
			chunkCount++
			if len(request.Data) > ownerFileUploadChunkSize {
				t.Fatalf("chunk size = %d, want <= %d", len(request.Data), ownerFileUploadChunkSize)
			}
			streamed = append(streamed, request.Data...)
			return nil
		},
		commitFn: func(_ context.Context, request proto.OwnerFileCommitRequest) error {
			if request.UploadID != "upload-large" {
				t.Fatalf("CommitFileUpload().UploadID = %q, want upload-large", request.UploadID)
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	reply, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "large.bin",
		Mode: 0o644,
		Data: []byte(content),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "large.bin",
		HandleID: reply.HandleID,
	}, nil); err != nil {
		t.Fatalf("FsMutation(flush) error = %v", err)
	}

	if began.Path != "large.bin" || began.ExpectedSize != int64(len(content)) {
		t.Fatalf("owner begin upload request = %#v", began)
	}
	if chunkCount < 2 {
		t.Fatalf("chunk count = %d, want at least 2", chunkCount)
	}
	if string(streamed) != content {
		t.Fatalf("streamed content length = %d, want %d", len(streamed), len(content))
	}
}

func TestWorkspaceManagerFlushAcknowledgesOnlyAfterOwnerApplyAndGenerationBump(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	enterApply := make(chan struct{}, 1)
	releaseApply := make(chan struct{})
	if err := manager.SetAuthoritativeClient(key, &fakeOwnerFileClient{
		commitFn: func(context.Context, proto.OwnerFileCommitRequest) error {
			enterApply <- struct{}{}
			<-releaseApply
			return nil
		},
	}); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	created, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("remote-owner"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}

	flushReplyCh := make(chan proto.FsReply, 1)
	flushErrCh := make(chan error, 1)
	go func() {
		reply, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
			Path:     "note.txt",
			HandleID: created.HandleID,
		}, nil)
		flushReplyCh <- reply
		flushErrCh <- err
	}()

	select {
	case <-enterApply:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for owner apply")
	}

	manager.mu.Lock()
	inFlightGeneration := manager.stateLocked(key).committed.objectGen["note.txt"]
	manager.mu.Unlock()
	if inFlightGeneration != 0 {
		t.Fatalf("object generation before owner ack = %d, want 0", inFlightGeneration)
	}
	select {
	case <-flushErrCh:
		t.Fatal("flush returned before owner apply completed")
	default:
	}

	close(releaseApply)

	reply := <-flushReplyCh
	if err := <-flushErrCh; err != nil {
		t.Fatalf("FsMutation(flush) error = %v", err)
	}
	if reply.ObjectGeneration == 0 {
		t.Fatalf("flush reply object generation = %d, want > 0", reply.ObjectGeneration)
	}

	manager.mu.Lock()
	finalGeneration := manager.stateLocked(key).committed.objectGen["note.txt"]
	manager.mu.Unlock()
	if finalGeneration != reply.ObjectGeneration {
		t.Fatalf("stored object generation = %d, want %d", finalGeneration, reply.ObjectGeneration)
	}
}

func TestWorkspaceManagerReplaceFailurePreservesCommittedView(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	manager.hooks.replaceFileFromStaged = func(string, string, proto.FileMetadata) error {
		return errors.New("rename failed")
	}
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte("stable"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace note.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "note.txt"), []byte("stable"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount note.txt) error = %v", err)
	}

	opened, err := manager.FsMutation(key, proto.FsOpOpen, proto.FsRequest{Path: "note.txt"}, nil)
	if err != nil {
		t.Fatalf("FsMutation(open) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpWrite, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
		Offset:   0,
		Data:     []byte("new"),
	}, nil); err != nil {
		t.Fatalf("FsMutation(write) error = %v", err)
	}

	_, err = manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
	}, nil)
	if err == nil {
		t.Fatal("FsMutation(flush) succeeded with replace failure")
	}
	assertCommittedFileContent(t, layout.WorkspaceDir, "note.txt", "stable")
	assertCommittedFileContent(t, layout.MountDir, "note.txt", "stable")
	manager.mu.Lock()
	generation := manager.stateLocked(key).committed.objectGen["note.txt"]
	manager.mu.Unlock()
	if generation != 0 {
		t.Fatalf("object generation after replace failure = %d, want 0", generation)
	}
}

func TestWorkspaceManagerGenerationFailureRollsBackCommittedView(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	manager.hooks.beforeFileGeneration = func(path string) error {
		if path != "note.txt" {
			t.Fatalf("beforeFileGeneration(path=%q)", path)
		}
		return errors.New("generation failed")
	}
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte("stable"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace note.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "note.txt"), []byte("stable"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount note.txt) error = %v", err)
	}

	opened, err := manager.FsMutation(key, proto.FsOpOpen, proto.FsRequest{Path: "note.txt"}, nil)
	if err != nil {
		t.Fatalf("FsMutation(open) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpWrite, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
		Offset:   0,
		Data:     []byte("new"),
	}, nil); err != nil {
		t.Fatalf("FsMutation(write) error = %v", err)
	}

	_, err = manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: opened.HandleID,
	}, nil)
	if err == nil {
		t.Fatal("FsMutation(flush) succeeded with generation failure")
	}
	assertCommittedFileContent(t, layout.WorkspaceDir, "note.txt", "stable")
	assertCommittedFileContent(t, layout.MountDir, "note.txt", "stable")
	manager.mu.Lock()
	generation := manager.stateLocked(key).committed.objectGen["note.txt"]
	manager.mu.Unlock()
	if generation != 0 {
		t.Fatalf("object generation after generation failure = %d, want 0", generation)
	}
}

func TestWorkspaceManagerFlushDetectsConflictAfterStaging(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	created, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}
	created, err = manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: created.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(flush create) error = %v", err)
	}

	staged, err := manager.FsMutation(key, proto.FsOpWrite, proto.FsRequest{
		Path:   "note.txt",
		Offset: 0,
		Data:   []byte("v2"),
	}, []proto.MutationPrecondition{{
		Path:             "note.txt",
		ObjectGeneration: created.ObjectGeneration,
	}})
	if err != nil {
		t.Fatalf("FsMutation(write) error = %v", err)
	}

	conflicting, err := manager.FsMutation(key, proto.FsOpWrite, proto.FsRequest{
		Path:   "note.txt",
		Offset: 0,
		Data:   []byte("zz"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(write conflicting) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: conflicting.HandleID,
	}, nil); err != nil {
		t.Fatalf("FsMutation(flush conflicting) error = %v", err)
	}

	_, err = manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: staged.HandleID,
	}, nil)
	if err == nil {
		t.Fatal("FsMutation(flush staged) succeeded with stale generation")
	}

	readReply, err := manager.FsRead(key, proto.FsOpRead, proto.FsRequest{
		Path: "note.txt",
		Size: 16,
	})
	if err != nil {
		t.Fatalf("FsRead(committed) error = %v", err)
	}
	if string(readReply.Data) != "zz" {
		t.Fatalf("committed read = %q", string(readReply.Data))
	}
}

func TestWorkspaceManagerDentryMutationsStageUntilFlush(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	authoritativeRoot := filepath.Join(root, "authoritative")
	if err := os.MkdirAll(authoritativeRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(authoritativeRoot) error = %v", err)
	}
	if err := manager.SetAuthoritativeRoot(key, authoritativeRoot); err != nil {
		t.Fatalf("SetAuthoritativeRoot() error = %v", err)
	}

	mkdirReply, err := manager.FsMutation(key, proto.FsOpMkdir, proto.FsRequest{
		Path: "docs",
		Mode: 0o755,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(mkdir) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(authoritativeRoot, "docs")); !os.IsNotExist(err) {
		t.Fatalf("authoritative mkdir committed before flush, err = %v", err)
	}

	stagedDir, err := manager.FsRead(key, proto.FsOpReadDir, proto.FsRequest{
		Path:     "docs",
		HandleID: mkdirReply.HandleID,
	})
	if err != nil {
		t.Fatalf("FsRead(staged mkdir) error = %v", err)
	}
	if string(stagedDir.Data) != "" {
		t.Fatalf("staged dir listing = %q", string(stagedDir.Data))
	}
	if _, err := manager.FsRead(key, proto.FsOpReadDir, proto.FsRequest{Path: "docs"}); err == nil {
		t.Fatal("committed readdir succeeded before mkdir flush")
	}

	flushedMkdir, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "docs",
		HandleID: mkdirReply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(flush mkdir) error = %v", err)
	}
	if flushedMkdir.ObjectGeneration == 0 {
		t.Fatal("mkdir ObjectGeneration = 0 after flush")
	}
	if info, err := os.Stat(filepath.Join(authoritativeRoot, "docs")); err != nil || !info.IsDir() {
		t.Fatalf("authoritative docs dir missing after flush, err = %v", err)
	}
	if info, err := os.Stat(filepath.Join(layout.MountDir, "docs")); err != nil || !info.IsDir() {
		t.Fatalf("mount docs dir missing after flush, err = %v", err)
	}

	sourceData := []byte("payload")
	if err := os.WriteFile(filepath.Join(authoritativeRoot, "old.txt"), sourceData, 0o644); err != nil {
		t.Fatalf("WriteFile(authoritative old.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "old.txt"), sourceData, 0o644); err != nil {
		t.Fatalf("WriteFile(mount old.txt) error = %v", err)
	}

	renameReply, err := manager.FsMutation(key, proto.FsOpRename, proto.FsRequest{
		Path:    "old.txt",
		NewPath: "renamed.txt",
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(rename) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(authoritativeRoot, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("authoritative rename committed before flush, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(authoritativeRoot, "old.txt")); err != nil {
		t.Fatalf("authoritative old.txt missing before flush, err = %v", err)
	}

	renamed, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "old.txt",
		NewPath:  "renamed.txt",
		HandleID: renameReply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(flush rename) error = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(authoritativeRoot, "renamed.txt")); err != nil || string(data) != "payload" {
		t.Fatalf("authoritative renamed.txt = %q, err = %v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(layout.MountDir, "renamed.txt")); err != nil || string(data) != "payload" {
		t.Fatalf("mount renamed.txt = %q, err = %v", string(data), err)
	}
	if len(renamed.Invalidations) == 0 || renamed.Invalidations[0].Kind != proto.InvalidateRename {
		t.Fatalf("rename invalidations = %#v", renamed.Invalidations)
	}
	assertInvalidationKinds(t, renamed.Invalidations,
		proto.InvalidateChange{Path: "old.txt", NewPath: "renamed.txt", Kind: proto.InvalidateRename},
		proto.InvalidateChange{Path: "", Kind: proto.InvalidateDentry},
	)
	if _, err := os.Stat(filepath.Join(authoritativeRoot, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("authoritative old.txt still exists after rename, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.MountDir, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("mount old.txt still exists after rename, err = %v", err)
	}

	repeatedRename, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "old.txt",
		NewPath:  "renamed.txt",
		HandleID: renameReply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(flush rename repeat) error = %v", err)
	}
	if repeatedRename.ObjectGeneration != renamed.ObjectGeneration || repeatedRename.DirGeneration != renamed.DirGeneration {
		t.Fatalf("repeat rename flush = %#v, want %#v", repeatedRename, renamed)
	}

	releasedRename, err := manager.FsMutation(key, proto.FsOpRelease, proto.FsRequest{
		Path:     "old.txt",
		NewPath:  "renamed.txt",
		HandleID: renameReply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(release rename) error = %v", err)
	}
	if releasedRename.ObjectGeneration != renamed.ObjectGeneration || releasedRename.DirGeneration != renamed.DirGeneration {
		t.Fatalf("release rename = %#v, want %#v", releasedRename, renamed)
	}
	repeatedRelease, err := manager.FsMutation(key, proto.FsOpRelease, proto.FsRequest{
		Path:     "old.txt",
		NewPath:  "renamed.txt",
		HandleID: renameReply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(release rename repeat) error = %v", err)
	}
	if repeatedRelease.ObjectGeneration != renamed.ObjectGeneration || repeatedRelease.DirGeneration != renamed.DirGeneration {
		t.Fatalf("repeat rename release = %#v, want %#v", repeatedRelease, renamed)
	}

	removeReply, err := manager.FsMutation(key, proto.FsOpRemove, proto.FsRequest{
		Path: "renamed.txt",
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(remove) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(authoritativeRoot, "renamed.txt")); err != nil {
		t.Fatalf("authoritative renamed.txt missing before remove flush, err = %v", err)
	}

	removed, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "renamed.txt",
		HandleID: removeReply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(flush remove) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(authoritativeRoot, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("authoritative renamed.txt still exists after remove, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.MountDir, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("mount renamed.txt still exists after remove, err = %v", err)
	}
	if removed.ObjectGeneration == 0 {
		t.Fatal("remove ObjectGeneration = 0 after flush")
	}
	if len(removed.Invalidations) == 0 || removed.Invalidations[0].Kind != proto.InvalidateDelete {
		t.Fatalf("remove invalidations = %#v", removed.Invalidations)
	}
	assertInvalidationKinds(t, removed.Invalidations,
		proto.InvalidateChange{Path: "renamed.txt", Kind: proto.InvalidateDelete},
		proto.InvalidateChange{Path: "", Kind: proto.InvalidateDentry},
	)
}

func TestWorkspaceManagerReleasePreservesFailedOutcome(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	created, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}
	created, err = manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: created.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(flush create) error = %v", err)
	}

	staged, err := manager.FsMutation(key, proto.FsOpWrite, proto.FsRequest{
		Path:   "note.txt",
		Offset: 0,
		Data:   []byte("v2"),
	}, []proto.MutationPrecondition{{
		Path:             "note.txt",
		ObjectGeneration: created.ObjectGeneration,
	}})
	if err != nil {
		t.Fatalf("FsMutation(write staged) error = %v", err)
	}

	conflicting, err := manager.FsMutation(key, proto.FsOpWrite, proto.FsRequest{
		Path:   "note.txt",
		Offset: 0,
		Data:   []byte("zz"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(write conflicting) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: conflicting.HandleID,
	}, nil); err != nil {
		t.Fatalf("FsMutation(flush conflicting) error = %v", err)
	}

	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "note.txt",
		HandleID: staged.HandleID,
	}, nil); err == nil {
		t.Fatal("FsMutation(flush staged) succeeded with stale generation")
	} else {
		if state := handleState(t, manager, key, staged.HandleID); state != stagedHandleFailed {
			t.Fatalf("handle state after failed flush = %q, want %q", state, stagedHandleFailed)
		}
		firstErr := err.Error()
		if _, err := manager.FsMutation(key, proto.FsOpRelease, proto.FsRequest{
			Path:     "note.txt",
			HandleID: staged.HandleID,
		}, nil); err == nil {
			t.Fatal("FsMutation(release staged) succeeded without conflict")
		} else if err.Error() != firstErr {
			t.Fatalf("release err = %v, want %q", err, firstErr)
		}
		if _, err := manager.FsMutation(key, proto.FsOpRelease, proto.FsRequest{
			Path:     "note.txt",
			HandleID: staged.HandleID,
		}, nil); err == nil {
			t.Fatal("FsMutation(release staged repeat) succeeded without conflict")
		} else if err.Error() != firstErr {
			t.Fatalf("repeat release err = %v, want %q", err, firstErr)
		}
	}
}

func TestWorkspaceManagerFlushTransitionsDirtyHandleThroughCommitting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	enterApply := make(chan struct{}, 1)
	releaseApply := make(chan struct{})
	if err := manager.SetAuthoritativeClient(key, &fakeOwnerFileClient{
		commitFn: func(context.Context, proto.OwnerFileCommitRequest) error {
			enterApply <- struct{}{}
			<-releaseApply
			return nil
		},
	}); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	reply, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}
	if state := handleState(t, manager, key, reply.HandleID); state != stagedHandleDirty {
		t.Fatalf("handle state after create = %q, want %q", state, stagedHandleDirty)
	}

	flushDone := make(chan error, 1)
	go func() {
		_, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
			Path:     "note.txt",
			HandleID: reply.HandleID,
		}, nil)
		flushDone <- err
	}()

	select {
	case <-enterApply:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for owner apply")
	}

	if state := handleState(t, manager, key, reply.HandleID); state != stagedHandleCommitting {
		t.Fatalf("handle state during flush = %q, want %q", state, stagedHandleCommitting)
	}

	close(releaseApply)
	if err := <-flushDone; err != nil {
		t.Fatalf("FsMutation(flush) error = %v", err)
	}
	if state := handleState(t, manager, key, reply.HandleID); state != stagedHandleCommitted {
		t.Fatalf("handle state after flush = %q, want %q", state, stagedHandleCommitted)
	}
}

func TestWorkspaceManagerConcurrentFlushSharesSingleCommitTransaction(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	enterCommit := make(chan int, 2)
	releaseCommit := make(chan struct{})
	var commitMu sync.Mutex
	commitCalls := 0
	if err := manager.SetAuthoritativeClient(key, &fakeOwnerFileClient{
		commitFn: func(context.Context, proto.OwnerFileCommitRequest) error {
			commitMu.Lock()
			commitCalls++
			count := commitCalls
			commitMu.Unlock()
			enterCommit <- count
			<-releaseCommit
			return nil
		},
	}); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	reply, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}

	type flushOutcome struct {
		reply proto.FsReply
		err   error
	}

	firstDone := make(chan flushOutcome, 1)
	go func() {
		reply, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
			Path:     "note.txt",
			HandleID: reply.HandleID,
		}, nil)
		firstDone <- flushOutcome{reply: reply, err: err}
	}()

	select {
	case count := <-enterCommit:
		if count != 1 {
			t.Fatalf("first commit call count = %d, want 1", count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first commit")
	}

	secondDone := make(chan flushOutcome, 1)
	go func() {
		reply, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
			Path:     "note.txt",
			HandleID: reply.HandleID,
		}, nil)
		secondDone <- flushOutcome{reply: reply, err: err}
	}()

	select {
	case count := <-enterCommit:
		t.Fatalf("second flush started a duplicate commit transaction: %d", count)
	case <-time.After(200 * time.Millisecond):
	}

	close(releaseCommit)

	first := <-firstDone
	if first.err != nil {
		t.Fatalf("first flush error = %v", first.err)
	}
	second := <-secondDone
	if second.err != nil {
		t.Fatalf("second flush error = %v", second.err)
	}
	if first.reply.ObjectGeneration != second.reply.ObjectGeneration || first.reply.DirGeneration != second.reply.DirGeneration {
		t.Fatalf("flush replies diverged: first=%#v second=%#v", first.reply, second.reply)
	}
	commitMu.Lock()
	finalCalls := commitCalls
	commitMu.Unlock()
	if finalCalls != 1 {
		t.Fatalf("commit call count = %d, want 1", finalCalls)
	}
}

func TestWorkspaceManagerReleaseWaitsForCommitTransactionResult(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	enterCommit := make(chan struct{}, 1)
	releaseCommit := make(chan struct{})
	if err := manager.SetAuthoritativeClient(key, &fakeOwnerFileClient{
		commitFn: func(context.Context, proto.OwnerFileCommitRequest) error {
			enterCommit <- struct{}{}
			<-releaseCommit
			return nil
		},
	}); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	reply, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}

	type mutationOutcome struct {
		reply proto.FsReply
		err   error
	}

	flushDone := make(chan mutationOutcome, 1)
	go func() {
		reply, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
			Path:     "note.txt",
			HandleID: reply.HandleID,
		}, nil)
		flushDone <- mutationOutcome{reply: reply, err: err}
	}()

	select {
	case <-enterCommit:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for commit start")
	}

	releaseDone := make(chan mutationOutcome, 1)
	go func() {
		reply, err := manager.FsMutation(key, proto.FsOpRelease, proto.FsRequest{
			Path:     "note.txt",
			HandleID: reply.HandleID,
		}, nil)
		releaseDone <- mutationOutcome{reply: reply, err: err}
	}()

	select {
	case outcome := <-releaseDone:
		t.Fatalf("release returned before commit completed: %#v err=%v", outcome.reply, outcome.err)
	case <-time.After(200 * time.Millisecond):
	}

	close(releaseCommit)

	flushed := <-flushDone
	if flushed.err != nil {
		t.Fatalf("flush error = %v", flushed.err)
	}
	released := <-releaseDone
	if released.err != nil {
		t.Fatalf("release error = %v", released.err)
	}
	if flushed.reply.ObjectGeneration != released.reply.ObjectGeneration || flushed.reply.DirGeneration != released.reply.DirGeneration {
		t.Fatalf("release result = %#v, want %#v", released.reply, flushed.reply)
	}
	if data, err := os.ReadFile(filepath.Join(layout.MountDir, "note.txt")); err != nil || string(data) != "v1" {
		t.Fatalf("mount note.txt after release = %q, err = %v", string(data), err)
	}
}

func TestWorkspaceManagerReleaseContextCancelsWaitingForCommitTransaction(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}

	enterCommit := make(chan struct{}, 1)
	releaseCommit := make(chan struct{})
	if err := manager.SetAuthoritativeClient(key, &fakeOwnerFileClient{
		commitFn: func(context.Context, proto.OwnerFileCommitRequest) error {
			enterCommit <- struct{}{}
			<-releaseCommit
			return nil
		},
	}); err != nil {
		t.Fatalf("SetAuthoritativeClient() error = %v", err)
	}

	reply, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "note.txt",
		Mode: 0o644,
		Data: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}

	flushDone := make(chan error, 1)
	go func() {
		_, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
			Path:     "note.txt",
			HandleID: reply.HandleID,
		}, nil)
		flushDone <- err
	}()

	select {
	case <-enterCommit:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for commit start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	releaseDone := make(chan error, 1)
	go func() {
		_, err := manager.FsMutationContext(ctx, key, proto.FsOpRelease, proto.FsRequest{
			Path:     "note.txt",
			HandleID: reply.HandleID,
		}, nil)
		releaseDone <- err
	}()

	cancel()

	select {
	case err := <-releaseDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FsMutationContext(release) error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FsMutationContext(release) did not return after cancellation")
	}

	if state := handleState(t, manager, key, reply.HandleID); state != stagedHandleCommitting {
		t.Fatalf("handle state after canceled release = %q, want %q", state, stagedHandleCommitting)
	}

	close(releaseCommit)

	select {
	case err := <-flushDone:
		if err != nil {
			t.Fatalf("FsMutation(flush) error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FsMutation(flush) did not complete after commit release")
	}

	released, err := manager.FsMutation(key, proto.FsOpRelease, proto.FsRequest{
		Path:     "note.txt",
		HandleID: reply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(release) error after cancellation = %v", err)
	}
	if released.HandleID != reply.HandleID {
		t.Fatalf("release HandleID = %q, want %q", released.HandleID, reply.HandleID)
	}
}

func TestWorkspaceManagerReleaseDoesNotCommitDirtyHandle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "note.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace note.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "note.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount note.txt) error = %v", err)
	}

	reply, err := manager.FsMutation(key, proto.FsOpOpen, proto.FsRequest{
		Path: "note.txt",
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(open) error = %v", err)
	}
	if reply.HandleID == "" {
		t.Fatal("open HandleID is empty")
	}

	if _, err := manager.FsMutation(key, proto.FsOpWrite, proto.FsRequest{
		Path:     "note.txt",
		HandleID: reply.HandleID,
		Offset:   0,
		Data:     []byte("v2"),
	}, nil); err != nil {
		t.Fatalf("FsMutation(write) error = %v", err)
	}

	released, err := manager.FsMutation(key, proto.FsOpRelease, proto.FsRequest{
		Path:     "note.txt",
		HandleID: reply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(release) error = %v", err)
	}
	if released.HandleID != reply.HandleID {
		t.Fatalf("release HandleID = %q, want %q", released.HandleID, reply.HandleID)
	}

	workspaceData, err := os.ReadFile(filepath.Join(layout.WorkspaceDir, "note.txt"))
	if err != nil {
		t.Fatalf("ReadFile(workspace note.txt) error = %v", err)
	}
	if string(workspaceData) != "v1" {
		t.Fatalf("workspace note.txt after release = %q, want v1", string(workspaceData))
	}
	mountData, err := os.ReadFile(filepath.Join(layout.MountDir, "note.txt"))
	if err != nil {
		t.Fatalf("ReadFile(mount note.txt) error = %v", err)
	}
	if string(mountData) != "v1" {
		t.Fatalf("mount note.txt after release = %q, want v1", string(mountData))
	}
}

func TestWorkspaceManagerRemoveAndRmdirRespectPathKinds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	authoritativeRoot := filepath.Join(root, "authoritative")
	if err := os.MkdirAll(authoritativeRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(authoritativeRoot) error = %v", err)
	}
	if err := manager.SetAuthoritativeRoot(key, authoritativeRoot); err != nil {
		t.Fatalf("SetAuthoritativeRoot() error = %v", err)
	}

	if err := os.Mkdir(filepath.Join(authoritativeRoot, "dir"), 0o755); err != nil {
		t.Fatalf("Mkdir(authoritative dir) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(layout.MountDir, "dir"), 0o755); err != nil {
		t.Fatalf("Mkdir(mount dir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(authoritativeRoot, "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(authoritative file) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount file) error = %v", err)
	}

	removeDir, err := manager.FsMutation(key, proto.FsOpRemove, proto.FsRequest{
		Path: "dir",
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(remove dir) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "dir",
		HandleID: removeDir.HandleID,
	}, nil); err == nil {
		t.Fatal("FsMutation(flush remove dir) succeeded for directory path")
	}

	removeFileViaRmdir, err := manager.FsMutation(key, proto.FsOpRmdir, proto.FsRequest{
		Path: "file.txt",
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(rmdir file) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "file.txt",
		HandleID: removeFileViaRmdir.HandleID,
	}, nil); err == nil {
		t.Fatal("FsMutation(flush rmdir file) succeeded for file path")
	}

	if info, err := os.Stat(filepath.Join(authoritativeRoot, "dir")); err != nil || !info.IsDir() {
		t.Fatalf("authoritative dir mutated after failed remove, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(authoritativeRoot, "file.txt")); err != nil {
		t.Fatalf("authoritative file mutated after failed rmdir, err = %v", err)
	}
}

func TestWorkspaceManagerFinalizeSyncSeedsMustNotExistState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	seedManifestState(t, manager, key, layout, []proto.ManifestEntry{
		{
			Path: "seed.txt",
			Metadata: proto.FileMetadata{
				Mode:  0o644,
				MTime: time.Unix(1_700_000_100, 0).UTC(),
				Size:  int64(len("seed")),
			},
			StatFingerprint: "seed-file",
		},
		{
			Path:  "docs",
			IsDir: true,
			Metadata: proto.FileMetadata{
				Mode:  0o755,
				MTime: time.Unix(1_700_000_100, 0).UTC(),
				Size:  0,
			},
			StatFingerprint: "seed-dir",
		},
	})

	reply, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "seed.txt",
		Mode: 0o644,
		Data: []byte("new"),
	}, []proto.MutationPrecondition{{
		Path:         "seed.txt",
		MustNotExist: true,
	}})
	if err == nil {
		t.Fatal("FsMutation(create must-not-exist) succeeded for existing file")
	}
	if !reply.Conflict {
		t.Fatalf("FsMutation(create must-not-exist) reply = %#v, want conflict", reply)
	}

	reply, err = manager.FsMutation(key, proto.FsOpMkdir, proto.FsRequest{
		Path: "docs",
		Mode: 0o755,
	}, []proto.MutationPrecondition{{
		Path:         "docs",
		MustNotExist: true,
	}})
	if err == nil {
		t.Fatal("FsMutation(mkdir must-not-exist) succeeded for existing directory")
	}
	if !reply.Conflict {
		t.Fatalf("FsMutation(mkdir must-not-exist) reply = %#v, want conflict", reply)
	}
}

func TestWorkspaceManagerRenameOverwriteRequiresStableTargetIdentity(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	seedManifestState(t, manager, key, layout, []proto.ManifestEntry{
		{
			Path: "source.txt",
			Metadata: proto.FileMetadata{
				Mode:  0o644,
				MTime: time.Unix(1_700_000_200, 0).UTC(),
				Size:  int64(len("source")),
			},
			StatFingerprint: "source-file",
		},
		{
			Path: "target.txt",
			Metadata: proto.FileMetadata{
				Mode:  0o644,
				MTime: time.Unix(1_700_000_200, 0).UTC(),
				Size:  int64(len("target")),
			},
			StatFingerprint: "target-file",
		},
	})

	manager.mu.Lock()
	targetIdentity := manager.stateLocked(key).committed.objectIdentity["target.txt"]
	manager.mu.Unlock()
	if targetIdentity == "" {
		t.Fatal("target identity was not seeded after FinalizeSync")
	}

	renameReply, err := manager.FsMutation(key, proto.FsOpRename, proto.FsRequest{
		Path:    "source.txt",
		NewPath: "target.txt",
	}, []proto.MutationPrecondition{{
		Path:           "target.txt",
		ObjectIdentity: targetIdentity,
	}})
	if err != nil {
		t.Fatalf("FsMutation(rename overwrite) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "source.txt",
		NewPath:  "target.txt",
		HandleID: renameReply.HandleID,
	}, nil); err != nil {
		t.Fatalf("FsMutation(flush rename overwrite) error = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(layout.MountDir, "target.txt")); err != nil || string(data) != "source" {
		t.Fatalf("target after overwrite rename = %q, err = %v", string(data), err)
	}

	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "source.txt"), []byte("source-again"), 0o644); err != nil {
		t.Fatalf("WriteFile(source.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "source.txt"), []byte("source-again"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount source.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "target.txt"), []byte("replacement"), 0o644); err != nil {
		t.Fatalf("WriteFile(target.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "target.txt"), []byte("replacement"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount target.txt) error = %v", err)
	}
	manager.mu.Lock()
	state := manager.stateLocked(key)
	state.bumpPathGenerationsLocked("source.txt")
	state.bumpDeleteGenerationsLocked("target.txt")
	targetReply := state.bumpPathGenerationsLocked("target.txt")
	stagedTargetIdentity := state.committed.objectIdentity["target.txt"]
	manager.mu.Unlock()
	if stagedTargetIdentity == "" || stagedTargetIdentity == targetIdentity || targetReply.ObjectGeneration == 0 {
		t.Fatalf("target identity not replaced after recreation: old=%q new=%q reply=%#v", targetIdentity, stagedTargetIdentity, targetReply)
	}

	staleRename, err := manager.FsMutation(key, proto.FsOpRename, proto.FsRequest{
		Path:    "source.txt",
		NewPath: "target.txt",
	}, []proto.MutationPrecondition{{
		Path:           "target.txt",
		ObjectIdentity: stagedTargetIdentity,
	}})
	if err != nil {
		t.Fatalf("FsMutation(rename overwrite stale) error = %v", err)
	}

	if err := os.Remove(filepath.Join(layout.WorkspaceDir, "target.txt")); err != nil {
		t.Fatalf("Remove(target.txt) error = %v", err)
	}
	if err := os.Remove(filepath.Join(layout.MountDir, "target.txt")); err != nil {
		t.Fatalf("Remove(mount target.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.WorkspaceDir, "target.txt"), []byte("replacement-2"), 0o644); err != nil {
		t.Fatalf("WriteFile(target.txt replacement-2) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.MountDir, "target.txt"), []byte("replacement-2"), 0o644); err != nil {
		t.Fatalf("WriteFile(mount target.txt replacement-2) error = %v", err)
	}
	manager.mu.Lock()
	state = manager.stateLocked(key)
	state.bumpDeleteGenerationsLocked("target.txt")
	state.bumpPathGenerationsLocked("target.txt")
	latestTargetIdentity := state.committed.objectIdentity["target.txt"]
	manager.mu.Unlock()
	if latestTargetIdentity == "" || latestTargetIdentity == stagedTargetIdentity {
		t.Fatalf("target identity did not change during staged rename: before=%q after=%q", stagedTargetIdentity, latestTargetIdentity)
	}

	flushReply, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "source.txt",
		NewPath:  "target.txt",
		HandleID: staleRename.HandleID,
	}, nil)
	if err == nil {
		t.Fatal("FsMutation(flush rename overwrite stale) succeeded with stale target identity")
	}
	if !flushReply.Conflict {
		t.Fatalf("FsMutation(flush rename overwrite stale) reply = %#v, want conflict", flushReply)
	}
	if data, err := os.ReadFile(filepath.Join(layout.MountDir, "source.txt")); err != nil || string(data) != "source-again" {
		t.Fatalf("source after failed stale rename = %q, err = %v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(layout.MountDir, "target.txt")); err != nil || string(data) != "replacement-2" {
		t.Fatalf("target after failed stale rename = %q, err = %v", string(data), err)
	}
}

func TestWorkspaceManagerFlushDetectsParentDirectoryGenerationConflict(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	seedManifestState(t, manager, key, layout, []proto.ManifestEntry{{
		Path:  "docs",
		IsDir: true,
		Metadata: proto.FileMetadata{
			Mode:  0o755,
			MTime: time.Unix(1_700_000_400, 0).UTC(),
			Size:  0,
		},
		StatFingerprint: "docs-dir",
	}})

	manager.mu.Lock()
	manager.stateLocked(key).committed.dirGen["docs"] = 5
	staleDirGeneration := manager.stateLocked(key).committed.dirGen["docs"]
	manager.mu.Unlock()

	staged, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "docs/new.txt",
		Mode: 0o644,
		Data: []byte("new"),
	}, []proto.MutationPrecondition{{
		Path:          "docs/new.txt",
		DirGeneration: staleDirGeneration,
	}})
	if err != nil {
		t.Fatalf("FsMutation(create staged) error = %v", err)
	}

	conflicting, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "docs/other.txt",
		Mode: 0o644,
		Data: []byte("other"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create conflicting) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "docs/other.txt",
		HandleID: conflicting.HandleID,
	}, nil); err != nil {
		t.Fatalf("FsMutation(flush conflicting) error = %v", err)
	}

	reply, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "docs/new.txt",
		HandleID: staged.HandleID,
	}, nil)
	if err == nil {
		t.Fatal("FsMutation(flush staged) succeeded with stale parent dir generation")
	}
	if !reply.Conflict {
		t.Fatalf("FsMutation(flush staged) reply = %#v, want conflict", reply)
	}
	if _, err := os.Stat(filepath.Join(layout.MountDir, "docs", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("docs/new.txt published despite dir generation conflict, err = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(layout.MountDir, "docs", "other.txt")); err != nil || string(data) != "other" {
		t.Fatalf("docs/other.txt = %q, err = %v", string(data), err)
	}
}

func TestWorkspaceManagerInvalidationsCoverObjectAndParentDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := NewWorkspaceManager(root)
	key := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	layout, err := ResolveProjectLayout(root, key)
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	createReply, err := manager.FsMutation(key, proto.FsOpCreate, proto.FsRequest{
		Path: "docs/note.txt",
		Mode: 0o644,
		Data: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(create) error = %v", err)
	}
	flushedCreate, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "docs/note.txt",
		HandleID: createReply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(flush create) error = %v", err)
	}
	assertInvalidationKinds(t, flushedCreate.Invalidations,
		proto.InvalidateChange{Path: "docs/note.txt", Kind: proto.InvalidateData},
		proto.InvalidateChange{Path: "docs", Kind: proto.InvalidateDentry},
	)

	mkdirReply, err := manager.FsMutation(key, proto.FsOpMkdir, proto.FsRequest{
		Path: "archive",
		Mode: 0o755,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(mkdir archive) error = %v", err)
	}
	if _, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "archive",
		HandleID: mkdirReply.HandleID,
	}, nil); err != nil {
		t.Fatalf("FsMutation(flush mkdir archive) error = %v", err)
	}

	renameReply, err := manager.FsMutation(key, proto.FsOpRename, proto.FsRequest{
		Path:    "docs/note.txt",
		NewPath: "archive/final.txt",
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(rename) error = %v", err)
	}
	flushedRename, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "docs/note.txt",
		NewPath:  "archive/final.txt",
		HandleID: renameReply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(flush rename) error = %v", err)
	}
	assertInvalidationKinds(t, flushedRename.Invalidations,
		proto.InvalidateChange{Path: "docs/note.txt", NewPath: "archive/final.txt", Kind: proto.InvalidateRename},
		proto.InvalidateChange{Path: "docs", Kind: proto.InvalidateDentry},
		proto.InvalidateChange{Path: "archive", Kind: proto.InvalidateDentry},
	)

	removeReply, err := manager.FsMutation(key, proto.FsOpRemove, proto.FsRequest{
		Path: "archive/final.txt",
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(remove) error = %v", err)
	}
	flushedRemove, err := manager.FsMutation(key, proto.FsOpFlush, proto.FsRequest{
		Path:     "archive/final.txt",
		HandleID: removeReply.HandleID,
	}, nil)
	if err != nil {
		t.Fatalf("FsMutation(flush remove) error = %v", err)
	}
	assertInvalidationKinds(t, flushedRemove.Invalidations,
		proto.InvalidateChange{Path: "archive/final.txt", Kind: proto.InvalidateDelete},
		proto.InvalidateChange{Path: "archive", Kind: proto.InvalidateDentry},
	)
}

func seedManifestState(t *testing.T, manager *WorkspaceManager, key proto.ProjectKey, layout ProjectLayout, entries []proto.ManifestEntry) {
	t.Helper()

	if err := manager.BeginSync(key, proto.BeginSyncRequest{
		SyncEpoch:       1,
		RootFingerprint: "seed-root",
	}); err != nil {
		t.Fatalf("BeginSync() error = %v", err)
	}
	stageRoot := activeSyncStageRoot(t, manager, key, 1)
	for _, entry := range entries {
		target := filepath.Join(stageRoot, filepath.FromSlash(entry.Path))
		if entry.IsDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v", entry.Path, err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatalf("MkdirAll(parent %q) error = %v", entry.Path, err)
			}
			content := []byte(strings.TrimSuffix(entry.Path, filepath.Ext(entry.Path)))
			if entry.Metadata.Size != int64(len(content)) {
				content = make([]byte, entry.Metadata.Size)
				for idx := range content {
					content[idx] = 'x'
				}
			}
			if err := os.WriteFile(target, content, 0o644); err != nil {
				t.Fatalf("WriteFile(%q) error = %v", entry.Path, err)
			}
		}
		if err := manager.ScanManifest(key, proto.ScanManifestRequest{Entries: []proto.ManifestEntry{entry}}); err != nil {
			t.Fatalf("ScanManifest(%q) error = %v", entry.Path, err)
		}
	}
	if err := manager.FinalizeSync(key, proto.FinalizeSyncRequest{
		SyncEpoch:   1,
		GuardStable: true,
	}); err != nil {
		t.Fatalf("FinalizeSync() error = %v", err)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func handleState(t *testing.T, manager *WorkspaceManager, key proto.ProjectKey, handleID string) stagedHandleState {
	t.Helper()

	manager.mu.Lock()
	defer manager.mu.Unlock()

	handle, ok := manager.stateLocked(key).handles.open[handleID]
	if !ok {
		t.Fatalf("handle %q not found", handleID)
	}
	return handle.state
}

func handleBackingKind(t *testing.T, manager *WorkspaceManager, key proto.ProjectKey, handleID string) stagedHandleBackingKind {
	t.Helper()

	manager.mu.Lock()
	defer manager.mu.Unlock()

	handle, ok := manager.stateLocked(key).handles.open[handleID]
	if !ok {
		t.Fatalf("handle %q not found", handleID)
	}
	return handle.backingKind
}

func activeSyncStageRoot(t *testing.T, manager *WorkspaceManager, key proto.ProjectKey, epoch uint64) string {
	t.Helper()

	manager.mu.Lock()
	defer manager.mu.Unlock()

	session, ok := manager.stateLocked(key).sync.sessions[epoch]
	if !ok {
		t.Fatalf("sync session %d not found", epoch)
	}
	return session.stageRoot
}

func assertCommittedFileContent(t *testing.T, root string, path string, want string) {
	t.Helper()

	fullPath := filepath.Join(root, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", fullPath, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", fullPath, string(data), want)
	}
}

func assertInvalidationKinds(t *testing.T, actual []proto.InvalidateChange, expected ...proto.InvalidateChange) {
	t.Helper()

	if len(actual) != len(expected) {
		t.Fatalf("invalidations len = %d, want %d (%#v)", len(actual), len(expected), actual)
	}
	for _, want := range expected {
		found := false
		for _, got := range actual {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing invalidation %#v in %#v", want, actual)
		}
	}
}
