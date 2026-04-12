//go:build !windows && !unix

package app

import "os"

var terminalSize = func(_ *os.File) (int, int, bool) {
	return 0, 0, false
}
