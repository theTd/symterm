package client

import (
	"context"
	"io"

	"symterm/internal/app"
	"symterm/internal/config"
	"symterm/internal/proto"
	"symterm/internal/transport"
)

type controlFlowLifecycle = app.ProjectSessionLifecycle

func runControlFlow(
	ctx context.Context,
	controlClient *transport.Client,
	cfg config.ClientConfig,
	streamStdin io.Reader,
	streamStdout io.Writer,
	streamStderr io.Writer,
	lifecycle controlFlowLifecycle,
) (Result, error) {
	useCase := app.ProjectSessionUseCase{
		ControlClient: controlClient,
		Config:        cfg,
		Lifecycle:     lifecycle,
		SessionKind:   proto.SessionKindInteractive,
		SyncFeedback:  newInitialSyncFeedback(streamStdout),
		Tracef:        newTraceLogger(cfg.Verbose, streamStderr).Printf,
	}
	return useCase.ConnectAndStartCommand(ctx, app.SessionIO{
		Stdin:  streamStdin,
		Stdout: streamStdout,
		Stderr: streamStderr,
	})
}
