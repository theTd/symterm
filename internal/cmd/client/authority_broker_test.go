package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"symterm/internal/proto"
)

func TestAuthorityBrokerHandlerTracksLeasesAndStatus(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	broker := &authorityBroker{
		token:  "token-123",
		leases: make(map[string]struct{}),
	}
	server := httptest.NewServer(broker.handler(cancel))
	defer server.Close()

	status := func() authorityBrokerStatus {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, server.URL+"/status", nil)
		if err != nil {
			t.Fatalf("NewRequest(status) error = %v", err)
		}
		request.Header.Set("X-Symterm-Broker-Token", broker.token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("Do(status) error = %v", err)
		}
		defer response.Body.Close()
		var current authorityBrokerStatus
		if err := json.NewDecoder(response.Body).Decode(&current); err != nil {
			t.Fatalf("Decode(status) error = %v", err)
		}
		return current
	}

	if current := status(); current.Ready || current.LeaseCount != 0 {
		t.Fatalf("initial status = %#v, want ready=false lease_count=0", current)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/acquire", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("NewRequest(acquire) error = %v", err)
	}
	request.Header.Set("X-Symterm-Broker-Token", broker.token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do(acquire) error = %v", err)
	}
	defer response.Body.Close()

	var acquired authorityBrokerAcquireResponse
	if err := json.NewDecoder(response.Body).Decode(&acquired); err != nil {
		t.Fatalf("Decode(acquire) error = %v", err)
	}
	if acquired.LeaseID == "" {
		t.Fatal("acquire returned an empty lease id")
	}
	if current := status(); current.LeaseCount != 1 {
		t.Fatalf("status after acquire = %#v, want lease_count=1", current)
	}

	broker.setStatus(true, nil)
	if current := status(); !current.Ready {
		t.Fatalf("status after ready = %#v, want ready=true", current)
	}

	body, err := json.Marshal(authorityBrokerReleaseRequest{LeaseID: acquired.LeaseID})
	if err != nil {
		t.Fatalf("Marshal(release) error = %v", err)
	}
	request, err = http.NewRequest(http.MethodPost, server.URL+"/release", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest(release) error = %v", err)
	}
	request.Header.Set("X-Symterm-Broker-Token", broker.token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do(release) error = %v", err)
	}
	defer response.Body.Close()
	if current := status(); current.LeaseCount != 0 {
		t.Fatalf("status after release = %#v, want lease_count=0", current)
	}

	select {
	case <-ctx.Done():
	default:
		t.Fatal("release did not trigger broker cancellation on the final lease")
	}
}

func TestAuthorityBrokerWaitReadyReturnsBrokerStatusError(t *testing.T) {
	t.Parallel()

	broker := &authorityBroker{
		token:  "token-123",
		leases: make(map[string]struct{}),
	}
	broker.setStatus(false, errors.New("authority ssh handshake failed"))

	server := httptest.NewServer(broker.handler(func() {}))
	defer server.Close()

	manifest := authorityBrokerManifest{
		Address: server.Listener.Addr().String(),
		Token:   broker.token,
	}
	err := authorityBrokerWaitReady(context.Background(), manifest)
	if err == nil {
		t.Fatal("authorityBrokerWaitReady() error = nil, want broker status error")
	}
	if got := err.Error(); got != "authority ssh handshake failed" {
		t.Fatalf("authorityBrokerWaitReady() error = %q, want authority ssh handshake failed", got)
	}
}

func TestAuthorityBrokerWaitReadyWithFeedbackRendersRemoteSyncProgress(t *testing.T) {
	t.Parallel()

	broker := &authorityBroker{
		token:  "token-123",
		leases: make(map[string]struct{}),
	}
	broker.recordSyncOperation("Scanning workspace manifest")
	broker.recordSyncProgress(proto.SyncProgress{
		Phase:     proto.SyncProgressPhaseScanManifest,
		Completed: 1,
		Total:     2,
	})

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Symterm-Broker-Token") != broker.token {
			http.Error(writer, "forbidden", http.StatusForbidden)
			return
		}
		status := broker.status()
		broker.setStatus(true, nil)
		_ = json.NewEncoder(writer).Encode(status)
	}))
	defer server.Close()

	manifest := authorityBrokerManifest{
		Address: server.Listener.Addr().String(),
		Token:   broker.token,
	}

	var output bytes.Buffer
	feedback := &syncProgressTUI{writer: &output}
	if err := authorityBrokerWaitReadyWithFeedback(context.Background(), manifest, feedback); err != nil {
		t.Fatalf("authorityBrokerWaitReadyWithFeedback() error = %v", err)
	}

	rendered := output.String()
	for _, needle := range []string{
		"Syncing workspace",
		"Phase: Scan manifest 1/2",
		"Current: Scanning workspace manifest",
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered output missing %q:\n%s", needle, rendered)
		}
	}
}

func TestAuthorityBrokerRunLoopKeepsInitialFailureVisibleUntilCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wantErr := errors.New("authority session bootstrap failed")
	broker := &authorityBroker{
		workspaceID: "wsi-demo",
		leases:      make(map[string]struct{}),
		connectFn: func(context.Context, func(string, ...any)) (*authorityBrokerSession, error) {
			return nil, wantErr
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- broker.runAuthorityLoop(ctx, nil)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		status := broker.status()
		if status.Error == wantErr.Error() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("broker status error = %q, want %q", status.Error, wantErr.Error())
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case err := <-done:
		t.Fatalf("runAuthorityLoop() returned early with %v, want it to stay alive until cancellation", err)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAuthorityLoop() after cancellation error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runAuthorityLoop() did not stop after cancellation")
	}
}

func TestWaitForAuthorityBrokerManifestReturnsAfterPublish(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "broker.json")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- waitForAuthorityBrokerManifest(ctx, path)
	}()

	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(path, []byte("{\"address\":\"127.0.0.1:9000\",\"token\":\"token-123\"}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForAuthorityBrokerManifest() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForAuthorityBrokerManifest() did not return after manifest publish")
	}
}
