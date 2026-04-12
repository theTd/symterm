package transport

import (
	"context"
	"errors"

	"symterm/internal/diagnostic"
	"symterm/internal/proto"
)

func (s *Server) streamAttachStdio(ctx context.Context, request Request) error {
	var params proto.AttachStdioRequest
	if err := decodeParams(request.Params, &params); err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	if err := s.service.RetainClient(request.ClientID); err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	defer func() {
		diagnostic.Cleanup(s.service.Diagnostics(), "release client "+request.ClientID+" after attach_stdio_stream", s.service.ReleaseClient(request.ClientID))
	}()
	liveAttach := true
	if err := s.service.OpenStdio(request.ClientID, params.CommandID); err != nil {
		if !isPostExitStdioRead(err) {
			return s.writeResponse(errorResponse(request.ID, err))
		}
		liveAttach = false
	}
	if liveAttach {
		defer func() {
			diagnostic.Cleanup(s.service.Diagnostics(), "detach stdio "+params.CommandID, s.service.DetachStdio(request.ClientID, params.CommandID))
		}()
	}

	for {
		result, err := s.service.AttachStdio(request.ClientID, params)
		if err != nil {
			return s.writeResponse(errorResponse(request.ID, err))
		}
		if err := s.writeResponse(resultResponse(request.ID, result)); err != nil {
			return err
		}
		if result.Complete {
			return nil
		}
		params.StdoutOffset = result.StdoutOffset
		params.StderrOffset = result.StderrOffset
		if err := s.service.WaitCommandOutput(ctx, request.ClientID, params); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return s.writeResponse(errorResponse(request.ID, err))
		}
	}
}

func (s *Server) streamWatchProject(ctx context.Context, request Request) error {
	var params proto.WatchProjectRequest
	if err := decodeParams(request.Params, &params); err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	if err := s.service.RetainClient(request.ClientID); err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	defer func() {
		diagnostic.Cleanup(s.service.Diagnostics(), "release client "+request.ClientID+" after watch_project_stream", s.service.ReleaseClient(request.ClientID))
	}()

	events, _, ch, unsubscribe, err := s.service.SubscribeProjectEvents(request.ClientID, params)
	if err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	defer unsubscribe()

	sinceCursor := params.SinceCursor
	for {
		for _, event := range events {
			if err := s.writeResponse(resultResponse(request.ID, newStreamItem(event))); err != nil {
				return err
			}
			sinceCursor = event.Cursor
			if event.Type == proto.ProjectEventInstanceTerminated {
				return writeDoneResponse[proto.ProjectEvent](s, request.ID)
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			events, err = s.service.WatchProject(request.ClientID, proto.WatchProjectRequest{
				ProjectID:   params.ProjectID,
				SinceCursor: sinceCursor,
			})
			if err != nil {
				return s.writeResponse(errorResponse(request.ID, err))
			}
		}
	}
}

func (s *Server) streamWatchCommand(ctx context.Context, request Request) error {
	var params proto.WatchCommandRequest
	if err := decodeParams(request.Params, &params); err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	if err := s.service.RetainClient(request.ClientID); err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	defer func() {
		diagnostic.Cleanup(s.service.Diagnostics(), "release client "+request.ClientID+" after watch_command_stream", s.service.ReleaseClient(request.ClientID))
	}()

	events, watcherID, ch, unsubscribe, err := s.service.SubscribeCommandEvents(request.ClientID, params)
	if err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	defer unsubscribe()

	sinceCursor := params.SinceCursor
	if err := s.writeCommandEvents(request.ID, events); err != nil {
		return err
	}
	if len(events) > 0 {
		sinceCursor = events[len(events)-1].Cursor
	}
	if hasTerminalCommandEvent(events) {
		return writeDoneResponse[proto.CommandEvent](s, request.ID)
	}
	_ = watcherID

	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			events, err = s.service.WatchCommand(request.ClientID, proto.WatchCommandRequest{
				CommandID:   params.CommandID,
				SinceCursor: sinceCursor,
			})
			if err != nil {
				return s.writeResponse(errorResponse(request.ID, err))
			}
			if len(events) == 0 {
				continue
			}
			if err := s.writeCommandEvents(request.ID, events); err != nil {
				return err
			}
			sinceCursor = events[len(events)-1].Cursor
			if hasTerminalCommandEvent(events) {
				return writeDoneResponse[proto.CommandEvent](s, request.ID)
			}
		}
	}
}

func (s *Server) streamWatchInvalidate(ctx context.Context, request Request) error {
	var params proto.WatchInvalidateRequest
	if err := decodeParams(request.Params, &params); err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	if err := s.service.RetainClient(request.ClientID); err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	defer func() {
		diagnostic.Cleanup(s.service.Diagnostics(), "release client "+request.ClientID+" after watch_invalidate_stream", s.service.ReleaseClient(request.ClientID))
	}()

	events, watcherID, ch, unsubscribe, err := s.service.SubscribeInvalidateEvents(request.ClientID, params)
	if err != nil {
		return s.writeResponse(errorResponse(request.ID, err))
	}
	defer unsubscribe()

	sinceCursor := params.SinceCursor
	if err := s.writeInvalidateEvents(request.ID, events); err != nil {
		return err
	}
	if len(events) > 0 {
		sinceCursor = events[len(events)-1].Cursor
	}
	_ = watcherID

	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			events, err = s.service.WatchInvalidate(request.ClientID, proto.WatchInvalidateRequest{
				ProjectID:   params.ProjectID,
				SinceCursor: sinceCursor,
			})
			if err != nil {
				return s.writeResponse(errorResponse(request.ID, err))
			}
			if err := s.writeInvalidateEvents(request.ID, events); err != nil {
				return err
			}
			if len(events) > 0 {
				sinceCursor = events[len(events)-1].Cursor
			}
		}
	}
}

func (s *Server) writeCommandEvents(requestID uint64, events []proto.CommandEvent) error {
	return writeEventStreamItems(s, requestID, events)
}

func hasTerminalCommandEvent(events []proto.CommandEvent) bool {
	for _, event := range events {
		switch event.Type {
		case proto.CommandEventExited, proto.CommandEventExecFailed:
			return true
		}
	}
	return false
}

func (s *Server) writeInvalidateEvents(requestID uint64, events []proto.InvalidateEvent) error {
	return writeEventStreamItems(s, requestID, events)
}

func writeEventStreamItems[T any](s *Server, requestID uint64, events []T) error {
	for _, event := range events {
		if err := s.writeResponse(resultResponse(requestID, newStreamItem(event))); err != nil {
			return err
		}
	}
	return nil
}

func writeDoneResponse[T any](s *Server, requestID uint64) error {
	return s.writeResponse(resultResponse(requestID, StreamItem[T]{Done: true}))
}

func newStreamItem[T any](event T) StreamItem[T] {
	copy := event
	return StreamItem[T]{Event: &copy}
}
