package store

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"cherry/internal/httpx"
)

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
	DiaryProfile   *DiaryProfile
	DyeColors      map[string][]DyeColor
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
	return AppendUniqueItemCodes(AccountInventoryCodes(acc), CuratedGrantCodes())
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
	for _, acc := range accountList {
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
				DiaryProfile:   acc.DiaryProfile,
				DyeColors:      acc.DyeColors,
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
	ProfileImage   ProfileImage          // saved profile picture (OBS paths + background), profileimage.go
	Quests         *QuestDay             // Garden daily quest progress, economy/quests.go; replaced, never edited in place
	Fishing        *FishingDay           // Square fishing Gems for one game day, economy/fishing.go; replaced, never edited in place
	FishCaught     map[int64]int64       // Square catches per fish id (the fishing book); replaced, never edited in place
	Gear           *FishingGear          // Square fish shop: bought gear and what is in use, economy/fishing_shop.go; replaced, never edited in place
	BossCoins      int64                 // Square boss Boss Coins (economy/fishboss.go); not in the ledger, an exchange will spend it
	BossTries      *BossDay              // Square boss tries used on one game day, economy/fishboss.go; replaced, never edited in place
	BossCaught     map[int64]int64       // Square boss catches per boss id (economy/fishboss.go); replaced, never edited in place
	BossBook       map[int64]bool        // Square boss book reward claimed per boss id (economy/fishboss.go); replaced, never edited in place
	Hearts         *Hearts               // heart quest state, economy/hearts.go; replaced, never edited in place
	PaidPrices     map[string]int64      // clothing codes bought at a daily deal: Gems paid; negative = a player's gift, sells for nothing (economy/deals.go); replaced, never edited in place
	Smiles         int64                 // earned Smiles (Garden gathering, economy/gathering.go); ledger-backed like Gems
	GardenBag      []GardenBagStack      // Garden materials in the Bag (economy/gathering.go); replaced, never edited in place
	Gather         *GatherDay            // Garden gathering caps and Gem bonus for one game day (economy/gathering.go); replaced, never edited in place
	Atelier        *AtelierState         // Garden Atelier stamina and blueprints (economy/atelier.go); replaced, never edited in place
	Racing         *RacingRecord         // Riding Pet Race tickets, exp, MMR and store points (economy/racing.go); replaced, never edited in place
	GardenMap      string                // Garden map this account's Garden shows (economy/gardens.go); "" = the default
	Landscapes     []string              // bought Garden landscape item codes (economy/landscape.go); replaced, never edited in place
	Pins           []string              // avatar ids on the requester's My Pins, in pin order (web/pins.go); replaced, never edited in place
	DiaryProfile   *DiaryProfile         // Diary profile edit fields (diaryprofile.go, web/profileedit.go); replaced, never edited in place
	Wishes         []string              // item codes on the requester's wish list, at most four (web/wishlist.go); replaced, never edited in place
	Butlers        *ButlerBook           // Garden butlers: contracts, chief, gathering (economy/butler.go); replaced, never edited in place
	Dyes           map[string]int        // dye items owned by item number (economy/dye.go); replaced, never edited in place
	DyeColors      map[string][]DyeColor // colours of dyed garments by item code (economy/dye.go); replaced, never edited in place
	LuckySpinDay   int64                 // game day (economy.AttendDayKey) of the last free Lucky Spin (economy/spin.go); 0 = none
	GuestCode      string                // device uniqueCode bound by the guest login (GuestLogin); "" = none
	Identity       string                // provider identity ("discord:<id>" or "dev-<name>"), discord and dev-fake modes (identity.go); "" = none
	CodeHashes     []string              // SHA-256 hex of the uniqueCodes minted for Identity, oldest first, at most maxCodeHashes (identity.go)
	GuestCodeHash  string                // SHA-256 hex of the guest uniqueCode (bindGuest); the lookups use it, GuestCode is "" after a reload
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

// BossDay is the Square boss tries an account spent on one game day (06:00 JST boundary). A record
// whose Day is not today counts as none spent.
type BossDay struct {
	Day  int64 `json:"day"`
	Used int64 `json:"used,omitempty"`
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
	AccountsMu   sync.Mutex
	LatestAcc    *Account // the newest account: the fallback of a request with no token (CurrentAccount, the welcome gift)
	NextAvatarID uint64

	accountsByHash = map[string]*Account{} // CodeHash of the own access token and of every alias -> its account
	accountList    []*Account              // every account once, in creation order
	accountRows    = map[*Account]*rowState{}
	pendingTokens  []pendingToken     // token rows the database does not have yet
	database       atomic.Pointer[DB] // nil: memory only (the tests' default; see OpenStore)
	savedLatest    *Account           // the latest account and next avatar id as the database last had them
	savedNext      uint64
)

// rowState is what the database holds for one account: its row id (0 = no row yet) and the checksum of the row as last
// written or read. It sits beside the account, not in it, so a handler's `*acc = previous` cannot undo a row a save wrote.
type rowState struct {
	id  int64
	sum string
}

// pendingToken is an access token or alias that resolves to acc and whose row the database does not have yet.
type pendingToken struct {
	acc  *Account
	hash string
	own  bool
}

func NewAccount() (*Account, error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	return newAccountLocked("")
}

// newAccountLocked creates an account bound to guestCode ("" = none); caller holds AccountsMu.
func newAccountLocked(guestCode string) (*Account, error) {
	return insertAccountLocked(newRecord(guestCode))
}

// newRecord is a fresh account with no avatar yet (aid "0") and new credentials; it is not in the store.
func newRecord(guestCode string) *Account {
	acc := &Account{
		AccessToken:  RandomToken(32),
		SessionKey:   RandomToken(32),
		Mid:          "1",
		AvatarUserID: "3001",
		Aid:          "0",
	}
	if guestCode != "" {
		bindGuest(acc, guestCode)
	}
	return acc
}

// bindGuest sets the device uniqueCode of acc: the clear code and its hash, which is what the lookups use.
func bindGuest(acc *Account, code string) {
	acc.GuestCode = code
	acc.GuestCodeHash = CodeHash(code)
}

// insertAccountLocked adds a new account with its own access token and saves it as the latest account; a failed save
// leaves no account behind. Caller holds AccountsMu.
func insertAccountLocked(acc *Account) (*Account, error) {
	accountList = append(accountList, acc)
	accountRows[acc] = &rowState{}
	addTokenLocked(acc, acc.AccessToken, true)
	previous := LatestAcc
	LatestAcc = acc
	if err := SaveLocked(nil, acc); err != nil {
		LatestAcc = previous
		forgetAccountLocked(acc)
		return nil, err
	}
	return acc, nil
}

// forgetAccountLocked drops acc from memory: its list entry, row state, pending tokens and every token that resolves to
// it. It returns those token hashes.
func forgetAccountLocked(acc *Account) []string {
	accountList = slices.DeleteFunc(accountList, func(a *Account) bool { return a == acc })
	delete(accountRows, acc)
	pendingTokens = slices.DeleteFunc(pendingTokens, func(p pendingToken) bool { return p.acc == acc })
	var hashes []string
	for h, a := range accountsByHash {
		if a == acc {
			hashes = append(hashes, h)
			delete(accountsByHash, h)
		}
	}
	return hashes
}

// addTokenLocked makes token resolve to acc; its row is pending until a save names acc.
func addTokenLocked(acc *Account, token string, own bool) string {
	h := CodeHash(token)
	accountsByHash[h] = acc
	pendingTokens = append(pendingTokens, pendingToken{acc: acc, hash: h, own: own})
	return h
}

// AccountByTokenLocked returns the account that a clear access token or alias resolves to, or nil. Caller holds AccountsMu.
func AccountByTokenLocked(token string) *Account {
	if token == "" {
		return nil
	}
	return accountsByHash[CodeHash(token)]
}

// AllAccountsLocked returns every account once, in creation order (a copy). Caller holds AccountsMu.
func AllAccountsLocked() []*Account { return slices.Clone(accountList) }

// AccountTokenHashesLocked returns the hashes of the tokens that resolve to acc. Caller holds AccountsMu.
func AccountTokenHashesLocked(acc *Account) []string {
	var hashes []string
	for h, a := range accountsByHash {
		if a == acc {
			hashes = append(hashes, h)
		}
	}
	slices.Sort(hashes)
	return hashes
}

// maxGuestCode bounds a device uniqueCode; the device UUID is 36 characters.
const maxGuestCode = 64

// ValidGuestCode reports whether code has the shape of a device uniqueCode: 1 to 64 ASCII letters, digits or dashes.
func ValidGuestCode(code string) bool {
	if code == "" || len(code) > maxGuestCode {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

// GuestAccountLocked returns the account bound to a device uniqueCode, or nil. A deleted account is not in the store, so
// it is never returned. Caller holds AccountsMu.
// ponytail: linear scan; index by guest code hash if the account table grows.
func GuestAccountLocked(code string) *Account {
	if code == "" {
		return nil
	}
	h := CodeHash(code)
	for _, acc := range accountList {
		if acc.GuestCodeHash == h {
			return acc
		}
	}
	return nil
}

// BindGuestLocked binds a device uniqueCode to acc (the staff tool and tests). A failed save leaves acc as it was.
// Caller holds AccountsMu.
func BindGuestLocked(acc *Account, code string) error {
	prev := *acc
	bindGuest(acc, code)
	if err := SaveLocked(nil, acc); err != nil {
		*acc = prev
		return err
	}
	return nil
}

// GuestLogin serves POST /v4/account/guest/generate for a validated device uniqueCode. The device's account gets a new
// access token alias (the credential createSession reads as cc); a device without one gets a new account bound to its
// code. A failed save leaves no alias or account behind.
func GuestLogin(code string) (string, error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := GuestAccountLocked(code)
	if acc == nil {
		created, err := newAccountLocked(code)
		if err != nil {
			return "", err
		}
		return created.AccessToken, nil
	}
	return loginAliasLocked(acc)
}

// loginAliasLocked gives acc a new alias and makes it the latest account, as a login does. Caller holds AccountsMu.
func loginAliasLocked(acc *Account) (string, error) {
	previous := LatestAcc
	LatestAcc = acc
	token, err := MintAliasLocked(acc)
	if err != nil {
		LatestAcc = previous
		return "", err
	}
	return token, nil
}

// AddAliasLocked makes a new random access token resolve to acc, in memory only: the row is written by the save the
// caller makes next (SaveLocked naming acc). RemoveAliasLocked undoes it when that save fails.
func AddAliasLocked(acc *Account) string {
	token := RandomToken(256)
	addTokenLocked(acc, token, false)
	return token
}

// RemoveAliasLocked drops an alias that AddAliasLocked made and whose save failed.
func RemoveAliasLocked(token string) {
	dropTokenLocked(CodeHash(token))
}

// dropTokenLocked forgets a token that was never saved.
func dropTokenLocked(h string) {
	delete(accountsByHash, h)
	pendingTokens = slices.DeleteFunc(pendingTokens, func(p pendingToken) bool { return p.hash == h })
}

// MintAliasLocked gives acc a new alias and saves it. The alias is the credential createSession reads as cc, and
// checkSession rotates AV_AUTH with it.
func MintAliasLocked(acc *Account) (string, error) {
	token := AddAliasLocked(acc)
	if err := SaveLocked(nil, acc); err != nil {
		RemoveAliasLocked(token)
		return "", err
	}
	return token, nil
}

// DeleteAccountLocked removes acc from memory and from the database in ONE transaction with lines. LatestAcc moves to the
// highest remaining aid when it was acc. The returned undo puts acc back and writes it again (with a compensating ledger
// line); the caller runs it when a later step of the same operation fails. On error memory is as it was.
func DeleteAccountLocked(acc *Account, lines []LedgerLine) (undo func() error, err error) {
	rs := accountRows[acc]
	if rs == nil {
		return nil, errors.New("account is not in the store")
	}
	prevRow, prevList, prevPending, prevLatest := *rs, slices.Clone(accountList), slices.Clone(pendingTokens), LatestAcc
	pos := slices.Index(accountList, acc)
	hashes := forgetAccountLocked(acc)
	if LatestAcc == acc {
		LatestAcc = highestAidLocked()
	}
	if err := commitLocked(lines, nil, prevRow.id, callerSite()); err != nil {
		accountList, pendingTokens, LatestAcc = prevList, prevPending, prevLatest
		accountRows[acc] = rs
		for _, h := range hashes {
			accountsByHash[h] = acc
		}
		return nil, err
	}
	undo = func() error {
		accountList = slices.Insert(accountList, min(pos, len(accountList)), acc)
		accountRows[acc] = &rowState{}
		for _, h := range hashes {
			accountsByHash[h] = acc
			pendingTokens = append(pendingTokens, pendingToken{acc: acc, hash: h})
		}
		LatestAcc = prevLatest
		compensating := []LedgerLine{{Time: time.Now().UTC().Format(time.RFC3339), Aid: acc.Aid, Reason: "account delete rolled back"}}
		if err := SaveLocked(compensating, acc); err != nil {
			forgetAccountLocked(acc)
			LatestAcc = highestAidLocked()
			return err
		}
		return nil
	}
	return undo, nil
}

// highestAidLocked is the account with the highest aid (the first on a tie), or nil. Caller holds AccountsMu.
func highestAidLocked() *Account {
	var latest *Account
	var best uint64
	for _, a := range accountList {
		if n, _ := strconv.ParseUint(a.Aid, 10, 64); latest == nil || n > best {
			latest, best = a, n
		}
	}
	return latest
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
		return AccountByTokenLocked(token)
	}
	return LatestAcc
}

func SessionAccountLocked(r *http.Request) *Account {
	return AccountByTokenLocked(httpx.CookieValue(r, "AV_AUTH"))
}

func AccountForRequest(r *http.Request) (AccountSnapshot, bool) {
	token := httpx.CookieValue(r, "AV_AUTH")
	if token == "" {
		return AccountSnapshot{}, false
	}
	AccountsMu.Lock()
	acc := AccountByTokenLocked(token)
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
