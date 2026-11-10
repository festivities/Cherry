package economy

import (
	"fmt"
	"hash/crc32"
	"image"
	"image/draw"
	"net/http"
	"slices"
	"strings"

	"cherry/internal/assets"
)

// The gacha card (gachaMainBannerImageUrl, and the category tile's repGachaList imgUrl): Cherry's own picture of a gacha,
// the first RARE item's thumbnail (itemDPImage) centred on a plain panel. Cherry-made, so it is cached in dp-cache under
// gc1_ and served at /cherry/gacha/<code>-<checksum>.png. The client caches a picture by path and never fetches it again,
// so the name carries a checksum of what the card shows (the item code and the size).

// gachaCardSize is the card's side in pixels. Calibration point: the spec gives no size for the card cell, so this is a
// square guess to check on the device (the shop list, the category tile).
const gachaCardSize = 320

// gachaCardPrefix is the dp-cache prefix; bump it when the drawing changes.
const gachaCardPrefix = "gc1_"

// gachaCardBg is the plain panel behind the item (the spin list's background colour).
var gachaCardBg = rgbHex(0x2A2250)

// gachaCardShow is what the card shows: its size and its item.
func gachaCardShow(code string) string { return fmt.Sprintf("%d %s", gachaCardSize, code) }

// gachaCardName is the card's file name under /cherry/gacha/.
func gachaCardName(code string) string {
	return fmt.Sprintf("%s-%08x.png", code, crc32.ChecksumIEEE([]byte(gachaCardShow(code))))
}

// gachaCardURL is the card's relative URL for a gacha row, or "" with no item or no asset store (the card needs the
// item's art, so no picture is better than a broken one).
func gachaCardURL(code string) string {
	if code == "" || assets.Archive == nil {
		return ""
	}
	return "cherry/gacha/" + gachaCardName(code)
}

// gachaPoolHas reports whether code is a gacha item of either tier (any requester's pool is a part of it).
func gachaPoolHas(code string) bool {
	p := gachaPoolNow()
	return slices.ContainsFunc(p.normal, func(e gachaEntry) bool { return e.code == code }) ||
		slices.ContainsFunc(p.rare, func(e gachaEntry) bool { return e.code == code })
}

// gachaCard draws the card for code: the item's dp.png fitted into the middle of the panel. An item with no picture
// leaves the plain panel.
func gachaCard(code string) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, gachaCardSize, gachaCardSize))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: gachaCardBg}, image.Point{}, draw.Src)
	if thumb, ok := itemDPImage(code); ok {
		spinPaste(img, gachaFit(thumb, gachaCardSize*3/4), gachaCardSize/2, gachaCardSize/2)
	}
	return img
}

// gachaFit scales src so its longer side is side, keeping the aspect (nearest neighbour).
func gachaFit(src image.Image, side int) image.Image {
	b := src.Bounds()
	if b.Empty() {
		return src
	}
	scale := float64(side) / float64(max(b.Dx(), b.Dy()))
	w := max(1, int(float64(b.Dx())*scale+0.5))
	h := max(1, int(float64(b.Dy())*scale+0.5))
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			out.Set(x, y, src.At(b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h))
		}
	}
	return out
}

// HandleGachaCardImage serves GET /cherry/gacha/<code>-<checksum>.png: the card of a gacha item. Only the name gachaCardName
// makes for a gacha item is served; anything else is 404.
func HandleGachaCardImage(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/cherry/gacha/")
	code, _, _ := strings.Cut(name, "-")
	if r.Method != http.MethodGet || !gachaPoolHas(code) || gachaCardName(code) != name {
		http.NotFound(w, r)
		return
	}
	b, ok := cherryPNG(gachaCardPrefix, code, gachaCardShow(code), func() image.Image { return gachaCard(code) })
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}
