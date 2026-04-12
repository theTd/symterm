//go:build !windows

package daemon

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startCommandPTY(cmd *exec.Cmd, columns int, rows int) (*os.File, error) {
	size := &pty.Winsize{
		Cols: uint16(normalizePTYDimension(columns, 80)),
		Rows: uint16(normalizePTYDimension(rows, 24)),
	}
	return pty.StartWithSize(cmd, size)
}

func resizeCommandPTY(file *os.File, columns int, rows int) error {
	if file == nil {
		return nil
	}
	return pty.Setsize(file, &pty.Winsize{
		Cols: uint16(normalizePTYDimension(columns, 80)),
		Rows: uint16(normalizePTYDimension(rows, 24)),
	})
}

func normalizePTYDimension(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
