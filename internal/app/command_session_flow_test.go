package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/term"

	"symterm/internal/proto"
	"symterm/internal/transport"
)

func TestResolveTTYDimensionsFallsBackToEnvAndDefaults(t *testing.T) {
	t.Setenv("COLUMNS", "132")
	t.Setenv("LINES", "40")

	columns, rows := resolveTTYDimensions(bytes.NewReader(nil), io.Discard, io.Discard)
	if columns != 132 || rows != 40 {
		t.Fatalf("resolveTTYDimensions() = %dx%d, want 132x40", columns, rows)
	}

	t.Setenv("COLUMNS", "")
	t.Setenv("LINES", "")
	columns, rows = resolveTTYDimensions(bytes.NewReader(nil), io.Discard, io.Discard)
	if columns != 80 || rows != 24 {
		t.Fatalf("resolveTTYDimensions() defaults = %dx%d, want 80x24", columns, rows)
	}
}

func TestDetectTTYSpecNonInteractiveStreamsReturnEmptySpec(t *testing.T) {
	spec := DetectTTYSpec(bytes.NewReader(nil), io.Discard, io.Discard, true)
	if spec.Interactive || spec.Columns != 0 || spec.Rows != 0 {
		t.Fatalf("DetectTTYSpec() = %#v, want zero spec", spec)
	}
}

func TestValidateTmuxStatusTTYRejectsNonInteractiveMode(t *testing.T) {
	t.Parallel()

	err := validateTmuxStatusTTY(true, proto.TTYSpec{})
	if err == nil {
		t.Fatal("validateTmuxStatusTTY() error = nil")
	}
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) {
		t.Fatalf("validateTmuxStatusTTY() error = %T, want *proto.Error", err)
	}
	if protoErr.Code != proto.ErrInvalidArgument {
		t.Fatalf("validateTmuxStatusTTY() code = %q, want %q", protoErr.Code, proto.ErrInvalidArgument)
	}
}

func TestSetTerminalInputModeMakesRawAndRestores(t *testing.T) {
	stdin, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile(os.DevNull) error = %v", err)
	}
	defer stdin.Close()

	stdout, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile(os.DevNull) error = %v", err)
	}
	defer stdout.Close()

	stderr, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile(os.DevNull) error = %v", err)
	}
	defer stderr.Close()

	originalMakeRaw := terminalMakeRaw
	originalRestore := terminalRestore
	defer func() {
		terminalMakeRaw = originalMakeRaw
		terminalRestore = originalRestore
	}()

	var makeRawCalls atomic.Int64
	var restoreCalls atomic.Int64
	state := &term.State{}
	terminalMakeRaw = func(file *os.File) (*term.State, error) {
		makeRawCalls.Add(1)
		if file != stdin {
			t.Fatalf("terminalMakeRaw() file = %v, want stdin", file)
		}
		return state, nil
	}
	terminalRestore = func(file *os.File, restored *term.State) error {
		restoreCalls.Add(1)
		if file != stdin {
			t.Fatalf("terminalRestore() file = %v, want stdin", file)
		}
		if restored != state {
			t.Fatalf("terminalRestore() state = %p, want %p", restored, state)
		}
		return nil
	}

	restore, err := setTerminalInputMode(stdin, stdout, stderr)
	if err != nil {
		t.Fatalf("setTerminalInputMode() error = %v", err)
	}
	if got := makeRawCalls.Load(); got != 1 {
		t.Fatalf("terminalMakeRaw calls = %d, want 1", got)
	}

	restore()
	if got := restoreCalls.Load(); got != 1 {
		t.Fatalf("terminalRestore calls = %d, want 1", got)
	}
}

func TestSetTerminalInputModeSkipsNonTerminalStreams(t *testing.T) {
	originalMakeRaw := terminalMakeRaw
	originalRestore := terminalRestore
	defer func() {
		terminalMakeRaw = originalMakeRaw
		terminalRestore = originalRestore
	}()

	var makeRawCalls atomic.Int64
	var restoreCalls atomic.Int64
	terminalMakeRaw = func(file *os.File) (*term.State, error) {
		makeRawCalls.Add(1)
		return &term.State{}, nil
	}
	terminalRestore = func(file *os.File, state *term.State) error {
		restoreCalls.Add(1)
		return nil
	}

	restore, err := setTerminalInputMode(bytes.NewReader(nil), io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("setTerminalInputMode() error = %v", err)
	}
	restore()

	if got := makeRawCalls.Load(); got != 0 {
		t.Fatalf("terminalMakeRaw calls = %d, want 0", got)
	}
	if got := restoreCalls.Load(); got != 0 {
		t.Fatalf("terminalRestore calls = %d, want 0", got)
	}
}

func TestResolveTTYDimensionsUsesMeasuredSizeBeforeEnv(t *testing.T) {
	stdout, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile(os.DevNull) error = %v", err)
	}
	defer stdout.Close()

	original := terminalSize
	terminalSize = func(file *os.File) (int, int, bool) {
		if file == stdout {
			return 101, 55, true
		}
		return 0, 0, false
	}
	defer func() {
		terminalSize = original
	}()

	t.Setenv("COLUMNS", "132")
	t.Setenv("LINES", "40")
	columns, rows := resolveTTYDimensions(bytes.NewReader(nil), stdout, io.Discard)
	if columns != 101 || rows != 55 {
		t.Fatalf("resolveTTYDimensions() = %dx%d, want 101x55", columns, rows)
	}
}

func TestStartTTYResizeMonitorReportsSizeChanges(t *testing.T) {
	stdout, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile(os.DevNull) error = %v", err)
	}
	defer stdout.Close()

	originalTerminalSize := terminalSize
	originalPollInterval := ttyResizePollInterval
	defer func() {
		terminalSize = originalTerminalSize
		ttyResizePollInterval = originalPollInterval
	}()

	var width atomic.Int64
	var height atomic.Int64
	width.Store(80)
	height.Store(24)
	terminalSize = func(file *os.File) (int, int, bool) {
		if file == stdout {
			return int(width.Load()), int(height.Load()), true
		}
		return 0, 0, false
	}
	ttyResizePollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resized := make(chan [2]int, 1)
	startTTYResizeMonitor(ctx, nil, bytes.NewReader(nil), stdout, io.Discard, func(columns int, rows int) error {
		resized <- [2]int{columns, rows}
		return nil
	})

	width.Store(132)
	height.Store(40)

	select {
	case got := <-resized:
		if got != [2]int{132, 40} {
			t.Fatalf("resize = %#v, want [132 40]", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for resize event")
	}
}

func TestStartTTYResizeMonitorStopsOnUnknownCommand(t *testing.T) {
	stdout, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile(os.DevNull) error = %v", err)
	}
	defer stdout.Close()

	originalTerminalSize := terminalSize
	originalPollInterval := ttyResizePollInterval
	defer func() {
		terminalSize = originalTerminalSize
		ttyResizePollInterval = originalPollInterval
	}()

	var width atomic.Int64
	var height atomic.Int64
	width.Store(80)
	height.Store(24)
	terminalSize = func(file *os.File) (int, int, bool) {
		if file == stdout {
			return int(width.Load()), int(height.Load()), true
		}
		return 0, 0, false
	}
	ttyResizePollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int64
	startTTYResizeMonitor(ctx, nil, bytes.NewReader(nil), stdout, io.Discard, func(columns int, rows int) error {
		calls.Add(1)
		return proto.NewError(proto.ErrUnknownCommand, "command is not running")
	})

	width.Store(132)
	height.Store(40)
	time.Sleep(100 * time.Millisecond)
	width.Store(140)
	height.Store(45)
	time.Sleep(100 * time.Millisecond)

	if got := calls.Load(); got != 1 {
		t.Fatalf("resize calls = %d, want 1", got)
	}
}

func TestWaitForCommandTerminalReturnsStdoutWriteError(t *testing.T) {
	t.Parallel()

	writeErr := errors.New("stdout sink failed")
	err := waitForCommandTerminalWithBrokenWriter(t, testStdioFrameStdout, &failingWriter{err: writeErr}, io.Discard)
	if !errors.Is(err, writeErr) {
		t.Fatalf("WaitForCommandTerminal() error = %v, want %v", err, writeErr)
	}
	if !strings.Contains(err.Error(), "stdout") {
		t.Fatalf("WaitForCommandTerminal() error = %q, want stdout context", err)
	}
}

func TestWaitForCommandTerminalReturnsStderrWriteError(t *testing.T) {
	t.Parallel()

	writeErr := errors.New("stderr sink failed")
	err := waitForCommandTerminalWithBrokenWriter(t, testStdioFrameStderr, io.Discard, &failingWriter{err: writeErr})
	if !errors.Is(err, writeErr) {
		t.Fatalf("WaitForCommandTerminal() error = %v, want %v", err, writeErr)
	}
	if !strings.Contains(err.Error(), "stderr") {
		t.Fatalf("WaitForCommandTerminal() error = %q, want stderr context", err)
	}
}

const (
	testStdioFrameStdout byte = 1
	testStdioFrameStderr byte = 2
)

type failingWriter struct {
	err error
}

func (w *failingWriter) Write(_ []byte) (int, error) {
	return 0, w.err
}

func waitForCommandTerminalWithBrokenWriter(t *testing.T, frameType byte, stdout io.Writer, stderr io.Writer) error {
	t.Helper()

	eventsStarted := make(chan struct{})
	controlClient, closeControl := newPendingCommandEventClient(t, eventsStarted)
	defer closeControl()

	stdioFactory, closeStdio := newSingleFrameStdioPipeFactory(t, frameType, []byte("chunk"), eventsStarted)
	defer closeStdio()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, _, err := WaitForCommandTerminal(
		ctx,
		controlClient,
		stdioFactory,
		nil,
		"client-1",
		"cmd-1",
		bytes.NewReader(nil),
		stdout,
		stderr,
	)
	return err
}

func newPendingCommandEventClient(t *testing.T, eventsStarted chan<- struct{}) (*transport.Client, func()) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	stop := make(chan struct{})
	done := make(chan struct{})
	var startOnce sync.Once
	signalStart := func() {
		startOnce.Do(func() {
			close(eventsStarted)
		})
	}
	go func() {
		defer close(done)
		defer serverConn.Close()
		defer signalStart()

		reader := bufio.NewReader(serverConn)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}

		var request transport.Request
		if err := json.Unmarshal(line, &request); err != nil {
			t.Errorf("json.Unmarshal(request) error = %v", err)
			return
		}
		if request.Method != "watch_command_stream" {
			t.Errorf("request.Method = %q, want watch_command_stream", request.Method)
			return
		}

		signalStart()
		<-stop
	}()

	return transport.NewClient(clientConn, clientConn), func() {
		close(stop)
		_ = clientConn.Close()
		_ = serverConn.Close()
		<-done
	}
}

func newSingleFrameStdioPipeFactory(
	t *testing.T,
	frameType byte,
	payload []byte,
	start <-chan struct{},
) (func(context.Context) (*transport.StdioPipeClient, io.Closer, error), func()) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer serverConn.Close()
		<-start
		if err := writeTestStdioFrame(serverConn, frameType, payload); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("writeTestStdioFrame() error = %v", err)
		}
	}()

	return func(context.Context) (*transport.StdioPipeClient, io.Closer, error) {
			client := transport.NewSSHStdioPipeClient(func(context.Context, string, string) (io.ReadWriteCloser, error) {
				return clientConn, nil
			})
			return client, clientConn, nil
		}, func() {
			_ = clientConn.Close()
			_ = serverConn.Close()
			<-done
		}
}

func writeTestStdioFrame(writer io.Writer, frameType byte, payload []byte) error {
	header := make([]byte, 5)
	header[0] = frameType
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := writer.Write(header); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := writer.Write(payload)
	return err
}
