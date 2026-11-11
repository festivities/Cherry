package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"cherry/internal/metrics"
)

// Treasure World board state (economy/treasure.go): each account's season, token position, tickets and their refill
// timer, jewels, tutorial flag, Ranking Points, exchange counters, ticket cap raises, quest and mission sets and claims.
// The book lives in the cherry.db table treasure_records (one row per aid); treasure.json is read only by cmd/cherry-import.
// The rules live in economy; this file holds the book and saves one board at a time.

// TreasureRecord is one account's board state. Charged is the Unix millisecond the refill timer started (0: not started).
// Points are the season's Ranking Points. Exchanged holds the Jewels exchanged today per ring position (key: the position
// as text) and ExDay the 06:00 JST day it counts; CapSteps is how many ticket cap raises were bought. PointClaimed lists the
// point reward ids claimed and RankClaimed whether the rank reward was. KeyPending is true while the token stands on a gold
// key square whose Mystery Card is not used yet; TravelPending is true while it stands on a travel square whose travel is
// not used yet. MiniGameLeft is the Jewels the mini-game square the token stands on still allows (0 elsewhere). Worn is
// legacy: the worn buffs are the account's equipped codes (economy/treasure_buff.go), and a write drops it. Secret is the
// secret quest of a secret quest square (nil: none). TravelSince is the Unix millisecond TravelPending was set (0: not set);
// Trapped is true while the token is held on a trap square and TrapCount the trapped rolls so far.
type TreasureRecord struct {
	Season        int64            `json:"season"`
	Position      int              `json:"position"`
	Tickets       int64            `json:"tickets"`
	Charged       int64            `json:"charged"`
	Jewels        int64            `json:"jewels"`
	Tutorial      bool             `json:"tutorial,omitempty"`
	Points        int64            `json:"points,omitempty"`
	ExDay         int64            `json:"exDay,omitempty"`
	Exchanged     map[string]int64 `json:"exchanged,omitempty"`
	CapSteps      int64            `json:"capSteps,omitempty"`
	KeyPending    bool             `json:"keyPending,omitempty"`
	TravelPending bool             `json:"travelPending,omitempty"`
	TravelSince   int64            `json:"travelSince,omitempty"`
	Trapped       bool             `json:"trapped,omitempty"`
	TrapCount     int64            `json:"trapCount,omitempty"`
	MiniGameLeft  int64            `json:"miniGameLeft,omitempty"`
	Worn          []string         `json:"worn,omitempty"`
	Secret        *TreasureSecret  `json:"secret,omitempty"`
	Quests        TreasureList     `json:"quests"`
	Missions      TreasureList     `json:"missions"`
	PointClaimed  []int64          `json:"pointClaimed,omitempty"`
	RankClaimed   bool             `json:"rankClaimed,omitempty"`
}

// TreasureSecret is a secret quest: the square it belongs to, whether the account enabled it (cs 26), the Gacha pulls counted
// toward Goal, and Done once Goal is reached (cs 25 pays it while the token stands on the square).
type TreasureSecret struct {
	Square  int   `json:"square"`
	Enabled bool  `json:"enabled,omitempty"`
	Count   int64 `json:"count,omitempty"`
	Goal    int64 `json:"goal"`
	Done    bool  `json:"done,omitempty"`
}

// TreasureList is a posted quest or mission set: Set counts the sets posted (0: none yet), Posted is the Unix millisecond
// the set went up and Slots its tasks in order.
type TreasureList struct {
	Set    int64          `json:"set"`
	Posted int64          `json:"posted"`
	Slots  []TreasureSlot `json:"slots,omitempty"`
}

// TreasureSlot is one task of a set: its index in the data pool, the progress count and the state (0 in progress,
// 1 complete and claimable, 2 claimed).
type TreasureSlot struct {
	Task  int64 `json:"task"`
	Count int64 `json:"count"`
	State int64 `json:"state"`
}

// Clone copies r: changing the copy leaves r, and the saved book, alone.
func (r TreasureRecord) Clone() TreasureRecord {
	r.Exchanged = maps.Clone(r.Exchanged)
	r.PointClaimed = slices.Clone(r.PointClaimed)
	r.Worn = slices.Clone(r.Worn)
	if r.Secret != nil {
		s := *r.Secret
		r.Secret = &s
	}
	r.Quests.Slots = slices.Clone(r.Quests.Slots)
	r.Missions.Slots = slices.Clone(r.Missions.Slots)
	return r
}

type savedTreasure struct {
	Version  int                       `json:"version"`
	Accounts map[string]TreasureRecord `json:"accounts"`
}

var (
	// TreasureMu guards TreasureAccounts and its saves. No other store lock is taken while it is held.
	TreasureMu sync.Mutex
	// TreasureAccounts maps an account's aid to its board state. UpdateTreasure changes one entry.
	TreasureAccounts = map[string]TreasureRecord{}
)

// loadTreasureFromDB reads the board rows of d into memory (OpenStore, ReloadStores). A nil d empties the book.
func loadTreasureFromDB(d *DB) error {
	m := map[string]TreasureRecord{}
	if d != nil {
		if err := d.View(func(q Querier) error {
			var err error
			m, err = readTreasure(q)
			return err
		}); err != nil {
			return err
		}
	}
	TreasureMu.Lock()
	defer TreasureMu.Unlock()
	TreasureAccounts = m
	return nil
}

// parseTreasureFile reads a treasure.json the way the server used to load it, for cmd/cherry-import and the startup guard.
// A missing file is an empty book.
func parseTreasureFile(path string) (map[string]TreasureRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	accounts := map[string]TreasureRecord{}
	if err == nil {
		var saved savedTreasure
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, fmt.Errorf("invalid treasure store: %w", err)
		}
		if saved.Version != 1 {
			return nil, errors.New("invalid treasure store version")
		}
		if saved.Accounts != nil {
			accounts = saved.Accounts
		}
	}
	return accounts, nil
}

// ResetTreasure empties the book in memory (tests). It does not touch an attached database.
func ResetTreasure() {
	TreasureMu.Lock()
	TreasureAccounts = map[string]TreasureRecord{}
	TreasureMu.Unlock()
}

// TreasureRecordOf returns aid's saved board state (false when it has none). Its maps and slices are shared with the
// book: Clone before changing them.
func TreasureRecordOf(aid string) (TreasureRecord, bool) {
	TreasureMu.Lock()
	defer TreasureMu.Unlock()
	rec, ok := TreasureAccounts[aid]
	return rec, ok
}

// TreasureSnapshot returns a copy of the whole book's map (the ranking reads every board).
func TreasureSnapshot() map[string]TreasureRecord {
	TreasureMu.Lock()
	defer TreasureMu.Unlock()
	return maps.Clone(TreasureAccounts)
}

// UpdateTreasure applies fn to aid's board state and saves that one row. The change is kept only when the save works, and
// the record after fn is returned either way. Only the row of aid is written: the save names the one board it changed.
func UpdateTreasure(aid string, fn func(*TreasureRecord)) (TreasureRecord, error) {
	TreasureMu.Lock()
	defer TreasureMu.Unlock()
	defer metrics.SaveSince(time.Now())
	prev, had := TreasureAccounts[aid]
	rec := prev
	fn(&rec)
	TreasureAccounts[aid] = rec
	if err := saveTreasureRowLocked(aid, rec); err != nil {
		if had {
			TreasureAccounts[aid] = prev
		} else {
			delete(TreasureAccounts, aid)
		}
		return rec, err
	}
	return rec, nil
}

// saveTreasureRowLocked writes the row of aid with rec in one transaction. Without an attached database nothing is written.
// Caller holds TreasureMu.
func saveTreasureRowLocked(aid string, rec TreasureRecord) error {
	d := attachedDB()
	if d == nil {
		return nil
	}
	site := callerSite()
	data, err := rowJSON(rec)
	if err != nil {
		return err
	}
	if err := d.Update(func(tx *sql.Tx) error {
		return upsertRow(tx, "treasure_records", slotBack, []string{"aid", "data"}, []any{aid, data}, "aid = ?", aid)
	}); err != nil {
		return err
	}
	if testing.Testing() {
		netCheckTreasure(d, site)
	}
	return nil
}
