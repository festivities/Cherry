package main

import (
	"encoding/json"
	"net/http"
	"strings"
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

// maleSets use hair/clothes flagged use_man=1 (use_girl=0) in the 2014
// items.db over M/F-compatible face parts. Every cd and element has a verified
// dp.png (the original iOS preview is 140x140 and the create cell scales it).
// Set cd is a real dress-category item from the same look: the
// client resolves cd through SbItemTable::GetIntIdx and rejects unknown element
// codes (NaCreateLayer::RequestFaceSetItemData @0x18ae1f8).
var maleSets = []dressSet{
	{
		CD:   "CUON00164",
		Name: "Style M1",
		ElementCodes: []string{
			"CUHE0000M", "CUNO00006", "CUEY0000Q", "CUEB0000A", "CUMO0000C",
			"CUHA0004X", "CUON00164", "CUSH000DU",
		},
	},
	{
		CD:   "CUON001BD",
		Name: "Style M2",
		ElementCodes: []string{
			"CUHE0000M", "CUNO00016", "CUEY0000S", "CUEB0000G", "CUMO0000F",
			"CUHA0004Z", "CUON001BD", "CUSH000KV",
		},
	},
}

// femaleSets use hair/clothes flagged use_girl=1 (use_man=0), or dual-flagged
// items where the archive lists them under the female alternatives
// (CUON001CH, CUPA000EQ, CUSH0005G).
var femaleSets = []dressSet{
	{
		CD:   "CUON001CH",
		Name: "Style F1",
		ElementCodes: []string{
			"CUHE0000M", "CUNO00006", "CUEY000CB", "CUEB0000A", "CUMO0000C",
			"CUHA0003L", "CUON001CH", "CUSH000Q5",
		},
	},
	{
		CD:   "CUON001GS",
		Name: "Style F2",
		ElementCodes: []string{
			"CUHE0000M", "CUNO00016", "CUEY000CG", "CUEB0000G", "CUMO0000F",
			"CUHA0003E", "CUON001GS", "CUSH000VR",
		},
	},
}

// animalSets keep the original fully-renderable bottoms/shoes as elements, but
// those codes are not advertised in the DRESS catalog because they have no
// dp.png. Every cd and custom-category element is dp-ready.
var animalSets = []dressSet{
	{
		CD:   "CATO000IU",
		Name: "Animal Set 1",
		ElementCodes: []string{
			"CAFA0000S", "CAEY0000D", "CAMO00002", "CANO00001", "CAEA0000E",
			"CATL00006", "CATO000IU", "CAPA000BP", "CASH00001",
		},
	},
	{
		CD:   "CATO000GR",
		Name: "Animal Set 2",
		ElementCodes: []string{
			"CAFA0000X", "CAEY0000G", "CAMO00009", "CANO00005", "CAEA0000M",
			"CATL0000K", "CATO000GR", "CAPA0006Q", "CASH0003L",
		},
	},
}

// humanSetsFor returns the sex-appropriate set list.
func humanSetsFor(sex string) []dressSet {
	if sex == "MALE" {
		return maleSets
	}
	return femaleSets
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
// auto-match analyze and the synthesized tutorial avatars. It is
// sex-appropriate and dp-ready at the set-cd level.
func workableLook(sex string) []string {
	switch sex {
	case "ANIMAL":
		return animalSets[0].ElementCodes
	case "MALE":
		return maleSets[0].ElementCodes
	default:
		return femaleSets[0].ElementCodes
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
// items.db sex flags.
func humanCatalog(sex string) map[string]any {
	prefix := "M"
	if sex == "FEMALE" {
		prefix = "F"
	}
	// Hair's client key is MFHAIR/FFHAIR (prefix + "FHAIR").
	cats := map[string][]string{
		"EYE":   {"CUEY0000Q", "CUEY0000S"},
		"FACE":  nil,
		"MOUTH": {"CUMO0000C", "CUMO0000F"},
		"FHAIR": {"CUHA0004X", "CUHA0004Z", "CUHA00053"},
		"NOSE":  {"CUNO00006", "CUNO00016"},
		"BROW":  {"CUEB0000A", "CUEB0000G"},
		"ETC":   {"CUFE0004H", "CUFE0008L"},
	}
	dress := []string{
		"CUON00164", "CUON001BD",
		"CUTO000JN", "CUTO000T3",
		"CUPA000DG",
		"CUSH000DU", "CUSH000KV",
	}
	if sex == "FEMALE" {
		cats["EYE"] = []string{"CUEY000CB", "CUEY000CG"}
		cats["FHAIR"] = []string{"CUHA0003L", "CUHA0003E", "CUHA0003C"}
		cats["ETC"] = []string{"CUFE00013", "CUFE00016"}
		dress = []string{
			"CUON001CH", "CUON001GS",
			"CUTO000AQ",
			"CUPA00091", "CUPA000DA", "CUPA000EP", "CUPA000EQ",
			"CUSH000Q5", "CUSH0005G", "CUSH000VR",
		}
	}
	out := make(map[string]any, 9)
	for key, codes := range cats {
		out[prefix+key] = dressCategory{Items: dressItems(codes)}
	}
	out["DRESS"] = dressItems(dress)
	out["SET"] = dressSetGroup{Items: humanSetsFor(sex)}
	return out
}

// animalCatalog builds the A sex object. AETC stays empty: SetPaveViewItemCellSexData
// maps ETC to category 56 (FE), and no CAFE item has both device render assets
// and an authentic dp.png. The verified CAAH accessories are category 54 (AH),
// which has no create-UI tab, so they are not advertised.
func animalCatalog() map[string]any {
	cats := map[string][]string{
		"EYE":   {"CAEY0000D", "CAEY0000G"},
		"FACE":  {"CAFA0000S", "CAFA0000X"},
		"MOUTH": {"CAMO00002", "CAMO00009"},
		"EAR":   {"CAEA0000E", "CAEA0000M"},
		"NOSE":  {"CANO00001", "CANO00005"},
		"TAIL":  {"CATL00006", "CATL0000K"},
		"ETC":   nil,
	}
	out := make(map[string]any, 9)
	for key, codes := range cats {
		out["A"+key] = dressCategory{Items: dressItems(codes)}
	}
	// Animal bottoms/shoes have no dp.png and stay set-element-only.
	out["DRESS"] = dressItems([]string{"CATO000GR", "CATO000IU"})
	out["SET"] = dressSetGroup{Items: animalSets}
	return out
}

func handleCreateFaceSetItemsRoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	payload, _ := json.Marshal(struct {
		Result []avatarSetRollEntry `json:"result"`
	}{Result: []avatarSetRollEntry{
		{AvatarType: "MALE", SetItem: humanSetsFor("MALE")},
		{AvatarType: "FEMALE", SetItem: humanSetsFor("FEMALE")},
		{AvatarType: "ANIMAL", SetItem: animalSets},
	}})
	writeJSON(w, http.StatusOK, string(payload))
}

func handleCreateAllItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	payload, _ := json.Marshal(struct {
		Result map[string]any `json:"result"`
	}{Result: map[string]any{
		"M": humanCatalog("MALE"),
		"F": humanCatalog("FEMALE"),
		"A": animalCatalog(),
	}})
	writeJSON(w, http.StatusOK, string(payload))
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
		serveNotFound(w)
		return
	}
	sex := normalizeAvatarType(r.URL.Query().Get("avatarType"))
	if sex == "" {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	payload, _ := json.Marshal(struct {
		Result []dressItem `json:"result"`
	}{Result: dressItems(workableLook(sex))})
	writeJSON(w, http.StatusOK, string(payload))
}
