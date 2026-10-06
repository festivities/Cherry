package main

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// dpCat is an item category derived from the numeric id prefix
// (22xx human / 23xx animal + the category digits).
type dpCat struct {
	code   string // HE EB EY NO MO HA TO ON PA SH AH AE FE; "" unknown
	animal bool
}

var dpCatByPrefix = map[string]string{
	"2238": "HE", "2240": "EB", "2241": "EY", "2242": "NO", "2243": "MO", "2244": "HA",
	"2250": "TO", "2251": "ON", "2252": "PA", "2253": "SH", "2254": "AH", "2255": "AE", "2256": "FE",
}

// dpAnimalCatByPrefix: 2339 face, 2341 eye, 2342 nose, 2343 mouth, then the
// human 225x categories shifted by +100.
var dpAnimalCatByPrefix = map[string]string{
	"2339": "HE", "2341": "EY", "2342": "NO", "2343": "MO",
	"2350": "TO", "2351": "ON", "2352": "PA", "2353": "SH", "2354": "AH", "2355": "AE", "2356": "FE",
}

func dpCategoryOf(id string) dpCat {
	if len(id) < 4 {
		return dpCat{}
	}
	p := id[:4]
	if p[:2] == "23" {
		return dpCat{code: dpAnimalCatByPrefix[p], animal: true}
	}
	return dpCat{code: dpCatByPrefix[p]}
}

// head reports whether the preview shows the item on the default bald head.
func (c dpCat) head() bool {
	switch c.code {
	case "HE", "EB", "EY", "NO", "MO", "HA", "FE":
		return true
	}
	return false
}

func (c dpCat) rig() string {
	if c.animal {
		return "a"
	}
	return "h"
}

const (
	dpOutW = 130
	dpOutH = 100
)

// dpFrameParam is the per-category framing fitted against the authentic
// on-device previews (mean mask IoU, 215 items): the content box is fitted into
// w x h with the scale capped at zoom, centred on (65, cy).
type dpFrameParam struct {
	w, h   float64
	zoom   float64
	cy     float64
	shadow bool
	// head anchors the default head centre at (hx, hy) with scale zoom and
	// clips to the canvas instead of fitting the content (hair previews).
	head   bool
	hx, hy float64
}

var (
	dpFaceFrame = dpFrameParam{w: 130, h: 100, zoom: 1, cy: 50}
	dpWornFrame = dpFrameParam{w: 120, h: 94, zoom: 1.3, cy: 50, shadow: true}
	dpCatFrames = map[string]dpFrameParam{
		"HE": dpFaceFrame, "EB": dpFaceFrame, "EY": dpFaceFrame, "NO": dpFaceFrame,
		"MO": dpFaceFrame, "FE": dpFaceFrame,
		"HA": {head: true, zoom: 0.83, hx: 65, hy: 60},
		"ON": dpWornFrame,
		"PA": dpWornFrame,
		"TO": {w: 120, h: 94, zoom: 1.2, cy: 48, shadow: true},
		"SH": {w: 124, h: 97, zoom: 1.9, cy: 46, shadow: true},
		"AH": {w: 120, h: 94, zoom: 1.1, cy: 56, shadow: true},
		"AE": {w: 120, h: 94, zoom: 1.1, cy: 50, shadow: true},
	}
)

func dpFrameFor(c dpCat) dpFrameParam {
	if p, ok := dpCatFrames[c.code]; ok {
		return p
	}
	return dpWornFrame
}

// dpAlphaBBox returns the bounding box of pixels with alpha > 0.
func dpAlphaBBox(img *image.RGBA) image.Rectangle {
	b := img.Bounds()
	r := image.Rectangle{Min: b.Max, Max: b.Min}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[(y-b.Min.Y)*img.Stride:]
		for x := b.Min.X; x < b.Max.X; x++ {
			if row[(x-b.Min.X)*4+3] != 0 {
				r.Min.X, r.Max.X = min(r.Min.X, x), max(r.Max.X, x+1)
				r.Min.Y, r.Max.Y = min(r.Min.Y, y), max(r.Max.Y, y+1)
			}
		}
	}
	if r.Empty() {
		return image.Rectangle{}
	}
	return r
}

// dpScale resamples src (premultiplied) into a dw x dh image: area average
// when shrinking, bilinear when enlarging.
func dpScale(src *image.RGBA, dw, dh int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	if sw == 0 || sh == 0 || dw <= 0 || dh <= 0 {
		return dst
	}
	px := func(x, y int) (r, g, b, a float64) {
		x = min(max(x, 0), sw-1)
		y = min(max(y, 0), sh-1)
		o := y*src.Stride + x*4
		return float64(src.Pix[o]), float64(src.Pix[o+1]), float64(src.Pix[o+2]), float64(src.Pix[o+3])
	}
	fx, fy := float64(sw)/float64(dw), float64(sh)/float64(dh)
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var r, g, b, a float64
			if fx > 1 || fy > 1 {
				x0, x1 := float64(x)*fx, float64(x+1)*fx
				y0, y1 := float64(y)*fy, float64(y+1)*fy
				var wsum float64
				for yy := int(y0); float64(yy) < y1 && yy < sh; yy++ {
					wy := math.Min(float64(yy+1), y1) - math.Max(float64(yy), y0)
					for xx := int(x0); float64(xx) < x1 && xx < sw; xx++ {
						w := wy * (math.Min(float64(xx+1), x1) - math.Max(float64(xx), x0))
						pr, pg, pb, pa := px(xx, yy)
						r, g, b, a, wsum = r+pr*w, g+pg*w, b+pb*w, a+pa*w, wsum+w
					}
				}
				if wsum > 0 {
					r, g, b, a = r/wsum, g/wsum, b/wsum, a/wsum
				}
			} else {
				u, v := (float64(x)+0.5)*fx-0.5, (float64(y)+0.5)*fy-0.5
				ix, iy := int(math.Floor(u)), int(math.Floor(v))
				tx, ty := u-float64(ix), v-float64(iy)
				for k, w := range [4]float64{(1 - tx) * (1 - ty), tx * (1 - ty), (1 - tx) * ty, tx * ty} {
					pr, pg, pb, pa := px(ix+k%2, iy+k/2)
					r, g, b, a = r+pr*w, g+pg*w, b+pb*w, a+pa*w
				}
			}
			o := y*dst.Stride + x*4
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] =
				uint8(r+0.5), uint8(g+0.5), uint8(b+0.5), uint8(a+0.5)
		}
	}
	return dst
}

// dpShadow paints a soft dark ellipse under the content.
func dpShadow(dst *image.RGBA, cx, cy, rx, ry float64) {
	for y := int(cy - ry - 1); y <= int(cy+ry+1); y++ {
		for x := int(cx - rx - 1); x <= int(cx+rx+1); x++ {
			if !image.Pt(x, y).In(dst.Bounds()) {
				continue
			}
			d := math.Hypot((float64(x)+0.5-cx)/rx, (float64(y)+0.5-cy)/ry)
			if d >= 1 {
				continue
			}
			a := uint8(51 * math.Min(1, (1-d)/0.2))
			draw.Draw(dst, image.Rect(x, y, x+1, y+1), image.NewUniform(color.NRGBA{0, 0, 0, a}), image.Point{}, draw.Over)
		}
	}
}

// dpFrame frames a natural render into a transparent 130x100 canvas per the
// category's framing.
func dpFrame(n *dpNat, c dpCat) *image.RGBA {
	return dpFrameWith(n, dpFrameFor(c))
}

func dpFrameWith(n *dpNat, p dpFrameParam) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, dpOutW, dpOutH))
	if p.head && n.skel {
		sb := n.img.Bounds()
		dw, dh := max(1, int(math.Round(float64(sb.Dx())*p.zoom))), max(1, int(math.Round(float64(sb.Dy())*p.zoom)))
		if dw > 2048 || dh > 2048 {
			return out
		}
		ox := int(math.Round(p.hx - n.headX*p.zoom))
		oy := int(math.Round(p.hy - n.headY*p.zoom))
		draw.Draw(out, image.Rect(ox, oy, ox+dw, oy+dh), dpScale(n.img, dw, dh), image.Point{}, draw.Over)
		return out
	}
	if p.w == 0 {
		p = dpFrameParam{w: 130, h: 100, zoom: 1.5, cy: 50}
	}
	img := n.img
	bb := dpAlphaBBox(img)
	if bb.Empty() {
		return out
	}
	w, h := bb.Dx(), bb.Dy()
	s := math.Min(math.Min(p.w/float64(w), p.h/float64(h)), p.zoom)
	dw, dh := min(max(1, int(math.Round(float64(w)*s))), dpOutW), min(max(1, int(math.Round(float64(h)*s))), dpOutH)
	crop := img.SubImage(bb).(*image.RGBA)
	crop = &image.RGBA{Pix: crop.Pix, Stride: crop.Stride, Rect: image.Rect(0, 0, w, h)}
	scaled := dpScale(crop, dw, dh)
	ox, oy := int(math.Round(dpOutW/2.0-float64(dw)/2)), int(math.Round(p.cy-float64(dh)/2))
	oy = min(max(oy, 0), dpOutH-dh)
	if p.shadow {
		rx := math.Min(math.Max(float64(dw)*0.4, 28), 42)
		dpShadow(out, dpOutW/2.0, float64(oy+dh)-2, rx, rx*0.22)
	}
	draw.Draw(out, image.Rect(ox, oy, ox+dw, oy+dh), scaled, image.Point{}, draw.Over)
	return out
}
