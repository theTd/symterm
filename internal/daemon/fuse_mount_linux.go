//go:build linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	gofusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"symterm/internal/proto"
)

func startFuseMount(projectKey proto.ProjectKey, layout ProjectLayout, projectFS ProjectFilesystem) (projectMountSession, error) {
	if err := resetMountDirectory(layout.MountDir); err != nil {
		return nil, err
	}

	root := &projectMountNode{
		projectKey: projectKey,
		projectFS:  projectFS,
		path:       "",
		isDir:      true,
	}
	timeout := time.Duration(0)
	server, err := gofusefs.Mount(layout.MountDir, root, &gofusefs.Options{
		EntryTimeout:    &timeout,
		AttrTimeout:     &timeout,
		NegativeTimeout: &timeout,
		MountOptions: fuse.MountOptions{
			Name: "symterm",
		},
	})
	if err != nil {
		return nil, proto.NewError(proto.ErrMountFailed, "fuse mount failed: "+err.Error())
	}

	session := &fuseMountSession{
		server:    server,
		mountDir:  layout.MountDir,
		failures:  make(chan error, 1),
		stopWatch: make(chan struct{}),
	}
	if err := waitForFuseMountReady(session, 5*time.Second); err != nil {
		_ = session.Stop()
		return nil, err
	}
	// Capture the FUSE connection ID now while the mount is healthy.
	// Storing it avoids creating temp files inside the mount during shutdown.
	if connID, err := fuseConnectionID(layout.MountDir); err == nil {
		session.connID = connID
	}
	session.startInvalidatePump(projectKey, projectFS)
	return session, nil
}

type fuseMountSession struct {
	server    *fuse.Server
	mountDir  string
	connID    uint64
	failures  chan error
	failOnce  sync.Once
	stopOnce  sync.Once
	stopWatch chan struct{}
}

func (s *fuseMountSession) WorkDir() string {
	return s.mountDir
}

func (s *fuseMountSession) Failure() <-chan error {
	return s.failures
}

func (s *fuseMountSession) Validate() error {
	if s == nil {
		return proto.NewError(proto.ErrMountFailed, "fuse mount session is unavailable")
	}
	mounted, err := isActiveMountpoint(s.mountDir)
	if err != nil {
		return proto.NewError(proto.ErrMountFailed, "check fuse mountpoint: "+err.Error())
	}
	if !mounted {
		return proto.NewError(proto.ErrMountFailed, "fuse mountpoint is not active")
	}
	// Do NOT access the mount contents (stat, open, readdir) from within
	// the same process. Accessing a FUSE mount from its own server process
	// can deadlock or cause D-state under SIGKILL because the kernel may
	// put the requesting thread in uninterruptible sleep waiting for a
	// FUSE response that can never arrive if the server threads are killed.
	return nil
}

func (s *fuseMountSession) Stop() error {
	var err error
	s.stopOnce.Do(func() {
		close(s.stopWatch)
		s.failOnce.Do(func() {
			close(s.failures)
		})
		if s.server != nil {
			// Abort the FUSE connection so the kernel wakes up any thread
			// blocked in read(/dev/fuse) and returns ENODEV. This is the
			// only reliable way to prevent D-state when the mount is torn
			// down while go-fuse loops are still running.
			_ = s.abortFuseConnection()

			// Give go-fuse server loops a moment to see ENODEV and exit.
			// server.Unmount() calls fusermount3 -u (non-lazy) which fails
			// when files are open, and then skips ms.loops.Wait(). Calling
			// detachMountpoint() while loops are still reading can put the
			// thread into D-state. A short sleep lets the loops finish.
			time.Sleep(200 * time.Millisecond)

			// Lazy-detach the mountpoint so it is no longer visible in the
			// namespace. go-fuse will not try to unmount again because we
			// do not call server.Unmount() here.
			if detachErr := detachMountpoint(s.mountDir); detachErr != nil {
				err = detachErr
			}
		}
	})
	return err
}

func (s *fuseMountSession) abortFuseConnection() error {
	connID := s.connID
	if connID == 0 {
		var err error
		connID, err = fuseConnectionID(s.mountDir)
		if err != nil {
			return err
		}
	}
	abortPath := fmt.Sprintf("/sys/fs/fuse/connections/%d/abort", connID)
	return os.WriteFile(abortPath, []byte("1"), 0o644)
}

func fuseConnectionID(mountDir string) (uint64, error) {
	// The FUSE connection ID is the Dev field from Stat_t for a file inside the mount.
	// We must create a temp file inside the mount and stat it; stat on the mount
	// directory itself may return the parent filesystem's device, not the FUSE
	// connection device.
	var st syscall.Stat_t
	tmpPath := filepath.Join(mountDir, ".fuse-stat-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	fd, err := syscall.Open(tmpPath, syscall.O_CREAT|syscall.O_RDONLY, 0o600)
	if err != nil {
		return 0, err
	}
	_ = syscall.Close(fd)
	defer syscall.Unlink(tmpPath)
	if err := syscall.Stat(tmpPath, &st); err != nil {
		return 0, err
	}
	return st.Dev, nil
}

func (s *fuseMountSession) startInvalidatePump(projectKey proto.ProjectKey, projectFS ProjectFilesystem) {
	if s.server == nil || projectFS == nil {
		return
	}
	go func() {
		sinceCursor := uint64(0)
		var watch ProjectInvalidateWatch
		defer func() {
			if watch.Close != nil {
				watch.Close()
			}
		}()
		for {
			nextWatch, watchErr := projectFS.WatchInvalidate(projectKey, sinceCursor)
			if watchErr != nil {
				s.reportFailure(watchErr)
				return
			}
			if watch.Close != nil {
				watch.Close()
			}
			watch = nextWatch
			s.applyInvalidateEvents(watch.Events)
			if len(watch.Events) > 0 {
				sinceCursor = watch.Events[len(watch.Events)-1].Cursor
			}

			select {
			case <-s.stopWatch:
				return
			case _, ok := <-watch.Notify:
				if !ok {
					return
				}
			}
		}
	}()
}

func (s *fuseMountSession) reportFailure(err error) {
	if err == nil {
		return
	}
	select {
	case <-s.stopWatch:
		return
	default:
	}

	s.failOnce.Do(func() {
		s.failures <- err
		close(s.failures)
	})
}

func waitForFuseMountReady(session *fuseMountSession, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	readyStreak := 0
	for {
		if err := session.Validate(); err == nil {
			readyStreak++
			if readyStreak >= 5 {
				return nil
			}
		} else {
			lastErr = err
			readyStreak = 0
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *fuseMountSession) applyInvalidateEvents(events []proto.InvalidateEvent) {
	for _, event := range events {
		for _, change := range event.Changes {
			s.applyInvalidateChange(change)
		}
	}
}

func (s *fuseMountSession) applyInvalidateChange(change proto.InvalidateChange) {
	if s.server == nil {
		return
	}
	switch change.Kind {
	case proto.InvalidateData:
		if isRootWorkspacePath(change.Path) {
			return
		}
		_ = s.server.InodeNotify(stableInodeForPath(change.Path, false), 0, 0)
	case proto.InvalidateDentry:
		parent, name := splitParentBase(change.Path)
		if name == "" {
			return
		}
		_ = s.server.EntryNotify(stableInodeForPath(parent, true), name)
	case proto.InvalidateDelete:
		parent, name := splitParentBase(change.Path)
		if name == "" {
			return
		}
		_ = s.server.DeleteNotify(stableInodeForPath(parent, true), stableInodeForPath(change.Path, false), name)
	case proto.InvalidateRename:
		oldParent, oldName := splitParentBase(change.Path)
		if oldName != "" {
			_ = s.server.EntryNotify(stableInodeForPath(oldParent, true), oldName)
		}
		newParent, newName := splitParentBase(change.NewPath)
		if newName != "" {
			_ = s.server.EntryNotify(stableInodeForPath(newParent, true), newName)
		}
	}
}

type projectMountNode struct {
	gofusefs.Inode
	projectKey proto.ProjectKey
	projectFS  ProjectFilesystem
	path       string
	isDir      bool

	cacheMu           sync.RWMutex
	cachedMetadata    proto.FileMetadata
	hasCachedMetadata bool
}

var _ = (gofusefs.NodeLookuper)((*projectMountNode)(nil))
var _ = (gofusefs.NodeGetattrer)((*projectMountNode)(nil))
var _ = (gofusefs.NodeReaddirer)((*projectMountNode)(nil))
var _ = (gofusefs.NodeOpener)((*projectMountNode)(nil))
var _ = (gofusefs.NodeCreater)((*projectMountNode)(nil))
var _ = (gofusefs.NodeMkdirer)((*projectMountNode)(nil))
var _ = (gofusefs.NodeUnlinker)((*projectMountNode)(nil))
var _ = (gofusefs.NodeRmdirer)((*projectMountNode)(nil))
var _ = (gofusefs.NodeRenamer)((*projectMountNode)(nil))
var _ = (gofusefs.NodeSetattrer)((*projectMountNode)(nil))

func (n *projectMountNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gofusefs.Inode, syscall.Errno) {
	if childInode, childNode, ok := n.lookupStagedChild(name); ok {
		fillEntryOut(out, childNode.path, childNode.cachedMetadataSnapshot(), childNode.isDir)
		return childInode, 0
	}

	reply, err := n.projectFS.FsRead(ctx, n.projectKey, proto.FsOpLookup, proto.FsRequest{
		Path: joinWorkspacePath(n.path, name),
	})
	if err != nil {
		return nil, errnoFromError(err)
	}
	childPath := joinWorkspacePath(n.path, name)
	fillEntryOut(out, childPath, reply.Metadata, reply.IsDir)
	child := &projectMountNode{
		projectKey: n.projectKey,
		projectFS:  n.projectFS,
		path:       childPath,
		isDir:      reply.IsDir,
	}
	return n.NewInode(ctx, child, stableAttrForPath(childPath, reply.IsDir)), 0
}

func (n *projectMountNode) Getattr(ctx context.Context, f gofusefs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	request := proto.FsRequest{Path: n.path}
	if handle, ok := f.(*projectFileHandle); ok {
		request.HandleID = handle.handleID
	}
	reply, err := n.projectFS.FsRead(ctx, n.projectKey, proto.FsOpGetAttr, request)
	if err != nil {
		if n.hasCachedMetadataSnapshot() {
			fillAttrOut(out, n.path, n.cachedMetadataSnapshot(), n.isDir)
			return 0
		}
		return errnoFromError(err)
	}
	fillAttrOut(out, n.path, reply.Metadata, reply.IsDir)
	return 0
}

func (n *projectMountNode) Readdir(ctx context.Context) (gofusefs.DirStream, syscall.Errno) {
	reply, err := n.projectFS.FsRead(ctx, n.projectKey, proto.FsOpReadDir, proto.FsRequest{Path: n.path})
	if err != nil {
		return nil, errnoFromError(err)
	}
	names := splitDirNames(reply.Data)
	entries := make([]fuse.DirEntry, 0, len(names))
	for _, name := range names {
		childPath := joinWorkspacePath(n.path, name)
		childReply, childErr := n.projectFS.FsRead(ctx, n.projectKey, proto.FsOpLookup, proto.FsRequest{Path: childPath})
		mode := uint32(syscall.S_IFREG)
		if childErr == nil {
			mode = fileModeForFuse(childReply.Metadata, childReply.IsDir)
		}
		entries = append(entries, fuse.DirEntry{
			Name: name,
			Mode: mode,
			Ino:  stableInodeForPath(childPath, mode == syscall.S_IFDIR),
		})
	}
	return gofusefs.NewListDirStream(entries), 0
}

func (n *projectMountNode) Open(ctx context.Context, flags uint32) (gofusefs.FileHandle, uint32, syscall.Errno) {
	reply, err := n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpOpen,
		Request: proto.FsRequest{
			Path:       n.path,
			OpenIntent: openIntentForFlags(flags),
		},
	})
	if err != nil {
		return nil, 0, errnoFromError(err)
	}
	return &projectFileHandle{
		node:     n,
		handleID: reply.HandleID,
		flags:    flags,
	}, fuse.FOPEN_DIRECT_IO, 0
}

func (n *projectMountNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*gofusefs.Inode, gofusefs.FileHandle, uint32, syscall.Errno) {
	childPath := joinWorkspacePath(n.path, name)
	reply, err := n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpCreate,
		Request: proto.FsRequest{
			Path: childPath,
			Mode: mode & 0o777,
		},
	})
	if err != nil {
		return nil, nil, 0, errnoFromError(err)
	}
	metadata := proto.FileMetadata{
		Mode:  mode & 0o777,
		MTime: time.Now().UTC(),
		Size:  0,
	}
	fillEntryOut(out, childPath, metadata, false)
	child := &projectMountNode{
		projectKey: n.projectKey,
		projectFS:  n.projectFS,
		path:       childPath,
		isDir:      false,
	}
	child.setCachedMetadata(metadata)
	handle := &projectFileHandle{
		node:     child,
		handleID: reply.HandleID,
		flags:    flags,
	}
	return n.NewInode(ctx, child, stableAttrForPath(childPath, false)), handle, fuse.FOPEN_DIRECT_IO, 0
}

func (n *projectMountNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*gofusefs.Inode, syscall.Errno) {
	childPath := joinWorkspacePath(n.path, name)
	reply, err := n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpMkdir,
		Request: proto.FsRequest{
			Path: childPath,
			Mode: mode & 0o777,
		},
	})
	if err != nil {
		return nil, errnoFromError(err)
	}
	if _, err := n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpFlush,
		Request: proto.FsRequest{
			Path:     childPath,
			HandleID: reply.HandleID,
		},
	}); err != nil {
		_, _ = n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
			Op: proto.FsOpRelease,
			Request: proto.FsRequest{
				Path:     childPath,
				HandleID: reply.HandleID,
			},
		})
		return nil, errnoFromError(err)
	}
	_, _ = n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpRelease,
		Request: proto.FsRequest{
			Path:     childPath,
			HandleID: reply.HandleID,
		},
	})
	metadata := proto.FileMetadata{
		Mode:  mode & 0o777,
		MTime: time.Now().UTC(),
	}
	fillEntryOut(out, childPath, metadata, true)
	child := &projectMountNode{
		projectKey: n.projectKey,
		projectFS:  n.projectFS,
		path:       childPath,
		isDir:      true,
	}
	child.setCachedMetadata(metadata)
	return n.NewInode(ctx, child, stableAttrForPath(childPath, true)), 0
}

func (n *projectMountNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.commitDentryMutation(ctx, proto.FsOpRemove, name, "")
}

func (n *projectMountNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.commitDentryMutation(ctx, proto.FsOpRmdir, name, "")
}

func (n *projectMountNode) Rename(ctx context.Context, name string, newParent gofusefs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if flags != 0 {
		return syscall.ENOTSUP
	}
	parentNode, ok := newParent.(*projectMountNode)
	if !ok {
		return syscall.EXDEV
	}
	reply, err := n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpRename,
		Request: proto.FsRequest{
			Path:    joinWorkspacePath(n.path, name),
			NewPath: joinWorkspacePath(parentNode.path, newName),
		},
	})
	if err != nil {
		return errnoFromError(err)
	}
	if _, err := n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpFlush,
		Request: proto.FsRequest{
			Path:     joinWorkspacePath(n.path, name),
			NewPath:  joinWorkspacePath(parentNode.path, newName),
			HandleID: reply.HandleID,
		},
	}); err != nil {
		_, _ = n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
			Op: proto.FsOpRelease,
			Request: proto.FsRequest{
				Path:     joinWorkspacePath(n.path, name),
				NewPath:  joinWorkspacePath(parentNode.path, newName),
				HandleID: reply.HandleID,
			},
		})
		return errnoFromError(err)
	}
	_, _ = n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpRelease,
		Request: proto.FsRequest{
			Path:     joinWorkspacePath(n.path, name),
			NewPath:  joinWorkspacePath(parentNode.path, newName),
			HandleID: reply.HandleID,
		},
	})
	return 0
}

func (n *projectMountNode) Setattr(ctx context.Context, f gofusefs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	handle, _ := f.(*projectFileHandle)
	if size, ok := in.GetSize(); ok {
		if errno := n.applyHandleMutation(ctx, proto.FsOpTruncate, handle, proto.FsRequest{
			Path: n.path,
			Size: int64(size),
		}); errno != 0 {
			return errno
		}
		n.updateCachedMetadata(func(metadata *proto.FileMetadata) {
			metadata.Size = int64(size)
			metadata.MTime = time.Now().UTC()
		})
	}
	if mode, ok := in.GetMode(); ok {
		if errno := n.applyHandleMutation(ctx, proto.FsOpChmod, handle, proto.FsRequest{
			Path: n.path,
			Mode: mode,
		}); errno != 0 {
			return errno
		}
		n.updateCachedMetadata(func(metadata *proto.FileMetadata) {
			metadata.Mode = mode
			metadata.MTime = time.Now().UTC()
		})
	}
	if _, ok := in.GetMTime(); ok {
		if errno := n.applyHandleMutation(ctx, proto.FsOpUtimens, handle, proto.FsRequest{
			Path: n.path,
		}); errno != 0 {
			return errno
		}
		n.updateCachedMetadata(func(metadata *proto.FileMetadata) {
			metadata.MTime = time.Now().UTC()
		})
	}
	if handle != nil {
		return handle.Getattr(ctx, out)
	}
	reply, err := n.projectFS.FsRead(ctx, n.projectKey, proto.FsOpGetAttr, proto.FsRequest{Path: n.path})
	if err != nil {
		return errnoFromError(err)
	}
	fillAttrOut(out, n.path, reply.Metadata, reply.IsDir)
	return 0
}

func (n *projectMountNode) commitDentryMutation(ctx context.Context, op proto.FsOperation, name string, newName string) syscall.Errno {
	request := proto.FsRequest{Path: joinWorkspacePath(n.path, name)}
	if newName != "" {
		request.NewPath = newName
	}
	reply, err := n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op:      op,
		Request: request,
	})
	if err != nil {
		return errnoFromError(err)
	}
	_, flushErr := n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpFlush,
		Request: proto.FsRequest{
			Path:     request.Path,
			NewPath:  request.NewPath,
			HandleID: reply.HandleID,
		},
	})
	_, _ = n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpRelease,
		Request: proto.FsRequest{
			Path:     request.Path,
			NewPath:  request.NewPath,
			HandleID: reply.HandleID,
		},
	})
	if flushErr != nil {
		return errnoFromError(flushErr)
	}
	return 0
}

func (n *projectMountNode) applyHandleMutation(ctx context.Context, op proto.FsOperation, handle *projectFileHandle, request proto.FsRequest) syscall.Errno {
	if handle != nil {
		request.HandleID = handle.handleID
	}
	reply, err := n.projectFS.FsMutation(ctx, n.projectKey, proto.FsMutationRequest{
		Op:      op,
		Request: request,
	})
	if err != nil {
		return errnoFromError(err)
	}
	if handle != nil && reply.HandleID != "" {
		handle.handleID = reply.HandleID
	}
	return 0
}

type projectFileHandle struct {
	node     *projectMountNode
	handleID string
	flags    uint32
}

var _ = (gofusefs.FileReader)((*projectFileHandle)(nil))
var _ = (gofusefs.FileWriter)((*projectFileHandle)(nil))
var _ = (gofusefs.FileFlusher)((*projectFileHandle)(nil))
var _ = (gofusefs.FileFsyncer)((*projectFileHandle)(nil))
var _ = (gofusefs.FileReleaser)((*projectFileHandle)(nil))
var _ = (gofusefs.FileGetattrer)((*projectFileHandle)(nil))

func (h *projectFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	reply, err := h.node.projectFS.FsRead(ctx, h.node.projectKey, proto.FsOpRead, proto.FsRequest{
		Path:     h.node.path,
		HandleID: h.handleID,
		Offset:   off,
		Size:     int64(len(dest)),
	})
	if err != nil {
		return nil, errnoFromError(err)
	}
	return fuse.ReadResultData(append([]byte(nil), reply.Data...)), 0
}

func (h *projectFileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	reply, err := h.node.projectFS.FsMutation(ctx, h.node.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpWrite,
		Request: proto.FsRequest{
			Path:     h.node.path,
			HandleID: h.handleID,
			Offset:   off,
			Data:     append([]byte(nil), data...),
		},
	})
	if err != nil {
		return 0, errnoFromError(err)
	}
	if reply.HandleID != "" {
		h.handleID = reply.HandleID
	}
	h.node.updateCachedMetadata(func(metadata *proto.FileMetadata) {
		nextSize := off + int64(len(data))
		if metadata.Size < nextSize {
			metadata.Size = nextSize
		}
		metadata.MTime = time.Now().UTC()
	})
	return uint32(len(data)), 0
}

func (h *projectFileHandle) Flush(ctx context.Context) syscall.Errno {
	if h.handleID == "" {
		return 0
	}
	_, err := h.node.projectFS.FsMutation(ctx, h.node.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpFlush,
		Request: proto.FsRequest{
			Path:     h.node.path,
			HandleID: h.handleID,
		},
	})
	if err != nil {
		return errnoFromError(err)
	}
	h.node.clearCachedMetadata()
	return 0
}

func (h *projectFileHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	if h.handleID == "" {
		return 0
	}
	_, err := h.node.projectFS.FsMutation(ctx, h.node.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpFSync,
		Request: proto.FsRequest{
			Path:     h.node.path,
			HandleID: h.handleID,
		},
	})
	if err != nil {
		return errnoFromError(err)
	}
	h.node.clearCachedMetadata()
	return 0
}

func (h *projectFileHandle) Release(ctx context.Context) syscall.Errno {
	if h.handleID == "" {
		return 0
	}
	_, err := h.node.projectFS.FsMutation(ctx, h.node.projectKey, proto.FsMutationRequest{
		Op: proto.FsOpRelease,
		Request: proto.FsRequest{
			Path:     h.node.path,
			HandleID: h.handleID,
		},
	})
	if err != nil {
		return errnoFromError(err)
	}
	return 0
}

func (h *projectFileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	reply, err := h.node.projectFS.FsRead(ctx, h.node.projectKey, proto.FsOpGetAttr, proto.FsRequest{
		Path:     h.node.path,
		HandleID: h.handleID,
	})
	if err != nil {
		return errnoFromError(err)
	}
	fillAttrOut(out, h.node.path, reply.Metadata, reply.IsDir)
	return 0
}

func (n *projectMountNode) lookupStagedChild(name string) (*gofusefs.Inode, *projectMountNode, bool) {
	childInode := n.GetChild(name)
	if childInode == nil {
		return nil, nil, false
	}
	childNode, ok := childInode.Operations().(*projectMountNode)
	if !ok || !childNode.hasCachedMetadataSnapshot() {
		return nil, nil, false
	}
	return childInode, childNode, true
}

func (n *projectMountNode) setCachedMetadata(metadata proto.FileMetadata) {
	n.cacheMu.Lock()
	defer n.cacheMu.Unlock()

	n.cachedMetadata = normalizeMetadata(metadata)
	n.hasCachedMetadata = true
}

func (n *projectMountNode) clearCachedMetadata() {
	n.cacheMu.Lock()
	defer n.cacheMu.Unlock()

	n.cachedMetadata = proto.FileMetadata{}
	n.hasCachedMetadata = false
}

func (n *projectMountNode) updateCachedMetadata(update func(*proto.FileMetadata)) {
	n.cacheMu.Lock()
	defer n.cacheMu.Unlock()

	if !n.hasCachedMetadata {
		return
	}
	update(&n.cachedMetadata)
	n.cachedMetadata = normalizeMetadata(n.cachedMetadata)
}

func (n *projectMountNode) cachedMetadataSnapshot() proto.FileMetadata {
	n.cacheMu.RLock()
	defer n.cacheMu.RUnlock()
	return n.cachedMetadata
}

func (n *projectMountNode) hasCachedMetadataSnapshot() bool {
	n.cacheMu.RLock()
	defer n.cacheMu.RUnlock()
	return n.hasCachedMetadata
}

func fillEntryOut(out *fuse.EntryOut, workspacePath string, metadata proto.FileMetadata, isDir bool) {
	fillAttr(&out.Attr, workspacePath, metadata, isDir)
	out.SetEntryTimeout(0)
	out.SetAttrTimeout(0)
}

func fillAttrOut(out *fuse.AttrOut, workspacePath string, metadata proto.FileMetadata, isDir bool) {
	fillAttr(&out.Attr, workspacePath, metadata, isDir)
	out.SetTimeout(0)
}

func fillAttr(attr *fuse.Attr, workspacePath string, metadata proto.FileMetadata, isDir bool) {
	attr.Ino = stableInodeForPath(workspacePath, isDir)
	attr.Mode = fileModeForFuse(metadata, isDir)
	attr.Size = uint64(metadata.Size)
	attr.Mtime = uint64(metadata.MTime.Unix())
	attr.Mtimensec = uint32(metadata.MTime.Nanosecond())
	attr.Ctime = attr.Mtime
	attr.Ctimensec = attr.Mtimensec
	attr.Atime = attr.Mtime
	attr.Atimensec = attr.Mtimensec
	attr.Blksize = 4096
	if metadata.Size > 0 {
		attr.Blocks = uint64((metadata.Size + 511) / 512)
	}
	attr.Nlink = 1
	if isDir {
		attr.Nlink = 2
	}
}

func stableAttrForPath(workspacePath string, isDir bool) gofusefs.StableAttr {
	return gofusefs.StableAttr{
		Mode: fileTypeBits(isDir),
		Ino:  stableInodeForPath(workspacePath, isDir),
	}
}

func stableInodeForPath(workspacePath string, isDir bool) uint64 {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(workspacePath))
	if isDir {
		_, _ = hasher.Write([]byte{1})
	}
	ino := hasher.Sum64()
	if ino == 0 {
		return 1
	}
	return ino
}

func fileModeForFuse(metadata proto.FileMetadata, isDir bool) uint32 {
	return fileTypeBits(isDir) | (metadata.Mode & 0o777)
}

func fileTypeBits(isDir bool) uint32 {
	if isDir {
		return syscall.S_IFDIR
	}
	return syscall.S_IFREG
}

func joinWorkspacePath(parent string, name string) string {
	if strings.TrimSpace(parent) == "" {
		return filepath.ToSlash(name)
	}
	return filepath.ToSlash(path.Join(parent, name))
}

func splitDirNames(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	parts := strings.Split(strings.TrimSpace(string(data)), "\n")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func splitParentBase(workspacePath string) (string, string) {
	clean := filepath.ToSlash(strings.TrimSpace(workspacePath))
	if clean == "" || clean == "." {
		return "", ""
	}
	return parentPath(clean), path.Base(clean)
}

func openIntentForFlags(flags uint32) proto.FsOpenIntent {
	switch flags & syscall.O_ACCMODE {
	case syscall.O_RDONLY:
		return proto.FsOpenIntentReadOnly
	default:
		return proto.FsOpenIntentWritable
	}
}

func errnoFromError(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	if errors.Is(err, fs.ErrNotExist) {
		return syscall.ENOENT
	}
	var protoErr *proto.Error
	if errors.As(err, &protoErr) {
		switch protoErr.Code {
		case proto.ErrAuthenticationFailed, proto.ErrPermissionDenied, proto.ErrFollowerSyncDenied:
			return syscall.EACCES
		case proto.ErrNeedsConfirmation, proto.ErrReadOnlyProject:
			return syscall.EROFS
		case proto.ErrProjectNotReady, proto.ErrProjectInitFailed, proto.ErrMountFailed:
			return syscall.EBUSY
		case proto.ErrProjectTerminated:
			return syscall.ENODEV
		case proto.ErrOwnerMissing:
			return syscall.ENXIO
		case proto.ErrSyncEpochMismatch, proto.ErrSyncRescanMismatch, proto.ErrReconcilePrecondition, proto.ErrConflict:
			return syscall.EBUSY
		case proto.ErrUnsupportedPath, proto.ErrInvalidArgument:
			return syscall.EINVAL
		case proto.ErrUnknownFile:
			return syscall.ENOENT
		case proto.ErrUnknownHandle:
			return syscall.EBADF
		case proto.ErrUnknownCommand, proto.ErrUnknownClient:
			return syscall.ENOENT
		case proto.ErrFileCommitFailed, proto.ErrOwnerWriteFailed, proto.ErrTransportInterrupted:
			return syscall.EIO
		default:
			return syscall.EIO
		}
	}
	return syscall.EIO
}

func resetMountDirectory(mountDir string) error {
	entries, err := os.ReadDir(mountDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(mountDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
