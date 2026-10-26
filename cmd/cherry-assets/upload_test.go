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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cherry/internal/assets"
)

// fakeBucket is an in-memory R2 for the uploader: PUT stores, HEAD and GET answer what is stored and 404 otherwise.
// Signatures are not checked here; the assets client's tests cover them. stored reads a key under the lock.
func fakeBucket(t *testing.T) (*assets.Client, func(key string) ([]byte, bool)) {
	t.Helper()
	objs := map[string][]byte{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, _ := strings.CutPrefix(r.URL.Path, "/cherry-assets/")
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			objs[key] = b
			w.WriteHeader(http.StatusOK)
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
	return assets.NewClient(assets.Config{Endpoint: srv.URL, Bucket: "cherry-assets", AccessKey: "AKTEST", SecretKey: "S"}), stored
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

// Each generated object gets one upsert into generated, after the tables exist. Originals and a run without
// generated objects write nothing.
func TestSyncGeneratedWritesRows(t *testing.T) {
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
	ctx := context.Background()
	logger := log.New(io.Discard, "", 0)
	now := time.Date(2026, 10, 26, 5, 0, 0, 0, time.UTC)
	original := &object{key: "static/garden/1900000000.mworldpack", size: 4, md5: "AB", pack: "garden", rel: "1900000000.mworldpack"}
	if err := syncGenerated(ctx, d, []*object{original}, now, logger); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := len(sqls)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("originals wrote %d statements", n)
	}

	sum := strings.Repeat("A", 32)
	gen := &object{key: assets.GeneratedPackKey("1999999999.mworldpack"), size: 5, md5: sum, pack: "garden", rel: "1999999999.mworldpack", generated: true, recipe: "test: two fixtures"}
	if err := syncGenerated(ctx, d, []*object{original, gen}, now, logger); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(sqls, "\n")
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS manifest (",
		"CREATE TABLE IF NOT EXISTS generated (key TEXT PRIMARY KEY",
		"INSERT OR REPLACE INTO generated (key, size, md5, recipe, created) VALUES ('cherry/garden/1999999999.mworldpack', 5, '" + sum + "', 'test: two fixtures', '2026-10-26T05:00:00Z')",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "1900000000") {
		t.Errorf("an original was recorded in generated:\n%s", joined)
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
