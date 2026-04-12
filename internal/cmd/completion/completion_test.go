package completion

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunPrintsUsageWithoutShell(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	if err := Run(nil, &stdout); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Usage: symterm completion bash") {
		t.Fatalf("Run() output = %q", got)
	}
}

func TestRunPrintsBashScript(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	if err := Run([]string{"bash"}, &stdout); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	script := stdout.String()
	checks := []string{
		"_symterm_complete_run()",
		"_symterm_complete_admin()",
		`compgen -W "run admin setup version completion"`,
		`compgen -W "--project-id --confirm-reconcile --tmux-status -v --verbose --help --"`,
		`compgen -W "daemon sessions users"`,
		"complete -F _symterm_completion -o bashdefault -o default symterm",
	}
	for _, want := range checks {
		if !strings.Contains(script, want) {
			t.Fatalf("Run() output missing %q", want)
		}
	}
}

func TestRunRejectsUnsupportedShell(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	err := Run([]string{"zsh"}, &stdout)
	if err == nil {
		t.Fatal("Run() error = nil, want unsupported shell error")
	}
	if !strings.Contains(err.Error(), `unsupported shell "zsh"`) {
		t.Fatalf("Run() error = %v", err)
	}
}
