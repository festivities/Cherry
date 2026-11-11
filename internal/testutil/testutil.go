// Package testutil holds test helpers that only need the store, so tests in any
// layer (and the web-level integration tests) can share them.
package testutil

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/store"
)

// InstallAccounts replaces the account table with entries for the test (every entry becomes a lab account, as the
// showcase seeding expects) and restores the previous table at cleanup. The entries are saved at once, so with a
// database attached (UseDatabase) they are written to it.
func InstallAccounts(t *testing.T, entries map[string]*store.Account) {
	t.Helper()
	restore, err := store.SwapAccountsForTest(entries)
	if err != nil {
		t.Fatalf("install accounts: %v", err)
	}
	t.Cleanup(restore)
	for _, acc := range entries {
		SetLab(t, acc.Aid, true) // test accounts default to lab (showcase seeding)
	}
}

// UseDatabase makes a fresh cherry.db in a temporary folder the store's record: the accounts in memory are written to it
// now and with every later save. Cleanup detaches and closes it. Tests that read the rows or the ledger use it; a test
// that needs a save to fail closes the returned database (the save then cannot begin its transaction).
func UseDatabase(t *testing.T) *store.DB {
	t.Helper()
	d, err := store.OpenDB(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := store.AttachDB(d); err != nil {
		d.Close()
		t.Fatalf("attach database: %v", err)
	}
	t.Cleanup(func() {
		store.DetachDB()
		d.Close()
	})
	return d
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

// FailSaves makes every save from now on fail: the test's database is closed under the store, so its transaction cannot
// begin (the rollback paths of the saves are tested this way).
func FailSaves(t *testing.T) {
	t.Helper()
	UseDatabase(t).Close()
}

// LedgerText is the attached database's ledger as the JSON lines the old ledger.jsonl held: one line per row, in order.
// Without a database it is empty.
func LedgerText(t *testing.T) ([]byte, error) {
	t.Helper()
	lines, err := store.LedgerLines()
	if err != nil {
		return nil, err
	}
	var buf []byte
	for _, l := range lines {
		b, _ := json.Marshal(l)
		buf = append(append(buf, b...), '\n')
	}
	return buf, nil
}

// WriteLedgerJSON replaces the attached database's ledger with the given ledger lines (JSON, one per string).
func WriteLedgerJSON(t *testing.T, raw []string) {
	t.Helper()
	var lines []store.LedgerLine
	for _, r := range raw {
		var l store.LedgerLine
		if err := json.Unmarshal([]byte(r), &l); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, l)
	}
	d := store.AttachedDB()
	if d == nil {
		t.Fatal("no database attached")
	}
	if err := d.Update(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM ledger`); err != nil {
			return err
		}
		return store.AppendLedgerTx(tx, lines)
	}); err != nil {
		t.Fatal(err)
	}
}

// AccountRowText is the stored record of the account with aid in the attached database: the JSON the old accounts.json
// record held.
func AccountRowText(t *testing.T, aid string) []byte {
	t.Helper()
	d := store.AttachedDB()
	if d == nil {
		t.Fatal("no database attached")
	}
	var data string
	if err := d.View(func(q store.Querier) error {
		return q.QueryRow(`SELECT data FROM accounts WHERE aid = ?`, aid).Scan(&data)
	}); err != nil {
		t.Fatal(err)
	}
	return []byte(data)
}

// ClearAccounts empties the in-memory accounts without touching the database, for a test that reloads them from it.
// Cleanup restores the previous state.
func ClearAccounts(t *testing.T) {
	t.Helper()
	t.Cleanup(store.ClearMemoryForTest())
}

// UseDatabaseAt is UseDatabase with the database in dir, for a test whose other files live there.
func UseDatabaseAt(t *testing.T, dir string) *store.DB {
	t.Helper()
	d, err := store.OpenDB(filepath.Join(dir, store.DBFileName))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := store.AttachDB(d); err != nil {
		d.Close()
		t.Fatalf("attach database: %v", err)
	}
	t.Cleanup(func() {
		store.DetachDB()
		d.Close()
	})
	return d
}
