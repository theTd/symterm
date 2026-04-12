package daemon

import (
	"strings"
	"testing"

	"symterm/internal/proto"
)

func TestTmuxSessionNameSanitizesAndBounds(t *testing.T) {
	t.Parallel()

	name := tmuxSessionName(proto.ProjectKey{
		Username:  "alice@example.com",
		ProjectID: "demo/project:with spaces",
	}, "cmd:0001")
	if strings.ContainsAny(name, " :/@.") {
		t.Fatalf("tmuxSessionName() = %q, want sanitized name", name)
	}
	if !strings.HasPrefix(name, "symterm_") {
		t.Fatalf("tmuxSessionName() = %q, want symterm prefix", name)
	}
	if len(name) > 64 {
		t.Fatalf("len(tmuxSessionName()) = %d, want <= 64", len(name))
	}
}

func TestTmuxExecShellCommandQuotesSpecialCharacters(t *testing.T) {
	t.Parallel()

	command, err := tmuxExecShellCommand([]string{
		"/bin/example tool",
		"arg with spaces",
		`double"quote`,
		"single'quote",
	})
	if err != nil {
		t.Fatalf("tmuxExecShellCommand() error = %v", err)
	}
	want := "exec '/bin/example tool' 'arg with spaces' 'double\"quote' 'single'\"'\"'quote'"
	if command != want {
		t.Fatalf("tmuxExecShellCommand() = %q, want %q", command, want)
	}
}

func TestBuildTmuxLaunchPlanWrapsCommandAndHelper(t *testing.T) {
	t.Parallel()

	originalLookPath := tmuxLookPath
	originalExecutable := currentExecutablePath
	t.Cleanup(func() {
		tmuxLookPath = originalLookPath
		currentExecutablePath = originalExecutable
	})
	tmuxLookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	currentExecutablePath = func() (string, error) { return "/opt/symtermd", nil }

	plan, err := buildTmuxLaunchPlan(
		"/var/run/symterm/admin.sock",
		proto.ProjectKey{Username: "alice", ProjectID: "demo"},
		proto.CommandSnapshot{
			CommandID:     "cmd-0001",
			ArgvTail:      []string{"bash", "-lc", "printf hello"},
			StartedBy:     "client-0001",
			StartedByRole: proto.RoleFollower,
			TmuxStatus:    true,
			TTY: proto.TTYSpec{
				Interactive: true,
				Columns:     120,
				Rows:        40,
			},
		},
		[]string{"/bin/env", "ENTRY=1"},
	)
	if err != nil {
		t.Fatalf("buildTmuxLaunchPlan() error = %v", err)
	}
	if plan.binary != "/usr/bin/tmux" {
		t.Fatalf("plan.binary = %q, want /usr/bin/tmux", plan.binary)
	}
	joined := strings.Join(plan.args, " ")
	for _, want := range []string{
		"new-session",
		"attach-session",
		"status-left",
		"status-right",
		"destroy-unattached",
		"symterm | alice@demo",
		"/opt/symtermd' 'internal' 'tmux-status'",
		"--client-id' 'client-0001'",
		"--command-id' 'cmd-0001'",
		"exec '/bin/env' 'ENTRY=1' 'bash' '-lc' 'printf hello'",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("tmux args missing %q: %q", want, joined)
		}
	}
}

func TestTmuxStatusTemplateUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv(envTmuxStatusLeft, " {brand}:{project}:{role} ")
	t.Setenv(envTmuxStatusRight, " {status} :: {clock} ")

	left := tmuxStatusLeft("alice", "demo-project", proto.RoleOwner)
	right := tmuxStatusRight("helper-cmd")
	if left != "symterm:demo-project:owner" {
		t.Fatalf("tmuxStatusLeft() = %q", left)
	}
	if right != "#(helper-cmd) :: %H:%M" {
		t.Fatalf("tmuxStatusRight() = %q", right)
	}
}

func TestTmuxStatusTemplateSupportsUserPlaceholder(t *testing.T) {
	t.Setenv(envTmuxStatusLeft, "{user}@{project}:{role}")

	left := tmuxStatusLeft("alice", "demo", proto.RoleFollower)
	if left != "alice@demo:follower" {
		t.Fatalf("tmuxStatusLeft() = %q", left)
	}
}

func TestTmuxStatusLeftDefaultFormat(t *testing.T) {
	left := tmuxStatusLeft("alice", "demo", proto.RoleOwner)
	if left != " symterm | alice@demo " {
		t.Fatalf("tmuxStatusLeft() = %q", left)
	}
}
