package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestServiceFsReadContextCancelsWhenProjectTerminates(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	expectedKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	service, clientID, projectKey, _ := newAuthorityProjectService(t, ServiceDependencies{
		Runtime: runtimeBackendStub{
			filesystemBackendStub: filesystemBackendStub{
				fsReadContext: func(ctx context.Context, key proto.ProjectKey, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
					if key != expectedKey {
						t.Fatalf("FsReadContext() key = %#v, want %#v", key, expectedKey)
					}
					close(entered)
					<-ctx.Done()
					return proto.FsReply{}, ctx.Err()
				},
			},
		},
		Now: fixedClock(),
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := service.FsReadContext(context.Background(), clientID, proto.FsOpRead, proto.FsRequest{Path: "docs/a.txt"})
		errCh <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("FsReadContext() did not reach backend")
	}

	if err := service.TerminateProject(projectKey, "test termination"); err != nil {
		t.Fatalf("TerminateProject() error = %v", err)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FsReadContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FsReadContext() did not return after project termination")
	}
}

func TestServiceFilesystemOperationsUseContextAwareBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*filesystemBackendStub, *bool)
		invoke    func(context.Context, *Service, string) error
	}{
		{
			name: "fs-read",
			configure: func(stub *filesystemBackendStub, called *bool) {
				stub.fsReadContext = func(ctx context.Context, _ proto.ProjectKey, _ proto.FsOperation, _ proto.FsRequest) (proto.FsReply, error) {
					*called = true
					return proto.FsReply{}, ctx.Err()
				}
			},
			invoke: func(ctx context.Context, service *Service, clientID string) error {
				_, err := service.FsReadContext(ctx, clientID, proto.FsOpRead, proto.FsRequest{Path: "docs/a.txt"})
				return err
			},
		},
		{
			name: "fs-mutation",
			configure: func(stub *filesystemBackendStub, called *bool) {
				stub.fsMutationContext = func(ctx context.Context, _ proto.ProjectKey, _ proto.FsOperation, _ proto.FsRequest, _ []proto.MutationPrecondition) (proto.FsReply, error) {
					*called = true
					return proto.FsReply{}, ctx.Err()
				}
			},
			invoke: func(ctx context.Context, service *Service, clientID string) error {
				_, err := service.FsMutationContext(ctx, clientID, proto.FsMutationRequest{
					Op:      proto.FsOpWrite,
					Request: proto.FsRequest{Path: "docs/a.txt", Data: []byte("x")},
				})
				return err
			},
		},
		{
			name: "invalidate",
			configure: func(stub *filesystemBackendStub, called *bool) {
				stub.applyOwnerInvalidationsContext = func(ctx context.Context, _ proto.ProjectKey, _ []proto.InvalidateChange) error {
					*called = true
					return ctx.Err()
				}
			},
			invoke: func(ctx context.Context, service *Service, clientID string) error {
				return service.InvalidateContext(ctx, clientID, proto.InvalidateRequest{
					Changes: []proto.InvalidateChange{{Path: "docs/a.txt", Kind: proto.InvalidateData}},
				})
			},
		},
		{
			name: "owner-watcher-failed",
			configure: func(stub *filesystemBackendStub, called *bool) {
				stub.enterConservativeModeContext = func(ctx context.Context, _ proto.ProjectKey, _ string) ([]proto.InvalidateChange, error) {
					*called = true
					return nil, ctx.Err()
				}
			},
			invoke: func(ctx context.Context, service *Service, clientID string) error {
				return service.OwnerWatcherFailedContext(ctx, clientID, proto.OwnerWatcherFailureRequest{Reason: "watch failed"})
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var called bool
			backend := filesystemBackendStub{}
			tc.configure(&backend, &called)

			service, clientID, _, snapshot := newAuthorityProjectService(t, ServiceDependencies{
				Runtime: runtimeBackendStub{
					filesystemBackendStub: backend,
				},
				Now: fixedClock(),
			})
			if _, err := service.CompleteInitialSync(clientID, snapshot.SyncEpoch); err != nil {
				t.Fatalf("CompleteInitialSync() error = %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := tc.invoke(ctx, service, clientID)
			if !called {
				t.Fatal("backend context method was not called")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("invoke() error = %v, want context.Canceled", err)
			}
		})
	}
}
