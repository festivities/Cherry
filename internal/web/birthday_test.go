package web

import (
	"net/http"
	"strings"
	"testing"
)

// TestBirthdayRoutesAreMounted: the birthday routes answer GET on the real mux (never 404 for a request with no avatar,
// the unqualified reply) and refuse other methods.
func TestBirthdayRoutesAreMounted(t *testing.T) {
	for _, path := range []string{"/v4/birthday/info/year", "/v4/birthday/info/day", "/v4/birthday/home", "/v4/birthday/reward/year/1"} {
		if rec := serve(t, http.MethodGet, path); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d %s", path, rec.Code, rec.Body.String())
		}
		if rec := serve(t, http.MethodPost, path); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, rec.Code)
		}
	}
	if rec := serve(t, http.MethodGet, "/v4/birthday/reward/year/1"); strings.TrimSpace(rec.Body.String()) != `{"result":false}` {
		t.Errorf("claim with no account = %s", rec.Body.String())
	}
}
