package control

import (
	"context"
	"strings"
	"testing"

	"symterm/internal/proto"
)

func TestHelloRequiresAuthenticatedPrincipal(t *testing.T) {
	t.Parallel()

	service, err := NewService(StaticTokenAuthenticator{"token-a": "alice"})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if _, err := service.Hello(context.Background(), proto.HelloRequest{ProjectID: "demo"}); err == nil {
		t.Fatal("Hello() error = nil")
	} else if !strings.Contains(err.Error(), "authenticated SSH control channel") {
		t.Fatalf("Hello() error = %v", err)
	}
}

func TestHelloAuthenticatedCreatesSessionWithoutSecondTokenCheck(t *testing.T) {
	t.Parallel()

	service, err := NewService(StaticTokenAuthenticator{"token-a": "alice"})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	response, err := service.HelloAuthenticated(context.Background(), AuthenticatedPrincipal{
		Username:    "alice",
		TokenID:     "tok-1",
		TokenSource: TokenSourceManaged,
	}, proto.HelloRequest{
		ProjectID:     "demo",
		TransportKind: string(TransportKindSSH),
	})
	if err != nil {
		t.Fatalf("HelloAuthenticated() error = %v", err)
	}
	if response.ClientID == "" || response.SessionID == "" {
		t.Fatalf("HelloAuthenticated() = %#v", response)
	}
	if response.SyncCapabilities.ProtocolVersion != 2 {
		t.Fatalf("HelloAuthenticated() sync protocol = %d, want 2", response.SyncCapabilities.ProtocolVersion)
	}
	if !response.SyncCapabilities.ManifestBatch || !response.SyncCapabilities.DeleteBatch || !response.SyncCapabilities.UploadBundle || !response.SyncCapabilities.PersistentHashCache {
		t.Fatalf("HelloAuthenticated() sync capabilities = %#v, want all v2 capabilities enabled", response.SyncCapabilities)
	}
}

func TestHelloAuthenticatedRejectsUnknownSessionKind(t *testing.T) {
	t.Parallel()

	service, err := NewService(StaticTokenAuthenticator{"token-a": "alice"})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	_, err = service.HelloAuthenticated(context.Background(), AuthenticatedPrincipal{
		Username:    "alice",
		TokenID:     "tok-1",
		TokenSource: TokenSourceManaged,
	}, proto.HelloRequest{
		ProjectID:   "demo",
		SessionKind: "sidecar",
	})
	if err == nil {
		t.Fatal("HelloAuthenticated() error = nil")
	}
}
