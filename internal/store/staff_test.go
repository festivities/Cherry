package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// staffAccount makes the account of identity in the attached database, with its avatar id, name and gems, and returns it with
// its row id. Its uniqueCode is identity + " code".
func staffAccount(t *testing.T, identity, aid, name string, gems int64) (*Account, int64) {
	t.Helper()
	if _, _, err := MintIdentityCode(identity, CodeHash(identity+" code"), true); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := IdentityAccountLocked(identity)
	acc.Aid, acc.Name, acc.Gems = aid, name, gems
	if err := SaveLocked(nil, acc); err != nil {
		t.Fatal(err)
	}
	return acc, accountRows[acc].id
}

// balances reads the gems and cash of acc under the lock.
func balances(acc *Account) (gems, cash int64) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	return acc.Gems, acc.Cash
}

// auditCount is the number of audit rows.
func auditCount(t *testing.T) int {
	t.Helper()
	rows, err := AuditRows(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// TestGrantWritesBalanceLedgerAndAudit: a grant changes the balance, writes one ledger line with the staff identity in its
// reason, and writes its audit row; the reason is trimmed.
func TestGrantWritesBalanceLedgerAndAudit(t *testing.T) {
	attachTestDB(t)
	acc, id := staffAccount(t, "dev-gr", "1001", "Gran", 100)
	if err := GrantPlayer(id, "gems", 250, "  event prize ", "discord:111"); err != nil {
		t.Fatal(err)
	}
	if err := GrantPlayer(id, "cash", 5, "refund", "discord:111"); err != nil {
		t.Fatal(err)
	}
	if gems, cash := balances(acc); gems != 350 || cash != 5 {
		t.Fatalf("balances gems %d cash %d, want 350 and 5", gems, cash)
	}
	lines, err := PlayerLedger(id, 20)
	if err != nil || len(lines) != 2 {
		t.Fatalf("ledger = %d lines, err %v; want 2", len(lines), err)
	}
	if l := lines[1]; l.Aid != "1001" || l.Currency != "gems" || l.Delta != 250 || l.Balance != 350 || l.Reason != "staff:discord:111:event prize" {
		t.Fatalf("gems line = %+v", l)
	}
	rows, err := AuditRows(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var grants []AuditRow
	for _, r := range rows {
		if r.Action == "grant" {
			grants = append(grants, r)
		}
	}
	if len(grants) != 2 || grants[1].Actor != "discord:111" || grants[1].Target != fmt.Sprintf("player:%d", id) {
		t.Fatalf("grant audit rows = %+v", grants)
	}
	if d := grants[1].Detail; !strings.Contains(d, `"delta":250`) || !strings.Contains(d, `"balance":350`) || !strings.Contains(d, `"aid":"1001"`) {
		t.Fatalf("grant audit detail = %s", d)
	}
}

// TestGrantRefusalsChangeNothing: each bad grant is refused with ErrBadInput, and the balance, the ledger and the audit trail
// stay as they were.
func TestGrantRefusalsChangeNothing(t *testing.T) {
	attachTestDB(t)
	acc, id := staffAccount(t, "dev-rf", "1002", "Refused", 350)
	ledgerBefore, auditBefore := len(must(LedgerLines())), auditCount(t)
	refused := []struct {
		name, currency, reason string
		delta                  int64
	}{
		{"bad currency", "gold", "x", 10},
		{"zero", "gems", "x", 0},
		{"too large", "gems", "x", 1000001},
		{"too small", "gems", "x", -1000001},
		{"below zero", "gems", "x", -351},
		{"no reason", "gems", "", 10},
		{"blank reason", "gems", "   ", 10},
		{"long reason", "gems", strings.Repeat("r", 201), 10},
	}
	for _, tc := range refused {
		err := GrantPlayer(id, tc.currency, tc.delta, tc.reason, "discord:1")
		if !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: err %v, want ErrBadInput", tc.name, err)
		}
	}
	if err := GrantPlayer(id+1000, "gems", 10, "x", "discord:1"); !errors.Is(err, ErrNoPlayer) {
		t.Errorf("unknown player: err %v, want ErrNoPlayer", err)
	}
	if gems, _ := balances(acc); gems != 350 {
		t.Fatalf("refused grants changed the balance to %d", gems)
	}
	if n := len(must(LedgerLines())); n != ledgerBefore {
		t.Fatalf("refused grants wrote %d ledger lines", n-ledgerBefore)
	}
	if n := auditCount(t); n != auditBefore {
		t.Fatalf("refused grants wrote %d audit rows", n-auditBefore)
	}
}

// must returns the value of a call that cannot fail in a test.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TestFailedSaveLeavesTheAccountAsItWas: with the database closed every save fails; the grant and the ban change nothing in
// memory, and the tokens still resolve.
func TestFailedSaveLeavesTheAccountAsItWas(t *testing.T) {
	d := attachTestDB(t)
	acc, id := staffAccount(t, "dev-fs", "1003", "Failed", 10)
	AccountsMu.Lock()
	own := acc.AccessToken
	AccountsMu.Unlock()
	if err := d.Close(); err != nil { // every save from now on fails
		t.Fatal(err)
	}
	if err := GrantPlayer(id, "gems", 5, "x", "discord:1"); err == nil {
		t.Fatal("a grant with a failing save succeeded")
	}
	if err := BanPlayer(id, "x", "discord:1"); err == nil {
		t.Fatal("a ban with a failing save succeeded")
	}
	if gems, _ := balances(acc); gems != 10 {
		t.Fatalf("a failed grant left gems %d, want 10", gems)
	}
	AccountsMu.Lock()
	banned := acc.Ban != nil
	AccountsMu.Unlock()
	if banned || resolves(own) != acc {
		t.Fatal("a failed ban left the account banned or its token unresolved")
	}
}

// TestBanRevokesTokensAndSessionKey: a ban ends every token of the account (own and alias), replaces its session key, refuses
// the next uniqueCode and the next login, and survives a reload; a second ban is refused, and the audit row names the reason.
func TestBanRevokesTokensAndSessionKey(t *testing.T) {
	d := attachTestDB(t)
	acc, id := staffAccount(t, "dev-bn", "1004", "Banned", 0)
	AccountsMu.Lock()
	own, session := acc.AccessToken, acc.SessionKey
	alias, err := MintAliasLocked(acc)
	AccountsMu.Unlock()
	if err != nil || resolves(alias) != acc || resolves(own) != acc {
		t.Fatalf("before the ban: alias %v, own resolves %v", err, resolves(own) == acc)
	}
	if err := BanPlayer(id, "  spam  ", "discord:9"); err != nil {
		t.Fatal(err)
	}
	if resolves(own) != nil || resolves(alias) != nil {
		t.Fatal("a token of the banned account still resolves")
	}
	if rows := tokenRows(t, d, acc); len(rows) != 0 {
		t.Fatalf("the ban left %d token rows", len(rows))
	}
	AccountsMu.Lock()
	accountsByHash[CodeHash(alias)] = acc // a token that survived the ban in memory still does not resolve
	AccountsMu.Unlock()
	if resolves(alias) != nil {
		t.Fatal("a surviving token of a banned account resolves")
	}
	AccountsMu.Lock()
	delete(accountsByHash, CodeHash(alias))
	AccountsMu.Unlock()
	AccountsMu.Lock()
	rotated := acc.SessionKey != session && acc.SessionKey != ""
	ban := acc.Ban
	AccountsMu.Unlock()
	if !rotated || ban == nil || ban.Reason != "spam" || ban.By != "discord:9" || ban.At == "" {
		t.Fatalf("after the ban: session rotated %v, ban %+v", rotated, ban)
	}
	if _, _, err := MintIdentityCode("dev-bn", CodeHash("fresh"), true); !errors.Is(err, ErrBanned) {
		t.Fatalf("mint for a banned identity: err %v, want ErrBanned", err)
	}
	if _, err := CodeLogin("dev-bn code"); !errors.Is(err, ErrBanned) {
		t.Fatalf("code login for a banned identity: err %v, want ErrBanned", err)
	}
	if err := BanPlayer(id, "again", "discord:9"); !errors.Is(err, ErrAlreadyBanned) {
		t.Fatalf("second ban: err %v, want ErrAlreadyBanned", err)
	}

	if err := ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	if !IdentityBanned("dev-bn") || resolves(alias) != nil {
		t.Fatal("the ban or the revoked token did not survive a reload")
	}
	rows, err := AuditRows(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Action == "ban" && r.Actor == "discord:9" && r.Target == fmt.Sprintf("player:%d", id) && strings.Contains(r.Detail, `"reason":"spam"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ban audit row among %+v", rows)
	}
}

// TestBanSurvivesResetIdentity: Delete Avatar in identity mode (ResetIdentityLocked) keeps the ban and still refuses the
// identity's next code.
func TestBanSurvivesResetIdentity(t *testing.T) {
	attachTestDB(t)
	acc, id := staffAccount(t, "dev-rs", "1005", "Reset", 0)
	if err := BanPlayer(id, "x", "discord:9"); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	_, err := ResetIdentityLocked(acc, []LedgerLine{{Aid: "1005", Reason: "account deleted"}})
	kept := acc.Ban != nil && acc.Aid == "0"
	fresh := acc.AccessToken
	AccountsMu.Unlock()
	if err != nil || !kept {
		t.Fatalf("reset: err %v, ban kept and aid 0 %v", err, kept)
	}
	if resolves(fresh) != nil {
		t.Fatal("the fresh own token of a banned identity resolves")
	}
	if !IdentityBanned("dev-rs") {
		t.Fatal("the reset dropped the ban")
	}
	if _, err := CodeLogin("dev-rs code"); !errors.Is(err, ErrBanned) {
		t.Fatalf("code login after the reset: err %v, want ErrBanned", err)
	}
}

// TestUnbanRestoresSignIn: an unban lifts the refusal; the identity signs in again through a new code, and the account is the
// same record (its aid is kept).
func TestUnbanRestoresSignIn(t *testing.T) {
	attachTestDB(t)
	acc, id := staffAccount(t, "dev-ub", "1006", "Unban", 0)
	if err := BanPlayer(id, "x", "discord:9"); err != nil {
		t.Fatal(err)
	}
	if err := UnbanPlayer(id, "discord:9"); err != nil {
		t.Fatal(err)
	}
	if err := UnbanPlayer(id, "discord:9"); !errors.Is(err, ErrNotBanned) {
		t.Fatalf("second unban: err %v, want ErrNotBanned", err)
	}
	aid, _, err := MintIdentityCode("dev-ub", CodeHash("after unban"), true)
	if err != nil || aid != "1006" {
		t.Fatalf("mint after unban: aid %q err %v", aid, err)
	}
	token, err := CodeLogin("after unban")
	if err != nil || token == "" || resolves(token) != acc {
		t.Fatalf("code login after unban: err %v", err)
	}
	AccountsMu.Lock()
	key := acc.SessionKey
	AccountsMu.Unlock()
	if key == "" {
		t.Fatal("the session key is empty after an unban")
	}
}

// TestGuestLoginRefusesBannedAccount: in lab-guest a banned device account gets no token and no new account for its code.
func TestGuestLoginRefusesBannedAccount(t *testing.T) {
	attachTestDB(t)
	AccountsMu.Lock()
	acc, err := newAccountLocked("device-1234")
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	id := accountRows[acc].id
	AccountsMu.Unlock()
	if err := BanPlayer(id, "x", "discord:9"); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	before := AccountCountLocked()
	AccountsMu.Unlock()
	if _, err := GuestLogin("device-1234"); !errors.Is(err, ErrBanned) {
		t.Fatalf("guest login of a banned device: err %v, want ErrBanned", err)
	}
	AccountsMu.Lock()
	after := AccountCountLocked()
	AccountsMu.Unlock()
	if after != before {
		t.Fatalf("the refused guest login changed the account count %d -> %d", before, after)
	}
}

// TestFindPlayersAndCap: a search matches the avatar id exactly, the identity exactly and the name in any case, returns at
// most 50 rows and says when there are more; an empty search finds nothing.
func TestFindPlayersAndCap(t *testing.T) {
	attachTestDB(t)
	staffAccount(t, "dev-f1", "2001", "Alice Ocean", 0)
	staffAccount(t, "dev-f2", "2002", "bob", 0)
	if rows, more := FindPlayers("2001"); len(rows) != 1 || rows[0].Aid != "2001" || more {
		t.Fatalf("by aid: %+v more %v", rows, more)
	}
	if rows, _ := FindPlayers("ALICE"); len(rows) != 1 || rows[0].Name != "Alice Ocean" || rows[0].ID == 0 {
		t.Fatalf("by name: %+v", rows)
	}
	if rows, _ := FindPlayers("dev-f2"); len(rows) != 1 || rows[0].Aid != "2002" {
		t.Fatalf("by identity: %+v", rows)
	}
	if rows, _ := FindPlayers("dev-f"); len(rows) != 0 {
		t.Fatalf("a part of an identity matched: %+v", rows)
	}
	if rows, more := FindPlayers("  "); rows != nil || more {
		t.Fatalf("an empty search: %+v", rows)
	}
	for i := 0; i < 60; i++ {
		staffAccount(t, fmt.Sprintf("dev-c%d", i), fmt.Sprintf("%d", 3000+i), "Crowd", 0)
	}
	rows, more := FindPlayers("crowd")
	if len(rows) != 50 || !more {
		t.Fatalf("60 matches: %d rows, more %v; want 50 and more", len(rows), more)
	}
}

// TestStaffActionsRefuseUnknownPlayers: an id with no account (or 0) is ErrNoPlayer for every staff call.
func TestStaffActionsRefuseUnknownPlayers(t *testing.T) {
	attachTestDB(t)
	for _, id := range []int64{0, 99999} {
		if _, ok := PlayerByID(id); ok {
			t.Errorf("PlayerByID(%d) found an account", id)
		}
		if _, err := PlayerLedger(id, 20); !errors.Is(err, ErrNoPlayer) {
			t.Errorf("PlayerLedger(%d): err %v", id, err)
		}
		if err := BanPlayer(id, "x", "discord:1"); !errors.Is(err, ErrNoPlayer) {
			t.Errorf("BanPlayer(%d): err %v", id, err)
		}
		if err := UnbanPlayer(id, "discord:1"); !errors.Is(err, ErrNoPlayer) {
			t.Errorf("UnbanPlayer(%d): err %v", id, err)
		}
	}
}
