package room

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// Starter is data/starter.json: what a new account's room starts with, and the default pet name.
//
//	starter_codes  item codes granted to a new account's empty room, in grant order. It must include the
//	               default floor, wall, door and diary (RUTI000EW, RUWA000HR, RUDO0002Z, RUAB00002): the
//	               layout places them, and the client refuses to save a floor 1 without a door and a diary.
//	pet_name       the default name of a pet; {n} is the pet's number in its owner's list
type Starter struct {
	StarterCodes []string `json:"starter_codes"`
	PetName      string   `json:"pet_name"`
}

var starterFile = opdata.New("starter.json", data.StarterJSON, checkStarter, nil)

// LoadOperatorData loads the room's operator data file. cmd/cherry calls it once at startup.
func LoadOperatorData() error { return starterFile.Load() }

// ActiveStarter is the starter file in use.
func ActiveStarter() *Starter { return starterFile.Get() }

// PetName is the default name of pet n (1-based) of an owner.
func PetName(n int) string {
	return strings.ReplaceAll(ActiveStarter().PetName, "{n}", strconv.Itoa(n))
}

func checkStarter(s *Starter) error {
	seen := map[string]bool{}
	for _, code := range s.StarterCodes {
		if !store.ValidItemCode(code) || seen[code] {
			return fmt.Errorf("starter_codes: %q must be an item code, listed once", code)
		}
		seen[code] = true
	}
	for _, need := range []string{RoomFloorCode, RoomWallCode, roomDoorCode, roomDiaryCode} {
		if !seen[need] {
			return fmt.Errorf("starter_codes must include %s (the default floor, wall, door and diary)", need)
		}
	}
	if !strings.Contains(s.PetName, "{n}") {
		return errors.New("pet_name must contain {n}, the pet number")
	}
	return nil
}
