package daemon

import (
	"path/filepath"
	"testing"

	"symterm/internal/proto"
)

func TestResolveProjectLayout(t *testing.T) {
	t.Parallel()

	layout, err := ResolveProjectLayout("/srv/symterm", proto.ProjectKey{
		Username:  "alice",
		ProjectID: "demo",
	})
	if err != nil {
		t.Fatalf("ResolveProjectLayout() error = %v", err)
	}

	if layout.Root != filepath.Join("/srv/symterm", "alice", "demo") {
		t.Fatalf("Root = %q", layout.Root)
	}
	if layout.WorkspaceDir != filepath.Join(layout.Root, "workspace") {
		t.Fatalf("WorkspaceDir = %q", layout.WorkspaceDir)
	}
	if layout.MountDir != filepath.Join(layout.Root, "mount") {
		t.Fatalf("MountDir = %q", layout.MountDir)
	}
	if layout.RuntimeDir != filepath.Join(layout.Root, "runtime") {
		t.Fatalf("RuntimeDir = %q", layout.RuntimeDir)
	}
	if layout.CommandsDir != filepath.Join(layout.Root, "commands") {
		t.Fatalf("CommandsDir = %q", layout.CommandsDir)
	}
}

func TestResolveProjectLayoutRejectsInvalidSegments(t *testing.T) {
	t.Parallel()

	_, err := ResolveProjectLayout("/srv/symterm", proto.ProjectKey{
		Username:  "alice/bob",
		ProjectID: "demo",
	})
	if err == nil {
		t.Fatal("expected invalid username error")
	}

	_, err = ResolveProjectLayout("/srv/symterm", proto.ProjectKey{
		Username:  "alice",
		ProjectID: "..",
	})
	if err == nil {
		t.Fatal("expected invalid project id error")
	}
}
