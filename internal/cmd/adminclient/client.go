package adminclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"symterm/internal/admin"
)

func Run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(adminUsage())
	}
	socketPath, err := resolveSocketPath()
	if err != nil {
		return err
	}
	client, err := admin.DialAdminSocket(socketPath)
	if err != nil {
		return err
	}
	defer client.Close()

	switch args[0] {
	case "daemon":
		if len(args) == 2 && args[1] == "info" {
			var info admin.DaemonInfo
			if err := client.Call(ctx, "admin_get_daemon_info", nil, &info); err != nil {
				return err
			}
			return printJSON(stdout, info)
		}
	case "sessions":
		return runSessions(ctx, client, args[1:], stdout)
	case "users":
		return runUsers(ctx, client, args[1:], stdout)
	}
	_, _ = io.WriteString(stderr, adminUsage()+"\n")
	return errors.New("unknown admin command")
}

func runSessions(ctx context.Context, client *admin.SocketClient, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing sessions subcommand")
	}
	switch args[0] {
	case "list":
		var sessions []any
		if err := client.Call(ctx, "admin_list_sessions", nil, &sessions); err != nil {
			return err
		}
		return printJSON(stdout, sessions)
	case "inspect":
		if len(args) < 2 {
			return errors.New("missing session id")
		}
		var session any
		if err := client.Call(ctx, "admin_get_session", map[string]string{"session_id": args[1]}, &session); err != nil {
			return err
		}
		return printJSON(stdout, session)
	case "terminate":
		if len(args) < 2 {
			return errors.New("missing session id")
		}
		return client.Call(ctx, "admin_terminate_session", map[string]string{
			"actor":      "cli-admin",
			"session_id": args[1],
		}, nil)
	default:
		return errors.New("unknown sessions subcommand")
	}
}

func runUsers(ctx context.Context, client *admin.SocketClient, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing users subcommand")
	}
	switch args[0] {
	case "list":
		var users []any
		if err := client.Call(ctx, "admin_list_users", nil, &users); err != nil {
			return err
		}
		return printJSON(stdout, users)
	case "create":
		if len(args) < 2 {
			return errors.New("missing username")
		}
		var user any
		if err := client.Call(ctx, "admin_create_user", map[string]string{
			"actor":    "cli-admin",
			"username": args[1],
		}, &user); err != nil {
			return err
		}
		return printJSON(stdout, user)
	case "disable":
		if len(args) < 2 {
			return errors.New("missing username")
		}
		var user any
		if err := client.Call(ctx, "admin_disable_user", map[string]string{
			"actor":    "cli-admin",
			"username": args[1],
		}, &user); err != nil {
			return err
		}
		return printJSON(stdout, user)
	case "token":
		if len(args) < 2 {
			return errors.New("missing token subcommand")
		}
		switch args[1] {
		case "issue":
			if len(args) < 3 {
				return errors.New("missing username")
			}
			var token admin.IssuedToken
			if err := client.Call(ctx, "admin_issue_user_token", map[string]string{
				"actor":    "cli-admin",
				"username": args[2],
			}, &token); err != nil {
				return err
			}
			return printJSON(stdout, token)
		case "revoke":
			if len(args) < 3 {
				return errors.New("missing token id")
			}
			var record any
			if err := client.Call(ctx, "admin_revoke_user_token", map[string]string{
				"actor":    "cli-admin",
				"token_id": args[2],
			}, &record); err != nil {
				return err
			}
			return printJSON(stdout, record)
		}
	case "entrypoint":
		if len(args) < 2 {
			return errors.New("missing entrypoint subcommand")
		}
		switch args[1] {
		case "get":
			if len(args) < 3 {
				return errors.New("missing username")
			}
			var value any
			if err := client.Call(ctx, "admin_get_user_entrypoint", map[string]string{
				"username": args[2],
			}, &value); err != nil {
				return err
			}
			return printJSON(stdout, value)
		case "set":
			if len(args) < 5 {
				return errors.New("usage: symterm admin users entrypoint set <username> -- <argv>")
			}
			sep := -1
			for i := 3; i < len(args); i++ {
				if args[i] == "--" {
					sep = i
					break
				}
			}
			if sep < 0 || sep == len(args)-1 {
				return errors.New("missing -- <argv>")
			}
			var user any
			if err := client.Call(ctx, "admin_set_user_entrypoint", map[string]any{
				"actor":      "cli-admin",
				"username":   args[2],
				"entrypoint": args[sep+1:],
			}, &user); err != nil {
				return err
			}
			return printJSON(stdout, user)
		}
	}
	return errors.New("unknown users subcommand")
}

func resolveSocketPath() (string, error) {
	if value := strings.TrimSpace(os.Getenv("SYMTERM_ADMIN_SOCKET")); value != "" {
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".symterm", "admin.sock"), nil
}

func printJSON(writer io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "%s\n", data)
	return err
}

func adminUsage() string {
	return strings.TrimSpace(`Usage:
  symterm admin daemon info
  symterm admin sessions list
  symterm admin sessions inspect <session-id>
  symterm admin sessions terminate <session-id>
  symterm admin users list
  symterm admin users create <username>
  symterm admin users disable <username>
  symterm admin users token issue <username>
  symterm admin users token revoke <token-id>
  symterm admin users entrypoint get <username>
  symterm admin users entrypoint set <username> -- <argv>`)
}
