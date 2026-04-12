package client

import (
	"context"
	"io"

	"symterm/internal/config"
	endpointssh "symterm/internal/ssh"
	"symterm/internal/transport"
)

func executeSSHEndpoint(ctx context.Context, cfg config.ClientConfig, streamStdin io.Reader, streamStdout io.Writer, streamStderr io.Writer) (Result, error) {
	trace := newTraceLogger(cfg.Verbose, streamStderr)
	trace.Printf("endpoint=ssh start target=%s", cfg.Endpoint.Target)
	prompter := newHostKeyPrompter(streamStdin, streamStdout, streamStderr)
	initialSyncFeedback := newInitialSyncFeedback(streamStdout)

	releaseAuthorityLease, err := acquireAuthorityLease(ctx, cfg, trace.Printf, prompter, initialSyncFeedback)
	if err != nil {
		return Result{}, err
	}
	defer releaseAuthorityLease()

	sshClient, err := endpointssh.DialClientWithPrompter(
		ctx,
		cfg,
		prompter,
	)
	if err != nil {
		return Result{}, err
	}

	controlConn, err := transport.OpenSSHControlChannel(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return Result{}, err
	}
	trace.Printf("endpoint=ssh connected control channel")

	controlClient := transport.NewClient(controlConn, controlConn)
	lifecycle := newServiceLifecycle(
		func(context.Context) (*transport.StdioPipeClient, io.Closer, error) {
			return transport.NewSSHStdioPipeClient(func(_ context.Context, clientID string, commandID string) (io.ReadWriteCloser, error) {
				trace.Printf("endpoint=ssh open stdio channel client_id=%s command_id=%s", clientID, commandID)
				return transport.OpenSSHStdioChannel(sshClient, clientID, commandID)
			}), transport.NoOpCloser{}, nil
		},
		func(_ context.Context, clientID string) (io.ReadWriteCloser, error) {
			trace.Printf("endpoint=ssh open ownerfs channel client_id=%s", clientID)
			return transport.OpenSSHOwnerFSChannel(sshClient, clientID)
		},
		trace.Printf,
		func() {
			_ = controlConn.Close()
			_ = sshClient.Close()
		},
	)

	return runControlFlow(
		ctx,
		controlClient,
		cfg,
		streamStdin,
		streamStdout,
		streamStderr,
		lifecycle,
	)
}
