package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

const (
	dsGet  = http.MethodGet
	dsPost = http.MethodPost
)

type testComment struct {
	CommentNo           string `json:"commentNo"`
	CommentAvatarID     string `json:"commentAvatarId"`
	CommentAvatarLocale string `json:"commentAvatarLocale"`
	Nickname            string `json:"nickname"`
	Content             string `json:"content"`
	ObsProfileImagePath string `json:"obsProfileImagePath"`
	Date                string `json:"date"`
}

type testCommentPage struct {
	Result struct {
		LastData bool          `json:"lastData"`
		Items    []testComment `json:"items"`
	} `json:"result"`
}

type testCounts struct {
	DiaryNo      string `json:"diaryNo"`
	CommentCount int    `json:"commentCount"`
	LikeCount    int    `json:"likeCount"`
	MyLikeType   int    `json:"myLikeType"`
}

// diarySocialAccounts installs the diary owner "1" (social-test-token), a friend "3" (friend-token) and a stranger
// "4" (third-token), with empty in-memory social state.
func diarySocialAccounts(t *testing.T) {
	t.Helper()
	installSocialTestAccounts(t, map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1", Name: "Cherry", Country: "JP"},
		"friend-token":      {AccessToken: "friend-token", Aid: "3", Name: "Friend", Country: "JP"},
		"third-token":       {AccessToken: "third-token", Aid: "4", Name: "Third", Country: "JP"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
}

// diaryCall sends a request as token's account; an empty token sends it anonymously.
func diaryCall(t *testing.T, token, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	if token == "" {
		return serveRequest(t, httptest.NewRequest(method, target, strings.NewReader(body)))
	}
	return socialTestRequestAs(t, token, method, target, body)
}

// diaryPost writes a post as aid (through token) and returns its diaryNo.
func diaryPost(t *testing.T, token, aid, openState string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"avatarId": aid, "title": "t", "content": "post " + openState, "openState": openState})
	if err != nil {
		t.Fatal(err)
	}
	rec := diaryCall(t, token, dsPost, "/v4/diary2/ext/write", string(body))
	var out struct {
		Result store.DiaryPost `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Result.DiaryNo == "" {
		t.Fatalf("write post = %d %s", rec.Code, rec.Body.String())
	}
	return out.Result.DiaryNo
}

func commentIDs(page testCommentPage) []string {
	ids := []string{}
	for _, c := range page.Result.Items {
		ids = append(ids, c.CommentNo)
	}
	return ids
}

func listComments(t *testing.T, token, owner, no, query string) testCommentPage {
	t.Helper()
	rec := diaryCall(t, token, dsGet, "/v4/diary2/comment/search/"+owner+"/"+no+query, "")
	var page testCommentPage
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
		t.Fatalf("comment search%s = %d %s", query, rec.Code, rec.Body.String())
	}
	return page
}

// unfoldCounts reads the post's row from the owner's diary list as the viewer token.
func unfoldCounts(t *testing.T, token, no string) testCounts {
	t.Helper()
	rec := diaryCall(t, token, dsGet, "/v4/diary2/ext/unfold/1/?lastSeq=-1&size=10", "")
	var page struct {
		Result struct {
			Items []testCounts `json:"items"`
		} `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
		t.Fatalf("unfold = %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range page.Result.Items {
		if c.DiaryNo == no {
			return c
		}
	}
	t.Fatalf("post %s missing from unfold: %s", no, rec.Body.String())
	return testCounts{}
}

// lookCounts reads the post's detail as the viewer token.
func lookCounts(t *testing.T, token, no string) testCounts {
	t.Helper()
	rec := diaryCall(t, token, dsGet, "/v4/diary2/ext/look/1/"+no, "")
	var out struct {
		Result testCounts `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("look = %d %s", rec.Code, rec.Body.String())
	}
	return out.Result
}

func TestDiaryCommentsWriteListErase(t *testing.T) {
	diarySocialAccounts(t)
	no := diaryPost(t, "social-test-token", "1", "public")
	write := func(token, aid, text string) string {
		t.Helper()
		body := fmt.Sprintf(`{"commentAvatarId":"1","diaryNo":%s,"avatarID":%q,"content":%q,"tagAvatarIds":["9"]}`, no, aid, text)
		rec := diaryCall(t, token, dsPost, "/v4/diary2/comment/write/1", body)
		var out struct {
			Result testComment `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Result.CommentAvatarID != aid || out.Result.Content != text || out.Result.CommentNo == "" || out.Result.Date == "" {
			t.Fatalf("comment write %q = %d %s", text, rec.Code, rec.Body.String())
		}
		return out.Result.CommentNo
	}
	first := write("social-test-token", "1", "first")
	second := write("friend-token", "3", "second")
	third := write("social-test-token", "1", "third")

	// Newest first, two per page; the next page starts below the last commentNo.
	page := listComments(t, "", "1", no, "?lastSeq=-1&size=2")
	if got := commentIDs(page); !slices.Equal(got, []string{third, second}) || page.Result.LastData {
		t.Fatalf("first page = %v last=%t", got, page.Result.LastData)
	}
	page = listComments(t, "", "1", no, "?lastSeq="+second+"&size=2")
	if got := commentIDs(page); !slices.Equal(got, []string{first}) || !page.Result.LastData {
		t.Fatalf("second page = %v last=%t", got, page.Result.LastData)
	}
	// The default size (20) and the friend's view return the same order.
	if got := commentIDs(listComments(t, "friend-token", "1", no, "?lastSeq=-1")); !slices.Equal(got, []string{third, second, first}) {
		t.Fatalf("friend list = %v", got)
	}

	// The owner and the writer can erase; a stranger cannot.
	erase := func(token, commentNo string) *httptest.ResponseRecorder {
		return diaryCall(t, token, dsPost, "/v4/diary2/comment/erase/1/"+no+"/"+commentNo, "null")
	}
	if rec := erase("third-token", second); rec.Code != http.StatusBadRequest {
		t.Fatalf("stranger erase = %d %s", rec.Code, rec.Body.String())
	}
	if rec := erase("social-test-token", second); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("owner erases friend comment = %d %s", rec.Code, rec.Body.String())
	}
	fourth := write("friend-token", "3", "fourth")
	if rec := erase("friend-token", fourth); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("writer erase = %d %s", rec.Code, rec.Body.String())
	}
	if got := commentIDs(listComments(t, "", "1", no, "?lastSeq=-1")); !slices.Equal(got, []string{third, first}) {
		t.Fatalf("after erases = %v", got)
	}
	// An unknown or already erased comment answers true (idempotent), and a non-null body is refused.
	if rec := erase("social-test-token", second); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("repeat erase = %d %s", rec.Code, rec.Body.String())
	}
	if rec := diaryCall(t, "social-test-token", dsPost, "/v4/diary2/comment/erase/1/"+no+"/"+third, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("erase with body = %d %s", rec.Code, rec.Body.String())
	}
	if d := lookCounts(t, "", no); d.CommentCount != 2 {
		t.Fatalf("detail commentCount = %d, want 2", d.CommentCount)
	}

	// A post with no comments answers an empty array, never null.
	empty := diaryPost(t, "social-test-token", "1", "public")
	if rec := diaryCall(t, "", dsGet, "/v4/diary2/comment/search/1/"+empty+"?lastSeq=-1", ""); rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"items":[],"lastData":true}}` {
		t.Fatalf("empty comment list = %d %s", rec.Code, rec.Body.String())
	}

	// A friend may not comment on a private post (404), and a viewer who cannot see it gets an empty page.
	private := diaryPost(t, "social-test-token", "1", "private")
	if rec := diaryCall(t, "friend-token", dsPost, "/v4/diary2/comment/write/1", fmt.Sprintf(`{"diaryNo":%s,"avatarID":"3","content":"hi"}`, private)); rec.Code != http.StatusNotFound {
		t.Fatalf("comment on private post = %d %s", rec.Code, rec.Body.String())
	}
	if rec := diaryCall(t, "friend-token", dsGet, "/v4/diary2/comment/search/1/"+private+"?lastSeq=-1", ""); rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"items":[],"lastData":true}}` {
		t.Fatalf("private comment list = %d %s", rec.Code, rec.Body.String())
	}
	// Refused writes: blank content, a forged avatarID, a string diaryNo.
	for _, body := range []string{
		fmt.Sprintf(`{"diaryNo":%s,"avatarID":"3","content":"   "}`, no),
		fmt.Sprintf(`{"diaryNo":%s,"avatarID":"1","content":"forged"}`, no),
		fmt.Sprintf(`{"diaryNo":"%s","avatarID":"3","content":"string id"}`, no),
	} {
		if rec := diaryCall(t, "friend-token", dsPost, "/v4/diary2/comment/write/1", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("refused write %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestDiaryLikesCountsAndPerViewerType(t *testing.T) {
	diarySocialAccounts(t)
	no := diaryPost(t, "social-test-token", "1", "public")
	like := func(token, aid string, kind int) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"avatarId":"1","diaryNo":%s,"likeAvatarId":%q,"likeType":%d}`, no, aid, kind)
		return diaryCall(t, token, dsPost, "/v4/diary2/like/save/1", body)
	}
	expect := func(label, token string, wantLikes, wantMine int) {
		t.Helper()
		for _, got := range []testCounts{unfoldCounts(t, token, no), lookCounts(t, token, no)} {
			if got.LikeCount != wantLikes || got.MyLikeType != wantMine || got.CommentCount != 0 {
				t.Fatalf("%s counts = %+v, want likes %d mine %d", label, got, wantLikes, wantMine)
			}
		}
	}
	if rec := like("friend-token", "3", 2); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("like = %d %s", rec.Code, rec.Body.String())
	}
	if rec := like("third-token", "4", 6); rec.Code != http.StatusOK {
		t.Fatalf("second like = %d %s", rec.Code, rec.Body.String())
	}
	expect("owner", "social-test-token", 2, 0)
	expect("friend", "friend-token", 2, 2)
	expect("third", "third-token", 2, 6)
	expect("anonymous", "", 2, 0)

	// Changing the type keeps one like; likeType 0 removes it.
	if rec := like("friend-token", "3", 5); rec.Code != http.StatusOK {
		t.Fatalf("change = %d %s", rec.Code, rec.Body.String())
	}
	expect("friend changed", "friend-token", 2, 5)
	if rec := like("friend-token", "3", 0); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	}
	expect("friend removed", "friend-token", 1, 0)
	expect("third after removal", "third-token", 1, 6)

	// The like list reports totalCount as a JSON string and paginates by likeNo, newest first.
	if rec := like("friend-token", "3", 1); rec.Code != http.StatusOK {
		t.Fatalf("re-like = %d %s", rec.Code, rec.Body.String())
	}
	rec := diaryCall(t, "", dsGet, "/v4/diary2/like/search/1/"+no+"?lastSeq=-1&size=1", "")
	var page struct {
		Result struct {
			TotalCount string `json:"totalCount"`
			LastData   bool   `json:"lastData"`
			Items      []struct {
				LikeNo       string `json:"likeNo"`
				LikeAvatarID string `json:"likeAvatarId"`
				LikeType     int    `json:"likeType"`
			} `json:"items"`
		} `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil || !strings.Contains(rec.Body.String(), `"totalCount":"2"`) {
		t.Fatalf("like list = %d %s", rec.Code, rec.Body.String())
	}
	if page.Result.LastData || len(page.Result.Items) != 1 || page.Result.Items[0].LikeAvatarID != "3" || page.Result.Items[0].LikeType != 1 {
		t.Fatalf("like list first page = %+v", page.Result)
	}
	rec = diaryCall(t, "", dsGet, "/v4/diary2/like/search/1/"+no+"?lastSeq="+page.Result.Items[0].LikeNo+"&size=1", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"likeAvatarId":"4"`) || !strings.Contains(rec.Body.String(), `"lastData":true`) {
		t.Fatalf("like list second page = %d %s", rec.Code, rec.Body.String())
	}

	// Refused: a type outside 0..6, another player's aid, an anonymous caller, and a post the liker cannot see.
	if rec := like("friend-token", "3", 7); rec.Code != http.StatusBadRequest {
		t.Fatalf("likeType 7 = %d", rec.Code)
	}
	if rec := like("friend-token", "4", 1); rec.Code != http.StatusBadRequest {
		t.Fatalf("forged liker = %d", rec.Code)
	}
	if rec := like("", "3", 1); rec.Code != http.StatusNotFound {
		t.Fatalf("anonymous like = %d", rec.Code)
	}
	private := diaryPost(t, "social-test-token", "1", "private")
	if rec := diaryCall(t, "friend-token", dsPost, "/v4/diary2/like/save/1", fmt.Sprintf(`{"diaryNo":%s,"likeAvatarId":"3","likeType":1}`, private)); rec.Code != http.StatusNotFound {
		t.Fatalf("like on private post = %d", rec.Code)
	}
}

func TestGuestbookRepliesWriteListErase(t *testing.T) {
	diarySocialAccounts(t)
	rec := diaryCall(t, "friend-token", dsPost, "/v4/guestbook/write", `{"hostAvtNo":"1","guestAvtNo":"3","content":"hello","secret":false}`)
	var entry struct {
		Result struct {
			GuestHistNo string `json:"guestHistNo"`
		} `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &entry) != nil || entry.Result.GuestHistNo == "" {
		t.Fatalf("guestbook write = %d %s", rec.Code, rec.Body.String())
	}
	parent := entry.Result.GuestHistNo
	reply := func(token, replyAvt, text string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"hostAvtNo":"1","guestAvtNo":"3","replyAvtNo":%q,"parentHistNo":%q,"content":%q}`, replyAvt, parent, text)
		return diaryCall(t, token, dsPost, "/v4/guestbookreply/write", body)
	}

	// A stranger cannot reply (404) and a forged replyAvtNo is refused (400).
	if rec := reply("third-token", "4", "stranger"); rec.Code != http.StatusNotFound {
		t.Fatalf("stranger reply = %d %s", rec.Code, rec.Body.String())
	}
	if rec := reply("friend-token", "1", "forged"); rec.Code != http.StatusBadRequest {
		t.Fatalf("forged reply = %d %s", rec.Code, rec.Body.String())
	}
	// The host answers; the guest writer adds a follow-up.
	hostRec := reply("social-test-token", "1", "thanks")
	var hostOut struct {
		Result store.GuestbookReply `json:"result"`
	}
	if hostRec.Code != http.StatusOK || json.Unmarshal(hostRec.Body.Bytes(), &hostOut) != nil || hostOut.Result.ReplyHistNo == "" || hostOut.Result.Blind ||
		hostOut.Result.ReplyAvtNo != "1" || hostOut.Result.ParentHistNo != parent || hostOut.Result.RegYmdt == "" {
		t.Fatalf("host reply = %d %s", hostRec.Code, hostRec.Body.String())
	}
	hostNo := hostOut.Result.ReplyHistNo
	followRec := reply("friend-token", "3", "follow-up")
	var followOut struct {
		Result store.GuestbookReply `json:"result"`
	}
	if followRec.Code != http.StatusOK || json.Unmarshal(followRec.Body.Bytes(), &followOut) != nil {
		t.Fatalf("guest reply = %d %s", followRec.Code, followRec.Body.String())
	}
	followNo := followOut.Result.ReplyHistNo

	// The replies show on the entry in the list, oldest first.
	list := diaryCall(t, "", dsGet, "/v4/guestbook3/list/1/?lastSeq=-1&size=10", "")
	var listed struct {
		Result struct {
			Items []struct {
				GuestHistNo string                 `json:"guestHistNo"`
				Reply       []store.GuestbookReply `json:"reply"`
			} `json:"items"`
		} `json:"result"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed.Result.Items) != 1 {
		t.Fatalf("guestbook list = %d %s", list.Code, list.Body.String())
	}
	if got := listed.Result.Items[0].Reply; len(got) != 2 || got[0].ReplyHistNo != hostNo || got[1].ReplyHistNo != followNo || got[0].ReplyNickname != "Cherry" {
		t.Fatalf("entry replies = %+v", got)
	}

	// Erase: the guest writer may not erase the host's reply; the host may erase the follow-up.
	erase := func(token, replyNo string) *httptest.ResponseRecorder {
		return diaryCall(t, token, dsGet, "/v4/guestbookreply/erase/1/"+replyNo, "")
	}
	isErased := func(path string) string {
		return diaryCall(t, "", dsGet, path, "").Body.String()
	}
	if rec := erase("friend-token", hostNo); rec.Code != http.StatusBadRequest {
		t.Fatalf("guest erases host reply = %d %s", rec.Code, rec.Body.String())
	}
	if rec := erase("social-test-token", followNo); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("host erases follow-up = %d %s", rec.Code, rec.Body.String())
	}
	if got := isErased("/v4/guestbookreply/isErased/1/" + followNo); got != `{"result":true}` {
		t.Fatalf("follow-up isErased = %s", got)
	}
	if got := isErased("/v4/guestbookreply/isErased/1/" + hostNo); got != `{"result":false}` {
		t.Fatalf("host reply isErased = %s", got)
	}
	if rec := erase("social-test-token", "999"); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("unknown reply erase = %d %s", rec.Code, rec.Body.String())
	}
	if rec := erase("social-test-token", hostNo); rec.Code != http.StatusOK {
		t.Fatalf("author erase = %d %s", rec.Code, rec.Body.String())
	}
	list = diaryCall(t, "", dsGet, "/v4/guestbook3/list/1/?lastSeq=-1&size=10", "")
	if !strings.Contains(list.Body.String(), `"reply":[]`) {
		t.Fatalf("erased replies still listed: %s", list.Body.String())
	}

	// Entry isErased is false while the entry exists and true after the guest erases it.
	if got := isErased("/v4/guestbook/isErased/1/" + parent); got != `{"result":false}` {
		t.Fatalf("entry isErased before = %s", got)
	}
	if rec := diaryCall(t, "friend-token", dsPost, "/v4/guestbook/erase/1/"+parent, "null"); rec.Code != http.StatusOK {
		t.Fatalf("entry erase = %d %s", rec.Code, rec.Body.String())
	}
	if got := isErased("/v4/guestbook/isErased/1/" + parent); got != `{"result":true}` {
		t.Fatalf("entry isErased after = %s", got)
	}
}

func TestSocialStoreLoadsOldFormatAndKeepsNewRows(t *testing.T) {
	diarySocialAccounts(t)
	store.SocialMu.Lock()
	store.NextDiaryNo = 1
	store.DiaryPosts = []store.DiaryPost{{DiaryNo: "1", AvatarID: "1", Nickname: "Cherry", OpenState: "public", Images: []store.DiaryImage{}}}
	store.NextGuestHistNo = 1
	store.GuestbookEntries = []store.SavedGuestbookEntry{{HostAvtNo: "1", GuestHistNo: "1", GuestAvtNo: "3", Content: "old", Reply: []store.GuestbookReply{}}}
	store.DiaryComments, store.DiaryLikes = []store.DiaryComment{}, []store.DiaryLike{}
	store.GuestbookRead = map[string]uint64{}
	store.SocialMu.Unlock()
	testutil.UseDatabase(t)
	store.SocialMu.Lock()
	loaded := len(store.DiaryComments) == 0 && len(store.DiaryLikes) == 0 && store.GuestbookEntries[0].Reply != nil && len(store.GuestbookEntries[0].Reply) == 0
	store.SocialMu.Unlock()
	if !loaded {
		t.Fatal("old-format file did not load with empty comment, like and reply lists")
	}

	if rec := diaryCall(t, "friend-token", dsPost, "/v4/diary2/comment/write/1", `{"diaryNo":1,"avatarID":"3","content":"hi"}`); rec.Code != http.StatusOK {
		t.Fatalf("comment = %d %s", rec.Code, rec.Body.String())
	}
	if rec := diaryCall(t, "friend-token", dsPost, "/v4/diary2/like/save/1", `{"diaryNo":1,"likeAvatarId":"3","likeType":4}`); rec.Code != http.StatusOK {
		t.Fatalf("like = %d %s", rec.Code, rec.Body.String())
	}
	if rec := diaryCall(t, "social-test-token", dsPost, "/v4/guestbookreply/write", `{"hostAvtNo":"1","replyAvtNo":"1","parentHistNo":"1","content":"ok"}`); rec.Code != http.StatusOK {
		t.Fatalf("reply = %d %s", rec.Code, rec.Body.String())
	}

	// A reload keeps the rows and the counters.
	if err := store.ReloadSocial(); err != nil {
		t.Fatal(err)
	}
	if got := lookCounts(t, "friend-token", "1"); got.CommentCount != 1 || got.LikeCount != 1 || got.MyLikeType != 4 {
		t.Fatalf("reloaded counts = %+v", got)
	}
	store.SocialMu.Lock()
	seqs := []uint64{store.NextCommentNo, store.NextLikeNo, store.NextReplyHistNo}
	store.SocialMu.Unlock()
	if !slices.Equal(seqs, []uint64{1, 1, 1}) {
		t.Fatalf("reloaded counters = %v", seqs)
	}
}

func TestDiaryPostEraseDropsItsCommentsAndLikes(t *testing.T) {
	diarySocialAccounts(t)
	gone := diaryPost(t, "social-test-token", "1", "public")
	kept := diaryPost(t, "social-test-token", "1", "public")
	for _, no := range []string{gone, kept} {
		if rec := diaryCall(t, "friend-token", dsPost, "/v4/diary2/comment/write/1", fmt.Sprintf(`{"diaryNo":%s,"avatarID":"3","content":"c"}`, no)); rec.Code != http.StatusOK {
			t.Fatalf("comment = %d %s", rec.Code, rec.Body.String())
		}
		if rec := diaryCall(t, "third-token", dsPost, "/v4/diary2/like/save/1", fmt.Sprintf(`{"diaryNo":%s,"likeAvatarId":"4","likeType":3}`, no)); rec.Code != http.StatusOK {
			t.Fatalf("like = %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := diaryCall(t, "social-test-token", dsPost, "/v4/diary2/erase/"+gone, "null"); rec.Code != http.StatusOK {
		t.Fatalf("post erase = %d %s", rec.Code, rec.Body.String())
	}
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	if len(store.DiaryComments) != 1 || store.DiaryComments[0].DiaryNo != kept || len(store.DiaryLikes) != 1 || store.DiaryLikes[0].DiaryNo != kept {
		t.Fatalf("rows after post erase: comments %+v likes %+v", store.DiaryComments, store.DiaryLikes)
	}
}

func TestDiaryCommentAndLikeWritesRollBackOnSaveFailure(t *testing.T) {
	diarySocialAccounts(t)
	no := diaryPost(t, "social-test-token", "1", "public")
	testutil.FailSaves(t)
	if rec := diaryCall(t, "friend-token", dsPost, "/v4/diary2/comment/write/1", fmt.Sprintf(`{"diaryNo":%s,"avatarID":"3","content":"c"}`, no)); rec.Code != http.StatusInternalServerError {
		t.Fatalf("comment with failing store = %d %s", rec.Code, rec.Body.String())
	}
	if rec := diaryCall(t, "friend-token", dsPost, "/v4/diary2/like/save/1", fmt.Sprintf(`{"diaryNo":%s,"likeAvatarId":"3","likeType":2}`, no)); rec.Code != http.StatusInternalServerError {
		t.Fatalf("like with failing store = %d %s", rec.Code, rec.Body.String())
	}
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	if store.NextCommentNo != 0 || len(store.DiaryComments) != 0 || store.NextLikeNo != 0 || len(store.DiaryLikes) != 0 {
		t.Fatalf("failed writes left comments=%d (seq %d) likes=%d (seq %d)", len(store.DiaryComments), store.NextCommentNo, len(store.DiaryLikes), store.NextLikeNo)
	}
}

func TestDiaryReportsAnswerTrueAndLogOnlyIDs(t *testing.T) {
	diarySocialAccounts(t)
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	body := `{"targetAvatarId":"3","reportType":"R1","reportMsg":"secret player words","refId":"77","refDtl":"detail"}`
	routes := map[string]string{
		"diary": "/v4/report/diary", "diary/comment": "/v4/report/diary/comment",
		"guestbook": "/v4/report/guestbook", "guestbookreply": "/v4/report/guestbookreply",
	}
	for kind, path := range routes {
		if rec := diaryCall(t, "friend-token", dsPost, path, body); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
			t.Fatalf("%s report = %d %s", kind, rec.Code, rec.Body.String())
		}
		if !strings.Contains(buf.String(), "cherry: diary report kind="+kind+" target=3 ref=77\n") {
			t.Fatalf("no log line for %s: %q", kind, buf.String())
		}
	}
	if strings.Contains(buf.String(), "secret player words") || strings.Contains(buf.String(), "detail") {
		t.Fatalf("report text reached the log: %q", buf.String())
	}
	if rec := diaryCall(t, "", dsPost, "/v4/report/diary", body); rec.Code != http.StatusNotFound {
		t.Fatalf("anonymous report = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDiarySocialArraysAreNeverNull(t *testing.T) {
	diarySocialAccounts(t)
	no := diaryPost(t, "social-test-token", "1", "public")
	look := diaryCall(t, "", dsGet, "/v4/diary2/ext/look/1/"+no, "")
	for _, want := range []string{`"images":[]`, `"comments":{"items":[],"lastData":true}`, `"likes":{"items":[],"lastData":true}`, `"commentCount":0`, `"likeCount":0`, `"myLikeType":0`} {
		if !strings.Contains(look.Body.String(), want) {
			t.Fatalf("detail lacks %s: %s", want, look.Body.String())
		}
	}
	if rec := diaryCall(t, "", dsGet, "/v4/diary2/like/search/1/"+no+"?lastSeq=-1", ""); rec.Body.String() != `{"result":{"items":[],"lastData":true,"totalCount":"0"}}` {
		t.Fatalf("empty like list = %s", rec.Body.String())
	}
	if rec := diaryCall(t, "friend-token", dsPost, "/v4/guestbook/write", `{"hostAvtNo":"1","guestAvtNo":"3","content":"x","secret":false}`); rec.Code != http.StatusOK {
		t.Fatalf("guestbook write = %d %s", rec.Code, rec.Body.String())
	}
	if rec := diaryCall(t, "", dsGet, "/v4/guestbook3/list/1/?lastSeq=-1&size=10", ""); !strings.Contains(rec.Body.String(), `"reply":[]`) {
		t.Fatalf("guestbook row lacks reply array: %s", rec.Body.String())
	}
}

func TestDeleteAccountDropsCommentsLikesAndReplies(t *testing.T) {
	f := delSetup(t)
	// The rows are added one at a time, the way the handlers add them: each save names the one row it adds, at the end of
	// its list (the safety net checks that the database holds what memory holds after every save).
	addRows := func(add func(), change store.SocialChange) {
		t.Helper()
		store.SocialMu.Lock()
		defer store.SocialMu.Unlock()
		add()
		if err := store.SaveSocialLocked(change); err != nil {
			t.Fatal(err)
		}
	}
	addRows(func() {
		store.DiaryComments = append(store.DiaryComments, store.DiaryComment{CommentNo: "1", DiaryNo: "1", OwnerAvatarID: "9501", CommentAvatarID: "9502"}) // on the deleted account's post
	}, store.SocialChange{Comments: []string{"1"}, Scalars: true})
	addRows(func() {
		store.DiaryComments = append(store.DiaryComments, store.DiaryComment{CommentNo: "2", DiaryNo: "2", OwnerAvatarID: "9502", CommentAvatarID: "9501"}) // written by the deleted account
	}, store.SocialChange{Comments: []string{"2"}})
	addRows(func() {
		store.DiaryComments = append(store.DiaryComments, store.DiaryComment{CommentNo: "3", DiaryNo: "2", OwnerAvatarID: "9502", CommentAvatarID: "9502"}) // kept
	}, store.SocialChange{Comments: []string{"3"}})
	addRows(func() {
		store.DiaryLikes = append(store.DiaryLikes, store.DiaryLike{LikeNo: "1", DiaryNo: "2", OwnerAvatarID: "9502", LikeAvatarID: "9501"}) // dropped
	}, store.SocialChange{Likes: []string{"1"}, Scalars: true})
	addRows(func() {
		store.DiaryLikes = append(store.DiaryLikes, store.DiaryLike{LikeNo: "2", DiaryNo: "2", OwnerAvatarID: "9502", LikeAvatarID: "9502"}) // kept
	}, store.SocialChange{Likes: []string{"2"}})
	addRows(func() {
		store.GuestbookEntries[2].Reply = append(store.GuestbookEntries[2].Reply, store.GuestbookReply{ReplyHistNo: "1", ParentHistNo: "3", ReplyAvtNo: "9501"}) // dropped
	}, store.SocialChange{Replies: []string{"1"}, Scalars: true})
	addRows(func() {
		store.GuestbookEntries[2].Reply = append(store.GuestbookEntries[2].Reply, store.GuestbookReply{ReplyHistNo: "2", ParentHistNo: "3", ReplyAvtNo: "9502"}) // kept
	}, store.SocialChange{Replies: []string{"2"}})
	if rec := f.del(t, "dtok"); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	store.SocialMu.Lock()
	var commentNos, likeNos, replyNos []string
	for _, c := range store.DiaryComments {
		commentNos = append(commentNos, c.CommentNo)
	}
	for _, l := range store.DiaryLikes {
		likeNos = append(likeNos, l.LikeNo)
	}
	for _, e := range store.GuestbookEntries {
		for _, r := range e.Reply {
			replyNos = append(replyNos, r.ReplyHistNo)
		}
	}
	store.SocialMu.Unlock()
	if !slices.Equal(commentNos, []string{"3"}) || !slices.Equal(likeNos, []string{"2"}) || !slices.Equal(replyNos, []string{"2"}) {
		t.Fatalf("social rows left: comments %v likes %v replies %v", commentNos, likeNos, replyNos)
	}

	raw, err := os.ReadFile(filepath.Join(f.dir, "deleted-accounts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var b deletedBackup
	if err := json.Unmarshal(raw, &b); err != nil || len(b.Comments) != 2 || len(b.Likes) != 1 || len(b.Replies) != 1 {
		t.Fatalf("backup = %s (%v)", raw, err)
	}
}
