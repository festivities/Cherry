package store

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	testSessionSecret  = []byte("0123456789abcdef0123456789abcdef")
	otherSessionSecret = []byte("fedcba9876543210fedcba9876543210")
	stockKeyShape      = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)
)

// useSessionSecret turns derived keys on for one test and off again when it ends.
func useSessionSecret(t *testing.T, secret []byte) {
	t.Helper()
	if err := SetSessionSecret(secret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sessionSecret = nil })
}

// fileHas reports whether the database file or its WAL holds needle in its bytes.
func fileHas(t *testing.T, path, needle string) bool {
	t.Helper()
	for _, name := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(name)
		if err != nil {
			continue // no WAL after a clean close
		}
		if bytes.Contains(data, []byte(needle)) {
			return true
		}
	}
	return false
}

// TestDerivedKeyHasTheStockShape: the derived key is the shape RandomToken(32) gives today (44 characters of standard
// base64, one trailing '='), the shape the stock client has accepted since the first login.
func TestDerivedKeyHasTheStockShape(t *testing.T) {
	key := deriveSessionKey(testSessionSecret, RandomToken(16), 0)
	if !stockKeyShape.MatchString(key) || len(key) != 44 || !stockKeyShape.MatchString(RandomToken(32)) {
		t.Fatalf("derived key %q is not the stock shape", key)
	}
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 32 {
		t.Fatalf("derived key does not decode to 32 bytes: %v", err)
	}
}

// TestDerivedKeyIsStableAndKeyedPerAccount: the same secret, salt and epoch give the same key; any change of them gives
// another one.
func TestDerivedKeyIsStableAndKeyedPerAccount(t *testing.T) {
	base := deriveSessionKey(testSessionSecret, "salt-a", 0)
	if base != deriveSessionKey(testSessionSecret, "salt-a", 0) {
		t.Fatal("the same inputs give different keys")
	}
	for name, other := range map[string]string{
		"salt":   deriveSessionKey(testSessionSecret, "salt-b", 0),
		"epoch":  deriveSessionKey(testSessionSecret, "salt-a", 1),
		"secret": deriveSessionKey(otherSessionSecret, "salt-a", 0),
	} {
		if other == base {
			t.Fatalf("a different %s gives the same key", name)
		}
	}
}

// TestDerivedKeySurvivesReloadAndEpochBump: a new account's key is the same after a reload from the database, and a bump
// of the epoch ends the old key for good (it stays ended after a reload too).
func TestDerivedKeySurvivesReloadAndEpochBump(t *testing.T) {
	useSessionSecret(t, testSessionSecret)
	attachTestDB(t)
	AccountsMu.Lock()
	acc, err := newAccountLocked("")
	var k0 string
	if err == nil {
		acc.Aid = "1001"
		err = SaveLocked(nil, acc)
		k0 = acc.SessionKey
	}
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc = AccountByAidLocked("1001")
	reloaded := acc.SessionKey
	AccountsMu.Unlock()
	if reloaded != k0 {
		t.Fatal("the key changed across a reload")
	}

	AccountsMu.Lock()
	rotateSessionKey(acc)
	err = SaveLocked(nil, acc)
	k1 := acc.SessionKey
	heldOld := HoldsSessionKeyLocked(acc, []byte(k0))
	heldNew := HoldsSessionKeyLocked(acc, []byte(k1))
	AccountsMu.Unlock()
	if err != nil || k1 == k0 || heldOld || !heldNew {
		t.Fatalf("after the bump: err %v, new key differs %v, old key held %v, new key held %v", err, k1 != k0, heldOld, heldNew)
	}
	if err := ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc = AccountByAidLocked("1001")
	ok := acc.SessionKey == k1 && !HoldsSessionKeyLocked(acc, []byte(k0)) && HoldsSessionKeyLocked(acc, []byte(k1))
	AccountsMu.Unlock()
	if !ok {
		t.Fatal("after a reload the bumped epoch does not hold")
	}
}

// TestBanAndDeleteAvatarEndTheDerivedKey: a ban and a Delete Avatar both bump the epoch, so the key the player had stops
// holding; the ban keeps its epoch across a reload.
func TestBanAndDeleteAvatarEndTheDerivedKey(t *testing.T) {
	useSessionSecret(t, testSessionSecret)
	attachTestDB(t)
	acc, id := staffAccount(t, "dev-sk", "1005", "Keyed", 0)
	AccountsMu.Lock()
	before := acc.SessionKey
	epoch := acc.SessionEpoch
	AccountsMu.Unlock()

	if err := BanPlayer(id, "spam", "discord:9"); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	heldBan := HoldsSessionKeyLocked(acc, []byte(before))
	bannedKey, bannedEpoch := acc.SessionKey, acc.SessionEpoch
	AccountsMu.Unlock()
	if heldBan || bannedKey == before || bannedEpoch != epoch+1 {
		t.Fatalf("after the ban: old key held %v, key changed %v, epoch %d (was %d)", heldBan, bannedKey != before, bannedEpoch, epoch)
	}
	if err := ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc = IdentityAccountLocked("dev-sk")
	reloadedKey := acc.SessionKey
	heldAfterReload := HoldsSessionKeyLocked(acc, []byte(before))
	AccountsMu.Unlock()
	if reloadedKey != bannedKey || heldAfterReload {
		t.Fatal("the banned key does not survive a reload")
	}

	// Delete Avatar in identity mode (ResetIdentityLocked): the epoch bumps again; the salt stays.
	AccountsMu.Lock()
	salt := acc.SessionSalt
	if _, err := ResetIdentityLocked(acc, nil); err != nil {
		AccountsMu.Unlock()
		t.Fatal(err)
	}
	resetKey, resetEpoch := acc.SessionKey, acc.SessionEpoch
	AccountsMu.Unlock()
	if resetKey == bannedKey || resetEpoch != bannedEpoch+1 || acc.SessionSalt != salt {
		t.Fatalf("after Delete Avatar: key changed %v, epoch %d, salt kept %v", resetKey != bannedKey, resetEpoch, acc.SessionSalt == salt)
	}
}

// TestWrongSecretRefusesTheStoredKeys: a restart with another secret derives other keys, so the stored account's key is
// refused.
func TestWrongSecretRefusesTheStoredKeys(t *testing.T) {
	useSessionSecret(t, testSessionSecret)
	attachTestDB(t)
	AccountsMu.Lock()
	acc, err := newAccountLocked("")
	var k string
	if err == nil {
		acc.Aid = "1001"
		err = SaveLocked(nil, acc)
		k = acc.SessionKey
	}
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	sessionSecret = otherSessionSecret
	if err := ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc = AccountByAidLocked("1001")
	held := HoldsSessionKeyLocked(acc, []byte(k))
	AccountsMu.Unlock()
	if held {
		t.Fatal("a key derived with another secret is accepted")
	}
}

// TestDerivedRowsNeedTheSecret: a database whose rows hold derived keys does not load without the secret; the error names
// the setting.
func TestDerivedRowsNeedTheSecret(t *testing.T) {
	useSessionSecret(t, testSessionSecret)
	attachTestDB(t)
	AccountsMu.Lock()
	acc, err := newAccountLocked("")
	if err == nil {
		acc.Aid = "1001"
		err = SaveLocked(nil, acc)
	}
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	sessionSecret = nil
	if err := ReloadAccounts(); err == nil || !strings.Contains(err.Error(), "auth.session-secret-file") {
		t.Fatalf("reload without the secret: err %v, want the setting named", err)
	}
}

// TestMigrationScrubsClearKeys: a database written with clear keys (the lab default) loads with the secret, its rows hold
// no clear key, the database files hold none (the rebuild), the legacy key still signs in until the account presents its
// derived key, and the first derived-key login drops the legacy hash for good.
func TestMigrationScrubsClearKeys(t *testing.T) {
	restore, err := SwapAccountsForTest(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	path := filepath.Join(t.TempDir(), DBFileName)

	// Before the secret: the key is random and stored in clear.
	d, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := AttachDB(d); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc, err := newAccountLocked("")
	var legacy string
	if err == nil {
		acc.Aid = "1001"
		err = SaveLocked(nil, acc)
		legacy = acc.SessionKey
	}
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !fileHas(t, path, legacy) {
		t.Fatal("the scan is blind: the clear key is not in the file before the migration")
	}
	DetachDB()
	d.Close()

	// With the secret: the first start migrates and scrubs.
	useSessionSecret(t, testSessionSecret)
	if err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseStore() })
	AccountsMu.Lock()
	acc = AccountByAidLocked("1001")
	derived := acc.SessionKey
	hashSet := acc.LegacyKeyHash == CodeHash(legacy)
	legacyHeld := HoldsSessionKeyLocked(acc, []byte(legacy))
	AccountsMu.Unlock()
	if derived == legacy || derived == "" || !hashSet || !legacyHeld {
		t.Fatalf("after the migration: derived differs %v, legacy hash set %v, legacy key held %v", derived != legacy, hashSet, legacyHeld)
	}
	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	if fileHas(t, path, legacy) || fileHas(t, path, derived) {
		t.Fatal("a session key is still in the database files after the migration")
	}

	// A restart keeps the legacy key valid until the derived one is presented; then the legacy key is gone for good.
	if err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc = AccountByAidLocked("1001")
	stillLegacy := HoldsSessionKeyLocked(acc, []byte(legacy))
	nowDerived := HoldsSessionKeyLocked(acc, []byte(derived))
	cleared := acc.LegacyKeyHash == ""
	AccountsMu.Unlock()
	if !stillLegacy || !nowDerived || !cleared {
		t.Fatalf("after a restart: legacy held %v, derived held %v, legacy hash dropped %v", stillLegacy, nowDerived, cleared)
	}
	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc = AccountByAidLocked("1001")
	gone := !HoldsSessionKeyLocked(acc, []byte(legacy)) && HoldsSessionKeyLocked(acc, []byte(derived))
	AccountsMu.Unlock()
	if !gone {
		t.Fatal("the legacy key still signs in after the derived key was presented")
	}
	if fileHas(t, path, legacy) {
		t.Fatal("the legacy key is in the database files again")
	}
}

// TestSessionSecretNeedsThirtyTwoBytes: a short secret, or a short secret file, is refused at start.
func TestSessionSecretNeedsThirtyTwoBytes(t *testing.T) {
	if err := SetSessionSecret(make([]byte, 31)); err == nil {
		t.Fatal("31 bytes accepted")
	}
	if sessionSecret != nil {
		t.Fatal("a refused secret was kept")
	}
	file := filepath.Join(t.TempDir(), "short.secret")
	if err := os.WriteFile(file, []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadSessionSecret(file); err == nil || !strings.Contains(err.Error(), "need at least 32") {
		t.Fatalf("short file: err %v", err)
	}
}
