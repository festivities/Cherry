package store

import (
	"encoding/json"
	"strconv"
)

// InitAvatarBaseItem @0x29a7f84 (called from AvActorManager::Initialize).
// Index 0=MALE, 1=FEMALE. Numeric ids → CU codes via squareNumericItem inverse.
var AvatarBaseItems = map[string][]string{
	"MALE":   {"CUTO0011X", "CUPA000LO", "CUSH0009Q", "CUHA00001"},
	"FEMALE": {"CUTO0011X", "CUPA000LO", "CUSH0009Q", "CUHA00004"},
}

func ItemSlot(code string) string {
	if len(code) < 4 {
		return ""
	}
	return code[2:4]
}

func IsAvatarBaseItem(code string) bool {
	for _, list := range AvatarBaseItems {
		for _, b := range list {
			if b == code {
				return true
			}
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
		Country: acc.Country, Items: avatarItemsFromInventory(AppearanceItemCodes(gender, acc.ItemCodes), acc.InventoryCodes),
		PetProfiles: []string{},
	}
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

func avatarItemsFromInventory(codes, inventory []string) []AvatarItem {
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
		items = append(items, AvatarItem{CD: code, InvenSeq: serial, ColorAndTransparencies: []any{}})
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
