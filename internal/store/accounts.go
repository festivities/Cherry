package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
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
	Butlers        *ButlerBook           `json:"butlers,omitempty"`
	Dyes           map[string]int        `json:"dyes,omitempty"`
	DyeColors      map[string][]DyeColor `json:"dyeColors,omitempty"`
	LuckySpinDay   int64                 `json:"luckySpinDay,omitempty"`
	GuestCode      string                `json:"guestCode,omitempty"`
	Identity       string                `json:"identity,omitempty"`
	CodeHashes     []string              `json:"codeHashes,omitempty"`
}

// accountFile is the account part of a legacy accounts.json, parsed without touching the store. Only cmd/cherry-import
// reads it: the server reads cherry.db.
type accountFile struct {
	accounts []*Account
	byToken  map[string]*Account // clear access token or alias -> account
	latest   *Account
	nextID   uint64
}

// readAccountFile parses an accounts.json the way the server used to load it; a missing file is an empty store.
func readAccountFile(path string) (accountFile, error) {
	f := accountFile{byToken: map[string]*Account{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	var state struct {
		Version      int                      `json:"version"`
		Accounts     map[string]StoredAccount `json:"accounts"`
		Aliases      map[string]string        `json:"aliases"`
		Latest       string                   `json:"latest"`
		NextAvatarID uint64                   `json:"nextAvatarId"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return f, fmt.Errorf("invalid account store: %w", err)
	}
	if state.Version != 1 || state.Accounts == nil || state.Aliases == nil {
		return f, fmt.Errorf("invalid account store version or tables")
	}
	byID := make(map[string]*Account, len(state.Accounts))
	for id, saved := range state.Accounts {
		aid, parseErr := strconv.ParseUint(saved.Aid, 10, 64)
		if id == "" || saved.AccessToken != id || saved.SessionKey == "" || parseErr != nil || aid > state.NextAvatarID {
			return f, fmt.Errorf("invalid account record")
		}
		byID[id] = accountFromStored(saved)
	}
	for token, id := range state.Aliases {
		acc := byID[id]
		if token == "" || acc == nil {
			return f, fmt.Errorf("invalid account alias")
		}
		f.byToken[token] = acc
	}
	identities := make(map[string]bool, len(byID))
	for id, acc := range byID {
		if f.byToken[id] != acc {
			return f, fmt.Errorf("missing guest account alias")
		}
		if acc.Identity != "" && identities[acc.Identity] {
			return f, fmt.Errorf("duplicate identity") // one provider identity maps to one account
		}
		identities[acc.Identity] = true
		f.accounts = append(f.accounts, acc)
	}
	if state.Latest != "" {
		f.latest = byID[state.Latest]
		if f.latest == nil {
			return f, fmt.Errorf("invalid latest account")
		}
	} else if len(byID) > 0 {
		return f, fmt.Errorf("missing latest account")
	}
	f.nextID = state.NextAvatarID
	return f, nil
}

// OpenStore opens (creating and migrating) the database at path, loads its accounts into memory and makes it the record
// of every save. Production calls it once at start; without it the store runs memory-only (the tests' default).
func OpenStore(path string) error {
	d, err := OpenDB(path)
	if err != nil {
		return err
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	if err := reloadLocked(d); err != nil {
		d.Close()
		return err
	}
	database = d
	return nil
}

// CloseStore detaches the database and closes it; saves after it run memory-only.
func CloseStore() error {
	AccountsMu.Lock()
	d := database
	database = nil
	AccountsMu.Unlock()
	if d == nil {
		return nil
	}
	return d.Close()
}

// ReloadAccounts replaces the in-memory accounts with the attached database's rows (a restart, in tests).
func ReloadAccounts() error {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	if database == nil {
		return errors.New("no database attached")
	}
	return reloadLocked(database)
}

// reloadLocked makes d's accounts the in-memory state; every loaded row counts as saved.
func reloadLocked(d *DB) error {
	var st AccountState
	if err := d.View(func(q Querier) error {
		var err error
		st, err = ReadAccounts(q)
		return err
	}); err != nil {
		return err
	}
	rows := make(map[*Account]*rowState, len(st.Accounts))
	for _, acc := range st.Accounts {
		_, _, sum, err := accountRow(acc)
		if err != nil {
			return err
		}
		rows[acc] = &rowState{id: st.rowIDs[acc], sum: sum}
	}
	accountsByHash, accountList, accountRows = st.ByToken, st.Accounts, rows
	pendingTokens = nil
	LatestAcc, NextAvatarID = st.Latest, st.NextAvatarID
	savedLatest, savedNext = LatestAcc, NextAvatarID
	return nil
}

// AttachDB makes d the record of every save and writes the accounts in memory into it (d must hold no accounts). Tests
// use it to persist what they installed; production uses OpenStore.
func AttachDB(d *DB) error {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	database = d
	pendingTokens = nil
	for _, acc := range accountList {
		accountRows[acc] = &rowState{}
	}
	for h, acc := range accountsByHash {
		pendingTokens = append(pendingTokens, pendingToken{acc: acc, hash: h, own: acc.AccessToken != "" && h == CodeHash(acc.AccessToken)})
	}
	savedLatest, savedNext = nil, 0
	if err := commitLocked(nil, accountList, 0, callerSite()); err != nil {
		database = nil
		return err
	}
	return nil
}

// DetachDB stops the record without closing the database (the caller closes it).
func DetachDB() {
	AccountsMu.Lock()
	database = nil
	AccountsMu.Unlock()
}

// SaveLocked is the one save of the store. The caller holds AccountsMu and has changed the named accounts in memory. In
// ONE database transaction it writes their rows (an account with no row is inserted with its token rows), the token rows
// created since the last save for them, the ledger lines and the account scalars when those changed; the latest account
// is written too while it has no row. On error nothing was written, and the caller restores what it changed. Without an
// attached database the save succeeds and writes nothing. In test builds it also checks that no account that was not
// named differs from its last saved form (checkNotMissed).
func SaveLocked(lines []LedgerLine, accs ...*Account) error {
	return commitLocked(lines, accs, 0, callerSite())
}

// commitLocked writes the named accounts, their pending tokens, the lines and the scalars in one transaction, and deletes
// the row removeID when it is not 0 (DeleteAccountLocked).
func commitLocked(lines []LedgerLine, named []*Account, removeID int64, site string) error {
	var rows []*Account
	for _, acc := range named {
		if accountRows[acc] == nil {
			return errors.New("account is not in the store")
		}
		if !slices.Contains(rows, acc) {
			rows = append(rows, acc)
		}
	}
	if database != nil && LatestAcc != nil && accountRows[LatestAcc] != nil && accountRows[LatestAcc].id == 0 && !slices.Contains(rows, LatestAcc) {
		rows = append(rows, LatestAcc) // the scalars name the latest account by its row
	}
	sums := make([]string, len(rows))
	if database != nil {
		var inserted []*rowState
		err := database.Update(func(tx *sql.Tx) error {
			for i, acc := range rows {
				rs := accountRows[acc]
				var err error
				if rs.id == 0 {
					if rs.id, sums[i], err = insertAccountRowTx(tx, acc); err != nil {
						return err
					}
					inserted = append(inserted, rs)
				} else if _, _, sum, serr := accountRow(acc); serr == nil && sum == rs.sum {
					sums[i] = sum // the row is as the database has it (an alias-only save, e.g. checkSession): no rewrite
				} else if sums[i], err = updateAccountRowTx(tx, rs.id, acc); err != nil {
					return err
				}
				for _, p := range pendingTokens {
					if p.acc == acc {
						if err := addTokenRowTx(tx, rs.id, p); err != nil {
							return err
						}
					}
				}
			}
			if removeID != 0 {
				if err := deleteAccountRowTx(tx, removeID); err != nil {
					return err
				}
			}
			if err := AppendLedgerTx(tx, lines); err != nil {
				return err
			}
			if LatestAcc != savedLatest || NextAvatarID != savedNext {
				var latestID int64
				if LatestAcc != nil {
					latestID = accountRows[LatestAcc].id
				}
				return SetAccountMetaTx(tx, NextAvatarID, latestID)
			}
			return nil
		})
		if err != nil {
			for _, rs := range inserted {
				rs.id = 0
			}
			return err
		}
	} else {
		for i, acc := range rows {
			_, _, sums[i], _ = accountRow(acc)
		}
	}
	for i, acc := range rows {
		accountRows[acc].sum = sums[i]
	}
	pendingTokens = slices.DeleteFunc(pendingTokens, func(p pendingToken) bool { return slices.Contains(rows, p.acc) })
	savedLatest, savedNext = LatestAcc, NextAvatarID
	if testing.Testing() {
		checkNotMissed(rows, site)
	}
	return nil
}

// checkNotMissed is the test-build safety net: every account a save did not name must still be as its last save left it.
// The panic names the aid and the save site, never the content.
func checkNotMissed(named []*Account, site string) {
	for _, acc := range accountList {
		if slices.Contains(named, acc) {
			continue
		}
		_, _, sum, err := accountRow(acc)
		if err != nil || sum != accountRows[acc].sum {
			panic(fmt.Sprintf("store: account aid %s changed but was not saved (save at %s)", acc.Aid, site))
		}
	}
}

// callerSite is the file:line of the first caller outside package store (test builds; "" otherwise).
func callerSite() string {
	if !testing.Testing() {
		return ""
	}
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	for {
		f, more := frames.Next()
		if !strings.HasPrefix(f.Function, "cherry/internal/store.") || strings.HasSuffix(f.File, "_test.go") {
			return fmt.Sprintf("%s:%d", filepath.Base(f.File), f.Line)
		}
		if !more {
			return "unknown"
		}
	}
}

// StoredFrom is the persisted form of an account (also the deleted-accounts backup form). The clear access token and
// guest code are not part of it: the database keeps their hashes only.
func StoredFrom(acc *Account) StoredAccount {
	var pi *ProfileImage
	if acc.ProfileImage != (ProfileImage{}) {
		copied := acc.ProfileImage
		pi = &copied
	}
	return StoredAccount{
		SessionKey: acc.SessionKey, Mid: acc.Mid,
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
		DiaryProfile: acc.DiaryProfile, Wishes: acc.Wishes, Butlers: acc.Butlers,
		Dyes: acc.Dyes, DyeColors: acc.DyeColors, LuckySpinDay: acc.LuckySpinDay,
		Identity: acc.Identity, CodeHashes: acc.CodeHashes,
	}
}

// accountFromStored is the in-memory account of one persisted record (the database reads and the legacy parser).
func accountFromStored(saved StoredAccount) *Account {
	inventoryCodes := saved.InventoryCodes
	if inventoryCodes == nil {
		inventoryCodes = saved.ItemCodes
	}
	inventoryCopy := AppendUniqueItemCodes(nil, inventoryCodes)
	return &Account{
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
		Pins: saved.Pins, DiaryProfile: saved.DiaryProfile, Wishes: saved.Wishes, Butlers: saved.Butlers,
		Dyes: saved.Dyes, DyeColors: saved.DyeColors, LuckySpinDay: saved.LuckySpinDay,
		GuestCode: saved.GuestCode, Identity: saved.Identity, CodeHashes: saved.CodeHashes,
	}
}

func derefProfile(p *ProfileImage) ProfileImage {
	if p == nil {
		return ProfileImage{}
	}
	return *p
}

// StoreDirLocked is the folder of the attached database, where the store's files sit beside it ("" when memory-only).
// Caller holds AccountsMu.
func StoreDirLocked() string {
	if database == nil {
		return ""
	}
	return filepath.Dir(database.path)
}

// AccountCountLocked is the number of accounts in memory. Caller holds AccountsMu.
func AccountCountLocked() int { return len(accountList) }

// accountsState is the account part of the store, as a test swaps it.
type accountsState struct {
	byHash  map[string]*Account
	list    []*Account
	rows    map[*Account]*rowState
	pending []pendingToken
	latest  *Account
	next    uint64
	db      *DB
	savedL  *Account
	savedN  uint64
}

func captureLocked() accountsState {
	return accountsState{accountsByHash, accountList, accountRows, pendingTokens, LatestAcc, NextAvatarID, database, savedLatest, savedNext}
}

func (s accountsState) restoreLocked() {
	accountsByHash, accountList, accountRows, pendingTokens = s.byHash, s.list, s.rows, s.pending
	LatestAcc, NextAvatarID, database, savedLatest, savedNext = s.latest, s.next, s.db, s.savedL, s.savedN
}

// SwapAccountsForTest replaces the in-memory accounts with entries (access token or alias -> account; aliases share their
// account). The entries are written to an attached database, and they count as saved as they are. It returns the function
// that restores the previous state, the attached database included. Tests only: production loads them with OpenStore.
func SwapAccountsForTest(entries map[string]*Account) (restore func(), err error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	prev := captureLocked()
	accountsByHash, accountList, accountRows, pendingTokens = map[string]*Account{}, nil, map[*Account]*rowState{}, nil
	for _, token := range slices.Sorted(maps.Keys(entries)) {
		acc := entries[token]
		if accountRows[acc] == nil {
			accountList = append(accountList, acc)
			accountRows[acc] = &rowState{}
		}
		addTokenLocked(acc, token, token == acc.AccessToken)
	}
	LatestAcc = highestAidLocked()
	savedLatest, savedNext = nil, 0
	if database != nil { // the table is replaced, so its rows go too
		if err := database.Update(func(tx *sql.Tx) error { _, err := tx.Exec(`DELETE FROM accounts`); return err }); err != nil {
			prev.restoreLocked()
			return nil, err
		}
		savedLatest, savedNext = nil, 0
	}
	if err := commitLocked(nil, accountList, 0, callerSite()); err != nil {
		prev.restoreLocked()
		return nil, err
	}
	return func() {
		AccountsMu.Lock()
		defer AccountsMu.Unlock()
		prev.restoreLocked()
	}, nil
}

// AddAccountForTest adds acc to the table under token (counted as its own token when it is acc's AccessToken) and saves
// it: a test that needs one more account than it installed. Caller holds AccountsMu.
func AddAccountForTest(token string, acc *Account) error {
	accountList = append(accountList, acc)
	accountRows[acc] = &rowState{}
	addTokenLocked(acc, token, token == acc.AccessToken)
	if err := SaveLocked(nil, acc); err != nil {
		forgetAccountLocked(acc)
		return err
	}
	return nil
}

// ClearMemoryForTest empties the in-memory accounts and leaves the database as it is, so a test can reload them from it.
// The returned function restores the previous state.
func ClearMemoryForTest() func() {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	prev := captureLocked()
	accountsByHash, accountList, accountRows, pendingTokens = map[string]*Account{}, nil, map[*Account]*rowState{}, nil
	LatestAcc, savedLatest, savedNext = nil, nil, 0
	return func() {
		AccountsMu.Lock()
		defer AccountsMu.Unlock()
		prev.restoreLocked()
	}
}
