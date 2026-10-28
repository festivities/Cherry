package web

import (
	"net/http"
	"sort"
	"strconv"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Home menu rows 72, 84-86 and 88 (audit rows-e-spec sections 3.1, 3.7-3.9 and 8). IDA, 2026-10-28:
//
//	GET /v4/rcmdpost/list?lastSeq=&size=  (row 72, Today's Best) -> {"result":{"items":[9 string keys]}}
//	     ResTodaysBestList 0x1b41ee0 reads result.items as an array; an empty array is a success (SbMsg 11761).
//	     Only a failed request posts 11762 ("Unable to connect"). Consumer NaTodaysBestLayerDX::MsgProc 0x1bb3c20.
//	GET /v4/storybook/list (row 84, Story Book tab) and GET /v4/shop/storygacha/list (rows 85, 86) ->
//	     an empty items array. stStoryBookBannerList::ParseJson 0x204f63c, stStoryGachaBannerList::ParseJson
//	     0x204d4ac, SetTableData 0x204f8a4 and 0x201db3c, SetPaging 0x201de8c: a size of 0 builds a placeholder
//	     row and never indexes the vector. Story gacha has no Cherry content.
//	GET /v4/line/event/invite/validate (row 88) -> four asBool keys. ResLineEventFriendInviteValid 0x1a5b388
//	     posts 11272; the popup's MsgProc stores validCountry, validAccount and validAgreement (not validEvent),
//	     then InitUI sends GET /v4/line/event/invite/friend/list (the static init at 0x14ef780 maps
//	     unk_3BE54F0 to that literal). The popup needs that second reply: an error there ends in
//	     "An unexpected error" and a close (11277).
const (
	storyEmptyListBody      = `{"result":{"items":[]}}`
	lineEventValidBody      = `{"result":{"validEvent":false,"validCountry":false,"validAccount":false,"validAgreement":false}}`
	lineEventFriendListBody = `{"result":{"campaignURL":"","friends":[]}}`
)

// todaysBestItem is one result.items row. The client reads every key with asString, so all nine are strings.
// icnImage, imageSubPath and linkType stay empty: their client use is not traced [I].
type todaysBestItem struct {
	Image          string `json:"image"`
	LinkType       string `json:"linkType"`
	Description    string `json:"description"`
	IcnImage       string `json:"icnImage"`
	ImageSubPath   string `json:"imageSubPath"`
	OrderSeq       string `json:"orderSeq"`
	PostAvatarID   string `json:"postAvatarId"`
	PostAvatarName string `json:"postAvatarName"`
	PostID         string `json:"postId"`
}

func handleRecommendPostList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	lastSeq, size, ok := cursorPage(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	httpx.WriteObj(w, map[string]any{"result": map[string]any{"items": recommendPosts(lastSeq, size)}})
}

// recommendPosts returns the newest public diary posts older than the lastSeq cursor, as the diary unfold route
// pages them. Visibility is canViewDiaryPost for an anonymous viewer; Blind posts are left out too.
func recommendPosts(lastSeq int64, size int) []todaysBestItem {
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	type candidate struct {
		seq  uint64
		post store.DiaryPost
	}
	var found []candidate
	for _, post := range store.DiaryPosts {
		seq, err := strconv.ParseUint(post.DiaryNo, 10, 64)
		if err != nil || post.Blind || !canViewDiaryPost(post, store.AccountSnapshot{}, false) || (lastSeq >= 0 && seq >= uint64(lastSeq)) {
			continue
		}
		found = append(found, candidate{seq, post})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].seq > found[j].seq })
	if len(found) > size {
		found = found[:size]
	}
	items := make([]todaysBestItem, 0, len(found))
	for _, c := range found {
		image := ""
		if len(c.post.Images) > 0 {
			image = c.post.Images[0].ImageURL
		}
		items = append(items, todaysBestItem{
			Image: image, Description: c.post.Title, OrderSeq: c.post.DiaryNo,
			PostAvatarID: c.post.AvatarID, PostAvatarName: c.post.Nickname, PostID: c.post.DiaryNo,
		})
	}
	return items
}
