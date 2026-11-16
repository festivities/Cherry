package store

import (
	"database/sql"
	"errors"
	"slices"
	"time"
)

// Reads for the staff player page (PLAN Roadmap 8, staff panel phase 5). They copy what the page shows and change nothing. A
// player is addressed by row id; the avatar id is "0" until the avatar is made, and an account with "0" has no friends, circle
// or ledger lines of its own (the ledger and the social rows are keyed by avatar id).

// PlayerTimes returns the account's creation time and the newest time one of its tokens was used (RFC 3339 UTC, "" when unknown).
// Without a database both are "": the page shows a dash.
func PlayerTimes(id int64) (created, seen string, err error) {
	d := attachedDB()
	if d == nil {
		return "", "", nil
	}
	AccountsMu.Lock()
	acc := playerLocked(id)
	AccountsMu.Unlock()
	if acc == nil {
		return "", "", ErrNoPlayer
	}
	err = d.View(func(q Querier) error {
		if err := q.QueryRow(`SELECT created FROM accounts WHERE id = ?`, id).Scan(&created); err != nil {
			return err
		}
		return q.QueryRow(`SELECT COALESCE(MAX(last_used), '') FROM tokens WHERE account_id = ?`, id).Scan(&seen)
	})
	if errors.Is(err, sql.ErrNoRows) { // an account not yet written has no row: the page shows a dash
		return "", "", nil
	}
	return created, seen, err
}

// PlayerAudit returns the newest limit audit rows whose target is exactly the player with row id, newest first. The target is
// compared whole, so player:1 does not match player:12. Without a database there are none.
func PlayerAudit(id int64, limit int) ([]AuditRow, error) {
	d := attachedDB()
	out := []AuditRow{}
	if d == nil {
		return out, nil
	}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(`SELECT id, time, actor, action, target, detail FROM audit WHERE target = ? ORDER BY id DESC LIMIT ?`,
			PlayerTarget(id), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r AuditRow
			if err := rows.Scan(&r.ID, &r.Time, &r.Actor, &r.Action, &r.Target, &r.Detail); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// PlayerLedgerSince is PlayerLedger for the lines at or after since (a zero time keeps every line): the newest limit lines,
// newest first. ErrNoPlayer for an unknown id.
func PlayerLedgerSince(id int64, since time.Time, limit int) ([]LedgerLine, error) {
	AccountsMu.Lock()
	acc := playerLocked(id)
	var aid string
	if acc != nil {
		aid = acc.Aid
	}
	d := database.Load()
	AccountsMu.Unlock()
	if acc == nil {
		return nil, ErrNoPlayer
	}
	lines := []LedgerLine{}
	if d == nil || aid == "0" {
		return lines, nil
	}
	from := ""
	if !since.IsZero() {
		from = since.UTC().Format(time.RFC3339)
	}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(`SELECT time, aid, currency, delta, balance, reason FROM ledger
			WHERE aid = ? AND (? = '' OR time >= ?) ORDER BY id DESC LIMIT ?`, aid, from, from, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l LedgerLine
			if err := rows.Scan(&l.Time, &l.Aid, &l.Currency, &l.Delta, &l.Balance, &l.Reason); err != nil {
				return err
			}
			lines = append(lines, l)
		}
		return rows.Err()
	})
	return lines, err
}

// PlayerFriends returns up to limit avatar ids of the accepted friends of aid (sorted) and how many there are in all. An aid of
// "0" has none.
func PlayerFriends(aid string, limit int) (friends []string, total int) {
	friends = []string{}
	if aid == "0" || aid == "" {
		return friends, 0
	}
	SocialMu.Lock()
	defer SocialMu.Unlock()
	for _, f := range Friendships {
		if f.State != "accepted" {
			continue
		}
		var other string
		switch aid {
		case f.A:
			other = f.B
		case f.B:
			other = f.A
		default:
			continue
		}
		total++
		if len(friends) < limit {
			friends = append(friends, other)
		}
	}
	slices.Sort(friends)
	return friends, total
}

// CircleMembership is the circle an account belongs to: the circle's id and name, and the account's role in it.
type CircleMembership struct {
	ID   int64
	Name string
	Role string
}

// PlayerCircle returns the circle aid is a member of, if any.
func PlayerCircle(aid string) (CircleMembership, bool) {
	if aid == "0" || aid == "" {
		return CircleMembership{}, false
	}
	CirclesMu.Lock()
	defer CirclesMu.Unlock()
	for _, c := range CircleState.Circles {
		for _, m := range c.Members {
			if m.Aid == aid {
				return CircleMembership{ID: c.ID, Name: c.Name, Role: m.Role}, true
			}
		}
	}
	return CircleMembership{}, false
}
