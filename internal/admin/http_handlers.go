package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/net/websocket"
)

func (s *HTTPServer) handleAdminShell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(adminShellHTML))
}

func (s *HTTPServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRequest(r) {
		http.Error(w, "admin login requires loopback", http.StatusForbidden)
		return
	}
	session, err := s.sessions.create(adminActorForRequest(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	setAdminSessionCookies(w, session)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *HTTPServer) handleLogout(w http.ResponseWriter, _ *http.Request, session webSession) {
	s.sessions.delete(session.ID)
	clearAdminSessionCookies(w)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *HTTPServer) handleSnapshot(w http.ResponseWriter, _ *http.Request, _ webSession) {
	writeJSON(w, s.service.Snapshot())
}

func (s *HTTPServer) handleDaemon(w http.ResponseWriter, _ *http.Request, _ webSession) {
	writeJSON(w, s.service.DaemonInfo())
}

func (s *HTTPServer) handleSessions(w http.ResponseWriter, r *http.Request, _ webSession) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.service.ListSessions())
}

func (s *HTTPServer) handleSession(w http.ResponseWriter, r *http.Request, session webSession) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/api/sessions/")
	if strings.HasSuffix(path, "/terminate") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessionID := strings.TrimSuffix(path, "/terminate")
		if err := s.service.TerminateSession(session.Actor, sessionID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	item, ok := s.service.GetSession(path)
	if !ok {
		http.Error(w, "session does not exist", http.StatusNotFound)
		return
	}
	writeJSON(w, item)
}

func (s *HTTPServer) handleUsers(w http.ResponseWriter, r *http.Request, session webSession) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.service.ListUsers())
	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Note     string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		record, err := s.service.CreateUser(session.Actor, body.Username, body.Note)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, record)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *HTTPServer) handleUser(w http.ResponseWriter, r *http.Request, session webSession) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/api/users/")
	switch {
	case strings.HasSuffix(path, "/disable"):
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		username := strings.TrimSuffix(path, "/disable")
		record, err := s.service.DisableUser(session.Actor, username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, record)
	case strings.HasSuffix(path, "/token"):
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		username := strings.TrimSuffix(path, "/token")
		token, err := s.service.IssueUserToken(session.Actor, username, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, token)
	case strings.Contains(path, "/tokens/") && strings.HasSuffix(path, "/revoke"):
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		parts := strings.Split(path, "/")
		if len(parts) < 3 {
			http.Error(w, "invalid token path", http.StatusBadRequest)
			return
		}
		tokenID := parts[len(parts)-2]
		record, err := s.service.RevokeUserToken(session.Actor, tokenID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, record)
	case strings.HasSuffix(path, "/entrypoint"):
		username := strings.TrimSuffix(path, "/entrypoint")
		if r.Method == http.MethodGet {
			entrypoint, err := s.service.GetUserEntrypoint(username)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"username": username, "entrypoint": entrypoint})
			return
		}
		if r.Method == http.MethodPost {
			var body struct {
				Entrypoint []string `json:"entrypoint"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			record, err := s.service.SetUserEntrypoint(session.Actor, username, body.Entrypoint)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, record)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		detail, ok := s.service.GetUserDetail(path)
		if !ok {
			http.Error(w, "user does not exist", http.StatusNotFound)
			return
		}
		writeJSON(w, detail)
	}
}

func (s *HTTPServer) handleEventsWebsocket(conn *websocket.Conn) {
	defer conn.Close()
	request := conn.Request()
	session, ok := s.sessionFromRequest(request)
	if !ok {
		_ = websocket.JSON.Send(conn, map[string]any{"type": "auth_error", "message": "not logged in"})
		return
	}
	since := uint64(0)
	if raw := request.URL.Query().Get("cursor"); raw != "" {
		_, _ = fmt.Sscanf(raw, "%d", &since)
	}
	events, subscriberID, ch, err := s.service.SubscribeEvents(since)
	if err != nil {
		_ = websocket.JSON.Send(conn, map[string]any{"type": "cursor_expired"})
		return
	}
	defer s.service.UnsubscribeEvents(subscriberID)
	lastCursor := since
	for {
		for _, event := range events {
			payload := map[string]any{"type": "event", "session": session.Actor, "event": event}
			if err := websocket.JSON.Send(conn, payload); err != nil {
				return
			}
			lastCursor = event.Cursor
		}
		if err := waitForAdminEvent(request.Context(), ch); err != nil {
			return
		}
		events, err = s.service.EventsSince(lastCursor)
		if err != nil {
			_ = websocket.JSON.Send(conn, map[string]any{"type": "cursor_expired"})
			return
		}
	}
}

func (s *HTTPServer) withAuth(next func(http.ResponseWriter, *http.Request, webSession)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.sessionFromRequest(r)
		if !ok {
			http.Error(w, "not logged in", http.StatusUnauthorized)
			return
		}
		next(w, r, session)
	}
}

func (s *HTTPServer) requireCSRF(next func(http.ResponseWriter, *http.Request, webSession)) func(http.ResponseWriter, *http.Request, webSession) {
	return func(w http.ResponseWriter, r *http.Request, session webSession) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next(w, r, session)
			return
		}
		if r.Header.Get("X-CSRF-Token") != session.CSRF {
			http.Error(w, "csrf validation failed", http.StatusForbidden)
			return
		}
		next(w, r, session)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func waitForAdminEvent(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case _, ok := <-ch:
		if !ok {
			return context.Canceled
		}
		return nil
	}
}
