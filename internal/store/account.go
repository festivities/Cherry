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
