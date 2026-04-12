//go:build unix

package app

import (
	"os"

	"golang.org/x/sys/unix"
)

var terminalSize = func(file *os.File) (int, int, bool) {
	if file == nil {
		return 0, 0, false
	}
	size, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, false
	}
	columns := int(size.Col)
	rows := int(size.Row)
	if columns <= 0 || rows <= 0 {
		return 0, 0, false
	}
	return columns, rows, true
}
