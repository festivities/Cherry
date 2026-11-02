package thumbs

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"cherry/internal/assets"
)

// artCache memoizes HasArt per item for the store it was asked with (a test swaps the store).
var artCache = struct {
	sync.Mutex
	store *assets.Store
	m     map[string]bool
}{m: map[string]bool{}}

// HasArt reports whether ServeDpPNG would serve real art for the item rather than the grey placeholder. It reads the
// store's index only (file names, no content), so it is cheap enough to ask for every catalog row. The economy leaves
// items without art out of sales, gacha and grants.
// ponytail: existence, not decoding. Against the santi archive this matches the old header-reading check on all but one of
// ~8,000 folders (dress 225601021: a dp.png-less folder whose sprites are unreadable). Decode a file when that case grows.
func HasArt(kind string, id int64) bool {
	s := assets.Archive
	artCache.Lock()
	defer artCache.Unlock()
	if artCache.store != s {
		artCache.store, artCache.m = s, map[string]bool{}
	}
	key := kind + "/" + strconv.FormatInt(id, 10)
	if v, ok := artCache.m[key]; ok {
		return v
	}
	v := dpHasArt(s.Items(), kind, id)
	artCache.m[key] = v
	return v
}

// dpHasArt is the index form of the generator's inputs: an authentic dp.png, a cached composite, then the parts a
// composite needs (a part sheet with at least one non-dp picture or an animation, a pet's skeleton, atlas and texture,
// a room's layer picture).
func dpHasArt(ix *assets.ItemIndex, kind string, n int64) bool {
	if ix.HasFile(kind, n, "dp.png") {
		return true
	}
	if DpCacheDir != "" && fileExists(dpCachePath(kind, strconv.FormatInt(n, 10))) {
		return true
	}
	switch kind {
	case "pet":
		return ix.HasFile(kind, n, "skeleton.xml") && ix.HasFile(kind, n, "texture.xml") && ix.HasFile(kind, n, "texture.png")
	case "interior", "tile":
		return roomHasArt(ix, kind, n)
	case "custom", "dress":
		if !ix.HasFile(kind, n, "1409_iteminfo.artsitem") {
			return false
		}
		for _, name := range ix.Names(kind, n) {
			if strings.HasSuffix(name, ".aniproj") || (strings.HasSuffix(name, ".png") && !strings.HasPrefix(name, "dp")) {
				return true
			}
		}
	}
	return false
}

// roomHasArt: a room or tile folder has art when one of its layer pictures is listed.
func roomHasArt(ix *assets.ItemIndex, kind string, n int64) bool {
	if kind == "tile" {
		if ix.HasFile(kind, n, "tile_1.png") {
			return true
		}
	} else if ix.HasFile(kind, n, "fl_l0_a00.png") || ix.HasFile(kind, n, "wall_l_1.png") {
		return true
	}
	for _, name := range ix.Names(kind, n) {
		if strings.HasSuffix(name, "_l0_a00.png") {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
