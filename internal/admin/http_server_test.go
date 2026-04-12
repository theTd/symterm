package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"symterm/internal/control"
)

func TestHTTPServerRequiresLoginAndSupportsSnapshot(t *testing.T) {
	t.Parallel()

	server, service := newTestHTTPServer(t)
	defer server.Close()

	res, err := http.Get(server.URL + "/admin/api/snapshot")
	if err != nil {
		t.Fatalf("GET snapshot error = %v", err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET snapshot status = %d, want 401", res.StatusCode)
	}

	sessionCookie, csrfCookie := adminLogin(t, server.URL)

	client := &http.Client{}
	createReq, _ := http.NewRequest(http.MethodPost, server.URL+"/admin/api/users", strings.NewReader(`{"username":"bob"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-CSRF-Token", csrfCookie.Value)
	createReq.AddCookie(sessionCookie)
	createReq.AddCookie(csrfCookie)
	createRes, err := client.Do(createReq)
	if err != nil {
		t.Fatalf("POST users error = %v", err)
	}
	if createRes.StatusCode != http.StatusOK {
		t.Fatalf("POST users status = %d", createRes.StatusCode)
	}

	snapshotReq, _ := http.NewRequest(http.MethodGet, server.URL+"/admin/api/snapshot", nil)
	snapshotReq.AddCookie(sessionCookie)
	snapshotReq.AddCookie(csrfCookie)
	snapshotRes, err := client.Do(snapshotReq)
	if err != nil {
		t.Fatalf("GET snapshot(authenticated) error = %v", err)
	}
	if snapshotRes.StatusCode != http.StatusOK {
		t.Fatalf("GET snapshot(authenticated) status = %d", snapshotRes.StatusCode)
	}
	var snapshot Snapshot
	if err := json.NewDecoder(snapshotRes.Body).Decode(&snapshot); err != nil {
		t.Fatalf("Decode(snapshot) error = %v", err)
	}
	if len(snapshot.Users) != 1 || snapshot.Users[0].Username != "bob" {
		t.Fatalf("snapshot users = %#v", snapshot.Users)
	}
	if snapshot.Daemon.Version != service.DaemonInfo().Version {
		t.Fatalf("snapshot daemon = %#v", snapshot.Daemon)
	}
}

func TestHTTPServerWebSocketRequiresLogin(t *testing.T) {
	t.Parallel()

	server, _ := newTestHTTPServer(t)
	defer server.Close()

	conn := dialAdminWS(t, server.URL, 0)
	defer conn.Close()

	var payload struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := websocket.JSON.Receive(conn, &payload); err != nil {
		t.Fatalf("websocket receive error = %v", err)
	}
	if payload.Type != "auth_error" || payload.Message != "not logged in" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestHTTPServerWebSocketSignalsCursorExpiryThenSnapshot(t *testing.T) {
	t.Parallel()

	server, service := newTestHTTPServer(t)
	defer server.Close()

	sessionCookie, csrfCookie := adminLogin(t, server.URL)
	for idx := 0; idx < 300; idx++ {
		service.SetListenAddr(fmt.Sprintf("127.0.0.1:%d", 7000+idx))
	}

	conn := dialAdminWS(t, server.URL, 1, sessionCookie, csrfCookie)
	defer conn.Close()

	var expired struct {
		Type string `json:"type"`
	}
	if err := websocket.JSON.Receive(conn, &expired); err != nil {
		t.Fatalf("websocket receive(cursor_expired) error = %v", err)
	}
	if expired.Type != "cursor_expired" {
		t.Fatalf("expired payload = %#v", expired)
	}
}

func TestHTTPServerWebSocketStreamsSnapshotUpdatesAndReconnects(t *testing.T) {
	t.Parallel()

	server, service := newTestHTTPServer(t)
	defer server.Close()

	sessionCookie, csrfCookie := adminLogin(t, server.URL)
	conn := dialAdminWS(t, server.URL, 0, sessionCookie, csrfCookie)
	initial := service.Snapshot()

	service.SetListenAddr("127.0.0.1:7100")
	updated := receiveEventWhere(t, conn, func(payload wsEventPayload) bool {
		return payload.Event.Kind == EventKindDaemonUpdated && payload.Event.Daemon != nil && payload.Event.Daemon.ListenAddr == "127.0.0.1:7100"
	})
	if updated.Event.Daemon == nil || updated.Event.Daemon.ListenAddr != "127.0.0.1:7100" {
		t.Fatalf("updated daemon event = %#v", updated)
	}

	lastCursor := updated.Event.Cursor
	_ = conn.Close()

	reconnected := dialAdminWS(t, server.URL, lastCursor, sessionCookie, csrfCookie)
	defer reconnected.Close()

	if _, err := service.CreateUser("loopback-admin:127.0.0.1", "carol", ""); err != nil {
		t.Fatalf("CreateUser(carol) error = %v", err)
	}
	next := receiveEventWhere(t, reconnected, func(payload wsEventPayload) bool {
		return payload.Event.Kind == EventKindUserUpsert && payload.Event.User != nil && payload.Event.User.Username == "carol"
	})
	if next.Event.Cursor <= initial.Cursor {
		t.Fatalf("user event cursor = %d, want > %d", next.Event.Cursor, initial.Cursor)
	}
}

func newTestHTTPServer(t *testing.T) (*httptest.Server, *Service) {
	t.Helper()

	controlService, err := control.NewService(control.StaticTokenAuthenticator{"token-a": "alice"})
	if err != nil {
		t.Fatalf("control.NewService() error = %v", err)
	}
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	eventHub, err := NewEventHub(0)
	if err != nil {
		t.Fatalf("NewEventHub() error = %v", err)
	}
	service, err := NewService(NewControlSessionCatalog(controlService), eventHub, store, DaemonInfo{
		Version:         "dev",
		StartedAt:       time.Unix(1_700_000_100, 0).UTC(),
		AdminSocketPath: "/tmp/admin.sock",
		AdminWebAddr:    "127.0.0.1:6040",
	}, time.Now)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return httptest.NewServer(NewHTTPServer(service).routes()), service
}

func adminLogin(t *testing.T, baseURL string) (*http.Cookie, *http.Cookie) {
	t.Helper()

	loginReq, _ := http.NewRequest(http.MethodPost, baseURL+"/admin/api/login", nil)
	loginRes, err := http.DefaultClient.Do(loginReq)
	if err != nil {
		t.Fatalf("POST login error = %v", err)
	}
	if loginRes.StatusCode != http.StatusOK {
		t.Fatalf("POST login status = %d", loginRes.StatusCode)
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range loginRes.Cookies() {
		switch cookie.Name {
		case adminSessionCookie:
			sessionCookie = cookie
		case adminCSRFCookie:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil {
		t.Fatalf("login cookies = %#v", loginRes.Cookies())
	}
	return sessionCookie, csrfCookie
}

func dialAdminWS(t *testing.T, baseURL string, cursor uint64, cookies ...*http.Cookie) *websocket.Conn {
	t.Helper()

	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", baseURL, err)
	}
	wsURL := "ws://" + parsed.Host + "/admin/ws"
	if cursor > 0 {
		wsURL += "?cursor=" + strconv.FormatUint(cursor, 10)
	}
	config, err := websocket.NewConfig(wsURL, baseURL+"/admin")
	if err != nil {
		t.Fatalf("websocket.NewConfig() error = %v", err)
	}
	config.Header = http.Header{}
	for _, cookie := range cookies {
		if cookie != nil {
			config.Header.Add("Cookie", cookie.String())
		}
	}
	conn, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("websocket.DialConfig() error = %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	return conn
}

type wsEventPayload struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Event   Event  `json:"event"`
}

func receiveEventWhere(t *testing.T, conn *websocket.Conn, match func(wsEventPayload) bool) wsEventPayload {
	t.Helper()

	deadline := time.Now().Add(4 * time.Second)
	for {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		var payload wsEventPayload
		if err := websocket.JSON.Receive(conn, &payload); err != nil {
			t.Fatalf("websocket receive(event) error = %v", err)
		}
		if match(payload) {
			return payload
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for matching event, last payload = %#v", payload)
		}
	}
}
