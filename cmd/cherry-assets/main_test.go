package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/assets"
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
		"cache/action/00005_chatting_deco_action_hello.png",
		"cache/action/00001_chatting_deco_action_hello.png", // another version: not served
		"cache/action/chatting_deco_action_hello.png",       // no version prefix: not served
		"cache/action/00005_chatting_deco_action_hello.jpg", // not a png: not served
		"cache/sticker/00005_hello.png",                     // another folder: not served
	)
	objs, err := buildObjects([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range objs {
		got[o.key] = o.cat + "|" + o.rel
	}
	want := map[string]string{
		"static/animation/world_icon/b.png":                        "animation|world_icon/b.png",
		"static/sound/BGM.ogg":                                     "sound|BGM.ogg",
		"static/ui/MarbleChallengePopup.plist":                     "ui|MarbleChallengePopup.plist",
		"static/ui/UIImage_hd/c.png":                               "ui|UIImage_hd/c.png",
		"static/subui/MarbleChallengePopup.plist":                  "subui|MarbleChallengePopup.plist",
		"static/tx/arts_strings.ast":                               "tx|arts_strings.ast",
		"static/item/custom/123/dp.png":                            "|",
		"static/item/custom/123/sub/part.png":                      "|",
		"static/item/pet/326400389/texture.png":                    "|",
		"static/chatting/action_hd/chatting_deco_action_hello.png": "|",
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
		"cache/action/00005_chatting_deco_action_hello.png",
		"garden/1908231803.mworldpack",
		"garden/1908231803/iteminfo.artsitem",
		"garden/notes.txt",
		"world/2201202900.mworldpack",
		"world/icon/x.png",
	}
	root := writeTree(t, files...)
	objs, err := buildObjects([]string{root})
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
	if _, err := buildObjects([]string{root}); err == nil {
		t.Fatal("a non-numeric pack name was planned")
	}
}

// Later roots add the items and map packs the first root lacks. A key the first root has keeps the first root's
// file, the bulk files of a later root are ignored, and a folder the later root lacks (world/ here) is skipped.
func TestBuildObjectsLaterRootsFillGaps(t *testing.T) {
	first := writeTree(t,
		"animation/a.png",
		"sound/BGM.ogg",
		"stringtable/arts_strings.ast",
		"UIResource/UIImage_hd/c.png",
		"garden/1908231803.mworldpack",
		"world/2201202900.mworldpack",
		"item/custom/123/dp.png",
		"item/custom/123/part.png",
		"item/dress/README",
		"item/interior/README",
		"item/tile/README",
		"item/pet/README",
		"item/garden/README",
		"cache/action/00005_chatting_deco_action_hello.png",
	)
	second := writeTree(t,
		"animation/a.png", // bulk: ignored
		"animation/b.png", // bulk: ignored
		"cache/action/00005_chatting_deco_action_yes.png", // chat icons: first root only
		"garden/1908231803.mworldpack",                    // the first root has this key
		"garden/1908261114.mworldpack",                    // added
		"item/custom/123/dp.png",                          // the first root has this key
		"item/custom/123/extra.png",                       // added to a shared id
		"item/custom/224301832/dp.png",                    // added id
		"item/dress/225103469/dp.png",                     // added id
	)
	objs, err := buildObjects([]string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"static/animation/a.png":                                   filepath.Join(first, "animation", "a.png"),
		"static/sound/BGM.ogg":                                     filepath.Join(first, "sound", "BGM.ogg"),
		"static/tx/arts_strings.ast":                               filepath.Join(first, "stringtable", "arts_strings.ast"),
		"static/ui/UIImage_hd/c.png":                               filepath.Join(first, "UIResource", "UIImage_hd", "c.png"),
		"static/garden/1908231803.mworldpack":                      filepath.Join(first, "garden", "1908231803.mworldpack"),
		"static/world/2201202900.mworldpack":                       filepath.Join(first, "world", "2201202900.mworldpack"),
		"static/item/custom/123/dp.png":                            filepath.Join(first, "item", "custom", "123", "dp.png"),
		"static/item/custom/123/part.png":                          filepath.Join(first, "item", "custom", "123", "part.png"),
		"static/chatting/action_hd/chatting_deco_action_hello.png": filepath.Join(first, "cache", "action", "00005_chatting_deco_action_hello.png"),
		"static/garden/1908261114.mworldpack":                      filepath.Join(second, "garden", "1908261114.mworldpack"),
		"static/item/custom/123/extra.png":                         filepath.Join(second, "item", "custom", "123", "extra.png"),
		"static/item/custom/224301832/dp.png":                      filepath.Join(second, "item", "custom", "224301832", "dp.png"),
		"static/item/dress/225103469/dp.png":                       filepath.Join(second, "item", "dress", "225103469", "dp.png"),
	}
	got := map[string]string{}
	for _, o := range objs {
		got[o.key] = o.path
	}
	if len(objs) != len(want) {
		t.Fatalf("objects = %d, want %d: %v", len(objs), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestSummarizeGroups(t *testing.T) {
	objs := []*object{
		{key: "static/tx/a", cat: "tx", size: 2},
		{key: "static/item/custom/1/dp.png", size: 5},
		{key: "static/item/custom/1/b.png", size: 7},
		{key: "static/garden/1.mworldpack", pack: "garden", size: 3},
		{key: "static/chatting/action_hd/chatting_deco_action_hello.png", size: 4},
	}
	tot := summarize(objs)
	if tot.count["bulk:tx"] != 1 || tot.count["item:custom"] != 2 || tot.bytes["item:custom"] != 12 {
		t.Fatalf("totals = %+v", tot)
	}
	if tot.count["pack:garden"] != 1 || tot.bytes["pack:garden"] != 3 {
		t.Fatalf("pack totals = %+v", tot)
	}
	if tot.count["chat:action"] != 1 || tot.bytes["chat:action"] != 4 || tot.count["item:action_hd"] != 0 {
		t.Fatalf("chat totals = %+v", tot)
	}
}

// The planner keeps originals (static/) to the archive roots. A generated object (cherry/) must not read an archive
// file, must name its recipe, and must sit where its kind allows: a pack at its generated key, and nothing else in
// cherry/manifest/. An item in Cherry's own id block (a generated landscape a device downloaded) is never an original.
func TestOriginsStayInArchive(t *testing.T) {
	root := writeTree(t,
		"animation/a.png", "sound/BGM.ogg", "UIResource/MarbleChallengePopup.plist", "stringtable/arts_strings.ast",
		"garden/1908231803.mworldpack", "world/README",
		"item/custom/123/dp.png", "item/dress/README", "item/interior/README", "item/tile/README", "item/pet/README", "item/garden/README",
		"cache/action/00005_chatting_deco_action_hello.png",
	)
	other := writeTree(t, "garden/1900000000.mworldpack")
	roots := []string{root}
	objs, err := buildObjects(roots)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOrigins(objs, roots); err != nil {
		t.Fatalf("archive originals refused: %v", err)
	}
	outside := filepath.Join(other, "garden", "1900000000.mworldpack")
	sum := strings.Repeat("A", 32)
	genPack := func(mut func(*object)) *object {
		o := &object{key: assets.GeneratedPackKey("1999999999.mworldpack"), path: outside, size: 1, md5: sum, pack: "garden", rel: "1999999999.mworldpack", generated: true, recipe: "test: fixture"}
		mut(o)
		return o
	}
	cases := map[string]struct {
		obj *object
		ok  bool
	}{
		"generated pack":               {genPack(func(*object) {}), true},
		"no recipe":                    {genPack(func(o *object) { o.recipe = "" }), false},
		"reads an archive file":        {genPack(func(o *object) { o.path = filepath.Join(root, "garden", "1908231803.mworldpack") }), false},
		"pack not at its key":          {genPack(func(o *object) { o.key = "static/garden/1999999999.mworldpack" }), false},
		"generated in cherry/manifest": {&object{key: "cherry/manifest/x.json", path: outside, size: 1, md5: sum, generated: true, recipe: "test"}, false},
		"generated outside cherry":     {&object{key: "static/misc/x.bin", path: outside, size: 1, md5: sum, generated: true, recipe: "test"}, false},
		"original outside the archive": {&object{key: "static/garden/1900000000.mworldpack", path: outside, size: 1, pack: "garden", rel: "1900000000.mworldpack"}, false},
		"original under cherry/":       {&object{key: "cherry/garden/1900000000.mworldpack", path: filepath.Join(root, "garden", "1908231803.mworldpack"), size: 1, pack: "garden", rel: "1900000000.mworldpack"}, false},
		"Cherry-made item in a backup": {&object{key: "static/item/garden/428499999/dp.png", path: filepath.Join(root, "item", "custom", "123", "dp.png"), size: 1}, false},
		"original landscape item":      {&object{key: "static/item/garden/428400003/dp.png", path: filepath.Join(root, "item", "custom", "123", "dp.png"), size: 1}, true},
	}
	for name, c := range cases {
		err := checkOrigins(append(objs[:len(objs):len(objs)], c.obj), roots)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, c.ok)
		}
	}
}
