package thumbs

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Dye masks (dp_mask.png) of the dyeable garments. The dye cell draws dp.png and dp_mask.png together. The archive has an
// original mask for only four of the fifteen dyeable garments (custom 224102309, 224102579, 224406975; dress 225001650);
// the others get a mask composed here from their per-part masks <part>_mask.png (Cherry-made, cached under dp-cache\).
//
// The convention, read off the four original pairs: the mask is the same size as dp.png, its alpha is the garment's, and
// each pixel's colour is a tone code, pure red (255,0,0) or pure blue (0,0,255) with blends at the edges of the parts.
// The bald head a face or hair preview draws under the parts is the base layer, and the originals paint it blue (skin
// pixels are blue in all three originals that have a head). A part without a mask contributes transparent pixels. A
// green (0,255,0) code, present in the masks of 224102536, is kept as the part gives it.
//
// The mask runs the dp.png compositor with the masks in place of the sprites, so the layout is the preview's: the same
// layers, canvas and frame box (the colour render's alpha box, so a part without a mask cannot move the frame), and no
// shadow. Every mask-bearing part is placed on its sprite's top-left corner.
// ponytail: 3 of the 15 garments have a mask 1 px off its sprite's size (224102816, 224102858, 225409234); top-left
// placement is the simple fit, and a centred or alpha-fitted offset would move their edges by a pixel. Upgrade when the
// device shows a seam there.

// dpMaskCacheVersion prefixes the cached masks (the dp.png composites carry dpCacheVersion).
const dpMaskCacheVersion = "dm1_"

// dpMaskSuffix is the name of a part's tone mask: <part>.png's name with _mask before the extension.
const dpMaskSuffix = "_mask.png"

// dpToneParts swaps each loaded part's sprite for its tone mask, keeping the sprite's bounds so the layout stays the
// preview's. A part without a mask becomes transparent. ok is false for a placeholder part (its art is an animation, which
// has no mask) and for a mask more than 2 px off its sprite's size.
func dpToneParts(dir string, loaded []dpLoaded) ([]dpLoaded, bool) {
	out := make([]dpLoaded, 0, len(loaded))
	for _, lp := range loaded {
		b := lp.img.Bounds()
		if b.Dx() <= 2 && b.Dy() <= 2 {
			return nil, false
		}
		tone := image.NewNRGBA(b)
		if f, err := os.Open(filepath.Join(dir, strings.TrimSuffix(lp.name, ".png")+dpMaskSuffix)); err == nil {
			m, err := png.Decode(f)
			f.Close()
			if err != nil {
				return nil, false
			}
			mb := m.Bounds()
			if dw, dh := mb.Dx()-b.Dx(), mb.Dy()-b.Dy(); max(dw, -dw) > 2 || max(dh, -dh) > 2 {
				return nil, false
			}
			draw.Draw(tone, b, m, mb.Min, draw.Src)
		}
		lp.img = tone
		out = append(out, lp)
	}
	return out, true
}

// dpBaseTone returns img in the base tone of a dye mask: blue, with img's alpha.
func dpBaseTone(img image.Image) image.Image {
	b := img.Bounds()
	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			out.SetNRGBA(x, y, color.NRGBA{B: 0xff, A: uint8(a >> 8)})
		}
	}
	return out
}

// composeItemMask renders the 130x100 dye mask of a custom or dress item (see the convention above). ok is false when
// the item has no colour preview to frame against, or its masks cannot be laid out.
func composeItemMask(dir, kind string) (*image.RGBA, bool) {
	col, cat, ok := dpComposeAs(dir, kind, false)
	if !ok {
		return nil, false
	}
	box := dpAlphaBBox(col.img)
	if box.Empty() {
		return nil, false
	}
	tone, _, ok := dpComposeAs(dir, kind, true)
	if !ok {
		return nil, false
	}
	tone.frame = box
	p := dpFrameFor(cat)
	p.shadow = false
	return dpFrameWith(tone, p), true
}

// ServeDyeMask answers GET dp_mask.png of a dyeable garment (the caller checks the id is dyeable). It serves the composed
// mask from dp-cache, or composes and caches it. It returns false, with nothing written, when the archive holds the original
// mask (the stream serves those, never this generator), the kind is not custom or dress, or the item cannot be composed;
// the caller then answers 404, which the dye cell shows as its warning icon.
func ServeDyeMask(w http.ResponseWriter, kind, id string) bool {
	if kind != "custom" && kind != "dress" {
		return false
	}
	dir := filepath.Join(DpItemRoot, kind, id)
	// An original mask is the archive's own; with streaming on the stream already answered it, so this serves it only
	// when streaming is off.
	if b, err := os.ReadFile(filepath.Join(dir, "dp_mask.png")); err == nil {
		dpWritePNG(w, b)
		return true
	}
	cachePath := filepath.Join(DpCacheDir, dpMaskCacheVersion+kind+"_"+id+".png")
	if b, err := os.ReadFile(cachePath); err == nil {
		dpWritePNG(w, b)
		return true
	}
	mu := dpFlight("mask/" + kind + "/" + id)
	mu.Lock()
	defer mu.Unlock()
	if b, err := os.ReadFile(cachePath); err == nil {
		dpWritePNG(w, b)
		return true
	}
	img, ok := composeItemMask(dir, kind)
	if !ok || dpAlphaBBox(img).Empty() {
		return false
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return false
	}
	b := buf.Bytes()
	if DpCacheDir != "" {
		if err := os.MkdirAll(DpCacheDir, 0o755); err == nil {
			dpAtomicWrite(cachePath, b)
		}
	}
	dpWritePNG(w, b)
	return true
}
