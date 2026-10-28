package main

import (
	"path/filepath"
	"testing"
)

// TestCraftObjects checks the Garden craft art of a dataset root: one object per PNG path, the craft folder's copy
// when both folders hold it, cache-only paths included, and nothing outside the two craft folders.
func TestCraftObjects(t *testing.T) {
	root := writeTree(t,
		"item/craftitem/garden01/items/item110000.png",
		"cache/garden01/items/item110000.png", // the same bytes in both folders: listed once
		"cache/garden01/items/item210000.png", // cache only
		"cache/garden01/npc/npc_portrait/npc_portrait_01.png",
		"cache/garden01/quest_symbol/readme.txt", // not a PNG: not listed
		"cache/craft01/1012/craft_1012_home_banner.png",
		"cache/other/x.png", // not a craft folder
	)
	objs, err := craftObjects(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"static/garden01/items/item110000.png":                 filepath.Join("item", "craftitem", "garden01", "items", "item110000.png"),
		"static/garden01/items/item210000.png":                 filepath.Join("cache", "garden01", "items", "item210000.png"),
		"static/garden01/npc/npc_portrait/npc_portrait_01.png": filepath.Join("cache", "garden01", "npc", "npc_portrait", "npc_portrait_01.png"),
		"static/craft01/1012/craft_1012_home_banner.png":       filepath.Join("cache", "craft01", "1012", "craft_1012_home_banner.png"),
	}
	if len(objs) != len(want) {
		t.Fatalf("%d objects, want %d", len(objs), len(want))
	}
	for _, o := range objs {
		if rel, ok := want[o.key]; !ok || filepath.Join(root, rel) != o.path {
			t.Errorf("%s -> %s, want %s", o.key, o.path, rel)
		}
	}
	if err := checkOrigins(objs, []string{root}); err != nil {
		t.Fatalf("checkOrigins: %v", err)
	}
	g := summarize(objs)
	if g.count["craft:garden01"] != 3 || g.count["craft:craft01"] != 1 {
		t.Fatalf("groups = %v", g.count)
	}
}
