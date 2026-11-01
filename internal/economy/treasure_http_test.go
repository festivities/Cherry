package economy

import (
	"io/fs"
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

// TestTreasureArtRoute: every season art file is served as the original PNG; other names are 404.
func TestTreasureArtRoute(t *testing.T) {
	if _, err := fs.Stat(treasureArtFS, "."); err != nil {
		t.Skip("archive UI art not present")
	}
	mux := http.NewServeMux()
	RegisterTreasureRoutes(mux)
	for _, file := range TreasureSeasonArt {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+TreasureArtPath(file), nil))
		want, _ := fs.ReadFile(treasureArtFS, file)
		if rec.Code != 200 || len(want) < 1000 || rec.Body.String() != string(want) {
			t.Fatalf("%s = %d (%d bytes), want the original %d bytes", file, rec.Code, rec.Body.Len(), len(want))
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cherry/treasure/marble_main_board.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unlisted file = %d, want 404", rec.Code)
	}
}
