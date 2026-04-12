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

func ParseRemoteEntrypointEnv(env map[string]string) ([]string, error) {
	entryRaw, ok := env[RemoteEntryEnvKey]
	entryRaw = strings.TrimSpace(entryRaw)
	if !ok || entryRaw == "" {
		return nil, errors.New("missing SYMTERMD_REMOTE_ENTRY")
	}
	if strings.HasPrefix(entryRaw, "[") {
		if strings.TrimSpace(env[RemoteEntryArgsJSONEnvKey]) != "" {
			return nil, errors.New("SYMTERMD_REMOTE_ENTRY JSON array cannot be combined with SYMTERMD_REMOTE_ENTRY_ARGS_JSON")
		}
		return parseStructuredArgv(entryRaw, "SYMTERMD_REMOTE_ENTRY")
	}
	if strings.ContainsAny(entryRaw, "\r\n\t ") {
		return nil, errors.New("legacy SYMTERMD_REMOTE_ENTRY must contain exactly one argv item; use a JSON array for paths with spaces or additional args")
	}

	argsRaw := strings.TrimSpace(env[RemoteEntryArgsJSONEnvKey])
	if argsRaw == "" {
		return []string{entryRaw}, nil
	}

	entryArgs, err := parseStructuredArgv(argsRaw, "SYMTERMD_REMOTE_ENTRY_ARGS_JSON")
	if err != nil {
		return nil, err
	}
	return append([]string{entryRaw}, entryArgs...), nil
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
