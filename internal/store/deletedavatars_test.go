package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rawDeletedRows reads deleted_avatars straight from d: aid, account_id ("" for NULL), identity, name, deleted_at and data.
func rawDeletedRows(t *testing.T, d *DB) [][6]string {
	t.Helper()
	var out [][6]string
	if err := d.View(func(q Querier) error {
		rows, err := q.Query(`SELECT aid, COALESCE(CAST(account_id AS TEXT), ''), identity, name, deleted_at, data FROM deleted_avatars ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r [6]string
			if err := rows.Scan(&r[0], &r[1], &r[2], &r[3], &r[4], &r[5]); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestDeletedAvatarsMigrationAddsTheTable: a database at schema 7 takes schema 8, with the table empty and no change to the rows
// that were there.
func TestDeletedAvatarsMigrationAddsTheTable(t *testing.T) {
	raw, err := sql.Open("sqlite", filepath.Join(t.TempDir(), DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	if err := migrateTo(raw, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO meta (key, value) VALUES ('probe', 'kept')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(raw); err != nil {
		t.Fatal(err)
	}
	var version, n int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) || version != 9 {
		t.Fatalf("user_version = %d (err %v), want 9", version, err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM deleted_avatars`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("deleted_avatars rows = %d (err %v), want the empty table", n, err)
	}
	var kept string
	if err := raw.QueryRow(`SELECT value FROM meta WHERE key = 'probe'`).Scan(&kept); err != nil || kept != "kept" {
		t.Fatalf("a row of schema 7 changed: %q (%v)", kept, err)
	}
}

// TestDeletedJSONLImportOnce: the lines of deleted-accounts.jsonl become rows in file order, without their "tokens"; a line that
// does not parse or has no avatar id is skipped; the file is renamed, never deleted, and a second import does nothing.
func TestDeletedJSONLImportOnce(t *testing.T) {
	d, path := openTestDB(t)
	dir := filepath.Dir(path)
	lines := strings.Join([]string{
		`{"time":"2026-11-01T10:00:00Z","account":{"aid":"8001","name":"Ann","identity":"discord:1","gems":3},"tokens":["h1","h2"],"posts":[{"diaryNo":"1","avatarId":"8001","content":"hello"}],"guestbookRead":4}`,
		`{"time":"2026-10-01T10:00:00Z","account":{"aid":"8002"},"scrubbed":true}`,
		`not json at all`,
		`{"time":"2026-10-02T10:00:00Z","account":{}}`,
	}, "\n") + "\n"
	src := filepath.Join(dir, deletedJSONLName)
	if err := os.WriteFile(src, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := importDeletedJSONL(d, dir)
	if err != nil || n != 2 {
		t.Fatalf("imported %d (err %v), want 2", n, err)
	}
	rows := rawDeletedRows(t, d)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if r := rows[0]; r[0] != "8001" || r[1] != "" || r[2] != "discord:1" || r[3] != "Ann" || r[4] != "2026-11-01T10:00:00Z" {
		t.Fatalf("first row = %q", r)
	}
	if strings.Contains(rows[0][5], `"tokens"`) || !strings.Contains(rows[0][5], `"posts"`) || !strings.Contains(rows[0][5], `"hello"`) {
		t.Fatalf("first row data = %s", rows[0][5])
	}
	if r := rows[1]; r[0] != "8002" || r[2] != "" || !strings.Contains(r[5], `"scrubbed":true`) {
		t.Fatalf("scrubbed row = %q", r)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("the jsonl file is still under its name: %v", err)
	}
	if _, err := os.Stat(src + ".imported"); err != nil {
		t.Fatalf("the renamed file: %v", err)
	}

	// The same file back again: the meta key says it was imported, so nothing is added.
	if err := os.WriteFile(src, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := importDeletedJSONL(d, dir); err != nil || n != 0 {
		t.Fatalf("second import = %d (err %v), want 0", n, err)
	}
	if rows := rawDeletedRows(t, d); len(rows) != 2 {
		t.Fatalf("rows after the second import = %d, want 2", len(rows))
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the file the second import skipped went: %v", err)
	}
}

// TestDeletedJSONLImportAtOpenStore: OpenStore imports the file beside cherry.db, and the staff reads see the records.
func TestDeletedJSONLImportAtOpenStore(t *testing.T) {
	dir := t.TempDir()
	line := `{"time":"2026-11-03T08:00:00Z","account":{"aid":"8011","name":"Bo","identity":"discord:9"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, deletedJSONLName), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(filepath.Join(dir, DBFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseStore() })
	rows, more, err := DeletedAvatarList(0, 10)
	if err != nil || more || len(rows) != 1 || rows[0].Aid != "8011" || rows[0].Name != "Bo" {
		t.Fatalf("list = %+v more %t (err %v)", rows, more, err)
	}
	if mine, err := DeletedAvatarsOfIdentity("discord:9", 10); err != nil || len(mine) != 1 {
		t.Fatalf("identity lookup = %+v (err %v)", mine, err)
	}
}

// TestDeletedAvatarRecordInsertAndUndo: the record is one row keyed by the account row (cleared when a lab account's row goes),
// it carries no session key, and an undo removes it.
func TestDeletedAvatarRecordInsertAndUndo(t *testing.T) {
	d := attachTestDB(t)
	acc, rowID := staffAccount(t, "dev-rec1", "8101", "Rec", 4)
	AccountsMu.Lock()
	acc.SessionKey = "clear-session-key-8101"
	rec := AvatarRecord{Time: time.Now().UTC().Format(time.RFC3339), Account: AvatarAccountOf(acc)}
	id, err := InsertAvatarRecordLocked(acc, rec)
	AccountsMu.Unlock()
	if err != nil || id == 0 {
		t.Fatalf("insert = %d (err %v)", id, err)
	}
	rows := rawDeletedRows(t, d)
	if len(rows) != 1 || rows[0][0] != "8101" || rows[0][1] != strconv.FormatInt(rowID, 10) || rows[0][3] != "Rec" {
		t.Fatalf("rows = %q, want one for 8101 keyed by the account row %d", rows, rowID)
	}
	if strings.Contains(rows[0][5], "clear-session-key-8101") {
		t.Fatal("the record holds the session key")
	}

	// A lab account's row goes with its delete: the record stays and loses the link.
	AccountsMu.Lock()
	_, err = DeleteAccountLocked(acc, nil)
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if rows := rawDeletedRows(t, d); len(rows) != 1 || rows[0][1] != "" {
		t.Fatalf("after the account row went: %q, want the record with a NULL account", rows)
	}
	if err := UndoAvatarRecord(id); err != nil {
		t.Fatal(err)
	}
	if rows := rawDeletedRows(t, d); len(rows) != 0 {
		t.Fatalf("undo left %d rows", len(rows))
	}
	if err := UndoAvatarRecord(0); err != nil {
		t.Fatalf("undo of no record: %v", err)
	}
}

// TestGatherAvatarHistoryReadsTheStores: the record takes the avatar's own chat lines and those of its 1:1 room (not another
// room's), the room with its stored messages, its Treasure World board and its circle.
func TestGatherAvatarHistoryReadsTheStores(t *testing.T) {
	d := attachTestDB(t)
	defer ResetCircles()
	if err := d.Update(func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO chat_log (time, kind, channel, sender, sender_name, text) VALUES (1700000000000, 'lounge', '5', '8201', 'Gia', 'mine')`,
			`INSERT INTO chat_log (time, kind, channel, sender, sender_name, text) VALUES (1700000001000, 'private', '1960000001', '8202', 'Hal', 'to you')`,
			`INSERT INTO chat_log (time, kind, channel, sender, sender_name, text) VALUES (1700000002000, 'lounge', '5', '9999', 'Zed', 'not theirs')`,
			`INSERT INTO chat_rooms (room_id, aid_a, aid_b, data) VALUES (1960000001, '8201', '8202', '{"id":1960000001,"aids":["8201","8202"],"ceil":2}')`,
			`INSERT INTO chat_rooms (room_id, aid_a, aid_b, data) VALUES (1960000002, '9998', '9999', '{"id":1960000002,"aids":["9998","9999"],"ceil":0}')`,
			`INSERT INTO chat_messages (room_id, sn, sec, sender, text) VALUES (1960000001, 1, 1700000001, '8202', 'to you')`,
			`INSERT INTO chat_messages (room_id, sn, sec, sender, text) VALUES (1960000001, 2, 1700000003, '8201', 'back')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateTreasure("8201", func(r *TreasureRecord) { r.Season, r.Jewels = 3, 7 }); err != nil {
		t.Fatal(err)
	}
	CirclesMu.Lock()
	CircleState.Circles = []Circle{{ID: 77, Name: "Gia Club", Members: []CircleMember{{Aid: "8201", Role: "LEADER"}}}}
	CirclesMu.Unlock()

	var rec AvatarRecord
	AccountsMu.Lock()
	err := GatherAvatarHistoryLocked(&rec, "8201")
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.ChatLog) != 2 || rec.ChatLog[0].Text != "mine" || rec.ChatLog[1].Text != "to you" {
		t.Fatalf("chat lines = %+v, want its own line and its room's line", rec.ChatLog)
	}
	if len(rec.ChatRooms) != 1 || rec.ChatRooms[0].ID != 1960000001 || len(rec.ChatRooms[0].Msgs) != 2 || rec.ChatRooms[0].Msgs[1].Text != "back" {
		t.Fatalf("rooms = %+v, want the one 1:1 room with its two messages", rec.ChatRooms)
	}
	if rec.Treasure == nil || rec.Treasure.Season != 3 || rec.Treasure.Jewels != 7 {
		t.Fatalf("treasure = %+v", rec.Treasure)
	}
	if rec.Circle == nil || rec.Circle.ID != 77 || rec.Circle.Role != "LEADER" {
		t.Fatalf("circle = %+v", rec.Circle)
	}
}

// TestRemoveDeletedAvatarMedia: a picture of a deleted avatar goes with its row, its file and an audit row that names the
// avatar; a picture of a live account (or of nobody) is not one this removal takes.
func TestRemoveDeletedAvatarMedia(t *testing.T) {
	attachTestDB(t)
	mediaDataDir(t)
	insertDeletedRowForTest(t, "8301")
	gone := storeMediaFile(t, MediaDiary, "8301", "8301", "1760000100", "8301_1760000100")
	live := storeMediaFile(t, MediaDiary, "1001", "1001", "1760000200", "1001_1760000200")

	if err := RemoveDeletedAvatarMedia(live.ID, "not theirs", "dev-owner"); err != ErrNoItem {
		t.Fatalf("a live account's picture: %v, want ErrNoItem", err)
	}
	if err := RemoveDeletedAvatarMedia(gone.ID, "  ", "dev-owner"); err == nil {
		t.Fatal("an empty reason was accepted")
	}
	if err := RemoveDeletedAvatarMedia(gone.ID, "old picture", "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := MediaRowByFile(gone.File); found || onDisk(t, gone) {
		t.Fatal("the removed picture's row or file remains")
	}
	if _, found, _ := MediaRowByFile(live.File); !found {
		t.Fatal("a live account's picture went")
	}
	var target string
	if err := AttachedDB().View(func(q Querier) error {
		return q.QueryRow(`SELECT target FROM audit WHERE action = 'remove-media' ORDER BY id DESC LIMIT 1`).Scan(&target)
	}); err != nil || target != "avatar:8301" {
		t.Fatalf("audit target = %q (%v), want avatar:8301", target, err)
	}
}

// TestPictureOfDeletedAvatarIsNotServed: the game's picture check names the deleted avatars' pictures only; a live player's
// picture and a name with no row are not.
func TestPictureOfDeletedAvatarIsNotServed(t *testing.T) {
	attachTestDB(t)
	mediaDataDir(t)
	insertDeletedRowForTest(t, "8601")
	gone := storeMediaFile(t, MediaDiary, "8601", "8601", "1760000300", "8601_1760000300")
	live := storeMediaFile(t, MediaDiary, "1001", "1001", "1760000400", "1001_1760000400")
	if !PictureOfDeletedAvatar(gone.File) {
		t.Fatal("a deleted avatar's picture is served")
	}
	if PictureOfDeletedAvatar(live.File) || PictureOfDeletedAvatar(strings.Repeat("0", 64)+".img") {
		t.Fatal("a live player's picture or a file with no row is refused")
	}
}

// insertDeletedRowForTest keeps one deleted avatar row for aid in the attached database (the record a delete would have kept).
func insertDeletedRowForTest(t *testing.T, aid string) {
	t.Helper()
	if err := AttachedDB().Update(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO deleted_avatars (aid, identity, name, deleted_at, data) VALUES (?, '', 'Gone', '2026-11-01T00:00:00Z', '{}')`, aid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
