package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"symterm/internal/diagnostic"
	"symterm/internal/proto"
	"symterm/internal/transport"
)

const (
	ownerWorkspaceRescanInterval = 2 * time.Second
	ownerWorkspaceWatchDebounce  = 75 * time.Millisecond
)

func startOwnerWorkspaceWatcher(ctx context.Context, client *transport.Client, clientID string, root string) func() {
	if client == nil || strings.TrimSpace(clientID) == "" || strings.TrimSpace(root) == "" {
		return nil
	}

	watchCtx, cancel := context.WithCancel(ctx)
	go runOwnerWorkspaceWatcher(watchCtx, client, clientID, root, ownerWorkspaceRescanInterval)
	return cancel
}

func runOwnerWorkspaceWatcher(ctx context.Context, client *transport.Client, clientID string, root string, interval time.Duration) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		runPollingOwnerWorkspaceWatcher(ctx, client, clientID, root, interval)
		return
	}
	defer watcher.Close()

	var (
		previous        *LocalWorkspaceSnapshot
		failureReported bool
		watchedDirs     = make(map[string]struct{})
	)

	initial, ok := rescanOwnerWorkspace(ctx, client, clientID, root, previous, &failureReported)
	if ok {
		previous = initial
		if err := syncOwnerWorkspaceWatches(watcher, watchedDirs, *initial); err != nil {
			reportOwnerWatcherFailure(ctx, client, clientID, &failureReported, err)
		}
	}

	rescanTicker := time.NewTicker(ownerWorkspaceRescanInterval)
	defer rescanTicker.Stop()
	debounce := time.NewTimer(time.Hour)
	stopOwnerWatchTimer(debounce)
	pendingScan := false

	scheduleScan := func() {
		if pendingScan {
			resetOwnerWatchTimer(debounce, ownerWorkspaceWatchDebounce)
			return
		}
		pendingScan = true
		resetOwnerWatchTimer(debounce, ownerWorkspaceWatchDebounce)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-watcher.Events:
			if !ok {
				reportOwnerWatcherFailure(ctx, client, clientID, &failureReported, errors.New("owner watcher event stream closed"))
				return
			}
			scheduleScan()
		case err, ok := <-watcher.Errors:
			if !ok {
				reportOwnerWatcherFailure(ctx, client, clientID, &failureReported, errors.New("owner watcher error stream closed"))
				return
			}
			reportOwnerWatcherFailure(ctx, client, clientID, &failureReported, err)
			scheduleScan()
		case <-rescanTicker.C:
			scheduleScan()
		case <-debounce.C:
			pendingScan = false
			current, ok := rescanOwnerWorkspace(ctx, client, clientID, root, previous, &failureReported)
			if !ok {
				continue
			}
			if err := syncOwnerWorkspaceWatches(watcher, watchedDirs, *current); err != nil {
				reportOwnerWatcherFailure(ctx, client, clientID, &failureReported, err)
			}
			previous = current
		}
	}
}

func runPollingOwnerWorkspaceWatcher(ctx context.Context, client *transport.Client, clientID string, root string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var previous *LocalWorkspaceSnapshot
	failureReported := false

	for {
		current, err := ScanLocalWorkspace(root, nil, true)
		if err != nil {
			if !failureReported {
				diagnostic.Error(diagnostic.Default(), "report owner watcher failure", client.Call(ctx, "_internal_owner_watcher_failed", clientID, proto.OwnerWatcherFailureRequest{
					Reason: err.Error(),
				}, nil))
				failureReported = true
			}
		} else {
			if previous != nil {
				changes := ownerWorkspaceChanges(*previous, current)
				if len(changes) > 0 {
					diagnostic.Error(diagnostic.Default(), "report owner workspace invalidation", client.Call(ctx, "_internal_invalidate", clientID, proto.InvalidateRequest{
						Changes: changes,
					}, nil))
				}
			}
			snapshot := current
			previous = &snapshot
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func rescanOwnerWorkspace(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	root string,
	previous *LocalWorkspaceSnapshot,
	failureReported *bool,
) (*LocalWorkspaceSnapshot, bool) {
	current, err := ScanLocalWorkspace(root, nil, true)
	if err != nil {
		reportOwnerWatcherFailure(ctx, client, clientID, failureReported, err)
		return nil, false
	}
	if previous != nil {
		changes := ownerWorkspaceChanges(*previous, current)
		if len(changes) > 0 {
			diagnostic.Error(diagnostic.Default(), "report owner workspace invalidation", client.Call(ctx, "_internal_invalidate", clientID, proto.InvalidateRequest{
				Changes: changes,
			}, nil))
		}
	}
	snapshot := current
	return &snapshot, true
}

func reportOwnerWatcherFailure(
	ctx context.Context,
	client *transport.Client,
	clientID string,
	failureReported *bool,
	err error,
) {
	if err == nil || *failureReported {
		return
	}
	diagnostic.Error(diagnostic.Default(), "report owner watcher failure", client.Call(ctx, "_internal_owner_watcher_failed", clientID, proto.OwnerWatcherFailureRequest{
		Reason: err.Error(),
	}, nil))
	*failureReported = true
}

func syncOwnerWorkspaceWatches(watcher *fsnotify.Watcher, watched map[string]struct{}, snapshot LocalWorkspaceSnapshot) error {
	if watcher == nil {
		return nil
	}

	desired := ownerWorkspaceWatchDirectories(snapshot)
	for path := range watched {
		if _, ok := desired[path]; ok {
			continue
		}
		if err := watcher.Remove(path); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			return err
		}
		delete(watched, path)
	}

	paths := make([]string, 0, len(desired))
	for path := range desired {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if _, ok := watched[path]; ok {
			continue
		}
		if err := watcher.Add(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		watched[path] = struct{}{}
	}
	return nil
}

func ownerWorkspaceWatchDirectories(snapshot LocalWorkspaceSnapshot) map[string]struct{} {
	paths := map[string]struct{}{
		snapshot.Root: {},
	}
	for _, entry := range snapshot.Dirs {
		if entry.Path == "" {
			continue
		}
		paths[filepath.Join(snapshot.Root, filepath.FromSlash(entry.Path))] = struct{}{}
	}
	return paths
}

func resetOwnerWatchTimer(timer *time.Timer, delay time.Duration) {
	stopOwnerWatchTimer(timer)
	timer.Reset(delay)
}

func stopOwnerWatchTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func ownerWorkspaceChanges(previous LocalWorkspaceSnapshot, current LocalWorkspaceSnapshot) []proto.InvalidateChange {
	previousEntries := ownerWorkspaceManifestEntries(previous)
	currentEntries := ownerWorkspaceManifestEntries(current)
	removed, added := ownerWorkspacePathDiff(previousEntries, currentEntries)
	renames, removed, added := ownerWorkspaceRenames(previousEntries, currentEntries, removed, added)
	paths := make([]string, 0, len(previousEntries)+len(currentEntries))
	for path := range currentEntries {
		paths = append(paths, path)
	}
	for path := range previousEntries {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	changes := make([]proto.InvalidateChange, 0)
	for _, rename := range renames {
		changes = appendOwnerInvalidateChange(changes,
			proto.InvalidateChange{Path: rename.oldPath, NewPath: rename.newPath, Kind: proto.InvalidateRename},
			proto.InvalidateChange{Path: ownerParentWorkspacePath(rename.oldPath), Kind: proto.InvalidateDentry},
			proto.InvalidateChange{Path: ownerParentWorkspacePath(rename.newPath), Kind: proto.InvalidateDentry},
		)
	}
	for _, path := range ownerUniqueStrings(paths) {
		currentEntry, currentOK := currentEntries[path]
		previousEntry, ok := previousEntries[path]
		if !currentOK {
			if !removed[path] {
				continue
			}
			changes = appendOwnerInvalidateChange(changes,
				proto.InvalidateChange{Path: path, Kind: proto.InvalidateDelete},
				proto.InvalidateChange{Path: ownerParentWorkspacePath(path), Kind: proto.InvalidateDentry},
			)
			continue
		}
		if !ok {
			if !added[path] {
				continue
			}
			changes = appendOwnerInvalidateChange(changes, ownerInvalidateForEntry(path, currentEntry.IsDir)...)
			continue
		}
		if previousEntry.IsDir != currentEntry.IsDir {
			changes = appendOwnerInvalidateChange(changes,
				proto.InvalidateChange{Path: path, Kind: proto.InvalidateDelete},
			)
			changes = appendOwnerInvalidateChange(changes, ownerInvalidateForEntry(path, currentEntry.IsDir)...)
			continue
		}
		if previousEntry.IsDir {
			if previousEntry.Metadata != currentEntry.Metadata {
				changes = appendOwnerInvalidateChange(changes,
					proto.InvalidateChange{Path: path, Kind: proto.InvalidateDentry},
					proto.InvalidateChange{Path: ownerParentWorkspacePath(path), Kind: proto.InvalidateDentry},
				)
			}
			continue
		}
		if previousEntry.Metadata != currentEntry.Metadata || previousEntry.ContentHash != currentEntry.ContentHash {
			changes = appendOwnerInvalidateChange(changes,
				proto.InvalidateChange{Path: path, Kind: proto.InvalidateData},
				proto.InvalidateChange{Path: ownerParentWorkspacePath(path), Kind: proto.InvalidateDentry},
			)
		}
	}
	return changes
}

type ownerWorkspaceRename struct {
	oldPath string
	newPath string
}

func ownerWorkspaceManifestEntries(snapshot LocalWorkspaceSnapshot) map[string]proto.ManifestEntry {
	entries := make(map[string]proto.ManifestEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries[entry.Path] = entry
	}
	return entries
}

func ownerWorkspacePathDiff(previousEntries map[string]proto.ManifestEntry, currentEntries map[string]proto.ManifestEntry) (map[string]bool, map[string]bool) {
	removed := make(map[string]bool)
	added := make(map[string]bool)
	for path := range previousEntries {
		if _, ok := currentEntries[path]; !ok {
			removed[path] = true
		}
	}
	for path := range currentEntries {
		if _, ok := previousEntries[path]; !ok {
			added[path] = true
		}
	}
	return removed, added
}

func ownerWorkspaceRenames(
	previousEntries map[string]proto.ManifestEntry,
	currentEntries map[string]proto.ManifestEntry,
	removed map[string]bool,
	added map[string]bool,
) ([]ownerWorkspaceRename, map[string]bool, map[string]bool) {
	removedBySignature := make(map[string][]string)
	addedBySignature := make(map[string][]string)
	for path := range removed {
		signature, ok := ownerWorkspaceRenameSignature(previousEntries[path])
		if !ok {
			continue
		}
		removedBySignature[signature] = append(removedBySignature[signature], path)
	}
	for path := range added {
		signature, ok := ownerWorkspaceRenameSignature(currentEntries[path])
		if !ok {
			continue
		}
		addedBySignature[signature] = append(addedBySignature[signature], path)
	}

	var renames []ownerWorkspaceRename
	for signature, oldPaths := range removedBySignature {
		newPaths := addedBySignature[signature]
		if len(oldPaths) == 0 || len(newPaths) == 0 {
			continue
		}
		sort.Strings(oldPaths)
		sort.Strings(newPaths)
		count := len(oldPaths)
		if len(newPaths) < count {
			count = len(newPaths)
		}
		for idx := 0; idx < count; idx++ {
			oldPath := oldPaths[idx]
			newPath := newPaths[idx]
			delete(removed, oldPath)
			delete(added, newPath)
			renames = append(renames, ownerWorkspaceRename{
				oldPath: oldPath,
				newPath: newPath,
			})
		}
	}
	sort.Slice(renames, func(left, right int) bool {
		if renames[left].oldPath == renames[right].oldPath {
			return renames[left].newPath < renames[right].newPath
		}
		return renames[left].oldPath < renames[right].oldPath
	})
	return renames, removed, added
}

func ownerWorkspaceRenameSignature(entry proto.ManifestEntry) (string, bool) {
	if entry.Path == "" {
		return "", false
	}
	if entry.IsDir {
		return "dir:" + entry.StatFingerprint + ":" + entry.Metadata.MTime.UTC().Format(time.RFC3339Nano), true
	}
	if entry.ContentHash == "" {
		return "", false
	}
	return "file:" + entry.ContentHash + ":" + entry.StatFingerprint, true
}

func ownerInvalidateForEntry(path string, isDir bool) []proto.InvalidateChange {
	if isDir {
		return []proto.InvalidateChange{
			{Path: path, Kind: proto.InvalidateDentry},
			{Path: ownerParentWorkspacePath(path), Kind: proto.InvalidateDentry},
		}
	}
	return []proto.InvalidateChange{
		{Path: path, Kind: proto.InvalidateData},
		{Path: ownerParentWorkspacePath(path), Kind: proto.InvalidateDentry},
	}
}

func ownerParentWorkspacePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return ""
	}
	index := strings.LastIndex(path, "/")
	if index < 0 {
		return ""
	}
	return path[:index]
}

func appendOwnerInvalidateChange(existing []proto.InvalidateChange, candidates ...proto.InvalidateChange) []proto.InvalidateChange {
	for _, candidate := range candidates {
		duplicate := false
		for _, current := range existing {
			if current.Path == candidate.Path && current.NewPath == candidate.NewPath && current.Kind == candidate.Kind {
				duplicate = true
				break
			}
		}
		if !duplicate {
			existing = append(existing, candidate)
		}
	}
	return existing
}

func ownerUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := values[:1]
	for _, value := range values[1:] {
		if result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result
}
