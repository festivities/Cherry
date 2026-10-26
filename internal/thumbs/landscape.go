package thumbs

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"sort"
	"strings"
)

// Generated Garden landscapes (the v6 generator, landscape-gen-20261026 gen/v2/make_v6.py, with the helpers of
// make_v2..v4 and objs.py). The picture is the room seen from the cube's front: two walls with the map's
// sky, the floor tiles, the map's fence pieces along the back edges, scenery on the edges, one centre prop.
// The steps and their order follow the Python reference; ponytail: marks the places that differ from it.

// landscapeHex is the outline of a landscape picture (the open cube of the archive's dp.png files, 130x100).
var landscapeHex = [][2]float64{{14, 30.5}, {65, 5}, {116, 30.5}, {116, 70.5}, {65, 96}, {14, 70.5}}

// Geometry of the picture, in 1x pixels unless a name says otherwise. The cube's back vertex is (65, 45), its
// walls rise 40 px to the apex (65, 5), and the room is lsN x lsN map tiles of lsK px per step.
const (
	lsW, lsH    = 130, 100
	lsSS        = 4 // the canvas is drawn at 4x and box-downscaled
	lsN         = 5
	lsK         = 51.0 / lsN // screen step per tile along each axis (the floor's half-width is 51 px)
	lsEdgeX     = 51.0       // x-extent of one floor edge
	lsWallAxis  = 57.0       // |(51, 25.5)|: a wall's width measured along its top edge
	lsFenceL    = lsEdgeX / 3
	lsFenceSpan = 2 * lsFenceL // each fence piece spans two spacings, so neighbours overlap
	lsKRight    = 0.5          // slope of the right floor edge
	lsKLeft     = -0.5         // slope of the left floor edge
	lsSlopeTol  = 0.08         // a fence piece's foot slope must be within this of its edge's slope
	lsKeptMin   = 0.30         // share of foot columns on the fitted line: a railing steps, a ridge is 1, noise < 0.3
	lsKeptMax   = 0.95
)

// lsSpawn is the owner's spawn tile: the centre prop is the upright object nearest to it.
var lsSpawn = [2]float64{30, 39}

// Sky is the backdrop of a generated landscape (gardens.json "sky"): the wall colour at the top edge and at
// the bottom edge of the walls, and whether the walls carry the map's star sprites.
type Sky struct {
	Top, Bottom color.NRGBA
	Stars       bool
}

// key names the sky in cache file names, so changed colours never serve an old thumbnail.
func (s Sky) key() string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE(fmt.Appendf(nil, "%v%v%t", s.Top, s.Bottom, s.Stars)))
}

// LandscapeArchived reports whether the archive holds the map pack that a generated landscape of stem renders
// from (tests that serve dp.png skip without it).
func LandscapeArchived(stem string) bool {
	_, ok := mapPackPath(stem)
	return ok
}

// LandscapeDP is the dp.png of a generated Garden landscape: the map stem rendered from its archive pack with
// the given sky. A cached PNG under DpCacheDir is served without rendering.
func LandscapeDP(stem string, sky Sky) ([]byte, error) {
	cachePath := ""
	if DpCacheDir != "" {
		cachePath = dpCachePath("landscape", stem+"-"+sky.key())
		if b, err := os.ReadFile(cachePath); err == nil {
			return b, nil
		}
	}
	mu := dpFlight("landscape/" + stem + "/" + sky.key())
	mu.Lock()
	defer mu.Unlock()
	if cachePath != "" {
		if b, err := os.ReadFile(cachePath); err == nil {
			return b, nil
		}
	}
	img, _, err := renderLandscape(stem, sky)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if cachePath != "" {
		if err := os.MkdirAll(DpCacheDir, 0o755); err == nil {
			dpAtomicWrite(cachePath, b)
		}
	}
	return b, nil
}

// landscapePicks is what the generator chose on a map: the window origin, the decal counts, the fence code on
// each back edge ("" for no run) and the centre prop. The golden test checks them against v6_objects.json.
type landscapePicks struct {
	Window        [2]int
	Clouds, Stars int
	FenceR        string
	FenceL        string
	Prop          string
}

// lsScene is one map's objects, with the item art they need read once per code.
type lsScene struct {
	objs []lsObject
	arts map[string]*lsArt
}

// art is the item art of code, read on first use.
func (sc *lsScene) art(code string) *lsArt {
	a, ok := sc.arts[code]
	if !ok {
		a = loadLsArt(code)
		sc.arts[code] = a
	}
	return a
}

// renderLandscape draws map stem with sky and returns the 130x100 picture with the picks it made.
func renderLandscape(stem string, sky Sky) (*image.NRGBA, landscapePicks, error) {
	var picks landscapePicks
	path, ok := mapPackPath(stem)
	if !ok {
		return nil, picks, fmt.Errorf("garden map %s: no pack in the archive", stem)
	}
	pk, err := readMapPack(path)
	if err != nil {
		return nil, picks, err
	}
	objs, err := pk.objects()
	if err != nil {
		return nil, picks, err
	}
	if len(objs) == 0 {
		return nil, picks, fmt.Errorf("garden map %s: no objects", stem)
	}
	atlas, err := pk.floorAtlas()
	if err != nil {
		return nil, picks, err
	}
	diamond, err := floorDiamond(atlas)
	if err != nil {
		return nil, picks, err
	}
	sc := &lsScene{objs: objs, arts: map[string]*lsArt{}}
	x0, y0 := lsWindow(objs)
	picks.Window = [2]int{x0, y0}

	canvas := newPmap(lsW*lsSS, lsH*lsSS)
	masks := map[byte][]float32{
		'L': lsPolyMask([][2]float64{{65, 45}, {14, 70.5}, {14, 30.5}, {65, 5}}),
		'R': lsPolyMask([][2]float64{{65, 45}, {116, 70.5}, {116, 30.5}, {65, 5}}),
	}
	canvas.over(lsWallLayer(sky, 0.92, masks['L']), 0, 0)
	canvas.over(lsWallLayer(sky, 1.0, masks['R']), 0, 0)

	// Backdrop decals: the cloud and star parts of the RUTD objects, flat on the walls (clouds 10 at most,
	// stars 16 and only where the sky has them).
	type decal struct {
		kind string
		spr  *image.NRGBA
		o    lsObject
	}
	var decals []decal
	for _, o := range objs {
		if !strings.HasPrefix(o.code, "RUTD") {
			continue
		}
		a := sc.art(o.code)
		if a.cloud != nil {
			decals = append(decals, decal{"cloud", a.cloud, o})
		}
		if a.star != nil {
			decals = append(decals, decal{"star", a.star, o})
		}
	}
	sort.SliceStable(decals, func(i, j int) bool {
		a, b := decals[i], decals[j]
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.o.code != b.o.code {
			return a.o.code < b.o.code
		}
		if a.o.tx != b.o.tx {
			return a.o.tx < b.o.tx
		}
		return a.o.ty < b.o.ty
	})
	nCloud, nStar := 0, 0
	for _, d := range decals {
		tx, ty := int64(d.o.tx), int64(d.o.ty)
		var frac float64
		if d.kind == "cloud" {
			if nCloud >= 10 {
				continue
			}
			nCloud++
			frac = 0.15 + 0.15*lsHash(tx, ty, 41)
		} else {
			if !sky.Stars || nStar >= 16 {
				continue
			}
			nStar++
			frac = 0.05 + 0.04*lsHash(tx, ty, 42)
		}
		side := byte('R')
		if lsHash(tx, ty, 43) < 0.5 {
			side = 'L'
		}
		a := frac/2.0 + 0.02 + (1.0-frac-0.04)*lsHash(tx, ty, 44)
		h := 0.52 + 0.33*lsHash(tx, ty, 45)
		lsPlaceDecal(canvas, d.spr, side, a, h, frac, masks[side])
	}
	picks.Clouds, picks.Stars = nCloud, nStar

	// Floor: the atlas diamond on every tile of the room.
	floor := newPmap(canvas.w, canvas.h)
	tile := premul(diamond).resize(max(1, pyRound(2*lsK*lsSS)), max(1, pyRound(lsK*lsSS)))
	for i := range lsN {
		for j := range lsN {
			sx, sy := lsProj(float64(i), float64(j))
			floor.over(tile, pyRound((sx-lsK)*lsSS), pyRound(sy*lsSS))
		}
	}
	canvas.over(floor, 0, 0)

	// Fence runs: the most placed wide, sparse objects (the 12 most common) whose foot line has the edge's slope.
	var placed []string
	for _, o := range objs {
		if isPropCat(o.code) && !sc.art(o.code).wall {
			placed = append(placed, o.code)
		}
	}
	var cands []string
	for _, code := range lsMostCommon(placed, 12) {
		if lsIsFence(sc.art(code).main) {
			cands = append(cands, code)
		}
	}
	fenceCodes := map[string]bool{}
	for _, side := range []byte{'R', 'L'} {
		k := lsKRight
		if side == 'L' {
			k = lsKLeft
		}
		for _, code := range cands {
			own := sc.art(code).main
			pts := lsFoot(own)
			m, b := lsFit(pts)
			kept := 0.0
			if len(pts) > 0 {
				kept = float64(lsCountOn(pts, m, b)) / float64(len(pts))
			}
			art, mArt := own, m
			if m*k <= 0 { // the mirror of the piece runs the other way
				art, mArt = lsFlipH(own), -m
			}
			if math.Abs(mArt-k) > lsSlopeTol || kept < lsKeptMin || kept > lsKeptMax {
				continue
			}
			lsFenceRun(canvas, art, side, k)
			if side == 'R' {
				picks.FenceR = code
			} else {
				picks.FenceL = code
			}
			fenceCodes[code] = true
			break
		}
	}
	propExcl := map[string]bool{}
	if len(cands) > 0 {
		propExcl[cands[0]] = true
	}
	for code := range fenceCodes {
		propExcl[code] = true
	}

	// Scenery on the back edges, nearest the back corner first.
	type edgeObj struct {
		sum float64
		o   lsObject
	}
	var edge []edgeObj
	for _, o := range objs {
		u, v := o.tx-float64(x0), o.ty-float64(y0)
		if !lsOnEdge(u, v) || !isPropCat(o.code) || propExcl[o.code] {
			continue
		}
		a := sc.art(o.code)
		if a.wall || a.main == nil || lsIsFence(a.main) || lsIsFlat(a.main) {
			continue
		}
		edge = append(edge, edgeObj{u + v, o})
	}
	sort.SliceStable(edge, func(i, j int) bool { return edge[i].sum < edge[j].sum })
	for _, e := range edge {
		ax, ay := lsProj(e.o.tx-float64(x0), e.o.ty-float64(y0))
		lsPasteScaled(canvas, sc.art(e.o.code).main, ax, ay, 2.6*lsK, 34)
	}

	// Centre prop: the upright decoration nearest the spawn (pass 1 also skips the fence's family).
	type propCand struct {
		score float64
		code  string
		spr   *image.NRGBA
	}
	var best *propCand
	for pass := 1; pass <= 2 && best == nil; pass++ {
		for _, o := range objs {
			code := o.code
			if !isPropCat(code) || propExcl[code] || sc.art(code).wall {
				continue
			}
			if pass == 1 && lsFamilyExcluded(code, propExcl) {
				continue
			}
			spr := sc.art(code).main
			if spr == nil || !lsHasAlpha(spr) || lsIsFence(spr) || lsIsGlow(spr) || lsIsFlat(spr) {
				continue
			}
			d := math.Hypot(o.tx-lsSpawn[0], o.ty-lsSpawn[1])
			w, h := float64(spr.Bounds().Dx()), float64(spr.Bounds().Dy())
			var score float64
			if h >= w && h >= 30 {
				score = math.Min(h, 120) / (1.0 + d/8.0)
			} else {
				score = 0.25 * math.Min(h, 120) / (1.0 + d/8.0)
			}
			if best == nil || score > best.score {
				best = &propCand{score, code, spr}
			}
		}
	}
	if best != nil {
		cx, cy := lsProj(lsN/2.0, lsN/2.0)
		lsPasteScaled(canvas, best.spr, cx, cy+6, 48, 46)
		picks.Prop = best.code
	}

	// Clip to the hexagon and box-downscale to the picture.
	hex := lsPolyMask(landscapeHex)
	for i, a := range hex {
		for c := range 4 {
			canvas.p[4*i+c] *= a
		}
	}
	return canvas.boxStraight(lsSS), picks, nil
}

// lsWindow is the N x N window whose back two edges carry the most objects: its origin tile (x0, y0). Ties
// keep the first origin in scan order.
func lsWindow(objs []lsObject) (int, int) {
	minX, maxX, minY, maxY := objs[0].tx, objs[0].tx, objs[0].ty, objs[0].ty
	for _, o := range objs {
		minX, maxX = math.Min(minX, o.tx), math.Max(maxX, o.tx)
		minY, maxY = math.Min(minY, o.ty), math.Max(maxY, o.ty)
	}
	bestX, bestY, bestScore := 0, 0, -1
	for x := int(minX) - 1; x < int(maxX)+1; x++ {
		for y := int(minY) - 1; y < int(maxY)+1; y++ {
			score := 0
			for _, o := range objs {
				if lsOnEdge(o.tx-float64(x), o.ty-float64(y)) {
					score++
				}
			}
			if score > bestScore {
				bestX, bestY, bestScore = x, y, score
			}
		}
	}
	return bestX, bestY
}

// lsOnEdge reports whether tile (u, v), relative to the window origin, lies in the window and on one of its
// two back edges.
func lsOnEdge(u, v float64) bool {
	return -0.5 <= u && u <= lsN+0.5 && -0.5 <= v && v <= lsN+0.5 && (math.Abs(u) <= 0.6 || math.Abs(v) <= 0.6)
}

// lsProj maps tile (u, v), relative to the room origin, to canvas pixels: the cube's back vertex is (65, 45).
func lsProj(u, v float64) (float64, float64) {
	return 65 + (u-v)*lsK, 45 + (u+v)*lsK/2
}

// lsHash is the per-tile value in [0, 1) that picks decal placements (make_v3 hash01).
func lsHash(tx, ty, salt int64) float64 {
	v := (tx * 73856093) ^ (ty * 19349663) ^ (salt * 83492791)
	m := v % 1000
	if m < 0 {
		m += 1000
	}
	return float64(m) / 1000
}

// lsMostCommon is collections.Counter.most_common(n) over codes: the distinct codes, most frequent first, ties
// in first-seen order.
func lsMostCommon(codes []string, n int) []string {
	count := map[string]int{}
	var order []string
	for _, c := range codes {
		if count[c] == 0 {
			order = append(order, c)
		}
		count[c]++
	}
	sort.SliceStable(order, func(i, j int) bool { return count[order[i]] > count[order[j]] })
	return order[:min(n, len(order))]
}

// isPropCat reports whether a code is a GO, PL or CH object: the categories that can stand as props, edges and
// fences (TD is floor and wall decoration).
func isPropCat(code string) bool {
	if len(code) < 4 {
		return false
	}
	switch code[2:4] {
	case "GO", "PL", "CH":
		return true
	}
	return false
}

// lsFamilyExcluded reports whether code shares its first 8 characters with an excluded code.
func lsFamilyExcluded(code string, excl map[string]bool) bool {
	for f := range excl {
		if len(code) >= 8 && len(f) >= 8 && code[:8] == f[:8] {
			return true
		}
	}
	return false
}

// lsPlaceDecal lays a backdrop sprite flat on a wall (make_v6 place_decal): its width is frac of the wall's width
// along the top edge, its height keeps the aspect, and the result is clipped to the wall.
func lsPlaceDecal(canvas *pmap, spr *image.NRGBA, side byte, a, h, frac float64, mask []float32) {
	sw, sh := float64(spr.Bounds().Dx()), float64(spr.Bounds().Dy())
	g := frac * lsWallAxis / sw
	ex := [2]float64{lsEdgeX * g / lsWallAxis, -25.5 * g / lsWallAxis}
	if side == 'R' {
		ex[1] = 25.5 * g / lsWallAxis
	}
	ax, ay := lsWallAnchor(side, a, h)
	pasteAffine(canvas, premul(spr), [2]float64{ax * lsSS, ay * lsSS},
		[2]float64{ex[0] * lsSS, ex[1] * lsSS}, [2]float64{0, g * lsSS}, [2]float64{sw / 2, sh / 2}, mask)
}

// lsWallAnchor is a point on a wall: a in [0, 1] along the floor edge from the back corner, h in [0, 1] up the wall.
func lsWallAnchor(side byte, a, h float64) (float64, float64) {
	dx := lsEdgeX * a
	if side == 'L' {
		dx = -dx
	}
	return 65 + dx, 45 + 25.5*a - 40*h
}

// lsWallLayer is one wall filled with the sky gradient (top at y 5, bottom at y 70.5), darkened by shade, and
// limited to mask.
func lsWallLayer(sky Sky, shade float64, mask []float32) *pmap {
	m := newPmap(lsW*lsSS, lsH*lsSS)
	top := [3]float64{float64(sky.Top.R), float64(sky.Top.G), float64(sky.Top.B)}
	bot := [3]float64{float64(sky.Bottom.R), float64(sky.Bottom.G), float64(sky.Bottom.B)}
	for yy := range m.h {
		t := math.Min(1, math.Max(0, (float64(yy)/lsSS-5)/65.5))
		var c [3]float32
		for i := range c {
			c[i] = float32(int((top[i]+(bot[i]-top[i])*t)*shade)) / 255
		}
		for x := range m.w {
			k := yy*m.w + x
			if mask[k] == 0 {
				continue
			}
			m.p[4*k], m.p[4*k+1], m.p[4*k+2], m.p[4*k+3] = c[0], c[1], c[2], 1
		}
	}
	return m
}

// lsPolyMask is the canvas-sized mask of a 1x polygon: 1 where a sub-pixel centre lies inside.
// ponytail: PIL's polygon fill also takes the boundary and walks its edges; the centre test differs on boundary
// pixels only (the hexagon outline: mean 0.3 levels, about 0.9% of pixels off by more than 24). Upgrade: port PIL's
// ImageDraw.polygon edge walk if the outline ever matters.
func lsPolyMask(poly [][2]float64) []float32 {
	m := make([]float32, lsW*lsSS*lsH*lsSS)
	for y := range lsH * lsSS {
		for x := range lsW * lsSS {
			if inConvex(poly, (float64(x)+0.5)/lsSS, (float64(y)+0.5)/lsSS) {
				m[y*lsW*lsSS+x] = 1
			}
		}
	}
	return m
}

// inConvex reports whether (x, y) lies inside the convex polygon p, whichever way its vertices run.
func inConvex(p [][2]float64, x, y float64) bool {
	pos, neg := false, false
	for i := range p {
		a, b := p[i], p[(i+1)%len(p)]
		c := (b[0]-a[0])*(y-a[1]) - (b[1]-a[1])*(x-a[0])
		pos = pos || c > 0
		neg = neg || c < 0
	}
	return !(pos && neg)
}

// lsPt is a foot point: a column and the lowest row with an opaque pixel in it.
type lsPt struct{ x, y int }

// lsFoot is the foot line of a sprite: per column, the lowest pixel with alpha above 128.
func lsFoot(s *image.NRGBA) []lsPt {
	if s == nil {
		return nil
	}
	w, h := s.Bounds().Dx(), s.Bounds().Dy()
	var pts []lsPt
	for x := range w {
		for y := h - 1; y >= 0; y-- {
			if s.NRGBAAt(x, y).A > 128 {
				pts = append(pts, lsPt{x, y})
				break
			}
		}
	}
	return pts
}

// lsCountOn counts the points within 2 px of the line y = m x + b.
func lsCountOn(pts []lsPt, m, b float64) int {
	n := 0
	for _, p := range pts {
		if math.Abs(float64(p.y)-(m*float64(p.x)+b)) <= 2.0 {
			n++
		}
	}
	return n
}

// lsFit is the least-squares line y = m x + b through pts, refit once after dropping points more than 2 px
// off the first line (make_v6 fit_line).
func lsFit(pts []lsPt) (float64, float64) {
	use := pts
	var m, b float64
	for range 2 {
		n := float64(len(use))
		if len(use) < 2 {
			break
		}
		var mx, my float64
		for _, p := range use {
			mx += float64(p.x) / n
			my += float64(p.y) / n
		}
		var sxx, sxy float64
		for _, p := range use {
			sxx += (float64(p.x) - mx) * (float64(p.x) - mx)
			sxy += (float64(p.x) - mx) * (float64(p.y) - my)
		}
		m = 0
		if sxx != 0 {
			m = sxy / sxx
		}
		b = my - m*mx
		var kept []lsPt
		for _, p := range use {
			if math.Abs(float64(p.y)-(m*float64(p.x)+b)) <= 2.0 {
				kept = append(kept, p)
			}
		}
		if len(kept) >= 2 {
			use = kept
		}
	}
	return m, b
}

// lsGroundPiece shears a fence piece so its foot line has the exact edge slope k (make_v6 ground_piece). It
// returns the sheared sprite, the foot pivot (xp, yp), the opaque x-extent and ok=false when the foot is too
// short to fit.
func lsGroundPiece(art *image.NRGBA, k float64) (*image.NRGBA, float64, float64, float64, bool) {
	w, h := art.Bounds().Dx(), art.Bounds().Dy()
	m, _ := lsFit(lsFoot(art))
	pad := int(math.Abs(k-m)*float64(w)) + 4
	padded := image.NewNRGBA(image.Rect(0, 0, w, h+2*pad))
	for y := range h {
		for x := range w {
			padded.SetNRGBA(x, y+pad, art.NRGBAAt(x, y))
		}
	}
	d := -(k - m)
	sheared := sampleAffine(premul(padded), w, h+2*pad, func(x, y float64) (float64, float64) {
		return x, d*x + y
	}).boxStraight(1)
	pts := lsFoot(sheared)
	if len(pts) < 2 {
		return nil, 0, 0, 0, false
	}
	m2, b2 := lsFit(pts)
	x0, x1 := pts[0].x, pts[0].x
	for _, p := range pts {
		x0, x1 = min(x0, p.x), max(x1, p.x)
	}
	xp := float64(x0+x1) / 2
	return sheared, xp, m2*xp + b2, float64(x1 - x0), true
}

// lsFenceRun places three overlapping copies of a fence piece along one back edge, each foot line on the edge
// (make_v6 place_fence_run). The right run keeps x >= 65 and the left x <= 65 (back corner, inclusive as PIL).
func lsFenceRun(canvas *pmap, art *image.NRGBA, side byte, k float64) {
	sheared, xp, yp, ext, ok := lsGroundPiece(art, k)
	if !ok {
		return
	}
	s := lsFenceSpan / ext
	src := premul(sheared)
	layer := newPmap(canvas.w, canvas.h)
	for i := range 3 {
		xc := 65 + lsFenceL*(float64(i)+0.5)
		if side == 'L' {
			xc = 65 - lsFenceL*(float64(i)+0.5)
		}
		yc := 45 + k*(xc-65)
		pasteAffine(layer, src, [2]float64{xc * lsSS, yc * lsSS},
			[2]float64{s * lsSS, s * k * lsSS}, [2]float64{0, s * lsSS}, [2]float64{xp, yp}, nil)
	}
	for y := range layer.h {
		for x := range layer.w {
			if (side == 'R' && x < 65*lsSS) || (side == 'L' && x > 65*lsSS) {
				i := 4 * (y*layer.w + x)
				layer.p[i], layer.p[i+1], layer.p[i+2], layer.p[i+3] = 0, 0, 0, 0
			}
		}
	}
	canvas.over(layer, 0, 0)
}

// lsPasteScaled fits spr into maxW x maxH 1x pixels (aspect kept) and pastes it bottom-centred on (ax, ay)
// (make_v4 paste_scaled3).
func lsPasteScaled(canvas *pmap, spr *image.NRGBA, ax, ay, maxW, maxH float64) {
	sw, sh := float64(spr.Bounds().Dx()), float64(spr.Bounds().Dy())
	s := math.Min(maxW/sw, maxH/sh)
	tw := max(1, pyRound(sw*s*lsSS))
	th := max(1, pyRound(sh*s*lsSS))
	canvas.over(premul(spr).resize(tw, th), pyRound(ax*lsSS-float64(tw)/2), pyRound(ay*lsSS-float64(th)))
}

// lsFill is the share of the sprite's box with alpha above 128; 1 when nothing is (make_v4 fence_stats).
func lsFill(s *image.NRGBA) float64 {
	w, h := s.Bounds().Dx(), s.Bounds().Dy()
	n := 0
	for y := range h {
		for x := range w {
			if s.NRGBAAt(x, y).A > 128 {
				n++
			}
		}
	}
	if n == 0 {
		return 1
	}
	return float64(n) / float64(w*h)
}

// lsIsFence is a wide, sparse sprite: a rail or fence piece, not a solid diamond or a square prop.
func lsIsFence(s *image.NRGBA) bool {
	if s == nil || s.Bounds().Dy() == 0 {
		return false
	}
	return float64(s.Bounds().Dx()) >= 1.4*float64(s.Bounds().Dy()) && lsFill(s) < 0.45
}

// lsIsFlat is a solid floor diamond: twice as wide as high and about half filled.
func lsIsFlat(s *image.NRGBA) bool {
	if s == nil || s.Bounds().Dy() == 0 {
		return false
	}
	return float64(s.Bounds().Dx()) >= 1.8*float64(s.Bounds().Dy()) && lsFill(s) >= 0.45
}

// lsIsGlow is a light effect: mostly semi-transparent or dim (make_v6 is_glow).
func lsIsGlow(s *image.NRGBA) bool {
	var n, semi, sumA, rgbN int
	sumL := 0.0
	w, h := s.Bounds().Dx(), s.Bounds().Dy()
	for y := range h {
		for x := range w {
			c := s.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			n++
			sumA += int(c.A)
			if c.A < 200 {
				semi++
			}
			rgbN++
			sumL += (float64(c.R) + float64(c.G) + float64(c.B)) / 3
		}
	}
	if n == 0 {
		return true
	}
	meanA := float64(sumA) / float64(n)
	semiF := float64(semi) / float64(n)
	meanL := sumL / float64(rgbN)
	return semiF > 0.6 || meanA < 120 || (meanL > 225 && semiF > 0.3)
}

// lsHasAlpha reports whether any pixel of s is not transparent (PIL getbbox is not None).
func lsHasAlpha(s *image.NRGBA) bool {
	for i := 3; i < len(s.Pix); i += 4 {
		if s.Pix[i] != 0 {
			return true
		}
	}
	return false
}

// lsFlipH mirrors a sprite left to right (PIL FLIP_LEFT_RIGHT).
func lsFlipH(s *image.NRGBA) *image.NRGBA {
	w, h := s.Bounds().Dx(), s.Bounds().Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			out.SetNRGBA(w-1-x, y, s.NRGBAAt(x, y))
		}
	}
	return out
}

// floorDiamond is the floor tile of the atlas (make_v3 floor_tile_v3): the 42x21 cell whose colour group has the
// most cells, or the first group that is not near white (luminance below 0.90), with the diamond as its alpha.
func floorDiamond(atlasPNG []byte) (*image.NRGBA, error) {
	im, err := decodeNRGBA(atlasPNG)
	if err != nil {
		return nil, err
	}
	const tw, th = 42, 21
	w, h := im.Bounds().Dx(), im.Bounds().Dy()
	diamond := func(x, y int, limit float64) bool {
		return math.Abs(float64(x)+0.5-21)/21+math.Abs(float64(y)+0.5-10.5)/10.5 <= limit
	}
	type cell struct {
		x0, y0 int
		mean   [3]int
	}
	type group struct {
		key   [3]int
		cells []cell
	}
	var groups []*group
	index := map[[3]int]*group{}
	for y0 := range min(42, h-th+1) {
		for x0 := range min(42, w-tw+1) {
			opaque := true
			var sum [3]int
			n := 0
			for y := range th {
				for x := range tw {
					if !diamond(x, y, 0.9) {
						continue
					}
					c := im.NRGBAAt(x0+x, y0+y)
					if c.A < 250 {
						opaque = false
					}
					sum[0] += int(c.R)
					sum[1] += int(c.G)
					sum[2] += int(c.B)
					n++
				}
			}
			if !opaque {
				continue
			}
			mean := [3]int{sum[0] / n, sum[1] / n, sum[2] / n}
			key := [3]int{mean[0] / 24, mean[1] / 24, mean[2] / 24}
			g, ok := index[key]
			if !ok {
				g = &group{key: key}
				index[key] = g
				groups = append(groups, g)
			}
			g.cells = append(g.cells, cell{x0, y0, mean})
		}
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("floor atlas: no opaque diamond")
	}
	ranked := append([]*group(nil), groups...)
	sort.SliceStable(ranked, func(i, j int) bool { return len(ranked[i].cells) > len(ranked[j].cells) })
	pick := ranked[0]
	for _, g := range ranked {
		m := g.cells[0].mean
		if (0.299*float64(m[0])+0.587*float64(m[1])+0.114*float64(m[2]))/255.0 < 0.90 {
			pick = g
			break
		}
	}
	x0, y0 := pick.cells[0].x0, pick.cells[0].y0
	out := image.NewNRGBA(image.Rect(0, 0, tw, th))
	for y := range th {
		for x := range tw {
			c := im.NRGBAAt(x0+x, y0+y)
			c.A = 0
			if diamond(x, y, 1.0) {
				c.A = 255
			}
			out.SetNRGBA(x, y, c)
		}
	}
	return out, nil
}
