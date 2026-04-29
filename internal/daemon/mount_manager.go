package daemon

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"sync"

	"symterm/internal/proto"
)

type ProjectFilesystem interface {
	FsRead(context.Context, proto.ProjectKey, proto.FsOperation, proto.FsRequest) (proto.FsReply, error)
	FsMutation(context.Context, proto.ProjectKey, proto.FsMutationRequest) (proto.FsReply, error)
	WatchInvalidate(projectKey proto.ProjectKey, sinceCursor uint64) (ProjectInvalidateWatch, error)
}

type ProjectInvalidateWatch struct {
	Events []proto.InvalidateEvent
	Notify <-chan struct{}
	Close  func()
}

type projectMountSession interface {
	WorkDir() string
	Failure() <-chan error
	Stop() error
	Validate() error
}

type MountManagerDependencies struct {
	ProjectFilesystem     ProjectFilesystem
	SessionFailureHandler func(proto.ProjectKey, error)
	SessionStarter        func(proto.ProjectKey, ProjectLayout) (projectMountSession, error)
	Tracef                func(string, ...any)
}

type MountManager struct {
	mu                sync.Mutex
	projectsRoot      string
	allowUnsafeNoFuse bool
	projectFS         ProjectFilesystem
	workdirs          map[string]string
	sessions          map[string]projectMountSession
	sessionFailures   map[string]projectMountSession
	onSessionFailure  func(proto.ProjectKey, error)
	sessionStarter    func(proto.ProjectKey, ProjectLayout) (projectMountSession, error)
	tracef            func(string, ...any)
}

func NewMountManager(projectsRoot string, allowUnsafeNoFuse bool, projectFS ProjectFilesystem) *MountManager {
	return NewMountManagerWithDependencies(projectsRoot, allowUnsafeNoFuse, MountManagerDependencies{
		ProjectFilesystem: projectFS,
	})
}

func NewMountManagerWithDependencies(projectsRoot string, allowUnsafeNoFuse bool, deps MountManagerDependencies) *MountManager {
	return &MountManager{
		projectsRoot:      projectsRoot,
		allowUnsafeNoFuse: allowUnsafeNoFuse,
		projectFS:         deps.ProjectFilesystem,
		workdirs:          make(map[string]string),
		sessions:          make(map[string]projectMountSession),
		sessionFailures:   make(map[string]projectMountSession),
		onSessionFailure:  deps.SessionFailureHandler,
		sessionStarter:    deps.SessionStarter,
		tracef:            deps.Tracef,
	}
}

func (m *MountManager) SetSessionFailureHandler(handler func(proto.ProjectKey, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.onSessionFailure = handler
}

func (m *MountManager) PrepareProject(key proto.ProjectKey) error {
	m.trace("mount prepare project begin key=%q", key.String())
	layout, err := ResolveProjectLayout(m.projectsRoot, key)
	if err != nil {
		m.trace("mount prepare project failed key=%q stage=resolve_layout error=%v", key.String(), err)
		return err
	}
	m.trace("mount prepare project layout key=%q root=%q mount_dir=%q", key.String(), layout.Root, layout.MountDir)
	if err := layout.EnsureNonMountDirectories(); err != nil {
		m.trace("mount prepare project failed key=%q stage=ensure_non_mount_directories error=%v", key.String(), err)
		return err
	}
	m.trace("mount prepare project ensured non-mount directories key=%q", key.String())
	if err := ensureMountDirectoryReady(layout.MountDir); err != nil {
		m.trace("mount prepare project failed key=%q stage=ensure_mount_directory error=%v", key.String(), err)
		return err
	}
	m.trace("mount prepare project ensured mount directory key=%q mount_dir=%q", key.String(), layout.MountDir)

	session, err := m.prepareSession(key, layout)
	if err != nil {
		m.trace("mount prepare project failed key=%q stage=prepare_session error=%v", key.String(), err)
		return err
	}
	if err := validateMountSession(session); err != nil {
		m.trace("mount prepare project failed key=%q stage=validate_session error=%v", key.String(), err)
		if session != nil {
			_ = session.Stop()
		}
		return err
	}

	shouldWatch := false
	m.mu.Lock()
	m.workdirs[key.String()] = session.WorkDir()
	if _, ok := m.sessions[key.String()]; !ok {
		shouldWatch = true
		m.sessionFailures[key.String()] = session
	}
	m.sessions[key.String()] = session
	m.mu.Unlock()
	if shouldWatch {
		m.watchSessionFailure(key, session)
	}
	m.trace("mount prepare project end key=%q workdir=%q", key.String(), session.WorkDir())
	return nil
}

func (m *MountManager) WorkDir(key proto.ProjectKey) (string, error) {
	m.mu.Lock()
	workdir, ok := m.workdirs[key.String()]
	session := m.sessions[key.String()]
	m.mu.Unlock()
	if ok {
		if err := validateMountSession(session); err == nil {
			return workdir, nil
		} else if session != nil {
			m.trace("mount workdir invalid key=%q error=%v", key.String(), err)
			m.invalidateSession(key, session)
		}
	}
	if err := m.PrepareProject(key); err != nil {
		return "", err
	}
	m.mu.Lock()
	workdir = m.workdirs[key.String()]
	session = m.sessions[key.String()]
	m.mu.Unlock()
	if err := validateMountSession(session); err != nil {
		if session != nil {
			m.invalidateSession(key, session)
		}
		return "", err
	}
	return workdir, nil
}

func (m *MountManager) invalidateSession(key proto.ProjectKey, expected projectMountSession) {
	var session projectMountSession
	m.mu.Lock()
	current := m.sessions[key.String()]
	if current == nil || (expected != nil && !mountSessionsMatch(current, expected)) {
		m.mu.Unlock()
		return
	}
	session = current
	delete(m.workdirs, key.String())
	delete(m.sessions, key.String())
	delete(m.sessionFailures, key.String())
	m.mu.Unlock()
	if session != nil {
		_ = session.Stop()
	}
}

func (m *MountManager) StopProject(key proto.ProjectKey) error {
	return m.stopMatchingSessions(func(candidate string) bool {
		return candidate == key.String()
	})
}

func (m *MountManager) StopAll() error {
	return m.stopMatchingSessions(func(string) bool {
		return true
	})
}

func (m *MountManager) stopMatchingSessions(match func(string) bool) error {
	if match == nil {
		return nil
	}

	var sessions []projectMountSession
	m.mu.Lock()
	for key, session := range m.sessions {
		if !match(key) {
			continue
		}
		sessions = append(sessions, session)
		delete(m.workdirs, key)
		delete(m.sessions, key)
		delete(m.sessionFailures, key)
	}
	m.mu.Unlock()

	var errs []error
	var wg sync.WaitGroup
	var errMu sync.Mutex
	for _, session := range sessions {
		if session == nil {
			continue
		}
		wg.Add(1)
		go func(s projectMountSession) {
			defer wg.Done()
			if err := s.Stop(); err != nil {
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
			}
		}(session)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (m *MountManager) prepareSession(key proto.ProjectKey, layout ProjectLayout) (projectMountSession, error) {
	m.mu.Lock()
	existing := m.sessions[key.String()]
	starter := m.sessionStarter
	m.mu.Unlock()
	if existing != nil {
		m.trace("mount prepare session reuse key=%q workdir=%q", key.String(), existing.WorkDir())
		return existing, nil
	}
	if starter != nil {
		m.trace("mount prepare session using custom starter key=%q", key.String())
		return starter(key, layout)
	}

	if runtime.GOOS != "linux" {
		if !m.allowUnsafeNoFuse {
			return nil, proto.NewError(proto.ErrMountFailed, "remote daemon requires linux + FUSE3; set SYMTERMD_ALLOW_UNSAFE_NO_FUSE=1 only for local testing")
		}
		m.trace("mount prepare session using passthrough key=%q reason=non_linux", key.String())
		return preparePassthroughMount(layout)
	}
	if m.allowUnsafeNoFuse {
		m.trace("mount prepare session using passthrough key=%q reason=unsafe_no_fuse", key.String())
		return preparePassthroughMount(layout)
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		m.trace("mount prepare session failed key=%q stage=stat_fuse error=%v", key.String(), err)
		return nil, proto.NewError(proto.ErrMountFailed, "fuse device is unavailable on the remote host")
	}
	if m.projectFS == nil {
		m.trace("mount prepare session failed key=%q stage=project_fs_missing", key.String())
		return nil, proto.NewError(proto.ErrMountFailed, "project filesystem bridge is unavailable for FUSE mount")
	}
	m.trace("mount prepare session using fuse key=%q mount_dir=%q", key.String(), layout.MountDir)
	return startFuseMount(key, layout, m.projectFS)
}

func (m *MountManager) watchSessionFailure(key proto.ProjectKey, session projectMountSession) {
	failures := session.Failure()
	if failures == nil {
		return
	}

	go func() {
		err, ok := <-failures
		if !ok || err == nil {
			return
		}

		m.mu.Lock()
		current := m.sessionFailures[key.String()]
		handler := m.onSessionFailure
		m.mu.Unlock()
		if !mountSessionsMatch(current, session) {
			return
		}
		m.invalidateSession(key, session)
		m.trace("mount session failure key=%q error=%v", key.String(), err)
		if handler != nil {
			handler(key, err)
		}
	}()
}

func validateMountSession(session projectMountSession) error {
	if session == nil {
		return proto.NewError(proto.ErrMountFailed, "mount session is unavailable")
	}
	return session.Validate()
}

func mountSessionsMatch(left projectMountSession, right projectMountSession) bool {
	if left == nil || right == nil {
		return left == right
	}
	leftType := reflect.TypeOf(left)
	rightType := reflect.TypeOf(right)
	if leftType != rightType || !leftType.Comparable() {
		return false
	}
	return left == right
}

func (m *MountManager) trace(format string, args ...any) {
	if m == nil || m.tracef == nil {
		return
	}
	m.tracef(format, args...)
}

func preparePassthroughMount(layout ProjectLayout) (projectMountSession, error) {
	if err := syncPublishedWorkspace(layout); err != nil {
		return nil, err
	}
	return passthroughMountSession{workdir: layout.MountDir}, nil
}

type passthroughMountSession struct {
	workdir string
}

func (p passthroughMountSession) WorkDir() string {
	return p.workdir
}

func (p passthroughMountSession) Failure() <-chan error {
	return nil
}

func (p passthroughMountSession) Stop() error {
	return nil
}

func (p passthroughMountSession) Validate() error {
	info, err := os.Stat(p.workdir)
	if err != nil {
		return proto.NewError(proto.ErrMountFailed, "passthrough workdir is unavailable: "+err.Error())
	}
	if !info.IsDir() {
		return proto.NewError(proto.ErrMountFailed, "passthrough workdir is not a directory")
	}
	return nil
}
