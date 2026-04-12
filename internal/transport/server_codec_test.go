package transport

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"symterm/internal/control"
	"symterm/internal/control/controltest"
	"symterm/internal/proto"
)

func TestServeCancelsAsyncWatchStreamsBeforeDisconnect(t *testing.T) {
	t.Parallel()

	service := controltest.NewService(t, control.StaticTokenAuthenticator{"token-a": "alice"}, control.ServiceDependencies{
		Runtime: controltest.RuntimeBackendStub{
			BootstrapperStub: controltest.BootstrapperStub{},
		},
	})

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()

	streamStarted := make(chan struct{})
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
		Tracef: func(format string, args ...any) {
			if strings.Contains(format, "server async route begin") && len(args) >= 2 && args[1] == internalWatchInvalidateMethod+"_stream" {
				select {
				case <-streamStarted:
				default:
					close(streamStarted)
				}
			}
		},
	})

	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(serveCtx)
	}()

	client := NewClient(clientConn, clientConn)

	var hello control.HelloResponse
	if err := client.Call(context.Background(), "hello", "", proto.HelloRequest{
		ProjectID:     "demo",
		TransportKind: string(control.TransportKindSSH),
	}, &hello); err != nil {
		t.Fatalf("hello error = %v", err)
	}
	if err := client.Call(context.Background(), "ensure_project", hello.ClientID, proto.EnsureProjectRequest{
		ProjectID: "demo",
	}, nil); err != nil {
		t.Fatalf("ensure_project error = %v", err)
	}

	streamErr := make(chan error, 1)
	go func() {
		streamErr <- client.StreamInvalidateEvents(context.Background(), hello.ClientID, proto.WatchInvalidateRequest{
			ProjectID: "demo",
		}, func(proto.InvalidateEvent) error {
			return nil
		})
	}()

	select {
	case <-streamStarted:
	case <-time.After(time.Second):
		t.Fatal("watch_invalidate_stream did not start")
	}

	if err := clientConn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after client disconnect")
	}

	select {
	case <-streamErr:
	case <-time.After(time.Second):
		t.Fatal("StreamInvalidateEvents() did not exit after client disconnect")
	}

	if sessions := service.ListSessions(); len(sessions) != 0 {
		t.Fatalf("ListSessions() = %#v, want no live sessions", sessions)
	}
}
