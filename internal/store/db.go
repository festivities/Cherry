package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (the one approved module; builds with CGO_ENABLED=0)
)

// cherry.db is the SQLite record of the stores (PLAN Roadmap 8, P1 and P2). The server reads it at start and writes every
// store through it (accounts since part B1; social, circles, Treasure World and chat since part B2). The JSON files are
// read only by cmd/cherry-import.

// DBFileName is the database file inside the data directory.
const DBFileName = "cherry.db"

// dbPragmas are the settings every connection of both handles gets from the DSN. modernc.org/sqlite runs each _pragma
// value as "PRAGMA <value>" when the connection opens: busy_timeout first, the others in lexical order (driver.go,
// "_pragma"). The journal mode is persistent: the first connection sets it, the others find it set.
const dbPragmas = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"

// DB is one database file: a write handle with a single connection, and a read pool of query_only connections.
type DB struct {
	write *sql.DB
	read  *sql.DB
	path  string // the file it was opened from (StoreDirLocked)
}

// OpenDB opens the database at path, creating it if needed, and migrates it to the schema this build knows. A database
// whose schema is newer than this build is refused.
func OpenDB(path string) (*DB, error) {
	// The driver cuts a plain file name at its first '?' (driver.go, Open), so a '?' in path would name another file.
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("database path contains '?'")
	}
	// _txlock=immediate: every transaction on the write handle begins IMMEDIATE (driver.go, "_txlock").
	write, err := sql.Open("sqlite", path+"?"+dbPragmas+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	// ponytail: one write connection, so writers queue in Go and never see SQLITE_BUSY; a second writer is not needed yet.
	write.SetMaxOpenConns(1)
	d := &DB{write: write, path: path}
	if err := write.Ping(); err != nil {
		d.Close()
		return nil, err
	}
	if err := migrate(write); err != nil {
		d.Close()
		return nil, err
	}
	// _query_only=1: the read pool cannot write, whatever a caller passes to it.
	read, err := sql.Open("sqlite", path+"?"+dbPragmas+"&_query_only=1")
	if err != nil {
		d.Close()
		return nil, err
	}
	read.SetMaxOpenConns(4) // ponytail: fixed small read pool; raise it when reads queue.
	d.read = read
	return d, nil
}

// Close closes both handles; the last connection checkpoints the WAL.
func (d *DB) Close() error {
	var err error
	if d.read != nil {
		err = d.read.Close()
	}
	if d.write != nil {
		if cerr := d.write.Close(); err == nil {
			err = cerr
		}
	}
	return err
}

// Update runs fn in one IMMEDIATE write transaction: it commits when fn returns nil and rolls back otherwise.
func (d *DB) Update(fn func(tx *sql.Tx) error) error {
	tx, err := d.write.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// View runs fn against one read snapshot (a deferred read transaction on the read pool).
func (d *DB) View(fn func(q Querier) error) error {
	tx, err := d.read.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

// Ping answers a trivial query on the attached database (the health endpoint). It returns ErrNoDatabase when none is
// attached, and an error when the query fails or takes more than 2 s.
func Ping() error {
	d := attachedDB()
	if d == nil {
		return ErrNoDatabase
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var one int
	return d.read.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// Querier is what the read functions take: a View snapshot or a write transaction.
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// migrations[i] takes the database from schema version i to i+1. Append only: a shipped step is never edited.
var migrations = []string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7, schemaV8, schemaV9, schemaV10}

// migrate applies the missing steps, each in its own transaction together with its user_version bump.
func migrate(db *sql.DB) error { return migrateTo(db, len(migrations)) }

// migrateTo applies the missing steps up to schema version target (migrate passes the newest; a test stops at an older one).
func migrateTo(db *sql.DB, target int) error {
	var have int
	if err := db.QueryRow("PRAGMA user_version").Scan(&have); err != nil {
		return err
	}
	if have > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this build (%d)", have, len(migrations))
	}
	for v := have; v < target; v++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration to version %d: %w", v+1, err)
		}
		// PRAGMA takes no placeholders; v+1 is this loop's counter, never input.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// schemaV1 holds the tables of the stores. Each list keeps its slice order in the ordinal id; a row's JSON is the exact
// marshal of its Go value, so reads give back what was written (nil and empty slices included). Only the access tokens
// and the guest code are kept as SHA-256 hex; see dbaccounts.go.
const schemaV1 = `
CREATE TABLE accounts (
	id              INTEGER PRIMARY KEY,
	aid             TEXT NOT NULL,
	name            TEXT NOT NULL,
	identity        TEXT NOT NULL DEFAULT '',
	guest_code_hash TEXT NOT NULL DEFAULT '',
	created         TEXT NOT NULL,
	updated         TEXT NOT NULL,
	data_hash       TEXT NOT NULL,
	data            TEXT NOT NULL
) STRICT;
-- aid "0" is the account that has no avatar yet: several may exist.
CREATE UNIQUE INDEX accounts_aid ON accounts(aid) WHERE aid <> '0';
CREATE UNIQUE INDEX accounts_identity ON accounts(identity) WHERE identity <> '';
CREATE UNIQUE INDEX accounts_guest ON accounts(guest_code_hash) WHERE guest_code_hash <> '';

CREATE TABLE tokens (
	hash       TEXT PRIMARY KEY,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	kind       TEXT NOT NULL CHECK (kind IN ('own', 'alias')),
	created    TEXT NOT NULL,
	last_used  TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX tokens_account ON tokens(account_id);
CREATE UNIQUE INDEX tokens_own ON tokens(account_id) WHERE kind = 'own';

CREATE TABLE ledger (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	time     TEXT NOT NULL,
	aid      TEXT NOT NULL,
	currency TEXT NOT NULL,
	delta    INTEGER NOT NULL,
	balance  INTEGER NOT NULL,
	reason   TEXT NOT NULL
) STRICT;
CREATE INDEX ledger_aid ON ledger(aid, id);

CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) STRICT;

CREATE TABLE diary_posts (
	id        INTEGER PRIMARY KEY,
	diary_no  TEXT NOT NULL,
	avatar_id TEXT NOT NULL,
	data      TEXT NOT NULL
) STRICT;
CREATE INDEX diary_posts_no ON diary_posts(diary_no);

CREATE TABLE diary_comments (
	id         INTEGER PRIMARY KEY,
	comment_no TEXT NOT NULL,
	diary_no   TEXT NOT NULL,
	data       TEXT NOT NULL
) STRICT;
CREATE INDEX diary_comments_diary ON diary_comments(diary_no);

CREATE TABLE diary_likes (
	id             INTEGER PRIMARY KEY,
	like_no        TEXT NOT NULL,
	diary_no       TEXT NOT NULL,
	like_avatar_id TEXT NOT NULL,
	data           TEXT NOT NULL
) STRICT;
CREATE INDEX diary_likes_diary ON diary_likes(diary_no, like_avatar_id);

CREATE TABLE guestbook_entries (
	id            INTEGER PRIMARY KEY,
	host_avt_no   TEXT NOT NULL,
	guest_hist_no TEXT NOT NULL,
	data          TEXT NOT NULL
) STRICT;
CREATE INDEX guestbook_entries_host ON guestbook_entries(host_avt_no, guest_hist_no);

CREATE TABLE guestbook_replies (
	id            INTEGER PRIMARY KEY,
	entry_id      INTEGER NOT NULL REFERENCES guestbook_entries(id) ON DELETE CASCADE,
	reply_hist_no TEXT NOT NULL,
	data          TEXT NOT NULL
) STRICT;
CREATE INDEX guestbook_replies_entry ON guestbook_replies(entry_id);

CREATE TABLE guestbook_read (
	host_aid TEXT PRIMARY KEY,
	hist_no  INTEGER NOT NULL
) STRICT;

CREATE TABLE friendships (
	id    INTEGER PRIMARY KEY,
	a     TEXT NOT NULL,
	b     TEXT NOT NULL,
	state TEXT NOT NULL
) STRICT;
CREATE INDEX friendships_pair ON friendships(a, b);

CREATE TABLE circles (
	id        INTEGER PRIMARY KEY,
	circle_id INTEGER NOT NULL UNIQUE,
	data      TEXT NOT NULL
) STRICT;

CREATE TABLE circle_leaves (
	aid   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
) STRICT;

CREATE TABLE treasure_records (
	aid  TEXT PRIMARY KEY,
	data TEXT NOT NULL
) STRICT;

CREATE TABLE chat_rooms (
	id      INTEGER PRIMARY KEY,
	room_id INTEGER NOT NULL UNIQUE,
	aid_a   TEXT NOT NULL,
	aid_b   TEXT NOT NULL,
	data    TEXT NOT NULL
) STRICT;

CREATE TABLE chat_messages (
	id      INTEGER PRIMARY KEY,
	room_id INTEGER NOT NULL REFERENCES chat_rooms(room_id) ON DELETE CASCADE,
	sn      INTEGER NOT NULL,
	sec     INTEGER NOT NULL,
	sender  TEXT NOT NULL,
	text    TEXT NOT NULL
) STRICT;
CREATE INDEX chat_messages_room ON chat_messages(room_id, id);
`

// schemaV2 gives the store tables the unique keys the code names rows by: a diary post number, a comment number, a like
// number and one like per post and liker, a guestbook entry number, a reply history number, one friendship per unordered
// pair, and a chat message per room and serial. Only indexes change, so every v1 row carries over. A v1 table whose rows
// repeat a key fails this step: the migration rolls back and the server does not start; nothing is dropped or merged.
// The row ids of the list tables stay the order of the list (dbstores.go).
const schemaV2 = `
DROP INDEX diary_posts_no;
CREATE UNIQUE INDEX diary_posts_no ON diary_posts(diary_no);
CREATE UNIQUE INDEX diary_comments_no ON diary_comments(comment_no);
DROP INDEX diary_likes_diary;
CREATE UNIQUE INDEX diary_likes_diary ON diary_likes(diary_no, like_avatar_id);
CREATE UNIQUE INDEX diary_likes_no ON diary_likes(like_no);
CREATE UNIQUE INDEX guestbook_entries_hist ON guestbook_entries(guest_hist_no);
CREATE UNIQUE INDEX guestbook_replies_hist ON guestbook_replies(reply_hist_no);
DROP INDEX friendships_pair;
CREATE UNIQUE INDEX friendships_pair ON friendships(min(a, b), max(a, b));
CREATE UNIQUE INDEX chat_messages_sn ON chat_messages(room_id, sn);
`

// schemaV3 holds the operator documents (documents.go): numbered versions of each operator data file, at most one
// published per name, and the audit trail of staff changes. Only new tables: every v1 and v2 row carries over.
const schemaV3 = `
CREATE TABLE documents (
	id        INTEGER PRIMARY KEY,
	name      TEXT NOT NULL,
	version   INTEGER NOT NULL,
	state     TEXT NOT NULL CHECK (state IN ('draft', 'published', 'retired')),
	body      TEXT NOT NULL,
	sha256    TEXT NOT NULL,
	author    TEXT NOT NULL,
	note      TEXT NOT NULL,
	created   TEXT NOT NULL,
	published TEXT NOT NULL DEFAULT '',
	replaces  INTEGER NOT NULL DEFAULT 0,
	UNIQUE (name, version)
) STRICT;
CREATE UNIQUE INDEX documents_published ON documents(name) WHERE state = 'published';

CREATE TABLE audit (
	id     INTEGER PRIMARY KEY,
	time   TEXT NOT NULL,
	actor  TEXT NOT NULL,
	action TEXT NOT NULL,
	target TEXT NOT NULL,
	detail TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX audit_target ON audit(target, id);
`

// schemaV4 records every stored player picture (web/media.go) with the avatar id of the account that uploaded it, so a quota
// counts one account's pictures and a removal finds the files that go with a post or a player. Only a new table.
const schemaV4 = `
CREATE TABLE media_files (
	id      INTEGER PRIMARY KEY,
	kind    TEXT NOT NULL CHECK (kind IN ('diary', 'room', 'profile')),
	owner   TEXT NOT NULL,
	file    TEXT NOT NULL UNIQUE,
	size    INTEGER NOT NULL CHECK (size > 0),
	created TEXT NOT NULL
) STRICT;
CREATE INDEX media_files_owner ON media_files(owner, kind, id);
`

// schemaV5 keeps the player chat log (chatlog.go): every message a player typed in a chat channel, with the time, the
// channel, the sender and the text, for the staff chat log and its retention sweep. Only a new table and its indexes.
const schemaV5 = `
CREATE TABLE chat_log (
	id          INTEGER PRIMARY KEY,
	time        INTEGER NOT NULL,
	kind        TEXT NOT NULL CHECK (kind IN ('private', 'lounge', 'circle', 'square', 'party')),
	channel     TEXT NOT NULL,
	sender      TEXT NOT NULL,
	sender_name TEXT NOT NULL,
	text        TEXT NOT NULL
) STRICT;
CREATE INDEX chat_log_time ON chat_log(time);
CREATE INDEX chat_log_sender ON chat_log(sender, id);
CREATE INDEX chat_log_channel ON chat_log(kind, channel, time);
`

// schemaV6 indexes the two columns the staff Insights pages (insights.go) cut by time: the ledger's time, so the economy window
// and the per-day series read one range of it, and the accounts' creation time, for the sign-up series. Only indexes.
const schemaV6 = `
CREATE INDEX ledger_time ON ledger(time);
CREATE INDEX accounts_created ON accounts(created);
`

// schemaV7 keeps every staff mail send (economy/staffmail.go): one row per send, its send id unique so a double submit cannot
// send twice, who sent it, to whom (player:<row id> or all-players), what it carried (kind, amount or item), its text and its
// reason. Only a new table.
const schemaV7 = `
CREATE TABLE staff_mail (
	id           INTEGER PRIMARY KEY,
	send_id      TEXT NOT NULL UNIQUE,
	created      TEXT NOT NULL,
	actor        TEXT NOT NULL,
	target       TEXT NOT NULL,
	recipients   INTEGER NOT NULL,
	skipped      INTEGER NOT NULL,
	kind         TEXT NOT NULL,
	amount       INTEGER NOT NULL,
	item         TEXT NOT NULL,
	message      TEXT NOT NULL,
	expires_days INTEGER NOT NULL,
	reason       TEXT NOT NULL
) STRICT;
`

// schemaV8 keeps every deleted avatar (deletedavatars.go): one row per Delete Avatar, with the whole record of the avatar as
// JSON in data. aid is the avatar id (never reused, so it names the avatar for good); account_id is the accounts row the
// avatar belonged to, NULL once that row is gone (lab accounts are deleted). identity is the Discord identity the avatar
// belonged to ("" for lab accounts), which the player page matches. The game only inserts rows (and removes the one a failed
// delete made); records kept in deleted-accounts.jsonl before this step are imported once (deletedavatars.go). Only new objects.
const schemaV8 = `
CREATE TABLE deleted_avatars (
	id         INTEGER PRIMARY KEY,
	aid        TEXT NOT NULL,
	account_id INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
	identity   TEXT NOT NULL DEFAULT '',
	name       TEXT NOT NULL DEFAULT '',
	deleted_at TEXT NOT NULL,
	data       TEXT NOT NULL
) STRICT;
CREATE INDEX deleted_avatars_aid ON deleted_avatars(aid);
CREATE INDEX deleted_avatars_identity ON deleted_avatars(identity, id);
`

// schemaV9 keeps every player report (reports.go): one row per report a player filed, with the reporter (always the
// authenticated account), the target, the reason code as the client sent it, the reporter's own text, the reference ids,
// a JSON snapshot of what was reported, and the staff handling (status, who, when, the note). Only new objects.
const schemaV9 = `
CREATE TABLE reports (
	id           INTEGER PRIMARY KEY,
	created      TEXT NOT NULL,
	source       TEXT NOT NULL CHECK (source IN ('chat', 'chat_img', 'tchat', 'diary', 'diary_comment', 'guestbook', 'guestbook_reply', 'circle', 'square', 'roomparty')),
	reporter_aid TEXT NOT NULL,
	target_aid   TEXT NOT NULL,
	reason       TEXT NOT NULL,
	message      TEXT NOT NULL DEFAULT '',
	ref_id       TEXT NOT NULL DEFAULT '',
	ref_detail   TEXT NOT NULL DEFAULT '',
	context      TEXT NOT NULL DEFAULT '',
	status       TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'dismissed')),
	handled_by   TEXT NOT NULL DEFAULT '',
	handled_at   TEXT NOT NULL DEFAULT '',
	note         TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX reports_status ON reports(status, id);
CREATE INDEX reports_target ON reports(target_aid);
`

// schemaV10 marks a restored deleted avatar (avatarrestore.go): restored_at is the time of the restore ("" = not restored) and
// restored_account the accounts row it went to (NULL once that row is gone). The record itself is not changed. Only new columns.
const schemaV10 = `
ALTER TABLE deleted_avatars ADD COLUMN restored_at TEXT NOT NULL DEFAULT '';
ALTER TABLE deleted_avatars ADD COLUMN restored_account INTEGER REFERENCES accounts(id) ON DELETE SET NULL;
`

// Path is the file the database was opened from.
func (d *DB) Path() string { return d.path }
