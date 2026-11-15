package store

import (
	"database/sql"
	"time"
)

// Token use and pruning (PLAN 8.6b part 4). Every lookup of a token notes its last use in memory; FlushTokenUse writes
// the pending times to tokens.last_used in one transaction (the maintenance timer runs it every few minutes), so a
// request is never a database write. PruneIdleTokens removes the alias tokens that went unused for the retention: each
// sign-in and checkSession mints one, and a device that has not come back for that long signs in again through
// Discord. Own tokens (one per account, its access token) are kept.

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

// PruneIdleTokens removes the alias tokens unused for days days or more (never the own tokens) from the database and
// from memory, and reports how many it removed. days below 1 prunes nothing.
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
	var hashes []string
	err := d.Update(func(tx *sql.Tx) error {
		rows, err := tx.Query(`DELETE FROM tokens WHERE kind = 'alias' AND (CASE WHEN last_used = '' THEN created ELSE last_used END) < ? RETURNING hash`, cutoff)
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
