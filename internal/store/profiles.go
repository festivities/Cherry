package store

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"cherry/data"
	"cherry/internal/httpx"
	"cherry/internal/opdata"
)

// Profiles is data/profiles.json: the synthetic friend, the four clone avatars the create flow asks for,
// and the lab grant list. The ids (FriendAID, the clone avatar ids) stay in code; the names and looks
// are Cherry's stand-ins (AGENTS "Operator-editable fabricated content").
//
//	friend_name      the display name of the synthetic friend (FriendAID)
//	clone_name       the display name of every clone avatar
//	clone_avatars    {avatar id: "MALE" | "FEMALE" | "ANIMAL"}: the sex of each clone avatar
//	lab_grant_codes  clothing item codes every lab account owns on top of its inventory (LabAids)
type Profiles struct {
	FriendName    string            `json:"friend_name"`
	CloneName     string            `json:"clone_name"`
	CloneAvatars  map[string]string `json:"clone_avatars"`
	LabGrantCodes []string          `json:"lab_grant_codes"`
}

var profilesFile = opdata.New("profiles.json", data.ProfilesJSON, checkProfiles, nil)

// LoadOperatorData loads the store's operator data file. cmd/cherry calls it once at startup.
func LoadOperatorData() error { return profilesFile.Load() }

// ActiveProfiles is the profiles file in use.
func ActiveProfiles() *Profiles { return profilesFile.Get() }

// FriendName is the synthetic friend's display name.
func FriendName() string { return ActiveProfiles().FriendName }

// CuratedGrantCodes are the clothing codes lab accounts own in addition to their inventory.
func CuratedGrantCodes() []string { return ActiveProfiles().LabGrantCodes }

func checkProfiles(p *Profiles) error {
	if strings.TrimSpace(p.FriendName) == "" {
		return errors.New("friend_name must not be empty")
	}
	if p.CloneName == "" {
		return errors.New("clone_name must not be empty")
	}
	for _, id := range slices.Sorted(maps.Keys(p.CloneAvatars)) {
		if !httpx.IsAllDigits(id) {
			return fmt.Errorf("clone avatar %q: id must be digits", id)
		}
		switch p.CloneAvatars[id] {
		case "MALE", "FEMALE", "ANIMAL":
		default:
			return fmt.Errorf("clone avatar %s: type must be MALE, FEMALE or ANIMAL", id)
		}
	}
	seen := map[string]bool{}
	for _, code := range p.LabGrantCodes {
		if !ValidItemCode(code) || seen[code] {
			return fmt.Errorf("lab_grant_codes: %q must be an item code, listed once", code)
		}
		seen[code] = true
	}
	return nil
}

// ValidItemCode reports whether s has the shape of an item code (9 characters: capital letters and
// digits). It does not check that the item exists; callers that can (economy, via the price table) do.
func ValidItemCode(s string) bool {
	if len(s) != 9 {
		return false
	}
	for i := range len(s) {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}
