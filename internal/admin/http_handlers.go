package admin

import (
	"fmt"
	"time"

	"golang.org/x/net/websocket"
)

func (s *HTTPServer) handleEventsWebsocket(conn *websocket.Conn) {
	defer conn.Close()
	request := conn.Request()
	if !isLoopbackRequest(request) {
		_ = websocket.JSON.Send(conn, map[string]any{"type": "auth_error", "message": "admin access requires loopback"})
		return
	}
	actor := adminActorForRequest(request)
	since := uint64(0)
	if raw := request.URL.Query().Get("cursor"); raw != "" {
		_, _ = fmt.Sscanf(raw, "%d", &since)
	}
	_ = websocket.JSON.Send(conn, map[string]any{
		"type":    "hello",
		"session": actor,
		"cursor":  s.service.currentCursor(),
	})
	events, subscriberID, ch, err := s.service.SubscribeEvents(since)
	if err != nil {
		_ = websocket.JSON.Send(conn, map[string]any{"type": "cursor_expired"})
		return
	}
	defer s.service.UnsubscribeEvents(subscriberID)
	lastCursor := since
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		for _, event := range events {
			payload := map[string]any{"type": "event", "session": actor, "event": event}
			if err := websocket.JSON.Send(conn, payload); err != nil {
				return
			}
			lastCursor = event.Cursor
		}
		select {
		case <-request.Context().Done():
			return
		case <-heartbeat.C:
			if err := websocket.JSON.Send(conn, map[string]any{
				"type":    "heartbeat",
				"session": actor,
				"cursor":  lastCursor,
			}); err != nil {
				return
			}
		case _, ok := <-ch:
			if !ok {
				return
			}
			events, err = s.service.EventsSince(lastCursor)
			if err != nil {
				_ = websocket.JSON.Send(conn, map[string]any{"type": "cursor_expired"})
				return
			}
		}
	}
}
