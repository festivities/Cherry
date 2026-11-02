package economy

import (
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"math"
	"strings"
)

// The v1 season pictures (board-art spec 5.3, case A), Cherry-made after the user's production screenshot: a gold field with
// sparkles, a gold plate behind the ring, tiles with a 3D bottom edge and one icon per cell type. Nothing here is original art.
// SkinUrl (art.board) is 640x684: the client stretches it over the 640x684 placeholder box, so the tiles sit at the design-target
// positions (ring TL (28,50), BR (612,634), square k's tile at (28 + 73 col, 561 - 73 row), 73x73). BgImgUrl (art.background) is
// 640x1400, stretched over the whole screen.
//
// Drawing: a shape is a coverage test; artPaint samples it 4x4 per pixel and composites a fill over the picture (the over
// operator, as image/draw does). Text is a 5x7 bitmap at whole-dot sizes.

const (
	treasureBoardTile  = 73  // one square: 36.5 design points x 2
	treasureBoardRingX = 28  // the ring's left edge (case A)
	treasureBoardBaseY = 561 // the top of the bottom row (the ring's bottom edge is at 634)
)

// The names art.board and art.background take in data/treasure.json (the stems), and the files they stand for. The board file
// carries the crc32 of its key (the ring and the exchange rates): the client caches a picture by its path, so a changed ring gets a
// new path. A change of the drawing needs a new stem (v2), since the key cannot see the code.
const (
	treasureBoardV1Stem  = "board_v1"
	treasureBoardCalStem = "board_cal_v1"
	treasureBgV1Stem     = "bg_v1"
	treasureBgCalStem    = "bg_cal_v1"
	treasureBgV1File     = "bg_v1.png"
)

// treasureBoardV1Key is what the v1 board shows of the data: the ring and the exchange rates. Its text is also the cache key.
func treasureBoardV1Key(d *TreasureData) string {
	return fmt.Sprintf("board v1 640x684 ring %v multiple %d tiles %v", d.Board.Ring, d.Exchange.Multiple, d.Exchange.Tiles)
}

// treasureBoardV1Name is the file name of the v1 board for d.
func treasureBoardV1Name(d *TreasureData) string {
	return fmt.Sprintf("board_v1_%08x.png", crc32.ChecksumIEEE([]byte(treasureBoardV1Key(d))))
}

// treasureBoardFile is the file art.board names for d; "" for a blank name.
func treasureBoardFile(d *TreasureData) string {
	switch d.Art.Board {
	case treasureBoardV1Stem:
		return treasureBoardV1Name(d)
	case treasureBoardCalStem:
		return treasureBoardCal
	}
	return ""
}

// treasureBgFile is the file art.background names; "" for a blank name.
func treasureBgFile(d *TreasureData) string {
	switch d.Art.Background {
	case treasureBgV1Stem:
		return treasureBgV1File
	case treasureBgCalStem:
		return treasureBgCal
	}
	return ""
}

// treasureTileOrigin is the top-left pixel of square k's tile on the 640x684 board.
func treasureTileOrigin(k int) (x, y float64) {
	col, row := treasureSquareCell(k)
	return float64(treasureBoardRingX + treasureBoardTile*col), float64(treasureBoardBaseY - treasureBoardTile*row)
}

// artShape is a region: r bounds the pixels it can cover, in tests a point in picture pixels (pixel centres at +0.5).
type artShape struct {
	r  image.Rectangle
	in func(x, y float64) bool
}

// artFill is the colour at picture point (x, y); alpha 0 leaves the picture as it is.
type artFill func(x, y float64) color.NRGBA

func artBox(x0, y0, x1, y1 float64) image.Rectangle {
	return image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1))+1, int(math.Ceil(y1))+1)
}

// artRR is a rectangle with corners of radius r.
func artRR(x0, y0, x1, y1, r float64) artShape {
	return artShape{artBox(x0, y0, x1, y1), func(x, y float64) bool { return roundRectInside(x, y, x0, y0, x1, y1, r) }}
}

// artEll is an ellipse.
func artEll(cx, cy, rx, ry float64) artShape {
	return artShape{artBox(cx-rx, cy-ry, cx+rx, cy+ry), func(x, y float64) bool {
		dx, dy := (x-cx)/rx, (y-cy)/ry
		return dx*dx+dy*dy <= 1
	}}
}

func artDisc(cx, cy, r float64) artShape { return artEll(cx, cy, r, r) }

// artSeg is a straight line of width w with round ends.
func artSeg(x0, y0, x1, y1, w float64) artShape {
	h := w / 2
	box := artBox(math.Min(x0, x1)-h, math.Min(y0, y1)-h, math.Max(x0, x1)+h, math.Max(y0, y1)+h)
	return artShape{box, func(x, y float64) bool {
		dx, dy := x1-x0, y1-y0
		t := math.Max(0, math.Min(1, ((x-x0)*dx+(y-y0)*dy)/(dx*dx+dy*dy)))
		return math.Hypot(x-x0-t*dx, y-y0-t*dy) <= h
	}}
}

// artPoly is the polygon through the points x0, y0, x1, y1, ... (even-odd rule).
func artPoly(v ...float64) artShape {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := 0; i+1 < len(v); i += 2 {
		minX, maxX = math.Min(minX, v[i]), math.Max(maxX, v[i])
		minY, maxY = math.Min(minY, v[i+1]), math.Max(maxY, v[i+1])
	}
	return artShape{artBox(minX, minY, maxX, maxY), func(x, y float64) bool {
		in, n := false, len(v)/2
		for i, j := 0, n-1; i < n; j, i = i, i+1 {
			xi, yi, xj, yj := v[2*i], v[2*i+1], v[2*j], v[2*j+1]
			if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
				in = !in
			}
		}
		return in
	}}
}

// artStar is a star of n points with outer radius r and inner radius r*inner, the first point up.
func artStar(cx, cy, r, inner float64, n int) artShape {
	v := make([]float64, 0, 4*n)
	for i := 0; i < 2*n; i++ {
		rad := r
		if i%2 == 1 {
			rad = r * inner
		}
		a := -math.Pi/2 + float64(i)*math.Pi/float64(n)
		v = append(v, cx+rad*math.Cos(a), cy+rad*math.Sin(a))
	}
	return artPoly(v...)
}

// artSeal is a wax seal of radius r with a scalloped edge.
func artSeal(cx, cy, r float64) artShape {
	return artShape{artBox(cx-r-2, cy-r-2, cx+r+2, cy+r+2), func(x, y float64) bool {
		dx, dy := x-cx, y-cy
		return math.Hypot(dx, dy) <= r+1.2*math.Cos(10*math.Atan2(dy, dx))
	}}
}

// artOr is the union of the shapes.
func artOr(s ...artShape) artShape {
	r := s[0].r
	for _, o := range s[1:] {
		r = r.Union(o.r)
	}
	return artShape{r, func(x, y float64) bool {
		for _, o := range s {
			if o.in(x, y) {
				return true
			}
		}
		return false
	}}
}

// artMinus is a without b.
func artMinus(a, b artShape) artShape {
	return artShape{a.r, func(x, y float64) bool { return a.in(x, y) && !b.in(x, y) }}
}

// artAnd is the part of a that lies in b.
func artAnd(a, b artShape) artShape {
	return artShape{a.r.Intersect(b.r), func(x, y float64) bool { return a.in(x, y) && b.in(x, y) }}
}

// artMove shifts s by whole pixels.
func artMove(s artShape, dx, dy int) artShape {
	fx, fy := float64(dx), float64(dy)
	return artShape{s.r.Add(image.Pt(dx, dy)), func(x, y float64) bool { return s.in(x-fx, y-fy) }}
}

// artPaint composites fill over img where s covers a pixel: the coverage is the share of 4x4 samples inside s, and the fill
// is taken at the pixel centre.
func artPaint(img *image.NRGBA, s artShape, fill artFill) {
	r := s.r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cov := treasureCover(x, y, s.in)
			if cov == 0 {
				continue
			}
			src := fill(float64(x)+0.5, float64(y)+0.5)
			a := float64(src.A) / 255 * cov
			dst := img.NRGBAAt(x, y)
			da := float64(dst.A) / 255
			out := a + da*(1-a)
			if out == 0 {
				continue
			}
			mix := func(sc, dc uint8) uint8 {
				return uint8(math.Round((float64(sc)*a + float64(dc)*da*(1-a)) / out))
			}
			img.SetNRGBA(x, y, color.NRGBA{R: mix(src.R, dst.R), G: mix(src.G, dst.G), B: mix(src.B, dst.B), A: uint8(math.Round(out * 255))})
		}
	}
}

// artHex is the colour 0xRRGGBB with alpha a; artC is opaque; artA is 0xRRGGBB with alpha a.
func artHex(v uint32, a uint8) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: a}
}

func artC(v uint32) color.NRGBA { return artHex(v, 0xFF) }

func artA(v uint32, a uint8) color.NRGBA { return artHex(v, a) }

// artMix blends a and b (t is the share of b), alpha included.
func artMix(a, b color.NRGBA, t float64) color.NRGBA {
	l := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*(1-t) + float64(y)*t)) }
	return color.NRGBA{R: l(a.R, b.R), G: l(a.G, b.G), B: l(a.B, b.B), A: l(a.A, b.A)}
}

func artSolid(c color.NRGBA) artFill { return func(_, _ float64) color.NRGBA { return c } }

// artVGrad is a vertical gradient from top at y0 to bottom at y1.
func artVGrad(y0, y1 float64, top, bottom color.NRGBA) artFill {
	return func(_, y float64) color.NRGBA {
		return artMix(top, bottom, math.Max(0, math.Min(1, (y-y0)/(y1-y0))))
	}
}

// artGlow is a soft light centred on (cx, cy), fading to nothing at radius r.
func artGlow(cx, cy, r float64, v uint32, alpha float64) artFill {
	return func(x, y float64) color.NRGBA {
		t := math.Min(1, math.Hypot(x-cx, y-cy)/r)
		return artHex(v, uint8(alpha*(1-t)*(1-t)))
	}
}

// artHatch is a diagonal lattice of colour c, lines every period pixels; between the lines the fill is transparent.
func artHatch(c color.NRGBA, period float64) artFill {
	wrap := func(v float64) float64 { return math.Mod(math.Mod(v, period)+period, period) }
	return func(x, y float64) color.NRGBA {
		if wrap(x+y) < 1.2 || wrap(x-y) < 1.2 {
			return c
		}
		return color.NRGBA{}
	}
}

// artLCG is a fixed pseudo-random sequence, so the background is the same picture on every render.
type artLCG uint32

// next returns the next value in [0, 1).
func (r *artLCG) next() float64 {
	*r = *r*1664525 + 1013904223
	return float64(*r>>8) / float64(1<<24)
}

// artFont is the 5x7 bitmap of the letters the pictures print (the square numbers are not printed on the v1 board).
var artFont = map[rune][7]string{
	'A': {".###.", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'B': {"####.", "#...#", "#...#", "####.", "#...#", "#...#", "####."},
	'D': {"####.", "#...#", "#...#", "#...#", "#...#", "#...#", "####."},
	'E': {"#####", "#....", "#....", "####.", "#....", "#....", "#####"},
	'L': {"#....", "#....", "#....", "#....", "#....", "#....", "#####"},
	'N': {"#...#", "##..#", "#.#.#", "#..##", "#...#", "#...#", "#...#"},
	'O': {".###.", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."},
	'R': {"####.", "#...#", "#...#", "####.", "#.#..", "#..#.", "#...#"},
	'S': {".####", "#....", "#....", ".###.", "....#", "....#", "####."},
	'T': {"#####", "..#..", "..#..", "..#..", "..#..", "..#..", "..#.."},
	'U': {"#...#", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."},
	'W': {"#...#", "#...#", "#...#", "#.#.#", "#.#.#", "##.##", "#...#"},
	'?': {".###.", "#...#", "....#", "...#.", "..#..", ".....", "..#.."},
}

// artTextWidth is the width of s in dots of d px (one dot between the 5-dot letters, a blank letter for a space).
func artTextWidth(s string, d int) int { return len([]rune(s))*6*d - d }

// artText prints s with its top at y, centred on cx, in dots of d px.
func artText(img *image.NRGBA, s string, cx, y, d int, c color.NRGBA) {
	x := cx - artTextWidth(s, d)/2
	for _, ch := range s {
		for r, row := range artFont[ch] {
			for col := range row {
				if row[col] == '#' {
					treasureFill(img, x+col*d, y+r*d, x+(col+1)*d, y+(r+1)*d, c)
				}
			}
		}
		x += 6 * d
	}
}

// artTextOutline prints s in fill with a 2-px outline of edge.
func artTextOutline(img *image.NRGBA, s string, cx, y, d int, fill, edge color.NRGBA) {
	for _, o := range [][2]int{{-2, 0}, {2, 0}, {0, -2}, {0, 2}} {
		artText(img, s, cx+o[0], y+o[1], d, edge)
	}
	artText(img, s, cx, y, d, fill)
}

// artFaces is each cell type's tile: the gradient top and bottom and the 3D bottom edge.
var artFaces = map[int]struct{ top, bottom, edge uint32 }{
	TreasureCellStart:    {0x5FBFB6, 0x2E8F88, 0x1D5F5A},
	TreasureCellCoin:     {0xA8DCFA, 0x6DB4EE, 0x3F7FC4},
	TreasureCellExchange: {0x7B4BB0, 0x3F2170, 0x211040},
	TreasureCellBuff:     {0xFFA3CB, 0xEE5B93, 0xA9335E},
	TreasureCellBonus:    {0xFFE77F, 0xF5B53F, 0xB8741C},
	TreasureCellSpecial:  {0xFFF9EC, 0xF1DBAA, 0xC7A064},
	TreasureCellTrap:     {0x7C8491, 0x3A414B, 0x1C2129},
	TreasureCellGoldKey:  {0xA86A3A, 0x5C3118, 0x33180A},
	TreasureCellTravel:   {0xE6F6FF, 0xB5DDF5, 0x7DB3D9},
	TreasureCellMiniGame: {0xCDB8FF, 0x9B7EE0, 0x5E45A8},
}

// artTile draws square k's tile at its origin: the face, the rim highlight and the icon of cell type typ. stars and question
// are the exchange rate of an exchange square (1, 2 or 3 stars; a gamble shows 3 stars and a question mark).
func artTile(img *image.NRGBA, k, typ, stars int, question bool) {
	x0, y0 := treasureTileOrigin(k)
	f := artFaces[typ]
	x1, y1 := x0+treasureBoardTile-1, y0+treasureBoardTile-1
	face := artRR(x0+1, y0+1, x1, y1-6, 10)
	artPaint(img, artRR(x0+1, y0+1, x1, y1, 10), artSolid(artC(f.edge)))
	artPaint(img, face, artVGrad(y0, y1-6, artC(f.top), artC(f.bottom)))
	artPaint(img, artMinus(artRR(x0+3, y0+3, x1-2, y1-8, 8), artRR(x0+4, y0+4, x1-3, y1-9, 7)), artSolid(artA(0xFFFFFF, 110)))
	switch typ {
	case TreasureCellStart:
		artIconStart(img, x0, y0)
	case TreasureCellCoin:
		artIconCoin(img, x0, y0)
	case TreasureCellExchange:
		artPaint(img, face, artHatch(artA(0xFFFFFF, 26), 12))
		artIconExchange(img, x0, y0, stars, question)
	case TreasureCellBuff:
		artIconBuff(img, x0, y0)
	case TreasureCellBonus:
		artIconBonus(img, x0, y0)
	case TreasureCellSpecial:
		artIconSecret(img, x0, y0)
	case TreasureCellTrap:
		artIconTrap(img, x0, y0)
	case TreasureCellGoldKey:
		artPaint(img, face, artHatch(artA(0xFFD9A0, 30), 12))
		artIconKey(img, x0, y0)
	case TreasureCellTravel:
		artIconBroom(img, x0, y0)
	case TreasureCellMiniGame:
		artIconDrums(img, x0, y0)
	}
}

// The icons are drawn in the upper part of the tile (the token point sits at the bottom edge); cx, cy is the icon centre.

func artIconStart(img *image.NRGBA, x0, y0 float64) {
	cx, cy := x0+36.5, y0+27
	artPaint(img, artEll(cx, cy+16, 21, 3.5), artSolid(artA(0x1D5F5A, 110)))
	artPaint(img, artOr(artRR(cx-19, cy-1, cx+19, cy+14, 3), artRR(cx-19, cy-12, cx+19, cy+3, 9)), artSolid(artC(0xB98A2E)))
	artPaint(img, artOr(artRR(cx-17, cy+1, cx+17, cy+12, 2), artRR(cx-17, cy-10, cx+17, cy+1, 7)), artVGrad(cy-10, cy+12, artC(0xFFFFFF), artC(0xF1E6CC)))
	for _, bx := range []float64{-10, 10} {
		artPaint(img, artRR(cx+bx-2, cy-12, cx+bx+2, cy+14, 0), artSolid(artC(0xE9B43C)))
	}
	artPaint(img, artRR(cx-4.5, cy-1, cx+4.5, cy+7, 2), artSolid(artC(0xE9B43C)))
	artPaint(img, artDisc(cx, cy+2, 1.3), artSolid(artC(0x7A5A1A)))
	artPaint(img, artDisc(cx+14, cy+12, 5), artSolid(artC(0xFFD54A)))
}

func artIconCoin(img *image.NRGBA, x0, y0 float64) {
	cx, cy := x0+36.5, y0+27
	artPaint(img, artMove(artDisc(cx, cy, 16), 1, 3), artSolid(artA(0x1F4E8C, 120)))
	artPaint(img, artDisc(cx, cy, 16), artSolid(artC(0xB9781A)))
	artPaint(img, artDisc(cx, cy, 14.5), artVGrad(cy-14.5, cy+14.5, artC(0xFFF0A6), artC(0xE9A92E)))
	artPaint(img, artMinus(artDisc(cx, cy, 11), artDisc(cx, cy, 9.8)), artSolid(artC(0xC98A1E)))
	artPaint(img, artPoly(cx, cy-6, cx+5, cy, cx, cy+6, cx-5, cy), artSolid(artC(0xFFF8D6)))
}

func artIconBonus(img *image.NRGBA, x0, y0 float64) {
	cx, y := int(x0)+36, int(y0)+23
	artText(img, "BONUS", cx+1, y+1, 2, artC(0xFFF4B8))
	artText(img, "BONUS", cx, y, 2, artC(0x9E3A10))
	artPaint(img, artRR(x0+22, y0+43, x0+51, y0+45, 1), artSolid(artC(0x9E3A10)))
}

// artIconExchange is a trophy cup with the exchange rate: one gold star per 1x, 2x or 3x; a gamble shows 3 stars and a "?".
func artIconExchange(img *image.NRGBA, x0, y0 float64, stars int, question bool) {
	cx, cy := x0+36.5, y0+24
	cup := artOr(
		artAnd(artEll(cx, cy-8, 11, 9), artRR(cx-12, cy-8, cx+12, cy+4, 0)),
		artRR(cx-13, cy-11, cx+13, cy-7, 2),
		artMinus(artEll(cx-11, cy-4, 4.5, 4.5), artEll(cx-11, cy-4, 2.6, 2.6)),
		artMinus(artEll(cx+11, cy-4, 4.5, 4.5), artEll(cx+11, cy-4, 2.6, 2.6)),
		artRR(cx-2.5, cy+4, cx+2.5, cy+10, 0),
		artRR(cx-8, cy+9, cx+8, cy+14, 2),
	)
	artPaint(img, artMove(cup, 1, 2), artSolid(artC(0x7A4E0C)))
	artPaint(img, cup, artVGrad(cy-11, cy+14, artC(0xFFE27A), artC(0xD99A1E)))
	artPaint(img, artDisc(cx-5, cy-5, 2.2), artSolid(artA(0xFFFFFF, 200)))
	for i := 0; i < stars; i++ {
		sx := cx + (float64(i)-float64(stars-1)/2)*12
		artPaint(img, artStar(sx, y0+46, 4.8, 0.45, 5), artSolid(artC(0xFFE14D)))
	}
	if question {
		artText(img, "?", int(x0)+60, int(y0)+7, 3, artC(0xFFE14D))
	}
}

func artIconBuff(img *image.NRGBA, x0, y0 float64) {
	cx, cy := x0+36.5, y0+27
	arrow := artOr(artPoly(cx-16, cy+2, cx, cy-15, cx+16, cy+2), artRR(cx-7, cy+1, cx+7, cy+17, 2))
	artPaint(img, artMove(arrow, 0, 2), artSolid(artC(0xA8670C)))
	artPaint(img, arrow, artVGrad(cy-15, cy+17, artC(0xFFE768), artC(0xF5B81E)))
}

// artIconSecret is a red wax seal with a gold S and two ribbon tails.
func artIconSecret(img *image.NRGBA, x0, y0 float64) {
	cx, cy := x0+36.5, y0+27
	artPaint(img, artOr(artPoly(cx-7, cy+6, cx-1, cy+6, cx-5, cy+18), artPoly(cx+7, cy+6, cx+1, cy+6, cx+5, cy+18)), artSolid(artC(0x8E1B1B)))
	artPaint(img, artSeal(cx, cy-1, 13), artVGrad(cy-14, cy+12, artC(0xF2584F), artC(0xB3261E)))
	artPaint(img, artMinus(artDisc(cx, cy-1, 10.5), artDisc(cx, cy-1, 9)), artSolid(artC(0xF5C542)))
	artText(img, "S", int(cx), int(cy)-8, 2, artC(0xF5C542))
}

// artIconKey is a gold key with a pink jewel in its bow and a pair of wings.
func artIconKey(img *image.NRGBA, x0, y0 float64) {
	cx, cy := x0+36.5, y0+27
	bx, by := cx-7, cy-7
	wings := artOr(
		artPoly(bx-4, by-3, bx-14, by-14, bx-22, by-12, bx-17, by-4, bx-8, by+1),
		artPoly(bx+4, by-3, bx+14, by-14, bx+22, by-12, bx+17, by-4, bx+8, by+1),
	)
	artPaint(img, wings, artSolid(artC(0xFCE7F3)))
	key := artOr(
		artSeg(bx+6.4, by+6.4, cx+14, cy+14, 4.5),
		artSeg(cx+7.4, cy+7.4, cx+3.2, cy+11.6, 3.2),
		artSeg(cx+11, cy+11, cx+6.8, cy+15.2, 3.2),
		artMinus(artDisc(bx, by, 9), artDisc(bx, by, 5.5)),
	)
	artPaint(img, artMove(key, 1, 2), artSolid(artC(0x7A4A12)))
	artPaint(img, key, artSolid(artC(0xFFD54A)))
	artPaint(img, artDisc(bx, by, 3.6), artSolid(artC(0xFF5C9A)))
	artPaint(img, artDisc(bx-1, by-1.2, 1.1), artSolid(artC(0xFFE0EE)))
}

// artIconBroom is a broom on a white cloud, with a blue ribbon on the handle.
func artIconBroom(img *image.NRGBA, x0, y0 float64) {
	cx, cy := x0+36.5, y0+27
	cloud := artOr(artEll(cx-12, cy+8, 8, 8), artEll(cx, cy+4, 11, 11), artEll(cx+12, cy+8, 8, 8), artRR(cx-20, cy+8, cx+20, cy+16, 4))
	artPaint(img, artMove(cloud, 0, 2), artSolid(artA(0x5F8FB8, 120)))
	artPaint(img, cloud, artSolid(artC(0xFFFFFF)))
	sx, sy, ex, ey := cx+13, cy-16, cx-6, cy+5
	ux, uy := ex-sx, ey-sy
	l := math.Hypot(ux, uy)
	ux, uy = ux/l, uy/l
	nx, ny := -uy, ux
	fx, fy := ex+8*ux, ey+8*uy
	head := artPoly(ex+4*nx, ey+4*ny, fx+10*nx, fy+10*ny, fx-10*nx, fy-10*ny, ex-4*nx, ey-4*ny)
	stick := artSeg(sx, sy, ex, ey, 3.5)
	artPaint(img, artMove(artOr(stick, head), 1, 2), artSolid(artA(0x3E2A14, 120)))
	artPaint(img, stick, artSolid(artC(0x9A6434)))
	artPaint(img, head, artVGrad(ey, fy+10, artC(0xF2C35A), artC(0xC98A24)))
	artPaint(img, artDisc(sx+0.35*(ex-sx), sy+0.35*(ey-sy), 3), artSolid(artC(0x3B82F6)))
}

// artIconDrums is a pair of drums with their sticks (the mini-game corner).
func artIconDrums(img *image.NRGBA, x0, y0 float64) {
	cx, cy := x0+36.5, y0+27
	drum := func(x, y float64, top, bottom uint32) {
		artPaint(img, artRR(x-11, y, x+11, y+13, 3), artVGrad(y, y+13, artC(top), artC(bottom)))
		artPaint(img, artRR(x-11, y+5, x+11, y+7.5, 0), artSolid(artC(0xFFD54A)))
		artPaint(img, artEll(x, y, 11, 4), artSolid(artC(0xFFF4E0)))
		artPaint(img, artMinus(artEll(x, y, 11, 4), artEll(x, y, 9, 2.6)), artSolid(artC(0xC9A66B)))
	}
	drum(cx-10, cy+1, 0xF0524A, 0xB3261E)
	drum(cx+10, cy+4, 0x4DA3FF, 0x1E5FB8)
	artPaint(img, artSeg(cx-15, cy-17, cx-8, cy-2, 2.6), artSolid(artC(0xFFD54A)))
	artPaint(img, artSeg(cx+16, cy-17, cx+9, cy-1, 2.6), artSolid(artC(0xFFD54A)))
}

// artIconTrap is a grey skull behind a cage, with a torch on each side.
func artIconTrap(img *image.NRGBA, x0, y0 float64) {
	cx, cy := x0+36.5, y0+27
	dark := artSolid(artC(0x1F262E))
	for _, side := range []float64{-25, 25} {
		tx := cx + side
		artPaint(img, artRR(tx-1.6, cy-2, tx+1.6, cy+12, 1), artSolid(artC(0x6D4C2F)))
		artPaint(img, artPoly(tx-5, cy-2, tx-6.5, cy-9, tx-2, cy-7, tx, cy-16, tx+2, cy-8, tx+6, cy-10, tx+5, cy-2), artVGrad(cy-16, cy-2, artC(0xFFD54A), artC(0xFF6A1A)))
		artPaint(img, artDisc(tx, cy-5, 2), artSolid(artC(0xFFF3B0)))
	}
	artPaint(img, artOr(artDisc(cx, cy-4, 12), artRR(cx-7, cy+4, cx+7, cy+12, 2.5)), artVGrad(cy-16, cy+12, artC(0xF4F6F8), artC(0xC3CAD2)))
	artPaint(img, artOr(artDisc(cx-4.5, cy-3, 3.3), artDisc(cx+4.5, cy-3, 3.3)), dark)
	artPaint(img, artPoly(cx-1.8, cy+2, cx+1.8, cy+2, cx, cy+4.6), dark)
	for _, tx := range []float64{-3.5, 0, 3.5} {
		artPaint(img, artSeg(cx+tx, cy+6, cx+tx, cy+11, 1.2), dark)
	}
	bars := []artShape{artRR(cx-14, cy-17.5, cx+14, cy-15, 1)}
	for _, bx := range []float64{-13, -6.5, 6.5, 13} {
		bars = append(bars, artRR(cx+bx-1.1, cy-17, cx+bx+1.1, cy+14, 1))
	}
	artPaint(img, artMove(artOr(bars...), 0, 1), artSolid(artA(0x1F262E, 150)))
	artPaint(img, artOr(bars...), artSolid(artC(0xD5DCE2)))
}

// treasureBoardV1Picture draws the 640x684 board for d: the gold plate, the centre platform (the client draws the die on it)
// with the title, then the 28 tiles from the ring and the exchange rates.
func treasureBoardV1Picture(d *TreasureData) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 640, 684))
	artPaint(img, artRR(10, 22, 630, 668, 50), artSolid(artC(0xB66F1E)))
	artPaint(img, artRR(10, 20, 630, 656, 50), artVGrad(20, 656, artC(0xFFE9A3), artC(0xF2B54A)))
	artPaint(img, artMinus(artRR(17, 27, 623, 649, 44), artRR(22, 32, 618, 644, 39)), artSolid(artA(0xFFF8DA, 170)))
	artPaint(img, artEll(320, 344, 168, 90), artSolid(artC(0xB9782A)))
	artPaint(img, artEll(320, 336, 166, 88), artVGrad(248, 424, artC(0xFFEFB8), artC(0xF0BE5C)))
	artPaint(img, artEll(320, 336, 146, 74), artVGrad(262, 410, artA(0xFFF6D0, 200), artA(0xFFDE8A, 200)))
	for k, typ := range d.Board.Ring {
		stars, question := 0, false
		if typ == TreasureCellExchange {
			times, gamble := d.Exchange.tile(k)
			stars, question = min(3, int(times)), gamble > 0
			if question {
				stars = 3
			}
		}
		artTile(img, k, typ, stars, question)
	}
	artTextOutline(img, "TREASURE WORLD", 320, 452, 3, artC(0xE9712A), artC(0xFFF6DE))
	return img
}

// treasureBgV1Picture is the 640x1400 background: a gold field with a warm glow, faint bokeh, sparkles and a lighter panel
// where the board sits.
func treasureBgV1Picture() image.Image {
	const w, h = 640, 1400
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	artPaint(img, artRR(0, 0, w, h, 0), artVGrad(0, h, artC(0xFFF2C2), artC(0xF7BE55)))
	artPaint(img, artDisc(320, 700, 560), artGlow(320, 700, 560, 0xFFFBE6, 150))
	rnd := artLCG(7)
	for i := 0; i < 14; i++ {
		x, y, r := rnd.next()*w, rnd.next()*h, 30+rnd.next()*90
		artPaint(img, artDisc(x, y, r), artSolid(artA(0xFFFFFF, 26)))
	}
	artPaint(img, artRR(22, 340, 618, 1084, 40), artSolid(artA(0xC98A2E, 90)))
	artPaint(img, artRR(18, 326, 622, 1070, 40), artVGrad(326, 1070, artA(0xFFF9E4, 235), artA(0xFFEDB8, 235)))
	artPaint(img, artMinus(artRR(26, 334, 614, 1062, 34), artRR(31, 339, 609, 1057, 29)), artSolid(artA(0xFFFFFF, 150)))
	for i := 0; i < 80; i++ {
		x, y, r := rnd.next()*w, rnd.next()*h, 3+rnd.next()*9
		artPaint(img, artStar(x, y, r, 0.22, 4), artSolid(artA(0xFFFFFF, 235)))
	}
	return img
}

// treasureBgV1Show is the cache key of the v1 background's drawing (the board's key is treasureBoardV1Key).
const treasureBgV1Show = "bg v1 640x1400 gold field sparkles panel"

// treasureSeasonPictureV1 serves the v1 pictures: the background, and the board for the active data (by its file name).
func treasureSeasonPictureV1(name string, d *TreasureData) ([]byte, bool) {
	switch name {
	case treasureBgV1File:
		return cherryPNG("tw1_", "bg_v1", treasureBgV1Show, treasureBgV1Picture)
	case treasureBoardV1Name(d):
		return cherryPNG("tw1_", strings.TrimSuffix(name, ".png"), treasureBoardV1Key(d), func() image.Image { return treasureBoardV1Picture(d) })
	}
	return nil, false
}
