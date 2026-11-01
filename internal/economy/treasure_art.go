package economy

import (
	"image"
	"image/color"
	"math"
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
