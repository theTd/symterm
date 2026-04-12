package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"symterm/internal/control"
)

func TestStoreManagedTokenLifecycle(t *testing.T) {
	t.Parallel()

	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	now := time.Unix(1_700_000_100, 0).UTC()

	user, err := store.CreateUser("alice", "demo", now)
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if user.Username != "alice" {
		t.Fatalf("CreateUser() = %#v", user)
	}

	issued, err := store.IssueToken("alice", "cli", now)
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	if issued.PlainSecret == "" {
		t.Fatal("IssueToken() returned empty plaintext")
	}

	principal, err := store.Authenticate(context.Background(), issued.PlainSecret)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.Username != "alice" || principal.TokenSource != control.TokenSourceManaged {
		t.Fatalf("Authenticate() principal = %#v", principal)
	}

	tokenPath := filepath.Join(store.tokensDir, safeName(issued.Record.TokenID)+".json")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("ReadFile(token) error = %v", err)
	}
	if strings.Contains(string(data), issued.PlainSecret) {
		t.Fatal("token file leaked plaintext secret")
	}

	if _, err := store.SetUserEntrypoint("alice", []string{"bash", "-lc"}, now); err != nil {
		t.Fatalf("SetUserEntrypoint() error = %v", err)
	}
	if got := store.EffectiveEntrypoint("alice", []string{"sh"}); len(got) != 2 || got[0] != "bash" {
		t.Fatalf("EffectiveEntrypoint() = %#v", got)
	}

	if _, err := store.RevokeToken(issued.Record.TokenID, now); err != nil {
		t.Fatalf("RevokeToken() error = %v", err)
	}
	if _, err := store.Authenticate(context.Background(), issued.PlainSecret); err == nil {
		t.Fatal("Authenticate() succeeded after revoke")
	}

	if _, err := store.DisableUser("alice", now); err != nil {
		t.Fatalf("DisableUser() error = %v", err)
	}
	issued, err = store.IssueToken("alice", "replacement", now)
	if err != nil {
		t.Fatalf("IssueToken(replacement) error = %v", err)
	}
	if _, err := store.Authenticate(context.Background(), issued.PlainSecret); err == nil {
		t.Fatal("Authenticate() succeeded for disabled user")
	}
}

func TestStoreBootstrapTokenLifecycleIsImportedAndReadOnly(t *testing.T) {
	t.Parallel()

	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	now := time.Unix(1_700_000_150, 0).UTC()

	if err := store.ImportBootstrapTokens(map[string]string{"bootstrap-secret": "alice"}, now); err != nil {
		t.Fatalf("ImportBootstrapTokens() error = %v", err)
	}
	if _, err := store.CreateUser("bob", "", now); err != nil {
		t.Fatalf("CreateUser(bob) error = %v", err)
	}
	issued, err := store.IssueToken("bob", "cli", now)
	if err != nil {
		t.Fatalf("IssueToken(bob) error = %v", err)
	}

	principal, err := store.Authenticate(context.Background(), "bootstrap-secret")
	if err != nil {
		t.Fatalf("Authenticate(bootstrap) error = %v", err)
	}
	if principal.TokenSource != control.TokenSourceBootstrap {
		t.Fatalf("Authenticate(bootstrap) source = %q, want %q", principal.TokenSource, control.TokenSourceBootstrap)
	}

	if got := len(store.ListBootstrapTokens("alice")); got != 1 {
		t.Fatalf("ListBootstrapTokens(alice) len = %d, want 1", got)
	}
	if got := len(store.ListManagedTokens("bob")); got != 1 {
		t.Fatalf("ListManagedTokens(bob) len = %d, want 1", got)
	}
	if got := len(store.ListTokens("")); got != 2 {
		t.Fatalf("ListTokens(all) len = %d, want 2", got)
	}

	if _, err := store.RevokeToken("bootstrap-"+shortHash("bootstrap-secret"), now); err == nil {
		t.Fatal("RevokeToken(bootstrap) succeeded")
	}
	if _, err := store.RevokeToken(issued.Record.TokenID, now); err != nil {
		t.Fatalf("RevokeToken(managed) error = %v", err)
	}
}

func TestStoreConcurrentMutationsPersistConsistentState(t *testing.T) {
	t.Parallel()

	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	now := time.Unix(1_700_000_200, 0).UTC()
	if _, err := store.CreateUser("shared", "", now); err != nil {
		t.Fatalf("CreateUser(shared) error = %v", err)
	}

	const workers = 8
	var wg sync.WaitGroup
	var tokenCount atomic.Int32
	for idx := 0; idx < workers; idx++ {
		idx := idx
		wg.Add(1)
		go func() {
			defer wg.Done()
			username := "user-" + string(rune('a'+idx))
			if _, createErr := store.CreateUser(username, "note", now); createErr != nil {
				t.Errorf("CreateUser(%s) error = %v", username, createErr)
				return
			}
			if _, issueErr := store.IssueToken("shared", username, now); issueErr != nil {
				t.Errorf("IssueToken(shared,%s) error = %v", username, issueErr)
				return
			}
			tokenCount.Add(1)
		}()
	}
	wg.Wait()

	reopened, err := OpenStore(store.root)
	if err != nil {
		t.Fatalf("OpenStore(reopen) error = %v", err)
	}
	users := reopened.ListUsers()
	if len(users) != workers+1 {
		t.Fatalf("ListUsers() len = %d, want %d", len(users), workers+1)
	}
	shared, ok := reopened.GetUser("shared")
	if !ok {
		t.Fatal("GetUser(shared) = not found")
	}
	if got := len(shared.TokenIDs); got != int(tokenCount.Load()) {
		t.Fatalf("shared token ids = %d, want %d", got, tokenCount.Load())
	}
	if got := len(reopened.ListTokens("shared")); got != int(tokenCount.Load()) {
		t.Fatalf("ListTokens(shared) len = %d, want %d", got, tokenCount.Load())
	}
}

func TestOpenStoreIgnoresInterruptedWriteArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	usersDir := filepath.Join(root, "users")
	tokensDir := filepath.Join(root, "tokens")
	if err := os.MkdirAll(usersDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(users) error = %v", err)
	}
	if err := os.MkdirAll(tokensDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(tokens) error = %v", err)
	}

	user := UserRecord{
		Username:  "alice",
		CreatedAt: time.Unix(1_700_000_300, 0).UTC(),
		UpdatedAt: time.Unix(1_700_000_300, 0).UTC(),
		TokenIDs:  []string{"tok-1"},
	}
	userJSON, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("Marshal(user) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(usersDir, "alice.json"), userJSON, 0o644); err != nil {
		t.Fatalf("WriteFile(user) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(usersDir, "alice.json.tmp"), []byte("{bad"), 0o644); err != nil {
		t.Fatalf("WriteFile(user tmp) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tokensDir, "tok-1.json.tmp"), []byte("{bad"), 0o644); err != nil {
		t.Fatalf("WriteFile(token tmp) error = %v", err)
	}

	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	if got, ok := store.GetUser("alice"); !ok || got.Username != "alice" {
		t.Fatalf("GetUser(alice) = %#v, %v", got, ok)
	}
}

func TestOpenStoreFailsOnCorruptJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	usersDir := filepath.Join(root, "users")
	if err := os.MkdirAll(usersDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(users) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(usersDir, "broken.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile(broken) error = %v", err)
	}

	if _, err := OpenStore(root); err == nil {
		t.Fatal("OpenStore() succeeded with corrupt json")
	}
}
