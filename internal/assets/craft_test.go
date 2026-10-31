package assets

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestCraftRel(t *testing.T) {
	for _, p := range []string{
		"/garden01/items/item110000.png",
		"/craft01/1012/craft_1012_home_banner.png",
		"/garden01/npc/npc_portrait/npc_portrait_01.png",
		"/photozone/frame/031/bg.png",
		"/photozone/frame/0-2/animation.aniproj",
	} {
		if rel, ok := CraftRel(p); !ok || "/"+rel != p {
			t.Errorf("CraftRel(%q) = %q, %v; want the path back", p, rel, ok)
		}
	}
	for _, p := range []string{
		"/garden01/items/item110000.jpg",
		"/garden01/",
		"/garden01",
		"/garden01/../craft01/x.png",
		"/craft02/x.png",
		"/craft01x/a.png",
		"/garden/x.mworldpack", // the map pack folder is not the garden01 craft folder
		"/cherry/garden/items/item110000.png",
		"/photozone/other/x",
		"/photozone/other/x.png",
		"/photozone/frame/../x.png",
	} {
		if rel, ok := CraftRel(p); ok {
			t.Errorf("CraftRel(%q) = %q, want refused", p, rel)
		}
	}
}

// TestCraftRouteReadsR2Original checks that streaming serves a craft file from its static/ key and caches it, and that
// an absent one falls through (the caller's 404).
func TestCraftRouteReadsR2Original(t *testing.T) {
	f := newFakeR2(t)
	f.put("static/garden01/items/item110000.png", []byte("icon"))
	s, dir := webStore(t, f)
	if rec, ok := get(t, s, "/garden01/items/item110000.png"); !ok || rec.Code != http.StatusOK || rec.Body.String() != "icon" {
		t.Fatalf("craft icon: handled=%v code=%d body=%q", ok, rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(dir, "garden01", "items", "item110000.png")); err != nil || string(b) != "icon" {
		t.Fatalf("local copy: %q, %v", b, err)
	}
	if _, ok := get(t, s, "/craft01/1012/craft_1012_home_banner.png"); ok {
		t.Fatal("absent craft art answered")
	}
	var nilStore *Store
	if _, ok := get(t, nilStore, "/garden01/items/item110000.png"); ok {
		t.Fatal("streaming off answered a craft file")
	}
}
