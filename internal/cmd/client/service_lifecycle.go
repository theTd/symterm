package client

import (
	"context"
	"io"
	stdsync "sync"

	"symterm/internal/proto"
	"symterm/internal/transport"
)

// serviceLifecycle only adapts endpoint resources into dedicated channels.
// Session business sequencing lives in the app-layer use case.
type serviceLifecycle struct {
	openStdioPipe      func(context.Context) (*transport.StdioPipeClient, io.Closer, error)
	openOwnerFileConn  func(context.Context, string) (io.ReadWriteCloser, error)
	tracef             func(string, ...any)
	closeOnce          stdsync.Once
	closeUnderlyingSSH func()
}

func newServiceLifecycle(
	openStdioPipe func(context.Context) (*transport.StdioPipeClient, io.Closer, error),
	openOwnerFileConn func(context.Context, string) (io.ReadWriteCloser, error),
	tracef func(string, ...any),
	closeUnderlyingSSH func(),
) *serviceLifecycle {
	return &serviceLifecycle{
		openStdioPipe:      openStdioPipe,
		openOwnerFileConn:  openOwnerFileConn,
		tracef:             tracef,
		closeUnderlyingSSH: closeUnderlyingSSH,
	}
}

func (s *serviceLifecycle) HasDedicatedStdio() bool {
	return s != nil && s.openStdioPipe != nil
}

func (s *serviceLifecycle) OpenDedicatedPipe(ctx context.Context) (*transport.StdioPipeClient, io.Closer, error) {
	if s == nil || s.openStdioPipe == nil {
		return nil, nil, proto.NewError(proto.ErrProjectNotReady, "dedicated stdio pipe is unavailable")
	}
	if s.tracef != nil {
		s.tracef("open dedicated stdio channel")
	}
	return s.openStdioPipe(ctx)
}

func (s *serviceLifecycle) OpenOwnerFileChannel(ctx context.Context, clientID string) (io.ReadWriteCloser, error) {
	if s == nil || s.openOwnerFileConn == nil {
		return nil, proto.NewError(proto.ErrProjectNotReady, "owner file channel is unavailable")
	}
	if s.tracef != nil {
		s.tracef("open owner file channel client_id=%s", clientID)
	}
	return s.openOwnerFileConn(ctx, clientID)
}

func (s *serviceLifecycle) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.closeUnderlyingSSH != nil {
			s.closeUnderlyingSSH()
		}
	})
}
