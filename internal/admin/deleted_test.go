package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/store"
)

// seedDeleted keeps one deleted avatar row with record rec (what a Delete Avatar keeps) and returns its row id.
func seedDeleted(t *testing.T, aid, identity, name string, rec store.AvatarRecord) int64 {
	t.Helper()
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := store.AttachedDB().Update(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO deleted_avatars (aid, identity, name, deleted_at, data) VALUES (?, ?, ?, ?, ?)`,
			aid, identity, name, "2026-11-10T08:00:00Z", string(data))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedDeletedPicture stores a diary picture of owner and returns its row id and file name (the picture is a file in the folder).
func seedDeletedPicture(t *testing.T, owner, userID, ctime string) (int64, string) {
	t.Helper()
	oid := userID + "_" + ctime
	file := store.MediaFileName(userID, ctime, oid)
	dir, err := store.MediaDir(store.MediaDiary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte("snap"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMediaFile(store.MediaFile{Kind: store.MediaDiary, Owner: owner, File: file, Size: 4}); err != nil {
		t.Fatal(err)
	}
	row, found, err := store.MediaRowByFile(file)
	if err != nil || !found {
		t.Fatalf("row of %s: found %t (err %v)", file, found, err)
	}
	return row.ID, file
}

// goneRecord is the record of avatar 8401 ("Gone One"): one of each kind of its data, with one picture that a post shows.
func goneRecord(t *testing.T) (store.AvatarRecord, int64) {
	t.Helper()
	pid, file := seedDeletedPicture(t, "8401", "8401", "1760000700")
	rec := store.AvatarRecord{
		Time: "2026-11-10T08:00:00Z",
		Account: store.StoredAccount{Aid: "8401", Name: "Gone One", Gems: 12, Cash: 3, AvatarCreated: 1700000000000,
			ItemCodes: []string{"CUAE0001A"}, InventoryCodes: []string{"CUAE0001A", "CUAE0002B"},
			Pets:   []store.PetItem{{Name: "Mo", Cd: "PET001"}},
			Mail:   []store.MailRow{{Type: "gems", Amount: 5, Message: "welcome back", Sender: "staff", Created: 1700000005000}},
			Smiles: 6},
		Posts: []store.DiaryPost{{DiaryNo: "9001", AvatarID: "8401", RegDate: "1700000000000", Content: "a deleted post",
			Images: []store.DiaryImage{{ImageURL: "/lineplay/d/download.nhn?oid=8401_1760000700&ctime=1760000700&userid=8401"}}}},
		Guestbook:   []store.SavedGuestbookEntry{{HostAvtNo: "8401", GuestHistNo: "77", GuestAvtNo: "9100", Content: "a guest wrote", RegYmdt: "1700000001000"}},
		Comments:    []store.DiaryComment{{CommentNo: "5", DiaryNo: "9001", OwnerAvatarID: "8401", CommentAvatarID: "9100", Content: "nice post", Date: "1700000002000"}},
		Likes:       []store.DiaryLike{{LikeNo: "3", DiaryNo: "9001", OwnerAvatarID: "8401", LikeAvatarID: "9100"}},
		Friendships: []store.Friendship{{A: "8401", B: "9100", State: "accepted"}},
		ChatLog:     []store.AvatarChatLine{{Time: 1700000003000, Kind: "lounge", Channel: "7", Sender: "8401", SenderName: "Gone One", Text: "lounge words"}},
		ChatRooms: []store.ChatRoom{{ID: 1960000005, Aids: [2]string{"8401", "9100"},
			Msgs: []store.ChatMsg{{Sn: 1, Sec: 1700000004, Sender: "9100", Text: "private hello"}}}},
		Treasure: &store.TreasureRecord{Season: 4, Position: 12, Jewels: 9},
		Circle:   &store.CircleMembership{ID: 55, Name: "Old Club", Role: "MEMBER"},
		Media:    []store.MediaFile{{ID: pid, Kind: store.MediaDiary, Owner: "8401", File: file, Size: 4}},
	}
	return rec, pid
}

// TestDeletedPagesNeedASession: the list and the record refuse a visitor without a session, like the other pages.
func TestDeletedPagesNeedASession(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	expect(t, c.get("/players/deleted"), http.StatusUnauthorized)
	expect(t, c.get("/players/deleted/1"), http.StatusUnauthorized)
}

// TestDeletedListAndRecordPages: the list shows the deleted avatars newest first with their links; the record shows every part
// the delete kept and the live ledger lines of its avatar id; its pictures are served by the photo route.
func TestDeletedListAndRecordPages(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	rec, pid := goneRecord(t)
	id := seedDeleted(t, "8401", "dev-gone1", "Gone One", rec)
	seedDeleted(t, "8402", "dev-gone2", "Gone Two", store.AvatarRecord{Account: store.StoredAccount{Aid: "8402"}})
	if err := store.AttachedDB().Update(func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO ledger (time, aid, currency, delta, balance, reason) VALUES ('2026-11-01T10:00:00Z', '8401', 'gems', 7, 19, 'lab note gems')`,
			`INSERT INTO ledger (time, aid, currency, delta, balance, reason) VALUES ('2026-11-01T10:05:00Z', '9999', 'gems', 1, 1, 'someone else')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	list := c.get("/players/deleted")
	expect(t, list, http.StatusOK)
	checkPage(t, "/players/deleted", list)
	if i, j := strings.Index(list.body, "Gone Two"), strings.Index(list.body, "Gone One"); i < 0 || j < 0 || i > j {
		t.Fatalf("the list is not newest first (Gone Two at %d, Gone One at %d)", i, j)
	}
	if !strings.Contains(list.body, `href="/players/deleted/`+strconv.FormatInt(id, 10)+`"`) {
		t.Fatal("the list has no link to the record")
	}
	if strings.Contains(list.body, `action="/players/deleted`) {
		t.Fatal("the list offers a change to the records: they are read-only")
	}

	page := c.get("/players/deleted/" + strconv.FormatInt(id, 10))
	expect(t, page, http.StatusOK)
	checkPage(t, "record", page)
	for _, want := range []string{
		`id="overview"`, `id="inventory"`, `id="writing"`, `id="pictures"`, `id="chat"`, `id="treasure"`, `id="ledger"`,
		"Gone One", "a deleted post", "nice post", "a guest wrote", "lounge words", "private hello", "Old Club",
		"Season", "welcome back", "lab note gems", "CUAE0001A", "Mo",
		"/photos/" + strconv.FormatInt(pid, 10) + "/file", // the post's picture, by row id
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the record page has no %q", want)
		}
	}
	if strings.Contains(page.body, "someone else") {
		t.Error("the record page shows a ledger line of another avatar")
	}
	if strings.Contains(page.body, `action="/players/deleted`) {
		t.Error("the record page offers a change to the record: it is read-only")
	}

	expect(t, c.get("/players/deleted/999999"), http.StatusNotFound)
	expect(t, c.get("/players/deleted/abc"), http.StatusBadRequest)
	expect(t, c.get("/players/deleted?before=abc"), http.StatusBadRequest)
	if r := c.post("/players/deleted/"+strconv.FormatInt(id, 10), url.Values{"csrf": {c.csrf()}}); r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST of a record: status %d, want 405", r.StatusCode)
	}
}

// TestPastAvatarsOnThePlayerPage: a live player's page lists the deleted avatars of its identity, with links to their records,
// and no others.
func TestPastAvatarsOnThePlayerPage(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-past1", "8501", "Live Past", 0)
	mine := seedDeleted(t, "8410", "dev-past1", "Earlier One", store.AvatarRecord{Account: store.StoredAccount{Aid: "8410"}})
	seedDeleted(t, "8411", "dev-other", "Stranger", store.AvatarRecord{Account: store.StoredAccount{Aid: "8411"}})

	page := c.get(playerURL(id))
	expect(t, page, http.StatusOK)
	checkPage(t, "player page", page)
	if !strings.Contains(page.body, `id="past-avatars"`) {
		t.Fatal("the player page has no Past avatars section")
	}
	if !strings.Contains(page.body, `href="/players/deleted/`+strconv.FormatInt(mine, 10)+`"`) || !strings.Contains(page.body, "Earlier One") {
		t.Fatal("the Past avatars section does not list the identity's deleted avatar")
	}
	if strings.Contains(page.body, "Stranger") {
		t.Fatal("the Past avatars section lists another identity's avatar")
	}
}

// TestSearchFindsDeletedAvatars: the header search finds a deleted avatar by its exact avatar id and by part of its name (any case).
func TestSearchFindsDeletedAvatars(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedDeleted(t, "8421", "dev-srch", "Searchable Name", store.AvatarRecord{Account: store.StoredAccount{Aid: "8421"}})
	link := `href="/players/deleted/` + strconv.FormatInt(id, 10) + `"`

	byAid := c.get("/search?q=8421")
	expect(t, byAid, http.StatusOK)
	if !strings.Contains(byAid.body, link) {
		t.Fatal("the search by avatar id does not find the deleted avatar")
	}
	byName := c.get("/search?q=" + url.QueryEscape("CHABLE NAM"))
	if !strings.Contains(byName.body, link) {
		t.Fatal("the search by a part of the name (any case) does not find the deleted avatar")
	}
	if none := c.get("/search?q=zzz-none"); !strings.Contains(none.body, "No deleted avatar matches.") {
		t.Fatal("the search reports a match for nothing")
	}
}

// TestDeletedAvatarPictureRemovalFromPhotos: a picture of a deleted avatar is removed from the Photos page like any other: the row
// and the file go, and the audit row names the avatar.
func TestDeletedAvatarPictureRemovalFromPhotos(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	pid, file := seedDeletedPicture(t, "8431", "8431", "1760000800")
	seedDeleted(t, "8431", "dev-pic", "Snapper", store.AvatarRecord{Account: store.StoredAccount{Aid: "8431"}})

	if page := c.get("/photos?player=8431"); !strings.Contains(page.body, "/photos/"+strconv.FormatInt(pid, 10)+"/remove") {
		t.Fatal("the Photos page does not offer the removal of the deleted avatar's picture")
	}
	expect(t, c.post("/photos/"+strconv.FormatInt(pid, 10)+"/remove", url.Values{"csrf": {c.csrf()}, "reason": {strings.Repeat("r", 201)}}), http.StatusBadRequest)
	expect(t, c.post("/photos/"+strconv.FormatInt(pid, 10)+"/remove", url.Values{"csrf": {c.csrf()}, "reason": {"old snap"}}), http.StatusSeeOther)
	if _, found, _ := store.MediaRowByFile(file); found {
		t.Fatal("the removed picture's row remains")
	}
	dir, _ := store.MediaDir(store.MediaDiary)
	if _, err := os.Stat(filepath.Join(dir, file)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the removed picture's file remains: %v", err)
	}
	found := false
	for _, r := range auditRows(t) {
		if r.Action == "remove-media" && r.Target == "avatar:8431" && strings.Contains(r.Detail, "old snap") {
			found = true
		}
	}
	if !found {
		t.Fatal("no remove-media audit row for the deleted avatar")
	}
	expect(t, c.post("/photos/"+strconv.FormatInt(pid, 10)+"/remove", url.Values{"csrf": {c.csrf()}, "reason": {"again"}}), http.StatusNotFound)
}
