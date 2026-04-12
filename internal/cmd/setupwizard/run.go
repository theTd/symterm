package setupwizard

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"symterm/internal/config"
)

type promptIO struct {
	reader *bufio.Reader
	stdout io.Writer
}

type setupPromptDefaults struct {
	endpoint         string
	token            string
	knownHostsChoice string
	knownHostsPath   string
}

func Run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, _ io.Writer, env map[string]string) error {
	_ = ctx

	fs := flag.NewFlagSet("symterm setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var showHelp bool
	fs.BoolVar(&showHelp, "help", false, "show setup help")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp || fs.NArg() != 0 {
		fmt.Fprintln(stdout, Usage())
		return nil
	}

	ui := promptIO{
		reader: bufio.NewReader(stdin),
		stdout: stdout,
	}
	fmt.Fprintln(stdout, "symterm setup")
	fmt.Fprintln(stdout, "This writes a single active client config to ~/.symterm/config.json.")
	fmt.Fprintln(stdout)

	setupCfg, err := collectSetupConfig(ui, env)
	if err != nil {
		return err
	}

	path, err := config.SaveDefaultSetupConfig(env, setupCfg)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "Saved config: %s\n", path)
	printSummary(stdout, path, setupCfg)
	return nil
}

func Usage() string {
	return strings.TrimSpace(`Usage: symterm setup

Interactive setup wizard for ~/.symterm/config.json.
`)
}

func collectSetupConfig(ui promptIO, env map[string]string) (config.SetupConfig, error) {
	defaults := loadSetupPromptDefaults(env)
	if defaults.hasValues() {
		fmt.Fprintln(ui.stdout, "Existing values are shown as defaults. Press Enter to keep them.")
		fmt.Fprintln(ui.stdout)
	}
	fmt.Fprintln(ui.stdout, "Configure the daemon SSH endpoint exposed by symtermd.")
	endpoint, err := promptEndpoint(ui, defaults.endpoint)
	if err != nil {
		return config.SetupConfig{}, err
	}
	token, err := ui.promptRequiredDefault("Token: ", defaults.token, maskSecret(defaults.token))
	if err != nil {
		return config.SetupConfig{}, err
	}
	auth := config.ClientAuthConfig{Token: token}
	if err := promptKnownHosts(ui, env, &auth, defaults); err != nil {
		return config.SetupConfig{}, err
	}
	return config.SetupConfig{
		Connection: config.ClientConnectionConfig{
			Endpoint: endpoint,
		},
		Auth:     auth,
		Metadata: config.SetupMetadata{Version: 1},
	}, nil
}

func promptEndpoint(ui promptIO, defaultValue string) (string, error) {
	for {
		value, err := ui.promptRequiredDefault("Endpoint (symterm://host:port): ", defaultValue, defaultValue)
		if err != nil {
			return "", err
		}
		endpoint, err := config.ParseEndpoint(value)
		if err != nil {
			fmt.Fprintf(ui.stdout, "Invalid endpoint: %v\n", err)
			continue
		}
		return "symterm://" + endpoint.Target, nil
	}
}

func promptKnownHosts(ui promptIO, env map[string]string, auth *config.ClientAuthConfig, defaults setupPromptDefaults) error {
	for {
		fmt.Fprintln(ui.stdout, "known_hosts strategy:")
		fmt.Fprintln(ui.stdout, "  1) default (~/.ssh/known_hosts)")
		fmt.Fprintln(ui.stdout, "  2) custom path")
		fmt.Fprintln(ui.stdout, "  3) disable verification")
		value, err := ui.prompt(promptLabel("Strategy [1-3]: ", defaults.knownHostsChoice))
		if err != nil {
			return err
		}
		if strings.TrimSpace(value) == "" && strings.TrimSpace(defaults.knownHostsChoice) != "" {
			auth.SSHKnownHostsPath = defaults.knownHostsPath
			auth.SSHDisableHostKeyCheck = defaults.knownHostsChoice == "disable"
			if defaults.knownHostsChoice == "default" {
				auth.SSHKnownHostsPath = defaultKnownHostsPath(env)
			}
			return nil
		}
		switch strings.TrimSpace(strings.ToLower(value)) {
		case "1", "default":
			path := defaultKnownHostsPath(env)
			auth.SSHKnownHostsPath = path
			auth.SSHDisableHostKeyCheck = false
			return nil
		case "2", "custom":
			path, err := ui.promptRequiredDefault("known_hosts path: ", defaults.knownHostsPath, defaults.knownHostsPath)
			if err != nil {
				return err
			}
			auth.SSHKnownHostsPath = path
			auth.SSHDisableHostKeyCheck = false
			return nil
		case "3", "disable":
			auth.SSHKnownHostsPath = ""
			auth.SSHDisableHostKeyCheck = true
			return nil
		default:
			fmt.Fprintln(ui.stdout, "Enter 1, 2, 3, default, custom, or disable.")
		}
	}
}

func loadSetupPromptDefaults(env map[string]string) setupPromptDefaults {
	cfg, _, ok, err := config.LoadDefaultSetupConfig(env)
	if err != nil || !ok {
		return setupPromptDefaults{}
	}
	return setupPromptDefaults{
		endpoint:         strings.TrimSpace(cfg.Connection.Endpoint),
		token:            strings.TrimSpace(cfg.Auth.Token),
		knownHostsChoice: knownHostsChoiceLabel(cfg.Auth, env),
		knownHostsPath:   strings.TrimSpace(cfg.Auth.SSHKnownHostsPath),
	}
}

func (d setupPromptDefaults) hasValues() bool {
	return strings.TrimSpace(d.endpoint) != "" ||
		strings.TrimSpace(d.token) != "" ||
		strings.TrimSpace(d.knownHostsChoice) != ""
}

func knownHostsChoiceLabel(auth config.ClientAuthConfig, env map[string]string) string {
	if auth.SSHDisableHostKeyCheck {
		return "disable"
	}
	path := strings.TrimSpace(auth.SSHKnownHostsPath)
	if path == "" || path == defaultKnownHostsPath(env) {
		return "default"
	}
	return "custom"
}

func defaultKnownHostsPath(env map[string]string) string {
	return filepath.Join(userHomeDir(env), ".ssh", "known_hosts")
}

func userHomeDir(env map[string]string) string {
	if path, err := config.DefaultSetupConfigPath(env); err == nil {
		return filepath.Dir(filepath.Dir(path))
	}
	home, _ := os.UserHomeDir()
	return home
}

func printSummary(stdout io.Writer, path string, cfg config.SetupConfig) {
	fmt.Fprintln(stdout, "Summary:")
	fmt.Fprintf(stdout, "  config: %s\n", path)
	fmt.Fprintf(stdout, "  endpoint: %s\n", cfg.Connection.Endpoint)
	if cfg.Auth.SSHDisableHostKeyCheck {
		fmt.Fprintln(stdout, "  known_hosts: disabled")
	} else {
		fmt.Fprintf(stdout, "  known_hosts: %s\n", cfg.Auth.SSHKnownHostsPath)
	}
	fmt.Fprintf(stdout, "  token: %s\n", maskSecret(cfg.Auth.Token))
}

func maskSecret(value string) string {
	if len(value) <= 4 {
		return strings.Repeat("*", len(value))
	}
	return value[:2] + strings.Repeat("*", len(value)-4) + value[len(value)-2:]
}

func (ui promptIO) prompt(label string) (string, error) {
	fmt.Fprint(ui.stdout, label)
	line, err := ui.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if errors.Is(err, io.EOF) && line == "" {
		return "", io.EOF
	}
	return line, nil
}

func (ui promptIO) promptDefault(label string, defaultValue string, displayValue string) (string, error) {
	value, err := ui.prompt(promptLabel(label, displayValue))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" && strings.TrimSpace(defaultValue) != "" {
		return defaultValue, nil
	}
	return strings.TrimSpace(value), nil
}

func (ui promptIO) promptRequiredDefault(label string, defaultValue string, displayValue string) (string, error) {
	for {
		value, err := ui.promptDefault(label, defaultValue, displayValue)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
		fmt.Fprintln(ui.stdout, "Value is required.")
	}
}

func promptLabel(label string, displayValue string) string {
	displayValue = strings.TrimSpace(displayValue)
	if displayValue == "" {
		return label
	}
	trimmed := strings.TrimRight(label, " ")
	if strings.HasSuffix(trimmed, ":") {
		return strings.TrimSuffix(trimmed, ":") + " [" + displayValue + "]: "
	}
	return trimmed + " [" + displayValue + "]: "
}
