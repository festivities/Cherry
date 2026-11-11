package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/economy"
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
