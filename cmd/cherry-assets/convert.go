package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"cherry/internal/assets"
	"cherry/internal/itemconv"
	"cherry/internal/thumbs"
)

// runConvert converts the iOS-only items (item.zip ids that no device backup has) into the local generated tree
// root and writes its items.json. Nothing leaves this machine: upload sends the tree later. Rerunning rewrites the
// same bytes.
func runConvert(zipPath, root string) error {
	if root == "" {
		return fmt.Errorf("no generated item root (no %%LOCALAPPDATA%%)")
	}
	rep, err := itemconv.Build(itemconv.Options{Zip: zipPath, Out: root, Original: originalItem, Render: thumbs.RenderDP})
	if err != nil {
		return err
	}
	m := &assets.ItemManifest{}
	for _, f := range rep.Files {
		m.Entries = append(m.Entries, assets.ItemEntry{Kind: f.Kind, ID: f.ID, Name: f.Name, Size: f.Size, MD5: f.MD5, Origin: assets.OriginCherry})
	}
	body, err := assets.EncodeItemManifest(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "items.json"), append(body, '\n'), 0o644); err != nil {
		return err
	}
	items := map[string]int{}
	for _, f := range rep.Files {
		if f.Name == "1409_iteminfo.artsitem" {
			items[f.Kind]++
		}
	}
	for _, kind := range itemconv.Kinds {
		fmt.Printf("%s: %d converted, %d device-backup originals skipped, %d undecodable category skipped, %d without art, %d failed\n",
			kind, items[kind], rep.Originals[kind], rep.Undecodable[kind], rep.NoArt[kind], rep.Failed[kind])
	}
	fmt.Printf("files: %d, %.1f MB in %s\n", len(rep.Files), float64(rep.Bytes)/1e6, root)
	log.Printf("itemconv: %d undecodable category ids skipped (the client has no category for them)", rep.Undecodable["custom"]+rep.Undecodable["dress"])
	for _, e := range rep.Errors {
		fmt.Printf("failed: %s\n", e)
	}
	return nil
}

// originalItem reports whether a device backup (archiveRoots) has the item folder: originals always win.
func originalItem(kind string, id int64) bool {
	for _, r := range archiveRoots {
		if fi, err := os.Stat(filepath.Join(r, "item", kind, strconv.FormatInt(id, 10))); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// withGenerated adds the converted items of the local generated tree to objs: one object per file listed in its
// items.json, under cherry/item/. Without an items.json there are none. checkOrigins runs over the whole set.
func withGenerated(objs []*object, roots []string, root string) ([]*object, error) {
	raw, err := os.ReadFile(filepath.Join(root, "items.json"))
	if os.IsNotExist(err) {
		return objs, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := assets.DecodeItemManifest(raw)
	if err != nil {
		return nil, err
	}
	for _, e := range m.Entries {
		p := filepath.Join(root, e.Kind, strconv.FormatInt(e.ID, 10), e.Name)
		fi, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("converted item %s/%d/%s: %w (rerun convert)", e.Kind, e.ID, e.Name, err)
		}
		if fi.Size() != e.Size {
			return nil, fmt.Errorf("converted item %s: %d bytes, index says %d (rerun convert)", p, fi.Size(), e.Size)
		}
		recipe := itemconv.Recipe
		if e.Name == "dp.png" {
			recipe = itemconv.DpRecipe
		}
		objs = append(objs, &object{key: assets.ItemKey(e.Kind, e.ID, e.Name), path: p, size: e.Size,
			generated: true, recipe: recipe})
	}
	sortObjects(objs)
	if err := checkOrigins(objs, roots); err != nil {
		return nil, err
	}
	return objs, nil
}

// itemRef is the "kind/id" of an item file key (static/item/<kind>/<id>/<file> or cherry/item/...), or "".
func itemRef(key string) string {
	parts := strings.Split(key, "/")
	if len(parts) != 5 || parts[1] != "item" {
		return ""
	}
	return parts[2] + "/" + parts[3]
}

// sortObjects orders objects by key, as buildObjects does.
func sortObjects(objs []*object) {
	sort.Slice(objs, func(i, j int) bool { return objs[i].key < objs[j].key })
}

// hasItemIndex reports whether objs include a converted item, so the bucket holds cherry/manifest/items.json.
func hasItemIndex(objs []*object) bool {
	for _, o := range objs {
		if o.generated && itemRef(o.key) != "" {
			return true
		}
	}
	return false
}
