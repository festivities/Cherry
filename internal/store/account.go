package store

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"

	"cherry/internal/httpx"
)

var CuratedGrantCodes = []string{"CUHA0036Z", "CUON004TV", "CUSH00267", "CUAH004JH"}

// AccountSnapshot is a copy of the fields /v4/avatar/<id> serves, taken under
// AccountsMu so the handler never reads fields while an avatar mutation writes.
type AccountSnapshot struct {
	Aid            string
	Name           string
	Gender         string
	Skin           string
	Country        string
	ItemCodes      []string
	InventoryCodes []string
	ProfileImage   ProfileImage
}

func AccountInventoryCodes(acc *Account) []string {
	if acc.InventoryCodes != nil {
		return acc.InventoryCodes
	}
	return acc.ItemCodes
}

func AccountOwnedCodes(acc *Account) []string {
	if acc == nil {
		return nil
	}
	if !LabAids[acc.Aid] { // economy: new players own only what they create, earn or buy
		return AccountInventoryCodes(acc)
	}
	return AppendUniqueItemCodes(AccountInventoryCodes(acc), CuratedGrantCodes)
}

func AppendUniqueItemCodes(existing, additions []string) []string {
	codes := make([]string, 0, len(existing)+len(additions))
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, code := range append(append([]string(nil), existing...), additions...) {
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	return codes
}

func AccountByAvatarID(id string) (AccountSnapshot, bool) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	for _, acc := range Accounts {
		if acc.Aid == id {
			return AccountSnapshot{
				Aid:            acc.Aid,
				Name:           acc.Name,
				Gender:         acc.Gender,
				Skin:           acc.Skin,
				Country:        acc.Country,
				ItemCodes:      AppearanceItemCodes(acc.Gender, acc.ItemCodes),
				InventoryCodes: AccountOwnedCodes(acc),
				ProfileImage:   acc.ProfileImage,
			}, true
		}
	}
	return AccountSnapshot{}, false
}

type Account struct {
	AccessToken    string
	SessionKey     string
	Mid            string
	AvatarUserID   string
	Aid            string
	Name           string
	Gender         string
	Skin           string
	Country        string
	ItemCodes      []string
	InventoryCodes []string
	RoomItems      []RoomItem
	NextRoomSeq    int64
	Rooms          map[string]RoomLayout
	Presets        map[string]RoomPreset
	Pets           []PetItem
	Gems           int64 // economy ledger balances, never negative (economy.go)
	Cash           int64
	FaceTickets    int64
	Welcomed       bool      // 300-Gem welcome gift already credited
	AttendDay      int       // last attended day in the 7-day login cycle (0 = none yet), loginbonus.go
	AttendKey      int64     // day key (06:00 JST boundary) of the last attend
	Mail           []MailRow // unclaimed mailbox rows, mailbox.go; replaced, never edited in place
	NextMailSeq    int64
	ProfileImage   ProfileImage    // saved profile picture (OBS paths + background), profileimage.go
	Quests         *QuestDay       // Garden daily quest progress, economy/quests.go; replaced, never edited in place
	Fishing        *FishingDay     // Square fishing Gems for one game day, economy/fishing.go; replaced, never edited in place
	FishCaught     map[int64]int64 // Square catches per fish id (the fishing book); replaced, never edited in place
	Gear           *FishingGear    // Square fish shop: bought gear and what is in use, economy/fishing_shop.go; replaced, never edited in place
	Hearts         *Hearts         // heart quest state, economy/hearts.go; replaced, never edited in place
}

// FishingGear is the Square fishing gear an account bought (economy/fishing_shop.go): the item IDs
// it owns and the rod and bait in use (0 = none).
type FishingGear struct {
	Owned []int64 `json:"owned,omitempty"`
	Rod   int64   `json:"rod,omitempty"`
	Bait  int64   `json:"bait,omitempty"`
}

// ProfileImage is the "Take profile picture" result: OBS paths as the client sent them
// ("/lineplay/pr/<userid>_<ctime>") and the "profilebg/<n>.jpg" background name.
type ProfileImage struct {
	Image     string `json:"image,omitempty"`
	WholeBody string `json:"wholeBody,omitempty"`
	Bg        string `json:"bg,omitempty"`
}

// QuestDay is one game day (06:00 JST boundary) of daily-quest progress. Done and
// Claimed hold quest IDs. A record whose Day is not today counts as empty.
type QuestDay struct {
	Day     int64   `json:"day"`
	Done    []int64 `json:"done,omitempty"`
	Claimed []int64 `json:"claimed,omitempty"`
}

// FishingDay is the Square fishing Gems credited on one game day (06:00 JST boundary).
// A record whose Day is not today counts as zero.
type FishingDay struct {
	Day  int64 `json:"day"`
	Gems int64 `json:"gems,omitempty"`
}

// Hearts is one account's heart state (economy/hearts.go). Sent and From belong to game
// day Day and count as empty on another day; Received is the lifetime total.
type Hearts struct {
	Day      int64    `json:"day"`
	Sent     int64    `json:"sent,omitempty"`     // hearts this account sent on Day
	Received int64    `json:"received,omitempty"` // hearts received, all days
	From     []string `json:"from,omitempty"`     // sender aids that hearted this account on Day, bounded
}

const MinAvatarID = 10

var (
	AccountsMu       sync.Mutex
	Accounts         = make(map[string]*Account)
	LatestAcc        *Account
	NextAvatarID     uint64
	AccountStorePath string
)

func NewAccount() (*Account, error) {
	acc := &Account{
		AccessToken:  RandomToken(32),
		SessionKey:   RandomToken(32),
		Mid:          "1",
		AvatarUserID: "3001",
		Aid:          "0",
	}
	AccountsMu.Lock()
	Accounts[acc.AccessToken] = acc
	previous := LatestAcc
	LatestAcc = acc
	err := SaveAccountsLocked()
	if err != nil {
		delete(Accounts, acc.AccessToken)
		LatestAcc = previous
	}
	AccountsMu.Unlock()
	return acc, err
}

func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func CurrentAccount(r *http.Request) *Account {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	token := httpx.CookieValue(r, "accessToken")
	if token == "" {
		token = httpx.CookieValue(r, "cc") // Native createSession sends its guest access token as cc.
	}
	if token != "" {
		return Accounts[token]
	}
	return LatestAcc
}

func SessionAccountLocked(r *http.Request) *Account { return Accounts[httpx.CookieValue(r, "AV_AUTH")] }

func AccountForRequest(r *http.Request) (AccountSnapshot, bool) {
	token := httpx.CookieValue(r, "AV_AUTH")
	if token == "" {
		return AccountSnapshot{}, false
	}
	AccountsMu.Lock()
	acc := Accounts[token]
	if acc == nil || !ValidAvatarID(acc.Aid) || acc.Aid == "0" {
		AccountsMu.Unlock()
		return AccountSnapshot{}, false
	}
	snapshot := AccountSnapshot{
		Aid: acc.Aid, Name: acc.Name, Gender: acc.Gender, Skin: acc.Skin,
		Country: acc.Country, ItemCodes: append([]string(nil), acc.ItemCodes...),
		InventoryCodes: append([]string(nil), AccountInventoryCodes(acc)...),
	}
	AccountsMu.Unlock()
	return snapshot, true
}
