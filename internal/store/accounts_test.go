package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swapStore gives a test an empty account table and a fresh database folder; cleanup closes the database and restores the
// previous state. It returns the database file path.
func swapStore(t *testing.T) string {
	t.Helper()
	restore, err := SwapAccountsForTest(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	path := filepath.Join(t.TempDir(), DBFileName)
	if err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseStore() })
	return path
}

// countRows returns the number of rows of table in the database at path, read through a second handle.
func countRows(t *testing.T, path, table string) int {
	t.Helper()
	d, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.View(func(q Querier) error {
		return q.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRestartRoundTrip: an account with a spend, a ledger line and aliases comes back after a restart with the same
// balances, ledger rows and tokens; every token, the own one included, resolves to it.
func TestRestartRoundTrip(t *testing.T) {
	path := swapStore(t)
	AccountsMu.Lock()
	acc, err := newAccountLocked("")
	if err == nil {
		acc.Aid, acc.Name = "9001", "restart"
		err = SaveLocked(nil, acc)
	}
	if err == nil {
		err = ApplyDeltasLocked(acc, LedgerDeltas{Gems: 300}, "test:credit")
	}
	if err == nil {
		err = ApplyDeltasLocked(acc, LedgerDeltas{Gems: -120}, "test:spend")
	}
	own := acc.AccessToken
	var aliases []string
	for i := 0; err == nil && i < 2; i++ {
		var tok string
		tok, err = MintAliasLocked(acc)
		aliases = append(aliases, tok)
	}
	NextAvatarID = 9005
	if err == nil {
		err = SaveLocked(nil)
	}
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(path); err != nil {
		t.Fatalf("restart: %v", err)
	}
	lines, lerr := LedgerLines()
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	back := AccountByAidLocked("9001")
	if back == nil || back.Gems != 180 || back.Name != "restart" {
		t.Fatalf("account after restart = %+v", back)
	}
	if LatestAcc != back || NextAvatarID != 9005 {
		t.Fatalf("latest or next id after restart: %v %d", LatestAcc == back, NextAvatarID)
	}
	for _, tok := range append([]string{own}, aliases...) {
		if AccountByTokenLocked(tok) != back {
			t.Fatalf("a token does not resolve to the account after restart")
		}
	}
	if n := len(AccountTokenHashesLocked(back)); n != 3 {
		t.Fatalf("token rows = %d, want 3", n)
	}
	if n := AccountCountLocked(); n != 1 {
		t.Fatalf("accounts = %d, want 1", n)
	}
	if lerr != nil || len(lines) != 2 || lines[0].Balance != 300 || lines[1].Balance != 180 || lines[1].Reason != "test:spend" {
		t.Fatalf("ledger after restart = %+v (%v)", lines, lerr)
	}
}

// TestSpendRollsBackWhenItsSaveFails: a spend whose transaction cannot begin leaves memory and the database as they were.
func TestSpendRollsBackWhenItsSaveFails(t *testing.T) {
	path := swapStore(t)
	AccountsMu.Lock()
	acc, err := newAccountLocked("")
	if err == nil {
		acc.Aid = "9002"
		err = ApplyDeltasLocked(acc, LedgerDeltas{Gems: 50}, "test:credit")
	}
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := AttachedDB().Close(); err != nil { // the injected failure: every transaction fails from now on
		t.Fatal(err)
	}
	AccountsMu.Lock()
	err = ApplyDeltasLocked(acc, LedgerDeltas{Gems: -10}, "test:fail")
	gems := acc.Gems
	AccountsMu.Unlock()
	if err == nil || gems != 50 {
		t.Fatalf("spend with a failing save: err %v, gems %d; want an error and 50", err, gems)
	}
	if n := countRows(t, path, "ledger"); n != 1 {
		t.Fatalf("ledger rows = %d, want 1 (the credit only)", n)
	}
}

// TestAliasRollsBackWhenItsSaveFails: an alias whose save fails is not added, in memory or in the database.
func TestAliasRollsBackWhenItsSaveFails(t *testing.T) {
	path := swapStore(t)
	AccountsMu.Lock()
	acc, err := newAccountLocked("")
	if err == nil {
		acc.Aid = "9003"
		err = SaveLocked(nil, acc)
	}
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := AttachedDB().Close(); err != nil { // the injected failure
		t.Fatal(err)
	}
	AccountsMu.Lock()
	before := len(AccountTokenHashesLocked(acc))
	_, err = MintAliasLocked(acc)
	after := len(AccountTokenHashesLocked(acc))
	AccountsMu.Unlock()
	if err == nil || after != before {
		t.Fatalf("alias with a failing save: err %v, tokens %d -> %d; want an error and no new token", err, before, after)
	}
	if n := countRows(t, path, "tokens"); n != 1 {
		t.Fatalf("token rows = %d, want the own one only", n)
	}
}

// TestSafetyNetTripsOnMissedAccount: a save that names one account while another one changed panics, with the aid and the
// caller's file:line and none of the content.
func TestSafetyNetTripsOnMissedAccount(t *testing.T) {
	swapStore(t)
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	a, err := newAccountLocked("")
	if err != nil {
		t.Fatal(err)
	}
	a.Aid = "9011"
	if err := SaveLocked(nil, a); err != nil {
		t.Fatal(err)
	}
	b, err := newAccountLocked("")
	if err != nil {
		t.Fatal(err)
	}
	b.Aid = "9012"
	if err := SaveLocked(nil, b); err != nil {
		t.Fatal(err)
	}
	b.Gems = 987654 // a handler that changes b and names only a
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		SaveLocked(nil, a)
	}()
	msg, _ := recovered.(string)
	if !strings.Contains(msg, "9012") || !strings.Contains(msg, "accounts_test.go:") || strings.Contains(msg, "987654") {
		t.Fatalf("safety net panic = %v, want the aid and the save site without content", recovered)
	}
}

// TestImportedDatabaseLoadsAndSaves: a database written by the importer from a legacy accounts.json with an account of 611
// aliases and an account with aid "0" loads, every alias resolves, and a save after the load writes.
func TestImportedDatabaseLoadsAndSaves(t *testing.T) {
	restore, err := SwapAccountsForTest(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	dir := t.TempDir()
	file := savedAccounts{Version: 1, Accounts: map[string]StoredAccount{}, Aliases: map[string]string{}, Latest: "tokA", NextAvatarID: 9100}
	file.Accounts["tokA"] = StoredAccount{AccessToken: "tokA", SessionKey: "skA", Aid: "9021", Name: "many", Gems: 40}
	file.Accounts["tokB"] = StoredAccount{AccessToken: "tokB", SessionKey: "skB", Aid: "0", Name: "new"}
	file.Aliases["tokA"], file.Aliases["tokB"] = "tokA", "tokB"
	for i := 0; i < 611; i++ {
		file.Aliases[fmt.Sprintf("alias-%03d-%s", i, strings.Repeat("x", 120))] = "tokA"
	}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), DBFileName)
	d, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportDir(d, dir); err != nil {
		d.Close()
		t.Fatalf("import: %v", err)
	}
	d.Close()
	if err := OpenStore(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { CloseStore() })
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	many := AccountByAidLocked("9021")
	if many == nil || LatestAcc != many || NextAvatarID != 9100 || AccountCountLocked() != 2 {
		t.Fatalf("loaded accounts: many=%v latest=%v next=%d count=%d", many != nil, LatestAcc == many, NextAvatarID, AccountCountLocked())
	}
	if n := len(AccountTokenHashesLocked(many)); n != 612 {
		t.Fatalf("tokens of the 611-alias account = %d, want 612", n)
	}
	for k, owner := range file.Aliases {
		if owner == "tokA" && AccountByTokenLocked(k) != many {
			t.Fatal("an alias of the 611-alias account does not resolve")
		}
	}
	zero := AccountByTokenLocked("tokB")
	if zero == nil || zero.Aid != "0" {
		t.Fatalf("aid 0 account = %+v", zero)
	}
	if err := ApplyDeltasLocked(zero, LedgerDeltas{Gems: 5}, "test:after-import"); err != nil {
		t.Fatalf("save after load: %v", err)
	}
}
