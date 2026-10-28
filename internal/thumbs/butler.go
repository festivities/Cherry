package thumbs

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
)

// Butler portraits (Cherry-made, data/butlers.json): the outfit of one butler on the human idle rig, over the bald
// head, fitted into a square PNG. The parts of every item are placed as the item previews place them
// (dpSkeletonCompose). Placeholder parts that carry only an animation are left out, so an animated part can be
// missing [I]. The PNG is Cherry's own image: cached under DpCacheDir as v1_butler_*, never under a static tree.

// butlerCacheVersion prefixes the cache files of butler portraits; the key names the butler and its outfit.
const butlerCacheVersion = "v1_butler_"

// butlerSize is the portrait's side in px.
const butlerSize = 256

// butlerMargin is the empty border around the figure, in px.
const butlerMargin = 24

// ButlerPart is one item folder of an outfit: its directory and its kind ("dress" or "custom").
type ButlerPart struct {
	Dir  string
	Kind string
}

// RenderButler returns the portrait PNG of an outfit. key names the cache file and must change with the outfit.
// ok is false when no part draws or the PNG cannot be encoded.
func RenderButler(key string, parts []ButlerPart) ([]byte, bool) {
	path := ""
	if DpCacheDir != "" {
		path = filepath.Join(DpCacheDir, butlerCacheVersion+key+".png")
	}
	if b, ok := readCachedButler(path); ok {
		return b, true
	}
	mu := dpFlight(butlerCacheVersion + key)
	mu.Lock()
	defer mu.Unlock()
	if b, ok := readCachedButler(path); ok {
		return b, true
	}
	img, ok := composeButler(parts)
	if !ok {
		return nil, false
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, false
	}
	if path != "" && os.MkdirAll(DpCacheDir, 0o755) == nil {
		dpAtomicWrite(path, buf.Bytes())
	}
	return buf.Bytes(), true
}

func readCachedButler(path string) ([]byte, bool) {
	if path == "" {
		return nil, false
	}
	b, err := os.ReadFile(path)
	return b, err == nil
}

// composeButler places the parts of every item on the human rig over the bald head, then fits the figure into the
// square on a soft background. An item whose folder fails to load is skipped, as the client skips a missing sprite.
func composeButler(parts []ButlerPart) (*image.RGBA, bool) {
	rig := dpLoadRig("h")
	if rig == nil {
		return nil, false
	}
	layers := rig.headLayers()
	seq := 0
	for _, p := range parts {
		loaded, _, ok := dpLoadParts(p.Dir, p.Kind)
		if !ok {
			continue
		}
		for _, lp := range loaded {
			if b := lp.img.Bounds(); b.Dx() <= 2 && b.Dy() <= 2 {
				continue // a placeholder carries only an animation
			}
			img, x, y, ok := rig.place(lp.anchor, lp.x, lp.y, lp.img)
			if !ok {
				continue
			}
			seq++
			layers = append(layers, dpLayer{z: lp.z, seq: seq, img: img, x: x, y: y})
		}
	}
	full, _, _ := dpRasterize(layers, dpPartPad)
	if full == nil {
		return nil, false
	}
	box := dpAlphaBBox(full)
	if box.Empty() {
		return nil, false
	}
	crop := image.NewRGBA(image.Rect(0, 0, box.Dx(), box.Dy()))
	draw.Draw(crop, crop.Bounds(), full, box.Min, draw.Src)
	inner := float64(butlerSize - 2*butlerMargin)
	scale := min(inner/float64(box.Dx()), inner/float64(box.Dy()))
	dw := max(int(float64(box.Dx())*scale+0.5), 1)
	dh := max(int(float64(box.Dy())*scale+0.5), 1)
	fit := dpScale(crop, dw, dh)
	out := image.NewRGBA(image.Rect(0, 0, butlerSize, butlerSize))
	draw.Draw(out, out.Bounds(), &image.Uniform{color.RGBA{243, 238, 247, 255}}, image.Point{}, draw.Src)
	at := image.Pt((butlerSize-dw)/2, (butlerSize-dh)/2)
	draw.Draw(out, image.Rectangle{Min: at, Max: at.Add(image.Pt(dw, dh))}, fit, image.Point{}, draw.Over)
	return out, true
}
