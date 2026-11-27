package economy

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"cherry/internal/assets"
	"cherry/internal/itemcode"
	"cherry/internal/room"
	"cherry/internal/thumbs"
)

// CatalogItem describes one nameable item in Cherry's catalog (cosmetics, faces, furniture, pets, Garden landscapes and
// remodeling layouts) for the staff Item names page.
type CatalogItem struct {
	Code          string // canonical 9-character item code (e.g. CUHA0066I)
	ID            int64  // numeric item id (itemcode.ID)
	Group         string // cosmetics | faces | furniture | pets | garden | remodel
	Category      string // subcategory code (e.g. HA, TO, EY, UWLPP, UDOG, LS, LA)
	CategoryLabel string // human-readable subcategory label
	Species       string // human | animal | "" (all)
	DefaultName   string // synthetic / operator-file name before data/item_names.json and live DB overrides
	Animated      bool   // has .aniproj animation (RARE)
	Converted     bool   // converted 2014 iOS item (cherry/item/)
}

// ItemGroups lists the catalog groups in display order.
var ItemGroups = []struct {
	Key   string
	Label string
}{
	{"cosmetics", "Cosmetics"},
	{"faces", "Faces"},
	{"furniture", "Furniture"},
	{"pets", "Pets"},
	{"garden", "Garden"},
	{"remodel", "Remodel"},
}

type itemCatalogSnapshot struct {
	cat     *shopCatalog
	remodel *Remodel
	prices  *Prices
	pets    *PetCatalog
	items   []CatalogItem
	byCode  map[string]CatalogItem
}

var (
	itemCatalogMu   sync.Mutex
	itemCatalogSnap *itemCatalogSnapshot
)

// CatalogItems returns every nameable item in the current catalog (cosmetics, faces, furniture, pets, Garden landscapes
// and remodeling layouts), sorted by group order then code. The slice is cached until the underlying catalog or operator
// data changes and must not be mutated by callers.
func CatalogItems() []CatalogItem {
	return loadItemCatalog().items
}

// CatalogItemByCode looks up one item in the nameable catalog by its canonical 9-character code.
func CatalogItemByCode(code string) (CatalogItem, bool) {
	it, ok := loadItemCatalog().byCode[code]
	return it, ok
}

func loadItemCatalog() *itemCatalogSnapshot {
	cat := loadCatalog()
	rem := ActiveRemodel()
	pr := ActivePrices()
	pt := ActivePets()

	itemCatalogMu.Lock()
	defer itemCatalogMu.Unlock()
	if itemCatalogSnap != nil && itemCatalogSnap.cat == cat && itemCatalogSnap.remodel == rem && itemCatalogSnap.prices == pr && itemCatalogSnap.pets == pt {
		return itemCatalogSnap
	}

	seen := map[string]bool{}
	var items []CatalogItem
	add := func(it CatalogItem) {
		if it.Code == "" || seen[it.Code] {
			return
		}
		seen[it.Code] = true
		items = append(items, it)
	}

	// 1. Cosmetics (fashion clothing & accessories)
	for _, it := range cat.fashion {
		species := "human"
		if strings.HasPrefix(it.Code, "CA") {
			species = "animal"
		}
		catKey := it.Code[2:4]
		converted := isConvertedItem(cat, it.ID, "custom", "dress")
		add(CatalogItem{
			Code:          it.Code,
			ID:            it.ID,
			Group:         "cosmetics",
			Category:      catKey,
			CategoryLabel: cosmeticCategoryLabel(catKey, pr),
			Species:       species,
			DefaultName:   FallbackItemName(it.Code),
			Animated:      it.Animated,
			Converted:     converted,
		})
	}

	// 2. Faces (human Face Shop parts + animal Face Shop parts)
	addFaceList := func(codes []string, species string) {
		sorted := slices.Clone(codes)
		slices.Sort(sorted)
		for _, code := range sorted {
			id, ok := itemcode.ID(code)
			if !ok {
				continue
			}
			// When an asset store is loaded, list only faces that actually have item files (matches Face Shop).
			if cat.originals != nil && !hasAnyFaceFolder(cat, id) {
				continue
			}
			catKey := code[2:4]
			converted := isConvertedItem(cat, id, "custom", "dress")
			add(CatalogItem{
				Code:          code,
				ID:            id,
				Group:         "faces",
				Category:      catKey,
				CategoryLabel: cosmeticCategoryLabel(catKey, pr),
				Species:       species,
				DefaultName:   FallbackItemName(code),
				Converted:     converted,
			})
		}
	}
	addFaceList(faceCatalog(), "human")
	addFaceList(animalFaceCatalog(), "animal")

	// 3. Furniture / Interior (regular interior + paired Minipet room items)
	for _, it := range cat.interior {
		catKey, label := furnitureCategory(it.Code, pr)
		add(CatalogItem{
			Code:          it.Code,
			ID:            it.ID,
			Group:         "furniture",
			Category:      catKey,
			CategoryLabel: label,
			DefaultName:   FallbackItemName(it.Code),
			Animated:      it.Animated,
		})
	}
	petItemCodes := make([]string, 0, len(cat.petItems))
	for code := range cat.petItems {
		petItemCodes = append(petItemCodes, code)
	}
	slices.Sort(petItemCodes)
	for _, code := range petItemCodes {
		it := cat.petItems[code]
		add(CatalogItem{
			Code:          it.Code,
			ID:            it.ID,
			Group:         "furniture",
			Category:      "RUPI",
			CategoryLabel: "Minipet Item",
			DefaultName:   FallbackItemName(it.Code),
			Animated:      it.Animated,
		})
	}

	// 4. Pets
	for _, it := range cat.pet {
		catKey := it.Catg
		if catKey == "" {
			catKey = "UTOY"
		}
		add(CatalogItem{
			Code:          it.Code,
			ID:            it.ID,
			Group:         "pets",
			Category:      catKey,
			CategoryLabel: petCategoryLabel(catKey),
			DefaultName:   FallbackItemName(it.Code),
			Animated:      it.Animated,
		})
	}

	// 5. Garden landscapes
	for _, it := range cat.landscape {
		add(CatalogItem{
			Code:          it.Code,
			ID:            it.ID,
			Group:         "garden",
			Category:      "LS",
			CategoryLabel: "Garden Landscape",
			DefaultName:   FallbackItemName(it.Code),
		})
	}

	// 6. Remodeling layouts
	for _, row := range rem.Rows {
		id, ok := itemcode.ID(row.Code)
		if !ok {
			continue
		}
		add(CatalogItem{
			Code:          row.Code,
			ID:            id,
			Group:         "remodel",
			Category:      row.Tab,
			CategoryLabel: remodelTabLabel(row.Tab),
			DefaultName:   FallbackItemName(row.Code),
		})
	}

	byCode := make(map[string]CatalogItem, len(items))
	for _, it := range items {
		byCode[it.Code] = it
	}
	snap := &itemCatalogSnapshot{cat: cat, remodel: rem, prices: pr, pets: pt, items: items, byCode: byCode}
	itemCatalogSnap = snap
	return snap
}

func isConvertedItem(cat *shopCatalog, id int64, kinds ...string) bool {
	for _, k := range kinds {
		if cat.originals.Has(k, id) {
			return false
		}
	}
	for _, k := range kinds {
		if cat.generated.Has(k, id) {
			return true
		}
	}
	return false
}

func hasAnyFaceFolder(cat *shopCatalog, id int64) bool {
	for _, k := range []string{"custom", "dress"} {
		if cat.originals.Has(k, id) || cat.generated.Has(k, id) {
			return true
		}
	}
	return false
}

func cosmeticCategoryLabel(cat string, pr *Prices) string {
	switch cat {
	case "AH":
		return "Head Accessory"
	case "AE":
		return "Body Accessory"
	case "FE":
		return "Face Accessory"
	case "FA":
		return "Animal Skin"
	case "EA":
		return "Animal Ears"
	case "TL":
		return "Animal Tail"
	}
	if n, ok := pr.ClothingNames[cat]; ok && n != "" {
		return n
	}
	return cat
}

func furnitureCategory(code string, pr *Prices) (key, label string) {
	rc := room.RoomCategoryCode(code)
	if rc != "" {
		if n, ok := pr.RoomNames[rc]; ok && n != "" {
			return rc, n
		}
		return rc, rc
	}
	if len(code) >= 4 {
		return code[2:4], "Room Item (" + code[2:4] + ")"
	}
	return "ROOM", "Room Item"
}

func petCategoryLabel(catg string) string {
	switch catg {
	case "UDOG":
		return "Dog"
	case "UCAT":
		return "Cat"
	case "URIDE":
		return "Ride Pet"
	default:
		return "Toy Pet"
	}
}

func remodelTabLabel(tab string) string {
	switch tab {
	case "URMEXT":
		return "1F Room"
	case "URM2F":
		return "2F Room"
	case "URM3F":
		return "3F Room"
	default:
		return "Room Layout"
	}
}

// ServeItemPreview writes the PNG thumbnail preview (dp.png) for canonical 9-character item code directly to w without
// redirecting off-origin, so the staff UI (CSP default-src 'self') can display previews for every item kind: original
// cosmetics/faces/furniture/pets, converted iOS items, Garden landscapes (original & generated), and Remodeling layouts.
// Returns true if a response was written, false if the code has no preview.
func ServeItemPreview(w http.ResponseWriter, code string) bool {
	id, ok := itemcode.ID(code)
	if !ok {
		return false
	}
	idText := strconv.FormatInt(id, 10)

	// 1. Garden landscapes (generated or original)
	if strings.HasPrefix(code, "GULS") {
		if stem, gen := generatedLandscapeMap(id); gen {
			sky, err := gardenSky(stem)
			if err != nil {
				log.Printf("cherry: item preview %s: %v", code, err)
				return false
			}
			body, err := thumbs.LandscapeDP(stem, sky)
			if err != nil {
				log.Printf("cherry: item preview %s: %v", code, err)
				return false
			}
			writePreviewPNG(w, body)
			return true
		}
		if b, err := assets.Archive.ReadItem("garden", id, "dp.png"); err == nil {
			writePreviewPNG(w, b)
			return true
		}
		return false
	}

	// 2. Remodeling layouts (may alias original room-grid art via artFrom)
	if row, _, ok := remodelByCode(code); ok {
		srcID := row.ArtFrom
		if srcID == "" {
			srcID = idText
		}
		if assets.Archive.Items().Has("interior", mustInt64(srcID)) {
			w.Header().Set("Cache-Control", "private, max-age=3600")
			thumbs.ServeDpPNG(w, "interior", srcID)
			return true
		}
		return false
	}

	// 3. Original items (custom, dress, interior, tile, pet) — authentic dp.png or rendered composite
	if orig := assets.Archive.Items(); orig != nil {
		for _, kind := range []string{"custom", "dress", "interior", "tile", "pet"} {
			if orig.Has(kind, id) {
				w.Header().Set("Cache-Control", "private, max-age=3600")
				thumbs.ServeDpPNG(w, kind, idText)
				return true
			}
		}
	}

	// 4. Converted iOS items (cherry/item/<kind>/<id>/dp.png in R2 or local GeneratedItemRoot)
	if gen := assets.GeneratedItems(); gen != nil {
		for _, kind := range assets.GeneratedKinds {
			if !gen.HasFile(kind, id, "dp.png") {
				continue
			}
			if assets.Archive != nil {
				if b, err := assets.Archive.ReadFile(assets.ItemKey(kind, id, "dp.png")); err == nil {
					writePreviewPNG(w, b)
					return true
				}
			}
			if assets.GeneratedItemRoot != "" {
				if b, err := os.ReadFile(filepath.Join(assets.GeneratedItemRoot, kind, idText, "dp.png")); err == nil {
					writePreviewPNG(w, b)
					return true
				}
			}
		}
	}
	return false
}

func writePreviewPNG(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func mustInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
