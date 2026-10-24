package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
)

// TestPopupRoutesAreMounted: the show check, the popup list and the picture reach their economy handlers
// on the real mux while a season with a popup is in force.
func TestPopupRoutesAreMounted(t *testing.T) {
	old := economy.NowFunc
	t.Cleanup(func() { economy.NowFunc = old })
	economy.NowFunc = func() time.Time { return time.Date(2026, time.October, 26, 12, 0, 0, 0, time.FixedZone("JST", 9*3600)) }
	if rec := serve(t, http.MethodGet, "/v4/popup/isExists"); rec.Body.String() != `{"result":true}` {
		t.Fatalf("isExists = %q", rec.Body.String())
	}
	if rec := serve(t, http.MethodGet, "/v4/popup/list"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"popupNo":9101`) {
		t.Fatalf("list = %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve(t, http.MethodGet, "/cherry/event/halloween.png"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("picture = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
