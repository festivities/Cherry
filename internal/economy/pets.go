package economy

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/room"
)

// PetCatalog is data/pets.json: the operator-editable Minipet catalog with display names, optional Shop prices
// and currencies, cuddles requirements and Riding Pet racing stats.
type PetCatalog struct {
	opdata.Provenance
	Pets map[string]PetSpec `json:"pets"`
}

// PetSpec is one Minipet entry keyed by its PUPE item code.
type PetSpec struct {
	Name     string         `json:"name"`
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
