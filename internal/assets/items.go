package assets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cherry/data"
	"cherry/internal/config"
	"cherry/internal/httpx"
	"cherry/internal/opdata"
)

// Converted items: the 2014 iOS items (item.zip) that no device backup has, converted to 10.1 folders by
// cmd/cherry-assets convert (recipe itemconv-v1). They are Cherry-made, so they live under cherry/item/ in R2 and
// in the local generated tree GeneratedItemRoot, and are listed in items.json in both places. An id the index
// lists is never read from static/: the converter skipped every id a device backup has.

// GeneratedKinds are the item kinds the converter writes (the iOS archive's custom and dress folders).
var GeneratedKinds = []string{"custom", "dress"}

// ItemManifest is cherry/manifest/items.json, and the local items.json beside the generated folders: one entry per
// file of every converted item.
type ItemManifest struct {
	Entries []ItemEntry `json:"entries"`
}

// ItemEntry is one file of a converted item. Origin is always OriginCherry.
type ItemEntry struct {
	Kind   string `json:"kind"`
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	MD5    string `json:"md5"`
	Origin string `json:"origin"`
}

// ItemKey is the R2 key of a converted item's file: cherry/item/<kind>/<id>/<name>.
func ItemKey(kind string, id int64, name string) string {
	return CherryPrefix + "item/" + kind + "/" + strconv.FormatInt(id, 10) + "/" + name
}

// ItemManifestKey is the R2 key of the converted item index.
var ItemManifestKey = ManifestKey("items.json")

// validItemName accepts the files a converted item has: the artsitem, the rendered dp.png and <side>_<k>.png.
func validItemName(name string) bool {
	if name == "1409_iteminfo.artsitem" || name == "dp.png" {
		return true
	}
	side, rest, ok := strings.Cut(name, "_")
	if !ok || (side != "0" && side != "1") {
		return false
	}
	k, ok := strings.CutSuffix(rest, ".png")
	return ok && isDigits(k)
}

// DecodeItemManifest reads an items.json document strictly and checks every entry.
func DecodeItemManifest(raw []byte) (*ItemManifest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m ItemManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("items.json: %w", err)
	}
	for _, e := range m.Entries {
		switch {
		case !slices.Contains(GeneratedKinds, e.Kind):
			return nil, fmt.Errorf("items.json: kind %q is not a converted kind", e.Kind)
		case e.ID <= 0:
			return nil, fmt.Errorf("items.json: %s id %d", e.Kind, e.ID)
		case !validItemName(e.Name):
			return nil, fmt.Errorf("items.json: %s/%d: file %q", e.Kind, e.ID, e.Name)
		case e.Origin != OriginCherry:
			return nil, fmt.Errorf("items.json: %s/%d/%s: origin %q, want %s", e.Kind, e.ID, e.Name, e.Origin, OriginCherry)
		case e.Size < 0 || len(e.MD5) != 32:
			return nil, fmt.Errorf("items.json: %s/%d/%s: size or md5", e.Kind, e.ID, e.Name)
		}
	}
	return &m, nil
}

// EncodeItemManifest renders the index with entries sorted by kind, id and name (a rerun writes the same bytes).
func EncodeItemManifest(m *ItemManifest) ([]byte, error) {
	sort.SliceStable(m.Entries, func(i, j int) bool {
		a, b := m.Entries[i], m.Entries[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Name < b.Name
	})
	return json.MarshalIndent(m, "", " ")
}

// ItemIndex lists the converted items: kind, then id, then file names. A nil *ItemIndex lists nothing.
type ItemIndex struct {
	files map[string]map[int64]map[string]bool
}

// NewItemIndex indexes a checked manifest.
func NewItemIndex(m *ItemManifest) *ItemIndex {
	ix := &ItemIndex{files: map[string]map[int64]map[string]bool{}}
	for _, e := range m.Entries {
		ids := ix.files[e.Kind]
		if ids == nil {
			ids = map[int64]map[string]bool{}
			ix.files[e.Kind] = ids
		}
		names := ids[e.ID]
		if names == nil {
			names = map[string]bool{}
			ids[e.ID] = names
		}
		names[e.Name] = true
	}
	return ix
}

// Has reports whether the index lists the item.
func (ix *ItemIndex) Has(kind string, id int64) bool {
	return ix != nil && ix.files[kind][id] != nil
}

// HasFile reports whether the index lists that file of the item.
func (ix *ItemIndex) HasFile(kind string, id int64, name string) bool {
	return ix != nil && ix.files[kind][id][name]
}

// IDs are the listed ids of a kind, ascending.
func (ix *ItemIndex) IDs(kind string) []int64 {
	if ix == nil {
		return nil
	}
	ids := make([]int64, 0, len(ix.files[kind]))
	for id := range ix.files[kind] {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// GeneratedItemRoot is the local generated tree (%LOCALAPPDATA%\Cherry\generated\item): <kind>/<id>/<file> and
// items.json, written by cmd/cherry-assets convert. Tests point it at a temp dir.
var GeneratedItemRoot = defaultGeneratedItemRoot()

func defaultGeneratedItemRoot() string {
	base, err := config.DataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "generated", "item")
}

// itemconvFlag is itemconv.json: the operator switch for the converted items (embedded default on).
type itemconvFlag struct {
	Enabled bool `json:"enabled"`
}

var itemconvFile = opdata.New[itemconvFlag]("itemconv.json", data.ItemconvJSON, nil, nil)

// LoadItemConv is the startup load of itemconv.json; cmd/cherry calls it with the other operator files.
func LoadItemConv() error { return itemconvFile.Load() }

var genItems struct {
	sync.Mutex
	root string
	ix   *ItemIndex
}

// GeneratedItems is the converted item index, read once per GeneratedItemRoot from items.json there. It is nil
// while itemconv.json disables the converted items; a missing index is empty (no batch run yet), and an unreadable
// one is logged and empty.
func GeneratedItems() *ItemIndex {
	if !itemconvFile.Get().Enabled {
		return nil
	}
	genItems.Lock()
	defer genItems.Unlock()
	if genItems.ix != nil && genItems.root == GeneratedItemRoot {
		return genItems.ix
	}
	ix := readGeneratedIndex(GeneratedItemRoot)
	genItems.root, genItems.ix = GeneratedItemRoot, ix
	return ix
}

func readGeneratedIndex(root string) *ItemIndex {
	raw, err := os.ReadFile(filepath.Join(root, "items.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return &ItemIndex{}
	}
	if err != nil {
		log.Printf("cherry: converted items: %v", err)
		return &ItemIndex{}
	}
	m, err := DecodeItemManifest(raw)
	if err != nil {
		log.Printf("cherry: converted items: %v", err)
		return &ItemIndex{}
	}
	return NewItemIndex(m)
}

// parseItemRoute splits /img/read/arts_item_<kind>_<id>/<file> or /arts_item_<kind>_<id>/<file> for the converted kinds.
func parseItemRoute(p string) (kind string, id int64, name string, ok bool) {
	rest, ok := strings.CutPrefix(p, "/img/read/arts_item_")
	if !ok {
		rest, ok = strings.CutPrefix(p, "/arts_item_")
		if !ok {
			return "", 0, "", false
		}
	}
	kind, tail, ok := strings.Cut(rest, "_")
	if !ok || !slices.Contains(GeneratedKinds, kind) {
		return "", 0, "", false
	}
	idText, name, ok := strings.Cut(tail, "/")
	if !ok || !isDigits(idText) {
		return "", 0, "", false
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		return "", 0, "", false
	}
	return kind, id, name, true
}

// generatedItemKey is the R2 key of a converted item's file the index lists.
func generatedItemKey(p string) (string, bool) {
	kind, id, name, ok := parseItemRoute(p)
	if !ok || !GeneratedItems().HasFile(kind, id, name) {
		return "", false
	}
	return ItemKey(kind, id, name), true
}

// ServeGeneratedItem answers a converted item's file from the local generated tree: the route with streaming off,
// and the fallback when R2 lacks the object (ServeRoute answers first when streaming is on). It reports whether it
// answered. Ids the index does not list fall through; an unlisted file of a listed id is a 404.
func ServeGeneratedItem(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	kind, id, name, ok := parseItemRoute(r.URL.Path)
	if !ok || !GeneratedItems().Has(kind, id) {
		return false
	}
	if !GeneratedItems().HasFile(kind, id, name) {
		httpx.ServeNotFound(w)
		return true
	}
	f, err := os.Open(filepath.Join(GeneratedItemRoot, kind, strconv.FormatInt(id, 10), name))
	if err != nil {
		httpx.ServeNotFound(w)
		return true
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		httpx.ServeNotFound(w)
		return true
	}
	http.ServeContent(w, r, name, fi.ModTime(), f)
	return true
}

// FetchItemIndex copies cherry/manifest/items.json from R2 into the local generated tree when this machine has no
// items.json, so a streaming server lists and serves the converted items without running the batch. A bucket
// without the index has no converted items. cmd/cherry calls it once after Open.
func (s *Store) FetchItemIndex() error {
	if s == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(GeneratedItemRoot, "items.json")); err == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := s.client.Get(ctx, ItemManifestKey)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if _, err := DecodeItemManifest(raw); err != nil {
		return err
	}
	if err := os.MkdirAll(GeneratedItemRoot, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(GeneratedItemRoot, "items.json.part")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(GeneratedItemRoot, "items.json"))
}

// ---- originals: the item folders of the device backups (static/item/<kind>/<id>/<name>) ----

// itemIndexFile is the originals index kept in the cache dir (a dot name, so no key can collide with it).
const itemIndexFile = ".items.json"

// OriginalItemKey is the R2 key of an original item file: static/item/<kind>/<id>/<name>.
func OriginalItemKey(kind string, id int64, name string) string {
	return "static/item/" + kind + "/" + strconv.FormatInt(id, 10) + "/" + name
}

func newItemIndex() *ItemIndex { return &ItemIndex{files: map[string]map[int64]map[string]bool{}} }

func (ix *ItemIndex) add(kind string, id int64, name string) {
	ids := ix.files[kind]
	if ids == nil {
		ids = map[int64]map[string]bool{}
		ix.files[kind] = ids
	}
	names := ids[id]
	if names == nil {
		names = map[string]bool{}
		ids[id] = names
	}
	names[name] = true
}

// Names are the listed file names of an item, sorted.
func (ix *ItemIndex) Names(kind string, id int64) []string {
	if ix == nil {
		return nil
	}
	names := make([]string, 0, len(ix.files[kind][id]))
	for n := range ix.files[kind][id] {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// parseOriginalItemKey splits static/item/<kind>/<id>/<name>; ok is false for any other key.
func parseOriginalItemKey(key string) (kind string, id int64, name string, ok bool) {
	rest, ok := strings.CutPrefix(key, "static/item/")
	if !ok {
		return "", 0, "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || !slices.Contains(ItemKinds, parts[0]) || !isDigits(parts[1]) || parts[2] == "" {
		return "", 0, "", false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, "", false
	}
	return parts[0], id, parts[2], true
}

// Items returns the originals index: the item folders and files the store has. It is read once and kept (from the
// bucket's listing, or from the cache dir's copy of it). The first call waits for the read. It is nil for a nil store
// or after a failed read, which is retried after a minute.
func (s *Store) Items() *ItemIndex {
	if s == nil {
		return nil
	}
	s.itemsMu.Lock()
	defer s.itemsMu.Unlock()
	if s.client == nil { // mounted folders (tests, the upload tool): walked once per store; a test writing fixtures makes a new store
		if s.items == nil {
			ix := newItemIndex()
			for _, dir := range s.mounts["static/item/"] {
				if err := walkItems(ix, dir); err != nil {
					log.Printf("cherry: original item index: %v", err)
				}
			}
			s.items = ix
		}
		return s.items
	}
	if s.items == nil && (s.itemsFailed.IsZero() || time.Since(s.itemsFailed) >= missTTL) {
		ix, err := s.readItems()
		if err != nil {
			log.Printf("cherry: original item index: %v", err)
			s.itemsFailed = time.Now()
		} else {
			s.items = ix
		}
	}
	return s.items
}

// readItems builds the index from the cached copy, or from one listing of static/item/. The listing is saved to the
// cache dir, so a later start reads it instead of listing again.
// ponytail: the saved index is never refreshed; delete <cache>\.items.json after new original items are uploaded.
func (s *Store) readItems() (*ItemIndex, error) {
	cached := filepath.Join(s.cacheDir, itemIndexFile)
	if b, err := os.ReadFile(cached); err == nil {
		if ix, err := decodeItemIndex(b); err == nil {
			return ix, nil
		}
		log.Printf("cherry: %s is unreadable; listing the bucket again", cached)
	}
	ix, err := s.listItems()
	if err != nil {
		return nil, err
	}
	b, err := encodeItemIndex(ix)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		return nil, err
	}
	tmp := cached + ".part"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, cached); err != nil {
		return nil, err
	}
	return ix, nil
}

// listItems lists static/item/ once: each kind by the first digit of its ids (ids never start with 0), eight listings at
// a time, so the bucket's pages come in parallel.
func (s *Store) listItems() (*ItemIndex, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var prefixes []string
	for _, kind := range ItemKinds {
		for d := '1'; d <= '9'; d++ {
			prefixes = append(prefixes, "static/item/"+kind+"/"+string(d))
		}
	}
	ix := newItemIndex()
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, prefix := range prefixes {
		wg.Add(1)
		sem <- struct{}{}
		go func(prefix string) {
			defer wg.Done()
			defer func() { <-sem }()
			for token := ""; ; {
				objs, next, err := s.client.ListPage(ctx, prefix, token)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("list %s: %w", prefix, err)
					}
					mu.Unlock()
					return
				}
				mu.Lock()
				for _, o := range objs {
					if kind, id, name, ok := parseOriginalItemKey(o.Key); ok {
						ix.add(kind, id, name)
					}
				}
				mu.Unlock()
				if next == "" {
					return
				}
				token = next
			}
		}(prefix)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return ix, nil
}

// walkItems adds every <kind>/<id>/<name> file under dir (a static/item/ mount) to ix.
func walkItems(ix *ItemIndex, dir string) error {
	for _, kind := range ItemKinds {
		ents, err := os.ReadDir(filepath.Join(dir, kind))
		if err != nil {
			continue
		}
		for _, e := range ents {
			id, err := strconv.ParseInt(e.Name(), 10, 64)
			if err != nil || !e.IsDir() {
				continue
			}
			err = filepath.WalkDir(filepath.Join(dir, kind, e.Name()), func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.Type().IsRegular() {
					rel, _ := filepath.Rel(filepath.Join(dir, kind, e.Name()), p)
					ix.add(kind, id, filepath.ToSlash(rel))
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// encodeItemIndex renders the index as {kind: {id: [names]}}.
func encodeItemIndex(ix *ItemIndex) ([]byte, error) {
	out := map[string]map[string][]string{}
	for kind, ids := range ix.files {
		out[kind] = map[string][]string{}
		for id := range ids {
			out[kind][strconv.FormatInt(id, 10)] = ix.Names(kind, id)
		}
	}
	return json.Marshal(out)
}

func decodeItemIndex(b []byte) (*ItemIndex, error) {
	var in map[string]map[string][]string
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	ix := newItemIndex()
	for kind, ids := range in {
		if !slices.Contains(ItemKinds, kind) {
			return nil, fmt.Errorf("kind %q", kind)
		}
		for idText, names := range ids {
			id, err := strconv.ParseInt(idText, 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("%s id %q", kind, idText)
			}
			for _, name := range names {
				ix.add(kind, id, name)
			}
		}
	}
	return ix, nil
}

// ItemDir returns the local folder of an original item once every file the index lists for it is local (fetched when
// missing). ok is false when the index lists no file of the item or none of them can be read.
func (s *Store) ItemDir(kind string, id int64) (string, bool) {
	ix := s.Items()
	if !ix.Has(kind, id) {
		return "", false
	}
	dir, ok := s.localPath("static/item/" + kind + "/" + strconv.FormatInt(id, 10))
	if !ok {
		return "", false
	}
	if s.client == nil { // the first mounted folder that holds the item
		for _, folder := range s.mounts["static/item/"] {
			if fi, err := os.Stat(filepath.Join(folder, kind, strconv.FormatInt(id, 10))); err == nil && fi.IsDir() {
				dir = filepath.Join(folder, kind, strconv.FormatInt(id, 10))
				break
			}
		}
	}
	names := ix.Names(kind, id)
	keys := make([]string, len(names))
	for i, name := range names {
		keys[i] = OriginalItemKey(kind, id, name)
	}
	s.Prefetch(keys) // the item's files in parallel: one round-trip for the item, not one per file
	got := false
	for i, name := range names {
		if _, err := s.Local(keys[i]); err == nil {
			got = true
		} else {
			log.Printf("cherry: original item %s/%d/%s: %v", kind, id, name, err)
		}
	}
	if !got {
		return "", false
	}
	return dir, true
}

// ReadItem reads one file of an original item. It answers fs.ErrNotExist without a request when the index does not
// list the file.
func (s *Store) ReadItem(kind string, id int64, name string) ([]byte, error) {
	if !s.Items().HasFile(kind, id, name) {
		return nil, fs.ErrNotExist
	}
	return s.ReadFile(OriginalItemKey(kind, id, name))
}

// localPath is where key lives locally: its cache copy, or for a mounted store the file under the first folder of its
// longest mount prefix. It does not test that the file exists.
func (s *Store) localPath(key string) (string, bool) {
	if s.client != nil {
		return filepath.Join(s.cacheDir, filepath.FromSlash(strings.TrimPrefix(key, "static/"))), true
	}
	best := s.mountPrefix(key)
	if best == "" || len(s.mounts[best]) == 0 {
		return "", false
	}
	return filepath.Join(s.mounts[best][0], filepath.FromSlash(key[len(best):])), true
}
