package store

import (
	"database/sql"
	"fmt"
)

// Staff mail sends (PLAN Roadmap 8, staff panel phase 5; schema v7): every send the staff panel makes is one row, keyed by the
// send id of its form. The send's mailboxes, this row and the audit row are one save (commitLocked), so a send that is not
// recorded does not happen, and a recorded send is never sent again: MailSendLocked finds it by its id.

// AllPlayersTarget is the target of a send to every account.
const AllPlayersTarget = "all-players"

// MailSend is one staff mail send. SendID is the form's id. Target is "player:<row id>" or AllPlayersTarget. Recipients are the
// mailboxes that got the mail; Skipped are the accounts whose mailbox was full. Kind is the attachment (gems, cash,
// faceTickets or item) with its Amount or Item. Message is the text the players read; Reason is the staff reason.
type MailSend struct {
	ID          int64
	SendID      string
	Created     string
	Actor       string
	Target      string
	Recipients  int64
	Skipped     int64
	Kind        string
	Amount      int64
	Item        string
	Message     string
	ExpiresDays int64
	Reason      string
}

// MailSendLocked returns the send kept under sendID, and whether there is one. Caller holds AccountsMu, so a check and the send
// that follows it cannot interleave with another send. ErrNoDatabase without a database.
func MailSendLocked(sendID string) (MailSend, bool, error) {
	d := attachedDB()
	if d == nil {
		return MailSend{}, false, ErrNoDatabase
	}
	var m MailSend
	err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT id, send_id, created, actor, target, recipients, skipped, kind, amount, item, message, expires_days, reason
			FROM staff_mail WHERE send_id = ?`, sendID).Scan(&m.ID, &m.SendID, &m.Created, &m.Actor, &m.Target, &m.Recipients, &m.Skipped,
			&m.Kind, &m.Amount, &m.Item, &m.Message, &m.ExpiresDays, &m.Reason)
	})
	if err == sql.ErrNoRows {
		return MailSend{}, false, nil
	}
	if err != nil {
		return MailSend{}, false, err
	}
	return m, true, nil
}

// SaveMailSendLocked writes rec, its audit row ("mail", the target, the attachment, the counts and the reason) and the saves of
// accs (the mailboxes), in one transaction. A duplicate send id fails the save (UNIQUE), and nothing is written. Caller holds
// AccountsMu and restores the accounts on error.
func SaveMailSendLocked(rec MailSend, accs ...*Account) error {
	detail := map[string]any{
		"send": rec.SendID, "recipients": rec.Recipients, "skipped": rec.Skipped, "kind": rec.Kind,
		"amount": rec.Amount, "item": rec.Item, "expires_days": rec.ExpiresDays, "message_bytes": len(rec.Message), "reason": rec.Reason,
	}
	audit := staffAudit{rec.Actor, "mail", rec.Target, detail}
	extra := func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO staff_mail (send_id, created, actor, target, recipients, skipped, kind, amount, item, message, expires_days, reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rec.SendID, rec.Created, rec.Actor, rec.Target, rec.Recipients, rec.Skipped,
			rec.Kind, rec.Amount, rec.Item, rec.Message, rec.ExpiresDays, rec.Reason)
		return err
	}
	return commitLocked(nil, accs, 0, callerSite(), &audit, extra)
}

// ListMailSends returns up to limit sends, newest first, with ids below before (0 = from the newest). ErrNoDatabase without a
// database; ErrBadInput for a limit outside 1 to 200.
func ListMailSends(before int64, limit int) ([]MailSend, error) {
	d := attachedDB()
	if d == nil {
		return nil, ErrNoDatabase
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: the sent mail page is 1 to 200 rows", ErrBadInput)
	}
	out := []MailSend{}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(`SELECT id, send_id, created, actor, target, recipients, skipped, kind, amount, item, message, expires_days, reason
			FROM staff_mail WHERE (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`, before, before, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m MailSend
			if err := rows.Scan(&m.ID, &m.SendID, &m.Created, &m.Actor, &m.Target, &m.Recipients, &m.Skipped, &m.Kind, &m.Amount,
				&m.Item, &m.Message, &m.ExpiresDays, &m.Reason); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}
