package transport

import (
	"context"

	"symterm/internal/proto"
)

type dispatchRoute struct {
	invoke func(context.Context, Request) (Response, string)
}

type completeInitialSyncParams struct {
	SyncEpoch uint64 `json:"sync_epoch"`
}

type fsReadParams struct {
	Op      proto.FsOperation `json:"op"`
	Request proto.FsRequest   `json:"request"`
}

func (s *Server) dispatch(ctx context.Context, request Request) (Response, string) {
	route, ok := s.dispatchRoutes[request.Method]
	if !ok {
		return Response{
			ID: request.ID,
			Error: &ResponseError{
				Code:    string(proto.ErrInvalidArgument),
				Message: "unknown method",
			},
		}, ""
	}
	return route.invoke(ctx, request)
}

func (s *Server) newDispatchRoutes() map[string]dispatchRoute {
	return map[string]dispatchRoute{
		"hello": newDispatchRoute(func(ctx context.Context, _ Request, params proto.HelloRequest) (any, string, error) {
			if s.principal == nil {
				return nil, "", proto.NewError(proto.ErrAuthenticationFailed, "hello requires an authenticated SSH control channel")
			}
			result, err := s.service.HelloAuthenticated(ctx, *s.principal, params)
			if err != nil {
				return nil, "", err
			}
			return result, result.ClientID, nil
		}),
		"open_project_session": newDispatchRoute(func(_ context.Context, request Request, params proto.OpenProjectSessionRequest) (any, string, error) {
			result, err := s.service.OpenProjectSession(request.ClientID, params)
			return result, "", err
		}),
		"ensure_project": newDispatchRoute(func(_ context.Context, request Request, params proto.EnsureProjectRequest) (any, string, error) {
			result, err := s.service.EnsureProjectRequest(request.ClientID, params)
			return result, "", err
		}),
		internalCompleteInitialSyncMethod: newDispatchRoute(func(_ context.Context, request Request, params completeInitialSyncParams) (any, string, error) {
			result, err := s.service.CompleteInitialSync(request.ClientID, params.SyncEpoch)
			return result, "", err
		}),
		"confirm_reconcile": newDispatchRoute(func(_ context.Context, request Request, params proto.ConfirmReconcileRequest) (any, string, error) {
			result, err := s.service.ConfirmReconcile(request.ClientID, params)
			return result, "", err
		}),
		"resume_project_session": newDispatchRoute(func(_ context.Context, request Request, params proto.ResumeProjectSessionRequest) (any, string, error) {
			result, err := s.service.ResumeProjectSession(request.ClientID, params)
			return result, "", err
		}),
		"watch_project": newDispatchRoute(func(_ context.Context, request Request, params proto.WatchProjectRequest) (any, string, error) {
			result, err := s.service.WatchProject(request.ClientID, params)
			return result, "", err
		}),
		internalReportSyncProgressMethod: newDispatchNoResultRoute(func(_ context.Context, request Request, params proto.ReportSyncProgressRequest) error {
			return s.service.ReportSyncProgress(request.ClientID, params)
		}),
		"begin_sync": newDispatchNoResultRoute(func(_ context.Context, request Request, params proto.BeginSyncRequest) error {
			return s.service.BeginSync(request.ClientID, params)
		}),
		"scan_manifest": newDispatchNoResultRoute(func(_ context.Context, request Request, params proto.ScanManifestRequest) error {
			return s.service.ScanManifest(request.ClientID, params)
		}),
		"plan_manifest_hashes": newDispatchRoute(func(_ context.Context, request Request, _ struct{}) (any, string, error) {
			result, err := s.service.PlanManifestHashes(request.ClientID)
			return result, "", err
		}),
		"plan_sync_actions": newDispatchRoute(func(_ context.Context, request Request, _ struct{}) (any, string, error) {
			result, err := s.service.PlanSyncActions(request.ClientID)
			return result, "", err
		}),
		"begin_file": newDispatchRoute(func(_ context.Context, request Request, params proto.BeginFileRequest) (any, string, error) {
			result, err := s.service.BeginFile(request.ClientID, params)
			return result, "", err
		}),
		"apply_chunk": newDispatchNoResultRoute(func(_ context.Context, request Request, params proto.ApplyChunkRequest) error {
			return s.service.ApplyChunk(request.ClientID, params)
		}),
		"commit_file": newDispatchNoResultRoute(func(_ context.Context, request Request, params proto.CommitFileRequest) error {
			return s.service.CommitFile(request.ClientID, params)
		}),
		"abort_file": newDispatchNoResultRoute(func(_ context.Context, request Request, params proto.AbortFileRequest) error {
			return s.service.AbortFile(request.ClientID, params)
		}),
		"delete_path": newDispatchNoResultRoute(func(_ context.Context, request Request, params proto.DeletePathRequest) error {
			return s.service.DeletePath(request.ClientID, params)
		}),
		"finalize_sync": newDispatchRoute(func(_ context.Context, request Request, params proto.FinalizeSyncRequest) (any, string, error) {
			result, err := s.service.FinalizeSync(request.ClientID, params)
			return result, "", err
		}),
		"fs_read": newDispatchRoute(func(ctx context.Context, request Request, params fsReadParams) (any, string, error) {
			result, err := s.service.FsReadContext(ctx, request.ClientID, params.Op, params.Request)
			return result, "", err
		}),
		"fs_mutation": newDispatchRoute(func(ctx context.Context, request Request, params proto.FsMutationRequest) (any, string, error) {
			result, err := s.service.FsMutationContext(ctx, request.ClientID, params)
			return result, "", err
		}),
		"start_command": newDispatchRoute(func(_ context.Context, request Request, params proto.StartCommandRequest) (any, string, error) {
			result, err := s.service.StartCommand(request.ClientID, params)
			return result, "", err
		}),
		"start_project_command_session": newDispatchRoute(func(ctx context.Context, request Request, params proto.StartProjectCommandSessionRequest) (any, string, error) {
			result, err := s.service.StartProjectCommandSession(ctx, request.ClientID, params)
			return result, "", err
		}),
		"attach_stdio": newDispatchRoute(func(_ context.Context, request Request, params proto.AttachStdioRequest) (any, string, error) {
			result, err := s.service.AttachStdio(request.ClientID, params)
			return result, "", err
		}),
		"resize_tty": newDispatchRoute(func(_ context.Context, request Request, params proto.ResizeTTYRequest) (any, string, error) {
			result, err := s.service.ResizeTTY(request.ClientID, params)
			return result, "", err
		}),
		"send_signal": newDispatchRoute(func(_ context.Context, request Request, params proto.SendSignalRequest) (any, string, error) {
			result, err := s.service.SendSignal(request.ClientID, params)
			return result, "", err
		}),
		"watch_command": newDispatchRoute(func(_ context.Context, request Request, params proto.WatchCommandRequest) (any, string, error) {
			result, err := s.service.WatchCommand(request.ClientID, params)
			return result, "", err
		}),
		internalWatchInvalidateMethod: newDispatchRoute(func(_ context.Context, request Request, params proto.WatchInvalidateRequest) (any, string, error) {
			result, err := s.service.WatchInvalidate(request.ClientID, params)
			return result, "", err
		}),
		internalInvalidateMethod: newDispatchNoResultRoute(func(ctx context.Context, request Request, params proto.InvalidateRequest) error {
			return s.service.InvalidateContext(ctx, request.ClientID, params)
		}),
		internalOwnerWatcherFailedMethod: newDispatchNoResultRoute(func(ctx context.Context, request Request, params proto.OwnerWatcherFailureRequest) error {
			return s.service.OwnerWatcherFailedContext(ctx, request.ClientID, params)
		}),
	}
}

func newDispatchRoute[Params any](handle func(context.Context, Request, Params) (any, string, error)) dispatchRoute {
	return dispatchRoute{
		invoke: func(ctx context.Context, request Request) (Response, string) {
			var params Params
			if err := decodeParams(request.Params, &params); err != nil {
				return errorResponse(request.ID, err), ""
			}
			result, controlClientID, err := handle(ctx, request, params)
			if err != nil {
				return errorResponse(request.ID, err), ""
			}
			return writeDispatchResult(request.ID, result), controlClientID
		},
	}
}

func newDispatchNoResultRoute[Params any](handle func(context.Context, Request, Params) error) dispatchRoute {
	return newDispatchRoute(func(ctx context.Context, request Request, params Params) (any, string, error) {
		if err := handle(ctx, request, params); err != nil {
			return nil, "", err
		}
		return struct{}{}, "", nil
	})
}

func writeDispatchResult(requestID uint64, value any) Response {
	return resultResponse(requestID, value)
}
