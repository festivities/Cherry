package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

func TestGuestbookPersistencePaginationAndSecretVisibility(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1", Name: "Cherry", Country: "JP"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.SocialMu.Lock()
	store.FriendRemoved, store.FriendBookmarked = true, true
	store.NextDiaryNo = 7
	store.DiaryPosts = []store.DiaryPost{{DiaryNo: "7", AvatarID: "1", Nickname: "Cherry", Images: []store.DiaryImage{}}}
	store.SocialMu.Unlock()
	testutil.UseDatabase(t)

	write := func(content string, secret bool) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(map[string]any{
			"hostAvtNo": "100000", "guestAvtNo": "1", "content": content, "secret": secret,
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v4/guestbook/write", bytes.NewReader(encoded))
		req.Header.Set("Cookie", socialTestCookie)
		return serveRequest(t, req)
	}
	for _, item := range []struct {
		text   string
		secret bool
	}{{"old public", false}, {"private note", true}, {"new public", false}} {
		response := write(item.text, item.secret)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"reply":[]`)) {
			t.Fatalf("guestbook write = %d %s", response.Code, response.Body.String())
		}
	}

	var first struct {
		Result struct {
			LastData bool `json:"lastData"`
			Items    []struct {
				GuestHistNo string `json:"guestHistNo"`
				Content     string `json:"content"`
				Secret      bool   `json:"secret"`
				Reply       []any  `json:"reply"`
			} `json:"items"`
		} `json:"result"`
	}
	list := socialTestRequest(t, http.MethodGet, "/v4/guestbook3/list/100000/?lastSeq=-1&size=2", "")
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &first) != nil || first.Result.LastData || len(first.Result.Items) != 2 || first.Result.Items[0].GuestHistNo != "3" || first.Result.Items[1].GuestHistNo != "2" || !first.Result.Items[1].Secret || first.Result.Items[0].Reply == nil {
		t.Fatalf("first guestbook page = %d %s", list.Code, list.Body.String())
	}
	var second struct {
		Result struct {
			LastData bool `json:"lastData"`
			Items    []struct {
				GuestHistNo string `json:"guestHistNo"`
			} `json:"items"`
		} `json:"result"`
	}
	list = socialTestRequest(t, http.MethodGet, "/v4/guestbook3/list/100000/?lastSeq=2&size=2", "")
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &second) != nil || !second.Result.LastData || len(second.Result.Items) != 1 || second.Result.Items[0].GuestHistNo != "1" {
		t.Fatalf("second guestbook page = %d %s", list.Code, list.Body.String())
	}
	public := serve(t, http.MethodGet, "/v4/guestbook3/list/100000/?lastSeq=-1&size=10")
	if public.Code != http.StatusOK || bytes.Contains(public.Body.Bytes(), []byte("private note")) || !bytes.Contains(public.Body.Bytes(), []byte(`"content":"new public"`)) {
		t.Fatalf("anonymous guestbook list = %d %s", public.Code, public.Body.String())
	}
	count := serve(t, http.MethodGet, "/v4/guestbook3/count/100000")
	if count.Code != http.StatusOK || !bytes.Contains(count.Body.Bytes(), []byte(`"count":3`)) {
		t.Fatalf("guestbook count = %d %s", count.Code, count.Body.String())
	}

	if err := store.ReloadSocial(); err != nil {
		t.Fatal(err)
	}
	store.SocialMu.Lock()
	removed, bookmarked, posts, seq, entries := store.FriendRemoved, store.FriendBookmarked, len(store.DiaryPosts), store.NextGuestHistNo, len(store.GuestbookEntries)
	store.SocialMu.Unlock()
	if !removed || !bookmarked || posts != 1 || seq != 3 || entries != 3 {
		t.Fatalf("reloaded social state removed=%t bookmarked=%t posts=%d seq=%d guestbook=%d", removed, bookmarked, posts, seq, entries)
	}

	erased := socialTestRequest(t, http.MethodPost, "/v4/guestbook/erase/100000/3", "null")
	if erased.Code != http.StatusOK {
		t.Fatalf("guestbook erase = %d %s", erased.Code, erased.Body.String())
	}
	count = serve(t, http.MethodGet, "/v4/guestbook3/count/100000")
	if !bytes.Contains(count.Body.Bytes(), []byte(`"count":2`)) {
		t.Fatalf("count after erase = %s", count.Body.String())
	}
}

// The unread badge: count is the entries past the host's read marker, clearing (the host's session only) moves the marker to
// its highest entry, and a reload keeps the marker even after the entry that set it was erased.
func TestGuestbookCountClearBadge(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"host-token":  {AccessToken: "host-token", Aid: "1", Name: "Host", Country: "JP"},
		"guest-token": {AccessToken: "guest-token", Aid: "3", Name: "Guest", Country: "JP"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	testutil.UseDatabase(t)
	write := func(content string) {
		t.Helper()
		body := `{"hostAvtNo":"1","guestAvtNo":"3","content":"` + content + `","secret":false}`
		if rec := socialTestRequestAs(t, "guest-token", http.MethodPost, "/v4/guestbook/write", body); rec.Code != http.StatusOK {
			t.Fatalf("write %s = %d %s", content, rec.Code, rec.Body.String())
		}
	}
	count := func(want string) {
		t.Helper()
		if rec := serve(t, http.MethodGet, "/v4/guestbook3/count/1"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("count = %d %s, want %s", rec.Code, rec.Body.String(), want)
		}
	}
	clearAs := func(token, want string) {
		t.Helper()
		rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/guestbookcount/clear/1", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("clear as %s = %d %s, want %s", token, rec.Code, rec.Body.String(), want)
		}
	}

	write("first")
	write("second")
	count(`"count":2`)
	clearAs("host-token", `{"result":{"count":0,"hostAvtNo":"1"}}`)
	count(`"count":0`)
	write("third")
	count(`"count":1`)
	clearAs("guest-token", `"count":1`) // not the host: the unread count, no change
	count(`"count":1`)
	if rec := serve(t, http.MethodGet, "/v4/guestbookcount/clear/1"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"count":1`) {
		t.Fatalf("clear without a session = %d %s, want the unread count", rec.Code, rec.Body.String())
	}
	if rec := serve(t, http.MethodGet, "/v4/guestbookcount/clear/999"); rec.Code != http.StatusNotFound {
		t.Fatalf("clear of an unknown host = %d, want 404", rec.Code)
	}
	if rec := serve(t, http.MethodPost, "/v4/guestbookcount/clear/1"); rec.Code != http.StatusNotFound {
		t.Fatalf("POST clear = %d, want 404", rec.Code)
	}
	clearAs("host-token", `"count":0`)

	// The marker sits at entry 3; erase that entry and reload. The marker survives, and the next entry still counts.
	if rec := socialTestRequestAs(t, "host-token", http.MethodPost, "/v4/guestbook/erase/1/3", "null"); rec.Code != http.StatusOK {
		t.Fatalf("erase = %d %s", rec.Code, rec.Body.String())
	}
	if err := store.ReloadSocial(); err != nil {
		t.Fatal(err)
	}
	count(`"count":0`)
	write("fourth")
	count(`"count":1`)

	// A failed save answers 500 and leaves the marker where it was (entry 3 read, "fourth" unread).
	testutil.FailSaves(t)
	if rec := socialTestRequestAs(t, "host-token", http.MethodGet, "/v4/guestbookcount/clear/1", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("clear with a failing store = %d %s", rec.Code, rec.Body.String())
	}
	store.SocialMu.Lock()
	marker := store.GuestbookRead["1"]
	store.SocialMu.Unlock()
	if marker != 3 {
		t.Fatalf("failed clear left marker %d, want 3", marker)
	}
}

func TestGuestbookRejectsUntrustedActorsAndRollsBackSaveFailure(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1", Name: "Cherry"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	body := `{"hostAvtNo":"100000","guestAvtNo":"1","content":"hello","secret":false}`
	noCookie := serveRequest(t, httptest.NewRequest(http.MethodPost, "/v4/guestbook/write", strings.NewReader(body)))
	if noCookie.Code != http.StatusNotFound {
		t.Fatalf("write without AV_AUTH = %d %s", noCookie.Code, noCookie.Body.String())
	}
	wrongGuest := strings.Replace(body, `"guestAvtNo":"1"`, `"guestAvtNo":"999"`, 1)
	if response := socialTestRequest(t, http.MethodPost, "/v4/guestbook/write", wrongGuest); response.Code != http.StatusBadRequest {
		t.Fatalf("forged guest ID = %d %s", response.Code, response.Body.String())
	}
	unknownHost := strings.Replace(body, `"hostAvtNo":"100000"`, `"hostAvtNo":"999"`, 1)
	if response := socialTestRequest(t, http.MethodPost, "/v4/guestbook/write", unknownHost); response.Code != http.StatusNotFound {
		t.Fatalf("unknown host = %d %s", response.Code, response.Body.String())
	}

	testutil.FailSaves(t)
	failed := socialTestRequest(t, http.MethodPost, "/v4/guestbook/write", body)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("write with failing store = %d %s", failed.Code, failed.Body.String())
	}
	store.SocialMu.Lock()
	seq, entries := store.NextGuestHistNo, len(store.GuestbookEntries)
	store.SocialMu.Unlock()
	if seq != 0 || entries != 0 {
		t.Fatalf("failed write left state seq=%d entries=%d", seq, entries)
	}
}
