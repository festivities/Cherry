package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeDataDir writes a synthetic data directory in the server's own file formats: three accounts (two with aid "0"),
// one alias each, every stored account field filled, a diary post, a guestbook entry with a reply, a friendship, a
// circle, a Treasure World record, a chat room with messages and a ledger.
func writeDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, v any) {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	accounts := savedAccounts{Version: 1, Accounts: map[string]StoredAccount{}, Aliases: map[string]string{}, NextAvatarID: 1100}
	for i := 0; i < 3; i++ {
		acc, own, alias, _ := syntheticAccount(i)
		if i > 0 {
			acc.Aid = "0" // an account with no avatar yet
		}
		rec := StoredFrom(acc)
		rec.AccessToken = own // the legacy file keeps the clear token as the record key
		accounts.Accounts[own] = rec
		accounts.Aliases[own] = own
		accounts.Aliases[alias] = own
		if i == 0 {
			accounts.Latest = own
		}
	}
	write("accounts.json", accounts)

	var rec TreasureRecord
	fillSynthetic(reflect.ValueOf(&rec).Elem())
	write("treasure.json", savedTreasure{Version: 1, Accounts: map[string]TreasureRecord{"1001": rec}})
	write("social.json", savedSocial{
		Version: 1, NextDiaryNo: 3, NextGuestHistNo: 2, NextCommentNo: 1, NextLikeNo: 1, NextReplyHistNo: 5,
		Posts:         []DiaryPost{{DiaryNo: "1", AvatarID: "1001", Nickname: "n", Images: []DiaryImage{{ImageURL: "u"}}}},
		Guestbook:     []SavedGuestbookEntry{{HostAvtNo: "1001", GuestHistNo: "1", GuestAvtNo: "1002", Content: "hi", Reply: []GuestbookReply{{ReplyHistNo: "5", ParentHistNo: "1", ReplyAvtNo: "1001", Content: "re"}}}},
		Friendships:   []Friendship{{A: "1001", B: "1002", State: "accepted"}},
		Comments:      []DiaryComment{{CommentNo: "1", DiaryNo: "1", OwnerAvatarID: "1001", CommentAvatarID: "1002", Content: "c"}},
		Likes:         []DiaryLike{{LikeNo: "1", DiaryNo: "1", OwnerAvatarID: "1001", LikeAvatarID: "1002", LikeType: 2}},
		GuestbookRead: map[string]uint64{"1001": 1},
	})
	write("circles.json", savedCircles{Version: 1, CircleBook: CircleBook{
		NextID:  1,
		Circles: []Circle{{ID: 1, Code: "c", Name: "n", JoinType: "OPEN", Created: 1, Modified: 1, Members: []CircleMember{{Aid: "1001", Role: "leader", Joined: 1}}}},
		Leaves:  map[string]int64{"1002": 5},
	}})
	write("chatrooms.json", savedChatRooms{Version: 1, ChatRoomBook: ChatRoomBook{NextID: 1960000002, Rooms: []ChatRoom{{
		ID: 1960000001, Aids: [2]string{"1001", "1002"}, Ceil: 2, Acks: map[string]int64{"1001": 0, "1002": 2},
		Msgs: []ChatMsg{{Sn: 1, Sec: 1700000000, Sender: "1001", Text: "hi"}, {Sn: 2, Sec: 1700000001, Sender: "1002", Text: "yo"}},
	}}}})

	var ledger []byte
	for _, l := range []LedgerLine{
		{Time: "2026-11-11T00:00:00Z", Aid: "1001", Currency: "gems", Delta: 300, Balance: 300, Reason: "welcome"},
		{Time: "2026-11-11T00:01:00Z", Aid: "1001", Currency: "gems", Delta: -50, Balance: 250, Reason: "buy"},
		{Time: "2026-11-11T00:02:00Z", Aid: "1002", Currency: "smiles", Delta: 4, Balance: 4, Reason: "gather"},
		{Time: "2026-11-11T00:03:00Z", Aid: "1002", Currency: "cash", Delta: 1, Balance: 1, Reason: "test"},
	} {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		ledger = append(append(ledger, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "ledger.jsonl"), ledger, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// hashDir is the SHA-256 of every file in dir, by name: the proof that the import wrote nothing there.
func hashDir(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][32]byte{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = sha256.Sum256(data)
	}
	return out
}

// TestImportDirEndToEnd: a synthetic data directory imports, the database reads back equal, the counts are right and the
// data directory is byte for byte unchanged. The loaders' process-wide state is put back afterwards.
func TestImportDirEndToEnd(t *testing.T) {
	dir := writeDataDir(t)
	before := hashDir(t, dir)
	path := filepath.Join(t.TempDir(), DBFileName)
	if err := PrepareDBPath(path, false); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rep, err := ImportDir(d, dir)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	want := ImportReport{
		Accounts: 3, Tokens: 6, LedgerLines: 4, DiaryPosts: 1, DiaryComments: 1, DiaryLikes: 1,
		GuestbookEntries: 1, GuestbookReplies: 1, GuestbookRead: 1, Friendships: 1,
		Circles: 1, CircleLeaves: 1, TreasureRecords: 1, ChatRooms: 1, ChatMessages: 2,
	}
	if !reflect.DeepEqual(rep, want) {
		t.Fatalf("report = %+v\nwant     %+v", rep, want)
	}
	if !reflect.DeepEqual(before, hashDir(t, dir)) {
		t.Fatal("the import changed the data directory")
	}
	if len(accountList) != 0 || LatestAcc != nil || database.Load() != nil || len(DiaryPosts) != 0 {
		t.Fatal("the importer changed the process-wide state")
	}
}

// TestPrepareDBPath: an existing non-empty database is refused without force and replaced with it, together with its
// WAL and shared-memory files; a stale WAL without its database is refused.
func TestPrepareDBPath(t *testing.T) {
	dir := writeDataDir(t)
	path := filepath.Join(t.TempDir(), DBFileName)
	d, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportDir(d, dir); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if err := PrepareDBPath(path, false); err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("an existing database was accepted without -force: %v", err)
	}
	if err := PrepareDBPath(path, true); err != nil {
		t.Fatalf("force: %v", err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived -force", filepath.Base(name))
		}
	}
	if err := os.WriteFile(path+"-wal", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareDBPath(path, false); err == nil {
		t.Fatal("a stale WAL without its database was accepted")
	}
}

// TestVerifyCatchesDifference: a change made in the database behind the import is reported with the store and key.
func TestVerifyCatchesDifference(t *testing.T) {
	dir := writeDataDir(t)
	path := filepath.Join(t.TempDir(), DBFileName)
	d, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := ImportDir(d, dir); err != nil {
		t.Fatal(err)
	}
	if err := d.Update(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE meta SET value = '999' WHERE key = 'social.nextDiaryNo'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	in, err := loadDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyDB(d, in)
	if err == nil || !strings.Contains(err.Error(), "social differs at social.nextDiaryNo") {
		t.Fatalf("verify = %v, want a social difference at social.nextDiaryNo", err)
	}
}

// TestJSONPath pins the path format of the first difference: keys and indexes, no values.
func TestJSONPath(t *testing.T) {
	var a, b any
	if err := json.Unmarshal([]byte(`{"posts":[{"diaryNo":"1"},{"diaryNo":"2"}]}`), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"posts":[{"diaryNo":"1"},{"diaryNo":"3"}]}`), &b); err != nil {
		t.Fatal(err)
	}
	if got := jsonPath("social", a, b); got != "social.posts[1].diaryNo" {
		t.Fatalf("path = %s", got)
	}
	if got := jsonPath("social", a, a); got != "" {
		t.Fatalf("equal values gave path %s", got)
	}
}

// TestLoadErrorShowsNoContent: a file that is not JSON fails with the file name and the byte offset only.
func TestLoadErrorShowsNoContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), []byte(`{"accessToken":secretvalue`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadDataDir(dir)
	if err == nil || !strings.Contains(err.Error(), "accounts.json: invalid JSON at byte") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error = %v", err)
	}
}
