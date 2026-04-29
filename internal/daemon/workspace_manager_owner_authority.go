package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"symterm/internal/ownerfs"
	"symterm/internal/proto"
	workspacesync "symterm/internal/sync"
)

type workspaceAuthority struct {
	root    string
	client  ownerfs.Client
	state   proto.AuthorityState
	managed bool
}

// ownerRPCTimeout bounds how long a single owner-fs RPC call may block. A
// silently dead owner channel would otherwise pin a FUSE handler indefinitely;
// SSH keepalive and the transport.Client idle watchdog defend the connection
// layer, but this hard per-call ceiling ensures the workspace operation surfaces
// an error rather than hanging forever even if those defences misfire.
const ownerRPCTimeout = 30 * time.Second

func ownerRPCContext(parent context.Context) (context.Context, context.CancelFunc) {
	if _, ok := parent.Deadline(); ok {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, ownerRPCTimeout)
}

func (a workspaceAuthority) normalizedState() proto.AuthorityState {
	if !a.managed {
		return proto.AuthorityStateStable
	}
	return proto.NormalizeAuthorityState(a.state)
}

func (m *WorkspaceManager) SetAuthoritativeRoot(projectKey proto.ProjectKey, root string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.authorities[projectKey.String()] = workspaceAuthority{
		root:    root,
		state:   proto.AuthorityStateStable,
		managed: false,
	}
	if m.authorityCond != nil {
		m.authorityCond.Broadcast()
	}
	return nil
}

func (m *WorkspaceManager) SetAuthoritativeClient(projectKey proto.ProjectKey, client ownerfs.Client) error {
	m.mu.Lock()
	m.authorities[projectKey.String()] = workspaceAuthority{
		client:  client,
		state:   proto.AuthorityStateStable,
		managed: true,
	}
	if m.authorityCond != nil {
		m.authorityCond.Broadcast()
	}
	m.mu.Unlock()

	// Defence in depth: even if the control-layer disconnect handler misfires,
	// watch the owner channel directly so a silently dead authority is demoted
	// instead of leaving waitForMutationAuthority pinned forever.
	if done := client.Done(); done != nil {
		go m.watchAuthoritativeClient(projectKey, client, done)
	}
	return nil
}

func (m *WorkspaceManager) watchAuthoritativeClient(projectKey proto.ProjectKey, client ownerfs.Client, done <-chan struct{}) {
	<-done
	m.mu.Lock()
	defer m.mu.Unlock()

	authority := m.authorities[projectKey.String()]
	if authority.client != client {
		return
	}
	authority.client = nil
	authority.root = ""
	authority.state = proto.AuthorityStateAbsent
	m.authorities[projectKey.String()] = authority
	if m.authorityCond != nil {
		m.authorityCond.Broadcast()
	}
}

func (m *WorkspaceManager) ClearAuthoritativeRoot(projectKey proto.ProjectKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	authority := m.authorities[projectKey.String()]
	authority.root = ""
	authority.client = nil
	authority.state = proto.AuthorityStateAbsent
	m.authorities[projectKey.String()] = authority
	if m.authorityCond != nil {
		m.authorityCond.Broadcast()
	}
	return nil
}

func (m *WorkspaceManager) SetAuthorityState(projectKey proto.ProjectKey, state proto.AuthorityState) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	authority := m.authorities[projectKey.String()]
	authority.state = proto.NormalizeAuthorityState(state)
	if authority.state != proto.AuthorityStateStable {
		authority.client = nil
	}
	m.authorities[projectKey.String()] = authority
	if m.authorityCond != nil {
		m.authorityCond.Broadcast()
	}
	return nil
}

func (m *WorkspaceManager) FsRead(projectKey proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	return m.FsReadContext(context.Background(), projectKey, op, request)
}

func (m *WorkspaceManager) FsReadContext(ctx context.Context, projectKey proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	layout, err := ResolveProjectLayout(m.projectsRoot, projectKey)
	if err != nil {
		return proto.FsReply{}, err
	}
	if err := validateWorkspaceReadPath(op, request.Path); err != nil {
		return proto.FsReply{}, err
	}
	if isRootWorkspacePath(request.Path) {
		return m.fsReadRoot(ctx, projectKey, layout, op, request)
	}
	handleTarget, usingHandle, err := m.readTargetForHandle(projectKey, request)
	if err != nil {
		return proto.FsReply{}, err
	}
	if !usingHandle || handleTarget.backingKind == stagedHandleBackingOwnerReadThrough {
		if err := m.revalidateConservativePaths(ctx, projectKey, layout, conservativePathsForAccess(request.Path)); err != nil {
			return proto.FsReply{}, err
		}
	}
	var target string
	if usingHandle {
		if op == proto.FsOpReadDir && handleTarget.metadata.Mode == 0 {
			handleTarget.metadata.Mode = 0o755
		}
		target = handleTarget.tempPath
	}
	authority := m.authorityForProject(projectKey)
	var authorityRules *workspacesync.WorkspaceViewRules
	if authority.client != nil && (!usingHandle || handleTarget.backingKind == stagedHandleBackingOwnerReadThrough) {
		ownerRequest := request
		ownerRequest.HandleID = ""
		ownerCtx, cancel := ownerRPCContext(ctx)
		reply, err := authority.client.FsRead(ownerCtx, op, ownerRequest)
		cancel()
		if err != nil {
			return proto.FsReply{}, err
		}
		reply = m.withGenerations(projectKey, request.Path, reply)
		reply.HandleID = request.HandleID
		return reply, nil
	}
	if !usingHandle {
		if authority.client == nil && strings.TrimSpace(authority.root) != "" {
			authorityRules, err = workspacesync.LoadWorkspaceViewRules(authority.root)
			if err != nil {
				return proto.FsReply{}, err
			}
			visible, err := workspacesync.PathVisibleOnDisk(authority.root, authorityRules, request.Path)
			if err != nil {
				return proto.FsReply{}, err
			}
			if !visible {
				return proto.FsReply{}, proto.NewError(proto.ErrUnknownFile, "path is excluded from the shared workspace")
			}
		}
		target = m.authoritativePath(authority, layout, request.Path)
	}

	switch op {
	case proto.FsOpLookup, proto.FsOpGetAttr:
		if usingHandle {
			reply := proto.FsReply{
				Metadata: normalizeMetadata(handleTarget.metadata),
				IsDir:    handleTarget.isDir,
			}
			reply.Data = []byte(fmt.Sprintf("%d:%d:%s", handleTarget.metadata.Size, handleTarget.metadata.Mode, handleTarget.metadata.MTime.UTC().Format(time.RFC3339Nano)))
			reply = m.withGenerations(projectKey, request.Path, reply)
			reply.HandleID = request.HandleID
			return reply, nil
		}
		info, err := os.Stat(target)
		if err != nil {
			return proto.FsReply{}, err
		}
		metadata := fileInfoMetadata(info)
		reply := proto.FsReply{
			Metadata: normalizeMetadata(metadata),
			IsDir:    info.IsDir(),
		}
		reply.Data = []byte(fmt.Sprintf("%d:%d:%s", metadata.Size, metadata.Mode, metadata.MTime.UTC().Format(time.RFC3339Nano)))
		reply = m.withGenerations(projectKey, request.Path, reply)
		reply.HandleID = request.HandleID
		return reply, nil
	case proto.FsOpRead:
		file, err := os.Open(target)
		if err != nil {
			return proto.FsReply{}, err
		}
		defer file.Close()
		if request.Offset > 0 {
			if _, err := file.Seek(request.Offset, io.SeekStart); err != nil {
				return proto.FsReply{}, err
			}
		}
		size := request.Size
		if size <= 0 {
			size = 64 * 1024
		}
		buf := make([]byte, size)
		n, err := file.Read(buf)
		if err != nil && err != io.EOF {
			return proto.FsReply{}, err
		}
		reply := m.withGenerations(projectKey, request.Path, proto.FsReply{Data: buf[:n]})
		reply.HandleID = request.HandleID
		return reply, nil
	case proto.FsOpReadDir:
		names := make([]string, 0)
		if authorityRules != nil {
			names, err = workspacesync.VisibleDirectoryNamesOnDisk(authority.root, authorityRules, request.Path)
			if err != nil {
				return proto.FsReply{}, err
			}
		} else {
			entries, err := os.ReadDir(target)
			if err != nil {
				return proto.FsReply{}, err
			}
			names = make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		reply := m.withGenerations(projectKey, request.Path, proto.FsReply{Data: []byte(strings.Join(names, "\n"))})
		reply.HandleID = request.HandleID
		return reply, nil
	default:
		return proto.FsReply{}, proto.NewError(proto.ErrInvalidArgument, "unsupported FsRead operation")
	}
}

func (m *WorkspaceManager) fsReadRoot(ctx context.Context, projectKey proto.ProjectKey, layout ProjectLayout, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	if err := m.revalidateConservativePaths(ctx, projectKey, layout, []string{""}); err != nil {
		return proto.FsReply{}, err
	}

	authority := m.authorityForProject(projectKey)
	if authority.client != nil {
		ownerCtx, cancel := ownerRPCContext(ctx)
		reply, err := authority.client.FsRead(ownerCtx, op, request)
		cancel()
		if err != nil {
			return proto.FsReply{}, err
		}
		return m.withGenerations(projectKey, "", reply), nil
	}
	target := m.authoritativePath(authority, layout, "")
	switch op {
	case proto.FsOpLookup, proto.FsOpGetAttr:
		info, err := os.Stat(target)
		if err != nil {
			return proto.FsReply{}, err
		}
		metadata := fileInfoMetadata(info)
		reply := proto.FsReply{
			Metadata: metadata,
			IsDir:    true,
			Data:     []byte(fmt.Sprintf("%d:%d:%s", metadata.Size, metadata.Mode, metadata.MTime.UTC().Format(time.RFC3339Nano))),
		}
		return m.withGenerations(projectKey, "", reply), nil
	case proto.FsOpReadDir:
		names := make([]string, 0)
		if authority.client == nil && strings.TrimSpace(authority.root) != "" {
			rules, err := workspacesync.LoadWorkspaceViewRules(authority.root)
			if err != nil {
				return proto.FsReply{}, err
			}
			names, err = workspacesync.VisibleDirectoryNamesOnDisk(authority.root, rules, "")
			if err != nil {
				return proto.FsReply{}, err
			}
		} else {
			entries, err := os.ReadDir(target)
			if err != nil {
				return proto.FsReply{}, err
			}
			names = make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		return m.withGenerations(projectKey, "", proto.FsReply{Data: []byte(strings.Join(names, "\n")), IsDir: true}), nil
	default:
		return proto.FsReply{}, proto.NewError(proto.ErrInvalidArgument, "unsupported root FsRead operation")
	}
}

func (m *WorkspaceManager) authoritativePath(authority workspaceAuthority, layout ProjectLayout, workspacePath string) string {
	root := strings.TrimSpace(authority.root)
	if root == "" {
		return canonicalWorkspacePath(layout, workspacePath)
	}
	return filepath.Join(root, filepath.FromSlash(workspacePath))
}

func (m *WorkspaceManager) authorityForProject(projectKey proto.ProjectKey) workspaceAuthority {
	m.mu.Lock()
	authority := m.authorities[projectKey.String()]
	m.mu.Unlock()
	return authority
}

func (m *WorkspaceManager) waitForMutationAuthority(ctx context.Context, projectKey proto.ProjectKey) error {
	for {
		m.mu.Lock()
		authority := m.authorities[projectKey.String()]
		state := authority.normalizedState()
		m.mu.Unlock()

		if !authority.managed || state == proto.AuthorityStateStable {
			return nil
		}
		if state == proto.AuthorityStateAbsent {
			return proto.NewError(proto.ErrOwnerMissing, "owner authority is unavailable")
		}

		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *WorkspaceManager) stagingBasePath(projectKey proto.ProjectKey, layout ProjectLayout, workspacePath string) string {
	authority := m.authorityForProject(projectKey)
	if authority.client != nil {
		return canonicalWorkspacePath(layout, workspacePath)
	}
	return m.authoritativePath(authority, layout, workspacePath)
}
