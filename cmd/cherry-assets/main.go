// Command cherry-assets uploads Cherry's game-asset dataset to the R2 bucket that streaming serves
// (internal/assets). It only reads the dataset; the dataset is never written.
//
//	cherry-assets plan     counts and bytes per category and item kind (no network)
//	cherry-assets upload   upload what is missing (HEAD, then PUT), then the manifests and the D1 mirror
//	cherry-assets verify   list the bucket and compare it with the plan
//
// Credentials come from %LOCALAPPDATA%\Cherry\cloudflare.json and are never printed or logged. Logs go to
// %TEMP% (keys and sizes only).
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"cherry/internal/assets"
)

const defaultDataset = `D:\Dev\projects\Cherry\.opencode\line-play-artifacts\santi-backup-20260921\jp.naver.lineplay.android\files`

// bulkSource maps a bulk category to its dataset folder. only, when set, limits the category to those
// files (paths relative to dir); subui is the one plist of UIResource, tx the string table.
type bulkSource struct {
	cat, dir string
	only     []string
}

var bulkSources = []bulkSource{
	{cat: "animation", dir: "animation"},
	{cat: "sound", dir: "sound"},
	{cat: "ui", dir: "UIResource"},
	{cat: "subui", dir: "UIResource", only: []string{"MarbleChallengePopup.plist"}},
	{cat: "tx", dir: "stringtable", only: []string{"arts_strings.ast"}},
}

// object is one R2 object to upload. Bulk objects also feed their category's manifest.
type object struct {
	key  string // static/<cat>/<rel> or static/item/<kind>/<id>/<rel>
	path string // local file
	size int64
	cat  string // bulk category; "" for item files
	rel  string // path within the category (bulk only)
	md5  string // uppercase hex, set when the file is read by upload
}

// buildObjects lists every object the server streams, sorted by key.
func buildObjects(dataset string) ([]*object, error) {
	var objs []*object
	for _, src := range bulkSources {
		root := filepath.Join(dataset, src.dir)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			rel := filepath.ToSlash(mustRel(root, p))
			if src.only != nil && !slices.Contains(src.only, rel) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			objs = append(objs, &object{key: "static/" + src.cat + "/" + rel, path: p, size: info.Size(), cat: src.cat, rel: rel})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src.dir, err)
		}
	}
	for _, kind := range assets.ItemKinds {
		kindDir := filepath.Join(dataset, "item", kind)
		ents, err := os.ReadDir(kindDir)
		if err != nil {
			return nil, err
		}
		for _, ent := range ents {
			// Only numeric folders are item ids; the client never requests any other name.
			if !ent.IsDir() || !isDigits(ent.Name()) {
				continue
			}
			idDir := filepath.Join(kindDir, ent.Name())
			err := filepath.WalkDir(idDir, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() || !d.Type().IsRegular() {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				rel := filepath.ToSlash(mustRel(idDir, p))
				key := "static/item/" + kind + "/" + ent.Name() + "/" + rel
				objs = append(objs, &object{key: key, path: p, size: info.Size()})
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].key < objs[j].key })
	for i := 1; i < len(objs); i++ {
		if objs[i].key == objs[i-1].key {
			return nil, fmt.Errorf("duplicate key %s", objs[i].key)
		}
	}
	return objs, nil
}

func mustRel(base, p string) string {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		panic(err) // both paths come from the same walk
	}
	return rel
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// totals summarises the objects per category ("bulk:<cat>", "item:<kind>").
type totals struct {
	count map[string]int
	bytes map[string]int64
}

func summarize(objs []*object) totals {
	t := totals{count: map[string]int{}, bytes: map[string]int64{}}
	for _, o := range objs {
		group := "item:" + strings.Split(o.key, "/")[2]
		if o.cat != "" {
			group = "bulk:" + o.cat
		}
		t.count[group]++
		t.bytes[group] += o.size
	}
	return t
}

func printPlan(objs []*object) {
	t := summarize(objs)
	groups := make([]string, 0, len(t.count))
	for g := range t.count {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	var bulkN, itemN int
	var bulkB, itemB int64
	for _, g := range groups {
		fmt.Printf("%-16s %8d objects %14d bytes\n", g, t.count[g], t.bytes[g])
		if strings.HasPrefix(g, "bulk:") {
			bulkN += t.count[g]
			bulkB += t.bytes[g]
		} else {
			itemN += t.count[g]
			itemB += t.bytes[g]
		}
	}
	fmt.Printf("%-16s %8d objects %14d bytes\n", "bulk total", bulkN, bulkB)
	fmt.Printf("%-16s %8d objects %14d bytes\n", "item total", itemN, itemB)
	fmt.Printf("%-16s %8d objects %14d bytes\n", "all", bulkN+itemN, bulkB+itemB)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cherry-assets plan|upload|verify [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	flags := flag.NewFlagSet(cmd, flag.ExitOnError)
	dataset := flags.String("dataset", defaultDataset, "dataset files folder (read only)")
	concurrency := flags.Int("concurrency", 8, "parallel R2 requests (upload)")
	forceD1 := flags.Bool("force-d1", false, "rewrite the D1 manifest rows even when they look current (upload)")
	_ = flags.Parse(args)

	objs, err := buildObjects(*dataset)
	if err != nil {
		fail("dataset: %v", err)
	}
	switch cmd {
	case "plan":
		printPlan(objs)
	case "upload":
		if err := runUpload(context.Background(), objs, *concurrency, *forceD1); err != nil {
			fail("upload: %v", err)
		}
	case "verify":
		if err := runVerify(context.Background(), objs); err != nil {
			fail("verify: %v", err)
		}
	default:
		fail("unknown command %q", cmd)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "cherry-assets: "+format+"\n", args...)
	os.Exit(1)
}

// credsPath is %LOCALAPPDATA%\Cherry\cloudflare.json.
func credsPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Cherry", "cloudflare.json"), nil
}

func loadR2() (assets.Config, error) {
	p, err := credsPath()
	if err != nil {
		return assets.Config{}, err
	}
	return assets.LoadConfig(p)
}
