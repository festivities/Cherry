package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/store"
)

// TestOpenAccountsStartsEmptyWithoutLegacyFile: a data folder with neither cherry.db nor accounts.json starts empty and
// creates the database.
func TestOpenAccountsStartsEmptyWithoutLegacyFile(t *testing.T) {
	dir := t.TempDir()
	if err := openAccounts(dir); err != nil {
		t.Fatalf("openAccounts on an empty folder: %v", err)
	}
	t.Cleanup(func() { store.CloseStore() })
	if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); err != nil {
		t.Fatalf("cherry.db not created: %v", err)
	}
	store.AccountsMu.Lock()
	n := store.AccountCountLocked()
	store.AccountsMu.Unlock()
	if n != 0 {
		t.Fatalf("accounts = %d, want 0", n)
	}
}

// TestOpenAccountsRefusesEmptyDatabaseBesideLegacyFile: accounts.json and an empty cherry.db (the lab before cherry-import)
// refuse to start, with a line that names the import, and leave no database open.
func TestOpenAccountsRefusesEmptyDatabaseBesideLegacyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.CloseStore() })
	err := openAccounts(dir)
	if err == nil || !strings.Contains(err.Error(), "cherry-import") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("openAccounts = %v, want a one-line refusal that names cherry-import", err)
	}
	if err := store.CloseStore(); err != nil {
		t.Fatalf("the refused database was left open: %v", err)
	}
}

// TestOpenAccountsLoadsDatabaseBesideLegacyFile: once the database holds accounts the legacy file is ignored.
func TestOpenAccountsLoadsDatabaseBesideLegacyFile(t *testing.T) {
	dir := t.TempDir()
	if err := store.OpenStore(filepath.Join(dir, store.DBFileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewAccount(); err != nil {
		t.Fatal(err)
	}
	if err := store.CloseStore(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.CloseStore() })
	if err := openAccounts(dir); err != nil {
		t.Fatalf("openAccounts with accounts in the database: %v", err)
	}
	store.AccountsMu.Lock()
	n := store.AccountCountLocked()
	store.AccountsMu.Unlock()
	if n != 1 {
		t.Fatalf("accounts = %d, want the 1 in the database", n)
	}
}

// TestOpenAccountsRefusesStoreWithoutRowsBesideItsFile: a database with accounts but no social rows, beside a social.json
// that holds rows (the lab right after the upgrade), refuses to start with one line that names cherry-import -stores social.
func TestOpenAccountsRefusesStoreWithoutRowsBesideItsFile(t *testing.T) {
	dir := t.TempDir()
	if err := store.OpenStore(filepath.Join(dir, store.DBFileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewAccount(); err != nil {
		t.Fatal(err)
	}
	if err := store.CloseStore(); err != nil {
		t.Fatal(err)
	}
	social := `{"version":1,"nextDiaryNo":1,"posts":[{"diaryNo":"1","avatarId":"1","nickname":"Cherry","images":[]}]}`
	if err := os.WriteFile(filepath.Join(dir, "social.json"), []byte(social), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.CloseStore() })
	err := openAccounts(dir)
	if err == nil || !strings.Contains(err.Error(), "-stores social") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("openAccounts = %v, want a one-line refusal that names -stores social", err)
	}
	if err := store.CloseStore(); err != nil {
		t.Fatalf("the refused database was left open: %v", err)
	}
}
