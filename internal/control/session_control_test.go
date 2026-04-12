package control

import (
	"context"
	"testing"

	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

func TestRegisterOwnerFileClientAssignsAuthoritativeClientForSSHWorkspaceRoot(t *testing.T) {
	t.Parallel()

	var rootCalls int
	var clientCalls int
	service := newTestService(t, StaticTokenAuthenticator{"token-a": "alice"}, ServiceDependencies{
		Runtime: runtimeBackendStub{
			filesystemBackendStub: filesystemBackendStub{
				setAuthoritativeRoot: func(proto.ProjectKey, string) error {
					rootCalls++
					return nil
				},
				setAuthoritativeClient: func(key proto.ProjectKey, client ownerfs.Client) error {
					clientCalls++
					if key != (proto.ProjectKey{Username: "alice", ProjectID: "demo"}) {
						t.Fatalf("SetAuthoritativeClient() key = %#v", key)
					}
					if _, ok := client.(*fakeOwnerFileClient); !ok {
						t.Fatalf("SetAuthoritativeClient() client = %T", client)
					}
					return nil
				},
			},
		},
		Now: fixedClock(),
	})

	hello, err := service.HelloAuthenticated(context.Background(), AuthenticatedPrincipal{
		Username:    "alice",
		TokenSource: TokenSourceManaged,
	}, proto.HelloRequest{
		ProjectID:          "demo",
		TransportKind:      string(TransportKindSSH),
		LocalWorkspaceRoot: `C:\workspace\symterm`,
		SessionKind:        proto.SessionKindAuthority,
		WorkspaceDigest:    testWorkspaceDigest(1, "root-a"),
	})
	if err != nil {
		t.Fatalf("HelloAuthenticated() error = %v", err)
	}
	if _, err := service.OpenProjectSession(hello.ClientID, proto.OpenProjectSessionRequest{ProjectID: "demo"}); err != nil {
		t.Fatalf("OpenProjectSession() error = %v", err)
	}

	if err := service.RegisterOwnerFileClient(hello.ClientID, &fakeOwnerFileClient{}); err != nil {
		t.Fatalf("RegisterOwnerFileClient() error = %v", err)
	}
	if rootCalls != 0 {
		t.Fatalf("SetAuthoritativeRoot() call count = %d, want 0", rootCalls)
	}
	if clientCalls != 1 {
		t.Fatalf("SetAuthoritativeClient() call count = %d, want 1", clientCalls)
	}
}
