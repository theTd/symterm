package setupwizard

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"symterm/internal/config"
)

func TestRunWritesSingleSymtermEndpointConfiguration(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"HOME":        t.TempDir(),
		"USERPROFILE": t.TempDir(),
	}
	knownHosts := filepath.Join(env["HOME"], ".ssh", "known_hosts")
	input := strings.Join([]string{
		"symterm://example.com:7000",
		"dev-token",
		"1",
		"",
	}, "\n")

	var stdout bytes.Buffer
	if err := Run(context.Background(), nil, strings.NewReader(input), &stdout, &stdout, env); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	cfg, _, _, err := config.LoadDefaultSetupConfig(env)
	if err != nil {
		t.Fatalf("LoadDefaultSetupConfig() error = %v", err)
	}
	if cfg.Connection.Endpoint != "symterm://example.com:7000" {
		t.Fatalf("Connection.Endpoint = %q", cfg.Connection.Endpoint)
	}
	if cfg.Auth.Token != "dev-token" {
		t.Fatalf("Token = %q", cfg.Auth.Token)
	}
	if cfg.Auth.SSHKnownHostsPath != knownHosts {
		t.Fatalf("SSHKnownHostsPath = %q, want %q", cfg.Auth.SSHKnownHostsPath, knownHosts)
	}
	if cfg.Auth.SSHDisableHostKeyCheck {
		t.Fatal("SSHDisableHostKeyCheck = true")
	}
	if strings.Contains(stdout.String(), "mode:") || strings.Contains(stdout.String(), "remote_entry") {
		t.Fatalf("stdout contains legacy setup fields: %s", stdout.String())
	}
}

func TestRunAllowsCustomKnownHostsPathWithoutExistingFile(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"HOME":        t.TempDir(),
		"USERPROFILE": t.TempDir(),
	}
	knownHosts := filepath.Join(env["HOME"], "custom", "known_hosts")
	input := strings.Join([]string{
		"symterm://example.com:7000",
		"dev-token",
		"2",
		knownHosts,
		"",
	}, "\n")

	if err := Run(context.Background(), nil, strings.NewReader(input), &bytes.Buffer{}, &bytes.Buffer{}, env); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	cfg, _, _, err := config.LoadDefaultSetupConfig(env)
	if err != nil {
		t.Fatalf("LoadDefaultSetupConfig() error = %v", err)
	}
	if cfg.Auth.SSHKnownHostsPath != knownHosts {
		t.Fatalf("SSHKnownHostsPath = %q, want %q", cfg.Auth.SSHKnownHostsPath, knownHosts)
	}
}

func TestRunAllowsKnownHostsDisable(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"HOME":        t.TempDir(),
		"USERPROFILE": t.TempDir(),
	}
	input := "symterm://example.com:7000\nsecret\n3\n"
	if err := Run(context.Background(), nil, strings.NewReader(input), &bytes.Buffer{}, &bytes.Buffer{}, env); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	cfg, _, _, err := config.LoadDefaultSetupConfig(env)
	if err != nil {
		t.Fatalf("LoadDefaultSetupConfig() error = %v", err)
	}
	if !cfg.Auth.SSHDisableHostKeyCheck {
		t.Fatal("SSHDisableHostKeyCheck = false, want true")
	}
	if cfg.Auth.SSHKnownHostsPath != "" {
		t.Fatalf("SSHKnownHostsPath = %q, want empty", cfg.Auth.SSHKnownHostsPath)
	}
}

func TestRunUsesExistingValuesAsDefaults(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	env := map[string]string{
		"HOME":        home,
		"USERPROFILE": home,
	}
	existing := config.SetupConfig{
		Connection: config.ClientConnectionConfig{
			Endpoint: "symterm://example.com:7000",
		},
		Auth: config.ClientAuthConfig{
			Token:             "dev-token",
			SSHKnownHostsPath: filepath.Join(home, "custom", "known_hosts"),
		},
		Metadata: config.SetupMetadata{Version: 1},
	}
	if _, err := config.SaveDefaultSetupConfig(env, existing); err != nil {
		t.Fatalf("SaveDefaultSetupConfig() error = %v", err)
	}

	var stdout bytes.Buffer
	input := "\n\n\n"
	if err := Run(context.Background(), nil, strings.NewReader(input), &stdout, &stdout, env); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	cfg, _, _, err := config.LoadDefaultSetupConfig(env)
	if err != nil {
		t.Fatalf("LoadDefaultSetupConfig() error = %v", err)
	}
	if cfg.Connection.Endpoint != existing.Connection.Endpoint {
		t.Fatalf("Connection.Endpoint = %q, want %q", cfg.Connection.Endpoint, existing.Connection.Endpoint)
	}
	if cfg.Auth.Token != existing.Auth.Token {
		t.Fatalf("Token = %q, want %q", cfg.Auth.Token, existing.Auth.Token)
	}
	if cfg.Auth.SSHKnownHostsPath != existing.Auth.SSHKnownHostsPath {
		t.Fatalf("SSHKnownHostsPath = %q, want %q", cfg.Auth.SSHKnownHostsPath, existing.Auth.SSHKnownHostsPath)
	}
	if !strings.Contains(stdout.String(), "Endpoint (symterm://host:port) [symterm://example.com:7000]: ") {
		t.Fatalf("stdout missing endpoint default prompt: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Token [de*****en]: ") {
		t.Fatalf("stdout missing token default prompt: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Strategy [1-3] [custom]: ") {
		t.Fatalf("stdout missing known_hosts default prompt: %s", stdout.String())
	}
}

func TestRunCanChangeSingleFieldWhileKeepingExistingDefaults(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	env := map[string]string{
		"HOME":        home,
		"USERPROFILE": home,
	}
	existing := config.SetupConfig{
		Connection: config.ClientConnectionConfig{
			Endpoint: "symterm://example.com:7000",
		},
		Auth: config.ClientAuthConfig{
			Token:             "dev-token",
			SSHKnownHostsPath: filepath.Join(home, ".ssh", "known_hosts"),
		},
		Metadata: config.SetupMetadata{Version: 1},
	}
	if _, err := config.SaveDefaultSetupConfig(env, existing); err != nil {
		t.Fatalf("SaveDefaultSetupConfig() error = %v", err)
	}

	input := "symterm://updated.example.com:7001\n\n\n"
	if err := Run(context.Background(), nil, strings.NewReader(input), &bytes.Buffer{}, &bytes.Buffer{}, env); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	cfg, _, _, err := config.LoadDefaultSetupConfig(env)
	if err != nil {
		t.Fatalf("LoadDefaultSetupConfig() error = %v", err)
	}
	if cfg.Connection.Endpoint != "symterm://updated.example.com:7001" {
		t.Fatalf("Connection.Endpoint = %q", cfg.Connection.Endpoint)
	}
	if cfg.Auth.Token != existing.Auth.Token {
		t.Fatalf("Token = %q, want %q", cfg.Auth.Token, existing.Auth.Token)
	}
	if cfg.Auth.SSHKnownHostsPath != existing.Auth.SSHKnownHostsPath {
		t.Fatalf("SSHKnownHostsPath = %q, want %q", cfg.Auth.SSHKnownHostsPath, existing.Auth.SSHKnownHostsPath)
	}
	if cfg.Auth.SSHDisableHostKeyCheck != existing.Auth.SSHDisableHostKeyCheck {
		t.Fatalf("SSHDisableHostKeyCheck = %v, want %v", cfg.Auth.SSHDisableHostKeyCheck, existing.Auth.SSHDisableHostKeyCheck)
	}
}
