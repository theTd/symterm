package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"symterm/internal/proto"
)

const (
	tmuxStatusIntervalSeconds = 2
	tmuxStatusLeftMaxProject  = 24
	envTmuxStatusLeft         = "SYMTERM_TMUX_STATUS_LEFT"
	envTmuxStatusRight        = "SYMTERM_TMUX_STATUS_RIGHT"
)

var tmuxLookPath = exec.LookPath
var currentExecutablePath = os.Executable

type tmuxLaunchPlan struct {
	binary string
	args   []string
}

func buildTmuxLaunchPlan(
	adminSocketPath string,
	projectKey proto.ProjectKey,
	command proto.CommandSnapshot,
	entrypoint []string,
) (tmuxLaunchPlan, error) {
	if !command.TTY.Interactive {
		return tmuxLaunchPlan{}, errors.New("tmux status mode requires an interactive TTY")
	}
	if len(entrypoint) == 0 || strings.TrimSpace(entrypoint[0]) == "" {
		return tmuxLaunchPlan{}, errors.New("remote entrypoint is empty")
	}

	tmuxBinary, err := tmuxLookPath("tmux")
	if err != nil {
		return tmuxLaunchPlan{}, errors.New("tmux is required for --tmux-status but was not found in PATH")
	}
	helperBinary, err := currentExecutablePath()
	if err != nil {
		return tmuxLaunchPlan{}, fmt.Errorf("resolve daemon executable for tmux helper: %w", err)
	}

	sessionName := tmuxSessionName(projectKey, command.CommandID)
	commandLine, err := tmuxExecShellCommand(append(append([]string(nil), entrypoint...), command.ArgvTail...))
	if err != nil {
		return tmuxLaunchPlan{}, err
	}
	helperCommand, err := tmuxHelperCommand(helperBinary, adminSocketPath, command.StartedBy, command.CommandID)
	if err != nil {
		return tmuxLaunchPlan{}, err
	}

	args := []string{
		"new-session",
		"-d",
		"-s", sessionName,
		"-x", strconv.Itoa(normalizeTerminalDimension(command.TTY.Columns, 80)),
		"-y", strconv.Itoa(normalizeTerminalDimension(command.TTY.Rows, 24)),
		commandLine,
		";",
		"set-option", "-q", "-t", sessionName, "status", "on",
		";",
		"set-option", "-q", "-t", sessionName, "status-interval", strconv.Itoa(tmuxStatusIntervalSeconds),
		";",
		"set-option", "-q", "-t", sessionName, "status-left", tmuxStatusLeft(projectKey.Username, projectKey.ProjectID, command.StartedByRole),
		";",
		"set-option", "-q", "-t", sessionName, "status-right", tmuxStatusRight(helperCommand),
		";",
		"set-option", "-q", "-t", sessionName, "status-left-length", strconv.Itoa(tmuxStatusLeftMaxProject + 24),
		";",
		"set-option", "-q", "-t", sessionName, "status-right-length", "96",
		";",
		"set-option", "-q", "-t", sessionName, "destroy-unattached", "on",
		";",
		"attach-session", "-t", sessionName,
	}
	return tmuxLaunchPlan{
		binary: tmuxBinary,
		args:   args,
	}, nil
}

func tmuxSessionName(projectKey proto.ProjectKey, commandID string) string {
	base := "symterm_" + projectKey.String() + "_" + strings.TrimSpace(commandID)
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '_' || r == '-':
			return r
		default:
			return '_'
		}
	}, base)
	base = strings.Trim(base, "_")
	if base == "" {
		return "symterm"
	}
	const limit = 64
	if len(base) <= limit {
		return base
	}
	return base[:limit]
}

func tmuxStatusLeft(username string, projectID string, role proto.Role) string {
	if template := strings.TrimSpace(os.Getenv(envTmuxStatusLeft)); template != "" {
		return expandTmuxStatusTemplate(template, tmuxTemplateValues{
			User:    strings.TrimSpace(username),
			Project: truncateForStatus(projectID, tmuxStatusLeftMaxProject),
			Role:    strings.TrimSpace(string(role)),
		})
	}
	return " " + expandTmuxStatusTemplate("{brand} | {user}@{project}", tmuxTemplateValues{
		User:    strings.TrimSpace(username),
		Project: truncateForStatus(projectID, tmuxStatusLeftMaxProject),
		Role:    strings.TrimSpace(string(role)),
	}) + " "
}

func tmuxStatusRight(helperCommand string) string {
	if template := strings.TrimSpace(os.Getenv(envTmuxStatusRight)); template != "" {
		return expandTmuxStatusTemplate(template, tmuxTemplateValues{
			StatusCommand: "#(" + helperCommand + ")",
			Clock:         "%H:%M",
		})
	}
	return fmt.Sprintf("#(%s) | %%H:%%M ", helperCommand)
}

func truncateForStatus(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func tmuxExecShellCommand(argv []string) (string, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return "", errors.New("tmux launch command is empty")
	}
	quoted := make([]string, 0, len(argv))
	for _, item := range argv {
		quoted = append(quoted, tmuxShellQuote(item))
	}
	return "exec " + strings.Join(quoted, " "), nil
}

func tmuxHelperCommand(helperBinary string, adminSocketPath string, clientID string, commandID string) (string, error) {
	if strings.TrimSpace(helperBinary) == "" {
		return "", errors.New("tmux helper binary path is empty")
	}
	if strings.TrimSpace(adminSocketPath) == "" {
		return "", errors.New("tmux helper admin socket path is empty")
	}
	if strings.TrimSpace(clientID) == "" {
		return "", errors.New("tmux helper client id is empty")
	}
	if strings.TrimSpace(commandID) == "" {
		return "", errors.New("tmux helper command id is empty")
	}
	argv := []string{
		helperBinary,
		"internal",
		"tmux-status",
		"--admin-socket", adminSocketPath,
		"--client-id", clientID,
		"--command-id", commandID,
	}
	quoted := make([]string, 0, len(argv))
	for _, item := range argv {
		quoted = append(quoted, tmuxShellQuote(item))
	}
	return strings.Join(quoted, " "), nil
}

func tmuxShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

type tmuxTemplateValues struct {
	User          string
	Project       string
	Role          string
	StatusCommand string
	Clock         string
}

func expandTmuxStatusTemplate(template string, values tmuxTemplateValues) string {
	user := values.User
	if user == "" {
		user = "user"
	}
	project := values.Project
	if project == "" {
		project = "project"
	}
	role := values.Role
	if role == "" {
		role = "unknown"
	}
	replacer := strings.NewReplacer(
		"{brand}", "symterm",
		"{user}", user,
		"{project}", project,
		"{role}", role,
		"{status}", values.StatusCommand,
		"{clock}", values.Clock,
	)
	return replacer.Replace(template)
}
