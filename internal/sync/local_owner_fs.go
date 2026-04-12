package sync

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"symterm/internal/diagnostic"
	"symterm/internal/portablefs"
	"symterm/internal/proto"
	"symterm/internal/transport"
)

type localOwnerFileService struct {
	mu           sync.Mutex
	root         string
	view         *WorkspaceViewResolver
	uploads      map[string]*ownerUploadSession
	nextUploadID uint64
}

type ownerUploadSession struct {
	uploadID     string
	op           proto.FsOperation
	path         string
	expectedSize int64
	metadata     proto.FileMetadata
	tempPath     string
	file         *os.File
	written      int64
}

func newLocalOwnerFileService(root string) transport.OwnerFileHandler {
	return &localOwnerFileService{
		root:    root,
		view:    NewWorkspaceViewResolver(root),
		uploads: make(map[string]*ownerUploadSession),
	}
}

func (s *localOwnerFileService) FsRead(_ context.Context, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	if err := validateOwnerReadPath(op, request.Path); err != nil {
		return proto.FsReply{}, err
	}
	rules, err := s.view.Rules()
	if err != nil {
		return proto.FsReply{}, err
	}
	if !isRootOwnerWorkspacePath(request.Path) {
		visible, err := PathVisibleOnDisk(s.root, rules, request.Path)
		if err != nil {
			return proto.FsReply{}, err
		}
		if !visible {
			return proto.FsReply{}, proto.NewError(proto.ErrUnknownFile, "path is excluded from the shared workspace")
		}
	}
	target := s.resolvePath(request.Path)

	switch op {
	case proto.FsOpLookup, proto.FsOpGetAttr:
		info, err := os.Stat(target)
		if err != nil {
			return proto.FsReply{}, err
		}
		metadata := ownerFileInfoMetadata(info)
		return proto.FsReply{
			Metadata: metadata,
			IsDir:    info.IsDir(),
			Data:     []byte(fmt.Sprintf("%d:%d:%s", metadata.Size, metadata.Mode, metadata.MTime.UTC().Format(time.RFC3339Nano))),
		}, nil
	case proto.FsOpRead:
		file, err := os.Open(target)
		if err != nil {
			return proto.FsReply{}, err
		}
		defer file.Close()
		if request.Offset > 0 {
			if _, err := file.Seek(request.Offset, io.SeekStart); err != nil {
				return proto.FsReply{}, err
			}
		}
		size := request.Size
		if size <= 0 {
			size = 64 * 1024
		}
		buf := make([]byte, size)
		n, err := file.Read(buf)
		if err != nil && err != io.EOF {
			return proto.FsReply{}, err
		}
		return proto.FsReply{Data: buf[:n]}, nil
	case proto.FsOpReadDir:
		names, err := VisibleDirectoryNamesOnDisk(s.root, rules, request.Path)
		if err != nil {
			return proto.FsReply{}, err
		}
		sort.Strings(names)
		return proto.FsReply{Data: []byte(strings.Join(names, "\n")), IsDir: true}, nil
	default:
		return proto.FsReply{}, proto.NewError(proto.ErrInvalidArgument, "unsupported owner fs read operation")
	}
}

func (s *localOwnerFileService) Apply(_ context.Context, request proto.OwnerFileApplyRequest) error {
	if err := validateOwnerMutationPath(request.Op, request.Path, request.NewPath); err != nil {
		return err
	}

	switch request.Op {
	case proto.FsOpCreate, proto.FsOpWrite, proto.FsOpTruncate, proto.FsOpChmod, proto.FsOpUtimens:
		metadata := normalizeOwnerMetadata(request.Metadata)
		metadata.Size = int64(len(request.Data))
		return replaceOwnerFile(s.resolvePath(request.Path), request.Data, metadata)
	case proto.FsOpMkdir:
		target := s.resolvePath(request.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.Mkdir(target, fs.FileMode(request.Metadata.Mode)); err != nil {
			return err
		}
		return applyOwnerDirMetadata(target, normalizeOwnerMetadata(request.Metadata))
	case proto.FsOpRemove, proto.FsOpRmdir:
		return removeOwnerPath(s.resolvePath(request.Path), request.Op)
	case proto.FsOpRename:
		return renameOwnerPath(s.resolvePath(request.Path), s.resolvePath(request.NewPath))
	default:
		return proto.NewError(proto.ErrInvalidArgument, "unsupported owner fs apply operation")
	}
}

func (s *localOwnerFileService) BeginFileUpload(_ context.Context, request proto.OwnerFileBeginRequest) (proto.OwnerFileBeginResponse, error) {
	if err := validateOwnerMutationPath(request.Op, request.Path, ""); err != nil {
		return proto.OwnerFileBeginResponse{}, err
	}
	switch request.Op {
	case proto.FsOpCreate, proto.FsOpWrite, proto.FsOpTruncate, proto.FsOpChmod, proto.FsOpUtimens:
	default:
		return proto.OwnerFileBeginResponse{}, proto.NewError(proto.ErrInvalidArgument, "unsupported owner file upload operation")
	}

	target := s.resolvePath(request.Path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return proto.OwnerFileBeginResponse{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextUploadID++
	uploadID := fmt.Sprintf("owner-upload-%04d", s.nextUploadID)
	tempPath := target + ".symterm-owner-upload"
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(request.Metadata.Mode))
	if err != nil {
		return proto.OwnerFileBeginResponse{}, err
	}
	s.uploads[uploadID] = &ownerUploadSession{
		uploadID:     uploadID,
		op:           request.Op,
		path:         request.Path,
		expectedSize: request.ExpectedSize,
		metadata:     normalizeOwnerMetadata(request.Metadata),
		tempPath:     tempPath,
		file:         file,
	}
	return proto.OwnerFileBeginResponse{UploadID: uploadID}, nil
}

func (s *localOwnerFileService) ApplyFileChunk(_ context.Context, request proto.OwnerFileApplyChunkRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	upload, ok := s.uploads[request.UploadID]
	if !ok {
		return proto.NewError(proto.ErrUnknownFile, "owner file upload handle does not exist")
	}
	if request.Offset != upload.written {
		return proto.NewError(proto.ErrFileCommitFailed, "chunk offset is not contiguous")
	}
	if _, err := upload.file.Write(request.Data); err != nil {
		return err
	}
	upload.written += int64(len(request.Data))
	return nil
}

func (s *localOwnerFileService) CommitFileUpload(_ context.Context, request proto.OwnerFileCommitRequest) error {
	s.mu.Lock()
	upload, ok := s.uploads[request.UploadID]
	if !ok {
		s.mu.Unlock()
		return proto.NewError(proto.ErrUnknownFile, "owner file upload handle does not exist")
	}
	delete(s.uploads, request.UploadID)
	s.mu.Unlock()

	defer cleanupOwnerUpload(upload)
	if upload.written != upload.expectedSize {
		return proto.NewError(proto.ErrFileCommitFailed, "uploaded file size does not match the expected size")
	}
	if err := upload.file.Close(); err != nil {
		return err
	}
	upload.file = nil
	if err := replaceOwnerFileFromTemp(upload.tempPath, s.resolvePath(upload.path), upload.metadata); err != nil {
		return err
	}
	upload.tempPath = ""
	return nil
}

func (s *localOwnerFileService) AbortFileUpload(_ context.Context, request proto.OwnerFileAbortRequest) error {
	s.mu.Lock()
	upload, ok := s.uploads[request.UploadID]
	if !ok {
		s.mu.Unlock()
		return proto.NewError(proto.ErrUnknownFile, "owner file upload handle does not exist")
	}
	delete(s.uploads, request.UploadID)
	s.mu.Unlock()

	return cleanupOwnerUpload(upload)
}

func (s *localOwnerFileService) resolvePath(workspacePath string) string {
	clean := filepath.ToSlash(strings.TrimSpace(workspacePath))
	if clean == "" || clean == "." {
		return s.root
	}
	return filepath.Join(s.root, filepath.FromSlash(clean))
}

func isRootOwnerWorkspacePath(path string) bool {
	clean := filepath.ToSlash(strings.TrimSpace(path))
	return clean == "" || clean == "."
}

func validateOwnerReadPath(op proto.FsOperation, path string) error {
	clean := filepath.ToSlash(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		switch op {
		case proto.FsOpLookup, proto.FsOpGetAttr, proto.FsOpReadDir:
			return nil
		default:
			return proto.NewError(proto.ErrUnsupportedPath, "root path only supports lookup/getattr/readdir")
		}
	}
	return validateOwnerWorkspacePath(clean)
}

func validateOwnerMutationPath(op proto.FsOperation, path string, newPath string) error {
	if err := validateOwnerWorkspacePath(path); err != nil {
		return err
	}
	if op == proto.FsOpRename {
		return validateOwnerWorkspacePath(newPath)
	}
	return nil
}

func validateOwnerWorkspacePath(path string) error {
	if err := portablefs.ValidateRelativePath(path); err != nil {
		return proto.NewError(proto.ErrUnsupportedPath, err.Error())
	}
	return nil
}

func normalizeOwnerMetadata(metadata proto.FileMetadata) proto.FileMetadata {
	metadata.MTime = metadata.MTime.UTC().Round(time.Second)
	return metadata
}

func ownerFileInfoMetadata(info fs.FileInfo) proto.FileMetadata {
	return normalizeOwnerMetadata(proto.FileMetadata{
		Mode:  uint32(info.Mode().Perm()),
		MTime: info.ModTime(),
		Size:  info.Size(),
	})
}

func replaceOwnerFile(path string, data []byte, metadata proto.FileMetadata) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tempPath := path + ".symterm-owner"
	if err := os.WriteFile(tempPath, data, fs.FileMode(metadata.Mode)); err != nil {
		return err
	}
	if err := applyOwnerFileMetadata(tempPath, metadata); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "remove owner temp file "+tempPath, os.Remove(tempPath))
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "remove owner temp file "+tempPath, os.Remove(tempPath))
		return err
	}
	return nil
}

func replaceOwnerFileFromTemp(tempPath string, path string, metadata proto.FileMetadata) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := applyOwnerFileMetadata(tempPath, metadata); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "remove owner temp file "+tempPath, os.Remove(tempPath))
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "remove owner temp file "+tempPath, os.Remove(tempPath))
		return err
	}
	return nil
}

func applyOwnerFileMetadata(path string, metadata proto.FileMetadata) error {
	if err := os.Chmod(path, fs.FileMode(metadata.Mode)); err != nil {
		return err
	}
	return os.Chtimes(path, metadata.MTime, metadata.MTime)
}

func applyOwnerDirMetadata(path string, metadata proto.FileMetadata) error {
	return os.Chtimes(path, metadata.MTime, metadata.MTime)
}

func removeOwnerPath(path string, op proto.FsOperation) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	switch op {
	case proto.FsOpRemove:
		if info.IsDir() {
			return proto.NewError(proto.ErrConflict, "remove requires a non-directory path")
		}
		return os.Remove(path)
	case proto.FsOpRmdir:
		if !info.IsDir() {
			return proto.NewError(proto.ErrConflict, "rmdir requires a directory path")
		}
		return os.Remove(path)
	default:
		return proto.NewError(proto.ErrInvalidArgument, "unsupported remove operation")
	}
}

func renameOwnerPath(source string, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if existing, err := os.Stat(target); err == nil {
		if existing.IsDir() != info.IsDir() {
			return proto.NewError(proto.ErrConflict, "rename target kind changed")
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.Rename(source, target)
}

func cleanupOwnerUpload(upload *ownerUploadSession) error {
	if upload.file != nil {
		diagnostic.Cleanup(diagnostic.Default(), "close owner upload temp file", upload.file.Close())
	}
	if upload.tempPath != "" {
		diagnostic.Cleanup(diagnostic.Default(), "remove owner upload temp file "+upload.tempPath, os.Remove(upload.tempPath))
	}
	return nil
}
