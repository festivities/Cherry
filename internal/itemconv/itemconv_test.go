package itemconv

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The pilot (tool/convert.py, validate.py) and its item.zip. The golden test skips when either is absent.
const (
	pilotDir = "C:/Users/fes/AppData/Local/Temp/opencode/cherry/itemzip-20261026"
	itemZip  = "D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lineplay-original-assets/ios-mini-4.3-2014/item.zip"
	santiDir = "D:/Dev/projects/Cherry/.opencode/line-play-artifacts/santi-backup-20260921/jp.naver.lineplay.android/files/item"
	lpDir    = "D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lpbackup/jp.naver.lineplay.android/files/item"
)

// TestBoxTaps: Pillow's BOX taps for 7 -> 2 are [0,4) and [4,7), so 3.5:1 keeps 3 or 4 inputs.
func TestBoxTaps(t *testing.T) {
	lo, cnt := boxTaps(7, 2)
	if !slices.Equal(lo, []int{0, 4}) || !slices.Equal(cnt, []int{4, 3}) {
		t.Fatalf("taps 7->2: lo %v cnt %v", lo, cnt)
	}
}

// TestItemSynthetic: a one-part custom item converts to the exact 10.1 artsitem text and a 2x1 part PNG.
func TestItemSynthetic(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	for i := range im.Pix {
		im.Pix[i] = 0xff
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	// custom iOS text: no header; front 1 row (anchor 5, z 7, x 7, y 10, 0_0.png); back 0 rows.
	files, err := Item("custom", func(name string) ([]byte, error) {
		if name == "iteminfo.artsitem" {
			return []byte("1;5;7;7;10;0_0.png;0;"), nil
		}
		return buf.Bytes(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "0;0;0;1;5;7;0;0;2.000000;3.000000;0_0.png;0;0;"
	if got := string(files["1409_iteminfo.artsitem"]); got != want {
		t.Fatalf("artsitem = %q, want %q", got, want)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(files["0_0.png"]))
	if err != nil || cfg.Width != 2 || cfg.Height != 1 {
		t.Fatalf("part PNG %v %+v, want 2x1", err, cfg)
	}
}

// TestParseIOSRejects: trailing fields and short rows fail the item, as the pilot's convert did.
func TestParseIOSRejects(t *testing.T) {
	for _, text := range []string{"0;0;0;0;9;", "0;0;1;5;7;1;2;3;x;0;", "0;0;1;5;7;1;2"} {
		if _, err := parseIOS(text, "dress"); err == nil {
			t.Errorf("parseIOS(%q) accepted", text)
		}
	}
}

// goldenPair is one overlap item of the pilot's pairlist.json: kind, id and the device backup that has it.
type goldenPair struct {
	kind, id, src string
}

func loadPairs(t *testing.T) []goldenPair {
	t.Helper()
	for _, dir := range []string{santiDir, lpDir} {
		if _, err := os.Stat(dir); err != nil {
			t.Skipf("device backup not present: %v", err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(pilotDir, "tool", "pairlist.json"))
	if err != nil {
		t.Skipf("pilot not present: %v", err)
	}
	var rows [][3]string
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	pairs := make([]goldenPair, len(rows))
	for i, r := range rows {
		pairs[i] = goldenPair{r[0], r[1], r[2]}
	}
	return pairs
}

// iosZipReader returns a reader over the item.zip entries of one item folder.
func itemReader(zr *zip.ReadCloser, kind, id string) func(string) ([]byte, error) {
	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[f.Name] = f
	}
	return func(name string) ([]byte, error) {
		f := entries["item/"+kind+"/"+id+"/"+name]
		if f == nil {
			return nil, fmt.Errorf("no %s", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
}

// TestGoldenAgainstPilot converts the 312 overlap items and checks them the way the pilot's validate.py does
// (its strict pass: files, header, structure, part size and anchors within 1 px, colour within 10 on the best
// 2 px shift), and against the pilot's out/ folders: artsitem bytes equal, part sizes equal.
func TestGoldenAgainstPilot(t *testing.T) {
	pairs := loadPairs(t)
	zr, err := zip.OpenReader(itemZip)
	if err != nil {
		t.Skipf("item.zip not present: %v", err)
	}
	defer zr.Close()
	pass, exactInfo := 0, 0
	pixParts, pixExact, pixWorst := 0, 0, 0.0
	var classes = map[string]int{}
	var mismatches []string
	for _, p := range pairs {
		gtRoot := santiDir
		if p.src == "lp" {
			gtRoot = lpDir
		}
		files, err := Item(p.kind, itemReader(zr, p.kind, p.id))
		if err != nil {
			t.Fatalf("%s/%s: %v", p.kind, p.id, err)
		}
		// Pilot output: byte-exact artsitem, same part names and sizes.
		pilotInfo, err := os.ReadFile(filepath.Join(pilotDir, "out", p.kind, p.id, "1409_iteminfo.artsitem"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(files["1409_iteminfo.artsitem"], pilotInfo) {
			exactInfo++
		} else {
			mismatches = append(mismatches, p.kind+"/"+p.id+" artsitem")
		}
		// Pilot part PNGs: same names, same size, and pixels within a unit of the pilot's BOX resize.
		for name, b := range files {
			if name == "1409_iteminfo.artsitem" {
				continue
			}
			pb, err := os.ReadFile(filepath.Join(pilotDir, "out", p.kind, p.id, name))
			if err != nil {
				t.Fatal(err)
			}
			ours, ow, oh := decodeRGBA(t, b)
			theirs, pw, ph := decodeRGBA(t, pb)
			if ow != pw || oh != ph {
				t.Errorf("%s/%s/%s: %dx%d, pilot %dx%d", p.kind, p.id, name, ow, oh, pw, ph)
				continue
			}
			worst := 0.0
			for i := range ours.pix {
				worst = math.Max(worst, math.Abs(ours.pix[i]-theirs.pix[i]))
			}
			pixWorst = math.Max(pixWorst, worst)
			pixParts++
			if worst == 0 {
				pixExact++
			}
		}
		classesFor, pass1 := strictCheck(t, p, files, gtRoot)
		for _, c := range classesFor {
			classes[c]++
		}
		if pass1 {
			pass++
		}
	}
	t.Logf("strict pass %d of %d (pilot 193 of 312); artsitem byte-exact %d of %d; failing classes %v",
		pass, len(pairs), exactInfo, len(pairs), classes)
	t.Logf("part PNGs vs pilot: %d parts, %d pixel-identical, worst channel difference %.0f", pixParts, pixExact, pixWorst)
	if pixWorst > 2 {
		t.Errorf("part pixels differ from the pilot by up to %.0f", pixWorst)
	}
	if len(mismatches) > 0 {
		t.Errorf("artsitem differs from the pilot in %d items, e.g. %v", len(mismatches), mismatches[:min(5, len(mismatches))])
	}
	if d := pass - 193; d < -2 || d > 2 {
		t.Errorf("strict pass %d, want 193 within 2 (pilot)", pass)
	}
}

// strictCheck is validate.py's check_item pass for one converted item against its device-backup original.
// It returns the failing classes (empty when the item passes) and whether it passes.
func strictCheck(t *testing.T, p goldenPair, out map[string][]byte, gtRoot string) ([]string, bool) {
	t.Helper()
	gtDir := filepath.Join(gtRoot, p.kind, p.id)
	gtFiles, err := os.ReadDir(gtDir)
	if err != nil {
		t.Fatal(err)
	}
	var gtCore []string
	for _, e := range gtFiles {
		if e.Name() != "dp.png" {
			gtCore = append(gtCore, e.Name())
		}
	}
	var outNames []string
	for n := range out {
		if n != "dp.png" {
			outNames = append(outNames, n)
		}
	}
	sort.Strings(gtCore)
	sort.Strings(outNames)
	var classes []string
	filesOK := slices.Equal(gtCore, outNames)
	if !filesOK {
		classes = append(classes, "FILES")
	}
	gtInfo, err := os.ReadFile(filepath.Join(gtDir, "1409_iteminfo.artsitem"))
	if err != nil {
		t.Fatal(err)
	}
	gt := parse10(string(gtInfo), p.kind)
	cv := parse10(string(out["1409_iteminfo.artsitem"]), p.kind)
	hdrOK := slices.Equal(gt.hdr, cv.hdr) && gt.ver == cv.ver && gt.hasAnim == cv.hasAnim
	if !hdrOK {
		classes = append(classes, "HDR")
	}
	structOK := true
	for side := 0; side < 2; side++ {
		g, c := gt.sides[side], cv.sides[side]
		if len(g) != len(c) {
			structOK = false
			continue
		}
		if !slices.Equal(sortedKeys(g), sortedKeys(c)) {
			structOK = false
		}
	}
	if !structOK {
		classes = append(classes, "STRUCT")
	}
	var xyErr, sizeErr []float64
	var pixBest []float64
	gtMissing := 0
	pixOK := true
	sizeOKAll := true
	xyOK := true
	for side := 0; side < 2; side++ {
		gmap := map[[2]int]part10{}
		for _, r := range gt.sides[side] {
			gmap[[2]int{r.anchor, r.z}] = r
		}
		cmap := map[[2]int]part10{}
		for _, r := range cv.sides[side] {
			cmap[[2]int{r.anchor, r.z}] = r
		}
		var keys [][2]int
		for k := range gmap {
			if _, ok := cmap[k]; ok {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i][0] != keys[j][0] {
				return keys[i][0] < keys[j][0]
			}
			return keys[i][1] < keys[j][1]
		})
		for _, k := range keys {
			g, c := gmap[k], cmap[k]
			ex, ey := math.Abs(c.x-g.x), math.Abs(c.y-g.y)
			xyErr = append(xyErr, math.Max(ex, ey))
			if ex > 1 || ey > 1 {
				xyOK = false
			}
			gb, err := os.ReadFile(filepath.Join(gtDir, g.name))
			if err != nil {
				gtMissing++
				continue
			}
			ob, ok := out[c.name]
			if !ok {
				t.Fatalf("%s/%s: no output part %s", p.kind, p.id, c.name)
			}
			gImg, gw, gh := decodeRGBA(t, gb)
			oImg, ow, oh := decodeRGBA(t, ob)
			se := math.Max(math.Abs(float64(gw-ow)), math.Abs(float64(gh-oh)))
			sizeErr = append(sizeErr, se)
			if se > 1 {
				sizeOKAll = false
			}
			a := placeRGBA(oImg, ow, oh, c.x, c.y)
			b := placeRGBA(gImg, gw, gh, g.x, g.y)
			best := bestShift(a, b, 2)
			pixBest = append(pixBest, best.rgb)
			if best.rgb > 10 {
				pixOK = false
			}
		}
	}
	if gtMissing > 0 {
		classes = append(classes, "GT-PNG-MISSING")
	}
	if !xyOK {
		classes = append(classes, "XY>1px")
	}
	if !sizeOKAll {
		classes = append(classes, "SIZE>1px")
	}
	if !pixOK {
		classes = append(classes, "PIX>10")
	}
	pass := filesOK && hdrOK && structOK && sizeOKAll && xyOK && pixOK && gtMissing == 0
	return classes, pass
}

// part10 is one 10.1 part row (name and placement are what the check reads).
type part10 struct {
	anchor, z int
	x, y      float64
	name      string
}

type parsed10 struct {
	hdr     []string
	ver     int
	hasAnim int
	sides   [2][]part10
}

// parse10 mirrors validate.py's parse_10 for the fields the check reads.
func parse10(text, kind string) parsed10 {
	f := strings.Split(text, ";")
	if f[len(f)-1] == "" {
		f = f[:len(f)-1]
	}
	at := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	atoi := func(s string) int {
		n, _ := strconv.Atoi(s)
		return n
	}
	var out parsed10
	var i, wide int
	if kind == "dress" {
		out.hdr = sliceOf(f, 0, 5)
		i = 5
		out.ver = atoi(at(4))
		wide = map[int]int{0: 0, 1: 0, 2: 1, 3: 2}[out.ver]
		if out.ver == 3 {
			out.hdr = append(out.hdr, sliceOf(f, 5, 8)...)
			i = 8
		}
	} else {
		out.hdr = sliceOf(f, 0, 3)
		i = 3
		out.ver = atoi(at(2))
		wide = map[int]int{0: 0, 1: 2, 2: 3}[out.ver]
		if out.ver == 2 {
			out.hdr = append(out.hdr, sliceOf(f, 3, 7)...)
			i = 7
		}
	}
	out.hasAnim = atoi(at(1))
	for side := 0; side < 2; side++ {
		n := atoi(at(i))
		i++
		for k := 0; k < n; k++ {
			r := part10{anchor: atoi(at(i)), z: atoi(at(i + 1))}
			r.x, _ = strconv.ParseFloat(at(i+4), 64)
			r.y, _ = strconv.ParseFloat(at(i+5), 64)
			r.name = at(i + 6)
			i += 7 + wide
			cnt := atoi(at(i))
			i += 1 + 8*cnt
			out.sides[side] = append(out.sides[side], r)
		}
	}
	return out
}

func sliceOf(f []string, lo, hi int) []string {
	if lo >= len(f) {
		return nil
	}
	return slices.Clone(f[lo:min(hi, len(f))])
}

func sortedKeys(rows []part10) [][2]int {
	keys := make([][2]int, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, [2]int{r.anchor, r.z})
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	return keys
}

// rgbaImg is a decoded RGBA image with its origin: pixels are 4 bytes each, row-major.
type rgbaImg struct {
	pix  []float64
	w, h int
}

func decodeRGBA(t *testing.T, b []byte) (rgbaImg, int, int) {
	t.Helper()
	im, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	bd := im.Bounds()
	w, h := bd.Dx(), bd.Dy()
	out := rgbaImg{pix: make([]float64, w*h*4), w: w, h: h}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(im.At(bd.Min.X+x, bd.Min.Y+y)).(color.NRGBA)
			i := (y*w + x) * 4
			out.pix[i], out.pix[i+1], out.pix[i+2], out.pix[i+3] = float64(c.R), float64(c.G), float64(c.B), float64(c.A)
		}
	}
	return out, w, h
}

// placed is a part on the shared canvas: its origin is the part's top-left at (left, top).
type placed struct {
	rgbaImg
	left, top int
}

// placeRGBA positions a part with its centre at (x, y), y up (validate.py place_arrays, Python round()).
func placeRGBA(img rgbaImg, w, h int, x, y float64) placed {
	return placed{rgbaImg: img,
		left: int(math.RoundToEven(x - float64(w)/2.0)),
		top:  int(math.RoundToEven(-(y + float64(h)/2.0)))}
}

// madUnion is validate.py's mad_on_union: the mean colour difference composited on grey 128, over the union
// canvas with b shifted by (dx, dy).
func madUnion(a, b placed, dx, dy int) float64 {
	lb2, tb2 := b.left+dx, b.top+dy
	L := min(a.left, lb2)
	T := min(a.top, tb2)
	R := max(a.left+a.w, lb2+b.w)
	B := max(a.top+a.h, tb2+b.h)
	W, H := R-L, B-T
	sum := 0.0
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			ca := sample(a.rgbaImg, a.left, a.top, L, T, x, y)
			cb := sample(b.rgbaImg, lb2, tb2, L, T, x, y)
			for c := 0; c < 3; c++ {
				pa := ca[c]*ca[3]/255 + 128*(1-ca[3]/255)
				pb := cb[c]*cb[3]/255 + 128*(1-cb[3]/255)
				sum += math.Abs(pa - pb)
			}
		}
	}
	return sum / float64(W*H*3)
}

// sample returns the pixel at canvas (x, y) of an image placed at (left, top); transparent outside.
func sample(img rgbaImg, left, top, L, T, x, y int) [4]float64 {
	sx, sy := L+x-left, T+y-top
	if sx < 0 || sy < 0 || sx >= img.w || sy >= img.h {
		return [4]float64{}
	}
	i := (sy*img.w + sx) * 4
	return [4]float64{img.pix[i], img.pix[i+1], img.pix[i+2], img.pix[i+3]}
}

type shiftResult struct {
	rgb    float64
	dx, dy int
}

// bestShift is validate.py's best_shift: the lowest colour difference over shifts within rng (dx outer, first
// minimum kept).
func bestShift(a, b placed, rng int) shiftResult {
	best := shiftResult{rgb: math.Inf(1)}
	for dx := -rng; dx <= rng; dx++ {
		for dy := -rng; dy <= rng; dy++ {
			if v := madUnion(a, b, dx, dy); v < best.rgb {
				best = shiftResult{rgb: v, dx: dx, dy: dy}
			}
		}
	}
	return best
}

// TestParse10Counts guards the parser behind strictCheck: part rows per side.
func TestParse10Counts(t *testing.T) {
	p := parse10("0;0;0;1;5;7;0;0;2.0;3.0;0_0.png;0;0;", "custom")
	if len(p.sides[0]) != 1 || len(p.sides[1]) != 0 || p.sides[0][0].name != "0_0.png" {
		t.Fatalf("parse10 = %+v", p)
	}
}
