package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

type buddyRow struct {
	AvatarNo            string `json:"avatarNo"`
	AvatarName          string `json:"avatarName"`
	BuddyAvatarNo       string `json:"buddyAvatarNo"`
	Status              int    `json:"status"`
	FriendStatus        int    `json:"friendStatus"`
	Bot                 bool   `json:"bot"`
	LineBuddyYn         string `json:"lineBuddyYn"`
	Mid                 string `json:"mid"`
	ProfileImageURL     string `json:"profileImageUrl"`
	ObsProfileImagePath string `json:"obsProfileImagePath"`
}

func handleFriendSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	actor, actorOK := store.AccountForRequest(r)
	type relRow struct {
		aid    string
		status int
	}
	var rels []relRow
	store.SocialMu.Lock()
	removed, marked := store.FriendRemoved, store.FriendBookmarked
	if actorOK {
		for _, f := range store.Friendships {
			other := f.B
			if f.A != actor.Aid && f.B != actor.Aid {
				continue
			}
			if f.B == actor.Aid {
				other = f.A
			}
			switch {
			case f.State == "accepted":
				rels = append(rels, relRow{other, 1})
			case f.State == "removed":
				rels = append(rels, relRow{other, -1})
			case f.State == "pending" && f.B == actor.Aid:
				rels = append(rels, relRow{other, relReceived})
			case f.State == "pending":
				// Sent request: 3 lands in an unread per-state list (PushFriendData); harmless cache row.
				rels = append(rels, relRow{other, relSent})
			}
		}
	}
	store.SocialMu.Unlock()
	// Native caches sync rows by avatarNo and never prunes omitted ones; see PLAN
	// "Stale friend-cache fix" before renumbering friendAID again.
	// Accounts 1 and 2 were renumbered to 1001/1002 (Room Party cells hide
	// single-character aids); clients cached them as friends, so keep hiding them.
	// ponytail: permanent tombstones; aids < minAvatarID are never allocated again.
	buddies := []buddyRow{}
	for _, aid := range store.RetiredAvatarIDs {
		if _, live := store.AccountByAvatarID(aid); live {
			continue
		}
		buddies = append(buddies, buddyRow{AvatarNo: aid, BuddyAvatarNo: aid, Status: -1, FriendStatus: -1, LineBuddyYn: "N", Mid: aid})
	}
	bookmarks := []string{}
	if !removed {
		// Native sorts by status: 1 is an accepted friend (My Friends); 0 lands in Received Requests.
		buddies = append(buddies, buddyRow{
			AvatarNo: store.FriendAID, AvatarName: store.FriendName, BuddyAvatarNo: store.FriendAID,
			Status: 1, FriendStatus: 1, LineBuddyYn: "N", Mid: store.FriendAID,
		})
	}
	for _, rel := range rels {
		name := rel.aid
		if acc, ok := store.AccountByAvatarID(rel.aid); ok {
			name = acc.Name
		}
		buddies = append(buddies, buddyRow{
			AvatarNo: rel.aid, AvatarName: name, BuddyAvatarNo: rel.aid,
			Status: rel.status, FriendStatus: rel.status, LineBuddyYn: "N", Mid: rel.aid,
		})
	}
	friends := 0
	for _, b := range buddies {
		if b.Status == 1 {
			friends++
		}
	}
	if marked && !removed {
		bookmarks = []string{store.FriendAID}
	}
	body, err := json.Marshal(map[string]any{
		"result": map[string]any{
			"existProfile": false, "nextCursor": 0, "timestamp": "0",
			"friendsCount": friends, "buddyList": buddies,
			"newbieRecommendList": []any{}, "nearbyRecommendList": []any{},
			"bookmarks": bookmarks,
		},
	})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// Client friendStatus enum, actor-relative (libgame NaProfilePopup::SetBottomButton @0x2286da8,
// NaSearchFriendCell::SetButtonState @0x1c7b270): 0 shows ACCEPT, 1 friends, 3 sent (Add disabled),
// 4 blocked; anything else (-1/2) shows Add. Never send 0 for strangers.
const (
	relReceived = 0
	relFriends  = 1
	relNone     = 2
	relSent     = 3
)

// friendRelation returns the enum from actor's point of view. Caller holds socialMu.
func friendRelation(actor string, actorOK bool, aid string) int {
	if aid == store.FriendAID {
		if store.FriendRemoved {
			return relNone
		}
		return relFriends
	}
	if !actorOK {
		return relNone
	}
	if i := store.FriendshipIndex(actor, aid, ""); i >= 0 {
		switch f := store.Friendships[i]; {
		case f.State == "accepted":
			return relFriends
		case f.State == "pending" && f.A == actor:
			return relSent
		case f.State == "pending":
			return relReceived
		}
	}
	return relNone
}

func handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, viewerOK := store.AccountForRequest(r)
	rest := strings.TrimPrefix(r.URL.Path, "/v4/profile/")
	aid, _, _ := strings.Cut(rest, "/")
	if aid == store.FriendAID {
		profileJSON(w, profileBodyFor(aid, store.FriendName, true, viewer, viewerOK))
		return
	}
	if acc, ok := store.AccountByAvatarID(aid); ok {
		profileJSON(w, profileBodyFor(aid, acc.Name, true, viewer, viewerOK))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, profileBody)
}

func profileBodyFor(aid, name string, hasGarden bool, viewer store.AccountSnapshot, viewerOK bool) map[string]any {
	store.SocialMu.Lock()
	friendStatus := friendRelation(viewer.Aid, viewerOK, aid)
	diaryCount, guestbookCount := 0, 0
	for _, post := range store.DiaryPosts {
		if post.AvatarID == aid && canViewDiaryPost(post, viewer, viewerOK) {
			diaryCount++
		}
	}
	for _, entry := range store.GuestbookEntries {
		if entry.HostAvtNo == aid {
			guestbookCount++
		}
	}
	store.SocialMu.Unlock()
	return map[string]any{
		"avatarId": aid, "name": name, "nickname": name, "diaryName": name,
		"newbie": false, "friendStatus": friendStatus, "hasGarden": hasGarden,
		"showGuestBook": true, "showCmt": true, "showProfile": true,
		"diaryCount": diaryCount, "guestBookCount": guestbookCount,
	}
}

func canViewDiaryPost(post store.DiaryPost, viewer store.AccountSnapshot, viewerOK bool) bool {
	return post.OpenState == "public" || (viewerOK && viewer.Aid == post.AvatarID)
}

func profileJSON(w http.ResponseWriter, body any) {
	data, err := json.Marshal(map[string]any{"result": body})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(data))
}

func handleFriendBookmark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	if _, ok := store.AccountForRequest(r); !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v4/friend/bookmark/")
	remove := false
	if cut, ok := strings.CutPrefix(rest, "remove/"); ok {
		remove = true
		rest = cut
	}
	aid := strings.Trim(rest, "/")
	if strings.Contains(aid, "/") || aid != store.FriendAID {
		httpx.ServeNotFound(w)
		return
	}
	store.SocialMu.Lock()
	prev := store.FriendBookmarked
	store.FriendBookmarked = !remove && !store.FriendRemoved
	err := store.SaveSocialLocked()
	if err != nil {
		store.FriendBookmarked = prev
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":1}`)
}

func handleFriendRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var aids []string
	if _, err = httpx.UnmarshalNativeJSON(raw, &aids); err != nil || aids == nil || len(aids) > 100 {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	for _, aid := range aids {
		if !store.ValidAvatarID(aid) {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
	}
	removed := []string{}
	store.SocialMu.Lock()
	prevRemoved, prevMarked := store.FriendRemoved, store.FriendBookmarked
	prevFriendships := append([]store.Friendship(nil), store.Friendships...)
	for _, aid := range aids {
		if i := store.FriendshipIndex(actor.Aid, aid, ""); i >= 0 && store.Friendships[i].State != "removed" {
			store.Friendships[i].State = "removed"
			removed = append(removed, aid)
		}
		if aid == store.FriendAID && !store.FriendRemoved {
			store.FriendRemoved = true
			store.FriendBookmarked = false
			removed = append(removed, store.FriendAID)
		}
	}
	err = store.SaveSocialLocked()
	if err != nil {
		store.FriendRemoved, store.FriendBookmarked = prevRemoved, prevMarked
		store.Friendships = prevFriendships
		removed = nil
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	if removed == nil {
		removed = []string{}
	}
	body, err := json.Marshal(map[string]any{"result": map[string]any{"success": removed}})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func handleDiaryCheckExist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v4/diary2/ext/checkExist/")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[2] != "" || !store.ValidAvatarID(parts[0]) || parts[1] != "0" || !store.KnownSocialAvatar(parts[0]) {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

func handleDiaryIntro(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, viewerOK := store.AccountForRequest(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/intro/"), "/")
	aid, _, _ := strings.Cut(rest, "/")
	if aid == "" {
		aid = "1"
	}
	nickname := "guest"
	if aid == store.FriendAID {
		nickname = store.FriendName
	} else if acc, ok := store.AccountByAvatarID(aid); ok && acc.Name != "" {
		nickname = acc.Name
	}
	store.SocialMu.Lock()
	count := 0
	for _, post := range store.DiaryPosts {
		if post.AvatarID == aid && canViewDiaryPost(post, viewer, viewerOK) {
			count++
		}
	}
	store.SocialMu.Unlock()
	body, err := json.Marshal(map[string]any{"result": map[string]any{
		"avatarId": aid, "nickname": nickname, "diaryName": nickname, "diaryCount": count,
		"showGuestBook": true, "showCmt": true, "showProfile": true,
	}})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func handleDiaryUnfold(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, viewerOK := store.AccountForRequest(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/ext/unfold/"), "/")
	aid, _, _ := strings.Cut(rest, "/")
	lastSeq, size, ok := cursorPage(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	store.SocialMu.Lock()
	items := make([]store.DiaryPost, 0)
	for _, post := range store.DiaryPosts {
		if (aid != "" && post.AvatarID != aid) || !canViewDiaryPost(post, viewer, viewerOK) {
			continue
		}
		seq, err := strconv.ParseUint(post.DiaryNo, 10, 64)
		if err != nil || (lastSeq >= 0 && seq >= uint64(lastSeq)) {
			continue
		}
		store.NormalizeDiaryPost(&post)
		if post.Images == nil {
			post.Images = []store.DiaryImage{}
		}
		items = append(items, post)
	}
	store.SocialMu.Unlock()
	lastData := len(items) <= size
	if len(items) > size {
		items = items[:size]
	}
	body, err := json.Marshal(map[string]any{"result": map[string]any{"lastData": lastData, "items": items}})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func handleDiaryPhotoAlbum(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, viewerOK := store.AccountForRequest(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/ext/unfold/photo/"), "/")
	if !store.ValidAvatarID(rest) {
		httpx.ServeNotFound(w)
		return
	}
	lastSeq, size, ok := cursorPage(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	store.SocialMu.Lock()
	items := make([]store.DiaryPost, 0)
	for _, post := range store.DiaryPosts {
		if post.AvatarID != rest || len(post.Images) == 0 || !canViewDiaryPost(post, viewer, viewerOK) {
			continue
		}
		seq, err := strconv.ParseUint(post.DiaryNo, 10, 64)
		if err != nil || (lastSeq >= 0 && seq >= uint64(lastSeq)) {
			continue
		}
		store.NormalizeDiaryPost(&post)
		items = append(items, post)
	}
	store.SocialMu.Unlock()
	lastData := len(items) <= size
	if len(items) > size {
		items = items[:size]
	}
	body, err := json.Marshal(map[string]any{"result": map[string]any{"lastData": lastData, "items": items}})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func cursorPage(r *http.Request) (lastSeq int64, size int, ok bool) {
	lastSeq, size = -1, 10
	query := r.URL.Query()
	if value := query.Get("lastSeq"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < -1 {
			return 0, 0, false
		}
		lastSeq = parsed
	}
	if value := query.Get("size"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, 0, false
		}
		size = parsed
	}
	return lastSeq, size, true
}

func handleDiaryLook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, viewerOK := store.AccountForRequest(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/ext/look/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || !store.ValidAvatarID(parts[0]) || !store.ValidAvatarID(parts[1]) {
		httpx.ServeNotFound(w)
		return
	}
	store.SocialMu.Lock()
	var found *store.DiaryPost
	for i := range store.DiaryPosts {
		if store.DiaryPosts[i].AvatarID == parts[0] && store.DiaryPosts[i].DiaryNo == parts[1] && canViewDiaryPost(store.DiaryPosts[i], viewer, viewerOK) {
			post := store.DiaryPosts[i]
			store.NormalizeDiaryPost(&post)
			if post.Images == nil {
				post.Images = []store.DiaryImage{}
			}
			found = &post
			break
		}
	}
	store.SocialMu.Unlock()
	if found == nil {
		httpx.ServeNotFound(w)
		return
	}
	data, err := json.Marshal(map[string]any{"result": map[string]any{
		"diaryNo": found.DiaryNo, "avatarId": found.AvatarID, "nickname": found.Nickname,
		"regDate": found.RegDate, "title": found.Title, "openState": found.OpenState,
		"content": found.Content, "imageTypeKind": found.ImageTypeKind,
		"imageLocationType": found.ImageLocationType, "feelingState": found.FeelingState,
		"commentCount": found.CommentCount, "likeCount": found.LikeCount, "myLikeType": found.MyLikeType,
		"blind": found.Blind, "images": found.Images,
	}})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(data))
}

func handleDiaryWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var req struct {
		AvatarID      string             `json:"avatarId"`
		Title         string             `json:"title"`
		Content       string             `json:"content"`
		ImageTypeKind string             `json:"imageTypeKind"`
		FeelingState  *int               `json:"feelingState"`
		OpenState     string             `json:"openState"`
		Images        []store.DiaryImage `json:"images"`
	}
	if _, err = httpx.UnmarshalNativeJSON(raw, &req); err != nil || req.AvatarID == "" || req.AvatarID != actor.Aid || len(req.Title) > 200 || len(req.Content) > 16000 || len(req.ImageTypeKind) > 64 || len(req.OpenState) > 32 || len(req.Images) > 20 || !utf8.ValidString(req.Title) || !utf8.ValidString(req.Content) || !utf8.ValidString(req.ImageTypeKind) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	for _, image := range req.Images {
		if !store.ValidDiaryImage(image) {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
	}
	feeling := -1
	if req.FeelingState != nil {
		feeling = *req.FeelingState
	}
	openState := req.OpenState
	if openState == "" {
		openState = "public"
	}
	images := req.Images
	if images == nil {
		images = []store.DiaryImage{}
	}
	nickname := actor.Name
	if nickname == "" {
		nickname = "guest"
	}
	store.SocialMu.Lock()
	if store.NextDiaryNo == ^uint64(0) {
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	store.NextDiaryNo++
	post := store.DiaryPost{
		DiaryNo: strconv.FormatUint(store.NextDiaryNo, 10), AvatarID: req.AvatarID, Nickname: nickname,
		RegDate: strconv.FormatInt(time.Now().UnixMilli(), 10), Title: req.Title, OpenState: openState,
		Content: req.Content, ImageTypeKind: req.ImageTypeKind,
		ImageLocationType: store.DiaryImageLocationType(images), FeelingState: feeling, Images: images,
	}
	store.DiaryPosts = append([]store.DiaryPost{post}, store.DiaryPosts...)
	err = store.SaveSocialLocked()
	if err != nil {
		store.DiaryPosts = store.DiaryPosts[1:]
		store.NextDiaryNo--
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	body, err := json.Marshal(map[string]any{"result": post})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func handleDiaryErase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/erase/"), "/")
	diaryNo, _, _ := strings.Cut(rest, "/")
	store.SocialMu.Lock()
	prev := append([]store.DiaryPost(nil), store.DiaryPosts...)
	kept := store.DiaryPosts[:0]
	removed := false
	for _, post := range store.DiaryPosts {
		if post.DiaryNo == diaryNo && post.AvatarID == actor.Aid {
			removed = true
			continue
		}
		kept = append(kept, post)
	}
	if kept == nil {
		kept = []store.DiaryPost{}
	}
	store.DiaryPosts = kept
	var err error
	if removed {
		err = store.SaveSocialLocked()
	}
	if err != nil {
		store.DiaryPosts = prev
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

type friendSearchRequest struct {
	Country       string `json:"country"`
	CaricNickName string `json:"caricNickName"`
	SearchType    string `json:"searchType"`
}

type friendSearchItem struct {
	AvatarID            string `json:"avatarId"`
	CaricNickName       string `json:"caricNickName"`
	FriendStatus        string `json:"friendStatus"`
	ProfileURL          string `json:"profileUrl"`
	ObsProfileImagePath string `json:"obsProfileImagePath"`
	WholeBodyProfileURL string `json:"wholeBodyProfileUrl"`
	BG                  string `json:"bg"`
	BadgeIcon           string `json:"badgeIcon"`
	BirthDayTime        string `json:"birthDayTime"`
	UseBrthYn           bool   `json:"useBrthYn"`
}

func handleFriendSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req friendSearchRequest
	if err != nil || func() bool { _, e := httpx.UnmarshalNativeJSON(raw, &req); return e != nil }() || strings.TrimSpace(req.Country) == "" || len(req.Country) > 8 || len(req.CaricNickName) > 128 {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	searchType := strings.ToUpper(strings.TrimSpace(req.SearchType))
	query := strings.TrimSpace(req.CaricNickName)
	if (searchType != "CODE" && searchType != "NICK") || query == "" {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	page, size, ok := requestPage(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}

	items := []friendSearchItem{{AvatarID: store.FriendAID, CaricNickName: store.FriendName, FriendStatus: "1"}}
	store.SocialMu.Lock()
	removed := store.FriendRemoved
	store.SocialMu.Unlock()
	if removed {
		items[0].FriendStatus = strconv.Itoa(relNone)
	}
	store.AccountsMu.Lock()
	byID := make(map[string]friendSearchItem, len(store.Accounts))
	for _, acc := range store.Accounts {
		if acc == nil || !store.ValidAvatarID(acc.Aid) || acc.Aid == "0" || acc.Aid == store.FriendAID {
			continue
		}
		candidate := friendSearchItem{AvatarID: acc.Aid, CaricNickName: acc.Name, FriendStatus: strconv.Itoa(relNone)}
		if current, exists := byID[acc.Aid]; exists && current.CaricNickName <= candidate.CaricNickName {
			continue
		}
		byID[acc.Aid] = candidate
	}
	store.AccountsMu.Unlock()
	searcher, searcherOK := store.AccountForRequest(r)
	store.SocialMu.Lock()
	for aid, item := range byID {
		if searcherOK {
			item.FriendStatus = strconv.Itoa(friendRelation(searcher.Aid, true, aid))
			byID[aid] = item
		}
	}
	store.SocialMu.Unlock()
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items[1:], func(i, j int) bool { return items[i+1].AvatarID < items[j+1].AvatarID })
	matches := make([]friendSearchItem, 0, len(items))
	for _, item := range items {
		match := item.AvatarID == query
		if searchType == "NICK" {
			match = strings.Contains(strings.ToLower(item.CaricNickName), strings.ToLower(query))
		}
		if match {
			matches = append(matches, item)
		}
	}
	start := len(matches)
	if page <= len(matches)/size {
		start = page * size
	}
	end := start + size
	if end > len(matches) {
		end = len(matches)
	}
	pageItems := append([]friendSearchItem(nil), matches[start:end]...)
	if pageItems == nil {
		pageItems = []friendSearchItem{}
	}
	previous, next := "0", "0"
	if page > 0 {
		previous = strconv.Itoa(page - 1)
	}
	if end < len(matches) {
		next = strconv.Itoa(page + 1)
	}
	body, err := json.Marshal(map[string]any{"result": map[string]any{
		"previousCursor": previous, "nextCursor": next,
		"totalCount": strconv.Itoa(len(matches)), "items": pageItems,
	}})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func requestPage(r *http.Request) (page, size int, ok bool) {
	page, size = 0, 20
	query := r.URL.Query()
	if value := query.Get("page"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return 0, 0, false
		}
		page = parsed
	}
	if value := query.Get("size"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, 0, false
		}
		size = parsed
	}
	return page, size, true
}

func handleFriendStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/friend/status/"), "/")
	if !store.KnownSocialAvatar(aid) {
		httpx.ServeNotFound(w)
		return
	}
	actor, actorOK := store.AccountForRequest(r)
	store.SocialMu.Lock()
	status := friendRelation(actor.Aid, actorOK, aid)
	store.SocialMu.Unlock()
	body, _ := json.Marshal(map[string]int{"result": status})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func handleFriendApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/friend/apply/"), "/")
	raw, err := httpx.ReadNativeBody(r)
	var req struct {
		ApplyAvatarID string `json:"applyAvatarId"`
	}
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if _, err := httpx.UnmarshalNativeJSON(raw, &req); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	// Native ReqFriendApplyWithAvatarID @0x1bcc6fc always POSTs /v4/friend/apply/ with an EMPTY path aid;
	// the target is only in the body.
	if aid == "" {
		aid = req.ApplyAvatarID
	}
	if !store.KnownFriendTarget(aid) {
		httpx.ServeNotFound(w)
		return
	}
	if req.ApplyAvatarID != aid {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if aid == store.FriendAID {
		restoreFriend(w)
		return
	}
	if aid == actor.Aid {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	setFriendship(w, actor.Aid, aid, false)
}

func handleFriendAccept(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/friend/accept/"), "/")
	raw, err := httpx.ReadNativeBody(r)
	var body any
	if !store.KnownFriendTarget(aid) {
		httpx.ServeNotFound(w)
		return
	}
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if _, err := httpx.UnmarshalNativeJSON(raw, &body); err != nil || body != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if aid == store.FriendAID {
		restoreFriend(w)
		return
	}
	setFriendship(w, actor.Aid, aid, true)
}

// setFriendship handles a real-account apply (accept=false) or accept (accept=true) by actor toward other.
func setFriendship(w http.ResponseWriter, actor, other string, accept bool) {
	store.SocialMu.Lock()
	prev := append([]store.Friendship(nil), store.Friendships...)
	i := store.FriendshipIndex(actor, other, "")
	switch {
	case accept && (i < 0 || store.Friendships[i].State != "pending" || store.Friendships[i].B != actor):
		if i >= 0 && store.Friendships[i].State == "accepted" {
			break // already friends: idempotent
		}
		store.SocialMu.Unlock()
		httpx.ServeNotFound(w)
		return
	case i < 0:
		store.Friendships = append(store.Friendships, store.Friendship{A: actor, B: other, State: "pending"})
	case store.Friendships[i].State == "removed":
		store.Friendships[i] = store.Friendship{A: actor, B: other, State: "pending"}
	case store.Friendships[i].State == "pending" && store.Friendships[i].A == other:
		store.Friendships[i].State = "accepted"
	}
	err := store.SaveSocialLocked()
	if err != nil {
		store.Friendships = prev
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":1}`)
}

func restoreFriend(w http.ResponseWriter) {
	store.SocialMu.Lock()
	prevRemoved, prevBookmarked := store.FriendRemoved, store.FriendBookmarked
	store.FriendRemoved = false
	err := store.SaveSocialLocked()
	if err != nil {
		store.FriendRemoved, store.FriendBookmarked = prevRemoved, prevBookmarked
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":1}`)
}
