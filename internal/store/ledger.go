package store

import (
	"fmt"
	"slices"
	"time"
)

// ponytail: lab accounts keep their showcase grants (room furniture, pets); every
// other account starts fresh. Hard-coded aids; move to a store flag if more are needed.
var LabAids = map[string]bool{"1001": true, "1002": true}

// MaxBalance keeps every balance inside the client's int32 asInt.
const MaxBalance = 1 << 30

// LedgerDeltas is a set of signed balance changes applied together. Smiles is the earned
// Atelier currency (Garden gathering pays it, economy/gathering.go).
type LedgerDeltas struct{ Gems, Cash, FaceTickets, Smiles int64 }

// BalanceError is returned when a delta would take a balance below 0 or above maxBalance.
type BalanceError struct {
	Currency string
	Balance  int64
	Delta    int64
}

func (e *BalanceError) Error() string {
	return fmt.Sprintf("ledger: %s balance %d cannot change by %d", e.Currency, e.Balance, e.Delta)
}

// LedgerLine is one row of the ledger table: a balance change of one account, with the balance after it.
type LedgerLine struct {
	Time     string `json:"time"`
	Aid      string `json:"aid"`
	Currency string `json:"currency"`
	Delta    int64  `json:"delta"`
	Balance  int64  `json:"balance"`
	Reason   string `json:"reason"`
}

// SpendLocked checks and applies d to acc's in-memory balances and returns the log
// lines. It does not save: the caller (holding accountsMu) saves them with its mutation
// through SaveLocked(lines, acc), restores `*acc = previous` on failure. Nothing changes
// when an error is returned.
func SpendLocked(acc *Account, d LedgerDeltas, reason string) ([]LedgerLine, error) {
	names := [4]string{"gems", "cash", "faceTickets", "smiles"}
	fields := [4]*int64{&acc.Gems, &acc.Cash, &acc.FaceTickets, &acc.Smiles}
	deltas := [4]int64{d.Gems, d.Cash, d.FaceTickets, d.Smiles}
	for i, v := range deltas {
		nb := *fields[i] + v
		if v > MaxBalance || v < -MaxBalance || nb < 0 || nb > MaxBalance {
			return nil, &BalanceError{names[i], *fields[i], v}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var lines []LedgerLine
	for i, v := range deltas {
		if v == 0 {
			continue
		}
		*fields[i] += v
		lines = append(lines, LedgerLine{now, acc.Aid, names[i], v, *fields[i], reason})
	}
	return lines, nil
}

// ApplyDeltasLocked is the standalone transaction: check, apply, save with its ledger lines, roll back when the save
// fails. Caller holds accountsMu.
func ApplyDeltasLocked(acc *Account, d LedgerDeltas, reason string) error {
	prev := *acc
	lines, err := SpendLocked(acc, d, reason)
	if err != nil {
		return err
	}
	if err := SaveLocked(lines, acc); err != nil {
		*acc = prev
		return err
	}
	return nil
}

// AccountByAidLocked finds an account by aid. Caller holds AccountsMu.
func AccountByAidLocked(aid string) *Account {
	i := slices.IndexFunc(accountList, func(a *Account) bool { return a.Aid == aid })
	if i < 0 {
		return nil
	}
	return accountList[i]
}

// LedgerLines returns the ledger rows of the attached database in append order, none when the store is memory-only.
// It takes AccountsMu itself: callers must not hold it.
func LedgerLines() ([]LedgerLine, error) {
	AccountsMu.Lock()
	d := database.Load()
	AccountsMu.Unlock()
	if d == nil {
		return nil, nil
	}
	var lines []LedgerLine
	err := d.View(func(q Querier) error {
		var err error
		lines, err = ReadLedger(q)
		return err
	})
	return lines, err
}

// AttachedDB returns the database the store saves to, or nil when it is memory-only. Tests and the lab tools use it.
func AttachedDB() *DB {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	return database.Load()
}
