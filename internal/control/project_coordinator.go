package control

import (
	"errors"
	"sync"

	"symterm/internal/ownerfs"
	"symterm/internal/project"
	"symterm/internal/proto"
)

type ProjectCoordinator struct {
	mu        sync.Mutex
	bootstrap ProjectBootstrapper
	instances map[string]*projectEntry
	tracef    func(string, ...any)
}

type projectEntry struct {
	instance *project.Instance
	ready    chan struct{}
	err      error
}

func newProjectCoordinator(bootstrap ProjectBootstrapper, traces ...func(string, ...any)) *ProjectCoordinator {
	var tracef func(string, ...any)
	if len(traces) > 0 {
		tracef = traces[0]
	}
	return &ProjectCoordinator{
		bootstrap: bootstrap,
		instances: make(map[string]*projectEntry),
		tracef:    tracef,
	}
}

func (c *ProjectCoordinator) InstanceForClient(registry *SessionRegistry, clientID string) (*project.Instance, session, error) {
	c.trace("project instance for client begin client_id=%q", clientID)
	clientSession, err := registry.Session(clientID)
	if err != nil {
		c.trace("project instance for client failed client_id=%q error=%v", clientID, err)
		return nil, session{}, err
	}

	instance, err := c.instanceForSession(clientSession)
	if err != nil {
		c.trace("project instance for client failed client_id=%q project=%q error=%v", clientID, clientSession.ProjectID, err)
		return nil, session{}, err
	}
	c.trace("project instance for client ready client_id=%q project=%q", clientID, clientSession.ProjectID)
	return instance, clientSession, nil
}

func (c *ProjectCoordinator) instanceForSession(clientSession session) (*project.Instance, error) {
	key := projectKeyForSession(clientSession)

	for {
		c.mu.Lock()
		entry := c.instances[key.String()]
		if entry != nil {
			if ready := entry.ready; ready != nil {
				c.mu.Unlock()
				<-ready
				if entry.err != nil {
					return nil, c.wrapBootstrapError(key, entry.err)
				}
				if entry.instance != nil {
					c.trace("project instance reuse key=%q", key.String())
					return entry.instance, nil
				}
				continue
			}
			if entry.err != nil {
				delete(c.instances, key.String())
				c.mu.Unlock()
				continue
			}
			c.mu.Unlock()
			c.trace("project instance reuse key=%q", key.String())
			return entry.instance, nil
		}

		c.trace("project instance create key=%q", key.String())
		instance, err := project.NewInstance(key)
		if err != nil {
			c.mu.Unlock()
			c.trace("project instance create failed key=%q error=%v", key.String(), err)
			return nil, err
		}

		entry = &projectEntry{instance: instance}
		if c.bootstrap != nil {
			entry.ready = make(chan struct{})
		}
		c.instances[key.String()] = entry
		c.mu.Unlock()

		if c.bootstrap == nil {
			c.trace("project instance stored key=%q", key.String())
			return instance, nil
		}

		c.trace("project bootstrap begin key=%q", key.String())
		err = c.bootstrap.PrepareProject(key)
		ready := entry.ready
		c.mu.Lock()
		if err != nil {
			entry.instance = nil
			entry.err = err
		}
		entry.ready = nil
		close(ready)
		c.mu.Unlock()
		if err != nil {
			return nil, c.wrapBootstrapError(key, err)
		}
		c.trace("project bootstrap end key=%q", key.String())
		c.trace("project instance stored key=%q", key.String())
		return instance, nil
	}
}

func (c *ProjectCoordinator) trace(format string, args ...any) {
	if c == nil || c.tracef == nil {
		return
	}
	c.tracef(format, args...)
}

func (c *ProjectCoordinator) InstanceForKey(key proto.ProjectKey) (*project.Instance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := c.instances[key.String()]
	if entry == nil || entry.instance == nil {
		return nil, proto.NewError(proto.ErrInvalidArgument, "project instance does not exist")
	}
	return entry.instance, nil
}

func (c *ProjectCoordinator) EnsureProject(
	registry *SessionRegistry,
	filesystem FilesystemBackend,
	clientID string,
	projectID string,
	now Clock,
	reportError func(string, error),
) (proto.ProjectSnapshot, error) {
	clientSession, err := registry.Session(clientID)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if projectID != clientSession.ProjectID {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrInvalidArgument, "project id does not match the authenticated session")
	}

	instance, err := c.instanceForSession(clientSession)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}

	snapshot, err := instance.AttachClientWithSession(
		clientID,
		clientSession.WorkspaceDigest,
		clientSession.WorkspaceRoot,
		clientSession.WorkspaceInstanceID,
		clientSession.SessionKind,
		now(),
	)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	c.assignAuthoritativeSource(registry, filesystem, instance, clientSession, reportError)
	return snapshot, nil
}

func (c *ProjectCoordinator) ConfirmReconcile(
	registry *SessionRegistry,
	filesystem FilesystemBackend,
	clientID string,
	request proto.ConfirmReconcileRequest,
	now Clock,
	reportError func(string, error),
) (proto.ProjectSnapshot, error) {
	instance, clientSession, err := c.InstanceForClient(registry, clientID)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if request.ProjectID != clientSession.ProjectID {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrInvalidArgument, "project id does not match the authenticated session")
	}

	snapshot, err := instance.ConfirmReconcileWithSession(
		clientID,
		request.ExpectedCursor,
		request.WorkspaceDigest,
		clientSession.WorkspaceInstanceID,
		clientSession.SessionKind,
		now(),
	)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	c.assignAuthoritativeSource(registry, filesystem, instance, clientSession, reportError)
	return snapshot, nil
}

func (c *ProjectCoordinator) CompleteInitialSync(registry *SessionRegistry, clientID string, syncEpoch uint64, now Clock) (proto.ProjectSnapshot, error) {
	instance, _, err := c.InstanceForClient(registry, clientID)
	if err != nil {
		return proto.ProjectSnapshot{}, err
	}
	return instance.CompleteInitialSync(clientID, syncEpoch, now())
}

func (c *ProjectCoordinator) ReleaseClient(
	registry *SessionRegistry,
	filesystem FilesystemBackend,
	commands *CommandController,
	uploads *UploadTracker,
	invalidates *InvalidateHub,
	clientID string,
	now Clock,
	reportCleanup func(string, error),
) error {
	release, err := registry.ReleaseClient(clientID)
	if err != nil || !release.Released {
		return err
	}

	projectKey := projectKeyForSession(release.Session)
	instance, ok := c.instanceIfExists(projectKey)
	if !ok {
		if release.OwnerFileClient != nil {
			reportCleanup("close owner file client for "+clientID, release.OwnerFileClient.Close())
		}
		return nil
	}

	removal := instance.RemoveClient(clientID, now())
	if removal.AuthorityRebinding && filesystem != nil {
		reportCleanup("mark authority rebinding for "+projectKey.String(), filesystem.SetAuthorityState(projectKey, proto.AuthorityStateRebinding))
	}
	if removal.RemainingParticipants == 0 {
		instance.Terminate("last project participant disconnected", now())
		c.clearAuthoritativeSource(filesystem, release.Session, reportCleanup)
		if commands != nil {
			reportCleanup("stop project "+projectKey.String()+" after final disconnect", commands.StopProject(projectKey))
		}
		c.cleanupProject(projectKey, commands, uploads, invalidates)
	}
	if release.OwnerFileClient != nil {
		reportCleanup("close owner file client for "+clientID, release.OwnerFileClient.Close())
	}
	return nil
}

func (c *ProjectCoordinator) HandleOwnerFileDisconnect(
	registry *SessionRegistry,
	filesystem FilesystemBackend,
	commands *CommandController,
	clientID string,
	client ownerfs.Client,
	now Clock,
	reportCleanup func(string, error),
) {
	disconnect, err := registry.OwnerFileDisconnected(clientID, client)
	if err != nil || !disconnect.Matched {
		return
	}

	instance, ok := c.instanceIfExists(projectKeyForSession(disconnect.Session))
	if !ok || !sessionUsesOwnerFileAuthority(disconnect.Session) {
		return
	}
	projectKey := projectKeyForSession(disconnect.Session)
	if !instance.EnterAuthorityRebinding(clientID, now()) {
		return
	}
	if filesystem != nil {
		reportCleanup("mark authority rebinding for "+projectKey.String(), filesystem.SetAuthorityState(projectKey, proto.AuthorityStateRebinding))
	}
}

func (c *ProjectCoordinator) TerminateProject(
	registry *SessionRegistry,
	filesystem FilesystemBackend,
	commands *CommandController,
	uploads *UploadTracker,
	invalidates *InvalidateHub,
	key proto.ProjectKey,
	reason string,
	now Clock,
	reportCleanup func(string, error),
) error {
	instance, ok := c.instanceIfExists(key)
	if !ok {
		return nil
	}

	ownerFileClients := registry.RemoveProjectOwnerFileClients(key, "")
	instance.Terminate(reason, now())
	if filesystem != nil {
		reportCleanup("clear authoritative root for "+key.String(), filesystem.ClearAuthoritativeRoot(key))
	}
	for _, client := range ownerFileClients {
		reportCleanup("close owner file client for "+key.String(), client.Close())
	}
	if commands != nil {
		reportCleanup("stop project "+key.String(), commands.StopProject(key))
	}

	if registry.ProjectSessionCount(key) == 0 {
		c.cleanupProject(key, commands, uploads, invalidates)
		return nil
	}
	c.cleanupProjectRuntimeState(key, commands, uploads, invalidates)
	return nil
}

func (c *ProjectCoordinator) assignAuthoritativeSource(
	registry *SessionRegistry,
	filesystem FilesystemBackend,
	instance *project.Instance,
	clientSession session,
	reportError func(string, error),
) {
	if filesystem == nil || instance == nil || !instance.IsActiveAuthorityClient(clientSession.ClientID) {
		return
	}

	projectKey := projectKeyForSession(clientSession)
	if sessionUsesWorkspaceRootAuthority(clientSession) {
		reportError("set authoritative root for "+projectKey.String(), filesystem.SetAuthoritativeRoot(projectKey, clientSession.WorkspaceRoot))
		return
	}

	ownerFileClient := registry.OwnerFileClient(clientSession.ClientID)
	if ownerFileClient != nil {
		reportError("set authoritative client for "+projectKey.String(), filesystem.SetAuthoritativeClient(projectKey, ownerFileClient))
	}
}

func (c *ProjectCoordinator) clearAuthoritativeSource(
	filesystem FilesystemBackend,
	clientSession session,
	reportCleanup func(string, error),
) {
	if filesystem == nil {
		return
	}
	projectKey := projectKeyForSession(clientSession)
	reportCleanup("clear authoritative root for "+projectKey.String(), filesystem.ClearAuthoritativeRoot(projectKey))
}

func (c *ProjectCoordinator) FailInitialSync(
	registry *SessionRegistry,
	filesystem FilesystemBackend,
	commands *CommandController,
	uploads *UploadTracker,
	invalidates *InvalidateHub,
	projectKey proto.ProjectKey,
	instance *project.Instance,
	err error,
	now Clock,
	reportCleanup func(string, error),
) {
	if err == nil || instance == nil || instance.CurrentState() != proto.ProjectStateSyncing || !shouldTerminateInitialSyncError(err) {
		return
	}
	reportCleanup("terminate project "+projectKey.String()+" after initial sync failure", c.TerminateProject(
		registry,
		filesystem,
		commands,
		uploads,
		invalidates,
		projectKey,
		"initial sync failed: "+err.Error(),
		now,
		reportCleanup,
	))
}

func (c *ProjectCoordinator) cleanupProject(
	key proto.ProjectKey,
	commands *CommandController,
	uploads *UploadTracker,
	invalidates *InvalidateHub,
) {
	c.mu.Lock()
	delete(c.instances, key.String())
	c.mu.Unlock()

	c.cleanupProjectRuntimeState(key, commands, uploads, invalidates)
}

func (c *ProjectCoordinator) cleanupProjectRuntimeState(
	key proto.ProjectKey,
	commands *CommandController,
	uploads *UploadTracker,
	invalidates *InvalidateHub,
) {
	if invalidates != nil {
		invalidates.CleanupProject(key)
	}
	if uploads != nil {
		uploads.CleanupProject(key)
	}
	if commands != nil {
		commands.CleanupProject(key)
	}
}

func (c *ProjectCoordinator) instanceIfExists(key proto.ProjectKey) (*project.Instance, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := c.instances[key.String()]
	if entry == nil || entry.instance == nil {
		return nil, false
	}
	return entry.instance, true
}

func (c *ProjectCoordinator) wrapBootstrapError(key proto.ProjectKey, err error) error {
	if err == nil {
		return nil
	}
	c.trace("project bootstrap failed key=%q error=%v", key.String(), err)
	var protoErr *proto.Error
	if errors.As(err, &protoErr) {
		return err
	}
	return proto.NewError(proto.ErrProjectInitFailed, err.Error())
}

func shouldTerminateInitialSyncError(err error) bool {
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) {
		return true
	}
	return protoErr.Code != proto.ErrSyncRescanMismatch
}
