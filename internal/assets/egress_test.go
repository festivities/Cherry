package assets

import (
	"expvar"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/metrics"
)

// testBase is the placeholder public base: no real domain goes into a test.
const testBase = "https://cdn.example.com/"

// loadEgressJSON loads body as egress.json and reports the load's error; the switch is reset at cleanup.
func loadEgressJSON(t *testing.T, body string) error {
	t.Helper()
	t.Cleanup(func() {
		egress.Store(nil)
		egressFile.Reset()
	})
	p := filepath.Join(t.TempDir(), "egress.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return loadEgressFrom(p)
}

// enableEgress turns the redirect on with the placeholder base and the embedded extension list.
func enableEgress(t *testing.T) {
	t.Helper()
	if err := loadEgressJSON(t, `{"enabled": true, "base": "`+testBase+`", "status": 302, "extensions": [".png", ".jpg", ".ogg", ".mp3", ".mworldpack", ".artsitem", ".avb", ".avc", ".plist", ".aniproj", ".xml", ".json"]}`); err != nil {
		t.Fatal(err)
	}
	if egress.Load() == nil {
		t.Fatal("enabled switch made no rule")
	}
}

// expvarInt reads one published counter (metrics publishes them on /debug/vars).
func expvarInt(t *testing.T, name string) int64 {
	t.Helper()
	v := expvar.Get(name)
	if v == nil {
		t.Fatalf("counter %s is not published", name)
	}
	n, err := strconv.ParseInt(v.String(), 10, 64)
	if err != nil {
		t.Fatalf("counter %s = %s: %v", name, v.String(), err)
	}
	return n
}

// readyItem makes one original item file known to the store (the index a startup read would build).
func readyItem(s *Store, kind string, id int64, name string) {
	ix := newItemIndex()
	ix.add(kind, id, name)
	s.items = ix
	s.readyItems.Store(ix)
}

func TestEgressDefaultIsOffAndValid(t *testing.T) {
	egressFile.Reset()
	t.Cleanup(egressFile.Reset)
	if err := loadEgressJSON(t, string(data.EgressJSON)); err != nil {
		t.Fatalf("embedded default refused: %v", err)
	}
	if egress.Load() != nil {
		t.Fatalf("embedded default enabled redirect, want off")
	}
	def := egressFile.Get()
	if def.Origin != "cherry" || def.Enabled || def.Base != "" || def.Status != http.StatusFound {
		t.Fatalf("embedded default = %+v", def)
	}
	wantExts := []string{".png", ".jpg", ".ogg", ".mp3", ".mworldpack", ".artsitem", ".avb", ".avc", ".plist", ".aniproj", ".xml", ".json"}
	for _, ext := range wantExts {
		if !slices.Contains(def.Extensions, ext) {
			t.Errorf("default list lacks %s", ext)
		}
	}
	if len(def.Extensions) != len(wantExts) {
		t.Errorf("default list len = %d (%v), want %d", len(def.Extensions), def.Extensions, len(wantExts))
	}
	for _, ext := range []string{".ini", ".ast"} {
		if slices.Contains(def.Extensions, ext) {
			t.Errorf("default list unexpectedly has %s", ext)
		}
	}
}

func TestEgressSwitchRefusals(t *testing.T) {
	for _, body := range []string{
		`{"enabled": true, "base": "", "status": 302, "extensions": [".png"]}`,
		`{"enabled": true, "base": "http://cdn.example.com", "status": 302, "extensions": [".png"]}`,
		`{"enabled": true, "base": "ftp://cdn.example.com/", "status": 302, "extensions": [".png"]}`,
		`{"enabled": true, "base": "https://cdn.example.com/?v=1", "status": 302, "extensions": [".png"]}`,
		`{"enabled": true, "base": "https://u@cdn.example.com/", "status": 302, "extensions": [".png"]}`,
		`{"enabled": false, "base": "", "status": 303, "extensions": [".png"]}`,
		`{"enabled": false, "base": "", "status": 302, "extensions": ["png"]}`,
		`{"enabled": false, "base": "", "status": 302, "extensions": [".png/x"]}`,
		`{"enabled": false, "base": "", "status": 302, "extensions": [], "unknown": 1}`,
	} {
		err := loadEgressJSON(t, body)
		if err == nil {
			t.Errorf("%s accepted", body)
			continue
		}
		if strings.Contains(err.Error(), "cdn.example.com") {
			t.Errorf("error names the base: %v", err)
		}
		if egress.Load() != nil {
			t.Errorf("%s left a rule on", body)
		}
	}
	if err := loadEgressJSON(t, `{"enabled": true, "base": "https://cdn.example.com/", "status": 307, "extensions": [".PNG"]}`); err != nil {
		t.Fatalf("valid switch refused: %v", err)
	}
	if r := egress.Load(); r == nil || r.status != 307 || !r.exts[".png"] || r.base != testBase {
		t.Fatalf("rule = %+v, want base, status 307 and .png lower-cased", r)
	}
	// Off: the base is not checked, and nothing is on.
	if err := loadEgressJSON(t, `{"enabled": false, "base": "not a url", "status": 301, "extensions": [".png"]}`); err != nil || egress.Load() != nil {
		t.Fatalf("switch off with a bad base: err %v, rule %v", err, egress.Load())
	}
}

// The originals the lists and the items index name: bulk files of several categories, an item file and a map pack.
func TestEgressRedirectsKnownOriginals(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	packs := packFixture(t, f)
	f.put("static/item/interior/223000001/dp.png", []byte("ORIG"))
	s, _ := webStore(t, f)
	if _, err := s.load(); err != nil {
		t.Fatal(err)
	}
	readyItem(s, "interior", 223000001, "dp.png")
	enableEgress(t)

	before := map[string]int64{}
	names := []string{"item", "bulk", "pack"}
	for _, n := range names {
		before[n] = expvarInt(t, "cherry.static_redirects_"+n)
	}
	for target, want := range map[string]string{
		"/arts_item_interior_223000001/dp.png":            "static/item/interior/223000001/dp.png",
		"/img/read/arts_item_interior_223000001/dp.png":   "static/item/interior/223000001/dp.png",
		"/arts_animation_tw_00531/a.png":                  "static/animation/a.png",
		"/arts_animation_tw_00531/world_icon/b.png":       "static/animation/world_icon/b.png",
		"/arts_ui_tw_00531/UIImage_hd/c.png":              "static/ui/UIImage_hd/c.png",
		"/arts_sound_tw_00531/BGM.ogg":                    "static/sound/BGM.ogg",
		"/arts_subui_en_00001/MarbleChallengePopup.plist": "static/subui/MarbleChallengePopup.plist",
		"/garden/" + packs[0].Name:                        "static/garden/" + packs[0].Name,
	} {
		rec, handled := get(t, s, target)
		if !handled || rec.Code != http.StatusFound || rec.Header().Get("Location") != testBase+want {
			t.Errorf("%s: handled=%v code=%d location=%q, want 302 to %s", target, handled, rec.Code, rec.Header().Get("Location"), testBase+want)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("%s: redirect has a body (%d bytes)", target, rec.Body.Len())
		}
	}
	if n := f.getCount("static/item/interior/223000001/dp.png"); n != 0 {
		t.Errorf("redirected item was read from R2 %d times", n)
	}
	// Counters: two item redirects, five bulk (a, world_icon/b, ui, ogg, plist) and one pack.
	for n, want := range map[string]int64{"item": 2, "bulk": 5, "pack": 1} {
		if got := expvarInt(t, "cherry.static_redirects_"+n) - before[n]; got != want {
			t.Errorf("static_redirects_%s moved by %d, want %d", n, got, want)
		}
	}
	if got := expvarInt(t, "cherry.static_redirects"); got < 8 {
		t.Errorf("static_redirects total %d, want at least 8", got)
	}
}

// Disallowed extensions, generated thumbnails, unknown keys and anything R2 may not hold are served by Cherry as before.
func TestEgressKeepsBytesWhereItMust(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	packFixture(t, f)
	f.put("static/animation/unlisted.png", []byte("U"))
	s, dir := webStore(t, f)
	if _, err := s.load(); err != nil {
		t.Fatal(err)
	}
	readyItem(s, "interior", 223000001, "dp.png")
	enableEgress(t)

	// An extension outside the switch list cached locally: the switch omits .ini, so the cached bytes go out.
	f.put("static/animation/x.ini", []byte("INI"))
	if err := os.MkdirAll(filepath.Join(dir, "animation"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "animation", "x.ini"), []byte("INI"), 0o600); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]string{
		"/arts_animation_tw_00531/x.ini":        "INI",
		"/arts_tx_en_00543/arts_strings.ast":    "ast-bytes",
		"/arts_animation_tw_00531/unlisted.png": "U",
	} {
		rec, handled := get(t, s, target)
		if !handled || rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s: handled=%v code=%d location=%q body=%q, want 200 %q", target, handled, rec.Code, rec.Header().Get("Location"), rec.Body.String(), want)
		}
	}
	// A generated thumbnail's key (no R2 object, no index entry) falls through, and a missing file is not redirected.
	for _, target := range []string{"/img/read/arts_item_interior_223000002/dp.png", "/arts_animation_tw_00531/missing.png"} {
		rec, handled := get(t, s, target)
		if handled || rec.Header().Get("Location") != "" {
			t.Errorf("%s: handled=%v location=%q, want not answered", target, handled, rec.Header().Get("Location"))
		}
	}
	if n := f.getCount("static/animation/x.ini"); n != 0 {
		t.Errorf("cached .ini read from R2 %d times", n)
	}
}

// Converted 2014 iOS items (cherry/item/) redirect when listed in items.json with an allowed extension, serve bytes when
// the extension is disallowed, and never redirect unlisted keys.
func TestEgressConvertedItems(t *testing.T) {
	f := newFakeR2(t)
	f.put("cherry/item/custom/223100001/0_0.png", []byte("PARTBYTES"))
	f.put("cherry/item/custom/223100001/1409_iteminfo.artsitem", []byte("INFO"))
	f.put("cherry/item/custom/223100001/unlisted.png", []byte("UNLISTED"))
	s, dir := webStore(t, f)
	setGeneratedRoot(t, converted(t))
	enableEgress(t)

	beforeItem := expvarInt(t, "cherry.static_redirects_item")
	for target, want := range map[string]string{
		"/img/read/arts_item_custom_223100001/0_0.png":       "cherry/item/custom/223100001/0_0.png",
		"/arts_item_custom_223100001/1409_iteminfo.artsitem": "cherry/item/custom/223100001/1409_iteminfo.artsitem",
	} {
		rec, handled := get(t, s, target)
		if !handled || rec.Code != http.StatusFound || rec.Header().Get("Location") != testBase+want {
			t.Errorf("%s: handled=%v code=%d location=%q, want 302 to %s", target, handled, rec.Code, rec.Header().Get("Location"), testBase+want)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("%s: redirect has a body (%d bytes)", target, rec.Body.Len())
		}
	}
	if got := expvarInt(t, "cherry.static_redirects_item") - beforeItem; got != 2 {
		t.Errorf("static_redirects_item moved by %d, want 2", got)
	}
	if n := f.getCount("cherry/item/custom/223100001/0_0.png"); n != 0 {
		t.Errorf("redirected converted item was read from R2 %d times", n)
	}

	// Unlisted cherry/item/ keys never redirect, even if cached locally.
	cachedUnlisted := filepath.Join(dir, "cherry", "item", "custom", "223100001", "unlisted.png")
	if err := os.MkdirAll(filepath.Dir(cachedUnlisted), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachedUnlisted, []byte("UNLISTED"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"cherry/item/custom/223100001/unlisted.png",
		"cherry/item/custom/999999999/0_0.png",
		"cherry/item/interior/223000001/dp.png",
		"cherry/item/custom/abc/dp.png",
		"cherry/garden/19000000.mworldpack",
	} {
		if loc, status, ok := s.egressTarget(key); ok {
			t.Errorf("egressTarget(%q) = (%q, %d, true), want ok=false", key, loc, status)
		}
	}
	if rec, handled := get(t, s, "/img/read/arts_item_custom_223100001/unlisted.png"); handled || rec.Header().Get("Location") != "" {
		t.Errorf("unlisted converted item: handled=%v location=%q, want not answered", handled, rec.Header().Get("Location"))
	}

	// Disallowed extension on a listed converted item serves bytes directly (no redirect).
	if err := loadEgressJSON(t, `{"enabled": true, "base": "`+testBase+`", "status": 302, "extensions": [".artsitem"]}`); err != nil {
		t.Fatal(err)
	}
	rec, handled := get(t, s, "/img/read/arts_item_custom_223100001/0_0.png")
	if !handled || rec.Code != http.StatusOK || rec.Header().Get("Location") != "" || rec.Body.String() != "PARTBYTES" {
		t.Fatalf("disallowed ext converted item: handled=%v code=%d location=%q body=%q, want 200 PARTBYTES",
			handled, rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if n := f.getCount("cherry/item/custom/223100001/0_0.png"); n != 1 {
		t.Errorf("served converted item R2 count = %d, want 1", n)
	}
}

// A file whose list is not read yet is served by Cherry, and the request never waits for the list.
func TestEgressWaitsForTheIndexes(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	f.put("static/item/interior/223000001/dp.png", []byte("ORIG"))
	s, _ := webStore(t, f) // no load, no items index yet
	enableEgress(t)

	if rec, handled := get(t, s, "/arts_animation_tw_00531/a.png"); !handled || rec.Code != http.StatusOK || rec.Body.String() != "pixels" {
		t.Fatalf("unread manifests: handled=%v code=%d location=%q", handled, rec.Code, rec.Header().Get("Location"))
	}
	if rec, handled := get(t, s, "/arts_item_interior_223000001/dp.png"); !handled || rec.Code != http.StatusOK || rec.Body.String() != "ORIG" {
		t.Fatalf("unread index: handled=%v code=%d location=%q", handled, rec.Code, rec.Header().Get("Location"))
	}
	// The copy served above is local now, so the next request redirects by that copy.
	if rec, _ := get(t, s, "/arts_animation_tw_00531/a.png"); rec.Code != http.StatusFound {
		t.Fatalf("cached copy: code %d, want a redirect", rec.Code)
	}
	// warm returns once the lists are read; the redirect then needs no request-time read.
	readyItem(s, "interior", 223000001, "dp.png")
	s.warm()
	if s.state.Load() == nil {
		t.Fatal("warm returned without the manifests")
	}
}

// With the switch off the streamed files are Cherry's bytes, and the byte counters move by what was written.
func TestEgressOffIsTodaysBehaviour(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	f.put("static/item/interior/223000001/dp.png", []byte("ORIG"))
	s, _ := webStore(t, f)
	if _, err := s.load(); err != nil {
		t.Fatal(err)
	}
	readyItem(s, "interior", 223000001, "dp.png")
	if err := loadEgressJSON(t, `{"enabled": false, "base": "", "status": 302, "extensions": [".png"]}`); err != nil {
		t.Fatal(err)
	}
	itemBytes := expvarInt(t, "cherry.static_bytes_served_item")
	bulkBytes := expvarInt(t, "cherry.static_bytes_served_bulk")
	redirects := expvarInt(t, "cherry.static_redirects")

	rec, handled := get(t, s, "/arts_item_interior_223000001/dp.png")
	if !handled || rec.Code != http.StatusOK || rec.Body.String() != "ORIG" || f.getCount("static/item/interior/223000001/dp.png") != 1 {
		t.Fatalf("switch off: handled=%v code=%d body=%q", handled, rec.Code, rec.Body.String())
	}
	if rec, _ := get(t, s, "/arts_animation_tw_00531/a.png"); rec.Code != http.StatusOK {
		t.Fatalf("switch off bulk: code %d", rec.Code)
	}
	if got := expvarInt(t, "cherry.static_bytes_served_item") - itemBytes; got != int64(len("ORIG")) {
		t.Errorf("item bytes moved by %d, want %d", got, len("ORIG"))
	}
	if got := expvarInt(t, "cherry.static_bytes_served_bulk") - bulkBytes; got != int64(len("pixels")) {
		t.Errorf("bulk bytes moved by %d, want %d", got, len("pixels"))
	}
	if got := expvarInt(t, "cherry.static_redirects") - redirects; got != 0 {
		t.Errorf("redirects moved by %d with the switch off", got)
	}
}

// The redirect reply on the wire has no Content-Length: the empty 302 goes out chunked over HTTP/1.1, while a served file
// keeps its Content-Length. Read raw, because Go's client drops the framing headers it parses.
func TestEgressRedirectIsChunkedWithoutContentLength(t *testing.T) {
	f := newFakeR2(t)
	fixture(t, f)
	f.put("static/animation/unlisted.png", []byte("U"))
	f.put("cherry/item/custom/223100001/0_0.png", []byte("PARTBYTES"))
	s, _ := webStore(t, f)
	if _, err := s.load(); err != nil {
		t.Fatal(err)
	}
	setGeneratedRoot(t, converted(t))
	enableEgress(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.ServeRoute(w, r) {
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	raw := func(target string) string {
		t.Helper()
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(conn, "GET "+target+" HTTP/1.1\r\nHost: cherry.test\r\nConnection: close\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(conn)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	for target, wantKey := range map[string]string{
		"/arts_animation_tw_00531/a.png":               "static/animation/a.png",
		"/img/read/arts_item_custom_223100001/0_0.png": "cherry/item/custom/223100001/0_0.png",
	} {
		head, body, _ := strings.Cut(raw(target), "\r\n\r\n")
		if !strings.HasPrefix(head, "HTTP/1.1 302 ") || !strings.Contains(head, "Location: "+testBase+wantKey) {
			t.Fatalf("%s redirect status or Location:\n%s", target, head)
		}
		if lower := strings.ToLower(head); strings.Contains(lower, "content-length:") || !strings.Contains(lower, "transfer-encoding: chunked") {
			t.Errorf("%s redirect framing:\n%s", target, head)
		}
		if body != "0\r\n\r\n" {
			t.Errorf("%s redirect body = %q, want the empty chunked body", target, body)
		}
	}

	head, _, _ := strings.Cut(raw("/arts_animation_tw_00531/unlisted.png"), "\r\n\r\n")
	if !strings.HasPrefix(head, "HTTP/1.1 200 ") || !strings.Contains(strings.ToLower(head), "content-length: 1") {
		t.Errorf("served file lost its Content-Length:\n%s", head)
	}
}

func TestEgressKeyEscapingAndKinds(t *testing.T) {
	if got := escapeKey("static/item/interior/1/a b#.png"); got != "static/item/interior/1/a%20b%23.png" {
		t.Errorf("escapeKey = %q", got)
	}
	for key, want := range map[string]metrics.StaticKind{
		"static/item/interior/1/dp.png":     metrics.StaticItem,
		"cherry/item/custom/1/0_0.png":      metrics.StaticItem,
		"static/garden/19000000.mworldpack": metrics.StaticPack,
		"static/animation/a.png":            metrics.StaticBulk,
		"static/chatting/action_hd/x.png":   metrics.StaticBulk,
	} {
		if got := staticKind(key); got != want {
			t.Errorf("staticKind(%s) = %v, want %v", key, got, want)
		}
	}
}
