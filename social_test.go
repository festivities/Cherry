package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestSocialFriendAndDiary(t *testing.T) {
	resetSocial()
	t.Cleanup(resetSocial)

	sync := serve(t, http.MethodGet, "/v4/sync/friends/0")
	if sync.Code != http.StatusOK || !bytes.Contains(sync.Body.Bytes(), []byte(`"avatarNo":"2"`)) || bytes.Contains(sync.Body.Bytes(), []byte(`"avatarNo":"1"`)) {
		t.Fatalf("sync = %d %s", sync.Code, sync.Body.String())
	}
	profile := serve(t, http.MethodGet, "/v4/profile/2?deviceType=Android")
	if profile.Code != http.StatusOK || !bytes.Contains(profile.Body.Bytes(), []byte(`"name":"Friend"`)) {
		t.Fatalf("profile/2 = %d %s", profile.Code, profile.Body.String())
	}
	own := serve(t, http.MethodGet, "/v4/profile/1")
	if own.Code != http.StatusOK || own.Body.String() != profileBody {
		t.Fatalf("profile/1 = %d %s", own.Code, own.Body.String())
	}
	if rec := serve(t, http.MethodPost, "/v4/friend/bookmark/2"); rec.Code != http.StatusOK {
		t.Fatalf("bookmark = %d", rec.Code)
	}
	marked := serve(t, http.MethodGet, "/v4/buddy/list/type/0")
	if !bytes.Contains(marked.Body.Bytes(), []byte(`"bookmarks":["2"]`)) {
		t.Fatalf("bookmarks = %s", marked.Body.String())
	}
	remove := serveRequest(t, httptest.NewRequest(http.MethodPost, "/v4/r/friend/remove/", bytes.NewBufferString(`["2"]`)))
	if remove.Code != http.StatusOK || !bytes.Contains(remove.Body.Bytes(), []byte(`"success":["2"]`)) {
		t.Fatalf("remove = %d %s", remove.Code, remove.Body.String())
	}
	after := serve(t, http.MethodGet, "/v4/sync/friends/1")
	if bytes.Contains(after.Body.Bytes(), []byte(`"avatarNo":"2"`)) {
		t.Fatalf("removed friend still listed: %s", after.Body.String())
	}

	resetSocial()
	dir := t.TempDir()
	socialMu.Lock()
	socialStorePath = filepath.Join(dir, "social.json")
	socialMu.Unlock()
	raw := append([]byte(`{"avatarId":"1","content":"hello","title":"","openState":"public"}`), 0)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v4/diary2/ext/write", bytes.NewReader(gz.Bytes()))
	req.Header.Set("Content-Encoding", "gzip")
	wrote := serveRequest(t, req)
	if wrote.Code != http.StatusOK {
		t.Fatalf("write = %d %s", wrote.Code, wrote.Body.String())
	}
	var created struct {
		Result diaryPost `json:"result"`
	}
	if err := json.Unmarshal(wrote.Body.Bytes(), &created); err != nil || created.Result.DiaryNo == "" || created.Result.Images == nil {
		t.Fatalf("write body = %s %v", wrote.Body.String(), err)
	}
	socialMu.Lock()
	path := socialStorePath
	socialMu.Unlock()
	if err := loadSocialFrom(path); err != nil {
		t.Fatal(err)
	}
	unfold := serve(t, http.MethodGet, "/v4/diary2/ext/unfold/1/?lastSeq=-1&size=10")
	if unfold.Code != http.StatusOK || !bytes.Contains(unfold.Body.Bytes(), []byte(`"diaryNo":"`+created.Result.DiaryNo+`"`)) || !bytes.Contains(unfold.Body.Bytes(), []byte(`"images":[]`)) {
		t.Fatalf("unfold = %d %s", unfold.Code, unfold.Body.String())
	}
	erased := serve(t, http.MethodPost, "/v4/diary2/erase/"+created.Result.DiaryNo)
	if erased.Code != http.StatusOK {
		t.Fatalf("erase = %d %s", erased.Code, erased.Body.String())
	}
	empty := serve(t, http.MethodGet, "/v4/diary2/ext/unfold/1/")
	if !bytes.Contains(empty.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("unfold after erase = %s", empty.Body.String())
	}
}
