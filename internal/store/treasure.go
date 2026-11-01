package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
)

// Treasure World board state (economy/treasure.go): each account's season, token position, tickets and their refill
// timer, jewels and tutorial flag, in %LOCALAPPDATA%\Cherry\treasure.json beside circles.json. Absence means no state.
// The rules live in economy; this file holds the book and saves it.

// TreasureRecord is one account's board state. Charged is the Unix millisecond the refill timer started (0: not started).
type TreasureRecord struct {
	Season   int64 `json:"season"`
	Position int   `json:"position"`
	Tickets  int64 `json:"tickets"`
	Charged  int64 `json:"charged"`
	Jewels   int64 `json:"jewels"`
	Tutorial bool  `json:"tutorial,omitempty"`
}

type savedTreasure struct {
	Version  int                       `json:"version"`
	Accounts map[string]TreasureRecord `json:"accounts"`
}

var (
	// TreasureMu guards TreasureAccounts and its saves. No other store lock is taken while it is held.
	TreasureMu sync.Mutex
	// TreasureStorePath is treasure.json ("" in tests: nothing is written).
	TreasureStorePath string
	// TreasureAccounts maps an account's aid to its board state. Writers replace the whole map (UpdateTreasure).
	TreasureAccounts = map[string]TreasureRecord{}
)

// LoadTreasure reads %LOCALAPPDATA%\Cherry\treasure.json (none is not an error).
func LoadTreasure() error {
	dir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	return LoadTreasureFrom(filepath.Join(dir, "Cherry", "treasure.json"))
}

// LoadTreasureFrom loads the board state at path; a missing file is an empty book.
func LoadTreasureFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	accounts := map[string]TreasureRecord{}
	if err == nil {
		var saved savedTreasure
		if err := json.Unmarshal(data, &saved); err != nil {
			return fmt.Errorf("invalid treasure store: %w", err)
		}
		if saved.Version != 1 {
			return errors.New("invalid treasure store version")
		}
		if saved.Accounts != nil {
			accounts = saved.Accounts
		}
	}
	TreasureMu.Lock()
	TreasureStorePath = path
	TreasureAccounts = accounts
	TreasureMu.Unlock()
	return nil
}

// ResetTreasure empties the book and forgets its path (tests).
func ResetTreasure() {
	TreasureMu.Lock()
	TreasureStorePath = ""
	TreasureAccounts = map[string]TreasureRecord{}
	TreasureMu.Unlock()
}

// TreasureRecordOf returns aid's saved board state (false when it has none).
func TreasureRecordOf(aid string) (TreasureRecord, bool) {
	TreasureMu.Lock()
	defer TreasureMu.Unlock()
	rec, ok := TreasureAccounts[aid]
	return rec, ok
}

// UpdateTreasure applies fn to aid's board state and saves the book. The change is kept only when the save works, and
// the record after fn is returned either way.
// ponytail: the whole book is copied and saved per update; per-account locks and a per-account file if the book grows
// past a few thousand boards.
func UpdateTreasure(aid string, fn func(*TreasureRecord)) (TreasureRecord, error) {
	TreasureMu.Lock()
	defer TreasureMu.Unlock()
	rec := TreasureAccounts[aid]
	fn(&rec)
	next := make(map[string]TreasureRecord, len(TreasureAccounts)+1)
	maps.Copy(next, TreasureAccounts)
	next[aid] = rec
	if err := saveTreasureLocked(next); err != nil {
		return rec, err
	}
	TreasureAccounts = next
	return rec, nil
}

// saveTreasureLocked writes accounts to treasure.json (temp file, then rename). Caller holds TreasureMu. Nothing is
// written when TreasureStorePath is "".
func saveTreasureLocked(accounts map[string]TreasureRecord) error {
	if TreasureStorePath == "" {
		return nil
	}
	data, err := json.Marshal(savedTreasure{Version: 1, Accounts: accounts})
	if err != nil {
		return err
	}
	dir := filepath.Dir(TreasureStorePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".treasure-*.tmp")
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
	return os.Rename(tmp.Name(), TreasureStorePath)
}
