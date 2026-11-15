package store

import (
	"database/sql"
	"testing"
	"time"
)

// TestIdleAliasTokensArePrunedAndUseIsBatched: a lookup notes the token's last use in memory only; FlushTokenUse writes
// the times in one go; PruneIdleTokens removes the alias tokens unused for the retention (never fresh ones, never the own
// token), from the database and from memory.
func TestIdleAliasTokensArePrunedAndUseIsBatched(t *testing.T) {
	d, _ := openTestDB(t)
	own := RandomToken(32)
	ac := &Account{AccessToken: own, Aid: "51", Name: "Own"}
	restore, err := SwapAccountsForTest(map[string]*Account{own: ac})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	AccountsMu.Lock()
	used, idle, stale := AddAliasLocked(ac), AddAliasLocked(ac), AddAliasLocked(ac)
	AccountsMu.Unlock()
	if err := AttachDB(d); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(DetachDB)

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -40).Format(time.RFC3339Nano)
	if err := d.Update(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE tokens SET created = ? WHERE hash IN (?, ?)`, old, CodeHash(idle), CodeHash(stale)); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE tokens SET last_used = ? WHERE hash = ?`, old, CodeHash(stale))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// A lookup is memory only: the row keeps its empty last_used until the flush.
	AccountsMu.Lock()
	if AccountByTokenLocked(used) == nil {
		AccountsMu.Unlock()
		t.Fatal("the alias does not resolve")
	}
	AccountsMu.Unlock()
	lastUsed := func(hash string) string {
		var v string
		if err := d.View(func(q Querier) error {
			return q.QueryRow(`SELECT last_used FROM tokens WHERE hash = ?`, hash).Scan(&v)
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := lastUsed(CodeHash(used)); got != "" {
		t.Fatalf("a lookup wrote last_used before the flush: %q", got)
	}
	if err := FlushTokenUse(); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(CodeHash(used)); got == "" {
		t.Fatal("the flush did not write the last use")
	}

	n, err := PruneIdleTokens(now, 30)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d tokens, want 2 (the never-used and the stale alias)", n)
	}
	AccountsMu.Lock()
	gone := AccountByTokenLocked(idle) == nil && AccountByTokenLocked(stale) == nil
	kept := AccountByTokenLocked(used) != nil && AccountByTokenLocked(own) != nil
	AccountsMu.Unlock()
	if !gone || !kept {
		t.Fatalf("memory after the prune: pruned gone=%v, used and own kept=%v", gone, kept)
	}
	var rows int
	if err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT COUNT(*) FROM tokens`).Scan(&rows)
	}); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("token rows after the prune = %d, want 2 (the own and the used alias)", rows)
	}
}
