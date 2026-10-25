package web

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cherry/internal/assets"
)

// Map packs answer on both request forms when the pack manifest lists them: /garden/<name> (what libcurl sends
// after removing the dot segments) and the un-normalised /arts_animation_<lang>_<v>/../garden/<name>.
func TestMapPacksServedInBothForms(t *testing.T) {
	webOperatorDir(t)
	objs, _ := streamBucket(t)
	packs := map[string]string{"garden/1908231803.mworldpack": "GARDEN-PACK", "world/2201202900.mworldpack": "WORLD-PACK"}
	var entries []assets.PackEntry
	for rel, body := range packs {
		dir, name, _ := strings.Cut(rel, "/")
		sum := md5.Sum([]byte(body))
		objs["static/"+rel] = []byte(body)
		entries = append(entries, assets.PackEntry{Dir: dir, Name: name, Size: int64(len(body)), MD5: strings.ToUpper(hex.EncodeToString(sum[:]))})
	}
	b, err := json.Marshal(assets.PackManifest{Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	objs["manifest/packs.json"] = b
	streamServer(t, objs)

	for target, want := range map[string]string{
		"/garden/1908231803.mworldpack":                            "GARDEN-PACK",
		"/arts_animation_en_00531/../garden/1908231803.mworldpack": "GARDEN-PACK",
		"/world/2201202900.mworldpack":                             "WORLD-PACK",
		"/arts_animation_en_00531/../world/2201202900.mworldpack":  "WORLD-PACK",
	} {
		if code, body := bodyOf(t, http.MethodGet, target); code != http.StatusOK || body != want {
			t.Fatalf("%s = %d %q, want 200 %q", target, code, body, want)
		}
	}
	// Not listed: the caller's 404, and the mux's redirect for an un-normalised path that is not a pack.
	if code, _ := bodyOf(t, http.MethodGet, "/garden/1900000000.mworldpack"); code != http.StatusNotFound {
		t.Fatalf("unlisted pack = %d, want 404", code)
	}
	if code, _ := bodyOf(t, http.MethodGet, "/arts_animation_en_00531/../garden/1900000000.mworldpack"); code == http.StatusOK {
		t.Fatalf("unlisted un-normalised pack served")
	}
	if code, _ := bodyOf(t, http.MethodGet, "/arts_animation_en_00531/../sound/BGM.ogg"); code == http.StatusOK {
		t.Fatalf("other .. path served")
	}
}

// With streaming off a map pack request is what it was before streaming: a 404, and the mux's redirect for the
// un-normalised form.
func TestMapPackRoutesOffAreUnchanged(t *testing.T) {
	webOperatorDir(t)
	if err := LoadOperatorData(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if code, _ := bodyOf(t, http.MethodGet, "/garden/1908231803.mworldpack"); code != http.StatusNotFound {
		t.Fatalf("pack with streaming off = %d, want 404", code)
	}
	if code, _ := bodyOf(t, http.MethodGet, "/arts_animation_en_00531/../garden/1908231803.mworldpack"); code/100 != 3 {
		t.Fatalf("un-normalised pack with streaming off = %d, want the mux's redirect", code)
	}
}
