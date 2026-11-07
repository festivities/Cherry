package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
)

// TestHomeIntegratedInfoIsMounted: the Garden home info reaches its economy handler on the real mux while the
// Halloween icon's season is in force. Only the exact path is served; POST and sub-paths are 404.
func TestHomeIntegratedInfoIsMounted(t *testing.T) {
	old := economy.NowFunc
	t.Cleanup(func() { economy.NowFunc = old })
	economy.NowFunc = func() time.Time { return time.Date(2026, time.October, 26, 12, 0, 0, 0, time.FixedZone("JST", 9*3600)) }
	if rec := serve(t, http.MethodGet, "/v4/home/integrated/info"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"iconId":"halloween_17s"`) {
		t.Fatalf("home info = %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve(t, http.MethodPost, "/v4/home/integrated/info"); rec.Code != http.StatusNotFound {
		t.Fatalf("POST home info = %d, want 404", rec.Code)
	}
	if rec := serve(t, http.MethodGet, "/v4/home/integrated/info/extra"); rec.Code != http.StatusNotFound {
		t.Fatalf("sub-path home info = %d, want 404", rec.Code)
	}
}
