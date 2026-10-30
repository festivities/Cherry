package economy

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cherry/internal/thumbs"
)

// Cherry's Lucky and Jackpot Spin pictures (spin-spec 6.1 and 12.4). The wheel faces are drawn from each tier's prizes,
// the panels are plain gradients, and the Gem icon is the client's own UI art, read in place from the archive
// (attendskin.go's attendIconFS). Everything is served under /cherry/spin/ and generated at runtime; the wheel and panel
// PNGs are cached in dp-cache under an sp1_ prefix and a checksum of what they draw, so an edited spin.json draws anew.

// spinSlotBaseDeg is the clockwise angle, from the top of the wheel picture, of slot 1's centre. The client stops slot p
// with the wheel turned (9-p)*45-22.5 degrees clockwise (spin-spec 6), so with the arrow at the top (device 2026-10-30:
// 12 o'clock, slot 3 stopped opposite a 202.5 base) slot p's centre must sit at this base plus 45*(p-1).
const spinSlotBaseDeg = 22.5

const (
	spinWheelSize    = 320 // design units of IMG_WHEEL (spin-spec 12.4)
	spinIconSize     = 40  // a prize's picture on the wheel
	spinCacheVersion = "sp1_"
)

// spinSlotCentreDeg is the clockwise angle from the top to slot p's centre (p is 1 to 8).
func spinSlotCentreDeg(p int) float64 { return spinSlotBaseDeg + 45*float64(p-1) }

// spinSectorAt returns the slot (1 to 8) whose 45-degree sector holds the clockwise angle deg from the top.
func spinSectorAt(deg float64) int {
	start := spinSlotBaseDeg - 22.5
	off := math.Mod(math.Mod(deg-start, 360)+360, 360)
	return int(off/45) + 1
}

func rgbHex(hex uint32) color.NRGBA {
	return color.NRGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: 255}
}

// spinWheel draws a wheel face: eight sectors in two alternating colours, a rim and a hub, and each slot's prize at its
// centre (an item's own thumbnail, or the Gem icon with its amount).
func spinWheel(slots []SpinSlot, colours [2]color.NRGBA) *image.NRGBA {
	const size = spinWheelSize
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := float64(size) / 2
	radius := c - 2
	rim, hub := rgbHex(0x2B1B0E), rgbHex(0xF9D35C)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-c, float64(y)+0.5-c
			d := math.Hypot(dx, dy)
			if d > radius {
				continue
			}
			deg := math.Mod(math.Atan2(dx, -dy)*180/math.Pi+360, 360)
			col := colours[0]
			if spinSectorAt(deg)%2 == 0 {
				col = colours[1]
			}
			switch {
			case d > radius-5:
				col = rim
			case d < 24:
				col = hub
			}
			img.SetNRGBA(x, y, col)
		}
	}
	gem, hasGem := spinGemIcon()
	for p := 1; p <= len(slots); p++ {
		rad := spinSlotCentreDeg(p) * math.Pi / 180
		s := slots[p-1]
		ix, iy := c+0.42*radius*math.Sin(rad), c-0.42*radius*math.Cos(rad) // inside the arrow's tip, so a stopped prize shows
		var icon image.Image
		if s.Item != "" {
			if thumb, ok := itemDPImage(s.Item); ok {
				icon = thumb
			}
		} else if hasGem {
			icon = gem
		}
		if icon != nil {
			spinPaste(img, spinScale(icon, spinIconSize), int(ix), int(iy))
		}
		if s.Item == "" {
			tx, ty := c+0.82*radius*math.Sin(rad), c-0.82*radius*math.Cos(rad)
			spinDrawNumber(img, s.Gems, int(tx), int(ty))
		}
	}
	return img
}

// spinGemIcon is the client's Gem icon (original UI art, read in place).
func spinGemIcon() (image.Image, bool) {
	b, err := fs.ReadFile(attendIconFS, "gachaplay_deco_gem.png")
	if err != nil {
		return nil, false
	}
	img, err := png.Decode(bytes.NewReader(b))
	return img, err == nil
}

// spinScale resizes src to size x size by nearest neighbour.
func spinScale(src image.Image, size int) *image.NRGBA {
	b := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			out.Set(x, y, src.At(b.Min.X+x*b.Dx()/size, b.Min.Y+y*b.Dy()/size))
		}
	}
	return out
}

// spinPaste draws src over dst, centred on (x, y).
func spinPaste(dst *image.NRGBA, src image.Image, x, y int) {
	b := src.Bounds()
	r := image.Rect(x-b.Dx()/2, y-b.Dy()/2, x-b.Dx()/2+b.Dx(), y-b.Dy()/2+b.Dy())
	draw.Draw(dst, r, src, b.Min, draw.Over)
}

// spinFont is a 3x5 bitmap of the digits 0 to 9 ('1' is a lit cell). It is the only text the pictures draw.
var spinFont = [10][5]string{
	{"111", "101", "101", "101", "111"},
	{"010", "110", "010", "010", "111"},
	{"111", "001", "111", "100", "111"},
	{"111", "001", "111", "001", "111"},
	{"101", "101", "111", "001", "001"},
	{"111", "100", "111", "001", "111"},
	{"111", "100", "111", "101", "111"},
	{"111", "001", "010", "010", "010"},
	{"111", "101", "111", "101", "111"},
	{"111", "101", "111", "001", "111"},
}

// spinDrawNumber writes n centred on (cx, cy): each font cell is 3 pixels square, digits 9 pixels wide with a 3-pixel gap,
// white over a dark shadow.
func spinDrawNumber(img *image.NRGBA, n int64, cx, cy int) {
	const cell, advance = 3, 12
	s := strconv.FormatInt(n, 10)
	x0 := cx - (len(s)*advance-cell)/2
	y0 := cy - 7
	for _, pass := range []struct {
		dx, dy int
		col    color.NRGBA
	}{{2, 2, color.NRGBA{A: 255}}, {0, 0, color.NRGBA{R: 255, G: 255, B: 255, A: 255}}} {
		for i, ch := range s {
			for row, bits := range spinFont[ch-'0'] {
				for col, bit := range bits {
					if bit != '1' {
						continue
					}
					px, py := x0+i*advance+col*cell+pass.dx, y0+row*cell+pass.dy
					for yy := py; yy < py+cell; yy++ {
						for xx := px; xx < px+cell; xx++ {
							if (image.Point{X: xx, Y: yy}).In(img.Bounds()) {
								img.SetNRGBA(xx, yy, pass.col)
							}
						}
					}
				}
			}
		}
	}
}

// spinPanel is a plain vertical gradient from top to bottom.
func spinPanel(w, h int, top, bottom color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		t := float64(y) / float64(max(h-1, 1))
		col := color.NRGBA{R: lerpByte(top.R, bottom.R, t), G: lerpByte(top.G, bottom.G, t), B: lerpByte(top.B, bottom.B, t), A: lerpByte(top.A, bottom.A, t)}
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, col)
		}
	}
	return img
}

func lerpByte(a, b uint8, t float64) uint8 {
	return uint8(float64(a) + (float64(b)-float64(a))*t + 0.5)
}

// spinPNG returns the PNG named stem: from dp-cache when a copy of the same content is there, else drawn and cached. The
// key is the stem and what the picture shows, so changed prizes draw anew. A failed cache write still serves the picture.
func spinPNG(stem, show string, draw func() image.Image) ([]byte, bool) {
	var path string
	if thumbs.DpCacheDir != "" {
		path = filepath.Join(thumbs.DpCacheDir, fmt.Sprintf("%s%s_%08x.png", spinCacheVersion, stem, crc32.ChecksumIEEE([]byte(show))))
		if b, err := os.ReadFile(path); err == nil {
			return b, true
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, draw()); err != nil {
		return nil, false
	}
	if path != "" && os.MkdirAll(thumbs.DpCacheDir, 0o755) == nil {
		_ = os.WriteFile(path, buf.Bytes(), 0o644)
	}
	return buf.Bytes(), true
}

// spinWheelPNG is a tier's wheel face.
func spinWheelPNG(stem string, t SpinTier, colours [2]color.NRGBA) ([]byte, bool) {
	show := fmt.Sprint(colours) + spinWheelShow(t)
	return spinPNG(stem, show, func() image.Image { return spinWheel(t.Slots, colours) })
}

// spinWheelShow is what decides where a wheel's prizes sit.
func spinWheelShow(t SpinTier) string {
	show := fmt.Sprint(spinSlotBaseDeg)
	for _, s := range t.Slots {
		show += fmt.Sprintf(" %d/%s", s.Gems, s.Item)
	}
	return show
}

// spinWheelName is a wheel's file name. The client keeps a picture by its path and never fetches it again, so the name
// carries a checksum of the prizes: an old cached face would stop the arrow on the wrong prize.
func spinWheelName(stem string, t SpinTier) string {
	return fmt.Sprintf("%s-%08x.png", stem, crc32.ChecksumIEEE([]byte(spinWheelShow(t))))
}

// spinPanelPNG is a plain gradient panel of w x h.
func spinPanelPNG(stem string, w, h int, top, bottom color.NRGBA) ([]byte, bool) {
	show := fmt.Sprint(w, h, top, bottom)
	return spinPNG(stem, show, func() image.Image { return spinPanel(w, h, top, bottom) })
}

// spinArtPNG is an original UI picture, read in place from the archive (attendIconFS).
func spinArtPNG(file string) ([]byte, bool) {
	b, err := fs.ReadFile(attendIconFS, file)
	return b, err == nil
}

// spinPicture returns the picture file name under /cherry/spin/. Unknown names are not Cherry's.
func spinPicture(name string) ([]byte, bool) {
	sp := ActiveSpin()
	stem, _, _ := strings.Cut(strings.TrimSuffix(name, ".png"), "-") // a wheel's name carries a checksum (spinWheelName)
	red := [2]color.NRGBA{rgbHex(0xD7263D), rgbHex(0xFFF1C1)}
	purple := [2]color.NRGBA{rgbHex(0x6A3DC8), rgbHex(0xB897F5)}
	blue := [2]color.NRGBA{rgbHex(0x1F6FD1), rgbHex(0x9CD3FF)}
	switch stem {
	case "lucky_wheel":
		return spinWheelPNG(stem, sp.Lucky.Tiers[0], red)
	case "jackpot_gem_wheel":
		return spinWheelPNG(stem, sp.Jackpot.Tiers[0], purple)
	case "jackpot_cash_wheel":
		return spinWheelPNG(stem, sp.Jackpot.Tiers[1], blue)
	case "board":
		return spinPanelPNG(stem, 320, 690, rgbHex(0x3B2A6B), rgbHex(0x1B1640))
	case "deco":
		// L_DECO_BG draws over the wheel and arrow (zOrder 4 against L_SPIN's 1, luckyspin01.plist), so it stays clear.
		return spinPanelPNG(stem, 320, 690, color.NRGBA{}, color.NRGBA{})
	case "list_bg":
		return spinPanelPNG(stem, 320, 660, rgbHex(0x2A2250), rgbHex(0x17142E))
	case "detail_cover":
		shade := color.NRGBA{A: 150}
		return spinPanelPNG(stem, 320, 660, shade, shade)
	case "title_clear":
		// The original strip carries a title in its art; Cherry has none, and an empty bar reads as broken.
		return spinPanelPNG(stem, 120, 20, color.NRGBA{}, color.NRGBA{})
	case "gem":
		return spinArtPNG("gachaplay_deco_gem.png")
	case "cash":
		return spinArtPNG("jackpot_spin_cash.png")
	}
	return nil, false
}

// HandleSpinImage serves GET /cherry/spin/<name>: the spin pictures (spinPicture) and item/<code>.png, the thumbnail of a
// catalog item (thumbs.ServeDpPNG, the picture the closet shows).
func HandleSpinImage(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/cherry/spin/")
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if code, ok := strings.CutPrefix(name, "item/"); ok {
		kind, id, ok := itemThumbRef(strings.TrimSuffix(code, ".png"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		thumbs.ServeDpPNG(w, kind, id)
		return
	}
	b, ok := spinPicture(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}
