package assets

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tx543 is the update list Cherry served before streaming (the stock string table).
const tx543 = `[start=1,end=1,size=454656,/arts_strings.ast:version=1492F278EC281078AC7F35479A85F197]`

// fixture publishes a small dataset to f: one or two files per category (one nested), each with its
// manifest. It returns the entries per category.
func fixture(t *testing.T, f *fakeR2) map[string][]Entry {
	t.Helper()
	files := map[string]map[string]string{
		"animation": {"a.png": "pixels", "world_icon/b.png": "bbbb"},
		"sound":     {"BGM.ogg": "ogg-bytes"},
		"ui":        {"UIImage_hd/c.png": "cccccc"},
		"tx":        {"arts_strings.ast": "ast-bytes"},
		"subui":     {"MarbleChallengePopup.plist": "<plist/>"},
	}
	out := map[string][]Entry{}
	for _, cat := range Categories {
		var paths []string
		for p := range files[cat] {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			body := []byte(files[cat][p])
			sum := md5.Sum(body)
			out[cat] = append(out[cat], Entry{Path: p, Size: int64(len(body)), MD5: strings.ToUpper(hex.EncodeToString(sum[:]))})
			f.put("static/"+cat+"/"+p, body)
		}
		m := Manifest{Cat: cat, Version: Version(out[cat]), Entries: out[cat]}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		f.put("manifest/"+cat+".json", b)
	}
	return out
}

// webStore returns a store over f with its cache in a temp dir.
func webStore(t *testing.T, f *fakeR2) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	return New(f.cfg(), dir), dir
}

func get(t *testing.T, s *Store, target string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	rec := httptest.NewRecorder()
	handled := s.ServeRoute(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec, handled
}

func TestIniBodyGrammar(t *testing.T) {
	m := Manifest{Cat: "animation", Entries: []Entry{
		{Path: "a.png", Size: 6, MD5: "abcdef0123456789abcdef0123456789"},
		{Path: "world_icon/b.png", Size: 4, MD5: "0123456789abcdef0123456789ABCDEF"},
	}}
	want := "[start=1,end=1,size=6,/a.png:version=ABCDEF0123456789ABCDEF0123456789]\n" +
		"[start=1,end=1,size=4,/world_icon/b.png:version=0123456789ABCDEF0123456789ABCDEF]"
	if got := m.IniBody(nil); got != want {
		t.Fatalf("body\n got %q\nwant %q", got, want)
	}
	tx := Manifest{Cat: "tx", Entries: []Entry{{Path: "arts_strings.ast", Size: 454656, MD5: "1492f278ec281078ac7f35479a85f197"}}}
	if got := tx.IniBody(nil); got != tx543 {
		t.Fatalf("tx body %q differs from the pre-streaming list %q", got, tx543)
	}
	if got := (&Manifest{Cat: "sound"}).IniBody(nil); got != "[]" {
		t.Fatalf("empty body = %q, want []", got)
	}
}

func TestManifestRefusals(t *testing.T) {
	ok := Entry{Path: "a.png", Size: 1, MD5: strings.Repeat("A", 32)}
	cases := map[string]Manifest{
		"wrong category": {Cat: "sound", Entries: []Entry{ok}},
		"traversal":      {Cat: "animation", Entries: []Entry{{Path: "../a.png", Size: 1, MD5: ok.MD5}}},
		"bracket":        {Cat: "animation", Entries: []Entry{{Path: "a]b.png", Size: 1, MD5: ok.MD5}}},
		"zero size":      {Cat: "animation", Entries: []Entry{{Path: "a.png", Size: 0, MD5: ok.MD5}}},
		"short md5":      {Cat: "animation", Entries: []Entry{{Path: "a.png", Size: 1, MD5: "ABCD"}}},
		"duplicate":      {Cat: "animation", Entries: []Entry{ok, ok}},
		"stale version":  {Cat: "animation", Version: 7, Entries: []Entry{ok}},
	}
	for name, m := range cases {
		if err := m.check("animation"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := Manifest{Cat: "animation", Entries: []Entry{ok}}
	good.Version = Version(good.Entries)
	if err := good.check("animation"); err != nil {
		t.Fatalf("good manifest refused: %v", err)
	}
}

func TestVersionFollowsContent(t *testing.T) {
	a := []Entry{{Path: "a.png", Size: 1, MD5: strings.Repeat("A", 32)}}
	b := []Entry{{Path: "a.png", Size: 1, MD5: strings.Repeat("B", 32)}}
	if v := Version(a); v < 1 || v > 99999 {
		t.Fatalf("version %d outside 1..99999", v)
	}
	if Version(a) != Version(a) || Version(a) == Version(b) {
		t.Fatal("version must be stable for the same content and change with the MD5")
	}
	if v := Version(nil); v < 1 || v > 99999 {
		t.Fatalf("empty version %d", v)
	}
}

func TestCheckResource2Body(t *testing.T) {
	body := checkResource2Body(map[string]int{"animation": 11, "sound": 22, "subui": 33, "tx": 44, "ui": 55})
	var got struct {
		Adr      map[string]string `json:"adr"`
		Resource map[string]map[string]string
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	r := got.Resource["1"]
	if got.Adr["10.1.0.0"] != "1" || r["animation"] != "11" || r["sound"] != "22" || r["subui"] != "33" ||
		r["tx"] != "44" || r["ui"] != "55" || r["he"] != "0" || r["ch"] != "0" || r["squareui"] != "0" {
		t.Fatalf("body = %s", body)
	}
}

func TestRouteParsing(t *testing.T) {
	ini := map[string]string{
		"/arts_animation_ini_00531/update_tw.ini": "animation",
		"/arts_tx_ini_00001/update_ja.ini":        "tx",
		"/arts_subui_ini_00000/update_cn.ini":     "subui",
	}
	for p, cat := range ini {
		if got, ok := iniRoute(p); !ok || got != cat {
			t.Errorf("iniRoute(%s) = %q, %v", p, got, ok)
		}
	}
	for _, p := range []string{"/arts_he_ini_00001/update_en.ini", "/arts_animation_ini_00531/update_xx.ini", "/arts_animation_ini_00531/other.ini"} {
		if _, ok := iniRoute(p); ok {
			t.Errorf("iniRoute(%s) accepted", p)
		}
	}
	files := map[string]string{
		"/arts_animation_tw_00531/world_icon/b.png":         "static/animation/world_icon/b.png",
		"/arts_tx_en_00543/arts_strings.ast":                "static/tx/arts_strings.ast",
		"/arts_item_custom_123/dp.png":                      "static/item/custom/123/dp.png",
		"/img/read/arts_item_pet_326400389/dp.png":          "static/item/pet/326400389/dp.png",
		"/arts_item_dress_225409162/1409_iteminfo.artsitem": "static/item/dress/225409162/1409_iteminfo.artsitem",
	}
	for p, key := range files {
		if got, ok := fileKey(p); !ok || got != key {
			t.Errorf("fileKey(%s) = %q, %v", p, got, ok)
		}
	}
	for _, p := range []string{
		"/arts_he_en_00001/x.png",            // not a streamed category
		"/arts_animation_xx_00531/x.png",     // unknown language
		"/arts_animation_en_00531/../x",      // traversal
		"/arts_item_set_123/dp.png",          // item kind not streamed
		"/arts_item_custom_12a/dp.png",       // id not numeric
		"/img/read/arts_item_custom_1/a.png", // thumbnail form is dp.png only
		"/arts_strings.ast",
	} {
		if key, ok := fileKey(p); ok {
			t.Errorf("fileKey(%s) accepted as %s", p, key)
		}
	}
}

func TestLanguageCoverage(t *testing.T) {
	f := newFakeR2(t)
	entries := fixture(t, f)
	s, _ := webStore(t, f)
	for _, cat := range Categories {
		want := (&Manifest{Cat: cat, Entries: entries[cat]}).IniBody(nil)
		for _, lang := range langs {
			rec, ok := get(t, s, "/arts_"+cat+"_ini_00001/update_"+lang+".ini")
			if !ok || rec.Code != http.StatusOK || rec.Body.String() != want || rec.Body.Len() == 0 {
				t.Fatalf("%s/%s: handled=%v code=%d body=%q", cat, lang, ok, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestCheckResource2FromManifests(t *testing.T) {
	f := newFakeR2(t)
	entries := fixture(t, f)
	s, _ := webStore(t, f)
	rec := httptest.NewRecorder()
	s.WriteCheckResource2(rec)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"animation":"`+strconv.Itoa(Version(entries["animation"]))+`"`) ||
		!strings.Contains(rec.Body.String(), `"subui":"`+strconv.Itoa(Version(entries["subui"]))+`"`) {
		t.Fatalf("checkresource2 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestManifestUnavailableAnswers503(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	f.mu.Lock()
	f.code["manifest/sound.json"] = http.StatusInternalServerError
	f.mu.Unlock()
	s, _ := webStore(t, f)
	rec := httptest.NewRecorder()
	s.WriteCheckResource2(rec)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("checkresource2 code = %d, want 503", rec.Code)
	}
	if rec, ok := get(t, s, "/arts_ui_ini_00001/update_en.ini"); !ok || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("update list: handled=%v code=%d", ok, rec.Code)
	}
}

func TestFileRouteReadThroughCache(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	f.put("static/item/custom/123/dp.png", []byte("thumb"))
	s, dir := webStore(t, f)

	rec, ok := get(t, s, "/arts_animation_tw_00531/a.png")
	if !ok || rec.Code != http.StatusOK || rec.Body.String() != "pixels" {
		t.Fatalf("first fetch: handled=%v code=%d body=%q", ok, rec.Code, rec.Body.String())
	}
	if rec, ok := get(t, s, "/arts_animation_tw_00531/a.png"); !ok || rec.Body.String() != "pixels" {
		t.Fatalf("second fetch: handled=%v body=%q", ok, rec.Body.String())
	}
	if n := f.getCount("static/animation/a.png"); n != 1 {
		t.Fatalf("R2 GETs for a cached file = %d, want 1", n)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "animation", "a.png")); err != nil || string(b) != "pixels" {
		t.Fatalf("local copy: %q, %v", b, err)
	}
	if rec, ok := get(t, s, "/arts_tx_en_00543/arts_strings.ast"); !ok || rec.Body.String() != "ast-bytes" {
		t.Fatalf("tx file: handled=%v body=%q", ok, rec.Body.String())
	}
	if rec, ok := get(t, s, "/img/read/arts_item_custom_123/dp.png"); !ok || rec.Body.String() != "thumb" {
		t.Fatalf("dp.png: handled=%v body=%q", ok, rec.Body.String())
	}
}

func TestMissingFileIsNegativeCached(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	s, _ := webStore(t, f)
	for i := 0; i < 2; i++ {
		if _, ok := get(t, s, "/arts_animation_en_00531/none.png"); ok {
			t.Fatal("absent file answered")
		}
	}
	if n := f.getCount("static/animation/none.png"); n != 1 {
		t.Fatalf("R2 GETs for an absent file = %d, want 1 (negative cache)", n)
	}
}

func TestBadCopiesAreNotCached(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	f.put("static/animation/bad.png", []byte("data"))
	f.mu.Lock()
	f.etag["static/animation/bad.png"] = strings.Repeat("0", 32)
	f.mu.Unlock()
	s, dir := webStore(t, f)
	if _, ok := get(t, s, "/arts_animation_en_00531/bad.png"); ok {
		t.Fatal("copy with the wrong MD5 served")
	}
	if _, err := os.Stat(filepath.Join(dir, "animation", "bad.png")); err == nil {
		t.Fatal("copy with the wrong MD5 was cached")
	}

	f.put("static/animation/short.png", []byte("data"))
	f.mu.Lock()
	f.short["static/animation/short.png"] = true
	f.mu.Unlock()
	if _, ok := get(t, s, "/arts_animation_en_00531/short.png"); ok {
		t.Fatal("short body served")
	}
	if _, err := os.Stat(filepath.Join(dir, "animation", "short.png")); err == nil {
		t.Fatal("short body was cached")
	}
}

func TestStalledR2RequestGivesUp(t *testing.T) {
	old := requestTimeout
	requestTimeout = 100 * time.Millisecond
	t.Cleanup(func() { requestTimeout = old })
	f := newFakeR2(t)
	fixture(t, f)
	f.put("static/animation/slow.png", []byte("data"))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.mu.Lock()
	f.stall["static/animation/slow.png"] = release
	f.mu.Unlock()
	s, _ := webStore(t, f)
	start := time.Now()
	if _, ok := get(t, s, "/arts_animation_en_00531/slow.png"); ok {
		t.Fatal("stalled request answered")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("stalled request held for %v", d)
	}
}

func TestNilStoreAnswersNothing(t *testing.T) {
	var s *Store
	if _, ok := get(t, s, "/arts_animation_en_00531/a.png"); ok {
		t.Fatal("nil store answered")
	}
}

func TestOpenIsOffUntilFlagAndConfigExist(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	t.Cleanup(flagFile.Reset)
	cherry := filepath.Join(base, "Cherry")
	cfgPath := filepath.Join(cherry, "cloudflare.json")

	if s, err := Open(); err != nil || s != nil {
		t.Fatalf("default: %v %v", s, err)
	}
	cfgDir := filepath.Join(cherry, "config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "streaming.json"), []byte(`{"enabled": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	flagFile.Reset()
	if err := flagFile.Load(); err != nil {
		t.Fatalf("flag load: %v", err)
	}
	if s, err := Open(); err != nil || s != nil {
		t.Fatalf("flag on without cloudflare.json: %v %v", s, err)
	}
	valid := `{"account_id":"x","r2_access_key_id":"AK","r2_secret_access_key":"SK","r2_bucket":"cherry-assets","r2_endpoint":"https://acct.r2.cloudflarestorage.com"}`
	if err := os.WriteFile(cfgPath, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open()
	if err != nil || s == nil {
		t.Fatalf("flag and config present: %v %v", s, err)
	}
	if want := filepath.Join(cherry, "static"); s.cacheDir != want {
		t.Fatalf("cache dir = %s, want %s", s.cacheDir, want)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"r2_bucket":"cherry-assets"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(); err == nil {
		t.Fatal("incomplete cloudflare.json accepted")
	}
}

func TestFlagDefaultIsOff(t *testing.T) {
	if flagFile.Get().Enabled {
		t.Fatal("embedded streaming.json must default to off")
	}
}
