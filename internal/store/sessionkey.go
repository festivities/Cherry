package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// Session keys (PLAN 8.6b part 6). The stock client gets its sessionKey from createSession, checkSession and create/avatar,
// presents it on the :10123 session socket (sendInit) and in every gateway login. Without a secret the key is random and
// stored in clear in the account row (the lab-guest default). With auth.session-secret-file the key is derived:
//
//	key = base64.StdEncoding(HMAC-SHA256(secret, "cherry-session\x00" + salt + "\x00" + epoch))
//
// which has the shape of RandomToken(32) (44 characters, the same alphabet, one trailing '='). The row keeps the salt and
// the epoch, never the key. The salt is a random, non-secret value per account. The aid is not used for it: it is "0"
// until create/avatar (every new account would share one key) and it changes at create/avatar (the key the client holds
// would stop matching). A ban and Delete Avatar bump the epoch.
//
// Migration: a row that still holds a clear key gets a salt and epoch 0, and its clear key becomes a SHA-256 hash (the
// legacy key). The legacy key keeps working until the account presents its derived key (sendInit or a gateway login),
// and then the hash is dropped in that same login. A device that never presents the derived key keeps its legacy key:
// a failed migration never locks a player out. Then the database files are rebuilt (checkpoint, VACUUM) so the old
// clear keys do not stay in free pages or the WAL.

// sessionSecretMin is the least secret the derivation accepts.
const sessionSecretMin = 32

// sessionSecret is the server secret of the derived keys. nil means keys are random and stored in clear (lab-guest without
// auth.session-secret-file, and the tests). It is set once at start, before the accounts load.
var sessionSecret []byte

// SetSessionSecret sets the secret the derived keys use. It refuses fewer than 32 bytes.
func SetSessionSecret(secret []byte) error {
	if len(secret) < sessionSecretMin {
		return fmt.Errorf("session secret: %d bytes, need at least %d", len(secret), sessionSecretMin)
	}
	sessionSecret = secret
	return nil
}

// LoadSessionSecret reads auth.session-secret-file once (its permissions are checked by config). The error names the
// file, never its content.
func LoadSessionSecret(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("session secret: %w", err)
	}
	return SetSessionSecret(raw)
}

// deriveSessionKey is the key of salt at epoch: the HMAC in the stock client's base64 shape.
func deriveSessionKey(secret []byte, salt string, epoch int64) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("cherry-session\x00" + salt + "\x00" + strconv.FormatInt(epoch, 10)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// initSessionKey gives a new account its session key: a fresh salt at epoch 0 (derived), or a random key (clear).
func initSessionKey(acc *Account) {
	if sessionSecret == nil {
		acc.SessionKey = RandomToken(32)
		return
	}
	acc.SessionSalt, acc.SessionEpoch = RandomToken(16), 0
	acc.SessionKey = deriveSessionKey(sessionSecret, acc.SessionSalt, acc.SessionEpoch)
}

// rotateSessionKey ends the account's session key (a ban, Delete Avatar): the next epoch (derived) or a new random key
// (clear). The legacy key of a migrated account stops working with it. Caller holds AccountsMu.
func rotateSessionKey(acc *Account) {
	acc.LegacyKeyHash = ""
	if sessionSecret == nil {
		acc.SessionKey = RandomToken(32)
		return
	}
	if acc.SessionSalt == "" {
		acc.SessionSalt = RandomToken(16)
	}
	acc.SessionEpoch++
	acc.SessionKey = deriveSessionKey(sessionSecret, acc.SessionSalt, acc.SessionEpoch)
}

// sessionKeyOnDisk is the clear key a row or a backup holds: none while the keys are derived.
func sessionKeyOnDisk(acc *Account) string {
	if sessionSecret != nil {
		return ""
	}
	return acc.SessionKey
}

// HoldsSessionKeyLocked reports whether presented is acc's session key (constant time), or the legacy key of a migrated
// account that has not presented its derived key yet. The first derived-key presentation drops the legacy hash in one
// save; a failed save keeps it and the login still passes. Caller holds AccountsMu.
func HoldsSessionKeyLocked(acc *Account, presented []byte) bool {
	if acc == nil || len(presented) == 0 {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(acc.SessionKey), presented) == 1 {
		if acc.LegacyKeyHash != "" {
			prev := acc.LegacyKeyHash
			acc.LegacyKeyHash = ""
			if err := SaveLocked(nil, acc); err != nil {
				acc.LegacyKeyHash = prev
			}
		}
		return true
	}
	return acc.LegacyKeyHash != "" && subtle.ConstantTimeCompare([]byte(CodeHash(string(presented))), []byte(acc.LegacyKeyHash)) == 1
}

// prepareSessionKeys gives every loaded account its session key in memory. With a secret, a row with a salt derives its
// key; a row with a clear key and no salt is migrated (see the file comment) and returned, so its row is rewritten. Without
// a secret, a row with a salt cannot be served: the error names the setting.
func prepareSessionKeys(accs []*Account) ([]*Account, error) {
	if sessionSecret == nil {
		derived := 0
		for _, acc := range accs {
			if acc.SessionSalt != "" && acc.SessionKey == "" { // a derived row holds no clear key
				derived++
			}
		}
		if derived > 0 {
			return nil, fmt.Errorf("store: %d accounts hold derived session keys: set auth.session-secret-file to the file they were derived with", derived)
		}
		return nil, nil
	}
	var migrated []*Account
	for _, acc := range accs {
		clearKey := acc.SessionKey
		if acc.SessionSalt == "" {
			if clearKey == "" {
				return nil, errors.New("store: an account has neither a session key nor a salt")
			}
			acc.LegacyKeyHash = CodeHash(clearKey)
			acc.SessionSalt, acc.SessionEpoch = RandomToken(16), 0
		}
		acc.SessionKey = deriveSessionKey(sessionSecret, acc.SessionSalt, acc.SessionEpoch)
		if clearKey != "" {
			migrated = append(migrated, acc)
		}
	}
	return migrated, nil
}

// scrubSessionKeys writes the migrated rows, then rebuilds the database files so the old clear keys leave them: a
// checkpoint, a VACUUM (which copies only the live pages) and a checkpoint that empties the WAL. It runs when rows were
// migrated and once more per database (meta sessionKeys.scrubbed), so a failed VACUUM is retried at the next start.
// Caller holds AccountsMu.
func scrubSessionKeys(d *DB, ids map[*Account]int64, migrated []*Account) error {
	if sessionSecret == nil {
		return nil
	}
	var done int64
	if err := d.View(func(q Querier) error {
		var err error
		done, err = metaInt(q, "sessionKeys.scrubbed")
		return err
	}); err != nil {
		return err
	}
	if len(migrated) == 0 && done == 1 {
		return nil
	}
	if len(migrated) > 0 {
		if err := d.Update(func(tx *sql.Tx) error {
			for _, acc := range migrated {
				if _, err := updateAccountRowTx(tx, ids[acc], acc); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for _, stmt := range []string{"PRAGMA wal_checkpoint(TRUNCATE)", "VACUUM", "PRAGMA wal_checkpoint(TRUNCATE)"} {
		if _, err := d.write.Exec(stmt); err != nil {
			return fmt.Errorf("session keys: %s: %w", stmt, err)
		}
	}
	return d.Update(func(tx *sql.Tx) error { return setMetaTx(tx, "sessionKeys.scrubbed", "1") })
}
