package web

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"cherry/data"
	"cherry/internal/httpx"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// Avatar creation dress-up fixtures.
//
// Wire shapes were verified against libgame.so (v10.1.0.0):
//   - NaDispatcherAvatar::ResAvatarCreationSetItemsRoll @0x1c30704 reads
//     result[] entries with avatarType + setItem[], and
//     parseAvatarCreationSetItemsRoll @0x1c30090 reads cd/name/imgPath/elementCodes.
//   - NaDispatcherAvatar::ResAvatarCreationAllItems @0x1c2fc50 reads
//     result.M / result.F / result.A, each parsed by parseAvatarCreationItems
//     @0x1c2f318 as MEYE/MFACE/MMOUTH/MFHAIR/MNOSE/MBROW/METC (F* for FEMALE,
//     AEYE/AFACE/AMOUTH/AEAR/ANOSE/ATAIL/AETC for ANIMAL) plus a direct
//     DRESS array and SET{"items":[...]}. Only "cd" is read from list items.
//   - NaCreateLayer::SetPaveViewItemCellSexData @0x18d3724 maps the parsed
//     vectors to tabs: EYE=cat41, FACE=39, MOUTH=43, EAR=57, HAIR=44, NOSE=42,
//     BROW=40, TAIL=58, ETC=56 (Face Deco), DRESS=100. ETC is cat 56 for every
//     sex, so animal ETC expects FE (CAFE) items, not cat-54 AH accessories.
//   - NaDispatcherAvatar::ResAvatarCreationFaceAnalyzeWithAvatarType @0x1b1c65c
//     reads result[] entries and only their "cd".
//
// Catalog and set-cd codes carry an authentic dp.png: either already in the
// santi device restore or sourced from the iOS 2014 pack
// (lineplay-original-artifacts/ios-mini-4.3-2014/item.zip). The create/closet
// list cells resolve thumbnails through NaCreateItemCell::SetDPImage
// @0x182bed4 -> NaDpSprite::createWithIndex @0x21817c4 (flags 0) ->
// NaDpSprite::AddDPSprite @0x20cf28c, which loads "<itemFolder>/dp.png";
// there is no render-PNG fallback, and render 0_0.png part textures (some 1x1)
// are not substitutes for the authored dp.png. Original iOS pack previews are
// 140x140; NaCreateItemCell scales them to 130x100.
//
// The four Cherry closet originals (CUHA0036Z, CUON004TV, CUSH00267,
// CUAH004JH) are intentionally not advertised here: they belong to the
// first-intro client closet, and CUON004TV has no dp.png anywhere.
//
// Which items each tab lists, and the look sets, are Cherry's choice: they are
// operator data in data/dressup.json (Dressup below). The client's category
// keys and the wire shapes above stay in code.

// dressItem is a per-category item entry (only "cd" is parsed by the client).
type dressItem struct {
	CD string `json:"cd"`
}

// dressCategory is the {"items":[...]} wrapper used by category keys.
type dressCategory struct {
	Items []dressItem `json:"items"`
}

// dressSetGroup is the same wrapper carrying full set objects (SET key).
type dressSetGroup struct {
	Items []dressSet `json:"items"`
}

// dressSet is a setItem bundle. Field names verified in
// parseAvatarCreationSetItemsRoll @0x1c30090. imgPath stays empty: no
// authenticated set preview URL is known, so the client thumbnails these sets
// from their cd's local dp.png.
type dressSet struct {
	CD           string   `json:"cd"`
	Name         string   `json:"name"`
	ImgPath      string   `json:"imgPath"`
	ElementCodes []string `json:"elementCodes"`
}

// avatarSetRollEntry is one element of the roll result array.
type avatarSetRollEntry struct {
	AvatarType string     `json:"avatarType"`
	SetItem    []dressSet `json:"setItem"`
}

// Dressup is data/dressup.json: the items and look sets the creation flow offers, one look per sex.
// The client's category keys (EYE, FHAIR, ... and the sex prefix M, F or A) stay in code.
//
//	male, female, animal  {categories, dress, sets}
//	categories            {category: [item code]}: the items each create tab lists. Human categories are
//	                      EYE, FACE, MOUTH, FHAIR, NOSE, BROW, ETC; animal ones EYE, FACE, MOUTH, EAR, NOSE,
//	                      TAIL, ETC. A category left out lists nothing.
//	dress                 [item code]: the DRESS tab
//	sets                  [{cd, name, elements}]: the look sets (the SET tab). cd is the dress item of the
//	                      look and elements the codes it wears. A look needs at least one set: the first
//	                      one is the stand-in appearance for auto-match and the tutorial avatars.
//
// Item codes are checked for shape only (store.ValidItemCode); the archive is not consulted.
type Dressup struct {
	Male   Look `json:"male"`
	Female Look `json:"female"`
	Animal Look `json:"animal"`
}

// Look is one sex's entry in dressup.json.
type Look struct {
	Categories map[string][]string `json:"categories"`
	Dress      []string            `json:"dress"`
	Sets       []LookSet           `json:"sets"`
}

// LookSet is one look set in dressup.json.
type LookSet struct {
	CD       string   `json:"cd"`
	Name     string   `json:"name"`
	Elements []string `json:"elements"`
}

var dressupFile = opdata.New("dressup.json", data.DressupJSON, checkDressup, nil)

// humanCategories and animalCategories are the category names the client reads (after the sex prefix).
var (
	humanCategories  = []string{"EYE", "FACE", "MOUTH", "FHAIR", "NOSE", "BROW", "ETC"}
	animalCategories = []string{"EYE", "FACE", "MOUTH", "EAR", "NOSE", "TAIL", "ETC"}
)

// dressupCurrent is the dress-up file in use.
func dressupCurrent() *Dressup { return dressupFile.Get() }

func checkDressup(d *Dressup) error {
	if err := checkLook("male", &d.Male, humanCategories); err != nil {
		return err
	}
	if err := checkLook("female", &d.Female, humanCategories); err != nil {
		return err
	}
	return checkLook("animal", &d.Animal, animalCategories)
}

func checkLook(sex string, l *Look, allowed []string) error {
	for _, cat := range slices.Sorted(maps.Keys(l.Categories)) {
		if !slices.Contains(allowed, cat) {
			return fmt.Errorf("%s: category %q must be one of %s", sex, cat, strings.Join(allowed, ", "))
		}
		if err := checkItemCodes(sex+" "+cat, l.Categories[cat]); err != nil {
			return err
		}
	}
	if err := checkItemCodes(sex+" dress", l.Dress); err != nil {
		return err
	}
	if len(l.Sets) == 0 {
		return fmt.Errorf("%s: sets must list at least one look", sex)
	}
	for _, s := range l.Sets {
		if s.Name == "" {
			return fmt.Errorf("%s: set %s needs a name", sex, s.CD)
		}
		if len(s.Elements) == 0 {
			return fmt.Errorf("%s: set %q needs elements", sex, s.Name)
		}
		if err := checkItemCodes(sex+" set "+s.Name, append([]string{s.CD}, s.Elements...)); err != nil {
			return err
		}
	}
	return nil
}

func checkItemCodes(where string, codes []string) error {
	for _, c := range codes {
		if !store.ValidItemCode(c) {
			return fmt.Errorf("%s: %q is not an item code", where, c)
		}
	}
	return nil
}

// wireSets turns look sets into the client's setItem objects (imgPath stays empty).
func wireSets(sets []LookSet) []dressSet {
	out := make([]dressSet, 0, len(sets))
	for _, s := range sets {
		out = append(out, dressSet{CD: s.CD, Name: s.Name, ElementCodes: s.Elements})
	}
	return out
}

// humanSetsFor returns the sex-appropriate set list (anything but MALE gets the female looks).
func humanSetsFor(sex string) []dressSet {
	if sex == "MALE" {
		return wireSets(dressupCurrent().Male.Sets)
	}
	return wireSets(dressupCurrent().Female.Sets)
}

// normalizeAvatarType maps short wire types to the long form used by avatar
// info and roll entries; unknown values return "".
func normalizeAvatarType(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "M", "MALE":
		return "MALE"
	case "F", "FEMALE":
		return "FEMALE"
	case "A", "ANIMAL":
		return "ANIMAL"
	default:
		return ""
	}
}

// workableLook is the deterministic stand-in appearance for a sex, shared by
// auto-match analyze and the synthesized tutorial avatars. It is the sex's first
// look set, which is dp-ready at the set-cd level.
func workableLook(sex string) []string {
	switch sex {
	case "ANIMAL":
		return dressupCurrent().Animal.Sets[0].Elements
	case "MALE":
		return dressupCurrent().Male.Sets[0].Elements
	default:
		return dressupCurrent().Female.Sets[0].Elements
	}
}

func dressItems(codes []string) []dressItem {
	items := make([]dressItem, 0, len(codes))
	for _, cd := range codes {
		items = append(items, dressItem{CD: cd})
	}
	return items
}

// humanCatalog builds one M/F sex object for /v4/create/all/items. MFACE and
// FFACE stay empty by project decision (no CUFA human face asset exists).
// Every advertised code has a verified authentic dp.png and matching 2014
// items.db sex flags (data/dressup.json).
func humanCatalog(sex string) map[string]any {
	prefix, look := "M", &dressupCurrent().Male
	if sex == "FEMALE" {
		prefix, look = "F", &dressupCurrent().Female
	}
	out := make(map[string]any, 9)
	for _, cat := range humanCategories {
		out[prefix+cat] = dressCategory{Items: dressItems(look.Categories[cat])}
	}
	out["DRESS"] = dressItems(look.Dress)
	out["SET"] = dressSetGroup{Items: wireSets(look.Sets)}
	return out
}

// animalCatalog builds the A sex object. Animal bottoms/shoes stay set-element-only
// (no dp.png), and AETC is empty by choice (data/dressup.json): SetPaveViewItemCellSexData
// maps ETC to category 56 (FE), and no CAFE item has both device render assets and an
// authentic dp.png. The verified CAAH accessories are category 54 (AH), which has no
// create-UI tab, so they are not advertised.
func animalCatalog() map[string]any {
	look := &dressupCurrent().Animal
	out := make(map[string]any, 9)
	for _, cat := range animalCategories {
		out["A"+cat] = dressCategory{Items: dressItems(look.Categories[cat])}
	}
	out["DRESS"] = dressItems(look.Dress)
	out["SET"] = dressSetGroup{Items: wireSets(look.Sets)}
	return out
}

func handleCreateFaceSetItemsRoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	payload, _ := json.Marshal(struct {
		Result []avatarSetRollEntry `json:"result"`
	}{Result: []avatarSetRollEntry{
		{AvatarType: "MALE", SetItem: humanSetsFor("MALE")},
		{AvatarType: "FEMALE", SetItem: humanSetsFor("FEMALE")},
		{AvatarType: "ANIMAL", SetItem: wireSets(dressupCurrent().Animal.Sets)},
	}})
	httpx.WriteJSON(w, http.StatusOK, string(payload))
}

func handleCreateAllItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	payload, _ := json.Marshal(struct {
		Result map[string]any `json:"result"`
	}{Result: map[string]any{
		"M": humanCatalog("MALE"),
		"F": humanCatalog("FEMALE"),
		"A": animalCatalog(),
	}})
	httpx.WriteJSON(w, http.StatusOK, string(payload))
}

// handleCreateFaceAnalyze answers the native auto-match request. The client
// sends POST multipart/form-data with avatarType in the query
// (NaDispatcherAvatar::ReqAvatarCreationFaceAnalyzeWithAvatarType @0x1bbb8d4
// calls RequestBuilder::PostMultiParts @0x1bba868), so only POST is accepted.
// The multipart body is never consumed: it carries the captured image, which
// this deterministic local fixture replaces, so there is nothing to parse.
// The client reads only result[].cd; unsupported avatarType is a clear 400.
func handleCreateFaceAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	sex := normalizeAvatarType(r.URL.Query().Get("avatarType"))
	if sex == "" {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	payload, _ := json.Marshal(struct {
		Result []dressItem `json:"result"`
	}{Result: dressItems(workableLook(sex))})
	httpx.WriteJSON(w, http.StatusOK, string(payload))
}
