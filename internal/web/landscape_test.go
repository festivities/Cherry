package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/thumbs"
)

// TestGeneratedLandscapeFilesSkipTheStream: with streaming on, a generated landscape's item files are
// Cherry's own (the template artsitem, a generated dp.png), even when the bucket holds objects under the
// same keys. An original landscape keeps its streamed file.
func TestGeneratedLandscapeFilesSkipTheStream(t *testing.T) {
	if !thumbs.LandscapeArchived("1908231804") {
		t.Skip("no map pack in the archive: the dp.png renders from it")
	}
	oldCache := thumbs.DpCacheDir
	thumbs.DpCacheDir = t.TempDir()
	t.Cleanup(func() { thumbs.DpCacheDir = oldCache })
	webOperatorDir(t)
	objs, _ := streamBucket(t)
	objs["static/item/garden/428499999/dp.png"] = []byte("R2THUMB")
	objs["static/item/garden/428499999/1409_iteminfo.artsitem"] = []byte("R2ART")
	objs["static/item/garden/428400003/dp.png"] = []byte("R2ORIG")
	streamServer(t, objs)
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
	if code, body := bodyOf(t, http.MethodGet, "/img/read/arts_item_garden_428400003/dp.png"); code != http.StatusOK || body != "R2ORIG" {
		t.Fatalf("original landscape dp.png = %d %q, want the streamed file", code, body)
	}
}
