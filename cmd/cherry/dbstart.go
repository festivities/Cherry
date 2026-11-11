package main

import (
	"fmt"
	"os"
	"path/filepath"

	"cherry/internal/store"
)

// openAccounts opens <data dir>\cherry.db (created and migrated when absent) and loads its accounts, social, circle and
// Treasure World rows into memory; the database is the record of every save from then on. A database with no accounts
// beside an accounts.json is refused: the accounts were never imported, and starting empty would hide them. A store whose
// database rows are empty while its JSON file holds rows is refused too (store.CheckImported), so nobody starts with the
// social data silently gone after the upgrade. A data directory with neither starts empty.
func openAccounts(dataDir string) error {
	dbPath := filepath.Join(dataDir, store.DBFileName)
	if err := store.OpenStore(dbPath); err != nil {
		return err
	}
	store.AccountsMu.Lock()
	n := store.AccountCountLocked()
	store.AccountsMu.Unlock()
	if n == 0 {
		legacy := filepath.Join(dataDir, "accounts.json")
		if _, err := os.Stat(legacy); err == nil {
			store.CloseStore()
			return fmt.Errorf("%s holds no accounts but %s exists: run cherry-import on %s first", dbPath, legacy, dataDir)
		}
	}
	if err := store.CheckImported(dataDir); err != nil {
		store.CloseStore()
		return err
	}
	return nil
}
