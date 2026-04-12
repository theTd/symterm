package control

import (
	"sync"

	"symterm/internal/eventstream"
	"symterm/internal/proto"
)

type InvalidateHub struct {
	mu      sync.Mutex
	now     Clock
	streams map[string]*eventstream.Store[proto.InvalidateEvent]
}

func newInvalidateHub(now Clock) (*InvalidateHub, error) {
	if _, err := newInvalidateEventStream(); err != nil {
		return nil, err
	}
	return &InvalidateHub{
		now:     now,
		streams: make(map[string]*eventstream.Store[proto.InvalidateEvent]),
	}, nil
}

func (h *InvalidateHub) Watch(projectKey proto.ProjectKey, sinceCursor uint64) ([]proto.InvalidateEvent, error) {
	stream, err := h.stream(projectKey)
	if err != nil {
		return nil, err
	}
	return stream.EventsSince(sinceCursor)
}

func (h *InvalidateHub) Subscribe(projectKey proto.ProjectKey, sinceCursor uint64) ([]proto.InvalidateEvent, uint64, <-chan struct{}, func(), error) {
	stream, err := h.stream(projectKey)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	events, watcherID, ch, err := stream.Subscribe(sinceCursor)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	return events, watcherID, ch, func() {
		stream.Unsubscribe(watcherID)
	}, nil
}

func (h *InvalidateHub) Append(projectKey proto.ProjectKey, changes []proto.InvalidateChange) error {
	if len(changes) == 0 {
		return nil
	}
	stream, err := h.stream(projectKey)
	if err != nil {
		return err
	}
	stream.Append(proto.InvalidateEvent{
		Timestamp: h.now(),
		Changes:   append([]proto.InvalidateChange(nil), changes...),
	})
	return nil
}

func (h *InvalidateHub) CleanupProject(projectKey proto.ProjectKey) {
	h.mu.Lock()
	defer h.mu.Unlock()

	key := projectKey.String()
	if stream := h.streams[key]; stream != nil {
		stream.CloseAll()
		delete(h.streams, key)
	}
}

func (h *InvalidateHub) stream(projectKey proto.ProjectKey) (*eventstream.Store[proto.InvalidateEvent], error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	key := projectKey.String()
	stream := h.streams[key]
	if stream == nil {
		var err error
		stream, err = newInvalidateEventStream()
		if err != nil {
			return nil, err
		}
		h.streams[key] = stream
	}
	return stream, nil
}
