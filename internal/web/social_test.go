package web

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

const socialTestCookie = `AV_AUTH="social-test-token"`

func installSocialTestAccounts(t *testing.T, entries map[string]*store.Account) {
	t.Helper()
	testutil.InstallAccounts(t, entries)
}

func socialTestRequest(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	return socialTestRequestAs(t, "social-test-token", method, target, body)
}

func socialTestRequestAs(t *testing.T, token, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	return serveRequest(t, req)
}

func TestDiaryCheckExistGate(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1", Name: "Cherry"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	valid := serve(t, http.MethodGet, "/v4/diary2/ext/checkExist/100000/0/")
	if valid.Code != http.StatusOK || valid.Body.String() != `{"result":true}` {
		t.Fatalf("Friend diary check = %d %s", valid.Code, valid.Body.String())
	}
	for _, target := range []string{
		"/v4/diary2/ext/checkExist/999/0/",
		"/v4/diary2/ext/checkExist/100000/1/",
		"/v4/diary2/ext/checkExist/100000/0",
		"/v4/diary2/ext/checkExist/100000/0/extra/",
	} {
		if response := serve(t, http.MethodGet, target); response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d %s, want 404", target, response.Code, response.Body.String())
		}
	}
	if method := serve(t, http.MethodPost, "/v4/diary2/ext/checkExist/100000/0/"); method.Code != http.StatusNotFound {
		t.Errorf("POST checkExist = %d %s, want 404", method.Code, method.Body.String())
	}
}

func TestDiaryVisibilityUsesOpenStateAndKnownViewer(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1", Name: "Cherry"},
		"other-token":       {AccessToken: "other-token", Aid: "3", Name: "Other"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	write := func(title, openState string) string {
		t.Helper()
		request := map[string]any{"avatarId": "1", "title": title, "content": title, "openState": openState}
		if title == "private" {
			request["images"] = []store.DiaryImage{{ImageURL: "/lineplay/d/download.nhn?oid=42_1710000000&ctime=1710000000&userid=42"}}
		}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		response := socialTestRequest(t, http.MethodPost, "/v4/diary2/ext/write", string(body))
		if response.Code != http.StatusOK {
			t.Fatalf("write %s = %d %s", title, response.Code, response.Body.String())
		}
		var created struct {
			Result store.DiaryPost `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created.Result.DiaryNo
	}
	privateNo := write("private", "private")
	unknownNo := write("unknown", "friends-only-unknown")
	publicNo := write("public", "public")

	list := func(token string, authenticated bool) []store.DiaryPost {
		t.Helper()
		var response *httptest.ResponseRecorder
		if authenticated {
			response = socialTestRequestAs(t, token, http.MethodGet, "/v4/diary2/ext/unfold/1/?lastSeq=-1&size=10", "")
		} else {
			response = serve(t, http.MethodGet, "/v4/diary2/ext/unfold/1/?lastSeq=-1&size=10")
		}
		if response.Code != http.StatusOK {
			t.Fatalf("diary list = %d %s", response.Code, response.Body.String())
		}
		var payload struct {
			Result struct {
				Items []store.DiaryPost `json:"items"`
			} `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Result.Items
	}
	ownerItems := list("social-test-token", true)
	if len(ownerItems) != 3 {
		t.Fatalf("owner sees %d posts, want 3: %+v", len(ownerItems), ownerItems)
	}
	if items := list("", false); len(items) != 1 || items[0].DiaryNo != publicNo {
		t.Fatalf("anonymous visible posts = %+v", items)
	}
	if items := list("other-token", true); len(items) != 1 || items[0].DiaryNo != publicNo {
		t.Fatalf("other account visible posts = %+v", items)
	}
	ownerAlbum := socialTestRequest(t, http.MethodGet, "/v4/diary2/ext/unfold/photo/1/?lastSeq=-1&size=15", "")
	if ownerAlbum.Code != http.StatusOK || !bytes.Contains(ownerAlbum.Body.Bytes(), []byte(`"diaryNo":"`+privateNo+`"`)) || !bytes.Contains(ownerAlbum.Body.Bytes(), []byte(`"imageLocationType":"obs"`)) {
		t.Fatalf("owner private photo album = %d %s", ownerAlbum.Code, ownerAlbum.Body.String())
	}
	if album := serve(t, http.MethodGet, "/v4/diary2/ext/unfold/photo/1/?lastSeq=-1&size=15"); album.Code != http.StatusOK || !bytes.Contains(album.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("anonymous private photo album = %d %s", album.Code, album.Body.String())
	}
	if album := socialTestRequestAs(t, "other-token", http.MethodGet, "/v4/diary2/ext/unfold/photo/1/?lastSeq=-1&size=15", ""); album.Code != http.StatusOK || !bytes.Contains(album.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("other account private photo album = %d %s", album.Code, album.Body.String())
	}
	if detail := socialTestRequest(t, http.MethodGet, "/v4/diary2/ext/look/1/"+privateNo, ""); detail.Code != http.StatusOK {
		t.Fatalf("owner private detail = %d %s", detail.Code, detail.Body.String())
	}
	if detail := serve(t, http.MethodGet, "/v4/diary2/ext/look/1/"+privateNo); detail.Code != http.StatusNotFound {
		t.Fatalf("anonymous private detail = %d %s", detail.Code, detail.Body.String())
	}
	if detail := socialTestRequestAs(t, "other-token", http.MethodGet, "/v4/diary2/ext/look/1/"+privateNo, ""); detail.Code != http.StatusNotFound {
		t.Fatalf("other account private detail = %d %s", detail.Code, detail.Body.String())
	}
	if detail := socialTestRequestAs(t, "other-token", http.MethodGet, "/v4/diary2/ext/look/1/"+unknownNo, ""); detail.Code != http.StatusNotFound {
		t.Fatalf("other account unknown-state detail = %d %s", detail.Code, detail.Body.String())
	}

	introCount := func(response *httptest.ResponseRecorder) int {
		t.Helper()
		var payload struct {
			Result struct {
				DiaryCount int `json:"diaryCount"`
			} `json:"result"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil {
			t.Fatalf("diary intro = %d %s", response.Code, response.Body.String())
		}
		return payload.Result.DiaryCount
	}
	if got := introCount(socialTestRequest(t, http.MethodGet, "/v4/diary2/intro/1", "")); got != 3 {
		t.Errorf("owner intro count = %d, want 3", got)
	}
	if got := introCount(serve(t, http.MethodGet, "/v4/diary2/intro/1")); got != 1 {
		t.Errorf("anonymous intro count = %d, want 1", got)
	}
	if got := introCount(socialTestRequestAs(t, "other-token", http.MethodGet, "/v4/diary2/intro/1", "")); got != 1 {
		t.Errorf("other account intro count = %d, want 1", got)
	}
	unknownCredential := httptest.NewRequest(http.MethodGet, "/v4/diary2/intro/1", nil)
	unknownCredential.Header.Set("Cookie", `AV_AUTH="unknown-token"`)
	if got := introCount(serveRequest(t, unknownCredential)); got != 1 {
		t.Errorf("unknown credential intro count = %d, want 1", got)
	}
}

func TestDiarySequenceResumesFromStoredPostsAndRejectsOverflow(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1", Name: "Cherry"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	path := filepath.Join(t.TempDir(), "social.json")
	legacy := `{"version":1,"nextDiaryNo":1,"posts":[{"diaryNo":"9","avatarId":"1","nickname":"Cherry","title":"legacy photo","content":"keep me","openState":"public","imageTypeKind":"obs","images":[{"imageUrl":"/lineplay/d/download.nhn?oid=42_1710000000&ctime=1710000000&userid=42"}]}]}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadSocialFrom(path); err != nil {
		t.Fatal(err)
	}
	store.SocialMu.Lock()
	legacyPost := store.DiaryPosts[0]
	store.SocialMu.Unlock()
	if legacyPost.ImageLocationType != "obs" || legacyPost.ImageTypeKind != "obs" || legacyPost.Title != "legacy photo" || legacyPost.Content != "keep me" || len(legacyPost.Images) != 1 {
		t.Fatalf("legacy photo normalization lost data: %+v", legacyPost)
	}
	legacyAlbum := socialTestRequest(t, http.MethodGet, "/v4/diary2/ext/unfold/photo/1/?lastSeq=-1&size=15", "")
	if legacyAlbum.Code != http.StatusOK || !bytes.Contains(legacyAlbum.Body.Bytes(), []byte(`"imageLocationType":"obs"`)) || !bytes.Contains(legacyAlbum.Body.Bytes(), []byte(`"title":"legacy photo"`)) {
		t.Fatalf("legacy photo album = %d %s", legacyAlbum.Code, legacyAlbum.Body.String())
	}
	response := socialTestRequest(t, http.MethodPost, "/v4/diary2/ext/write", `{"avatarId":"1","content":"next","openState":"public"}`)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"diaryNo":"10"`)) {
		t.Fatalf("post after reload = %d %s", response.Code, response.Body.String())
	}
	store.SocialMu.Lock()
	store.NextDiaryNo = ^uint64(0)
	postsBeforeOverflow := len(store.DiaryPosts)
	store.SocialMu.Unlock()
	response = socialTestRequest(t, http.MethodPost, "/v4/diary2/ext/write", `{"avatarId":"1","content":"overflow","openState":"public"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("overflow write = %d %s", response.Code, response.Body.String())
	}
	store.SocialMu.Lock()
	gotNext, gotPosts := store.NextDiaryNo, len(store.DiaryPosts)
	store.SocialMu.Unlock()
	if gotNext != ^uint64(0) || gotPosts != postsBeforeOverflow {
		t.Fatalf("overflow mutated state: next=%d posts=%d want next=%d posts=%d", gotNext, gotPosts, ^uint64(0), postsBeforeOverflow)
	}
}

func TestSocialFriendAndDiary(t *testing.T) {
	tokenAccount := &store.Account{AccessToken: "social-test-token", Aid: "1", Name: "Cherry", Country: "JP"}
	installSocialTestAccounts(t, map[string]*store.Account{"social-test-token": tokenAccount})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	sync := serve(t, http.MethodGet, "/v4/sync/friends/0")
	if sync.Code != http.StatusOK || !bytes.Contains(sync.Body.Bytes(), []byte(`"avatarNo":"100000"`)) || !bytes.Contains(sync.Body.Bytes(), []byte(`"avatarName":"Friend"`)) || !bytes.Contains(sync.Body.Bytes(), []byte(`"buddyAvatarNo":"100000"`)) || !bytes.Contains(sync.Body.Bytes(), []byte(`"status":1`)) || !bytes.Contains(sync.Body.Bytes(), []byte(`"friendStatus":1`)) || bytes.Contains(sync.Body.Bytes(), []byte(`"avatarNo":"2","avatarName":"Friend"`)) || !bytes.Contains(sync.Body.Bytes(), []byte(`"friendsCount":1`)) || bytes.Contains(sync.Body.Bytes(), []byte(`"avatarNo":"1"`)) {
		t.Fatalf("sync = %d %s", sync.Code, sync.Body.String())
	}
	profile := serve(t, http.MethodGet, "/v4/profile/100000?deviceType=Android")
	if profile.Code != http.StatusOK || !bytes.Contains(profile.Body.Bytes(), []byte(`"name":"Friend"`)) || !bytes.Contains(profile.Body.Bytes(), []byte(`"hasGarden":true`)) || !bytes.Contains(profile.Body.Bytes(), []byte(`"showGuestBook":true`)) {
		t.Fatalf("profile/friend = %d %s", profile.Code, profile.Body.String())
	}
	own := serve(t, http.MethodGet, "/v4/profile/1")
	if own.Code != http.StatusOK || !bytes.Contains(own.Body.Bytes(), []byte(`"avatarId":"1"`)) || !bytes.Contains(own.Body.Bytes(), []byte(`"name":"Cherry"`)) {
		t.Fatalf("profile/1 = %d %s", own.Code, own.Body.String())
	}
	avatar := serve(t, http.MethodGet, "/v4/avatar/100000")
	if avatar.Code != http.StatusOK || !bytes.Contains(avatar.Body.Bytes(), []byte(`"name":"Friend"`)) || !bytes.Contains(avatar.Body.Bytes(), []byte(`"items":[`)) || bytes.Contains(avatar.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("avatar/friend = %d %s", avatar.Code, avatar.Body.String())
	}
	if rec := socialTestRequest(t, http.MethodPost, "/v4/friend/bookmark/100000", ""); rec.Code != http.StatusOK {
		t.Fatalf("bookmark = %d", rec.Code)
	}
	marked := serve(t, http.MethodGet, "/v4/buddy/list/type/0")
	if !bytes.Contains(marked.Body.Bytes(), []byte(`"bookmarks":["100000"]`)) {
		t.Fatalf("bookmarks = %s", marked.Body.String())
	}
	remove := socialTestRequest(t, http.MethodPost, "/v4/r/friend/remove/", `["100000"]`)
	if remove.Code != http.StatusOK || !bytes.Contains(remove.Body.Bytes(), []byte(`"success":["100000"]`)) {
		t.Fatalf("remove = %d %s", remove.Code, remove.Body.String())
	}
	after := serve(t, http.MethodGet, "/v4/sync/friends/1")
	if bytes.Contains(after.Body.Bytes(), []byte(`"avatarNo":"100000"`)) {
		t.Fatalf("removed friend still listed: %s", after.Body.String())
	}
	removedSearch := serveRequest(t, httptest.NewRequest(http.MethodPost, "/v4/square/friends/search?page=0&size=20", strings.NewReader(`{"country":"JP","caricNickName":"100000","searchType":"CODE"}`)))
	if removedSearch.Code != http.StatusOK || !bytes.Contains(removedSearch.Body.Bytes(), []byte(`"avatarId":"100000"`)) || !bytes.Contains(removedSearch.Body.Bytes(), []byte(`"friendStatus":"2"`)) {
		t.Fatalf("removed friend search = %d %s", removedSearch.Code, removedSearch.Body.String())
	}
	if status := serve(t, http.MethodGet, "/v4/friend/status/100000"); status.Code != http.StatusOK || status.Body.String() != `{"result":2}` {
		t.Fatalf("removed friend status = %d %s", status.Code, status.Body.String())
	}
	if applied := socialTestRequest(t, http.MethodPost, "/v4/friend/apply/100000", `{"applyAvatarId":"100000"}`); applied.Code != http.StatusOK || applied.Body.String() != `{"result":1}` {
		t.Fatalf("apply = %d %s", applied.Code, applied.Body.String())
	}
	if status := serve(t, http.MethodGet, "/v4/friend/status/100000"); status.Code != http.StatusOK || status.Body.String() != `{"result":1}` {
		t.Fatalf("restored friend status = %d %s", status.Code, status.Body.String())
	}
	if unknown := serve(t, http.MethodGet, "/v4/friend/status/999"); unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown friend status = %d %s", unknown.Code, unknown.Body.String())
	}
	if unknown := socialTestRequest(t, http.MethodPost, "/v4/friend/apply/999", `{"applyAvatarId":"999"}`); unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown friend apply = %d %s", unknown.Code, unknown.Body.String())
	}
	if accepted := socialTestRequest(t, http.MethodPut, "/v4/friend/accept/100000", "null"); accepted.Code != http.StatusOK || accepted.Body.String() != `{"result":1}` {
		t.Fatalf("accept = %d %s", accepted.Code, accepted.Body.String())
	}

	store.ResetSocial()
	dir := t.TempDir()
	store.SocialMu.Lock()
	store.SocialStorePath = filepath.Join(dir, "social.json")
	store.SocialMu.Unlock()
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
	req.Header.Set("Cookie", socialTestCookie)
	wrote := serveRequest(t, req)
	if wrote.Code != http.StatusOK {
		t.Fatalf("write = %d %s", wrote.Code, wrote.Body.String())
	}
	var created struct {
		Result store.DiaryPost `json:"result"`
	}
	if err := json.Unmarshal(wrote.Body.Bytes(), &created); err != nil || created.Result.DiaryNo == "" || created.Result.Images == nil {
		t.Fatalf("write body = %s %v", wrote.Body.String(), err)
	}
	store.SocialMu.Lock()
	path := store.SocialStorePath
	store.SocialMu.Unlock()
	if err := store.LoadSocialFrom(path); err != nil {
		t.Fatal(err)
	}
	unfold := serve(t, http.MethodGet, "/v4/diary2/ext/unfold/1/?lastSeq=-1&size=10")
	if unfold.Code != http.StatusOK || !bytes.Contains(unfold.Body.Bytes(), []byte(`"diaryNo":"`+created.Result.DiaryNo+`"`)) || !bytes.Contains(unfold.Body.Bytes(), []byte(`"images":[]`)) {
		t.Fatalf("unfold = %d %s", unfold.Code, unfold.Body.String())
	}
	erased := socialTestRequest(t, http.MethodPost, "/v4/diary2/erase/"+created.Result.DiaryNo, "")
	if erased.Code != http.StatusOK {
		t.Fatalf("erase = %d %s", erased.Code, erased.Body.String())
	}
	empty := serve(t, http.MethodGet, "/v4/diary2/ext/unfold/1/")
	if !bytes.Contains(empty.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("unfold after erase = %s", empty.Body.String())
	}
}

func TestFriendSearchPaginationAndDiaryPhotoMetadata(t *testing.T) {
	accountsForTest := map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1", Name: "Cherry", Country: "JP"},
		"other-a":           {AccessToken: "other-a", Aid: "3", Name: "Friend Extra"},
		"other-b":           {AccessToken: "other-b", Aid: "4", Name: "Friendship"},
	}
	installSocialTestAccounts(t, accountsForTest)
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	search := func(term, kind, page string) *httptest.ResponseRecorder {
		t.Helper()
		target := "/v4/square/friends/search?page=" + page + "&size=2"
		return serveRequest(t, httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"country":"JP","caricNickName":"`+term+`","searchType":"`+kind+`"}`)))
	}
	first := search("friend", "NICK", "0")
	if first.Code != http.StatusOK || !bytes.Contains(first.Body.Bytes(), []byte(`"previousCursor":"0"`)) || !bytes.Contains(first.Body.Bytes(), []byte(`"nextCursor":"1"`)) || !bytes.Contains(first.Body.Bytes(), []byte(`"totalCount":"3"`)) || !bytes.Contains(first.Body.Bytes(), []byte(`"avatarId":"100000"`)) {
		t.Fatalf("first search page = %d %s", first.Code, first.Body.String())
	}
	second := search("friend", "NICK", "1")
	if second.Code != http.StatusOK || !bytes.Contains(second.Body.Bytes(), []byte(`"previousCursor":"0"`)) || !bytes.Contains(second.Body.Bytes(), []byte(`"nextCursor":"0"`)) || !bytes.Contains(second.Body.Bytes(), []byte(`"avatarId":"4"`)) {
		t.Fatalf("second search page = %d %s", second.Code, second.Body.String())
	}
	code := search("100000", "CODE", "0")
	if code.Code != http.StatusOK || !bytes.Contains(code.Body.Bytes(), []byte(`"totalCount":"1"`)) {
		t.Fatalf("CODE search = %d %s", code.Code, code.Body.String())
	}
	noCode := search("02", "CODE", "0")
	if noCode.Code != http.StatusOK || !bytes.Contains(noCode.Body.Bytes(), []byte(`"totalCount":"0"`)) {
		t.Fatalf("non-exact CODE search = %d %s", noCode.Code, noCode.Body.String())
	}
	if strings.Contains(first.Body.String(), "social-test-token") || strings.Contains(first.Body.String(), "other-a") {
		t.Fatalf("search exposed account tokens: %s", first.Body.String())
	}

	dir := t.TempDir()
	store.SocialMu.Lock()
	store.SocialStorePath = filepath.Join(dir, "social.json")
	store.SocialMu.Unlock()
	imageURL := "/lineplay/d/download.nhn?oid=42_1710000000&ctime=1710000000&userid=42"
	if !store.ValidDiaryImage(store.DiaryImage{ImageURL: "lineplay/d/download.nhn?userid=42&ctime=1710000000&oid=42_1710000000&tid=306x0.r"}) {
		t.Fatal("valid media tuple with optional tid rejected")
	}
	if store.ValidDiaryImage(store.DiaryImage{ImageURL: imageURL + "&oid=42_1710000000"}) {
		t.Fatal("duplicate media query key accepted")
	}
	// Synthetic metadata literal verifies persistence only; the client-supplied value is runtime-derived.
	photo := `{"avatarId":"1","title":"photo","content":"caption","openState":"public","imageTypeKind":"photo","images":[{"imageUrl":"` + imageURL + `"}]}`
	wrote := socialTestRequest(t, http.MethodPost, "/v4/diary2/ext/write", photo)
	if wrote.Code != http.StatusOK || !bytes.Contains(wrote.Body.Bytes(), []byte(`"imageTypeKind":"photo"`)) || !bytes.Contains(wrote.Body.Bytes(), []byte(`"imageLocationType":"obs"`)) {
		t.Fatalf("photo write = %d %s", wrote.Code, wrote.Body.String())
	}
	var post struct {
		Result store.DiaryPost `json:"result"`
	}
	if err := json.Unmarshal(wrote.Body.Bytes(), &post); err != nil || post.Result.DiaryNo == "" || post.Result.ImageTypeKind != "photo" || post.Result.ImageLocationType != "obs" {
		t.Fatalf("photo post = %s (%v)", wrote.Body.String(), err)
	}
	var imageShape struct {
		Result struct {
			ImageTypeKind     string                       `json:"imageTypeKind"`
			ImageLocationType string                       `json:"imageLocationType"`
			Images            []map[string]json.RawMessage `json:"images"`
		} `json:"result"`
	}
	if err := json.Unmarshal(wrote.Body.Bytes(), &imageShape); err != nil || imageShape.Result.ImageTypeKind != "photo" || imageShape.Result.ImageLocationType != "obs" || len(imageShape.Result.Images) != 1 || imageShape.Result.Images[0]["imageTypeKind"] != nil {
		t.Fatalf("photo metadata placement = %s (%v)", wrote.Body.String(), err)
	}
	if err := store.LoadSocialFrom(filepath.Join(dir, "social.json")); err != nil {
		t.Fatal(err)
	}
	unfold := serve(t, http.MethodGet, "/v4/diary2/ext/unfold/1/?lastSeq=-1&size=10")
	if unfold.Code != http.StatusOK || !bytes.Contains(unfold.Body.Bytes(), []byte(`"imageTypeKind":"photo"`)) || !bytes.Contains(unfold.Body.Bytes(), []byte(`"imageLocationType":"obs"`)) {
		t.Fatalf("photo unfold = %d %s", unfold.Code, unfold.Body.String())
	}
	text := socialTestRequest(t, http.MethodPost, "/v4/diary2/ext/write", `{"avatarId":"1","content":"text only","openState":"public"}`)
	if text.Code != http.StatusOK || !bytes.Contains(text.Body.Bytes(), []byte(`"imageLocationType":"none"`)) {
		t.Fatalf("text post location = %d %s", text.Code, text.Body.String())
	}
	album := serve(t, http.MethodGet, "/v4/diary2/ext/unfold/photo/1/?lastSeq=-1&size=15")
	if album.Code != http.StatusOK || !bytes.Contains(album.Body.Bytes(), []byte(`"diaryNo":"`+post.Result.DiaryNo+`"`)) || bytes.Contains(album.Body.Bytes(), []byte(`"title":"text only"`)) || !bytes.Contains(album.Body.Bytes(), []byte(`"imageLocationType":"obs"`)) {
		t.Fatalf("photo album = %d %s", album.Code, album.Body.String())
	}
	look := serve(t, http.MethodGet, "/v4/diary2/ext/look/1/"+post.Result.DiaryNo)
	if look.Code != http.StatusOK || !bytes.Contains(look.Body.Bytes(), []byte(`"imageTypeKind":"photo"`)) || !bytes.Contains(look.Body.Bytes(), []byte(`"imageLocationType":"obs"`)) || bytes.Contains(look.Body.Bytes(), []byte(`"comments"`)) || bytes.Contains(look.Body.Bytes(), []byte(`"likes"`)) {
		t.Fatalf("diary detail = %d %s", look.Code, look.Body.String())
	}
	badImage := strings.Replace(photo, imageURL, "https://elsewhere.invalid/photo", 1)
	if bad := socialTestRequest(t, http.MethodPost, "/v4/diary2/ext/write", badImage); bad.Code != http.StatusBadRequest {
		t.Fatalf("external photo URL status = %d body=%s", bad.Code, bad.Body.String())
	}
}

func TestRealAccountNotShadowedBySyntheticFriend(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1", Name: "Cherry"},
		"phone-token":       {AccessToken: "phone-token", Aid: "2", Name: "test"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	if profile := serve(t, http.MethodGet, "/v4/profile/2"); !bytes.Contains(profile.Body.Bytes(), []byte(`"name":"test"`)) {
		t.Fatalf("profile/2 = %s", profile.Body.String())
	}
	if status := serve(t, http.MethodGet, "/v4/friend/status/2"); status.Body.String() != `{"result":2}` {
		t.Fatalf("friend/status/2 = %s", status.Body.String())
	}
	search := serveRequest(t, httptest.NewRequest(http.MethodPost, "/v4/square/friends/search", strings.NewReader(`{"country":"JP","caricNickName":"2","searchType":"CODE"}`)))
	if !bytes.Contains(search.Body.Bytes(), []byte(`"caricNickName":"test"`)) || bytes.Contains(search.Body.Bytes(), []byte(`"caricNickName":"Friend"`)) {
		t.Fatalf("CODE 2 search = %s", search.Body.String())
	}

	// Allocation must never hand out the synthetic friend's aid.
	store.AccountsMu.Lock()
	reserved, _ := strconv.ParseUint(store.FriendAID, 10, 64)
	store.NextAvatarID = reserved - 1
	store.AccountsMu.Unlock()
	token := avAuthValue(t, createSession(t, guestGenerate(t)))
	created := createAvatar(t, token, []byte(`{"name":"Next","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON00164"]}`), "")
	var avatar avatarResponse
	if err := json.Unmarshal(created.Body.Bytes(), &avatar); err != nil || avatar.Result == nil || avatar.Result.AvatarID == store.FriendAID {
		t.Fatalf("create avatar = %d %s", created.Code, created.Body.String())
	}
}

type syncRow struct {
	AvatarNo   string `json:"avatarNo"`
	AvatarName string `json:"avatarName"`
	Status     int    `json:"status"`
}

func syncRowsAs(t *testing.T, token string) (map[string]syncRow, int) {
	t.Helper()
	rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/sync/friends/", "")
	var body struct {
		Result struct {
			FriendsCount int       `json:"friendsCount"`
			BuddyList    []syncRow `json:"buddyList"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("sync = %s", rec.Body.String())
	}
	rows := map[string]syncRow{}
	for _, row := range body.Result.BuddyList {
		rows[row.AvatarNo] = row
	}
	return rows, body.Result.FriendsCount
}

func TestRealFriendships(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"t1": {AccessToken: "t1", Aid: "1", Name: "One"},
		"t2": {AccessToken: "t2", Aid: "2", Name: "Two"},
		"t3": {AccessToken: "t3", Aid: "3", Name: "Three"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	path := filepath.Join(t.TempDir(), "social.json")
	if err := store.LoadSocialFrom(path); err != nil {
		t.Fatal(err)
	}
	do := func(token, method, target, body string) int {
		return socialTestRequestAs(t, token, method, target, body).Code
	}
	if c := do("t1", http.MethodPost, "/v4/friend/apply/1", `{"applyAvatarId":"1"}`); c != http.StatusBadRequest {
		t.Fatalf("self apply = %d", c)
	}
	if c := do("t2", http.MethodPut, "/v4/friend/accept/1", "null"); c != http.StatusNotFound {
		t.Fatalf("accept without request = %d", c)
	}
	relOf := func(token, aid string) string {
		p := socialTestRequestAs(t, token, http.MethodGet, "/v4/profile/"+aid, "").Body.String()
		s := socialTestRequestAs(t, token, http.MethodGet, "/v4/friend/status/"+aid, "").Body.String()
		q := socialTestRequestAs(t, token, http.MethodPost, "/v4/square/friends/search", `{"country":"JP","caricNickName":"`+aid+`","searchType":"CODE"}`).Body.String()
		var n string
		for _, c := range []string{"0", "1", "2", "3"} {
			if strings.Contains(p, `"friendStatus":`+c) && s == `{"result":`+c+`}` && strings.Contains(q, `"friendStatus":"`+c+`"`) {
				n = c
			}
		}
		return n
	}
	if got := relOf("t1", "2"); got != "2" {
		t.Fatalf("none relation = %q", got)
	}
	// The native client posts to /v4/friend/apply/ with an EMPTY path aid; the target is only in the body.
	if c := do("t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"2"}`); c != http.StatusOK {
		t.Fatalf("apply = %d", c)
	}
	if c := do("t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"999"}`); c != http.StatusNotFound {
		t.Fatalf("empty-path apply unknown = %d", c)
	}
	if c := do("t1", http.MethodPost, "/v4/friend/apply/", `{}`); c != http.StatusNotFound {
		t.Fatalf("empty-path apply no target = %d", c)
	}
	if got := relOf("t1", "2"); got != "3" {
		t.Fatalf("sent relation = %q", got)
	}
	if got := relOf("t2", "1"); got != "0" {
		t.Fatalf("received relation = %q", got)
	}
	rows1, _ := syncRowsAs(t, "t1")
	rows2, _ := syncRowsAs(t, "t2")
	if rows1["2"].Status != relSent || rows1[store.FriendAID].Status != 1 {
		t.Fatalf("applicant sync = %+v", rows1)
	}
	if rows2["1"].Status != 0 || rows2["1"].AvatarName != "One" {
		t.Fatalf("received request row = %+v", rows2)
	}
	if c := do("t2", http.MethodPut, "/v4/friend/accept/1", "null"); c != http.StatusOK {
		t.Fatalf("accept = %d", c)
	}
	rows1, n1 := syncRowsAs(t, "t1")
	rows2, n2 := syncRowsAs(t, "t2")
	if rows1["2"].Status != 1 || rows2["1"].Status != 1 || rows1[store.FriendAID].Status != 1 || n1 != 2 || n2 != 2 {
		t.Fatalf("accepted sync = %+v %+v", rows1, rows2)
	}
	if s := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/friend/status/2", "").Body.String(); s != `{"result":1}` {
		t.Fatalf("status = %s", s)
	}
	if s := socialTestRequestAs(t, "t3", http.MethodGet, "/v4/friend/status/2", "").Body.String(); s != `{"result":2}` {
		t.Fatalf("stranger status = %s", s)
	}
	if p := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/profile/2", "").Body.String(); !strings.Contains(p, `"friendStatus":1`) {
		t.Fatalf("profile = %s", p)
	}
	search := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/square/friends/search", `{"country":"JP","caricNickName":"2","searchType":"CODE"}`).Body.String()
	if !strings.Contains(search, `"friendStatus":"1"`) {
		t.Fatalf("search = %s", search)
	}

	// Persistence round trip.
	store.ResetSocial()
	if err := store.LoadSocialFrom(path); err != nil {
		t.Fatal(err)
	}
	if rows, _ := syncRowsAs(t, "t2"); rows["1"].Status != 1 {
		t.Fatalf("after reload = %+v", rows)
	}

	// Remove leaves -1 tombstones for both sides; synthetic friend untouched.
	if rec := socialTestRequestAs(t, "t2", http.MethodPost, "/v4/r/friend/remove/", `["1"]`); !strings.Contains(rec.Body.String(), `"success":["1"]`) {
		t.Fatalf("remove = %s", rec.Body.String())
	}
	rows1, n1 = syncRowsAs(t, "t1")
	rows2, _ = syncRowsAs(t, "t2")
	if rows1["2"].Status != -1 || rows2["1"].Status != -1 || rows1[store.FriendAID].Status != 1 || n1 != 1 {
		t.Fatalf("removed sync = %+v %+v", rows1, rows2)
	}
	if s := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/friend/status/2", "").Body.String(); s != `{"result":2}` {
		t.Fatalf("removed status = %s", s)
	}

	// Mutual apply auto-accepts (including re-apply after removal).
	do("t1", http.MethodPost, "/v4/friend/apply/2", `{"applyAvatarId":"2"}`)
	do("t2", http.MethodPost, "/v4/friend/apply/1", `{"applyAvatarId":"1"}`)
	if rows, _ := syncRowsAs(t, "t1"); rows["2"].Status != 1 {
		t.Fatalf("mutual apply = %+v", rows)
	}

	// Unauthenticated sync keeps the synthetic-only response.
	rec := serve(t, http.MethodGet, "/v4/sync/friends/")
	if strings.Contains(rec.Body.String(), `"avatarNo":"2"`) || !strings.Contains(rec.Body.String(), `"friendsCount":1`) {
		t.Fatalf("anonymous sync = %s", rec.Body.String())
	}
}

func TestRetiredAvatarIDsTombstoned(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1001", Name: "Cherry"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	sync := serve(t, http.MethodGet, "/v4/sync/friends/0")
	for _, aid := range []string{"1", "2"} {
		if !bytes.Contains(sync.Body.Bytes(), []byte(`"avatarNo":"`+aid+`","avatarName":"","buddyAvatarNo":"`+aid+`","status":-1`)) {
			t.Fatalf("sync lacks tombstone %s: %s", aid, sync.Body.String())
		}
	}
	if !bytes.Contains(sync.Body.Bytes(), []byte(`"friendsCount":1`)) {
		t.Fatalf("tombstones counted: %s", sync.Body.String())
	}
}
