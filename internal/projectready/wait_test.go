package projectready

import (
	"context"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestWaitUnblocksOnAuthorityStateChanged(t *testing.T) {
	t.Parallel()

	snapshot := proto.ProjectSnapshot{
		ProjectID:      "demo",
		ProjectState:   proto.ProjectStateActive,
		AuthorityState: proto.AuthorityStateRebinding,
		SyncEpoch:      2,
		CurrentCursor:  5,
	}

	updated, err := Wait(context.Background(), snapshot,
		func(_ context.Context, sinceCursor uint64, onEvent func(proto.ProjectEvent) error) error {
			if sinceCursor != 5 {
				t.Fatalf("stream sinceCursor = %d, want 5", sinceCursor)
			}
			return onEvent(proto.ProjectEvent{
				Cursor:                   6,
				Type:                     proto.ProjectEventAuthorityStateChanged,
				Timestamp:                time.Unix(1_700_000_001, 0).UTC(),
				ProjectState:             proto.ProjectStateActive,
				AuthorityState:           proto.AuthorityStateStable,
				OwnerWorkspaceInstanceID: "wsi-owner",
				SyncEpoch:                2,
			})
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !updated.CanStartCommands {
		t.Fatal("CanStartCommands = false, want true after authority stabilizes")
	}
	if updated.AuthorityState != proto.AuthorityStateStable {
		t.Fatalf("AuthorityState = %q, want stable", updated.AuthorityState)
	}
	if updated.OwnerWorkspaceInstanceID != "wsi-owner" {
		t.Fatalf("OwnerWorkspaceInstanceID = %q, want wsi-owner", updated.OwnerWorkspaceInstanceID)
	}
	if updated.CurrentCursor != 6 {
		t.Fatalf("CurrentCursor = %d, want 6", updated.CurrentCursor)
	}
}

func TestApplyProjectEventClearsWarningsWhenConfirmationEnds(t *testing.T) {
	t.Parallel()

	snapshot := proto.ProjectSnapshot{
		ProjectID:      "demo",
		ProjectState:   proto.ProjectStateNeedsConfirmation,
		AuthorityState: proto.AuthorityStateStable,
		SyncEpoch:      1,
		Warnings: []proto.Warning{{
			Code:      proto.WarningSourceDrift,
			Message:   "drift",
			DiffLevel: proto.SourceDiffSevere,
		}},
	}

	updated := ApplyProjectEvent(snapshot, proto.ProjectEvent{
		Cursor:            2,
		Type:              proto.ProjectEventNeedsConfirmation,
		ProjectState:      proto.ProjectStateSyncing,
		NeedsConfirmation: false,
		AuthorityState:    proto.AuthorityStateStable,
		SyncEpoch:         2,
	})
	if updated.NeedsConfirmation {
		t.Fatal("NeedsConfirmation = true, want false")
	}
	if len(updated.Warnings) != 0 {
		t.Fatalf("Warnings = %#v, want cleared warnings", updated.Warnings)
	}
}
