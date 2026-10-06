package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type storedAccount struct {
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
	RoomItems      []roomItem            `json:"roomItems,omitempty"`
	NextRoomSeq    int64                 `json:"nextRoomSeq,omitempty"`
	Rooms          map[string]roomLayout `json:"rooms,omitempty"`
	Presets        map[string]roomPreset `json:"roomPresets,omitempty"`
	Pets           []petItem             `json:"pets,omitempty"`
	Gems           int64                 `json:"gems,omitempty"`
	Cash           int64                 `json:"cash,omitempty"`
	FaceTickets    int64                 `json:"faceTickets,omitempty"`
	Welcomed       bool                  `json:"welcomed,omitempty"`
	AttendDay      int                   `json:"attendDay,omitempty"`
	AttendKey      int64                 `json:"attendKey,omitempty"`
	Mail           []mailRow             `json:"mail,omitempty"`
	NextMailSeq    int64                 `json:"nextMailSeq,omitempty"`
}

type savedAccounts struct {
	Version      int                      `json:"version"`
	Accounts     map[string]storedAccount `json:"accounts"`
	Aliases      map[string]string        `json:"aliases"`
	Latest       string                   `json:"latest"`
	NextAvatarID uint64                   `json:"nextAvatarId"`
}

func loadAccounts() error {
	dir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	return loadAccountsFrom(filepath.Join(dir, "Cherry", "accounts.json"))
}

func loadAccountsFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	aliases := make(map[string]*account)
	var latest *account
	var nextID uint64
	if err == nil {
		var state savedAccounts
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("invalid account store: %w", err)
		}
		if state.Version != 1 || state.Accounts == nil || state.Aliases == nil {
			return fmt.Errorf("invalid account store version or tables")
		}
		byID := make(map[string]*account, len(state.Accounts))
		for id, saved := range state.Accounts {
			aid, parseErr := strconv.ParseUint(saved.Aid, 10, 64)
			if id == "" || saved.AccessToken != id || saved.SessionKey == "" || parseErr != nil || aid > state.NextAvatarID {
				return fmt.Errorf("invalid account record")
			}
			inventoryCodes := saved.InventoryCodes
			if inventoryCodes == nil {
				inventoryCodes = saved.ItemCodes
			}
			inventoryCopy := appendUniqueItemCodes(nil, inventoryCodes)
			byID[id] = &account{
				accessToken: saved.AccessToken, sessionKey: saved.SessionKey, mid: saved.Mid,
				avatarUserID: saved.AvatarUserID, aid: saved.Aid, name: saved.Name,
				gender: saved.Gender, skin: saved.Skin, country: saved.Country,
				itemCodes: saved.ItemCodes, inventoryCodes: inventoryCopy,
				roomItems: saved.RoomItems, nextRoomSeq: roomNextSeq(saved.RoomItems, saved.NextRoomSeq), rooms: saved.Rooms, presets: saved.Presets, pets: saved.Pets,
				gems: max(saved.Gems, 0), cash: max(saved.Cash, 0), faceTickets: max(saved.FaceTickets, 0), welcomed: saved.Welcomed,
				attendDay: saved.AttendDay, attendKey: saved.AttendKey, mail: saved.Mail, nextMailSeq: saved.NextMailSeq,
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
	accountsMu.Lock()
	accounts, latestAcc, nextAvatarID, accountStorePath = aliases, latest, nextID, path
	accountsMu.Unlock()
	return nil
}

// Caller holds accountsMu; save the account and its token aliases together.
func saveAccountsLocked() error {
	if accountStorePath == "" {
		return nil // In-memory httptest sessions do not write user-profile files.
	}
	state := savedAccounts{Version: 1, Accounts: make(map[string]storedAccount),
		Aliases: make(map[string]string, len(accounts)), NextAvatarID: nextAvatarID}
	if latestAcc != nil {
		state.Latest = latestAcc.accessToken
	}
	for token, acc := range accounts {
		state.Aliases[token] = acc.accessToken
		state.Accounts[acc.accessToken] = storedFrom(acc)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(accountStorePath)
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
	return os.Rename(tmp.Name(), accountStorePath)
}

// storedFrom is the persisted form of an account (also the deleted-accounts backup form).
func storedFrom(acc *account) storedAccount {
	return storedAccount{
		AccessToken: acc.accessToken, SessionKey: acc.sessionKey, Mid: acc.mid,
		AvatarUserID: acc.avatarUserID, Aid: acc.aid, Name: acc.name, Gender: acc.gender,
		Skin: acc.skin, Country: acc.country, ItemCodes: acc.itemCodes,
		InventoryCodes: acc.inventoryCodes,
		RoomItems:      acc.roomItems, NextRoomSeq: acc.nextRoomSeq, Rooms: acc.rooms, Presets: acc.presets, Pets: acc.pets,
		Gems: acc.gems, Cash: acc.cash, FaceTickets: acc.faceTickets, Welcomed: acc.welcomed,
		AttendDay: acc.attendDay, AttendKey: acc.attendKey, Mail: acc.mail, NextMailSeq: acc.nextMailSeq,
	}
}
