package economy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAdminAnswersOnlyLoopbackHostNames: the staff tool answers to a loopback Host name only (PLAN 8.6b part 4): a name
// that resolves to 127.0.0.1 from another site (DNS rebinding) gets the 404 of an unknown route.
func TestAdminAnswersOnlyLoopbackHostNames(t *testing.T) {
	h := AdminHandler()
	for host, reached := range map[string]bool{
		"127.0.0.1:8099":      true,
		"127.0.0.1":           true,
		"localhost:8099":      true,
		"LOCALHOST":           true,
		"[::1]:8099":          true,
		"evil.example:8099":   false,
		"127.0.0.1.nip.io:80": false,
		"192.168.1.7:8099":    false,
		"":                    false,
		"localhost.evil.test": false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = "127.0.0.1:50000" // the loopback check of /healthz passes; only the Host decides here
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Code != http.StatusNotFound; got != reached {
			t.Errorf("Host %q: reached the tool = %v (status %d), want %v", host, got, rec.Code, reached)
		}
	}
}
