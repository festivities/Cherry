package web

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Shape and visibility checks for menu rows 72, 84-86 and 88 (menurows2.go). Each reply is typed as the client's
// parser reads it (rows-e-spec sections 3.1, 3.7-3.9).

// todaysRows decodes {"result":{"items":[...]}} into raw key maps; a non-array items value fails the kind check.
func todaysRows(t *testing.T, body []byte) []map[string]json.RawMessage {
	t.Helper()
	var env struct {
		Result struct {
			Items []map[string]json.RawMessage `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("reply %s: %v", body, err)
	}
	top := topLevelKinds(t, body)
	var result map[string]json.RawMessage
	if err := json.Unmarshal(top["result"], &result); err != nil {
		t.Fatalf("result %s: %v", top["result"], err)
	}
	if got := jsonKind(result["items"]); got != "array" {
		t.Fatalf("items is %s, want array", got)
	}
	return env.Result.Items
}

// todaysPostIDs requests target and returns the postId of each row, in reply order.
func todaysPostIDs(t *testing.T, target string) []string {
	t.Helper()
	rec := serve(t, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d body %s", target, rec.Code, rec.Body)
	}
	var ids []string
	for _, row := range todaysRows(t, rec.Body.Bytes()) {
		var id string
		if err := json.Unmarshal(row["postId"], &id); err != nil {
			t.Fatalf("postId %s: %v", row["postId"], err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestStoryListsEmptyBodies: the Story Book tab and both story gacha lists answer an empty items array (no content).
func TestStoryListsEmptyBodies(t *testing.T) {
	for _, path := range []string{"/v4/storybook/list", "/v4/shop/storygacha/list"} {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"items":[]}}` {
			t.Fatalf("%s: status %d body %s", path, rec.Code, rec.Body)
		}
		if rows := todaysRows(t, rec.Body.Bytes()); len(rows) != 0 {
			t.Fatalf("%s: %d rows, want 0", path, len(rows))
		}
	}
}

// TestLineEventInviteBodies: validate answers the four asBool keys, all false (not eligible); the follow-up
// friend/list reply has campaignURL as a string and friends as an empty array.
func TestLineEventInviteBodies(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/line/event/invite/validate")
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status %d", rec.Code)
	}
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("validate reply %s: %v", rec.Body, err)
	}
	checkKinds(t, env.Result, map[string]string{
		"validEvent": "bool", "validCountry": "bool", "validAccount": "bool", "validAgreement": "bool",
	})
	for key, raw := range env.Result {
		if string(raw) != "false" {
			t.Fatalf("%s = %s, want false", key, raw)
		}
	}

	rec = serve(t, http.MethodGet, "/v4/line/event/invite/friend/list")
	if rec.Code != http.StatusOK {
		t.Fatalf("friend/list status %d", rec.Code)
	}
	env.Result = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("friend/list reply %s: %v", rec.Body, err)
	}
	checkKinds(t, env.Result, map[string]string{"campaignURL": "string", "friends": "array"})
}

// TestRecommendPostListEmpty: with no diary posts the reply is an empty items array (success, not an error).
func TestRecommendPostListEmpty(t *testing.T) {
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	rec := serve(t, http.MethodGet, "/v4/rcmdpost/list?lastSeq=-1&size=12")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"items":[]}}` {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

// TestRecommendPostListRankingPagingAndShape: only public, non-Blind photo posts inside the window; ranked by
// distinct likers (1 point) and commenters (2 points) other than the owner, newest first on a tie; orderSeq is the
// rank and lastSeq pages by it; every row has nine string keys, linkType "goSomewhere(diaryDetail)" (the list
// tab drops any other row) and the author's relative profile picture + background.
func TestRecommendPostListRankingPagingAndShape(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"ra": {AccessToken: "ra", SessionKey: "sk", Aid: "9501", Name: "alice",
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9501_1", Bg: "profilebg/3.jpg"}},
		"rb": {AccessToken: "rb", SessionKey: "sk", Aid: "9502", Name: "bob"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	const photo = "/lineplay/d/download.nhn?oid=9501_1700000000&ctime=1700000000&userid=9501&tid=460x0.r"
	now := time.Now()
	recent := strconv.FormatInt(now.Add(-time.Hour).UnixMilli(), 10)
	old := strconv.FormatInt(now.Add(-100*time.Hour).UnixMilli(), 10) // outside the default 72-hour window
	store.SocialMu.Lock()
	store.DiaryPosts = []store.DiaryPost{
		{DiaryNo: "1", AvatarID: "9502", Nickname: "bob", Content: "one", OpenState: "public", RegDate: recent, ImageLocationType: "obs", Images: []store.DiaryImage{{ImageURL: photo}}},
		{DiaryNo: "2", AvatarID: "9502", Nickname: "bob", Content: "two", OpenState: "public", RegDate: recent, ImageLocationType: "obs", Images: []store.DiaryImage{{ImageURL: photo}}},
		// A text post (and a Share to Diary post) is a blank, untappable album tile: never listed, however active.
		{DiaryNo: "7", AvatarID: "9502", Nickname: "bob", Content: "text only", OpenState: "public", RegDate: recent, ImageLocationType: "none", Images: []store.DiaryImage{}},
		{DiaryNo: "8", AvatarID: "9502", Nickname: "bob", Content: "achieve", OpenState: "public", RegDate: recent, ImageTypeKind: "achieve", ImageLocationType: "none", Images: []store.DiaryImage{}},
		{DiaryNo: "3", AvatarID: "9503", Nickname: "carol", Content: "blind", OpenState: "public", Blind: true, RegDate: recent, Images: []store.DiaryImage{}},
		{DiaryNo: "4", AvatarID: "9501", Nickname: "alice", Content: "hidden", OpenState: "private", RegDate: recent, ImageLocationType: "obs", Images: []store.DiaryImage{{ImageURL: photo}}},
		{DiaryNo: "5", AvatarID: "9501", Nickname: "alice", Content: "five", OpenState: "public", RegDate: recent, ImageLocationType: "obs", Images: []store.DiaryImage{{ImageURL: photo}}},
		{DiaryNo: "6", AvatarID: "9502", Nickname: "bob", Content: "old", OpenState: "public", RegDate: old, ImageLocationType: "obs", Images: []store.DiaryImage{{ImageURL: photo}}},
	}
	store.DiaryLikes = []store.DiaryLike{
		// Post 1: two distinct likers = 2 points; a repeat like and the owner's own like add nothing.
		{LikeNo: "1", DiaryNo: "1", OwnerAvatarID: "9502", LikeAvatarID: "9601", LikeType: 1},
		{LikeNo: "2", DiaryNo: "1", OwnerAvatarID: "9502", LikeAvatarID: "9602", LikeType: 1},
		{LikeNo: "3", DiaryNo: "1", OwnerAvatarID: "9502", LikeAvatarID: "9602", LikeType: 2},
		{LikeNo: "4", DiaryNo: "1", OwnerAvatarID: "9502", LikeAvatarID: "9502", LikeType: 1},
		// The old post's activity does not bring it back inside the window.
		{LikeNo: "5", DiaryNo: "6", OwnerAvatarID: "9502", LikeAvatarID: "9601", LikeType: 1},
		// Nor does any amount of activity list the text post 7.
		{LikeNo: "8", DiaryNo: "7", OwnerAvatarID: "9502", LikeAvatarID: "9601", LikeType: 1},
		{LikeNo: "9", DiaryNo: "7", OwnerAvatarID: "9502", LikeAvatarID: "9602", LikeType: 1},
		{LikeNo: "10", DiaryNo: "7", OwnerAvatarID: "9502", LikeAvatarID: "9603", LikeType: 1},
		{LikeNo: "11", DiaryNo: "7", OwnerAvatarID: "9502", LikeAvatarID: "9604", LikeType: 1},
	}
	store.DiaryComments = []store.DiaryComment{
		// Post 5: one commenter (three comments) = 2 points, plus one liker = 3 points in all.
		{CommentNo: "1", DiaryNo: "5", OwnerAvatarID: "9501", CommentAvatarID: "9603", Content: "a"},
		{CommentNo: "2", DiaryNo: "5", OwnerAvatarID: "9501", CommentAvatarID: "9603", Content: "b"},
		{CommentNo: "3", DiaryNo: "5", OwnerAvatarID: "9501", CommentAvatarID: "9603", Content: "c"},
		{CommentNo: "4", DiaryNo: "5", OwnerAvatarID: "9501", CommentAvatarID: "9501", Content: "own"},
	}
	store.DiaryLikes = append(store.DiaryLikes, store.DiaryLike{LikeNo: "6", DiaryNo: "5", OwnerAvatarID: "9501", LikeAvatarID: "9604", LikeType: 1})
	store.SocialMu.Unlock()

	rec := serve(t, http.MethodGet, "/v4/rcmdpost/list?lastSeq=-1&size=12")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	rows := todaysRows(t, rec.Body.Bytes())
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3 (5, 1, 2)", len(rows))
	}
	for _, row := range rows {
		checkKinds(t, row, map[string]string{
			"image": "string", "linkType": "string", "description": "string", "icnImage": "string",
			"imageSubPath": "string", "orderSeq": "string", "postAvatarId": "string",
			"postAvatarName": "string", "postId": "string",
		})
	}
	var first map[string]string
	raw, _ := json.Marshal(rows[0])
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("first row %s: %v", raw, err)
	}
	want := map[string]string{
		"image": photo, "linkType": "goSomewhere(diaryDetail)", "description": "five", "icnImage": "lineplay/pr/9501_1",
		"imageSubPath": "profilebg/3.jpg", "orderSeq": "1", "postAvatarId": "9501", "postAvatarName": "alice", "postId": "5",
	}
	for key, value := range want {
		if first[key] != value {
			t.Fatalf("row 5 %s = %q, want %q", key, first[key], value)
		}
	}

	if got := todaysPostIDs(t, "/v4/rcmdpost/list?lastSeq=-1&size=12"); !slices.Equal(got, []string{"5", "1", "2"}) {
		t.Fatalf("full page %v, want [5 1 2]", got)
	}
	for _, step := range []struct {
		target string
		want   []string
	}{
		{"/v4/rcmdpost/list?lastSeq=-1&size=1", []string{"5"}},
		{"/v4/rcmdpost/list?lastSeq=1&size=1", []string{"1"}},
		{"/v4/rcmdpost/list?lastSeq=2&size=1", []string{"2"}},
		{"/v4/rcmdpost/list?lastSeq=3&size=1", nil},
	} {
		if got := todaysPostIDs(t, step.target); !slices.Equal(got, step.want) {
			t.Fatalf("%s: %v, want %v", step.target, got, step.want)
		}
	}
	if rec := serve(t, http.MethodGet, "/v4/rcmdpost/list?size=0"); rec.Code != http.StatusBadRequest {
		t.Fatalf("size=0 status %d, want 400", rec.Code)
	}

	// The scores themselves. The owner is the post's owner, not the like or comment row's copy: bob's own like and
	// comment on post 2 (rows without OwnerAvatarID) still add nothing.
	store.SocialMu.Lock()
	store.DiaryLikes = append(store.DiaryLikes, store.DiaryLike{LikeNo: "7", DiaryNo: "2", LikeAvatarID: "9502", LikeType: 1})
	store.DiaryComments = append(store.DiaryComments, store.DiaryComment{CommentNo: "5", DiaryNo: "2", CommentAvatarID: "9502", Content: "own"})
	ranked := bestRankLocked(economy.ActiveNews(), func(post store.DiaryPost) bool { return post.OpenState == "public" && !post.Blind })
	store.SocialMu.Unlock()
	scores := map[string]int{}
	for _, b := range ranked {
		scores[b.post.DiaryNo] = b.score
	}
	if want := map[string]int{"5": 3, "7": 4, "1": 2, "2": 0, "8": 0}; !maps.Equal(scores, want) {
		t.Fatalf("scores %v, want %v", scores, want)
	}
}
