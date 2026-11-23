package economy

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"cherry/data"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
	"cherry/internal/room"
)

// PetCatalog is data/pets.json: the operator-editable Minipet catalog with display names, optional Shop prices
// and currencies, cuddles requirements, the room item each pet is paired with, Riding Pet racing stats and the
// Minipet loop's tuning (minipet.go).
type PetCatalog struct {
	opdata.Provenance
	Minipet MiniPetRules       `json:"minipet"`
	Pets    map[string]PetSpec `json:"pets"`
}

// MiniPetRules is the tuning of the Minipet loop (minipet.go): friendship per petting, the free call's cooldown, the
// visit length, the paid call's Gem price and the presents an adopted pet gives (Cherry-made numbers, owner-editable).
type MiniPetRules struct {
	GainedExp       int64    `json:"gained_exp"`
	FreeCallMinutes int      `json:"free_call_minutes"`
	VisitMinutes    int      `json:"visit_minutes"`
	PaidCallGems    int64    `json:"paid_call_gems"`
	Presents        []string `json:"presents"`
}

// PetSpec is one Minipet entry keyed by its PUPE item code. Item is the RUPI room item that places the pet
// (Minipet Shop item, the pairing is Cherry-made); a pet without one is not sold.
type PetSpec struct {
	Name     string         `json:"name"`
	Item     string         `json:"item,omitempty"`
	Price    int64          `json:"price,omitempty"`
	Currency string         `json:"currency,omitempty"`
	Cuddles  int            `json:"cuddles,omitempty"`
	Stats    *RacingPetStat `json:"stats,omitempty"`
}

var petsFile = opdata.New("pets.json", data.PetsJSON, checkPets, nil)

// ActivePets is the pet catalog in use.
func ActivePets() *PetCatalog { return petsFile.Get() }

func checkPets(c *PetCatalog) error {
	if err := c.CheckOrigin("mixed"); err != nil {
		return err
	}
	items := map[string]string{} // RUPI item code -> pet code, so a room item places one pet only
	for _, code := range slices.Sorted(maps.Keys(c.Pets)) {
		if len(code) != 9 || !strings.HasPrefix(code, "PUPE") || room.PetSkinID(code) <= 326400000 {
			return fmt.Errorf("pet %q: must be a 9-char PUPE code above 326400000", code)
		}
		spec := c.Pets[code]
		if spec.Name == "" {
			return fmt.Errorf("pet %s: name must not be empty", code)
		}
		if spec.Price < 0 {
			return fmt.Errorf("pet %s: price must not be negative", code)
		}
		if spec.Price > 0 && spec.Currency != "GEM" && spec.Currency != "CASH" {
			return fmt.Errorf("pet %s: currency %q must be GEM or CASH", code, spec.Currency)
		}
		if spec.Price == 0 && spec.Currency != "" {
			return fmt.Errorf("pet %s: currency %q must be empty when price is 0", code, spec.Currency)
		}
		if spec.Cuddles < 0 {
			return fmt.Errorf("pet %s: cuddles must not be negative", code)
		}
		if s := spec.Stats; s != nil {
			if min(s.Power, s.Speed, s.Stamina, s.ItemSlot) < 0 || max(s.Power, s.Speed, s.Stamina, s.ItemSlot) > 1000 {
				return fmt.Errorf("pet %s: stats must be 0 to 1000", code)
			}
		}
		if spec.Item != "" {
			if err := checkPetItem(spec.Item); err != nil {
				return fmt.Errorf("pet %s: %w", code, err)
			}
			if other, dup := items[spec.Item]; dup {
				return fmt.Errorf("pet %s: item %s already places pet %s", code, spec.Item, other)
			}
			items[spec.Item] = code
		}
	}
	return checkMiniPetRules(c.Minipet)
}

// checkPetItem: a Minipet room item is a RUPI code that decodes to a catIdx-29 (PI) interior item.
func checkPetItem(item string) error {
	id, ok := itemcode.ID(item)
	if !ok || !strings.HasPrefix(item, "RUPI") || (id%10000000)/100000 != 29 {
		return fmt.Errorf("item %q must be a RUPI room item (catIdx 29)", item)
	}
	return nil
}

// checkMiniPetRules: the Minipet loop's numbers must be usable; presents are clothing codes (CU or CA, 9 characters).
func checkMiniPetRules(r MiniPetRules) error {
	if r.GainedExp < 1 || r.VisitMinutes < 1 || r.FreeCallMinutes < 0 || r.PaidCallGems < 0 {
		return fmt.Errorf("minipet: gained_exp and visit_minutes must be at least 1, free_call_minutes and paid_call_gems not negative")
	}
	if len(r.Presents) == 0 {
		return fmt.Errorf("minipet: presents must not be empty")
	}
	seen := map[string]bool{}
	for _, code := range r.Presents {
		if len(code) != 9 || (code[:2] != "CU" && code[:2] != "CA") || seen[code] {
			return fmt.Errorf("minipet: present %q must be a unique 9-character clothing code", code)
		}
		seen[code] = true
	}
	return nil
}

// PetSpecOf returns the catalog entry for code, if any.
func PetSpecOf(code string) (PetSpec, bool) {
	spec, ok := ActivePets().Pets[code]
	return spec, ok
}

// PetDisplayName returns the catalog display name for code, or "" if unlisted.
func PetDisplayName(code string) string {
	if spec, ok := PetSpecOf(code); ok {
		return spec.Name
	}
	return ""
}

// PetRaceStat returns the per-pet racing stats for code when configured, else the racing.json default.
func PetRaceStat(code string) RacingPetStat {
	if spec, ok := PetSpecOf(code); ok && spec.Stats != nil {
		return *spec.Stats
	}
	return ActiveRacing().PetStat
}
