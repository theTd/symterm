//go:build windows

package daemon

import (
	"os"
	"os/exec"

	"symterm/internal/proto"
)

func startCommandPTY(_ *exec.Cmd, _ int, _ int) (*os.File, error) {
	return nil, proto.NewError(proto.ErrProjectNotReady, "pty mode is unavailable on this platform")
}

func resizeCommandPTY(_ *os.File, _ int, _ int) error {
	return proto.NewError(proto.ErrProjectNotReady, "pty mode is unavailable on this platform")
}
