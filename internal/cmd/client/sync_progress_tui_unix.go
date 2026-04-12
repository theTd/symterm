//go:build !windows

package client

import "os"

func enableVirtualTerminalSequences(_ *os.File) bool {
	return true
}
