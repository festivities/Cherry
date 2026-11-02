package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// The originals the store serves that no device-backup root holds. Each listed folder is read from this machine and
// stored under static/<top>/ with the folder's own layout, as byte copies: the 10.1.0.0 APK's avatar_data tree and the
// 2016 APK's fish silhouette (lineplay-original-assets, never read by Cherry at runtime), and the lpbackup DailyBonus
// folder (the calendar skin, read from the device backup's files/). A Cherry-made file never goes up (cherryMadeName).
const originalAssetsRoot = `D:\Dev\projects\Cherry\.opencode\line-play-artifacts\lineplay-original-assets`

// origSource is one listed folder: the files under base\top\tree (tree "" is the whole top folder), or one file of top
// (file, a path under top).
type origSource struct {
	top  string
	base string
	tree string
	file string
}

// origSources are the only such originals uploaded. The key folder of each is its top name (D1 cat = top, path = the
// path under it).
var origSources = []origSource{
	{top: "android-10.1.0.0-2024", base: originalAssetsRoot, tree: "avatar_data"},
	{top: "android-4.0.0.0-2016", base: originalAssetsRoot, file: "animation/fish/fish_ani_0001.png"},
	{top: "DailyBonus", base: extraDataset},
}

// origRoot is the folder a source's files are under.
func origRoot(src origSource) string {
	return filepath.Join(src.base, src.top, filepath.FromSlash(src.tree))
}

// joinRel is the path under top of a file found under origRoot.
func joinRel(tree, rel string) string {
	if tree == "" {
		return rel
	}
	return tree + "/" + rel
}

// cherryMadeName reports whether a path under a source is Cherry-made: a generated calendar tile (cherry_day*) or
// anything under a -cherry/ folder (the probe the server answers, never a device file).
func cherryMadeName(rel string) bool {
	return strings.HasPrefix(path.Base(rel), "cherry_day") || strings.Contains("/"+rel, "/-cherry/")
}

// origObjects lists the origin originals of origSources, sorted by key. object.source is the top folder and object.rel the
// path under it, so the D1 mirror rows and the plan group are the source's own.
func origObjects() ([]*object, error) {
	var objs []*object
	for _, src := range origSources {
		if src.file != "" {
			p := filepath.Join(src.base, src.top, filepath.FromSlash(src.file))
			fi, err := os.Stat(p)
			if err != nil {
				return nil, err
			}
			objs = append(objs, &object{key: "static/" + src.top + "/" + src.file, path: p, size: fi.Size(), source: src.top, rel: src.file})
			continue
		}
		root := origRoot(src)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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
			rel := joinRel(src.tree, filepath.ToSlash(mustRel(root, p)))
			objs = append(objs, &object{key: "static/" + src.top + "/" + rel, path: p, size: info.Size(), source: src.top, rel: rel})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src.top, err)
		}
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].key < objs[j].key })
	return objs, nil
}

// origSourceOf names the origSources folder a bucket key belongs to ("" for any other key).
func origSourceOf(key string) string {
	for _, src := range origSources {
		if strings.HasPrefix(key, "static/"+src.top+"/") {
			return src.top
		}
	}
	return ""
}

// checkOrig refuses an origin original that is not one of origSources: it must be the listed file or sit under the listed
// folder, with the folder's own key, and it must not be Cherry-made.
func checkOrig(o *object) error {
	for _, src := range origSources {
		if o.source != src.top || o.key != "static/"+src.top+"/"+o.rel {
			continue
		}
		if cherryMadeName(o.rel) {
			return fmt.Errorf("%s is Cherry-made (cherry_day or -cherry/), not an original", o.key)
		}
		top := filepath.Join(src.base, src.top)
		switch {
		case src.file != "":
			if o.rel == src.file && o.path == filepath.Join(top, filepath.FromSlash(src.file)) {
				return nil
			}
		case src.tree == "" || strings.HasPrefix(o.rel, src.tree+"/"):
			if underAny(o.path, []string{origRoot(src)}) {
				return nil
			}
		}
	}
	return fmt.Errorf("original %s is not in one of the listed folders (origSources)", o.key)
}

// onlySource keeps the objects of one origin source.
func onlySource(objs []*object, source string) []*object {
	var out []*object
	for _, o := range objs {
		if o.source == source {
			out = append(out, o)
		}
	}
	return out
}

// origMounts maps each origin key folder to its local folder, for the tool's own reads (convert renders the avatar rigs).
func origMounts() map[string][]string {
	m := map[string][]string{}
	for _, src := range origSources {
		m["static/"+src.top+"/"] = []string{filepath.Join(src.base, src.top)}
	}
	return m
}

// originNames lists the origin source names the -only flag accepts.
func originNames() []string {
	names := make([]string, 0, len(origSources))
	for _, src := range origSources {
		names = append(names, src.top)
	}
	return names
}
