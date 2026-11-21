package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Deleted avatars (2026-11-21, user: avatar ids are never reused, so a deleted avatar and all its data stay viewable in the
// staff panel, with no expiry). Delete Avatar keeps one row of deleted_avatars (schema v8) before it removes the avatar: the
// whole record as JSON. The avatar's pictures are not removed (their rows and files keep the deleted aid as owner).

const (
	deletedImportKey  = "deleted_avatars.imported" // meta key: the jsonl records are imported (value: how many)
	deletedJSONLName  = "deleted-accounts.jsonl"   // the file the records were kept in before schema v8
	deletedListMax    = 200                        // rows a search or a list may ask for
	deletedImportNote = ".imported"
)

// AvatarRecord is what Delete Avatar keeps of an avatar: its account as the store saves it (no access tokens, session keys or
// code hashes), its social rows, its chat, Treasure World board, circle and picture rows. It is the data column of a
// deleted_avatars row. Lines imported from the jsonl file before schema v8 have the same account and social fields.
type AvatarRecord struct {
	Time          string                `json:"time"`
	Account       StoredAccount         `json:"account"`
	Posts         []DiaryPost           `json:"posts"`
	Guestbook     []SavedGuestbookEntry `json:"guestbook"`
	Friendships   []Friendship          `json:"friendships"`
	Comments      []DiaryComment        `json:"comments"`
	Likes         []DiaryLike           `json:"likes"`
	Replies       []GuestbookReply      `json:"replies"`
	GuestbookRead uint64                `json:"guestbookRead"`
	ChatLog       []AvatarChatLine      `json:"chatLog"`
	ChatRooms     []ChatRoom            `json:"chatRooms"` // 1:1 rooms the avatar was in, with their stored messages (Msgs)
	Treasure      *TreasureRecord       `json:"treasure,omitempty"`
	Circle        *CircleMembership     `json:"circle,omitempty"`
	Media         []MediaFile           `json:"media"` // the picture rows the avatar owned
}

// AvatarChatLine is one chat_log row of a deleted avatar: a line it sent, or a line of one of its 1:1 rooms.
type AvatarChatLine struct {
	Time       int64  `json:"time"`
	Kind       string `json:"kind"`
	Channel    string `json:"channel"`
	Sender     string `json:"sender"`
	SenderName string `json:"senderName"`
	Text       string `json:"text"`
}

// AvatarAccountOf is the account part of a record: the saved form of acc without its session keys, salts, legacy key hash
// and code hashes. StoredFrom never holds the access tokens.
func AvatarAccountOf(acc *Account) StoredAccount {
	s := StoredFrom(acc)
	s.SessionKey, s.SessionSalt, s.SessionEpoch, s.LegacyKeyHash, s.CodeHashes = "", "", 0, "", nil
	return s
}

// AccountOfRecord is the account of a record as an in-memory account, for reads only (the staff page's Garden and economy
// figures). It takes no lock and is never put in the account table.
func AccountOfRecord(s StoredAccount) *Account { return accountFromStored(s) }

// GatherAvatarHistoryLocked fills the parts of rec that the store keeps for aid: its chat lines (the lines it sent and the
// lines of its 1:1 rooms), its 1:1 rooms with their stored messages, its Treasure World board, its circle and its picture
// rows. Caller holds AccountsMu and not SocialMu (the lock order is AccountsMu, CirclesMu, SocialMu). Without a database the
// database parts stay empty.
func GatherAvatarHistoryLocked(rec *AvatarRecord, aid string) error {
	if tr, ok := TreasureRecordOf(aid); ok {
		c := tr.Clone()
		rec.Treasure = &c
	}
	if c, ok := PlayerCircle(aid); ok {
		rec.Circle = &c
	}
	media, err := AccountMediaLocked(aid)
	if err != nil {
		return err
	}
	rec.Media = media
	d := attachedDB()
	if d == nil {
		return nil
	}
	var rooms []ChatRoom
	err = d.View(func(q Querier) error {
		lines, err := q.Query(`SELECT time, kind, channel, sender, sender_name, text FROM chat_log
			WHERE sender = ? OR (kind = ? AND channel IN (SELECT CAST(room_id AS TEXT) FROM chat_rooms WHERE aid_a = ? OR aid_b = ?))
			ORDER BY id`, aid, ChatPrivate, aid, aid)
		if err != nil {
			return err
		}
		defer lines.Close()
		for lines.Next() {
			var l AvatarChatLine
			if err := lines.Scan(&l.Time, &l.Kind, &l.Channel, &l.Sender, &l.SenderName, &l.Text); err != nil {
				return err
			}
			rec.ChatLog = append(rec.ChatLog, l)
		}
		if err := lines.Err(); err != nil {
			return err
		}
		rr, err := q.Query(`SELECT data FROM chat_rooms WHERE aid_a = ? OR aid_b = ? ORDER BY id`, aid, aid)
		if err != nil {
			return err
		}
		defer rr.Close()
		for rr.Next() {
			var data string
			if err := rr.Scan(&data); err != nil {
				return err
			}
			var room ChatRoom
			if err := json.Unmarshal([]byte(data), &room); err != nil {
				return fmt.Errorf("chat room row: %w", err)
			}
			rooms = append(rooms, room)
		}
		return rr.Err()
	})
	if err != nil {
		return err
	}
	for i := range rooms {
		err := d.View(func(q Querier) error {
			msgs, err := q.Query(`SELECT sn, sec, sender, text FROM chat_messages WHERE room_id = ? ORDER BY id`, rooms[i].ID)
			if err != nil {
				return err
			}
			defer msgs.Close()
			for msgs.Next() {
				var m ChatMsg
				if err := msgs.Scan(&m.Sn, &m.Sec, &m.Sender, &m.Text); err != nil {
					return err
				}
				rooms[i].Msgs = append(rooms[i].Msgs, m)
			}
			return msgs.Err()
		})
		if err != nil {
			return err
		}
	}
	rec.ChatRooms = rooms
	return nil
}

// InsertAvatarRecordLocked keeps rec as the record of acc's avatar: one row of deleted_avatars, whose id it returns. Its
// account_id is acc's accounts row (cleared by the database when that row is deleted). Caller holds AccountsMu. Without an
// attached database nothing is kept and the id is 0.
func InsertAvatarRecordLocked(acc *Account, rec AvatarRecord) (int64, error) {
	d := attachedDB()
	if d == nil {
		return 0, nil
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return 0, err
	}
	var accountID sql.NullInt64
	if rs := accountRows[acc]; rs != nil && rs.id != 0 {
		accountID = sql.NullInt64{Int64: rs.id, Valid: true}
	}
	var id int64
	err = d.Update(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO deleted_avatars (aid, account_id, identity, name, deleted_at, data) VALUES (?, ?, ?, ?, ?, ?)`,
			acc.Aid, accountID, acc.Identity, acc.Name, rec.Time, string(data))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// UndoAvatarRecord removes the row InsertAvatarRecordLocked made, when the delete that made it failed after the insert. The
// id 0 (no row was made) does nothing.
func UndoAvatarRecord(id int64) error {
	d := attachedDB()
	if id == 0 || d == nil {
		return nil
	}
	return d.Update(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM deleted_avatars WHERE id = ?`, id)
		return err
	})
}

// DeletedAvatar is a row of the deleted avatars: its id (the staff page's key), avatar id, identity, name and the time it was
// deleted (RFC 3339, UTC). Name and identity are "" for a record without them.
type DeletedAvatar struct {
	ID                             int64
	Aid, Identity, Name, DeletedAt string
	RestoredAt                     string // RFC 3339 UTC time of the restore; "" = not restored (avatarrestore.go)
	RestoredAccount                int64  // the accounts row the restore went to; 0 = not restored
}

// DeletedAvatarDetail is one deleted avatar with its whole record.
type DeletedAvatarDetail struct {
	DeletedAvatar
	Record AvatarRecord
}

// DeletedAvatarList returns up to limit deleted avatars, newest first, with ids below before (0 = from the newest). more
// reports that older ones exist. limit is 1 to deletedListMax.
func DeletedAvatarList(before int64, limit int) (rows []DeletedAvatar, more bool, err error) {
	if limit < 1 || limit > deletedListMax {
		return nil, false, fmt.Errorf("%w: the list is 1 to %d rows", ErrBadInput, deletedListMax)
	}
	rows, err = deletedQuery(`SELECT id, aid, identity, name, deleted_at, restored_at, COALESCE(restored_account, 0) FROM deleted_avatars
		WHERE (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`, before, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		return rows[:limit], true, nil
	}
	return rows, false, nil
}

// DeletedAvatarByID returns the deleted avatar with row id, with its record. found is false for an unknown id.
func DeletedAvatarByID(id int64) (det DeletedAvatarDetail, found bool, err error) {
	d := attachedDB()
	if d == nil {
		return det, false, ErrNoDatabase
	}
	var data string
	err = d.View(func(q Querier) error {
		return q.QueryRow(`SELECT id, aid, identity, name, deleted_at, restored_at, COALESCE(restored_account, 0), data FROM deleted_avatars WHERE id = ?`, id).
			Scan(&det.ID, &det.Aid, &det.Identity, &det.Name, &det.DeletedAt, &det.RestoredAt, &det.RestoredAccount, &data)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return det, false, nil
	}
	if err != nil {
		return det, false, err
	}
	if err := json.Unmarshal([]byte(data), &det.Record); err != nil {
		return det, false, fmt.Errorf("deleted avatar %d: %w", id, err)
	}
	return det, true, nil
}

// DeletedAvatarsOfIdentity returns up to limit deleted avatars that belonged to identity, newest first. An empty identity (a
// lab account) has none.
func DeletedAvatarsOfIdentity(identity string, limit int) ([]DeletedAvatar, error) {
	if identity == "" || limit < 1 {
		return nil, nil
	}
	return deletedQuery(`SELECT id, aid, identity, name, deleted_at, restored_at, COALESCE(restored_account, 0) FROM deleted_avatars
		WHERE identity = ? ORDER BY id DESC LIMIT ?`, identity, limit)
}

// SearchDeletedAvatars returns up to limit deleted avatars whose avatar id is q exactly or whose name contains q (any case),
// newest first. more reports that there are more. An empty q finds nothing.
func SearchDeletedAvatars(q string, limit int) (rows []DeletedAvatar, more bool, err error) {
	q = strings.TrimSpace(q)
	if q == "" || limit < 1 {
		return nil, false, nil
	}
	// ponytail: the name is a scan of the table; index a lowercased name column when the table grows past a few thousand rows.
	rows, err = deletedQuery(`SELECT id, aid, identity, name, deleted_at, restored_at, COALESCE(restored_account, 0) FROM deleted_avatars
		WHERE aid = ? OR instr(lower(name), lower(?)) > 0 ORDER BY id DESC LIMIT ?`, q, q, limit+1)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		return rows[:limit], true, nil
	}
	return rows, false, nil
}

// LedgerOfAid returns the newest limit ledger lines of avatar id aid, newest first (the live ledger table, which keeps its lines
// by avatar id after the avatar is deleted). "0" has none.
func LedgerOfAid(aid string, limit int) ([]LedgerLine, error) {
	lines := []LedgerLine{}
	d := attachedDB()
	if d == nil || aid == "0" || limit < 1 {
		return lines, nil
	}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(`SELECT time, aid, currency, delta, balance, reason FROM ledger WHERE aid = ? ORDER BY id DESC LIMIT ?`, aid, limit)
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

// RemoveDeletedAvatarMedia removes one stored picture (media row id mediaID) whose uploader is a deleted avatar: the row, its file
// and every reference to the file, with its audit row, as RemovePlayerMedia does for a live player. ErrNoItem when the row is not
// a deleted avatar's.
func RemoveDeletedAvatarMedia(mediaID int64, reason, actor string) error {
	reason, err := cleanReason(reason)
	if err != nil {
		return err
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	d := attachedDB()
	if d == nil {
		return ErrNoDatabase
	}
	var row MediaFile
	var deleted bool
	err = d.View(func(q Querier) error {
		var found bool
		var err error
		row, found, err = mediaRow(q, "id", mediaID)
		if err != nil || !found {
			return err
		}
		var n int
		err = q.QueryRow(`SELECT COUNT(*) FROM deleted_avatars WHERE aid = ?`, row.Owner).Scan(&n)
		deleted = n > 0
		return err
	})
	if err != nil {
		return err
	}
	if !deleted || !mediaFileOK(row.File) {
		return ErrNoItem
	}
	return removeMediaLocked(d, row, reason, actor, "avatar:"+row.Owner)
}

// PictureOfDeletedAvatar reports whether file is a picture whose uploader is a deleted avatar that is not restored, or whose
// uploader cannot be read (the game serves none of them; the staff panel keeps them). A file with no row is not one. An
// unrestored record with no live account holding its aid is the test: a restored avatar's pictures are served again.
// ponytail: one indexed read per picture download; an in-memory set of deleted aids is the upgrade if downloads show in profiles.
func PictureOfDeletedAvatar(file string) bool {
	d := attachedDB()
	if d == nil {
		return false
	}
	var n int
	err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT COUNT(*) FROM deleted_avatars d WHERE d.restored_at = ''
			AND d.aid = (SELECT owner FROM media_files WHERE file = ?)
			AND NOT EXISTS (SELECT 1 FROM accounts a WHERE a.aid = d.aid)`, file).Scan(&n)
	})
	return err != nil || n > 0
}

// deletedQuery runs a query of the deleted_avatars list columns (id, aid, identity, name, deleted_at).
func deletedQuery(query string, args ...any) ([]DeletedAvatar, error) {
	d := attachedDB()
	if d == nil {
		return nil, ErrNoDatabase
	}
	out := []DeletedAvatar{}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r DeletedAvatar
			if err := rows.Scan(&r.ID, &r.Aid, &r.Identity, &r.Name, &r.DeletedAt, &r.RestoredAt, &r.RestoredAccount); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// importDeletedJSONL imports the records of dir's deleted-accounts.jsonl (the file kept before schema v8) into deleted_avatars,
// once: the rows and a meta key go in one transaction, so a later start skips the file. Each line becomes one row, in file
// order, with its avatar id, name, identity and time read from it; its data is the line without the "tokens" of the old backup
// (scrubbed lines are imported as they are). A line that does not parse or has no avatar id is skipped and counted. After the
// commit the file is renamed to deleted-accounts.jsonl.imported; it is never deleted. Only OpenStore calls it.
func importDeletedJSONL(d *DB, dir string) (int, error) {
	var done string
	if err := d.View(func(q Querier) (err error) {
		done, err = metaValue(q, deletedImportKey)
		return err
	}); err != nil {
		return 0, err
	}
	if done != "" {
		return 0, nil
	}
	path := filepath.Join(dir, deletedJSONLName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	type imported struct{ aid, name, identity, at, data string }
	var rows []imported
	skipped := 0
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var head struct {
			Time    string `json:"time"`
			Account struct {
				Aid      string `json:"aid"`
				Name     string `json:"name"`
				Identity string `json:"identity"`
			} `json:"account"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(line, &head) != nil || head.Account.Aid == "" || json.Unmarshal(line, &fields) != nil {
			skipped++
			continue
		}
		delete(fields, "tokens")
		data, err := json.Marshal(fields)
		if err != nil {
			return 0, err
		}
		rows = append(rows, imported{head.Account.Aid, head.Account.Name, head.Account.Identity, head.Time, string(data)})
	}
	if err := d.Update(func(tx *sql.Tx) error {
		for _, r := range rows {
			if _, err := tx.Exec(`INSERT INTO deleted_avatars (aid, account_id, identity, name, deleted_at, data) VALUES (?, NULL, ?, ?, ?, ?)`,
				r.aid, r.identity, r.name, r.at, r.data); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)`, deletedImportKey, strconv.Itoa(len(rows)))
		return err
	}); err != nil {
		return 0, err
	}
	if skipped > 0 {
		log.Printf("cherry: %s: %d lines do not parse and were not imported", deletedJSONLName, skipped)
	}
	log.Printf("cherry: imported %d deleted avatars from %s", len(rows), deletedJSONLName)
	dst := path + deletedImportNote
	if _, err := os.Stat(dst); err == nil {
		log.Printf("cherry: %s kept: %s already exists", deletedJSONLName, filepath.Base(dst))
		return len(rows), nil
	}
	if err := os.Rename(path, dst); err != nil {
		log.Printf("cherry: rename %s: %v", deletedJSONLName, err)
	}
	return len(rows), nil
}
