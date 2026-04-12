//go:build !linux

package daemon

import "os"

func ensureMountDirectoryReady(mountDir string) error {
	return os.MkdirAll(mountDir, 0o755)
}

func isActiveMountpoint(string) (bool, error) {
	return false, nil
}
