// Package testutil holds test helpers that only need the store, so tests in any
// layer (and the web-level integration tests) can share them.
package testutil

import (
	"testing"

	"cherry/internal/store"
)

// InstallAccounts replaces the account table with entries for the test (every
// entry becomes a lab account, as the showcase seeding expects) and restores it.
func InstallAccounts(t *testing.T, entries map[string]*store.Account) {
	t.Helper()
	store.AccountsMu.Lock()
	oldAccounts, oldLatest, oldNext, oldPath := store.Accounts, store.LatestAcc, store.NextAvatarID, store.AccountStorePath
	store.Accounts = entries
	store.LatestAcc = nil
	for _, acc := range entries {
		store.LatestAcc = acc
		SetLab(t, acc.Aid, true) // test accounts default to lab (showcase seeding)
	}
	store.AccountStorePath = ""
	store.AccountsMu.Unlock()
	t.Cleanup(func() {
		store.AccountsMu.Lock()
		store.Accounts, store.LatestAcc, store.NextAvatarID, store.AccountStorePath = oldAccounts, oldLatest, oldNext, oldPath
		store.AccountsMu.Unlock()
	})
}

// SetLab marks or unmarks aid as a lab account for the test.
func SetLab(t *testing.T, aid string, lab bool) {
	t.Helper()
	old, had := store.LabAids[aid]
	store.LabAids[aid] = lab
	t.Cleanup(func() {
		if had {
			store.LabAids[aid] = old
		} else {
			delete(store.LabAids, aid)
		}
	})
}
