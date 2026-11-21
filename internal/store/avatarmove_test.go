package store

import (
	"testing"

	"cherry/internal/config"
)

// moveSetup makes the identities dev-a (aid 1234, with a token and a sign-in code) and dev-b (an empty row, when emptyB is set),
// and returns the row ids. The sign-in code of dev-a is minted, so CodeLogin works for it.
func moveSetup(t *testing.T, emptyB bool) (src, tgt int64, token string) {
	t.Helper()
	activeMode(t, config.AuthDevFake)
	acc, id := staffAccount(t, "dev-a", "1234", "Ayame", 40)
	AccountsMu.Lock()
	token = acc.AccessToken
	_, err := MintAliasLocked(acc)
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if emptyB {
		if _, _, err := MintIdentityCode("dev-b", CodeHash("dev-b code"), true); err != nil {
			t.Fatal(err)
		}
		AccountsMu.Lock()
		tgt = accountRows[IdentityAccountLocked("dev-b")].id
		AccountsMu.Unlock()
	}
	return id, tgt, token
}

// TestMoveAvatarToEmptyRow: the avatar goes to an identity whose row is empty. The empty row is removed in the same save, the
// source identity has no row, every token of the avatar stops resolving, and the target's next sign-in gets the avatar.
func TestMoveAvatarToEmptyRow(t *testing.T) {
	d := attachTestDB(t)
	src, emptyRow, token := moveSetup(t, true)
	if err := MoveAvatar(src, "dev-b", "discord:owner"); err != nil {
		t.Fatalf("move: %v", err)
	}
	AccountsMu.Lock()
	moved := IdentityAccountLocked("dev-b")
	gone := IdentityAccountLocked("dev-a") == nil
	AccountsMu.Unlock()
	if moved == nil || moved.Aid != "1234" || moved.Gems != 40 || !gone {
		t.Fatalf("after the move: target %+v, source row left: %v", moved, !gone)
	}
	if err := d.View(func(q Querier) error {
		var n int
		if err := q.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id = ?`, emptyRow).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("the empty row %d is still in the database", emptyRow)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if resolves(token) != nil {
		t.Fatal("a token of the moved avatar still resolves")
	}
	var rows int
	if err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT COUNT(*) FROM accounts WHERE identity IN ('dev-a', 'dev-b')`).Scan(&rows)
	}); err != nil || rows != 1 {
		t.Fatalf("accounts rows with the two identities = %d (err %v), want the one moved row", rows, err)
	}
	if got := auditRowsOf(t, "avatar-move"); len(got) != 1 {
		t.Fatalf("avatar-move audit rows = %d, want 1", len(got))
	}
	// The target's next sign-in: exchange mints a code for the identity, the code logs in and reaches the avatar.
	code := "dev-b code 2"
	if _, _, err := MintIdentityCode("dev-b", CodeHash(code), false); err != nil {
		t.Fatalf("exchange for the target: %v", err)
	}
	if _, err := CodeLogin(code); err != nil {
		t.Fatalf("the target's code login: %v", err)
	}
	AccountsMu.Lock()
	aid := IdentityAccountLocked("dev-b").Aid
	AccountsMu.Unlock()
	if aid != "1234" {
		t.Fatalf("the target's account aid = %s, want 1234", aid)
	}
}

// TestMoveAvatarToIdentityWithoutRow: no account exists for the target, so the source row just takes the identity.
func TestMoveAvatarToIdentityWithoutRow(t *testing.T) {
	attachTestDB(t)
	src, _, _ := moveSetup(t, false)
	if err := MoveAvatar(src, "dev-c", "discord:owner"); err != nil {
		t.Fatalf("move: %v", err)
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	if c := IdentityAccountLocked("dev-c"); c == nil || c.Aid != "1234" || accountRows[c].id != src {
		t.Fatalf("the moved row = %+v, want the source row under dev-c", c)
	}
	if IdentityAccountLocked("dev-a") != nil {
		t.Fatal("the source identity still has a row")
	}
}

// TestMoveAvatarRefusals: every refusal changes nothing (the identities, the tokens and the audit rows stay as they were).
func TestMoveAvatarRefusals(t *testing.T) {
	attachTestDB(t)
	src, _, token := moveSetup(t, false)
	if _, _, err := MintIdentityCode("dev-busy", CodeHash("busy"), true); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	busy := IdentityAccountLocked("dev-busy")
	busy.Aid, busy.Name = "2222", "Busy"
	if err := SaveLocked(nil, busy); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Unlock()
	if _, _, err := MintIdentityCode("dev-banned", CodeHash("banned"), true); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	banned := IdentityAccountLocked("dev-banned")
	banned.Ban = &Ban{Reason: "test", By: "dev-owner", At: "2026-11-21T00:00:00Z"}
	if err := SaveLocked(nil, banned); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Unlock()

	refuse := map[string]struct {
		id int64
		to string
	}{
		"target has an avatar": {src, "dev-busy"},
		"target banned":        {src, "dev-banned"},
		"same identity":        {src, "dev-a"},
		"not an identity":      {src, "discord:"},
		"the account is gone":  {999999, "dev-z"},
	}
	for name, c := range refuse {
		err := MoveAvatar(c.id, c.to, "dev-owner")
		if err == nil || (name != "the account is gone" && !IsRefused(err)) {
			t.Fatalf("%s: err = %v, want a refusal", name, err)
		}
	}
	if resolves(token) == nil {
		t.Fatal("a refused move ended the source's login")
	}
	AccountsMu.Lock()
	src2 := IdentityAccountLocked("dev-a")
	AccountsMu.Unlock()
	if src2 == nil || src2.Aid != "1234" {
		t.Fatal("a refused move changed the source row")
	}
	if got := auditRowsOf(t, "avatar-move"); len(got) != 0 {
		t.Fatalf("refused moves wrote %d audit rows", len(got))
	}

	// A banned source, and a source with no avatar.
	AccountsMu.Lock()
	src2.Ban = &Ban{Reason: "test", By: "dev-owner", At: "2026-11-21T00:00:00Z"}
	if err := SaveLocked(nil, src2); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Unlock()
	if err := MoveAvatar(src, "dev-c", "dev-owner"); !IsRefused(err) {
		t.Fatalf("banned source: err = %v, want a refusal", err)
	}
	AccountsMu.Lock()
	src2.Ban = nil
	src2.Aid = "0"
	if err := SaveLocked(nil, src2); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Unlock()
	if err := MoveAvatar(src, "dev-c", "dev-owner"); !IsRefused(err) {
		t.Fatalf("no avatar: err = %v, want a refusal", err)
	}
}

// TestMoveAvatarRefusedInLabMode: the lab keeps its one account; a move there is refused.
func TestMoveAvatarRefusedInLabMode(t *testing.T) {
	attachTestDB(t)
	activeMode(t, config.AuthLabGuest)
	_, id := staffAccount(t, "lab-x", "1001", "Lab", 0)

	if err := MoveAvatar(id, "dev-x", "dev-owner"); !IsRefused(err) {
		t.Fatalf("lab move: err = %v, want a refusal", err)
	}
}
