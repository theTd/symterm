package projectready

import (
	"context"
	"errors"
	"io"

	"symterm/internal/proto"
)

type StreamFunc func(context.Context, uint64, func(proto.ProjectEvent) error) error
type RefreshFunc func(context.Context) (proto.ProjectSnapshot, error)

func Wait(
	ctx context.Context,
	snapshot proto.ProjectSnapshot,
	stream StreamFunc,
	refresh RefreshFunc,
) (proto.ProjectSnapshot, error) {
	if stream == nil || snapshot.CanStartCommands {
		return snapshot, nil
	}

	for {
		if snapshot.CanStartCommands || snapshot.NeedsConfirmation || snapshot.ProjectState == proto.ProjectStateTerminated {
			return snapshot, nil
		}

		waitCtx, cancel := context.WithCancel(ctx)
		streamErr := stream(waitCtx, snapshot.CurrentCursor, func(event proto.ProjectEvent) error {
			snapshot = ApplyProjectEvent(snapshot, event)
			if snapshot.CanStartCommands || snapshot.NeedsConfirmation || snapshot.ProjectState == proto.ProjectStateTerminated {
				cancel()
				return io.EOF
			}
			return nil
		})
		cancel()
		if streamErr == nil || errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, io.EOF) {
			return snapshot, nil
		}

		var protoErr *proto.Error
		if !errors.As(streamErr, &protoErr) || protoErr.Code != proto.ErrCursorExpired {
			return proto.ProjectSnapshot{}, streamErr
		}
		if refresh == nil {
			return proto.ProjectSnapshot{}, streamErr
		}
		var err error
		snapshot, err = refresh(ctx)
		if err != nil {
			return proto.ProjectSnapshot{}, err
		}
	}
}

func ApplyProjectEvent(snapshot proto.ProjectSnapshot, event proto.ProjectEvent) proto.ProjectSnapshot {
	snapshot.CurrentCursor = event.Cursor
	if event.ProjectState != "" {
		snapshot.ProjectState = event.ProjectState
	}
	if event.AuthorityState != "" {
		snapshot.AuthorityState = event.AuthorityState
	}
	if event.OwnerWorkspaceInstanceID != "" || event.Type == proto.ProjectEventOwnerChanged {
		snapshot.OwnerWorkspaceInstanceID = event.OwnerWorkspaceInstanceID
	}
	snapshot.SyncEpoch = event.SyncEpoch
	snapshot.NeedsConfirmation = event.NeedsConfirmation
	snapshot.CanStartCommands = snapshot.ProjectState == proto.ProjectStateActive &&
		!snapshot.NeedsConfirmation &&
		proto.AuthorityReady(snapshot.AuthorityState)
	if event.Warning != nil {
		snapshot.Warnings = []proto.Warning{*event.Warning}
	} else if !snapshot.NeedsConfirmation {
		snapshot.Warnings = nil
	}
	if event.Command != nil {
		replaced := false
		for idx := range snapshot.CommandSnapshots {
			if snapshot.CommandSnapshots[idx].CommandID == event.Command.CommandID {
				snapshot.CommandSnapshots[idx] = *event.Command
				replaced = true
				break
			}
		}
		if !replaced {
			snapshot.CommandSnapshots = append(snapshot.CommandSnapshots, *event.Command)
		}
	}
	return snapshot
}
