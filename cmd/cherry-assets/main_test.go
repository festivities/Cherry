package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree creates the files of a miniature dataset (paths relative to the dataset root).
func writeTree(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestBuildObjectsMapsCategories(t *testing.T) {
	root := writeTree(t,
		"animation/world_icon/b.png",
		"sound/BGM.ogg",
		"UIResource/MarbleChallengePopup.plist",
		"UIResource/UIImage_hd/c.png",
		"stringtable/arts_strings.ast",
		"stringtable/other.ast",
		"garden/README", // the pack folders must exist; a loose file that is not a pack is ignored
		"world/README",
		"item/dress/README", // every kind folder must exist; a loose file is not an item
		"item/interior/README",
		"item/tile/README",
		"item/garden/README",
		"item/custom/123/dp.png",
		"item/custom/123/sub/part.png",
		"item/pet/326400389/texture.png",
		"item/craftitem/garden01/x.png", // non-numeric folder: not served
		"item/set/1/dp.png",             // kind not in ItemKinds
	)
	objs, err := buildObjects(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range objs {
		got[o.key] = o.cat + "|" + o.rel
	}
	want := map[string]string{
		"static/animation/world_icon/b.png":       "animation|world_icon/b.png",
		"static/sound/BGM.ogg":                    "sound|BGM.ogg",
		"static/ui/MarbleChallengePopup.plist":    "ui|MarbleChallengePopup.plist",
		"static/ui/UIImage_hd/c.png":              "ui|UIImage_hd/c.png",
		"static/subui/MarbleChallengePopup.plist": "subui|MarbleChallengePopup.plist",
		"static/tx/arts_strings.ast":              "tx|arts_strings.ast",
		"static/item/custom/123/dp.png":           "|",
		"static/item/custom/123/sub/part.png":     "|",
		"static/item/pet/326400389/texture.png":   "|",
	}
	if len(got) != len(want) {
		t.Fatalf("objects = %d, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// Only the top-level <digits>.mworldpack files of garden and world are map packs; the extracted folders and
// other files beside them are not served, and a pack-looking name that is not a pack name stops the plan.
func TestBuildObjectsMapsPacks(t *testing.T) {
	files := []string{
		"animation/a.png", "sound/BGM.ogg", "UIResource/MarbleChallengePopup.plist", "stringtable/arts_strings.ast",
		"item/custom/README", "item/dress/README", "item/interior/README", "item/tile/README", "item/garden/README", "item/pet/README",
		"garden/1908231803.mworldpack",
		"garden/1908231803/iteminfo.artsitem",
		"garden/notes.txt",
		"world/2201202900.mworldpack",
		"world/icon/x.png",
	}
	root := writeTree(t, files...)
	objs, err := buildObjects(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range objs {
		if o.pack != "" {
			got[o.key] = o.pack + "|" + o.rel
		}
	}
	want := map[string]string{
		"static/garden/1908231803.mworldpack": "garden|1908231803.mworldpack",
		"static/world/2201202900.mworldpack":  "world|2201202900.mworldpack",
	}
	if len(got) != len(want) || got["static/garden/1908231803.mworldpack"] != want["static/garden/1908231803.mworldpack"] || got["static/world/2201202900.mworldpack"] != want["static/world/2201202900.mworldpack"] {
		t.Fatalf("packs = %v, want %v", got, want)
	}
	if err := os.WriteFile(filepath.Join(root, "garden", "notes.mworldpack"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildObjects(root); err == nil {
		t.Fatal("a non-numeric pack name was planned")
	}
}

func TestSummarizeGroups(t *testing.T) {
	objs := []*object{
		{key: "static/tx/a", cat: "tx", size: 2},
		{key: "static/item/custom/1/dp.png", size: 5},
		{key: "static/item/custom/1/b.png", size: 7},
		{key: "static/garden/1.mworldpack", pack: "garden", size: 3},
	}
	tot := summarize(objs)
	if tot.count["bulk:tx"] != 1 || tot.count["item:custom"] != 2 || tot.bytes["item:custom"] != 12 {
		t.Fatalf("totals = %+v", tot)
	}
	if tot.count["pack:garden"] != 1 || tot.bytes["pack:garden"] != 3 {
		t.Fatalf("pack totals = %+v", tot)
	}
}
