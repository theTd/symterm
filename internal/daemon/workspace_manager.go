package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
)

type WorkspaceManager struct {
	mu            sync.Mutex
	projectsRoot  string
	states        map[string]*workspaceState
	authorities   map[string]workspaceAuthority
	authorityCond *sync.Cond
	hooks         workspaceManagerHooks
}

type workspaceManagerHooks struct {
	replaceFileFromStaged func(source string, target string, metadata proto.FileMetadata) error
	mirrorFile            func(source string, target string, mode uint32) error
	beforeFileGeneration  func(path string) error
}

type workspaceState struct {
	// committed is the published workspace truth visible to callers.
	committed workspaceCommittedState
	// sync tracks the in-flight reconcile session and uploads.
	sync workspaceSyncState
	// handles owns open staged handles and their terminal outcomes.
	handles workspaceHandleState
	// validation tracks conservative-mode observations used for revalidation.
	validation workspaceValidationState
}

type workspaceCommittedState struct {
	objectGen      map[string]uint64
	dirGen         map[string]uint64
	objectIdentity map[string]string
	nextObjectID   uint64
	manifest       map[string]proto.ManifestEntry
	generation     uint64
}

type workspaceSyncState struct {
	activeEpoch uint64
	sessions    map[uint64]*syncSession
}

type workspaceHandleState struct {
	open         map[string]*stagedHandle
	closed       map[string]closedHandleOutcome
	results      map[string]stagedHandleResult
	transactions map[string]*stagedHandleCommitTransaction
	nextHandleID uint64
	nextResultID uint64
	nextTxnID    uint64
}

type workspaceValidationState struct {
	conservative  bool
	objectState   map[string]string
	directoryView map[string]string
}

type stagedHandleState string

const (
	stagedHandleClean      stagedHandleState = "clean"
	stagedHandleDirty      stagedHandleState = "dirty"
	stagedHandleCommitting stagedHandleState = "committing"
	stagedHandleCommitted  stagedHandleState = "committed"
	stagedHandleFailed     stagedHandleState = "failed"
)

type stagedHandleTargetKind string

const (
	stagedHandleTargetNone stagedHandleTargetKind = "none"
	stagedHandleTargetFile stagedHandleTargetKind = "file"
	stagedHandleTargetDir  stagedHandleTargetKind = "dir"
)

type stagedHandleBackingKind string

const (
	stagedHandleBackingStagedTemp       stagedHandleBackingKind = "staged-temp"
	stagedHandleBackingOwnerReadThrough stagedHandleBackingKind = "owner-read-through"
)

type stagedHandle struct {
	handleID      string
	op            proto.FsOperation
	path          string
	newPath       string
	tempPath      string
	targetKind    stagedHandleTargetKind
	backingKind   stagedHandleBackingKind
	openIntent    proto.FsOpenIntent
	metadata      proto.FileMetadata
	preconditions []proto.MutationPrecondition
	state         stagedHandleState
	resultID      string
	transactionID string
}

type closedHandleOutcome struct {
	resultID string
}

type stagedHandleResult struct {
	reply proto.FsReply
	err   error
}

type stagedHandleCommitTransaction struct {
	transactionID string
	done          chan struct{}
}

type conservativeObservation struct {
	path              string
	objectFingerprint string
	directoryView     string
}

func NewWorkspaceManager(projectsRoot string) *WorkspaceManager {
	manager := &WorkspaceManager{
		projectsRoot: projectsRoot,
		states:       make(map[string]*workspaceState),
		authorities:  make(map[string]workspaceAuthority),
	}
	manager.authorityCond = sync.NewCond(&manager.mu)
	return manager
}

func (m *WorkspaceManager) FsMutation(projectKey proto.ProjectKey, op proto.FsOperation, request proto.FsRequest, preconditions []proto.MutationPrecondition) (proto.FsReply, error) {
	return m.FsMutationContext(context.Background(), projectKey, op, request, preconditions)
}

func (m *WorkspaceManager) FsMutationContext(ctx context.Context, projectKey proto.ProjectKey, op proto.FsOperation, request proto.FsRequest, preconditions []proto.MutationPrecondition) (proto.FsReply, error) {
	layout, err := ResolveProjectLayout(m.projectsRoot, projectKey)
	if err != nil {
		return proto.FsReply{}, err
	}
	if err := validateWorkspacePath(request.Path); err != nil {
		return proto.FsReply{}, err
	}
	if request.NewPath != "" {
		if err := validateWorkspacePath(request.NewPath); err != nil {
			return proto.FsReply{}, err
		}
	}
	if requiresStableMutationAuthority(op, request) {
		if err := m.waitForMutationAuthority(ctx, projectKey); err != nil {
			return proto.FsReply{}, err
		}
	}
	if err := m.revalidateConservativePaths(ctx, projectKey, layout, conservativePathsForMutation(request, preconditions)); err != nil {
		return proto.FsReply{}, err
	}

	m.mu.Lock()
	state := m.stateLocked(projectKey)
	for _, condition := range preconditions {
		if err := state.checkPreconditionsLocked(condition.Path, condition); err != nil {
			m.mu.Unlock()
			reply := proto.FsReply{Conflict: true}
			return reply, err
		}
	}
	m.mu.Unlock()

	switch op {
	case proto.FsOpOpen:
		return m.stageHandleMutation(ctx, projectKey, layout, op, request, preconditions)
	case proto.FsOpCreate:
		return m.stageHandleMutation(ctx, projectKey, layout, op, request, preconditions)
	case proto.FsOpWrite:
		return m.stageHandleMutation(ctx, projectKey, layout, op, request, preconditions)
	case proto.FsOpMkdir:
		return m.stageDentryMutation(ctx, projectKey, layout, op, request, preconditions)
	case proto.FsOpRemove, proto.FsOpRmdir:
		return m.stageDentryMutation(ctx, projectKey, layout, op, request, preconditions)
	case proto.FsOpRename:
		return m.stageDentryMutation(ctx, projectKey, layout, op, request, preconditions)
	case proto.FsOpTruncate:
		return m.stageHandleMutation(ctx, projectKey, layout, op, request, preconditions)
	case proto.FsOpChmod:
		return m.stageHandleMutation(ctx, projectKey, layout, op, request, preconditions)
	case proto.FsOpUtimens:
		return m.stageHandleMutation(ctx, projectKey, layout, op, request, preconditions)
	case proto.FsOpFSync, proto.FsOpFlush:
		return m.commitHandle(ctx, projectKey, layout, request)
	case proto.FsOpRelease:
		return m.releaseHandle(ctx, projectKey, request)
	default:
		return proto.FsReply{}, proto.NewError(proto.ErrInvalidArgument, "unsupported FsMutation operation")
	}
}

func requiresStableMutationAuthority(op proto.FsOperation, request proto.FsRequest) bool {
	switch op {
	case proto.FsOpOpen:
		return effectiveOpenIntent(request) == proto.FsOpenIntentWritable
	case proto.FsOpCreate, proto.FsOpWrite, proto.FsOpMkdir, proto.FsOpRemove, proto.FsOpRmdir,
		proto.FsOpRename, proto.FsOpTruncate, proto.FsOpChmod, proto.FsOpUtimens, proto.FsOpFSync, proto.FsOpFlush:
		return true
	default:
		return false
	}
}

func (m *WorkspaceManager) withGenerations(projectKey proto.ProjectKey, path string, reply proto.FsReply) proto.FsReply {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.stateLocked(projectKey)
	reply.ObjectGeneration = state.committed.objectGen[path]
	reply.DirGeneration = state.committed.dirGen[parentPath(path)]
	return reply
}

func (m *WorkspaceManager) stateLocked(projectKey proto.ProjectKey) *workspaceState {
	key := projectKey.String()
	state, ok := m.states[key]
	if !ok {
		state = &workspaceState{
			committed: workspaceCommittedState{
				objectGen:      make(map[string]uint64),
				dirGen:         make(map[string]uint64),
				objectIdentity: make(map[string]string),
				manifest:       make(map[string]proto.ManifestEntry),
			},
			sync: workspaceSyncState{
				sessions: make(map[uint64]*syncSession),
			},
			handles: workspaceHandleState{
				open:         make(map[string]*stagedHandle),
				closed:       make(map[string]closedHandleOutcome),
				results:      make(map[string]stagedHandleResult),
				transactions: make(map[string]*stagedHandleCommitTransaction),
			},
			validation: workspaceValidationState{
				objectState:   make(map[string]string),
				directoryView: make(map[string]string),
			},
		}
		m.states[key] = state
	}
	return state
}

func (s *workspaceState) checkPreconditionsLocked(path string, condition proto.MutationPrecondition) error {
	if condition.Path != "" {
		path = condition.Path
	}
	if condition.MustNotExist {
		if _, ok := s.committed.objectIdentity[path]; ok {
			return proto.NewError(proto.ErrConflict, "path already exists")
		}
	}
	if condition.ObjectGeneration != 0 && s.committed.objectGen[path] != condition.ObjectGeneration {
		return proto.NewError(proto.ErrConflict, "object generation precondition failed")
	}
	if condition.DirGeneration != 0 && s.committed.dirGen[parentPath(path)] != condition.DirGeneration {
		return proto.NewError(proto.ErrConflict, "directory generation precondition failed")
	}
	if condition.ObjectIdentity != "" && s.committed.objectIdentity[path] != condition.ObjectIdentity {
		return proto.NewError(proto.ErrConflict, "object identity precondition failed")
	}
	return nil
}

func (s *workspaceState) bumpPathGenerationsLocked(path string) proto.FsReply {
	s.committed.objectGen[path]++
	s.committed.dirGen[parentPath(path)]++
	if s.committed.objectIdentity[path] == "" {
		s.committed.nextObjectID++
		s.committed.objectIdentity[path] = fmt.Sprintf("obj-%d", s.committed.nextObjectID)
	}
	return proto.FsReply{
		ObjectGeneration: s.committed.objectGen[path],
		DirGeneration:    s.committed.dirGen[parentPath(path)],
	}
}

func (s *workspaceState) bumpOwnerDataLocked(path string) {
	s.committed.objectGen[path]++
	s.ensureObjectIdentityLocked(path)
}

func (s *workspaceState) bumpOwnerDeleteLocked(path string) {
	s.committed.objectGen[path]++
	delete(s.committed.objectIdentity, path)
}

func (s *workspaceState) bumpOwnerRenameLocked(path string, newPath string) {
	if path == newPath {
		s.bumpOwnerDataLocked(path)
		return
	}
	movedIdentity := s.committed.objectIdentity[path]
	s.committed.objectGen[path]++
	delete(s.committed.objectIdentity, path)
	s.committed.objectGen[newPath]++
	if movedIdentity == "" {
		movedIdentity = s.nextObjectIdentityLocked()
	}
	s.committed.objectIdentity[newPath] = movedIdentity
}

func (s *workspaceState) bumpOwnerDentryLocked(path string) {
	s.committed.dirGen[path]++
	s.committed.objectGen[path]++
	if path != "" {
		s.ensureObjectIdentityLocked(path)
	}
}

func (s *workspaceState) bumpDeleteGenerationsLocked(path string) proto.FsReply {
	s.committed.objectGen[path]++
	s.committed.dirGen[parentPath(path)]++
	delete(s.committed.objectIdentity, path)
	return proto.FsReply{
		ObjectGeneration: s.committed.objectGen[path],
		DirGeneration:    s.committed.dirGen[parentPath(path)],
	}
}

func (s *workspaceState) bumpRenameGenerationsLocked(path string, newPath string) proto.FsReply {
	if path == newPath {
		return proto.FsReply{
			ObjectGeneration: s.committed.objectGen[path],
			DirGeneration:    s.committed.dirGen[parentPath(path)],
		}
	}
	movedIdentity := s.committed.objectIdentity[path]
	s.committed.objectGen[path]++
	s.committed.dirGen[parentPath(path)]++
	delete(s.committed.objectIdentity, path)
	s.committed.objectGen[newPath]++
	s.committed.dirGen[parentPath(newPath)]++
	if movedIdentity == "" {
		s.committed.nextObjectID++
		movedIdentity = fmt.Sprintf("obj-%d", s.committed.nextObjectID)
	}
	s.committed.objectIdentity[newPath] = movedIdentity
	return proto.FsReply{
		ObjectGeneration: s.committed.objectGen[newPath],
		DirGeneration:    s.committed.dirGen[parentPath(newPath)],
	}
}

func (s *workspaceState) syncManifestStateLocked(entries map[string]proto.ManifestEntry) {
	s.committed.manifest = cloneManifestMap(entries)
	s.committed.generation++
	keep := make(map[string]struct{}, len(entries))
	for path := range entries {
		keep[path] = struct{}{}
		if s.committed.objectIdentity[path] == "" {
			s.committed.nextObjectID++
			s.committed.objectIdentity[path] = fmt.Sprintf("obj-%d", s.committed.nextObjectID)
		}
		if _, ok := s.committed.objectGen[path]; !ok {
			s.committed.objectGen[path] = 0
		}
		parent := parentPath(path)
		if _, ok := s.committed.dirGen[parent]; !ok {
			s.committed.dirGen[parent] = 0
		}
	}
	for path := range s.committed.objectIdentity {
		if _, ok := keep[path]; !ok {
			delete(s.committed.objectIdentity, path)
		}
	}
	for path := range s.committed.objectGen {
		if _, ok := keep[path]; !ok {
			delete(s.committed.objectGen, path)
		}
	}
	s.clearConservativePathsLocked(trackedPathsForManifest(entries)...)
}

func (s *workspaceState) ensureObjectIdentityLocked(path string) {
	if path == "" {
		return
	}
	if _, ok := s.committed.objectIdentity[path]; ok {
		return
	}
	s.committed.objectIdentity[path] = s.nextObjectIdentityLocked()
}

func (s *workspaceState) nextObjectIdentityLocked() string {
	s.committed.nextObjectID++
	return fmt.Sprintf("obj-%d", s.committed.nextObjectID)
}

func (s *workspaceState) committedReplyLocked(path string) proto.FsReply {
	return proto.FsReply{
		ObjectGeneration: s.committed.objectGen[path],
		DirGeneration:    s.committed.dirGen[parentPath(path)],
	}
}

func (s *workspaceHandleState) recordResultLocked(reply proto.FsReply, err error) string {
	s.nextResultID++
	resultID := fmt.Sprintf("result-%04d", s.nextResultID)
	s.results[resultID] = stagedHandleResult{reply: reply, err: err}
	return resultID
}

func (s *workspaceHandleState) clearResultLocked(resultID string) {
	if resultID == "" {
		return
	}
	delete(s.results, resultID)
}

func (s *workspaceHandleState) openResultLocked(handle *stagedHandle) (stagedHandleResult, bool) {
	if handle == nil || handle.resultID == "" {
		return stagedHandleResult{}, false
	}
	result, ok := s.results[handle.resultID]
	return result, ok
}

func (s *workspaceHandleState) closedResultLocked(handleID string) (stagedHandleResult, bool) {
	outcome, ok := s.closed[handleID]
	if !ok || outcome.resultID == "" {
		return stagedHandleResult{}, false
	}
	result, ok := s.results[outcome.resultID]
	return result, ok
}

func (s *workspaceHandleState) setHandleResultLocked(handle *stagedHandle, state stagedHandleState, reply proto.FsReply, err error) stagedHandleResult {
	s.clearResultLocked(handle.resultID)
	handle.resultID = s.recordResultLocked(reply, err)
	handle.state = state
	handle.transactionID = ""
	return s.results[handle.resultID]
}

func (s *workspaceHandleState) clearHandleResultLocked(handle *stagedHandle) {
	if handle == nil {
		return
	}
	s.clearResultLocked(handle.resultID)
	handle.resultID = ""
}

func (s *workspaceHandleState) beginTransactionLocked(handle *stagedHandle) *stagedHandleCommitTransaction {
	s.nextTxnID++
	transactionID := fmt.Sprintf("txn-%04d", s.nextTxnID)
	transaction := &stagedHandleCommitTransaction{
		transactionID: transactionID,
		done:          make(chan struct{}),
	}
	s.transactions[transactionID] = transaction
	handle.transactionID = transactionID
	return transaction
}

func (s *workspaceHandleState) transactionLocked(handle *stagedHandle) (*stagedHandleCommitTransaction, bool) {
	if handle == nil || handle.transactionID == "" {
		return nil, false
	}
	transaction, ok := s.transactions[handle.transactionID]
	return transaction, ok
}

func (s *workspaceHandleState) finishTransactionLocked(handle *stagedHandle, state stagedHandleState, reply proto.FsReply, err error) stagedHandleResult {
	transaction, ok := s.transactions[handle.transactionID]
	if ok {
		delete(s.transactions, handle.transactionID)
	}
	result := s.setHandleResultLocked(handle, state, reply, err)
	if ok {
		close(transaction.done)
	}
	return result
}

func normalizeMetadata(metadata proto.FileMetadata) proto.FileMetadata {
	metadata.MTime = metadata.MTime.UTC().Round(time.Second)
	return metadata
}

func fileInfoMetadata(info fs.FileInfo) proto.FileMetadata {
	return normalizeMetadata(proto.FileMetadata{
		Mode:  uint32(info.Mode().Perm()),
		MTime: info.ModTime(),
		Size:  info.Size(),
	})
}

func applyFileMetadata(path string, metadata proto.FileMetadata) error {
	if err := os.Chmod(path, fs.FileMode(metadata.Mode)); err != nil {
		return err
	}
	return os.Chtimes(path, metadata.MTime, metadata.MTime)
}

func applyDirMetadata(path string, metadata proto.FileMetadata) error {
	return os.Chtimes(path, metadata.MTime, metadata.MTime)
}

func validateWorkspacePath(path string) error {
	if err := portablefs.ValidateRelativePath(path); err != nil {
		return proto.NewError(proto.ErrUnsupportedPath, err.Error())
	}
	return nil
}

func validateWorkspaceReadPath(op proto.FsOperation, path string) error {
	if isRootWorkspacePath(path) {
		switch op {
		case proto.FsOpLookup, proto.FsOpGetAttr, proto.FsOpReadDir:
			return nil
		default:
			return proto.NewError(proto.ErrUnsupportedPath, "root path only supports lookup/getattr/readdir")
		}
	}
	return validateWorkspacePath(path)
}

func isRootWorkspacePath(path string) bool {
	clean := filepath.ToSlash(strings.TrimSpace(path))
	return clean == "" || clean == "."
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func listWorkspaceEntries(root string) ([]string, error) {
	var result []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result = append(result, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(result)
	return result, nil
}

func mirrorFile(source string, target string, mode uint32) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(mode))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "close mirrored file "+target, out.Close())
		return err
	}
	return out.Close()
}

func copyFileContents(source string, target string, mode uint32) error {
	return mirrorFile(source, target, mode)
}

type fileSnapshot struct {
	exists   bool
	data     []byte
	metadata proto.FileMetadata
}

func captureFileSnapshot(path string) (fileSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errorsIsNotExist(err) {
			return fileSnapshot{}, nil
		}
		return fileSnapshot{}, err
	}
	if info.IsDir() {
		return fileSnapshot{}, fmt.Errorf("file snapshot requires a non-directory path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{
		exists:   true,
		data:     data,
		metadata: fileInfoMetadata(info),
	}, nil
}

func restoreFileSnapshot(path string, snapshot fileSnapshot) error {
	if !snapshot.exists {
		if err := os.Remove(path); err != nil && !errorsIsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tempPath := path + ".symterm-rollback"
	if err := os.WriteFile(tempPath, snapshot.data, fs.FileMode(snapshot.metadata.Mode)); err != nil {
		return err
	}
	if err := applyFileMetadata(tempPath, snapshot.metadata); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "remove rollback temp file "+tempPath, os.Remove(tempPath))
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "remove rollback temp file "+tempPath, os.Remove(tempPath))
		return err
	}
	return nil
}

func canonicalWorkspacePath(layout ProjectLayout, workspacePath string) string {
	return filepath.Join(layout.WorkspaceDir, filepath.FromSlash(workspacePath))
}

func publishedWorkspacePath(layout ProjectLayout, workspacePath string) string {
	return filepath.Join(layout.MountDir, filepath.FromSlash(workspacePath))
}

func syncPublishedWorkspace(layout ProjectLayout) error {
	return mirrorPath(layout.WorkspaceDir, layout.MountDir)
}

func replaceFileFromStaged(source string, target string, metadata proto.FileMetadata) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tempTarget := target + ".symterm-commit"
	if err := copyFileContents(source, tempTarget, metadata.Mode); err != nil {
		return err
	}
	if err := applyFileMetadata(tempTarget, metadata); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "remove staged commit temp file "+tempTarget, os.Remove(tempTarget))
		return err
	}
	if err := os.Rename(tempTarget, target); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "remove staged commit temp file "+tempTarget, os.Remove(tempTarget))
		return err
	}
	return nil
}

func removePathForOperation(path string, op proto.FsOperation) error {
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

func renamePathWithOverwrite(source string, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if existing, err := os.Stat(target); err == nil {
		if info.IsDir() != existing.IsDir() {
			return proto.NewError(proto.ErrConflict, "rename target kind changed")
		}
		if err := os.Remove(target); err != nil {
			return err
		}
	} else if !errorsIsNotExist(err) {
		return err
	}
	return os.Rename(source, target)
}

func syncRenamedMirrorPath(authoritativeTarget string, mirrorSource string, mirrorTarget string) error {
	if mirrorSource != mirrorTarget {
		if err := renamePathWithOverwrite(mirrorSource, mirrorTarget); err == nil {
			return nil
		} else if !errorsIsNotExist(err) {
			return err
		}
	}
	if err := mirrorPath(authoritativeTarget, mirrorTarget); err != nil {
		return err
	}
	if mirrorSource != mirrorTarget {
		if err := os.RemoveAll(mirrorSource); err != nil && !errorsIsNotExist(err) {
			return err
		}
	}
	return nil
}

func mirrorPath(source string, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		metadata := fileInfoMetadata(info)
		if err := mirrorFile(source, target, metadata.Mode); err != nil {
			return err
		}
		return applyFileMetadata(target, metadata)
	}
	if err := prepareMirrorDirectoryRoot(target); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		dest := target
		if rel != "." {
			dest = filepath.Join(target, rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		metadata := fileInfoMetadata(info)
		if entry.IsDir() {
			if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
				existing, statErr := os.Stat(dest)
				if statErr != nil || !existing.IsDir() {
					return err
				}
			}
			return applyDirMetadata(dest, metadata)
		}
		if err := mirrorFile(current, dest, metadata.Mode); err != nil {
			return err
		}
		return applyFileMetadata(dest, metadata)
	})
}

func prepareMirrorDirectoryRoot(target string) error {
	info, err := os.Lstat(target)
	if err != nil {
		if errorsIsNotExist(err) {
			return os.MkdirAll(target, 0o755)
		}
		return err
	}
	if !info.IsDir() {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		return os.MkdirAll(target, 0o755)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := values[:0]
	var previous string
	for idx, value := range values {
		if idx == 0 || value != previous {
			result = append(result, value)
			previous = value
		}
	}
	return result
}

func clonePreconditions(values []proto.MutationPrecondition) []proto.MutationPrecondition {
	if len(values) == 0 {
		return nil
	}
	result := make([]proto.MutationPrecondition, len(values))
	copy(result, values)
	return result
}

func parentPath(path string) string {
	path = filepath.ToSlash(path)
	dir := filepath.Dir(path)
	if dir == "." {
		return ""
	}
	return filepath.ToSlash(dir)
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}

func ensureDirectoryPresent(path string, mode fs.FileMode) error {
	mounted, err := isActiveMountpoint(filepath.Clean(path))
	if err == nil && mounted {
		return nil
	}
	if err != nil && !errorsIsNotExist(err) {
		return err
	}

	info, err := os.Lstat(path)
	if err == nil {
		if info.IsDir() {
			return nil
		}
		return fmt.Errorf("%s exists and is not a directory", path)
	}
	if !errorsIsNotExist(err) {
		return err
	}
	return os.MkdirAll(path, mode)
}

func wrapOwnerWriteError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var protoErr *proto.Error
	if errors.As(err, &protoErr) {
		switch protoErr.Code {
		case proto.ErrConflict, proto.ErrInvalidArgument, proto.ErrUnsupportedPath, proto.ErrUnknownFile, proto.ErrUnknownHandle:
			return err
		}
	}
	return proto.NewError(proto.ErrOwnerWriteFailed, err.Error())
}

func (m *WorkspaceManager) replaceFileFromStaged(source string, target string, metadata proto.FileMetadata) error {
	if m.hooks.replaceFileFromStaged != nil {
		return m.hooks.replaceFileFromStaged(source, target, metadata)
	}
	return replaceFileFromStaged(source, target, metadata)
}

func (m *WorkspaceManager) mirrorCommittedFile(source string, target string, mode uint32) error {
	if m.hooks.mirrorFile != nil {
		return m.hooks.mirrorFile(source, target, mode)
	}
	return mirrorFile(source, target, mode)
}

func (m *WorkspaceManager) beforeFileGeneration(path string) error {
	if m.hooks.beforeFileGeneration != nil {
		return m.hooks.beforeFileGeneration(path)
	}
	return nil
}
