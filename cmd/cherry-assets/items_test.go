package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cherry/internal/assets"
	"cherry/internal/itemconv"
)

// convertedTree writes a generated item tree with one converted item (custom 224400179, hair) and its items.json,
// the way runConvert leaves it.
func convertedTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"1409_iteminfo.artsitem": "0;0;0;1;5;7;0;0;2.000000;3.000000;0_0.png;0;0;",
		"0_0.png":                "PARTBYTES",
		"dp.png":                 "DPBYTES",
	}
	m := &assets.ItemManifest{}
	dir := filepath.Join(root, "custom", "224400179")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := md5.Sum([]byte(body))
		m.Entries = append(m.Entries, assets.ItemEntry{Kind: "custom", ID: 224400179, Name: name, Size: int64(len(body)),
			MD5: strings.ToUpper(hex.EncodeToString(sum[:])), Origin: assets.OriginCherry})
	}
	body, err := assets.EncodeItemManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "items.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestConvertedItemsUploadUnderCherry: the converted files go to cherry/item/ (never static/), the items.json index
// lists them with origin cherry and the md5 uploaded, and nothing lands under static/.
func TestConvertedItemsUploadUnderCherry(t *testing.T) {
	root := convertedTree(t)
	objs, err := withGenerated(nil, archiveRoots, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 3 {
		t.Fatalf("%d objects, want 3", len(objs))
	}
	client, stored := fakeBucket(t)
	ctx := context.Background()
	u := &uploader{client: client, log: log.New(io.Discard, "", 0), total: int64(len(objs)), stop: func() {}}
	for _, o := range objs {
		u.one(ctx, o)
	}
	if n := u.failed.Load(); n != 0 {
		t.Fatalf("%d uploads failed", n)
	}
	for _, key := range []string{"cherry/item/custom/224400179/0_0.png", "cherry/item/custom/224400179/dp.png", "cherry/item/custom/224400179/1409_iteminfo.artsitem"} {
		if _, ok := stored(key); !ok {
			t.Errorf("%s not uploaded", key)
		}
	}
	if err := writeItems(ctx, client, objs); err != nil {
		t.Fatal(err)
	}
	raw, ok := stored(assets.ItemManifestKey)
	if !ok {
		t.Fatal("no cherry/manifest/items.json")
	}
	m, err := assets.DecodeItemManifest(raw)
	if err != nil {
		t.Fatalf("uploaded index: %v", err)
	}
	if len(m.Entries) != 3 {
		t.Fatalf("index has %d entries, want 3", len(m.Entries))
	}
	if !hasItemIndex(objs) {
		t.Fatal("hasItemIndex misses the converted items")
	}
	for _, e := range m.Entries {
		if e.Origin != assets.OriginCherry || len(e.MD5) != 32 || e.Kind != "custom" || e.ID != 224400179 {
			t.Errorf("entry %+v", e)
		}
	}
	// The index names the bytes R2 holds: each entry's md5 matches the stored object.
	for _, e := range m.Entries {
		b, ok := stored(assets.ItemKey(e.Kind, e.ID, e.Name))
		if !ok {
			t.Fatalf("%s missing", e.Name)
		}
		sum := md5.Sum(b)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), e.MD5) {
			t.Errorf("%s: index md5 %s, stored %x", e.Name, e.MD5, sum)
		}
	}
	for _, o := range objs {
		if strings.HasPrefix(o.key, "static/") || !o.generated {
			t.Errorf("converted object %s is not generated under cherry/", o.key)
		}
	}
}

// TestConvertedItemRefusedUnderStatic: the uploader refuses a converted item under static/ (as an original, or as
// a generated object), and a generated item outside cherry/item/<kind>/<id>/<file>.
func TestConvertedItemRefusedUnderStatic(t *testing.T) {
	gen := &object{key: assets.ItemKey("custom", 224400179, "0_0.png"), path: filepath.Join(t.TempDir(), "p.png"), size: 1,
		generated: true, recipe: itemconv.Recipe}
	orig := &object{key: "static/item/custom/224400179/0_0.png", path: filepath.Join(archiveRoots[0], "item", "custom", "224400179", "0_0.png"), size: 1}
	if err := checkOrigins([]*object{orig, gen}, archiveRoots); err == nil || !strings.Contains(err.Error(), "converted item is not an original") {
		t.Fatalf("original of a converted id: %v", err)
	}
	staticGen := &object{key: "static/item/custom/224400179/0_0.png", path: gen.path, size: 1, generated: true, recipe: itemconv.Recipe}
	if err := checkOrigins([]*object{staticGen}, archiveRoots); err == nil {
		t.Fatal("generated item under static/ accepted")
	}
	for _, key := range []string{"cherry/item/custom/224400179/x/0_0.png", "cherry/item/custom/224400179", "cherry/manifest/item/custom/224400179/0_0.png"} {
		bad := &object{key: key, path: gen.path, size: 1, generated: true, recipe: itemconv.Recipe}
		if err := checkOrigins([]*object{bad}, archiveRoots); err == nil {
			t.Errorf("generated %s accepted", key)
		}
	}
	if err := checkOrigins([]*object{gen}, archiveRoots); err != nil {
		t.Fatalf("a valid converted item refused: %v", err)
	}
}

// TestConvertedItemsD1Rows: every converted file gets a generated row with its key, md5 and recipe, and an original
// of the same id writes nothing.
func TestConvertedItemsD1Rows(t *testing.T) {
	var mu sync.Mutex
	var sqls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SQL string `json:"sql"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		sqls = append(sqls, req.SQL)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[{"success":true,"results":[]}]}`))
	}))
	t.Cleanup(srv.Close)
	d := &d1Client{creds: d1Creds{accountID: "acct", token: "tok", database: "db"}, api: srv.URL, hc: srv.Client()}
	objs, err := withGenerated(nil, archiveRoots, convertedTree(t))
	if err != nil {
		t.Fatal(err)
	}
	logger := log.New(io.Discard, "", 0)
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	if err := syncGenerated(context.Background(), d, objs, now, logger); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(sqls, "\n")
	for _, want := range []string{
		"'cherry/item/custom/224400179/0_0.png'",
		"'cherry/item/custom/224400179/dp.png'",
		"'" + itemconv.Recipe + "'",
		"'" + itemconv.DpRecipe + "'",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "static/") {
		t.Errorf("an original was recorded:\n%s", joined)
	}
}
