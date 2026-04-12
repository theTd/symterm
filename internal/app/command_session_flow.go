package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/term"

	"symterm/internal/diagnostic"
	"symterm/internal/projectready"
	"symterm/internal/proto"
	"symterm/internal/transport"
)

var terminalMakeRaw = func(file *os.File) (*term.State, error) {
	return term.MakeRaw(int(file.Fd()))
}

var terminalRestore = func(file *os.File, state *term.State) error {
	return term.Restore(int(file.Fd()), state)
}

func (u ProjectSessionUseCase) ConnectAndStartCommand(ctx context.Context, stream SessionIO) (ProjectSessionResult, error) {
	if u.Lifecycle != nil {
		defer u.Lifecycle.Close()
	}
	u.tracef("connect and start command begin")

	session, err := u.ConnectProjectSession(ctx)
	if err != nil {
		return ProjectSessionResult{}, err
	}
	defer func() {
		session.Close()
	}()
	session, err = u.ConfirmAndResumeProjectSession(ctx, session)
	if err != nil {
		return ProjectSessionResult{}, err
	}

	result := projectSessionResult(session)
	if u.Lifecycle == nil || !u.Lifecycle.HasDedicatedStdio() {
		return ProjectSessionResult{}, proto.NewError(proto.ErrProjectNotReady, "dedicated stdio channel is unavailable")
	}

	ttySpec := DetectTTYSpec(stream.Stdin, stream.Stdout, stream.Stderr, true)
	u.tracef(
		"start_project_command_session argv=%q tty_interactive=%t cols=%d rows=%d tmux_status=%t",
		u.Config.ArgvTail,
		ttySpec.Interactive,
		ttySpec.Columns,
		ttySpec.Rows,
		u.Config.TmuxStatus,
	)
	if err := validateTmuxStatusTTY(u.Config.TmuxStatus, ttySpec); err != nil {
		return ProjectSessionResult{}, err
	}

	var started proto.StartProjectCommandSessionResponse
	if err := u.ControlClient.Call(ctx, "start_project_command_session", session.ClientID, proto.StartProjectCommandSessionRequest{
		ProjectID:  u.Config.ProjectID,
		ArgvTail:   u.Config.ArgvTail,
		TTY:        ttySpec,
		TmuxStatus: u.Config.TmuxStatus,
	}, &started); err != nil {
		return ProjectSessionResult{}, err
	}
	result.Snapshot = started.Snapshot
	u.tracef(
		"start command response state=%s can_start=%t commands=%d cursor=%d command_present=%t",
		started.Snapshot.ProjectState,
		started.Snapshot.CanStartCommands,
		len(started.Snapshot.CommandSnapshots),
		started.Snapshot.CurrentCursor,
		started.Command != nil,
	)
	if started.Command == nil {
		return result, nil
	}
	result.Command = started.Command
	u.tracef("command started command_id=%s", started.Command.CommandID)

	output, events, err := WaitForCommandTerminal(
		ctx,
		u.ControlClient,
		u.Lifecycle.OpenDedicatedPipe,
		u.Tracef,
		session.ClientID,
		started.Command.CommandID,
		stream.Stdin,
		stream.Stdout,
		stream.Stderr,
	)
	if err != nil {
		return ProjectSessionResult{}, err
	}
	result.Events = events
	result.Output = &output
	u.tracef(
		"command terminal complete stdout_bytes=%d stderr_bytes=%d complete=%t events=%d",
		len(output.Stdout),
		len(output.Stderr),
		output.Complete,
		len(events),
	)
	return result, nil
}

func WaitForProjectReady(
	ctx context.Context,
	controlClient *transport.Client,
	clientID string,
	snapshot proto.ProjectSnapshot,
) (proto.ProjectSnapshot, error) {
	if controlClient == nil {
		return snapshot, nil
	}
	return projectready.Wait(ctx, snapshot,
		func(waitCtx context.Context, sinceCursor uint64, onEvent func(proto.ProjectEvent) error) error {
			return controlClient.StreamProjectEvents(waitCtx, clientID, proto.WatchProjectRequest{
				ProjectID:   snapshot.ProjectID,
				SinceCursor: sinceCursor,
			}, onEvent)
		},
		func(refreshCtx context.Context) (proto.ProjectSnapshot, error) {
			var refreshed proto.ProjectSnapshot
			if err := controlClient.Call(refreshCtx, "ensure_project", clientID, proto.EnsureProjectRequest{
				ProjectID: snapshot.ProjectID,
			}, &refreshed); err != nil {
				return proto.ProjectSnapshot{}, err
			}
			return refreshed, nil
		},
	)
}

func ApplyProjectEvent(snapshot proto.ProjectSnapshot, event proto.ProjectEvent) proto.ProjectSnapshot {
	return projectready.ApplyProjectEvent(snapshot, event)
}

func WaitForCommandTerminal(
	ctx context.Context,
	controlClient *transport.Client,
	stdioPipeFactory func(context.Context) (*transport.StdioPipeClient, io.Closer, error),
	tracef func(string, ...any),
	clientID string,
	commandID string,
	streamStdin io.Reader,
	streamStdout io.Writer,
	streamStderr io.Writer,
) (proto.AttachStdioResponse, []proto.CommandEvent, error) {
	if stdioPipeFactory == nil {
		return proto.AttachStdioResponse{}, nil, proto.NewError(proto.ErrProjectNotReady, "dedicated stdio pipe is unavailable")
	}
	tracefOrNoop(tracef, "wait for command terminal command_id=%s", commandID)
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var closeOnce sync.Once
	stdioPipe, closer, err := stdioPipeFactory(runCtx)
	if err != nil {
		return proto.AttachStdioResponse{}, nil, err
	}
	defer closeOnce.Do(func() {
		diagnostic.Cleanup(diagnostic.Default(), "close stdio pipe connection", closer.Close())
	})
	restoreInputMode, err := setTerminalInputMode(streamStdin, streamStdout, streamStderr)
	if err != nil {
		return proto.AttachStdioResponse{}, nil, err
	}
	defer restoreInputMode()
	if controlClient != nil && shouldMonitorTTYResize(streamStdin, streamStdout, streamStderr) {
		resizeCtx, cancelResize := context.WithCancel(ctx)
		defer cancelResize()
		startTTYResizeMonitor(resizeCtx, tracef, streamStdin, streamStdout, streamStderr, func(columns int, rows int) error {
			return controlClient.Call(resizeCtx, "resize_tty", clientID, proto.ResizeTTYRequest{
				CommandID: commandID,
				Columns:   columns,
				Rows:      rows,
			}, nil)
		})
	}

	var combinedStdout bytes.Buffer
	var combinedStderr bytes.Buffer
	var stdoutOffset int64
	var stderrOffset int64
	var finalComplete bool
	attachParams, err := marshalAttachParams(proto.AttachStdioRequest{
		CommandID: commandID,
	})
	if err != nil {
		return proto.AttachStdioResponse{}, nil, err
	}

	writeChunk := func(output proto.AttachStdioResponse) error {
		stdoutOffset = output.StdoutOffset
		stderrOffset = output.StderrOffset
		finalComplete = output.Complete
		if len(output.Stdout) > 0 {
			_, _ = combinedStdout.Write(output.Stdout)
			if err := writeOutputChunk(streamStdout, output.Stdout); err != nil {
				return fmt.Errorf("write command stdout: %w", err)
			}
		}
		if len(output.Stderr) > 0 {
			_, _ = combinedStderr.Write(output.Stderr)
			if err := writeOutputChunk(streamStderr, output.Stderr); err != nil {
				return fmt.Errorf("write command stderr: %w", err)
			}
		}
		return nil
	}

	attachErrCh := make(chan error, 1)
	go func() {
		err := stdioPipe.StreamAttach(runCtx, clientID, transport.Request{
			ID:     1,
			Method: transport.SSHChannelStdio,
			Params: attachParams,
		}, streamStdin, func(data []byte) error {
			tracefOrNoop(tracef, "stdio stdout chunk command_id=%s bytes=%d stdout_offset=%d", commandID, len(data), stdoutOffset+int64(len(data)))
			return writeChunk(proto.AttachStdioResponse{
				Stdout:       data,
				StdoutOffset: stdoutOffset + int64(len(data)),
			})
		}, func(data []byte) error {
			tracefOrNoop(tracef, "stdio stderr chunk command_id=%s bytes=%d stderr_offset=%d", commandID, len(data), stderrOffset+int64(len(data)))
			return writeChunk(proto.AttachStdioResponse{
				Stderr:       data,
				StderrOffset: stderrOffset + int64(len(data)),
			})
		})
		if err == nil {
			finalComplete = true
		} else if !isExitedCommandAttachError(err) {
			cancelRun()
		}
		attachErrCh <- err
	}()

	var events []proto.CommandEvent
	eventErrCh := make(chan error, 1)
	go func() {
		err := controlClient.StreamCommandEvents(runCtx, clientID, proto.WatchCommandRequest{
			CommandID: commandID,
		}, func(event proto.CommandEvent) error {
			tracefOrNoop(
				tracef,
				"command event command_id=%s type=%s exit=%s message=%q",
				commandID,
				event.Type,
				formatEventExitCode(event.ExitCode),
				event.Message,
			)
			events = append(events, event)
			return nil
		})
		if err != nil {
			cancelRun()
		}
		eventErrCh <- err
	}()

	attachErr := <-attachErrCh
	eventErr := <-eventErrCh
	if attachErr != nil {
		tracefOrNoop(tracef, "stdio stream returned err=%v", attachErr)
	}
	if shouldIgnoreTerminalFlowError(attachErr, eventErr) {
		attachErr = nil
	}
	if shouldIgnoreTerminalFlowError(eventErr, attachErr) {
		eventErr = nil
	}
	if eventErr != nil {
		return proto.AttachStdioResponse{}, nil, eventErr
	}
	if attachErr != nil {
		if !isExitedCommandAttachError(attachErr) || !HasTerminalCommandEvent(events) {
			return proto.AttachStdioResponse{}, nil, attachErr
		}
	}
	tracefOrNoop(
		tracef,
		"command terminal finalized command_id=%s stdout_bytes=%d stderr_bytes=%d complete=%t",
		commandID,
		combinedStdout.Len(),
		combinedStderr.Len(),
		finalComplete,
	)
	return proto.AttachStdioResponse{
		Stdout:       combinedStdout.Bytes(),
		Stderr:       combinedStderr.Bytes(),
		StdoutOffset: stdoutOffset,
		StderrOffset: stderrOffset,
		Complete:     finalComplete,
	}, events, nil
}

func HasTerminalCommandEvent(events []proto.CommandEvent) bool {
	_, ok := LatestTerminalCommandEvent(events)
	return ok
}

func LatestTerminalCommandEvent(events []proto.CommandEvent) (proto.CommandEvent, bool) {
	for idx := len(events) - 1; idx >= 0; idx-- {
		switch events[idx].Type {
		case proto.CommandEventExited, proto.CommandEventExecFailed:
			return events[idx], true
		}
	}
	return proto.CommandEvent{}, false
}

func DetectTTYSpec(stdin io.Reader, stdout io.Writer, stderr io.Writer, supportsInteractive bool) proto.TTYSpec {
	spec := proto.TTYSpec{}
	if !supportsInteractive {
		return spec
	}
	if !isTerminalLike(stdin) || !isTerminalLike(stdout) || !isTerminalLike(stderr) {
		return spec
	}
	spec.Interactive = true
	spec.Columns, spec.Rows = resolveTTYDimensions(stdin, stdout, stderr)
	return spec
}

func validateTmuxStatusTTY(tmuxStatus bool, ttySpec proto.TTYSpec) error {
	if tmuxStatus && !ttySpec.Interactive {
		return proto.NewError(proto.ErrInvalidArgument, "tmux status mode requires an interactive TTY")
	}
	return nil
}

func resolveTTYDimensions(stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, int) {
	for _, stream := range []any{stdout, stderr, stdin} {
		file, ok := stream.(*os.File)
		if !ok || file == nil {
			continue
		}
		columns, rows, ok := terminalSize(file)
		if ok {
			return columns, rows
		}
	}

	columns := envInt("COLUMNS")
	rows := envInt("LINES")
	if columns <= 0 {
		columns = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return columns, rows
}

var ttyResizePollInterval = 250 * time.Millisecond

func shouldMonitorTTYResize(stdin io.Reader, stdout io.Writer, stderr io.Writer) bool {
	return isTerminalLike(stdin) && isTerminalLike(stdout) && isTerminalLike(stderr)
}

func setTerminalInputMode(stdin io.Reader, stdout io.Writer, stderr io.Writer) (func(), error) {
	if !shouldMonitorTTYResize(stdin, stdout, stderr) {
		return func() {}, nil
	}
	file, ok := stdin.(*os.File)
	if !ok || file == nil {
		return func() {}, nil
	}
	state, err := terminalMakeRaw(file)
	if err != nil {
		return nil, err
	}
	return func() {
		diagnostic.Cleanup(diagnostic.Default(), "restore terminal input mode", terminalRestore(file, state))
	}, nil
}

func startTTYResizeMonitor(
	ctx context.Context,
	tracef func(string, ...any),
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	onResize func(int, int) error,
) {
	if ctx == nil || onResize == nil {
		return
	}

	lastColumns, lastRows := resolveTTYDimensions(stdin, stdout, stderr)
	go func() {
		ticker := time.NewTicker(ttyResizePollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			columns, rows := resolveTTYDimensions(stdin, stdout, stderr)
			if columns == lastColumns && rows == lastRows {
				continue
			}
			if err := onResize(columns, rows); err != nil {
				tracefOrNoop(tracef, "resize_tty failed cols=%d rows=%d err=%v", columns, rows, err)
				if shouldStopTTYResize(err) {
					return
				}
				continue
			}
			lastColumns = columns
			lastRows = rows
			tracefOrNoop(tracef, "resize_tty applied cols=%d rows=%d", columns, rows)
		}
	}()
}

func shouldStopTTYResize(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) {
		return false
	}
	switch protoErr.Code {
	case proto.ErrUnknownCommand, proto.ErrProjectTerminated, proto.ErrUnknownClient:
		return true
	default:
		return false
	}
}

func isExitedCommandAttachError(err error) bool {
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) {
		return false
	}
	return protoErr.Code == proto.ErrUnknownCommand
}

func marshalAttachParams(request proto.AttachStdioRequest) (json.RawMessage, error) {
	return json.Marshal(request)
}

func writeOutputChunk(writer io.Writer, data []byte) error {
	if writer == nil || len(data) == 0 {
		return nil
	}
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func shouldIgnoreTerminalFlowError(err error, sibling error) bool {
	if err == nil || sibling == nil {
		return false
	}
	if !isTerminalFlowCancellation(err) {
		return false
	}
	return !isTerminalFlowCancellation(sibling)
}

func isTerminalFlowCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isTerminalLike(stream any) bool {
	file, ok := stream.(*os.File)
	if !ok || file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func envInt(key string) int {
	value := os.Getenv(key)
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func tracefOrNoop(tracef func(string, ...any), format string, args ...any) {
	if tracef == nil {
		return
	}
	tracef(format, args...)
}

func formatEventExitCode(exitCode *int) string {
	if exitCode == nil {
		return "-"
	}
	return strconv.Itoa(*exitCode)
}
