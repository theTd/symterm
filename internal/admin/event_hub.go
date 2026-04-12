package admin

import (
	"sort"
	"sync"

	"symterm/internal/eventstream"
)

const defaultEventRetention = 256

const (
	EventKindDaemonUpdated = "daemon_updated"
	EventKindSessionUpsert = "session_upsert"
	EventKindSessionClosed = "session_closed"
	EventKindUserUpsert    = "user_upsert"
	EventKindTokenIssued   = "token_issued"
	EventKindTokenRevoked  = "token_revoked"
	EventKindAuditAppended = "audit_appended"
)

type Event struct {
	Cursor    uint64           `json:"cursor"`
	Kind      string           `json:"kind"`
	Daemon    *DaemonInfo      `json:"daemon,omitempty"`
	Session   *SessionSnapshot `json:"session,omitempty"`
	SessionID string           `json:"session_id,omitempty"`
	User      *UserRecord      `json:"user,omitempty"`
	Token     *UserTokenRecord `json:"token,omitempty"`
	Audit     *AuditRecord     `json:"audit,omitempty"`
}

type EventHub struct {
	mu     sync.Mutex
	live   map[string]SessionSnapshot
	closed map[string]SessionSnapshot
	events *eventstream.Store[Event]
}

func NewEventHub(retention int) (*EventHub, error) {
	if retention <= 0 {
		retention = defaultEventRetention
	}
	events, err := eventstream.New(retention, eventstream.CursorCodec[Event]{
		Name: "admin",
		GetCursor: func(event Event) uint64 {
			return event.Cursor
		},
		SetCursor: func(event *Event, cursor uint64) {
			event.Cursor = cursor
		},
		Clone: cloneEvent,
	})
	if err != nil {
		return nil, err
	}
	return &EventHub{
		live:   make(map[string]SessionSnapshot),
		closed: make(map[string]SessionSnapshot),
		events: events,
	}, nil
}

func (h *EventHub) CurrentCursor() uint64 {
	if h == nil {
		return 0
	}
	return h.events.CurrentCursor()
}

func (h *EventHub) EventsSince(since uint64) ([]Event, error) {
	if h == nil {
		return nil, nil
	}
	return h.events.EventsSince(since)
}

func (h *EventHub) Subscribe(since uint64) ([]Event, uint64, <-chan struct{}, error) {
	if h == nil {
		ch := make(chan struct{})
		close(ch)
		return nil, 0, ch, nil
	}
	return h.events.Subscribe(since)
}

func (h *EventHub) Unsubscribe(subscriberID uint64) {
	if h == nil {
		return
	}
	h.events.Unsubscribe(subscriberID)
}

func (h *EventHub) ListSessions(includeClosed bool) []SessionSnapshot {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	sessions := make([]SessionSnapshot, 0, len(h.live))
	for _, snapshot := range h.live {
		sessions = append(sessions, cloneSessionSnapshot(snapshot))
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].ConnectedAt.Equal(sessions[j].ConnectedAt) {
			return sessions[i].SessionID < sessions[j].SessionID
		}
		return sessions[i].ConnectedAt.Before(sessions[j].ConnectedAt)
	})
	if includeClosed {
		for _, snapshot := range h.closed {
			sessions = append(sessions, cloneSessionSnapshot(snapshot))
		}
		sort.Slice(sessions, func(i, j int) bool {
			if sessions[i].ConnectedAt.Equal(sessions[j].ConnectedAt) {
				return sessions[i].SessionID < sessions[j].SessionID
			}
			return sessions[i].ConnectedAt.Before(sessions[j].ConnectedAt)
		})
	}
	return sessions
}

func (h *EventHub) GetSession(sessionID string) (SessionSnapshot, bool) {
	if h == nil {
		return SessionSnapshot{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	if snapshot, ok := h.live[sessionID]; ok {
		return cloneSessionSnapshot(snapshot), true
	}
	snapshot, ok := h.closed[sessionID]
	return cloneSessionSnapshot(snapshot), ok
}

func (h *EventHub) RecordDaemon(info DaemonInfo) {
	if h == nil {
		return
	}
	copy := cloneDaemonInfo(info)
	h.events.Append(Event{
		Kind:   EventKindDaemonUpdated,
		Daemon: &copy,
	})
}

func (h *EventHub) RecordUser(user UserRecord) {
	if h == nil {
		return
	}
	copy := cloneUserRecord(user)
	h.events.Append(Event{
		Kind: EventKindUserUpsert,
		User: &copy,
	})
}

func (h *EventHub) RecordToken(kind string, record UserTokenRecord) {
	if h == nil {
		return
	}
	copy := cloneTokenRecord(record)
	h.events.Append(Event{
		Kind:  kind,
		Token: &copy,
	})
}

func (h *EventHub) RecordAudit(record AuditRecord) {
	if h == nil {
		return
	}
	copy := record
	h.events.Append(Event{
		Kind:  EventKindAuditAppended,
		Audit: &copy,
	})
}

func (h *EventHub) UpsertSession(snapshot SessionSnapshot) {
	if h == nil {
		return
	}

	copy := cloneSessionSnapshot(snapshot)
	h.mu.Lock()
	h.live[copy.SessionID] = copy
	delete(h.closed, copy.SessionID)
	h.mu.Unlock()

	h.events.Append(Event{
		Kind:    EventKindSessionUpsert,
		Session: &copy,
	})
}

func (h *EventHub) CloseSession(snapshot SessionSnapshot) {
	if h == nil {
		return
	}

	copy := cloneSessionSnapshot(snapshot)
	h.mu.Lock()
	delete(h.live, copy.SessionID)
	h.closed[copy.SessionID] = copy
	h.mu.Unlock()

	h.events.Append(Event{
		Kind:      EventKindSessionClosed,
		SessionID: copy.SessionID,
		Session:   &copy,
	})
}

func cloneEvent(event Event) Event {
	if event.Daemon != nil {
		copy := cloneDaemonInfo(*event.Daemon)
		event.Daemon = &copy
	}
	if event.Session != nil {
		copy := cloneSessionSnapshot(*event.Session)
		event.Session = &copy
	}
	if event.User != nil {
		copy := cloneUserRecord(*event.User)
		event.User = &copy
	}
	if event.Token != nil {
		copy := cloneTokenRecord(*event.Token)
		event.Token = &copy
	}
	if event.Audit != nil {
		copy := *event.Audit
		event.Audit = &copy
	}
	return event
}

func cloneDaemonInfo(info DaemonInfo) DaemonInfo {
	return info
}
