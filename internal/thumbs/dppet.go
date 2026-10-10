package thumbs

import (
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

// Pet shop thumbnails. Authentic pet dp.png files are 260x200; the santi dump lacks about 15
// of them, so the first frame of the pet's own "idle" animation is composed instead. The
// skeleton.xml / texture.xml files are DragonBones 2.3 data. Bone `transform` values are
// GLOBAL (armature space, y down) in this export and children do not inherit them: a slot is
// placed with its bone's idle frame-0 transform and its display transform. The origin and
// the 1:1 scale were fitted against authentic dp.png files of four pets (bounding boxes
// within about 2 px). A pet too large for the canvas at that scale (ride pets) is scaled down
// and centred instead (petFit).

const (
	dpPetW, dpPetH   = 260, 200
	dpPetOX, dpPetOY = 138.0, 151.0 // armature origin on the canvas
	dpPetMargin      = 6.0          // clearance kept between a scaled pet and the canvas edges
)

type pkTransform struct {
	X   string `xml:"x,attr"`
	Y   string `xml:"y,attr"`
	SkX string `xml:"skX,attr"`
	SkY string `xml:"skY,attr"`
	ScX string `xml:"scX,attr"`
	ScY string `xml:"scY,attr"`
	PX  string `xml:"pX,attr"`
	PY  string `xml:"pY,attr"`
}

func pkNum(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
		return v
	}
	return def
}

// affine is [a b c d tx ty]: x' = a*x + c*y + tx, y' = b*x + d*y + ty.
type affine [6]float64

func (t pkTransform) matrix() affine {
	kx, ky := pkNum(t.SkX, 0)*math.Pi/180, pkNum(t.SkY, 0)*math.Pi/180
	sx, sy := pkNum(t.ScX, 1), pkNum(t.ScY, 1)
	return affine{sx * math.Cos(ky), sx * math.Sin(ky), -sy * math.Sin(kx), sy * math.Cos(kx), pkNum(t.X, 0), pkNum(t.Y, 0)}
}

func (p affine) mul(c affine) affine { // apply c, then p
	return affine{p[0]*c[0] + p[2]*c[1], p[1]*c[0] + p[3]*c[1], p[0]*c[2] + p[2]*c[3], p[1]*c[2] + p[3]*c[3],
		p[0]*c[4] + p[2]*c[5] + p[4], p[1]*c[4] + p[3]*c[5] + p[5]}
}

type pkSkeleton struct {
	Armatures []struct {
		Skins []struct {
			Slots []struct {
				Name     string `xml:"name,attr"`
				Parent   string `xml:"parent,attr"`
				Z        string `xml:"z,attr"`
				Displays []struct {
					Name      string       `xml:"name,attr"`
					Type      string       `xml:"type,attr"`
					Transform *pkTransform `xml:"transform"`
				} `xml:"display"`
			} `xml:"slot"`
		} `xml:"skin"`
		Animations []struct {
			Name      string `xml:"name,attr"`
			Timelines []struct {
				Name   string `xml:"name,attr"`
				Frames []struct {
					Z            string       `xml:"z,attr"`
					DisplayIndex string       `xml:"displayIndex,attr"`
					Transform    *pkTransform `xml:"transform"`
					Color        *struct {
						AM string `xml:"aM,attr"`
					} `xml:"colorTransform"`
				} `xml:"frame"`
			} `xml:"timeline"`
		} `xml:"animation"`
	} `xml:"armature"`
}

type pkAtlas struct {
	Sub []struct {
		Name string `xml:"name,attr"`
		X    int    `xml:"x,attr"`
		Y    int    `xml:"y,attr"`
		W    int    `xml:"width,attr"`
		H    int    `xml:"height,attr"`
	} `xml:"SubTexture"`
}

type petLayer struct {
	z      int
	img    *image.NRGBA
	m      affine
	px, py float64
	alpha  float64
}

func readXML(path string, v any) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return xml.NewDecoder(f).Decode(v) == nil
}

// composePetDP renders the idle pose of a pet folder onto the 260x200 shop canvas.
func composePetDP(dir string) (*image.RGBA, bool) {
	var sk pkSkeleton
	var atlas pkAtlas
	if !readXML(filepath.Join(dir, "skeleton.xml"), &sk) || !readXML(filepath.Join(dir, "texture.xml"), &atlas) || len(sk.Armatures) == 0 {
		return nil, false
	}
	f, err := os.Open(filepath.Join(dir, "texture.png"))
	if err != nil {
		return nil, false
	}
	tex, err := png.Decode(f)
	f.Close()
	if err != nil {
		return nil, false
	}
	b := tex.Bounds()
	rect := map[string]image.Rectangle{}
	for _, s := range atlas.Sub {
		r := image.Rect(s.X, s.Y, s.X+s.W, s.Y+s.H)
		if s.W > 0 && s.H > 0 && r.In(b) {
			rect[s.Name] = r
		}
	}
	arm := sk.Armatures[0]
	if len(arm.Skins) == 0 {
		return nil, false
	}
	type frame0 struct {
		tr    *pkTransform
		z     string
		disp  string
		alpha float64
	}
	idle := map[string]frame0{}
	for _, a := range arm.Animations {
		if a.Name != "idle" {
			continue
		}
		for _, t := range a.Timelines {
			if len(t.Frames) == 0 {
				continue
			}
			fr, al := t.Frames[0], 1.0
			if fr.Color != nil {
				al = math.Min(math.Max(pkNum(fr.Color.AM, 100)/100, 0), 1)
			}
			idle[t.Name] = frame0{fr.Transform, fr.Z, fr.DisplayIndex, al}
		}
		break
	}
	var layers []petLayer
	for _, s := range arm.Skins[0].Slots {
		fr, ok := idle[s.Name] // slots with no idle timeline are the back view or hidden extras
		if !ok || fr.tr == nil || strings.HasPrefix(s.Name, "ef_") || strings.HasPrefix(s.Name, "e_") || s.Name == "bubble" || s.Name == "frisbee" {
			continue
		}
		di := int(pkNum(fr.disp, 0))
		if di < 0 || di >= len(s.Displays) {
			continue
		}
		d := s.Displays[di]
		r, ok := rect[d.Name]
		if !ok || d.Type == "armature" || d.Transform == nil {
			continue
		}
		img := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		for y := 0; y < r.Dy(); y++ {
			for x := 0; x < r.Dx(); x++ {
				img.Set(x, y, color.NRGBAModel.Convert(tex.At(r.Min.X+x, r.Min.Y+y)))
			}
		}
		layers = append(layers, petLayer{z: int(pkNum(fr.z, pkNum(s.Z, 0))), img: img,
			m: fr.tr.matrix().mul(d.Transform.matrix()), px: pkNum(d.Transform.PX, float64(r.Dx())/2), py: pkNum(d.Transform.PY, float64(r.Dy())/2), alpha: fr.alpha})
	}
	if len(layers) == 0 {
		return nil, false
	}
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].z < layers[j].z })
	view := petFit(layers)
	out := image.NewRGBA(image.Rect(0, 0, dpPetW, dpPetH))
	for _, l := range layers {
		petBlit(out, l, view)
	}
	return out, true
}

// petFit returns the armature-to-canvas transform. When every layer's image corners fit inside
// the canvas with dpPetMargin at the usual origin, that is the plain origin (pixel-identical to
// the fitted 1:1 render). Otherwise the whole pose is scaled down uniformly (never up) and its
// bounding box is centred on the canvas.
func petFit(layers []petLayer) affine {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, l := range layers {
		w, h := float64(l.img.Bounds().Dx()), float64(l.img.Bounds().Dy())
		// the image spans [-px, w-px] x [-py, h-py] in armature space (petBlit's pivot)
		for _, c := range [4][2]float64{{-l.px, -l.py}, {w - l.px, -l.py}, {w - l.px, h - l.py}, {-l.px, h - l.py}} {
			x := l.m[0]*c[0] + l.m[2]*c[1] + l.m[4] + dpPetOX
			y := l.m[1]*c[0] + l.m[3]*c[1] + l.m[5] + dpPetOY
			minX, maxX = math.Min(minX, x), math.Max(maxX, x)
			minY, maxY = math.Min(minY, y), math.Max(maxY, y)
		}
	}
	if minX >= dpPetMargin && minY >= dpPetMargin && maxX <= dpPetW-dpPetMargin && maxY <= dpPetH-dpPetMargin {
		return affine{1, 0, 0, 1, dpPetOX, dpPetOY}
	}
	s := math.Min(1, math.Min((dpPetW-2*dpPetMargin)/(maxX-minX), (dpPetH-2*dpPetMargin)/(maxY-minY)))
	cx, cy := (minX+maxX)/2, (minY+maxY)/2
	return affine{s, 0, 0, s, dpPetW/2 + s*(dpPetOX-cx), dpPetH/2 + s*(dpPetOY-cy)}
}

// petBlit draws one layer with inverse mapping and bilinear sampling (source-over). view maps
// the armature space onto the canvas.
func petBlit(dst *image.RGBA, l petLayer, view affine) {
	m := view.mul(l.m)
	det := m[0]*m[3] - m[1]*m[2]
	if math.Abs(det) < 1e-9 {
		return
	}
	ia, ib, ic, id := m[3]/det, -m[1]/det, -m[2]/det, m[0]/det
	w, h := l.img.Bounds().Dx(), l.img.Bounds().Dy()
	for y := 0; y < dpPetH; y++ {
		for x := 0; x < dpPetW; x++ {
			dx, dy := float64(x)+0.5-m[4], float64(y)+0.5-m[5]
			u, v := ia*dx+ic*dy+l.px-0.5, ib*dx+id*dy+l.py-0.5
			if u < -1 || v < -1 || u > float64(w) || v > float64(h) {
				continue
			}
			r, g, b, a := petSample(l.img, u, v)
			if a <= 0 {
				continue
			}
			a *= l.alpha
			r, g, b = r*l.alpha, g*l.alpha, b*l.alpha // premultiplied
			i := dst.PixOffset(x, y)
			inv := 1 - a
			dst.Pix[i] = uint8(math.Min(255, r*255+float64(dst.Pix[i])*inv) + 0.5)
			dst.Pix[i+1] = uint8(math.Min(255, g*255+float64(dst.Pix[i+1])*inv) + 0.5)
			dst.Pix[i+2] = uint8(math.Min(255, b*255+float64(dst.Pix[i+2])*inv) + 0.5)
			dst.Pix[i+3] = uint8(math.Min(255, a*255+float64(dst.Pix[i+3])*inv) + 0.5)
		}
	}
}

// petSample bilinearly samples img at (u,v) (texel centres at integers); outside is transparent.
// It returns premultiplied r,g,b and alpha in 0..1.
func petSample(img *image.NRGBA, u, v float64) (r, g, b, a float64) {
	x0, y0 := int(math.Floor(u)), int(math.Floor(v))
	fx, fy := u-float64(x0), v-float64(y0)
	bd := img.Bounds()
	for dy := 0; dy < 2; dy++ {
		for dx := 0; dx < 2; dx++ {
			wgt := (1 - fx) * (1 - fy)
			switch {
			case dx == 1 && dy == 0:
				wgt = fx * (1 - fy)
			case dx == 0 && dy == 1:
				wgt = (1 - fx) * fy
			case dx == 1 && dy == 1:
				wgt = fx * fy
			}
			x, y := x0+dx, y0+dy
			if wgt == 0 || x < bd.Min.X || y < bd.Min.Y || x >= bd.Max.X || y >= bd.Max.Y {
				continue
			}
			c := img.NRGBAAt(x, y)
			ca := float64(c.A) / 255
			a += wgt * ca
			r += wgt * ca * float64(c.R) / 255
			g += wgt * ca * float64(c.G) / 255
			b += wgt * ca * float64(c.B) / 255
		}
	}
	return
}
