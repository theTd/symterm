package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const setupConfigVersion = 1

type SetupConfig struct {
	Connection ClientConnectionConfig `json:"connection"`
	Auth       ClientAuthConfig       `json:"auth"`
	Metadata   SetupMetadata          `json:"metadata"`
}

type ClientConnectionConfig struct {
	Endpoint string `json:"endpoint"`
}

type ClientAuthConfig struct {
	Token                  string `json:"token,omitempty"`
	SSHKnownHostsPath      string `json:"ssh_known_hosts_path,omitempty"`
	SSHDisableHostKeyCheck bool   `json:"ssh_disable_host_key_check,omitempty"`
}

type SetupMetadata struct {
	Version   int    `json:"version"`
	UpdatedAt string `json:"updated_at"`
}

func DefaultSetupConfigPath(env map[string]string) (string, error) {
	home, err := resolveUserHomeDir(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".symterm", "config.json"), nil
}

func LoadSetupConfig(path string) (SetupConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SetupConfig{}, err
	}

	var cfg SetupConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return SetupConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := validateSetupConfig(cfg, false); err != nil {
		return SetupConfig{}, fmt.Errorf("invalid %s: %w", path, err)
	}
	return cfg, nil
}

func LoadDefaultSetupConfig(env map[string]string) (SetupConfig, string, bool, error) {
	path, err := DefaultSetupConfigPath(env)
	if err != nil {
		return SetupConfig{}, "", false, err
	}

	cfg, err := LoadSetupConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SetupConfig{}, path, false, nil
		}
		return SetupConfig{}, path, false, err
	}
	return cfg, path, true, nil
}

func SaveDefaultSetupConfig(env map[string]string, cfg SetupConfig) (string, error) {
	path, err := DefaultSetupConfigPath(env)
	if err != nil {
		return "", err
	}
	if err := SaveSetupConfig(path, cfg); err != nil {
		return "", err
	}
	return path, nil
}

func SaveSetupConfig(path string, cfg SetupConfig) error {
	cfg.Metadata.Version = setupConfigVersion
	cfg.Metadata.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := validateSetupConfig(cfg, true); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
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

func (c ClientConnectionConfig) EndpointRaw() (string, error) {
	endpoint := strings.TrimSpace(c.Endpoint)
	if endpoint == "" {
		return "", errors.New("endpoint is required")
	}
	return endpoint, nil
}

func validateSetupConfig(cfg SetupConfig, strict bool) error {
	if cfg.Metadata.Version == 0 {
		cfg.Metadata.Version = setupConfigVersion
	}
	if cfg.Metadata.Version != setupConfigVersion {
		return fmt.Errorf("unsupported config version %d", cfg.Metadata.Version)
	}

	endpointRaw, err := cfg.Connection.EndpointRaw()
	if err != nil {
		if strict {
			return err
		}
	} else if _, err := ParseEndpoint(endpointRaw); err != nil {
		return err
	}

	if strict && strings.TrimSpace(cfg.Auth.Token) == "" {
		return errors.New("token is required")
	}
	return nil
}

func mergeSetupConfig(fileCfg SetupConfig, env map[string]string) (SetupConfig, error) {
	merged := fileCfg

	if endpointRaw := strings.TrimSpace(env["SYMTERM_ENDPOINT"]); endpointRaw != "" {
		endpoint, err := ParseEndpoint(endpointRaw)
		if err != nil {
			return SetupConfig{}, err
		}
		merged.Connection = connectionConfigFromEndpoint(endpoint)
	}

	if token := strings.TrimSpace(env["SYMTERM_TOKEN"]); token != "" {
		merged.Auth.Token = token
	}
	if knownHosts, ok := env["SYMTERM_SSH_KNOWN_HOSTS"]; ok {
		merged.Auth.SSHDisableHostKeyCheck = strings.TrimSpace(knownHosts) == ""
		merged.Auth.SSHKnownHostsPath = strings.TrimSpace(knownHosts)
	}

	return merged, nil
}

func connectionConfigFromEndpoint(endpoint Endpoint) ClientConnectionConfig {
	return ClientConnectionConfig{
		Endpoint: "symterm://" + endpoint.Target,
	}
}

func resolveUserHomeDir(env map[string]string) (string, error) {
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if value := strings.TrimSpace(env[key]); value != "" {
			return value, nil
		}
	}
	drive := strings.TrimSpace(env["HOMEDRIVE"])
	path := strings.TrimSpace(env["HOMEPATH"])
	if drive != "" && path != "" {
		return drive + path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("cannot resolve user home directory")
	}
	return home, nil
}
