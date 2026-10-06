package main

import (
	"archive/zip"
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
	oldRoot, oldCache, oldZip := dpItemRoot, dpCacheDir, dpZipPath
	dpItemRoot, dpCacheDir, dpZipPath = itemRoot, cacheDir, filepath.Join(t.TempDir(), "none.zip")
	t.Cleanup(func() {
		dpItemRoot, dpCacheDir, dpZipPath = oldRoot, oldCache, oldZip
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
	if b := img.Bounds(); b.Dx() != 130 || b.Dy() != 100 {
		t.Fatalf("composite is %v, want 130x100", b)
	}
	cached, err := os.ReadFile(filepath.Join(cacheDir, "v2_dress_225000263.png"))
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
	cachePath := filepath.Join(cacheDir, "v2_custom_223800021.png")
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

	nat, _, ok := dpComposeNatural(filepath.Join(itemRoot, "dress", "1"), "dress")
	if !ok {
		t.Fatal("compose failed")
	}
	img := nat.img
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
	cached, err := os.ReadFile(filepath.Join(cacheDir, "v2_dress_9.png"))
	if err != nil {
		t.Fatalf("cache missing: %v", err)
	}
	decodePNG(t, cached)
}

func TestDpZipPreviewServedAndCached(t *testing.T) {
	_, cacheDir := setDpFixture(t)
	want := solidPNG(t, 7, 5, color.RGBA{R: 9, A: 0xff})
	zp := filepath.Join(t.TempDir(), "item.zip")
	f, err := os.Create(zp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("item/custom/224400001/dp.png")
	w.Write(want)
	zw.Close()
	f.Close()
	dpZipPath = zp
	t.Cleanup(func() {
		dpZipIdx.Lock()
		if dpZipIdx.rc != nil {
			dpZipIdx.rc.Close()
		}
		dpZipIdx.rc, dpZipIdx.path = nil, ""
		dpZipIdx.Unlock()
	})
	rec := serve(t, http.MethodGet, "/arts_item_custom_224400001/dp.png")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("zip preview not served unchanged: %d", rec.Code)
	}
	if b, err := os.ReadFile(filepath.Join(cacheDir, "v2_custom_224400001.png")); err != nil || !bytes.Equal(b, want) {
		t.Fatalf("zip preview not cached: %v", err)
	}
	if rec := serve(t, http.MethodGet, "/arts_item_custom_224400002/dp.png"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing id status = %d", rec.Code)
	}
}

func TestDpOldCacheFileIgnored(t *testing.T) {
	itemRoot, cacheDir := setDpFixture(t)
	writeItemFile(t, itemRoot, "dress", "3", "0_0.png", solidPNG(t, 6, 6, color.RGBA{G: 9, A: 0xff}))
	writeItemFile(t, itemRoot, "dress", "3", "1409_iteminfo.artsitem", []byte(dressItemInfo("4", "10", "1", "1", "0_0.png")))
	if err := os.WriteFile(filepath.Join(cacheDir, "dress_3.png"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := serve(t, http.MethodGet, "/arts_item_dress_3/dp.png")
	if rec.Code != http.StatusOK || bytes.Equal(rec.Body.Bytes(), []byte("stale")) {
		t.Fatalf("stale unversioned cache served (status %d)", rec.Code)
	}
}

func TestDpCustomV1ExtraFields(t *testing.T) {
	// ver 1 custom rows carry two ints after the filename (Custom_V1).
	text := "0;1;1;1;4;209;0;0;48;32;0_0.png;0;0;1;7;0;0;0;0;0;0;0;0;0;"
	front, _, ok := parseCustomItemParts(text)
	if !ok || len(front) != 1 || front[0].name != "0_0.png" || front[0].x != 48 {
		t.Fatalf("parse = %+v ok=%v", front, ok)
	}
}

func TestDpCategoryOf(t *testing.T) {
	for id, want := range map[string]dpCat{
		"224406200": {code: "HA"}, "225303274": {code: "SH"},
		"233900001": {code: "HE", animal: true}, "225400001": {code: "AH"}, "9": {},
	} {
		if got := dpCategoryOf(id); got != want {
			t.Errorf("dpCategoryOf(%s) = %+v, want %+v", id, got, want)
		}
	}
}

func TestDpRigPlacementMath(t *testing.T) {
	rig := dpLoadRig("h")
	if rig == nil {
		t.Fatal("no rig")
	}
	// part centre = node lower-left + (x, y); lower-left of sprite = centre - size/2.
	ll := rig.ll[dpTagHead]
	img := image.NewRGBA(image.Rect(0, 0, 10, 6))
	x, y, ok := rig.place(dpTagHead, 20, 30, img)
	if !ok || x != ll[0]+20-5 || y != ll[1]+30-3 {
		t.Fatalf("place = (%v,%v,%v), want (%v,%v)", x, y, ok, ll[0]+15, ll[1]+27)
	}
	if _, _, ok := rig.place(dpTagSpecial, 0, 0, img); ok {
		t.Fatal("anchor 17 must have no frame")
	}
	for _, tag := range []int{dpTagNoneHd, dpTagNoneChe, 11, 13, 14} {
		if _, ok := rig.ll[tag]; !ok {
			t.Errorf("rig missing node %d", tag)
		}
	}
}

func TestDpRasterizeZOrderAndFlip(t *testing.T) {
	red := image.NewRGBA(image.Rect(0, 0, 4, 4))
	blue := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(red.Pix); i += 4 {
		red.Pix[i], red.Pix[i+3] = 255, 255
		blue.Pix[i+2], blue.Pix[i+3] = 255, 255
	}
	// blue has the lower z but is listed last: red must stay on top; blue sits
	// 2px higher (y up) so it is drawn 2px higher in image space.
	img, _, _ := dpRasterize([]dpLayer{{z: 9, img: red, x: 0, y: 0}, {z: 1, img: blue, x: 0, y: 2}}, 0)
	if img.Bounds().Dy() != 6 {
		t.Fatalf("height = %d, want 6", img.Bounds().Dy())
	}
	if c := img.RGBAAt(1, 3); c.R != 255 || c.B != 0 {
		t.Fatalf("overlap pixel = %+v, want red on top", c)
	}
	if c := img.RGBAAt(1, 0); c.B != 255 {
		t.Fatalf("top pixel = %+v, want blue", c)
	}
}

func TestDpMultiAnchorPartsAllDrawn(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	writeItemFile(t, itemRoot, "dress", "225303000", "0_0.png", solidPNG(t, 10, 10, color.RGBA{R: 255, A: 255}))
	writeItemFile(t, itemRoot, "dress", "225303000", "0_1.png", solidPNG(t, 10, 10, color.RGBA{B: 255, A: 255}))
	writeItemFile(t, itemRoot, "dress", "225303000", "1409_iteminfo.artsitem", []byte(dressItemInfo(
		"11", "131", "5", "5", "0_0.png", "13", "155", "5", "5", "0_1.png")))
	nat, _, ok := dpComposeNatural(filepath.Join(itemRoot, "dress", "225303000"), "dress")
	if !ok || !nat.skel {
		t.Fatalf("expected skeleton compose, ok=%v", ok)
	}
	// Right and left foot nodes are apart: the render is wider than one 10px sprite.
	if nat.img.Bounds().Dx() < 20 {
		t.Fatalf("width %d: only one anchor group drawn", nat.img.Bounds().Dx())
	}
}

func TestDpFrameAlways130x100(t *testing.T) {
	for _, code := range []string{"SH", "HA", "EY", ""} {
		n := &dpNat{img: image.NewRGBA(image.Rect(0, 0, 300, 20)), headX: 5, headY: 5}
		n.img.Pix[3] = 255
		n.img.Pix[len(n.img.Pix)-1] = 255
		if b := dpFrame(n, dpCat{code: code}).Bounds(); b.Dx() != 130 || b.Dy() != 100 {
			t.Errorf("%q framed %v", code, b)
		}
	}
}

func TestDpEyebrowDefaultExpressionOnly(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	id := "224000028"
	// Two stacked expression variants at different places; only the first row
	// (default) may be drawn, so the render is just that one sprite.
	writeItemFile(t, itemRoot, "custom", id, "0_0.png", solidPNG(t, 6, 3, color.RGBA{R: 255, A: 255}))
	writeItemFile(t, itemRoot, "custom", id, "0_1.png", solidPNG(t, 6, 3, color.RGBA{B: 255, A: 255}))
	writeItemFile(t, itemRoot, "custom", id, "1409_iteminfo.artsitem", []byte(customItemInfo(
		"4", "204", "37", "43", "0_0.png", "4", "203", "30", "40", "0_1.png")))
	nat, _, ok := dpComposeNatural(filepath.Join(itemRoot, "custom", id), "custom")
	if !ok {
		t.Fatal("compose failed")
	}
	for y := 0; y < nat.img.Bounds().Dy(); y++ {
		for x := 0; x < nat.img.Bounds().Dx(); x++ {
			if c := nat.img.RGBAAt(x, y); c.B > c.R {
				t.Fatalf("non-default expression variant drawn at (%d,%d)", x, y)
			}
		}
	}
}
