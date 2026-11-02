package economy

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strconv"
)

// Cherry's own Treasure World buttons (roll-gate.md section 3, treasure-spec 9.2): the QUEST button, the help button and the
// ranking board are drawn at runtime with plain geometry at the plist sizes, cached in dp-cache under a tw1_ prefix, and
// served under /cherry/treasure/ next to the original art. They are Cherry-made (AGENTS.md "Cherry-made content"). The
// colours follow the round brown back button (marble_common_btn_back.png, 88x88: an orange-brown disc with a white arrow),
// read by eye, not sampled.

// treasureDrawnFields maps SeasonInfo picture fields to the Cherry-drawn file served for them.
var treasureDrawnFields = map[uint64]string{
	2:  "rank_board.png",   // RankingBoardUrl (tag 108, plist default marble_main_point_bg.png, 250x67)
	22: "quest_button.png", // QuestButtonUrl (tag 7, plist default marble_main_quest_btn.png, 180x67)
	25: "help_button.png",  // HelperButtonUrl (tag 111, plist default marble_main_help_btn.png, 62x67)
}

// treasureDrawSpec is one drawn picture: its size, its outline and its symbol. show names what it draws (the cache key).
type treasureDrawSpec struct {
	w, h   int
	outer  func(w, h, inset float64) func(px, py float64) bool
	symbol func(w, h float64) func(px, py float64) bool
	show   string
}

var (
	treasureTop    = color.NRGBA{R: 0xDB, G: 0x91, B: 0x52, A: 0xFF}
	treasureBottom = color.NRGBA{R: 0x9C, G: 0x4F, B: 0x1A, A: 0xFF}
	treasureRim    = color.NRGBA{R: 0x5E, G: 0x2C, B: 0x0C, A: 0xFF}
	treasureInk    = color.NRGBA{R: 0xFF, G: 0xF7, B: 0xEA, A: 0xFF}
)

// treasureDrawn is the drawing table of the Cherry-drawn pictures by file name.
var treasureDrawn = map[string]treasureDrawSpec{
	"quest_button.png": {w: 180, h: 67, outer: treasurePill, symbol: treasureListSymbol, show: "quest v1 180x67 list"},
	"help_button.png":  {w: 62, h: 67, outer: treasureDisc, symbol: treasureQuestionSymbol, show: "help v1 62x67 question"},
	"rank_board.png":   {w: 250, h: 67, outer: treasurePanel, symbol: treasurePodiumSymbol, show: "rank v1 250x67 podium"},
}

// treasurePill is a rounded rectangle with full round ends, inset from the edge by inset pixels.
func treasurePill(w, h, inset float64) func(px, py float64) bool {
	r := h/2 - inset
	return func(px, py float64) bool { return roundRectInside(px, py, inset, inset, w-inset, h-inset, r) }
}

// treasurePanel is a rounded rectangle with a fixed corner radius.
func treasurePanel(w, h, inset float64) func(px, py float64) bool {
	return func(px, py float64) bool { return roundRectInside(px, py, inset, inset, w-inset, h-inset, 12-inset) }
}

// treasureDisc is the ellipse that fills the picture.
func treasureDisc(w, h, inset float64) func(px, py float64) bool {
	cx, cy, rx, ry := w/2, h/2, w/2-inset, h/2-inset
	return func(px, py float64) bool {
		dx, dy := (px-cx)/rx, (py-cy)/ry
		return dx*dx+dy*dy <= 1
	}
}

// roundRectInside reports whether (px, py) lies in the rectangle x0..x1, y0..y1 with corners of radius r.
func roundRectInside(px, py, x0, y0, x1, y1, r float64) bool {
	if px < x0 || px > x1 || py < y0 || py > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(px, x1-r))
	cy := math.Max(y0+r, math.Min(py, y1-r))
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= r*r
}

// treasureListSymbol is three bars with a dot before each: the quest list.
func treasureListSymbol(w, h float64) func(px, py float64) bool {
	bar := h * 0.06
	return func(px, py float64) bool {
		for _, f := range []float64{0.36, 0.5, 0.64} {
			cy := h * f
			if px >= w*0.42 && px <= w*0.66 && math.Abs(py-cy) <= bar/2 {
				return true
			}
			if math.Hypot(px-w*0.36, py-cy) <= bar*0.8 {
				return true
			}
		}
		return false
	}
}

// treasureQuestionRows is the question mark as a 5x7 bitmap, scaled to the picture.
var treasureQuestionRows = [7]string{
	".###.",
	"#...#",
	"....#",
	"...#.",
	"..#..",
	".....",
	"..#..",
}

// treasureQuestionSymbol draws the white question mark of the help button.
func treasureQuestionSymbol(w, h float64) func(px, py float64) bool {
	cell := h * 0.058
	gw, gh := 5*cell, 7*cell
	x0, y0 := (w-gw)/2, (h-gh)/2
	return func(px, py float64) bool {
		col, row := int(math.Floor((px-x0)/cell)), int(math.Floor((py-y0)/cell))
		if col < 0 || col >= 5 || row < 0 || row >= 7 {
			return false
		}
		return treasureQuestionRows[row][col] == '#'
	}
}

// treasurePodiumSymbol is three bars of podium heights: the ranking board.
func treasurePodiumSymbol(w, h float64) func(px, py float64) bool {
	heights := [3]float64{0.42, 0.62, 0.32} // share of the picture height, left to right
	return func(px, py float64) bool {
		for i, hf := range heights {
			x0 := w*0.38 + float64(i)*w*0.08
			if px >= x0 && px <= x0+w*0.06 && py >= h*0.8-hf*h && py <= h*0.8 {
				return true
			}
		}
		return false
	}
}

// treasureCover is the share of the pixel (x, y) that lies inside shape, sampled 4x4.
func treasureCover(x, y int, shape func(px, py float64) bool) float64 {
	hit := 0
	for sy := 0; sy < 4; sy++ {
		for sx := 0; sx < 4; sx++ {
			if shape(float64(x)+(float64(sx)+0.5)/4, float64(y)+(float64(sy)+0.5)/4) {
				hit++
			}
		}
	}
	return float64(hit) / 16
}

// treasureMix blends two colours: t is the share of b.
func treasureMix(a, b color.NRGBA, t float64) color.NRGBA {
	mix := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*(1-t) + float64(y)*t)) }
	return color.NRGBA{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: 0xFF}
}

// treasureDraw paints one picture: a vertical gradient disc with a darker rim, and the white symbol on top.
func treasureDraw(spec treasureDrawSpec) image.Image {
	w, h := float64(spec.w), float64(spec.h)
	outer, inner := spec.outer(w, h, 0), spec.outer(w, h, 3)
	symbol := spec.symbol(w, h)
	img := image.NewNRGBA(image.Rect(0, 0, spec.w, spec.h))
	for y := 0; y < spec.h; y++ {
		fill := treasureMix(treasureTop, treasureBottom, float64(y)/(h-1))
		for x := 0; x < spec.w; x++ {
			edge := treasureCover(x, y, outer)
			if edge == 0 {
				continue
			}
			body := treasureCover(x, y, inner)
			col := treasureMix(treasureRim, fill, body)
			col = treasureMix(col, treasureInk, treasureCover(x, y, symbol))
			col.A = uint8(math.Round(edge * 255))
			img.SetNRGBA(x, y, col)
		}
	}
	return img
}

// treasureDrawnPicture returns the Cherry-drawn picture named name: from dp-cache when it was drawn before, else drawn
// and cached. ok is false for a name Cherry does not draw.
func treasureDrawnPicture(name string) ([]byte, bool) {
	spec, ok := treasureDrawn[name]
	if !ok {
		return nil, false
	}
	return cherryPNG("tw1_", name[:len(name)-len(".png")], spec.show, func() image.Image { return treasureDraw(spec) })
}

// The calibration pictures (kept under their names for checks; the data does not send them by default). They are drawn for the
// lab AVD Cherry's canvas (board-art spec 5.3, case B: 640x846 px, the ring's bottom-left tile at (28, 772)): the board fills
// each square by its cell type of the active ring, numbers it and marks its token point; the background is a plain gold field.
// The show text is the cache key: change it with the drawing. The v1 art (treasure_board_art.go) is what the data sends.
const (
	treasureBoardCal = "board_cal_v1.png"
	treasureBgCal    = "bg_cal_v1.png"

	treasureCalW, treasureCalH   = 640, 846 // the board canvas
	treasureTilePx               = 73       // one square: 36.5 design points x 2
	treasureRingX, treasureRingY = 28, 772  // left of column 0 and top of row 0 (the bottom row)
	treasureRimPx                = 4        // the tile border
)

var (
	treasureCalInk   = color.NRGBA{R: 0x26, G: 0x32, B: 0x38, A: 0xFF} // the square numbers
	treasureCalRim   = color.NRGBA{R: 0x37, G: 0x47, B: 0x4F, A: 0xFF} // the tile border
	treasureCalToken = color.NRGBA{R: 0xD5, G: 0x00, B: 0xF9, A: 0xFF} // the token point
	treasureCalCross = color.NRGBA{R: 0xFF, G: 0x17, B: 0x44, A: 0xFF} // the crosshair
	treasureCalFrame = color.NRGBA{R: 0x29, G: 0x62, B: 0xFF, A: 0xFF} // the canvas frame
	treasureCalGold  = color.NRGBA{R: 0xF7, G: 0xD5, B: 0x8A, A: 0xFF} // the background (#F7D58A)
	treasureCalEdge  = color.NRGBA{R: 0xE3, G: 0xBE, B: 0x72, A: 0xFF} // the background's 20-px edge
)

// treasureCellFill is the light fill of each cell type (ring values 0 to 9): start, coin, exchange, buff, bonus, secret, trap,
// gold key, broom and mini-game.
var treasureCellFill = [10]color.NRGBA{
	{R: 0xC8, G: 0xE6, B: 0xC9, A: 0xFF},
	{R: 0xFF, G: 0xF5, B: 0x9D, A: 0xFF},
	{R: 0x90, G: 0xCA, B: 0xF9, A: 0xFF},
	{R: 0xCE, G: 0x93, B: 0xD8, A: 0xFF},
	{R: 0xFF, G: 0xCC, B: 0x80, A: 0xFF},
	{R: 0xF4, G: 0x8F, B: 0xB1, A: 0xFF},
	{R: 0xEF, G: 0x9A, B: 0x9A, A: 0xFF},
	{R: 0xFF, G: 0xD5, B: 0x4F, A: 0xFF},
	{R: 0xBC, G: 0xAA, B: 0xA4, A: 0xFF},
	{R: 0x80, G: 0xCB, B: 0xC4, A: 0xFF},
}

// treasureDigits is the digit font for the square numbers: 5x7 bitmaps, 0 to 9.
var treasureDigits = [10][7]string{
	{".###.", "#...#", "#..##", "#.#.#", "##..#", "#...#", ".###."},
	{"..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."},
	{".###.", "#...#", "....#", "...#.", "..#..", ".#...", "#####"},
	{"#####", "...#.", "..#..", "...#.", "....#", "#...#", ".###."},
	{"...#.", "..##.", ".#.#.", "#..#.", "#####", "...#.", "...#."},
	{"#####", "#....", "####.", "....#", "....#", "#...#", ".###."},
	{"..##.", ".#...", "#....", "####.", "#...#", "#...#", ".###."},
	{"#####", "....#", "...#.", "..#..", ".#...", ".#...", ".#..."},
	{".###.", "#...#", "#...#", ".###.", "#...#", "#...#", ".###."},
	{".###.", "#...#", "#...#", ".####", "....#", "...#.", ".##.."},
}

// treasureSquareCell is the tile column and row of square k on the 8x8 ring, counted from the bottom-left corner (square 0):
// the bottom edge is squares 0..7, the right 8..14, the top 15..21 and the left 22..27 (the client's own square table).
func treasureSquareCell(k int) (col, row int) {
	switch {
	case k <= 7:
		return k, 0
	case k <= 14:
		return 7, k - 7
	case k <= 21:
		return 21 - k, 7
	default:
		return 0, 28 - k
	}
}

// treasureFill sets the pixels x0..x1-1, y0..y1-1 to c (NRGBA.SetNRGBA ignores pixels outside the picture).
func treasureFill(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

// treasureNumber draws the square number n (0 to 27) centred on (cx, cy) with the digit font at 5x scale.
func treasureNumber(img *image.NRGBA, n, cx, cy int) {
	const scale, gap = 5, 5
	s := strconv.Itoa(n)
	x, y := cx-(len(s)*6*scale-gap)/2, cy-7*scale/2
	for _, ch := range s {
		for r, line := range treasureDigits[ch-'0'] {
			for c, bit := range line {
				if bit == '#' {
					treasureFill(img, x+c*scale, y+r*scale, x+(c+1)*scale, y+(r+1)*scale, treasureCalInk)
				}
			}
		}
		x += 6 * scale
	}
}

// treasureFrame is a 1-px frame on the edge of img.
func treasureFrame(img *image.NRGBA, c color.NRGBA) {
	b := img.Bounds()
	treasureFill(img, b.Min.X, b.Min.Y, b.Max.X, b.Min.Y+1, c)
	treasureFill(img, b.Min.X, b.Max.Y-1, b.Max.X, b.Max.Y, c)
	treasureFill(img, b.Min.X, b.Min.Y, b.Min.X+1, b.Max.Y, c)
	treasureFill(img, b.Max.X-1, b.Min.Y, b.Max.X, b.Max.Y, c)
}

// treasureBoardCalPicture draws the 640x846 board: 28 tiles of 73 px, each filled by its cell type of ring, numbered, with its
// token point (the token sprite's point, board-art spec 2.2: 18.5 x 7.5 design points from the tile's corner), then a
// crosshair at the canvas centre and a frame. Transparent everywhere else.
func treasureBoardCalPicture(ring []int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, treasureCalW, treasureCalH))
	for k, typ := range ring {
		col, row := treasureSquareCell(k)
		x0, y0 := treasureRingX+treasureTilePx*col, treasureRingY-treasureTilePx*row
		x1, y1 := x0+treasureTilePx, y0+treasureTilePx
		treasureFill(img, x0, y0, x1, y1, treasureCalRim)
		treasureFill(img, x0+treasureRimPx, y0+treasureRimPx, x1-treasureRimPx, y1-treasureRimPx, treasureCellFill[typ])
		treasureNumber(img, k, x0+treasureTilePx/2, y0+treasureTilePx/2)
		treasureFill(img, x0+35, y0+56, x0+40, y0+61, treasureCalToken)
	}
	cx, cy := treasureCalW/2, treasureCalH/2
	treasureFill(img, cx, 0, cx+1, treasureCalH, treasureCalCross)
	treasureFill(img, 0, cy, treasureCalW, cy+1, treasureCalCross)
	treasureFrame(img, treasureCalFrame)
	return img
}

// treasureBgCalPicture is the 640x1400 background: gold with a 20-px darker edge and a centre crosshair.
func treasureBgCalPicture() image.Image {
	const w, h, edge = 640, 1400, 20
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	treasureFill(img, 0, 0, w, h, treasureCalEdge)
	treasureFill(img, edge, edge, w-edge, h-edge, treasureCalGold)
	treasureFill(img, w/2, 0, w/2+1, h, treasureCalCross)
	treasureFill(img, 0, h/2, w, h/2+1, treasureCalCross)
	return img
}

// treasureSeasonPicture is the season picture named name, drawn from the active data (the board colours the active ring) and
// cached like the buttons. ok is false for a name Cherry does not draw.
func treasureSeasonPicture(name string) ([]byte, bool) {
	d := ActiveTreasureData()
	switch name {
	case treasureBoardCal:
		ring := d.Board.Ring
		show := fmt.Sprintf("board cal v1 640x846 ring %v", ring)
		return cherryPNG("tw1_", "board_cal_v1", show, func() image.Image { return treasureBoardCalPicture(ring) })
	case treasureBgCal:
		return cherryPNG("tw1_", "bg_cal_v1", "bg cal v1 640x1400 gold edge", treasureBgCalPicture)
	}
	return treasureSeasonPictureV1(name, d)
}
