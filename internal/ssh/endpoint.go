package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"symterm/internal/config"
	"symterm/internal/diagnostic"

	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const clientUser = "symterm"

func DialClient(ctx context.Context, cfg config.ClientConfig) (*cryptossh.Client, error) {
	return DialClientWithPrompter(ctx, cfg, nil)
}

type HostKeyPrompt struct {
	Host           string
	RemoteAddress  string
	KeyType        string
	FingerprintSHA string
	KnownHostsPath string
}

type HostKeyPrompter interface {
	ConfirmUnknownHost(HostKeyPrompt) (bool, error)
}

func DialClientWithPrompter(ctx context.Context, cfg config.ClientConfig, prompter HostKeyPrompter) (*cryptossh.Client, error) {
	if cfg.Endpoint.Kind != config.EndpointSSH {
		return nil, fmt.Errorf("unsupported endpoint kind %q", cfg.Endpoint.Kind)
	}
	hostKeyCallback, err := sshHostKeyCallback(cfg.Auth, prompter)
	if err != nil {
		return nil, err
	}
	return DialEndpoint(ctx, cfg.Endpoint.Target, cfg.Token, hostKeyCallback)
}

func DialEndpoint(
	ctx context.Context,
	target string,
	token string,
	hostKeyCallback cryptossh.HostKeyCallback,
) (*cryptossh.Client, error) {
	return dialSSHClient(ctx, target, &cryptossh.ClientConfig{
		User:            clientUser,
		Auth:            []cryptossh.AuthMethod{cryptossh.Password(token)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	})
}

func sshHostKeyCallback(auth config.ClientAuthConfig, prompter HostKeyPrompter) (cryptossh.HostKeyCallback, error) {
	knownHostsPaths, savePath, allowInsecure, err := sshKnownHostsPaths(auth)
	if err != nil {
		return nil, err
	}
	if allowInsecure {
		return cryptossh.InsecureIgnoreHostKey(), nil
	}
	validator, err := knownHostsValidator(knownHostsPaths)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key cryptossh.PublicKey) error {
		if validator != nil {
			err := validator(hostname, remote, key)
			if err == nil {
				return nil
			}
			var keyErr *knownhosts.KeyError
			if !errors.As(err, &keyErr) || len(keyErr.Want) != 0 {
				return err
			}
		}

		if prompter == nil {
			return fmt.Errorf(
				"ssh host %s is not trusted and no interactive prompt is available; rerun from a terminal to confirm trust or pre-populate %s",
				hostname,
				savePath,
			)
		}

		accepted, err := prompter.ConfirmUnknownHost(HostKeyPrompt{
			Host:           hostname,
			RemoteAddress:  remoteAddressString(remote),
			KeyType:        key.Type(),
			FingerprintSHA: cryptossh.FingerprintSHA256(key),
			KnownHostsPath: savePath,
		})
		if err != nil {
			return err
		}
		if !accepted {
			return fmt.Errorf("ssh host %s was not trusted by the user", hostname)
		}
		if err := appendKnownHost(savePath, hostname, key); err != nil {
			return err
		}

		validator, err = knownHostsValidator(knownHostsPaths)
		if err != nil {
			return err
		}
		if validator == nil {
			return nil
		}
		return validator(hostname, remote, key)
	}, nil
}

func sshKnownHostsPaths(auth config.ClientAuthConfig) ([]string, string, bool, error) {
	if auth.SSHDisableHostKeyCheck {
		return nil, "", true, nil
	}
	if knownHostsPath := strings.TrimSpace(auth.SSHKnownHostsPath); knownHostsPath != "" {
		paths := splitKnownHostsPathList(knownHostsPath)
		if len(paths) == 0 {
			return nil, "", false, errors.New("SYMTERM_SSH_KNOWN_HOSTS does not contain a valid known_hosts path")
		}
		return paths, paths[0], false, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", false, fmt.Errorf("ssh host key verification requires known_hosts; run `symterm setup` or create ~/.ssh/known_hosts: %w", err)
	}
	path := filepath.Join(home, ".ssh", "known_hosts")
	return []string{path}, path, false, nil
}

func splitKnownHostsPathList(value string) []string {
	parts := strings.Split(value, string(os.PathListSeparator))
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	return paths
}

func knownHostsValidator(paths []string) (cryptossh.HostKeyCallback, error) {
	existing := make([]string, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err == nil {
			if info.IsDir() {
				return nil, fmt.Errorf("known_hosts path %s is a directory", path)
			}
			existing = append(existing, path)
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return nil, err
	}
	if len(existing) == 0 {
		return nil, nil
	}
	return knownhosts.New(existing...)
}

func appendKnownHost(path string, hostname string, key cryptossh.PublicKey) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("known_hosts save path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	if _, err := io.WriteString(file, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)+"\n"); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func remoteAddressString(remote net.Addr) string {
	if remote == nil {
		return ""
	}
	return remote.String()
}

func dialSSHClient(ctx context.Context, addr string, cfg *cryptossh.ClientConfig) (*cryptossh.Client, error) {
	conn, err := sshDialContext(ctx, nil, "tcp", addr)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(cfg.Timeout)
	if connectDeadline, ok := ctx.Deadline(); ok {
		deadline = connectDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "close SSH dial connection after deadline failure", conn.Close())
		return nil, err
	}

	clientConn, chans, reqs, err := cryptossh.NewClientConn(conn, addr, cfg)
	diagnostic.Cleanup(diagnostic.Default(), "clear SSH dial deadline", conn.SetDeadline(time.Time{}))
	if err != nil {
		diagnostic.Cleanup(diagnostic.Default(), "close SSH dial connection after handshake failure", conn.Close())
		return nil, err
	}
	return cryptossh.NewClient(clientConn, chans, reqs), nil
}

func sshDialContext(ctx context.Context, sshClient *cryptossh.Client, network string, addr string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}

	done := make(chan result, 1)
	go func() {
		if sshClient == nil {
			var dialer net.Dialer
			conn, err := dialer.DialContext(ctx, network, addr)
			done <- result{conn: conn, err: err}
			return
		}
		conn, err := sshClient.Dial(network, addr)
		done <- result{conn: conn, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case outcome := <-done:
		return outcome.conn, outcome.err
	}
}
