package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cherry/internal/config"
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
	BirthdayClaims map[int]int64 // birthday reward claims by avatar turn (economy/birthday.go); replaced, never edited in place
	Remodel        *RoomRemodel  // unlocked floors and per-floor tile sizes (economy/remodel.go); replaced, never edited in place
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
	if !IsLab(acc.Aid) { // economy: new players own only what they create, earn or buy
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
				Remodel:        acc.Remodel,
			}, true
		}
	}
	return AccountSnapshot{}, false
}

type Account struct {
	AccessToken  string
	SessionKey   string // the key the client presents: derived (SessionSalt, SessionEpoch) or clear; memory only when derived
	SessionSalt  string // derived keys: random per account (sessionkey.go)
	SessionEpoch int64  // derived keys: bumped by a ban and Delete Avatar
	// LegacyKeyHash is the SHA-256 of a migrated account's old clear key, until the account presents its derived key.
	LegacyKeyHash   string
	Mid             string
	AvatarUserID    string
	Aid             string
	Name            string
	Gender          string
	Skin            string
	Country         string
	ItemCodes       []string
	InventoryCodes  []string
	RoomItems       []RoomItem
	NextRoomSeq     int64
	Rooms           map[string]RoomLayout
	Presets         map[string]RoomPreset
	Pets            []PetItem
	MiniPets        []MiniPetState // Minipet friendship and visits for pets paired with a room item (economy/minipet.go); replaced, never edited in place
	Gems            int64          // economy ledger balances, never negative (economy.go)
	Cash            int64
	FaceTickets     int64
	Welcomed        bool      // 300-Gem welcome gift already credited
	AttendDay       int       // last attended day in the 7-day login cycle (0 = none yet), loginbonus.go
	AttendKey       int64     // day key (06:00 JST boundary) of the last attend
	Mail            []MailRow // unclaimed mailbox rows, mailbox.go; replaced, never edited in place
	NextMailSeq     int64
	ProfileImage    ProfileImage          // saved profile picture (OBS paths + background), profileimage.go
	Quests          *QuestDay             // Garden daily quest progress, economy/quests.go; replaced, never edited in place
	Fishing         *FishingDay           // Square fishing Gems for one game day, economy/fishing.go; replaced, never edited in place
	FishCaught      map[int64]int64       // Square catches per fish id (the fishing book); replaced, never edited in place
	FishBookClaimed []int64               // Square fishing book page numbers (1-based) claimed; replaced, never edited in place
	Gear            *FishingGear          // Square fish shop: bought gear and what is in use, economy/fishing_shop.go; replaced, never edited in place
	BossCoins       int64                 // Square boss Boss Coins (economy/fishboss.go); not in the ledger, an exchange will spend it
	BossTries       *BossDay              // Square boss tries used on one game day, economy/fishboss.go; replaced, never edited in place
	BossCaught      map[int64]int64       // Square boss catches per boss id (economy/fishboss.go); replaced, never edited in place
	BossBook        map[int64]bool        // Square boss book reward claimed per boss id (economy/fishboss.go); replaced, never edited in place
	Hearts          *Hearts               // heart quest state, economy/hearts.go; replaced, never edited in place
	PaidPrices      map[string]int64      // clothing codes bought at a daily deal: Gems paid; negative = a player's gift, sells for nothing (economy/deals.go); replaced, never edited in place
	Smiles          int64                 // earned Smiles (Garden gathering, economy/gathering.go); ledger-backed like Gems
	GardenBag       []GardenBagStack      // Garden materials in the Bag (economy/gathering.go); replaced, never edited in place
	Gather          *GatherDay            // Garden gathering caps and Gem bonus for one game day (economy/gathering.go); replaced, never edited in place
	Atelier         *AtelierState         // Garden Atelier stamina and blueprints (economy/atelier.go); replaced, never edited in place
	Racing          *RacingRecord         // Riding Pet Race tickets, exp, MMR and store points (economy/racing.go); replaced, never edited in place
	GardenMap       string                // Garden map this account's Garden shows (economy/gardens.go); "" = the default
	Landscapes      []string              // bought Garden landscape item codes (economy/landscape.go); replaced, never edited in place
	Pins            []string              // avatar ids on the requester's My Pins, in pin order (web/pins.go); replaced, never edited in place
	DiaryProfile    *DiaryProfile         // Diary profile edit fields (diaryprofile.go, web/profileedit.go); replaced, never edited in place
	Wishes          []string              // item codes on the requester's wish list, at most four (web/wishlist.go); replaced, never edited in place
	Butlers         *ButlerBook           // Garden butlers: contracts, chief, gathering (economy/butler.go); replaced, never edited in place
	Dyes            map[string]int        // dye items owned by item number (economy/dye.go); replaced, never edited in place
	DyeColors       map[string][]DyeColor // colours of dyed garments by item code (economy/dye.go); replaced, never edited in place
	LuckySpinDay    int64                 // game day (economy.AttendDayKey) of the last free Lucky Spin (economy/spin.go); 0 = none
	PopupDay        int64                 // game day (economy.AttendDayKey) whose operator login popups were served (economy/popups.go); 0 = none
	AvatarCreated   int64                 // create/avatar time in Unix ms: the profile's "Avatar Birthday" (result.created); 0 = unknown
	BirthdayClaims  map[int]int64         // avatar birthday reward claims: turn -> claim time in Unix ms (economy/birthday.go); replaced, never edited in place
	GuestCode       string                // device uniqueCode bound by the guest login (GuestLogin); "" = none
	Identity        string                // provider identity ("discord:<id>" or "dev-<name>"), discord and dev-fake modes (identity.go); "" = none
	CodeHashes      []string              // SHA-256 hex of the uniqueCodes minted for Identity, oldest first, at most maxCodeHashes (identity.go)
	GuestCodeHash   string                // SHA-256 hex of the guest uniqueCode (bindGuest); the lookups use it, GuestCode is "" after a reload
	Ban             *Ban                  // staff ban (staff.go); nil = not banned. A banned account resolves to no token and mints none
	StyleSlots      []StyleSlot           // Closet styling slots (economy/styling.go); replaced, never edited in place
	StyleBought     int64                 // extra styling slots bought (economy/styling.go)
	StyleNextID     int64                 // last styling slot id handed out; ids are never reused
	Remodel         *RoomRemodel          // unlocked floors and per-floor tile sizes (economy/remodel.go); replaced, never edited in place
}

// Room remodeling defaults: 1F and 2F are free at 12x12 (legacy default); 3F and larger sizes unlock via Remodeling Shop.
const (
	DefaultRoomFloors   = 2
	MaxRoomFloors       = 3
	DefaultRoomTileSize = 12
)

// RoomRemodel holds a player's unlocked floor count (2 default, 3 once 3F unlocked), active per-floor tileSize, and
// owned remodeling item codes.
type RoomRemodel struct {
	MaxFloor   int            `json:"maxFloor,omitempty"`
	FloorSizes map[string]int `json:"floorSizes,omitempty"`
	Owned      []string       `json:"owned,omitempty"`
}

// RoomMaxFloor returns the highest floor (2 or 3) the remodel state unlocks.
func (r *RoomRemodel) RoomMaxFloor() int {
	if r == nil || r.MaxFloor < DefaultRoomFloors {
		return DefaultRoomFloors
	}
	if r.MaxFloor > MaxRoomFloors {
		return MaxRoomFloors
	}
	return r.MaxFloor
}

// FloorTileSize returns the active tileSize for level ("LEVEL_1".."LEVEL_3"), defaulting to DefaultRoomTileSize (12).
func (r *RoomRemodel) FloorTileSize(level string) int {
	if r != nil && r.FloorSizes != nil {
		if sz := r.FloorSizes[level]; sz >= DefaultRoomTileSize {
			return sz
		}
	}
	return DefaultRoomTileSize
}

// Owns reports whether code is in Owned.
func (r *RoomRemodel) Owns(code string) bool {
	if r == nil {
		return false
	}
	for _, c := range r.Owned {
		if c == code {
			return true
		}
	}
	return false
}

// RoomMaxFloor is the account's highest unlocked floor (2 default, 3 once 3F is unlocked).
func (a *Account) RoomMaxFloor() int {
	if a == nil {
		return DefaultRoomFloors
	}
	return a.Remodel.RoomMaxFloor()
}

// FloorTileSize is the account's active tileSize for level.
func (a *Account) FloorTileSize(level string) int {
	if a == nil {
		return DefaultRoomTileSize
	}
	return a.Remodel.FloorTileSize(level)
}

// FishingGear is the Square fishing gear an account bought (economy/fishing_shop.go): the item IDs
// it owns, remaining consumable bait uses per item ID, and the rod and bait in use (0 = none).
type FishingGear struct {
	Owned    []int64         `json:"owned,omitempty"`
	Rod      int64           `json:"rod,omitempty"`
	Bait     int64           `json:"bait,omitempty"`
	BaitUses map[int64]int64 `json:"baitUses,omitempty"`
}

// DefaultProfileBg is the Cherry-chosen fallback background when an account has a saved profile picture
// with no stored background (several client cells gate background/picture rendering on non-empty bg).
const DefaultProfileBg = "profilebg/12.jpg"

// ProfileImage is the "Take profile picture" result: OBS paths as the client sent them
// ("/lineplay/pr/<userid>_<ctime>") and the "profilebg/<n>.jpg" background name.
type ProfileImage struct {
	Image     string `json:"image,omitempty"`
	WholeBody string `json:"wholeBody,omitempty"`
	Bg        string `json:"bg,omitempty"`
}

// Fields returns the wire values (image, wholeBody, bg) for p. When Image is empty, all three are "".
// Otherwise WholeBody defaults to Image and Bg defaults to DefaultProfileBg.
func (p ProfileImage) Fields() (img, wb, bg string) {
	if p.Image == "" {
		return "", "", ""
	}
	img = p.Image
	wb = p.WholeBody
	if wb == "" {
		wb = img
	}
	bg = p.Bg
	if bg == "" {
		bg = DefaultProfileBg
	}
	return img, wb, bg
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
	id     int64
	sum    string
	revoke bool // the next save deletes every token row of the account before writing its pending ones (ResetIdentityLocked)
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
		Mid:          "1",
		AvatarUserID: "3001",
		Aid:          "0",
	}
	initSessionKey(acc)
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
	h := CodeHash(token)
	if acc := accountsByHash[h]; acc != nil && acc.Ban == nil {
		tokenUsed[h] = time.Now() // written in a batch by FlushTokenUse (tokens.go), not per request
		return acc
	}
	return nil
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

// loginAliasLocked gives acc a new alias and makes it the latest account, as a login does. Caller holds AccountsMu. A banned
// account gets ErrBanned and no alias.
func loginAliasLocked(acc *Account) (string, error) {
	if acc.Ban != nil {
		return "", ErrBanned
	}
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
	if err := commitLocked(lines, nil, prevRow.id, callerSite(), nil, nil); err != nil {
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

// ResetIdentityLocked is Delete Avatar in identity mode (discord, dev-fake). acc keeps its Identity and its ban and loses every
// other field: aid becomes "0", the session key changes and the stored codes go (the next avatar needs a new Discord sign-in).
// The once-only grants (the welcome gift, BossBook, the butler free hire) go too: a recreated avatar is a new player, even on
// a Discord identity that played before (user, 2026-11-21). Every token of the account ends, its own and its
// aliases, in memory and in the database: the save deletes the account's token rows first (rowState.revoke) in the same
// transaction, then writes the fresh own token that acc gets here. The same identity signs in again through exchange and guest/generate, which mints an alias. The undo puts
// the account, its previous tokens and their rows back. On error acc is as it was. Caller holds AccountsMu.
func ResetIdentityLocked(acc *Account, lines []LedgerLine) (undo func() error, err error) {
	rs := accountRows[acc]
	if rs == nil {
		return nil, errors.New("account is not in the store")
	}
	prev, prevRS, prevPending := *acc, *rs, slices.Clone(pendingTokens)
	prevHashes := AccountTokenHashesLocked(acc)
	for _, h := range prevHashes {
		delete(accountsByHash, h)
	}
	pendingTokens = slices.DeleteFunc(pendingTokens, func(p pendingToken) bool { return p.acc == acc })
	own := RandomToken(32)
	*acc = Account{
		AccessToken: own, Mid: prev.Mid, AvatarUserID: prev.AvatarUserID, Aid: "0",
		SessionSalt: prev.SessionSalt, SessionEpoch: prev.SessionEpoch,
		Identity: prev.Identity, Ban: prev.Ban, // no CodeHashes: a new Discord sign-in; no once-only grants: a new player
	}
	rotateSessionKey(acc)
	addTokenLocked(acc, own, true)
	rs.revoke = true
	if err := SaveLocked(lines, acc); err != nil {
		*acc, *rs, pendingTokens = prev, prevRS, prevPending
		for _, h := range prevHashes {
			accountsByHash[h] = acc
		}
		dropTokenLocked(CodeHash(own))
		return nil, err
	}
	return func() error {
		dropTokenLocked(CodeHash(acc.AccessToken)) // the fresh own token goes with the reset
		*acc = prev
		for _, h := range prevHashes {
			// A reload leaves prev.AccessToken "" (clear tokens are not stored), so such a token comes back as an alias: the
			// database keeps at most one own row per account (tokens_own) and nothing reads the kind otherwise.
			accountsByHash[h] = acc
			pendingTokens = append(pendingTokens, pendingToken{acc: acc, hash: h, own: prev.AccessToken != "" && h == CodeHash(prev.AccessToken)})
		}
		rs.revoke = true // the reset's rows are deleted, then the previous tokens are written
		return SaveLocked([]LedgerLine{{Time: time.Now().UTC().Format(time.RFC3339), Aid: prev.Aid, Reason: "account reset rolled back"}}, acc)
	}, nil
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
	if config.IdentityMode() { // no newest-account fallback: a request without a token acts on no account
		return nil
	}
	if LatestAcc != nil && LatestAcc.Ban != nil { // a banned account is not the fallback either
		return nil
	}
	return LatestAcc
}

func SessionAccountLocked(r *http.Request) *Account {
	return AccountByTokenLocked(httpx.CookieValue(r, "AV_AUTH"))
}

func AccountForRequest(r *http.Request) (AccountSnapshot, bool) {
	return accountForToken(httpx.CookieValue(r, "AV_AUTH"))
}

// AccountForUpload is AccountForRequest for the picture uploads. The stock client sends AV_AUTH two ways: as the AV_AUTH header
// (My Room thumbnails, captured 2026-10-06/07) and as a Cookie (diary photos, profile pictures). The header wins when both are sent.
func AccountForUpload(r *http.Request) (AccountSnapshot, bool) {
	token := strings.Trim(r.Header.Get("AV_AUTH"), `"`)
	if token == "" {
		token = httpx.CookieValue(r, "AV_AUTH")
	}
	return accountForToken(token)
}

// accountForToken is the snapshot of the account an AV_AUTH value resolves to: an account with an avatar, never aid "0".
func accountForToken(token string) (AccountSnapshot, bool) {
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
		BirthdayClaims: acc.BirthdayClaims, ProfileImage: acc.ProfileImage, Remodel: acc.Remodel,
	}
	AccountsMu.Unlock()
	return snapshot, true
}

// AvatarBirthday is the creation time of the avatar aid in Unix ms, the profile's "Avatar Birthday" (result.created). An
// avatar made before AvatarCreated was kept gets, once, the time of its first ledger line (the welcome gift follows
// create/avatar), and that value is saved; 0 when neither is known (the client then shows Jan 1 1970).
func AvatarBirthday(aid string) int64 {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := AccountByAidLocked(aid)
	if acc == nil || acc.AvatarCreated != 0 || acc.Aid == "0" || database.Load() == nil {
		if acc == nil {
			return 0
		}
		return acc.AvatarCreated
	}
	var first sql.NullString
	if err := database.Load().View(func(q Querier) error {
		return q.QueryRow(`SELECT time FROM ledger WHERE aid = ? ORDER BY id LIMIT 1`, acc.Aid).Scan(&first)
	}); err != nil || !first.Valid {
		return 0
	}
	t, err := time.Parse(time.RFC3339, first.String)
	if err != nil {
		return 0
	}
	acc.AvatarCreated = t.UnixMilli()
	if err := SaveLocked(nil, acc); err != nil {
		acc.AvatarCreated = 0
	}
	return acc.AvatarCreated
}
