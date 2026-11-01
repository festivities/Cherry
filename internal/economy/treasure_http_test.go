package economy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTreasureMoneyRoutesRefuse: the treasure pass is not offered and the special pack list is empty.
func TestTreasureMoneyRoutesRefuse(t *testing.T) {
	mux := http.NewServeMux()
	RegisterTreasureRoutes(mux)
	for path, want := range map[string]string{
		"/v4/bill/prod/treasure/icon?deviceType=Android&marketLocale=": `{"result":{"available":false}}`,
		"/v4/bill/package2/bluemarble/info":                            `{"result":{}}`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
			t.Fatalf("%s: %d %s, want 200 %s", path, rec.Code, rec.Body, want)
		}
	}
}
