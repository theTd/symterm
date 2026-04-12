//go:build linux

package daemon

import "syscall"

const fuseSuperMagic = 0x65735546

func mountRequiresMirror(path string) (bool, error) {
	mounted, err := isListedMountpoint(path)
	if err != nil {
		return false, err
	}
	if mounted {
		return false, nil
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false, err
	}
	return int64(stat.Type) != fuseSuperMagic, nil
}
