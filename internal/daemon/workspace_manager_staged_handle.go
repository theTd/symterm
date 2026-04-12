package daemon

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"symterm/internal/diagnostic"
	"symterm/internal/invalidation"
	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

const ownerFileUploadChunkSize = 64 * 1024

type handleReadTarget struct {
	tempPath    string
	metadata    proto.FileMetadata
	isDir       bool
	backingKind stagedHandleBackingKind
}

func (m *WorkspaceManager) readTargetForHandle(projectKey proto.ProjectKey, request proto.FsRequest) (handleReadTarget, bool, error) {
	if request.HandleID == "" {
		return handleReadTarget{}, false, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	handle, err := m.handleLocked(projectKey, request.HandleID)
	if err != nil {
		return handleReadTarget{}, false, err
	}
	if handle.path != request.Path {
		return handleReadTarget{}, false, proto.NewError(proto.ErrInvalidArgument, "handle does not match requested path")
	}
	if handle.targetKind == stagedHandleTargetNone {
		return handleReadTarget{}, false, nil
	}
	return handleReadTarget{
		tempPath:    handle.tempPath,
		metadata:    handle.metadata,
		isDir:       handle.targetKind == stagedHandleTargetDir,
		backingKind: handle.backingKind,
	}, true, nil
}

func effectiveOpenIntent(request proto.FsRequest) proto.FsOpenIntent {
	switch request.OpenIntent {
	case proto.FsOpenIntentReadOnly, proto.FsOpenIntentWritable:
		return request.OpenIntent
	default:
		return proto.FsOpenIntentWritable
	}
}

func isWritableHandleMutation(op proto.FsOperation) bool {
	switch op {
	case proto.FsOpWrite, proto.FsOpTruncate, proto.FsOpChmod, proto.FsOpUtimens:
		return true
	default:
		return false
	}
}

func (m *WorkspaceManager) stageHandleMutation(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, op proto.FsOperation, request proto.FsRequest, preconditions []proto.MutationPrecondition) (proto.FsReply, error) {
	handle, err := m.ensureHandle(ctx, projectKey, layout, op, request, preconditions, op == proto.FsOpCreate)
	if err != nil {
		return proto.FsReply{}, err
	}
	if result, ok, err := m.failedHandleResult(projectKey, handle); ok {
		return replyWithHandle(result, handle.handleID), err
	}
	if err := m.prepareHandleForMutation(projectKey, handle); err != nil {
		return proto.FsReply{HandleID: handle.handleID}, err
	}

	switch op {
	case proto.FsOpOpen:
		if handle.targetKind != stagedHandleTargetFile {
			return proto.FsReply{}, proto.NewError(proto.ErrConflict, "open requires a regular file path")
		}
	case proto.FsOpCreate:
		file, err := os.OpenFile(handle.tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(request.Mode))
		if err != nil {
			return proto.FsReply{}, err
		}
		if len(request.Data) > 0 {
			if _, err := file.Write(request.Data); err != nil {
				diagnostic.Cleanup(diagnostic.Default(), "close staged file "+handle.tempPath, file.Close())
				return proto.FsReply{}, err
			}
		}
		if err := file.Close(); err != nil {
			return proto.FsReply{}, err
		}
		handle.metadata.Mode = request.Mode
		handle.metadata.Size = int64(len(request.Data))
		handle.metadata.MTime = time.Now().UTC()
	case proto.FsOpWrite:
		file, err := os.OpenFile(handle.tempPath, os.O_WRONLY|os.O_CREATE, fs.FileMode(handle.metadata.Mode))
		if err != nil {
			return proto.FsReply{}, err
		}
		if _, err := file.Seek(request.Offset, io.SeekStart); err != nil {
			diagnostic.Cleanup(diagnostic.Default(), "close staged file "+handle.tempPath, file.Close())
			return proto.FsReply{}, err
		}
		if _, err := file.Write(request.Data); err != nil {
			diagnostic.Cleanup(diagnostic.Default(), "close staged file "+handle.tempPath, file.Close())
			return proto.FsReply{}, err
		}
		if err := file.Close(); err != nil {
			return proto.FsReply{}, err
		}
		info, err := os.Stat(handle.tempPath)
		if err != nil {
			return proto.FsReply{}, err
		}
		handle.metadata.Size = info.Size()
		handle.metadata.MTime = time.Now().UTC()
	case proto.FsOpTruncate:
		if err := os.Truncate(handle.tempPath, request.Size); err != nil {
			return proto.FsReply{}, err
		}
		handle.metadata.Size = request.Size
		handle.metadata.MTime = time.Now().UTC()
	case proto.FsOpChmod:
		handle.metadata.Mode = request.Mode
		handle.metadata.MTime = time.Now().UTC()
	case proto.FsOpUtimens:
		handle.metadata.MTime = time.Now().UTC()
	default:
		return proto.FsReply{}, proto.NewError(proto.ErrInvalidArgument, "unsupported staged handle mutation")
	}

	if op == proto.FsOpOpen {
		handle.state = stagedHandleClean
	} else {
		handle.op = op
		handle.state = stagedHandleDirty
	}
	m.mu.Lock()
	m.stateLocked(projectKey).handles.open[handle.handleID] = handle
	m.mu.Unlock()

	reply := m.withGenerations(projectKey, request.Path, proto.FsReply{})
	reply.HandleID = handle.handleID
	return reply, nil
}

func (m *WorkspaceManager) stageDentryMutation(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, op proto.FsOperation, request proto.FsRequest, preconditions []proto.MutationPrecondition) (proto.FsReply, error) {
	handle, err := m.ensureHandle(ctx, projectKey, layout, op, request, preconditions, false)
	if err != nil {
		return proto.FsReply{}, err
	}
	if result, ok, err := m.failedHandleResult(projectKey, handle); ok {
		return replyWithHandle(result, handle.handleID), err
	}
	if err := m.prepareHandleForMutation(projectKey, handle); err != nil {
		return proto.FsReply{HandleID: handle.handleID}, err
	}
	if op == proto.FsOpRename && strings.TrimSpace(request.NewPath) == "" {
		return proto.FsReply{}, proto.NewError(proto.ErrInvalidArgument, "rename requires a destination path")
	}

	handle.state = stagedHandleDirty
	m.mu.Lock()
	m.stateLocked(projectKey).handles.open[handle.handleID] = handle
	m.mu.Unlock()

	reply := m.withGenerations(projectKey, request.Path, proto.FsReply{})
	reply.HandleID = handle.handleID
	return reply, nil
}

func (m *WorkspaceManager) commitHandle(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, request proto.FsRequest) (proto.FsReply, error) {
	if request.HandleID == "" {
		return proto.FsReply{}, proto.NewError(proto.ErrInvalidArgument, "flush/fsync requires a handle id")
	}

	for {
		var (
			handle            *stagedHandle
			conservativePaths []string
			transaction       *stagedHandleCommitTransaction
			err               error
		)

		m.mu.Lock()
		state := m.stateLocked(projectKey)
		handle, err = m.handleLocked(projectKey, request.HandleID)
		if err != nil {
			if result, ok := state.handles.closedResultLocked(request.HandleID); ok {
				m.mu.Unlock()
				return replyWithHandle(result, request.HandleID), result.err
			}
			m.mu.Unlock()
			return proto.FsReply{}, err
		}
		switch handle.state {
		case stagedHandleCommitted, stagedHandleFailed:
			if result, ok := state.handles.openResultLocked(handle); ok {
				m.mu.Unlock()
				return replyWithHandle(result, handle.handleID), result.err
			}
			m.mu.Unlock()
			return proto.FsReply{}, proto.NewError(proto.ErrFileCommitFailed, "handle result is unavailable")
		case stagedHandleClean:
			result := state.handles.setHandleResultLocked(handle, stagedHandleCommitted, state.committedReplyLocked(handle.path), nil)
			state.handles.open[handle.handleID] = handle
			m.mu.Unlock()
			return replyWithHandle(result, handle.handleID), nil
		case stagedHandleCommitting:
			transaction, _ = state.handles.transactionLocked(handle)
			m.mu.Unlock()
			if err := waitForCommitTransaction(ctx, transaction); err != nil {
				return proto.FsReply{HandleID: request.HandleID}, err
			}
			continue
		case stagedHandleDirty:
			conservativePaths = conservativePathsForMutation(proto.FsRequest{
				Path:    handle.path,
				NewPath: handle.newPath,
			}, handle.preconditions)
			m.mu.Unlock()
		default:
			m.mu.Unlock()
			return proto.FsReply{}, proto.NewError(proto.ErrFileCommitFailed, "unsupported staged handle state")
		}

		if err := m.revalidateConservativePaths(ctx, projectKey, layout, conservativePaths); err != nil {
			return proto.FsReply{HandleID: request.HandleID}, err
		}

		var transactionID string

		m.mu.Lock()
		state = m.stateLocked(projectKey)
		handle, err = m.handleLocked(projectKey, request.HandleID)
		if err != nil {
			if result, ok := state.handles.closedResultLocked(request.HandleID); ok {
				m.mu.Unlock()
				return replyWithHandle(result, request.HandleID), result.err
			}
			m.mu.Unlock()
			return proto.FsReply{}, err
		}
		switch handle.state {
		case stagedHandleCommitted, stagedHandleFailed, stagedHandleClean:
			m.mu.Unlock()
			continue
		case stagedHandleCommitting:
			transaction, _ = state.handles.transactionLocked(handle)
			m.mu.Unlock()
			if err := waitForCommitTransaction(ctx, transaction); err != nil {
				return proto.FsReply{HandleID: request.HandleID}, err
			}
			continue
		}
		for _, condition := range handle.preconditions {
			if err := state.checkPreconditionsLocked(condition.Path, condition); err != nil {
				result := state.handles.setHandleResultLocked(handle, stagedHandleFailed, proto.FsReply{Conflict: true}, err)
				state.handles.open[handle.handleID] = handle
				m.mu.Unlock()
				return replyWithHandle(result, handle.handleID), err
			}
		}
		transaction = state.handles.beginTransactionLocked(handle)
		handle.state = stagedHandleCommitting
		state.handles.open[handle.handleID] = handle
		transactionID = transaction.transactionID
		m.mu.Unlock()

		reply, commitErr := m.commitStagedHandle(ctx, projectKey, layout, handle)

		m.mu.Lock()
		state = m.stateLocked(projectKey)
		handle, err = m.handleLocked(projectKey, request.HandleID)
		if err != nil {
			if transaction != nil {
				delete(state.handles.transactions, transactionID)
				close(transaction.done)
			}
			m.mu.Unlock()
			if commitErr != nil {
				return proto.FsReply{HandleID: request.HandleID}, commitErr
			}
			reply.HandleID = request.HandleID
			return reply, nil
		}
		if handle.transactionID != transactionID {
			m.mu.Unlock()
			continue
		}
		terminalState := stagedHandleCommitted
		if commitErr != nil {
			terminalState = stagedHandleFailed
		}
		result := state.handles.finishTransactionLocked(handle, terminalState, reply, commitErr)
		state.handles.open[handle.handleID] = handle
		m.mu.Unlock()
		return replyWithHandle(result, handle.handleID), commitErr
	}
}

func (m *WorkspaceManager) releaseHandle(ctx context.Context, projectKey proto.ProjectKey, request proto.FsRequest) (proto.FsReply, error) {
	if request.HandleID == "" {
		return proto.FsReply{}, nil
	}

	for {
		m.mu.Lock()
		state := m.stateLocked(projectKey)
		handle, err := m.handleLocked(projectKey, request.HandleID)
		if err != nil {
			if result, ok := state.handles.closedResultLocked(request.HandleID); ok {
				m.mu.Unlock()
				return replyWithHandle(result, request.HandleID), result.err
			}
			m.mu.Unlock()
			return proto.FsReply{}, err
		}
		if handle.state == stagedHandleCommitting {
			transaction, _ := state.handles.transactionLocked(handle)
			m.mu.Unlock()
			if err := waitForCommitTransaction(ctx, transaction); err != nil {
				return proto.FsReply{HandleID: request.HandleID}, err
			}
			continue
		}

		delete(state.handles.open, request.HandleID)

		resultID := handle.resultID
		if resultID == "" {
			resultID = state.handles.recordResultLocked(proto.FsReply{}, nil)
		}
		state.handles.closed[request.HandleID] = closedHandleOutcome{resultID: resultID}
		result, _ := state.handles.results[resultID]
		m.mu.Unlock()

		if handle.tempPath != "" {
			diagnostic.Cleanup(diagnostic.Default(), "remove staged handle temp file "+handle.tempPath, os.Remove(handle.tempPath))
		}
		return replyWithHandle(result, request.HandleID), result.err
	}
}

func replyWithHandle(result stagedHandleResult, handleID string) proto.FsReply {
	reply := result.reply
	reply.HandleID = handleID
	return reply
}

func waitForCommitTransaction(ctx context.Context, transaction *stagedHandleCommitTransaction) error {
	if transaction == nil {
		return proto.NewError(proto.ErrFileCommitFailed, "handle commit transaction is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-transaction.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *WorkspaceManager) failedHandleResult(projectKey proto.ProjectKey, handle *stagedHandle) (stagedHandleResult, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if handle.state != stagedHandleFailed {
		return stagedHandleResult{}, false, nil
	}
	result, ok := m.stateLocked(projectKey).handles.openResultLocked(handle)
	if !ok {
		return stagedHandleResult{}, true, proto.NewError(proto.ErrFileCommitFailed, "failed handle result is unavailable")
	}
	return result, true, result.err
}

func (m *WorkspaceManager) prepareHandleForMutation(projectKey proto.ProjectKey, handle *stagedHandle) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if handle.state == stagedHandleCommitting {
		return proto.NewError(proto.ErrConflict, "handle commit is in progress")
	}
	if handle.state == stagedHandleCommitted {
		m.stateLocked(projectKey).handles.clearHandleResultLocked(handle)
	}
	return nil
}

func (m *WorkspaceManager) ensureHandle(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, op proto.FsOperation, request proto.FsRequest, preconditions []proto.MutationPrecondition, createEmpty bool) (*stagedHandle, error) {
	if request.HandleID != "" {
		m.mu.Lock()
		handle, err := m.handleLocked(projectKey, request.HandleID)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if handle.path != request.Path {
			m.mu.Unlock()
			return nil, proto.NewError(proto.ErrInvalidArgument, "handle does not match requested path")
		}
		if handle.newPath != "" || request.NewPath != "" {
			if handle.newPath != request.NewPath {
				m.mu.Unlock()
				return nil, proto.NewError(proto.ErrInvalidArgument, "handle does not match requested rename target")
			}
		}
		if handle.state == stagedHandleCommitting {
			m.mu.Unlock()
			return nil, proto.NewError(proto.ErrConflict, "handle commit is in progress")
		}
		needsPromotion := handle.backingKind == stagedHandleBackingOwnerReadThrough && isWritableHandleMutation(op)
		m.mu.Unlock()
		if needsPromotion {
			if err := m.promoteHandleToStaged(ctx, projectKey, layout, request.HandleID, request.Path); err != nil {
				return nil, err
			}
			m.mu.Lock()
			handle, err = m.handleLocked(projectKey, request.HandleID)
			m.mu.Unlock()
			if err != nil {
				return nil, err
			}
		}
		return handle, nil
	}

	if err := os.MkdirAll(layout.RuntimeDir, 0o755); err != nil {
		return nil, err
	}

	tempPath := ""
	var err error
	targetKind := stagedHandleTargetFile
	backingKind := stagedHandleBackingStagedTemp
	openIntent := effectiveOpenIntent(request)
	switch op {
	case proto.FsOpMkdir:
		targetKind = stagedHandleTargetDir
		tempPath, err = os.MkdirTemp(layout.RuntimeDir, "fs-stage-dir-*")
		if err != nil {
			return nil, err
		}
	default:
		if op == proto.FsOpRemove || op == proto.FsOpRmdir || op == proto.FsOpRename {
			targetKind = stagedHandleTargetNone
			tempFile, err := os.CreateTemp(layout.RuntimeDir, "fs-stage-*")
			if err != nil {
				return nil, err
			}
			tempPath = tempFile.Name()
			if err := tempFile.Close(); err != nil {
				diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
				return nil, err
			}
		}
	}

	metadata := proto.FileMetadata{
		Mode:  request.Mode,
		MTime: time.Now().UTC(),
		Size:  0,
	}
	target := m.stagingBasePath(projectKey, layout, request.Path)
	if targetKind == stagedHandleTargetFile && op == proto.FsOpOpen && m.authorityForProject(projectKey).client != nil && openIntent == proto.FsOpenIntentReadOnly {
		authority := m.authorityForProject(projectKey)
		metadata, err = ownerFileMetadata(ctx, authority.client, request.Path)
		if err != nil {
			if tempPath != "" {
				diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
			}
			return nil, err
		}
		if tempPath != "" {
			diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
		}
		tempPath = ""
		backingKind = stagedHandleBackingOwnerReadThrough
	} else if createEmpty && targetKind == stagedHandleTargetFile {
		tempPath, metadata, err = m.createStagedFileBacking(ctx, projectKey, layout, request.Path, true, false)
		if err != nil {
			return nil, err
		}
	} else if !createEmpty && targetKind == stagedHandleTargetFile {
		var err error
		tempPath, metadata, err = m.createStagedFileBacking(ctx, projectKey, layout, request.Path, false, op == proto.FsOpOpen)
		if err != nil {
			if tempPath != "" {
				diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
			}
			return nil, err
		}
	}
	if targetKind == stagedHandleTargetDir {
		metadata = normalizeMetadata(proto.FileMetadata{
			Mode:  request.Mode,
			MTime: time.Now().UTC(),
			Size:  0,
		})
	}
	if targetKind == stagedHandleTargetNone {
		if info, err := os.Stat(target); err == nil {
			metadata = fileInfoMetadata(info)
		} else if !errorsIsNotExist(err) {
			diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
			return nil, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.stateLocked(projectKey)
	state.handles.nextHandleID++
	handleID := fmt.Sprintf("handle-%04d", state.handles.nextHandleID)
	handle := &stagedHandle{
		handleID:      handleID,
		op:            op,
		path:          request.Path,
		newPath:       request.NewPath,
		tempPath:      tempPath,
		targetKind:    targetKind,
		backingKind:   backingKind,
		openIntent:    openIntent,
		metadata:      metadata,
		preconditions: clonePreconditions(preconditions),
		state:         stagedHandleClean,
	}
	state.handles.open[handleID] = handle
	return handle, nil
}

func ownerFileMetadata(ctx context.Context, client ownerfs.Client, workspacePath string) (proto.FileMetadata, error) {
	reply, err := client.FsRead(ctx, proto.FsOpGetAttr, proto.FsRequest{Path: workspacePath})
	if err != nil {
		return proto.FileMetadata{}, err
	}
	if reply.IsDir {
		return proto.FileMetadata{}, proto.NewError(proto.ErrConflict, "open requires a regular file path")
	}
	return normalizeMetadata(reply.Metadata), nil
}

func (m *WorkspaceManager) createStagedFileBacking(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, workspacePath string, createEmpty bool, failIfMissing bool) (string, proto.FileMetadata, error) {
	tempFile, err := os.CreateTemp(layout.RuntimeDir, "fs-stage-*")
	if err != nil {
		return "", proto.FileMetadata{}, err
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
		return "", proto.FileMetadata{}, err
	}

	metadata := proto.FileMetadata{
		Mode:  0,
		MTime: time.Now().UTC(),
		Size:  0,
	}
	if createEmpty {
		return tempPath, metadata, nil
	}

	authority := m.authorityForProject(projectKey)
	if authority.client != nil {
		metadata, copied, err := streamOwnerFileToTemp(ctx, authority.client, workspacePath, tempPath, failIfMissing)
		if err != nil {
			diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
			return "", proto.FileMetadata{}, err
		}
		if copied {
			return tempPath, metadata, nil
		}
		return tempPath, metadata, nil
	}

	target := m.stagingBasePath(projectKey, layout, workspacePath)
	if info, err := os.Stat(target); err == nil {
		metadata = fileInfoMetadata(info)
		if err := copyFileContents(target, tempPath, metadata.Mode); err != nil {
			diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
			return "", proto.FileMetadata{}, err
		}
		return tempPath, metadata, nil
	} else if !errorsIsNotExist(err) {
		diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
		return "", proto.FileMetadata{}, err
	}
	if failIfMissing {
		diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
		return "", proto.FileMetadata{}, os.ErrNotExist
	}
	return tempPath, metadata, nil
}

func streamOwnerFileToTemp(ctx context.Context, client ownerfs.Client, workspacePath string, tempPath string, failIfMissing bool) (proto.FileMetadata, bool, error) {
	metadata, err := ownerFileMetadata(ctx, client, workspacePath)
	if err != nil {
		if isMissingPathError(err) && !failIfMissing {
			return proto.FileMetadata{}, false, nil
		}
		return proto.FileMetadata{}, false, err
	}

	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(metadata.Mode))
	if err != nil {
		return proto.FileMetadata{}, false, err
	}
	for offset := int64(0); ; {
		reply, err := client.FsRead(ctx, proto.FsOpRead, proto.FsRequest{
			Path:   workspacePath,
			Offset: offset,
			Size:   ownerFileUploadChunkSize,
		})
		if err != nil {
			diagnostic.Cleanup(diagnostic.Default(), "close staged file "+tempPath, file.Close())
			return proto.FileMetadata{}, false, err
		}
		if len(reply.Data) == 0 {
			break
		}
		if _, err := file.Write(reply.Data); err != nil {
			diagnostic.Cleanup(diagnostic.Default(), "close staged file "+tempPath, file.Close())
			return proto.FileMetadata{}, false, err
		}
		offset += int64(len(reply.Data))
	}
	if err := file.Close(); err != nil {
		return proto.FileMetadata{}, false, err
	}
	return metadata, true, nil
}

func (m *WorkspaceManager) promoteHandleToStaged(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, handleID string, workspacePath string) error {
	tempPath, metadata, err := m.createStagedFileBacking(ctx, projectKey, layout, workspacePath, false, true)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	handle, err := m.handleLocked(projectKey, handleID)
	if err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
		return err
	}
	if handle.backingKind != stagedHandleBackingOwnerReadThrough {
		diagnostic.Cleanup(diagnostic.Default(), "remove stage temp file "+tempPath, os.Remove(tempPath))
		return nil
	}
	handle.tempPath = tempPath
	handle.targetKind = stagedHandleTargetFile
	handle.backingKind = stagedHandleBackingStagedTemp
	handle.openIntent = proto.FsOpenIntentWritable
	handle.metadata = metadata
	return nil
}

func (m *WorkspaceManager) handleLocked(projectKey proto.ProjectKey, handleID string) (*stagedHandle, error) {
	handle, ok := m.stateLocked(projectKey).handles.open[handleID]
	if !ok {
		return nil, proto.NewError(proto.ErrUnknownHandle, "staged handle does not exist")
	}
	return handle, nil
}

func (m *WorkspaceManager) commitStagedHandle(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, handle *stagedHandle) (proto.FsReply, error) {
	switch handle.op {
	case proto.FsOpCreate, proto.FsOpWrite, proto.FsOpTruncate, proto.FsOpChmod, proto.FsOpUtimens:
		return m.commitStagedFile(ctx, projectKey, layout, handle)
	case proto.FsOpMkdir:
		return m.commitStagedMkdir(ctx, projectKey, layout, handle)
	case proto.FsOpRemove, proto.FsOpRmdir:
		return m.commitStagedDelete(ctx, projectKey, layout, handle)
	case proto.FsOpRename:
		return m.commitStagedRename(ctx, projectKey, layout, handle)
	default:
		return proto.FsReply{}, proto.NewError(proto.ErrInvalidArgument, "unsupported staged handle operation")
	}
}

func (m *WorkspaceManager) commitStagedFile(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, handle *stagedHandle) (proto.FsReply, error) {
	authority := m.authorityForProject(projectKey)
	if authority.client != nil {
		if err := m.streamOwnerFileApply(ctx, authority.client, handle); err != nil {
			return proto.FsReply{}, wrapOwnerWriteError(err)
		}
	}
	mirrorRequired, err := publishedMountRequiresMirror(layout.MountDir)
	if err != nil {
		return proto.FsReply{}, wrapOwnerWriteError(err)
	}

	target := canonicalWorkspacePath(layout, handle.path)
	mirrorTarget := target
	if mirrorRequired {
		mirrorTarget = publishedWorkspacePath(layout, handle.path)
	}
	if authority.client == nil {
		target = m.authoritativePath(authority, layout, handle.path)
	}
	targetSnapshot, err := captureFileSnapshot(target)
	if err != nil {
		return proto.FsReply{}, wrapOwnerWriteError(err)
	}
	mirrorSnapshot := fileSnapshot{}
	if target != mirrorTarget {
		mirrorSnapshot, err = captureFileSnapshot(mirrorTarget)
		if err != nil {
			return proto.FsReply{}, wrapOwnerWriteError(err)
		}
	}
	restoreCommittedView := func(commitErr error) error {
		if rollbackErr := restoreFileSnapshot(target, targetSnapshot); rollbackErr != nil {
			return wrapOwnerWriteError(fmt.Errorf("%w; rollback failed: %v", commitErr, rollbackErr))
		}
		if target != mirrorTarget {
			if rollbackErr := restoreFileSnapshot(mirrorTarget, mirrorSnapshot); rollbackErr != nil {
				return wrapOwnerWriteError(fmt.Errorf("%w; rollback failed: %v", commitErr, rollbackErr))
			}
		}
		return wrapOwnerWriteError(commitErr)
	}
	if err := m.replaceFileFromStaged(handle.tempPath, target, handle.metadata); err != nil {
		return proto.FsReply{}, restoreCommittedView(err)
	}
	if target != mirrorTarget {
		if err := m.mirrorCommittedFile(target, mirrorTarget, handle.metadata.Mode); err != nil {
			return proto.FsReply{}, restoreCommittedView(err)
		}
		if err := applyFileMetadata(mirrorTarget, handle.metadata); err != nil {
			return proto.FsReply{}, restoreCommittedView(err)
		}
	}
	if err := m.beforeFileGeneration(handle.path); err != nil {
		return proto.FsReply{}, restoreCommittedView(err)
	}

	m.mu.Lock()
	reply := m.stateLocked(projectKey).bumpPathGenerationsLocked(handle.path)
	m.mu.Unlock()
	diagnostic.Error(diagnostic.Default(), "seed conservative paths for "+handle.path, m.seedConservativePaths(ctx, projectKey, layout, conservativePathsForAccess(handle.path)))
	reply.Invalidations = invalidation.DataPath(handle.path)
	return reply, nil
}

func (m *WorkspaceManager) streamOwnerFileApply(ctx context.Context, client ownerfs.Client, handle *stagedHandle) error {
	file, err := os.Open(handle.tempPath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	metadata := handle.metadata
	metadata.Size = info.Size()

	started, err := client.BeginFileUpload(ctx, proto.OwnerFileBeginRequest{
		Op:           handle.op,
		Path:         handle.path,
		Metadata:     metadata,
		ExpectedSize: metadata.Size,
	})
	if err != nil {
		return err
	}

	abortUpload := func(reason string) {
		abortCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		diagnostic.Cleanup(diagnostic.Default(), "abort owner file upload "+handle.path, client.AbortFileUpload(abortCtx, proto.OwnerFileAbortRequest{
			UploadID: started.UploadID,
			Reason:   reason,
		}))
	}

	buf := make([]byte, ownerFileUploadChunkSize)
	var offset int64
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			if err := client.ApplyFileChunk(ctx, proto.OwnerFileApplyChunkRequest{
				UploadID: started.UploadID,
				Offset:   offset,
				Data:     append([]byte(nil), buf[:n]...),
			}); err != nil {
				abortUpload(err.Error())
				return err
			}
			offset += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			abortUpload(readErr.Error())
			return readErr
		}
	}

	if err := client.CommitFileUpload(ctx, proto.OwnerFileCommitRequest{
		UploadID: started.UploadID,
	}); err != nil {
		abortUpload(err.Error())
		return err
	}
	return nil
}

func (m *WorkspaceManager) commitStagedMkdir(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, handle *stagedHandle) (proto.FsReply, error) {
	authority := m.authorityForProject(projectKey)
	if authority.client != nil {
		if err := authority.client.Apply(ctx, proto.OwnerFileApplyRequest{
			Op:       handle.op,
			Path:     handle.path,
			Metadata: handle.metadata,
		}); err != nil {
			return proto.FsReply{}, wrapOwnerWriteError(err)
		}
	}
	mirrorRequired, err := publishedMountRequiresMirror(layout.MountDir)
	if err != nil {
		return proto.FsReply{}, wrapOwnerWriteError(err)
	}

	target := canonicalWorkspacePath(layout, handle.path)
	if authority.client == nil {
		target = m.authoritativePath(authority, layout, handle.path)
	}
	if err := os.Mkdir(target, fs.FileMode(handle.metadata.Mode)); err != nil {
		return proto.FsReply{}, wrapOwnerWriteError(err)
	}
	if err := applyDirMetadata(target, handle.metadata); err != nil {
		return proto.FsReply{}, wrapOwnerWriteError(err)
	}
	mirrorTarget := target
	if mirrorRequired {
		mirrorTarget = publishedWorkspacePath(layout, handle.path)
	}
	if target != mirrorTarget {
		if err := os.MkdirAll(filepath.Dir(mirrorTarget), 0o755); err != nil {
			return proto.FsReply{}, wrapOwnerWriteError(err)
		}
		if err := os.Mkdir(mirrorTarget, fs.FileMode(handle.metadata.Mode)); err != nil {
			return proto.FsReply{}, wrapOwnerWriteError(err)
		}
		if err := applyDirMetadata(mirrorTarget, handle.metadata); err != nil {
			return proto.FsReply{}, wrapOwnerWriteError(err)
		}
	}

	m.mu.Lock()
	reply := m.stateLocked(projectKey).bumpPathGenerationsLocked(handle.path)
	m.mu.Unlock()
	diagnostic.Error(diagnostic.Default(), "seed conservative paths for "+handle.path, m.seedConservativePaths(ctx, projectKey, layout, conservativePathsForAccess(handle.path)))
	reply.Invalidations = invalidation.MkdirPath(handle.path)
	return reply, nil
}

func (m *WorkspaceManager) commitStagedDelete(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, handle *stagedHandle) (proto.FsReply, error) {
	authority := m.authorityForProject(projectKey)
	if authority.client != nil {
		if err := authority.client.Apply(ctx, proto.OwnerFileApplyRequest{
			Op:   handle.op,
			Path: handle.path,
		}); err != nil {
			return proto.FsReply{}, wrapOwnerWriteError(err)
		}
	}
	mirrorRequired, err := publishedMountRequiresMirror(layout.MountDir)
	if err != nil {
		return proto.FsReply{}, wrapOwnerWriteError(err)
	}
	target := canonicalWorkspacePath(layout, handle.path)
	if authority.client == nil {
		target = m.authoritativePath(authority, layout, handle.path)
	}
	if err := removePathForOperation(target, handle.op); err != nil {
		return proto.FsReply{}, wrapOwnerWriteError(err)
	}
	mirrorTarget := target
	if mirrorRequired {
		mirrorTarget = publishedWorkspacePath(layout, handle.path)
	}
	if target != mirrorTarget {
		if err := removePathForOperation(mirrorTarget, handle.op); err != nil && !errorsIsNotExist(err) {
			return proto.FsReply{}, wrapOwnerWriteError(err)
		}
	}

	m.mu.Lock()
	reply := m.stateLocked(projectKey).bumpDeleteGenerationsLocked(handle.path)
	m.stateLocked(projectKey).clearConservativePathsLocked(handle.path, parentPath(handle.path))
	m.mu.Unlock()
	diagnostic.Error(diagnostic.Default(), "seed conservative paths for "+parentPath(handle.path), m.seedConservativePaths(ctx, projectKey, layout, []string{parentPath(handle.path)}))
	reply.Invalidations = invalidation.DeletePath(handle.path)
	return reply, nil
}

func (m *WorkspaceManager) commitStagedRename(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, handle *stagedHandle) (proto.FsReply, error) {
	if handle.path == handle.newPath {
		return m.withGenerations(projectKey, handle.path, proto.FsReply{}), nil
	}

	authority := m.authorityForProject(projectKey)
	if authority.client != nil {
		if err := authority.client.Apply(ctx, proto.OwnerFileApplyRequest{
			Op:      handle.op,
			Path:    handle.path,
			NewPath: handle.newPath,
		}); err != nil {
			return proto.FsReply{}, wrapOwnerWriteError(err)
		}
	}
	mirrorRequired, err := publishedMountRequiresMirror(layout.MountDir)
	if err != nil {
		return proto.FsReply{}, wrapOwnerWriteError(err)
	}

	source := canonicalWorkspacePath(layout, handle.path)
	target := canonicalWorkspacePath(layout, handle.newPath)
	if authority.client == nil {
		source = m.authoritativePath(authority, layout, handle.path)
		target = m.authoritativePath(authority, layout, handle.newPath)
	}
	if err := renamePathWithOverwrite(source, target); err != nil {
		return proto.FsReply{}, wrapOwnerWriteError(err)
	}

	mirrorSource := source
	mirrorTarget := target
	if mirrorRequired {
		mirrorSource = publishedWorkspacePath(layout, handle.path)
		mirrorTarget = publishedWorkspacePath(layout, handle.newPath)
	}
	if source != mirrorSource || target != mirrorTarget {
		if err := syncRenamedMirrorPath(source, mirrorSource, mirrorTarget); err != nil {
			return proto.FsReply{}, wrapOwnerWriteError(err)
		}
	}

	m.mu.Lock()
	reply := m.stateLocked(projectKey).bumpRenameGenerationsLocked(handle.path, handle.newPath)
	m.stateLocked(projectKey).clearConservativePathsLocked(handle.path, handle.newPath, parentPath(handle.path), parentPath(handle.newPath))
	m.mu.Unlock()
	diagnostic.Error(diagnostic.Default(), "seed conservative paths for rename "+handle.path+" -> "+handle.newPath, m.seedConservativePaths(ctx, projectKey, layout, []string{handle.path, handle.newPath, parentPath(handle.path), parentPath(handle.newPath)}))
	reply.Invalidations = invalidation.RenamePaths(handle.path, handle.newPath)
	return reply, nil
}
