package auth

import (
	"net/http"
	"testing"
)

// TestCallbackHasItsOwnPerAddressLimit: the Discord redirect hop is limited per address (PLAN 8.6b part 4). A bad state
// is a page, and every call counts, so the flood of callbacks from one address ends in 429 while another address is fine.
func TestCallbackHasItsOwnPerAddressLimit(t *testing.T) {
	env := newEnv(t, devConfig())
	for i := 0; i < callbackPerMinute; i++ {
		if rec := env.get("/auth/v1/discord/callback?state=x", testIP); rec.Code != http.StatusBadRequest {
			t.Fatalf("callback %d = %d, want the 400 page for a bad state", i+1, rec.Code)
		}
	}
	if rec := env.get("/auth/v1/discord/callback?state=x", testIP); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("callback past the limit = %d, want 429", rec.Code)
	}
	if rec := env.get("/auth/v1/discord/callback?state=x", "192.0.2.99"); rec.Code != http.StatusBadRequest {
		t.Fatalf("another address = %d, want its own budget", rec.Code)
	}
}
