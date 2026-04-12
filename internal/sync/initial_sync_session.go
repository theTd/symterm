package sync

import (
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"symterm/internal/diagnostic"
	"symterm/internal/proto"
	"symterm/internal/transport"
)

const syncChunkSize = 512 * 1024
const initialSyncMaxAttempts = 3
const syncManifestBatchSize = 512
const syncBundleFileThreshold = 256 * 1024
const syncBundleTargetBytes = 8 * 1024 * 1024
const syncBundleMaxFiles = 256

type InitialSyncSessionRunner struct {
	Capabilities proto.SyncCapabilities
	Tracef       func(string, ...any)
}

type syncTraceStats struct {
	protocolVersion uint32
	manifestBatches uint64
	hashRequests    uint64
	deleteBatches   uint64
	uploadBundles   uint64
	hashCacheHits   uint64
	hashCacheMisses uint64
	rpcCount        uint64
}

type uploadBundle struct {
	files     []LocalWorkspaceFile
	totalSize int64
}

func (r InitialSyncSessionRunner) SyncProjectWorkspace(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	snapshot LocalWorkspaceSnapshot,
	syncEpoch uint64,
	observer *InitialSyncObserver,
) (proto.ProjectSnapshot, error) {
	if supportsSyncV2(r.Capabilities) {
		finalized, err := r.performSyncV2(ctx, client, clientID, snapshot, syncEpoch, observer)
		if err == nil {
			return finalized, nil
		}
		var protoErr *proto.Error
		if errors.As(err, &protoErr) && protoErr.Code != proto.ErrInvalidArgument {
			return proto.ProjectSnapshot{}, err
		}
		r.tracef("sync v2 fallback activated err=%v", err)
	}
	return r.performSyncV1(ctx, client, clientID, snapshot, syncEpoch, observer)
}

func PerformInitialSync(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	snapshot LocalWorkspaceSnapshot,
	syncEpoch uint64,
	observer *InitialSyncObserver,
) (proto.ProjectSnapshot, error) {
	return InitialSyncSessionRunner{}.SyncProjectWorkspace(ctx, client, clientID, snapshot, syncEpoch, observer)
}

func (r InitialSyncSessionRunner) performSyncV1(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	snapshot LocalWorkspaceSnapshot,
	syncEpoch uint64,
	observer *InitialSyncObserver,
) (proto.ProjectSnapshot, error) {
	guardCtx, cancelGuard := context.WithCancel(ctx)
	defer cancelGuard()

	guard := StartSyncGuard(guardCtx, snapshot.Root, snapshot, observer)
	current := snapshot
	var lastErr error
	stats := syncTraceStats{protocolVersion: 1}
	defer func() {
		r.finishTrace(&stats, guard)
	}()
	for attempt := 0; attempt < initialSyncMaxAttempts; attempt++ {
		observer.Operationf("Starting sync attempt %d/%d", attempt+1, initialSyncMaxAttempts)
		attemptID := uint64(attempt + 1)
		attemptState := guard.AttemptStart()
		finalized, err := r.performInitialSyncAttemptV1(ctx, client, clientID, current, syncEpoch, attemptID, guard, attemptState, observer, &stats)
		if err == nil {
			return finalized, nil
		}
		var protoErr *proto.Error
		if errors.As(err, &protoErr) && protoErr.Code == proto.ErrSyncRescanMismatch {
			observer.Operation("Workspace changed during sync; retrying")
			lastErr = err
			refreshed, refreshErr := guard.RefreshSnapshot()
			if refreshErr != nil {
				return proto.ProjectSnapshot{}, refreshErr
			}
			current = refreshed
			continue
		}
		return proto.ProjectSnapshot{}, err
	}
	if lastErr != nil {
		return proto.ProjectSnapshot{}, lastErr
	}
	return proto.ProjectSnapshot{}, proto.NewError(proto.ErrSyncRescanMismatch, "workspace changed during initial sync")
}

func (r InitialSyncSessionRunner) performSyncV2(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	snapshot LocalWorkspaceSnapshot,
	syncEpoch uint64,
	observer *InitialSyncObserver,
) (proto.ProjectSnapshot, error) {
	guardCtx, cancelGuard := context.WithCancel(ctx)
	defer cancelGuard()

	guard := StartSyncGuard(guardCtx, snapshot.Root, snapshot, observer)
	current := snapshot
	var lastErr error
	stats := syncTraceStats{protocolVersion: 2}
	defer func() {
		r.finishTrace(&stats, guard)
	}()
	for attempt := 0; attempt < initialSyncMaxAttempts; attempt++ {
		observer.Operationf("Starting sync attempt %d/%d", attempt+1, initialSyncMaxAttempts)
		attemptID := uint64(attempt + 1)
		attemptState := guard.AttemptStart()
		finalized, err := r.performInitialSyncAttemptV2(ctx, client, clientID, current, syncEpoch, attemptID, guard, attemptState, observer, &stats)
		if err == nil {
			return finalized, nil
		}
		var protoErr *proto.Error
		if errors.As(err, &protoErr) && protoErr.Code == proto.ErrSyncRescanMismatch {
			observer.Operation("Workspace changed during sync; retrying")
			lastErr = err
			refreshed, refreshErr := guard.RefreshSnapshot()
			if refreshErr != nil {
				return proto.ProjectSnapshot{}, refreshErr
			}
			current = refreshed
			continue
		}
		return proto.ProjectSnapshot{}, err
	}
	if lastErr != nil {
		return proto.ProjectSnapshot{}, lastErr
	}
	return proto.ProjectSnapshot{}, proto.NewError(proto.ErrSyncRescanMismatch, "workspace changed during initial sync")
}

func (r InitialSyncSessionRunner) performInitialSyncAttemptV1(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	snapshot LocalWorkspaceSnapshot,
	syncEpoch uint64,
	attemptID uint64,
	guard *SyncGuard,
	attemptState SyncGuardAttempt,
	observer *InitialSyncObserver,
	stats *syncTraceStats,
) (proto.ProjectSnapshot, error) {
	observer.Operation("Scanning workspace manifest")
	scanStarted := time.Now()
	if err := trackedCall(ctx, client, clientID, "begin_sync", proto.BeginSyncRequest{
		SyncEpoch:       syncEpoch,
		AttemptID:       attemptID,
		RootFingerprint: snapshot.Fingerprint,
	}, nil, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if err := trackedCall(ctx, client, clientID, "scan_manifest", proto.ScanManifestRequest{
		Entries: snapshot.Entries,
	}, nil, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseScanManifest, 1, 1, observer, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	observer.Operationf("Stage scan manifest finished in %s", time.Since(scanStarted).Round(time.Millisecond))

	observer.Operation("Planning manifest hash requirements")
	hashPlanStarted := time.Now()
	hashesNeeded, err := requestPlanManifestHashes(ctx, client, clientID, stats)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	stats.hashRequests += uint64(len(hashesNeeded.Paths))
	if len(hashesNeeded.Paths) > 0 {
		observer.Operationf("Hashing %d requested paths", len(hashesNeeded.Paths))
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseHashManifest, 0, uint64(len(hashesNeeded.Paths)), observer, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		hashSnapshot, err := guard.HashPaths(snapshot, hashesNeeded.Paths)
		if err != nil {
			return proto.ProjectSnapshot{}, err
		}
		entries := manifestEntriesForPaths(hashSnapshot, hashesNeeded.Paths)
		snapshot = hashSnapshot
		if len(entries) > 0 {
			if err := trackedCall(ctx, client, clientID, "scan_manifest", proto.ScanManifestRequest{
				Entries: entries,
			}, nil, stats); err != nil {
				return proto.ProjectSnapshot{}, err
			}
			if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseHashManifest, uint64(len(entries)), uint64(len(hashesNeeded.Paths)), observer, stats); err != nil {
				return proto.ProjectSnapshot{}, err
			}
		}
	}
	observer.Operationf("Stage plan manifest hashes finished in %s", time.Since(hashPlanStarted).Round(time.Millisecond))

	observer.Operation("Planning sync actions")
	uploadStarted := time.Now()
	actions, err := requestPlanSyncActions(ctx, client, clientID, stats)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	snapshot, err = guard.HashPaths(snapshot, actions.UploadPaths)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	deletePaths := collapseDeletePaths(actions.DeletePaths)
	uploadFiles := localWorkspaceFilesForPaths(snapshot, actions.UploadPaths)
	totalUploads := uint64(len(deletePaths) + len(uploadFiles))
	if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseUploadFiles, 0, totalUploads, observer, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	var completedUploads uint64
	for _, path := range deletePaths {
		observer.Operationf("Deleting stale path %s", path)
		if err := trackedCall(ctx, client, clientID, "delete_path", proto.DeletePathRequest{
			SyncEpoch: syncEpoch,
			Path:      path,
		}, nil, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		completedUploads++
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseUploadFiles, completedUploads, totalUploads, observer, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
	}
	for _, localFile := range uploadFiles {
		observer.Operationf("Uploading %s", localFile.Path)
		if err := UploadLocalFile(ctx, client, clientID, syncEpoch, localFile, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		completedUploads++
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseUploadFiles, completedUploads, totalUploads, observer, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
	}
	observer.Operationf("Stage upload files finished in %s", time.Since(uploadStarted).Round(time.Millisecond))

	return finalizeSyncAttemptV1(ctx, client, clientID, syncEpoch, attemptID, guard, attemptState, observer, stats)
}

func (r InitialSyncSessionRunner) performInitialSyncAttemptV2(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	snapshot LocalWorkspaceSnapshot,
	syncEpoch uint64,
	attemptID uint64,
	guard *SyncGuard,
	attemptState SyncGuardAttempt,
	observer *InitialSyncObserver,
	stats *syncTraceStats,
) (proto.ProjectSnapshot, error) {
	observer.Operation("Starting sync session")
	var session proto.StartSyncSessionResponse
	if err := trackedCall(ctx, client, clientID, "start_sync_session", proto.StartSyncSessionRequest{
		SyncEpoch:       syncEpoch,
		AttemptID:       attemptID,
		RootFingerprint: snapshot.Fingerprint,
	}, &session, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if session.ProtocolVersion != 0 {
		stats.protocolVersion = session.ProtocolVersion
	}

	observer.Operation("Sending workspace manifest")
	scanStarted := time.Now()
	if err := sendManifestBatches(ctx, client, clientID, session.SessionID, snapshot.Entries, true, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseScanManifest, 1, 1, observer, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	observer.Operationf("Stage scan manifest finished in %s", time.Since(scanStarted).Round(time.Millisecond))

	observer.Operation("Planning sync actions")
	hashPlanStarted := time.Now()
	var actions proto.PlanSyncV2Response
	for {
		planned, err := requestPlanSyncV2(ctx, client, clientID, session.SessionID, stats)
		if err != nil {
			return proto.ProjectSnapshot{}, err
		}
		if len(planned.HashPaths) == 0 {
			actions = planned
			break
		}
		stats.hashRequests += uint64(len(planned.HashPaths))
		observer.Operationf("Hashing %d requested paths", len(planned.HashPaths))
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseHashManifest, 0, uint64(len(planned.HashPaths)), observer, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		hashSnapshot, err := guard.HashPaths(snapshot, planned.HashPaths)
		if err != nil {
			return proto.ProjectSnapshot{}, err
		}
		snapshot = hashSnapshot
		entries := manifestEntriesForPaths(hashSnapshot, planned.HashPaths)
		if err := sendManifestBatches(ctx, client, clientID, session.SessionID, entries, false, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseHashManifest, uint64(len(entries)), uint64(len(planned.HashPaths)), observer, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
	}
	observer.Operationf("Stage plan manifest hashes finished in %s", time.Since(hashPlanStarted).Round(time.Millisecond))

	uploadStarted := time.Now()
	deletePaths := collapseDeletePaths(actions.DeletePaths)
	snapshot, err := guard.HashPaths(snapshot, actions.UploadPaths)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	uploadFiles := localWorkspaceFilesForPaths(snapshot, actions.UploadPaths)
	largeFiles, bundles := buildUploadPlan(uploadFiles)
	totalUploads := uint64(len(deletePaths) + len(largeFiles) + len(bundles))
	if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseUploadFiles, 0, totalUploads, observer, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	var completedUploads uint64
	if len(deletePaths) > 0 {
		observer.Operationf("Deleting %d stale paths", len(deletePaths))
		if err := trackedCall(ctx, client, clientID, "delete_paths_batch", proto.DeletePathsBatchRequest{
			SessionID: session.SessionID,
			SyncEpoch: syncEpoch,
			Paths:     deletePaths,
		}, nil, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		stats.deleteBatches++
		completedUploads += uint64(len(deletePaths))
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseUploadFiles, completedUploads, totalUploads, observer, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
	}
	for _, bundle := range bundles {
		observer.Operationf("Uploading bundle with %d files", len(bundle.files))
		if err := uploadBundleFiles(ctx, client, clientID, session.SessionID, syncEpoch, bundle.files, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		stats.uploadBundles++
		completedUploads++
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseUploadFiles, completedUploads, totalUploads, observer, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
	}
	for _, localFile := range largeFiles {
		observer.Operationf("Uploading %s", localFile.Path)
		if err := UploadLocalFile(ctx, client, clientID, syncEpoch, localFile, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		completedUploads++
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseUploadFiles, completedUploads, totalUploads, observer, stats); err != nil {
			return proto.ProjectSnapshot{}, err
		}
	}
	observer.Operationf("Stage upload files finished in %s", time.Since(uploadStarted).Round(time.Millisecond))

	observer.Operation("Validating sync guard before finalize")
	finalizeStarted := time.Now()
	if err := guard.Validate(attemptState); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	observer.Operation("Finalizing workspace sync")
	if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseFinalize, 0, 1, observer, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	var finalized proto.ProjectSnapshot
	if err := trackedCall(ctx, client, clientID, "finalize_sync_v2", proto.FinalizeSyncV2Request{
		SessionID:   session.SessionID,
		SyncEpoch:   syncEpoch,
		AttemptID:   attemptID,
		GuardStable: true,
	}, &finalized, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	observer.Operationf("Stage finalize publish finished in %s", time.Since(finalizeStarted).Round(time.Millisecond))
	return finalized, nil
}

func finalizeSyncAttemptV1(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	syncEpoch uint64,
	attemptID uint64,
	guard *SyncGuard,
	attemptState SyncGuardAttempt,
	observer *InitialSyncObserver,
	stats *syncTraceStats,
) (proto.ProjectSnapshot, error) {
	observer.Operation("Validating sync guard before finalize")
	finalizeStarted := time.Now()
	if err := guard.Validate(attemptState); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	observer.Operation("Finalizing workspace sync")
	if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseFinalize, 0, 1, observer, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	var finalized proto.ProjectSnapshot
	if err := trackedCall(ctx, client, clientID, "finalize_sync", proto.FinalizeSyncRequest{
		SyncEpoch:   syncEpoch,
		AttemptID:   attemptID,
		GuardStable: true,
	}, &finalized, stats); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	observer.Operationf("Stage finalize publish finished in %s", time.Since(finalizeStarted).Round(time.Millisecond))
	return finalized, nil
}

func requestPlanManifestHashes(ctx context.Context, client *transport.Client, clientID string, stats *syncTraceStats) (proto.PlanManifestHashesResponse, error) {
	var response proto.PlanManifestHashesResponse
	if err := trackedCall(ctx, client, clientID, "plan_manifest_hashes", nil, &response, stats); err != nil {
		return proto.PlanManifestHashesResponse{}, err
	}
	return response, nil
}

func requestPlanSyncActions(ctx context.Context, client *transport.Client, clientID string, stats *syncTraceStats) (proto.PlanSyncActionsResponse, error) {
	var response proto.PlanSyncActionsResponse
	if err := trackedCall(ctx, client, clientID, "plan_sync_actions", nil, &response, stats); err != nil {
		return proto.PlanSyncActionsResponse{}, err
	}
	return response, nil
}

func requestPlanSyncV2(ctx context.Context, client *transport.Client, clientID string, sessionID string, stats *syncTraceStats) (proto.PlanSyncV2Response, error) {
	var response proto.PlanSyncV2Response
	if err := trackedCall(ctx, client, clientID, "plan_sync_v2", proto.PlanSyncV2Request{SessionID: sessionID}, &response, stats); err != nil {
		return proto.PlanSyncV2Response{}, err
	}
	return response, nil
}

func reportSyncProgress(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	syncEpoch uint64,
	phase proto.SyncProgressPhase,
	completed uint64,
	total uint64,
	observer *InitialSyncObserver,
	stats *syncTraceStats,
) error {
	progress := proto.SyncProgress{
		Phase:     phase,
		Completed: completed,
		Total:     total,
	}
	observer.Progress(progress)
	if stats != nil {
		stats.rpcCount++
	}
	return client.ReportSyncProgress(ctx, clientID, proto.ReportSyncProgressRequest{
		SyncEpoch: syncEpoch,
		Progress:  progress,
	})
}

func UploadLocalFile(ctx context.Context, client *transport.Client, clientID string, syncEpoch uint64, localFile LocalWorkspaceFile, stats *syncTraceStats) error {
	var started proto.BeginFileResponse
	if err := trackedCall(ctx, client, clientID, "begin_file", proto.BeginFileRequest{
		SyncEpoch:       syncEpoch,
		Path:            localFile.Path,
		Metadata:        localFile.Entry.Metadata,
		ExpectedSize:    localFile.Entry.Metadata.Size,
		StatFingerprint: localFile.Entry.StatFingerprint,
	}, &started, stats); err != nil {
		return err
	}

	file, err := os.Open(localFile.Abs)
	if err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "abort file upload "+localFile.Path, trackedCall(ctx, client, clientID, "abort_file", proto.AbortFileRequest{
			FileID: started.FileID,
			Reason: err.Error(),
		}, nil, stats))
		return err
	}
	defer file.Close()

	buf := make([]byte, syncChunkSize)
	var offset int64
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			if err := trackedCall(ctx, client, clientID, "apply_chunk", proto.ApplyChunkRequest{
				FileID:   started.FileID,
				Offset:   offset,
				Data:     append([]byte(nil), buf[:n]...),
				Checksum: "",
			}, nil, stats); err != nil {
				diagnostic.Cleanup(diagnostic.Default(), "abort file upload "+localFile.Path, trackedCall(ctx, client, clientID, "abort_file", proto.AbortFileRequest{
					FileID: started.FileID,
					Reason: err.Error(),
				}, nil, stats))
				return err
			}
			offset += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			diagnostic.Cleanup(diagnostic.Default(), "abort file upload "+localFile.Path, trackedCall(ctx, client, clientID, "abort_file", proto.AbortFileRequest{
				FileID: started.FileID,
				Reason: readErr.Error(),
			}, nil, stats))
			return readErr
		}
	}

	return trackedCall(ctx, client, clientID, "commit_file", proto.CommitFileRequest{
		FileID:          started.FileID,
		FinalHash:       localFile.Entry.ContentHash,
		FinalSize:       localFile.Entry.Metadata.Size,
		MTime:           localFile.Entry.Metadata.MTime,
		Mode:            localFile.Entry.Metadata.Mode,
		StatFingerprint: localFile.Entry.StatFingerprint,
	}, nil, stats)
}

func trackedCall(ctx context.Context, client *transport.Client, clientID string, method string, params any, result any, stats *syncTraceStats) error {
	if stats != nil {
		stats.rpcCount++
	}
	return client.Call(ctx, method, clientID, params, result)
}

func sendManifestBatches(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	sessionID string,
	entries []proto.ManifestEntry,
	final bool,
	stats *syncTraceStats,
) error {
	if len(entries) == 0 {
		if !final {
			return nil
		}
		if err := trackedCall(ctx, client, clientID, "sync_manifest_batch", proto.SyncManifestBatchRequest{
			SessionID: sessionID,
			Final:     true,
		}, nil, stats); err != nil {
			return err
		}
		stats.manifestBatches++
		return nil
	}
	for start := 0; start < len(entries); start += syncManifestBatchSize {
		end := start + syncManifestBatchSize
		if end > len(entries) {
			end = len(entries)
		}
		if err := trackedCall(ctx, client, clientID, "sync_manifest_batch", proto.SyncManifestBatchRequest{
			SessionID: sessionID,
			Entries:   append([]proto.ManifestEntry(nil), entries[start:end]...),
			Final:     final && end == len(entries),
		}, nil, stats); err != nil {
			return err
		}
		stats.manifestBatches++
	}
	return nil
}

func manifestEntriesForPaths(snapshot LocalWorkspaceSnapshot, paths []string) []proto.ManifestEntry {
	entries := make([]proto.ManifestEntry, 0, len(paths))
	for _, path := range paths {
		localFile, ok := snapshot.HashedFiles[path]
		if !ok {
			continue
		}
		entries = append(entries, localFile.Entry)
	}
	return entries
}

func localWorkspaceFilesForPaths(snapshot LocalWorkspaceSnapshot, paths []string) []LocalWorkspaceFile {
	files := make([]LocalWorkspaceFile, 0, len(paths))
	for _, path := range paths {
		localFile, ok := snapshot.Files[path]
		if !ok {
			continue
		}
		files = append(files, localFile)
	}
	return files
}

func buildUploadPlan(files []LocalWorkspaceFile) ([]LocalWorkspaceFile, []uploadBundle) {
	if len(files) == 0 {
		return nil, nil
	}
	sort.Slice(files, func(left, right int) bool {
		if files[left].Entry.Metadata.Size == files[right].Entry.Metadata.Size {
			return files[left].Path < files[right].Path
		}
		return files[left].Entry.Metadata.Size < files[right].Entry.Metadata.Size
	})

	largeFiles := make([]LocalWorkspaceFile, 0, len(files))
	bundles := make([]uploadBundle, 0)
	current := uploadBundle{}
	for _, file := range files {
		size := file.Entry.Metadata.Size
		if size <= 0 || size > syncBundleFileThreshold {
			largeFiles = append(largeFiles, file)
			continue
		}
		if len(current.files) > 0 && (len(current.files) >= syncBundleMaxFiles || current.totalSize+size > syncBundleTargetBytes) {
			bundles = append(bundles, current)
			current = uploadBundle{}
		}
		current.files = append(current.files, file)
		current.totalSize += size
	}
	if len(current.files) > 0 {
		bundles = append(bundles, current)
	}
	return largeFiles, bundles
}

func uploadBundleFiles(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	sessionID string,
	syncEpoch uint64,
	files []LocalWorkspaceFile,
	stats *syncTraceStats,
) error {
	var started proto.UploadBundleBeginResponse
	if err := trackedCall(ctx, client, clientID, "upload_bundle_begin", proto.UploadBundleBeginRequest{
		SessionID: sessionID,
		SyncEpoch: syncEpoch,
	}, &started, stats); err != nil {
		return err
	}
	request := proto.UploadBundleCommitRequest{
		SessionID: sessionID,
		BundleID:  started.BundleID,
		Files:     make([]proto.UploadBundleFile, 0, len(files)),
	}
	for _, localFile := range files {
		data, err := os.ReadFile(localFile.Abs)
		if err != nil {
			return err
		}
		request.Files = append(request.Files, proto.UploadBundleFile{
			Path:            localFile.Path,
			Metadata:        localFile.Entry.Metadata,
			StatFingerprint: localFile.Entry.StatFingerprint,
			ContentHash:     localFile.Entry.ContentHash,
			Data:            data,
		})
	}
	return trackedCall(ctx, client, clientID, "upload_bundle_commit", request, nil, stats)
}

func supportsSyncV2(capabilities proto.SyncCapabilities) bool {
	return capabilities.ProtocolVersion >= 2 &&
		capabilities.ManifestBatch &&
		capabilities.DeleteBatch &&
		capabilities.UploadBundle
}

func (r InitialSyncSessionRunner) finishTrace(stats *syncTraceStats, guard *SyncGuard) {
	if stats == nil {
		return
	}
	if guard != nil {
		stats.hashCacheHits, stats.hashCacheMisses = guard.PersistentHashCacheStats()
	}
	r.tracef(
		"sync trace protocol_version=%d manifest_batches=%d hash_requests=%d delete_batches=%d upload_bundles=%d hash_cache_hits=%d hash_cache_misses=%d sync_rpc_count=%d",
		stats.protocolVersion,
		stats.manifestBatches,
		stats.hashRequests,
		stats.deleteBatches,
		stats.uploadBundles,
		stats.hashCacheHits,
		stats.hashCacheMisses,
		stats.rpcCount,
	)
}

func (r InitialSyncSessionRunner) tracef(format string, args ...any) {
	if r.Tracef == nil {
		return
	}
	r.Tracef(format, args...)
}

func pathSet(paths []string) map[string]bool {
	result := make(map[string]bool, len(paths))
	for _, path := range paths {
		result[path] = true
	}
	return result
}

func collapseDeletePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	sorted := append([]string(nil), paths...)
	sort.Slice(sorted, func(left, right int) bool {
		if len(sorted[left]) == len(sorted[right]) {
			return sorted[left] < sorted[right]
		}
		return len(sorted[left]) < len(sorted[right])
	})

	var collapsed []string
	for _, path := range sorted {
		if isNestedUnderDeletedPath(path, collapsed) {
			continue
		}
		collapsed = append(collapsed, path)
	}
	return collapsed
}

func isNestedUnderDeletedPath(path string, deleted []string) bool {
	for _, candidate := range deleted {
		if candidate == "" || !strings.HasPrefix(path, candidate) {
			continue
		}
		if path == candidate || strings.HasPrefix(path, candidate+"/") {
			return true
		}
	}
	return false
}
