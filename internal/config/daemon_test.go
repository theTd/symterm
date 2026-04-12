package config

import (
	"path/filepath"
	"testing"
)

func TestLoadDaemonConfigParsesStaticTokens(t *testing.T) {
	t.Parallel()

	cfg, err := LoadDaemonConfig(map[string]string{
		"SYMTERMD_REMOTE_ENTRY":    "[\"/usr/bin/env\",\"bash\",\"-lc\",\"test entry with spaces\"]",
		"SYMTERMD_STATIC_TOKENS":   "token-a=alice,token-b=bob",
		"SYMTERMD_SSH_LISTEN_ADDR": "127.0.0.1:7000",
	}, "/home/tester")
	if err != nil {
		t.Fatalf("LoadDaemonConfig() error = %v", err)
	}

	if cfg.ProjectsRoot != filepath.Join("/home/tester", ".symterm") {
		t.Fatalf("ProjectsRoot = %q", cfg.ProjectsRoot)
	}
	if cfg.AdminRoot != filepath.Join("/home/tester", ".symterm", "admin") {
		t.Fatalf("AdminRoot = %q", cfg.AdminRoot)
	}
	if cfg.AdminSocketPath != filepath.Join("/home/tester", ".symterm", "admin.sock") {
		t.Fatalf("AdminSocketPath = %q", cfg.AdminSocketPath)
	}
	if cfg.AdminWebAddr != "127.0.0.1:6040" {
		t.Fatalf("AdminWebAddr = %q", cfg.AdminWebAddr)
	}
	if len(cfg.RemoteEntrypoint) != 4 {
		t.Fatalf("RemoteEntrypoint = %#v", cfg.RemoteEntrypoint)
	}
	if cfg.RemoteEntrypoint[3] != "test entry with spaces" {
		t.Fatalf("RemoteEntrypoint = %#v", cfg.RemoteEntrypoint)
	}
	if cfg.StaticTokens["token-a"] != "alice" || cfg.StaticTokens["token-b"] != "bob" {
		t.Fatalf("StaticTokens = %#v", cfg.StaticTokens)
	}
	if cfg.SSHListenAddr != "127.0.0.1:7000" {
		t.Fatalf("SSHListenAddr = %q", cfg.SSHListenAddr)
	}
	if cfg.SSHHostKeyPath != filepath.Join("/home/tester", ".symterm", "ssh_host_ed25519") {
		t.Fatalf("SSHHostKeyPath = %q", cfg.SSHHostKeyPath)
	}
	if cfg.AllowUnsafeNoFuse {
		t.Fatal("AllowUnsafeNoFuse = true, want false by default")
	}
}

func TestLoadDaemonConfigAcceptsLegacySplitRemoteEntryEnv(t *testing.T) {
	t.Parallel()

	cfg, err := LoadDaemonConfig(map[string]string{
		"SYMTERMD_REMOTE_ENTRY":           "/usr/bin/env",
		"SYMTERMD_REMOTE_ENTRY_ARGS_JSON": "[\"bash\",\"-lc\",\"legacy\"]",
		"SYMTERMD_STATIC_TOKENS":          "token-a=alice",
	}, "/home/tester")
	if err != nil {
		t.Fatalf("LoadDaemonConfig() error = %v", err)
	}
	if len(cfg.RemoteEntrypoint) != 4 {
		t.Fatalf("RemoteEntrypoint = %#v", cfg.RemoteEntrypoint)
	}
	if cfg.RemoteEntrypoint[3] != "legacy" {
		t.Fatalf("RemoteEntrypoint = %#v", cfg.RemoteEntrypoint)
	}
}

func TestLoadDaemonConfigAllowsMissingStaticTokens(t *testing.T) {
	t.Parallel()

	cfg, err := LoadDaemonConfig(map[string]string{}, "/home/tester")
	if err != nil {
		t.Fatalf("LoadDaemonConfig() error = %v", err)
	}
	if len(cfg.RemoteEntrypoint) != 1 || cfg.RemoteEntrypoint[0] != "bash" {
		t.Fatalf("RemoteEntrypoint = %#v, want [\"bash\"]", cfg.RemoteEntrypoint)
	}
	if len(cfg.StaticTokens) != 0 {
		t.Fatalf("StaticTokens = %#v, want empty map", cfg.StaticTokens)
	}
}

func TestLoadDaemonConfigParsesUnsafeNoFuseFlag(t *testing.T) {
	t.Parallel()

	cfg, err := LoadDaemonConfig(map[string]string{
		"SYMTERMD_REMOTE_ENTRY":         "/usr/bin/env",
		"SYMTERMD_STATIC_TOKENS":        "token-a=alice",
		"SYMTERMD_ALLOW_UNSAFE_NO_FUSE": "true",
	}, "/home/tester")
	if err != nil {
		t.Fatalf("LoadDaemonConfig() error = %v", err)
	}
	if !cfg.AllowUnsafeNoFuse {
		t.Fatal("AllowUnsafeNoFuse = false, want true")
	}
}

func TestLoadDaemonConfigRejectsShellStyleRemoteEntryString(t *testing.T) {
	t.Parallel()

	_, err := LoadDaemonConfig(map[string]string{
		"SYMTERMD_REMOTE_ENTRY":  "/usr/bin/env bash",
		"SYMTERMD_STATIC_TOKENS": "token-a=alice",
	}, "/home/tester")
	if err == nil {
		t.Fatal("expected remote entry parse error")
	}
}

func TestLoadDaemonConfigRejectsInvalidRemoteEntryArgsJSON(t *testing.T) {
	t.Parallel()

	_, err := LoadDaemonConfig(map[string]string{
		"SYMTERMD_REMOTE_ENTRY":           "/usr/bin/env",
		"SYMTERMD_REMOTE_ENTRY_ARGS_JSON": "[1]",
		"SYMTERMD_STATIC_TOKENS":          "token-a=alice",
	}, "/home/tester")
	if err == nil {
		t.Fatal("expected remote entry args json error")
	}
}

func TestLoadDaemonConfigRejectsMixedStructuredAndLegacyRemoteEntryEnv(t *testing.T) {
	t.Parallel()

	_, err := LoadDaemonConfig(map[string]string{
		"SYMTERMD_REMOTE_ENTRY":           "[\"/usr/bin/env\"]",
		"SYMTERMD_REMOTE_ENTRY_ARGS_JSON": "[\"bash\"]",
		"SYMTERMD_STATIC_TOKENS":          "token-a=alice",
	}, "/home/tester")
	if err == nil {
		t.Fatal("expected mixed remote entry config error")
	}
}
