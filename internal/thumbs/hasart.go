package thumbs

import (
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// artCache memoizes HasArt per item folder. The archive is read-only, so an answer does not change.
var artCache = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// HasArt reports whether ServeDpPNG would serve real art for the item rather than the grey
// placeholder: the same sources in the same order (authentic dp.png, cache, 2014 preview, then the
// generator's inputs). It reads file headers only and renders nothing, so it is cheap enough to ask
// for every catalog row. The economy leaves items without art out of sales, gacha and grants.
// ponytail: header-only, so a PNG with a valid header but a broken body counts as art here; the
// generator would then serve the placeholder for it. Add a decode when that case shows up.
func HasArt(kind string, id int64) bool {
	dir := filepath.Join(DpItemRoot, kind, strconv.FormatInt(id, 10))
	artCache.Lock()
	defer artCache.Unlock()
	if v, ok := artCache.m[dir]; ok {
		return v
	}
	v := dpHasArt(dir, kind, strconv.FormatInt(id, 10))
	artCache.m[dir] = v
	return v
}

func dpHasArt(dir, kind, id string) bool {
	if fileExists(filepath.Join(dir, "dp.png")) {
		return true
	}
	if DpCacheDir != "" && fileExists(dpCachePath(kind, id)) {
		return true
	}
	if _, ok := dpZipPreview(kind, id); ok {
		return true
	}
	switch kind {
	case "pet":
		return petHasArt(dir)
	case "interior", "tile":
		return roomHasArt(dir, kind)
	case "custom", "dress":
		return partsHaveArt(dir, kind)
	}
	return false
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// pngHeaderSize reads only the IHDR of a PNG; ok is false for a missing or non-PNG file.
func pngHeaderSize(path string) (w, h int, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	c, err := png.DecodeConfig(f)
	if err != nil {
		return 0, 0, false
	}
	return c.Width, c.Height, true
}

// partsHaveArt mirrors dpLoadParts and dpComposeNatural without decoding: the artsitem parses with
// the generator's parsers, the front parts (back when the front is empty) are read, and the first
// readable sprite decides. A sprite above 2 px is drawn; a 1x1 or 2x2 part is an animation stand-in
// that only an .aniproj can draw.
func partsHaveArt(dir, kind string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "1409_iteminfo.artsitem"))
	if err != nil {
		return false
	}
	var front, back []dpPart
	var ok bool
	if kind == "dress" {
		front, back, ok = parseDressItemParts(string(raw))
	} else {
		front, back, ok = parseCustomItemParts(string(raw))
	}
	if !ok {
		return false
	}
	list := front
	if len(list) == 0 {
		list = back
	}
	var sizes [][2]int
	for _, pt := range list {
		if w, h, ok := pngHeaderSize(filepath.Join(dir, pt.name)); ok {
			sizes = append(sizes, [2]int{w, h})
		}
	}
	if dpDefaultExpressionOnly[dpCategoryOf(filepath.Base(dir)).code] && len(sizes) > 1 {
		sizes = sizes[:1]
	}
	aniproj, _ := filepath.Glob(filepath.Join(dir, "*.aniproj"))
	for _, s := range sizes {
		if s[0] > 2 || s[1] > 2 || len(aniproj) > 0 {
			return true
		}
	}
	return false
}

// roomHasArt mirrors composeRoomDP: any of its layer PNGs with a non-empty size is drawn.
func roomHasArt(dir, kind string) bool {
	names := []string{"fl_l0_a00.png", "wall_l_1.png"}
	if kind == "tile" {
		names = []string{"tile_1.png"}
	}
	layer0, _ := filepath.Glob(filepath.Join(dir, "*_l0_a00.png"))
	for _, p := range layer0 {
		names = append(names, filepath.Base(p))
	}
	for _, name := range names {
		if w, h, ok := pngHeaderSize(filepath.Join(dir, name)); ok && w > 0 && h > 0 {
			return true
		}
	}
	return false
}

// petHasArt mirrors composePetDP: skeleton and atlas parse, the skeleton has a skin, and the
// texture is a PNG.
func petHasArt(dir string) bool {
	var sk pkSkeleton
	var atlas pkAtlas
	if !readXML(filepath.Join(dir, "skeleton.xml"), &sk) || !readXML(filepath.Join(dir, "texture.xml"), &atlas) ||
		len(sk.Armatures) == 0 || len(sk.Armatures[0].Skins) == 0 {
		return false
	}
	_, _, ok := pngHeaderSize(filepath.Join(dir, "texture.png"))
	return ok
}
