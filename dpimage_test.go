package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// setDpFixture points the dp handler at temp dirs and restores the globals.
func setDpFixture(t *testing.T) (itemRoot, cacheDir string) {
	t.Helper()
	itemRoot = t.TempDir()
	cacheDir = t.TempDir()
	oldRoot, oldCache := dpItemRoot, dpCacheDir
	dpItemRoot, dpCacheDir = itemRoot, cacheDir
	t.Cleanup(func() {
		dpItemRoot, dpCacheDir = oldRoot, oldCache
	})
	return itemRoot, cacheDir
}

func writeItemFile(t *testing.T, itemRoot, kind, id, name string, data []byte) {
	t.Helper()
	dir := filepath.Join(itemRoot, kind, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// solidPNG returns an opaque RGBA PNG of w*h filled with c.
func solidPNG(t *testing.T, w, h int, c color.RGBA) []byte {
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

// dressItemInfo builds a version-0 dress 1409_iteminfo.artsitem with the
// given parts: each entry is anchor, z, x, y, filename.
func dressItemInfo(parts ...string) string {
	f := []string{"0", "0", "0", "0", "0", itoa(len(parts) / 5)}
	for i := 0; i < len(parts); i += 5 {
		f = append(f, parts[i], parts[i+1], "0", "0", parts[i+2], parts[i+3], parts[i+4], "0")
	}
	f = append(f, "0")
	return joinSemis(f)
}

func customItemInfo(parts ...string) string {
	f := []string{"0", "0", "0", itoa(len(parts) / 5)}
	for i := 0; i < len(parts); i += 5 {
		f = append(f, parts[i], parts[i+1], "0", "0", parts[i+2], parts[i+3], parts[i+4], "0")
	}
	f = append(f, "0")
	return joinSemis(f)
}

func itoa(n int) string {
	return string([]byte{byte('0' + n)})
}

func joinSemis(f []string) string {
	s := ""
	for i, v := range f {
		if i > 0 {
			s += ";"
		}
		s += v
	}
	return s + ";"
}

func decodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img
}

func TestParseDpPath(t *testing.T) {
	cases := []struct {
		path string
		kind string
		id   string
		ok   bool
	}{
		{"/img/read/arts_item_dress_225000263/dp.png", "dress", "225000263", true},
		{"/arts_item_custom_223800021/dp.png", "custom", "223800021", true},
		{"/arts_item_dress_1/dp.png", "dress", "1", true},
		{"/img/read/arts_item_custom_1/dp.png", "custom", "1", true},
		{"/arts_item_dress_1/0_0.png", "", "", false},
		{"/arts_item_dress_1/dp.png/extra", "", "", false},
		{"/arts_item_dress_12ab/dp.png", "", "", false},
		{"/arts_item_dress_/dp.png", "", "", false},
		{"/arts_item_dress_1/dp.png/", "", "", false},
		{"/arts_item_room_5/dp.png", "", "", false},
		{"/arts_item_dress_1/dp.pngx", "", "", false},
		{"/arts_item_../etc/dp.png", "", "", false},
		{"/1409_iteminfo.artsitem", "", "", false},
		{"/arts_item_custom_1/DP.PNG", "", "", false},
	}
	for _, tc := range cases {
		kind, id, ok := parseDpPath(tc.path)
		if ok != tc.ok || kind != tc.kind || id != tc.id {
			t.Errorf("parseDpPath(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.path, kind, id, ok, tc.kind, tc.id, tc.ok)
		}
	}
}

func TestDpAuthenticServedUnchanged(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	authentic := []byte("authentic dp bytes")
	writeItemFile(t, itemRoot, "dress", "225000263", "dp.png", authentic)
	for _, path := range []string{"/img/read/arts_item_dress_225000263/dp.png", "/arts_item_dress_225000263/dp.png"} {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want %d", path, rec.Code, http.StatusOK)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
			t.Fatalf("%s: Content-Type = %q, want image/png", path, ct)
		}
		if !bytes.Equal(rec.Body.Bytes(), authentic) {
			t.Fatalf("%s: body not served unchanged", path)
		}
	}
}

func TestDpCompositeGeneratedAndCached(t *testing.T) {
	itemRoot, cacheDir := setDpFixture(t)
	dir := filepath.Join(itemRoot, "dress", "225000263")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeItemFile(t, itemRoot, "dress", "225000263", "0_0.png", solidPNG(t, 8, 8, color.RGBA{R: 0xff, A: 0xff}))
	info := dressItemInfo("4", "10", "3", "5", "0_0.png")
	writeItemFile(t, itemRoot, "dress", "225000263", "1409_iteminfo.artsitem", []byte(info))

	rec := serve(t, http.MethodGet, "/arts_item_dress_225000263/dp.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	img := decodePNG(t, rec.Body.Bytes())
	if b := img.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("composite has zero dimensions: %v", b)
	}
	cached, err := os.ReadFile(filepath.Join(cacheDir, "dress_225000263.png"))
	if err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
	if !bytes.Equal(cached, rec.Body.Bytes()) {
		t.Fatal("served bytes differ from cached composite")
	}
}

func TestDpSecondRequestServesCacheWithoutRewrite(t *testing.T) {
	itemRoot, cacheDir := setDpFixture(t)
	writeItemFile(t, itemRoot, "custom", "223800021", "0_0.png", solidPNG(t, 6, 6, color.RGBA{G: 0xff, A: 0xff}))
	writeItemFile(t, itemRoot, "custom", "223800021", "1409_iteminfo.artsitem", []byte(customItemInfo("4", "10", "1", "2", "0_0.png")))

	if rec := serve(t, http.MethodGet, "/arts_item_custom_223800021/dp.png"); rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d", rec.Code)
	}
	cachePath := filepath.Join(cacheDir, "custom_223800021.png")
	replacement := solidPNG(t, 4, 4, color.RGBA{B: 0xff, A: 0xff})
	if err := os.WriteFile(cachePath, replacement, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := serve(t, http.MethodGet, "/img/read/arts_item_custom_223800021/dp.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("second request status = %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), replacement) {
		t.Fatal("second request did not serve the existing cache")
	}
}

func TestDpPartLandsAtRecordedOffset(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	red := color.RGBA{R: 0xff, A: 0xff}
	blue := color.RGBA{B: 0xff, A: 0xff}
	// Same anchor group: 0_0 8x8 red at (3,5); 0_1 4x4 blue at (12,2).
	writeItemFile(t, itemRoot, "dress", "1", "0_0.png", solidPNG(t, 8, 8, red))
	writeItemFile(t, itemRoot, "dress", "1", "0_1.png", solidPNG(t, 4, 4, blue))
	writeItemFile(t, itemRoot, "dress", "1", "1409_iteminfo.artsitem", []byte(dressItemInfo(
		"4", "10", "3", "5", "0_0.png",
		"4", "11", "12", "2", "0_1.png",
	)))

	rec := serve(t, http.MethodGet, "/arts_item_dress_1/dp.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	img := decodePNG(t, rec.Body.Bytes())
	at := func(x, y int) (r, g, b, a uint32) {
		r, g, b, a = img.At(x, y).RGBA()
		return r >> 8, g >> 8, b >> 8, a >> 8
	}
	if r, g, b, a := at(3+dpPartPad, 5+dpPartPad); r != 0xff || g != 0 || b != 0 || a != 0xff {
		t.Fatalf("part 0_0 did not land at (3,5): got rgba(%d,%d,%d,%d)", r, g, b, a)
	}
	if r, g, b, a := at(12+dpPartPad, 2+dpPartPad); r != 0 || g != 0 || b != 0xff || a != 0xff {
		t.Fatalf("part 0_1 did not land at (12,2): got rgba(%d,%d,%d,%d)", r, g, b, a)
	}
}

func TestDpUnknownIDNotFound(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	if err := os.MkdirAll(filepath.Join(itemRoot, "dress", "999999999"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := serve(t, http.MethodGet, "/arts_item_dress_999999999/dp.png")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDpNonDpPathNotFound(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	writeItemFile(t, itemRoot, "dress", "5", "0_0.png", solidPNG(t, 2, 2, color.RGBA{A: 0xff}))
	writeItemFile(t, itemRoot, "dress", "5", "1409_iteminfo.artsitem", []byte(dressItemInfo("4", "10", "0", "0", "0_0.png")))
	for _, path := range []string{
		"/arts_item_dress_5/0_0.png",
		"/arts_item_dress_5/1409_iteminfo.artsitem",
		"/img/read/arts_item_dress_5/iteminfo.txt",
		"/arts_item_garden_5/dp.png",
	} {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestDpNonGetNotFound(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	writeItemFile(t, itemRoot, "dress", "7", "dp.png", []byte("x"))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead} {
		rec := serve(t, method, "/arts_item_dress_7/dp.png")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want %d", method, rec.Code, http.StatusNotFound)
		}
	}
}

func TestDpMissingPartsNotFound(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	writeItemFile(t, itemRoot, "dress", "8", "1409_iteminfo.artsitem", []byte(dressItemInfo("4", "10", "0", "0", "0_0.png")))
	rec := serve(t, http.MethodGet, "/arts_item_dress_8/dp.png")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDpConcurrentRequestsNoCorruption(t *testing.T) {
	itemRoot, cacheDir := setDpFixture(t)
	writeItemFile(t, itemRoot, "dress", "9", "0_0.png", solidPNG(t, 20, 14, color.RGBA{R: 0x40, G: 0x80, A: 0xff}))
	writeItemFile(t, itemRoot, "dress", "9", "1409_iteminfo.artsitem", []byte(dressItemInfo("4", "10", "1", "1", "0_0.png")))

	var wg sync.WaitGroup
	bodies := make([][]byte, 16)
	for i := range bodies {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := serve(t, http.MethodGet, "/arts_item_dress_9/dp.png")
			if rec.Code == http.StatusOK {
				bodies[i] = rec.Body.Bytes()
			}
		}(i)
	}
	wg.Wait()
	first := bodies[0]
	if first == nil {
		t.Fatal("no successful response")
	}
	want := decodePNG(t, first).Bounds()
	for i, b := range bodies {
		if b == nil {
			t.Fatalf("request %d failed", i)
			continue
		}
		img := decodePNG(t, b)
		if img.Bounds() != want {
			t.Fatalf("request %d produced %v, want %v", i, img.Bounds(), want)
		}
	}
	cached, err := os.ReadFile(filepath.Join(cacheDir, "dress_9.png"))
	if err != nil {
		t.Fatalf("cache missing: %v", err)
	}
	decodePNG(t, cached)
}
