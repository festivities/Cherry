// Package testutil holds test helpers that only need the store, so tests in any
// layer (and the web-level integration tests) can share them.
package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"cherry/internal/assets"
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

// SetArchive makes the original-asset store read the folders in mounts (key prefix -> local folders, as
// assets.NewLocal) for the test, and restores the previous store at cleanup.
func SetArchive(t *testing.T, mounts map[string][]string) {
	t.Helper()
	old := assets.Archive
	assets.Archive = assets.NewLocal(mounts)
	t.Cleanup(func() { assets.Archive = old })
}

// The device backups and the APK avatar tree, the folders the uploader reads.
var (
	santiFiles   = filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/santi-backup-20260921/jp.naver.lineplay.android/files`)
	lpFiles      = filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lpbackup/jp.naver.lineplay.android/files`)
	originalsAPK = filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lineplay-original-assets`)
)

// ArchiveFound reports whether the santi device backup is on this machine (the archive tests skip without it).
func ArchiveFound() bool {
	_, err := os.Stat(filepath.Join(santiFiles, "item"))
	return err == nil
}

// SantiItems is the santi device backup's item folder.
func SantiItems() string { return filepath.Join(santiFiles, "item") }

// LPBackupItems is the lpbackup device backup's item folder: the items production R2 holds beside santi's (the Treasure
// World dress CUAH008A9 has its art only here).
func LPBackupItems() string { return filepath.Join(lpFiles, "item") }

// ArchiveMounts maps the original keys to the device backups, as production R2 holds them. The item folders are given
// (exactly those, in order, for a fixture) or, by default, santi's then lpbackup's, the order the uploader reads them
// (the first folder holding a file wins). lpbackup is left out when it is absent; every other key reads santi, then
// lpbackup.
func ArchiveMounts(items ...string) map[string][]string {
	if len(items) == 0 {
		items = []string{SantiItems()}
		if _, err := os.Stat(LPBackupItems()); err == nil {
			items = append(items, LPBackupItems())
		}
	}
	craft := filepath.Join(santiFiles, "item", "craftitem")
	return map[string][]string{
		"static/":                       {lpFiles}, // the rest of the device tree (DailyBonus), as the old in-place reads did
		"static/item/":                  items,
		"static/garden/":                {filepath.Join(santiFiles, "garden"), filepath.Join(lpFiles, "garden")},
		"static/ui/":                    {filepath.Join(santiFiles, "UIResource"), filepath.Join(lpFiles, "UIResource")},
		"static/animation/":             {filepath.Join(santiFiles, "animation"), filepath.Join(lpFiles, "animation")},
		"static/tx/":                    {filepath.Join(santiFiles, "stringtable")},
		"static/garden01/":              {filepath.Join(craft, "garden01")},
		"static/craft01/":               {filepath.Join(craft, "craft01")},
		"static/photozone/frame/":       {filepath.Join(santiFiles, "cache", "photozone", "frame")},
		"static/android-10.1.0.0-2024/": {filepath.Join(originalsAPK, "android-10.1.0.0-2024")},
		"static/android-4.0.0.0-2016/":  {filepath.Join(originalsAPK, "android-4.0.0.0-2016")},
	}
}

// InstallArchive makes the device backups the default archive of a test package: its TestMain calls it, as the item
// tests always read the archive by default. A test that needs none, or a fixture, sets its own with SetArchive.
func InstallArchive() {
	if ArchiveFound() {
		assets.Archive = assets.NewLocal(ArchiveMounts())
	}
}

// UseArchive makes the device backups the archive for the test and skips the test without them.
func UseArchive(t *testing.T) {
	t.Helper()
	if !ArchiveFound() {
		t.Skipf("device archive absent at %s", santiFiles)
	}
	SetArchive(t, ArchiveMounts())
}
