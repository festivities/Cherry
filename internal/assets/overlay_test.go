package assets

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func md5Upper(b []byte) string {
	sum := md5.Sum(b)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// overlayStore is a streaming store whose lists are built from in-memory indexes (no bucket).
func overlayStore(t *testing.T) *Store {
	t.Helper()
	orig := []byte("original")
	anim := &Manifest{Cat: "animation", Entries: []Entry{{Path: "a.png", Size: 1, MD5: md5Upper([]byte("a"))}, {Path: "carrier.aniproj", Size: int64(len(orig)), MD5: md5Upper(orig)}}}
	mans := []*Manifest{anim}
	for _, cat := range Categories[1:] {
		mans = append(mans, &Manifest{Cat: cat})
	}
	pm := &PackManifest{Entries: []PackEntry{{Dir: "garden", Name: "1.mworldpack", Size: 1, MD5: md5Upper([]byte("p"))}}}
	s := &Store{streaming: true}
	s.loadMu.Lock()
	s.state.Store(s.buildState(mans, pm))
	s.loadMu.Unlock()
	return s
}

func animVersion(t *testing.T, s *Store) int {
	t.Helper()
	body := s.state.Load().check
	i := strings.Index(body, `"animation":"`) + len(`"animation":"`)
	v, err := strconv.Atoi(body[i : i+strings.IndexByte(body[i:], '"')])
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func overlayGet(s *Store, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	if !s.ServeRoute(rec, httptest.NewRequest(http.MethodGet, path, nil)) {
		rec.Code = 0
	}
	return rec
}

// TestAnimationOverlay (egress on): an overlay replaces a listed file and adds -cherry/ files, changes the animation version, is
// served by the list version the client fetched (the last overlayKeep), and clearing it lists the original again.
func TestAnimationOverlay(t *testing.T) {
	enableEgress(t) // the redirect is on: overlay files must still come from Cherry
	s := overlayStore(t)
	v0 := animVersion(t, s)
	v1Bytes, pic := []byte("superset one"), []byte("png bytes")
	if err := s.SetAnimationOverlay([]OverlayFile{{Path: "carrier.aniproj", Data: v1Bytes}, {Path: "-cherry/cherry_gb_x.png", Data: pic}, {Path: "nowhere.aniproj", Data: []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	v1 := animVersion(t, s)
	ini := s.state.Load().ini["animation"]
	if v1 == v0 || !strings.Contains(ini, "/carrier.aniproj:version="+md5Upper(v1Bytes)) || !strings.Contains(ini, "/-cherry/cherry_gb_x.png:version="+md5Upper(pic)) {
		t.Fatalf("v0 %d v1 %d list:\n%s", v0, v1, ini)
	}
	if strings.Contains(ini, "nowhere.aniproj") || strings.Count(ini, "carrier.aniproj") != 1 {
		t.Fatalf("an overlay path that replaces no listed file must be left out, the carrier listed once:\n%s", ini)
	}
	if rec := overlayGet(s, "/arts_animation_en_"+strconv.Itoa(v1)+"/carrier.aniproj"); rec.Code != 200 || rec.Body.String() != string(v1Bytes) {
		t.Fatalf("carrier v1 = %d %q", rec.Code, rec.Body.String())
	}
	if rec := overlayGet(s, "/arts_animation_jp_"+strconv.Itoa(v1)+"/-cherry/cherry_gb_x.png"); rec.Code != 200 || rec.Body.String() != string(pic) {
		t.Fatalf("picture = %d", rec.Code)
	}
	if rec := overlayGet(s, "/arts_animation_en_"+strconv.Itoa(v1)+"/a.png"); rec.Code != http.StatusFound {
		t.Fatalf("an original listed file = %d, want the redirect (egress on)", rec.Code)
	}

	v2Bytes := []byte("superset two")
	if err := s.SetAnimationOverlay([]OverlayFile{{Path: "carrier.aniproj", Data: v2Bytes}}); err != nil {
		t.Fatal(err)
	}
	v2 := animVersion(t, s)
	if rec := overlayGet(s, "/arts_animation_en_"+strconv.Itoa(v1)+"/carrier.aniproj"); rec.Body.String() != string(v1Bytes) {
		t.Fatalf("a client on list v1 got %q, want the v1 bytes", rec.Body.String())
	}
	if rec := overlayGet(s, "/arts_animation_en_"+strconv.Itoa(v2)+"/carrier.aniproj"); rec.Body.String() != string(v2Bytes) {
		t.Fatalf("list v2 got %q", rec.Body.String())
	}
	if rec := overlayGet(s, "/arts_animation_en_99999/carrier.aniproj"); rec.Body.String() != string(v2Bytes) {
		t.Fatalf("an unknown version got %q, want the current overlay", rec.Body.String())
	}

	if err := s.SetAnimationOverlay(nil); err != nil {
		t.Fatal(err)
	}
	if v := animVersion(t, s); v != v0 {
		t.Fatalf("cleared overlay version %d, want the original %d", v, v0)
	}
	if strings.Contains(s.state.Load().ini["animation"], "cherry_gb_") {
		t.Fatal("cleared overlay still lists a Cherry picture")
	}
	for _, bad := range [][]OverlayFile{{{Path: "../x", Data: []byte("x")}}, {{Path: "a.png"}}, {{Path: "a.png", Data: []byte("1")}, {Path: "a.png", Data: []byte("2")}}} {
		if err := s.SetAnimationOverlay(bad); err == nil {
			t.Fatalf("overlay %v accepted", bad)
		}
	}
}
