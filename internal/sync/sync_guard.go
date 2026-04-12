package sync

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"symterm/internal/proto"
)

const (
	syncGuardPollInterval     = 2 * time.Second
	syncGuardWatchDebounce    = 75 * time.Millisecond
	syncGuardQuiescenceWindow = 150 * time.Millisecond
)

type SyncGuard struct {
	mu                  sync.Mutex
	root                string
	dirtyEpoch          uint64
	quiescentSince      time.Time
	watchHealthy        bool
	failureReason       string
	lastSnapshot        LocalWorkspaceSnapshot
	localHashCache      map[string]LocalWorkspaceFile
	persistentHashCache *PersistentHashCache
}

type SyncGuardAttempt struct {
	dirtyEpoch uint64
}

func StartSyncGuard(
	ctx context.Context,
	root string,
	initial LocalWorkspaceSnapshot,
	observer *InitialSyncObserver,
) *SyncGuard {
	guard := &SyncGuard{
		root:           root,
		quiescentSince: time.Now().UTC(),
		watchHealthy:   true,
		lastSnapshot:   cloneLocalWorkspaceSnapshot(initial),
		localHashCache: make(map[string]LocalWorkspaceFile),
	}
	if cache, err := loadPersistentHashCache(initial.WorkspaceInstanceID); err == nil {
		guard.persistentHashCache = cache
	} else if observer != nil {
		observer.Operation("Persistent hash cache unavailable; continuing without it")
	}
	go guard.run(ctx, observer)
	return guard
}

func (g *SyncGuard) AttemptStart() SyncGuardAttempt {
	if g == nil {
		return SyncGuardAttempt{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return SyncGuardAttempt{dirtyEpoch: g.dirtyEpoch}
}

func (g *SyncGuard) Validate(attempt SyncGuardAttempt) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.watchHealthy {
		message := "sync guard watcher degraded"
		if strings.TrimSpace(g.failureReason) != "" {
			message += ": " + strings.TrimSpace(g.failureReason)
		}
		return proto.NewError(proto.ErrSyncRescanMismatch, message)
	}
	if g.dirtyEpoch != attempt.dirtyEpoch {
		return proto.NewError(proto.ErrSyncRescanMismatch, "workspace changed during initial sync")
	}
	if time.Since(g.quiescentSince) < syncGuardQuiescenceWindow {
		return proto.NewError(proto.ErrSyncRescanMismatch, "workspace did not remain quiescent through finalize")
	}
	return nil
}

func (g *SyncGuard) RefreshSnapshot() (LocalWorkspaceSnapshot, error) {
	if g == nil {
		return LocalWorkspaceSnapshot{}, nil
	}
	current, err := ScanLocalWorkspace(g.root, nil, false)
	if err != nil {
		return LocalWorkspaceSnapshot{}, err
	}
	g.mu.Lock()
	g.lastSnapshot = cloneLocalWorkspaceSnapshot(current)
	current = g.applyLocalHashCacheLocked(current)
	g.mu.Unlock()
	return current, nil
}

func (g *SyncGuard) HashPaths(snapshot LocalWorkspaceSnapshot, paths []string) (LocalWorkspaceSnapshot, error) {
	if g == nil {
		return HashWorkspaceSnapshotPaths(snapshot, paths)
	}
	g.mu.Lock()
	prepared := g.applyLocalHashCacheLocked(snapshot)
	g.mu.Unlock()

	hashed, err := HashWorkspaceSnapshotPaths(prepared, paths)
	if err != nil {
		return LocalWorkspaceSnapshot{}, err
	}

	g.mu.Lock()
	for path, file := range hashed.HashedFiles {
		g.localHashCache[path] = file
		g.persistentHashCache.Store(file)
	}
	g.mu.Unlock()
	_ = g.persistentHashCache.Save()
	return hashed, nil
}

func (g *SyncGuard) run(ctx context.Context, observer *InitialSyncObserver) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		g.runPolling(ctx, observer)
		return
	}
	defer watcher.Close()

	g.syncFsnotifyWatches(watcher)
	rescanTicker := time.NewTicker(syncGuardPollInterval)
	defer rescanTicker.Stop()
	debounce := time.NewTimer(time.Hour)
	stopOwnerWatchTimer(debounce)
	pendingRescan := false

	scheduleRescan := func() {
		pendingRescan = true
		resetOwnerWatchTimer(debounce, syncGuardWatchDebounce)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-watcher.Events:
			if !ok {
				g.fail("sync guard event stream closed", observer)
				return
			}
			g.markDirty()
			scheduleRescan()
		case err, ok := <-watcher.Errors:
			if !ok {
				g.fail("sync guard error stream closed", observer)
				return
			}
			g.fail(err.Error(), observer)
			scheduleRescan()
		case <-rescanTicker.C:
			scheduleRescan()
		case <-debounce.C:
			if !pendingRescan {
				continue
			}
			pendingRescan = false
			current, err := ScanLocalWorkspace(g.root, nil, false)
			if err != nil {
				g.fail(err.Error(), observer)
				continue
			}
			g.mu.Lock()
			if localWorkspaceMetadataChanged(g.lastSnapshot, current) {
				g.markDirtyLocked()
			}
			g.lastSnapshot = cloneLocalWorkspaceSnapshot(current)
			g.mu.Unlock()
			g.syncFsnotifyWatches(watcher)
		}
	}
}

func (g *SyncGuard) runPolling(ctx context.Context, observer *InitialSyncObserver) {
	if observer != nil {
		observer.Operation("Sync guard fsnotify unavailable; falling back to polling")
	}
	ticker := time.NewTicker(syncGuardPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		current, err := ScanLocalWorkspace(g.root, nil, false)
		if err != nil {
			g.fail(err.Error(), observer)
			continue
		}
		g.mu.Lock()
		if localWorkspaceMetadataChanged(g.lastSnapshot, current) {
			g.markDirtyLocked()
		}
		g.lastSnapshot = cloneLocalWorkspaceSnapshot(current)
		g.mu.Unlock()
	}
}

func (g *SyncGuard) markDirty() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.markDirtyLocked()
}

func (g *SyncGuard) markDirtyLocked() {
	g.dirtyEpoch++
	g.quiescentSince = time.Now().UTC()
}

func (g *SyncGuard) fail(reason string, observer *InitialSyncObserver) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.watchHealthy {
		return
	}
	g.watchHealthy = false
	g.failureReason = strings.TrimSpace(reason)
	if observer != nil {
		message := "Sync guard degraded"
		if g.failureReason != "" {
			message += ": " + g.failureReason
		}
		observer.Operation(message)
	}
}

func (g *SyncGuard) applyLocalHashCacheLocked(snapshot LocalWorkspaceSnapshot) LocalWorkspaceSnapshot {
	prepared := cloneLocalWorkspaceSnapshot(snapshot)
	if prepared.HashedFiles == nil {
		prepared.HashedFiles = make(map[string]LocalWorkspaceFile)
	}
	for path, cached := range g.localHashCache {
		current, ok := prepared.Files[path]
		if !ok {
			continue
		}
		if cached.HashCacheKey() != current.HashCacheKey() || cached.Entry.ContentHash == "" {
			continue
		}
		current.Entry.ContentHash = cached.Entry.ContentHash
		prepared.Files[path] = current
		prepared.HashedFiles[path] = current
	}
	for path, current := range prepared.Files {
		if _, ok := prepared.HashedFiles[path]; ok {
			continue
		}
		hashValue, ok := g.persistentHashCache.Lookup(current)
		if !ok {
			continue
		}
		current.Entry.ContentHash = hashValue
		prepared.Files[path] = current
		prepared.HashedFiles[path] = current
	}
	return prepared
}

func (g *SyncGuard) PersistentHashCacheStats() (uint64, uint64) {
	if g == nil {
		return 0, 0
	}
	return g.persistentHashCache.Stats()
}

func (g *SyncGuard) syncFsnotifyWatches(watcher *fsnotify.Watcher) {
	if g == nil || watcher == nil {
		return
	}
	g.mu.Lock()
	snapshot := cloneLocalWorkspaceSnapshot(g.lastSnapshot)
	g.mu.Unlock()

	desired := map[string]struct{}{
		filepath.Clean(snapshot.Root): {},
	}
	for path := range snapshot.Dirs {
		desired[filepath.Join(snapshot.Root, filepath.FromSlash(path))] = struct{}{}
	}
	paths := make([]string, 0, len(desired))
	for path := range desired {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		_ = watcher.Add(path)
	}
}

func localWorkspaceMetadataChanged(previous LocalWorkspaceSnapshot, current LocalWorkspaceSnapshot) bool {
	if previous.Fingerprint != current.Fingerprint {
		return true
	}
	if len(previous.Files) != len(current.Files) || len(previous.Dirs) != len(current.Dirs) {
		return true
	}
	for path, currentFile := range current.Files {
		previousFile, ok := previous.Files[path]
		if !ok {
			return true
		}
		if previousFile.Entry.Metadata != currentFile.Entry.Metadata {
			return true
		}
		if !previousFile.PreciseMTime.Equal(currentFile.PreciseMTime) {
			return true
		}
	}
	for path, currentDir := range current.Dirs {
		previousDir, ok := previous.Dirs[path]
		if !ok {
			return true
		}
		if previousDir.Metadata != currentDir.Metadata {
			return true
		}
	}
	return false
}

func WaitForSyncGuardQuiescence(ctx context.Context, guard *SyncGuard) error {
	if guard == nil {
		return nil
	}
	timer := time.NewTicker(syncGuardQuiescenceWindow / 3)
	defer timer.Stop()
	for {
		if err := guard.Validate(guard.AttemptStart()); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func closeSyncGuard(stop func()) {
	if stop == nil {
		return
	}
	stop()
}

func isFsnotifyClosedError(err error) bool {
	return errors.Is(err, fsnotify.ErrClosed)
}
