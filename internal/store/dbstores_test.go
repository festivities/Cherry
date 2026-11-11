package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// socialJSON is the canonical text of the social store in memory (the round-trip comparison).
func socialJSON(t *testing.T) string {
	t.Helper()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	b, err := json.Marshal(socialSnapshotLocked())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSocialRowsRoundTrip: every kind of social change is saved by the rows it names (a post, a post in front of it, a
// comment, a like, an entry, a reply, a friendship, a read marker, an erase of a post with its comment and like), and the
// store after a restart is the store before it.
func TestSocialSaveRoundTrip(t *testing.T) {
	path := swapStore(t)
	t.Cleanup(ResetSocial)
	save := func(c SocialChange) {
		t.Helper()
		SocialMu.Lock()
		defer SocialMu.Unlock()
		if err := SaveSocialLocked(c); err != nil {
			t.Fatalf("save %+v: %v", c, err)
		}
	}
	SocialMu.Lock()
	DiaryPosts = []DiaryPost{{DiaryNo: "1", AvatarID: "1001", Title: "one", Images: []DiaryImage{}}}
	NextDiaryNo = 1
	SocialMu.Unlock()
	save(SocialChange{Posts: []string{"1"}, Scalars: true})

	SocialMu.Lock()
	DiaryPosts = append([]DiaryPost{{DiaryNo: "2", AvatarID: "1001", Title: "two", Images: []DiaryImage{}}}, DiaryPosts...)
	NextDiaryNo = 2
	SocialMu.Unlock()
	save(SocialChange{Posts: []string{"2"}, Scalars: true})

	SocialMu.Lock()
	DiaryComments = []DiaryComment{{CommentNo: "1", DiaryNo: "1", OwnerAvatarID: "1001", CommentAvatarID: "1002", Content: "hi"}}
	NextCommentNo = 1
	SocialMu.Unlock()
	save(SocialChange{Comments: []string{"1"}, Scalars: true})

	SocialMu.Lock()
	DiaryLikes = []DiaryLike{{LikeNo: "1", DiaryNo: "1", OwnerAvatarID: "1001", LikeAvatarID: "1002", LikeType: 2}}
	NextLikeNo = 1
	SocialMu.Unlock()
	save(SocialChange{Likes: []string{"1"}, Scalars: true})

	SocialMu.Lock()
	GuestbookEntries = []SavedGuestbookEntry{{HostAvtNo: "1001", GuestHistNo: "1", GuestAvtNo: "1002", Content: "book", Reply: []GuestbookReply{}}}
	NextGuestHistNo = 1
	SocialMu.Unlock()
	save(SocialChange{Entries: []string{"1"}, Scalars: true})
	SocialMu.Lock()
	GuestbookEntries[0].Reply = append(GuestbookEntries[0].Reply, GuestbookReply{ReplyHistNo: "1", ParentHistNo: "1", ReplyAvtNo: "1001", Content: "re"})
	NextReplyHistNo = 1
	SocialMu.Unlock()
	save(SocialChange{Replies: []string{"1"}, Scalars: true})

	SocialMu.Lock()
	Friendships = []Friendship{{A: "1001", B: "1002", State: "pending"}}
	SocialMu.Unlock()
	save(SocialChange{Friends: [][2]string{{"1002", "1001"}}})

	SocialMu.Lock()
	GuestbookRead = map[string]uint64{"1001": 1}
	SocialMu.Unlock()
	save(SocialChange{Reads: []string{"1001"}})

	// Erase post 1 with its comment and its like, and mark the friendship accepted.
	SocialMu.Lock()
	DiaryPosts = DiaryPosts[:1]
	DiaryComments = []DiaryComment{}
	DiaryLikes = []DiaryLike{}
	Friendships[0].State = "accepted"
	SocialMu.Unlock()
	save(SocialChange{Posts: []string{"1"}, Comments: []string{"1"}, Likes: []string{"1"}, Friends: [][2]string{{"1001", "1002"}}})
	want := socialJSON(t)

	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseStore() })
	if got := socialJSON(t); got != want {
		t.Fatalf("social after a restart differs:\n got %s\nwant %s", got, want)
	}
}

// TestSafetyNetTripsOnMissedSocialRow: a save that names one post while another changed panics with the kind, the key and
// the save site, and none of the content.
func TestSafetyNetTripsOnMissedSocialRow(t *testing.T) {
	swapStore(t)
	SocialMu.Lock()
	defer SocialMu.Unlock()
	DiaryPosts = []DiaryPost{{DiaryNo: "1", AvatarID: "1001", Title: "first"}}
	NextDiaryNo = 1
	if err := SaveSocialLocked(SocialChange{Posts: []string{"1"}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
	DiaryPosts[0].Title = "secretcontent" // a handler that changes post 1 and names only the new post
	DiaryPosts = append([]DiaryPost{{DiaryNo: "2", AvatarID: "1001", Title: "second"}}, DiaryPosts...)
	NextDiaryNo = 2
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		SaveSocialLocked(SocialChange{Posts: []string{"2"}, Scalars: true})
	}()
	msg, _ := recovered.(string)
	if !strings.Contains(msg, "diary post 1") || !strings.Contains(msg, "dbstores_test.go:") || strings.Contains(msg, "secretcontent") {
		t.Fatalf("safety net panic = %v, want the kind, the key and the save site without content", recovered)
	}
}

// TestMigrationV1ToV2KeepsRows: a database written at schema v1 (every table of the stores with rows) is migrated to v2
// with every row in place and the new unique keys enforced; a v1 table whose rows repeat a key stops the migration, and the
// database stays at v1.
func TestMigrationV1ToV2KeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), DBFileName)
	raw, err := sql.Open("sqlite", path+"?"+dbPragmas)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(raw, 1); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO diary_posts (diary_no, avatar_id, data) VALUES ('1', '1001', '{}')`,
		`INSERT INTO diary_comments (comment_no, diary_no, data) VALUES ('1', '1', '{}')`,
		`INSERT INTO diary_likes (like_no, diary_no, like_avatar_id, data) VALUES ('1', '1', '1002', '{}')`,
		`INSERT INTO guestbook_entries (host_avt_no, guest_hist_no, data) VALUES ('1001', '1', '{}')`,
		`INSERT INTO guestbook_replies (entry_id, reply_hist_no, data) VALUES (1, '1', '{}')`,
		`INSERT INTO friendships (a, b, state) VALUES ('1001', '1002', 'accepted')`,
		`INSERT INTO circles (circle_id, data) VALUES (1, '{}')`,
		`INSERT INTO treasure_records (aid, data) VALUES ('1001', '{}')`,
		`INSERT INTO chat_rooms (room_id, aid_a, aid_b, data) VALUES (1960000001, '1001', '1002', '{}')`,
		`INSERT INTO chat_messages (room_id, sn, sec, sender, text) VALUES (1960000001, 1, 1, '1001', 'hi')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDB(path)
	if err != nil {
		t.Fatalf("migrate v1 to v2: %v", err)
	}
	defer d.Close()
	if err := d.View(func(q Querier) error {
		var v int
		if err := q.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != len(migrations) {
			return fmt.Errorf("user_version = %d (%v), want %d", v, err, len(migrations))
		}
		for _, table := range []string{"diary_posts", "diary_comments", "diary_likes", "guestbook_entries", "guestbook_replies", "friendships", "circles", "treasure_records", "chat_rooms", "chat_messages"} {
			var n int
			if err := q.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 1 {
				return fmt.Errorf("%s has %d rows after the migration (%v), want 1", table, n, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The new keys: a second diary post with number 1 is refused.
	if _, err := d.write.Exec(`INSERT INTO diary_posts (diary_no, avatar_id, data) VALUES ('1', '1003', '{}')`); err == nil {
		t.Fatal("a duplicate diary number was accepted after the migration")
	}

	// A v1 table that repeats a key stops the migration: nothing is dropped or merged, and the version stays at v1.
	dup := filepath.Join(t.TempDir(), DBFileName)
	raw, err = sql.Open("sqlite", dup+"?"+dbPragmas)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(raw, 1); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO diary_posts (diary_no, avatar_id, data) VALUES ('7', '1001', '{}')`,
		`INSERT INTO diary_posts (diary_no, avatar_id, data) VALUES ('7', '1002', '{}')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	if _, err := OpenDB(dup); err == nil {
		t.Fatal("a v1 database with a repeated diary number migrated")
	}
	check, err := sql.Open("sqlite", dup)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var v, n int
	if err := check.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("user_version after a refused migration = %d (%v), want 1", v, err)
	}
	if err := check.QueryRow(`SELECT COUNT(*) FROM diary_posts`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows after a refused migration = %d (%v), want 2", n, err)
	}
}

// storeRowsText is the accounts, tokens, ledger and account meta of d as text: what the -stores import must leave alone.
func storeRowsText(t *testing.T, d *DB) string {
	t.Helper()
	var out strings.Builder
	if err := d.View(func(q Querier) error {
		for _, query := range []string{
			`SELECT id, aid, data_hash FROM accounts ORDER BY id`,
			`SELECT hash, account_id, kind FROM tokens ORDER BY hash`,
			`SELECT time, aid, delta, balance, reason FROM ledger ORDER BY id`,
			`SELECT key, value FROM meta WHERE key LIKE 'accounts.%' ORDER BY key`,
		} {
			rows, err := q.Query(query)
			if err != nil {
				return err
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					rows.Close()
					return err
				}
				fmt.Fprintln(&out, vals...)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// TestImportStoresReplacesOnlyTheNamedStores: -stores replaces the named store's rows from its file and leaves the accounts,
// tokens, ledger and account meta exactly as they were; a database with no accounts and an unknown store name are refused.
func TestImportStoresReplacesOnlyTheNamedStores(t *testing.T) {
	dir := writeDataDir(t)
	path := filepath.Join(t.TempDir(), DBFileName)
	if err := PrepareDBPath(path, false); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := ImportDir(d, dir); err != nil {
		t.Fatalf("full import: %v", err)
	}
	before := storeRowsText(t, d)

	// A new social.json with one post; the other stores' files stay as they were.
	social := savedSocial{Version: 1, Posts: []DiaryPost{{DiaryNo: "9", AvatarID: "1001", Title: "re-imported"}}, NextDiaryNo: 9}
	raw, err := json.Marshal(social)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "social.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := ImportStores(d, dir, []string{"social"})
	if err != nil {
		t.Fatalf("ImportStores: %v", err)
	}
	if rep.DiaryPosts != 1 {
		t.Fatalf("report diary posts = %d, want 1", rep.DiaryPosts)
	}
	if got := storeRowsText(t, d); got != before {
		t.Fatalf("accounts, tokens, ledger or account meta changed:\n got %s\nwant %s", got, before)
	}
	if err := d.View(func(q Querier) error {
		s, err := readSocial(q)
		if err != nil {
			return err
		}
		if len(s.Posts) != 1 || s.Posts[0].DiaryNo != "9" || len(s.Guestbook) != 0 {
			return fmt.Errorf("social after the import: %d posts, %d entries", len(s.Posts), len(s.Guestbook))
		}
		circles, err := readCircles(q)
		if err != nil {
			return err
		}
		if len(circles.Circles) != 1 {
			return fmt.Errorf("circles changed: %d", len(circles.Circles))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ImportStores(d, dir, []string{"lottery"}); err == nil {
		t.Fatal("an unknown store was accepted")
	}
	empty, err := OpenDB(filepath.Join(t.TempDir(), DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if _, err := ImportStores(empty, dir, []string{"social"}); err == nil || !strings.Contains(err.Error(), "no accounts") {
		t.Fatalf("a database with no accounts = %v, want a refusal", err)
	}
}

// TestCheckImportedRefusesEmptyStoreWithFile: the startup guard refuses a database whose social rows are missing while
// social.json holds rows, names cherry-import -stores on one line, and lets a database that holds the rows, or a directory
// without the files, start.
func TestCheckImportedRefusesEmptyStoreWithFile(t *testing.T) {
	dir := writeDataDir(t)
	swapStore(t) // an attached database with no rows
	err := CheckImported(dir)
	if err == nil || !strings.Contains(err.Error(), "cherry-import -data-dir") || !strings.Contains(err.Error(), "-stores social") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("CheckImported with empty stores = %v, want a one-line refusal that names -stores", err)
	}
	if err := CheckImported(t.TempDir()); err != nil {
		t.Fatalf("CheckImported without JSON files: %v", err)
	}

	full := filepath.Join(t.TempDir(), DBFileName)
	if err := PrepareDBPath(full, false); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDB(full)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportDir(d, dir); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CloseStore(); err != nil { // the attached database from swapStore
		t.Fatal(err)
	}
	if err := OpenStore(full); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseStore() })
	if err := CheckImported(dir); err != nil {
		t.Fatalf("CheckImported with the stores imported: %v", err)
	}
}

// refuseWrites makes the attached database refuse every write to table (a trigger that aborts it); the test's cleanup drops
// nothing, because the database is closed with the test.
func refuseWrites(t *testing.T, table string) {
	t.Helper()
	if err := AttachedDB().Update(func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER refuse_` + table + ` BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'refused'); END`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`CREATE TRIGGER refuse_update_` + table + ` BEFORE UPDATE ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'refused'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestTreasureUpdateRollsBack: a board change the database refuses is not kept in memory, for an existing board and for a
// new one.
func TestTreasureUpdateRollsBack(t *testing.T) {
	swapStore(t)
	t.Cleanup(ResetTreasure)
	if _, err := UpdateTreasure("1001", func(r *TreasureRecord) { r.Points = 5 }); err != nil {
		t.Fatal(err)
	}
	refuseWrites(t, "treasure_records")
	if _, err := UpdateTreasure("1001", func(r *TreasureRecord) { r.Points = 9 }); err == nil {
		t.Fatal("a refused update returned no error")
	}
	if _, err := UpdateTreasure("1002", func(r *TreasureRecord) { r.Points = 3 }); err == nil {
		t.Fatal("a refused new board returned no error")
	}
	if got, _ := TreasureRecordOf("1001"); got.Points != 5 {
		t.Fatalf("refused update kept in memory: points %d, want 5", got.Points)
	}
	if _, ok := TreasureRecordOf("1002"); ok {
		t.Fatal("a refused new board is in memory")
	}
}

// TestSafetyNetTripsOnMissedTreasureRecord: a board changed in memory and not named by the save panics with its aid and the
// save site, and none of the content.
func TestSafetyNetTripsOnMissedTreasureRecord(t *testing.T) {
	swapStore(t)
	t.Cleanup(ResetTreasure)
	if _, err := UpdateTreasure("1001", func(r *TreasureRecord) { r.Points = 1 }); err != nil {
		t.Fatal(err)
	}
	TreasureAccounts["1001"] = TreasureRecord{Points: 424242} // a handler that changes board 1001 and saves board 1002
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		UpdateTreasure("1002", func(r *TreasureRecord) { r.Points = 2 })
	}()
	msg, _ := recovered.(string)
	if !strings.Contains(msg, "treasure record 1001") || !strings.Contains(msg, "dbstores_test.go:") || strings.Contains(msg, "424242") {
		t.Fatalf("safety net panic = %v, want the kind, the key and the save site without content", recovered)
	}
}
