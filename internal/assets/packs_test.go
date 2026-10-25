package assets

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// packFixture publishes the 23 garden and 4 world packs (small bodies) and manifest/packs.json to f. It returns
// the entries in manifest order.
func packFixture(t *testing.T, f *fakeR2) []PackEntry {
	t.Helper()
	var packs []PackEntry
	for i := 0; i < 23; i++ {
		packs = append(packs, packEntry(f, "garden", fmt.Sprintf("19%08d.mworldpack", i), fmt.Sprintf("garden %d", i)))
	}
	for i := 0; i < 4; i++ {
		packs = append(packs, packEntry(f, "world", fmt.Sprintf("22%08d.mworldpack", i), fmt.Sprintf("world %d", i)))
	}
	b, err := json.Marshal(PackManifest{Entries: packs})
	if err != nil {
		t.Fatal(err)
	}
	f.put("manifest/packs.json", b)
	return packs
}

func packEntry(f *fakeR2, dir, name, body string) PackEntry {
	sum := md5.Sum([]byte(body))
	f.put("static/"+dir+"/"+name, []byte(body))
	return PackEntry{Dir: dir, Name: name, Size: int64(len(body)), MD5: strings.ToUpper(hex.EncodeToString(sum[:]))}
}

func TestValidPackNames(t *testing.T) {
	for _, c := range []struct {
		dir, name string
		ok        bool
	}{
		{"garden", "1908231803.mworldpack", true},
		{"world", "2201202900.mworldpack", true},
		{"sound", "1908231803.mworldpack", false},
		{"garden", "../1908231803.mworldpack", false},
		{"garden", "1908231803.mworldpack/x", false},
		{"garden", "1908231803", false},
		{"garden", "1908231803.mworldpack.bak", false},
		{"garden", "garden.mworldpack", false},
	} {
		if got := ValidPack(c.dir, c.name); got != c.ok {
			t.Errorf("ValidPack(%q, %q) = %v, want %v", c.dir, c.name, got, c.ok)
		}
	}
}

func TestPackManifestRefusals(t *testing.T) {
	md := strings.Repeat("A", 32)
	ok := PackEntry{Dir: "garden", Name: "1908231803.mworldpack", Size: 1, MD5: md}
	if err := (&PackManifest{Entries: []PackEntry{ok}}).check(); err != nil {
		t.Fatalf("valid pack refused: %v", err)
	}
	for name, e := range map[string]PackEntry{
		"unsafe name": {Dir: "garden", Name: "../1908231803.mworldpack", Size: 1, MD5: md},
		"unknown dir": {Dir: "sound", Name: "1908231803.mworldpack", Size: 1, MD5: md},
		"no size":     {Dir: "garden", Name: "1908231803.mworldpack", MD5: md},
		"no MD5":      {Dir: "garden", Name: "1908231803.mworldpack", Size: 1, MD5: "xyz"},
	} {
		if err := (&PackManifest{Entries: []PackEntry{e}}).check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (&PackManifest{Entries: []PackEntry{ok, ok}}).check(); err == nil {
		t.Error("duplicate pack accepted")
	}
}

// Pack records need an own record before them: the client writes them through files/animation/...
func TestPackRecordsNeedAnOwnRecord(t *testing.T) {
	packs := []PackEntry{{Dir: "garden", Name: "1908231803.mworldpack", Size: 3, MD5: strings.Repeat("A", 32)}}
	if got := (&Manifest{Cat: "animation"}).IniBody(packs); got != "[]" {
		t.Fatalf("body = %q, want []", got)
	}
}

// The animation list is the category's own records, then one "/../<dir>/<name>" record per pack, in manifest order.
func TestAnimationListCarriesPackRecordsAfterOwnRecords(t *testing.T) {
	f := newFakeR2(t)
	entries := fixture(t, f)
	packs := packFixture(t, f)
	if len(packs) != 27 {
		t.Fatalf("fixture has %d packs, want 27", len(packs))
	}
	s, _ := webStore(t, f)
	rec, ok := get(t, s, "/arts_animation_ini_00001/update_en.ini")
	if !ok || rec.Code != http.StatusOK {
		t.Fatalf("animation list: handled=%v code=%d", ok, rec.Code)
	}
	own := (&Manifest{Cat: "animation", Entries: entries["animation"]}).IniBody(nil)
	if !strings.HasPrefix(rec.Body.String(), own+"\n") {
		t.Fatalf("own records are not first: %q", rec.Body.String())
	}
	lines := strings.Split(rec.Body.String(), "\n")
	if len(lines) != len(entries["animation"])+len(packs) {
		t.Fatalf("records = %d, want %d", len(lines), len(entries["animation"])+len(packs))
	}
	for i, p := range packs {
		want := fmt.Sprintf("[start=1,end=1,size=%d,/../%s/%s:version=%s]", p.Size, p.Dir, p.Name, p.MD5)
		if got := lines[len(entries["animation"])+i]; got != want {
			t.Fatalf("pack record %d = %q, want %q", i, got, want)
		}
	}
}

// The animation version moves with the map packs; without packs it is the category's own version.
func TestAnimationVersionFollowsPacks(t *testing.T) {
	f := newFakeR2(t)
	entries := fixture(t, f)
	anim := entries["animation"]
	packs := packFixture(t, f)
	if got := AnimationVersion(anim, nil); got != Version(anim) {
		t.Fatalf("version without packs = %d, want %d", got, Version(anim))
	}
	changed := slices.Clone(packs)
	changed[3].MD5 = strings.Repeat("A", 32)
	if AnimationVersion(anim, changed) == AnimationVersion(anim, packs) {
		t.Fatal("a changed pack kept the version")
	}
	if AnimationVersion(anim, packs[:26]) == AnimationVersion(anim, packs) {
		t.Fatal("a removed pack kept the version")
	}
	s, _ := webStore(t, f)
	rec := httptest.NewRecorder()
	s.WriteCheckResource2(rec)
	if want := `"animation":"` + strconv.Itoa(AnimationVersion(anim, packs)) + `"`; !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("checkresource2 = %s, want %s", rec.Body.String(), want)
	}
}

// Both request forms serve a listed pack from R2. Anything else is not handled, so the caller's 404 applies.
func TestPackRoutesServeListedNamesOnly(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	packs := packFixture(t, f)
	s, _ := webStore(t, f)
	garden, world := packs[0], packs[23]
	for target, want := range map[string]string{
		"/garden/" + garden.Name:                            "static/garden/" + garden.Name,
		"/arts_animation_en_00531/../garden/" + garden.Name: "static/garden/" + garden.Name,
		"/arts_animation_ja_99999/../garden/" + garden.Name: "static/garden/" + garden.Name,
		"/world/" + world.Name:                              "static/world/" + world.Name,
		"/arts_animation_tw_00001/../world/" + world.Name:   "static/world/" + world.Name,
	} {
		rec, ok := get(t, s, target)
		if !ok || rec.Code != http.StatusOK || rec.Body.String() != string(f.objs[want]) {
			t.Errorf("%s: handled=%v code=%d body=%q", target, ok, rec.Code, rec.Body.String())
		}
	}
	for _, target := range []string{
		"/garden/1999999999.mworldpack",                            // not listed
		"/world/" + garden.Name,                                    // listed under garden only
		"/garden/" + world.Name,                                    // listed under world only
		"/garden/" + garden.Name + "/x",                            // not a pack name
		"/arts_animation_en_00531/../garden/1999999999.mworldpack", // not listed, un-normalised
		"/arts_animation_en_00531/../sound/BGM.ogg",                // other directory: safeRel refuses the ".."
		"/arts_animation_en_00531/../../garden/" + garden.Name,     // climbs out of the category
		"/arts_animation_en_00531/../garden/" + garden.Name + "/../" + garden.Name,
		"/arts_sound_en_00530/../garden/" + garden.Name,  // not the animation directory
		"/arts_animation_en_00531/garden/" + garden.Name, // no dot segments: a plain file, absent
		"/garden/../world/" + world.Name,
	} {
		if rec, ok := get(t, s, target); ok {
			t.Errorf("%s handled (code %d), want not handled", target, rec.Code)
		}
	}
}

// A bucket without manifest/packs.json has no map packs: the list and the version are the category's own.
func TestBucketWithoutPackManifestHasNoPacks(t *testing.T) {
	f := newFakeR2(t)
	entries := fixture(t, f)
	s, _ := webStore(t, f)
	rec, ok := get(t, s, "/arts_animation_ini_00001/update_en.ini")
	if want := (&Manifest{Cat: "animation", Entries: entries["animation"]}).IniBody(nil); !ok || rec.Body.String() != want {
		t.Fatalf("animation list: handled=%v body=%q", ok, rec.Body.String())
	}
	crec := httptest.NewRecorder()
	s.WriteCheckResource2(crec)
	if want := `"animation":"` + strconv.Itoa(Version(entries["animation"])) + `"`; !strings.Contains(crec.Body.String(), want) {
		t.Fatalf("checkresource2 = %s", crec.Body.String())
	}
}
