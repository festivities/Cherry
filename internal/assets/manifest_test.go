package assets

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
)

// A pack is an original unless it says cherry; a cherry pack is a Garden pack under cherry/garden/.
func TestPackOriginsAndKeys(t *testing.T) {
	md := strings.Repeat("A", 32)
	orig := PackEntry{Dir: "garden", Name: "1908231803.mworldpack", Size: 1, MD5: md}
	gen := PackEntry{Dir: "garden", Name: "1999999999.mworldpack", Size: 1, MD5: md, Origin: OriginCherry}
	if orig.Key() != "static/garden/1908231803.mworldpack" || gen.Key() != "cherry/garden/1999999999.mworldpack" {
		t.Fatalf("keys %q and %q", orig.Key(), gen.Key())
	}
	good := []PackEntry{orig, {Dir: "world", Name: "2201202900.mworldpack", Size: 1, MD5: md, Origin: OriginOriginal}, gen}
	if err := (&PackManifest{Entries: good}).check(); err != nil {
		t.Fatalf("good origins refused: %v", err)
	}
	bad := map[string]PackEntry{
		"cherry outside garden": {Dir: "world", Name: "2201202900.mworldpack", Size: 1, MD5: md, Origin: OriginCherry},
		"unknown origin":        {Dir: "garden", Name: "1908231803.mworldpack", Size: 1, MD5: md, Origin: "elsewhere"},
		"same name as original": {Dir: "garden", Name: orig.Name, Size: 1, MD5: md, Origin: OriginCherry},
	}
	for name, e := range bad {
		if err := (&PackManifest{Entries: []PackEntry{orig, e}}).check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A pack the index lists with origin cherry is served from cherry/garden/, never asked for under static/.
func TestCherryPackServedFromItsOwnKey(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	packs := packFixture(t, f)
	name, body := "1999999999.mworldpack", "generated pack"
	sum := md5.Sum([]byte(body))
	f.put(GeneratedPackKey(name), []byte(body))
	packs = append(packs, PackEntry{Dir: "garden", Name: name, Size: int64(len(body)), MD5: strings.ToUpper(hex.EncodeToString(sum[:])), Origin: OriginCherry})
	b, err := json.Marshal(PackManifest{Entries: packs})
	if err != nil {
		t.Fatal(err)
	}
	f.put(ManifestKey("packs.json"), b)
	s, _ := webStore(t, f)
	rec, ok := get(t, s, "/garden/"+name)
	if !ok || rec.Body.String() != body {
		t.Fatalf("generated pack: handled=%v body=%q", ok, rec.Body.String())
	}
	if n := f.getCount("static/garden/" + name); n != 0 {
		t.Fatalf("generated pack asked for under static/ %d times", n)
	}
}

// Until migrate-manifests has run, the legacy manifest/ keys answer. Each legacy key is logged once.
func TestLegacyManifestFallbackLogsOnce(t *testing.T) {
	f := newFakeR2(t)
	entries := fixture(t, f)
	for _, cat := range Categories {
		file := cat + ".json"
		f.put(legacyManifestKey(file), f.objs[ManifestKey(file)])
		delete(f.objs, ManifestKey(file))
	}
	var logs bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logs)
	s, _ := webStore(t, f)
	rec, ok := get(t, s, "/arts_animation_ini_00001/update_en.ini")
	if want := (&Manifest{Cat: "animation", Entries: entries["animation"]}).IniBody(nil); !ok || rec.Body.String() != want {
		t.Fatalf("legacy index not served: handled=%v body=%q", ok, rec.Body.String())
	}
	// One fallback line per file: the five categories and the pack index (which has no legacy copy here).
	before := strings.Count(logs.String(), "legacy key")
	if before != len(ManifestFiles()) {
		t.Fatalf("fallback logged %d times, want %d: %q", before, len(ManifestFiles()), logs.String())
	}
	for i := 0; i < 2; i++ {
		resp, err := s.getManifest("sound.json")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if after := strings.Count(logs.String(), "legacy key"); after != before {
		t.Fatalf("repeat reads logged again: %d, was %d", after, before)
	}
}

// A cherry/manifest/ copy wins over a stale legacy key, and the legacy key is not asked for at all.
func TestCherryIndexWinsOverLegacy(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	f.put(legacyManifestKey("sound.json"), []byte("stale"))
	s, _ := webStore(t, f)
	resp, err := s.getManifest("sound.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, f.objs[ManifestKey("sound.json")]) {
		t.Fatalf("read %q, want the cherry/ copy", body)
	}
	if n := f.getCount(legacyManifestKey("sound.json")); n != 0 {
		t.Fatalf("legacy key read %d times", n)
	}
}

// legacyCopies publishes a distinct body under every legacy manifest/ key and returns the bodies.
func legacyCopies(f *fakeR2) map[string][]byte {
	out := map[string][]byte{}
	for _, file := range ManifestFiles() {
		body := []byte("index " + file)
		f.put(legacyManifestKey(file), body)
		out[file] = body
	}
	return out
}

func TestMigrateManifestsMovesEveryIndex(t *testing.T) {
	f := newFakeR2(t)
	bodies := legacyCopies(f)
	var logs []string
	logf := func(m string) { logs = append(logs, m) }
	if err := MigrateManifests(context.Background(), NewClient(f.cfg()), logf); err != nil {
		t.Fatal(err)
	}
	for file, body := range bodies {
		if f.has(legacyManifestKey(file)) {
			t.Errorf("%s: legacy key kept", file)
		}
		if !bytes.Equal(f.objs[ManifestKey(file)], body) {
			t.Errorf("%s: copy differs from the legacy bytes", file)
		}
	}
	if len(logs) != len(bodies) {
		t.Fatalf("logged %d moves, want %d", len(logs), len(bodies))
	}
	// A second run finds every copy in place and changes nothing.
	logs = nil
	if err := MigrateManifests(context.Background(), NewClient(f.cfg()), logf); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	for _, m := range logs {
		if !strings.Contains(m, "already") {
			t.Fatalf("rerun moved again: %q", m)
		}
	}
}

// A cherry/ copy that differs from the legacy key is never overwritten, and neither key is deleted.
func TestMigrateManifestsRefusesToOverwrite(t *testing.T) {
	f := newFakeR2(t)
	legacyCopies(f)
	f.put(ManifestKey("sound.json"), []byte("newer copy"))
	if err := MigrateManifests(context.Background(), NewClient(f.cfg()), func(string) {}); err == nil {
		t.Fatal("differing copies accepted")
	}
	if !f.has(legacyManifestKey("sound.json")) || string(f.objs[ManifestKey("sound.json")]) != "newer copy" {
		t.Fatal("a copy of sound.json was touched")
	}
}

// A failed read stops the run before the legacy key is deleted.
func TestMigrateManifestsKeepsLegacyOnError(t *testing.T) {
	f := newFakeR2(t)
	legacyCopies(f)
	f.code[ManifestKey("ui.json")] = http.StatusInternalServerError
	if err := MigrateManifests(context.Background(), NewClient(f.cfg()), func(string) {}); err == nil {
		t.Fatal("read error not reported")
	}
	if !f.has(legacyManifestKey("ui.json")) {
		t.Fatal("legacy ui.json deleted after a failed read")
	}
}

func TestMigrateManifestsNeedsACopy(t *testing.T) {
	f := newFakeR2(t)
	if err := MigrateManifests(context.Background(), NewClient(f.cfg()), func(string) {}); err == nil {
		t.Fatal("nothing to migrate accepted")
	}
}
