package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/thumbs"
)

// TestGeneratedLandscapeFilesSkipTheStream: a generated landscape's item files are Cherry's own (the template artsitem, a
// generated dp.png), answered before any stored or streamed file could be. The store here is the local archive, so the
// streamed precedence is not exercised (the streamed routes are tested in assets_test.go).
func TestGeneratedLandscapeFilesSkipTheStream(t *testing.T) {
	if !thumbs.LandscapeArchived("1908231804") {
		t.Skip("no map pack in the archive: the dp.png renders from it")
	}
	oldCache := thumbs.DpCacheDir
	thumbs.DpCacheDir = t.TempDir()
	t.Cleanup(func() { thumbs.DpCacheDir = oldCache })
	webOperatorDir(t)
	root := localArchive(t)
	if err := os.MkdirAll(filepath.Join(root, "garden", "428400149"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "garden", "428400149", "1409_iteminfo.artsitem"), []byte("TEMPLATE"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, body := bodyOf(t, http.MethodGet, "/img/read/arts_item_garden_428499999/1409_iteminfo.artsitem"); code != http.StatusOK || body != "TEMPLATE" {
		t.Fatalf("generated artsitem = %d %q, want the template", code, body)
	}
	if code, body := bodyOf(t, http.MethodGet, "/arts_item_garden_428499999/dp.png"); code != http.StatusOK || !strings.HasPrefix(body, "\x89PNG") {
		t.Fatalf("generated dp.png = %d, not a PNG", code)
	}
}
