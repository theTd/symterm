package client

import (
	"context"
	"fmt"
	"io"
	"os"

	"symterm/internal/app"
	"symterm/internal/config"
	"symterm/internal/proto"
)

type Result = app.ProjectSessionResult

func Run(ctx context.Context, cfg config.ClientConfig, stdout io.Writer, stderr io.Writer) error {
	trace := newTraceLogger(cfg.Verbose, stderr)
	trace.Printf(
		"client start endpoint=%s target=%s project=%s workdir=%q confirm_reconcile=%t argv=%q",
		cfg.Endpoint.Kind,
		cfg.Endpoint.Target,
		cfg.ProjectID,
		cfg.Workdir,
		cfg.ConfirmReconcile,
		cfg.ArgvTail,
	)
	result, err := executeClientWithStreams(ctx, cfg, os.Stdin, stdout, stderr)
	if err != nil {
		trace.Printf("client finished with error=%v", err)
		return err
	}

	fmt.Fprintf(
		stdout,
		"project=%s role=%s state=%s cursor=%d\n",
		result.Snapshot.ProjectID,
		result.Snapshot.Role,
		result.Snapshot.ProjectState,
		result.Snapshot.CurrentCursor,
	)
	if result.Command != nil {
		fmt.Fprintf(stdout, "command=%s argv=%d\n", result.Command.CommandID, len(cfg.ArgvTail))
		if terminal, ok := app.LatestTerminalCommandEvent(result.Events); ok {
			switch terminal.Type {
			case proto.CommandEventExited:
				exitCode := -1
				if terminal.ExitCode != nil {
					exitCode = *terminal.ExitCode
				}
				fmt.Fprintf(stdout, "exit=%d\n", exitCode)
			case proto.CommandEventExecFailed:
				fmt.Fprintf(stdout, "exec_failed=%s\n", terminal.Message)
			}
		}
	}
	trace.Printf("client finished successfully")
	_ = stderr
	return nil
}

func executeClientWithStreams(
	ctx context.Context,
	cfg config.ClientConfig,
	streamStdin io.Reader,
	streamStdout io.Writer,
	streamStderr io.Writer,
) (Result, error) {
	if cfg.Endpoint.Kind == config.EndpointSSH {
		return executeSSHEndpoint(ctx, cfg, streamStdin, streamStdout, streamStderr)
	}
	return Result{}, fmt.Errorf("unsupported endpoint kind %q", cfg.Endpoint.Kind)
}
