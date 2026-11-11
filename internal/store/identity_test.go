package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fileAccounts points the account table at a fresh file for one test and restores the old state after it.
func fileAccounts(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts.json")
	AccountsMu.Lock()
	oldAccounts, oldLatest, oldNext, oldPath := Accounts, LatestAcc, NextAvatarID, AccountStorePath
	AccountsMu.Unlock()
	t.Cleanup(func() {
		AccountsMu.Lock()
		Accounts, LatestAcc, NextAvatarID, AccountStorePath = oldAccounts, oldLatest, oldNext, oldPath
		AccountsMu.Unlock()
	})
	if err := LoadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOldAccountFileRoundTrips: an account file written before the identity fields existed loads, and saving it again
// writes the same bytes (the new fields are omitted when empty).
func TestOldAccountFileRoundTrips(t *testing.T) {
	old, err := os.ReadFile(filepath.Join("testdata", "accounts_before_identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	old = bytes.TrimSuffix(old, []byte("\n"))
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadAccountsFrom(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	AccountsMu.Lock()
	err = SaveAccountsLocked()
	AccountsMu.Unlock()
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, old) {
		t.Fatalf("re-saved file differs:\n got %s\nwant %s", again, old)
	}
}

// TestMintIdentityCodeCapsAndLogsIn: the first mint creates the identity's account (aid "0", new); later mints add codes
// to the same account, the eleventh drops the first; every held code logs in to that account and the dropped one does not.
func TestMintIdentityCodeCapsAndLogsIn(t *testing.T) {
	path := fileAccounts(t)
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
	accounts := make(map[*Account]bool)
	for _, a := range Accounts {
		accounts[a] = true
	}
	AccountsMu.Unlock()
	if n != maxCodeHashes || len(accounts) != 1 {
		t.Fatalf("codes = %d, accounts = %d; want %d codes on one account", n, len(accounts), maxCodeHashes)
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
		got := Accounts[token]
		AccountsMu.Unlock()
		if got == nil || got.Identity != identity {
			t.Fatalf("code %d logged in to %+v, want the identity's account", i, got)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(codes[len(codes)-1])) || bytes.Contains(raw, []byte(codes[1])) {
		t.Fatal("a uniqueCode appears in the account file; only its hash may")
	}
	if err := LoadAccountsFrom(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	AccountsMu.Lock()
	reloaded := IdentityAccountLocked(identity)
	AccountsMu.Unlock()
	if reloaded == nil || len(reloaded.CodeHashes) != maxCodeHashes {
		t.Fatalf("reloaded identity = %+v, want its codes back", reloaded)
	}
}

// TestMintFailureLeavesNothing: a save that fails creates no account and keeps the codes an account had.
func TestMintFailureLeavesNothing(t *testing.T) {
	fileAccounts(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	AccountStorePath = filepath.Join(blocker, "accounts.json") // its parent is a file, so the save cannot succeed
	AccountsMu.Unlock()
	if _, _, err := MintIdentityCode("dev-x", CodeHash("a")); err == nil {
		t.Fatal("mint with a failing save succeeded")
	}
	AccountsMu.Lock()
	n := len(Accounts)
	AccountsMu.Unlock()
	if n != 0 {
		t.Fatalf("failed mint left %d accounts", n)
	}
}

// TestDuplicateIdentityRefusesToLoad: one identity maps to one account; a file that says otherwise is refused.
func TestDuplicateIdentityRefusesToLoad(t *testing.T) {
	path := fileAccounts(t)
	if _, _, err := MintIdentityCode("dev-same", CodeHash("one")); err != nil {
		t.Fatal(err)
	}
	second := newRecord("")
	second.Identity = "dev-same"
	second.CodeHashes = nil
	AccountsMu.Lock()
	Accounts[second.AccessToken] = second
	err := SaveAccountsLocked()
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := LoadAccountsFrom(path); err == nil {
		t.Fatal("a file with two accounts for one identity loaded")
	}
}

func TestCodeLoginRefusesUnknownCode(t *testing.T) {
	fileAccounts(t)
	if _, err := CodeLogin("00000000-0000-4000-8000-000000000001"); err != ErrUnknownCode {
		t.Fatalf("err = %v, want ErrUnknownCode", err)
	}
}
