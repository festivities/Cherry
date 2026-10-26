package thumbs

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Skeleton placement for item previews. Native facts (libgame 10.1.0.0,
// AvItem::AttatchSprite @0x29b8150): every item part is a CCSprite (anchor
// 0.5,0.5) child of the body-node sprite named by the part's anchor key, placed
// at (x,y) = part CENTER in node-sprite space (origin bottom-left, y UP). A
// node's lower-left corner in avatar space is idlePos - anchor*spriteSize, with
// idlePos/anchor read from avatar_data/ani/{h,a}_f_idle.avb. The z field in the
// item file is already the global draw order (body-node base z + local index).

// dpAvatarDataRoot is the read-only APK assets/avatar_data tree (ani/{h,a}_f_idle.avb,
// body/{h,a}/*.png). Game assets are read in place, never committed (AGENTS.md).
var dpAvatarDataRoot = filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lineplay-original-assets/android-10.1.0.0-2024/avatar_data`)

// dpNodeNames is ms_szNodeName.
var dpNodeNames = [...]string{"none_body", "pelvis", "chest", "none_chest", "head", "none_head",
	"r_arm_0", "r_arm_1", "l_arm_0", "l_arm_1", "r_leg_0", "r_leg_1", "l_leg_0", "l_leg_1", "ear"}

// dpBodyZ is the body-node z table @0x3006038 for tags 1..14 (front view).
var dpBodyZ = [...]int{145, 163, 2, 183, 1, 109, 115, 229, 238, 125, 131, 149, 155, 180}

const (
	dpTagChest   = 2
	dpTagLArm0   = 8
	dpTagRArm0   = 6
	dpTagNoneChe = 3
	dpTagHead    = 4
	dpTagNoneHd  = 5
	dpTagEar     = 14
	dpTagSpecial = 17
	// noneHeadDY is the empirical vertical offset (avatar px, y up) of the
	// none_head frame from the head node position, fitted on authentic hair
	// previews; the idle rig has no none_head node.
	dpNoneHeadDY = 8
)

type dpSkelNode struct {
	px, py, ax, ay float64
	key0           float64 // rotation keyframe at t=0 (degrees, Cocos clockwise): the idle pose
	hasKey         bool
	parent         string
}

// dpRig is one body type's idle-pose frames and body sprites.
type dpRig struct {
	ll     map[int][2]float64 // node tag -> lower-left corner of the unrotated sprite, y up
	size   map[int][2]int     // node tag -> sprite size
	sprite map[int]image.Image
	rot    map[int]float64    // node tag -> idle rotation in degrees (Cocos clockwise, world)
	anc    map[int][2]float64 // node tag -> anchor point (the node's pivot) in sprite px from lower-left
}

// parseAvb reads node name -> idle position, anchor, parent and t=0 rotation out of
// an .avb file, skipping the other key lists (layout verified against the shipped files).
func parseAvb(d []byte) map[string]dpSkelNode {
	out := map[string]dpSkelNode{}
	if len(d) < 12 {
		return out
	}
	n := int(binary.LittleEndian.Uint16(d))
	f347, f348, f349, f350 := d[3] != 0, d[4] != 0, d[5] != 0, d[6] != 0
	o := 12
	list := func(sz int) bool {
		if o+2 > len(d) {
			return false
		}
		c := int(binary.LittleEndian.Uint16(d[o:]))
		o += 2 + c*sz
		return o <= len(d)
	}
	// rotList skips the rotation keys (time, degrees) and keeps the first value.
	var key0 float64
	var hasKey bool
	rotList := func() bool {
		if o+2 > len(d) {
			return false
		}
		c := int(binary.LittleEndian.Uint16(d[o:]))
		if c > 0 && o+2+8 <= len(d) {
			key0 = float64(math.Float32frombits(binary.LittleEndian.Uint32(d[o+6:])))
			hasKey = true
		}
		o += 2 + c*8
		return o <= len(d)
	}
	cstr := func(b []byte) string {
		if i := bytes.IndexByte(b, 0); i >= 0 {
			b = b[:i]
		}
		return string(b)
	}
	for i := 0; i < n; i++ {
		if o+33+20 > len(d) {
			break
		}
		name := cstr(d[o : o+16])
		parent := cstr(d[o+16 : o+32])
		o += 33
		var f [5]float64
		for k := range f {
			f[k] = float64(math.Float32frombits(binary.LittleEndian.Uint32(d[o+4*k:])))
		}
		o += 20
		key0, hasKey = 0, false
		ok := rotList()
		if ok && (name == "none_body" || f348) {
			ok = list(12)
		}
		if ok && f350 {
			ok = list(8) && list(8)
		}
		if ok && f349 {
			ok = list(8)
		}
		if ok && f347 {
			ok = list(12)
		}
		out[name] = dpSkelNode{px: f[0], py: f[1], ax: f[3], ay: f[4],
			key0: key0, hasKey: hasKey, parent: parent}
		if !ok {
			break
		}
	}
	return out
}

var dpRigs = struct {
	sync.Mutex
	m map[string]*dpRig
}{m: map[string]*dpRig{}}

// dpLoadRig loads the "h" (human) or "a" (animal) rig from the embedded data.
func dpLoadRig(set string) *dpRig {
	dpRigs.Lock()
	defer dpRigs.Unlock()
	if r, ok := dpRigs.m[set]; ok {
		return r
	}
	r := buildRig(set)
	dpRigs.m[set] = r
	return r
}

// dpIdleRotations returns each node's idle rotation in degrees (Cocos clockwise,
// world): its t=0 rotation key plus its parent's, so a forearm turns with its upper
// arm. A node without a rotation key (the ear) stays upright with its parent.
func dpIdleRotations(nodes map[string]dpSkelNode) map[string]float64 {
	out := map[string]float64{}
	var rot func(name string, depth int) float64
	rot = func(name string, depth int) float64 {
		if v, ok := out[name]; ok {
			return v
		}
		nd, ok := nodes[name]
		if !ok || depth > 32 {
			return 0
		}
		v := 0.0
		if nd.hasKey {
			v = nd.key0
		}
		if _, ok := nodes[nd.parent]; ok {
			v += rot(nd.parent, depth+1)
		}
		out[name] = v
		return v
	}
	for name := range nodes {
		rot(name, 0)
	}
	return out
}

func buildRig(set string) *dpRig {
	avb, err := os.ReadFile(filepath.Join(dpAvatarDataRoot, "ani", set+"_f_idle.avb"))
	if err != nil {
		return nil
	}
	nodes := parseAvb(avb)
	rots := dpIdleRotations(nodes)
	r := &dpRig{ll: map[int][2]float64{}, size: map[int][2]int{}, sprite: map[int]image.Image{},
		rot: map[int]float64{}, anc: map[int][2]float64{}}
	for tag, name := range dpNodeNames {
		nd, ok := nodes[name]
		if !ok {
			continue
		}
		var w, h int
		if b, err := os.ReadFile(filepath.Join(dpAvatarDataRoot, "body", set, name+".png")); err == nil {
			if img, err := png.Decode(bytes.NewReader(b)); err == nil {
				if set == "h" && (tag == dpTagHead || tag == dpTagEar) {
					img = dpSkinTint(img)
				}
				r.sprite[tag] = img
				w, h = img.Bounds().Dx(), img.Bounds().Dy()
			}
		}
		r.size[tag] = [2]int{w, h}
		r.rot[tag] = rots[name]
		r.anc[tag] = [2]float64{nd.ax * float64(w), nd.ay * float64(h)}
		r.ll[tag] = [2]float64{nd.px - nd.ax*float64(w), nd.py - nd.ay*float64(h)}
	}
	if hd, ok := nodes["head"]; ok {
		r.ll[dpTagNoneHd] = [2]float64{hd.px, hd.py + dpNoneHeadDY}
	}
	if ch, ok := nodes["chest"]; ok {
		r.ll[dpTagNoneChe] = [2]float64{ch.px, ch.py}
	}
	if nb, ok := nodes["none_body"]; ok {
		r.ll[0] = [2]float64{nb.px, nb.py}
	}
	// Anchor 17 has no body node; its parts and animations hang off the root (none_body).
	if root, ok := r.ll[0]; ok {
		r.ll[dpTagSpecial] = root
	}
	// The ear node position is local to the head sprite (not avatar space).
	if hl, ok := r.ll[dpTagHead]; ok {
		if ear, ok2 := nodes["ear"]; ok2 && r.sprite[dpTagEar] != nil {
			w, h := float64(r.size[dpTagEar][0]), float64(r.size[dpTagEar][1])
			r.ll[dpTagEar] = [2]float64{hl[0] + ear.px - w/2, hl[1] + ear.py - h/2}
		}
	}
	return r
}

// dpLayer is one sprite in avatar space; x,y is its lower-left, y up.
type dpLayer struct {
	z, seq int
	img    image.Image
	x, y   float64
}

// point maps (px, py), a point in node-sprite space (origin lower-left, y up), to
// avatar space through the node's idle rotation about its anchor point.
func (r *dpRig) point(tag int, px, py float64) (x, y float64) {
	ll, a, theta := r.ll[tag], r.anc[tag], r.rot[tag]
	if theta == 0 {
		return ll[0] + px, ll[1] + py
	}
	c, s := math.Cos(theta*math.Pi/180), math.Sin(theta*math.Pi/180)
	dx, dy := px-a[0], py-a[1]
	return ll[0] + a[0] + c*dx + s*dy, ll[1] + a[1] - s*dx + c*dy
}

// place puts a part sprite on its node. The record's (px, py) is the part's centre
// in node space; the part is a child of the node sprite, so it turns with the node's
// idle rotation. It returns the sprite to draw (turned when the node is), its
// avatar-space lower-left corner, and ok=false when the node has no frame in the rig.
func (r *dpRig) place(anchor int, px, py float64, img image.Image) (image.Image, float64, float64, bool) {
	if _, found := r.ll[anchor]; !found {
		return nil, 0, 0, false
	}
	b := img.Bounds()
	cx, cy := r.point(anchor, px, py)
	if r.rot[anchor] == 0 {
		return img, cx - float64(b.Dx())/2, cy - float64(b.Dy())/2, true
	}
	turned, x, y := dpTurn(img, r.rot[anchor], [2]float64{float64(b.Dx()) / 2, float64(b.Dy()) / 2}, [2]float64{cx, cy})
	return turned, x, y, true
}

// dpTurn turns img by deg (Cocos clockwise, y up) about the image point pin (px,
// origin lower-left) and puts that point at avatar position at. It returns the turned
// image and the avatar-space lower-left corner of its canvas (nearest-neighbour).
func dpTurn(img image.Image, deg float64, pin, at [2]float64) (*image.RGBA, float64, float64) {
	src := dpToRGBA(img)
	w, h := float64(src.Rect.Dx()), float64(src.Rect.Dy())
	th := deg * math.Pi / 180
	c, s := math.Cos(th), math.Sin(th)
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, q := range [4][2]float64{{0, 0}, {w, 0}, {0, h}, {w, h}} {
		dx, dy := q[0]-pin[0], q[1]-pin[1]
		x, y := at[0]+c*dx+s*dy, at[1]-s*dx+c*dy
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
	}
	cw, ch := int(math.Ceil(maxX-minX))+2, int(math.Ceil(maxY-minY))+2
	ox, oy := minX-1, minY-1
	out := image.NewRGBA(image.Rect(0, 0, cw, ch))
	for Y := 0; Y < ch; Y++ {
		ay := oy + float64(ch-Y) - 0.5
		for X := 0; X < cw; X++ {
			ax := ox + float64(X) + 0.5
			dx, dy := ax-at[0], ay-at[1]
			// inverse map: the source point is pin + R(theta)^T (avatar - at)
			qx := pin[0] + c*dx - s*dy
			qy := pin[1] + s*dx + c*dy
			u, v := int(math.Floor(qx)), int(math.Floor(h-qy))
			if u < 0 || v < 0 || u >= int(w) || v >= int(h) {
				continue
			}
			so := v*src.Stride + u*4
			do := Y*out.Stride + X*4
			copy(out.Pix[do:do+4], src.Pix[so:so+4])
		}
	}
	return out, ox, oy
}

// dpToRGBA returns img as *image.RGBA with its own origin at (0,0).
func dpToRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return &image.RGBA{Pix: r.Pix, Stride: r.Stride, Rect: image.Rect(0, 0, r.Rect.Dx(), r.Rect.Dy())}
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// bodyLayers returns the default bald head (+ear) at the table z order.
func (r *dpRig) headLayers() []dpLayer {
	var out []dpLayer
	for _, tag := range []int{dpTagEar, dpTagHead} {
		img := r.sprite[tag]
		ll, ok := r.ll[tag]
		if img == nil || !ok {
			continue
		}
		out = append(out, dpLayer{z: dpBodyZ[tag-1], seq: -1, img: img, x: ll[0], y: ll[1]})
	}
	return out
}

// dpRasterize paints layers in (z, seq) order into a tight RGBA with pad px
// around the union of the layer rectangles (y flipped to image space). ox, oy
// is the image position of the avatar-space origin (image y = oy - avatar y).
func dpRasterize(layers []dpLayer, pad int) (img *image.RGBA, ox, oy float64) {
	if len(layers) == 0 {
		return nil, 0, 0
	}
	sort.SliceStable(layers, func(i, j int) bool {
		if layers[i].z != layers[j].z {
			return layers[i].z < layers[j].z
		}
		return layers[i].seq < layers[j].seq
	})
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, l := range layers {
		b := l.img.Bounds()
		minX = math.Min(minX, l.x)
		minY = math.Min(minY, l.y)
		maxX = math.Max(maxX, l.x+float64(b.Dx()))
		maxY = math.Max(maxY, l.y+float64(b.Dy()))
	}
	w := int(math.Round(maxX-minX)) + pad*2
	h := int(math.Round(maxY-minY)) + pad*2
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil, 0, 0
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	for _, l := range layers {
		b := l.img.Bounds()
		x0 := int(math.Round(l.x-minX)) + pad
		y0 := int(math.Round(maxY-(l.y+float64(b.Dy())))) + pad
		draw.Draw(canvas, image.Rect(x0, y0, x0+b.Dx(), y0+b.Dy()), l.img, b.Min, draw.Over)
	}
	return canvas, float64(pad) - minX, maxY + float64(pad)
}

// dpSkinTint multiplies the human head/ear sprites (a near-white skin base) by
// the default skin colour (253,204,166) the authentic previews show.
func dpSkinTint(img image.Image) image.Image {
	b := img.Bounds()
	out := image.NewNRGBA(b)
	draw.Draw(out, b, img, b.Min, draw.Src)
	for i := 0; i+3 < len(out.Pix); i += 4 {
		out.Pix[i] = uint8(int(out.Pix[i]) * 253 / 255)
		out.Pix[i+1] = uint8(min(255, int(out.Pix[i+1])*204/227))
		out.Pix[i+2] = uint8(min(255, int(out.Pix[i+2])*166/210))
	}
	return out
}
