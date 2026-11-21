package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cherry/internal/assets"
)

// fakeBucket is an in-memory R2 for the uploader: PUT stores, DELETE deletes, ListPage lists by prefix,
// and HEAD and GET answer what is stored and 404 otherwise. Signatures are not checked here; the assets
// client's tests cover them. stored reads a key under the lock.
func fakeBucket(t *testing.T) (*assets.Client, func(key string) ([]byte, bool)) {
	client, stored, _ := fakeBucketWithPrefixes(t)
	return client, stored
}

func fakeBucketWithPrefixes(t *testing.T) (*assets.Client, func(key string) ([]byte, bool), func() []string) {
	t.Helper()
	objs := map[string][]byte{}
	var listPrefixes []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet && r.URL.Path == "/cherry-assets/" && r.URL.Query().Get("list-type") == "2" {
			prefix := r.URL.Query().Get("prefix")
			listPrefixes = append(listPrefixes, prefix)
			type item struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
				ETag string `xml:"ETag"`
			}
			var contents []item
			for k, b := range objs {
				if strings.HasPrefix(k, prefix) {
					sum := md5.Sum(b)
					contents = append(contents, item{Key: k, Size: int64(len(b)), ETag: `"` + hex.EncodeToString(sum[:]) + `"`})
				}
			}
			sort.Slice(contents, func(i, j int) bool { return contents[i].Key < contents[j].Key })
			w.Header().Set("Content-Type", "application/xml")
			_ = xml.NewEncoder(w).Encode(struct {
				XMLName   xml.Name `xml:"ListBucketResult"`
				Truncated bool     `xml:"IsTruncated"`
				Contents  []item   `xml:"Contents"`
			}{Contents: contents})
			return
		}
		key, _ := strings.CutPrefix(r.URL.Path, "/cherry-assets/")
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			objs[key] = b
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodDelete {
			delete(objs, key)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		b, ok := objs[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sum := md5.Sum(b)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
	}))
	t.Cleanup(srv.Close)
	stored := func(key string) ([]byte, bool) {
		mu.Lock()
		defer mu.Unlock()
		b, ok := objs[key]
		return b, ok
	}
	prefixes := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(listPrefixes)
	}
	return assets.NewClient(assets.Config{Endpoint: srv.URL, Bucket: "cherry-assets", AccessKey: "AKTEST", SecretKey: "S"}), stored, prefixes
}

// A generated pack is uploaded under its cherry/garden key, and the pack index lists it with origin cherry beside
// the original pack (origin original), each under the key its bytes are stored at.
func TestGeneratedPackUploadsUnderCherry(t *testing.T) {
	client, stored := fakeBucket(t)
	dir := t.TempDir()
	origPath := filepath.Join(dir, "1900000000.mworldpack")
	genPath := filepath.Join(dir, "1999999999.mworldpack")
	if err := os.WriteFile(origPath, []byte("orig"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(genPath, []byte("gen!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	objs := []*object{
		{key: "static/garden/1900000000.mworldpack", path: origPath, size: 4, pack: "garden", rel: "1900000000.mworldpack"},
		{key: assets.GeneratedPackKey("1999999999.mworldpack"), path: genPath, size: 5, pack: "garden", rel: "1999999999.mworldpack", generated: true, recipe: "test: two fixtures"},
	}
	ctx := context.Background()
	u := &uploader{client: client, log: log.New(io.Discard, "", 0), total: int64(len(objs)), stop: func() {}}
	for _, o := range objs {
		u.one(ctx, o)
	}
	if n := u.failed.Load(); n != 0 {
		t.Fatalf("%d uploads failed", n)
	}
	if b, ok := stored("cherry/garden/1999999999.mworldpack"); !ok || string(b) != "gen!!" {
		t.Fatalf("generated pack at its key = %q, %v", b, ok)
	}
	if err := writePacks(ctx, client, objs); err != nil {
		t.Fatal(err)
	}
	b, ok := stored(assets.ManifestKey("packs.json"))
	if !ok {
		t.Fatal("no cherry/manifest/packs.json")
	}
	var pm assets.PackManifest
	if err := json.Unmarshal(b, &pm); err != nil {
		t.Fatal(err)
	}
	if len(pm.Entries) != 2 {
		t.Fatalf("index has %d entries, want 2", len(pm.Entries))
	}
	for _, e := range pm.Entries {
		wantOrigin, wantKey := assets.OriginOriginal, "static/garden/"+e.Name
		if e.Name == "1999999999.mworldpack" {
			wantOrigin, wantKey = assets.OriginCherry, "cherry/garden/"+e.Name
		}
		if e.Origin != wantOrigin || e.Key() != wantKey {
			t.Errorf("%s: origin %q key %q, want %q %q", e.Name, e.Origin, e.Key(), wantOrigin, wantKey)
		}
	}
}

// verify groups a bucket key under the plan's group names, or under one of the three bookkeeping groups.
func TestVerifyGroups(t *testing.T) {
	for key, want := range map[string]string{
		"static/animation/a.png":                                   "bulk:animation",
		"static/item/custom/123/dp.png":                            "item:custom",
		"static/garden/1908231803.mworldpack":                      "pack:garden",
		"static/chatting/action_hd/chatting_deco_action_hello.png": "chat:action",
		"cherry/manifest/packs.json":                               "manifest",
		"cherry/garden/1999999999.mworldpack":                      "generated",
		"manifest/sound.json":                                      "legacy",
	} {
		if got := verifyGroup(key); got != want {
			t.Errorf("verifyGroup(%q) = %q, want %q", key, got, want)
		}
	}
	if tot := summarize([]*object{{key: assets.GeneratedPackKey("1999999999.mworldpack"), pack: "garden", generated: true, size: 5}}); tot.count["generated"] != 1 || tot.count["pack:garden"] != 0 {
		t.Fatalf("generated object summarized as %+v", tot)
	}
}

func TestUploadSingleCategoryWritesOnlyThatManifest(t *testing.T) {
	client, stored := fakeBucket(t)
	ctx := context.Background()
	sentinels := map[string]string{
		assets.ManifestKey("sound.json"): `{"sentinel":"sound"}`,
		assets.ManifestKey("packs.json"): `{"sentinel":"packs"}`,
		assets.ItemManifestKey:           `{"sentinel":"items"}`,
	}
	for k, v := range sentinels {
		if err := client.Put(ctx, k, []byte(v), "application/json"); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	animPath := filepath.Join(dir, "a.png")
	soundPath := filepath.Join(dir, "bgm.ogg")
	if err := os.WriteFile(animPath, []byte("anim"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(soundPath, []byte("sound"), 0o600); err != nil {
		t.Fatal(err)
	}
	all := []*object{
		{key: "static/animation/a.png", path: animPath, size: 4, cat: "animation", rel: "a.png"},
		{key: "static/sound/bgm.ogg", path: soundPath, size: 5, cat: "sound", rel: "bgm.ogg"},
		{key: "static/garden/1908231803.mworldpack", path: animPath, size: 4, pack: "garden", rel: "1908231803.mworldpack"},
		{key: "cherry/item/custom/1/dp.png", path: animPath, size: 4, generated: true, recipe: "test"},
	}
	filtered := onlyCat(all, "animation")
	if err := uploadWithClient(ctx, client, log.New(io.Discard, "", 0), filtered, 2, false, false, "animation"); err != nil {
		t.Fatal(err)
	}
	if b, ok := stored("static/animation/a.png"); !ok || string(b) != "anim" {
		t.Fatalf("animation object = %q, %v", b, ok)
	}
	if _, ok := stored("static/sound/bgm.ogg"); ok {
		t.Fatal("sound object was uploaded during -cat animation")
	}
	mb, ok := stored(assets.ManifestKey("animation.json"))
	if !ok {
		t.Fatal("missing cherry/manifest/animation.json")
	}
	var m assets.Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatal(err)
	}
	if m.Cat != "animation" || len(m.Entries) != 1 || m.Entries[0].Path != "a.png" {
		t.Fatalf("unexpected animation manifest: %+v", m)
	}
	for _, cat := range assets.Categories {
		if cat == "animation" || cat == "sound" {
			continue
		}
		if _, ok := stored(assets.ManifestKey(cat + ".json")); ok {
			t.Fatalf("unexpected manifest written for %s", cat)
		}
	}
	for k, want := range sentinels {
		if got, ok := stored(k); !ok || string(got) != want {
			t.Fatalf("%s modified: got %q (%v), want %q", k, got, ok, want)
		}
	}
}

func TestPruneIncludesExcludedBulkOnly(t *testing.T) {
	client, stored, prefixes := fakeBucketWithPrefixes(t)
	ctx := context.Background()
	excludedKey := "static/animation/world_icon/ds_easter_w_exchange_i0.png"
	keptStatic := "static/animation/world_icon/b.png"
	otherStatic := "static/sound/unlisted.ogg"
	staleItem := "cherry/item/custom/1/dp.png"
	for k, v := range map[string]string{
		excludedKey: "bad101mb",
		keptStatic:  "good",
		otherStatic: "keepme",
		staleItem:   "olditem",
	} {
		if err := client.Put(ctx, k, []byte(v), "application/octet-stream"); err != nil {
			t.Fatal(err)
		}
	}
	plan := []*object{{key: keptStatic, cat: "animation", rel: "world_icon/b.png"}}

	// Dry run lists stale keys without deleting anything.
	if err := pruneClient(ctx, client, plan, false); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{excludedKey, keptStatic, otherStatic, staleItem} {
		if _, ok := stored(k); !ok {
			t.Fatalf("dry-run prune deleted %s", k)
		}
	}
	for _, p := range prefixes() {
		if strings.HasPrefix(p, "static/") {
			t.Fatalf("prune listed static prefix %q", p)
		}
	}

	// Confirming deletes stale cherry/item/ and excludedBulk keys, leaving all other static/ keys untouched.
	if err := pruneClient(ctx, client, plan, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored(excludedKey); ok {
		t.Fatalf("prune did not delete excluded key %s", excludedKey)
	}
	if _, ok := stored(staleItem); ok {
		t.Fatalf("prune did not delete stale item %s", staleItem)
	}
	if _, ok := stored(keptStatic); !ok {
		t.Fatalf("prune deleted planned static key %s", keptStatic)
	}
	if _, ok := stored(otherStatic); !ok {
		t.Fatalf("prune deleted unrelated static key %s", otherStatic)
	}

	// Second run when excludedBulk is already absent (ErrNotFound) succeeds cleanly.
	if err := pruneClient(ctx, client, plan, true); err != nil {
		t.Fatalf("second prune failed when excluded key absent: %v", err)
	}
}
