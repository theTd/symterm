//go:build linux

package daemon

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	gofusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"symterm/internal/proto"
)

type stubProjectFilesystem struct {
	lastMutation  proto.FsMutationRequest
	mutationReply proto.FsReply
	lastReadOp    proto.FsOperation
	lastRead      proto.FsRequest
	readReply     proto.FsReply
	readErr       error
}

func (s *stubProjectFilesystem) FsRead(_ context.Context, _ proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	s.lastReadOp = op
	s.lastRead = request
	return s.readReply, s.readErr
}

func (s *stubProjectFilesystem) FsMutation(_ context.Context, _ proto.ProjectKey, request proto.FsMutationRequest) (proto.FsReply, error) {
	s.lastMutation = request
	return s.mutationReply, nil
}

func (s *stubProjectFilesystem) WatchInvalidate(proto.ProjectKey, uint64) (ProjectInvalidateWatch, error) {
	ch := make(chan struct{})
	close(ch)
	return ProjectInvalidateWatch{
		Notify: ch,
		Close:  func() {},
	}, nil
}

func TestProjectMountNodeOpenRequestsOpenHandle(t *testing.T) {
	t.Parallel()

	fs := &stubProjectFilesystem{
		mutationReply: proto.FsReply{HandleID: "handle-0001"},
	}
	node := &projectMountNode{
		projectKey: proto.ProjectKey{Username: "alice", ProjectID: "demo"},
		projectFS:  fs,
		path:       "note.txt",
	}

	handle, flags, errno := node.Open(context.Background(), 0)
	if errno != 0 {
		t.Fatalf("Open() errno = %v", errno)
	}
	if flags == 0 {
		t.Fatal("Open() returned no FUSE flags")
	}
	fileHandle, ok := handle.(*projectFileHandle)
	if !ok {
		t.Fatalf("Open() handle = %T, want *projectFileHandle", handle)
	}
	if fileHandle.handleID != "handle-0001" {
		t.Fatalf("Open() handleID = %q, want handle-0001", fileHandle.handleID)
	}
	if fs.lastMutation.Op != proto.FsOpOpen {
		t.Fatalf("FsMutation().Op = %q, want %q", fs.lastMutation.Op, proto.FsOpOpen)
	}
	if fs.lastMutation.Request.Path != "note.txt" {
		t.Fatalf("FsMutation().Request.Path = %q, want note.txt", fs.lastMutation.Request.Path)
	}
	if fs.lastMutation.Request.OpenIntent != proto.FsOpenIntentReadOnly {
		t.Fatalf("FsMutation().Request.OpenIntent = %q, want %q", fs.lastMutation.Request.OpenIntent, proto.FsOpenIntentReadOnly)
	}
}

func TestProjectMountNodeOpenMarksWritableHandles(t *testing.T) {
	t.Parallel()

	fs := &stubProjectFilesystem{
		mutationReply: proto.FsReply{HandleID: "handle-0002"},
	}
	node := &projectMountNode{
		projectKey: proto.ProjectKey{Username: "alice", ProjectID: "demo"},
		projectFS:  fs,
		path:       "note.txt",
	}

	if _, _, errno := node.Open(context.Background(), syscall.O_RDWR); errno != 0 {
		t.Fatalf("Open() errno = %v", errno)
	}
	if fs.lastMutation.Request.OpenIntent != proto.FsOpenIntentWritable {
		t.Fatalf("FsMutation().Request.OpenIntent = %q, want %q", fs.lastMutation.Request.OpenIntent, proto.FsOpenIntentWritable)
	}
}

func TestProjectMountNodeGetattrFallsBackToCachedMetadata(t *testing.T) {
	t.Parallel()

	fs := &stubProjectFilesystem{
		readErr: errors.New("backing path not visible yet"),
	}
	node := &projectMountNode{
		projectKey: proto.ProjectKey{Username: "alice", ProjectID: "demo"},
		projectFS:  fs,
		path:       "note.txt",
	}
	node.setCachedMetadata(proto.FileMetadata{
		Mode:  0o640,
		Size:  7,
		MTime: time.Unix(1_700_000_000, 0).UTC(),
	})

	var out fuse.AttrOut
	if errno := node.Getattr(context.Background(), nil, &out); errno != 0 {
		t.Fatalf("Getattr() errno = %v", errno)
	}
	if out.Size != 7 {
		t.Fatalf("Getattr().Size = %d, want 7", out.Size)
	}
	if out.Mode != syscall.S_IFREG|0o640 {
		t.Fatalf("Getattr().Mode = %#o, want %#o", out.Mode, syscall.S_IFREG|0o640)
	}
}

func TestProjectMountNodeLookupUsesStagedChildCache(t *testing.T) {
	t.Parallel()

	fs := &stubProjectFilesystem{
		readErr: errors.New("committed lookup should not run"),
	}
	root := &projectMountNode{
		projectKey: proto.ProjectKey{Username: "alice", ProjectID: "demo"},
		projectFS:  fs,
		isDir:      true,
	}
	gofusefs.NewNodeFS(root, &gofusefs.Options{})

	child := &projectMountNode{
		projectKey: root.projectKey,
		projectFS:  fs,
		path:       "note.txt",
		isDir:      false,
	}
	child.setCachedMetadata(proto.FileMetadata{
		Mode:  0o644,
		Size:  0,
		MTime: time.Unix(1_700_000_100, 0).UTC(),
	})
	childInode := root.NewInode(context.Background(), child, stableAttrForPath("note.txt", false))
	if ok := root.AddChild("note.txt", childInode, true); !ok {
		t.Fatal("AddChild() = false")
	}

	var out fuse.EntryOut
	gotInode, errno := root.Lookup(context.Background(), "note.txt", &out)
	if errno != 0 {
		t.Fatalf("Lookup() errno = %v", errno)
	}
	if gotInode != childInode {
		t.Fatalf("Lookup() inode = %p, want %p", gotInode, childInode)
	}
	if fs.lastReadOp != "" {
		t.Fatalf("FsRead() unexpectedly called with op %q", fs.lastReadOp)
	}
	if out.Mode != syscall.S_IFREG|0o644 {
		t.Fatalf("Lookup().Mode = %#o, want %#o", out.Mode, syscall.S_IFREG|0o644)
	}
}

func TestErrnoFromErrorMapsUnknownFileToEnoent(t *testing.T) {
	t.Parallel()

	if got := errnoFromError(proto.NewError(proto.ErrUnknownFile, "missing")); got != syscall.ENOENT {
		t.Fatalf("errnoFromError(unknown file) = %v, want %v", got, syscall.ENOENT)
	}
	if got := errnoFromError(proto.NewError(proto.ErrUnknownHandle, "missing")); got != syscall.EBADF {
		t.Fatalf("errnoFromError(unknown handle) = %v, want %v", got, syscall.EBADF)
	}
}

var _ gofusefs.FileHandle = (*projectFileHandle)(nil)
