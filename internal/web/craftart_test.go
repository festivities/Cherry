package web

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"cherry/internal/thumbs"
)

// TestCraftArtRouting checks the streaming switch for the Garden craft art (GET /garden01/…, /craft01/…): with
// streaming on R2 alone answers, and a miss is a 404 (the archive is not read); with streaming off the archive
// answers in place.
func TestCraftArtRouting(t *testing.T) {
	files := t.TempDir()
	old := thumbs.DpItemRoot
	thumbs.DpItemRoot = filepath.Join(files, "item")
	t.Cleanup(func() { thumbs.DpItemRoot = old })
	arch := filepath.Join(files, "item", "craftitem", "garden01", "items")
	if err := os.MkdirAll(arch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(arch, "item110000.png"), []byte("archive-icon"), 0o600); err != nil {
		t.Fatal(err)
	}

	streamServer(t, map[string][]byte{"static/garden01/items/item110000.png": []byte("r2-icon")})
	if code, body := bodyOf(t, http.MethodGet, "/garden01/items/item110000.png"); code != http.StatusOK || body != "r2-icon" {
		t.Fatalf("streaming on: %d %q, want the R2 original", code, body)
	}
	if code, _ := bodyOf(t, http.MethodGet, "/craft01/1012/craft_1012_home_banner.png"); code != http.StatusNotFound {
		t.Fatalf("streaming on, absent in R2: %d, want 404", code)
	}

	streaming = nil
	if code, body := bodyOf(t, http.MethodGet, "/garden01/items/item110000.png"); code != http.StatusOK || body != "archive-icon" {
		t.Fatalf("streaming off: %d %q, want the archive file", code, body)
	}
	if code, _ := bodyOf(t, http.MethodGet, "/garden01/items/item999999.png"); code != http.StatusNotFound {
		t.Fatalf("streaming off, absent: %d, want 404", code)
	}
}
