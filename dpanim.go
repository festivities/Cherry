package main

import (
	"bytes"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Closet thumbnails for animated items. Many custom/dress items ship 1x1
// placeholder part sprites; the real art is an ArtsAn animation project
// "<itemid>.aniproj" next to the frames. Format traced from libgame.so 10.1.0.0:
//
//	artsitem part row: [anchor, z, c, d, x, y, filename, (wide extras), aniCount,
//	aniCount * 8 fields]. The 8 fields fill sAniParts (SbItemTableLoader_*_Vn,
//	e.g. SbItemTableLoader_Custom_V1 @0x2acf380) as: +8 byte symbol index n,
//	+0xC int (unused by the attach), +0x10 byte z, +0x12 short angle (degrees),
//	+0x14 float scale, +0x18 byte alpha, +0x1C CCPoint (dx, dy). All are atoi()
//	conversions, so "0.000000" reads as 0.
//	AvItem::AttachPartsAnimation @0x29b6b10: creates CAnAnimation
//	"<itemid>/IA_<n>", adds it to the part node with z=z, setRotation(angle),
//	setPosition(dx, dy) (y up). Scale/alpha fields are loaded but not applied there.
//
//	aniproj (ArtsAn::CAnAnimationData::startElementForLoad @0x299a624): XML
//	<SYMBOLS><symbol szName dwTotalFrame dwFrameTime><layer dwIndex dwOption>
//	<process dwProcessKey><property dwFrame ...>. Process keys (PROCESS_TYPE
//	order, CAnProcess::sCreateProcessByType @0x2991144): 0 symbol (sprite png or
//	nested symbol; held dwKeepFrame frames), 1 scissor, 2 anchor (pixel offset
//	of the layer origin, ::UpdateProcess @0x298d930), 3 move (@0x298ce40, added
//	to the anchor, optional quadratic bezier), 4 scale (@0x298d010), 5 rotation
//	(@0x298d1a4, degrees clockwise), 6 skew (ignored here), 7 colour RGBA
//	multiply (@0x298d484), 8 sound, 9 event. eInterpolate selects
//	ArtsSb::GetInterpolationData @0x2ae1a68: -1 hold, 0 linear, 1..9 easings.
//	Coordinates are cocos2d y-up; sprites are drawn centred on the layer origin.

// dpAniPart is one sAniParts record joined with the part row that owns it.
// The animation symbol "IA_<Symbol>" is drawn with its origin at the part
// position plus (DX, DY) (y up), rotated Angle degrees clockwise, at Z.
type dpAniPart struct {
	Anchor int     // part parent-node key
	Z      int     // part z
	X, Y   float64 // part row x, y
	DX, DY int     // sAniParts position (y up)
	Angle  int     // degrees clockwise
	AniZ   int     // sAniParts z
	Symbol string  // "IA_<n>"
	Back   bool    // from the back list
}

// dpItemAniParts extracts the animation attachments of an artsitem, trying the
// custom layout then the dress layout (the text itself does not say which).
func dpItemAniParts(text string) ([]dpAniPart, error) {
	for _, dress := range []bool{false, true} {
		if parts, ok := dpScanAniParts(text, dress); ok {
			return parts, nil
		}
	}
	return nil, errDpAniLayout
}

type dpAniErr string

func (e dpAniErr) Error() string { return string(e) }

const errDpAniLayout = dpAniErr("artsitem: unrecognised layout")

func dpScanAniParts(text string, dress bool) ([]dpAniPart, bool) {
	p := &dpParser{fields: strings.Split(text, ";")}
	p.num() // id
	p.num() // hasAnim
	wide := 0
	if dress {
		p.num()
		p.num()
		ver := p.num()
		switch ver {
		case 2:
			wide = 1
		case 3:
			wide = 2
			p.num()
			p.num()
			p.num()
		}
		if ver < 0 || ver > 3 {
			return nil, false
		}
	} else {
		ver := p.num()
		if ver == 1 {
			wide = 2 // Custom_V1 @0x2acf380 reads two ints after the filename
		}
		if ver == 2 {
			wide = 3
			p.num()
			p.num()
			p.num()
			p.num()
		}
		if ver < 0 || ver > 2 {
			return nil, false
		}
	}
	var out []dpAniPart
	rows := 0
	for _, back := range []bool{false, true} {
		n := p.num()
		if n < 0 || n > dpMaxParts {
			return nil, false
		}
		for i := 0; i < n; i++ {
			rows++
			var pt dpAniPart
			pt.Back = back
			pt.Anchor = p.num()
			pt.Z = p.num()
			p.num()
			p.num()
			pt.X = p.flt()
			pt.Y = p.flt()
			name := p.name()
			if !strings.HasSuffix(strings.ToLower(name), ".png") {
				return nil, false
			}
			for w := 0; w < wide; w++ {
				p.num()
			}
			cnt := p.num()
			if cnt < 0 || cnt > dpMaxAniParts {
				return nil, false
			}
			for a := 0; a < cnt; a++ {
				ap := pt
				ap.Symbol = "IA_" + strconv.Itoa(p.num())
				p.num()
				ap.AniZ = p.num()
				ap.Angle = p.num()
				p.num() // scale
				p.num() // alpha
				ap.DX = p.num()
				ap.DY = p.num()
				out = append(out, ap)
			}
		}
	}
	return out, rows > 0
}

// ---- aniproj model ----

type anProp struct {
	Frame          int
	Sym            string
	Keep, Blend    int
	PX, PY, BX, BY float64
	Rot            float64
	CR, CG, CB, CA int
	Interp         *int
}

type anProc struct {
	Key   int
	Props []anProp
}

type anLayer struct {
	Index int
	Procs []anProc
}

type anSymbol struct {
	Name   string
	Total  int
	Layers []anLayer
}

type anCtx struct {
	dir   string
	syms  map[string]*anSymbol
	imgs  map[string]*image.NRGBA
	draws []anDraw
}

type anDraw struct {
	img   *image.NRGBA
	m     [6]float64 // a b tx c d ty : x' = a x + b y + tx (y down)
	col   [4]float64 // rgba multiply 0..1
	add   bool
	w, h  float64
	order int
}

func dpLoadAniproj(dir string) (*anCtx, bool) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.aniproj"))
	if len(matches) == 0 {
		return nil, false
	}
	sort.Strings(matches)
	raw, err := os.ReadFile(matches[0])
	if err != nil || len(raw) > 8<<20 {
		return nil, false
	}
	syms, ok := anParse(raw)
	if !ok {
		return nil, false
	}
	c := &anCtx{dir: dir, syms: map[string]*anSymbol{}, imgs: map[string]*image.NRGBA{}}
	for i := range syms {
		c.syms[syms[i].Name] = &syms[i]
	}
	return c, true
}

// dpAnimSymbols lists the symbol names of the item's .aniproj, sorted.
func dpAnimSymbols(itemDir string) []string {
	c, ok := dpLoadAniproj(itemDir)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(c.syms))
	for n := range c.syms {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c *anCtx) image(name string) *image.NRGBA {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if im, ok := c.imgs[name]; ok {
		return im
	}
	var out *image.NRGBA
	if f, err := os.Open(filepath.Join(c.dir, name)); err == nil {
		if im, err := png.Decode(f); err == nil {
			b := im.Bounds()
			if b.Dx() <= 4096 && b.Dy() <= 4096 {
				n := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
				for y := 0; y < b.Dy(); y++ {
					for x := 0; x < b.Dx(); x++ {
						n.Set(x, y, color.NRGBAModel.Convert(im.At(b.Min.X+x, b.Min.Y+y)))
					}
				}
				out = n
			}
		}
		f.Close()
	}
	c.imgs[name] = out
	return out
}

// anEase maps t in [0,1] per ArtsSb::GetInterpolationData; ok=false means hold.
func anInterp(kind int, a, b, t float64) float64 {
	d := b - a
	switch kind {
	case 0:
		return a + d*t
	case 1:
		return a + d*(1-math.Cos(t*math.Pi/2))
	case 2:
		return a + d*math.Sin(t*math.Pi/2)
	case 3:
		return a + d*t*t
	case 4:
		return b - d*(1-t)*(1-t)
	case 5:
		return a + d*t*t*t
	case 6:
		return b - d*(1-t)*(1-t)*(1-t)
	case 7:
		return a - d*0.5*(math.Cos(t*math.Pi)-1)
	case 8:
		u := 2 * t
		if u < 1 {
			return a + d*0.5*u*u
		}
		return a - d*0.5*((u-1-2)*(u-1)-1)
	case 9:
		u := 2 * t
		if u < 1 {
			return a + d*0.5*u*u*u
		}
		return a + d*0.5*((u-2)*(u-2)*(u-2)+2)
	}
	return a
}

// sample evaluates a numeric track at frame t: the last key at or before t,
// blended toward the next key when that key's interpolate type is >= 0.
// get returns the channel values of a key; n is the channel count.
func anTrack(props []anProp, t float64, n int, get func(*anProp) []float64, bez bool) ([]float64, bool) {
	if len(props) == 0 {
		return nil, false
	}
	cur := -1
	for i := range props {
		if float64(props[i].Frame) <= t {
			cur = i
		}
	}
	if cur < 0 {
		cur = 0
	}
	p := &props[cur]
	v := get(p)
	if cur+1 >= len(props) || p.Interp == nil || *p.Interp < 0 || float64(p.Frame) > t {
		return v, true
	}
	q := &props[cur+1]
	span := float64(q.Frame - p.Frame)
	if span <= 0 {
		return v, true
	}
	frac := (t - float64(p.Frame)) / span
	w := get(q)
	out := make([]float64, n)
	if bez && (p.BX != 0 || p.BY != 0) {
		s := anInterp(*p.Interp, 0, 1, frac)
		ctl := []float64{p.BX, p.BY}
		for i := 0; i < n; i++ {
			out[i] = s*(2*ctl[i]*(1-s)) + v[i]*(1-s)*(1-s) + s*s*w[i]
		}
		return out, true
	}
	for i := 0; i < n; i++ {
		out[i] = anInterp(*p.Interp, v[i], w[i], frac)
	}
	return out, true
}

func anMul(a, b [6]float64) [6]float64 {
	return [6]float64{
		a[0]*b[0] + a[1]*b[3], a[0]*b[1] + a[1]*b[4], a[0]*b[2] + a[1]*b[5] + a[2],
		a[3]*b[0] + a[4]*b[3], a[3]*b[1] + a[4]*b[4], a[3]*b[2] + a[4]*b[5] + a[5],
	}
}

const anMaxDepth = 6

// collect appends the draws of symbol s at local frame t under matrix m.
func (c *anCtx) collect(s *anSymbol, t float64, m [6]float64, col [4]float64, depth int) {
	if depth > anMaxDepth {
		return
	}
	for _, ly := range s.Layers {
		var symProc *anProc
		var anchorP, moveP, scaleP, rotP, colP *anProc
		for i := range ly.Procs {
			switch ly.Procs[i].Key {
			case 0:
				symProc = &ly.Procs[i]
			case 2:
				anchorP = &ly.Procs[i]
			case 3:
				moveP = &ly.Procs[i]
			case 4:
				scaleP = &ly.Procs[i]
			case 5:
				rotP = &ly.Procs[i]
			case 7:
				colP = &ly.Procs[i]
			}
		}
		if symProc == nil {
			continue
		}
		// active symbol key: latest key at or before t still within its keep span
		var key *anProp
		for i := range symProc.Props {
			p := &symProc.Props[i]
			if float64(p.Frame) > t {
				continue
			}
			end := p.Frame + p.Keep
			if p.Keep < 0 {
				end = math.MaxInt32
			}
			if t <= float64(end) {
				key = p
			}
		}
		if key == nil {
			continue
		}
		px, py := 0.0, 0.0
		if anchorP != nil {
			if v, ok := anTrack(anchorP.Props, t, 2, func(p *anProp) []float64 { return []float64{p.PX, p.PY} }, false); ok {
				px, py = v[0], v[1]
			}
		}
		if moveP != nil {
			if v, ok := anTrack(moveP.Props, t, 2, func(p *anProp) []float64 { return []float64{p.PX, p.PY} }, true); ok {
				px += v[0]
				py += v[1]
			}
		}
		sx, sy := 1.0, 1.0
		if scaleP != nil {
			if v, ok := anTrack(scaleP.Props, t, 2, func(p *anProp) []float64 { return []float64{p.PX, p.PY} }, false); ok {
				sx, sy = v[0], v[1]
			}
		}
		rot := 0.0
		if rotP != nil {
			if v, ok := anTrack(rotP.Props, t, 1, func(p *anProp) []float64 { return []float64{p.Rot} }, false); ok {
				rot = v[0]
			}
		}
		lc := col
		if colP != nil {
			if v, ok := anTrack(colP.Props, t, 4, func(p *anProp) []float64 {
				return []float64{float64(p.CR), float64(p.CG), float64(p.CB), float64(p.CA)}
			}, false); ok {
				for i := 0; i < 4; i++ {
					lc[i] *= math.Max(0, math.Min(255, v[i])) / 255
				}
			}
		}
		if lc[3] <= 0 || sx == 0 || sy == 0 {
			continue
		}
		r := rot * math.Pi / 180
		cs, sn := math.Cos(r), math.Sin(r)
		// y-down: translate(px,-py) * rotate(cw) * scale
		lm := anMul(m, [6]float64{cs * sx, -sn * sy, px, sn * sx, cs * sy, -py})
		if strings.HasSuffix(strings.ToLower(key.Sym), ".png") {
			im := c.image(key.Sym)
			if im == nil {
				continue
			}
			c.draws = append(c.draws, anDraw{img: im, m: lm, col: lc, add: key.Blend == 1,
				w: float64(im.Bounds().Dx()), h: float64(im.Bounds().Dy()), order: len(c.draws)})
		} else if sub := c.syms[key.Sym]; sub != nil {
			local := t - float64(key.Frame)
			if sub.Total > 0 {
				local = math.Mod(local, float64(sub.Total))
			}
			c.collect(sub, local, lm, lc, depth+1)
		}
	}
}

// layers: later layer index draws on top.
func (c *anCtx) sortLayers() {
	for _, s := range c.syms {
		sort.SliceStable(s.Layers, func(i, j int) bool { return s.Layers[i].Index < s.Layers[j].Index })
	}
}

func (d *anDraw) area() float64 {
	det := math.Abs(d.m[0]*d.m[4] - d.m[1]*d.m[3])
	return d.col[3] * det * d.w * d.h
}

func (d *anDraw) corners() [4][2]float64 {
	var o [4][2]float64
	for i, p := range [4][2]float64{{-d.w / 2, -d.h / 2}, {d.w / 2, -d.h / 2}, {d.w / 2, d.h / 2}, {-d.w / 2, d.h / 2}} {
		o[i] = [2]float64{d.m[0]*p[0] + d.m[1]*p[1] + d.m[2], d.m[3]*p[0] + d.m[4]*p[1] + d.m[5]}
	}
	return o
}

const anMaxSide = 1536

// dpAnimFrame renders symbol of the item's .aniproj at a representative frame:
// the first frame whose visible area reaches 85% of the best frame's. origin is
// the symbol origin inside the image (pixels, y down). ok=false when the
// project/symbol is missing or nothing is visible.
func dpAnimFrame(itemDir, symbol string) (img *image.RGBA, origin image.Point, ok bool) {
	c, ok := dpLoadAniproj(itemDir)
	if !ok {
		return nil, image.Point{}, false
	}
	c.sortLayers()
	s := c.syms[symbol]
	if s == nil {
		return nil, image.Point{}, false
	}
	total := s.Total
	if total < 1 {
		total = 1
	}
	step := 1
	if total > 120 {
		step = total / 120
	}
	ident := [6]float64{1, 0, 0, 0, 1, 0}
	white := [4]float64{1, 1, 1, 1}
	type fr struct {
		t    int
		area float64
	}
	var frames []fr
	best := 0.0
	for t := 0; t < total; t += step {
		c.draws = c.draws[:0]
		c.collect(s, float64(t), ident, white, 0)
		a := 0.0
		for i := range c.draws {
			a += c.draws[i].area()
		}
		frames = append(frames, fr{t, a})
		best = math.Max(best, a)
	}
	if best < 1 {
		return nil, image.Point{}, false
	}
	pick := 0
	for _, f := range frames {
		if f.area >= 0.85*best {
			pick = f.t
			break
		}
	}
	c.draws = c.draws[:0]
	c.collect(s, float64(pick), ident, white, 0)
	return c.raster(c.draws)
}

func (c *anCtx) raster(draws []anDraw) (*image.RGBA, image.Point, bool) {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	any := false
	for i := range draws {
		if draws[i].area() <= 0 {
			continue
		}
		any = true
		for _, p := range draws[i].corners() {
			minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
			minY, maxY = math.Min(minY, p[1]), math.Max(maxY, p[1])
		}
	}
	// the origin must be inside the canvas
	minX, minY = math.Min(minX, 0), math.Min(minY, 0)
	maxX, maxY = math.Max(maxX, 0), math.Max(maxY, 0)
	if !any {
		return nil, image.Point{}, false
	}
	const eps = 1e-6 // rotation leaves cos(90deg) residue
	x0, y0 := int(math.Floor(minX+eps)), int(math.Floor(minY+eps))
	w, h := int(math.Ceil(maxX-eps))-x0, int(math.Ceil(maxY-eps))-y0
	if w < 1 || h < 1 || w > anMaxSide || h > anMaxSide {
		return nil, image.Point{}, false
	}
	buf := make([]float32, w*h*4) // premultiplied rgba 0..1
	for i := range draws {
		d := &draws[i]
		if d.area() <= 0 {
			continue
		}
		det := d.m[0]*d.m[4] - d.m[1]*d.m[3]
		ia, ib := d.m[4]/det, -d.m[1]/det
		ic, id := -d.m[3]/det, d.m[0]/det
		cr := d.corners()
		bx0, by0, bx1, by1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, p := range cr {
			bx0, bx1 = math.Min(bx0, p[0]), math.Max(bx1, p[0])
			by0, by1 = math.Min(by0, p[1]), math.Max(by1, p[1])
		}
		px0, px1 := max(0, int(math.Floor(bx0))-x0), min(w, int(math.Ceil(bx1))-x0)
		py0, py1 := max(0, int(math.Floor(by0))-y0), min(h, int(math.Ceil(by1))-y0)
		for py := py0; py < py1; py++ {
			for px := px0; px < px1; px++ {
				dx := float64(px+x0) + 0.5 - d.m[2]
				dy := float64(py+y0) + 0.5 - d.m[5]
				u := ia*dx + ib*dy + d.w/2 - 0.5
				v := ic*dx + id*dy + d.h/2 - 0.5
				r, g, b, a := anBilinear(d.img, u, v)
				if a <= 0 {
					continue
				}
				ca := float32(d.col[3])
				r, g, b, a = r*float32(d.col[0])*ca, g*float32(d.col[1])*ca, b*float32(d.col[2])*ca, a*ca
				o := (py*w + px) * 4
				if d.add {
					buf[o] = min(1, buf[o]+r)
					buf[o+1] = min(1, buf[o+1]+g)
					buf[o+2] = min(1, buf[o+2]+b)
					buf[o+3] = max(buf[o+3], a)
					continue
				}
				k := 1 - a
				buf[o] = r + buf[o]*k
				buf[o+1] = g + buf[o+1]*k
				buf[o+2] = b + buf[o+2]*k
				buf[o+3] = a + buf[o+3]*k
			}
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		a := buf[i*4+3]
		if a <= 0 {
			continue
		}
		q := func(v float32) uint8 { return uint8(math.Round(float64(max(0, min(1, v))) * 255)) }
		out.Pix[i*4], out.Pix[i*4+1], out.Pix[i*4+2], out.Pix[i*4+3] = q(buf[i*4]), q(buf[i*4+1]), q(buf[i*4+2]), q(a)
	}
	return out, image.Pt(-x0, -y0), true
}

// anBilinear samples straight-alpha NRGBA at texel coords (u,v) -> premultiplied
// float rgb and alpha; texels outside the image are transparent.
func anBilinear(im *image.NRGBA, u, v float64) (r, g, b, a float32) {
	x0, y0 := int(math.Floor(u)), int(math.Floor(v))
	fx, fy := float32(u-float64(x0)), float32(v-float64(y0))
	W, H := im.Bounds().Dx(), im.Bounds().Dy()
	for j := 0; j < 2; j++ {
		for i := 0; i < 2; i++ {
			x, y := x0+i, y0+j
			if x < 0 || y < 0 || x >= W || y >= H {
				continue
			}
			wgt := (1 - fx + float32(i)*(2*fx-1)) * (1 - fy + float32(j)*(2*fy-1))
			o := y*im.Stride + x*4
			ta := float32(im.Pix[o+3]) / 255
			wa := wgt * ta
			r += wa * float32(im.Pix[o]) / 255
			g += wa * float32(im.Pix[o+1]) / 255
			b += wa * float32(im.Pix[o+2]) / 255
			a += wa
		}
	}
	return
}

// anNum is the lenient number read of the native loader: atof/atoi style,
// trailing junk (the old 0.48.x format writes values like "120l") is ignored.
func anNum(s string) float64 {
	k := 0
	for k < len(s) && (s[k] >= '0' && s[k] <= '9' || s[k] == '.' || s[k] == '-' || s[k] == '+' || s[k] == 'e' && k > 0) {
		k++
	}
	for k > 0 {
		if f, err := strconv.ParseFloat(s[:k], 64); err == nil {
			return f
		}
		k--
	}
	return 0
}

// processKeys maps the old (version 0.48.x) PROCESS_* element names to keys.
var anOldKeys = map[string]int{
	"PROCESS_SYMBOL": 0, "PROCESS_SCISSOR": 1, "PROCESS_ANCHOR": 2, "PROCESS_MOVE": 3,
	"PROCESS_SCALE": 4, "PROCESS_ROTATION": 5, "PROCESS_SKEW": 6, "PROCESS_COLOR": 7,
}

// anParse reads both aniproj dialects: version 1 nests <process><property/></process>
// in each <layer>; the old 0.48.x dialect puts one <PROCESS_X .../> per key directly
// under the layer.
func anParse(raw []byte) ([]anSymbol, bool) {
	d := xml.NewDecoder(bytes.NewReader(raw))
	var syms []anSymbol
	var sym *anSymbol
	var ly *anLayer
	var proc *anProc
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch e := tok.(type) {
		case xml.StartElement:
			at := map[string]string{}
			for _, a := range e.Attr {
				at[a.Name.Local] = a.Value
			}
			switch name := e.Name.Local; {
			case name == "symbol":
				syms = append(syms, anSymbol{Name: at["szName"], Total: int(anNum(at["dwTotalFrame"]))})
				sym, ly, proc = &syms[len(syms)-1], nil, nil
			case name == "layer" && sym != nil:
				sym.Layers = append(sym.Layers, anLayer{Index: int(anNum(at["dwIndex"]))})
				ly, proc = &sym.Layers[len(sym.Layers)-1], nil
			case name == "process" && ly != nil:
				ly.Procs = append(ly.Procs, anProc{Key: int(anNum(at["dwProcessKey"]))})
				proc = &ly.Procs[len(ly.Procs)-1]
			case (name == "property" && proc != nil) || (anOldKeys[name] != 0 || name == "PROCESS_SYMBOL") && ly != nil:
				p := anProp{Frame: int(anNum(at["dwFrame"])), Sym: at["szSymbolKey"], Keep: int(anNum(at["dwKeepFrame"])),
					Blend: int(anNum(at["dwBlendType"])), PX: anNum(at["fPointX"]), PY: anNum(at["fPointY"]),
					BX: anNum(at["fBezierX"]), BY: anNum(at["fBezierY"]), Rot: anNum(at["fRotation"]),
					CR: int(anNum(at["byColorR"])), CG: int(anNum(at["byColorG"])), CB: int(anNum(at["byColorB"])), CA: int(anNum(at["byColorA"]))}
				if v, ok := at["eInterpolate"]; ok {
					i := int(anNum(v))
					p.Interp = &i
				}
				if name != "property" {
					key := anOldKeys[name]
					found := false
					for i := range ly.Procs {
						if ly.Procs[i].Key == key {
							ly.Procs[i].Props = append(ly.Procs[i].Props, p)
							found = true
						}
					}
					if !found {
						ly.Procs = append(ly.Procs, anProc{Key: key, Props: []anProp{p}})
					}
				} else {
					proc.Props = append(proc.Props, p)
				}
			}
		case xml.EndElement:
			switch e.Name.Local {
			case "process":
				proc = nil
			case "layer":
				ly = nil
			}
		}
	}
	return syms, len(syms) > 0
}
