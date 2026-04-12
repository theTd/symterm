package project

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestInstanceOwnerLifecycleAndCommandGate(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	snapshot := attachAuthoritySession(t, instance, "client-owner", proto.WorkspaceDigest{
		Algorithm: "sha256",
		Value:     "aaa",
	}, "/workspace/owner", "", now)

	if snapshot.Role != proto.RoleOwner {
		t.Fatalf("Role = %q, want owner", snapshot.Role)
	}
	if snapshot.ProjectState != proto.ProjectStateSyncing {
		t.Fatalf("ProjectState = %q, want syncing", snapshot.ProjectState)
	}
	if snapshot.CanStartCommands {
		t.Fatal("CanStartCommands = true, want false during initial sync")
	}
	if snapshot.SyncEpoch != 1 {
		t.Fatalf("SyncEpoch = %d, want 1", snapshot.SyncEpoch)
	}

	if _, err := instance.StartCommand("client-owner", []string{"pwd"}, proto.TTYSpec{}, false, now); err == nil {
		t.Fatal("StartCommand() succeeded before initial sync completed")
	}

	snapshot, err = instance.CompleteInitialSync("client-owner", snapshot.SyncEpoch, now.Add(time.Second))
	if err != nil {
		t.Fatalf("CompleteInitialSync() error = %v", err)
	}
	if !snapshot.CanStartCommands {
		t.Fatal("CanStartCommands = false, want true after initial sync")
	}

	command, err := instance.StartCommand("client-owner", []string{"pwd"}, proto.TTYSpec{Interactive: true, Columns: 120, Rows: 40}, true, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if command.CommandID == "" {
		t.Fatal("CommandID is empty")
	}
	if command.StartedByRole != proto.RoleOwner {
		t.Fatalf("StartedByRole = %q, want owner", command.StartedByRole)
	}
	if !command.TmuxStatus {
		t.Fatal("TmuxStatus = false, want true")
	}
}

func TestInstanceTracksAuthorityMetadataFromSessionIdentity(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	snapshot, err := instance.AttachClientWithSession(
		"client-owner",
		testWorkspaceDigest(10, "root-a"),
		"/workspace/owner",
		"wsi-owner",
		proto.SessionKindAuthority,
		now,
	)
	if err != nil {
		t.Fatalf("AttachClientWithSession() error = %v", err)
	}
	if snapshot.AuthorityState != proto.AuthorityStateStable {
		t.Fatalf("AuthorityState = %q, want stable", snapshot.AuthorityState)
	}
	if snapshot.OwnerWorkspaceInstanceID != "wsi-owner" {
		t.Fatalf("OwnerWorkspaceInstanceID = %q, want wsi-owner", snapshot.OwnerWorkspaceInstanceID)
	}

	events, err := instance.EventsSince(0)
	if err != nil {
		t.Fatalf("EventsSince() error = %v", err)
	}
	if len(events) == 0 {
		t.Fatal("EventsSince() returned no events")
	}
	first := events[0]
	if first.AuthorityState != proto.AuthorityStateStable {
		t.Fatalf("first event AuthorityState = %q, want stable", first.AuthorityState)
	}
	if first.OwnerWorkspaceInstanceID != "wsi-owner" {
		t.Fatalf("first event OwnerWorkspaceInstanceID = %q, want wsi-owner", first.OwnerWorkspaceInstanceID)
	}
}

func TestInstanceInteractiveSessionDoesNotClaimAuthorityWithoutAuthoritySession(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	snapshot, err := instance.AttachClientWithSession(
		"interactive",
		testWorkspaceDigest(10, "root-a"),
		"/workspace/owner",
		"wsi-owner",
		proto.SessionKindInteractive,
		now,
	)
	if err != nil {
		t.Fatalf("AttachClientWithSession() error = %v", err)
	}
	if snapshot.Role != proto.RoleFollower {
		t.Fatalf("Role = %q, want follower", snapshot.Role)
	}
	if snapshot.ProjectState != proto.ProjectStateInitializing {
		t.Fatalf("ProjectState = %q, want initializing", snapshot.ProjectState)
	}
	if snapshot.AuthorityState != proto.AuthorityStateAbsent {
		t.Fatalf("AuthorityState = %q, want absent", snapshot.AuthorityState)
	}
	if snapshot.SyncEpoch != 0 {
		t.Fatalf("SyncEpoch = %d, want 0", snapshot.SyncEpoch)
	}
	if snapshot.OwnerWorkspaceInstanceID != "" {
		t.Fatalf("OwnerWorkspaceInstanceID = %q, want empty", snapshot.OwnerWorkspaceInstanceID)
	}

	events, err := instance.EventsSince(0)
	if err != nil {
		t.Fatalf("EventsSince() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("EventsSince() = %#v, want no authority events", events)
	}
}

func TestInstanceShowsSameWorkspaceInteractiveClientAsOwner(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	authority, err := instance.AttachClientWithSession(
		"authority",
		testWorkspaceDigest(10, "root-a"),
		"/workspace/owner",
		"wsi-owner",
		proto.SessionKindAuthority,
		now,
	)
	if err != nil {
		t.Fatalf("AttachClientWithSession(authority) error = %v", err)
	}
	if _, err := instance.CompleteInitialSync("authority", authority.SyncEpoch, now.Add(time.Second)); err != nil {
		t.Fatalf("CompleteInitialSync() error = %v", err)
	}

	snapshot, err := instance.AttachClientWithSession(
		"interactive",
		testWorkspaceDigest(10, "root-a"),
		"/workspace/owner",
		"wsi-owner",
		proto.SessionKindInteractive,
		now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatalf("AttachClientWithSession(interactive) error = %v", err)
	}
	if snapshot.Role != proto.RoleOwner {
		t.Fatalf("Role = %q, want owner for same workspace identity", snapshot.Role)
	}
	if !snapshot.CanStartCommands {
		t.Fatal("CanStartCommands = false, want true after authority is active")
	}
}

func TestInstanceAuthorityRebindingEmitsSingleAuthorityStateStream(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	authority := attachAuthoritySession(t, instance, "authority-1", testWorkspaceDigest(10, "root-a"), "/workspace/owner", "wsi-owner", now)
	if _, err := instance.CompleteInitialSync("authority-1", authority.SyncEpoch, now.Add(time.Second)); err != nil {
		t.Fatalf("CompleteInitialSync() error = %v", err)
	}
	if _, err := instance.AttachClientWithSession(
		"interactive",
		testWorkspaceDigest(10, "root-a"),
		"/workspace/owner",
		"wsi-owner",
		proto.SessionKindInteractive,
		now.Add(2*time.Second),
	); err != nil {
		t.Fatalf("AttachClientWithSession(interactive) error = %v", err)
	}

	instance.RemoveClient("authority-1", now.Add(3*time.Second))
	rebound := attachAuthoritySession(t, instance, "authority-2", testWorkspaceDigest(10, "root-a"), "/workspace/owner", "wsi-owner", now.Add(4*time.Second))
	if rebound.AuthorityState != proto.AuthorityStateStable {
		t.Fatalf("AuthorityState = %q, want stable after rebinding", rebound.AuthorityState)
	}

	events, err := instance.EventsSince(0)
	if err != nil {
		t.Fatalf("EventsSince() error = %v", err)
	}

	stableEvents := 0
	rebindingEvents := 0
	for _, event := range events {
		if event.Type != proto.ProjectEventAuthorityStateChanged {
			continue
		}
		switch event.AuthorityState {
		case proto.AuthorityStateStable:
			stableEvents++
		case proto.AuthorityStateRebinding:
			rebindingEvents++
		}
	}
	if stableEvents != 1 {
		t.Fatalf("stable authority event count = %d, want 1; events=%#v", stableEvents, events)
	}
	if rebindingEvents != 1 {
		t.Fatalf("rebinding authority event count = %d, want 1; events=%#v", rebindingEvents, events)
	}
}

func TestInstanceNeedsConfirmationAndReconcile(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	ownerSnapshot := attachAuthoritySession(t, instance, "owner", testWorkspaceDigest(10, "root-a"), "/workspace/owner", "", now)
	if _, err := instance.CompleteInitialSync("owner", ownerSnapshot.SyncEpoch, now.Add(time.Second)); err != nil {
		t.Fatalf("CompleteInitialSync(owner) error = %v", err)
	}

	followerSnapshot, err := instance.AttachClient("follower", proto.WorkspaceDigest{Algorithm: "sha256", Value: "follower"}, "/workspace/follower", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("AttachClient(follower) error = %v", err)
	}

	if !followerSnapshot.NeedsConfirmation {
		t.Fatal("NeedsConfirmation = false, want true")
	}
	if followerSnapshot.CanStartCommands {
		t.Fatal("CanStartCommands = true, want false while confirmation is required")
	}

	if _, err := instance.ConfirmReconcileWithSession("follower", followerSnapshot.CurrentCursor-1, proto.WorkspaceDigest{Algorithm: "sha256", Value: "new"}, "", proto.SessionKindAuthority, now.Add(3*time.Second)); err == nil {
		t.Fatal("ConfirmReconcile() succeeded with stale cursor")
	}

	reconciled, err := instance.ConfirmReconcileWithSession("follower", followerSnapshot.CurrentCursor, proto.WorkspaceDigest{Algorithm: "sha256", Value: "new"}, "", proto.SessionKindAuthority, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("ConfirmReconcile() error = %v", err)
	}
	if reconciled.Role != proto.RoleOwner {
		t.Fatalf("Role = %q, want owner after reconcile", reconciled.Role)
	}
	if reconciled.ProjectState != proto.ProjectStateSyncing {
		t.Fatalf("ProjectState = %q, want syncing after reconcile", reconciled.ProjectState)
	}
	if reconciled.SyncEpoch != 2 {
		t.Fatalf("SyncEpoch = %d, want 2 after reconcile", reconciled.SyncEpoch)
	}
	formerOwner, err := instance.Snapshot("owner")
	if err != nil {
		t.Fatalf("Snapshot(owner) error = %v", err)
	}
	if formerOwner.Role != proto.RoleFollower {
		t.Fatalf("former owner role = %q, want follower", formerOwner.Role)
	}
	if formerOwner.ProjectState != proto.ProjectStateSyncing {
		t.Fatalf("former owner state = %q, want syncing", formerOwner.ProjectState)
	}
	if formerOwner.SyncEpoch != reconciled.SyncEpoch {
		t.Fatalf("former owner sync epoch = %d, want %d", formerOwner.SyncEpoch, reconciled.SyncEpoch)
	}
	if formerOwner.CanStartCommands {
		t.Fatal("former owner can still start commands during reconcile resync")
	}
}

func TestInstanceMinorSourceDriftWarningIsScopedToAttachingClient(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	ownerSnapshot := attachAuthoritySession(t, instance, "owner", testWorkspaceDigest(10, "root-a"), "/workspace/owner", "", now)
	if _, err := instance.CompleteInitialSync("owner", ownerSnapshot.SyncEpoch, now.Add(time.Second)); err != nil {
		t.Fatalf("CompleteInitialSync(owner) error = %v", err)
	}

	driftedSnapshot, err := instance.AttachClient("follower-drifted", testWorkspaceDigest(8, "root-b"), "/workspace/drifted", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("AttachClient(follower-drifted) error = %v", err)
	}
	if driftedSnapshot.ProjectState != proto.ProjectStateActive {
		t.Fatalf("ProjectState = %q, want active", driftedSnapshot.ProjectState)
	}
	if len(driftedSnapshot.Warnings) != 1 || driftedSnapshot.Warnings[0].Code != proto.WarningSourceDrift {
		t.Fatalf("drifted warnings = %#v", driftedSnapshot.Warnings)
	}
	if !strings.Contains(driftedSnapshot.Warnings[0].Message, "/workspace/owner") || !strings.Contains(driftedSnapshot.Warnings[0].Message, "/workspace/drifted") {
		t.Fatalf("drifted warning message = %q", driftedSnapshot.Warnings[0].Message)
	}

	ownerSnapshot, err = instance.Snapshot("owner")
	if err != nil {
		t.Fatalf("Snapshot(owner) error = %v", err)
	}
	if len(ownerSnapshot.Warnings) != 0 {
		t.Fatalf("owner warnings = %#v, want none", ownerSnapshot.Warnings)
	}

	matchingSnapshot, err := instance.AttachClient("follower-matching", testWorkspaceDigest(10, "root-a"), "/workspace/owner", now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("AttachClient(follower-matching) error = %v", err)
	}
	if len(matchingSnapshot.Warnings) != 0 {
		t.Fatalf("matching follower warnings = %#v, want none", matchingSnapshot.Warnings)
	}
}

func TestInstanceConfirmReconcileRequiresNeedsConfirmation(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	ownerSnapshot := attachAuthoritySession(t, instance, "owner", proto.WorkspaceDigest{Algorithm: "sha256", Value: "owner"}, "/workspace/owner", "", now)
	if _, err := instance.CompleteInitialSync("owner", ownerSnapshot.SyncEpoch, now.Add(time.Second)); err != nil {
		t.Fatalf("CompleteInitialSync(owner) error = %v", err)
	}

	_, err = instance.ConfirmReconcile("owner", ownerSnapshot.CurrentCursor, proto.WorkspaceDigest{Algorithm: "sha256", Value: "owner"}, now.Add(2*time.Second))
	if err == nil {
		t.Fatal("ConfirmReconcile() succeeded while project was active")
	}
	protoErr, ok := err.(*proto.Error)
	if !ok {
		t.Fatalf("ConfirmReconcile() error = %T, want *proto.Error", err)
	}
	if protoErr.Code != proto.ErrInvalidArgument {
		t.Fatalf("ConfirmReconcile() error code = %q, want %q", protoErr.Code, proto.ErrInvalidArgument)
	}
}

func TestInstanceNeedsConfirmationEventOnlyOnStateTransition(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	ownerSnapshot := attachAuthoritySession(t, instance, "owner", proto.WorkspaceDigest{Algorithm: "sha256", Value: "owner"}, "/workspace/owner", "", now)
	if _, err := instance.CompleteInitialSync("owner", ownerSnapshot.SyncEpoch, now.Add(time.Second)); err != nil {
		t.Fatalf("CompleteInitialSync(owner) error = %v", err)
	}

	firstSevere, err := instance.AttachClient("follower-1", proto.WorkspaceDigest{Algorithm: "sha256", Value: "other-1"}, "/workspace/follower-1", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("AttachClient(follower-1) error = %v", err)
	}
	cursorAfterFirst := firstSevere.CurrentCursor

	secondSevere, err := instance.AttachClient("follower-2", proto.WorkspaceDigest{Algorithm: "sha256", Value: "other-2"}, "/workspace/follower-2", now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("AttachClient(follower-2) error = %v", err)
	}
	if secondSevere.CurrentCursor != cursorAfterFirst {
		t.Fatalf("CurrentCursor = %d, want %d without duplicate needs-confirmation event", secondSevere.CurrentCursor, cursorAfterFirst)
	}

	events, err := instance.EventsSince(0)
	if err != nil {
		t.Fatalf("EventsSince() error = %v", err)
	}
	needsConfirmationEvents := 0
	for _, event := range events {
		if event.Type == proto.ProjectEventNeedsConfirmation && event.NeedsConfirmation {
			needsConfirmationEvents++
		}
	}
	if needsConfirmationEvents != 1 {
		t.Fatalf("needs-confirmation event count = %d, want 1; events=%#v", needsConfirmationEvents, events)
	}
}

func TestInstanceEventsSinceHonorsCursorWindow(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	snapshot := attachAuthoritySession(t, instance, "owner", proto.WorkspaceDigest{Algorithm: "sha256", Value: "owner"}, "/workspace/owner", "", now)

	events, err := instance.EventsSince(0)
	if err != nil {
		t.Fatalf("EventsSince() error = %v", err)
	}
	if len(events) == 0 {
		t.Fatal("EventsSince() returned no events")
	}

	if _, err := instance.EventsSince(snapshot.CurrentCursor + 10); err == nil {
		t.Fatal("EventsSince() succeeded with cursor beyond current head")
	}
}

func TestInstanceReportSyncProgressValidatesCounts(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	snapshot := attachAuthoritySession(t, instance, "owner", proto.WorkspaceDigest{Algorithm: "sha256", Value: "owner"}, "/workspace/owner", "", now)

	if err := instance.ReportSyncProgress("owner", proto.SyncProgress{
		Phase:     proto.SyncProgressPhaseUploadFiles,
		Completed: 3,
		Total:     2,
	}, snapshot.SyncEpoch, now.Add(time.Second)); err == nil {
		t.Fatal("ReportSyncProgress() succeeded with completed count above total")
	}

	if err := instance.ReportSyncProgress("owner", proto.SyncProgress{
		Phase:     proto.SyncProgressPhaseUploadFiles,
		Completed: 1,
		Total:     2,
	}, snapshot.SyncEpoch, now.Add(2*time.Second)); err != nil {
		t.Fatalf("ReportSyncProgress(valid) error = %v", err)
	}

	events, err := instance.EventsSince(snapshot.CurrentCursor)
	if err != nil {
		t.Fatalf("EventsSince() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != proto.ProjectEventSyncProgress {
		t.Fatalf("progress events = %#v", events)
	}
	if events[0].SyncProgress == nil || events[0].SyncProgress.Percent == nil || *events[0].SyncProgress.Percent != 50 {
		t.Fatalf("sync progress payload = %#v, want percent 50", events[0].SyncProgress)
	}
}

func TestInstanceFailCommandMarksFailure(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	snapshot := attachAuthoritySession(t, instance, "owner", proto.WorkspaceDigest{Algorithm: "sha256", Value: "owner"}, "/workspace/owner", "", now)
	if _, err := instance.CompleteInitialSync("owner", snapshot.SyncEpoch, now.Add(time.Second)); err != nil {
		t.Fatalf("CompleteInitialSync() error = %v", err)
	}

	command, err := instance.StartCommand("owner", []string{"pwd"}, proto.TTYSpec{}, false, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	command, err = instance.FailCommand(command.CommandID, "exec failed", now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("FailCommand() error = %v", err)
	}
	if command.State != proto.CommandStateFailed {
		t.Fatalf("State = %q, want failed", command.State)
	}
	if command.FailureReason != "exec failed" {
		t.Fatalf("FailureReason = %q", command.FailureReason)
	}
}

func TestInstanceAttachClassifiesSourceDiff(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	ownerDigest := testWorkspaceDigest(10, "root-a")

	newAttachedInstance := func(t *testing.T, digest proto.WorkspaceDigest, workspaceRoot string) *Instance {
		t.Helper()

		instance, err := NewInstance(proto.ProjectKey{Username: "alice", ProjectID: "demo"})
		if err != nil {
			t.Fatalf("NewInstance() error = %v", err)
		}
		snapshot := attachAuthoritySession(t, instance, "owner-client", digest, workspaceRoot, "", now)
		if snapshot.Role != proto.RoleOwner {
			t.Fatalf("owner snapshot role = %q, want owner", snapshot.Role)
		}
		return instance
	}

	tests := []struct {
		name     string
		instance func(t *testing.T) *Instance
		clientID string
		root     string
		digest   proto.WorkspaceDigest
		want     proto.SourceDiffLevel
	}{
		{
			name:     "owner client never conflicts with itself",
			instance: func(t *testing.T) *Instance { return newAttachedInstance(t, ownerDigest, "/workspace/owner") },
			clientID: "owner-client",
			root:     "/workspace/owner",
			digest:   testWorkspaceDigest(99, "root-z"),
			want:     proto.SourceDiffNone,
		},
		{
			name: "missing owner digest does not block attach",
			instance: func(t *testing.T) *Instance {
				t.Helper()
				instance, err := NewInstance(proto.ProjectKey{Username: "alice", ProjectID: "demo"})
				if err != nil {
					t.Fatalf("NewInstance() error = %v", err)
				}
				return instance
			},
			clientID: "follower-client",
			root:     "/workspace/follower",
			digest:   ownerDigest,
			want:     proto.SourceDiffNone,
		},
		{
			name:     "missing client digest is treated as unknown rather than drift",
			instance: func(t *testing.T) *Instance { return newAttachedInstance(t, ownerDigest, "/workspace/owner") },
			clientID: "follower-client",
			root:     "/workspace/owner",
			digest:   proto.WorkspaceDigest{},
			want:     proto.SourceDiffNone,
		},
		{
			name:     "exact digest match is equivalent",
			instance: func(t *testing.T) *Instance { return newAttachedInstance(t, ownerDigest, "/workspace/owner") },
			clientID: "follower-client",
			root:     "/workspace/owner",
			digest:   ownerDigest,
			want:     proto.SourceDiffNone,
		},
		{
			name:     "different workspace roots still warn even when digest matches",
			instance: func(t *testing.T) *Instance { return newAttachedInstance(t, ownerDigest, "/workspace/owner") },
			clientID: "follower-client",
			root:     "/workspace/other",
			digest:   ownerDigest,
			want:     proto.SourceDiffMinor,
		},
		{
			name:     "different workspace roots without client digest still warn",
			instance: func(t *testing.T) *Instance { return newAttachedInstance(t, ownerDigest, "/workspace/owner") },
			clientID: "follower-client",
			root:     "/workspace/other",
			digest:   proto.WorkspaceDigest{},
			want:     proto.SourceDiffMinor,
		},
		{
			name:     "matching root fingerprint wins over file-count noise",
			instance: func(t *testing.T) *Instance { return newAttachedInstance(t, ownerDigest, "/workspace/owner") },
			clientID: "follower-client",
			root:     "/workspace/owner",
			digest:   testWorkspaceDigest(12, "root-a"),
			want:     proto.SourceDiffNone,
		},
		{
			name:     "small file-count delta on different roots is minor",
			instance: func(t *testing.T) *Instance { return newAttachedInstance(t, ownerDigest, "/workspace/owner") },
			clientID: "follower-client",
			root:     "/workspace/other",
			digest:   testWorkspaceDigest(8, "root-b"),
			want:     proto.SourceDiffMinor,
		},
		{
			name:     "large file-count delta on different roots is severe",
			instance: func(t *testing.T) *Instance { return newAttachedInstance(t, ownerDigest, "/workspace/owner") },
			clientID: "follower-client",
			root:     "/workspace/other",
			digest:   testWorkspaceDigest(13, "root-b"),
			want:     proto.SourceDiffSevere,
		},
		{
			name:     "malformed structured digest escalates to severe",
			instance: func(t *testing.T) *Instance { return newAttachedInstance(t, ownerDigest, "/workspace/owner") },
			clientID: "follower-client",
			root:     "/workspace/other",
			digest: proto.WorkspaceDigest{
				Algorithm: "workspace-sha256",
				Value:     "root=root-b",
			},
			want: proto.SourceDiffSevere,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.instance(t).sourceDiffLevelLocked(tc.clientID, tc.digest, tc.root)
			if got != tc.want {
				t.Fatalf("sourceDiffLevelLocked() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInstanceMinorAttachWarningDoesNotEmitProjectEvent(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	instance, err := NewInstance(proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}

	ownerSnapshot := attachAuthoritySession(t, instance, "owner", testWorkspaceDigest(10, "root-a"), "/workspace/owner", "", now)
	if _, err := instance.CompleteInitialSync("owner", ownerSnapshot.SyncEpoch, now.Add(time.Second)); err != nil {
		t.Fatalf("CompleteInitialSync(owner) error = %v", err)
	}

	followerSnapshot, err := instance.AttachClient("follower", testWorkspaceDigest(10, "root-a"), "/workspace/other", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("AttachClient(follower) error = %v", err)
	}
	if len(followerSnapshot.Warnings) != 1 || followerSnapshot.Warnings[0].DiffLevel != proto.SourceDiffMinor {
		t.Fatalf("follower warnings = %#v, want one minor warning", followerSnapshot.Warnings)
	}

	events, err := instance.EventsSince(ownerSnapshot.CurrentCursor)
	if err != nil {
		t.Fatalf("EventsSince() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != proto.ProjectEventSyncStateChanged || events[0].ProjectState != proto.ProjectStateActive {
		t.Fatalf("events after owner snapshot = %#v, want only active transition", events)
	}
}

func TestParseWorkspaceDigest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		digest proto.WorkspaceDigest
		want   workspaceDigestSummary
		ok     bool
	}{
		{
			name:   "valid structured digest",
			digest: testWorkspaceDigest(4, "root-a"),
			want:   workspaceDigestSummary{Files: 4, Root: "root-a"},
			ok:     true,
		},
		{
			name: "missing files field is rejected",
			digest: proto.WorkspaceDigest{
				Algorithm: "workspace-sha256",
				Value:     "root=root-a",
			},
			ok: false,
		},
		{
			name: "negative file counts are rejected",
			digest: proto.WorkspaceDigest{
				Algorithm: "workspace-sha256",
				Value:     "files=-1;root=root-a",
			},
			ok: false,
		},
		{
			name: "duplicate fields are rejected",
			digest: proto.WorkspaceDigest{
				Algorithm: "workspace-sha256",
				Value:     "files=1;files=2;root=root-a",
			},
			ok: false,
		},
		{
			name: "unexpected algorithm is rejected",
			digest: proto.WorkspaceDigest{
				Algorithm: "sha256",
				Value:     "files=1;root=root-a",
			},
			ok: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseWorkspaceDigest(tc.digest)
			if ok != tc.ok {
				t.Fatalf("parseWorkspaceDigest() ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("parseWorkspaceDigest() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func testWorkspaceDigest(files int, root string) proto.WorkspaceDigest {
	return proto.WorkspaceDigest{
		Algorithm: "workspace-sha256",
		Value:     fmt.Sprintf("files=%d;root=%s", files, root),
	}
}

func attachAuthoritySession(
	t testing.TB,
	instance *Instance,
	clientID string,
	digest proto.WorkspaceDigest,
	workspaceRoot string,
	workspaceInstanceID string,
	now time.Time,
) proto.ProjectSnapshot {
	t.Helper()

	snapshot, err := instance.AttachClientWithSession(
		clientID,
		digest,
		workspaceRoot,
		workspaceInstanceID,
		proto.SessionKindAuthority,
		now,
	)
	if err != nil {
		t.Fatalf("AttachClientWithSession(%s) error = %v", clientID, err)
	}
	return snapshot
}
