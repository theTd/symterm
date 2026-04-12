package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

func TestHTTPServerRejectsLegacyAdminRoutes(t *testing.T) {
	t.Parallel()

	server, _ := newTestHTTPServer(t)
	defer server.Close()

	client := &http.Client{}
	for _, path := range []string{"/admin/legacy", "/admin/api/snapshot", "/admin/api/users"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s error = %v", path, err)
		}
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, res.StatusCode)
		}
	}
}

func TestHTTPServerWebSocketOpensWithoutLogin(t *testing.T) {
	t.Parallel()

	server, _ := newTestHTTPServer(t)
	defer server.Close()

	conn := dialAdminWS(t, server.URL, 0)
	defer conn.Close()

	var payload struct {
		Type string `json:"type"`
	}
	if err := websocket.JSON.Receive(conn, &payload); err != nil {
		t.Fatalf("websocket receive error = %v", err)
	}
	if payload.Type != "hello" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestHTTPServerWebSocketSignalsCursorExpiryThenSnapshot(t *testing.T) {
	t.Parallel()

	server, service := newTestHTTPServer(t)
	defer server.Close()

	for idx := 0; idx < 300; idx++ {
		service.SetListenAddr(fmt.Sprintf("127.0.0.1:%d", 7000+idx))
	}

	conn := dialAdminWS(t, server.URL, 1)
	defer conn.Close()

	var hello struct {
		Type string `json:"type"`
	}
	if err := websocket.JSON.Receive(conn, &hello); err != nil {
		t.Fatalf("websocket receive(hello) error = %v", err)
	}
	if hello.Type != "hello" {
		t.Fatalf("hello payload = %#v", hello)
	}

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

	conn := dialAdminWS(t, server.URL, 0)
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

	reconnected := dialAdminWS(t, server.URL, lastCursor)
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

func TestHTTPServerServesEmbeddedSPAAndBootstrapV1(t *testing.T) {
	t.Parallel()

	server, _ := newTestHTTPServer(t)
	defer server.Close()

	res, err := http.Get(server.URL + "/admin")
	if err != nil {
		t.Fatalf("GET /admin error = %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin status = %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll(/admin) error = %v", err)
	}
	if !bytes.Contains(body, []byte(`<div id="root"></div>`)) {
		t.Fatalf("/admin body = %q", string(body))
	}

	bootstrapRes, err := http.Get(server.URL + "/admin/api/v1/bootstrap")
	if err != nil {
		t.Fatalf("GET bootstrap v1 error = %v", err)
	}
	var bootstrap struct {
		OK   bool `json:"ok"`
		Data struct {
			Actor   string `json:"actor"`
			APIBase string `json:"api_base"`
		} `json:"data"`
	}
	if err := json.NewDecoder(bootstrapRes.Body).Decode(&bootstrap); err != nil {
		t.Fatalf("Decode(bootstrap) error = %v", err)
	}
	if !bootstrap.OK || bootstrap.Data.Actor == "" {
		t.Fatalf("bootstrap payload = %#v", bootstrap)
	}
	if bootstrap.Data.APIBase != "/admin/api/v1" {
		t.Fatalf("bootstrap api_base = %q", bootstrap.Data.APIBase)
	}
}

func TestHTTPServerV1UserLifecycleAndAuditFilters(t *testing.T) {
	t.Parallel()

	server, _ := newTestHTTPServer(t)
	defer server.Close()

	client := &http.Client{}

	createReq, _ := http.NewRequest(http.MethodPost, server.URL+"/admin/api/v1/users", strings.NewReader(`{"username":"dora","note":"ops"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRes, err := client.Do(createReq)
	if err != nil {
		t.Fatalf("POST v1 users error = %v", err)
	}
	if createRes.StatusCode != http.StatusOK {
		t.Fatalf("POST v1 users status = %d", createRes.StatusCode)
	}

	tokenReq, _ := http.NewRequest(http.MethodPost, server.URL+"/admin/api/v1/users/dora/tokens", strings.NewReader(`{"description":"cli"}`))
	tokenReq.Header.Set("Content-Type", "application/json")
	tokenRes, err := client.Do(tokenReq)
	if err != nil {
		t.Fatalf("POST v1 token error = %v", err)
	}
	if tokenRes.StatusCode != http.StatusOK {
		t.Fatalf("POST v1 token status = %d", tokenRes.StatusCode)
	}

	auditReq, _ := http.NewRequest(http.MethodGet, server.URL+"/admin/api/v1/audit?action=create_user&target=dora&page=1&page_size=10", nil)
	auditRes, err := client.Do(auditReq)
	if err != nil {
		t.Fatalf("GET v1 audit error = %v", err)
	}
	if auditRes.StatusCode != http.StatusOK {
		t.Fatalf("GET v1 audit status = %d", auditRes.StatusCode)
	}
	var audit struct {
		OK   bool          `json:"ok"`
		Data []AuditRecord `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(auditRes.Body).Decode(&audit); err != nil {
		t.Fatalf("Decode(v1 audit) error = %v", err)
	}
	if !audit.OK || audit.Meta.Total == 0 {
		t.Fatalf("audit payload = %#v", audit)
	}
	if audit.Data[0].Action != "create_user" || audit.Data[0].Target != "dora" {
		t.Fatalf("audit data = %#v", audit.Data)
	}
}

func TestHTTPServerV1EncodesEmptyCollectionsAsArrays(t *testing.T) {
	t.Parallel()

	server, service := newTestHTTPServer(t)
	defer server.Close()

	if _, err := service.store.CreateUser("empty", "", time.Unix(1_700_000_200, 0).UTC()); err != nil {
		t.Fatalf("CreateUser(empty) error = %v", err)
	}

	client := &http.Client{}

	assertBodyContains := func(path string, snippets ...string) {
		t.Helper()

		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s error = %v", path, err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, res.StatusCode)
		}
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("ReadAll(%s) error = %v", path, err)
		}
		text := string(body)
		for _, snippet := range snippets {
			if !strings.Contains(text, snippet) {
				t.Fatalf("GET %s body missing %q: %s", path, snippet, text)
			}
		}
	}

	assertBodyContains("/admin/api/v1/overview", `"recent_audit":[]`)
	assertBodyContains("/admin/api/v1/sessions", `"items":[]`)
	assertBodyContains("/admin/api/v1/users", `"token_ids":[]`)
	assertBodyContains(
		"/admin/api/v1/users/empty",
		`"tokens":[]`,
		`"related_audit":[]`,
	)
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

func dialAdminWS(t *testing.T, baseURL string, cursor uint64) *websocket.Conn {
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
