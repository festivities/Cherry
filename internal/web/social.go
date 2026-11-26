package web

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cherry/internal/config"
	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/lpn"
	"cherry/internal/store"
)

func syntheticFriendEnabled() bool { return !config.IdentityMode() }

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
	Bg                  string `json:"bg"`
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
	if !syntheticFriendEnabled() {
		if _, live := store.AccountByAvatarID(store.FriendAID); !live {
			buddies = append(buddies, buddyRow{
				AvatarNo: store.FriendAID, BuddyAvatarNo: store.FriendAID,
				Status: -1, FriendStatus: -1, LineBuddyYn: "N", Mid: store.FriendAID,
			})
		}
	} else if !removed {
		// Native sorts by status: 1 is an accepted friend (My Friends); 0 lands in Received Requests.
		buddies = append(buddies, buddyRow{
			AvatarNo: store.FriendAID, AvatarName: store.FriendName(), BuddyAvatarNo: store.FriendAID,
			Status: 1, FriendStatus: 1, LineBuddyYn: "N", Mid: store.FriendAID,
		})
	}
	for _, rel := range rels {
		name, img, bg := rel.aid, "", ""
		if acc, ok := store.AccountByAvatarID(rel.aid); ok {
			name = acc.Name
			img, _, bg = acc.ProfileImage.Fields()
		}
		// NaBaseFriendCell::SetAvatarImage (0x1c4431c) and NaRoomPartySearchCell (0x1ef9e60/0x1ef9d7c) gate on
		// profileImageUrl != "" and bg != "", then fetch obsProfileImagePath.
		buddies = append(buddies, buddyRow{
			AvatarNo: rel.aid, AvatarName: name, BuddyAvatarNo: rel.aid,
			Status: rel.status, FriendStatus: rel.status, LineBuddyYn: "N", Mid: rel.aid,
			ProfileImageURL: img, ObsProfileImagePath: img, Bg: bg,
		})
	}
	friends := 0
	accepted := make(map[string]bool, len(buddies))
	for _, b := range buddies {
		if b.Status == 1 {
			friends++
			accepted[b.AvatarNo] = true
		}
	}
	if syntheticFriendEnabled() && marked && !removed {
		bookmarks = append(bookmarks, store.FriendAID)
	}
	if actorOK {
		for _, aid := range actor.FriendBookmarks {
			if accepted[aid] && !slices.Contains(bookmarks, aid) {
				bookmarks = append(bookmarks, aid)
			}
		}
	}
	// The Friends screen's own "Profile" row (NaFriendProfileCell, from the sync reply's top level) has the same gate:
	// myProfileImageUrl non-empty, then obsProfileImagePath (friendlist-picture-spec 2026-11-22).
	own, ownBg, ownName, ownAid := "", "", "", ""
	if actorOK {
		own, _, ownBg = actor.ProfileImage.Fields()
		ownName, ownAid = actor.Name, actor.Aid
	}
	body, err := json.Marshal(map[string]any{
		"result": map[string]any{
			"existProfile": false, "nextCursor": 0, "timestamp": "0",
			"friendsCount": friends, "buddyList": buddies,
			"newbieRecommendList": []any{}, "nearbyRecommendList": []any{},
			"bookmarks": bookmarks, "mid": ownAid, "myAvatarName": ownName,
			"myProfileImageUrl": own, "obsProfileImagePath": own, "bg": ownBg,
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
		if !syntheticFriendEnabled() || store.FriendRemoved {
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
		profileJSON(w, profileBodyFor(aid, store.FriendName(), true, viewer, viewerOK))
		return
	}
	if acc, ok := store.AccountByAvatarID(aid); ok {
		body := profileBodyFor(aid, acc.Name, true, viewer, viewerOK)
		if img, wb, bg := acc.ProfileImage.Fields(); img != "" {
			// Home MENU "Profile" cell (profilecell-spec.md, 2026-11-22): NaHomeMoreViewCell::SetProfileImage 0x16a2114
			// draws the photo only with result.profile and a non-empty result.bg (sDataProfileWithAvatarID::ParseJson
			// 0x1b6f000 keys). Fields() defaults empty bg to DefaultProfileBg (profilebg/12.jpg) and empty wb to img.
			body["obsProfileImagePath"], body["obsWholeBodyProfileImagePath"], body["bg"] = img, wb, bg
			body["profile"] = img
		}
		body["myGardenAvatarInfo"] = map[string]any{
			"level":          acc.GardenStats.Level(),
			"footprintCount": acc.GardenStats.Footprints,
			"userPlanet":     acc.GardenStats.UserPlanet,
		}
		body["intro"] = profileIntro(acc) // ParseJson (0x1b6f000) reads result.intro as the Status Message
		// "Avatar Birthday" (NaProfilePopup::SetBirthday 0x21de278): result.created read asString, epoch ms, shown at GMT+9;
		// the row shows without birthday (Jan 1 1970 before f8180ef). birthday (asBool) = "today is the birthday": the
		// profile's "Happy Birthday" and the Garden hat (NaGardenUILayer::SetAvatarBirthImage). duplogin-spec.md section 6.
		created := store.AvatarBirthday(aid)
		body["created"], body["birthday"] = strconv.FormatInt(created, 10), economy.IsAvatarBirthday(created, economy.NowFunc())
		profileJSON(w, body)
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
	friendsCount := 0
	for _, f := range store.Friendships {
		if f.State == "accepted" && (f.A == aid || f.B == aid) {
			friendsCount++
		}
	}
	if aid != store.FriendAID && syntheticFriendEnabled() && !store.FriendRemoved {
		friendsCount++
	}
	store.SocialMu.Unlock()
	body := map[string]any{
		"avatarId": aid, "name": name, "nickname": name, "diaryName": name,
		"newbie": false, "friendStatus": friendStatus, "friendApplyYn": true, "hasGarden": hasGarden,
		"showGuestBook": true, "showCmt": true, "showProfile": true,
		"diaryCount": diaryCount, "guestBookCount": guestbookCount, "friendsCount": friendsCount,
		"myGardenAvatarInfo": map[string]any{"level": 1, "footprintCount": int64(0), "userPlanet": 0},
	}
	for k, v := range heartProfileFields(viewer.Aid, aid) {
		body[k] = v
	}
	return body
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

// Client ResRecommandMyCodeURL (libgame 0x1a5b768) reads result.recommendCode.
// Friend search's CODE type matches the avatar id, so the code is the aid.
func handleRecommendCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	acc, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	httpx.WriteObj(w, map[string]any{"result": map[string]string{"recommendCode": acc.Aid}})
}

// Badge-set response handler is a no-op in the client; just acknowledge.
func handleBadgeSetAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, badgeSetAckBody)
}

const maxFriendBookmarks = 500

func handleFriendBookmark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
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
	if strings.Contains(aid, "/") || !store.ValidAvatarID(aid) {
		httpx.ServeNotFound(w)
		return
	}
	if aid == store.FriendAID {
		if !syntheticFriendEnabled() {
			httpx.ServeNotFound(w)
			return
		}
		store.SocialMu.Lock()
		prev := store.FriendBookmarked
		store.FriendBookmarked = !remove && !store.FriendRemoved
		err := store.SaveSocialLocked(store.SocialChange{Scalars: true})
		if err != nil {
			store.FriendBookmarked = prev
		}
		store.SocialMu.Unlock()
		if err != nil {
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, `{"result":1}`)
		return
	}
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked(actor.Aid)
	if acc == nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	if !remove {
		store.SocialMu.Lock()
		accepted := store.FriendshipIndex(acc.Aid, aid, "accepted") >= 0
		store.SocialMu.Unlock()
		if !accepted {
			store.AccountsMu.Unlock()
			httpx.ServeNotFound(w)
			return
		}
	}
	prev := acc.FriendBookmarks
	var next []string
	changed := false
	if remove {
		if slices.Contains(prev, aid) {
			next = make([]string, 0, len(prev))
			for _, id := range prev {
				if id != aid {
					next = append(next, id)
				}
			}
			changed = true
		}
	} else if !slices.Contains(prev, aid) {
		if len(prev) >= maxFriendBookmarks {
			store.AccountsMu.Unlock()
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		next = append(slices.Clone(prev), aid)
		changed = true
	}
	var err error
	if changed {
		acc.FriendBookmarks = next
		err = store.SaveLocked(nil, acc)
		if err != nil {
			acc.FriendBookmarks = prev
		}
	}
	store.AccountsMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":1}`)
}

func parseFriendRemoveIDs(raw []byte) ([]string, bool) {
	var items []json.RawMessage
	if _, err := httpx.UnmarshalNativeJSON(raw, &items); err != nil || items == nil || len(items) > 100 {
		return nil, false
	}
	aids := make([]string, 0, len(items))
	for _, elem := range items {
		trimmed := strings.TrimSpace(string(elem))
		if trimmed == "" || trimmed == "null" {
			return nil, false
		}
		var aid string
		if trimmed[0] == '"' {
			if err := json.Unmarshal(elem, &aid); err != nil {
				return nil, false
			}
		} else {
			var n json.Number
			if err := json.Unmarshal(elem, &n); err != nil {
				return nil, false
			}
			aid = n.String()
		}
		if !store.ValidAvatarID(aid) {
			return nil, false
		}
		aids = append(aids, aid)
	}
	return aids, true
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
	aids, ok := parseFriendRemoveIDs(raw)
	if !ok {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	removed := []string{}
	store.SocialMu.Lock()
	prevRemoved, prevMarked := store.FriendRemoved, store.FriendBookmarked
	prevFriendships := append([]store.Friendship(nil), store.Friendships...)
	var changed [][2]string
	scalarsChanged := false
	for _, aid := range aids {
		if i := store.FriendshipIndex(actor.Aid, aid, ""); i >= 0 {
			if store.Friendships[i].State != "removed" {
				store.Friendships[i].State = "removed"
				changed = append(changed, [2]string{actor.Aid, aid})
			}
			if !slices.Contains(removed, aid) {
				removed = append(removed, aid)
			}
		}
		if aid == store.FriendAID {
			if !syntheticFriendEnabled() {
				if !slices.Contains(removed, store.FriendAID) {
					removed = append(removed, store.FriendAID)
				}
			} else if !store.FriendRemoved {
				store.FriendRemoved = true
				store.FriendBookmarked = false
				removed = append(removed, store.FriendAID)
				scalarsChanged = true
			}
		}
	}
	if len(changed) > 0 || scalarsChanged {
		err = store.SaveSocialLocked(store.SocialChange{Friends: changed, Scalars: scalarsChanged})
		if err != nil {
			store.FriendRemoved, store.FriendBookmarked = prevRemoved, prevMarked
			store.Friendships = prevFriendships
			removed = nil
		}
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	if len(changed) > 0 {
		dropBookmarkIDs(actor.Aid, changed)
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

func dropBookmarkIDs(actorAid string, changed [][2]string) {
	dropForActor := make(map[string]bool, len(changed))
	for _, pair := range changed {
		dropForActor[pair[1]] = true
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	type savedPrev struct {
		acc  *store.Account
		prev []string
	}
	var dirty []*store.Account
	var undo []savedPrev
	prune := func(acc *store.Account, match func(string) bool) {
		if acc == nil || len(acc.FriendBookmarks) == 0 {
			return
		}
		var next []string
		removed := false
		for _, id := range acc.FriendBookmarks {
			if match(id) {
				removed = true
				continue
			}
			next = append(next, id)
		}
		if !removed {
			return
		}
		undo = append(undo, savedPrev{acc: acc, prev: acc.FriendBookmarks})
		acc.FriendBookmarks = next
		dirty = append(dirty, acc)
	}
	prune(store.AccountByAidLocked(actorAid), func(id string) bool { return dropForActor[id] })
	seenPeer := make(map[string]bool, len(changed))
	for _, pair := range changed {
		peerAid := pair[1]
		if seenPeer[peerAid] {
			continue
		}
		seenPeer[peerAid] = true
		prune(store.AccountByAidLocked(peerAid), func(id string) bool { return id == actorAid })
	}
	if len(dirty) > 0 {
		if err := store.SaveLocked(nil, dirty...); err != nil {
			for _, u := range undo {
				u.acc.FriendBookmarks = u.prev
			}
		}
	}
}

func handleFriendDefer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	aid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v4/friend/defer/"), "/")
	if strings.Contains(aid, "/") || !store.ValidAvatarID(aid) {
		httpx.ServeNotFound(w)
		return
	}
	if _, err := httpx.ReadNativeBody(r); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var err error
	store.SocialMu.Lock()
	if i := store.FriendshipIndex(actor.Aid, aid, ""); i >= 0 &&
		store.Friendships[i].State == "pending" && store.Friendships[i].B == actor.Aid {
		prev := store.Friendships[i].State
		store.Friendships[i].State = "removed"
		err = store.SaveSocialLocked(store.SocialChange{Friends: [][2]string{{actor.Aid, aid}}})
		if err != nil {
			store.Friendships[i].State = prev
		}
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":1}`)
}

func handleDiaryCheckExist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v4/diary2/ext/checkExist/")
	if entryRest, ok := strings.CutPrefix(rest, "entry/"); ok {
		entryRest = strings.TrimSuffix(entryRest, "/")
		parts := strings.Split(entryRest, "/")
		if len(parts) != 2 || !store.ValidAvatarID(parts[0]) || parts[1] == "" || !httpx.IsAllDigits(parts[1]) {
			httpx.ServeNotFound(w)
			return
		}
		owner, diaryNo := parts[0], parts[1]
		viewer, viewerOK := store.AccountForRequest(r)
		store.SocialMu.Lock()
		post, found := findPostLocked(owner, diaryNo)
		visible := found && canViewDiaryPost(post, viewer, viewerOK)
		frndApplyYn := viewerOK && viewer.Aid != owner && friendRelation(viewer.Aid, true, owner) == relNone
		store.SocialMu.Unlock()
		if !visible {
			httpx.ServeNotFound(w)
			return
		}
		httpx.WriteObj(w, map[string]any{"result": map[string]any{"frndApplyYn": frndApplyYn}})
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || !store.ValidAvatarID(parts[0]) || !httpx.IsAllDigits(parts[1]) {
		httpx.ServeNotFound(w)
		return
	}
	if parts[1] == "0" {
		if parts[2] != "" || !store.KnownSocialAvatar(parts[0]) {
			httpx.ServeNotFound(w)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
		return
	}
	if parts[2] != "" && !httpx.IsAllDigits(parts[2]) {
		httpx.ServeNotFound(w)
		return
	}
	owner, diaryNo := parts[0], parts[1]
	if !store.KnownSocialAvatar(owner) {
		httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"10001"}`)
		return
	}
	viewer, viewerOK := store.AccountForRequest(r)
	store.SocialMu.Lock()
	post, found := findPostLocked(owner, diaryNo)
	store.SocialMu.Unlock()
	if !found {
		httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"200003"}`)
		return
	}
	if post.Blind || !canViewDiaryPost(post, viewer, viewerOK) {
		httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"200002"}`)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// diaryIntroTextOK accepts valid UTF-8 of at most 200 characters, allowing newlines (\n, \r)
// while rejecting other control characters.
func diaryIntroTextOK(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > 200 {
		return false
	}
	for _, c := range s {
		if c != '\n' && c != '\r' && unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// diaryIntroResult builds the SetDiaryIntroduceData result map for aid. Acquires and releases store.SocialMu.
func diaryIntroResult(aid, nickname string, pi store.ProfileImage, dp store.DiaryProfile, gs store.GardenStats, viewer store.AccountSnapshot, viewerOK bool) map[string]any {
	store.SocialMu.Lock()
	count := 0
	for _, post := range store.DiaryPosts {
		if post.AvatarID == aid && canViewDiaryPost(post, viewer, viewerOK) {
			count++
		}
	}
	store.SocialMu.Unlock()
	own := viewerOK && viewer.Aid == aid
	shown := func(value string, secret bool) string {
		if secret && !own {
			return ""
		}
		return value
	}
	diaryName := dp.DiaryName
	if diaryName == "" {
		diaryName = nickname
	}
	var todayVisitor int64
	if gs.TodayDay == economy.AttendDayKey(economy.NowFunc()) {
		todayVisitor = gs.TodayVisits
	}
	img, _, bg := pi.Fields()
	return map[string]any{
		"avatarId": aid, "nickname": nickname, "diaryName": diaryName,
		"introduce": dp.Introduce, "skinId": dp.SkinID,
		"birthDay": shown(dp.BirthDay, dp.BirthDaySecret), "birthDaySecret": dp.BirthDaySecret,
		"sex": shown(dp.Sex, dp.SexSecret), "sexSecret": dp.SexSecret,
		"bloodType": shown(dp.BloodType, dp.BloodTypeSecret), "bloodTypeSecret": dp.BloodTypeSecret,
		"job": shown(dp.Job, dp.JobSecret), "jobSecret": dp.JobSecret,
		"created": true, "diaryCount": count, "todayVisitor": todayVisitor, "totalVisitor": gs.Footprints,
		"profile": img, "bg": bg, "obsProfileImagePath": img,
		"showGuestBook": true, "showCmt": true, "useCmtTagBrandAvatarOnly": false, "showProfile": true,
	}
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
	var pi store.ProfileImage
	var dp store.DiaryProfile
	var gs store.GardenStats
	if aid == store.FriendAID {
		nickname = store.FriendName()
	} else if acc, ok := store.AccountByAvatarID(aid); ok {
		if acc.Name != "" {
			nickname = acc.Name
		}
		pi = acc.ProfileImage
		if acc.DiaryProfile != nil {
			dp = *acc.DiaryProfile
		}
		gs = acc.GardenStats
	}
	body, err := json.Marshal(map[string]any{"result": diaryIntroResult(aid, nickname, pi, dp, gs, viewer, viewerOK)})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func handleDiaryIntroSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req struct {
		AvatarID        string `json:"avatarId"`
		DiaryName       string `json:"diaryName"`
		Introduce       string `json:"introduce"`
		SkinID          string `json:"skinId"`
		BirthDay        string `json:"birthDay"`
		BirthDaySecret  bool   `json:"birthDaySecret"`
		BloodType       string `json:"bloodType"`
		BloodTypeSecret bool   `json:"bloodTypeSecret"`
		Job             string `json:"job"`
		JobSecret       bool   `json:"jobSecret"`
		Sex             string `json:"sex"`
		SexSecret       bool   `json:"sexSecret"`
	}
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	if err != nil || !profileTextOK(req.DiaryName, 64) || !diaryIntroTextOK(req.Introduce) ||
		!diaryProfileText(req.SkinID) || !diaryProfileText(req.BirthDay) ||
		!diaryProfileText(req.BloodType) || !diaryProfileText(req.Job) || !diaryProfileText(req.Sex) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	store.AccountsMu.Lock()
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	if req.AvatarID != "" && req.AvatarID != acc.Aid {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	previous := acc.DiaryProfile
	next := store.DiaryProfile{}
	if previous != nil {
		next = *previous
	}
	next.DiaryName = req.DiaryName
	next.Introduce = req.Introduce
	next.SkinID = req.SkinID
	next.BirthDay = req.BirthDay
	next.BirthDaySecret = req.BirthDaySecret
	next.BloodType = req.BloodType
	next.BloodTypeSecret = req.BloodTypeSecret
	next.Job = req.Job
	next.JobSecret = req.JobSecret
	next.Sex = req.Sex
	next.SexSecret = req.SexSecret
	acc.DiaryProfile = &next
	if err := store.SaveLocked(nil, acc); err != nil {
		acc.DiaryProfile = previous
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	aid := acc.Aid
	nickname := acc.Name
	if nickname == "" {
		nickname = "guest"
	}
	pi := acc.ProfileImage
	dp := next
	gs := acc.GardenStats
	store.AccountsMu.Unlock()

	result := diaryIntroResult(aid, nickname, pi, dp, gs, store.AccountSnapshot{Aid: aid}, true)
	body, err := json.Marshal(map[string]any{"result": result})
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
	lastData := len(items) <= size
	if len(items) > size {
		items = items[:size]
	}
	for i := range items {
		items[i].CommentCount, items[i].LikeCount, items[i].MyLikeType = diaryCountsLocked(items[i].AvatarID, items[i].DiaryNo, viewer.Aid)
	}
	store.SocialMu.Unlock()
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
	lastData := len(items) <= size
	if len(items) > size {
		items = items[:size]
	}
	for i := range items {
		items[i].CommentCount, items[i].LikeCount, items[i].MyLikeType = diaryCountsLocked(items[i].AvatarID, items[i].DiaryNo, viewer.Aid)
	}
	store.SocialMu.Unlock()
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

// diaryPostDetail holds the snapshot gathered under SocialMu for diaryPostDetailMap.
type diaryPostDetail struct {
	post         store.DiaryPost
	comments     []store.DiaryComment
	commentsLast bool
	likes        []store.DiaryLike
	likesLast    bool
}

// diaryPostDetailLocked snapshots post, counts, and the first page of comments/likes.
// Caller holds store.SocialMu; build the JSON map with diaryPostDetailMap after unlocking
// because commentRows/likeRows acquire AccountsMu.
func diaryPostDetailLocked(post store.DiaryPost, viewerAid string) diaryPostDetail {
	store.NormalizeDiaryPost(&post)
	if post.Images == nil {
		post.Images = []store.DiaryImage{}
	}
	post.CommentCount, post.LikeCount, post.MyLikeType = diaryCountsLocked(post.AvatarID, post.DiaryNo, viewerAid)
	comments, commentsLast := commentPageLocked(post.AvatarID, post.DiaryNo, -1, diaryPageSize)
	likes, _, likesLast := likePageLocked(post.AvatarID, post.DiaryNo, -1, diaryPageSize)
	return diaryPostDetail{
		post: post, comments: comments, commentsLast: commentsLast,
		likes: likes, likesLast: likesLast,
	}
}

// diaryPostDetailMap builds the SetDiaryPostDetail result map. Never call under SocialMu.
func diaryPostDetailMap(d diaryPostDetail) map[string]any {
	post := d.post
	result := map[string]any{
		"diaryNo": post.DiaryNo, "avatarId": post.AvatarID, "nickname": post.Nickname,
		"regDate": post.RegDate, "title": post.Title, "openState": post.OpenState,
		"content": post.Content, "imageTypeKind": post.ImageTypeKind,
		"imageLocationType": post.ImageLocationType, "feelingState": post.FeelingState,
		"commentCount": post.CommentCount, "likeCount": post.LikeCount, "myLikeType": post.MyLikeType,
		"blind": post.Blind, "images": post.Images,
		"comments": map[string]any{"items": commentRows(d.comments), "lastData": d.commentsLast},
		"likes":    map[string]any{"items": likeRows(d.likes), "lastData": d.likesLast},
	}
	if post.AchieveType != "" {
		result["achieveType"], result["achieveTypeLevel"] = post.AchieveType, post.AchieveTypeLevel
		result["achieveDataType"], result["achieveData"] = post.AchieveDataType, post.AchieveData
		result["achieveLinkValue"], result["achieveDataDetail"] = post.AchieveLinkValue, post.AchieveDataDetail
	}
	return result
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
	post, found := findPostLocked(parts[0], parts[1])
	var detail diaryPostDetail
	visible := found && canViewDiaryPost(post, viewer, viewerOK)
	if visible {
		detail = diaryPostDetailLocked(post, viewer.Aid)
	}
	store.SocialMu.Unlock()
	if !visible {
		httpx.ServeNotFound(w)
		return
	}
	data, err := json.Marshal(map[string]any{"result": diaryPostDetailMap(detail)})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(data))
}

func handleDiaryModify(w http.ResponseWriter, r *http.Request) {
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
		DiaryNo       string             `json:"diaryNo"`
		AvatarID      string             `json:"avatarId"`
		Title         string             `json:"title"`
		Content       string             `json:"content"`
		ImageTypeKind string             `json:"imageTypeKind"`
		FeelingState  *int               `json:"feelingState"`
		OpenState     string             `json:"openState"`
		Images        []store.DiaryImage `json:"images"`
	}
	if _, err = httpx.UnmarshalNativeJSON(raw, &req); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if req.DiaryNo == "" || !httpx.IsAllDigits(req.DiaryNo) || req.AvatarID != actor.Aid ||
		len(req.Title) > 200 || len(req.Content) > 16000 || len(req.ImageTypeKind) > 64 || len(req.OpenState) > 32 || len(req.Images) > 20 ||
		!utf8.ValidString(req.Title) || !utf8.ValidString(req.Content) || !utf8.ValidString(req.ImageTypeKind) || !utf8.ValidString(req.OpenState) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	req.Images = slices.DeleteFunc(req.Images, func(image store.DiaryImage) bool { return image.ImageURL == "" })
	for _, image := range req.Images {
		if !store.ValidDiaryImage(image) || !store.MediaOwnedBy(image.ImageURL, actor.Aid) {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
	}
	openState := req.OpenState
	if openState == "" {
		openState = "public"
	}
	images := req.Images
	if images == nil {
		images = []store.DiaryImage{}
	}
	updated, found, err := store.ModifyDiaryPost(actor.Aid, req.DiaryNo, func(post *store.DiaryPost) {
		post.Title = req.Title
		post.Content = req.Content
		post.OpenState = openState
		post.Images = images
		post.ImageLocationType = store.DiaryImageLocationType(images)
		if req.FeelingState != nil {
			post.FeelingState = *req.FeelingState
		}
		if req.ImageTypeKind != "" {
			post.ImageTypeKind = req.ImageTypeKind
		}
	})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	if !found {
		httpx.ServeNotFound(w)
		return
	}
	store.SocialMu.Lock()
	detail := diaryPostDetailLocked(updated, actor.Aid)
	store.SocialMu.Unlock()
	data, err := json.Marshal(map[string]any{"result": diaryPostDetailMap(detail)})
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
	if _, err = httpx.UnmarshalNativeJSON(raw, &req); err != nil {
		log.Printf("cherry: diary write reject aid=%s bytes=%d err=%v", actor.Aid, len(raw), err)
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if req.AvatarID == "" || req.AvatarID != actor.Aid || len(req.Title) > 200 || len(req.Content) > 16000 || len(req.ImageTypeKind) > 64 || len(req.OpenState) > 32 || len(req.Images) > 20 || !utf8.ValidString(req.Title) || !utf8.ValidString(req.Content) || !utf8.ValidString(req.ImageTypeKind) {
		log.Printf("cherry: diary write reject aid=%s avatarId=%q title=%d content=%d kind=%q open=%q images=%d", actor.Aid, req.AvatarID, len(req.Title), len(req.Content), req.ImageTypeKind, req.OpenState, len(req.Images))
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	// A text-only post still carries images:[{"imageUrl":""}] (runtime 2026-10-11); drop empty rows.
	req.Images = slices.DeleteFunc(req.Images, func(image store.DiaryImage) bool { return image.ImageURL == "" })
	for _, image := range req.Images {
		// A picture must be one the poster uploaded (its row names the poster): a modified client may name anyone's URL.
		if !store.ValidDiaryImage(image) || !store.MediaOwnedBy(image.ImageURL, actor.Aid) {
			log.Printf("cherry: diary write reject aid=%s invalid or foreign image", actor.Aid)
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
	post := store.DiaryPost{
		AvatarID: req.AvatarID, Nickname: nickname,
		RegDate: strconv.FormatInt(time.Now().UnixMilli(), 10), Title: req.Title, OpenState: openState,
		Content: req.Content, ImageTypeKind: req.ImageTypeKind,
		ImageLocationType: store.DiaryImageLocationType(images), FeelingState: feeling, Images: images,
	}
	store.SocialMu.Lock()
	full := capsOn() && postsToday(actor.Aid, todayKey()) >= maxPostsPerDay
	stored := !full && insertDiaryPostLocked(&post)
	store.SocialMu.Unlock()
	if full {
		log.Printf("cherry: diary post refused aid=%s: daily limit", actor.Aid)
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if !stored {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	body, err := json.Marshal(map[string]any{"result": post})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	markDiaryQuest(actor.Aid)
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// insertDiaryPostLocked numbers post, puts it first and saves. A failed save restores the list and the
// number. The caller holds SocialMu; false means the number space is full or the save failed.
func insertDiaryPostLocked(post *store.DiaryPost) bool {
	if store.NextDiaryNo == ^uint64(0) {
		return false
	}
	store.NextDiaryNo++
	post.DiaryNo = strconv.FormatUint(store.NextDiaryNo, 10)
	store.DiaryPosts = append([]store.DiaryPost{*post}, store.DiaryPosts...)
	if err := store.SaveSocialLocked(store.SocialChange{Posts: []string{post.DiaryNo}, Scalars: true}); err != nil {
		store.DiaryPosts = store.DiaryPosts[1:]
		store.NextDiaryNo--
		return false
	}
	return true
}

var (
	// achieveTypes are the kAchieveTypeCode names with a caller of RequestShareAchievement in the client
	// (achieve-share-spec.md); a name outside them draws an empty diary cell. Only spins fill the other fields.
	achieveTypes = []string{"gacha", "spin", "jackpotspin", "minipet", "minipet_levelup", "fashion_request", "fashion_complete",
		"remodeling", "fashion_brand", "item_composite", "super_upgrade", "storygacha_item", "factory_cmplt_rwrd", "factory_craft_end",
		"cf_request", "cf_brand", "cf_complete", "colorshop_complete"}
	achieveTypeLevels = []string{"", "N", "N2", "N3", "R", "S", "U", "E", "C", "P"}
	achieveDataTypes  = []string{"", "item", "heart", "gem", "cash"}
)

// handleAchieveWrite is POST /v4/achieve/write, the Share to Diary request for a spin result
// (client ReqDiaryAchieveWriteURL 0x1bd20c4). Each request stores a new public diary post; a repeated
// send of the same result stores another post. The reply {"result":{"diaryNo":"<n>"}} is all the client reads.
//
// Row shape the client's parsers read (libgame.so arm64, 10.1.0.0; unfold items[] and look result use the same keys):
//   - unfold items[]: NaDiaryData::SetDiaryPostMainListData 0x1e8dd94; look result: NaDiaryData::SetDiaryPostDetail 0x1e925a8.
//   - diaryNo: non-empty string, else the unfold list skips the row. avatarId, nickname, regDate, title, openState,
//     content: strings. feelingState, commentCount, likeCount, myLikeType: ints. blind: bool.
//   - imageTypeKind "achieve": exact match to index 5 of the 17-entry image type table (sub_1505E28). Only this key
//     selects the achieve cell (NaDiaryPostAchieveContentCell, tableCellCreate type 6 in the list; type 20 in the detail):
//     NaDiaryMainView::AddTableViewDataForMain 0x1d78394, NaDiaryPostDetailView::InitTableViewData 0x1e5e704.
//   - achieveType "spin" (index 2) or "jackpotspin" (index 5) of kAchieveTypeCode: the index picks the background art
//     (InitBgImage 0x1e537c8). An unknown name gives -1 and the cell draws no achieve content.
//   - achieveTypeLevel "N","N2","N3","R","S","U","E","C","P": grade icon via NaRareManager::ConvertStringToRareType
//     0x20f9fc4 (R=1 ... P=6, others no icon). achieveDataType "item","heart","gem","cash" (kAchieveDataTypeCode 1-4);
//     "" is -1 and draws no grade icon.
//   - title (drawn) = the client's achieveDataDetail (locale title, e.g. "Lucky Spin"); content "" (the client sends no prize text).
//   - images []: no picture. The list picture key (images[0]) is empty and SetDPImage returns without a sprite (0x1d69ecc).
//   - achieveData (cash amount), achieveLinkValue (rank): strings, stored and returned; not drawn for spin.
//     achieveLink is omitted, so the list link button stays hidden.
func handleAchieveWrite(w http.ResponseWriter, r *http.Request) {
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
		AvatarID          string `json:"avatarId"`
		AchieveType       string `json:"achieveType"`
		AchieveTypeLevel  string `json:"achieveTypeLevel"`
		AchieveDataType   string `json:"achieveDataType"`
		AchieveData       string `json:"achieveData"`
		AchieveLinkValue  string `json:"achieveLinkValue"`
		AchieveDataDetail string `json:"achieveDataDetail"`
	}
	if _, err = httpx.UnmarshalNativeJSON(raw, &req); err != nil {
		log.Printf("cherry: achieve write reject aid=%s bytes=%d err=%v", actor.Aid, len(raw), err)
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if req.AvatarID != actor.Aid || !slices.Contains(achieveTypes, req.AchieveType) || !slices.Contains(achieveTypeLevels, req.AchieveTypeLevel) || !slices.Contains(achieveDataTypes, req.AchieveDataType) || len(req.AchieveData) > 32 || len(req.AchieveLinkValue) > 32 || len(req.AchieveDataDetail) > 200 || !utf8.ValidString(req.AchieveData) || !utf8.ValidString(req.AchieveDataDetail) {
		log.Printf("cherry: achieve write reject aid=%s avatarId=%q type=%q level=%q data=%q", actor.Aid, req.AvatarID, req.AchieveType, req.AchieveTypeLevel, req.AchieveDataType)
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	// The dye result sends the colours ("#E53935_#FFFFFF") as its detail, and the diary cell draws the detail as its title
	// (device 2026-10-31): the garment's name reads better. The craft result sends no detail at all, which leaves the post
	// untitled (device 2026-10-31): name the item, by its Atelier design when one makes it.
	if req.AchieveDataType == "item" && req.AchieveData != "" && (req.AchieveType == "colorshop_complete" || req.AchieveDataDetail == "") {
		req.AchieveDataDetail = economy.ActiveAtelier().ClothesName(req.AchieveData)
		if req.AchieveDataDetail == "" {
			req.AchieveDataDetail = economy.ItemName(req.AchieveData)
		}
	}
	log.Printf("cherry: achieve write aid=%s type=%q level=%q dataType=%q data=%q link=%q detail=%q", actor.Aid, req.AchieveType, req.AchieveTypeLevel, req.AchieveDataType, req.AchieveData, req.AchieveLinkValue, req.AchieveDataDetail)
	nickname := actor.Name
	if nickname == "" {
		nickname = "guest"
	}
	post := store.DiaryPost{
		AvatarID: actor.Aid, Nickname: nickname,
		RegDate: strconv.FormatInt(time.Now().UnixMilli(), 10), Title: req.AchieveDataDetail, OpenState: "public",
		ImageTypeKind: "achieve", ImageLocationType: "none", FeelingState: -1, Images: []store.DiaryImage{},
		AchieveType: req.AchieveType, AchieveTypeLevel: req.AchieveTypeLevel, AchieveDataType: req.AchieveDataType,
		AchieveData: req.AchieveData, AchieveLinkValue: req.AchieveLinkValue, AchieveDataDetail: req.AchieveDataDetail,
	}
	store.SocialMu.Lock()
	full := capsOn() && postsToday(actor.Aid, todayKey()) >= maxPostsPerDay
	stored := !full && insertDiaryPostLocked(&post)
	store.SocialMu.Unlock()
	if full {
		log.Printf("cherry: diary post refused aid=%s: daily limit", actor.Aid)
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if !stored {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	body, err := json.Marshal(map[string]any{"result": map[string]string{"diaryNo": post.DiaryNo}})
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
	// The post, its comments and likes, and the pictures only it named go in one save (store.EraseDiaryPost).
	if err := store.EraseDiaryPost(actor.Aid, diaryNo); err != nil {
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

	var items []friendSearchItem
	if syntheticFriendEnabled() {
		status := "1"
		store.SocialMu.Lock()
		removed := store.FriendRemoved
		store.SocialMu.Unlock()
		if removed {
			status = strconv.Itoa(relNone)
		}
		items = append(items, friendSearchItem{AvatarID: store.FriendAID, CaricNickName: store.FriendName(), FriendStatus: status})
	}
	store.AccountsMu.Lock()
	byID := make(map[string]friendSearchItem, store.AccountCountLocked())
	for _, acc := range store.AllAccountsLocked() {
		if acc == nil || !store.ValidAvatarID(acc.Aid) || acc.Aid == "0" || acc.Aid == store.FriendAID {
			continue
		}
		img, wb, bg := acc.ProfileImage.Fields()
		candidate := friendSearchItem{
			AvatarID: acc.Aid, CaricNickName: acc.Name, FriendStatus: strconv.Itoa(relNone),
			ProfileURL: img, ObsProfileImagePath: img, WholeBodyProfileURL: wb, BG: bg,
		}
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
	sortStart := len(items)
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items[sortStart:], func(i, j int) bool { return items[sortStart+i].AvatarID < items[sortStart+j].AvatarID })
	matches := make([]friendSearchItem, 0, len(items))
	for _, item := range items {
		match := item.AvatarID == strings.ReplaceAll(query, "-", "") // the client groups a typed code: 1001 arrives as "10-01"
		if searchType == "NICK" {
			match = strings.Contains(strings.ToLower(item.CaricNickName), strings.ToLower(query))
		}
		if match {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		log.Printf("cherry: friend search no match type=%q query=%q country=%q bytes=%d", req.SearchType, req.CaricNickName, req.Country, len(raw))
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

func handleFriendRelationship(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/friend/relationship/"), "/")
	if !store.KnownSocialAvatar(aid) {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":{}}`)
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
	if !store.KnownFriendTarget(aid) || (!syntheticFriendEnabled() && aid == store.FriendAID) {
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
	setFriendship(w, actor.Aid, actor.Name, aid, false)
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
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/friend/accept/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 2 {
		aid, notiSn := parts[0], parts[1]
		if notiSn == "" || len(notiSn) > 32 || !httpx.IsAllDigits(notiSn) {
			httpx.ServeNotFound(w)
			return
		}
		raw, err := httpx.ReadNativeBody(r)
		var body any
		if err != nil {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		if _, err := httpx.UnmarshalNativeJSON(raw, &body); err != nil || body != nil {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		if !store.ValidAvatarID(aid) || !store.KnownFriendTarget(aid) {
			httpx.WriteJSON(w, http.StatusOK, `{"result":false,"errorCode":"19509"}`)
			return
		}
		if aid == store.FriendAID {
			if !syntheticFriendEnabled() {
				httpx.ServeNotFound(w)
				return
			}
			restoreFriendWithBody(w, `{"result":true}`)
			return
		}
		if aid == actor.Aid {
			httpx.WriteJSON(w, http.StatusOK, `{"result":false,"errorCode":"19406"}`)
			return
		}
		acceptFriendNews(w, actor.Aid, actor.Name, aid)
		return
	}
	if len(parts) != 1 || parts[0] == "" {
		httpx.ServeNotFound(w)
		return
	}
	aid := parts[0]
	raw, err := httpx.ReadNativeBody(r)
	var body any
	if !store.KnownFriendTarget(aid) || (!syntheticFriendEnabled() && aid == store.FriendAID) {
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
	setFriendship(w, actor.Aid, actor.Name, aid, true)
}

func acceptFriendNews(w http.ResponseWriter, actor, actorName, other string) {
	store.SocialMu.Lock()
	prev := append([]store.Friendship(nil), store.Friendships...)
	i := store.FriendshipIndex(actor, other, "")
	switch {
	case i >= 0 && store.Friendships[i].State == "accepted":
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, `{"result":false,"errorCode":"19505"}`)
		return
	case i >= 0 && store.Friendships[i].State == "pending" && store.Friendships[i].A == actor:
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, `{"result":false,"errorCode":"19508"}`)
		return
	case i < 0 || store.Friendships[i].State != "pending" || store.Friendships[i].B != actor:
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, `{"result":false,"errorCode":"19406"}`)
		return
	case capsOn() && friendsFullLocked(actor, other, true, i):
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, `{"result":false,"errorCode":"19406"}`)
		return
	}
	store.Friendships[i].State = "accepted"
	err := store.SaveSocialLocked(store.SocialChange{Friends: [][2]string{{actor, other}}})
	if err != nil {
		store.Friendships = prev
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	lpn.PushFriendNoti(other, actor, actorName, "FRA", 0, 1)
	store.AddNewsAlert(other, store.NewsAlert{
		Type:      "FRIEND_ACCEPT",
		At:        economy.NowFunc().UnixMilli(),
		Actor:     actor,
		ActorName: actorName,
	})
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// incomingFriendRequestsLocked counts incoming pending friend requests for aid. Caller holds store.SocialMu.
func incomingFriendRequestsLocked(aid string) int {
	if aid == "" || aid == "0" {
		return 0
	}
	n := 0
	for _, f := range store.Friendships {
		if f.State == "pending" && f.B == aid {
			n++
		}
	}
	return n
}

// setFriendship handles a real-account apply (accept=false) or accept (accept=true) by actor toward other.
func setFriendship(w http.ResponseWriter, actor, actorName, other string, accept bool) {
	store.SocialMu.Lock()
	prev := append([]store.Friendship(nil), store.Friendships...)
	i := store.FriendshipIndex(actor, other, "")
	if capsOn() && friendsFullLocked(actor, other, accept, i) {
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var notifyType string
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
		notifyType = "FRR"
	case store.Friendships[i].State == "removed":
		store.Friendships[i] = store.Friendship{A: actor, B: other, State: "pending"}
		notifyType = "FRR"
	case store.Friendships[i].State == "pending" && store.Friendships[i].A == other:
		store.Friendships[i].State = "accepted"
		notifyType = "FRA"
	}
	err := store.SaveSocialLocked(store.SocialChange{Friends: [][2]string{{actor, other}}})
	if err != nil {
		store.Friendships = prev
	}
	ifrd := 0
	if err == nil && notifyType == "FRR" {
		ifrd = incomingFriendRequestsLocked(other)
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	switch notifyType {
	case "FRR":
		lpn.PushFriendNoti(other, actor, actorName, "FRR", ifrd, 0)
		store.AddNewsAlert(other, store.NewsAlert{
			Type:      "FRIEND_RECEIVE",
			At:        economy.NowFunc().UnixMilli(),
			Actor:     actor,
			ActorName: actorName,
		})
	case "FRA":
		lpn.PushFriendNoti(other, actor, actorName, "FRA", 0, 1)
		store.AddNewsAlert(other, store.NewsAlert{
			Type:      "FRIEND_ACCEPT",
			At:        economy.NowFunc().UnixMilli(),
			Actor:     actor,
			ActorName: actorName,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":1}`)
}

func restoreFriend(w http.ResponseWriter) {
	restoreFriendWithBody(w, `{"result":1}`)
}

func restoreFriendWithBody(w http.ResponseWriter, replyBody string) {
	store.SocialMu.Lock()
	prevRemoved, prevBookmarked := store.FriendRemoved, store.FriendBookmarked
	store.FriendRemoved = false
	err := store.SaveSocialLocked(store.SocialChange{Scalars: true})
	if err != nil {
		store.FriendRemoved, store.FriendBookmarked = prevRemoved, prevBookmarked
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, replyBody)
}

// markDiaryQuest completes the diary daily quest; the notice shows on the next Garden entry.
func markDiaryQuest(aid string) {
	if n, err := strconv.ParseUint(aid, 10, 64); err == nil {
		lpn.MarkQuest(n, "diary")
	}
}

type diarySkinEntry struct {
	SkinSn         int    `json:"skinSn"`
	SkinID         string `json:"skinId"`
	SkinCategoryID string `json:"skinCategoryId"`
	ResourceNumber int    `json:"resourceNumber"`
	Flag           int    `json:"flag"`
	Sort           int    `json:"sort"`
	HasSkin        bool   `json:"hasSkin"`
	SkinNm         string `json:"skin_nm"`
	Version        int    `json:"version"`
}

var diarySkins = []diarySkinEntry{
	{SkinSn: 1, SkinID: "default", SkinCategoryID: "default", ResourceNumber: 1, Flag: 0, Sort: 1, HasSkin: true, SkinNm: "default", Version: 7},
	{SkinSn: 2, SkinID: "00002", SkinCategoryID: "default", ResourceNumber: 2, Flag: 0, Sort: 2, HasSkin: true, SkinNm: "00002", Version: 7},
	{SkinSn: 3, SkinID: "00003", SkinCategoryID: "default", ResourceNumber: 3, Flag: 0, Sort: 3, HasSkin: true, SkinNm: "00003", Version: 7},
	{SkinSn: 4, SkinID: "00005", SkinCategoryID: "default", ResourceNumber: 5, Flag: 0, Sort: 4, HasSkin: true, SkinNm: "00005", Version: 7},
	{SkinSn: 5, SkinID: "00009", SkinCategoryID: "default", ResourceNumber: 9, Flag: 0, Sort: 5, HasSkin: true, SkinNm: "00009", Version: 7},
	{SkinSn: 6, SkinID: "00015", SkinCategoryID: "default", ResourceNumber: 15, Flag: 0, Sort: 6, HasSkin: true, SkinNm: "00015", Version: 7},
	{SkinSn: 7, SkinID: "00016", SkinCategoryID: "default", ResourceNumber: 16, Flag: 0, Sort: 7, HasSkin: true, SkinNm: "00016", Version: 7},
}

func handleDiarySkinList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v4/diary2/skinListWithVersionOS/")
	platform, ver, ok := strings.Cut(rest, "/")
	if !ok || (platform != "Android" && platform != "iPhone") || ver == "" || strings.Contains(ver, "/") {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteObj(w, map[string]any{
		"result": []any{
			diarySkins,
			map[string]int{"commonVersion": 7},
		},
	})
}

func handleDiarySkinInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	platform := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v4/diary2/skinInfo/"), "/")
	if platform != "Android" && platform != "iPhone" {
		httpx.ServeNotFound(w)
		return
	}
	id := r.URL.Query().Get("diary_skin_id")
	if id == "" {
		id = "default"
	}
	for _, entry := range diarySkins {
		if entry.SkinID == id {
			httpx.WriteObj(w, map[string]any{"result": entry})
			return
		}
	}
	httpx.ServeNotFound(w)
}
