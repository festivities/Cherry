package economy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cherry/internal/thumbs"
)

// TestGardenCraftArtInPlace checks ServeGardenCraftArt (streaming off): the craft folder answers first, the download
// cache next, and an absent, non-PNG or non-GET request is refused so the caller's 404 applies.
func TestGardenCraftArtInPlace(t *testing.T) {
	files := t.TempDir()
	old := thumbs.DpItemRoot
	thumbs.DpItemRoot = filepath.Join(files, "item")
	t.Cleanup(func() { thumbs.DpItemRoot = old })
	write := func(rel, body string) {
		p := filepath.Join(files, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("item/craftitem/garden01/items/item110000.png", "craft")
	write("cache/garden01/items/item110000.png", "cache") // the craft folder wins for a path both hold
	write("cache/garden01/items/item210000.png", "cache-only")
	write("cache/craft01/1012/craft_1012_home_banner.png", "banner")

	serve := func(method, p string) (*httptest.ResponseRecorder, bool) {
		rec := httptest.NewRecorder()
		return rec, ServeGardenCraftArt(rec, httptest.NewRequest(method, p, nil))
	}
	for p, want := range map[string]string{
		"/garden01/items/item110000.png":           "craft",
		"/garden01/items/item210000.png":           "cache-only",
		"/craft01/1012/craft_1012_home_banner.png": "banner",
	} {
		if rec, ok := serve(http.MethodGet, p); !ok || rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s: handled=%v code=%d body=%q, want %q", p, ok, rec.Code, rec.Body.String(), want)
		}
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/garden01/items/item999999.png"},
		{http.MethodGet, "/craft01/1013/craft_1013_home_banner.png"},
		{http.MethodGet, "/garden01/items/item110000.jpg"},
		{http.MethodPost, "/garden01/items/item110000.png"},
	} {
		if _, ok := serve(c.method, c.path); ok {
			t.Errorf("%s %s answered", c.method, c.path)
		}
	}
}
