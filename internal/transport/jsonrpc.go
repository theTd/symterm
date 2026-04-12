package transport

import (
	"bytes"
	"encoding/json"
	"errors"

	"symterm/internal/proto"
)

type Request struct {
	ID       uint64          `json:"id"`
	Method   string          `json:"method"`
	ClientID string          `json:"client_id,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ResponseError  `json:"error,omitempty"`
}

type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type StreamItem[T any] struct {
	Event *T   `json:"event,omitempty"`
	Done  bool `json:"done,omitempty"`
}

type ProjectEventStreamItem = StreamItem[proto.ProjectEvent]
type CommandEventStreamItem = StreamItem[proto.CommandEvent]
type InvalidateEventStreamItem = StreamItem[proto.InvalidateEvent]

const (
	internalCompleteInitialSyncMethod = "_internal_complete_initial_sync"
	internalWatchInvalidateMethod     = "_internal_watch_invalidate"
	internalInvalidateMethod          = "_internal_invalidate"
	internalOwnerWatcherFailedMethod  = "_internal_owner_watcher_failed"
	internalReportSyncProgressMethod  = "_internal_report_sync_progress"
)

func decodeParams(data json.RawMessage, target any) error {
	if len(data) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func isPostExitStdioRead(err error) bool {
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) {
		return false
	}
	return protoErr.Code == proto.ErrUnknownCommand
}

func errorResponse(id uint64, err error) Response {
	code, message := proto.ErrorFields(err, proto.ErrInvalidArgument)
	return Response{
		ID: id,
		Error: &ResponseError{
			Code:    string(code),
			Message: message,
		},
	}
}

func marshalRawMessage(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func resultResponse(id uint64, value any) Response {
	raw, err := marshalRawMessage(value)
	if err != nil {
		return errorResponse(id, err)
	}
	return Response{ID: id, Result: raw}
}
