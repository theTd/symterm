package admin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func newAdminSocketTestDir(t *testing.T) string {
	t.Helper()

	if runtime.GOOS != "windows" {
		const shortTmp = "/tmp"
		if info, err := os.Stat(shortTmp); err == nil && info.IsDir() {
			dir, err := os.MkdirTemp(shortTmp, "symterm-admin-")
			if err == nil {
				t.Cleanup(func() {
					_ = os.RemoveAll(dir)
				})
				return dir
			}
		}
	}

	return t.TempDir()
}

func newAdminSocketTestPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(newAdminSocketTestDir(t), "admin.sock")
}
