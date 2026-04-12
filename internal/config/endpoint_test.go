package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEndpointAcceptsSymtermHostPort(t *testing.T) {
	t.Parallel()

	endpoint, err := ParseEndpoint("symterm://example.com:7000")
	if err != nil {
		t.Fatalf("ParseEndpoint() error = %v", err)
	}
	if endpoint.Kind != EndpointSSH {
		t.Fatalf("Kind = %q, want %q", endpoint.Kind, EndpointSSH)
	}
	if endpoint.Target != "example.com:7000" {
		t.Fatalf("Target = %q, want example.com:7000", endpoint.Target)
	}
}

func TestParseEndpointRejectsLegacyAndInvalidForms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "missing port", value: "symterm://example.com", wantErr: "missing port"},
		{name: "user target", value: "symterm://alice@example.com:7000", wantErr: "must not include user@host"},
		{name: "legacy ssh scheme", value: "ssh://alice@example.com:22", wantErr: "migrate to symterm://<host>:<port>"},
		{name: "legacy tcp scheme", value: "tcp://127.0.0.1:7000", wantErr: "migrate to symterm://<host>:<port>"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseEndpoint(tc.value); err == nil {
				t.Fatalf("ParseEndpoint(%q) error = nil", tc.value)
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ParseEndpoint(%q) error = %q, want substring %q", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestParseClientConfigUsesSymtermEndpointAndKnownHosts(t *testing.T) {
	t.Parallel()

	env := testHomeEnv(t, map[string]string{
		"SYMTERM_ENDPOINT":        "symterm://example.com:7000",
		"SYMTERM_TOKEN":           "secret",
		"SYMTERM_SSH_KNOWN_HOSTS": "/tmp/known_hosts",
	})
	cfg, err := ParseClientConfig([]string{"--project-id", "demo"}, env, "/tmp/demo")
	if err != nil {
		t.Fatalf("ParseClientConfig() error = %v", err)
	}
	if cfg.Endpoint.Kind != EndpointSSH || cfg.Endpoint.Target != "example.com:7000" {
		t.Fatalf("Endpoint = %#v", cfg.Endpoint)
	}
	if cfg.Auth.SSHKnownHostsPath != "/tmp/known_hosts" {
		t.Fatalf("SSHKnownHostsPath = %q", cfg.Auth.SSHKnownHostsPath)
	}
	if cfg.Auth.SSHDisableHostKeyCheck {
		t.Fatal("SSHDisableHostKeyCheck = true, want false")
	}
}

func TestParseClientConfigEnablesTmuxStatusFlag(t *testing.T) {
	t.Parallel()

	env := testHomeEnv(t, map[string]string{
		"SYMTERM_ENDPOINT": "symterm://example.com:7000",
		"SYMTERM_TOKEN":    "secret",
	})
	cfg, err := ParseClientConfig([]string{"--project-id", "demo", "--tmux-status", "--", "bash"}, env, "/tmp/demo")
	if err != nil {
		t.Fatalf("ParseClientConfig() error = %v", err)
	}
	if !cfg.TmuxStatus {
		t.Fatal("TmuxStatus = false, want true")
	}
}

func TestParseDefaultClientConfigEnablesTmuxStatusByDefault(t *testing.T) {
	t.Parallel()

	env := testHomeEnv(t, map[string]string{
		"SYMTERM_ENDPOINT": "symterm://example.com:7000",
		"SYMTERM_TOKEN":    "secret",
	})
	cfg, err := ParseDefaultClientConfig([]string{"bash"}, env, "/tmp/demo")
	if err != nil {
		t.Fatalf("ParseDefaultClientConfig() error = %v", err)
	}
	if !cfg.TmuxStatus {
		t.Fatal("TmuxStatus = false, want true by default")
	}
}

func TestParseDefaultClientConfigCanHideTmuxStatusViaEnv(t *testing.T) {
	t.Parallel()

	env := testHomeEnv(t, map[string]string{
		"SYMTERM_ENDPOINT":         "symterm://example.com:7000",
		"SYMTERM_TOKEN":            "secret",
		"SYMTERM_HIDE_TMUX_STATUS": "1",
	})
	cfg, err := ParseDefaultClientConfig([]string{"bash"}, env, "/tmp/demo")
	if err != nil {
		t.Fatalf("ParseDefaultClientConfig() error = %v", err)
	}
	if cfg.TmuxStatus {
		t.Fatal("TmuxStatus = true, want false when hidden by env")
	}
}

func TestParseClientConfigLoadsSetupFileWithSingleEndpointField(t *testing.T) {
	t.Parallel()

	env := testHomeEnv(t, nil)
	if _, err := SaveDefaultSetupConfig(env, SetupConfig{
		Connection: ClientConnectionConfig{
			Endpoint: "symterm://configured.example:7022",
		},
		Auth: ClientAuthConfig{
			Token:                  "file-secret",
			SSHKnownHostsPath:      "/file/known_hosts",
			SSHDisableHostKeyCheck: false,
		},
		Metadata: SetupMetadata{Version: 1},
	}); err != nil {
		t.Fatalf("SaveDefaultSetupConfig() error = %v", err)
	}

	cfg, err := ParseClientConfig(nil, env, "/tmp/example-project")
	if err != nil {
		t.Fatalf("ParseClientConfig() error = %v", err)
	}
	if cfg.Endpoint.Target != "configured.example:7022" {
		t.Fatalf("Endpoint.Target = %q", cfg.Endpoint.Target)
	}
	if cfg.Token != "file-secret" {
		t.Fatalf("Token = %q", cfg.Token)
	}
}

func TestParseDefaultClientConfigTreatsArgsAsRemoteArgv(t *testing.T) {
	t.Parallel()

	env := testHomeEnv(t, map[string]string{
		"SYMTERM_ENDPOINT": "symterm://example.com:7000",
		"SYMTERM_TOKEN":    "secret",
	})
	cfg, err := ParseDefaultClientConfig([]string{"echo", "hi"}, env, "/tmp/demo")
	if err != nil {
		t.Fatalf("ParseDefaultClientConfig() error = %v", err)
	}
	if len(cfg.ArgvTail) != 2 || cfg.ArgvTail[0] != "echo" || cfg.ArgvTail[1] != "hi" {
		t.Fatalf("ArgvTail = %#v", cfg.ArgvTail)
	}
	if cfg.ProjectID != "demo" {
		t.Fatalf("ProjectID = %q, want demo", cfg.ProjectID)
	}
}

func TestParseClientConfigMissingConfigurationPointsToSetup(t *testing.T) {
	t.Parallel()

	env := testHomeEnv(t, nil)
	_, err := ParseDefaultClientConfig(nil, env, "/tmp/demo")
	if err == nil {
		t.Fatal("ParseDefaultClientConfig() error = nil")
	}
	if !strings.Contains(err.Error(), "symterm setup") {
		t.Fatalf("error = %q, want setup guidance", err)
	}
}

func testHomeEnv(t *testing.T, values map[string]string) map[string]string {
	t.Helper()

	home := t.TempDir()
	env := map[string]string{
		"HOME":        home,
		"USERPROFILE": home,
		"HOMEDRIVE":   filepath.VolumeName(home),
		"HOMEPATH":    strings.TrimPrefix(home, filepath.VolumeName(home)),
	}
	for key, value := range values {
		env[key] = value
	}
	return env
}
