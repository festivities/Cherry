// Command cherry-assets uploads Cherry's game-asset dataset to the R2 bucket that streaming serves
// (internal/assets). It only reads the dataset; the dataset is never written.
//
//	cherry-assets plan     counts and bytes per category and item kind (no network)
//	cherry-assets upload   upload what is missing (HEAD, then PUT), then the manifests and the D1 mirror
//	cherry-assets upload -packs-only   upload only the map packs and manifest/packs.json (no bulk manifests, no D1)
//	cherry-assets verify   list the bucket and compare it with the plan
//
// Credentials come from %LOCALAPPDATA%\Cherry\cloudflare.json and are never printed or logged. Logs go to
// %TEMP% (keys and sizes only).
package main

import (
	"context"
	"errors"
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

// defaultDataset is the santi backup, the first root: the bulk categories come from it alone.
const defaultDataset = `D:\Dev\projects\Cherry\.opencode\line-play-artifacts\santi-backup-20260921\jp.naver.lineplay.android\files`

// extraDataset is the lpbackup device backup. Only the items and map packs it has and santi lacks are added; its
// bulk categories are never read.
const extraDataset = `D:\Dev\projects\Cherry\.opencode\line-play-artifacts\lpbackup\jp.naver.lineplay.android\files`

// chatActionPrefix is the version prefix of the chat action icons in the santi cache,
// cache/action/00005_chatting_deco_action_<name>.png. The client reads them under checkresource2 ch 00005, and
// Cherry serves them without the prefix.
const chatActionPrefix = "00005_"

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
	key  string // static/<cat>/<rel>, static/item/<kind>/<id>/<rel>, static/<dir>/<name> (map pack) or static/chatting/action_hd/<name>
	path string // local file
	size int64
	cat  string // bulk category; "" for item and pack files
	pack string // map pack directory (garden or world); "" otherwise
	rel  string // path within the category or pack directory (bulk and packs only)
	md5  string // uppercase hex, set when the file is read by upload
}

// buildObjects lists every object the server streams, sorted by key. roots are dataset folders in priority order.
// The bulk categories come from roots[0] alone, so the manifests describe that dataset. Items and map packs of a
// later root are added only where no earlier root has the same key; a duplicate within one root is an error.
func buildObjects(roots []string) ([]*object, error) {
	var objs []*object
	have := map[string]bool{}
	for i, root := range roots {
		got, err := listRoot(root, i == 0)
		if err != nil {
			return nil, err
		}
		sort.Slice(got, func(a, b int) bool { return got[a].key < got[b].key })
		for j, o := range got {
			if j > 0 && o.key == got[j-1].key {
				return nil, fmt.Errorf("duplicate key %s", o.key)
			}
			if !have[o.key] {
				have[o.key] = true
				objs = append(objs, o)
			}
		}
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].key < objs[j].key })
	return objs, nil
}

// listRoot lists the objects of one dataset root. first marks the upload source: only it has bulk categories and
// chat icons, and it must have every folder. A later root without a folder (lpbackup has no world/) contributes
// nothing for it.
func listRoot(dataset string, first bool) ([]*object, error) {
	var objs []*object
	bulk := bulkSources
	if !first {
		bulk = nil
	}
	for _, src := range bulk {
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
	// Map packs: the top-level <digits>.mworldpack files of garden and world. The extracted folders beside them
	// are not served.
	for _, dir := range assets.PackDirs {
		root := filepath.Join(dataset, dir)
		ents, err := os.ReadDir(root)
		if err != nil {
			if !first && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, ent := range ents {
			if ent.IsDir() || !ent.Type().IsRegular() || !strings.HasSuffix(ent.Name(), ".mworldpack") {
				continue
			}
			if !assets.ValidPack(dir, ent.Name()) {
				return nil, fmt.Errorf("%s: not a map pack name", filepath.Join(dir, ent.Name()))
			}
			info, err := ent.Info()
			if err != nil {
				return nil, err
			}
			objs = append(objs, &object{key: "static/" + dir + "/" + ent.Name(), path: filepath.Join(root, ent.Name()), size: info.Size(), pack: dir, rel: ent.Name()})
		}
	}
	// Chat action icons come from the first root only and are not manifest entries (the client never lists them).
	if first {
		chatDir := filepath.Join(dataset, "cache", "action")
		ents, err := os.ReadDir(chatDir)
		if err != nil {
			return nil, fmt.Errorf("cache/action: %w", err)
		}
		for _, ent := range ents {
			name, ok := strings.CutPrefix(ent.Name(), chatActionPrefix)
			if !ok || !assets.ChatActionFile(name) || !ent.Type().IsRegular() {
				continue
			}
			info, err := ent.Info()
			if err != nil {
				return nil, err
			}
			objs = append(objs, &object{key: "static/chatting/action_hd/" + name, path: filepath.Join(chatDir, ent.Name()), size: info.Size()})
		}
	}
	for _, kind := range assets.ItemKinds {
		kindDir := filepath.Join(dataset, "item", kind)
		ents, err := os.ReadDir(kindDir)
		if err != nil {
			if !first && errors.Is(err, fs.ErrNotExist) {
				continue
			}
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
	return objs, nil
}

// onlyPacks keeps the map pack objects.
func onlyPacks(objs []*object) []*object {
	var out []*object
	for _, o := range objs {
		if o.pack != "" {
			out = append(out, o)
		}
	}
	return out
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

// totals summarises the objects per group ("bulk:<cat>", "pack:<dir>", "chat:action", "item:<kind>").
type totals struct {
	count map[string]int
	bytes map[string]int64
}

func summarize(objs []*object) totals {
	t := totals{count: map[string]int{}, bytes: map[string]int64{}}
	for _, o := range objs {
		group := "item:" + strings.Split(o.key, "/")[2]
		switch {
		case o.cat != "":
			group = "bulk:" + o.cat
		case o.pack != "":
			group = "pack:" + o.pack
		case strings.HasPrefix(o.key, "static/chatting/"):
			group = "chat:action"
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
	var bulkN, packN, chatN, itemN int
	var bulkB, packB, chatB, itemB int64
	for _, g := range groups {
		fmt.Printf("%-16s %8d objects %14d bytes\n", g, t.count[g], t.bytes[g])
		switch {
		case strings.HasPrefix(g, "bulk:"):
			bulkN += t.count[g]
			bulkB += t.bytes[g]
		case strings.HasPrefix(g, "pack:"):
			packN += t.count[g]
			packB += t.bytes[g]
		case strings.HasPrefix(g, "chat:"):
			chatN += t.count[g]
			chatB += t.bytes[g]
		default:
			itemN += t.count[g]
			itemB += t.bytes[g]
		}
	}
	fmt.Printf("%-16s %8d objects %14d bytes\n", "bulk total", bulkN, bulkB)
	fmt.Printf("%-16s %8d objects %14d bytes\n", "pack total", packN, packB)
	fmt.Printf("%-16s %8d objects %14d bytes\n", "chat total", chatN, chatB)
	fmt.Printf("%-16s %8d objects %14d bytes\n", "item total", itemN, itemB)
	fmt.Printf("%-16s %8d objects %14d bytes\n", "all", bulkN+packN+chatN+itemN, bulkB+packB+chatB+itemB)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cherry-assets plan|upload|verify [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	flags := flag.NewFlagSet(cmd, flag.ExitOnError)
	dataset := flags.String("dataset", defaultDataset, "first dataset files folder (read only; the bulk categories come from it)")
	concurrency := flags.Int("concurrency", 8, "parallel R2 requests (upload)")
	forceD1 := flags.Bool("force-d1", false, "rewrite the D1 manifest rows even when they look current (upload)")
	packsOnly := flags.Bool("packs-only", false, "upload only the map packs and manifest/packs.json (upload)")
	_ = flags.Parse(args)

	objs, err := buildObjects([]string{*dataset, extraDataset})
	if err != nil {
		fail("dataset: %v", err)
	}
	switch cmd {
	case "plan":
		printPlan(objs)
	case "upload":
		if *packsOnly {
			objs = onlyPacks(objs)
		}
		if err := runUpload(context.Background(), objs, *concurrency, *forceD1, *packsOnly); err != nil {
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
