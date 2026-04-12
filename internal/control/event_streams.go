package control

import (
	"symterm/internal/eventstream"
	"symterm/internal/proto"
)

const (
	defaultCommandEventRetention    = 256
	defaultInvalidateEventRetention = 256
)

func newCommandEventStream() (*eventstream.Store[proto.CommandEvent], error) {
	return newCommandEventStreamWithRetention(defaultCommandEventRetention)
}

func newCommandEventStreamWithRetention(retention int) (*eventstream.Store[proto.CommandEvent], error) {
	return eventstream.New(retention, eventstream.CursorCodec[proto.CommandEvent]{
		Name: "command",
		GetCursor: func(event proto.CommandEvent) uint64 {
			return event.Cursor
		},
		SetCursor: func(event *proto.CommandEvent, cursor uint64) {
			event.Cursor = cursor
		},
	})
}

func newInvalidateEventStream() (*eventstream.Store[proto.InvalidateEvent], error) {
	return newInvalidateEventStreamWithRetention(defaultInvalidateEventRetention)
}

func newInvalidateEventStreamWithRetention(retention int) (*eventstream.Store[proto.InvalidateEvent], error) {
	return eventstream.New(retention, eventstream.CursorCodec[proto.InvalidateEvent]{
		Name: "invalidate",
		GetCursor: func(event proto.InvalidateEvent) uint64 {
			return event.Cursor
		},
		SetCursor: func(event *proto.InvalidateEvent, cursor uint64) {
			event.Cursor = cursor
		},
		Clone: cloneInvalidateEvent,
	})
}

func cloneInvalidateEvent(event proto.InvalidateEvent) proto.InvalidateEvent {
	event.Changes = append([]proto.InvalidateChange(nil), event.Changes...)
	return event
}
