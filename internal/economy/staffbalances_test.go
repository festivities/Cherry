package economy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// balancesFixture gives the test an empty memory of accounts on a fresh database.
func balancesFixture(t *testing.T) {
	t.Helper()
	t.Cleanup(store.ClearMemoryForTest())
	testutil.UseDatabase(t)
}

// balanceAccount makes an account in the store with the given balances (ride points are its racing record's points).
func balanceAccount(t *testing.T, gems, cash, tickets, smiles, boss, ride int64) *store.Account {
	t.Helper()
	acc, err := store.NewAccount()
	if err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc.Gems, acc.Cash, acc.FaceTickets, acc.Smiles, acc.BossCoins = gems, cash, tickets, smiles, boss
	setRidePointsLocked(acc, ride)
	if err := store.SaveLocked(nil, acc); err != nil {
		t.Fatal(err)
	}
	return acc
}

// balanceTotal is how many accounts a change to every account reaches.
func balanceTotal(t *testing.T) int {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return len(store.AllAccountsLocked())
}

// ledgerWithReason returns the ledger lines whose reason is reason.
func ledgerWithReason(t *testing.T, reason string) []store.LedgerLine {
	t.Helper()
	lines, err := store.LedgerLines()
	if err != nil {
		t.Fatal(err)
	}
	var out []store.LedgerLine
	for _, l := range lines {
		if l.Reason == reason {
			out = append(out, l)
		}
	}
	return out
}

// auditGrantAll counts the audit rows of the change to every account whose detail names send.
func auditGrantAll(t *testing.T, send string) int {
	t.Helper()
	rows, err := store.AuditRows(0, 50)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Action == "grant-all" && r.Target == store.AllPlayersTarget && strings.Contains(r.Detail, send) {
			n++
		}
	}
	return n
}

// TestStaffBalancesSetResetsChosenOnly: a set to 0 of Gems and Cash changes the accounts that hold them, writes one ledger line per
// currency with the staff reason, leaves the other balances alone, and writes one audit row.
func TestStaffBalancesSetResetsChosenOnly(t *testing.T) {
	balancesFixture(t)
	a := balanceAccount(t, 500, 7, 3, 9, 4, 20)
	balanceAccount(t, 0, 0, 0, 0, 0, 0)
	total := balanceTotal(t)

	changed, applied, err := StaffBalancesAll(StaffBalances{SendID: "reset-send-0001", Set: true,
		Values: map[string]int64{"gems": 0, "cash": 0}, Expect: total, Actor: "dev-owner", Reason: "full reset"}, time.Now())
	if err != nil || !applied {
		t.Fatalf("apply: applied %v, err %v", applied, err)
	}
	if changed != 1 {
		t.Fatalf("changed %d accounts, want 1 (only the one holding Gems or Cash)", changed)
	}
	store.AccountsMu.Lock()
	got := [6]int64{a.Gems, a.Cash, a.FaceTickets, a.Smiles, a.BossCoins, ridePointsLocked(a)}
	store.AccountsMu.Unlock()
	if want := [6]int64{0, 0, 3, 9, 4, 20}; got != want {
		t.Fatalf("balances after the reset = %v, want %v (only Gems and Cash change)", got, want)
	}
	lines := ledgerWithReason(t, "staff:dev-owner:full reset")
	if len(lines) != 2 {
		t.Fatalf("ledger lines with the staff reason = %+v, want one gems and one cash line", lines)
	}
	for _, l := range lines {
		if l.Balance != 0 || (l.Currency != "gems" && l.Currency != "cash") {
			t.Fatalf("ledger line %+v: want a gems or cash line down to 0", l)
		}
	}
	if n := auditGrantAll(t, "reset-send-0001"); n != 1 {
		t.Fatalf("%d audit rows for the change, want 1", n)
	}
}

// TestStaffBalancesAddClampsAtMaxBalance: an add gives every account the value and stops at store.MaxBalance: the account near the
// cap gains only the room it has, and the account at the cap gains no Gems.
func TestStaffBalancesAddClampsAtMaxBalance(t *testing.T) {
	balancesFixture(t)
	near := balanceAccount(t, store.MaxBalance-10, 0, 0, 0, 0, 0)
	atCap := balanceAccount(t, store.MaxBalance, 0, 0, 0, 0, 0)
	empty := balanceAccount(t, 0, 0, 0, 0, 0, 0)
	total := balanceTotal(t)

	changed, applied, err := StaffBalancesAll(StaffBalances{SendID: "add-send-0001", Values: map[string]int64{"gems": 1000, "cash": 5},
		Expect: total, Actor: "dev-owner", Reason: "free money"}, time.Now())
	if err != nil || !applied {
		t.Fatalf("apply: applied %v, err %v", applied, err)
	}
	if changed != 3 {
		t.Fatalf("changed %d accounts, want 3 (the cash gains on all of them)", changed)
	}
	store.AccountsMu.Lock()
	ok := near.Gems == store.MaxBalance && near.Cash == 5 && atCap.Gems == store.MaxBalance && atCap.Cash == 5 &&
		empty.Gems == 1000 && empty.Cash == 5
	nearGems, atCapGems, emptyGems := near.Gems, atCap.Gems, empty.Gems
	store.AccountsMu.Unlock()
	if !ok {
		t.Fatalf("gems after the add = near %d, at cap %d, empty %d; want %d, %d, 1000", nearGems, atCapGems, emptyGems, store.MaxBalance, store.MaxBalance)
	}
	found := false
	for _, l := range ledgerWithReason(t, "staff:dev-owner:free money") {
		if l.Currency == "gems" && l.Delta == 10 && l.Balance == store.MaxBalance {
			found = true
		}
	}
	if !found {
		t.Fatal("no gems ledger line of 10 for the account near the cap")
	}
}

// TestStaffBalancesRefusesBadChanges: an unknown balance, an amount out of range, no balance, an empty reason, a count the reviewer
// did not see and a send id that is missing are refused, and no account moves.
func TestStaffBalancesRefusesBadChanges(t *testing.T) {
	balancesFixture(t)
	a := balanceAccount(t, 40, 0, 0, 0, 0, 0)
	total := balanceTotal(t)
	cases := map[string]StaffBalances{
		"no balance chosen":     {SendID: "x-send-0001", Values: map[string]int64{}, Expect: total, Reason: "r"},
		"unknown balance":       {SendID: "x-send-0001", Values: map[string]int64{"gold": 5}, Expect: total, Reason: "r"},
		"add zero":              {SendID: "x-send-0001", Values: map[string]int64{"gems": 0}, Expect: total, Reason: "r"},
		"add above the grant":   {SendID: "x-send-0001", Values: map[string]int64{"gems": store.MaxGrant + 1}, Expect: total, Reason: "r"},
		"set below zero":        {SendID: "x-send-0001", Set: true, Values: map[string]int64{"gems": -1}, Expect: total, Reason: "r"},
		"set above the balance": {SendID: "x-send-0001", Set: true, Values: map[string]int64{"gems": store.MaxBalance + 1}, Expect: total, Reason: "r"},
		"reason of 201 bytes":   {SendID: "x-send-0001", Values: map[string]int64{"gems": 5}, Expect: total, Reason: strings.Repeat("r", 201)},
		"no send id":            {Values: map[string]int64{"gems": 5}, Expect: total, Reason: "r"},
		"count changed":         {SendID: "x-send-0001", Values: map[string]int64{"gems": 5}, Expect: total + 1, Reason: "r"},
	}
	for name, b := range cases {
		if _, applied, err := StaffBalancesAll(b, time.Now()); err == nil || applied || !errors.Is(err, store.ErrBadInput) {
			t.Errorf("%s: applied %v, err %v; want a refusal (ErrBadInput)", name, applied, err)
		}
	}
	store.AccountsMu.Lock()
	gems := a.Gems
	store.AccountsMu.Unlock()
	if gems != 40 || len(ledgerWithReason(t, "staff:dev-owner:r")) != 0 {
		t.Fatalf("a refused change moved gems to %d or wrote ledger lines", gems)
	}
	if n := auditGrantAll(t, "x-send-0001"); n != 0 {
		t.Fatalf("refused changes wrote %d audit rows", n)
	}
}

// TestStaffBalancesNoChangeIsRefused: a set of 0 of a balance that every account already holds at 0 changes nothing, so the review
// and the apply both refuse it.
func TestStaffBalancesNoChangeIsRefused(t *testing.T) {
	balancesFixture(t)
	balanceAccount(t, 0, 0, 0, 0, 0, 0)
	balanceAccount(t, 0, 0, 0, 0, 0, 0)
	total := balanceTotal(t)
	b := StaffBalances{Set: true, Values: map[string]int64{"cash": 0}, Expect: total, Reason: "r", SendID: "none-send-0001", Actor: "dev-owner"}
	if _, _, err := PreviewStaffBalances(b); !errors.Is(err, store.ErrBadInput) {
		t.Errorf("preview err %v, want ErrBadInput", err)
	}
	if _, applied, err := StaffBalancesAll(b, time.Now()); !errors.Is(err, store.ErrBadInput) || applied {
		t.Errorf("apply: applied %v, err %v; want ErrBadInput", applied, err)
	}
}

// TestStaffBalancesSendIDAppliesOnce: the same send id applied again changes nothing, even after the balances moved on.
func TestStaffBalancesSendIDAppliesOnce(t *testing.T) {
	balancesFixture(t)
	a := balanceAccount(t, 0, 0, 0, 0, 0, 0)
	total := balanceTotal(t)
	first := StaffBalances{SendID: "once-send-0001", Values: map[string]int64{"gems": 9}, Expect: total, Actor: "dev-owner", Reason: "gift"}
	if _, applied, err := StaffBalancesAll(first, time.Now()); err != nil || !applied {
		t.Fatalf("first apply: applied %v, err %v", applied, err)
	}
	store.AccountsMu.Lock()
	a.Gems = 25
	err := store.SaveLocked(nil, a)
	store.AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	again := StaffBalances{SendID: "once-send-0001", Set: true, Values: map[string]int64{"gems": 0}, Expect: total, Actor: "dev-owner", Reason: "reset"}
	changed, applied, err := StaffBalancesAll(again, time.Now())
	if err != nil || applied || changed != 0 {
		t.Fatalf("a send id already applied: changed %d, applied %v, err %v; want nothing", changed, applied, err)
	}
	store.AccountsMu.Lock()
	gems := a.Gems
	store.AccountsMu.Unlock()
	if gems != 25 {
		t.Fatalf("gems = %d after the repeat, want 25", gems)
	}
	if n := auditGrantAll(t, "once-send-0001"); n != 1 {
		t.Fatalf("%d audit rows for the send, want 1", n)
	}
}

// TestStaffBalancesFailureRestoresEveryAccount: an account the change cannot move (a negative balance held in memory, which the
// ledger never writes) refuses the whole change: the accounts changed before it go back, and nothing is saved or audited.
func TestStaffBalancesFailureRestoresEveryAccount(t *testing.T) {
	balancesFixture(t)
	good := balanceAccount(t, 100, 0, 0, 0, 0, 0)
	bad := balanceAccount(t, -100, 0, 0, 0, 0, 0)
	total := balanceTotal(t)

	_, applied, err := StaffBalancesAll(StaffBalances{SendID: "fail-send-0001", Values: map[string]int64{"gems": 1}, Expect: total,
		Actor: "dev-owner", Reason: "boom"}, time.Now())
	if err == nil || applied {
		t.Fatalf("applied %v, err %v; want a refusal", applied, err)
	}
	store.AccountsMu.Lock()
	goodGems, badGems := good.Gems, bad.Gems
	store.AccountsMu.Unlock()
	if goodGems != 100 || badGems != -100 {
		t.Fatalf("gems = %d, %d after the refusal; want 100 and -100 (restored)", goodGems, badGems)
	}
	if n := len(ledgerWithReason(t, "staff:dev-owner:boom")); n != 0 {
		t.Fatalf("the refused change wrote %d ledger lines", n)
	}
	if n := auditGrantAll(t, "fail-send-0001"); n != 0 {
		t.Fatalf("the refused change wrote %d audit rows", n)
	}
}

// TestStaffBalancesExtraHeartsAndTake: extra hearts are a balance of every account (an add gives each account the value). A grant
// that takes away more than an account holds is refused and changes nothing. A take removes the value from each chosen balance and
// stops at 0, so an account with none of it does not change; the ledger carries the staff reason for Gems only.
func TestStaffBalancesExtraHeartsAndTake(t *testing.T) {
	balancesFixture(t)
	a := balanceAccount(t, 10, 0, 0, 0, 0, 0)
	b := balanceAccount(t, 500, 0, 0, 0, 0, 0)
	c := balanceAccount(t, 0, 0, 0, 0, 0, 0)
	total := balanceTotal(t)
	now := jstAt(22, 12, 0)

	if changed, applied, err := StaffBalancesAll(StaffBalances{SendID: "extra-send-0001", Values: map[string]int64{"extraHearts": 5},
		Expect: total, Actor: "dev-owner", Reason: "event"}, now); err != nil || !applied || changed != 3 {
		t.Fatalf("add: changed %d, applied %v, err %v; want 3 accounts", changed, applied, err)
	}
	store.AccountsMu.Lock()
	_, refusal := GrantLocked(a, StaffGrant{ExtraHearts: -6}, "staff:dev-owner:too many", now)
	extras := [3]int64{heartExtra(a), heartExtra(b), heartExtra(c)}
	store.AccountsMu.Unlock()
	if !errors.Is(refusal, store.ErrBadInput) || extras != [3]int64{5, 5, 5} {
		t.Fatalf("a take-away below 0: err %v, extra hearts %v; want ErrBadInput and 5 each", refusal, extras)
	}

	changed, applied, err := StaffBalancesAll(StaffBalances{SendID: "extra-send-0002", Take: true,
		Values: map[string]int64{"extraHearts": 9, "gems": 50}, Expect: total, Actor: "dev-owner", Reason: "clean up"}, now)
	if err != nil || !applied || changed != 3 {
		t.Fatalf("take: changed %d, applied %v, err %v; want 3 accounts", changed, applied, err)
	}
	store.AccountsMu.Lock()
	got := [5]int64{a.Gems, b.Gems, c.Gems, heartExtra(a), heartExtra(b)}
	store.AccountsMu.Unlock()
	if want := [5]int64{0, 450, 0, 0, 0}; got != want {
		t.Fatalf("after the take: gems a/b/c and extra a/b = %v, want %v (gems stop at 0, extra hearts go to 0)", got, want)
	}
	if lines := ledgerWithReason(t, "staff:dev-owner:clean up"); len(lines) != 2 || lines[0].Currency != "gems" || lines[1].Currency != "gems" ||
		lines[0].Delta+lines[1].Delta != -60 {
		t.Fatalf("ledger lines = %+v, want the two gems take-aways (-10 and -50 in all) and no extra hearts line", lines)
	}

	if changed, applied, err = StaffBalancesAll(StaffBalances{SendID: "extra-send-0003", Take: true, Values: map[string]int64{"gems": 50},
		Expect: total, Actor: "dev-owner", Reason: "clean up again"}, now); err != nil || !applied || changed != 1 {
		t.Fatalf("second take: changed %d, applied %v, err %v; want only the account that still holds Gems", changed, applied, err)
	}
}
