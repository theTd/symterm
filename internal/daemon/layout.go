package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"symterm/internal/proto"
)

type ProjectLayout struct {
	Root         string
	WorkspaceDir string
	MountDir     string
	RuntimeDir   string
	CommandsDir  string
}

type CommandLayout struct {
	Dir          string
	StdoutPath   string
	StderrPath   string
	ExitCodePath string
}

func ResolveProjectLayout(projectsRoot string, key proto.ProjectKey) (ProjectLayout, error) {
	if strings.TrimSpace(projectsRoot) == "" {
		return ProjectLayout{}, errors.New("projects root is required")
	}
	if err := validatePathSegment(key.Username, "username"); err != nil {
		return ProjectLayout{}, err
	}
	if err := validatePathSegment(key.ProjectID, "project id"); err != nil {
		return ProjectLayout{}, err
	}

	root := filepath.Join(projectsRoot, key.Username, key.ProjectID)
	return ProjectLayout{
		Root:         root,
		WorkspaceDir: filepath.Join(root, "workspace"),
		MountDir:     filepath.Join(root, "mount"),
		RuntimeDir:   filepath.Join(root, "runtime"),
		CommandsDir:  filepath.Join(root, "commands"),
	}, nil
}

func (l ProjectLayout) EnsureDirectories() error {
	for _, path := range []string{l.WorkspaceDir, l.MountDir, l.RuntimeDir, l.CommandsDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (l ProjectLayout) EnsureNonMountDirectories() error {
	for _, path := range []string{l.WorkspaceDir, l.RuntimeDir, l.CommandsDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (l ProjectLayout) ResolveCommandLayout(commandID string) (CommandLayout, error) {
	if err := validatePathSegment(commandID, "command id"); err != nil {
		return CommandLayout{}, err
	}

	dir := filepath.Join(l.CommandsDir, commandID)
	return CommandLayout{
		Dir:          dir,
		StdoutPath:   filepath.Join(dir, "stdout.log"),
		StderrPath:   filepath.Join(dir, "stderr.log"),
		ExitCodePath: filepath.Join(dir, "exit_code.txt"),
	}, nil
}

func validatePathSegment(value string, field string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New(field + " is required")
	}
	if value == "." || value == ".." {
		return errors.New(field + " must not be dot segments")
	}
	if strings.ContainsRune(value, filepath.Separator) {
		return errors.New(field + " must not contain path separators")
	}
	if filepath.Separator != '/' && strings.ContainsRune(value, '/') {
		return errors.New(field + " must not contain path separators")
	}
	return nil
}
