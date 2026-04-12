package sync

import (
	"context"
	"strings"

	"symterm/internal/proto"
	"symterm/internal/transport"
)

type OwnerWorkspaceRuntime struct {
	Root string
}

func NewOwnerWorkspaceRuntime(root string) OwnerWorkspaceRuntime {
	return OwnerWorkspaceRuntime{Root: root}
}

func (r OwnerWorkspaceRuntime) StartWatcher(ctx context.Context, client *transport.Client, clientID string) func() {
	if client == nil || strings.TrimSpace(clientID) == "" || strings.TrimSpace(r.Root) == "" {
		return nil
	}
	return startOwnerWorkspaceWatcher(ctx, client, clientID, r.Root)
}

func (r OwnerWorkspaceRuntime) FileService() transport.OwnerFileHandler {
	return newLocalOwnerFileService(r.Root)
}

func (r OwnerWorkspaceRuntime) Ensure(ctx context.Context, client *transport.Client, clientID string, snapshot proto.ProjectSnapshot) func() {
	if snapshot.Role != proto.RoleOwner || snapshot.ProjectState == proto.ProjectStateSyncing {
		return nil
	}
	return r.StartWatcher(ctx, client, clientID)
}

func StartOwnerWorkspaceWatcher(ctx context.Context, client *transport.Client, clientID string, root string) func() {
	return NewOwnerWorkspaceRuntime(root).StartWatcher(ctx, client, clientID)
}

func NewLocalOwnerFileService(root string) transport.OwnerFileHandler {
	return NewOwnerWorkspaceRuntime(root).FileService()
}
