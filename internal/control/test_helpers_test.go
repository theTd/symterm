package control

import (
	"context"
	"testing"
	"time"

	"symterm/internal/proto"
)

func fixedClock() Clock {
	now := time.Unix(1_700_000_000, 0).UTC()
	return func() time.Time { return now }
}

func testWorkspaceDigest(_ int, value string) proto.WorkspaceDigest {
	return proto.WorkspaceDigest{
		Algorithm: "sha256",
		Value:     value,
	}
}

func newAuthorityProjectService(t testing.TB, deps ServiceDependencies) (*Service, string, proto.ProjectKey, proto.ProjectSnapshot) {
	t.Helper()

	service := newTestService(t, StaticTokenAuthenticator{"token-a": "alice"}, deps)
	hello, err := service.HelloAuthenticated(context.Background(), AuthenticatedPrincipal{
		Username:    "alice",
		TokenSource: TokenSourceBootstrap,
	}, proto.HelloRequest{
		ProjectID:          "demo",
		TransportKind:      string(TransportKindSSH),
		LocalWorkspaceRoot: `C:\Users\cui\standalone\symterm`,
		SessionKind:        proto.SessionKindAuthority,
		WorkspaceDigest:    testWorkspaceDigest(1, "root-a"),
	})
	if err != nil {
		t.Fatalf("HelloAuthenticated() error = %v", err)
	}

	snapshot, err := service.EnsureProjectRequest(hello.ClientID, proto.EnsureProjectRequest{ProjectID: "demo"})
	if err != nil {
		t.Fatalf("EnsureProjectRequest() error = %v", err)
	}
	return service, hello.ClientID, proto.ProjectKey{Username: "alice", ProjectID: "demo"}, snapshot
}

type fakeOwnerFileClient struct {
	done chan struct{}
}

func (c *fakeOwnerFileClient) FsRead(context.Context, proto.FsOperation, proto.FsRequest) (proto.FsReply, error) {
	return proto.FsReply{}, nil
}

func (c *fakeOwnerFileClient) Apply(context.Context, proto.OwnerFileApplyRequest) error {
	return nil
}

func (c *fakeOwnerFileClient) BeginFileUpload(context.Context, proto.OwnerFileBeginRequest) (proto.OwnerFileBeginResponse, error) {
	return proto.OwnerFileBeginResponse{}, nil
}

func (c *fakeOwnerFileClient) ApplyFileChunk(context.Context, proto.OwnerFileApplyChunkRequest) error {
	return nil
}

func (c *fakeOwnerFileClient) CommitFileUpload(context.Context, proto.OwnerFileCommitRequest) error {
	return nil
}

func (c *fakeOwnerFileClient) AbortFileUpload(context.Context, proto.OwnerFileAbortRequest) error {
	return nil
}

func (c *fakeOwnerFileClient) Done() <-chan struct{} {
	if c.done == nil {
		c.done = make(chan struct{})
	}
	return c.done
}

func (c *fakeOwnerFileClient) Close() error {
	if c.done != nil {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}
	return nil
}
