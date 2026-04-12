package app

import (
	"context"
	"io"
	"sync"

	"symterm/internal/diagnostic"
	"symterm/internal/proto"
	workspacesync "symterm/internal/sync"
	"symterm/internal/transport"
)

type ownerProjectRuntime struct {
	closeOnce sync.Once
	watchMu   sync.Mutex

	ctx         context.Context
	client      *transport.Client
	clientID    string
	workspace   workspacesync.OwnerWorkspaceRuntime
	stopWatcher func()
	closeFn     func()
	done        <-chan struct{}
}

func StartOwnerProjectRuntime(
	ctx context.Context,
	conn io.ReadWriteCloser,
	controlClient *transport.Client,
	clientID string,
	runtime workspacesync.OwnerWorkspaceRuntime,
) *ownerProjectRuntime {
	serviceCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		diagnostic.Background(
			diagnostic.Default(),
			"serve owner file channel",
			transport.ServeOwnerFileChannel(serviceCtx, conn, conn, runtime.FileService()),
		)
	}()

	var runtimeState *ownerProjectRuntime
	runtimeState = &ownerProjectRuntime{
		ctx:       serviceCtx,
		client:    controlClient,
		clientID:  clientID,
		workspace: runtime,
		done:      done,
		closeFn: func() {
			if stop := runtimeState.stopWatcherForClose(); stop != nil {
				stop()
			}
			cancel()
			diagnostic.Cleanup(diagnostic.Default(), "close owner file connection", conn.Close())
			<-done
		},
	}
	return runtimeState
}

func (r *ownerProjectRuntime) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.closeFn != nil {
			r.closeFn()
		}
	})
}

func (r *ownerProjectRuntime) Done() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.done
}

func (r *ownerProjectRuntime) Ensure(snapshot proto.ProjectSnapshot) {
	if r == nil {
		return
	}
	r.watchMu.Lock()
	defer r.watchMu.Unlock()

	if r.stopWatcher != nil {
		return
	}
	r.stopWatcher = r.workspace.Ensure(r.ctx, r.client, r.clientID, snapshot)
}

func (r *ownerProjectRuntime) stopWatcherForClose() func() {
	if r == nil {
		return nil
	}
	r.watchMu.Lock()
	defer r.watchMu.Unlock()

	stop := r.stopWatcher
	r.stopWatcher = nil
	return stop
}
