package store

import (
	"encoding/json"
	"slices"
	"strconv"
)

// InitAvatarBaseItem @0x29a7f84 (called from AvActorManager::Initialize): three lists, one per AvActor::GetSex
// (0 MALE, 1 FEMALE, 2 ANIMAL; AvAvatar::_FillEquipBaseSlot reads g_vecBaseItem[3*sex]). Cherry keeps the clothes,
// the hair and the animal skin (CAFA0005O, the FA entry of the ANIMAL list); the face starters of each list are
// owned faces here, as they are for humans.
var AvatarBaseItems = map[string][]string{
	"MALE":   {"CUTO0011X", "CUPA000LO", "CUSH0009Q", "CUHA00001"},
	"FEMALE": {"CUTO0011X", "CUPA000LO", "CUSH0009Q", "CUHA00004"},
	"ANIMAL": {"CATO000L8", "CAPA000BP", "CASH00001", "CAFA0005O"},
}

func ItemSlot(code string) string {
	if len(code) < 4 {
		return ""
	}
	return code[2:4]
}

// IsAvatarBaseItem reports whether save/v2 skips code as a base item. Every account skips the human bases, as
// before; an ANIMAL account also skips its animal base, so a human save never skips an animal code.
func IsAvatarBaseItem(gender, code string) bool {
	for key, list := range AvatarBaseItems {
		if key == "ANIMAL" && gender != "ANIMAL" {
			continue
		}
		if slices.Contains(list, code) {
			return true
		}
	}
	return false
}

func AppearanceItemCodes(gender string, equipped []string) []string {
	have := map[string]bool{}
	for _, c := range equipped {
		s := ItemSlot(c)
		have[s] = true
		if s == "ON" {
			have["TO"], have["PA"] = true, true
		}
	}
	base := AvatarBaseItems[gender]
	if base == nil {
		base = AvatarBaseItems["FEMALE"]
	}
	out := append([]string(nil), equipped...)
	for _, c := range base {
		if have[ItemSlot(c)] {
			continue
		}
		out = append(out, c)
		have[ItemSlot(c)] = true
	}
	return out
}

type AvatarSaveItem struct {
	ItemCode *string         `json:"itemCode"`
	InvenSeq json.RawMessage `json:"invenSeq"`
}

func AvatarInfoForAccount(acc AccountSnapshot) AvatarInfoResult {
	gender, skin := acc.Gender, acc.Skin
	if gender == "" {
		gender = "FEMALE"
	}
	if skin == "" {
		skin = "1"
	}
	return AvatarInfoResult{
		AvatarID: acc.Aid, Name: acc.Name, Gender: gender, SType: "NORMAL", Skin: skin,
		Country: acc.Country, Items: avatarItemsFromInventory(AppearanceItemCodes(gender, acc.ItemCodes), acc.InventoryCodes, acc.DyeColors),
		PetProfiles: []string{},
	}
}

// ClosetOrder is the order inven/closet/items/all lists owned codes in: wearables in owned order, then the basic faces.
// A code's invenSeq there is its 1-based place in this order; the dye routes number garments the same way.
func ClosetOrder(owned []string) []string {
	order := make([]string, 0, len(owned))
	for _, code := range owned {
		if !IsBasicFaceItemCode(code) {
			order = append(order, code)
		}
	}
	for _, code := range owned {
		if IsBasicFaceItemCode(code) {
			order = append(order, code)
		}
	}
	return order
}

// DyeColor is one tone layer of a dyed garment: its colour as "#RRGGBB" and its transparency in percent (0 opaque).
type DyeColor struct {
	ColorCode    string `json:"colorCode"`
	Transparency int    `json:"transparency"`
}

// DyeTypeTwoLayers is the dyeType of a dyed garment: two tone layers, colorAndTransparencies[0] and [1].
const DyeTypeTwoLayers = 2

// DyeLayers returns the two tone layers of a garment: its stored colours, or white and opaque when it is undyed.
func DyeLayers(colors []DyeColor) []DyeColor {
	out := []DyeColor{{ColorCode: "#FFFFFF"}, {ColorCode: "#FFFFFF"}}
	copy(out, colors)
	return out
}

func IsBasicFaceItemCode(code string) bool {
	if len(code) < 4 {
		return false
	}
	switch code[2:4] {
	case "EY", "MO", "EB", "NO", "HE":
		return true
	default:
		return false
	}
}

func ItemSerials(codes []string) map[string]string {
	serials := make(map[string]string, len(codes))
	for i, code := range codes {
		if _, exists := serials[code]; !exists {
			serials[code] = strconv.Itoa(i + 1)
		}
	}
	return serials
}

func avatarItemsFromInventory(codes, inventory []string, dyes map[string][]DyeColor) []AvatarItem {
	wearableCodes := make([]string, 0, len(inventory))
	for _, code := range inventory {
		if !IsBasicFaceItemCode(code) {
			wearableCodes = append(wearableCodes, code)
		}
	}
	serials := ItemSerials(wearableCodes)
	items := make([]AvatarItem, 0, len(codes))
	for _, code := range codes {
		serial := serials[code]
		if serial == "" {
			serial = "0"
		}
		item := AvatarItem{CD: code, InvenSeq: serial, ColorAndTransparencies: []any{}}
		if colors, ok := dyes[code]; ok {
			layers := DyeLayers(colors)
			item.DyeType = DyeTypeTwoLayers
			item.ColorAndTransparencies = []any{layers[0], layers[1]}
		}
		items = append(items, item)
	}
	return items
}

// AvatarItem is the object form parsed by sDataAvatar::SetData @0x1c0a39c.
type AvatarItem struct {
	CD                     string `json:"cd"`
	InvenSeq               string `json:"invenSeq"`
	DyeType                int    `json:"dyeType"`
	ColorAndTransparencies []any  `json:"colorAndTransparencies"`
}

func AvatarItemsFromCodes(codes []string) []AvatarItem {
	items := make([]AvatarItem, 0, len(codes))
	for _, cd := range codes {
		items = append(items, AvatarItem{CD: cd, InvenSeq: "0", ColorAndTransparencies: []any{}})
	}
	return items
}

type AvatarResult struct {
	AvatarID    string       `json:"avatarId"`
	Name        string       `json:"name"`
	Gender      string       `json:"gender"`
	SessionKey  string       `json:"sessionKey"`
	AvatarCode  string       `json:"avatarCode"`
	Items       []AvatarItem `json:"items"`
	PetProfiles []string     `json:"petProfiles"`
}

type AvatarInfoResult struct {
	AvatarID    string       `json:"avatarId"`
	Name        string       `json:"name"`
	Gender      string       `json:"gender"`
	SType       string       `json:"sType"`
	Skin        string       `json:"skin"`
	Country     string       `json:"country"`
	Items       []AvatarItem `json:"items"`
	PetProfiles []string     `json:"petProfiles"`
}
