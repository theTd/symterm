package daemoncmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"symterm/internal/admin"
	"symterm/internal/proto"
)

func RunInternalTmuxStatus(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("tmux-status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var adminSocketPath string
	var clientID string
	var commandID string
	fs.StringVar(&adminSocketPath, "admin-socket", "", "local admin unix socket path")
	fs.StringVar(&clientID, "client-id", "", "client id that owns the command")
	fs.StringVar(&commandID, "command-id", "", "command id to summarize")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected tmux-status arguments: %v", fs.Args())
	}
	if strings.TrimSpace(adminSocketPath) == "" {
		return errors.New("tmux-status requires --admin-socket")
	}
	if strings.TrimSpace(clientID) == "" {
		return errors.New("tmux-status requires --client-id")
	}
	if strings.TrimSpace(commandID) == "" {
		return errors.New("tmux-status requires --command-id")
	}

	client, err := admin.DialAdminSocket(adminSocketPath)
	if err != nil {
		_, _ = io.WriteString(stdout, tmuxStatusFallbackText())
		_, _ = io.WriteString(stderr, err.Error())
		return nil
	}
	defer client.Close()

	var status proto.TmuxStatusSnapshot
	if err := client.Call(ctx, "admin_get_tmux_status", map[string]string{
		"client_id":  clientID,
		"command_id": commandID,
	}, &status); err != nil {
		_, _ = io.WriteString(stdout, tmuxStatusFallbackText())
		_, _ = io.WriteString(stderr, err.Error())
		return nil
	}
	_, err = io.WriteString(stdout, formatTmuxStatusLine(status))
	return err
}

func formatTmuxStatusLine(status proto.TmuxStatusSnapshot) string {
	parts := []string{
		shortCommandState(status.CommandState),
		"ctl " + upDown(status.ControlConnected),
		"io " + upDown(status.StdioConnected),
		fmt.Sprintf("rx %s tx %s", humanizeTrafficBytes(status.StdioBytesOut), humanizeTrafficBytes(status.StdioBytesIn)),
	}
	return strings.Join(parts, " | ")
}

func tmuxStatusFallbackText() string {
	return "status unavailable"
}

func shortCommandState(state proto.CommandState) string {
	switch state {
	case proto.CommandStateRunning:
		return "run"
	case proto.CommandStateExited:
		return "exit"
	case proto.CommandStateFailed:
		return "fail"
	default:
		return "unknown"
	}
}

func upDown(ok bool) string {
	if ok {
		return "up"
	}
	return "down"
}

func humanizeTrafficBytes(value uint64) string {
	const unit = 1024
	switch {
	case value < unit:
		return fmt.Sprintf("%dB", value)
	case value < unit*unit:
		return fmt.Sprintf("%.1fK", float64(value)/unit)
	case value < unit*unit*unit:
		return fmt.Sprintf("%.1fM", float64(value)/(unit*unit))
	default:
		return fmt.Sprintf("%.1fG", float64(value)/(unit*unit*unit))
	}
}
