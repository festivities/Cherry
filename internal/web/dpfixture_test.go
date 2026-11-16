package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

// thumbFixture points the thumbnail generator at temp dirs and restores the globals.
func thumbFixture(t *testing.T) (itemRoot, cacheDir string) {
	t.Helper()
	itemRoot = t.TempDir()
	cacheDir = t.TempDir()
	oldCache := thumbs.DpCacheDir
	thumbs.DpCacheDir = cacheDir
	t.Cleanup(func() { thumbs.DpCacheDir = oldCache })
	testutil.SetArchive(t, testutil.ArchiveMounts(itemRoot))
	thumbFixtureRoot = itemRoot
	return itemRoot, cacheDir
}

// thumbFixtureRoot is the item root of the last thumbFixture (the tests write their item folders there).
var thumbFixtureRoot string

func writeThumbItem(t *testing.T, itemRoot, kind, id, name string, data []byte) {
	t.Helper()
	dir := filepath.Join(itemRoot, kind, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// The "x" art marker on a garment or face part stands for a complete item: thumbs.HasArt wants its parts too.
	if name == "dp.png" && string(data) == "x" && (kind == "dress" || kind == "custom") {
		for _, part := range []string{"1409_iteminfo.artsitem", "0_0.png"} {
			if _, err := os.Stat(filepath.Join(dir, part)); os.IsNotExist(err) {
				writeThumbItem(t, itemRoot, kind, id, part, []byte("x"))
			}
		}
	}
}

// solidThumbPNG returns an opaque RGBA PNG of w*h filled with c.
func solidThumbPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
