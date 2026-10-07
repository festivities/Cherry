package store

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// ponytail: lab accounts keep their showcase grants (room furniture, pets); every
// other account starts fresh. Hard-coded aids; move to a store flag if more are needed.
var LabAids = map[string]bool{"1001": true, "1002": true}

// MaxBalance keeps every balance inside the client's int32 asInt.
const MaxBalance = 1 << 30

// LedgerDeltas is a set of signed balance changes applied together.
type LedgerDeltas struct{ Gems, Cash, FaceTickets int64 }

// BalanceError is returned when a delta would take a balance below 0 or above maxBalance.
type BalanceError struct {
	Currency string
	Balance  int64
	Delta    int64
}

func (e *BalanceError) Error() string {
	return fmt.Sprintf("ledger: %s balance %d cannot change by %d", e.Currency, e.Balance, e.Delta)
}

type LedgerLine struct {
	Time     string `json:"time"`
	Aid      string `json:"aid"`
	Currency string `json:"currency"`
	Delta    int64  `json:"delta"`
	Balance  int64  `json:"balance"`
	Reason   string `json:"reason"`
}

// SpendLocked checks and applies d to acc's in-memory balances and returns the log
// lines. It does not save: the caller (holding accountsMu) saves with the rest of its
// mutation, restores `*acc = previous` on failure, and calls flushLedgerLocked after a
// successful save. Nothing changes when an error is returned.
func SpendLocked(acc *Account, d LedgerDeltas, reason string) ([]LedgerLine, error) {
	names := [3]string{"gems", "cash", "faceTickets"}
	fields := [3]*int64{&acc.Gems, &acc.Cash, &acc.FaceTickets}
	deltas := [3]int64{d.Gems, d.Cash, d.FaceTickets}
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

// FlushLedgerLocked appends lines to ledger.jsonl next to accounts.json. Call only
// after the account save succeeded; a failure here is logged, never fatal.
func FlushLedgerLocked(lines []LedgerLine) {
	if len(lines) == 0 || AccountStorePath == "" {
		return
	}
	var buf []byte
	for _, l := range lines {
		b, _ := json.Marshal(l)
		buf = append(append(buf, b...), '\n')
	}
	path := filepath.Join(filepath.Dir(AccountStorePath), "ledger.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = f.Write(buf)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		log.Printf("cherry: ledger log write failed: %v", err)
	}
}

// ApplyDeltasLocked is the standalone transaction: check, apply, save, roll back on a
// save failure, then log. Caller holds accountsMu.
func ApplyDeltasLocked(acc *Account, d LedgerDeltas, reason string) error {
	prev := *acc
	lines, err := SpendLocked(acc, d, reason)
	if err != nil {
		return err
	}
	if err := SaveAccountsLocked(); err != nil {
		*acc = prev
		return err
	}
	FlushLedgerLocked(lines)
	return nil
}

// AccountByAidLocked finds an account by aid (aliases share one pointer).
func AccountByAidLocked(aid string) *Account {
	for _, a := range Accounts {
		if a.Aid == aid {
			return a
		}
	}
	return nil
}
