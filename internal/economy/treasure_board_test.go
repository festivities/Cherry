package economy

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"cherry/internal/thumbs"
)

// treasureTileTable is the case-A tile left and top of squares 0 to 27 (board-art spec 5.3): the 640x684 board's design target.
var treasureTileTable = [28][2]float64{
	{28, 561}, {101, 561}, {174, 561}, {247, 561}, {320, 561}, {393, 561}, {466, 561}, {539, 561},
	{539, 488}, {539, 415}, {539, 342}, {539, 269}, {539, 196}, {539, 123}, {539, 50},
	{466, 50}, {393, 50}, {320, 50}, {247, 50}, {174, 50}, {101, 50}, {28, 50},
	{28, 123}, {28, 196}, {28, 269}, {28, 342}, {28, 415}, {28, 488},
}

// treasureNear reports whether two colours agree within tol per channel.
func treasureNear(a, b color.NRGBA, tol int) bool {
	d := func(x, y uint8) int {
		if x > y {
			return int(x) - int(y)
		}
		return int(y) - int(x)
	}
	return d(a.R, b.R) <= tol && d(a.G, b.G) <= tol && d(a.B, b.B) <= tol
}

// TestTreasureBoardV1Pictures: the board is 640x684 and the background 640x1400, both opaque where they should be (the plate and
// the panel) and transparent at the corner of the board; the background is the same picture on every render.
func TestTreasureBoardV1Pictures(t *testing.T) {
	d := ActiveTreasureData()
	board := treasureBoardV1Picture(d).(*image.NRGBA)
	if b := board.Bounds(); b.Dx() != 640 || b.Dy() != 684 {
		t.Fatalf("board is %v, want 640x684", b)
	}
	if board.NRGBAAt(2, 2).A != 0 || board.NRGBAAt(320, 336).A != 0xFF {
		t.Fatalf("board corner alpha %d, platform alpha %d: want transparent and opaque", board.NRGBAAt(2, 2).A, board.NRGBAAt(320, 336).A)
	}
	bg := treasureBgV1Picture().(*image.NRGBA)
	if b := bg.Bounds(); b.Dx() != 640 || b.Dy() != 1400 {
		t.Fatalf("background is %v, want 640x1400", b)
	}
	if !bytes.Equal(bg.Pix, treasureBgV1Picture().(*image.NRGBA).Pix) {
		t.Fatal("two renders of the background differ")
	}
}

// TestTreasureBoardV1Geometry: the tiles sit at the case-A table, 73 px apart along the ring, inside the 640x684 canvas.
func TestTreasureBoardV1Geometry(t *testing.T) {
	for k, want := range treasureTileTable {
		if x, y := treasureTileOrigin(k); x != want[0] || y != want[1] {
			t.Fatalf("square %d tile at (%v, %v), case A (%v, %v)", k, x, y, want[0], want[1])
		}
		if want[0]+treasureBoardTile > 640 || want[1]+treasureBoardTile > 684 {
			t.Fatalf("square %d tile leaves the canvas", k)
		}
	}
}

// TestTreasureBoardV1TileFaces: each square's tile carries its cell type's face. The sample sits low on the left of the tile,
// clear of the icon, the rim and the token point; the hatch of the exchange and gold key tiles adds lines, so 30 per channel is
// allowed.
func TestTreasureBoardV1TileFaces(t *testing.T) {
	d := ActiveTreasureData()
	img := treasureBoardV1Picture(d).(*image.NRGBA)
	for k, typ := range d.Board.Ring {
		x0, y0 := treasureTileOrigin(k)
		f := artFaces[typ]
		y := int(y0) + 50
		want := artMix(artC(f.top), artC(f.bottom), (float64(y)+0.5-y0)/66)
		if got := img.NRGBAAt(int(x0)+5, y); !treasureNear(got, want, 30) {
			t.Fatalf("square %d (type %d): sample %v, want the face %v", k, typ, got, want)
		}
	}
}

// TestTreasureBoardV1ExchangeStars: an exchange square shows its rate as gold stars: 1x one star, 2x two, 3x three, and a gamble
// three (the question mark sits above the stars and is not counted).
func TestTreasureBoardV1ExchangeStars(t *testing.T) {
	d := ActiveTreasureData()
	img := treasureBoardV1Picture(d).(*image.NRGBA)
	gold := artC(0xFFE14D)
	counts := map[int]int{}
	for k, typ := range d.Board.Ring {
		if typ != TreasureCellExchange {
			continue
		}
		times, gamble := d.Exchange.tile(k)
		stars := min(3, int(times))
		if gamble > 0 {
			stars = 3
		}
		x0, y0 := treasureTileOrigin(k)
		n := 0
		for y := int(y0) + 40; y < int(y0)+52; y++ {
			for x := int(x0); x < int(x0)+treasureBoardTile; x++ {
				if img.NRGBAAt(x, y) == gold {
					n++
				}
			}
		}
		if prev, ok := counts[stars]; ok && prev != n {
			t.Fatalf("square %d: %d star pixels, %d for another square with %d stars", k, n, prev, stars)
		}
		counts[stars] = n
	}
	if len(counts) < 2 || counts[1] == 0 {
		t.Fatalf("star counts %v: the ring has no exchange squares at two rates", counts)
	}
	for s := 2; s <= 3; s++ {
		if n, ok := counts[s]; ok && n <= counts[s-1] {
			t.Fatalf("star counts %v: %d stars are not more than %d", counts, s, s-1)
		}
	}
}

// TestTreasureBoardV1Name: the board's name is board_v1_<crc32 of its key>; it changes with the ring and with the exchange rates,
// and stays the same for the same data.
func TestTreasureBoardV1Name(t *testing.T) {
	d := ActiveTreasureData()
	name := treasureBoardV1Name(d)
	if !regexp.MustCompile(`^board_v1_[0-9a-f]{8}\.png$`).MatchString(name) {
		t.Fatalf("name %q", name)
	}
	if again := *d; treasureBoardV1Name(&again) != name {
		t.Fatal("the same data gave another name")
	}
	ring := *d
	ring.Board.Ring = append([]int(nil), d.Board.Ring...)
	ring.Board.Ring[0] = TreasureCellMiniGame
	if treasureBoardV1Name(&ring) == name {
		t.Fatal("a changed ring kept the name")
	}
	rates := *d
	rates.Exchange.Tiles = append([]TreasureTile(nil), d.Exchange.Tiles...)
	rates.Exchange.Tiles[0].Times++
	if treasureBoardV1Name(&rates) == name {
		t.Fatal("changed exchange rates kept the name")
	}
}

// TestTreasureBoardV1Routes: the board and the background serve as PNGs under the names SeasonInfo sends (TreasureBoardPicture and
// TreasureBackgroundPicture); the board's name for another ring is 404, and a blank name sends no picture.
func TestTreasureBoardV1Routes(t *testing.T) {
	calCache(t)
	d := ActiveTreasureData()
	mux := http.NewServeMux()
	RegisterTreasureRoutes(mux)
	if got, want := TreasureBoardPicture(d), "cherry/treasure/"+treasureBoardV1Name(d); got != want {
		t.Fatalf("SkinUrl path %q, want %q", got, want)
	}
	if got := TreasureBackgroundPicture(d); got != "cherry/treasure/bg_v1.png" {
		t.Fatalf("BgImgUrl path %q", got)
	}
	for path, size := range map[string][2]int{"/cherry/treasure/" + treasureBoardV1Name(d): {640, 684}, "/cherry/treasure/bg_v1.png": {640, 1400}} {
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
	other := *d
	other.Board.Ring = append([]int(nil), d.Board.Ring...)
	other.Board.Ring[0] = TreasureCellMiniGame
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cherry/treasure/"+treasureBoardV1Name(&other), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("the board for another ring = %d, want 404", rec.Code)
	}
	blank := *d
	blank.Art.Board, blank.Art.Background = "", ""
	if TreasureBoardPicture(&blank) != "" || TreasureBackgroundPicture(&blank) != "" {
		t.Fatal("a blank art name sent a picture")
	}
	if entries := calEntries(t); entries != 2 {
		t.Fatalf("cache entries = %d, want the board and the background", entries)
	}
}

// calEntries counts the cached Treasure World pictures in the current test directory.
func calEntries(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(thumbs.DpCacheDir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tw1_") {
			n++
		}
	}
	return n
}

// TestTreasureBoardV1NoStore: the pictures draw with no store (no archive files) and no cache.
func TestTreasureBoardV1NoStore(t *testing.T) {
	prevFS, prevDir := treasureArtFS, thumbs.DpCacheDir
	treasureArtFS, thumbs.DpCacheDir = nil, ""
	t.Cleanup(func() { treasureArtFS, thumbs.DpCacheDir = prevFS, prevDir })
	d := ActiveTreasureData()
	for _, name := range []string{treasureBoardV1Name(d), treasureBgV1File} {
		if b, ok := treasureSeasonPicture(name); !ok || len(b) == 0 {
			t.Fatalf("%s did not draw without a store", name)
		}
	}
}
