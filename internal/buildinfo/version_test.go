package buildinfo

import "testing"

func TestResolveVersion(t *testing.T) {
	t.Parallel()

	previous := Version
	t.Cleanup(func() {
		Version = previous
	})

	Version = " v1.2.3 "
	if got := ResolveVersion(); got != "v1.2.3" {
		t.Fatalf("ResolveVersion() = %q, want %q", got, "v1.2.3")
	}

	Version = "   "
	if got := ResolveVersion(); got != "dev" {
		t.Fatalf("ResolveVersion() with blank version = %q, want %q", got, "dev")
	}
}
