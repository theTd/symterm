//go:build windows

package app

import (
	"os"

	"golang.org/x/sys/windows"
)

var terminalSize = func(file *os.File) (int, int, bool) {
	if file == nil {
		return 0, 0, false
	}
	handle := windows.Handle(file.Fd())
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(handle, &info); err != nil {
		return 0, 0, false
	}
	columns := int(info.Window.Right-info.Window.Left) + 1
	rows := int(info.Window.Bottom-info.Window.Top) + 1
	if columns <= 0 || rows <= 0 {
		return 0, 0, false
	}
	return columns, rows, true
}
