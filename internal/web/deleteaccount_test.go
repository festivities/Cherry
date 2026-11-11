package web

import (
	"cherry/internal/testutil"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

type delFixture struct {
	dir  string
	acc  *store.Account
	code int
	key  string
}

// delSetup installs two accounts (1 with two token aliases), file-backed stores, an accepted
// friendship, a diary post and guestbook rows, and fetches a delete code for "dtok".
func delSetup(t *testing.T) *delFixture {
	t.Helper()
	acc := &store.Account{AccessToken: "dtok", SessionKey: "sk", Aid: "9501", Name: "Del", Gender: "FEMALE", Gems: 5}
	other := &store.Account{AccessToken: "otok", SessionKey: "sk2", Aid: "9502", Name: "Other", Gender: "MALE"}
	installSocialTestAccounts(t, map[string]*store.Account{"dtok": acc, "dtok2": acc, "otok": other})
	dir := t.TempDir()
	store.AccountsMu.Lock()
	store.NextAvatarID = 9502
	store.LatestAcc = acc
	store.AccountsMu.Unlock()
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.SocialMu.Lock()
	store.Friendships = []store.Friendship{{A: "9501", B: "9502", State: "accepted"}}
	store.DiaryPosts = []store.DiaryPost{{DiaryNo: "1", AvatarID: "9501", Images: []store.DiaryImage{}}, {DiaryNo: "2", AvatarID: "9502", Images: []store.DiaryImage{}}}
	store.GuestbookEntries = []store.SavedGuestbookEntry{
		{HostAvtNo: "9502", GuestHistNo: "1", GuestAvtNo: "9501"}, // written by the deleted account
		{HostAvtNo: "9501", GuestHistNo: "2", GuestAvtNo: "9502"}, // written on it
		{HostAvtNo: "9502", GuestHistNo: "3", GuestAvtNo: "9502"},
	}
	store.GuestbookRead = map[string]uint64{"9501": 2, "9502": 3}
	store.SocialMu.Unlock()
	// The accounts and the social fixture above are in memory; attaching the database writes them.
	testutil.UseDatabaseAt(t, dir)
	f := &delFixture{dir: dir, acc: acc}
	rec := socialTestRequestAs(t, "dtok", http.MethodGet, "/v4/avatar/9501/removeCode", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("removeCode = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result struct {
			Key  string `json:"key"`
			Code int    `json:"code"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Result.Key == "" || body.Result.Code < 1000 || body.Result.Code > 9999 {
		t.Fatalf("removeCode body = %s", rec.Body.String())
	}
	f.key, f.code = body.Result.Key, body.Result.Code
	return f
}

func (f *delFixture) del(t *testing.T, token string) *httptest.ResponseRecorder {
	return socialTestRequestAs(t, token, http.MethodPost, "/v4/r/avatar/9501/"+f.key+"/"+strconv.Itoa(f.code), "null")
}

func TestDeleteAccountRemovesEverything(t *testing.T) {
	f := delSetup(t)
	rec := f.del(t, "dtok")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	store.AccountsMu.Lock()
	a1 := store.AccountByTokenLocked("dtok") != nil
	a2 := store.AccountByTokenLocked("dtok2") != nil
	latest, next := store.LatestAcc, store.NextAvatarID
	store.AccountsMu.Unlock()
	if a1 || a2 || latest == nil || latest.Aid != "9502" || next != 9502 {
		t.Fatalf("aliases %v %v latest %v next %d", a1, a2, latest, next)
	}
	if rec := checkSession(t, "dtok"); rec.Code != http.StatusNotFound {
		t.Fatalf("old token checkSession = %d", rec.Code)
	}
	if rec := checkSession(t, "dtok2"); rec.Code != http.StatusNotFound {
		t.Fatalf("old alias checkSession = %d", rec.Code)
	}
	store.SocialMu.Lock()
	if len(store.DiaryPosts) != 1 || store.DiaryPosts[0].AvatarID != "9502" || len(store.GuestbookEntries) != 1 || store.GuestbookEntries[0].GuestHistNo != "3" {
		t.Fatalf("social rows left: %+v %+v", store.DiaryPosts, store.GuestbookEntries)
	}
	if _, kept := store.GuestbookRead["9501"]; kept || store.GuestbookRead["9502"] != 3 {
		t.Fatalf("read markers = %v, want only 9502", store.GuestbookRead)
	}
	store.SocialMu.Unlock()

	// Stores on disk reload cleanly (latest valid, no deleted aid).
	if err := store.ReloadAccounts(); err != nil {
		t.Fatalf("reload accounts: %v", err)
	}
	// Backup line carries the full account and social rows.
	raw, err := os.ReadFile(filepath.Join(f.dir, "deleted-accounts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var b deletedBackup
	if err := json.Unmarshal(raw, &b); err != nil || b.Account.Aid != "9501" || b.Account.Gems != 5 || len(b.Tokens) != 2 ||
		len(b.Posts) != 1 || len(b.Guestbook) != 2 || len(b.Friendships) != 1 || b.GuestbookRead != 2 {
		t.Fatalf("backup = %s (%v)", raw, err)
	}
	if l := readLedger(t); len(l) != 1 || l[0].Reason != "account deleted" || l[0].Aid != "9501" {
		t.Fatalf("ledger = %+v", l)
	}
	// The friend's next sync tombstones the deleted aid (status -1).
	sync := socialTestRequestAs(t, "otok", http.MethodGet, "/v4/sync/friends/0", "")
	var sb struct {
		Result struct {
			Buddies []buddyRow `json:"buddyList"`
		} `json:"result"`
	}
	if err := json.Unmarshal(sync.Body.Bytes(), &sb); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range sb.Result.Buddies {
		if r.AvatarNo == "9501" {
			found = r.Status == -1
		}
	}
	if !found {
		t.Fatalf("no status -1 tombstone for deleted friend: %s", sync.Body.String())
	}
	// A repeat with the old token is an unknown session.
	if rec := f.del(t, "dtok"); rec.Code != http.StatusNotFound || rec.Body.String() != httpx.UnknownSessionBody {
		t.Fatalf("repeat delete = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAccountLabAccountAllowed(t *testing.T) {
	f := delSetup(t)
	setLab(t, "9501", true)
	if rec := f.del(t, "dtok"); rec.Code != http.StatusOK {
		t.Fatalf("lab delete = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAccountUnknownSessionAndBadCode(t *testing.T) {
	f := delSetup(t)
	for _, target := range []string{"/v4/avatar/9501/removeCode"} {
		if rec := socialTestRequestAs(t, "nobody", http.MethodGet, target, ""); rec.Code != http.StatusNotFound || rec.Body.String() != httpx.UnknownSessionBody {
			t.Fatalf("%s = %d %s", target, rec.Code, rec.Body.String())
		}
	}
	if rec := f.del(t, "nobody"); rec.Code != http.StatusNotFound || rec.Body.String() != httpx.UnknownSessionBody {
		t.Fatalf("unknown delete = %d %s", rec.Code, rec.Body.String())
	}
	bad := socialTestRequestAs(t, "dtok", http.MethodPost, "/v4/r/avatar/9501/"+f.key+"/0000", "null")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("wrong code = %d", bad.Code)
	}
	// The code is single use: even the right one now needs a fresh removeCode.
	if rec := f.del(t, "dtok"); rec.Code != http.StatusBadRequest {
		t.Fatalf("reused code = %d", rec.Code)
	}
	store.AccountsMu.Lock()
	ok := store.AccountByTokenLocked("dtok") != nil
	store.AccountsMu.Unlock()
	if !ok {
		t.Fatal("account removed by a refused request")
	}
}

func TestDeleteAccountSaveFailureRollsBack(t *testing.T) {
	f := delSetup(t)
	blocker := filepath.Join(f.dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	refuseSocialDeletes(t)
	if rec := f.del(t, "dtok"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	store.AccountsMu.Lock()
	a1 := store.AccountByTokenLocked("dtok") != nil
	a2 := store.AccountByTokenLocked("dtok2") != nil
	latest := store.LatestAcc
	store.AccountsMu.Unlock()
	store.SocialMu.Lock()
	state := store.Friendships[0].State
	n, g, read := len(store.DiaryPosts), len(store.GuestbookEntries), store.GuestbookRead["9501"]
	store.SocialMu.Unlock()
	if !a1 || !a2 || latest != f.acc || state != "accepted" || n != 2 || g != 3 || read != 2 {
		t.Fatalf("not rolled back: %v %v %v %s %d %d %d", a1, a2, latest, state, n, g, read)
	}
	// The database still holds the account: the rollback wrote it back.
	if err := store.ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	kept := store.AccountByAidLocked("9501") != nil
	store.AccountsMu.Unlock()
	if !kept {
		t.Fatal("the database lost the account after a failed delete")
	}
}

// refuseSocialDeletes makes the database refuse the delete of a read marker, so the social save of a delete fails while the
// account rows (the same operation writes them first, and the undo writes them back) stay writable.
func refuseSocialDeletes(t *testing.T) {
	t.Helper()
	if err := store.AttachedDB().Update(func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER social_refused BEFORE DELETE ON guestbook_read BEGIN SELECT RAISE(ABORT, 'refused'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
