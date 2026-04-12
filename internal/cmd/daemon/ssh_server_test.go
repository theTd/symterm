package daemoncmd

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"symterm/internal/admin"
	"symterm/internal/control"
	"symterm/internal/control/controltest"
	"symterm/internal/proto"
	"symterm/internal/transport"

	cryptossh "golang.org/x/crypto/ssh"
)

func TestLoadOrCreateSSHHostSignerReusesPersistedKey(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ssh_host_ed25519")
	first, err := LoadOrCreateSSHHostSigner(path)
	if err != nil {
		t.Fatalf("LoadOrCreateSSHHostSigner(first) error = %v", err)
	}
	second, err := LoadOrCreateSSHHostSigner(path)
	if err != nil {
		t.Fatalf("LoadOrCreateSSHHostSigner(second) error = %v", err)
	}
	if string(first.PublicKey().Marshal()) != string(second.PublicKey().Marshal()) {
		t.Fatal("host key changed after reload")
	}
}

func TestRunSSHListenerAcceptsValidTokenAndRejectsInvalidToken(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	auth := control.StaticTokenAuthenticator{"token-a": "alice"}
	service := controltest.NewService(t, auth, control.ServiceDependencies{})
	signer, err := LoadOrCreateSSHHostSigner(filepath.Join(t.TempDir(), "ssh_host_ed25519"))
	if err != nil {
		t.Fatalf("LoadOrCreateSSHHostSigner() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	go func() {
		_ = RunSSHListener(ctx, service, auth, listener, signer)
	}()

	if _, err := dialSSH(listener.Addr().String(), signer, "token-a"); err != nil {
		t.Fatalf("dialSSH(valid) error = %v", err)
	}
	if _, err := dialSSH(listener.Addr().String(), signer, "wrong-token"); err == nil {
		t.Fatal("dialSSH(invalid) error = nil")
	}
}

func TestRunSSHListenerServesControlChannelAndRejectsInvalidOpenRequests(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	auth := control.StaticTokenAuthenticator{"token-a": "alice"}
	service := controltest.NewService(t, auth, control.ServiceDependencies{
		Runtime: controltest.RuntimeBackendStub{
			BootstrapperStub: controltest.BootstrapperStub{},
		},
	})
	signer, err := LoadOrCreateSSHHostSigner(filepath.Join(t.TempDir(), "ssh_host_ed25519"))
	if err != nil {
		t.Fatalf("LoadOrCreateSSHHostSigner() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	go func() {
		_ = RunSSHListener(ctx, service, auth, listener, signer)
	}()

	client, err := dialSSH(listener.Addr().String(), signer, "token-a")
	if err != nil {
		t.Fatalf("dialSSH() error = %v", err)
	}
	defer client.Close()

	controlConn, err := transport.OpenSSHControlChannel(client)
	if err != nil {
		t.Fatalf("OpenSSHControlChannel() error = %v", err)
	}
	defer controlConn.Close()
	controlClient := transport.NewClient(controlConn, controlConn)

	var hello control.HelloResponse
	if err := controlClient.Call(ctx, "hello", "", proto.HelloRequest{
		Version:            "v1alpha1",
		ProjectID:          "demo",
		TransportKind:      string(control.TransportKindSSH),
		LocalWorkspaceRoot: "/workspace/demo",
	}, &hello); err != nil {
		t.Fatalf("hello error = %v", err)
	}

	var session proto.ProjectSessionResponse
	if err := controlClient.Call(ctx, "open_project_session", hello.ClientID, proto.OpenProjectSessionRequest{
		ProjectID: "demo",
	}, &session); err != nil {
		t.Fatalf("open_project_session error = %v", err)
	}
	if session.Snapshot.ProjectID != "demo" {
		t.Fatalf("ProjectID = %q", session.Snapshot.ProjectID)
	}

	var ensured proto.ProjectSnapshot
	if err := controlClient.Call(ctx, "ensure_project", hello.ClientID, proto.EnsureProjectRequest{
		ProjectID: "demo",
	}, &ensured); err != nil {
		t.Fatalf("ensure_project error = %v", err)
	}
	if ensured.ProjectID != "demo" {
		t.Fatalf("ensure_project ProjectID = %q", ensured.ProjectID)
	}

	if _, _, err := client.OpenChannel("unknown-channel", nil); err == nil {
		t.Fatal("OpenChannel(unknown) error = nil")
	}
	if _, _, err := client.OpenChannel(transport.SSHChannelControl, nil); err == nil {
		t.Fatal("OpenChannel(duplicate control) error = nil")
	}
}

func TestRunSSHListenerDisconnectsSessionWithPendingWatchStream(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	auth := control.StaticTokenAuthenticator{"token-a": "alice"}
	service := controltest.NewService(t, auth, control.ServiceDependencies{
		Runtime: controltest.RuntimeBackendStub{
			BootstrapperStub: controltest.BootstrapperStub{},
		},
	})
	signer, err := LoadOrCreateSSHHostSigner(filepath.Join(t.TempDir(), "ssh_host_ed25519"))
	if err != nil {
		t.Fatalf("LoadOrCreateSSHHostSigner() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	streamStarted := make(chan struct{})
	go func() {
		_ = RunSSHListener(ctx, service, auth, listener, signer, func(format string, args ...any) {
			if strings.Contains(format, "server async route begin") && len(args) >= 2 && args[1] == "_internal_watch_invalidate_stream" {
				select {
				case <-streamStarted:
				default:
					close(streamStarted)
				}
			}
		})
	}()

	client, err := dialSSH(listener.Addr().String(), signer, "token-a")
	if err != nil {
		t.Fatalf("dialSSH() error = %v", err)
	}

	controlConn, err := transport.OpenSSHControlChannel(client)
	if err != nil {
		t.Fatalf("OpenSSHControlChannel() error = %v", err)
	}
	controlClient := transport.NewClient(controlConn, controlConn)

	var hello control.HelloResponse
	if err := controlClient.Call(ctx, "hello", "", proto.HelloRequest{
		ProjectID:     "demo",
		TransportKind: string(control.TransportKindSSH),
	}, &hello); err != nil {
		t.Fatalf("hello error = %v", err)
	}
	if err := controlClient.Call(ctx, "ensure_project", hello.ClientID, proto.EnsureProjectRequest{
		ProjectID: "demo",
	}, nil); err != nil {
		t.Fatalf("ensure_project error = %v", err)
	}

	streamErr := make(chan error, 1)
	go func() {
		streamErr <- controlClient.StreamInvalidateEvents(context.Background(), hello.ClientID, proto.WatchInvalidateRequest{
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

	if err := controlConn.Close(); err != nil {
		t.Fatalf("controlConn.Close() error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client.Close() error = %v", err)
	}

	select {
	case <-streamErr:
	case <-time.After(time.Second):
		t.Fatal("StreamInvalidateEvents() did not exit after SSH disconnect")
	}

	deadline := time.Now().Add(time.Second)
	for {
		if sessions := service.ListSessions(); len(sessions) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ListSessions() retained live sessions after SSH disconnect: %#v", service.ListSessions())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunSSHListenerImportedBootstrapConnectionsCanUseHello(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := admin.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	if err := store.ImportBootstrapTokens(map[string]string{"bootstrap-secret": "alice"}, time.Now().UTC()); err != nil {
		t.Fatalf("ImportBootstrapTokens() error = %v", err)
	}
	service := controltest.NewService(t, store, control.ServiceDependencies{})
	signer, err := LoadOrCreateSSHHostSigner(filepath.Join(t.TempDir(), "ssh_host_ed25519"))
	if err != nil {
		t.Fatalf("LoadOrCreateSSHHostSigner() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	go func() {
		_ = RunSSHListener(ctx, service, store, listener, signer)
	}()

	client, err := dialSSH(listener.Addr().String(), signer, "bootstrap-secret")
	if err != nil {
		t.Fatalf("dialSSH() error = %v", err)
	}
	defer client.Close()

	controlConn, err := transport.OpenSSHControlChannel(client)
	if err != nil {
		t.Fatalf("OpenSSHControlChannel() error = %v", err)
	}
	defer controlConn.Close()
	controlClient := transport.NewClient(controlConn, controlConn)

	var hello control.HelloResponse
	if err := controlClient.Call(ctx, "hello", "", proto.HelloRequest{
		ProjectID:     "demo",
		TransportKind: string(control.TransportKindSSH),
	}, &hello); err != nil {
		t.Fatalf("hello() error = %v", err)
	}
	if hello.Username != "alice" {
		t.Fatalf("hello.Username = %q, want alice", hello.Username)
	}
}

func dialSSH(addr string, signer cryptossh.Signer, password string) (*cryptossh.Client, error) {
	return cryptossh.Dial("tcp", addr, &cryptossh.ClientConfig{
		User:            sshUser,
		Auth:            []cryptossh.AuthMethod{cryptossh.Password(password)},
		HostKeyCallback: cryptossh.FixedHostKey(signer.PublicKey()),
	})
}
