package assets

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// setGeneratedRoot writes a converted tree under a fresh GeneratedItemRoot: files maps "kind/id/name" to its bytes,
// and items.json lists them. The root is restored at cleanup.
func setGeneratedRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	m := &ItemManifest{}
	for rel, body := range files {
		parts := strings.Split(rel, "/")
		dir := filepath.Join(root, parts[0], parts[1])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, parts[2]), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := md5.Sum([]byte(body))
		m.Entries = append(m.Entries, ItemEntry{Kind: parts[0], ID: atoi64(t, parts[1]), Name: parts[2],
			Size: int64(len(body)), MD5: strings.ToUpper(hex.EncodeToString(sum[:])), Origin: OriginCherry})
	}
	body, err := EncodeItemManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "items.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	old := GeneratedItemRoot
	GeneratedItemRoot = root
	t.Cleanup(func() {
		GeneratedItemRoot = old
		genItems.Lock()
		genItems.ix = nil
		genItems.Unlock()
	})
	return root
}

func atoi64(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("id %q", s)
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

const (
	convID = 223100001 // converted custom item in the fixtures
	origID = 223000001 // a device-backup original (not in the index)
)

func converted(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"custom/223100001/1409_iteminfo.artsitem": "INFO",
		"custom/223100001/0_0.png":                "PARTBYTES",
		"custom/223100001/dp.png":                 "DPBYTES",
	}
}

func TestDecodeItemManifestRefusesBadEntries(t *testing.T) {
	good := `{"entries":[{"kind":"custom","id":223100001,"name":"0_0.png","size":3,"md5":"` + strings.Repeat("A", 32) + `","origin":"cherry"}]}`
	if _, err := DecodeItemManifest([]byte(good)); err != nil {
		t.Fatalf("good manifest refused: %v", err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"cherry"`, `"original"`, 1),
		strings.Replace(good, `"custom"`, `"interior"`, 1),
		strings.Replace(good, `"0_0.png"`, `"../0_0.png"`, 1),
		strings.Replace(good, strings.Repeat("A", 32), "AB", 1),
		strings.Replace(good, `"size":3`, `"size":3,"extra":1`, 1),
	} {
		if _, err := DecodeItemManifest([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestGeneratedIndexLists(t *testing.T) {
	setGeneratedRoot(t, converted(t))
	ix := GeneratedItems()
	if !ix.Has("custom", convID) || ix.Has("custom", origID) || ix.Has("dress", convID) {
		t.Fatalf("Has: converted %v original %v", ix.Has("custom", convID), ix.Has("custom", origID))
	}
	if !ix.HasFile("custom", convID, "0_0.png") || ix.HasFile("custom", convID, "9_9.png") {
		t.Fatal("HasFile wrong")
	}
	if got := ix.IDs("custom"); !slices.Equal(got, []int64{convID}) {
		t.Fatalf("IDs = %v", got)
	}
	var nilIx *ItemIndex
	if nilIx.Has("custom", convID) || nilIx.HasFile("custom", convID, "0_0.png") || nilIx.IDs("custom") != nil {
		t.Fatal("nil index must list nothing")
	}
}

// Streaming on: a converted item's files come from cherry/item/ in R2 (never static/), an original id still
// streams from static/, and an unlisted file of a converted id falls through to the local 404.
func TestConvertedItemsServeFromR2WhenStreaming(t *testing.T) {
	setGeneratedRoot(t, converted(t))
	f := newFakeR2(t)
	f.put("cherry/item/custom/223100001/0_0.png", []byte("R2PART"))
	f.put("static/item/custom/223000001/dp.png", []byte("ORIG"))
	s, _ := webStore(t, f)

	rec, handled := get(t, s, "/img/read/arts_item_custom_223100001/0_0.png")
	if !handled || rec.Code != http.StatusOK || rec.Body.String() != "R2PART" {
		t.Fatalf("converted part = %v %d %q, want R2PART", handled, rec.Code, rec.Body.String())
	}
	if f.getCount("cherry/item/custom/223100001/0_0.png") != 1 || f.getCount("static/item/custom/223100001/0_0.png") != 0 {
		t.Fatalf("converted part keys: cherry %d static %d", f.getCount("cherry/item/custom/223100001/0_0.png"), f.getCount("static/item/custom/223100001/0_0.png"))
	}
	if rec, handled = get(t, s, "/arts_item_custom_223000001/dp.png"); !handled || rec.Body.String() != "ORIG" {
		t.Fatalf("original dp.png = %v %q, want ORIG from static/", handled, rec.Body.String())
	}
	if _, handled = get(t, s, "/arts_item_custom_223100001/9_9.png"); handled {
		t.Fatal("an unlisted file of a converted id was answered by the stream")
	}
	rec = httptest.NewRecorder()
	if !ServeGeneratedItem(rec, httptest.NewRequest(http.MethodGet, "/arts_item_custom_223100001/9_9.png", nil)) || rec.Code != http.StatusNotFound {
		t.Fatalf("unlisted file: answered by local tree with %d, want 404", rec.Code)
	}
}

// Streaming off (nil store): the local generated tree answers the listed files; originals and POSTs fall through.
func TestConvertedItemsServeLocallyWhenStreamingOff(t *testing.T) {
	setGeneratedRoot(t, converted(t))
	var off *Store
	_, handled := get(t, off, "/img/read/arts_item_custom_223100001/0_0.png")
	if handled {
		t.Fatal("nil store answered")
	}
	serve := func(method, target string) (*httptest.ResponseRecorder, bool) {
		rec := httptest.NewRecorder()
		return rec, ServeGeneratedItem(rec, httptest.NewRequest(method, target, nil))
	}
	if rec, ok := serve(http.MethodGet, "/img/read/arts_item_custom_223100001/dp.png"); !ok || rec.Code != http.StatusOK || rec.Body.String() != "DPBYTES" {
		t.Fatalf("local dp.png = %v %d %q", ok, rec.Code, rec.Body.String())
	}
	if rec, ok := serve(http.MethodGet, "/arts_item_custom_223100001/1409_iteminfo.artsitem"); !ok || rec.Body.String() != "INFO" {
		t.Fatalf("local artsitem = %v %q", ok, rec.Body.String())
	}
	if _, ok := serve(http.MethodGet, "/arts_item_custom_223000001/dp.png"); ok {
		t.Fatal("an original id was answered by the converted tree")
	}
	if _, ok := serve(http.MethodPost, "/arts_item_custom_223100001/dp.png"); ok {
		t.Fatal("POST answered")
	}
}

// itemconv.json off: the converted items list nothing and serve nothing.
func TestConvertedSwitchOff(t *testing.T) {
	setGeneratedRoot(t, converted(t))
	t.Cleanup(func() { _ = LoadItemConv() })
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	dir := filepath.Join(base, "Cherry", "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "itemconv.json"), []byte(`{"enabled": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadItemConv(); err != nil {
		t.Fatal(err)
	}
	if GeneratedItems() != nil {
		t.Fatal("switch off still indexes")
	}
	if _, ok := generatedItemKey("/arts_item_custom_223100001/0_0.png"); ok {
		t.Fatal("switch off still routes to cherry/item")
	}
	rec := httptest.NewRecorder()
	if ServeGeneratedItem(rec, httptest.NewRequest(http.MethodGet, "/arts_item_custom_223100001/dp.png", nil)) {
		t.Fatal("switch off still serves")
	}
}

// FetchItemIndex copies the bucket's index when this machine has none, and does nothing when the bucket has none.
func TestFetchItemIndexFromBucket(t *testing.T) {
	setGeneratedRoot(t, converted(t))
	idx, err := os.ReadFile(filepath.Join(GeneratedItemRoot, "items.json"))
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	old := GeneratedItemRoot
	GeneratedItemRoot = empty
	t.Cleanup(func() { GeneratedItemRoot = old })

	f := newFakeR2(t)
	s, _ := webStore(t, f)
	if err := s.FetchItemIndex(); err != nil {
		t.Fatalf("empty bucket: %v", err)
	}
	if _, err := os.Stat(filepath.Join(empty, "items.json")); err == nil {
		t.Fatal("an empty bucket wrote an index")
	}
	f.put(ItemManifestKey, idx)
	if err := s.FetchItemIndex(); err != nil {
		t.Fatal(err)
	}
	genItems.Lock()
	genItems.ix = nil
	genItems.Unlock()
	if !GeneratedItems().HasFile("custom", convID, "0_0.png") {
		t.Fatal("fetched index does not list the item")
	}
}
