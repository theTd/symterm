package daemoncmd

import (
	"testing"

	"symterm/internal/proto"
)

func TestFormatTmuxStatusLine(t *testing.T) {
	t.Parallel()

	line := formatTmuxStatusLine(proto.TmuxStatusSnapshot{
		CommandState:     proto.CommandStateRunning,
		ControlConnected: true,
		StdioConnected:   false,
		StdioBytesIn:     1536,
		StdioBytesOut:    12,
	})
	want := "run | ctl up | io down | rx 12B tx 1.5K"
	if line != want {
		t.Fatalf("formatTmuxStatusLine() = %q, want %q", line, want)
	}
}

func TestTmuxStatusFallbackText(t *testing.T) {
	t.Parallel()

	if got := tmuxStatusFallbackText(); got != "status unavailable" {
		t.Fatalf("tmuxStatusFallbackText() = %q", got)
	}
}
