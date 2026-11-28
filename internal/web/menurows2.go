package web

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cherry/internal/economy"
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

// todaysBestItem is one result.items row. The client reads every key with asString, so all nine are strings
// (IDA 2026-11-28, ResTodaysBestList 0x1b41ee0 and the cells below). The screen has two tabs (ToggleViewType
// 0x1b8be50: view type 0 = list, 1 = album grid). The list tab (AddTableViewData 0x1a9169c) keeps a row ONLY
// when linkType is "goSomewhere(diaryDetail)"; the album tab keeps every row, which is why an empty linkType
// showed an empty left tab and a full right one. List cell (NaTodaysBestDiaryCell): icnImage = the author's
// profile picture, fetched as <static base> + icnImage into cache/<icnImage> (SetAvatarImg 0x1f40e10,
// NaImageFileReqMgr type 2 -> ReqDownloadImage_Diary), so it is the relative "lineplay/pr/<oid>" form;
// imageSubPath = the profile background "profilebg/N.jpg" (SetAvatarUI 0x1f56598, ConvertToLocalSquareBGName);
// a non-empty image makes the tall photo cell and is loaded as an OBS path; postAvatarId + postId feed the
// diary check on tap (CheckValidTodaysBestDiaryCell 0x1bb382c). Paging: the next request's lastSeq is
// atoi(orderSeq) of the last row (tableViewScrolledBottom 0x1b8b4ec), so orderSeq is the 1-based rank.
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

const todaysBestLinkType = "goSomewhere(diaryDetail)"

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

// recommendPosts returns one page of Today's Best: public, non-Blind photo posts ranked by bestRankLocked, starting
// after rank lastSeq (-1 or 0 = from the top). Photo posts only: the album tab (NaTodaysBestAlbumCellData::SetPostData
// 0x1fb5518) adds no picture for an empty image, so a text or Share to Diary post was a blank tile that no tap opens
// (NaTodaysBestDiaryAlbumCell::CButtonHandler 0x1f55630 needs a picture index), and both tabs send the same request.
func recommendPosts(lastSeq int64, size int) []todaysBestItem {
	cfg := economy.ActiveNews()
	store.SocialMu.Lock()
	ranked := bestRankLocked(cfg, func(post store.DiaryPost) bool {
		return !post.Blind && canViewDiaryPost(post, store.AccountSnapshot{}, false) && hasDiaryPhoto(post)
	})
	store.SocialMu.Unlock()
	start := int(max(lastSeq, 0))
	if start >= len(ranked) {
		return []todaysBestItem{}
	}
	page := ranked[start:min(start+size, len(ranked))]
	var actors newsActorResolver
	items := make([]todaysBestItem, 0, len(page))
	for i, b := range page {
		image := ""
		if len(b.post.Images) > 0 {
			image = b.post.Images[0].ImageURL
		}
		actor := actors.resolve(b.post.AvatarID, b.post.Nickname)
		items = append(items, todaysBestItem{
			Image: image, LinkType: todaysBestLinkType, Description: diaryPreviewText(b.post),
			IcnImage: strings.TrimPrefix(actor.img, "/"), ImageSubPath: actor.bg,
			OrderSeq: strconv.Itoa(start + i + 1), PostAvatarID: b.post.AvatarID, PostAvatarName: actor.name,
			PostID: b.post.DiaryNo,
		})
	}
	return items
}

// hasDiaryPhoto: the post's first image is an OBS picture, the only kind the Today's Best cells and the News hot-post cell
// load (both force the location type "obs").
func hasDiaryPhoto(post store.DiaryPost) bool {
	return len(post.Images) > 0 && post.Images[0].ImageURL != "" && post.ImageLocationType == "obs"
}

type bestPost struct {
	post  store.DiaryPost
	score int
}

// bestRankLocked ranks the diary posts keep accepts by activity, highest first, newest first on a tie: each
// distinct player other than the post's owner who likes the post adds cfg.BestLikePoints, each who commented on it
// adds cfg.BestCommentPoints. With cfg.BestWindowHours > 0 only posts made within that many hours count; the window
// is on the wall clock, like a post's RegDate (social.go), not the game clock. Caller holds store.SocialMu.
// ponytail: scans every like and comment per call (fine at a few thousand posts); keep per-post counters if it shows.
func bestRankLocked(cfg *economy.NewsConfig, keep func(store.DiaryPost) bool) []bestPost {
	since := int64(0)
	if cfg.BestWindowHours > 0 {
		since = time.Now().Add(-time.Duration(cfg.BestWindowHours) * time.Hour).UnixMilli()
	}
	type candidate struct {
		bestPost
		seq uint64
	}
	var found []*candidate
	byNo := make(map[string]*candidate)
	for _, post := range store.DiaryPosts {
		seq, err := strconv.ParseUint(post.DiaryNo, 10, 64)
		if err != nil || !keep(post) {
			continue
		}
		if since > 0 {
			if reg, err := strconv.ParseInt(post.RegDate, 10, 64); err != nil || reg < since {
				continue
			}
		}
		c := &candidate{bestPost{post: post}, seq}
		found = append(found, c)
		byNo[post.DiaryNo] = c
	}
	type actorKey struct {
		diaryNo, actor string
		comment        bool
	}
	seen := make(map[actorKey]bool)
	add := func(key actorKey, points int) {
		c := byNo[key.diaryNo]
		if c == nil || points == 0 || key.actor == "" || key.actor == c.post.AvatarID || seen[key] {
			return
		}
		seen[key] = true
		c.score += points
	}
	for _, like := range store.DiaryLikes {
		if like.LikeType > 0 { // an unlike deletes the row; 0 would be one anyway
			add(actorKey{like.DiaryNo, like.LikeAvatarID, false}, cfg.BestLikePoints)
		}
	}
	for _, comment := range store.DiaryComments {
		add(actorKey{comment.DiaryNo, comment.CommentAvatarID, true}, cfg.BestCommentPoints)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		return found[i].seq > found[j].seq
	})
	out := make([]bestPost, len(found))
	for i, c := range found {
		out[i] = c.bestPost
	}
	return out
}
