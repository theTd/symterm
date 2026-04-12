package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const (
	envHideTmuxStatus = "SYMTERM_HIDE_TMUX_STATUS"
)

type ClientConfig struct {
	EndpointRaw      string
	Endpoint         Endpoint
	Token            string
	Auth             ClientAuthConfig
	SetupConfigPath  string
	ProjectID        string
	Workdir          string
	ConfirmReconcile bool
	TmuxStatus       bool
	Verbose          bool
	ArgvTail         []string
}

func ClientUsage() string {
	return strings.TrimSpace(`Usage: symterm run [local-options] -- <remote-argv...>

Local options:
  --project-id string
        project identifier within the authenticated username scope
  --confirm-reconcile
        explicitly confirm reconcile takeover when the project is locked
  --tmux-status
        force-enable tmux status mode for this invocation
  -v, --verbose
        print detailed client-side tracing to stderr
  --help
        show local CLI help
`)
}

func ParseClientConfig(args []string, env map[string]string, cwd string) (ClientConfig, error) {
	localArgs, argvTail, err := splitLocalAndRemoteArgs(args)
	if err != nil {
		return ClientConfig{}, err
	}

	fs := flag.NewFlagSet("symterm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var projectID string
	var confirmReconcile bool
	tmuxStatus := tmuxStatusDefaultEnabled(env)
	var verbose bool
	fs.StringVar(&projectID, "project-id", "", "project identifier within the authenticated username scope")
	fs.BoolVar(&confirmReconcile, "confirm-reconcile", false, "explicitly confirm reconcile takeover when the project is locked")
	fs.BoolVar(&tmuxStatus, "tmux-status", tmuxStatus, "force-enable tmux status mode for this invocation")
	fs.BoolVar(&verbose, "v", false, "print detailed client-side tracing to stderr")
	fs.BoolVar(&verbose, "verbose", false, "print detailed client-side tracing to stderr")
	if err := fs.Parse(localArgs); err != nil {
		return ClientConfig{}, err
	}
	if fs.NArg() != 0 {
		return ClientConfig{}, fmt.Errorf("unexpected local arguments: %v; use -- before remote command arguments", fs.Args())
	}

	return parseResolvedClientConfig(env, cwd, projectID, confirmReconcile, tmuxStatus, verbose, argvTail)
}

func ParseDefaultClientConfig(argvTail []string, env map[string]string, cwd string) (ClientConfig, error) {
	return parseResolvedClientConfig(env, cwd, "", false, tmuxStatusDefaultEnabled(env), false, argvTail)
}

func parseResolvedClientConfig(
	env map[string]string,
	cwd string,
	projectID string,
	confirmReconcile bool,
	tmuxStatus bool,
	verbose bool,
	argvTail []string,
) (ClientConfig, error) {
	merged, setupPath, endpointRaw, endpoint, token, err := loadResolvedClientConnection(env)
	if err != nil {
		return ClientConfig{}, err
	}

	if projectID == "" {
		projectID = filepath.Base(filepath.Clean(cwd))
		if projectID == "." || projectID == string(filepath.Separator) || projectID == "" {
			return ClientConfig{}, errors.New("cannot derive project id from cwd")
		}
	}

	return ClientConfig{
		EndpointRaw:      endpointRaw,
		Endpoint:         endpoint,
		Token:            token,
		Auth:             merged.Auth,
		SetupConfigPath:  setupPath,
		ProjectID:        projectID,
		Workdir:          cwd,
		ConfirmReconcile: confirmReconcile,
		TmuxStatus:       tmuxStatus,
		Verbose:          verbose,
		ArgvTail:         append([]string(nil), argvTail...),
	}, nil
}

func loadResolvedClientConnection(env map[string]string) (SetupConfig, string, string, Endpoint, string, error) {
	fileCfg, setupPath, _, err := LoadDefaultSetupConfig(env)
	if err != nil {
		return SetupConfig{}, "", "", Endpoint{}, "", err
	}
	merged, err := mergeSetupConfig(fileCfg, env)
	if err != nil {
		return SetupConfig{}, "", "", Endpoint{}, "", err
	}

	endpointRaw, err := merged.Connection.EndpointRaw()
	if err != nil || endpointRaw == "" {
		return SetupConfig{}, setupPath, "", Endpoint{}, "", missingClientConfigurationError(setupPath)
	}
	endpoint, err := ParseEndpoint(endpointRaw)
	if err != nil {
		return SetupConfig{}, "", "", Endpoint{}, "", err
	}

	token := strings.TrimSpace(merged.Auth.Token)
	if token == "" {
		return SetupConfig{}, setupPath, "", Endpoint{}, "", missingClientConfigurationError(setupPath)
	}
	return merged, setupPath, endpointRaw, endpoint, token, nil
}

func missingClientConfigurationError(setupPath string) error {
	if strings.TrimSpace(setupPath) == "" {
		setupPath = "~/.symterm/config.json"
	}
	return fmt.Errorf("missing client configuration; run `symterm setup` to write %s or set SYMTERM_ENDPOINT and SYMTERM_TOKEN", setupPath)
}

func splitLocalAndRemoteArgs(args []string) ([]string, []string, error) {
	for idx, arg := range args {
		if arg == "--" {
			return args[:idx], append([]string(nil), args[idx+1:]...), nil
		}
	}
	return append([]string(nil), args...), nil, nil
}

func tmuxStatusDefaultEnabled(env map[string]string) bool {
	value, ok := env[envHideTmuxStatus]
	if !ok {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return true
	default:
		return false
	}
}
