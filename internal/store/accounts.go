// Package store is the lowest layer: the account table and its persistence
// (accounts.json, token aliases), the append-only currency ledger, the social
// state (social.json), the avatar/room/pet/mail record types and the shared
// locks. Every other internal package builds on it.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type StoredAccount struct {
	AccessToken    string                `json:"accessToken"`
	SessionKey     string                `json:"sessionKey"`
	Mid            string                `json:"mid"`
	AvatarUserID   string                `json:"avatarUserId"`
	Aid            string                `json:"aid"`
	Name           string                `json:"name"`
	Gender         string                `json:"gender"`
	Skin           string                `json:"skin"`
	Country        string                `json:"country"`
	ItemCodes      []string              `json:"itemCodes"`
	InventoryCodes []string              `json:"inventoryCodes"`
	RoomItems      []RoomItem            `json:"roomItems,omitempty"`
	NextRoomSeq    int64                 `json:"nextRoomSeq,omitempty"`
	Rooms          map[string]RoomLayout `json:"rooms,omitempty"`
	Presets        map[string]RoomPreset `json:"roomPresets,omitempty"`
	Pets           []PetItem             `json:"pets,omitempty"`
	Gems           int64                 `json:"gems,omitempty"`
	Cash           int64                 `json:"cash,omitempty"`
	FaceTickets    int64                 `json:"faceTickets,omitempty"`
	Welcomed       bool                  `json:"welcomed,omitempty"`
	AttendDay      int                   `json:"attendDay,omitempty"`
	AttendKey      int64                 `json:"attendKey,omitempty"`
	Mail           []MailRow             `json:"mail,omitempty"`
	NextMailSeq    int64                 `json:"nextMailSeq,omitempty"`
	ProfileImage   *ProfileImage         `json:"profileImage,omitempty"`
	Quests         *QuestDay             `json:"quests,omitempty"`
	Fishing        *FishingDay           `json:"fishing,omitempty"`
	FishCaught     map[int64]int64       `json:"fishCaught,omitempty"`
	Gear           *FishingGear          `json:"fishingGear,omitempty"`
	BossCoins      int64                 `json:"bossCoins,omitempty"`
	BossTries      *BossDay              `json:"bossTries,omitempty"`
	BossCaught     map[int64]int64       `json:"bossCaught,omitempty"`
	BossBook       map[int64]bool        `json:"bossBook,omitempty"`
	Hearts         *Hearts               `json:"hearts,omitempty"`
	PaidPrices     map[string]int64      `json:"paidPrices,omitempty"`
	Smiles         int64                 `json:"smiles,omitempty"`
	GardenBag      []GardenBagStack      `json:"gardenBag,omitempty"`
	Gather         *GatherDay            `json:"gathering,omitempty"`
	Atelier        *AtelierState         `json:"atelier,omitempty"`
	Racing         *RacingRecord         `json:"racing,omitempty"`
	GardenMap      string                `json:"gardenMap,omitempty"`
	Landscapes     []string              `json:"landscapes,omitempty"`
	Pins           []string              `json:"pins,omitempty"`
	DiaryProfile   *DiaryProfile         `json:"diaryProfile,omitempty"`
	Wishes         []string              `json:"wishes,omitempty"`
}

type savedAccounts struct {
	Version      int                      `json:"version"`
	Accounts     map[string]StoredAccount `json:"accounts"`
	Aliases      map[string]string        `json:"aliases"`
	Latest       string                   `json:"latest"`
	NextAvatarID uint64                   `json:"nextAvatarId"`
}

func LoadAccounts() error {
	dir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	return LoadAccountsFrom(filepath.Join(dir, "Cherry", "accounts.json"))
}

func LoadAccountsFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	aliases := make(map[string]*Account)
	var latest *Account
	var nextID uint64
	if err == nil {
		var state savedAccounts
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("invalid account store: %w", err)
		}
		if state.Version != 1 || state.Accounts == nil || state.Aliases == nil {
			return fmt.Errorf("invalid account store version or tables")
		}
		byID := make(map[string]*Account, len(state.Accounts))
		for id, saved := range state.Accounts {
			aid, parseErr := strconv.ParseUint(saved.Aid, 10, 64)
			if id == "" || saved.AccessToken != id || saved.SessionKey == "" || parseErr != nil || aid > state.NextAvatarID {
				return fmt.Errorf("invalid account record")
			}
			inventoryCodes := saved.InventoryCodes
			if inventoryCodes == nil {
				inventoryCodes = saved.ItemCodes
			}
			inventoryCopy := AppendUniqueItemCodes(nil, inventoryCodes)
			byID[id] = &Account{
				AccessToken: saved.AccessToken, SessionKey: saved.SessionKey, Mid: saved.Mid,
				AvatarUserID: saved.AvatarUserID, Aid: saved.Aid, Name: saved.Name,
				Gender: saved.Gender, Skin: saved.Skin, Country: saved.Country,
				ItemCodes: saved.ItemCodes, InventoryCodes: inventoryCopy,
				RoomItems: saved.RoomItems, NextRoomSeq: roomNextSeq(saved.RoomItems, saved.NextRoomSeq), Rooms: saved.Rooms, Presets: saved.Presets, Pets: saved.Pets,
				Gems: max(saved.Gems, 0), Cash: max(saved.Cash, 0), FaceTickets: max(saved.FaceTickets, 0), Welcomed: saved.Welcomed,
				AttendDay: saved.AttendDay, AttendKey: saved.AttendKey, Mail: saved.Mail, NextMailSeq: saved.NextMailSeq,
				ProfileImage: derefProfile(saved.ProfileImage), Quests: saved.Quests, Fishing: saved.Fishing,
				FishCaught: saved.FishCaught, Gear: saved.Gear, BossCoins: max(saved.BossCoins, 0), BossTries: saved.BossTries,
				BossCaught: saved.BossCaught, BossBook: saved.BossBook, Hearts: saved.Hearts, PaidPrices: saved.PaidPrices,
				Smiles: max(saved.Smiles, 0), GardenBag: saved.GardenBag, Gather: saved.Gather,
				Atelier: saved.Atelier,
				Racing:  saved.Racing, GardenMap: saved.GardenMap, Landscapes: saved.Landscapes,
				Pins: saved.Pins, DiaryProfile: saved.DiaryProfile, Wishes: saved.Wishes,
			}
		}
		for token, id := range state.Aliases {
			acc := byID[id]
			if token == "" || acc == nil {
				return fmt.Errorf("invalid account alias")
			}
			aliases[token] = acc
		}
		for id, acc := range byID {
			if aliases[id] != acc {
				return fmt.Errorf("missing guest account alias")
			}
		}
		if state.Latest != "" {
			latest = byID[state.Latest]
			if latest == nil {
				return fmt.Errorf("invalid latest account")
			}
		} else if len(byID) > 0 {
			return fmt.Errorf("missing latest account")
		}
		nextID = state.NextAvatarID
	}
	AccountsMu.Lock()
	Accounts, LatestAcc, NextAvatarID, AccountStorePath = aliases, latest, nextID, path
	AccountsMu.Unlock()
	return nil
}

// Caller holds accountsMu; save the account and its token aliases together.
func SaveAccountsLocked() error {
	if AccountStorePath == "" {
		return nil // In-memory httptest sessions do not write user-profile files.
	}
	state := savedAccounts{Version: 1, Accounts: make(map[string]StoredAccount),
		Aliases: make(map[string]string, len(Accounts)), NextAvatarID: NextAvatarID}
	if LatestAcc != nil {
		state.Latest = LatestAcc.AccessToken
	}
	for token, acc := range Accounts {
		state.Aliases[token] = acc.AccessToken
		state.Accounts[acc.AccessToken] = StoredFrom(acc)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(AccountStorePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".accounts-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), AccountStorePath)
}

// StoredFrom is the persisted form of an account (also the deleted-accounts backup form).
func StoredFrom(acc *Account) StoredAccount {
	var pi *ProfileImage
	if acc.ProfileImage != (ProfileImage{}) {
		copied := acc.ProfileImage
		pi = &copied
	}
	return StoredAccount{
		AccessToken: acc.AccessToken, SessionKey: acc.SessionKey, Mid: acc.Mid,
		AvatarUserID: acc.AvatarUserID, Aid: acc.Aid, Name: acc.Name, Gender: acc.Gender,
		Skin: acc.Skin, Country: acc.Country, ItemCodes: acc.ItemCodes,
		InventoryCodes: acc.InventoryCodes,
		RoomItems:      acc.RoomItems, NextRoomSeq: acc.NextRoomSeq, Rooms: acc.Rooms, Presets: acc.Presets, Pets: acc.Pets,
		Gems: acc.Gems, Cash: acc.Cash, FaceTickets: acc.FaceTickets, Welcomed: acc.Welcomed,
		AttendDay: acc.AttendDay, AttendKey: acc.AttendKey, Mail: acc.Mail, NextMailSeq: acc.NextMailSeq,
		ProfileImage: pi, Quests: acc.Quests, Fishing: acc.Fishing, FishCaught: acc.FishCaught, Gear: acc.Gear,
		BossCoins: acc.BossCoins, BossTries: acc.BossTries, BossCaught: acc.BossCaught, BossBook: acc.BossBook,
		Hearts: acc.Hearts, PaidPrices: acc.PaidPrices,
		Smiles: acc.Smiles, GardenBag: acc.GardenBag, Gather: acc.Gather, Atelier: acc.Atelier,
		Racing: acc.Racing, GardenMap: acc.GardenMap, Landscapes: acc.Landscapes, Pins: acc.Pins,
		DiaryProfile: acc.DiaryProfile, Wishes: acc.Wishes,
	}
}

func derefProfile(p *ProfileImage) ProfileImage {
	if p == nil {
		return ProfileImage{}
	}
	return *p
}
