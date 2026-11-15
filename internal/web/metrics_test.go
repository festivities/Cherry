package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestDebugVarsOnlyOnAdminMux: the counters are served by the admin mux (loopback GET) and by no game mux.
func TestDebugVarsOnlyOnAdminMux(t *testing.T) {
	for name, h := range map[string]http.Handler{"game https": NewGameMux(nil), "plain http": NewMux()} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/vars", nil))
		if strings.Contains(rec.Body.String(), "cherry.requests") {
			t.Fatalf("%s serves the counters at /debug/vars", name)
		}
	}
	admin := economy.NewAdminMux()
	req := httptest.NewRequest(http.MethodGet, "/debug/vars", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cherry.saves"`) {
		t.Fatalf("admin /debug/vars = %d, want the counters", rec.Code)
	}
	req.RemoteAddr = "192.0.2.7:50000"
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("admin /debug/vars from a non-loopback address = %d, want 404", rec.Code)
	}
}

// TestHealthzOnAdminMux: /healthz answers ok on the admin mux while the database answers, 503 with no database
// attached, and is loopback only like the other staff routes.
func TestHealthzOnAdminMux(t *testing.T) {
	admin := economy.NewAdminMux()
	get := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		admin.ServeHTTP(rec, req)
		return rec
	}
	testutil.UseDatabase(t)
	if rec := get("127.0.0.1:50000"); rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("healthz with an open store = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
	if rec := get("192.0.2.7:50000"); rec.Code != http.StatusNotFound {
		t.Fatalf("healthz from a non-loopback address = %d, want 404", rec.Code)
	}
	store.DetachDB()
	if rec := get("127.0.0.1:50000"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz with no database = %d, want 503", rec.Code)
	}
}
