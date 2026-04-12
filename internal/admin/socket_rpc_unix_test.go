//go:build !windows

package admin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenAdminSocketPermissionDenied(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("permission checks are unreliable as root")
	}

	root := newAdminSocketTestDir(t)
	socketDir := filepath.Join(root, "private")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	socketPath := filepath.Join(socketDir, "admin.sock")
	listener, err := ListenAdminSocket(socketPath)
	if err != nil {
		t.Fatalf("ListenAdminSocket() error = %v", err)
	}
	defer listener.Close()

	if err := os.Chmod(socketDir, 0o000); err != nil {
		t.Fatalf("Chmod(000) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(socketDir, 0o700)
	})

	client, err := DialAdminSocket(socketPath)
	if err == nil {
		client.Close()
		t.Fatal("DialAdminSocket() succeeded without directory permissions")
	}
	if !errors.Is(err, os.ErrPermission) && !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Fatalf("DialAdminSocket() error = %v, want permission error", err)
	}
}
