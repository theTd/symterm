package transport

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"symterm/internal/control"
)

func TestHelloRejectsLegacyTokenField(t *testing.T) {
	t.Parallel()

	service, err := control.NewService(control.StaticTokenAuthenticator{"token-a": "alice"})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	server := NewServerWithOptions(service, serverConn, serverConn, ServerOptions{
		ConnMeta: control.ConnMeta{
			TransportKind: control.TransportKindSSH,
			RemoteAddr:    "203.0.113.10:1234",
			LocalAddr:     "127.0.0.1:7000",
		},
		Principal: &control.AuthenticatedPrincipal{
			Username:    "alice",
			TokenID:     "tok-1",
			TokenSource: control.TokenSourceManaged,
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.Serve(ctx)
	}()

	if _, err := clientConn.Write([]byte("{\"id\":1,\"method\":\"hello\",\"params\":{\"ProjectID\":\"demo\",\"TransportKind\":\"ssh\",\"Token\":\"legacy\"}}\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	response := make([]byte, 4096)
	n, err := clientConn.Read(response)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	var decoded Response
	if err := json.Unmarshal(response[:n], &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.Error == nil {
		t.Fatal("response error = nil")
	}
	if !strings.Contains(decoded.Error.Message, "unknown field") || !strings.Contains(strings.ToLower(decoded.Error.Message), "token") {
		t.Fatalf("response error = %#v", decoded.Error)
	}
}
