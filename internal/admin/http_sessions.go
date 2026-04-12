package admin

import (
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	adminSessionCookie = "symterm_admin_session"
	adminCSRFCookie    = "symterm_admin_csrf"
)

type webSession struct {
	ID        string
	CSRF      string
	Actor     string
	CreatedAt time.Time
}

type adminSessionStore struct {
	mu       sync.Mutex
	sessions map[string]webSession
}

func newAdminSessionStore() *adminSessionStore {
	return &adminSessionStore{sessions: make(map[string]webSession)}
}

func (s *adminSessionStore) create(actor string) (webSession, error) {
	sessionID, err := randomString()
	if err != nil {
		return webSession{}, err
	}
	csrf, err := randomString()
	if err != nil {
		return webSession{}, err
	}
	session := webSession{ID: sessionID, CSRF: csrf, Actor: actor, CreatedAt: time.Now().UTC()}
	s.mu.Lock()
	s.sessions[session.ID] = session
	s.mu.Unlock()
	return session, nil
}

func (s *adminSessionStore) delete(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, strings.TrimSpace(sessionID))
	s.mu.Unlock()
}

func (s *adminSessionStore) fromRequest(r *http.Request) (webSession, bool) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		return webSession{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[cookie.Value]
	return session, ok
}

func (s *HTTPServer) sessionFromRequest(r *http.Request) (webSession, bool) {
	return s.sessions.fromRequest(r)
}

func setAdminSessionCookies(w http.ResponseWriter, session webSession) {
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Value: session.ID, HttpOnly: true, Path: "/", SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: adminCSRFCookie, Value: session.CSRF, HttpOnly: false, Path: "/", SameSite: http.SameSiteStrictMode})
}

func clearAdminSessionCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: adminCSRFCookie, Value: "", Path: "/", MaxAge: -1})
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func adminActorForRequest(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		host = "loopback"
	}
	return "loopback-admin:" + host
}

func randomString() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
