package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

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

// TestRecommendPostListVisibilityPagingAndShape: only public, non-blind posts; newest first; the lastSeq cursor
// pages like the diary unfold route; every row has nine string keys; image is the diary route's imageUrl as stored.
func TestRecommendPostListVisibilityPagingAndShape(t *testing.T) {
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	const photo = "/lineplay/d/download.nhn?oid=9501_1700000000&ctime=1700000000&userid=9501&tid=460x0.r"
	store.SocialMu.Lock()
	store.DiaryPosts = []store.DiaryPost{
		{DiaryNo: "2", AvatarID: "9502", Nickname: "bob", Title: "two", OpenState: "public", Images: []store.DiaryImage{}},
		{DiaryNo: "4", AvatarID: "9501", Nickname: "alice", Title: "hidden", OpenState: "private", Images: []store.DiaryImage{{ImageURL: photo}}},
		{DiaryNo: "5", AvatarID: "9501", Nickname: "alice", Title: "five", OpenState: "public", Images: []store.DiaryImage{{ImageURL: photo}}},
		{DiaryNo: "3", AvatarID: "9503", Nickname: "carol", Title: "blind", OpenState: "public", Blind: true, Images: []store.DiaryImage{}},
		{DiaryNo: "1", AvatarID: "9502", Nickname: "bob", Title: "one", OpenState: "public", Images: []store.DiaryImage{}},
	}
	store.SocialMu.Unlock()

	rec := serve(t, http.MethodGet, "/v4/rcmdpost/list?lastSeq=-1&size=12")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	rows := todaysRows(t, rec.Body.Bytes())
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3 (5, 2, 1)", len(rows))
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
		"image": photo, "linkType": "", "description": "five", "icnImage": "", "imageSubPath": "",
		"orderSeq": "5", "postAvatarId": "9501", "postAvatarName": "alice", "postId": "5",
	}
	for key, value := range want {
		if first[key] != value {
			t.Fatalf("row 5 %s = %q, want %q", key, first[key], value)
		}
	}

	if got := todaysPostIDs(t, "/v4/rcmdpost/list?lastSeq=-1&size=12"); !slices.Equal(got, []string{"5", "2", "1"}) {
		t.Fatalf("full page %v, want [5 2 1]", got)
	}
	for _, step := range []struct {
		target string
		want   []string
	}{
		{"/v4/rcmdpost/list?lastSeq=-1&size=1", []string{"5"}},
		{"/v4/rcmdpost/list?lastSeq=5&size=1", []string{"2"}},
		{"/v4/rcmdpost/list?lastSeq=2&size=1", []string{"1"}},
		{"/v4/rcmdpost/list?lastSeq=1&size=1", nil},
	} {
		if got := todaysPostIDs(t, step.target); !slices.Equal(got, step.want) {
			t.Fatalf("%s: %v, want %v", step.target, got, step.want)
		}
	}
	if rec := serve(t, http.MethodGet, "/v4/rcmdpost/list?size=0"); rec.Code != http.StatusBadRequest {
		t.Fatalf("size=0 status %d, want 400", rec.Code)
	}
}
