package config

import (
	"errors"
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
	StaticTokens      map[string]string
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

	staticTokens, err := parseStaticTokens(strings.TrimSpace(env["SYMTERMD_STATIC_TOKENS"]))
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
		StaticTokens:      staticTokens,
		AllowUnsafeNoFuse: parseBoolEnv(env["SYMTERMD_ALLOW_UNSAFE_NO_FUSE"]),
	}, nil
}

func parseStaticTokens(raw string) (map[string]string, error) {
	if raw == "" {
		return map[string]string{}, nil
	}

	items := strings.Split(raw, ",")
	tokens := make(map[string]string, len(items))
	for _, item := range items {
		pair := strings.TrimSpace(item)
		if pair == "" {
			continue
		}
		token, username, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, errors.New("invalid SYMTERMD_STATIC_TOKENS entry")
		}
		token = strings.TrimSpace(token)
		username = strings.TrimSpace(username)
		if token == "" || username == "" {
			return nil, errors.New("invalid SYMTERMD_STATIC_TOKENS entry")
		}
		tokens[token] = username
	}
	if len(tokens) == 0 {
		return map[string]string{}, nil
	}
	return tokens, nil
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
