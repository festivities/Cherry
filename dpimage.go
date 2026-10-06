package main

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Closet cell thumbnails. The client loads a local <itemFolder>/dp.png and,
// when that file is missing, GETs it from play-static (DNS-sunk to Cherry):
//   /img/read/arts_item_{custom|dress}_{id}/dp.png
//   /arts_item_{custom|dress}_{id}/dp.png
// Item folders live under the santi device backup. An item without an
// authentic dp.png gets a composite of its own part sprites, laid out with the
// offsets recorded in 1409_iteminfo.artsitem (format reverse-engineered from
// SbItemTable::_LoadDressLiveBinaryFile_ver1409 / _LoadCustomLiveBinaryFile_ver1409
// and SbItemTableLoader_{Dress,Custom}_V0..V3 in libgame.so 10.1.0.0:
// each part record is [parent-node key, z-order, c, d, x, y, filename, ...]
// with x,y = part top-left in pixels, y growing downward).

// dpItemRoot is the read-only archive tree holding item/{custom,dress}/{id}/.
var dpItemRoot = filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/santi-backup-20260921/jp.naver.lineplay.android/files/item`)

// dpCacheDir holds generated composites; empty disables caching.
var dpCacheDir = dpDefaultCacheDir()

func dpDefaultCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "Cherry", "dp-cache")
}

// parseDpPath accepts only the two observed dp.png request shapes.
func parseDpPath(path string) (kind, id string, ok bool) {
	rest, ok := strings.CutPrefix(path, "/img/read/arts_item_")
	if !ok {
		rest, ok = strings.CutPrefix(path, "/arts_item_")
		if !ok {
			return "", "", false
		}
	}
	kind, tail, ok := strings.Cut(rest, "_")
	if !ok {
		return "", "", false
	}
	id, file, ok := strings.Cut(tail, "/")
	if !ok || file != "dp.png" || !isAllDigits(id) {
		return "", "", false
	}
	switch kind {
	case "custom", "dress", "interior", "tile":
		return kind, id, true
	default:
		return "", "", false
	}
}

// dpPart is one sNodeParts record: the fields the compositor needs.
type dpPart struct {
	anchor int
	z      int
	x, y   float64
	name   string
}

const (
	dpMaxParts      = 1024
	dpMaxAniParts   = 1024
	dpMaxAnimNames  = 4096
	dpMaxActionIDs  = 4096
	dpPartPad       = 2
	dpAniPartFields = 8
)

// dpParser walks the semicolon CSV with the native parser's lenient
// conversions (atoi for integer fields, strtod for the x/y floats) and
// returns empty strings past EOF, like GetDressBinaryStrDataForIdx does.
type dpParser struct {
	fields []string
	i      int
}

func (p *dpParser) next() string {
	if p.i >= len(p.fields) {
		return ""
	}
	s := p.fields[p.i]
	p.i++
	return s
}

func (p *dpParser) num() int {
	s := p.next()
	j := 0
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		j++
	}
	k := j
	for k < len(s) && s[k] >= '0' && s[k] <= '9' {
		k++
	}
	n, err := strconv.Atoi(s[:k])
	if err != nil {
		return 0
	}
	return n
}

func (p *dpParser) flt() float64 {
	s := p.next()
	j := 0
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		j++
	}
	k := j
	dot := false
	for k < len(s) && (s[k] >= '0' && s[k] <= '9' || (s[k] == '.' && !dot)) {
		if s[k] == '.' {
			dot = true
		}
		k++
	}
	f, err := strconv.ParseFloat(s[:k], 64)
	if err != nil {
		return 0
	}
	return f
}

func (p *dpParser) name() string { return p.next() }

// parts parses one vector<sNodeParts>: a count, then per part
// [anchor, z, c, d, x, y, filename, (wide extras), aniCount, aniCount*8 ints].
// wide mirrors the per-version extra fields between filename and aniCount.
func (p *dpParser) parts(wide int) []dpPart {
	n := p.num()
	if n < 0 || n > dpMaxParts {
		return nil
	}
	recs := make([]dpPart, 0, n)
	for j := 0; j < n; j++ {
		var pt dpPart
		pt.anchor = p.num()
		pt.z = p.num()
		p.num() // c
		p.num() // d
		pt.x = p.flt()
		pt.y = p.flt()
		pt.name = p.name()
		for w := 0; w < wide; w++ {
			p.num()
		}
		cnt := p.num()
		if cnt < 0 || cnt > dpMaxAniParts {
			return nil
		}
		for a := 0; a < cnt; a++ {
			for f := 0; f < dpAniPartFields; f++ {
				p.num()
			}
		}
		recs = append(recs, pt)
	}
	return recs
}

// animNames parses the trailing [count, count names] list.
func (p *dpParser) animNames() {
	k := p.num()
	if k < 0 || k > dpMaxAnimNames {
		return
	}
	for i := 0; i < k; i++ {
		p.name()
	}
}

// actionLoad parses SbItemTableLoader_ActionLoad: [k, k ids, (m, m names)].
func (p *dpParser) actionLoad() {
	k := p.num()
	if k < 0 || k > dpMaxActionIDs {
		return
	}
	for i := 0; i < k; i++ {
		p.num()
	}
	if k > 0 {
		p.animNames()
	}
}

// parseDressItemParts parses a dress 1409_iteminfo.artsitem.
func parseDressItemParts(text string) (front, back []dpPart, ok bool) {
	p := &dpParser{fields: strings.Split(text, ";")}
	p.num() // id
	hasAnim := p.num()
	p.num()
	p.num()
	ver := p.num()
	wide := 0
	switch ver {
	case 2:
		wide = 1
	case 3:
		wide = 2
		p.num() // dye flag
		p.num() // dye color 1
		p.num() // dye color 2
	}
	if ver < 0 || ver > 3 {
		return nil, nil, false
	}
	front = p.parts(wide)
	back = p.parts(wide)
	if front == nil || back == nil {
		return nil, nil, false
	}
	if hasAnim != 0 {
		p.animNames()
	}
	if ver >= 1 {
		p.actionLoad()
	}
	return front, back, true
}

// parseCustomItemParts parses a custom 1409_iteminfo.artsitem.
func parseCustomItemParts(text string) (front, back []dpPart, ok bool) {
	p := &dpParser{fields: strings.Split(text, ";")}
	p.num() // id
	hasAnim := p.num()
	ver := p.num()
	wide := 0
	if ver == 2 {
		wide = 3
		p.num() // flag
		p.num() // flag
		p.num() // dye color 1
		p.num() // dye color 2
	}
	if ver < 0 || ver > 2 {
		return nil, nil, false
	}
	front = p.parts(wide)
	back = p.parts(wide)
	if front == nil || back == nil {
		return nil, nil, false
	}
	if hasAnim != 0 {
		p.animNames()
	}
	if ver == 1 {
		p.actionLoad()
	}
	return front, back, true
}

// composeItemDP composites the item's own part sprites. Parts attach to avatar
// body nodes (anchor key), so offsets are only comparable within one anchor
// group; the group holding the largest sprite carries the item's main art.
func composeItemDP(dir, kind string) (*image.RGBA, bool) {
	if kind == "interior" || kind == "tile" {
		return composeRoomDP(dir, kind)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "1409_iteminfo.artsitem"))
	if err != nil {
		return nil, false
	}
	var front, back []dpPart
	var ok bool
	if kind == "dress" {
		front, back, ok = parseDressItemParts(string(raw))
	} else {
		front, back, ok = parseCustomItemParts(string(raw))
	}
	if !ok {
		return nil, false
	}
	list := front
	if len(list) == 0 {
		list = back
	}
	type loadedPart struct {
		dpPart
		img image.Image
	}
	loaded := make([]loadedPart, 0, len(list))
	for _, pt := range list {
		f, err := os.Open(filepath.Join(dir, pt.name))
		if err != nil {
			continue
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			continue
		}
		loaded = append(loaded, loadedPart{dpPart: pt, img: img})
	}
	if len(loaded) == 0 {
		return nil, false
	}
	big := 0
	for i, lp := range loaded {
		b := lp.img.Bounds()
		bb := loaded[big].img.Bounds()
		if b.Dx()*b.Dy() > bb.Dx()*bb.Dy() {
			big = i
		}
	}
	anchor := loaded[big].anchor
	group := make([]loadedPart, 0, len(loaded))
	for _, lp := range loaded {
		if lp.anchor == anchor {
			group = append(group, lp)
		}
	}
	sort.SliceStable(group, func(i, j int) bool { return group[i].z < group[j].z })
	minX, minY := math.MaxInt, math.MaxInt
	maxX, maxY := math.MinInt, math.MinInt
	for _, lp := range group {
		x := int(math.Round(lp.x))
		y := int(math.Round(lp.y))
		b := lp.img.Bounds()
		minX = min(minX, x)
		minY = min(minY, y)
		maxX = max(maxX, x+b.Dx())
		maxY = max(maxY, y+b.Dy())
	}
	canvas := image.NewRGBA(image.Rect(0, 0, maxX-minX+dpPartPad*2, maxY-minY+dpPartPad*2))
	for _, lp := range group {
		b := lp.img.Bounds()
		ox := int(math.Round(lp.x)) - minX + dpPartPad
		oy := int(math.Round(lp.y)) - minY + dpPartPad
		draw.Draw(canvas, image.Rect(ox, oy, ox+b.Dx(), oy+b.Dy()), lp.img, b.Min, draw.Over)
	}
	return canvas, true
}

// dpFlights serializes composite generation per item so concurrent requests
// neither corrupt the cache file nor duplicate work.
var dpFlights = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: make(map[string]*sync.Mutex)}

func dpFlight(key string) *sync.Mutex {
	dpFlights.Lock()
	defer dpFlights.Unlock()
	mu, ok := dpFlights.m[key]
	if !ok {
		mu = &sync.Mutex{}
		dpFlights.m[key] = mu
	}
	return mu
}

func dpCachePath(kind, id string) string {
	return filepath.Join(dpCacheDir, kind+"_"+id+".png")
}

func dpWritePNG(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func serveDpPNG(w http.ResponseWriter, kind, id string) {
	dir := filepath.Join(dpItemRoot, kind, id)
	if b, err := os.ReadFile(filepath.Join(dir, "dp.png")); err == nil {
		dpWritePNG(w, b)
		return
	}
	cachePath := dpCachePath(kind, id)
	if b, err := os.ReadFile(cachePath); err == nil {
		dpWritePNG(w, b)
		return
	}
	mu := dpFlight(kind + "/" + id)
	mu.Lock()
	defer mu.Unlock()
	if b, err := os.ReadFile(cachePath); err == nil {
		dpWritePNG(w, b)
		return
	}
	img, ok := composeItemDP(dir, kind)
	if !ok {
		serveNotFound(w)
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		serveNotFound(w)
		return
	}
	b := buf.Bytes()
	if dpCacheDir != "" {
		if err := os.MkdirAll(dpCacheDir, 0o755); err == nil {
			dpAtomicWrite(cachePath, b)
		}
	}
	dpWritePNG(w, b)
}

// dpAtomicWrite writes b to path via temp file + rename so concurrent readers
// never observe a partial cache file.
func dpAtomicWrite(path string, b []byte) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dp-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return
	}
	_ = os.Rename(name, path)
}
