package thumbs

import (
	"image/color"
	"testing"
)

// TestHasArtSources walks the sources ServeDpPNG tries, in the same order, from file headers only.
func TestHasArtSources(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	red := solidPNG(t, 20, 20, color.RGBA{R: 0xff, A: 0xff})
	dot := solidPNG(t, 1, 1, color.RGBA{R: 0xff, A: 0xff})
	part := dressItemInfo("4", "10", "0", "0", "0_0.png")

	writeItemFile(t, itemRoot, "dress", "1", "dp.png", []byte("x")) // authentic preview wins
	writeItemFile(t, itemRoot, "dress", "2", "1409_iteminfo.artsitem", []byte(part))
	writeItemFile(t, itemRoot, "dress", "2", "0_0.png", red)                         // drawable part sprite
	writeItemFile(t, itemRoot, "dress", "3", "1409_iteminfo.artsitem", []byte(part)) // part PNG absent: no art
	writeItemFile(t, itemRoot, "dress", "4", "1409_iteminfo.artsitem", []byte(part))
	writeItemFile(t, itemRoot, "dress", "4", "0_0.png", dot) // 1x1 stand-in: a listed picture counts (existence, see HasArt)
	writeItemFile(t, itemRoot, "dress", "5", "1409_iteminfo.artsitem", []byte(part))
	writeItemFile(t, itemRoot, "dress", "5", "0_0.png", dot)
	writeItemFile(t, itemRoot, "dress", "5", "a.aniproj", []byte("x"))              // 1x1 stand-in with an animation: art
	writeItemFile(t, itemRoot, "dress", "6", "1409_iteminfo.artsitem", []byte("x")) // unparseable artsitem
	writeItemFile(t, itemRoot, "dress", "6", "0_0.png", red)
	writeItemFile(t, itemRoot, "interior", "7", "fl_l0_a00.png", red)                   // room main layer
	writeItemFile(t, itemRoot, "interior", "8", "bl_l0_a00.png", red)                   // any prefix (Glob)
	writeItemFile(t, itemRoot, "interior", "9", "1409_iteminfo.artsitem", []byte(part)) // folder without layers
	writeItemFile(t, itemRoot, "tile", "10", "tile_1.png", red)
	writeItemFile(t, itemRoot, "tile", "11", "fl_l0_a00.png", red)
	writePetFixture(t, itemRoot, "326400016")
	writeItemFile(t, itemRoot, "pet", "326400017", "skeleton.xml", []byte(petSkeletonXML))
	writeItemFile(t, itemRoot, "pet", "326400017", "texture.xml", []byte(petTextureXML)) // no texture.png

	cases := []struct {
		kind string
		id   int64
		want bool
	}{
		{"dress", 1, true},
		{"dress", 2, true},
		{"dress", 3, false},
		{"dress", 4, true},
		{"dress", 5, true},
		{"dress", 6, true},   // an unparseable artsitem with a listed picture: existence, see HasArt
		{"dress", 99, false}, // no folder
		{"interior", 7, true},
		{"interior", 8, true},
		{"interior", 9, false},
		{"tile", 10, true},
		{"tile", 11, true}, // the *_l0_a00.png glob applies to tiles too (composeRoomDP)
		{"pet", 326400016, true},
		{"pet", 326400017, false},
		{"bogus", 1, false},
	}
	for _, c := range cases {
		if got := HasArt(c.kind, c.id); got != c.want {
			t.Errorf("HasArt(%s, %d) = %v, want %v", c.kind, c.id, got, c.want)
		}
	}
}

// An answer is cached per item for the store it was asked of: the same store keeps a false answer, and a store made after
// a fixture is written answers afresh.
func TestHasArtCachedPerStore(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	writeItemFile(t, itemRoot, "dress", "12", "1409_iteminfo.artsitem", []byte(dressItemInfo("4", "10", "0", "0", "0_0.png")))
	if HasArt("dress", 12) {
		t.Fatal("part-less folder counted as art")
	}
	if v, ok := artCache.m["dress/12"]; !ok || v {
		t.Fatalf("cache entry = %v %v, want false stored", v, ok)
	}
	writeItemFile(t, itemRoot, "dress", "12", "dp.png", []byte("x"))
	if !HasArt("dress", 12) {
		t.Fatal("a new store did not answer afresh")
	}
}
