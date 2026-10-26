package thumbs

import (
	"image"
	"image/color"
	"math"
)

// Premultiplied raster helpers of the landscape generator: the canvas, sprite resampling (PIL resize with
// the bilinear filter), affine sampling (PIL transform) and the BOX downscale. All of it works on premultiplied
// float32 values, so alpha compositing ("over") and soft edges behave as the spec asks.

// pyRound is Python's round() of a float: ties to even.
func pyRound(x float64) int {
	return int(math.RoundToEven(x))
}

// pmap is a premultiplied RGBA image of float32 values in 0..1, four per pixel: colour already multiplied by
// alpha. Sprites are resampled and composited in it, so soft edges neither darken nor ring.
type pmap struct {
	w, h int
	p    []float32
}

// newPmap is a transparent w x h image.
func newPmap(w, h int) *pmap {
	return &pmap{w: w, h: h, p: make([]float32, 4*w*h)}
}

// premul converts a straight-alpha sprite.
func premul(s *image.NRGBA) *pmap {
	b := s.Bounds()
	m := newPmap(b.Dx(), b.Dy())
	for y := range m.h {
		for x := range m.w {
			c := s.NRGBAAt(b.Min.X+x, b.Min.Y+y)
			a := float32(c.A) / 255
			i := 4 * (y*m.w + x)
			m.p[i] = float32(c.R) / 255 * a
			m.p[i+1] = float32(c.G) / 255 * a
			m.p[i+2] = float32(c.B) / 255 * a
			m.p[i+3] = a
		}
	}
	return m
}

// over composites src onto m with its top-left corner at (x0, y0); the part outside m is dropped.
func (m *pmap) over(src *pmap, x0, y0 int) {
	for sy := range src.h {
		dy := sy + y0
		if dy < 0 || dy >= m.h {
			continue
		}
		for sx := range src.w {
			dx := sx + x0
			if dx < 0 || dx >= m.w {
				continue
			}
			si, di := 4*(sy*src.w+sx), 4*(dy*m.w+dx)
			inv := 1 - src.p[si+3]
			for c := range 4 {
				m.p[di+c] = src.p[si+c] + m.p[di+c]*inv
			}
		}
	}
}

// boxStraight averages f x f blocks of m and returns them as straight 8-bit RGBA: the BOX downscale of the
// canvas (f = 1 converts only).
func (m *pmap) boxStraight(f int) *image.NRGBA {
	w, h := m.w/f, m.h/f
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	inv := 1 / float32(f*f)
	for y := range h {
		for x := range w {
			var acc [4]float32
			for dy := range f {
				for dx := range f {
					i := 4 * ((y*f+dy)*m.w + x*f + dx)
					for c := range acc {
						acc[c] += m.p[i+c]
					}
				}
			}
			a := acc[3] * inv
			if a <= 0 {
				continue
			}
			out.SetNRGBA(x, y, color.NRGBA{
				R: unit8(acc[0] * inv / a), G: unit8(acc[1] * inv / a), B: unit8(acc[2] * inv / a), A: unit8(a),
			})
		}
	}
	return out
}

// unit8 is a 0..1 value as an 8-bit channel, rounded and clamped.
func unit8(v float32) uint8 {
	return uint8(math.Max(0, math.Min(255, math.Round(float64(v)*255))))
}

// resize is PIL's resize with the BILINEAR filter (a triangle that widens when the image shrinks), separable,
// on premultiplied values. ponytail: float passes, where PIL rounds each pass to 8 bits (a level or two).
func (m *pmap) resize(w, h int) *pmap {
	xs := bilinearRows(m.w, w)
	ys := bilinearRows(m.h, h)
	tmp := newPmap(w, m.h)
	for y := range m.h {
		for xx, row := range xs {
			di := 4 * (y*w + xx)
			for k, wt := range row.w {
				si := 4 * (y*m.w + row.start + k)
				for c := range 4 {
					tmp.p[di+c] += float32(wt) * m.p[si+c]
				}
			}
		}
	}
	out := newPmap(w, h)
	for yy, row := range ys {
		for k, wt := range row.w {
			sy := row.start + k
			for x := range w {
				di, si := 4*(yy*w+x), 4*(sy*w+x)
				for c := range 4 {
					out.p[di+c] += float32(wt) * tmp.p[si+c]
				}
			}
		}
	}
	return out
}

// filterRow is the weights of one output row or column: input start..start+len(w)-1.
type filterRow struct {
	start int
	w     []float64
}

// bilinearRows are PIL's coefficients for resampling in -> out with the bilinear filter.
func bilinearRows(in, out int) []filterRow {
	scale := float64(in) / float64(out)
	fs := math.Max(scale, 1)
	rows := make([]filterRow, out)
	for xx := range out {
		center := (float64(xx) + 0.5) * scale
		lo := max(int(center-fs+0.5), 0)
		hi := min(int(center+fs+0.5), in)
		r := filterRow{start: lo, w: make([]float64, hi-lo)}
		sum := 0.0
		for k := range r.w {
			r.w[k] = triangle((float64(lo+k) - center + 0.5) * (1 / fs))
			sum += r.w[k]
		}
		if sum != 0 {
			for k := range r.w {
				r.w[k] /= sum
			}
		}
		rows[xx] = r
	}
	return rows
}

// triangle is the bilinear filter kernel.
func triangle(x float64) float64 {
	x = math.Abs(x)
	if x < 1 {
		return 1 - x
	}
	return 0
}

// sampleAffine returns a w x h image whose pixel (x, y) is src sampled bilinearly at inv(x+0.5, y+0.5), the
// PIL Image.transform of an affine map: no prefilter, transparent outside src.
func sampleAffine(src *pmap, w, h int, inv func(x, y float64) (float64, float64)) *pmap {
	out := newPmap(w, h)
	for y := range h {
		for x := range w {
			sx, sy := inv(float64(x)+0.5, float64(y)+0.5)
			if sx < 0 || sy < 0 || sx >= float64(src.w) || sy >= float64(src.h) {
				continue
			}
			fx, fy := sx-0.5, sy-0.5
			x0, y0 := math.Floor(fx), math.Floor(fy)
			tx, ty := float32(fx-x0), float32(fy-y0)
			di := 4 * (y*w + x)
			for dy := range 2 {
				wy := 1 - ty
				if dy == 1 {
					wy = ty
				}
				py := int(y0) + dy
				for dx := range 2 {
					wx := 1 - tx
					if dx == 1 {
						wx = tx
					}
					px := int(x0) + dx
					if px < 0 || py < 0 || px >= src.w || py >= src.h {
						continue
					}
					si := 4 * (py*src.w + px)
					for c := range 4 {
						out.p[di+c] += wx * wy * src.p[si+c]
					}
				}
			}
		}
	}
	return out
}

// pasteAffine composites src over dst through the map that sends src's origin to anchor and its axes to ex (the
// x axis) and ey (the y axis), in canvas pixels (make_v4 paste_affine). mask, when set, scales each pixel.
func pasteAffine(dst, src *pmap, anchor, ex, ey, origin [2]float64, mask []float32) {
	det := ex[0]*ey[1] - ey[0]*ex[1]
	if math.Abs(det) < 1e-9 {
		return
	}
	i00, i01 := ey[1]/det, -ey[0]/det
	i10, i11 := -ex[1]/det, ex[0]/det
	layer := sampleAffine(src, dst.w, dst.h, func(x, y float64) (float64, float64) {
		dx, dy := x-anchor[0], y-anchor[1]
		return origin[0] + i00*dx + i01*dy, origin[1] + i10*dx + i11*dy
	})
	if mask != nil {
		for i, a := range mask {
			for c := range 4 {
				layer.p[4*i+c] *= a
			}
		}
	}
	dst.over(layer, 0, 0)
}
