package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cherry/internal/assets"
)

// runUpload stores every object that is missing or different in R2 (HEAD, then PUT), then writes the
// indexes and the D1 mirror. The indexes are written only when every object is in place, so Cherry never
// lists a file R2 does not have. packsOnly skips the bulk indexes and the D1 mirror (objs must then be the map
// packs only, since the bulk indexes list every bulk object given). Generated objects get their D1 rows in
// either mode.
func runUpload(ctx context.Context, objs []*object, concurrency int, forceD1, packsOnly bool) error {
	cfg, err := loadR2()
	if err != nil {
		return err
	}
	creds, err := loadD1Creds()
	if err != nil {
		return err
	}
	d1 := &d1Client{creds: creds, api: d1API, hc: &http.Client{Timeout: 60 * time.Second}}
	logPath := filepath.Join(os.TempDir(), "cherry-assets-"+time.Now().Format("20060102-150405")+".log")
	lf, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer lf.Close()
	logger := log.New(lf, "", log.LstdFlags)
	fmt.Printf("log: %s\n", logPath)
	logger.Printf("start: %d objects, bucket %s, concurrency %d", len(objs), cfg.Bucket, concurrency)

	// Probe the signature first: a HEAD for a key that never exists must answer 404, not 403.
	client := assets.NewClient(cfg)
	if _, err := client.Head(ctx, "probe/cherry-assets-check"); !errors.Is(err, assets.ErrNotFound) {
		return fmt.Errorf("R2 probe: %v", err)
	}

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	r := &uploader{client: client, log: logger, total: int64(len(objs)), stop: stop}
	stopProgress := r.progress()
	jobs := make(chan *object)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for o := range jobs {
				if ctx.Err() == nil {
					r.one(ctx, o)
				}
			}
		}()
	}
	for _, o := range objs {
		jobs <- o
	}
	close(jobs)
	wg.Wait()
	stopProgress()

	summary := fmt.Sprintf("objects: %d done, %d uploaded, %d already present, %d failed, %.1f MB uploaded",
		r.done.Load(), r.uploaded.Load(), r.skipped.Load(), r.failed.Load(), float64(r.bytesUp.Load())/1e6)
	fmt.Println(summary)
	logger.Println(summary)
	if n := r.failed.Load(); n > 0 {
		return fmt.Errorf("%d objects failed (run stopped after %d); manifests not written, rerun to resume", n, maxFailures)
	}

	if !packsOnly {
		manifests, err := writeManifests(ctx, r.client, objs)
		if err != nil {
			return err
		}
		logger.Printf("manifests written: %d categories", len(manifests))
		fmt.Printf("manifests: %d categories written to R2\n", len(manifests))

		if err := syncD1(ctx, d1, manifests, r.putCats(), forceD1, logger); err != nil {
			return fmt.Errorf("d1: %w", err)
		}
		fmt.Println("d1: manifest mirror current")
	}
	if err := syncGenerated(ctx, d1, objs, time.Now(), logger); err != nil {
		return fmt.Errorf("d1 generated: %w", err)
	}
	return writePacks(ctx, r.client, objs)
}

// uploader counts what each object did; the counters are read after the workers finish.
type uploader struct {
	client   *assets.Client
	log      *log.Logger
	stop     context.CancelFunc // stops the run after maxFailures
	total    int64
	done     atomic.Int64
	uploaded atomic.Int64
	skipped  atomic.Int64
	failed   atomic.Int64
	bytesUp  atomic.Int64
	putMu    sync.Mutex
	putSet   map[string]bool // categories that had at least one PUT this run
}

// putCats reports which bulk categories had an object PUT this run.
func (u *uploader) putCats() map[string]bool {
	u.putMu.Lock()
	defer u.putMu.Unlock()
	return u.putSet
}

// progress logs a line every 30 seconds until the returned stop function runs.
func (u *uploader) progress() func() {
	start := time.Now()
	tick := time.NewTicker(30 * time.Second)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-tick.C:
				line := fmt.Sprintf("progress: %d/%d (%d uploaded, %d present, %d failed), %.1f MB up, %.0fs",
					u.done.Load(), u.total, u.uploaded.Load(), u.skipped.Load(), u.failed.Load(),
					float64(u.bytesUp.Load())/1e6, time.Since(start).Seconds())
				fmt.Println(line)
				u.log.Println(line)
			case <-done:
				tick.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// one handles a single object: it reads the file once (for its MD5), compares it with R2, and PUTs it if
// R2 has no identical object. Failures are retried with backoff.
func (u *uploader) one(ctx context.Context, o *object) {
	defer u.done.Add(1)
	b, err := os.ReadFile(o.path)
	if err == nil && int64(len(b)) != o.size {
		err = fmt.Errorf("file changed while planning (%d bytes, planned %d)", len(b), o.size)
	}
	if err != nil {
		u.fail(o, err)
		return
	}
	sum := md5.Sum(b)
	o.md5 = strings.ToUpper(hex.EncodeToString(sum[:]))

	var head assets.Object
	err = retry(ctx, func() error {
		var herr error
		head, herr = u.client.Head(ctx, o.key)
		return herr
	})
	if err == nil && head.Size == o.size && strings.EqualFold(strings.Trim(head.ETag, `"`), o.md5) {
		u.skipped.Add(1)
		u.log.Printf("present %s %d", o.key, o.size)
		return
	}
	if err != nil && !errors.Is(err, assets.ErrNotFound) {
		u.fail(o, err)
		return
	}
	err = retry(ctx, func() error { return u.client.Put(ctx, o.key, b, "application/octet-stream") })
	if err != nil {
		u.fail(o, err)
		return
	}
	u.uploaded.Add(1)
	u.bytesUp.Add(o.size)
	if o.cat != "" {
		u.putMu.Lock()
		if u.putSet == nil {
			u.putSet = map[string]bool{}
		}
		u.putSet[o.cat] = true
		u.putMu.Unlock()
	}
	u.log.Printf("uploaded %s %d", o.key, o.size)
}

// maxFailures stops the run: a bucket that keeps failing is not retried for every remaining object.
const maxFailures = 100

func (u *uploader) fail(o *object, err error) {
	if u.failed.Add(1) == maxFailures {
		u.stop()
	}
	u.log.Printf("FAILED %s: %v", o.key, err)
}

// retry runs fn up to four times with exponential backoff. ErrNotFound is an answer, not a failure.
func retry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
		if err = fn(); err == nil || errors.Is(err, assets.ErrNotFound) {
			return err
		}
	}
	return err
}

// writeManifests builds cherry/manifest/<cat>.json for each bulk category from the uploaded objects' MD5s and
// PUTs it. It returns the entries per category (sorted by path), for the D1 mirror.
func writeManifests(ctx context.Context, client *assets.Client, objs []*object) (map[string][]assets.Entry, error) {
	byCat := map[string][]assets.Entry{}
	for _, o := range objs {
		if o.cat == "" {
			continue
		}
		byCat[o.cat] = append(byCat[o.cat], assets.Entry{Path: o.rel, Size: o.size, MD5: o.md5})
	}
	for _, cat := range assets.Categories {
		entries := byCat[cat]
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		m := assets.Manifest{Cat: cat, Version: assets.Version(entries), Entries: entries}
		body, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		if err := retry(ctx, func() error { return client.Put(ctx, assets.ManifestKey(cat+".json"), body, "application/json") }); err != nil {
			return nil, fmt.Errorf("manifest %s: %w", cat, err)
		}
		fmt.Printf("manifest %-9s %5d entries, version %d\n", cat, len(entries), m.Version)
	}
	return byCat, nil
}

// writePacks writes cherry/manifest/packs.json from the uploaded map packs, sorted by directory and name, each with
// its origin (generated packs are cherry, the rest original). The packs are not mirrored to D1.
func writePacks(ctx context.Context, client *assets.Client, objs []*object) error {
	var entries []assets.PackEntry
	for _, o := range objs {
		if o.pack == "" {
			continue
		}
		origin := assets.OriginOriginal
		if o.generated {
			origin = assets.OriginCherry
		}
		entries = append(entries, assets.PackEntry{Dir: o.pack, Name: o.rel, Size: o.size, MD5: o.md5, Origin: origin})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Dir+"/"+entries[i].Name < entries[j].Dir+"/"+entries[j].Name
	})
	body, err := json.Marshal(assets.PackManifest{Entries: entries})
	if err != nil {
		return err
	}
	if err := retry(ctx, func() error { return client.Put(ctx, assets.ManifestKey("packs.json"), body, "application/json") }); err != nil {
		return fmt.Errorf("manifest packs: %w", err)
	}
	fmt.Printf("manifest packs: %d entries written\n", len(entries))
	return nil
}

// runVerify lists static/, cherry/ and the legacy manifest/ keys and compares them with the plan. Any legacy
// key fails the check: migrate-manifests has not run.
func runVerify(ctx context.Context, objs []*object) error {
	cfg, err := loadR2()
	if err != nil {
		return err
	}
	client := assets.NewClient(cfg)
	want := summarize(objs)
	got := map[string]int{}
	gotBytes := map[string]int64{}
	var indexes, legacy int
	for _, prefix := range []string{"static/", assets.CherryPrefix, "manifest/"} {
		token := ""
		for {
			page, next, err := client.ListPage(ctx, prefix, token)
			if err != nil {
				return err
			}
			for _, o := range page {
				switch group := verifyGroup(o.Key); group {
				case "manifest":
					indexes++
				case "legacy":
					legacy++
				default:
					got[group]++
					gotBytes[group] += o.Size
				}
			}
			if next == "" {
				break
			}
			token = next
		}
	}
	groups := map[string]bool{}
	for g := range want.count {
		groups[g] = true
	}
	for g := range got {
		groups[g] = true
	}
	bad := 0
	for _, g := range slices.Sorted(maps.Keys(groups)) {
		if got[g] != want.count[g] || gotBytes[g] != want.bytes[g] {
			bad++
		}
		fmt.Printf("%-16s plan %8d / %14d   bucket %8d / %14d\n", g, want.count[g], want.bytes[g], got[g], gotBytes[g])
	}
	if indexes != len(assets.ManifestFiles()) { // one index per category and the pack index
		bad++
	}
	fmt.Printf("%-16s bucket %d objects, want %d\n", "indexes", indexes, len(assets.ManifestFiles()))
	if legacy > 0 {
		bad++
		fmt.Printf("%-16s bucket %d objects: run migrate-manifests\n", "legacy manifest/", legacy)
	}
	if bad > 0 {
		return fmt.Errorf("%d groups differ from the plan", bad)
	}
	fmt.Println("verify: bucket matches the plan")
	return nil
}

// verifyGroup names a bucket key for verify: the plan's groups, "generated" for other cherry/ objects,
// "manifest" for the cherry/manifest/ indexes and "legacy" for the manifest/ keys migrate-manifests moves.
func verifyGroup(key string) string {
	switch {
	case strings.HasPrefix(key, "manifest/"):
		return "legacy"
	case strings.HasPrefix(key, assets.ManifestPrefix):
		return "manifest"
	case strings.HasPrefix(key, assets.CherryPrefix):
		return "generated"
	}
	parts := strings.Split(key, "/")
	switch {
	case len(parts) > 2 && parts[1] == "item":
		return "item:" + parts[2]
	case len(parts) > 2 && parts[1] == "chatting":
		return "chat:action"
	case len(parts) > 1 && (parts[1] == "garden" || parts[1] == "world"):
		return "pack:" + parts[1]
	}
	return "bulk:" + parts[1]
}
