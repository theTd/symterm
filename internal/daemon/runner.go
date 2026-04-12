package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"symterm/internal/diagnostic"
	"symterm/internal/proto"
)

type LaunchRequest struct {
	ProjectKey proto.ProjectKey
	Command    proto.CommandSnapshot
}

type RuntimeManager struct {
	mu                 sync.Mutex
	projectsRoot       string
	adminSocketPath    string
	entrypoint         []string
	resolveEntrypoint  func(string, []string) []string
	mounts             *MountManager
	active             map[string]*activeCommand
	startupWaiters     map[string]chan struct{}
	nextOutputWaiterID uint64
	outputWaiters      map[string]map[uint64]chan struct{}
}

type activeCommand struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdinMu     sync.Mutex
	stdinClosed bool
	columns     int
	rows        int
	interactive bool
	ptyFile     *os.File
}

func NewRuntimeManager(projectsRoot string, entrypoint []string, mounts *MountManager) *RuntimeManager {
	return &RuntimeManager{
		projectsRoot:      projectsRoot,
		adminSocketPath:   filepath.Join(projectsRoot, "admin.sock"),
		entrypoint:        append([]string(nil), entrypoint...),
		resolveEntrypoint: func(_ string, fallback []string) []string { return append([]string(nil), fallback...) },
		mounts:            mounts,
		active:            make(map[string]*activeCommand),
		startupWaiters:    make(map[string]chan struct{}),
		outputWaiters:     make(map[string]map[uint64]chan struct{}),
	}
}

func (m *RuntimeManager) SetEntrypointResolver(resolver func(string, []string) []string) {
	if resolver == nil {
		return
	}
	m.mu.Lock()
	m.resolveEntrypoint = resolver
	m.mu.Unlock()
}

func (m *RuntimeManager) SetAdminSocketPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	m.mu.Lock()
	m.adminSocketPath = path
	m.mu.Unlock()
}

func (m *RuntimeManager) Launch(
	ctx context.Context,
	request LaunchRequest,
	onExit func(int),
	onFailure func(string),
) {
	startupWait := m.beginStartupWait(runtimeKey(request.ProjectKey, request.Command.CommandID))
	go m.launch(ctx, request, startupWait, onExit, onFailure)
}

func (m *RuntimeManager) launch(
	ctx context.Context,
	request LaunchRequest,
	startupWait chan struct{},
	onExit func(int),
	onFailure func(string),
) {
	commandKey := runtimeKey(request.ProjectKey, request.Command.CommandID)
	failLaunch := func(err error) {
		m.finishStartupWait(commandKey, startupWait)
		onFailure(err.Error())
	}

	entrypoint := m.effectiveEntrypoint(request.ProjectKey.Username)
	if len(entrypoint) == 0 {
		m.finishStartupWait(commandKey, startupWait)
		onFailure("remote entrypoint is empty")
		return
	}

	layout, err := ResolveProjectLayout(m.projectsRoot, request.ProjectKey)
	if err != nil {
		failLaunch(err)
		return
	}
	commandLayout, err := layout.ResolveCommandLayout(request.Command.CommandID)
	if err != nil {
		failLaunch(err)
		return
	}
	if err := os.MkdirAll(commandLayout.Dir, 0o755); err != nil {
		failLaunch(err)
		return
	}
	if err := os.Remove(commandLayout.ExitCodePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		failLaunch(err)
		return
	}

	stdoutFile, err := os.Create(commandLayout.StdoutPath)
	if err != nil {
		failLaunch(err)
		return
	}

	stderrFile, err := os.Create(commandLayout.StderrPath)
	if err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "close stdout log after stderr open failure", stdoutFile.Close())
		failLaunch(err)
		return
	}
	failLaunchWithLogs := func(err error) {
		diagnostic.Cleanup(diagnostic.Default(), "close stdout log after pre-start failure", stdoutFile.Close())
		diagnostic.Cleanup(diagnostic.Default(), "close stderr log after pre-start failure", stderrFile.Close())
		failLaunch(err)
	}

	commandBinary := entrypoint[0]
	args := append(append([]string(nil), entrypoint[1:]...), request.Command.ArgvTail...)
	workdir := layout.WorkspaceDir
	if m.mounts != nil {
		workdir, err = m.mounts.WorkDir(request.ProjectKey)
		if err != nil {
			failLaunchWithLogs(err)
			return
		}
	}
	if request.Command.TmuxStatus {
		plan, err := buildTmuxLaunchPlan(m.currentAdminSocketPath(), request.ProjectKey, request.Command, entrypoint)
		if err != nil {
			failLaunchWithLogs(err)
			return
		}
		commandBinary = plan.binary
		args = plan.args
	}
	cmd := exec.CommandContext(ctx, commandBinary, args...)
	cmd.Dir = workdir
	cmd.Env = commandEnvironment(os.Environ(), request.Command.TTY)
	active := &activeCommand{
		cmd:         cmd,
		columns:     request.Command.TTY.Columns,
		rows:        request.Command.TTY.Rows,
		interactive: request.Command.TTY.Interactive,
	}
	copyDone := make(chan error, 1)

	if request.Command.TTY.Interactive {
		ptmx, err := startCommandPTY(cmd, request.Command.TTY.Columns, request.Command.TTY.Rows)
		if err != nil {
			diagnostic.Cleanup(diagnostic.Default(), "close stdout log after PTY start failure", stdoutFile.Close())
			diagnostic.Cleanup(diagnostic.Default(), "close stderr log after PTY start failure", stderrFile.Close())
			failLaunch(err)
			return
		}
		active.stdin = ptmx
		active.ptyFile = ptmx
		go func() {
			_, err := io.Copy(notifyingWriter{writer: stdoutFile, notify: func() {
				m.notifyOutputWaiters(commandKey)
			}}, ptmx)
			copyDone <- err
		}()
	} else {
		cmd.Stdout = notifyingWriter{writer: stdoutFile, notify: func() {
			m.notifyOutputWaiters(commandKey)
		}}
		cmd.Stderr = notifyingWriter{writer: stderrFile, notify: func() {
			m.notifyOutputWaiters(commandKey)
		}}
		stdinPipe, err := cmd.StdinPipe()
		if err != nil {
			diagnostic.Cleanup(diagnostic.Default(), "close stdout log after stdin pipe failure", stdoutFile.Close())
			diagnostic.Cleanup(diagnostic.Default(), "close stderr log after stdin pipe failure", stderrFile.Close())
			failLaunch(err)
			return
		}
		if err := cmd.Start(); err != nil {
			diagnostic.Cleanup(diagnostic.Default(), "close stdin pipe after command start failure", stdinPipe.Close())
			diagnostic.Cleanup(diagnostic.Default(), "close stdout log after command start failure", stdoutFile.Close())
			diagnostic.Cleanup(diagnostic.Default(), "close stderr log after command start failure", stderrFile.Close())
			failLaunch(err)
			return
		}
		active.stdin = stdinPipe
	}
	m.track(request, active)
	m.finishStartupWait(commandKey, startupWait)
	defer m.untrack(request)
	defer active.closeInput()

	waitErr := cmd.Wait()
	if active.ptyFile != nil {
		diagnostic.Cleanup(diagnostic.Default(), "close PTY for "+commandKey, active.ptyFile.Close())
		if copyErr := <-copyDone; copyErr != nil && !errors.Is(copyErr, os.ErrClosed) && !errors.Is(copyErr, io.EOF) && !errors.Is(copyErr, syscall.EIO) {
			onFailure(copyErr.Error())
			return
		}
	}
	exitCode := cmd.ProcessState.ExitCode()
	diagnostic.Cleanup(diagnostic.Default(), "close stdout log for "+commandKey, stdoutFile.Close())
	diagnostic.Cleanup(diagnostic.Default(), "close stderr log for "+commandKey, stderrFile.Close())
	if err := os.WriteFile(commandLayout.ExitCodePath, []byte(strconv.Itoa(exitCode)), 0o644); err != nil {
		onFailure(err.Error())
		return
	}
	m.notifyOutputWaiters(commandKey)

	if waitErr != nil && exitCode == -1 {
		onFailure(waitErr.Error())
		return
	}
	onExit(exitCode)
}

func (m *RuntimeManager) effectiveEntrypoint(username string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resolveEntrypoint(username, m.entrypoint)
}

func (m *RuntimeManager) currentAdminSocketPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.adminSocketPath
}

func (m *RuntimeManager) AttachStdio(projectKey proto.ProjectKey, request proto.AttachStdioRequest) (proto.AttachStdioResponse, error) {
	layout, err := CommandLogPaths(m.projectsRoot, projectKey, request.CommandID)
	if err != nil {
		return proto.AttachStdioResponse{}, err
	}

	stdoutData, stdoutOffset, err := readFromOffset(layout.StdoutPath, request.StdoutOffset)
	if err != nil {
		return proto.AttachStdioResponse{}, err
	}
	stderrData, stderrOffset, err := readFromOffset(layout.StderrPath, request.StderrOffset)
	if err != nil {
		return proto.AttachStdioResponse{}, err
	}

	complete := false
	if _, err := os.Stat(layout.ExitCodePath); err == nil {
		complete = true
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return proto.AttachStdioResponse{}, err
	}

	return proto.AttachStdioResponse{
		Stdout:       stdoutData,
		Stderr:       stderrData,
		StdoutOffset: stdoutOffset,
		StderrOffset: stderrOffset,
		Complete:     complete,
	}, nil
}

func (m *RuntimeManager) WaitOutput(ctx context.Context, projectKey proto.ProjectKey, request proto.AttachStdioRequest) error {
	layout, err := CommandLogPaths(m.projectsRoot, projectKey, request.CommandID)
	if err != nil {
		return err
	}
	key := runtimeKey(projectKey, request.CommandID)
	waitCh, waiterID := m.subscribeOutputWaiter(key)
	defer m.unsubscribeOutputWaiter(key, waiterID)

	ready, err := stdioReady(layout, request)
	if err != nil {
		return err
	}
	if ready {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-waitCh:
		return nil
	}
}

func (m *RuntimeManager) SendSignal(projectKey proto.ProjectKey, commandID string, name string) error {
	key := runtimeKey(projectKey, commandID)

	m.mu.Lock()
	active, ok := m.active[key]
	m.mu.Unlock()
	if !ok || active.cmd == nil || active.cmd.Process == nil {
		return proto.NewError(proto.ErrUnknownCommand, "command is not running")
	}

	signalName := strings.ToUpper(strings.TrimSpace(name))
	if signalName == "" {
		signalName = "TERM"
	}
	if runtime.GOOS == "windows" {
		return active.cmd.Process.Kill()
	}

	switch signalName {
	case "TERM":
		return active.cmd.Process.Signal(syscall.SIGTERM)
	case "INT", "INTERRUPT":
		return active.cmd.Process.Signal(syscall.SIGINT)
	case "KILL":
		return active.cmd.Process.Kill()
	default:
		return proto.NewError(proto.ErrInvalidArgument, "unsupported signal")
	}
}

func (m *RuntimeManager) ResizeTTY(projectKey proto.ProjectKey, commandID string, columns int, rows int) error {
	active, err := m.lookupActive(projectKey, commandID)
	if err != nil {
		return err
	}
	active.stdinMu.Lock()
	active.columns = columns
	active.rows = rows
	ptyFile := active.ptyFile
	active.stdinMu.Unlock()
	if ptyFile != nil {
		return resizeCommandPTY(ptyFile, columns, rows)
	}
	return nil
}

func (m *RuntimeManager) WriteStdin(projectKey proto.ProjectKey, commandID string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	active, err := m.waitForActive(ctx, projectKey, commandID)
	if err != nil {
		return err
	}
	return active.writeInput(data)
}

func (m *RuntimeManager) CloseStdin(projectKey proto.ProjectKey, commandID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	active, err := m.waitForActive(ctx, projectKey, commandID)
	if err != nil {
		return err
	}
	return active.closeInput()
}

func (m *RuntimeManager) StopProject(projectKey proto.ProjectKey) error {
	prefix := projectKey.String() + ":"
	return m.stopMatchingProjects(func(key string) bool {
		return strings.HasPrefix(key, prefix)
	})
}

func (m *RuntimeManager) StopAllProjects() error {
	return m.stopMatchingProjects(func(string) bool {
		return true
	})
}

func (m *RuntimeManager) stopMatchingProjects(match func(string) bool) error {
	if match == nil {
		return nil
	}

	var (
		commands      []*activeCommand
		startupWaits  []chan struct{}
		outputWaiters []chan struct{}
	)

	m.mu.Lock()
	for key, active := range m.active {
		if match(key) {
			commands = append(commands, active)
		}
	}
	for key, waitCh := range m.startupWaiters {
		if !match(key) {
			continue
		}
		delete(m.startupWaiters, key)
		startupWaits = append(startupWaits, waitCh)
	}
	for key, watchers := range m.outputWaiters {
		if !match(key) {
			continue
		}
		for watcherID, ch := range watchers {
			delete(watchers, watcherID)
			outputWaiters = append(outputWaiters, ch)
		}
		delete(m.outputWaiters, key)
	}
	m.mu.Unlock()

	var errs []error
	for _, active := range commands {
		if active == nil || active.cmd == nil || active.cmd.Process == nil {
			continue
		}
		if err := active.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			errs = append(errs, err)
		}
	}
	for _, waitCh := range startupWaits {
		close(waitCh)
	}
	for _, ch := range outputWaiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return errors.Join(errs...)
}

func CommandLogPaths(projectsRoot string, key proto.ProjectKey, commandID string) (CommandLayout, error) {
	layout, err := ResolveProjectLayout(projectsRoot, key)
	if err != nil {
		return CommandLayout{}, err
	}
	return layout.ResolveCommandLayout(commandID)
}

func (m *RuntimeManager) track(request LaunchRequest, active *activeCommand) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := runtimeKey(request.ProjectKey, request.Command.CommandID)
	m.active[key] = active
	if _, ok := m.outputWaiters[key]; !ok {
		m.outputWaiters[key] = make(map[uint64]chan struct{})
	}
}

func (m *RuntimeManager) untrack(request LaunchRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.active, runtimeKey(request.ProjectKey, request.Command.CommandID))
}

func runtimeKey(projectKey proto.ProjectKey, commandID string) string {
	return projectKey.String() + ":" + commandID
}

func (m *RuntimeManager) lookupActive(projectKey proto.ProjectKey, commandID string) (*activeCommand, error) {
	key := runtimeKey(projectKey, commandID)

	m.mu.Lock()
	active, ok := m.active[key]
	m.mu.Unlock()
	if !ok || active == nil || active.cmd == nil || active.cmd.Process == nil {
		return nil, proto.NewError(proto.ErrUnknownCommand, "command is not running")
	}
	return active, nil
}

func (m *RuntimeManager) waitForActive(ctx context.Context, projectKey proto.ProjectKey, commandID string) (*activeCommand, error) {
	key := runtimeKey(projectKey, commandID)

	active, waitCh, err := m.lookupActiveOrStartupWaiter(key)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return active, nil
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-waitCh:
	}

	return m.lookupActive(projectKey, commandID)
}

func (m *RuntimeManager) lookupActiveOrStartupWaiter(key string) (*activeCommand, <-chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	active, ok := m.active[key]
	if ok && active != nil && active.cmd != nil && active.cmd.Process != nil {
		return active, nil, nil
	}
	waitCh := m.startupWaiters[key]
	if waitCh == nil {
		return nil, nil, proto.NewError(proto.ErrUnknownCommand, "command is not running")
	}
	return nil, waitCh, nil
}

func (m *RuntimeManager) beginStartupWait(key string) chan struct{} {
	readyCh := make(chan struct{})

	m.mu.Lock()
	existing := m.startupWaiters[key]
	m.startupWaiters[key] = readyCh
	m.mu.Unlock()

	if existing != nil {
		close(existing)
	}

	return readyCh
}

func (m *RuntimeManager) finishStartupWait(key string, readyCh chan struct{}) {
	m.mu.Lock()
	waitCh := m.startupWaiters[key]
	if waitCh == readyCh {
		delete(m.startupWaiters, key)
	} else {
		waitCh = nil
	}
	m.mu.Unlock()

	if waitCh != nil {
		close(waitCh)
	}
}

func (c *activeCommand) writeInput(data []byte) error {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()

	if c.stdin == nil || c.stdinClosed {
		return proto.NewError(proto.ErrUnknownCommand, "command stdin is closed")
	}
	if len(data) == 0 {
		return nil
	}
	_, err := c.stdin.Write(data)
	return err
}

func (c *activeCommand) closeInput() error {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()

	if c.stdin == nil || c.stdinClosed {
		return nil
	}
	c.stdinClosed = true
	if c.interactive {
		_, err := c.stdin.Write([]byte{4})
		diagnostic.Cleanup(diagnostic.Default(), "send PTY EOF", err)
		return nil
	}
	return c.stdin.Close()
}

func readFromOffset(path string, offset int64) ([]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, offset, nil
		}
		return nil, offset, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, offset, err
	}
	size := info.Size()
	if offset < 0 {
		offset = 0
	}
	if offset > size {
		offset = size
	}

	buf := make([]byte, size-offset)
	n, err := file.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, offset, err
	}
	return buf[:n], offset + int64(n), nil
}

type notifyingWriter struct {
	writer io.Writer
	notify func()
}

func (w notifyingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 && w.notify != nil {
		w.notify()
	}
	return n, err
}

func (m *RuntimeManager) subscribeOutputWaiter(key string) (<-chan struct{}, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextOutputWaiterID++
	waiterID := m.nextOutputWaiterID
	waiters := m.outputWaiters[key]
	if waiters == nil {
		waiters = make(map[uint64]chan struct{})
		m.outputWaiters[key] = waiters
	}
	ch := make(chan struct{}, 1)
	waiters[waiterID] = ch
	return ch, waiterID
}

func (m *RuntimeManager) unsubscribeOutputWaiter(key string, waiterID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	waiters := m.outputWaiters[key]
	if waiters == nil {
		return
	}
	ch, ok := waiters[waiterID]
	if !ok {
		return
	}
	delete(waiters, waiterID)
	close(ch)
}

func (m *RuntimeManager) notifyOutputWaiters(key string) {
	m.mu.Lock()
	waiters := m.outputWaiters[key]
	channels := make([]chan struct{}, 0, len(waiters))
	for _, ch := range waiters {
		channels = append(channels, ch)
	}
	m.mu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func stdioReady(layout CommandLayout, request proto.AttachStdioRequest) (bool, error) {
	stdoutReady, err := fileHasBytesAfter(layout.StdoutPath, request.StdoutOffset)
	if err != nil {
		return false, err
	}
	if stdoutReady {
		return true, nil
	}
	stderrReady, err := fileHasBytesAfter(layout.StderrPath, request.StderrOffset)
	if err != nil {
		return false, err
	}
	if stderrReady {
		return true, nil
	}
	if _, err := os.Stat(layout.ExitCodePath); err == nil {
		return true, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return false, nil
}

func fileHasBytesAfter(path string, offset int64) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return info.Size() > offset, nil
}

func commandEnvironment(base []string, tty proto.TTYSpec) []string {
	env := append([]string(nil), base...)
	if !tty.Interactive {
		return env
	}

	env = upsertEnv(env, "TERM", "xterm-256color")
	env = upsertEnv(env, "COLORTERM", "truecolor")
	env = upsertEnv(env, "LINES", strconv.Itoa(normalizeTerminalDimension(tty.Rows, 24)))
	env = upsertEnv(env, "COLUMNS", strconv.Itoa(normalizeTerminalDimension(tty.Columns, 80)))
	return env
}

func upsertEnv(env []string, key string, value string) []string {
	prefix := key + "="
	for idx, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[idx] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func normalizeTerminalDimension(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
