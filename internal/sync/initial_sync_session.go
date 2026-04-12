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

type InitialSyncSessionRunner struct{}

func (InitialSyncSessionRunner) SyncProjectWorkspace(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	snapshot LocalWorkspaceSnapshot,
	syncEpoch uint64,
	observer *InitialSyncObserver,
) (proto.ProjectSnapshot, error) {
	return PerformInitialSync(ctx, client, clientID, snapshot, syncEpoch, observer)
}

func PerformInitialSync(
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
	for attempt := 0; attempt < initialSyncMaxAttempts; attempt++ {
		observer.Operationf("Starting sync attempt %d/%d", attempt+1, initialSyncMaxAttempts)
		attemptID := uint64(attempt + 1)
		attemptState := guard.AttemptStart()
		finalized, err := performInitialSyncAttempt(ctx, client, clientID, current, syncEpoch, attemptID, guard, attemptState, observer)
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

func performInitialSyncAttempt(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	snapshot LocalWorkspaceSnapshot,
	syncEpoch uint64,
	attemptID uint64,
	guard *SyncGuard,
	attemptState SyncGuardAttempt,
	observer *InitialSyncObserver,
) (proto.ProjectSnapshot, error) {
	observer.Operation("Scanning workspace manifest")
	scanStarted := time.Now()
	if err := client.Call(ctx, "begin_sync", clientID, proto.BeginSyncRequest{
		SyncEpoch:       syncEpoch,
		AttemptID:       attemptID,
		RootFingerprint: snapshot.Fingerprint,
	}, nil); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if err := client.Call(ctx, "scan_manifest", clientID, proto.ScanManifestRequest{
		Entries: snapshot.Entries,
	}, nil); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseScanManifest, 1, 1, observer); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	observer.Operationf("Stage scan manifest finished in %s", time.Since(scanStarted).Round(time.Millisecond))

	observer.Operation("Planning manifest hash requirements")
	hashPlanStarted := time.Now()
	hashesNeeded, err := requestPlanManifestHashes(ctx, client, clientID)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if len(hashesNeeded.Paths) > 0 {
		observer.Operationf("Hashing %d requested paths", len(hashesNeeded.Paths))
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseHashManifest, 0, uint64(len(hashesNeeded.Paths)), observer); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		hashSnapshot, err := guard.HashPaths(snapshot, hashesNeeded.Paths)
		if err != nil {
			return proto.ProjectSnapshot{}, err
		}
		entries := make([]proto.ManifestEntry, 0, len(hashesNeeded.Paths))
		for _, path := range hashesNeeded.Paths {
			localFile, ok := hashSnapshot.HashedFiles[path]
			if !ok {
				continue
			}
			entries = append(entries, localFile.Entry)
		}
		snapshot = hashSnapshot
		if len(entries) > 0 {
			if err := client.Call(ctx, "scan_manifest", clientID, proto.ScanManifestRequest{
				Entries: entries,
			}, nil); err != nil {
				return proto.ProjectSnapshot{}, err
			}
			if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseHashManifest, uint64(len(entries)), uint64(len(hashesNeeded.Paths)), observer); err != nil {
				return proto.ProjectSnapshot{}, err
			}
		}
	}
	observer.Operationf("Stage plan manifest hashes finished in %s", time.Since(hashPlanStarted).Round(time.Millisecond))

	observer.Operation("Planning sync actions")
	uploadStarted := time.Now()
	actions, err := requestPlanSyncActions(ctx, client, clientID)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	snapshot, err = guard.HashPaths(snapshot, actions.UploadPaths)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	deletePaths := collapseDeletePaths(actions.DeletePaths)
	uploadFiles := make([]LocalWorkspaceFile, 0, len(actions.UploadPaths))
	for _, path := range actions.UploadPaths {
		localFile, ok := snapshot.Files[path]
		if !ok {
			continue
		}
		uploadFiles = append(uploadFiles, localFile)
	}
	totalUploads := uint64(len(deletePaths) + len(uploadFiles))
	if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseUploadFiles, 0, totalUploads, observer); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	var completedUploads uint64
	for _, path := range deletePaths {
		observer.Operationf("Deleting stale path %s", path)
		if err := client.Call(ctx, "delete_path", clientID, proto.DeletePathRequest{
			SyncEpoch: syncEpoch,
			Path:      path,
		}, nil); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		completedUploads++
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseUploadFiles, completedUploads, totalUploads, observer); err != nil {
			return proto.ProjectSnapshot{}, err
		}
	}
	for _, localFile := range uploadFiles {
		observer.Operationf("Uploading %s", localFile.Path)
		if err := UploadLocalFile(ctx, client, clientID, syncEpoch, localFile); err != nil {
			return proto.ProjectSnapshot{}, err
		}
		completedUploads++
		if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseUploadFiles, completedUploads, totalUploads, observer); err != nil {
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
	if err := reportSyncProgress(ctx, client, clientID, syncEpoch, proto.SyncProgressPhaseFinalize, 0, 1, observer); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	var finalized proto.ProjectSnapshot
	if err := client.Call(ctx, "finalize_sync", clientID, proto.FinalizeSyncRequest{
		SyncEpoch:   syncEpoch,
		AttemptID:   attemptID,
		GuardStable: true,
	}, &finalized); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	observer.Operationf("Stage finalize publish finished in %s", time.Since(finalizeStarted).Round(time.Millisecond))
	return finalized, nil
}

func requestPlanManifestHashes(ctx context.Context, client *transport.Client, clientID string) (proto.PlanManifestHashesResponse, error) {
	var response proto.PlanManifestHashesResponse
	if err := client.Call(ctx, "plan_manifest_hashes", clientID, nil, &response); err != nil {
		return proto.PlanManifestHashesResponse{}, err
	}
	return response, nil
}

func requestPlanSyncActions(ctx context.Context, client *transport.Client, clientID string) (proto.PlanSyncActionsResponse, error) {
	var response proto.PlanSyncActionsResponse
	if err := client.Call(ctx, "plan_sync_actions", clientID, nil, &response); err != nil {
		return proto.PlanSyncActionsResponse{}, err
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
) error {
	progress := proto.SyncProgress{
		Phase:     phase,
		Completed: completed,
		Total:     total,
	}
	observer.Progress(progress)
	return client.ReportSyncProgress(ctx, clientID, proto.ReportSyncProgressRequest{
		SyncEpoch: syncEpoch,
		Progress:  progress,
	})
}

func UploadLocalFile(ctx context.Context, client *transport.Client, clientID string, syncEpoch uint64, localFile LocalWorkspaceFile) error {
	var started proto.BeginFileResponse
	if err := client.Call(ctx, "begin_file", clientID, proto.BeginFileRequest{
		SyncEpoch:       syncEpoch,
		Path:            localFile.Path,
		Metadata:        localFile.Entry.Metadata,
		ExpectedSize:    localFile.Entry.Metadata.Size,
		StatFingerprint: localFile.Entry.StatFingerprint,
	}, &started); err != nil {
		return err
	}

	file, err := os.Open(localFile.Abs)
	if err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "abort file upload "+localFile.Path, client.Call(ctx, "abort_file", clientID, proto.AbortFileRequest{
			FileID: started.FileID,
			Reason: err.Error(),
		}, nil))
		return err
	}
	defer file.Close()

	buf := make([]byte, syncChunkSize)
	var offset int64
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			if err := client.Call(ctx, "apply_chunk", clientID, proto.ApplyChunkRequest{
				FileID:   started.FileID,
				Offset:   offset,
				Data:     append([]byte(nil), buf[:n]...),
				Checksum: "",
			}, nil); err != nil {
				diagnostic.Cleanup(diagnostic.Default(), "abort file upload "+localFile.Path, client.Call(ctx, "abort_file", clientID, proto.AbortFileRequest{
					FileID: started.FileID,
					Reason: err.Error(),
				}, nil))
				return err
			}
			offset += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			diagnostic.Cleanup(diagnostic.Default(), "abort file upload "+localFile.Path, client.Call(ctx, "abort_file", clientID, proto.AbortFileRequest{
				FileID: started.FileID,
				Reason: readErr.Error(),
			}, nil))
			return readErr
		}
	}

	return client.Call(ctx, "commit_file", clientID, proto.CommitFileRequest{
		FileID:          started.FileID,
		FinalHash:       localFile.Entry.ContentHash,
		FinalSize:       localFile.Entry.Metadata.Size,
		MTime:           localFile.Entry.Metadata.MTime,
		Mode:            localFile.Entry.Metadata.Mode,
		StatFingerprint: localFile.Entry.StatFingerprint,
	}, nil)
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
