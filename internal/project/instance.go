package project

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"symterm/internal/eventstream"
	"symterm/internal/proto"
)

const defaultEventRetention = 128

type Instance struct {
	mu                       sync.Mutex
	key                      proto.ProjectKey
	state                    proto.ProjectState
	authorityState           proto.AuthorityState
	terminateReason          string
	done                     chan struct{}
	syncEpoch                uint64
	nextCommandID            uint64
	ownerID                  string
	ownerWorkspaceInstanceID string
	warnings                 []proto.Warning
	clients                  map[string]clientState
	commands                 map[string]proto.CommandSnapshot
	eventStream              *eventstream.Store[proto.ProjectEvent]
}

type ClientRemoval struct {
	Removed               bool
	RemovedAuthority      bool
	RemainingParticipants int
	AuthorityRebinding    bool
	AuthorityAbsent       bool
}

type clientState struct {
	ID                  string
	Role                proto.Role
	Digest              proto.WorkspaceDigest
	WorkspaceRoot       string
	WorkspaceInstanceID string
	SessionKind         proto.SessionKind
	JoinedAt            time.Time
	Warnings            []proto.Warning
}

func NewInstance(key proto.ProjectKey) (*Instance, error) {
	if key.Username == "" || key.ProjectID == "" {
		return nil, proto.NewError(proto.ErrInvalidArgument, "project key requires username and project id")
	}
	eventStream, err := newProjectEventStream(defaultEventRetention)
	if err != nil {
		return nil, err
	}

	return &Instance{
		key:            key,
		state:          proto.ProjectStateInitializing,
		authorityState: proto.AuthorityStateAbsent,
		done:           make(chan struct{}),
		clients:        make(map[string]clientState),
		commands:       make(map[string]proto.CommandSnapshot),
		eventStream:    eventStream,
	}, nil
}

func (i *Instance) AttachClient(clientID string, digest proto.WorkspaceDigest, workspaceRoot string, now time.Time) (proto.ProjectSnapshot, error) {
	return i.AttachClientWithSession(clientID, digest, workspaceRoot, "", proto.SessionKindInteractive, now)
}

func (i *Instance) AttachClientWithSession(
	clientID string,
	digest proto.WorkspaceDigest,
	workspaceRoot string,
	workspaceInstanceID string,
	sessionKind proto.SessionKind,
	now time.Time,
) (proto.ProjectSnapshot, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if clientID == "" {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrInvalidArgument, "client id is required")
	}
	if i.state == proto.ProjectStateTerminating || i.state == proto.ProjectStateTerminated {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrProjectTerminated, "project instance has already terminated")
	}

	if _, ok := i.clients[clientID]; !ok {
		role := proto.RoleFollower
		normalizedKind := proto.NormalizeSessionKind(sessionKind)
		workspaceInstanceID = strings.TrimSpace(workspaceInstanceID)
		if normalizedKind == proto.SessionKindAuthority {
			if i.ownerID != "" && i.ownerID != clientID {
				return proto.ProjectSnapshot{}, proto.NewError(proto.ErrConflict, "project already has an active authority session")
			}
			role = proto.RoleOwner
			i.ownerID = clientID
			if workspaceInstanceID != "" {
				i.ownerWorkspaceInstanceID = workspaceInstanceID
			}
			previousAuthorityState := i.authorityState
			i.authorityState = proto.AuthorityStateStable
			if i.state == proto.ProjectStateInitializing {
				i.syncEpoch++
				i.state = proto.ProjectStateSyncing
				i.appendEventLocked(proto.ProjectEvent{
					Type:         proto.ProjectEventOwnerChanged,
					Timestamp:    now,
					OwnerID:      i.ownerID,
					ProjectState: i.state,
					SyncEpoch:    i.syncEpoch,
				})
				i.appendEventLocked(proto.ProjectEvent{
					Type:         proto.ProjectEventSyncStateChanged,
					Timestamp:    now,
					OwnerID:      i.ownerID,
					ProjectState: i.state,
					SyncEpoch:    i.syncEpoch,
				})
			} else if previousAuthorityState != proto.AuthorityStateStable {
				i.appendEventLocked(proto.ProjectEvent{
					Type:           proto.ProjectEventAuthorityStateChanged,
					Timestamp:      now,
					OwnerID:        i.ownerID,
					AuthorityState: proto.AuthorityStateStable,
					ProjectState:   i.state,
					SyncEpoch:      i.syncEpoch,
				})
			}
		}
		i.clients[clientID] = clientState{
			ID:                  clientID,
			Role:                role,
			Digest:              digest,
			WorkspaceRoot:       strings.TrimSpace(workspaceRoot),
			WorkspaceInstanceID: workspaceInstanceID,
			SessionKind:         normalizedKind,
			JoinedAt:            now,
		}
	}

	if !i.isVisibleOwnerLocked(clientID) {
		client := i.clients[clientID]
		diff := i.sourceDiffLevelLocked(clientID, digest, workspaceRoot)
		client.Warnings = nil
		switch diff {
		case proto.SourceDiffMinor:
			client.Warnings = []proto.Warning{i.sourceDriftWarningLocked(clientID, diff)}
		case proto.SourceDiffSevere:
			wasNeedsConfirmation := i.state == proto.ProjectStateNeedsConfirmation
			i.state = proto.ProjectStateNeedsConfirmation
			i.warnings = []proto.Warning{i.sourceDriftWarningLocked(clientID, diff)}
			if !wasNeedsConfirmation {
				i.appendEventLocked(proto.ProjectEvent{
					Type:              proto.ProjectEventNeedsConfirmation,
					Timestamp:         now,
					OwnerID:           i.ownerID,
					ProjectState:      i.state,
					NeedsConfirmation: true,
					SyncEpoch:         i.syncEpoch,
					Warning:           warningPtr(i.warnings[0]),
				})
			}
		}
		i.clients[clientID] = client
	}

	return i.snapshotLocked(clientID)
}

func (i *Instance) CompleteInitialSync(clientID string, syncEpoch uint64, now time.Time) (proto.ProjectSnapshot, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := i.requireOwnerLocked(clientID); err != nil {
		return proto.ProjectSnapshot{}, err
	}
	if syncEpoch != i.syncEpoch {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrSyncEpochMismatch, "sync epoch does not match the active owner epoch")
	}
	if i.state == proto.ProjectStateNeedsConfirmation {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrNeedsConfirmation, "project is locked pending reconcile confirmation")
	}
	if i.state == proto.ProjectStateTerminating || i.state == proto.ProjectStateTerminated {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrProjectTerminated, "project instance has already terminated")
	}

	i.state = proto.ProjectStateActive
	i.authorityState = proto.AuthorityStateStable
	i.appendEventLocked(proto.ProjectEvent{
		Type:         proto.ProjectEventSyncStateChanged,
		Timestamp:    now,
		OwnerID:      i.ownerID,
		ProjectState: i.state,
		SyncEpoch:    i.syncEpoch,
	})

	return i.snapshotLocked(clientID)
}

func (i *Instance) ConfirmReconcile(clientID string, expectedCursor uint64, digest proto.WorkspaceDigest, now time.Time) (proto.ProjectSnapshot, error) {
	return i.ConfirmReconcileWithSession(clientID, expectedCursor, digest, "", proto.SessionKindInteractive, now)
}

func (i *Instance) ConfirmReconcileWithSession(
	clientID string,
	expectedCursor uint64,
	digest proto.WorkspaceDigest,
	workspaceInstanceID string,
	sessionKind proto.SessionKind,
	now time.Time,
) (proto.ProjectSnapshot, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if _, ok := i.clients[clientID]; !ok {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrUnknownClient, "client is not attached")
	}
	if i.state != proto.ProjectStateNeedsConfirmation {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrInvalidArgument, "project is not waiting for reconcile confirmation")
	}
	if proto.NormalizeSessionKind(sessionKind) != proto.SessionKindAuthority {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrPermissionDenied, "reconcile confirmation requires the authority session")
	}
	if expectedCursor != i.eventStream.CurrentCursor() {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrReconcilePrecondition, "expected cursor does not match current project cursor")
	}

	i.ownerID = clientID
	i.ownerWorkspaceInstanceID = strings.TrimSpace(workspaceInstanceID)
	i.authorityState = proto.AuthorityStateStable
	i.syncEpoch++
	i.state = proto.ProjectStateSyncing
	i.warnings = nil

	for id, client := range i.clients {
		client.Role = proto.RoleFollower
		client.Warnings = nil
		if id == clientID {
			client.Role = proto.RoleOwner
			client.Digest = digest
			client.WorkspaceInstanceID = strings.TrimSpace(workspaceInstanceID)
			client.SessionKind = proto.NormalizeSessionKind(sessionKind)
		}
		i.clients[id] = client
	}

	i.appendEventLocked(proto.ProjectEvent{
		Type:         proto.ProjectEventOwnerChanged,
		Timestamp:    now,
		OwnerID:      i.ownerID,
		ProjectState: i.state,
		SyncEpoch:    i.syncEpoch,
	})
	i.appendEventLocked(proto.ProjectEvent{
		Type:              proto.ProjectEventNeedsConfirmation,
		Timestamp:         now,
		OwnerID:           i.ownerID,
		ProjectState:      i.state,
		NeedsConfirmation: false,
		SyncEpoch:         i.syncEpoch,
	})
	i.appendEventLocked(proto.ProjectEvent{
		Type:         proto.ProjectEventSyncStateChanged,
		Timestamp:    now,
		OwnerID:      i.ownerID,
		ProjectState: i.state,
		SyncEpoch:    i.syncEpoch,
	})

	return i.snapshotLocked(clientID)
}

func (i *Instance) StartCommand(clientID string, argvTail []string, tty proto.TTYSpec, tmuxStatus bool, now time.Time) (proto.CommandSnapshot, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if _, ok := i.clients[clientID]; !ok {
		return proto.CommandSnapshot{}, proto.NewError(proto.ErrUnknownClient, "client is not attached")
	}
	if i.state == proto.ProjectStateNeedsConfirmation {
		return proto.CommandSnapshot{}, proto.NewError(proto.ErrNeedsConfirmation, "project is locked pending reconcile confirmation")
	}
	if i.state == proto.ProjectStateTerminating || i.state == proto.ProjectStateTerminated {
		return proto.CommandSnapshot{}, proto.NewError(proto.ErrProjectTerminated, "project instance has already terminated")
	}
	if i.state != proto.ProjectStateActive || !proto.AuthorityReady(i.authorityState) {
		return proto.CommandSnapshot{}, proto.NewError(proto.ErrProjectNotReady, "project cannot start commands before the shared workspace is active")
	}

	i.nextCommandID++
	commandID := fmt.Sprintf("cmd-%04d", i.nextCommandID)
	snapshot := proto.CommandSnapshot{
		CommandID:     commandID,
		ArgvTail:      append([]string(nil), argvTail...),
		TTY:           tty,
		StartedBy:     clientID,
		StartedByRole: i.visibleRoleLocked(clientID),
		TmuxStatus:    tmuxStatus,
		State:         proto.CommandStateRunning,
		StartedAt:     now,
	}
	i.commands[commandID] = snapshot
	i.appendEventLocked(proto.ProjectEvent{
		Type:         proto.ProjectEventCommandStarted,
		Timestamp:    now,
		OwnerID:      i.ownerID,
		ProjectState: i.state,
		SyncEpoch:    i.syncEpoch,
		Command:      commandPtr(snapshot),
	})

	return snapshot, nil
}

func (i *Instance) FinishCommand(commandID string, exitCode int, now time.Time) (proto.CommandSnapshot, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	snapshot, ok := i.commands[commandID]
	if !ok {
		return proto.CommandSnapshot{}, proto.NewError(proto.ErrUnknownCommand, "command is not known to this project")
	}

	snapshot.State = proto.CommandStateExited
	snapshot.ExitCode = intPtr(exitCode)
	snapshot.ExitedAt = timePtr(now)
	i.commands[commandID] = snapshot
	i.appendEventLocked(proto.ProjectEvent{
		Type:         proto.ProjectEventCommandUpdated,
		Timestamp:    now,
		OwnerID:      i.ownerID,
		ProjectState: i.state,
		SyncEpoch:    i.syncEpoch,
		Command:      commandPtr(snapshot),
	})

	return snapshot, nil
}

func (i *Instance) FailCommand(commandID string, reason string, now time.Time) (proto.CommandSnapshot, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	snapshot, ok := i.commands[commandID]
	if !ok {
		return proto.CommandSnapshot{}, proto.NewError(proto.ErrUnknownCommand, "command is not known to this project")
	}

	snapshot.State = proto.CommandStateFailed
	snapshot.FailureReason = reason
	snapshot.ExitedAt = timePtr(now)
	i.commands[commandID] = snapshot
	i.appendEventLocked(proto.ProjectEvent{
		Type:         proto.ProjectEventCommandUpdated,
		Timestamp:    now,
		OwnerID:      i.ownerID,
		ProjectState: i.state,
		SyncEpoch:    i.syncEpoch,
		Command:      commandPtr(snapshot),
	})

	return snapshot, nil
}

func (i *Instance) Snapshot(clientID string) (proto.ProjectSnapshot, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.snapshotLocked(clientID)
}

func (i *Instance) Command(commandID string) (proto.CommandSnapshot, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	command, ok := i.commands[commandID]
	if !ok {
		return proto.CommandSnapshot{}, proto.NewError(proto.ErrUnknownCommand, "command is not known to this project")
	}
	return cloneCommand(command), nil
}

func (i *Instance) EventsSince(since uint64) ([]proto.ProjectEvent, error) {
	return i.eventStream.EventsSince(since)
}

func (i *Instance) SubscribeProjectEvents(since uint64) ([]proto.ProjectEvent, uint64, <-chan struct{}, error) {
	return i.eventStream.Subscribe(since)
}

func (i *Instance) UnsubscribeProjectEvents(watcherID uint64) {
	i.eventStream.Unsubscribe(watcherID)
}

func (i *Instance) SyncState(clientID string) (proto.ProjectState, uint64, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if _, ok := i.clients[clientID]; !ok {
		return "", 0, proto.NewError(proto.ErrUnknownClient, "client is not attached")
	}
	return i.state, i.syncEpoch, nil
}

func (i *Instance) ReportSyncProgress(clientID string, progress proto.SyncProgress, syncEpoch uint64, now time.Time) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := i.requireOwnerLocked(clientID); err != nil {
		return err
	}
	if syncEpoch != i.syncEpoch {
		return proto.NewError(proto.ErrSyncEpochMismatch, "sync epoch does not match the active owner epoch")
	}
	if i.state != proto.ProjectStateSyncing {
		return proto.NewError(proto.ErrProjectNotReady, "project is not performing initial sync")
	}
	normalized, err := normalizeSyncProgress(progress)
	if err != nil {
		return err
	}
	i.appendEventLocked(proto.ProjectEvent{
		Type:         proto.ProjectEventSyncProgress,
		Timestamp:    now,
		OwnerID:      i.ownerID,
		ProjectState: i.state,
		SyncEpoch:    i.syncEpoch,
		SyncProgress: syncProgressPtr(normalized),
	})
	return nil
}

func (i *Instance) ReadOnly(clientID string) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if _, ok := i.clients[clientID]; !ok {
		return false, proto.NewError(proto.ErrUnknownClient, "client is not attached")
	}
	return i.state == proto.ProjectStateNeedsConfirmation ||
		i.state == proto.ProjectStateTerminating ||
		i.state == proto.ProjectStateTerminated ||
		!proto.AuthorityReady(i.authorityState), nil
}

func (i *Instance) RequireAuthority(clientID string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.requireAuthorityLocked(clientID)
}

func (i *Instance) IsActiveAuthorityClient(clientID string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.ownerID != "" && i.ownerID == clientID
}

func (i *Instance) EnterAuthorityRebinding(clientID string, now time.Time) bool {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.ownerID == "" || i.ownerID != clientID {
		return false
	}
	if i.authorityState == proto.AuthorityStateRebinding {
		return true
	}
	i.authorityState = proto.AuthorityStateRebinding
	i.ownerID = ""
	i.appendEventLocked(proto.ProjectEvent{
		Type:           proto.ProjectEventAuthorityStateChanged,
		Timestamp:      now,
		AuthorityState: proto.AuthorityStateRebinding,
		ProjectState:   i.state,
		SyncEpoch:      i.syncEpoch,
	})
	return true
}

func (i *Instance) CurrentState() proto.ProjectState {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.state
}

func (i *Instance) Done() <-chan struct{} {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.done
}

func (i *Instance) BindContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	done := i.Done()
	if done == nil {
		return ctx, cancel
	}
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (i *Instance) Terminate(reason string, now time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.state == proto.ProjectStateTerminated {
		return
	}
	i.terminateReason = reason
	i.authorityState = proto.AuthorityStateAbsent
	i.state = proto.ProjectStateTerminated
	i.appendEventLocked(proto.ProjectEvent{
		Type:         proto.ProjectEventInstanceTerminated,
		Timestamp:    now,
		OwnerID:      i.ownerID,
		ProjectState: i.state,
		SyncEpoch:    i.syncEpoch,
	})
	close(i.done)
}

func (i *Instance) RemoveClient(clientID string, now time.Time) ClientRemoval {
	i.mu.Lock()
	defer i.mu.Unlock()

	client, ok := i.clients[clientID]
	if !ok {
		return ClientRemoval{}
	}
	delete(i.clients, clientID)
	removal := ClientRemoval{
		Removed:               true,
		RemainingParticipants: len(i.clients),
	}
	if client.Role == proto.RoleOwner && i.ownerID == clientID {
		removal.RemovedAuthority = true
		i.ownerID = ""
		if len(i.clients) == 0 {
			i.ownerWorkspaceInstanceID = ""
			i.authorityState = proto.AuthorityStateAbsent
			removal.AuthorityAbsent = true
			return removal
		}
		if i.authorityState != proto.AuthorityStateRebinding {
			i.authorityState = proto.AuthorityStateRebinding
			i.appendEventLocked(proto.ProjectEvent{
				Type:           proto.ProjectEventAuthorityStateChanged,
				Timestamp:      now,
				AuthorityState: proto.AuthorityStateRebinding,
				ProjectState:   i.state,
				SyncEpoch:      i.syncEpoch,
			})
		}
		removal.AuthorityRebinding = true
	}
	return removal
}

func (i *Instance) snapshotLocked(clientID string) (proto.ProjectSnapshot, error) {
	client, ok := i.clients[clientID]
	if !ok {
		return proto.ProjectSnapshot{}, proto.NewError(proto.ErrUnknownClient, "client is not attached")
	}

	commands := make([]proto.CommandSnapshot, 0, len(i.commands))
	for _, command := range i.commands {
		commands = append(commands, cloneCommand(command))
	}
	sort.Slice(commands, func(left, right int) bool {
		if commands[left].StartedAt.Equal(commands[right].StartedAt) {
			return commands[left].CommandID < commands[right].CommandID
		}
		return commands[left].StartedAt.Before(commands[right].StartedAt)
	})

	warnings := append([]proto.Warning(nil), i.warnings...)
	warnings = appendUniqueWarnings(warnings, client.Warnings...)

	return proto.ProjectSnapshot{
		ProjectID:                i.key.ProjectID,
		Role:                     i.visibleRoleLocked(clientID),
		AuthorityState:           proto.NormalizeAuthorityState(i.authorityState),
		OwnerWorkspaceInstanceID: i.ownerWorkspaceInstanceID,
		ProjectState:             i.state,
		CommandSnapshots:         commands,
		CanStartCommands:         i.state == proto.ProjectStateActive && proto.AuthorityReady(i.authorityState),
		SyncEpoch:                i.syncEpoch,
		NeedsConfirmation:        i.state == proto.ProjectStateNeedsConfirmation,
		Warnings:                 warnings,
		CurrentCursor:            i.eventStream.CurrentCursor(),
	}, nil
}

func (i *Instance) requireOwnerLocked(clientID string) error {
	return i.requireAuthorityLocked(clientID)
}

func (i *Instance) requireAuthorityLocked(clientID string) error {
	client, ok := i.clients[clientID]
	if !ok {
		return proto.NewError(proto.ErrUnknownClient, "client is not attached")
	}
	if client.Role != proto.RoleOwner || i.ownerID != clientID {
		return proto.NewError(proto.ErrPermissionDenied, "operation requires the active authority session")
	}
	return nil
}

func (i *Instance) isVisibleOwnerLocked(clientID string) bool {
	return i.visibleRoleLocked(clientID) == proto.RoleOwner
}

func (i *Instance) visibleRoleLocked(clientID string) proto.Role {
	client, ok := i.clients[clientID]
	if !ok {
		return proto.RoleFollower
	}
	if client.Role == proto.RoleOwner {
		return proto.RoleOwner
	}
	if client.WorkspaceInstanceID != "" && client.WorkspaceInstanceID == i.ownerWorkspaceInstanceID {
		return proto.RoleOwner
	}
	return proto.RoleFollower
}

func (i *Instance) appendEventLocked(event proto.ProjectEvent) {
	if event.Type != proto.ProjectEventNeedsConfirmation {
		event.NeedsConfirmation = i.state == proto.ProjectStateNeedsConfirmation
	}
	if event.AuthorityState == "" {
		event.AuthorityState = proto.NormalizeAuthorityState(i.authorityState)
	}
	if event.OwnerWorkspaceInstanceID == "" {
		event.OwnerWorkspaceInstanceID = i.ownerWorkspaceInstanceID
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	i.eventStream.Append(event)
}

func cloneCommand(command proto.CommandSnapshot) proto.CommandSnapshot {
	command.ArgvTail = append([]string(nil), command.ArgvTail...)
	if command.ExitedAt != nil {
		exitedAt := *command.ExitedAt
		command.ExitedAt = &exitedAt
	}
	if command.ExitCode != nil {
		exitCode := *command.ExitCode
		command.ExitCode = &exitCode
	}
	return command
}

func cloneProjectEvent(event proto.ProjectEvent) proto.ProjectEvent {
	if event.Warning != nil {
		warning := *event.Warning
		event.Warning = &warning
	}
	if event.SyncProgress != nil {
		progress := *event.SyncProgress
		if progress.Percent != nil {
			percent := *progress.Percent
			progress.Percent = &percent
		}
		event.SyncProgress = &progress
	}
	if event.Command != nil {
		command := cloneCommand(*event.Command)
		event.Command = &command
	}
	return event
}

func newProjectEventStream(retention int) (*eventstream.Store[proto.ProjectEvent], error) {
	return eventstream.New(retention, eventstream.CursorCodec[proto.ProjectEvent]{
		Name: "project",
		GetCursor: func(event proto.ProjectEvent) uint64 {
			return event.Cursor
		},
		SetCursor: func(event *proto.ProjectEvent, cursor uint64) {
			event.Cursor = cursor
		},
		Clone: cloneProjectEvent,
	})
}

func warningPtr(warning proto.Warning) *proto.Warning {
	copy := warning
	return &copy
}

func appendUniqueWarnings(existing []proto.Warning, candidates ...proto.Warning) []proto.Warning {
	for _, candidate := range candidates {
		duplicate := false
		for _, current := range existing {
			if current == candidate {
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

func (i *Instance) sourceDriftWarningLocked(clientID string, diff proto.SourceDiffLevel) proto.Warning {
	ownerRoot := ""
	if owner, ok := i.clients[i.ownerID]; ok {
		ownerRoot = owner.WorkspaceRoot
	}
	clientRoot := ""
	if client, ok := i.clients[clientID]; ok {
		clientRoot = client.WorkspaceRoot
	}

	var message string
	switch diff {
	case proto.SourceDiffMinor:
		message = "project key is already attached to another local workspace; current attach remains a follower and only the active owner may keep writing"
	case proto.SourceDiffSevere:
		message = "project key is already attached to another local workspace with incompatible content; project is locked until reconcile is confirmed"
	default:
		message = "workspace source drift detected"
	}
	if attachRootsDiffer(ownerRoot, clientRoot) {
		message = fmt.Sprintf("%s (owner=%s current=%s)", message, ownerRoot, clientRoot)
	}
	return proto.Warning{
		Code:      proto.WarningSourceDrift,
		Message:   message,
		DiffLevel: diff,
	}
}

func (i *Instance) sourceDiffLevelLocked(clientID string, digest proto.WorkspaceDigest, workspaceRoot string) proto.SourceDiffLevel {
	if client, ok := i.clients[clientID]; ok && client.Role == proto.RoleOwner && i.ownerID == clientID {
		return proto.SourceDiffNone
	}
	if client, ok := i.clients[clientID]; ok && client.WorkspaceInstanceID != "" && client.WorkspaceInstanceID == i.ownerWorkspaceInstanceID {
		return proto.SourceDiffNone
	}

	owner, ok := i.clients[i.ownerID]
	if !ok {
		return proto.SourceDiffNone
	}
	reusedWorkspaceRoot := attachRootsDiffer(owner.WorkspaceRoot, workspaceRoot)
	if owner.Digest.IsZero() || digest.IsZero() {
		if reusedWorkspaceRoot {
			return proto.SourceDiffMinor
		}
		return proto.SourceDiffNone
	}
	if owner.Digest == digest {
		if reusedWorkspaceRoot {
			return proto.SourceDiffMinor
		}
		return proto.SourceDiffNone
	}
	if ownerSummary, ok := parseWorkspaceDigest(owner.Digest); ok {
		if clientSummary, ok := parseWorkspaceDigest(digest); ok {
			if ownerSummary.Root == clientSummary.Root {
				if reusedWorkspaceRoot {
					return proto.SourceDiffMinor
				}
				return proto.SourceDiffNone
			}
			diff := ownerSummary.Files - clientSummary.Files
			if diff < 0 {
				diff = -diff
			}
			if diff <= 2 {
				return proto.SourceDiffMinor
			}
		}
	}
	return proto.SourceDiffSevere
}

type workspaceDigestSummary struct {
	Files int
	Root  string
}

func parseWorkspaceDigest(digest proto.WorkspaceDigest) (workspaceDigestSummary, bool) {
	if digest.Algorithm != "workspace-sha256" {
		return workspaceDigestSummary{}, false
	}
	var summary workspaceDigestSummary
	var filesSet bool
	var rootSet bool
	for _, part := range strings.Split(digest.Value, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "files":
			if filesSet {
				return workspaceDigestSummary{}, false
			}
			files, err := strconv.Atoi(value)
			if err != nil || files < 0 {
				return workspaceDigestSummary{}, false
			}
			summary.Files = files
			filesSet = true
		case "root":
			if rootSet || value == "" {
				return workspaceDigestSummary{}, false
			}
			summary.Root = value
			rootSet = true
		}
	}
	if !filesSet || !rootSet {
		return workspaceDigestSummary{}, false
	}
	return summary, true
}

func attachRootsDiffer(left string, right string) bool {
	left = normalizeWorkspaceRoot(left)
	right = normalizeWorkspaceRoot(right)
	if left == "" || right == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return !strings.EqualFold(left, right)
	}
	return left != right
}

func normalizeWorkspaceRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	return filepath.Clean(root)
}

func commandPtr(command proto.CommandSnapshot) *proto.CommandSnapshot {
	copy := cloneCommand(command)
	return &copy
}

func syncProgressPtr(progress proto.SyncProgress) *proto.SyncProgress {
	copy := progress
	return &copy
}

func intPtr(value int) *int {
	return &value
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func normalizeSyncProgress(progress proto.SyncProgress) (proto.SyncProgress, error) {
	if strings.TrimSpace(string(progress.Phase)) == "" {
		return proto.SyncProgress{}, proto.NewError(proto.ErrInvalidArgument, "sync progress phase is required")
	}
	if progress.Total > 0 && progress.Completed > progress.Total {
		return proto.SyncProgress{}, proto.NewError(proto.ErrInvalidArgument, "sync progress completed count exceeds total")
	}
	progress.Percent = nil
	if progress.Total > 0 {
		percent := int((progress.Completed*100 + progress.Total/2) / progress.Total)
		progress.Percent = intPtr(percent)
	}
	return progress, nil
}
