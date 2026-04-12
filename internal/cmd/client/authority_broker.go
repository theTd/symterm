package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"symterm/internal/app"
	"symterm/internal/config"
	"symterm/internal/proto"
	endpointssh "symterm/internal/ssh"
	workspacesync "symterm/internal/sync"
	"symterm/internal/transport"
	"symterm/internal/workspaceidentity"
)

const (
	authorityBrokerStartTimeout = 20 * time.Second
	authorityBrokerPollInterval = 150 * time.Millisecond
)

type authorityBrokerLaunchConfig struct {
	Config              config.ClientConfig `json:"config"`
	WorkspaceInstanceID string              `json:"workspace_instance_id"`
}

type authorityBrokerManifest struct {
	Address             string `json:"address"`
	Token               string `json:"token"`
	WorkspaceInstanceID string `json:"workspace_instance_id"`
	PID                 int    `json:"pid"`
}

type authorityBrokerStatus struct {
	Ready            bool                `json:"ready"`
	Error            string              `json:"error,omitempty"`
	LeaseCount       int                 `json:"lease_count"`
	SyncProgress     *proto.SyncProgress `json:"sync_progress,omitempty"`
	RecentOperations []string            `json:"recent_operations,omitempty"`
}

type authorityBrokerAcquireResponse struct {
	LeaseID string `json:"lease_id"`
}

type authorityBrokerReleaseRequest struct {
	LeaseID string `json:"lease_id"`
}

type authorityBroker struct {
	cfg          config.ClientConfig
	workspaceID  string
	manifestPath string
	token        string
	connectFn    func(context.Context, func(string, ...any)) (*authorityBrokerSession, error)

	mu       sync.Mutex
	leases   map[string]struct{}
	ready    bool
	lastErr  string
	progress *proto.SyncProgress
	ops      []string
}

type authorityBrokerSession struct {
	done    <-chan struct{}
	closeFn func()
}

func (s *authorityBrokerSession) Close() {
	if s == nil || s.closeFn == nil {
		return
	}
	s.closeFn()
}

func acquireAuthorityLease(
	ctx context.Context,
	cfg config.ClientConfig,
	tracef func(string, ...any),
	prompter endpointssh.HostKeyPrompter,
	syncFeedback app.SyncFeedback,
) (func(), error) {
	workspaceInstanceID, err := workspaceidentity.DefaultWorkspaceInstanceID(cfg.Workdir)
	if err != nil {
		return nil, err
	}
	manifestPath, err := authorityBrokerManifestPath(workspaceInstanceID)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(authorityBrokerStartTimeout)
	for {
		if manifest, err := readAuthorityBrokerManifest(manifestPath); err == nil {
			leaseID, err := authorityBrokerAcquire(ctx, manifest)
			if err == nil {
				if err := authorityBrokerWaitReadyWithFeedback(ctx, manifest, syncFeedback); err != nil {
					_ = authorityBrokerRelease(context.Background(), manifest, leaseID)
					return nil, err
				}
				brokerTracef(tracef, "authority broker lease acquired workspace_instance_id=%s addr=%s", workspaceInstanceID, manifest.Address)
				return func() {
					_ = authorityBrokerRelease(context.Background(), manifest, leaseID)
				}, nil
			}
			_ = os.Remove(manifestPath)
		}

		locked, unlock, err := tryLockAuthorityBroker(manifestPath)
		if err != nil {
			return nil, err
		}
		if locked {
			if err := preflightAuthorityBrokerLaunch(ctx, cfg, prompter, tracef); err != nil {
				unlock()
				return nil, err
			}
			startErr := startAuthorityBrokerProcess(cfg, workspaceInstanceID, manifestPath)
			if startErr == nil {
				startErr = waitForAuthorityBrokerManifest(ctx, manifestPath)
			}
			unlock()
			if startErr != nil {
				return nil, startErr
			}
			continue
		}

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("authority broker startup timed out for %s", workspaceInstanceID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(authorityBrokerPollInterval):
		}
	}
}

func authorityBrokerManifestPath(workspaceInstanceID string) (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve broker config directory: %w", err)
	}
	return filepath.Join(configDir, "symterm", "brokers", workspaceInstanceID+".json"), nil
}

func readAuthorityBrokerManifest(path string) (authorityBrokerManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return authorityBrokerManifest{}, err
	}
	var manifest authorityBrokerManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return authorityBrokerManifest{}, err
	}
	if strings.TrimSpace(manifest.Address) == "" || strings.TrimSpace(manifest.Token) == "" {
		return authorityBrokerManifest{}, fmt.Errorf("authority broker manifest is incomplete")
	}
	return manifest, nil
}

func waitForAuthorityBrokerManifest(ctx context.Context, manifestPath string) error {
	deadline := time.Now().Add(authorityBrokerStartTimeout)
	for {
		if _, err := readAuthorityBrokerManifest(manifestPath); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("authority broker manifest was not published at %s", manifestPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(authorityBrokerPollInterval):
		}
	}
}

func tryLockAuthorityBroker(manifestPath string) (bool, func(), error) {
	lockPath := manifestPath + ".lock"
	if info, err := os.Stat(lockPath); err == nil {
		if time.Since(info.ModTime()) > authorityBrokerStartTimeout {
			_ = os.Remove(lockPath)
		}
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return false, nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return false, func() {}, nil
		}
		return false, nil, err
	}
	_ = file.Close()
	return true, func() {
		_ = os.Remove(lockPath)
	}, nil
}

func startAuthorityBrokerProcess(cfg config.ClientConfig, workspaceInstanceID string, manifestPath string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable for authority broker: %w", err)
	}
	launchConfig := authorityBrokerLaunchConfig{
		Config:              cfg,
		WorkspaceInstanceID: workspaceInstanceID,
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		return err
	}
	configFile, err := os.CreateTemp(filepath.Dir(manifestPath), "authority-broker-*.json")
	if err != nil {
		return err
	}
	configPath := configFile.Name()
	encoder := json.NewEncoder(configFile)
	if err := encoder.Encode(launchConfig); err != nil {
		_ = configFile.Close()
		_ = os.Remove(configPath)
		return err
	}
	if err := configFile.Close(); err != nil {
		_ = os.Remove(configPath)
		return err
	}

	cmd := exec.Command(exePath, "internal-authority-broker", "--manifest", manifestPath, "--config", configPath)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = os.Remove(configPath)
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

func authorityBrokerAcquire(ctx context.Context, manifest authorityBrokerManifest) (string, error) {
	var response authorityBrokerAcquireResponse
	if err := authorityBrokerJSON(ctx, manifest, http.MethodPost, "/acquire", struct{}{}, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.LeaseID) == "" {
		return "", errors.New("authority broker returned an empty lease id")
	}
	return response.LeaseID, nil
}

func authorityBrokerRelease(ctx context.Context, manifest authorityBrokerManifest, leaseID string) error {
	if strings.TrimSpace(leaseID) == "" {
		return nil
	}
	return authorityBrokerJSON(ctx, manifest, http.MethodPost, "/release", authorityBrokerReleaseRequest{LeaseID: leaseID}, nil)
}

func authorityBrokerWaitReady(ctx context.Context, manifest authorityBrokerManifest) error {
	return authorityBrokerWaitReadyWithFeedback(ctx, manifest, nil)
}

func authorityBrokerWaitReadyWithFeedback(ctx context.Context, manifest authorityBrokerManifest, syncFeedback app.SyncFeedback) error {
	defer func() {
		if syncFeedback != nil {
			syncFeedback.FinishInitialSync()
		}
	}()
	for {
		var status authorityBrokerStatus
		if err := authorityBrokerJSON(ctx, manifest, http.MethodGet, "/status", nil, &status); err != nil {
			return err
		}
		applyAuthorityBrokerSyncStatus(syncFeedback, status)
		if status.Ready {
			return nil
		}
		if strings.TrimSpace(status.Error) != "" {
			return errors.New(status.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(authorityBrokerPollInterval):
		}
	}
}

func applyAuthorityBrokerSyncStatus(syncFeedback app.SyncFeedback, status authorityBrokerStatus) {
	if syncFeedback == nil {
		return
	}
	tui, ok := syncFeedback.(*syncProgressTUI)
	if !ok {
		return
	}
	tui.applyRemoteStatus(status.SyncProgress, status.RecentOperations)
}

func authorityBrokerJSON(ctx context.Context, manifest authorityBrokerManifest, method string, path string, requestBody any, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		data, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+manifest.Address+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Symterm-Broker-Token", manifest.Token)
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		if len(data) == 0 {
			return fmt.Errorf("authority broker %s %s returned %s", method, path, resp.Status)
		}
		return errors.New(strings.TrimSpace(string(data)))
	}
	if responseBody == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(responseBody)
}

func preflightAuthorityBrokerLaunch(
	ctx context.Context,
	cfg config.ClientConfig,
	prompter endpointssh.HostKeyPrompter,
	tracef func(string, ...any),
) error {
	brokerTracef(tracef, "authority broker SSH preflight begin target=%s", cfg.Endpoint.Target)
	sshClient, err := endpointssh.DialClientWithPrompter(ctx, cfg, prompter)
	if err != nil {
		return err
	}
	defer func() {
		_ = sshClient.Close()
	}()

	controlConn, err := transport.OpenSSHControlChannel(sshClient)
	if err != nil {
		return err
	}
	defer func() {
		_ = controlConn.Close()
	}()

	brokerTracef(tracef, "authority broker SSH preflight complete target=%s", cfg.Endpoint.Target)
	return nil
}

func RunAuthorityBroker(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	_ = stdout

	fs := flag.NewFlagSet("internal-authority-broker", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var manifestPath string
	var configPath string
	fs.StringVar(&manifestPath, "manifest", "", "broker manifest path")
	fs.StringVar(&configPath, "config", "", "broker launch config path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(manifestPath) == "" || strings.TrimSpace(configPath) == "" {
		return errors.New("authority broker requires --manifest and --config")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	_ = os.Remove(configPath)

	var launch authorityBrokerLaunchConfig
	if err := json.Unmarshal(data, &launch); err != nil {
		return err
	}

	tracef := newTraceLogger(launch.Config.Verbose, stderr).Printf
	token, err := randomHex(16)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()

	brokerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	broker := &authorityBroker{
		cfg:          launch.Config,
		workspaceID:  launch.WorkspaceInstanceID,
		manifestPath: manifestPath,
		token:        token,
		leases:       make(map[string]struct{}),
	}
	server := &http.Server{Handler: broker.handler(cancel)}
	serverErrCh := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		_ = os.Remove(manifestPath)
	}()

	if err := writeAuthorityBrokerManifest(manifestPath, authorityBrokerManifest{
		Address:             listener.Addr().String(),
		Token:               token,
		WorkspaceInstanceID: launch.WorkspaceInstanceID,
		PID:                 os.Getpid(),
	}); err != nil {
		return err
	}

	sessionErrCh := make(chan error, 1)
	go func() {
		sessionErrCh <- broker.runAuthorityLoop(brokerCtx, tracef)
	}()

	select {
	case <-brokerCtx.Done():
		return nil
	case err := <-sessionErrCh:
		return err
	case err := <-serverErrCh:
		return err
	}
}

func writeAuthorityBrokerManifest(path string, manifest authorityBrokerManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tempFile, err := os.CreateTemp(filepath.Dir(path), "manifest-*.json")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	encoder := json.NewEncoder(tempFile)
	if err := encoder.Encode(manifest); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return os.Rename(tempPath, path)
}

func (b *authorityBroker) handler(cancel context.CancelFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/acquire", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !b.authorize(writer, request) {
			return
		}
		leaseID, err := randomHex(12)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		b.mu.Lock()
		b.leases[leaseID] = struct{}{}
		b.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(authorityBrokerAcquireResponse{LeaseID: leaseID})
	})
	mux.HandleFunc("/release", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !b.authorize(writer, request) {
			return
		}
		var release authorityBrokerReleaseRequest
		if err := json.NewDecoder(request.Body).Decode(&release); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		delete(b.leases, release.LeaseID)
		remaining := len(b.leases)
		b.mu.Unlock()
		if remaining == 0 {
			cancel()
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/status", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !b.authorize(writer, request) {
			return
		}
		status := b.status()
		_ = json.NewEncoder(writer).Encode(status)
	})
	return mux
}

func (b *authorityBroker) authorize(writer http.ResponseWriter, request *http.Request) bool {
	if request.Header.Get("X-Symterm-Broker-Token") != b.token {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func (b *authorityBroker) status() authorityBrokerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()

	var progress *proto.SyncProgress
	if b.progress != nil {
		current := *b.progress
		progress = &current
	}
	return authorityBrokerStatus{
		Ready:            b.ready,
		Error:            b.lastErr,
		LeaseCount:       len(b.leases),
		SyncProgress:     progress,
		RecentOperations: append([]string(nil), b.ops...),
	}
}

func (b *authorityBroker) setStatus(ready bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ready = ready
	if err == nil {
		b.lastErr = ""
		return
	}
	b.lastErr = err.Error()
}

func (b *authorityBroker) syncFeedback() app.SyncFeedback {
	return &authorityBrokerSyncFeedback{broker: b}
}

type authorityBrokerSyncFeedback struct {
	broker *authorityBroker
}

func (f *authorityBrokerSyncFeedback) InitialSyncObserver() *workspacesync.InitialSyncObserver {
	if f == nil || f.broker == nil {
		return nil
	}
	return &workspacesync.InitialSyncObserver{
		OnOperation: f.broker.recordSyncOperation,
		OnProgress:  f.broker.recordSyncProgress,
	}
}

func (f *authorityBrokerSyncFeedback) FinishInitialSync() {}

func (b *authorityBroker) recordSyncOperation(message string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if len(b.ops) == 0 || b.ops[len(b.ops)-1] != message {
		b.ops = append(b.ops, message)
		if len(b.ops) > syncProgressRecentLines {
			b.ops = append([]string(nil), b.ops[len(b.ops)-syncProgressRecentLines:]...)
		}
	}
}

func (b *authorityBroker) recordSyncProgress(progress proto.SyncProgress) {
	b.mu.Lock()
	defer b.mu.Unlock()

	current := progress
	b.progress = &current
}

func (b *authorityBroker) runAuthorityLoop(ctx context.Context, tracef func(string, ...any)) error {
	everReady := false
	for {
		if ctx.Err() != nil {
			return nil
		}
		brokerTracef(tracef, "authority broker connect begin workspace_instance_id=%s", b.workspaceID)
		session, err := b.connectAuthoritySession(ctx, tracef)
		if err != nil {
			b.setStatus(false, err)
			if !everReady {
				brokerTracef(tracef, "authority broker initial connect failed workspace_instance_id=%s err=%v", b.workspaceID, err)
				timer := time.NewTimer(authorityBrokerStartTimeout)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return nil
				case <-timer.C:
					return err
				}
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(authorityBrokerPollInterval):
			}
			continue
		}
		b.setStatus(true, nil)
		everReady = true
		brokerTracef(tracef, "authority broker ready workspace_instance_id=%s", b.workspaceID)

		done := session.done
		if done == nil {
			<-ctx.Done()
			session.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			session.Close()
			return nil
		case <-done:
			session.Close()
			if ctx.Err() != nil {
				return nil
			}
			b.setStatus(false, errors.New("authority runtime disconnected"))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(authorityBrokerPollInterval):
			}
		}
	}
}

func (b *authorityBroker) connectAuthoritySession(ctx context.Context, tracef func(string, ...any)) (*authorityBrokerSession, error) {
	if b.connectFn != nil {
		return b.connectFn(ctx, tracef)
	}
	sshClient, err := endpointssh.DialClient(ctx, b.cfg)
	if err != nil {
		return nil, err
	}
	controlConn, err := transport.OpenSSHControlChannel(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, err
	}
	controlClient := transport.NewClient(controlConn, controlConn)
	lifecycle := newServiceLifecycle(
		nil,
		func(_ context.Context, clientID string) (io.ReadWriteCloser, error) {
			brokerTracef(tracef, "authority broker open ownerfs channel client_id=%s", clientID)
			return transport.OpenSSHOwnerFSChannel(sshClient, clientID)
		},
		tracef,
		func() {
			_ = controlConn.Close()
			_ = sshClient.Close()
		},
	)

	useCase := app.ProjectSessionUseCase{
		ControlClient: controlClient,
		Config:        b.cfg,
		Lifecycle:     lifecycle,
		SessionKind:   proto.SessionKindAuthority,
		SyncFeedback:  b.syncFeedback(),
		Tracef:        tracef,
	}
	session, err := useCase.ConnectProjectSession(ctx)
	if err != nil {
		lifecycle.Close()
		return nil, err
	}
	session, err = useCase.ConfirmAndResumeProjectSession(ctx, session)
	if err != nil {
		session.Close()
		lifecycle.Close()
		return nil, err
	}
	return &authorityBrokerSession{
		done: session.Done(),
		closeFn: func() {
			session.Close()
			lifecycle.Close()
		},
	}, nil
}

func randomHex(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func brokerTracef(tracef func(string, ...any), format string, args ...any) {
	if tracef == nil {
		return
	}
	tracef(format, args...)
}
