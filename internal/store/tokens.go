package store

import (
	"database/sql"
	"math"
	"time"

	"cherry/internal/config"
)

// Token use and pruning (PLAN 8.6b part 4). Every lookup of a token notes its last use in memory; FlushTokenUse writes
// the pending times to tokens.last_used in one transaction (the maintenance timer runs it every few minutes), so a
// request is never a database write. PruneIdleTokens removes the alias tokens that went unused for the retention: each
// sign-in and checkSession mints one, and a device that has not come back for that long signs in again through
// Discord. In identity modes it also removes the superseded aliases: the client keeps only the newest AV_AUTH and one login
// per account ends every other device's tokens, so beyond an account's aliasKeep newest, an alias not seen for aliasGrace is
// never presented again (2026-11-25: ~15k aliases a day, 13 an account on average, up to 114). Own tokens (one per account,
// its access token) are kept.

const (
	aliasKeep  = 5              // an account's newest aliases, always kept: room for a few unsaved rotations
	aliasGrace = 24 * time.Hour // and any alias seen this recently
)

// tokenUsed is the last use of each token, by hash, that the database does not hold yet. Guarded by AccountsMu.
var tokenUsed = map[string]time.Time{}

// AidForToken returns the aid of the account that token resolves to, for the per-account request limit: "" for no token,
// an unknown or banned token, or an account without an avatar yet (aid "0", which all share one limit otherwise).
func AidForToken(token string) string {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := AccountByTokenLocked(token)
	if acc == nil || acc.Aid == "0" {
		return ""
	}
	return acc.Aid
}

// FlushTokenUse writes the pending last-use times in one transaction.
func FlushTokenUse() error {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	return flushTokenUseLocked()
}

// flushTokenUseLocked is FlushTokenUse for a caller that holds AccountsMu. Nothing is pending without a database.
func flushTokenUseLocked() error {
	d := attachedDB()
	if d == nil || len(tokenUsed) == 0 {
		return nil
	}
	err := d.Update(func(tx *sql.Tx) error {
		for h, at := range tokenUsed {
			if _, err := tx.Exec(`UPDATE tokens SET last_used = ? WHERE hash = ?`, at.UTC().Format(time.RFC3339Nano), h); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		clear(tokenUsed)
	}
	return err
}

// PruneIdleTokens removes the alias tokens unused for days days or more and, in identity modes, the superseded ones (never
// the own tokens) from the database and from memory, and reports how many it removed. days below 1 prunes nothing.
func PruneIdleTokens(now time.Time, days int) (int, error) {
	if days < 1 {
		return 0, nil
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	d := attachedDB()
	if d == nil {
		return 0, ErrNoDatabase
	}
	if err := flushTokenUseLocked(); err != nil {
		return 0, err
	}
	cutoff := now.AddDate(0, 0, -days).UTC().Format(time.RFC3339Nano)
	keep := math.MaxInt32 // lab-guest: several devices may share an account, so only the idle rule
	if config.IdentityMode() {
		keep = aliasKeep
	}
	grace := now.Add(-aliasGrace).UTC().Format(time.RFC3339Nano)
	var hashes []string
	err := d.Update(func(tx *sql.Tx) error {
		rows, err := tx.Query(`DELETE FROM tokens WHERE hash IN (
			SELECT hash FROM (
				SELECT hash, CASE WHEN last_used = '' THEN created ELSE last_used END AS seen,
					ROW_NUMBER() OVER (PARTITION BY account_id ORDER BY created DESC, rowid DESC) AS rn
				FROM tokens WHERE kind = 'alias')
			WHERE seen < ? OR (rn > ? AND seen < ?))
			RETURNING hash`, cutoff, keep, grace)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				return err
			}
			hashes = append(hashes, h)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	for _, h := range hashes {
		dropTokenLocked(h)
		delete(tokenUsed, h)
	}
	return len(hashes), nil
}
