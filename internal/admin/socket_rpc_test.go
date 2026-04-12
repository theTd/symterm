package admin

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"symterm/internal/control"
	"symterm/internal/proto"
)

func TestSocketServerServesDaemonInfo(t *testing.T) {
	t.Parallel()

	service := newTestAdminService(t)
	socketPath := filepath.Join(t.TempDir(), "admin.sock")
	listener, err := ListenAdminSocket(socketPath)
	if err != nil {
		t.Fatalf("ListenAdminSocket() error = %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = NewSocketServer(service).Serve(ctx, listener)
	}()

	client, err := DialAdminSocket(socketPath)
	if err != nil {
		t.Fatalf("DialAdminSocket() error = %v", err)
	}
	defer client.Close()

	var info DaemonInfo
	if err := client.Call(context.Background(), "admin_get_daemon_info", nil, &info); err != nil {
		t.Fatalf("client.Call(admin_get_daemon_info) error = %v", err)
	}
	if info.Version != "dev" || info.AdminSocketPath != "/tmp/admin.sock" {
		t.Fatalf("daemon info = %#v", info)
	}
}

func TestSocketServerServesTmuxStatus(t *testing.T) {
	t.Parallel()

	service := newTestAdminService(t)
	service.SetTmuxStatusSource(tmuxStatusSourceFunc(func(clientID string, commandID string) (proto.TmuxStatusSnapshot, error) {
		return proto.TmuxStatusSnapshot{
			ClientID:         clientID,
			CommandID:        commandID,
			CommandState:     proto.CommandStateRunning,
			ControlConnected: true,
			StdioConnected:   true,
			StdioBytesIn:     10,
			StdioBytesOut:    20,
		}, nil
	}))
	socketPath := filepath.Join(t.TempDir(), "admin.sock")
	listener, err := ListenAdminSocket(socketPath)
	if err != nil {
		t.Fatalf("ListenAdminSocket() error = %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = NewSocketServer(service).Serve(ctx, listener)
	}()

	client, err := DialAdminSocket(socketPath)
	if err != nil {
		t.Fatalf("DialAdminSocket() error = %v", err)
	}
	defer client.Close()

	var status proto.TmuxStatusSnapshot
	if err := client.Call(context.Background(), "admin_get_tmux_status", map[string]string{
		"client_id":  "client-1",
		"command_id": "cmd-1",
	}, &status); err != nil {
		t.Fatalf("client.Call(admin_get_tmux_status) error = %v", err)
	}
	if status.CommandID != "cmd-1" || status.StdioBytesOut != 20 {
		t.Fatalf("tmux status = %#v", status)
	}
}

type tmuxStatusSourceFunc func(string, string) (proto.TmuxStatusSnapshot, error)

func (f tmuxStatusSourceFunc) TmuxStatus(clientID string, commandID string) (proto.TmuxStatusSnapshot, error) {
	return f(clientID, commandID)
}

func newTestAdminService(t *testing.T) *Service {
	t.Helper()

	controlService, err := control.NewService(control.StaticTokenAuthenticator{"token-a": "alice"})
	if err != nil {
		t.Fatalf("control.NewService() error = %v", err)
	}
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	eventHub, err := NewEventHub(0)
	if err != nil {
		t.Fatalf("NewEventHub() error = %v", err)
	}
	service, err := NewService(NewControlSessionCatalog(controlService), eventHub, store, DaemonInfo{
		Version:         "dev",
		StartedAt:       time.Unix(1_700_000_100, 0).UTC(),
		AdminSocketPath: "/tmp/admin.sock",
		AdminWebAddr:    "127.0.0.1:6040",
	}, time.Now)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}
