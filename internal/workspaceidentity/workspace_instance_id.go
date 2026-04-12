package workspaceidentity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Generator struct {
	ConfigDir string
	Random    io.Reader
}

func DefaultWorkspaceInstanceID(workdir string) (string, error) {
	return Generator{}.WorkspaceInstanceID(workdir)
}

func (g Generator) WorkspaceInstanceID(workdir string) (string, error) {
	installID, err := g.installID()
	if err != nil {
		return "", err
	}
	normalizedPath, err := normalizeWorkspacePath(workdir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(installID + "\n" + normalizedPath))
	return "wsi-" + hex.EncodeToString(sum[:16]), nil
}

func (g Generator) installID() (string, error) {
	path, err := g.installIDPath()
	if err != nil {
		return "", err
	}
	if data, err := os.ReadFile(path); err == nil {
		value := strings.TrimSpace(string(data))
		if value != "" {
			return value, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read workspace install id: %w", err)
	}

	value, err := g.newInstallID()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create workspace identity directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write workspace install id: %w", err)
	}
	return value, nil
}

func (g Generator) installIDPath() (string, error) {
	configDir := strings.TrimSpace(g.ConfigDir)
	if configDir == "" {
		var err error
		configDir, err = os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user config directory: %w", err)
		}
	}
	return filepath.Join(configDir, "symterm", "install-id"), nil
}

func (g Generator) newInstallID() (string, error) {
	reader := g.Random
	if reader == nil {
		reader = rand.Reader
	}
	buf := make([]byte, 16)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", fmt.Errorf("generate workspace install id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func normalizeWorkspacePath(workdir string) (string, error) {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	absPath, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	normalized := filepath.Clean(absPath)
	if resolved, err := filepath.EvalSymlinks(normalized); err == nil && strings.TrimSpace(resolved) != "" {
		normalized = filepath.Clean(resolved)
	}
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	return normalized, nil
}
