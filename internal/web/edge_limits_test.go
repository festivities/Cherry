package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cherry/internal/config"
	"cherry/internal/httpx"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestApplyLimitsChangesLiveBuckets: ApplyLimits changes the buckets of a wrapper that is already serving. A lowered burst
// cuts an address's tokens, a raised burst serves new addresses, a zero rate turns the limit off, and the account bucket
// starts to count once its rate is above zero.
func TestApplyLimitsChangesLiveBuckets(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"live-token": {AccessToken: "live-token", Aid: "51", Name: "Live"}})
	pConns, tConns := httpx.Conns.Caps()
	t.Cleanup(func() { httpx.Conns.Set(pConns, tConns) })

	h := newLimited(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }), config.Config{APIRate: 1, APIBurst: 2})
	get := func(remote, cookie string) int {
		req := httptest.NewRequest(http.MethodGet, "/v4/profile/51", nil)
		req.RemoteAddr = remote
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for range 2 {
		if c := get("198.51.100.1:1", ""); c != http.StatusOK {
			t.Fatalf("burst request = %d", c)
		}
	}
	if c := get("198.51.100.1:2", ""); c != http.StatusTooManyRequests {
		t.Fatalf("request past the burst = %d, want 429", c)
	}

	// Raise the burst to 5: a new address starts full at 5; the old address has no tokens yet (no time has passed).
	ApplyLimits(store.Limits{APIRate: 1, APIBurst: 5})
	for i := range 5 {
		if c := get("198.51.100.2:1", ""); c != http.StatusOK {
			t.Fatalf("new address request %d under burst 5 = %d", i+1, c)
		}
	}
	if c := get("198.51.100.2:1", ""); c != http.StatusTooManyRequests {
		t.Fatalf("6th request under burst 5 = %d, want 429", c)
	}

	// Lower the burst to 1: the second address keeps at most one token.
	ApplyLimits(store.Limits{APIRate: 1, APIBurst: 1})
	if c := get("198.51.100.2:2", ""); c != http.StatusTooManyRequests {
		t.Fatalf("request after the burst was lowered to 1 = %d, want 429", c)
	}

	// Off: nothing is refused, for any address.
	ApplyLimits(store.Limits{})
	for range 50 {
		if c := get("198.51.100.1:3", ""); c != http.StatusOK {
			t.Fatalf("request with the limit off = %d", c)
		}
	}

	// The account bucket follows its rate: off at first, one token a second with burst 1 once it is set.
	ApplyLimits(store.Limits{AccountRate: 1, AccountBurst: 1})
	if c := get("203.0.113.1:1", "live-token"); c != http.StatusOK {
		t.Fatalf("first account request = %d", c)
	}
	if c := get("203.0.113.2:1", "live-token"); c != http.StatusTooManyRequests {
		t.Fatalf("second account request from another address = %d, want 429", c)
	}
	ApplyLimits(store.Limits{})
	if c := get("203.0.113.3:1", "live-token"); c != http.StatusOK {
		t.Fatalf("account request after the account limit went off = %d", c)
	}
}

// TestApplyLimitsSetsConnectionCaps: the caps of httpx.Conns are the effective ones.
func TestApplyLimitsSetsConnectionCaps(t *testing.T) {
	pConns, tConns := httpx.Conns.Caps()
	t.Cleanup(func() { httpx.Conns.Set(pConns, tConns) })
	ApplyLimits(store.Limits{ConnsPerAddress: 200, ConnsTotal: 3000})
	if perHost, total := httpx.Conns.Caps(); perHost != 200 || total != 3000 {
		t.Fatalf("caps = %d, %d; want 200, 3000", perHost, total)
	}
}
