package eventstream

import (
	"testing"

	"symterm/internal/proto"
)

type testEvent struct {
	Cursor  uint64
	Message string
}

func newTestStore(t *testing.T, retention int) *Store[testEvent] {
	t.Helper()

	store, err := New(retention, CursorCodec[testEvent]{
		Name: "test",
		GetCursor: func(event testEvent) uint64 {
			return event.Cursor
		},
		SetCursor: func(event *testEvent, cursor uint64) {
			event.Cursor = cursor
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return store
}

func TestStoreRejectsMissingCodecFields(t *testing.T) {
	t.Parallel()

	if _, err := New[testEvent](1, CursorCodec[testEvent]{
		SetCursor: func(event *testEvent, cursor uint64) {
			event.Cursor = cursor
		},
	}); err == nil {
		t.Fatal("New() succeeded without GetCursor")
	}
	if _, err := New[testEvent](1, CursorCodec[testEvent]{
		GetCursor: func(event testEvent) uint64 {
			return event.Cursor
		},
	}); err == nil {
		t.Fatal("New() succeeded without SetCursor")
	}
}

func TestStoreAssignsCursorAndTrimsRetention(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 2)
	store.Append(testEvent{Message: "one"})
	store.Append(testEvent{Message: "two"})
	last := store.Append(testEvent{Message: "three"})

	if last.Cursor != 3 {
		t.Fatalf("last cursor = %d, want 3", last.Cursor)
	}

	events, err := store.EventsSince(1)
	if err != nil {
		t.Fatalf("EventsSince() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Message != "two" || events[1].Message != "three" {
		t.Fatalf("events = %#v", events)
	}
}

func TestStoreRejectsExpiredCursor(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1)
	store.Append(testEvent{Message: "one"})
	store.Append(testEvent{Message: "two"})

	if _, err := store.EventsSince(0); err == nil {
		t.Fatal("EventsSince() succeeded with expired cursor")
	} else {
		protoErr, ok := err.(*proto.Error)
		if !ok {
			t.Fatalf("EventsSince() error = %T, want *proto.Error", err)
		}
		if protoErr.Code != proto.ErrCursorExpired {
			t.Fatalf("EventsSince() error code = %q, want %q", protoErr.Code, proto.ErrCursorExpired)
		}
	}
}

func TestStoreSubscribeNotifiesAndUnsubscribes(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 4)
	_, subscriberID, ch, err := store.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	store.Append(testEvent{Message: "one"})
	select {
	case <-ch:
	default:
		t.Fatal("subscriber was not notified")
	}

	store.Unsubscribe(subscriberID)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("subscriber channel remained open after Unsubscribe()")
		}
	default:
		t.Fatal("subscriber channel did not close after Unsubscribe()")
	}
}
