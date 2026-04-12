package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	RemoteEntryEnvKey         = "SYMTERMD_REMOTE_ENTRY"
	RemoteEntryArgsJSONEnvKey = "SYMTERMD_REMOTE_ENTRY_ARGS_JSON"
)

var defaultRemoteEntrypoint = []string{"bash"}

func ParseRemoteEntrypointEnv(env map[string]string) ([]string, error) {
	entryRaw, ok := env[RemoteEntryEnvKey]
	entryRaw = strings.TrimSpace(entryRaw)
	if strings.TrimSpace(env[RemoteEntryArgsJSONEnvKey]) != "" {
		return nil, errors.New("SYMTERMD_REMOTE_ENTRY_ARGS_JSON is no longer supported; use SYMTERMD_REMOTE_ENTRY as a JSON array")
	}
	if !ok || entryRaw == "" {
		return append([]string(nil), defaultRemoteEntrypoint...), nil
	}
	if strings.HasPrefix(entryRaw, "[") {
		return parseStructuredArgv(entryRaw, "SYMTERMD_REMOTE_ENTRY")
	}
	if strings.ContainsAny(entryRaw, "\r\n\t ") {
		return nil, errors.New("SYMTERMD_REMOTE_ENTRY must be a JSON array when specifying multiple argv items or paths with spaces")
	}
	return []string{entryRaw}, nil
}

func parseStructuredArgv(raw string, label string) ([]string, error) {
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil {
		return nil, fmt.Errorf("invalid %s JSON array", label)
	}
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return nil, fmt.Errorf("%s must contain at least one argv item", label)
	}
	return append([]string(nil), argv...), nil
}

func formatStructuredArgv(argv []string, label string) (string, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return "", fmt.Errorf("%s is empty", label)
	}

	rawArgs, err := json.Marshal(argv)
	if err != nil {
		return "", err
	}
	return string(rawArgs), nil
}
