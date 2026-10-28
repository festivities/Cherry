package thumbs

import (
	"bytes"
	"image/color"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// archiveMaskFixture points the mask cache at a temp dir (never the live dp-cache) and skips without the device archive.
func archiveMaskFixture(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(DpItemRoot, "custom")); err != nil {
		t.Skip("device archive absent")
	}
	cacheDir := t.TempDir()
	old := DpCacheDir
	DpCacheDir = cacheDir
	t.Cleanup(func() { DpCacheDir = old })
	return cacheDir
}

// TestDyeMaskOfAGeneratedGarment: the mask of 224102536 (no original mask in the archive) is a non-empty picture the size of
// its dp.png, drawn inside the dp.png's opaque area, with the base tone of the bald head and part red; ServeDyeMask serves
// it and caches it under dm1_.
func TestDyeMaskOfAGeneratedGarment(t *testing.T) {
	cacheDir := archiveMaskFixture(t)
	dir := filepath.Join(DpItemRoot, "custom", "224102536")
	mask, ok := composeItemMask(dir, "custom")
	if !ok {
		t.Fatal("mask not composed")
	}
	dpb, ok := RenderDP(dir, "custom")
	if !ok {
		t.Fatal("dp.png not rendered")
	}
	dp := decodePNG(t, dpb)
	if mask.Bounds() != dp.Bounds() {
		t.Fatalf("mask %v, dp.png %v", mask.Bounds(), dp.Bounds())
	}
	var opaque, inside, blue, red int
	for y := mask.Bounds().Min.Y; y < mask.Bounds().Max.Y; y++ {
		for x := mask.Bounds().Min.X; x < mask.Bounds().Max.X; x++ {
			r, g, b, a := mask.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			opaque++
			if _, _, _, da := dp.At(x, y).RGBA(); da != 0 {
				inside++
			}
			switch {
			case a == 0xffff && r == 0 && g == 0 && b == 0xffff:
				blue++
			case a == 0xffff && r == 0xffff && g == 0 && b == 0:
				red++
			}
		}
	}
	if opaque == 0 || inside*100 < opaque*99 || blue == 0 || red == 0 {
		t.Fatalf("mask opaque %d, inside dp.png %d, blue %d, red %d", opaque, inside, blue, red)
	}
	rec := httptest.NewRecorder()
	if !ServeDyeMask(rec, "custom", "224102536") || rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("ServeDyeMask: code %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if served := decodePNG(t, rec.Body.Bytes()); served.Bounds() != dp.Bounds() {
		t.Fatalf("served %v, want %v", served.Bounds(), dp.Bounds())
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "dm1_custom_224102536.png")); err != nil {
		t.Fatalf("mask not cached: %v", err)
	}
}

// TestDyeMaskServesAnOriginal: an archive original mask is served byte for byte (streaming off), never generated or
// cached; a kind other than custom or dress is refused.
func TestDyeMaskServesAnOriginal(t *testing.T) {
	cacheDir := archiveMaskFixture(t)
	for _, c := range [][2]string{{"custom", "224102309"}, {"dress", "225001650"}} {
		want, err := os.ReadFile(filepath.Join(DpItemRoot, c[0], c[1], "dp_mask.png"))
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		if !ServeDyeMask(rec, c[0], c[1]) || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Fatalf("%s %s: the original mask was not served as is", c[0], c[1])
		}
	}
	if rec := httptest.NewRecorder(); ServeDyeMask(rec, "pet", "224102536") {
		t.Fatal("pet kind answered")
	}
	if entries, _ := os.ReadDir(cacheDir); len(entries) != 0 {
		t.Fatalf("cache holds %d files after refusals", len(entries))
	}
}

// TestDyeMaskLayout (no archive): the mask is laid out by the sprites. A part with no mask is transparent in the mask but
// opaque in the colour preview, a mask holds only its part's red, and a mask more than 2 px off its sprite is refused.
func TestDyeMaskLayout(t *testing.T) {
	root := t.TempDir()
	writeItemFile(t, root, "custom", "224999001", "1409_iteminfo.artsitem", []byte(customItemInfo(
		"1", "1", "0", "0", "a.png",
		"1", "2", "10", "0", "b.png",
	)))
	writeItemFile(t, root, "custom", "224999001", "a.png", solidPNG(t, 8, 8, color.RGBA{G: 0xff, A: 0xff}))
	writeItemFile(t, root, "custom", "224999001", "a_mask.png", solidPNG(t, 8, 8, color.RGBA{R: 0xff, A: 0xff}))
	writeItemFile(t, root, "custom", "224999001", "b.png", solidPNG(t, 4, 4, color.RGBA{B: 0xff, A: 0xff}))
	dir := filepath.Join(root, "custom", "224999001")

	mask, ok := composeItemMask(dir, "custom")
	if !ok {
		t.Fatal("mask not composed")
	}
	col, cat, ok := dpComposeNatural(dir, "custom")
	if !ok {
		t.Fatal("colour not composed")
	}
	p := dpFrameFor(cat)
	p.shadow = false
	colour := dpFrameWith(col, p)
	var maskOpaque, colOpaque int
	for y := mask.Bounds().Min.Y; y < mask.Bounds().Max.Y; y++ {
		for x := mask.Bounds().Min.X; x < mask.Bounds().Max.X; x++ {
			r, g, b, a := mask.At(x, y).RGBA()
			if g != 0 || b != 0 || r > a+0x100 {
				t.Fatalf("mask pixel (%d,%d) is not red: rgba(%x,%x,%x,%x)", x, y, r, g, b, a)
			}
			if a != 0 {
				maskOpaque++
			}
			if _, _, _, ca := colour.At(x, y).RGBA(); ca != 0 {
				colOpaque++
			}
		}
	}
	if maskOpaque == 0 || maskOpaque >= colOpaque {
		t.Fatalf("mask opaque %d, colour opaque %d: the unmasked part should be transparent only in the mask", maskOpaque, colOpaque)
	}

	writeItemFile(t, root, "custom", "224999002", "1409_iteminfo.artsitem", []byte(customItemInfo("1", "1", "0", "0", "c.png")))
	writeItemFile(t, root, "custom", "224999002", "c.png", solidPNG(t, 8, 8, color.RGBA{G: 0xff, A: 0xff}))
	writeItemFile(t, root, "custom", "224999002", "c_mask.png", solidPNG(t, 20, 20, color.RGBA{R: 0xff, A: 0xff}))
	if _, ok := composeItemMask(filepath.Join(root, "custom", "224999002"), "custom"); ok {
		t.Fatal("a mask 12 px off its sprite was composed")
	}
}
