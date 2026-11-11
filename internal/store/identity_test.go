package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		aid, isNew, err := MintIdentityCode(identity, CodeHash(codes[i]))
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
	for i, code := range codes {
		token, err := CodeLogin(code)
		if i == 0 {
			if err != ErrUnknownCode {
				t.Fatalf("dropped code: err = %v, want ErrUnknownCode", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("code %d: %v", i, err)
		}
		AccountsMu.Lock()
		got := AccountByTokenLocked(token)
		AccountsMu.Unlock()
		if got == nil || got.Identity != identity {
			t.Fatalf("code %d logged in to %+v, want the identity's account", i, got)
		}
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
	if reloaded == nil || len(reloaded.CodeHashes) != maxCodeHashes {
		t.Fatalf("reloaded identity = %+v, want its codes back", reloaded)
	}
}

// TestMintFailureLeavesNothing: a save that fails creates no account.
func TestMintFailureLeavesNothing(t *testing.T) {
	d := attachTestDB(t)
	if err := d.Close(); err != nil { // every save from now on fails: the transaction cannot begin
		t.Fatal(err)
	}
	if _, _, err := MintIdentityCode("dev-x", CodeHash("a")); err == nil {
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
	if _, _, err := MintIdentityCode("dev-same", CodeHash("one")); err != nil {
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
