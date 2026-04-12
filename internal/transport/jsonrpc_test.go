package transport

import (
	"testing"
)

func TestResultResponseFallsBackToErrorResponseOnMarshalFailure(t *testing.T) {
	t.Parallel()

	response := resultResponse(7, struct {
		Ch chan int `json:"ch"`
	}{
		Ch: make(chan int),
	})
	if response.Error == nil {
		t.Fatal("resultResponse() error = nil")
	}
	if response.ID != 7 {
		t.Fatalf("resultResponse() id = %d, want 7", response.ID)
	}
	if response.Result != nil {
		t.Fatalf("resultResponse() result = %q, want nil", string(response.Result))
	}
}
