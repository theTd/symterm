package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"symterm/internal/portablefs"
	"symterm/internal/proto"
)

type LocalWorkspaceSnapshot struct {
	WorkspaceInstanceID string
	Root                string
	Fingerprint         string
	ContentFingerprint  string
	Digest              proto.WorkspaceDigest
	Files               map[string]LocalWorkspaceFile
	HashedFiles         map[string]LocalWorkspaceFile
	Dirs                map[string]proto.ManifestEntry
	Entries             []proto.ManifestEntry
}

type LocalWorkspaceFile struct {
	Path         string
	Abs          string
	Entry        proto.ManifestEntry
	PreciseMTime time.Time
}

type LocalFileHashCacheKey struct {
	Path       string
	Size       int64
	Mode       uint32
	MTimeNanos int64
}

type WorkspaceSnapshotScanner struct{}

func (WorkspaceSnapshotScanner) Snapshot(root string, hashPaths map[string]bool, hashAll bool) (LocalWorkspaceSnapshot, error) {
	return ScanLocalWorkspace(root, hashPaths, hashAll)
}

func ScanLocalWorkspace(root string, hashPaths map[string]bool, hashAll bool) (LocalWorkspaceSnapshot, error) {
	rules, err := LoadWorkspaceViewRules(root)
	if err != nil {
		return LocalWorkspaceSnapshot{}, err
	}

	snapshot := LocalWorkspaceSnapshot{
		Root:        root,
		Files:       make(map[string]LocalWorkspaceFile),
		HashedFiles: make(map[string]LocalWorkspaceFile),
		Dirs:        make(map[string]proto.ManifestEntry),
	}
	collisionPaths := make(map[string]string)
	dirCandidates := make(map[string]proto.ManifestEntry)
	visibleDirs := make(map[string]struct{})

	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			fingerprint, fingerprintErr := fingerprintEntries(nil, false)
			if fingerprintErr != nil {
				return LocalWorkspaceSnapshot{}, fingerprintErr
			}
			snapshot.Fingerprint = fingerprint
			snapshot.Digest = proto.WorkspaceDigest{
				Algorithm: "workspace-sha256",
				Value:     fmt.Sprintf("files=0;root=%s", fingerprint),
			}
			return snapshot, nil
		}
		return LocalWorkspaceSnapshot{}, err
	}

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		rel = filepath.ToSlash(rel)
		if err := portablefs.ValidateRelativePath(rel); err != nil {
			return proto.NewError(proto.ErrUnsupportedPath, err.Error())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return proto.NewError(proto.ErrUnsupportedPath, "symbolic links are not supported in the shared workspace")
		}
		collisionKey := portablefs.CollisionKey(rel)
		if previous, ok := collisionPaths[collisionKey]; ok && previous != rel {
			return proto.NewError(proto.ErrUnsupportedPath, "paths that collide after cross-platform normalization are not supported")
		}
		collisionPaths[collisionKey] = rel
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirEntry := proto.ManifestEntry{
				Path:  rel,
				IsDir: true,
				Metadata: proto.FileMetadata{
					Mode:  uint32(info.Mode().Perm()),
					MTime: info.ModTime().UTC().Round(time.Second),
					Size:  0,
				},
				StatFingerprint: fmt.Sprintf("dir:%d:%d", uint32(info.Mode().Perm()), info.ModTime().UTC().Round(time.Second).Unix()),
			}
			dirCandidates[rel] = dirEntry
			if rules.AllowsPath(rel, true) {
				markVisibleWorkspaceAncestors(visibleDirs, rel, true)
			}
			if !rules.AllowsPath(rel, true) && rules.CanPruneDirectory(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return proto.NewError(proto.ErrUnsupportedPath, "only regular files are supported in the shared workspace")
		}

		manifestEntry := proto.ManifestEntry{
			Path: rel,
			Metadata: proto.FileMetadata{
				Mode:  uint32(info.Mode().Perm()),
				MTime: info.ModTime().UTC().Round(time.Second),
				Size:  info.Size(),
			},
			StatFingerprint: fmt.Sprintf("%d:%d:%d", info.Size(), uint32(info.Mode().Perm()), info.ModTime().UTC().Round(time.Second).Unix()),
		}
		if !rules.AllowsPath(rel, false) {
			return nil
		}
		if hashAll || (hashPaths != nil && hashPaths[rel]) {
			hashValue, err := hashLocalFileFn(path)
			if err != nil {
				return err
			}
			manifestEntry.ContentHash = hashValue
		}
		localFile := LocalWorkspaceFile{
			Path:         rel,
			Abs:          path,
			Entry:        manifestEntry,
			PreciseMTime: info.ModTime().UTC(),
		}
		snapshot.Files[rel] = localFile
		if manifestEntry.ContentHash != "" {
			snapshot.HashedFiles[rel] = localFile
		}
		markVisibleWorkspaceAncestors(visibleDirs, rel, false)
		return nil
	})
	if err != nil {
		return LocalWorkspaceSnapshot{}, err
	}

	for path, entry := range dirCandidates {
		if _, ok := visibleDirs[path]; !ok {
			continue
		}
		snapshot.Dirs[path] = entry
	}
	for _, path := range sortedWorkspacePaths(snapshot.Dirs) {
		snapshot.Entries = append(snapshot.Entries, snapshot.Dirs[path])
	}
	for _, path := range sortedWorkspacePaths(snapshot.Files) {
		snapshot.Entries = append(snapshot.Entries, snapshot.Files[path].Entry)
	}

	sort.Slice(snapshot.Entries, func(left, right int) bool {
		return snapshot.Entries[left].Path < snapshot.Entries[right].Path
	})
	fingerprint, err := fingerprintEntries(snapshot.Entries, false)
	if err != nil {
		return LocalWorkspaceSnapshot{}, err
	}
	snapshot.Fingerprint = fingerprint
	if allWorkspaceFilesHashed(snapshot) {
		contentFingerprint, err := fingerprintEntries(snapshot.Entries, true)
		if err != nil {
			return LocalWorkspaceSnapshot{}, err
		}
		snapshot.ContentFingerprint = contentFingerprint
	}
	snapshot.Digest = proto.WorkspaceDigest{
		Algorithm: "workspace-sha256",
		Value:     fmt.Sprintf("files=%d;root=%s", len(snapshot.Entries), fingerprint),
	}
	return snapshot, nil
}

func HashWorkspaceSnapshotPaths(snapshot LocalWorkspaceSnapshot, paths []string) (LocalWorkspaceSnapshot, error) {
	return hashWorkspaceSnapshot(snapshot, pathSet(paths), false, true)
}

func CompleteWorkspaceSnapshotContent(snapshot LocalWorkspaceSnapshot, allowCacheReuse bool) (LocalWorkspaceSnapshot, error) {
	return hashWorkspaceSnapshot(snapshot, nil, true, allowCacheReuse)
}

func hashWorkspaceSnapshot(snapshot LocalWorkspaceSnapshot, selected map[string]bool, hashAll bool, allowCacheReuse bool) (LocalWorkspaceSnapshot, error) {
	result := cloneLocalWorkspaceSnapshot(snapshot)
	if result.HashedFiles == nil {
		result.HashedFiles = make(map[string]LocalWorkspaceFile)
	}

	updatedEntries := make(map[string]proto.ManifestEntry, len(result.Files)+len(result.Dirs))
	for path, entry := range result.Dirs {
		updatedEntries[path] = entry
	}

	hashTargets := make([]LocalWorkspaceFile, 0, len(result.Files))
	for path, localFile := range result.Files {
		shouldHash := hashAll
		if !shouldHash && selected != nil {
			shouldHash = selected[path]
		}
		if !shouldHash {
			updatedEntries[path] = localFile.Entry
			continue
		}
		if allowCacheReuse {
			if cached, ok := result.HashedFiles[path]; ok && cached.HashCacheKey() == localFile.HashCacheKey() && cached.Entry.ContentHash != "" {
				localFile.Entry.ContentHash = cached.Entry.ContentHash
				result.Files[path] = localFile
				result.HashedFiles[path] = localFile
				updatedEntries[path] = localFile.Entry
				continue
			}
		}
		hashTargets = append(hashTargets, localFile)
	}

	hashedFiles, err := hashWorkspaceFilesParallel(hashTargets)
	if err != nil {
		return LocalWorkspaceSnapshot{}, err
	}
	for _, localFile := range hashedFiles {
		result.Files[localFile.Path] = localFile
		result.HashedFiles[localFile.Path] = localFile
		updatedEntries[localFile.Path] = localFile.Entry
	}

	result.Entries = make([]proto.ManifestEntry, 0, len(updatedEntries))
	for _, path := range sortedWorkspacePaths(updatedEntries) {
		result.Entries = append(result.Entries, updatedEntries[path])
	}
	contentFingerprint, err := fingerprintEntries(result.Entries, true)
	if err != nil {
		return LocalWorkspaceSnapshot{}, err
	}
	if hashAll {
		result.ContentFingerprint = contentFingerprint
	}
	return result, nil
}

func hashWorkspaceFilesParallel(files []LocalWorkspaceFile) ([]LocalWorkspaceFile, error) {
	if len(files) == 0 {
		return nil, nil
	}
	workerCount := runtime.NumCPU()
	if workerCount < 2 {
		workerCount = 2
	}
	if workerCount > 8 {
		workerCount = 8
	}
	if workerCount > len(files) {
		workerCount = len(files)
	}

	results := make([]LocalWorkspaceFile, len(files))
	indexCh := make(chan int, len(files))
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range indexCh {
				localFile := files[idx]
				hashValue, err := hashLocalFileFn(localFile.Abs)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				localFile.Entry.ContentHash = hashValue
				results[idx] = localFile
			}
		}()
	}
	for idx := range files {
		select {
		case err := <-errCh:
			close(indexCh)
			wg.Wait()
			return nil, err
		default:
		}
		indexCh <- idx
	}
	close(indexCh)
	wg.Wait()
	select {
	case err := <-errCh:
		return nil, err
	default:
	}
	return results, nil
}

func ApplyPersistentHashCache(snapshot LocalWorkspaceSnapshot, cache *PersistentHashCache) LocalWorkspaceSnapshot {
	if cache == nil {
		return snapshot
	}
	result := cloneLocalWorkspaceSnapshot(snapshot)
	if result.HashedFiles == nil {
		result.HashedFiles = make(map[string]LocalWorkspaceFile)
	}

	for path, file := range result.Files {
		if file.Entry.ContentHash != "" {
			continue
		}
		if hashValue, ok := cache.Lookup(file); ok {
			file.Entry.ContentHash = hashValue
			result.Files[path] = file
			result.HashedFiles[path] = file
		}
	}

	updatedEntries := make(map[string]proto.ManifestEntry, len(result.Files)+len(result.Dirs))
	for path, entry := range result.Dirs {
		updatedEntries[path] = entry
	}
	for path, file := range result.Files {
		updatedEntries[path] = file.Entry
	}
	result.Entries = make([]proto.ManifestEntry, 0, len(updatedEntries))
	for _, path := range sortedWorkspacePaths(updatedEntries) {
		result.Entries = append(result.Entries, updatedEntries[path])
	}

	if allWorkspaceFilesHashed(result) {
		contentFingerprint, err := fingerprintEntries(result.Entries, true)
		if err == nil {
			result.ContentFingerprint = contentFingerprint
		}
	}

	return result
}

func cloneLocalWorkspaceSnapshot(snapshot LocalWorkspaceSnapshot) LocalWorkspaceSnapshot {
	cloned := LocalWorkspaceSnapshot{
		WorkspaceInstanceID: snapshot.WorkspaceInstanceID,
		Root:                snapshot.Root,
		Fingerprint:         snapshot.Fingerprint,
		ContentFingerprint:  snapshot.ContentFingerprint,
		Digest:              snapshot.Digest,
		Files:               make(map[string]LocalWorkspaceFile, len(snapshot.Files)),
		HashedFiles:         make(map[string]LocalWorkspaceFile, len(snapshot.HashedFiles)),
		Dirs:                make(map[string]proto.ManifestEntry, len(snapshot.Dirs)),
		Entries:             append([]proto.ManifestEntry(nil), snapshot.Entries...),
	}
	for path, file := range snapshot.Files {
		cloned.Files[path] = file
	}
	for path, file := range snapshot.HashedFiles {
		cloned.HashedFiles[path] = file
	}
	for path, entry := range snapshot.Dirs {
		cloned.Dirs[path] = entry
	}
	return cloned
}

func markVisibleWorkspaceAncestors(visible map[string]struct{}, path string, includeSelf bool) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" || path == "." {
		return
	}
	if !includeSelf {
		path = parentWorkspacePath(path)
	}
	for path != "" && path != "." {
		visible[path] = struct{}{}
		path = parentWorkspacePath(path)
	}
}

func parentWorkspacePath(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" || path == "." {
		return ""
	}
	index := strings.LastIndex(path, "/")
	if index < 0 {
		return ""
	}
	return path[:index]
}

func sortedWorkspacePaths[T any](values map[string]T) []string {
	if len(values) == 0 {
		return nil
	}
	paths := make([]string, 0, len(values))
	for path := range values {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func allWorkspaceFilesHashed(snapshot LocalWorkspaceSnapshot) bool {
	for _, localFile := range snapshot.Files {
		if strings.TrimSpace(localFile.Entry.ContentHash) == "" {
			return false
		}
	}
	return true
}

func hashLocalFile(path string) (string, error) {
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

var hashLocalFileFn = hashLocalFile

func (f LocalWorkspaceFile) HashCacheKey() LocalFileHashCacheKey {
	return LocalFileHashCacheKey{
		Path:       f.Path,
		Size:       f.Entry.Metadata.Size,
		Mode:       f.Entry.Metadata.Mode,
		MTimeNanos: f.PreciseMTime.UTC().UnixNano(),
	}
}

func fingerprintEntries(entries []proto.ManifestEntry, includeContentHash bool) (string, error) {
	hasher := sha256.New()
	for _, entry := range entries {
		contentHash := ""
		if includeContentHash {
			contentHash = entry.ContentHash
		}
		line := fmt.Sprintf(
			"%s|%t|%d|%d|%d|%s|%s\n",
			entry.Path,
			entry.IsDir,
			entry.Metadata.Size,
			entry.Metadata.Mode,
			entry.Metadata.MTime.UTC().Round(time.Second).Unix(),
			entry.StatFingerprint,
			contentHash,
		)
		if _, err := io.WriteString(hasher, line); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
