package workspaceidentity

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestWorkspaceInstanceIDIsStablePerInstallAndPath(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	workdir := filepath.Join(configDir, "workspace")
	generator := Generator{
		ConfigDir: configDir,
		Random:    bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
	}

	first, err := generator.WorkspaceInstanceID(workdir)
	if err != nil {
		t.Fatalf("WorkspaceInstanceID(first) error = %v", err)
	}
	second, err := generator.WorkspaceInstanceID(workdir)
	if err != nil {
		t.Fatalf("WorkspaceInstanceID(second) error = %v", err)
	}
	if first != second {
		t.Fatalf("WorkspaceInstanceID() = %q then %q, want stable value", first, second)
	}
}

func TestWorkspaceInstanceIDDiffersAcrossWorkspacePaths(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	generator := Generator{
		ConfigDir: configDir,
		Random:    bytes.NewReader(bytes.Repeat([]byte{0x11}, 16)),
	}

	left, err := generator.WorkspaceInstanceID(filepath.Join(configDir, "workspace-a"))
	if err != nil {
		t.Fatalf("WorkspaceInstanceID(left) error = %v", err)
	}
	right, err := generator.WorkspaceInstanceID(filepath.Join(configDir, "workspace-b"))
	if err != nil {
		t.Fatalf("WorkspaceInstanceID(right) error = %v", err)
	}
	if left == right {
		t.Fatalf("WorkspaceInstanceID() = %q for different workspace paths", left)
	}
}
