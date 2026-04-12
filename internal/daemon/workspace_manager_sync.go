package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"symterm/internal/diagnostic"
	"symterm/internal/proto"
)

type syncSession struct {
	sessionID       string
	epoch           uint64
	attemptID       uint64
	rootFingerprint string
	stageRoot       string
	manifest        map[string]proto.ManifestEntry
	changedFiles    map[string]proto.ManifestEntry
	deleted         map[string]struct{}
	uploads         map[string]*uploadSession
	bundles         map[string]*syncBundleSession
	remoteHashMemo  map[string]remoteHashMemoEntry
	nextUploadID    uint64
	nextBundleID    uint64
	manifestBatches uint64
	deleteBatches   uint64
	uploadBundles   uint64
}

type syncBundleSession struct {
	bundleID string
}

type remoteHashMemoEntry struct {
	size       int64
	mode       uint32
	mtimeNanos int64
	hash       string
}

type uploadSession struct {
	fileID          string
	path            string
	expectedSize    int64
	metadata        proto.FileMetadata
	statFingerprint string
	tempPath        string
	file            *os.File
	hasher          hash.Hash
	written         int64
}

func (m *WorkspaceManager) BeginSync(projectKey proto.ProjectKey, request proto.BeginSyncRequest) error {
	_, err := m.startSyncSession(projectKey, proto.StartSyncSessionRequest{
		SyncEpoch:       request.SyncEpoch,
		AttemptID:       request.AttemptID,
		RootFingerprint: request.RootFingerprint,
	})
	return err
}

func (m *WorkspaceManager) StartSyncSession(projectKey proto.ProjectKey, request proto.StartSyncSessionRequest) (proto.StartSyncSessionResponse, error) {
	return m.startSyncSession(projectKey, request)
}

func (m *WorkspaceManager) startSyncSession(projectKey proto.ProjectKey, request proto.StartSyncSessionRequest) (proto.StartSyncSessionResponse, error) {
	layout, err := ResolveProjectLayout(m.projectsRoot, projectKey)
	if err != nil {
		return proto.StartSyncSessionResponse{}, fmt.Errorf("begin sync resolve layout: %w", err)
	}
	if err := layout.EnsureNonMountDirectories(); err != nil {
		return proto.StartSyncSessionResponse{}, fmt.Errorf("begin sync ensure non-mount directories: %w", err)
	}
	if err := ensureDirectoryPresent(layout.MountDir, 0o755); err != nil {
		return proto.StartSyncSessionResponse{}, fmt.Errorf("begin sync ensure mount directory: %w", err)
	}
	stageRoot := filepath.Join(layout.RuntimeDir, fmt.Sprintf("sync-%d-stage", request.SyncEpoch))
	if err := os.RemoveAll(stageRoot); err != nil {
		return proto.StartSyncSessionResponse{}, fmt.Errorf("begin sync remove stage root: %w", err)
	}
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return proto.StartSyncSessionResponse{}, fmt.Errorf("begin sync create stage root: %w", err)
	}

	m.mu.Lock()
	state := m.stateLocked(projectKey)
	if err := m.ensureCommittedManifestLocked(layout, state); err != nil {
		m.mu.Unlock()
		return proto.StartSyncSessionResponse{}, err
	}
	staleSessions := make([]*syncSession, 0, len(state.sync.sessions))
	for _, session := range state.sync.sessions {
		staleSessions = append(staleSessions, session)
	}
	sessionID := fmt.Sprintf("sync-%d-%d", request.SyncEpoch, time.Now().UnixNano())
	state.sync.activeEpoch = request.SyncEpoch
	state.sync.sessions = map[uint64]*syncSession{
		request.SyncEpoch: {
			sessionID:       sessionID,
			epoch:           request.SyncEpoch,
			attemptID:       request.AttemptID,
			rootFingerprint: request.RootFingerprint,
			stageRoot:       stageRoot,
			manifest:        make(map[string]proto.ManifestEntry),
			changedFiles:    make(map[string]proto.ManifestEntry),
			deleted:         make(map[string]struct{}),
			uploads:         make(map[string]*uploadSession),
			bundles:         make(map[string]*syncBundleSession),
			remoteHashMemo:  make(map[string]remoteHashMemoEntry),
		},
	}
	remoteGeneration := state.committed.generation
	remoteEntries := uint64(len(state.committed.manifest))
	m.mu.Unlock()

	for _, session := range staleSessions {
		if session.stageRoot == stageRoot {
			continue
		}
		if err := cleanupSyncSession(session); err != nil {
			return proto.StartSyncSessionResponse{}, fmt.Errorf("begin sync cleanup stale session: %w", err)
		}
	}
	return proto.StartSyncSessionResponse{
		SessionID:             sessionID,
		SyncEpoch:             request.SyncEpoch,
		ProtocolVersion:       2,
		RemoteGeneration:      remoteGeneration,
		RemoteManifestEntries: remoteEntries,
	}, nil
}

func (m *WorkspaceManager) ScanManifest(projectKey proto.ProjectKey, request proto.ScanManifestRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		return err
	}
	for _, entry := range request.Entries {
		if err := validateWorkspacePath(entry.Path); err != nil {
			return err
		}
		session.manifest[entry.Path] = cloneManifestEntry(entry)
	}
	return nil
}

func (m *WorkspaceManager) SyncManifestBatch(projectKey proto.ProjectKey, request proto.SyncManifestBatchRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		return err
	}
	if session.sessionID != strings.TrimSpace(request.SessionID) {
		return proto.NewError(proto.ErrSyncEpochMismatch, "sync manifest batch does not match the active sync session")
	}
	session.manifestBatches++
	for _, entry := range request.Entries {
		if err := validateWorkspacePath(entry.Path); err != nil {
			return err
		}
		session.manifest[entry.Path] = cloneManifestEntry(entry)
	}
	return nil
}

func (m *WorkspaceManager) PlanManifestHashes(projectKey proto.ProjectKey) (proto.PlanManifestHashesResponse, error) {
	layout, err := ResolveProjectLayout(m.projectsRoot, projectKey)
	if err != nil {
		return proto.PlanManifestHashesResponse{}, err
	}

	started := time.Now()
	m.mu.Lock()
	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		m.mu.Unlock()
		return proto.PlanManifestHashesResponse{}, err
	}
	if err := m.ensureCommittedManifestLocked(layout, m.stateLocked(projectKey)); err != nil {
		m.mu.Unlock()
		return proto.PlanManifestHashesResponse{}, err
	}
	entries := make([]proto.ManifestEntry, 0, len(session.manifest))
	for _, entry := range session.manifest {
		entries = append(entries, cloneManifestEntry(entry))
	}
	changedFiles := cloneManifestMap(session.changedFiles)
	deleted := cloneWorkspaceDeleteSet(session.deleted)
	committedEntries := cloneManifestMap(m.stateLocked(projectKey).committed.manifest)
	m.mu.Unlock()

	var paths []string
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		remoteEntry, _, exists, err := remoteManifestEntryForPath(layout, session, committedEntries, entry.Path, changedFiles, deleted)
		if err != nil {
			return proto.PlanManifestHashesResponse{}, err
		}
		if exists && !remoteEntry.IsDir && normalizeMetadata(remoteEntry.Metadata) != normalizeMetadata(entry.Metadata) {
			if refreshed, _, refreshedExists, refreshErr := refreshRemoteManifestEntry(layout, entry.Path); refreshErr == nil && refreshedExists {
				remoteEntry = refreshed
			}
		}
		if !exists || remoteEntry.IsDir {
			continue
		}
		if normalizeMetadata(remoteEntry.Metadata) == normalizeMetadata(entry.Metadata) && strings.TrimSpace(entry.ContentHash) == "" {
			paths = append(paths, entry.Path)
		}
	}
	sort.Strings(paths)
	traceSyncStage(projectKey, "plan_manifest_hashes", started, "paths", len(paths))
	return proto.PlanManifestHashesResponse{Paths: uniqueStrings(paths)}, nil
}

func (m *WorkspaceManager) PlanSyncActions(projectKey proto.ProjectKey) (proto.PlanSyncActionsResponse, error) {
	layout, err := ResolveProjectLayout(m.projectsRoot, projectKey)
	if err != nil {
		return proto.PlanSyncActionsResponse{}, err
	}

	started := time.Now()
	m.mu.Lock()
	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		m.mu.Unlock()
		return proto.PlanSyncActionsResponse{}, err
	}
	if err := m.ensureCommittedManifestLocked(layout, m.stateLocked(projectKey)); err != nil {
		m.mu.Unlock()
		return proto.PlanSyncActionsResponse{}, err
	}
	entries := make([]proto.ManifestEntry, 0, len(session.manifest))
	for _, entry := range session.manifest {
		entries = append(entries, cloneManifestEntry(entry))
	}
	changedFiles := cloneManifestMap(session.changedFiles)
	deleted := cloneWorkspaceDeleteSet(session.deleted)
	committedEntries := cloneManifestMap(m.stateLocked(projectKey).committed.manifest)
	m.mu.Unlock()

	uploadPaths := make([]string, 0)
	manifestPaths := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		manifestPaths[entry.Path] = struct{}{}
		if entry.IsDir {
			continue
		}
		remoteEntry, remotePath, exists, err := remoteManifestEntryForPath(layout, session, committedEntries, entry.Path, changedFiles, deleted)
		if err != nil {
			return proto.PlanSyncActionsResponse{}, err
		}
		if exists && !remoteEntry.IsDir && normalizeMetadata(remoteEntry.Metadata) != normalizeMetadata(entry.Metadata) {
			if refreshed, refreshedPath, refreshedExists, refreshErr := refreshRemoteManifestEntry(layout, entry.Path); refreshErr == nil {
				exists = refreshedExists
				remoteEntry = refreshed
				remotePath = refreshedPath
			}
		}
		switch {
		case !exists:
			uploadPaths = append(uploadPaths, entry.Path)
		case remoteEntry.IsDir:
			uploadPaths = append(uploadPaths, entry.Path)
		case normalizeMetadata(remoteEntry.Metadata) != normalizeMetadata(entry.Metadata):
			uploadPaths = append(uploadPaths, entry.Path)
		case strings.TrimSpace(entry.ContentHash) == "":
			return proto.PlanSyncActionsResponse{}, proto.NewError(proto.ErrInvalidArgument, "manifest file is missing a required content hash")
		default:
			hashValue, hashErr := m.remoteContentHash(session, committedEntries, entry.Path, remotePath)
			if hashErr != nil {
				return proto.PlanSyncActionsResponse{}, hashErr
			}
			if hashValue != entry.ContentHash {
				uploadPaths = append(uploadPaths, entry.Path)
			}
		}
	}

	deletePaths := make([]string, 0)
	for path := range committedEntries {
		if _, ok := manifestPaths[path]; ok {
			continue
		}
		if isPathDeletedOrNested(path, deleted) {
			continue
		}
		deletePaths = append(deletePaths, path)
	}

	sort.Strings(uploadPaths)
	sort.Strings(deletePaths)
	traceSyncStage(projectKey, "plan_sync_actions", started, "uploads", len(uploadPaths), "deletes", len(deletePaths))
	return proto.PlanSyncActionsResponse{
		UploadPaths: uniqueStrings(uploadPaths),
		DeletePaths: uniqueStrings(deletePaths),
	}, nil
}

func (m *WorkspaceManager) PlanSyncV2(projectKey proto.ProjectKey, request proto.PlanSyncV2Request) (proto.PlanSyncV2Response, error) {
	layout, err := ResolveProjectLayout(m.projectsRoot, projectKey)
	if err != nil {
		return proto.PlanSyncV2Response{}, err
	}

	started := time.Now()
	m.mu.Lock()
	state := m.stateLocked(projectKey)
	if err := m.ensureCommittedManifestLocked(layout, state); err != nil {
		m.mu.Unlock()
		return proto.PlanSyncV2Response{}, err
	}
	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		m.mu.Unlock()
		return proto.PlanSyncV2Response{}, err
	}
	if session.sessionID != strings.TrimSpace(request.SessionID) {
		m.mu.Unlock()
		return proto.PlanSyncV2Response{}, proto.NewError(proto.ErrSyncEpochMismatch, "plan sync v2 does not match the active sync session")
	}
	entries := make([]proto.ManifestEntry, 0, len(session.manifest))
	for _, entry := range session.manifest {
		entries = append(entries, cloneManifestEntry(entry))
	}
	changedFiles := cloneManifestMap(session.changedFiles)
	deleted := cloneWorkspaceDeleteSet(session.deleted)
	committedEntries := cloneManifestMap(state.committed.manifest)
	m.mu.Unlock()

	hashPaths := make([]string, 0)
	uploadPaths := make([]string, 0)
	manifestPaths := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		manifestPaths[entry.Path] = struct{}{}
		if entry.IsDir {
			continue
		}
		remoteEntry, remotePath, exists, err := remoteManifestEntryForPath(layout, session, committedEntries, entry.Path, changedFiles, deleted)
		if err != nil {
			return proto.PlanSyncV2Response{}, err
		}
		if exists && !remoteEntry.IsDir && normalizeMetadata(remoteEntry.Metadata) != normalizeMetadata(entry.Metadata) {
			if refreshed, refreshedPath, refreshedExists, refreshErr := refreshRemoteManifestEntry(layout, entry.Path); refreshErr == nil {
				exists = refreshedExists
				remoteEntry = refreshed
				remotePath = refreshedPath
			}
		}
		switch {
		case !exists:
			uploadPaths = append(uploadPaths, entry.Path)
		case remoteEntry.IsDir:
			uploadPaths = append(uploadPaths, entry.Path)
		case normalizeMetadata(remoteEntry.Metadata) != normalizeMetadata(entry.Metadata):
			uploadPaths = append(uploadPaths, entry.Path)
		case strings.TrimSpace(entry.ContentHash) == "":
			hashPaths = append(hashPaths, entry.Path)
		default:
			hashValue, hashErr := m.remoteContentHash(session, committedEntries, entry.Path, remotePath)
			if hashErr != nil {
				return proto.PlanSyncV2Response{}, hashErr
			}
			if hashValue != entry.ContentHash {
				uploadPaths = append(uploadPaths, entry.Path)
			}
		}
	}
	if len(hashPaths) > 0 {
		sort.Strings(hashPaths)
		traceSyncStage(projectKey, "plan_sync_v2_hashes", started, "hash_paths", len(hashPaths), "manifest_batches", session.manifestBatches)
		return proto.PlanSyncV2Response{HashPaths: uniqueStrings(hashPaths)}, nil
	}

	deletePaths := make([]string, 0)
	for path := range committedEntries {
		if _, ok := manifestPaths[path]; ok {
			continue
		}
		if isPathDeletedOrNested(path, deleted) {
			continue
		}
		deletePaths = append(deletePaths, path)
	}

	sort.Strings(uploadPaths)
	sort.Strings(deletePaths)
	traceSyncStage(projectKey, "plan_sync_v2", started, "uploads", len(uploadPaths), "deletes", len(deletePaths), "manifest_batches", session.manifestBatches)
	return proto.PlanSyncV2Response{
		HashPaths:   nil,
		UploadPaths: uniqueStrings(uploadPaths),
		DeletePaths: uniqueStrings(deletePaths),
	}, nil
}

func (m *WorkspaceManager) BeginFile(projectKey proto.ProjectKey, request proto.BeginFileRequest) (proto.BeginFileResponse, error) {
	layout, err := ResolveProjectLayout(m.projectsRoot, projectKey)
	if err != nil {
		return proto.BeginFileResponse{}, err
	}
	if err := validateWorkspacePath(request.Path); err != nil {
		return proto.BeginFileResponse{}, err
	}
	if err := os.MkdirAll(layout.RuntimeDir, 0o755); err != nil {
		return proto.BeginFileResponse{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		return proto.BeginFileResponse{}, err
	}
	if session.epoch != request.SyncEpoch {
		return proto.BeginFileResponse{}, proto.NewError(proto.ErrSyncEpochMismatch, "file upload does not match the active sync epoch")
	}
	session.nextUploadID++
	fileID := fmt.Sprintf("file-%04d", session.nextUploadID)
	tempPath := filepath.Join(layout.RuntimeDir, fmt.Sprintf("%s-%d.tmp", fileID, time.Now().UnixNano()))
	file, err := os.Create(tempPath)
	if err != nil {
		return proto.BeginFileResponse{}, err
	}
	session.uploads[fileID] = &uploadSession{
		fileID:          fileID,
		path:            request.Path,
		expectedSize:    request.ExpectedSize,
		metadata:        normalizeMetadata(request.Metadata),
		statFingerprint: request.StatFingerprint,
		tempPath:        tempPath,
		file:            file,
		hasher:          sha256.New(),
	}
	return proto.BeginFileResponse{FileID: fileID}, nil
}

func (m *WorkspaceManager) ApplyChunk(projectKey proto.ProjectKey, request proto.ApplyChunkRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	upload, err := m.uploadLocked(projectKey, request.FileID)
	if err != nil {
		return err
	}
	if request.Offset != upload.written {
		return proto.NewError(proto.ErrFileCommitFailed, "chunk offset is not contiguous")
	}
	if _, err := upload.file.Write(request.Data); err != nil {
		return err
	}
	if _, err := upload.hasher.Write(request.Data); err != nil {
		return err
	}
	upload.written += int64(len(request.Data))
	return nil
}

func (m *WorkspaceManager) CommitFile(projectKey proto.ProjectKey, request proto.CommitFileRequest) error {
	m.mu.Lock()
	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	upload, ok := session.uploads[request.FileID]
	if !ok {
		m.mu.Unlock()
		return proto.NewError(proto.ErrUnknownFile, "file upload handle does not exist")
	}
	delete(session.uploads, request.FileID)
	manifestEntry, ok := session.manifest[upload.path]
	if !ok {
		manifestEntry = proto.ManifestEntry{
			Path:            upload.path,
			Metadata:        upload.metadata,
			StatFingerprint: upload.statFingerprint,
		}
	}
	m.mu.Unlock()

	defer cleanupUpload(upload)
	if upload.written != request.FinalSize || upload.written != upload.expectedSize {
		return proto.NewError(proto.ErrFileCommitFailed, "uploaded file size does not match the expected size")
	}
	if err := upload.file.Close(); err != nil {
		return err
	}
	upload.file = nil
	if hex.EncodeToString(upload.hasher.Sum(nil)) != request.FinalHash {
		return proto.NewError(proto.ErrFileCommitFailed, "uploaded file hash does not match the committed hash")
	}

	target := syncWorkspacePath(session, upload.path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return wrapOwnerWriteError(err)
	}
	if err := os.Rename(upload.tempPath, target); err != nil {
		return wrapOwnerWriteError(err)
	}
	upload.tempPath = ""
	if err := applyFileMetadata(target, normalizeMetadata(proto.FileMetadata{
		Mode:  request.Mode,
		MTime: request.MTime,
		Size:  request.FinalSize,
	})); err != nil {
		return wrapOwnerWriteError(err)
	}

	m.mu.Lock()
	session, err = m.activeSyncLocked(projectKey)
	if err == nil {
		manifestEntry.Metadata = normalizeMetadata(proto.FileMetadata{
			Mode:  request.Mode,
			MTime: request.MTime,
			Size:  request.FinalSize,
		})
		manifestEntry.ContentHash = request.FinalHash
		manifestEntry.StatFingerprint = request.StatFingerprint
		session.changedFiles[upload.path] = cloneManifestEntry(manifestEntry)
		delete(session.deleted, upload.path)
	}
	m.mu.Unlock()
	return err
}

func (m *WorkspaceManager) AbortFile(projectKey proto.ProjectKey, request proto.AbortFileRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	upload, err := m.uploadLocked(projectKey, request.FileID)
	if err != nil {
		return err
	}
	state := m.stateLocked(projectKey)
	delete(state.sync.sessions[state.sync.activeEpoch].uploads, request.FileID)
	return cleanupUpload(upload)
}

func (m *WorkspaceManager) DeletePath(projectKey proto.ProjectKey, request proto.DeletePathRequest) error {
	if err := validateWorkspacePath(request.Path); err != nil {
		return err
	}

	m.mu.Lock()
	state := m.stateLocked(projectKey)
	if state.sync.activeEpoch != request.SyncEpoch {
		m.mu.Unlock()
		return proto.NewError(proto.ErrSyncEpochMismatch, "delete path does not match the active sync epoch")
	}
	if err := state.checkPreconditionsLocked(request.Path, request.Precondition); err != nil {
		m.mu.Unlock()
		return err
	}
	session, ok := state.sync.sessions[request.SyncEpoch]
	if ok {
		session.deleted[request.Path] = struct{}{}
		for path := range session.changedFiles {
			if isWorkspacePathEqualOrNested(path, request.Path) {
				delete(session.changedFiles, path)
			}
		}
	}
	m.mu.Unlock()
	if !ok {
		return proto.NewError(proto.ErrSyncEpochMismatch, "delete path does not match the active sync epoch")
	}

	target := syncWorkspacePath(session, request.Path)
	if err := os.RemoveAll(target); err != nil && !errorsIsNotExist(err) {
		return err
	}
	return nil
}

func (m *WorkspaceManager) DeletePathsBatch(projectKey proto.ProjectKey, request proto.DeletePathsBatchRequest) error {
	m.mu.Lock()
	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if session.sessionID != strings.TrimSpace(request.SessionID) {
		m.mu.Unlock()
		return proto.NewError(proto.ErrSyncEpochMismatch, "delete batch does not match the active sync session")
	}
	session.deleteBatches++
	m.mu.Unlock()

	for _, path := range request.Paths {
		if err := m.DeletePath(projectKey, proto.DeletePathRequest{
			SyncEpoch: request.SyncEpoch,
			Path:      path,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (m *WorkspaceManager) UploadBundleBegin(projectKey proto.ProjectKey, request proto.UploadBundleBeginRequest) (proto.UploadBundleBeginResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		return proto.UploadBundleBeginResponse{}, err
	}
	if session.epoch != request.SyncEpoch {
		return proto.UploadBundleBeginResponse{}, proto.NewError(proto.ErrSyncEpochMismatch, "upload bundle does not match the active sync epoch")
	}
	if session.sessionID != strings.TrimSpace(request.SessionID) {
		return proto.UploadBundleBeginResponse{}, proto.NewError(proto.ErrSyncEpochMismatch, "upload bundle does not match the active sync session")
	}
	session.nextBundleID++
	bundleID := fmt.Sprintf("bundle-%04d", session.nextBundleID)
	session.bundles[bundleID] = &syncBundleSession{bundleID: bundleID}
	return proto.UploadBundleBeginResponse{BundleID: bundleID}, nil
}

func (m *WorkspaceManager) UploadBundleCommit(projectKey proto.ProjectKey, request proto.UploadBundleCommitRequest) error {
	m.mu.Lock()
	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if session.sessionID != strings.TrimSpace(request.SessionID) {
		m.mu.Unlock()
		return proto.NewError(proto.ErrSyncEpochMismatch, "upload bundle does not match the active sync session")
	}
	if _, ok := session.bundles[request.BundleID]; !ok {
		m.mu.Unlock()
		return proto.NewError(proto.ErrUnknownFile, "upload bundle handle does not exist")
	}
	delete(session.bundles, request.BundleID)
	session.uploadBundles++
	m.mu.Unlock()

	for _, file := range request.Files {
		if err := validateWorkspacePath(file.Path); err != nil {
			return err
		}
		if hash := syncSHA256Hex(file.Data); hash != file.ContentHash {
			return proto.NewError(proto.ErrFileCommitFailed, "uploaded bundle file hash does not match the committed hash")
		}
		if int64(len(file.Data)) != file.Metadata.Size {
			return proto.NewError(proto.ErrFileCommitFailed, "uploaded bundle file size does not match the expected size")
		}
		target := syncWorkspacePath(session, file.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return wrapOwnerWriteError(err)
		}
		if err := os.WriteFile(target, file.Data, fs.FileMode(file.Metadata.Mode)); err != nil {
			return wrapOwnerWriteError(err)
		}
		if err := applyFileMetadata(target, normalizeMetadata(file.Metadata)); err != nil {
			return wrapOwnerWriteError(err)
		}
		m.mu.Lock()
		current, currentErr := m.activeSyncLocked(projectKey)
		if currentErr == nil {
			entry, ok := current.manifest[file.Path]
			if !ok {
				entry = proto.ManifestEntry{Path: file.Path}
			}
			entry.Metadata = normalizeMetadata(file.Metadata)
			entry.ContentHash = file.ContentHash
			entry.StatFingerprint = file.StatFingerprint
			current.changedFiles[file.Path] = cloneManifestEntry(entry)
			delete(current.deleted, file.Path)
		}
		m.mu.Unlock()
		if currentErr != nil {
			return currentErr
		}
	}
	return nil
}

func (m *WorkspaceManager) FinalizeSync(projectKey proto.ProjectKey, request proto.FinalizeSyncRequest) error {
	layout, err := ResolveProjectLayout(m.projectsRoot, projectKey)
	if err != nil {
		return err
	}

	started := time.Now()
	m.mu.Lock()
	state := m.stateLocked(projectKey)
	session, ok := state.sync.sessions[request.SyncEpoch]
	if !ok {
		m.mu.Unlock()
		return proto.NewError(proto.ErrSyncEpochMismatch, "finalize sync does not match the active epoch")
	}
	if state.sync.activeEpoch != request.SyncEpoch {
		m.mu.Unlock()
		return proto.NewError(proto.ErrSyncEpochMismatch, "finalize sync does not match the active epoch")
	}
	if session.attemptID != request.AttemptID {
		m.mu.Unlock()
		return proto.NewError(proto.ErrSyncEpochMismatch, "finalize sync does not match the active attempt")
	}
	if !request.GuardStable {
		m.mu.Unlock()
		return proto.NewError(proto.ErrSyncRescanMismatch, "workspace changed during initial sync")
	}
	if len(session.uploads) != 0 || len(session.bundles) != 0 {
		m.mu.Unlock()
		return proto.NewError(proto.ErrFileCommitFailed, "cannot finalize sync with unfinished uploads")
	}
	entries := cloneManifestMap(session.manifest)
	changedFiles := cloneManifestMap(session.changedFiles)
	deleted := cloneWorkspaceDeleteSet(session.deleted)
	m.mu.Unlock()
	if err := mergeStagedFilesIntoChangedSet(session.stageRoot, entries, changedFiles); err != nil {
		return wrapOwnerWriteError(err)
	}

	if err := m.publishSyncWorkspaceIncremental(layout, session, entries, changedFiles, deleted); err != nil {
		return wrapOwnerWriteError(err)
	}

	m.mu.Lock()
	state = m.stateLocked(projectKey)
	delete(state.sync.sessions, request.SyncEpoch)
	if state.sync.activeEpoch == request.SyncEpoch {
		state.sync.activeEpoch = 0
	}
	state.syncManifestStateLocked(entries)
	m.mu.Unlock()
	if err := cleanupSyncSession(session); err != nil {
		return err
	}
	diagnostic.Error(diagnostic.Default(), "seed conservative paths for sync "+projectKey.String(), m.seedConservativePaths(context.Background(), projectKey, layout, trackedPathsForManifest(entries)))
	traceSyncStage(projectKey, "finalize_sync", started, "changed_files", len(changedFiles), "deleted_paths", len(deleted), "manifest_batches", session.manifestBatches, "delete_batches", session.deleteBatches, "upload_bundles", session.uploadBundles)
	return nil
}

func (m *WorkspaceManager) FinalizeSyncV2(projectKey proto.ProjectKey, request proto.FinalizeSyncV2Request) error {
	m.mu.Lock()
	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if session.sessionID != strings.TrimSpace(request.SessionID) {
		m.mu.Unlock()
		return proto.NewError(proto.ErrSyncEpochMismatch, "finalize sync v2 does not match the active sync session")
	}
	m.mu.Unlock()
	return m.FinalizeSync(projectKey, proto.FinalizeSyncRequest{
		SyncEpoch:   request.SyncEpoch,
		AttemptID:   request.AttemptID,
		GuardStable: request.GuardStable,
	})
}

func (m *WorkspaceManager) activeSyncLocked(projectKey proto.ProjectKey) (*syncSession, error) {
	state := m.stateLocked(projectKey)
	if state.sync.activeEpoch == 0 {
		return nil, proto.NewError(proto.ErrProjectNotReady, "there is no active sync session")
	}
	session, ok := state.sync.sessions[state.sync.activeEpoch]
	if !ok {
		return nil, proto.NewError(proto.ErrProjectNotReady, "there is no active sync session")
	}
	return session, nil
}

func (m *WorkspaceManager) uploadLocked(projectKey proto.ProjectKey, fileID string) (*uploadSession, error) {
	session, err := m.activeSyncLocked(projectKey)
	if err != nil {
		return nil, err
	}
	upload, ok := session.uploads[fileID]
	if !ok {
		return nil, proto.NewError(proto.ErrUnknownFile, "file upload handle does not exist")
	}
	return upload, nil
}

func cloneManifestEntry(entry proto.ManifestEntry) proto.ManifestEntry {
	entry.Metadata = normalizeMetadata(entry.Metadata)
	return entry
}

func cloneManifestMap(values map[string]proto.ManifestEntry) map[string]proto.ManifestEntry {
	cloned := make(map[string]proto.ManifestEntry, len(values))
	for path, entry := range values {
		cloned[path] = cloneManifestEntry(entry)
	}
	return cloned
}

func cloneWorkspaceDeleteSet(values map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(values))
	for path := range values {
		cloned[path] = struct{}{}
	}
	return cloned
}

func cleanupUpload(upload *uploadSession) error {
	if upload.file != nil {
		if err := upload.file.Close(); err != nil && err != os.ErrClosed {
			return err
		}
	}
	if upload.tempPath != "" {
		if err := os.Remove(upload.tempPath); err != nil && !errorsIsNotExist(err) {
			return err
		}
	}
	return nil
}

func cleanupSyncSession(session *syncSession) error {
	if session == nil {
		return nil
	}
	for _, upload := range session.uploads {
		if err := cleanupUpload(upload); err != nil {
			return err
		}
	}
	if session.stageRoot != "" {
		if err := os.RemoveAll(session.stageRoot); err != nil {
			return err
		}
	}
	return nil
}

func sortedManifestEntries(entries map[string]proto.ManifestEntry) []proto.ManifestEntry {
	result := make([]proto.ManifestEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Path < result[right].Path
	})
	return result
}

func syncWorkspacePath(session *syncSession, workspacePath string) string {
	return filepath.Join(session.stageRoot, filepath.FromSlash(workspacePath))
}

func manifestEntriesFromRoot(root string) (map[string]proto.ManifestEntry, error) {
	result := make(map[string]proto.ManifestEntry)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result[filepath.ToSlash(rel)] = proto.ManifestEntry{
			Path:     filepath.ToSlash(rel),
			IsDir:    entry.IsDir(),
			Metadata: fileInfoMetadata(info),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (m *WorkspaceManager) publishSyncWorkspaceIncremental(
	layout ProjectLayout,
	session *syncSession,
	entries map[string]proto.ManifestEntry,
	changedFiles map[string]proto.ManifestEntry,
	deleted map[string]struct{},
) error {
	syncMount, err := publishedMountRequiresMirror(layout.MountDir)
	if err != nil {
		return err
	}
	if len(changedFiles) == 0 && len(deleted) == 0 && syncManifestMatchesWorkspace(entries, layout.WorkspaceDir) {
		return nil
	}
	for _, path := range sortedDeletePathsDeepestFirst(deleted) {
		workspaceTarget := filepath.Join(layout.WorkspaceDir, filepath.FromSlash(path))
		if err := os.RemoveAll(workspaceTarget); err != nil && !errorsIsNotExist(err) {
			return err
		}
		if syncMount {
			mountTarget := filepath.Join(layout.MountDir, filepath.FromSlash(path))
			if err := os.RemoveAll(mountTarget); err != nil && !errorsIsNotExist(err) {
				return err
			}
		}
	}
	for _, entry := range sortedManifestEntries(entries) {
		if !entry.IsDir {
			continue
		}
		workspaceTarget := filepath.Join(layout.WorkspaceDir, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(workspaceTarget, 0o755); err != nil {
			return err
		}
		if err := applyDirMetadata(workspaceTarget, normalizeMetadata(entry.Metadata)); err != nil {
			return err
		}
		if syncMount {
			mountTarget := filepath.Join(layout.MountDir, filepath.FromSlash(entry.Path))
			if err := os.MkdirAll(mountTarget, 0o755); err != nil {
				return err
			}
			if err := applyDirMetadata(mountTarget, normalizeMetadata(entry.Metadata)); err != nil {
				return err
			}
		}
	}
	for _, entry := range sortedManifestEntries(changedFiles) {
		stageSource := filepath.Join(session.stageRoot, filepath.FromSlash(entry.Path))
		workspaceTarget := filepath.Join(layout.WorkspaceDir, filepath.FromSlash(entry.Path))
		if err := m.replaceFileFromStaged(stageSource, workspaceTarget, normalizeMetadata(entry.Metadata)); err != nil {
			return err
		}
		if syncMount {
			mountTarget := filepath.Join(layout.MountDir, filepath.FromSlash(entry.Path))
			if err := m.replaceFileFromStaged(stageSource, mountTarget, normalizeMetadata(entry.Metadata)); err != nil {
				return err
			}
		}
	}
	return cleanupWorkspaceDirectories(layout, entries, syncMount)
}

func remoteManifestEntryForPath(
	layout ProjectLayout,
	session *syncSession,
	committedEntries map[string]proto.ManifestEntry,
	path string,
	changedFiles map[string]proto.ManifestEntry,
	deleted map[string]struct{},
) (proto.ManifestEntry, string, bool, error) {
	if isPathDeletedOrNested(path, deleted) {
		return proto.ManifestEntry{}, "", false, nil
	}
	if changedEntry, ok := changedFiles[path]; ok {
		return cloneManifestEntry(changedEntry), filepath.Join(session.stageRoot, filepath.FromSlash(path)), true, nil
	}
	if committedEntry, ok := committedEntries[path]; ok {
		return cloneManifestEntry(committedEntry), filepath.Join(layout.WorkspaceDir, filepath.FromSlash(path)), true, nil
	}
	return proto.ManifestEntry{}, "", false, nil
}

func refreshRemoteManifestEntry(layout ProjectLayout, path string) (proto.ManifestEntry, string, bool, error) {
	target := filepath.Join(layout.WorkspaceDir, filepath.FromSlash(path))
	info, err := os.Stat(target)
	if err != nil {
		if errorsIsNotExist(err) {
			return proto.ManifestEntry{}, "", false, nil
		}
		return proto.ManifestEntry{}, "", false, err
	}
	return proto.ManifestEntry{
		Path:     path,
		IsDir:    info.IsDir(),
		Metadata: fileInfoMetadata(info),
	}, target, true, nil
}

func (m *WorkspaceManager) remoteContentHash(session *syncSession, committedEntries map[string]proto.ManifestEntry, path string, target string) (string, error) {
	if entry, ok := committedEntries[path]; ok && entry.ContentHash != "" {
		return entry.ContentHash, nil
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	memoKey := remoteHashMemoEntry{
		size:       info.Size(),
		mode:       uint32(info.Mode().Perm()),
		mtimeNanos: info.ModTime().UTC().UnixNano(),
	}

	m.mu.Lock()
	if memo, ok := session.remoteHashMemo[path]; ok && memo.size == memoKey.size && memo.mode == memoKey.mode && memo.mtimeNanos == memoKey.mtimeNanos {
		hashValue := memo.hash
		m.mu.Unlock()
		return hashValue, nil
	}
	m.mu.Unlock()

	hashValue, err := syncHashFileFn(target)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	session.remoteHashMemo[path] = remoteHashMemoEntry{
		size:       memoKey.size,
		mode:       memoKey.mode,
		mtimeNanos: memoKey.mtimeNanos,
		hash:       hashValue,
	}
	m.mu.Unlock()
	return hashValue, nil
}

func (m *WorkspaceManager) ensureCommittedManifestLocked(layout ProjectLayout, state *workspaceState) error {
	if state == nil {
		return nil
	}
	if len(state.committed.manifest) > 0 || state.committed.generation != 0 {
		return nil
	}
	entries, err := manifestEntriesFromRoot(layout.WorkspaceDir)
	if err != nil {
		if errorsIsNotExist(err) {
			entries = map[string]proto.ManifestEntry{}
		} else {
			return err
		}
	}
	state.syncManifestStateLocked(entries)
	if state.committed.generation > 0 {
		state.committed.generation--
	}
	return nil
}

func syncManifestMatchesWorkspace(entries map[string]proto.ManifestEntry, root string) bool {
	currentEntries, err := manifestEntriesFromRoot(root)
	if err != nil {
		return false
	}
	if len(currentEntries) != len(entries) {
		return false
	}
	for path, entry := range entries {
		currentEntry, ok := currentEntries[path]
		if !ok {
			return false
		}
		if currentEntry.IsDir != entry.IsDir {
			return false
		}
		if normalizeMetadata(currentEntry.Metadata) != normalizeMetadata(entry.Metadata) {
			return false
		}
	}
	return true
}

func cleanupWorkspaceDirectories(layout ProjectLayout, entries map[string]proto.ManifestEntry, syncMount bool) error {
	expectedDirs := make(map[string]struct{})
	for path, entry := range entries {
		if entry.IsDir {
			expectedDirs[path] = struct{}{}
			continue
		}
		for parent := parentPath(path); parent != ""; parent = parentPath(parent) {
			expectedDirs[parent] = struct{}{}
		}
	}
	if err := removeUnexpectedDirectories(layout.WorkspaceDir, expectedDirs); err != nil {
		return err
	}
	if syncMount {
		if err := removeUnexpectedDirectories(layout.MountDir, expectedDirs); err != nil {
			return err
		}
	}
	return nil
}

func removeUnexpectedDirectories(root string, expected map[string]struct{}) error {
	currentEntries, err := manifestEntriesFromRoot(root)
	if err != nil {
		if errorsIsNotExist(err) {
			return nil
		}
		return err
	}
	var removePaths []string
	for path, entry := range currentEntries {
		if !entry.IsDir {
			continue
		}
		if _, ok := expected[path]; ok {
			continue
		}
		removePaths = append(removePaths, path)
	}
	sort.Slice(removePaths, func(left, right int) bool {
		if len(removePaths[left]) == len(removePaths[right]) {
			return removePaths[left] > removePaths[right]
		}
		return len(removePaths[left]) > len(removePaths[right])
	})
	for _, path := range removePaths {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.Remove(target); err != nil && !errorsIsNotExist(err) {
			if !isDirectoryNotEmpty(err) {
				return err
			}
		}
	}
	return nil
}

func sortedDeletePathsDeepestFirst(paths map[string]struct{}) []string {
	values := sortedDeletePaths(paths)
	sort.Slice(values, func(left, right int) bool {
		if len(values[left]) == len(values[right]) {
			return values[left] > values[right]
		}
		return len(values[left]) > len(values[right])
	})
	return values
}

func traceSyncStage(projectKey proto.ProjectKey, stage string, started time.Time, kv ...any) {
	if started.IsZero() {
		return
	}
	message := fmt.Sprintf("sync trace project=%s stage=%s duration=%s", projectKey.String(), strings.TrimSpace(stage), time.Since(started).Round(time.Millisecond))
	for idx := 0; idx+1 < len(kv); idx += 2 {
		message += fmt.Sprintf(" %v=%v", kv[idx], kv[idx+1])
	}
	diagnostic.Default().Errorf("%s", message)
}

func isDirectoryNotEmpty(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "directory not empty")
}

var syncHashFileFn = hashFile

func sortedDeletePaths(paths map[string]struct{}) []string {
	if len(paths) == 0 {
		return nil
	}
	values := make([]string, 0, len(paths))
	for path := range paths {
		values = append(values, path)
	}
	sort.Slice(values, func(left, right int) bool {
		if len(values[left]) == len(values[right]) {
			return values[left] < values[right]
		}
		return len(values[left]) < len(values[right])
	})
	return uniqueStrings(values)
}

func isPathDeletedOrNested(path string, deleted map[string]struct{}) bool {
	for candidate := range deleted {
		if isWorkspacePathEqualOrNested(path, candidate) {
			return true
		}
	}
	return false
}

func isWorkspacePathEqualOrNested(path string, prefix string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	prefix = filepath.ToSlash(strings.TrimSpace(prefix))
	if path == "" || prefix == "" {
		return false
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func mergeStagedFilesIntoChangedSet(stageRoot string, entries map[string]proto.ManifestEntry, changedFiles map[string]proto.ManifestEntry) error {
	stagedPaths, err := listWorkspaceEntries(stageRoot)
	if err != nil {
		if errorsIsNotExist(err) {
			return nil
		}
		return err
	}
	for _, path := range stagedPaths {
		entry, ok := entries[path]
		if !ok || entry.IsDir {
			continue
		}
		changedFiles[path] = entry
	}
	return nil
}

func syncSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
