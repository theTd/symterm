package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"symterm/internal/invalidation"
	"symterm/internal/ownerfs"
	"symterm/internal/proto"
	workspacesync "symterm/internal/sync"
)

func (m *WorkspaceManager) EnterConservativeMode(projectKey proto.ProjectKey, reason string) ([]proto.InvalidateChange, error) {
	return m.EnterConservativeModeContext(context.Background(), projectKey, reason)
}

func (m *WorkspaceManager) EnterConservativeModeContext(ctx context.Context, projectKey proto.ProjectKey, _ string) ([]proto.InvalidateChange, error) {
	layout, err := ResolveProjectLayout(m.projectsRoot, projectKey)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	state := m.stateLocked(projectKey)
	state.validation.conservative = true
	observedPaths := state.trackedPathsLocked()

	paths := make([]string, 0, len(observedPaths))
	for _, path := range observedPaths {
		if !isRootWorkspacePath(path) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)

	changes := make([]proto.InvalidateChange, 0, len(paths)*2)
	for _, path := range uniqueStrings(paths) {
		changes = invalidation.AppendChanges(changes,
			proto.InvalidateChange{Path: path, Kind: proto.InvalidateDentry},
			proto.InvalidateChange{Path: parentPath(path), Kind: proto.InvalidateDentry},
		)
	}
	m.mu.Unlock()

	if err := m.seedConservativePaths(ctx, projectKey, layout, observedPaths); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return changes, nil
	} else if err != nil {
		return nil, err
	}
	return changes, nil
}

func (m *WorkspaceManager) ApplyOwnerInvalidations(projectKey proto.ProjectKey, changes []proto.InvalidateChange) error {
	return m.ApplyOwnerInvalidationsContext(context.Background(), projectKey, changes)
}

func (m *WorkspaceManager) ApplyOwnerInvalidationsContext(ctx context.Context, projectKey proto.ProjectKey, changes []proto.InvalidateChange) error {
	if len(changes) == 0 {
		return nil
	}

	layout, err := ResolveProjectLayout(m.projectsRoot, projectKey)
	if err != nil {
		return err
	}
	observePaths := make([]string, 0, len(changes)*2)
	m.mu.Lock()
	state := m.stateLocked(projectKey)
	conservative := state.validation.conservative
	for _, change := range changes {
		if conservative {
			continue
		}
		switch change.Kind {
		case proto.InvalidateData:
			state.bumpOwnerDataLocked(change.Path)
			state.clearConservativePathsLocked(change.Path)
			observePaths = append(observePaths, change.Path, parentPath(change.Path))
		case proto.InvalidateDelete:
			state.bumpOwnerDeleteLocked(change.Path)
			state.clearConservativePathsLocked(change.Path, parentPath(change.Path))
			observePaths = append(observePaths, parentPath(change.Path))
		case proto.InvalidateRename:
			state.bumpOwnerRenameLocked(change.Path, change.NewPath)
			state.clearConservativePathsLocked(change.Path, change.NewPath, parentPath(change.Path), parentPath(change.NewPath))
			observePaths = append(observePaths, change.Path, change.NewPath, parentPath(change.Path), parentPath(change.NewPath))
		case proto.InvalidateDentry:
			state.bumpOwnerDentryLocked(change.Path)
			state.clearConservativePathsLocked(change.Path)
			observePaths = append(observePaths, change.Path)
		}
	}
	m.mu.Unlock()
	if conservative {
		return nil
	}
	if err := m.seedConservativePaths(ctx, projectKey, layout, observePaths); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return nil
	} else if err != nil {
		return err
	}
	return nil
}

func (m *WorkspaceManager) seedConservativePaths(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, paths []string) error {
	observations, err := m.collectConservativeObservations(ctx, projectKey, layout, paths)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.stateLocked(projectKey)
	if !state.validation.conservative {
		return nil
	}
	for _, observation := range observations {
		state.setConservativeObservationLocked(observation)
	}
	return nil
}

func (m *WorkspaceManager) revalidateConservativePaths(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, paths []string) error {
	observations, err := m.collectConservativeObservations(ctx, projectKey, layout, paths)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.stateLocked(projectKey)
	if !state.validation.conservative {
		return nil
	}
	for _, observation := range observations {
		state.validateConservativeObservationLocked(observation)
	}
	return nil
}

func (m *WorkspaceManager) collectConservativeObservations(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, paths []string) ([]conservativeObservation, error) {
	m.mu.Lock()
	state := m.stateLocked(projectKey)
	if !state.validation.conservative {
		m.mu.Unlock()
		return nil, nil
	}
	m.mu.Unlock()

	authority := m.authorityForProject(projectKey)
	uniquePaths := uniqueWorkspacePaths(paths)
	observations := make([]conservativeObservation, 0, len(uniquePaths))
	for _, path := range uniquePaths {
		observation, err := m.observeConservativePath(ctx, authority, layout, path)
		if err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

func (m *WorkspaceManager) observeConservativePath(ctx context.Context, authority workspaceAuthority, layout ProjectLayout, workspacePath string) (conservativeObservation, error) {
	if authority.client != nil {
		return observeConservativePathViaClient(ctx, authority.client, workspacePath)
	}
	return m.observeConservativePathOnDisk(authority, layout, workspacePath)
}

func (m *WorkspaceManager) observeConservativePathOnDisk(authority workspaceAuthority, layout ProjectLayout, workspacePath string) (conservativeObservation, error) {
	var rules *workspacesync.WorkspaceViewRules
	var err error
	if strings.TrimSpace(authority.root) != "" {
		rules, err = workspacesync.LoadWorkspaceViewRules(authority.root)
		if err != nil {
			return conservativeObservation{}, err
		}
		if workspacePath != "" {
			visible, err := workspacesync.PathVisibleOnDisk(authority.root, rules, workspacePath)
			if err != nil {
				return conservativeObservation{}, err
			}
			if !visible {
				return conservativeObservation{
					path:              workspacePath,
					objectFingerprint: conservativeMissingFingerprint,
					directoryView:     conservativeNotDirectoryFingerprint,
				}, nil
			}
		}
	}
	target := m.authoritativePath(authority, layout, workspacePath)
	info, err := os.Stat(target)
	if err != nil {
		if isMissingPathError(err) {
			return conservativeObservation{
				path:              workspacePath,
				objectFingerprint: conservativeMissingFingerprint,
				directoryView:     conservativeNotDirectoryFingerprint,
			}, nil
		}
		return conservativeObservation{}, err
	}

	metadata := fileInfoMetadata(info)
	if info.IsDir() {
		var names []string
		if rules != nil {
			names, err = workspacesync.VisibleDirectoryNamesOnDisk(authority.root, rules, workspacePath)
			if err != nil {
				return conservativeObservation{}, err
			}
		} else {
			names, err = readDirectoryNames(target)
			if err != nil {
				return conservativeObservation{}, err
			}
		}
		return conservativeObservation{
			path:              workspacePath,
			objectFingerprint: conservativeDirectoryObjectFingerprint(metadata),
			directoryView:     conservativeDirectoryViewFingerprint(names),
		}, nil
	}

	contentHash, err := hashFile(target)
	if err != nil {
		return conservativeObservation{}, err
	}
	return conservativeObservation{
		path:              workspacePath,
		objectFingerprint: conservativeFileFingerprint(metadata, contentHash),
		directoryView:     conservativeNotDirectoryFingerprint,
	}, nil
}

func observeConservativePathViaClient(ctx context.Context, client ownerfs.Client, workspacePath string) (conservativeObservation, error) {
	reply, err := client.FsRead(ctx, proto.FsOpGetAttr, proto.FsRequest{Path: workspacePath})
	if err != nil {
		if isMissingPathError(err) {
			return conservativeObservation{
				path:              workspacePath,
				objectFingerprint: conservativeMissingFingerprint,
				directoryView:     conservativeNotDirectoryFingerprint,
			}, nil
		}
		return conservativeObservation{}, err
	}

	metadata := normalizeMetadata(reply.Metadata)
	if reply.IsDir {
		dirReply, err := client.FsRead(ctx, proto.FsOpReadDir, proto.FsRequest{Path: workspacePath})
		if err != nil {
			return conservativeObservation{}, err
		}
		return conservativeObservation{
			path:              workspacePath,
			objectFingerprint: conservativeDirectoryObjectFingerprint(metadata),
			directoryView:     conservativeDirectoryViewFingerprint(splitDirectoryListing(dirReply.Data)),
		}, nil
	}

	contentHash, err := hashClientFile(ctx, client, workspacePath)
	if err != nil {
		return conservativeObservation{}, err
	}
	return conservativeObservation{
		path:              workspacePath,
		objectFingerprint: conservativeFileFingerprint(metadata, contentHash),
		directoryView:     conservativeNotDirectoryFingerprint,
	}, nil
}

func hashClientFile(ctx context.Context, client ownerfs.Client, workspacePath string) (string, error) {
	hasher := sha256.New()
	offset := int64(0)
	for {
		reply, err := client.FsRead(ctx, proto.FsOpRead, proto.FsRequest{
			Path:   workspacePath,
			Offset: offset,
			Size:   64 * 1024,
		})
		if err != nil {
			return "", err
		}
		if len(reply.Data) == 0 {
			break
		}
		if _, err := hasher.Write(reply.Data); err != nil {
			return "", err
		}
		offset += int64(len(reply.Data))
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func readDirectoryNames(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func splitDirectoryListing(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	parts := strings.Split(strings.TrimSpace(string(data)), "\n")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			names = append(names, part)
		}
	}
	return names
}

func conservativePathsForAccess(path string) []string {
	return uniqueWorkspacePaths([]string{path, parentPath(path)})
}

func conservativePathsForMutation(request proto.FsRequest, preconditions []proto.MutationPrecondition) []string {
	paths := []string{request.Path, parentPath(request.Path)}
	if request.NewPath != "" {
		paths = append(paths, request.NewPath, parentPath(request.NewPath))
	}
	for _, condition := range preconditions {
		target := condition.Path
		if target == "" {
			target = request.Path
		}
		paths = append(paths, target, parentPath(target))
	}
	return uniqueWorkspacePaths(paths)
}

func trackedPathsForManifest(entries map[string]proto.ManifestEntry) []string {
	paths := make([]string, 0, len(entries)*2+1)
	paths = append(paths, "")
	for path := range entries {
		paths = append(paths, path, parentPath(path))
	}
	return uniqueWorkspacePaths(paths)
}

func uniqueWorkspacePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.ToSlash(strings.TrimSpace(path))
		if clean == "." {
			clean = ""
		}
		normalized = append(normalized, clean)
	}
	sort.Strings(normalized)
	return uniqueStrings(normalized)
}

const (
	conservativeMissingFingerprint      = "missing"
	conservativeNotDirectoryFingerprint = "not-dir"
)

func conservativeFileFingerprint(metadata proto.FileMetadata, contentHash string) string {
	metadata = normalizeMetadata(metadata)
	return fmt.Sprintf("file|%d|%d|%s|%s", metadata.Mode, metadata.Size, metadata.MTime.UTC().Format(time.RFC3339Nano), contentHash)
}

func conservativeDirectoryObjectFingerprint(metadata proto.FileMetadata) string {
	metadata = normalizeMetadata(metadata)
	return fmt.Sprintf("dir|%d|%d|%s", metadata.Mode, metadata.Size, metadata.MTime.UTC().Format(time.RFC3339Nano))
}

func conservativeDirectoryViewFingerprint(names []string) string {
	return "dirview|" + strings.Join(names, "\n")
}

func isMissingFingerprint(fingerprint string) bool {
	return fingerprint == conservativeMissingFingerprint
}

func isMissingPathError(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var protoErr *proto.Error
	return errors.As(err, &protoErr) && protoErr.Code == proto.ErrUnknownFile
}

func (s *workspaceState) trackedPathsLocked() []string {
	paths := []string{""}
	for path := range s.committed.objectGen {
		paths = append(paths, path, parentPath(path))
	}
	for path := range s.committed.dirGen {
		paths = append(paths, path)
	}
	for path := range s.committed.objectIdentity {
		paths = append(paths, path, parentPath(path))
	}
	return uniqueWorkspacePaths(paths)
}

func (s *workspaceState) clearConservativePathsLocked(paths ...string) {
	for _, path := range uniqueWorkspacePaths(paths) {
		delete(s.validation.objectState, path)
		delete(s.validation.directoryView, path)
	}
}

func (s *workspaceState) setConservativeObservationLocked(observation conservativeObservation) {
	s.validation.objectState[observation.path] = observation.objectFingerprint
	s.validation.directoryView[observation.path] = observation.directoryView
}

func (s *workspaceState) validateConservativeObservationLocked(observation conservativeObservation) {
	previousObject, hasPreviousObject := s.validation.objectState[observation.path]
	if hasPreviousObject {
		if previousObject != observation.objectFingerprint {
			if isMissingFingerprint(observation.objectFingerprint) {
				s.bumpOwnerDeleteLocked(observation.path)
			} else {
				s.bumpOwnerDataLocked(observation.path)
			}
		}
	} else if s.committedPathExistsLocked(observation.path) && isMissingFingerprint(observation.objectFingerprint) {
		s.bumpOwnerDeleteLocked(observation.path)
	} else if !s.committedPathExistsLocked(observation.path) && !isMissingFingerprint(observation.objectFingerprint) {
		s.bumpOwnerDataLocked(observation.path)
	}
	s.validation.objectState[observation.path] = observation.objectFingerprint

	previousDirectory, hasPreviousDirectory := s.validation.directoryView[observation.path]
	if hasPreviousDirectory && previousDirectory != observation.directoryView {
		s.bumpOwnerDentryLocked(observation.path)
	}
	s.validation.directoryView[observation.path] = observation.directoryView
}

func (s *workspaceState) committedPathExistsLocked(path string) bool {
	if isRootWorkspacePath(path) {
		return true
	}
	return s.committed.objectIdentity[path] != ""
}
