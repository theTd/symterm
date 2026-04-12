package control

import (
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestInvalidateHubAppendSubscribeAndCleanup(t *testing.T) {
	t.Parallel()

	now := fixedClock()
	projectKey := proto.ProjectKey{Username: "alice", ProjectID: "demo"}
	hub, err := newInvalidateHub(now)
	if err != nil {
		t.Fatalf("newInvalidateHub() error = %v", err)
	}

	initial, _, ch, err := func() ([]proto.InvalidateEvent, uint64, <-chan struct{}, error) {
		events, watcherID, watcherCh, _, subscribeErr := hub.Subscribe(projectKey, 0)
		return events, watcherID, watcherCh, subscribeErr
	}()
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	if len(initial) != 0 {
		t.Fatalf("Subscribe() initial events = %#v, want empty", initial)
	}

	changes := []proto.InvalidateChange{{Path: "docs/a.txt", Kind: proto.InvalidateData}}
	if err := hub.Append(projectKey, changes); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	changes[0].Path = "mutated"

	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Subscribe() watcher was not notified")
	}

	events, err := hub.Watch(projectKey, 0)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Watch() len = %d, want 1", len(events))
	}
	if events[0].Timestamp != now() {
		t.Fatalf("Watch() timestamp = %v, want %v", events[0].Timestamp, now())
	}
	if len(events[0].Changes) != 1 || events[0].Changes[0].Path != "docs/a.txt" {
		t.Fatalf("Watch() changes = %#v", events[0].Changes)
	}

	hub.CleanupProject(projectKey)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("Subscribe() channel remained open after CleanupProject()")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Subscribe() channel was not closed by CleanupProject()")
	}

	events, err = hub.Watch(projectKey, 0)
	if err != nil {
		t.Fatalf("Watch() after cleanup error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Watch() after cleanup = %#v, want empty recreated stream", events)
	}
}
