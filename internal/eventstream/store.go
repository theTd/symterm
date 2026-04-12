package eventstream

import (
	"fmt"
	"sync"

	"symterm/internal/proto"
)

type CursorCodec[T any] struct {
	Name      string
	GetCursor func(T) uint64
	SetCursor func(*T, uint64)
	Clone     func(T) T
}

type Store[T any] struct {
	mu               sync.Mutex
	retention        int
	cursor           uint64
	nextSubscriberID uint64
	events           []T
	subscribers      map[uint64]chan struct{}
	codec            CursorCodec[T]
}

func New[T any](retention int, codec CursorCodec[T]) (*Store[T], error) {
	if retention <= 0 {
		retention = 1
	}
	var err error
	codec, err = prepareCursorCodec(codec)
	if err != nil {
		return nil, err
	}
	return newStore(retention, codec), nil
}

func prepareCursorCodec[T any](codec CursorCodec[T]) (CursorCodec[T], error) {
	if codec.GetCursor == nil {
		return CursorCodec[T]{}, fmt.Errorf("eventstream: GetCursor is required")
	}
	if codec.SetCursor == nil {
		return CursorCodec[T]{}, fmt.Errorf("eventstream: SetCursor is required")
	}
	if codec.Clone == nil {
		codec.Clone = func(event T) T { return event }
	}
	if codec.Name == "" {
		codec.Name = "event"
	}
	return codec, nil
}

func newStore[T any](retention int, codec CursorCodec[T]) *Store[T] {
	return &Store[T]{
		retention:   retention,
		subscribers: make(map[uint64]chan struct{}),
		codec:       codec,
	}
}

func (s *Store[T]) CurrentCursor() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

func (s *Store[T]) EventsSince(since uint64) ([]T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventsSinceLocked(since)
}

func (s *Store[T]) Subscribe(since uint64) ([]T, uint64, <-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	events, err := s.eventsSinceLocked(since)
	if err != nil {
		return nil, 0, nil, err
	}
	s.nextSubscriberID++
	subscriberID := s.nextSubscriberID
	ch := make(chan struct{}, 1)
	s.subscribers[subscriberID] = ch
	return events, subscriberID, ch, nil
}

func (s *Store[T]) Append(event T) T {
	events := s.AppendBatch(event)
	return events[0]
}

func (s *Store[T]) AppendBatch(events ...T) []T {
	if len(events) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	appended := make([]T, 0, len(events))
	for _, event := range events {
		copy := s.codec.Clone(event)
		s.cursor++
		s.codec.SetCursor(&copy, s.cursor)
		s.events = append(s.events, copy)
		appended = append(appended, s.codec.Clone(copy))
	}
	if len(s.events) > s.retention {
		s.events = append([]T(nil), s.events[len(s.events)-s.retention:]...)
	}
	for _, subscriber := range s.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
	return appended
}

func (s *Store[T]) Unsubscribe(subscriberID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch, ok := s.subscribers[subscriberID]
	if !ok {
		return
	}
	delete(s.subscribers, subscriberID)
	close(ch)
}

func (s *Store[T]) CloseAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for subscriberID, ch := range s.subscribers {
		delete(s.subscribers, subscriberID)
		close(ch)
	}
}

func (s *Store[T]) eventsSinceLocked(since uint64) ([]T, error) {
	if since > s.cursor {
		return nil, proto.NewError(proto.ErrCursorExpired, fmt.Sprintf("%s cursor is ahead of the current head", s.codec.Name))
	}
	if len(s.events) == 0 {
		return nil, nil
	}

	oldest := s.codec.GetCursor(s.events[0])
	if since+1 < oldest {
		return nil, proto.NewError(proto.ErrCursorExpired, fmt.Sprintf("%s cursor is no longer retained", s.codec.Name))
	}

	result := make([]T, 0, len(s.events))
	for _, event := range s.events {
		if s.codec.GetCursor(event) > since {
			result = append(result, s.codec.Clone(event))
		}
	}
	return result, nil
}
