package store

import (
	"net/http/httptest"
	"slices"
	"testing"

	"cherry/internal/config"
)

// activeMode makes mode the activated configuration for one test; the identity-mode switches read it.
func activeMode(t *testing.T, mode string) {
	t.Helper()
	config.Activate(config.Config{AuthMode: mode})
	t.Cleanup(func() { config.Activate(config.Config{}) })
}

// TestNoNewestAccountFallbackInIdentityMode: a request without a token gets no account in dev-fake; lab-guest keeps the
// fallback to the newest account.
func TestNoNewestAccountFallbackInIdentityMode(t *testing.T) {
	attachTestDB(t)
	if _, _, err := MintIdentityCode("dev-x", CodeHash("code-x"), true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v4/createSession", nil)
	activeMode(t, config.AuthDevFake)
	if got := CurrentAccount(req); got != nil {
		t.Fatalf("dev-fake: a request without a token got the account of %q", got.Identity)
	}
	activeMode(t, config.AuthLabGuest)
	if got := CurrentAccount(req); got == nil || got.Identity != "dev-x" {
		t.Fatalf("lab-guest: the newest account is no longer the fallback: %+v", got)
	}
}

// TestLabGrantsOnlyInLabGuest: 1001 is a lab account in lab-guest only; in identity mode its inventory is its own and the
// showcase grants do not apply.
func TestLabGrantsOnlyInLabGuest(t *testing.T) {
	grants := CuratedGrantCodes()
	if len(grants) == 0 {
		t.Skip("no lab grant codes in profiles.json")
	}
	acc := &Account{Aid: "1001", InventoryCodes: []string{"CUON00164"}}
	activeMode(t, config.AuthLabGuest)
	if !IsLab("1001") || len(AccountOwnedCodes(acc)) <= 1 {
		t.Fatalf("lab-guest: 1001 is not a lab account or owns no grants: %v", AccountOwnedCodes(acc))
	}
	activeMode(t, config.AuthDevFake)
	if IsLab("1001") {
		t.Fatal("dev-fake: 1001 is still a lab account")
	}
	if owned := AccountOwnedCodes(acc); len(owned) != 1 || owned[0] != "CUON00164" {
		t.Fatalf("dev-fake: owned %v, want the inventory only", owned)
	}
}

// tokenRows returns the hashes of the token rows of acc's account, sorted.
func tokenRows(t *testing.T, d *DB, acc *Account) []string {
	t.Helper()
	AccountsMu.Lock()
	id := accountRows[acc].id
	AccountsMu.Unlock()
	var hashes []string
	err := d.View(func(q Querier) error {
		rows, err := q.Query(`SELECT hash FROM tokens WHERE account_id = ? ORDER BY hash`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				return err
			}
			hashes = append(hashes, h)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return hashes
}

// resolves is the account a clear token reaches now, or nil.
func resolves(token string) *Account {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	return AccountByTokenLocked(token)
}

// TestResetIdentityRevokesEveryToken: Delete Avatar in identity mode keeps the identity, its codes, Welcomed and the
// boss book and butler free hire, drops the game data, and ends every token of the account (own and aliases) in memory
// and in the database, in one save. The undo restores the tokens and the avatar. The same identity signs in again through
// a newly minted code, to the same record, before and after a reload.
func TestResetIdentityRevokesEveryToken(t *testing.T) {
	d := attachTestDB(t)
	activeMode(t, config.AuthDevFake)
	if _, _, err := MintIdentityCode("dev-y", CodeHash("code-y"), true); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc := IdentityAccountLocked("dev-y")
	acc.Aid, acc.Name, acc.Gems, acc.Welcomed = "1234", "Yuki", 300, true
	acc.InventoryCodes = []string{"CUON00164"}
	acc.Butlers = &ButlerBook{FreeUsed: true, Leader: 3}
	acc.BossBook = map[int64]bool{4901: true}
	own, session := acc.AccessToken, acc.SessionKey
	alias, err := MintAliasLocked(acc)
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	before := []string{CodeHash(own), CodeHash(alias)}
	slices.Sort(before)
	if got := tokenRows(t, d, acc); !slices.Equal(got, before) {
		t.Fatalf("before the reset: token rows %d, want the own token and the alias", len(got))
	}

	reset := func() func() error {
		AccountsMu.Lock()
		defer AccountsMu.Unlock()
		undo, err := ResetIdentityLocked(acc, []LedgerLine{{Aid: "1234", Reason: "account deleted"}})
		if err != nil {
			t.Fatal(err)
		}
		return undo
	}
	check := func(when string) (newOwn string) {
		t.Helper()
		AccountsMu.Lock()
		got := IdentityAccountLocked("dev-y")
		newOwn = acc.AccessToken
		AccountsMu.Unlock()
		if got == nil || got.Aid != "0" || got.Gems != 0 || len(got.InventoryCodes) != 0 || got.Name != "" {
			t.Fatalf("%s: the reset identity holds game data: %+v", when, got)
		}
		if !got.Welcomed || !got.BossBook[4901] || got.Butlers == nil || !got.Butlers.FreeUsed || got.Butlers.Leader != 0 {
			t.Fatalf("%s: once-only grants lost: welcomed=%v boss=%v butlers=%+v", when, got.Welcomed, got.BossBook, got.Butlers)
		}
		if len(got.CodeHashes) != 1 || got.SessionKey == session {
			t.Fatalf("%s: identity or session wrong (codes %d, session rotated %v)", when, len(got.CodeHashes), got.SessionKey != session)
		}
		if resolves(own) != nil || resolves(alias) != nil {
			t.Fatalf("%s: an old token still resolves to an account", when)
		}
		if newOwn == "" || newOwn == own || resolves(newOwn) != got {
			t.Fatalf("%s: the fresh own token does not resolve to the identity", when)
		}
		if rows := tokenRows(t, d, got); !slices.Equal(rows, []string{CodeHash(newOwn)}) {
			t.Fatalf("%s: token rows %v, want only the fresh own token", when, rows)
		}
		return newOwn
	}

	undo := reset()
	check("after the reset")
	AccountsMu.Lock()
	if err := undo(); err != nil {
		AccountsMu.Unlock()
		t.Fatal(err)
	}
	restored := IdentityAccountLocked("dev-y")
	AccountsMu.Unlock()
	if restored == nil || restored.Aid != "1234" || restored.Gems != 300 || resolves(own) != restored || resolves(alias) != restored {
		t.Fatalf("undo did not restore the avatar and its tokens: %+v", restored)
	}
	if rows := tokenRows(t, d, restored); !slices.Equal(rows, before) {
		t.Fatalf("undo: token rows %v, want %v", rows, before)
	}

	reset()
	check("after a second reset")
	// The same identity signs in again: a newly minted code logs in to the identity's record, with a new alias.
	const again = "00000000-0000-4000-8000-00000000c0de"
	if aid, isNew, err := MintIdentityCode("dev-y", CodeHash(again), true); err != nil || aid != "0" || isNew != true {
		t.Fatalf("re-mint = %q %v %v", aid, isNew, err)
	}
	token, err := CodeLogin(again)
	if err != nil || resolves(token) == nil || resolves(token) != IdentityAccountLocked("dev-y") {
		t.Fatalf("the new sign-in does not reach the identity's record: %v", err)
	}
	if err := ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	reloaded := IdentityAccountLocked("dev-y")
	AccountsMu.Unlock()
	if reloaded == nil || resolves(token) != reloaded || resolves(own) != nil || resolves(alias) != nil {
		t.Fatal("after a reload the sign-in or the revoked tokens differ from the record")
	}
	if rows := tokenRows(t, d, reloaded); len(rows) != 2 {
		t.Fatalf("after a reload: %d token rows, want the fresh own token and the new alias", len(rows))
	}
}
