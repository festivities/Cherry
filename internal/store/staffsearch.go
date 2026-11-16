package store

import (
	"strconv"
	"strings"
)

// Global search reads (PLAN Roadmap 8, staff panel phase 5): the players and the audit rows a staff search names. Both are
// bounded by their limit; the chat log search is ChatLogSearch (chatlog.go).

// SearchPlayers returns the accounts that match q: the row id when q is a number (with or without its "#"), the avatar id exactly, or the identity or the
// name as a case-insensitive substring. At most limit rows; more reports that the search has more. An empty q finds nothing.
func SearchPlayers(q string, limit int) (rows []Player, more bool) {
	q = strings.TrimSpace(q)
	if q == "" || limit < 1 {
		return nil, false
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(q, "#"), 10, 64) // "#12" is the row id the player page shows
	needle := strings.ToLower(q)
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	rows = []Player{}
	for _, acc := range accountList {
		match := (id > 0 && accountRows[acc].id == id) || acc.Aid == q ||
			strings.Contains(strings.ToLower(acc.Identity), needle) || strings.Contains(strings.ToLower(acc.Name), needle)
		if !match {
			continue
		}
		if len(rows) == limit {
			return rows, true
		}
		rows = append(rows, playerOf(acc))
	}
	return rows, false
}

// AuditFind returns up to limit audit rows whose actor, target or detail (which holds the reason) contains q, any case, newest
// first. ErrNoDatabase without a database; an empty q finds nothing.
// ponytail: a full scan of the audit table; index a search column when it grows past a few hundred thousand rows.
func AuditFind(q string, limit int) ([]AuditRow, error) {
	d := attachedDB()
	if d == nil {
		return nil, ErrNoDatabase
	}
	out := []AuditRow{}
	if q == "" || limit < 1 {
		return out, nil
	}
	err := d.View(func(q2 Querier) error {
		rows, err := q2.Query(`SELECT id, time, actor, action, target, detail FROM audit
			WHERE instr(lower(actor), lower(?)) > 0 OR instr(lower(target), lower(?)) > 0 OR instr(lower(detail), lower(?)) > 0
			ORDER BY id DESC LIMIT ?`, q, q, q, limit)
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
