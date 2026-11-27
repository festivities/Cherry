package web

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/lpn"
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
		"/v4/diary2/ext/checkExist/100000/0",
		"/v4/diary2/ext/checkExist/100000/0/extra/",
	} {
		if response := serve(t, http.MethodGet, target); response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d %s, want 404", target, response.Code, response.Body.String())
		}
	}
	if missing := serve(t, http.MethodGet, "/v4/diary2/ext/checkExist/100000/1/"); missing.Code != http.StatusBadRequest || missing.Body.String() != `{"errorCode":"200003"}` {
		t.Errorf("GET missing diary = %d %s, want 400 200003", missing.Code, missing.Body.String())
	}
	if method := serve(t, http.MethodPost, "/v4/diary2/ext/checkExist/100000/0/"); method.Code != http.StatusNotFound {
		t.Errorf("POST checkExist = %d %s, want 404", method.Code, method.Body.String())
	}
}

func TestDiaryVisibilityUsesOpenStateAndKnownViewer(t *testing.T) {
	testutil.UseDatabase(t)
	recordPicture(t, store.MediaDiary, "1", "1", "1710000000", "1_1710000000")
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
			request["images"] = []store.DiaryImage{{ImageURL: "/lineplay/d/download.nhn?oid=1_1710000000&ctime=1710000000&userid=1"}}
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
	// The legacy photo post as the old file held it, normalized as the load did; the database holds it from the attach.
	legacy := store.DiaryPost{DiaryNo: "9", AvatarID: "1", Nickname: "Cherry", Title: "legacy photo", Content: "keep me", OpenState: "public", ImageTypeKind: "obs",
		Images: []store.DiaryImage{{ImageURL: "/lineplay/d/download.nhn?oid=42_1710000000&ctime=1710000000&userid=42"}}}
	store.NormalizeDiaryPost(&legacy)
	store.SocialMu.Lock()
	store.NextDiaryNo = 9
	store.DiaryPosts = []store.DiaryPost{legacy}
	store.SocialMu.Unlock()
	testutil.UseDatabase(t)
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
	testutil.UseDatabase(t)
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
	if err := store.ReloadSocial(); err != nil {
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

	testutil.UseDatabase(t)
	// The photo is one aid 1 uploaded (a row names its owner), so the post may name it.
	imageURL := "/lineplay/d/download.nhn?oid=1_1710000000&ctime=1710000000&userid=1"
	recordPicture(t, store.MediaDiary, "1", "1", "1710000000", "1_1710000000")
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
	if err := store.ReloadSocial(); err != nil {
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
	if look.Code != http.StatusOK || !bytes.Contains(look.Body.Bytes(), []byte(`"imageTypeKind":"photo"`)) || !bytes.Contains(look.Body.Bytes(), []byte(`"imageLocationType":"obs"`)) || !bytes.Contains(look.Body.Bytes(), []byte(`"comments":{"items":[],"lastData":true}`)) || !bytes.Contains(look.Body.Bytes(), []byte(`"likes":{"items":[],"lastData":true}`)) {
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
	testutil.UseDatabase(t)
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
	if grouped := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/square/friends/search", `{"country":"JP","caricNickName":"-2","searchType":"CODE"}`).Body.String(); grouped != search {
		t.Fatalf("grouped code search = %s, want %s", grouped, search)
	}
	if !strings.Contains(search, `"friendStatus":"1"`) {
		t.Fatalf("search = %s", search)
	}

	// Persistence round trip.
	store.ResetSocial()
	if err := store.ReloadSocial(); err != nil {
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

func TestInviteScreenRoutes(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"invite-token": {AccessToken: "invite-token", Aid: "7", Name: "Cherry"},
	})
	for _, c := range []struct{ method, target, want string }{
		{http.MethodGet, "/v4/friends/invitation/mission/info", inviteMissionBody},
		{http.MethodGet, "/v4/friends/invitation/list?lastSeq=-1&size=100", inviteListBody},
		{http.MethodPost, "/v4/badge/IFRD/0", badgeSetAckBody},
		{http.MethodPost, "/v4/badge/nopush/CHAT/3", badgeSetAckBody},
	} {
		if rec := serveRequest(t, httptest.NewRequest(c.method, c.target, nil)); rec.Code != http.StatusOK || rec.Body.String() != c.want {
			t.Fatalf("%s %s: status = %d, body = %q", c.method, c.target, rec.Code, rec.Body.String())
		}
	}
	if rec := serve(t, http.MethodGet, "/v4/recommend/code"); rec.Code != http.StatusNotFound {
		t.Fatalf("recommend/code without session: status = %d, want 404", rec.Code)
	}
	rec := socialTestRequestAs(t, "invite-token", http.MethodGet, "/v4/recommend/code", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"recommendCode":"7"`) {
		t.Fatalf("recommend/code: status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestProfileFriendApplyAndRelationship(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"t1": {AccessToken: "t1", Aid: "1", Name: "One"},
		"t2": {AccessToken: "t2", Aid: "2", Name: "Two"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	for _, aid := range []string{"2", store.FriendAID} {
		rec := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/profile/"+aid, "")
		var out struct {
			Result map[string]any `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("GET /v4/profile/%s = %d %s", aid, rec.Code, rec.Body.String())
		}
		if out.Result["friendApplyYn"] != true {
			t.Errorf("GET /v4/profile/%s friendApplyYn = %v, want true", aid, out.Result["friendApplyYn"])
		}
	}

	for _, aid := range []string{"2", store.FriendAID} {
		rec := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/friend/relationship/"+aid, "")
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{}}` {
			t.Errorf("GET /v4/friend/relationship/%s = %d %s, want 200 {\"result\":{}}", aid, rec.Code, rec.Body.String())
		}
	}
	if rec := serve(t, http.MethodGet, "/v4/friend/relationship/999"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /v4/friend/relationship/999 = %d, want 404", rec.Code)
	}
	if rec := serve(t, http.MethodPost, "/v4/friend/relationship/2"); rec.Code != http.StatusNotFound {
		t.Errorf("POST /v4/friend/relationship/2 = %d, want 404", rec.Code)
	}
}

func TestDiaryCheckExistEntry(t *testing.T) {
	testutil.UseDatabase(t)
	installSocialTestAccounts(t, map[string]*store.Account{
		"owner":    {AccessToken: "owner", Aid: "1", Name: "Owner"},
		"stranger": {AccessToken: "stranger", Aid: "2", Name: "Stranger"},
		"friend":   {AccessToken: "friend", Aid: "3", Name: "Friend"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	pubNo := diaryPost(t, "owner", "1", "public")
	privNo := diaryPost(t, "owner", "1", "private")

	// Make aid 3 an accepted friend of aid 1.
	store.SocialMu.Lock()
	store.Friendships = append(store.Friendships, store.Friendship{A: "1", B: "3", State: "accepted"})
	if err := store.SaveSocialLocked(store.SocialChange{Friends: [][2]string{{"1", "3"}}}); err != nil {
		store.SocialMu.Unlock()
		t.Fatal(err)
	}
	store.SocialMu.Unlock()

	check := func(token, path string) (int, bool) {
		t.Helper()
		rec := diaryCall(t, token, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			return rec.Code, false
		}
		var out struct {
			Result struct {
				FrndApplyYn bool `json:"frndApplyYn"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal %s: %v (%s)", path, err, rec.Body.String())
		}
		return rec.Code, out.Result.FrndApplyYn
	}

	for _, suffix := range []string{"", "/"} {
		path := "/v4/diary2/ext/checkExist/entry/1/" + pubNo + suffix
		if code, yn := check("stranger", path); code != http.StatusOK || !yn {
			t.Errorf("stranger %s = (%d, %v), want (200, true)", path, code, yn)
		}
		if code, yn := check("owner", path); code != http.StatusOK || yn {
			t.Errorf("owner %s = (%d, %v), want (200, false)", path, code, yn)
		}
		if code, yn := check("friend", path); code != http.StatusOK || yn {
			t.Errorf("friend %s = (%d, %v), want (200, false)", path, code, yn)
		}
		if code, yn := check("", path); code != http.StatusOK || yn {
			t.Errorf("anonymous %s = (%d, %v), want (200, false)", path, code, yn)
		}
	}

	privPath := "/v4/diary2/ext/checkExist/entry/1/" + privNo
	if code, _ := check("owner", privPath); code != http.StatusOK {
		t.Errorf("owner private %s = %d, want 200", privPath, code)
	}
	if code, _ := check("stranger", privPath); code != http.StatusNotFound {
		t.Errorf("stranger private %s = %d, want 404", privPath, code)
	}
	if code, _ := check("stranger", "/v4/diary2/ext/checkExist/entry/1/99999"); code != http.StatusNotFound {
		t.Errorf("missing post = %d, want 404", code)
	}
	for _, bad := range []string{
		"/v4/diary2/ext/checkExist/entry/bad/1",
		"/v4/diary2/ext/checkExist/entry/1/abc",
		"/v4/diary2/ext/checkExist/entry/1/1/extra",
	} {
		if rec := serve(t, http.MethodGet, bad); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", bad, rec.Code)
		}
	}
}

func TestDiaryModify(t *testing.T) {
	testutil.UseDatabase(t)
	recordPicture(t, store.MediaDiary, "1", "1", "1710000000", "1_1710000000")
	recordPicture(t, store.MediaDiary, "2", "2", "1710000001", "2_1710000001")
	installSocialTestAccounts(t, map[string]*store.Account{
		"owner": {AccessToken: "owner", Aid: "1", Name: "Owner"},
		"other": {AccessToken: "other", Aid: "2", Name: "Other"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	no := diaryPost(t, "owner", "1", "public")
	// Add a comment so we verify the modify response includes comments/likes counts and pages.
	cmt := socialTestRequestAs(t, "other", http.MethodPost, "/v4/diary2/comment/write/1",
		`{"commentAvatarId":"1","diaryNo":`+no+`,"avatarID":"2","content":"nice"}`)
	if cmt.Code != http.StatusOK {
		t.Fatalf("comment write = %d %s", cmt.Code, cmt.Body.String())
	}

	ownImage := "/lineplay/d/download.nhn?oid=1_1710000000&ctime=1710000000&userid=1"
	foreignImage := "/lineplay/d/download.nhn?oid=2_1710000001&ctime=1710000001&userid=2"

	// Reject non-owner, foreign image, invalid body, unknown post, and unauthenticated.
	if rec := serve(t, http.MethodPost, "/v4/diary2/ext/modify"); rec.Code != http.StatusNotFound {
		t.Errorf("anonymous modify = %d, want 404", rec.Code)
	}
	if rec := socialTestRequestAs(t, "owner", http.MethodGet, "/v4/diary2/ext/modify", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET modify = %d, want 404", rec.Code)
	}
	if rec := socialTestRequestAs(t, "other", http.MethodPost, "/v4/diary2/ext/modify",
		`{"diaryNo":"`+no+`","avatarId":"1","title":"x","content":"y"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("mismatched avatarId = %d, want 400", rec.Code)
	}
	if rec := socialTestRequestAs(t, "other", http.MethodPost, "/v4/diary2/ext/modify",
		`{"diaryNo":"`+no+`","avatarId":"2","title":"x","content":"y"}`); rec.Code != http.StatusNotFound {
		t.Errorf("non-owner modify = %d, want 404", rec.Code)
	}
	if rec := socialTestRequestAs(t, "owner", http.MethodPost, "/v4/diary2/ext/modify",
		`{"diaryNo":"`+no+`","avatarId":"1","title":"x","content":"y","images":[{"imageUrl":"`+foreignImage+`"}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("foreign image modify = %d, want 400", rec.Code)
	}
	if rec := socialTestRequestAs(t, "owner", http.MethodPost, "/v4/diary2/ext/modify",
		`{"diaryNo":"abc","avatarId":"1","title":"x","content":"y"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("non-digit diaryNo = %d, want 400", rec.Code)
	}

	// Valid modify updating title, content, openState, feelingState, imageTypeKind, and images.
	modBody, _ := json.Marshal(map[string]any{
		"diaryNo":       no,
		"avatarId":      "1",
		"title":         "Updated Title",
		"content":       "Updated Body",
		"openState":     "private",
		"feelingState":  3,
		"imageTypeKind": "photo",
		"images":        []store.DiaryImage{{ImageURL: ownImage}, {ImageURL: ""}},
	})
	mod := socialTestRequestAs(t, "owner", http.MethodPost, "/v4/diary2/ext/modify", string(modBody))
	if mod.Code != http.StatusOK {
		t.Fatalf("modify = %d %s", mod.Code, mod.Body.String())
	}
	var detail struct {
		Result struct {
			DiaryNo           string             `json:"diaryNo"`
			Title             string             `json:"title"`
			Content           string             `json:"content"`
			OpenState         string             `json:"openState"`
			FeelingState      int                `json:"feelingState"`
			ImageTypeKind     string             `json:"imageTypeKind"`
			ImageLocationType string             `json:"imageLocationType"`
			CommentCount      int                `json:"commentCount"`
			Images            []store.DiaryImage `json:"images"`
			Comments          struct {
				Items []testComment `json:"items"`
			} `json:"comments"`
			Likes struct {
				Items []any `json:"items"`
			} `json:"likes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(mod.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal modify: %v (%s)", err, mod.Body.String())
	}
	r := detail.Result
	if r.DiaryNo != no || r.Title != "Updated Title" || r.Content != "Updated Body" || r.OpenState != "private" ||
		r.FeelingState != 3 || r.ImageTypeKind != "photo" || r.ImageLocationType != "obs" ||
		len(r.Images) != 1 || r.Images[0].ImageURL != ownImage || r.CommentCount != 1 || len(r.Comments.Items) != 1 || r.Likes.Items == nil {
		t.Fatalf("unexpected modify result: %+v", r)
	}

	// Persists across reload and shows up in handleDiaryLook.
	if err := store.ReloadSocial(); err != nil {
		t.Fatal(err)
	}
	look := socialTestRequestAs(t, "owner", http.MethodGet, "/v4/diary2/ext/look/1/"+no, "")
	if look.Code != http.StatusOK || !strings.Contains(look.Body.String(), `"title":"Updated Title"`) || !strings.Contains(look.Body.String(), `"openState":"private"`) {
		t.Fatalf("look after reload = %d %s", look.Code, look.Body.String())
	}
	// Now private, so stranger cannot view it.
	if lookOther := socialTestRequestAs(t, "other", http.MethodGet, "/v4/diary2/ext/look/1/"+no, ""); lookOther.Code != http.StatusNotFound {
		t.Errorf("stranger look on modified private post = %d, want 404", lookOther.Code)
	}
}

func TestDiaryIntroSaveAndGet(t *testing.T) {
	testutil.UseDatabase(t)
	installSocialTestAccounts(t, map[string]*store.Account{
		"owner": {
			AccessToken:  "owner",
			Aid:          "1",
			Name:         "Cherry",
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/1_1"},
			DiaryProfile: &store.DiaryProfile{
				StatusMessage: "keep status",
				Location:      "Tokyo",
				Photos:        []store.ProfilePhoto{{ImageID: "p1", ImageURL: "/lineplay/pr/p1", Main: true}},
			},
		},
		"visitor": {AccessToken: "visitor", Aid: "2", Name: "Visitor"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	// Invalid requests.
	if rec := socialTestRequestAs(t, "owner", http.MethodGet, "/v4/diary2/intro", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /v4/diary2/intro = %d, want 404 (not 307)", rec.Code)
	}
	if rec := socialTestRequestAs(t, "", http.MethodPost, "/v4/diary2/intro", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("anonymous POST /v4/diary2/intro = %d, want 404", rec.Code)
	}
	if rec := socialTestRequestAs(t, "owner", http.MethodPost, "/v4/diary2/intro",
		`{"avatarId":"2","diaryName":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("mismatched avatarId = %d, want 400", rec.Code)
	}
	if rec := socialTestRequestAs(t, "owner", http.MethodPost, "/v4/diary2/intro",
		`{"avatarId":"1","introduce":"bad\u0007ctrl"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("control char introduce = %d, want 400", rec.Code)
	}

	saveBody := `{"avatarId":"1","diaryName":"My Diary","introduce":"Line 1\nLine 2","skinId":"00002","birthDay":"1999-05-01","birthDaySecret":true,"bloodType":"255T_2","bloodTypeSecret":false,"job":"256C_1","jobSecret":true,"sex":"255S_2","sexSecret":false}`
	saved := socialTestRequestAs(t, "owner", http.MethodPost, "/v4/diary2/intro", saveBody)
	if saved.Code != http.StatusOK {
		t.Fatalf("POST /v4/diary2/intro = %d %s", saved.Code, saved.Body.String())
	}

	checkIntro := func(rec *httptest.ResponseRecorder, wantSecret bool) {
		t.Helper()
		var out struct {
			Result map[string]any `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("intro = %d %s", rec.Code, rec.Body.String())
		}
		r := out.Result
		wantBirthDay := ""
		wantJob := ""
		if wantSecret {
			wantBirthDay = "1999-05-01"
			wantJob = "256C_1"
		}
		checks := map[string]any{
			"avatarId":                 "1",
			"nickname":                 "Cherry",
			"diaryName":                "My Diary",
			"introduce":                "Line 1\nLine 2",
			"skinId":                   "00002",
			"birthDay":                 wantBirthDay,
			"birthDaySecret":           true,
			"bloodType":                "255T_2",
			"bloodTypeSecret":          false,
			"job":                      wantJob,
			"jobSecret":                true,
			"sex":                      "255S_2",
			"sexSecret":                false,
			"created":                  true,
			"obsProfileImagePath":      "/lineplay/pr/1_1",
			"showGuestBook":            true,
			"showCmt":                  true,
			"useCmtTagBrandAvatarOnly": false,
			"showProfile":              true,
		}
		for k, want := range checks {
			if r[k] != want {
				t.Errorf("%s = %#v, want %#v", k, r[k], want)
			}
		}
		if r["todayVisitor"] != float64(0) || r["totalVisitor"] != float64(0) {
			t.Errorf("visitors = (%v, %v), want (0, 0)", r["todayVisitor"], r["totalVisitor"])
		}
	}

	checkIntro(saved, true)

	// Reload from DB and confirm persistence + StatusMessage/Location/Photos preserved.
	if err := store.ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	acc, ok := store.AccountByAvatarID("1")
	if !ok || acc.DiaryProfile == nil || acc.DiaryProfile.StatusMessage != "keep status" ||
		acc.DiaryProfile.Location != "Tokyo" || len(acc.DiaryProfile.Photos) != 1 {
		t.Fatalf("preserved profile fields lost: %+v", acc.DiaryProfile)
	}

	checkIntro(socialTestRequestAs(t, "owner", http.MethodGet, "/v4/diary2/intro/1", ""), true)
	checkIntro(socialTestRequestAs(t, "visitor", http.MethodGet, "/v4/diary2/intro/1", ""), false)
}

func TestDiarySkinList(t *testing.T) {
	for _, path := range []string{
		"/v4/diary2/skinListWithVersionOS/Android/0?osVersion=10.1.0.0&isBase=false",
		"/v4/diary2/skinListWithVersionOS/iPhone/0?osVersion=10.1.0&isBase=false",
		"/v4/diary2/skinListWithVersionOS/Android/ALL",
	} {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s, want 200", path, rec.Code, rec.Body.String())
		}
		var out struct {
			Result []json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET %s invalid JSON: %v", path, err)
		}
		if len(out.Result) != 2 {
			t.Fatalf("GET %s result len = %d, want 2", path, len(out.Result))
		}
		var entries []diarySkinEntry
		if err := json.Unmarshal(out.Result[0], &entries); err != nil {
			t.Fatalf("GET %s result[0]: %v", path, err)
		}
		if len(entries) != 7 {
			t.Fatalf("GET %s entries = %d, want 7", path, len(entries))
		}
		wantResources := []int{1, 2, 3, 5, 9, 15, 16}
		wantIDs := []string{"default", "00002", "00003", "00005", "00009", "00015", "00016"}
		for i, e := range entries {
			if e.SkinSn != i+1 || e.SkinID != wantIDs[i] || e.SkinCategoryID != "default" ||
				e.ResourceNumber != wantResources[i] || e.Flag != 0 || e.Sort != i+1 ||
				!e.HasSkin || e.SkinNm != wantIDs[i] || e.Version != 7 {
				t.Errorf("entry[%d] = %+v", i, e)
			}
		}
		var common struct {
			CommonVersion int `json:"commonVersion"`
		}
		if err := json.Unmarshal(out.Result[1], &common); err != nil || common.CommonVersion != 7 {
			t.Fatalf("GET %s result[1] = %s (%v), want commonVersion=7", path, out.Result[1], err)
		}
	}

	for _, bad := range []string{
		"/v4/diary2/skinListWithVersionOS/Windows/0",
		"/v4/diary2/skinListWithVersionOS/Android/",
		"/v4/diary2/skinListWithVersionOS/Android/0/",
		"/v4/diary2/skinListWithVersionOS/Android",
	} {
		if rec := serve(t, http.MethodGet, bad); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", bad, rec.Code)
		}
	}
	if rec := serve(t, http.MethodPost, "/v4/diary2/skinListWithVersionOS/Android/0"); rec.Code != http.StatusNotFound {
		t.Errorf("POST skinListWithVersionOS = %d, want 404", rec.Code)
	}
}

func TestDiarySkinInfo(t *testing.T) {
	cases := []struct {
		path    string
		wantID  string
		wantRes int
	}{
		{"/v4/diary2/skinInfo/Android?diary_skin_id=default&osVersion=10.1.0.0", "default", 1},
		{"/v4/diary2/skinInfo/Android?osVersion=10.1.0.0", "default", 1},
		{"/v4/diary2/skinInfo/Android?diary_skin_id=", "default", 1},
		{"/v4/diary2/skinInfo/Android/?diary_skin_id=00005", "00005", 5},
		{"/v4/diary2/skinInfo/iPhone?diary_skin_id=00016&osVersion=10.1.0", "00016", 16},
	}
	for _, tc := range cases {
		rec := serve(t, http.MethodGet, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s, want 200", tc.path, rec.Code, rec.Body.String())
		}
		var out struct {
			Result diarySkinEntry `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET %s invalid JSON: %v", tc.path, err)
		}
		if out.Result.SkinID != tc.wantID || out.Result.ResourceNumber != tc.wantRes || out.Result.Version != 7 || !out.Result.HasSkin {
			t.Errorf("GET %s result = %+v, want id=%q resourceNumber=%d", tc.path, out.Result, tc.wantID, tc.wantRes)
		}
	}

	for _, bad := range []string{
		"/v4/diary2/skinInfo/Android?diary_skin_id=99999",
		"/v4/diary2/skinInfo/Android?diary_skin_id=00004",
		"/v4/diary2/skinInfo/Windows?diary_skin_id=default",
		"/v4/diary2/skinInfo/",
		"/v4/diary2/skinInfo/Android/extra",
	} {
		if rec := serve(t, http.MethodGet, bad); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", bad, rec.Code)
		}
	}
	if rec := serve(t, http.MethodPost, "/v4/diary2/skinInfo/Android?diary_skin_id=default"); rec.Code != http.StatusNotFound {
		t.Errorf("POST skinInfo = %d, want 404", rec.Code)
	}
}

func TestFriendRemoveNativeIntegersAndValidation(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"t1": {AccessToken: "t1", Aid: "1001", Name: "One"},
		"t2": {AccessToken: "t2", Aid: "1002", Name: "Two"},
		"t3": {AccessToken: "t3", Aid: "1003", Name: "Three"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	testutil.UseDatabase(t)

	// Make 1001 friends with both 1002 and 1003.
	if c := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`); c.Code != http.StatusOK {
		t.Fatalf("apply 1002 = %d", c.Code)
	}
	if c := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1003"}`); c.Code != http.StatusOK {
		t.Fatalf("apply 1003 = %d", c.Code)
	}
	if c := socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/accept/1001", "null\x00"); c.Code != http.StatusOK {
		t.Fatalf("accept 1002 = %d", c.Code)
	}
	if c := socialTestRequestAs(t, "t3", http.MethodPut, "/v4/friend/accept/1001", "null\x00"); c.Code != http.StatusOK {
		t.Fatalf("accept 1003 = %d", c.Code)
	}

	// Unquoted integer array with trailing NUL, and verify JSON string array response.
	rec := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/remove/", "[1002]\x00")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"success":["1002"]}}` {
		t.Fatalf("POST /v4/friend/remove/ [1002] = %d %s", rec.Code, rec.Body.String())
	}
	rows1, _ := syncRowsAs(t, "t1")
	rows2, _ := syncRowsAs(t, "t2")
	if rows1["1002"].Status != -1 || rows2["1001"].Status != -1 {
		t.Fatalf("removed sync = %+v %+v", rows1["1002"], rows2["1001"])
	}
	if s := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/friend/status/1002", ""); s.Body.String() != `{"result":2}` {
		t.Fatalf("removed status = %s", s.Body.String())
	}

	// Re-applying after removal works.
	if c := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`); c.Code != http.StatusOK {
		t.Fatalf("re-apply 1002 = %d %s", c.Code, c.Body.String())
	}
	if c := socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/accept/1001", "null\x00"); c.Code != http.StatusOK {
		t.Fatalf("re-accept 1002 = %d", c.Code)
	}

	// Multi-element integer array removes multiple friends at once and persists across reload.
	rec = socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/remove/", "[1002,1003]\x00")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"success":["1002","1003"]}}` {
		t.Fatalf("POST /v4/friend/remove/ [1002,1003] = %d %s", rec.Code, rec.Body.String())
	}
	store.ResetSocial()
	if err := store.ReloadSocial(); err != nil {
		t.Fatal(err)
	}
	rows1, _ = syncRowsAs(t, "t1")
	rows2, _ = syncRowsAs(t, "t2")
	rows3, _ := syncRowsAs(t, "t3")
	if rows1["1002"].Status != -1 || rows1["1003"].Status != -1 || rows2["1001"].Status != -1 || rows3["1001"].Status != -1 {
		t.Fatalf("multi-remove after reload = %+v %+v %+v", rows1, rows2, rows3)
	}

	// Invalid payloads rejected with 400.
	over100 := "[" + strings.Repeat("1002,", 100) + "1003]"
	for _, bad := range []string{"[true]", "[1.5]", "[-1]", "[null]", "[{}]", "[[]]", "{}", "null", over100} {
		if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/remove/", bad); r.Code != http.StatusBadRequest {
			t.Errorf("POST /v4/friend/remove/ %s = %d, want 400", bad, r.Code)
		}
	}
}

func TestFriendDefer(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"t1": {AccessToken: "t1", Aid: "1001", Name: "One"},
		"t2": {AccessToken: "t2", Aid: "1002", Name: "Two"},
		"t3": {AccessToken: "t3", Aid: "1003", Name: "Three"},
		"t4": {AccessToken: "t4", Aid: "1004", Name: "Four"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	testutil.UseDatabase(t)

	// 1001 -> 1002 incoming request for t2; 1002 -> 1003 outgoing sent by t2; 1004 <-> 1002 accepted friendship.
	if c := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`); c.Code != http.StatusOK {
		t.Fatalf("apply 1001->1002 = %d", c.Code)
	}
	if c := socialTestRequestAs(t, "t2", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1003"}`); c.Code != http.StatusOK {
		t.Fatalf("apply 1002->1003 = %d", c.Code)
	}
	if c := socialTestRequestAs(t, "t4", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`); c.Code != http.StatusOK {
		t.Fatalf("apply 1004->1002 = %d", c.Code)
	}
	if c := socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/accept/1004", "null\x00"); c.Code != http.StatusOK {
		t.Fatalf("accept 1004 = %d", c.Code)
	}

	badge := socialTestRequestAs(t, "t2", http.MethodGet, "/v4/badge/infos/", "")
	if !strings.Contains(badge.Body.String(), `"IFRD":1`) {
		t.Fatalf("before defer badge = %s, want IFRD:1", badge.Body.String())
	}

	// Unauthenticated / bad method / invalid paths.
	if r := serveRequest(t, httptest.NewRequest(http.MethodPut, "/v4/friend/defer/1001", strings.NewReader("null\x00"))); r.Code != http.StatusNotFound {
		t.Errorf("unauth defer = %d, want 404", r.Code)
	}
	for _, bad := range []struct{ method, path string }{
		{http.MethodGet, "/v4/friend/defer/1001"},
		{http.MethodPut, "/v4/friend/defer/"},
		{http.MethodPut, "/v4/friend/defer/1001/extra"},
		{http.MethodPut, "/v4/friend/defer/abc"},
	} {
		if r := socialTestRequestAs(t, "t2", bad.method, bad.path, "null\x00"); r.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", bad.method, bad.path, r.Code)
		}
	}

	// Decline incoming request from 1001.
	def := socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/defer/1001", "null\x00")
	if def.Code != http.StatusOK || def.Body.String() != `{"result":1}` {
		t.Fatalf("PUT /v4/friend/defer/1001 = %d %s", def.Code, def.Body.String())
	}
	badge = socialTestRequestAs(t, "t2", http.MethodGet, "/v4/badge/infos/", "")
	if !strings.Contains(badge.Body.String(), `"IFRD":0`) {
		t.Fatalf("after defer badge = %s, want IFRD:0", badge.Body.String())
	}
	// Idempotent second defer also succeeds.
	if again := socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/defer/1001", "null\x00"); again.Code != http.StatusOK || again.Body.String() != `{"result":1}` {
		t.Fatalf("repeat defer = %d %s", again.Code, again.Body.String())
	}

	// Deferring an accepted friend or an outgoing sent request does NOT remove them.
	if r := socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/defer/1004", "null\x00"); r.Code != http.StatusOK || r.Body.String() != `{"result":1}` {
		t.Fatalf("defer accepted = %d %s", r.Code, r.Body.String())
	}
	if r := socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/defer/1003", "null\x00"); r.Code != http.StatusOK || r.Body.String() != `{"result":1}` {
		t.Fatalf("defer outgoing = %d %s", r.Code, r.Body.String())
	}

	store.ResetSocial()
	if err := store.ReloadSocial(); err != nil {
		t.Fatal(err)
	}
	rows2, _ := syncRowsAs(t, "t2")
	if rows2["1001"].Status != -1 || rows2["1004"].Status != 1 || rows2["1003"].Status != relSent {
		t.Fatalf("sync after defer + reload = %+v", rows2)
	}
}

func TestIdentityModeRetiresSyntheticFriend(t *testing.T) {
	useConfig(t, devFakeConfig())
	installSocialTestAccounts(t, map[string]*store.Account{
		"t1": {AccessToken: "t1", Aid: "1001", Name: "One"},
		"t2": {AccessToken: "t2", Aid: "1002", Name: "Two"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.SocialMu.Lock()
	store.FriendBookmarked = true
	store.SocialMu.Unlock()

	// sync/friends tombstones 100000 with status -1, excludes it from bookmarks and friendsCount.
	syncRec := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/sync/friends/0", "")
	if syncRec.Code != http.StatusOK {
		t.Fatalf("sync = %d %s", syncRec.Code, syncRec.Body.String())
	}
	var syncOut struct {
		Result struct {
			FriendsCount int       `json:"friendsCount"`
			Bookmarks    []string  `json:"bookmarks"`
			BuddyList    []syncRow `json:"buddyList"`
		} `json:"result"`
	}
	if err := json.Unmarshal(syncRec.Body.Bytes(), &syncOut); err != nil {
		t.Fatal(err)
	}
	if syncOut.Result.FriendsCount != 0 || len(syncOut.Result.Bookmarks) != 0 {
		t.Fatalf("identity sync counts = %+v", syncOut.Result)
	}
	var foundTombstone bool
	for _, b := range syncOut.Result.BuddyList {
		if b.AvatarNo == store.FriendAID {
			foundTombstone = b.Status == -1
		}
	}
	if !foundTombstone {
		t.Fatalf("sync missing -1 tombstone for 100000: %+v", syncOut.Result.BuddyList)
	}

	// Profile friendsCount excludes 100000 and friendStatus for 100000 is relNone (2).
	if p := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/profile/1001", ""); !strings.Contains(p.Body.String(), `"friendsCount":0`) {
		t.Fatalf("profile/1001 = %s", p.Body.String())
	}
	if s := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/friend/status/100000", ""); s.Body.String() != `{"result":2}` {
		t.Fatalf("status/100000 = %s", s.Body.String())
	}

	// Friend search excludes 100000 (by CODE and NICK) while still finding real accounts.
	for _, body := range []string{
		`{"country":"JP","caricNickName":"100000","searchType":"CODE"}`,
		`{"country":"JP","caricNickName":"Friend","searchType":"NICK"}`,
	} {
		sr := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/square/friends/search", body)
		if sr.Code != http.StatusOK || !strings.Contains(sr.Body.String(), `"items":[]`) {
			t.Fatalf("search %s = %d %s", body, sr.Code, sr.Body.String())
		}
	}
	srReal := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/square/friends/search", `{"country":"JP","caricNickName":"1002","searchType":"CODE"}`)
	if srReal.Code != http.StatusOK || !strings.Contains(srReal.Body.String(), `"avatarId":"1002"`) {
		t.Fatalf("search real = %d %s", srReal.Code, srReal.Body.String())
	}

	// Apply and accept against 100000 are refused with 404.
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"100000"}`); r.Code != http.StatusNotFound {
		t.Errorf("apply 100000 = %d, want 404", r.Code)
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPut, "/v4/friend/accept/100000", "null\x00"); r.Code != http.StatusNotFound {
		t.Errorf("accept 100000 = %d, want 404", r.Code)
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPut, "/v4/friend/accept/100000/1", "null\x00"); r.Code != http.StatusNotFound {
		t.Errorf("news accept 100000 = %d, want 404", r.Code)
	}

	// POST /v4/friend/remove/ with [100000]\x00 returns string success without mutating store.FriendRemoved.
	rem := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/remove/", "[100000]\x00")
	if rem.Code != http.StatusOK || rem.Body.String() != `{"result":{"success":["100000"]}}` {
		t.Fatalf("remove [100000] = %d %s", rem.Code, rem.Body.String())
	}
	store.SocialMu.Lock()
	removedFlag := store.FriendRemoved
	store.SocialMu.Unlock()
	if removedFlag {
		t.Fatalf("store.FriendRemoved mutated to true in identity mode")
	}
}

func TestRealFriendBookmarks(t *testing.T) {
	useConfig(t, devFakeConfig())
	testutil.UseDatabase(t)
	installSocialTestAccounts(t, map[string]*store.Account{
		"t1": {AccessToken: "t1", Aid: "1001", Name: "One"},
		"t2": {AccessToken: "t2", Aid: "1002", Name: "Two"},
		"t3": {AccessToken: "t3", Aid: "1003", Name: "Three"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	bookmarksOf := func(token string) []string {
		t.Helper()
		rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/sync/friends/0", "")
		var out struct {
			Result struct {
				Bookmarks []string `json:"bookmarks"`
			} `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("sync = %d %s", rec.Code, rec.Body.String())
		}
		return out.Result.Bookmarks
	}

	// Non-friend and retired synthetic 100000 cannot be bookmarked in identity mode.
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/bookmark/1002", "{}\x00"); r.Code != http.StatusNotFound {
		t.Fatalf("bookmark non-friend = %d, want 404", r.Code)
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/bookmark/100000", "{}\x00"); r.Code != http.StatusNotFound {
		t.Fatalf("bookmark 100000 in identity mode = %d, want 404", r.Code)
	}

	// Make 1001 and 1002 friends; 1003 stays pending.
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`); r.Code != http.StatusOK {
		t.Fatalf("apply 1002 = %d", r.Code)
	}
	if r := socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/accept/1001", "null\x00"); r.Code != http.StatusOK {
		t.Fatalf("accept 1001 = %d", r.Code)
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1003"}`); r.Code != http.StatusOK {
		t.Fatalf("apply 1003 = %d", r.Code)
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/bookmark/1003", "{}\x00"); r.Code != http.StatusNotFound {
		t.Fatalf("bookmark pending friend = %d, want 404", r.Code)
	}

	// Add bookmark for accepted friend 1002 (and verify idempotency + persistence).
	for i := 0; i < 2; i++ {
		r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/bookmark/1002", "{}\x00")
		if r.Code != http.StatusOK || r.Body.String() != `{"result":1}` {
			t.Fatalf("bookmark 1002 = %d %s", r.Code, r.Body.String())
		}
	}
	if got := bookmarksOf("t1"); len(got) != 1 || got[0] != "1002" {
		t.Fatalf("t1 bookmarks = %v, want [1002]", got)
	}
	if got := bookmarksOf("t2"); len(got) != 0 {
		t.Fatalf("t2 bookmarks = %v, want []", got)
	}
	if err := store.ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	if got := bookmarksOf("t1"); len(got) != 1 || got[0] != "1002" {
		t.Fatalf("t1 bookmarks after reload = %v, want [1002]", got)
	}

	// Remove bookmark and verify persistence + idempotency.
	r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/bookmark/remove/1002", "{}\x00")
	if r.Code != http.StatusOK || r.Body.String() != `{"result":1}` {
		t.Fatalf("remove bookmark = %d %s", r.Code, r.Body.String())
	}
	if err := store.ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	if got := bookmarksOf("t1"); len(got) != 0 {
		t.Fatalf("t1 bookmarks after remove+reload = %v, want []", got)
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/bookmark/remove/1002", "{}\x00"); r.Code != http.StatusOK || r.Body.String() != `{"result":1}` {
		t.Fatalf("idempotent remove bookmark = %d %s", r.Code, r.Body.String())
	}

	// Mutual bookmark -> unfriend clears both sides' bookmarks cleanly (even after re-friending).
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/bookmark/1002", "{}\x00"); r.Code != http.StatusOK {
		t.Fatalf("re-bookmark t1->1002 = %d", r.Code)
	}
	if r := socialTestRequestAs(t, "t2", http.MethodPost, "/v4/friend/bookmark/1001", "{}\x00"); r.Code != http.StatusOK {
		t.Fatalf("bookmark t2->1001 = %d", r.Code)
	}
	if rem := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/remove/", "[1002]\x00"); rem.Code != http.StatusOK {
		t.Fatalf("unfriend = %d %s", rem.Code, rem.Body.String())
	}
	if err := store.ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	if got1, got2 := bookmarksOf("t1"), bookmarksOf("t2"); len(got1) != 0 || len(got2) != 0 {
		t.Fatalf("bookmarks after unfriend = t1:%v t2:%v, want empty", got1, got2)
	}
	// Re-friending stays unbookmarked.
	socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`)
	socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/accept/1001", "null\x00")
	if got1, got2 := bookmarksOf("t1"), bookmarksOf("t2"); len(got1) != 0 || len(got2) != 0 {
		t.Fatalf("bookmarks after re-friend = t1:%v t2:%v, want empty", got1, got2)
	}
}

func TestProfileOnlineIndicator(t *testing.T) {
	useConfig(t, devFakeConfig())
	testutil.UseDatabase(t)
	installSocialTestAccounts(t, map[string]*store.Account{
		"t1": {AccessToken: "t1", SessionKey: "s1", Aid: "1001", Name: "One"},
		"t2": {AccessToken: "t2", SessionKey: "s2", Aid: "1002", Name: "Two"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	profileOnline := func(token, aid string) (online, busy bool) {
		t.Helper()
		rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/profile/"+aid+"?deviceType=Android", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("profile %s = %d %s", aid, rec.Code, rec.Body.String())
		}
		var out struct {
			Result struct {
				Online bool `json:"online"`
				Busy   bool `json:"busy"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal profile: %v (%s)", err, rec.Body.String())
		}
		return out.Result.Online, out.Result.Busy
	}

	if online, busy := profileOnline("t1", "1002"); online || busy {
		t.Fatalf("offline profile = online:%v busy:%v, want false/false", online, busy)
	}

	conn := startTestSessionConn(t, "s2", 1002)
	if online, busy := profileOnline("t1", "1002"); !online || busy {
		t.Fatalf("online profile = online:%v busy:%v, want true/false", online, busy)
	}

	// Blocking hides online status on both profile and session/status.
	if rec := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/chat/block/1002", ""); rec.Code != http.StatusOK {
		t.Fatalf("block 1002 = %d %s", rec.Code, rec.Body.String())
	}
	if online, _ := profileOnline("t1", "1002"); online {
		t.Fatalf("blocked profile online = true, want false")
	}
	if online, _ := profileOnline("t2", "1001"); online {
		t.Fatalf("reverse-blocked profile online = true, want false")
	}
	ss := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/session/status", `["1002"]`)
	if ss.Code != http.StatusOK || !strings.Contains(ss.Body.String(), `"online":false`) {
		t.Fatalf("session/status while blocked = %d %s", ss.Code, ss.Body.String())
	}

	// Unblocking restores online status while session is still live.
	if rec := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/chat/block/remove/1002", "null\x00"); rec.Code != http.StatusOK {
		t.Fatalf("unblock 1002 = %d %s", rec.Code, rec.Body.String())
	}
	if online, _ := profileOnline("t1", "1002"); !online {
		t.Fatalf("unblocked online profile = false, want true")
	}

	_ = conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && lpn.SessionOnline(1002) {
		time.Sleep(10 * time.Millisecond)
	}
	if online, _ := profileOnline("t1", "1002"); online {
		t.Fatalf("disconnected profile online = true, want false")
	}
}

func TestBlockUnblockAndEnforcement(t *testing.T) {
	useConfig(t, devFakeConfig())
	testutil.UseDatabase(t)
	installSocialTestAccounts(t, map[string]*store.Account{
		"t1": {AccessToken: "t1", Aid: "1001", Name: "One"},
		"t2": {AccessToken: "t2", Aid: "1002", Name: "Two"},
		"t3": {AccessToken: "t3", Aid: "1003", Name: "Three"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	syncInfo := func(token string) (friendsCount int, statuses map[string]int, bookmarks []string) {
		t.Helper()
		rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/sync/friends/0", "")
		var out struct {
			Result struct {
				FriendsCount int `json:"friendsCount"`
				BuddyList    []struct {
					AvatarNo     string `json:"avatarNo"`
					Status       int    `json:"status"`
					FriendStatus int    `json:"friendStatus"`
				} `json:"buddyList"`
				Bookmarks []string `json:"bookmarks"`
			} `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("sync = %d %s", rec.Code, rec.Body.String())
		}
		statuses = make(map[string]int, len(out.Result.BuddyList))
		for _, b := range out.Result.BuddyList {
			statuses[b.AvatarNo] = b.Status
		}
		return out.Result.FriendsCount, statuses, out.Result.Bookmarks
	}

	profileStatus := func(token, aid string) (friendStatus int, applyYn bool) {
		t.Helper()
		rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/profile/"+aid, "")
		var out struct {
			Result struct {
				FriendStatus  int  `json:"friendStatus"`
				FriendApplyYn bool `json:"friendApplyYn"`
			} `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("profile = %d %s", rec.Code, rec.Body.String())
		}
		return out.Result.FriendStatus, out.Result.FriendApplyYn
	}

	// Edge cases: self-block 400, unknown 404, wrong method 404.
	if r := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/chat/block/1001", ""); r.Code != http.StatusBadRequest {
		t.Fatalf("self block = %d, want 400", r.Code)
	}
	if r := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/chat/block/9999", ""); r.Code != http.StatusNotFound {
		t.Fatalf("unknown block = %d, want 404", r.Code)
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/chat/block/1002", ""); r.Code != http.StatusNotFound {
		t.Fatalf("POST chat/block = %d, want 404", r.Code)
	}
	if r := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/chat/block/remove/1002", ""); r.Code != http.StatusNotFound {
		t.Fatalf("GET chat/block/remove = %d, want 404", r.Code)
	}

	// Make 1001 and 1002 friends with mutual bookmarks, and create a diary post on 1002.
	socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`)
	socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/accept/1001", "null\x00")
	socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/bookmark/1002", "{}\x00")
	socialTestRequestAs(t, "t2", http.MethodPost, "/v4/friend/bookmark/1001", "{}\x00")
	store.SocialMu.Lock()
	store.DiaryPosts = append(store.DiaryPosts, store.DiaryPost{
		DiaryNo: "1", AvatarID: "1002", OpenState: "public", Content: "hi",
	})
	_ = store.SaveSocialLocked(store.SocialChange{Posts: []string{"1"}})
	store.SocialMu.Unlock()

	// 1001 blocks 1002.
	blk := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/chat/block/1002", "")
	if blk.Code != http.StatusOK {
		t.Fatalf("block 1002 = %d %s", blk.Code, blk.Body.String())
	}
	var blkOut struct {
		Result struct {
			Blocked bool `json:"blocked"`
			Avatar  struct {
				AvatarID string `json:"avatarId"`
				Name     string `json:"name"`
			} `json:"avatar"`
		} `json:"result"`
	}
	if err := json.Unmarshal(blk.Body.Bytes(), &blkOut); err != nil || !blkOut.Result.Blocked || blkOut.Result.Avatar.AvatarID != "1002" || blkOut.Result.Avatar.Name != "Two" {
		t.Fatalf("block reply = %s", blk.Body.String())
	}

	// Reload from DB to verify persistence.
	if err := store.ReloadStores(); err != nil {
		t.Fatal(err)
	}

	fc1, st1, bm1 := syncInfo("t1")
	if fc1 != 0 || st1["1002"] != 4 || len(bm1) != 0 {
		t.Fatalf("t1 sync after block = count:%d status:%d bm:%v, want 0/4/[]", fc1, st1["1002"], bm1)
	}
	fc2, st2, bm2 := syncInfo("t2")
	if fc2 != 0 || st2["1001"] != -1 || len(bm2) != 0 {
		t.Fatalf("t2 sync after block = count:%d status:%d bm:%v, want 0/-1/[]", fc2, st2["1001"], bm2)
	}

	// Profile status: 1001 sees friendStatus=4, 1002 sees friendStatus=2 and friendApplyYn=false.
	if fs, apply := profileStatus("t1", "1002"); fs != 4 || apply {
		t.Fatalf("t1 profile 1002 = fs:%d apply:%v, want 4/false", fs, apply)
	}
	if fs, apply := profileStatus("t2", "1001"); fs != 2 || apply {
		t.Fatalf("t2 profile 1001 = fs:%d apply:%v, want 2/false", fs, apply)
	}

	// Enforcement: friend apply, diary comment, guestbook, hearts all rejected.
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`); r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), `"19500"`) {
		t.Fatalf("apply blocked = %d %s", r.Code, r.Body.String())
	}
	if r := socialTestRequestAs(t, "t2", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1001"}`); r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), `"19500"`) {
		t.Fatalf("apply by blocked = %d %s", r.Code, r.Body.String())
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/diary2/comment/write/1002", `{"diaryNo":1,"avatarID":"1001","content":"yo"}`); r.Code != http.StatusOK || r.Body.String() != `{"result":{"blocked":true}}` {
		t.Fatalf("diary comment blocked = %d %s", r.Code, r.Body.String())
	}
	if r := socialTestRequestAs(t, "t2", http.MethodPost, "/v4/guestbook/write", `{"hostAvtNo":"1001","guestAvtNo":"1002","content":"hi","secret":false}`); r.Code != http.StatusBadRequest {
		t.Fatalf("guestbook write blocked = %d %s", r.Code, r.Body.String())
	}
	if economy.HeartReceivable("1001", "1002") || economy.HeartReceivable("1002", "1001") {
		t.Fatalf("HeartReceivable should be false while blocked")
	}

	// Mutual block: 1002 also blocks 1001; 1001 unblocking leaves 1002's block intact.
	if r := socialTestRequestAs(t, "t2", http.MethodGet, "/v4/chat/block/1001", ""); r.Code != http.StatusOK {
		t.Fatalf("mutual block = %d %s", r.Code, r.Body.String())
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/chat/block/remove/1002", "null\x00"); r.Code != http.StatusOK || r.Body.String() != `{"result":1}` {
		t.Fatalf("unblock 1002 = %d %s", r.Code, r.Body.String())
	}
	_, st1, _ = syncInfo("t1")
	_, st2, _ = syncInfo("t2")
	if st1["1002"] != -1 || st2["1001"] != 4 {
		t.Fatalf("after one-sided unblock: st1[1002]=%d (want -1), st2[1001]=%d (want 4)", st1["1002"], st2["1001"])
	}
	// Still blocked by 1002 so apply still fails.
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`); r.Code != http.StatusBadRequest {
		t.Fatalf("apply while still blocked by peer = %d", r.Code)
	}
	// 1002 unblocks 1001 -> now both have -1 tombstone and can re-friend.
	if r := socialTestRequestAs(t, "t2", http.MethodPost, "/v4/chat/block/remove/1001", "null\x00"); r.Code != http.StatusOK || r.Body.String() != `{"result":1}` {
		t.Fatalf("unblock 1001 = %d %s", r.Code, r.Body.String())
	}
	_, st2, _ = syncInfo("t2")
	if st2["1001"] != -1 {
		t.Fatalf("t2 sync after unblock = %d, want -1 tombstone", st2["1001"])
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`); r.Code != http.StatusOK {
		t.Fatalf("re-apply after mutual unblock = %d %s", r.Code, r.Body.String())
	}

	// Block a stranger (no prior friendship) -> unblock still emits -1 tombstone to clear artsdata.db.
	if r := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/chat/block/1003", ""); r.Code != http.StatusOK {
		t.Fatalf("block stranger 1003 = %d %s", r.Code, r.Body.String())
	}
	_, st1, _ = syncInfo("t1")
	if st1["1003"] != 4 {
		t.Fatalf("stranger sync status = %d, want 4", st1["1003"])
	}
	if r := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/chat/block/remove/1003", "null\x00"); r.Code != http.StatusOK {
		t.Fatalf("unblock stranger 1003 = %d %s", r.Code, r.Body.String())
	}
	_, st1, _ = syncInfo("t1")
	if st1["1003"] != -1 {
		t.Fatalf("stranger sync status after unblock = %d, want -1", st1["1003"])
	}
}
