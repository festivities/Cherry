package economy

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"cherry/internal/httpx"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Garden landscapes: the items the stock Garden skin editor switches an owner's Garden map with
// (spec %TEMP%\opencode\cherry\garden-tour-20261026\landscape-spec.md). Each one is a category 84 item
// (GULS and five base-36 digits) that gardens.json lists next to its map. The Interior Shop sells it
// for Cash (prices.json landscape_cash). An account owns, for free, the landscapes of the default map
// and of the map its Garden shows now, so the editor can always switch back. lpn/garden_landscape.go
// serves the editor.

// Landscape is one landscape item as the shop and the editor see it.
type Landscape struct {
	Code      string // item code: GULS and five base-36 digits
	ID        int64  // numeric item id: the seq the editor lists
	Map       string // the Garden map it switches to
	Name      string // the shop and editor name
	Generated bool   // no archive folder: Cherry serves its item files (generatedLandscapeMap)
}

// landscapeBase is the numeric id of GULS00000: attribute G (4), type U (2), category 84 (LS).
const landscapeBase = 4e8 + 2e7 + 84e5

// landscapeTemplateID is the archive item whose 1409_iteminfo.artsitem every generated landscape copies.
// The file is the same for every landscape item (landscape-spec.md section 7).
const landscapeTemplateID = 428400149

// landscapeItemID is the numeric id of a landscape code, the inverse of itemCodeFromID. The round trip
// passes only canonical codes: lower case and a tail that is not five base-36 digits fail it.
func landscapeItemID(code string) (int64, bool) {
	if len(code) != 9 || code[:4] != "GULS" {
		return 0, false
	}
	tail, err := strconv.ParseInt(code[4:], 36, 64)
	if err != nil {
		return 0, false
	}
	id := landscapeBase + tail
	if back, ok := itemCodeFromID(id); !ok || back != code {
		return 0, false
	}
	return id, true
}

// listedLandscapes are the landscapes gardens.json lists, in file order. checkGardens has accepted
// every code, so the id always parses.
func listedLandscapes() []Landscape {
	var out []Landscape
	for _, e := range gardensFile.Get().Gardens {
		if e.Landscape == "" {
			continue
		}
		id, _ := landscapeItemID(e.Landscape)
		out = append(out, Landscape{Code: e.Landscape, ID: id, Map: e.Map, Name: e.Name, Generated: e.Generated})
	}
	return out
}

// listedLandscape is the listed landscape with item code, if gardens.json lists it.
func listedLandscape(code string) (Landscape, bool) {
	for _, l := range listedLandscapes() {
		if l.Code == code {
			return l, true
		}
	}
	return Landscape{}, false
}

// landscapeCatalog is the shop's landscape rows: every listed landscape, whether or not the archive has
// its folder (a generated one has none), so the catalog never scans for them.
func landscapeCatalog() []shopItem {
	var items []shopItem
	for _, l := range listedLandscapes() {
		items = append(items, shopItem{Code: l.Code, ID: l.ID})
	}
	return items
}

// ownedLandscapesLocked is the landscapes acc owns, in file order: the bought ones, and the landscapes
// of the default map and of the map its Garden shows now. The caller holds AccountsMu.
func ownedLandscapesLocked(acc *store.Account) []Landscape {
	if acc == nil {
		return nil
	}
	def, current := DefaultGardenMap(), gardenMapLocked(acc)
	var owned []Landscape
	for _, l := range listedLandscapes() {
		if l.Map == def || l.Map == current || slices.Contains(acc.Landscapes, l.Code) {
			owned = append(owned, l)
		}
	}
	return owned
}

// OwnedLandscapes is the landscapes aid owns; nil for an unknown account.
func OwnedLandscapes(aid string) []Landscape {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return ownedLandscapesLocked(store.AccountByAidLocked(aid))
}

// OwnedLandscapeMap is the Garden map that aid's landscape code switches to. ok is false when aid does
// not own that landscape, so the editor cannot preview it.
func OwnedLandscapeMap(aid, code string) (stem string, ok bool) {
	for _, l := range OwnedLandscapes(aid) {
		if l.Code == code {
			return l.Map, true
		}
	}
	return "", false
}

// SetGardenMap is the editor's change: aid's Garden shows stem from now on, saved at once (the next room
// enter reads it). Only a map gardens.json lists is accepted.
func SetGardenMap(aid, stem string) error {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil || !gardenListed(gardensFile.Get(), stem) {
		return errors.New("garden map " + stem + " for aid " + aid + " is refused")
	}
	prev := acc.GardenMap
	acc.GardenMap = stem
	if err := store.SaveAccountsLocked(); err != nil {
		acc.GardenMap = prev
		return err
	}
	return nil
}

// generatedLandscapeMap is the map of the generated landscape with item id; ok is false for any other id.
func generatedLandscapeMap(id int64) (string, bool) {
	for _, l := range listedLandscapes() {
		if l.Generated && l.ID == id {
			return l.Map, true
		}
	}
	return "", false
}

// ServeLandscapeFile answers the item files of a generated landscape. The client asks for them under
// /img/read/arts_item_garden_<id>/ (or /arts_item_garden_<id>/); the archive has no such folder and the
// asset stream must never be asked for them, so the caller runs this before the stream. It reports
// whether it answered: original landscapes and every other path fall through.
func ServeLandscapeFile(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/img/read/arts_item_garden_")
	if !ok {
		rest, ok = strings.CutPrefix(r.URL.Path, "/arts_item_garden_")
	}
	if !ok {
		return false
	}
	idText, file, ok := strings.Cut(rest, "/")
	if !ok || !httpx.IsAllDigits(idText) {
		return false
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		return false
	}
	stem, ok := generatedLandscapeMap(id)
	if !ok {
		return false
	}
	var body []byte
	var contentType string
	switch file {
	case "1409_iteminfo.artsitem": // the template's bytes: the same file for every landscape
		contentType = "application/octet-stream"
		body, err = os.ReadFile(filepath.Join(thumbs.DpItemRoot, "garden", strconv.Itoa(landscapeTemplateID), file))
	case "dp.png":
		contentType = "image/png"
		body, err = thumbs.LandscapeDP(stem)
	default:
		httpx.ServeNotFound(w)
		return true
	}
	if err != nil {
		log.Printf("cherry: landscape item %d %s: %v", id, file, err)
		httpx.ServeNotFound(w)
		return true
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true
}
