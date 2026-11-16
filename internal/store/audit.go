package store

import (
	"database/sql"
	"fmt"
)

// The staff UI's audit trail (PLAN P4). Every staff action writes one row through the same transaction shape as the
// document functions; the row names the actor, never a secret, and the detail is small JSON.

// AuditRow is one row of the audit table, newest first in listings.
type AuditRow struct {
	ID     int64  `json:"id"`
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail"`
}

// Audit appends one row. It fails without an attached database: a staff action that cannot be recorded must not run.
func Audit(actor, action, target string, detail map[string]any) error {
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error { return auditTx(tx, actor, action, target, detail) })
}

// AuditRows returns up to limit rows older than id before (0 = from the newest), newest first.
func AuditRows(before int64, limit int) ([]AuditRow, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: the audit page is 1 to 1000 rows", ErrBadInput)
	}
	out := []AuditRow{}
	err = d.View(func(q Querier) error {
		rows, err := q.Query("SELECT id, time, actor, action, target, detail FROM audit WHERE ? = 0 OR id < ? ORDER BY id DESC LIMIT ?",
			before, before, limit)
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
