package config

import (
	"path/filepath"
	"strings"
)

type DaemonConfig struct {
	ProjectsRoot      string
	AdminRoot         string
	AdminSocketPath   string
	AdminWebAddr      string
	SSHListenAddr     string
	SSHHostKeyPath    string
	RemoteEntrypoint  []string
	AllowUnsafeNoFuse bool
	Tracef            func(string, ...any)
}

func LoadDaemonConfig(env map[string]string, homeDir string) (DaemonConfig, error) {
	root := strings.TrimSpace(env["SYMTERMD_PROJECTS_ROOT"])
	if root == "" {
		root = filepath.Join(homeDir, ".symterm")
	}

	entry, err := ParseRemoteEntrypointEnv(env)
	if err != nil {
		return DaemonConfig{}, err
	}

	return DaemonConfig{
		ProjectsRoot:      root,
		AdminRoot:         filepath.Join(root, "admin"),
		AdminSocketPath:   filepath.Join(root, "admin.sock"),
		AdminWebAddr:      defaultString(strings.TrimSpace(env["SYMTERMD_ADMIN_WEB_ADDR"]), "127.0.0.1:6040"),
		SSHListenAddr:     defaultString(strings.TrimSpace(env["SYMTERMD_SSH_LISTEN_ADDR"]), "127.0.0.1:7000"),
		SSHHostKeyPath:    filepath.Join(root, "ssh_host_ed25519"),
		RemoteEntrypoint:  entry,
		AllowUnsafeNoFuse: parseBoolEnv(env["SYMTERMD_ALLOW_UNSAFE_NO_FUSE"]),
	}, nil
}

func parseBoolEnv(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func defaultString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
