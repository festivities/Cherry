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

// HasArt reports whether the item has the picture files it draws from: a garment its part sprites, a room or tile its
// layer picture, a pet its skeleton, atlas and texture. A dp.png alone does not count (see dpHasArt). It reads the
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

// dpHasArt is the index form of the generator's inputs. A garment or face part always needs its parts: a dp.png alone
// draws a shop cell but nothing on the avatar (the robo iOS cache, 2026-11-16, holds 2,569 such folders: the client
// fetched only the preview). A room or tile needs its layer picture for the same reason: 93 interior and 71 tile
// folders hold only dp.png (2026-11-23, all three audited roots; the uploader has no other file for them), so they
// drew blank in My Room while the shops sold them. Other kinds: an authentic dp.png, a cached composite, a pet's parts.
func dpHasArt(ix *assets.ItemIndex, kind string, n int64) bool {
	switch kind {
	case "custom", "dress":
		return wearableArt(ix, kind, n)
	case "interior", "tile":
		return roomHasArt(ix, kind, n)
	}
	if ix.HasFile(kind, n, "dp.png") {
		return true
	}
	if DpCacheDir != "" && fileExists(dpCachePath(kind, strconv.FormatInt(n, 10))) {
		return true
	}
	if kind == "pet" {
		return ix.HasFile(kind, n, "skeleton.xml") && ix.HasFile(kind, n, "texture.xml") && ix.HasFile(kind, n, "texture.png")
	}
	return false
}

// wearableArt: the artsitem and at least one part picture (not a dp*) or an animation.
func wearableArt(ix *assets.ItemIndex, kind string, n int64) bool {
	if !ix.HasFile(kind, n, "1409_iteminfo.artsitem") {
		return false
	}
	for _, name := range ix.Names(kind, n) {
		if strings.HasSuffix(name, ".aniproj") || (strings.HasSuffix(name, ".png") && !strings.HasPrefix(name, "dp")) {
			return true
		}
	}
	return false
}

// roomHasArt: a room or tile folder has art when one of its layer pictures is listed (the composeRoomDP inputs: a
// tile's tile_1.png, a wallpaper's wall_l_1.png, a room's fl_l0_a00.png or any <side>_l0_a00.png). Other layers
// (fl_l1_a00, a01 frames, ex.png) are not required: the client asks for some the bucket never had.
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
