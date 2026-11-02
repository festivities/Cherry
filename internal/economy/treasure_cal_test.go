package economy

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"cherry/internal/thumbs"
)

// calSpecCentres is the centre of each square on the lab canvas (640x846): board-art spec 5.3, case B, x and y in pixels
// (the spec's fractions fall inside the pixel whose index is the integer part).
var calSpecCentres = [28][2]float64{
	{64.5, 808.7}, {137.5, 808.7}, {210.5, 808.7}, {283.5, 808.7}, {356.5, 808.7}, {429.5, 808.7}, {502.5, 808.7}, {575.5, 808.7},
	{575.5, 735.7}, {575.5, 662.7}, {575.5, 589.7}, {575.5, 516.7}, {575.5, 443.7}, {575.5, 370.7}, {575.5, 297.7},
	{502.5, 297.7}, {429.5, 297.7}, {356.5, 297.7}, {283.5, 297.7}, {210.5, 297.7}, {137.5, 297.7}, {64.5, 297.7},
	{64.5, 370.7}, {64.5, 443.7}, {64.5, 516.7}, {64.5, 589.7}, {64.5, 662.7}, {64.5, 735.7},
}

// calAt is the colour of pixel (x, y): transparent pixels come back as the zero colour.
func calAt(img image.Image, x, y int) color.NRGBA {
	r, g, b, a := img.At(x, y).RGBA()
	if a == 0 {
		return color.NRGBA{}
	}
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xFF}
}

// calCache points the picture cache at a test directory for the test's life.
func calCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := thumbs.DpCacheDir
	thumbs.DpCacheDir = dir
	t.Cleanup(func() { thumbs.DpCacheDir = prev })
	return dir
}

// TestTreasureCalBoardTiles: each square's 73-px tile sits at the spec's centre, its fill is its cell type's colour, its border is
// the rim, its number is drawn and its token point is marked (spec 2.2 offset).
func TestTreasureCalBoardTiles(t *testing.T) {
	ring := ActiveTreasureData().Board.Ring
	img := treasureBoardCalPicture(ring)
	if b := img.Bounds(); b.Dx() != 640 || b.Dy() != 846 {
		t.Fatalf("board is %v, want 640x846", b)
	}
	for k, c := range calSpecCentres {
		cx, cy := int(c[0]), int(c[1])
		if got := calAt(img, cx-30, cy-30); got != treasureCellFill[ring[k]] {
			t.Fatalf("square %d: inside the tile %v, want the fill of type %d %v", k, got, ring[k], treasureCellFill[ring[k]])
		}
		if got := calAt(img, cx+36, cy); got != treasureCalRim {
			t.Fatalf("square %d: right edge %v, want the rim", k, got)
		}
		if got := calAt(img, cx, cy-36); got != treasureCalRim {
			t.Fatalf("square %d: top edge %v, want the rim", k, got)
		}
		if got := calAt(img, cx+1, cy+22); got != treasureCalToken {
			t.Fatalf("square %d: token point %v, want the marker", k, got)
		}
		ink := 0
		for y := cy - 36; y <= cy+36; y++ {
			for x := cx - 36; x <= cx+36; x++ {
				if calAt(img, x, y) == treasureCalInk {
					ink++
				}
			}
		}
		if ink < 40 {
			t.Fatalf("square %d: %d number pixels, want the number drawn", k, ink)
		}
	}
	// Square 0 sits in the bottom-left corner of the ring: tile (28, 772) to (100, 844), the token point at (65, 830).
	if got := calAt(img, 28, 772); got != treasureCalRim || calAt(img, 27, 800) != (color.NRGBA{}) {
		t.Fatalf("square 0 corner %v, outside %v", got, calAt(img, 27, 800))
	}
}

// TestTreasureCalBoardRing: the fill follows the ring passed in (the active data), so a new ring gives new colours.
func TestTreasureCalBoardRing(t *testing.T) {
	ring := append([]int(nil), ActiveTreasureData().Board.Ring...)
	ring[0] = 9
	if got := calAt(treasureBoardCalPicture(ring), 34, 778); got != treasureCellFill[9] {
		t.Fatalf("square 0 fill = %v, want the mini-game colour %v", got, treasureCellFill[9])
	}
}

// TestTreasureCalSquareCells: the 28 squares walk the ring's edges from the bottom-left corner, one tile apart, with no gaps or
// repeats (the 8x8 ring: 0..7 bottom, 8..14 right, 15..21 top, 22..27 left).
func TestTreasureCalSquareCells(t *testing.T) {
	seen := map[[2]int]bool{}
	for k := 0; k < 28; k++ {
		col, row := treasureSquareCell(k)
		if col < 0 || col > 7 || row < 0 || row > 7 || (col > 0 && col < 7 && row > 0 && row < 7) || seen[[2]int{col, row}] {
			t.Fatalf("square %d at (%d, %d): not a ring cell", k, col, row)
		}
		seen[[2]int{col, row}] = true
		// the spec's centre for square k, in tiles: x = 64.5 + 73 col, bottom row y = 808.7 - 73 row
		if x, y := treasureRingX+treasureTilePx*col+36, treasureRingY-treasureTilePx*row+36; x != int(calSpecCentres[k][0]) || y != int(calSpecCentres[k][1]) {
			t.Fatalf("square %d tile centre (%d, %d), spec (%v, %v)", k, x, y, calSpecCentres[k][0], calSpecCentres[k][1])
		}
	}
	if len(seen) != 28 {
		t.Fatalf("%d distinct cells, want 28", len(seen))
	}
}

// TestTreasureCalPictures: the board and the background decode at their sizes, carry their frame, edge and crosshair, and are
// cached once each under tw1_; an unknown season picture name is refused.
func TestTreasureCalPictures(t *testing.T) {
	dir := calCache(t)
	for name, size := range map[string][2]int{treasureBoardCal: {640, 846}, treasureBgCal: {640, 1400}} {
		b, ok := treasureSeasonPicture(name)
		if !ok {
			t.Fatalf("%s not drawn", name)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != size[0] || img.Bounds().Dy() != size[1] {
			t.Fatalf("%s: %v, bounds %v, want %v", name, err, img.Bounds(), size)
		}
		if name == treasureBoardCal {
			if calAt(img, 0, 0) != treasureCalFrame || calAt(img, 5, 5) != (color.NRGBA{}) {
				t.Fatalf("board frame or transparent corner wrong: %v %v", calAt(img, 0, 0), calAt(img, 5, 5))
			}
			if calAt(img, 320, 100) != treasureCalCross || calAt(img, 321, 100).A != 0 {
				t.Fatalf("board vertical crosshair at x=320 wrong")
			}
			if calAt(img, 200, 423) != treasureCalCross || calAt(img, 200, 422).A != 0 {
				t.Fatalf("board horizontal crosshair at y=423 wrong")
			}
			continue
		}
		if calAt(img, 0, 0) != treasureCalEdge || calAt(img, 19, 19) != treasureCalEdge || calAt(img, 20, 20) != treasureCalGold {
			t.Fatalf("background edge 20 px wrong: %v %v %v", calAt(img, 0, 0), calAt(img, 19, 19), calAt(img, 20, 20))
		}
		if calAt(img, 320, 100) != treasureCalCross || calAt(img, 100, 700) != treasureCalCross || calAt(img, 100, 699) != treasureCalGold {
			t.Fatalf("background centre crosshair wrong")
		}
	}
	if _, ok := treasureSeasonPicture("bg_cal_v2.png"); ok {
		t.Fatal("an unknown picture was drawn")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 || !strings.HasPrefix(entries[0].Name(), "tw1_") || !strings.HasPrefix(entries[1].Name(), "tw1_") {
		t.Fatalf("cache entries = %v, want two tw1_ files", entries)
	}
}

// TestTreasureCalRoutes: GET serves both pictures as PNG under /cherry/treasure/; other names and methods are 404; the file names
// map to SeasonInfo paths (TreasureArtPath).
func TestTreasureCalRoutes(t *testing.T) {
	calCache(t)
	mux := http.NewServeMux()
	RegisterTreasureRoutes(mux)
	for path, size := range map[string][2]int{"/cherry/treasure/board_cal_v1.png": {640, 846}, "/cherry/treasure/bg_cal_v1.png": {640, 1400}} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("%s = %d %q, want 200 image/png", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		img, err := png.Decode(rec.Body)
		if err != nil || img.Bounds().Dx() != size[0] || img.Bounds().Dy() != size[1] {
			t.Fatalf("%s: %v, bounds %v, want %v", path, err, img.Bounds(), size)
		}
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/cherry/treasure/board_cal_v2.png"},
		{http.MethodPost, "/cherry/treasure/board_cal_v1.png"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", c.method, c.path, rec.Code)
		}
	}
	if TreasureArtPath("board_cal_v1.png") != "cherry/treasure/board_cal_v1.png" {
		t.Fatalf("art path: %q", TreasureArtPath("board_cal_v1.png"))
	}
}
