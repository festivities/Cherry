package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// attachTestDB makes a fresh database the store's record for one test, with no accounts; cleanup detaches it and puts the
// previous account table back.
func attachTestDB(t *testing.T) *DB {
	t.Helper()
	restore, err := SwapAccountsForTest(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	d, err := OpenDB(filepath.Join(t.TempDir(), DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := AttachDB(d); err != nil {
		d.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		DetachDB()
		d.Close()
	})
	return d
}

// rowText is every stored account, guest hash and token hash of d as one string, to check what the database holds.
func rowText(t *testing.T, d *DB) string {
	t.Helper()
	var b strings.Builder
	err := d.View(func(q Querier) error {
		for _, query := range []string{`SELECT data || guest_code_hash FROM accounts`, `SELECT hash FROM tokens`} {
			rows, err := q.Query(query)
			if err != nil {
				return err
			}
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					rows.Close()
					return err
				}
				b.WriteString(s + "\n")
			}
			rows.Close()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestOldAccountFileImports: an account file written before the identity fields existed imports; the importer checks the
// database against its input, so the identity fields come back as they were.
func TestOldAccountFileImports(t *testing.T) {
	old, err := os.ReadFile(filepath.Join("testdata", "accounts_before_identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), bytes.TrimSuffix(old, []byte("\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDB(filepath.Join(t.TempDir(), DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := ImportDir(d, dir); err != nil {
		t.Fatalf("import: %v", err)
	}
}

// TestMintIdentityCodeCapsAndLogsIn: the first mint creates the identity's account (aid "0", new); later mints add codes
// to the same account, the eleventh drops the first; every held code logs in to that account and the dropped one does not.
func TestMintIdentityCodeCapsAndLogsIn(t *testing.T) {
	d := attachTestDB(t)
	const identity = "discord:1"
	codes := make([]string, maxCodeHashes+1)
	for i := range codes {
		codes[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		aid, isNew, err := MintIdentityCode(identity, CodeHash(codes[i]), true)
		if err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
		if aid != "0" || !isNew { // no avatar yet on any mint, so new stays true
			t.Fatalf("mint %d: aid %q new %v, want \"0\" and new", i, aid, isNew)
		}
	}
	AccountsMu.Lock()
	acc := IdentityAccountLocked(identity)
	n := len(acc.CodeHashes)
	count := AccountCountLocked()
	AccountsMu.Unlock()
	if n != maxCodeHashes || count != 1 {
		t.Fatalf("codes = %d, accounts = %d; want %d codes on one account", n, count, maxCodeHashes)
	}
	if _, err := CodeLogin(codes[0]); err != ErrUnknownCode {
		t.Fatalf("dropped code: err = %v, want ErrUnknownCode", err)
	}
	// Any kept code signs in; the sign-in then keeps only its own code (TestSignInEndsTheOtherDevicesLogin).
	last := codes[len(codes)-1]
	token, err := CodeLogin(last)
	if err != nil {
		t.Fatalf("newest code: %v", err)
	}
	AccountsMu.Lock()
	got := AccountByTokenLocked(token)
	AccountsMu.Unlock()
	if got == nil || got.Identity != identity {
		t.Fatalf("newest code logged in to %+v, want the identity's account", got)
	}
	if _, err := CodeLogin(codes[1]); err != ErrUnknownCode {
		t.Fatalf("an older code after the sign-in: err = %v, want ErrUnknownCode", err)
	}
	if stored := rowText(t, d); strings.Contains(stored, codes[len(codes)-1]) || strings.Contains(stored, codes[1]) {
		t.Fatal("a uniqueCode appears in the database; only its hash may")
	}
	if err := ReloadAccounts(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	AccountsMu.Lock()
	reloaded := IdentityAccountLocked(identity)
	AccountsMu.Unlock()
	if reloaded == nil || !slices.Equal(reloaded.CodeHashes, []string{CodeHash(last)}) {
		t.Fatalf("reloaded identity = %+v, want its codes back", reloaded)
	}
}

// TestMintFailureLeavesNothing: a save that fails creates no account.
func TestMintFailureLeavesNothing(t *testing.T) {
	d := attachTestDB(t)
	if err := d.Close(); err != nil { // every save from now on fails: the transaction cannot begin
		t.Fatal(err)
	}
	if _, _, err := MintIdentityCode("dev-x", CodeHash("a"), true); err == nil {
		t.Fatal("mint with a failing save succeeded")
	}
	AccountsMu.Lock()
	n := AccountCountLocked()
	AccountsMu.Unlock()
	if n != 0 {
		t.Fatalf("failed mint left %d accounts", n)
	}
}

// TestDuplicateIdentityIsRefused: one identity maps to one account; the database refuses a second row for it, and memory
// keeps the one account.
func TestDuplicateIdentityIsRefused(t *testing.T) {
	attachTestDB(t)
	if _, _, err := MintIdentityCode("dev-same", CodeHash("one"), true); err != nil {
		t.Fatal(err)
	}
	second := newRecord("")
	second.Identity = "dev-same"
	AccountsMu.Lock()
	_, err := insertAccountLocked(second)
	count := AccountCountLocked()
	AccountsMu.Unlock()
	if err == nil {
		t.Fatal("a second account for one identity was saved")
	}
	if count != 1 {
		t.Fatalf("accounts after the refused insert = %d, want 1", count)
	}
}

func TestCodeLoginRefusesUnknownCode(t *testing.T) {
	attachTestDB(t)
	if _, err := CodeLogin("00000000-0000-4000-8000-000000000001"); err != ErrUnknownCode {
		t.Fatalf("err = %v, want ErrUnknownCode", err)
	}
}

// TestSignInEndsTheOtherDevicesLogin: a sign-in on a second device (a second minted code) ends the first device's login,
// as LINE's servers did (client string 10066): its own token and aliases stop resolving, in memory and in the token rows,
// the session key rotates, the first device's code stops working, OnTakeover gets the avatar id, and the second device's
// alias works. A failed save changes nothing.
func TestSignInEndsTheOtherDevicesLogin(t *testing.T) {
	d := attachTestDB(t)
	var kicked []string
	prev := OnTakeover
	OnTakeover = func(aid string) { kicked = append(kicked, aid) }
	t.Cleanup(func() { OnTakeover = prev })
	if _, _, err := MintIdentityCode("dev-two", CodeHash("device a"), true); err != nil {
		t.Fatal(err)
	}
	first, err := CodeLogin("device a")
	if err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc := IdentityAccountLocked("dev-two")
	acc.Aid = "1201"
	later, err := MintAliasLocked(acc) // device a's createSession / checkSession alias
	session := acc.SessionKey
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := MintIdentityCode("dev-two", CodeHash("device b"), true); err != nil { // device b's Discord sign-in
		t.Fatal(err)
	}
	second, err := CodeLogin("device b")
	if err != nil {
		t.Fatal(err)
	}
	if resolves(first) != nil || resolves(later) != nil {
		t.Fatal("device a's login still resolves after device b signed in")
	}
	if resolves(second) != acc {
		t.Fatal("device b's alias does not resolve to the account")
	}
	AccountsMu.Lock()
	rotated := acc.SessionKey != session && !HoldsSessionKeyLocked(acc, []byte(session))
	codes := slices.Clone(acc.CodeHashes)
	AccountsMu.Unlock()
	if !rotated {
		t.Fatal("device a's session key still holds")
	}
	if !slices.Equal(codes, []string{CodeHash("device b")}) {
		t.Fatalf("stored codes %d, want only device b's", len(codes))
	}
	if _, err := CodeLogin("device a"); err != ErrUnknownCode {
		t.Fatalf("device a's code after device b signed in: err %v, want ErrUnknownCode", err)
	}
	if !slices.Equal(kicked, []string{"0", "1201"}) {
		t.Fatalf("OnTakeover calls = %v, want [0 1201]", kicked)
	}
	if rows := tokenRows(t, d, acc); !slices.Equal(rows, []string{CodeHash(second)}) {
		t.Fatalf("token rows %d, want only device b's alias", len(rows))
	}

	if err := d.Close(); err != nil { // every save from now on fails
		t.Fatal(err)
	}
	if _, err := CodeLogin("device b"); err == nil {
		t.Fatal("a sign-in with a failing save succeeded")
	}
	if resolves(second) != acc || len(kicked) != 2 {
		t.Fatal("a failed sign-in ended device b's login")
	}
}

// TestLogoutEndsEveryLoginAndCode: Logout ends the account's tokens and codes and rotates its session key, so only a new
// Discord sign-in (a new code) gets back in; OnRevoke gets the avatar id. An unknown token finds no account.
func TestLogoutEndsEveryLoginAndCode(t *testing.T) {
	d := attachTestDB(t)
	var closed []string
	prev := OnRevoke
	OnRevoke = func(aid string) { closed = append(closed, aid) }
	t.Cleanup(func() { OnRevoke = prev })
	if _, _, err := MintIdentityCode("dev-out", CodeHash("out code"), true); err != nil {
		t.Fatal(err)
	}
	token, err := CodeLogin("out code")
	if err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc := IdentityAccountLocked("dev-out")
	acc.Aid = "1202"
	session := acc.SessionKey
	AccountsMu.Unlock()
	if found, err := Logout(token); !found || err != nil {
		t.Fatalf("logout = %v %v", found, err)
	}
	AccountsMu.Lock()
	rotated, codes := acc.SessionKey != session, len(acc.CodeHashes)
	AccountsMu.Unlock()
	if resolves(token) != nil || !rotated || codes != 0 || len(tokenRows(t, d, acc)) != 0 {
		t.Fatalf("after logout: token resolves %v, session rotated %v, codes %d", resolves(token) != nil, rotated, codes)
	}
	if !slices.Equal(closed, []string{"1202"}) {
		t.Fatalf("OnRevoke calls = %v, want [1202]", closed)
	}
	if _, err := CodeLogin("out code"); err != ErrUnknownCode {
		t.Fatalf("the logged-out code: err %v, want ErrUnknownCode", err)
	}
	if found, err := Logout(token); found || err != nil {
		t.Fatalf("second logout = %v %v, want no account", found, err)
	}
}

// TestAvatarBirthdayBackfill: an avatar made before AvatarCreated was kept gets the time of its first ledger line, once,
// and keeps it after a reload; an unknown aid is 0.
func TestAvatarBirthdayBackfill(t *testing.T) {
	attachTestDB(t)
	if _, _, err := MintIdentityCode("dev-bd", CodeHash("bd code"), true); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc := IdentityAccountLocked("dev-bd")
	acc.Aid = "1301"
	err := SaveLocked([]LedgerLine{
		{Time: "2026-11-15T10:00:00Z", Aid: "1301", Currency: "gems", Delta: 300, Balance: 300, Reason: "welcome"},
		{Time: "2026-11-16T10:00:00Z", Aid: "1301", Currency: "gems", Delta: 30, Balance: 330, Reason: "later"},
	}, acc)
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	first, _ := time.Parse(time.RFC3339, "2026-11-15T10:00:00Z")
	want := first.UnixMilli()
	if got := AvatarBirthday("1301"); got != want {
		t.Fatalf("backfilled birthday = %d, want %d", got, want)
	}
	if err := ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	saved := IdentityAccountLocked("dev-bd").AvatarCreated
	AccountsMu.Unlock()
	if saved != want || AvatarBirthday("9999") != 0 {
		t.Fatalf("after reload %d, unknown aid %d", saved, AvatarBirthday("9999"))
	}
}
