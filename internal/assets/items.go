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
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "Cherry", "generated", "item")
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
