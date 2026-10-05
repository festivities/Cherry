package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestbookPersistencePaginationAndSecretVisibility(t *testing.T) {
	installSocialTestAccounts(t, map[string]*account{
		"social-test-token": {accessToken: "social-test-token", aid: "1", name: "Cherry", country: "JP"},
	})
	resetSocial()
	t.Cleanup(resetSocial)
	dir := t.TempDir()
	store := filepath.Join(dir, "social.json")
	legacy := `{"version":1,"friendRemoved":true,"friendBookmarked":true,"nextDiaryNo":7,"posts":[{"diaryNo":"7","avatarId":"1","nickname":"Cherry","images":[]}]}`
	if err := os.WriteFile(store, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadSocialFrom(store); err != nil {
		t.Fatal(err)
	}

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

	if err := loadSocialFrom(store); err != nil {
		t.Fatal(err)
	}
	socialMu.Lock()
	removed, bookmarked, posts, seq, entries := friendRemoved, friendBookmarked, len(diaryPosts), nextGuestHistNo, len(guestbookEntries)
	socialMu.Unlock()
	if !removed || !bookmarked || posts != 1 || seq != 3 || entries != 3 {
		t.Fatalf("reloaded social state removed=%t bookmarked=%t posts=%d seq=%d guestbook=%d", removed, bookmarked, posts, seq, entries)
	}
	data, err := os.ReadFile(store)
	if err != nil || !bytes.Contains(data, []byte(`"nextGuestHistNo":3`)) || !bytes.Contains(data, []byte(`"guestbook":[`)) || !bytes.Contains(data, []byte(`"nextDiaryNo":7`)) {
		t.Fatalf("saved social store = %s (%v)", data, err)
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

func TestGuestbookRejectsUntrustedActorsAndRollsBackSaveFailure(t *testing.T) {
	installSocialTestAccounts(t, map[string]*account{
		"social-test-token": {accessToken: "social-test-token", aid: "1", name: "Cherry"},
	})
	resetSocial()
	t.Cleanup(resetSocial)

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

	blockedPath := filepath.Join(t.TempDir(), "social-directory")
	if err := os.Mkdir(blockedPath, 0700); err != nil {
		t.Fatal(err)
	}
	socialMu.Lock()
	socialStorePath = blockedPath
	socialMu.Unlock()
	failed := socialTestRequest(t, http.MethodPost, "/v4/guestbook/write", body)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("write with failing store = %d %s", failed.Code, failed.Body.String())
	}
	socialMu.Lock()
	seq, entries := nextGuestHistNo, len(guestbookEntries)
	socialMu.Unlock()
	if seq != 0 || entries != 0 {
		t.Fatalf("failed write left state seq=%d entries=%d", seq, entries)
	}
}
