package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// Synthetic friend lives outside the account allocator's range; it was "2"
	// until a real phone guest was also assigned aid 2 and got shadowed.
	friendAID      = "100000"
	friendName     = "Friend"
	saveFailedBody = `{"errorCode":"500","errorMessage":"cherry: save failed"}`
)

type diaryImage struct {
	ImageURL string `json:"imageUrl"`
}

type diaryPost struct {
	DiaryNo           string       `json:"diaryNo"`
	AvatarID          string       `json:"avatarId"`
	Nickname          string       `json:"nickname"`
	RegDate           string       `json:"regDate"`
	Title             string       `json:"title"`
	OpenState         string       `json:"openState"`
	Content           string       `json:"content"`
	ImageTypeKind     string       `json:"imageTypeKind"`
	ImageLocationType string       `json:"imageLocationType"`
	FeelingState      int          `json:"feelingState"`
	CommentCount      int          `json:"commentCount"`
	LikeCount         int          `json:"likeCount"`
	MyLikeType        int          `json:"myLikeType"`
	Blind             bool         `json:"blind"`
	Images            []diaryImage `json:"images"`
}

type savedSocial struct {
	Version          int                   `json:"version"`
	FriendRemoved    bool                  `json:"friendRemoved"`
	FriendBookmarked bool                  `json:"friendBookmarked"`
	NextDiaryNo      uint64                `json:"nextDiaryNo"`
	Posts            []diaryPost           `json:"posts"`
	NextGuestHistNo  uint64                `json:"nextGuestHistNo"`
	Guestbook        []savedGuestbookEntry `json:"guestbook"`
	Friendships      []friendship          `json:"friendships,omitempty"`
}

// friendship is one record per unordered pair of real accounts. "pending":
// A applied to B; "accepted": mutual; "removed": tombstone so both clients'
// never-pruned native caches get status -1.
type friendship struct {
	A     string `json:"a"`
	B     string `json:"b"`
	State string `json:"state"`
}

var (
	socialMu         sync.Mutex
	socialStorePath  string
	friendRemoved    bool
	friendBookmarked bool
	nextDiaryNo      uint64
	diaryPosts       []diaryPost
	nextGuestHistNo  uint64
	guestbookEntries []savedGuestbookEntry
	friendships      []friendship
)

func loadSocial() error {
	dir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	return loadSocialFrom(filepath.Join(dir, "Cherry", "social.json"))
}

func loadSocialFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := savedSocial{Version: 1, Posts: []diaryPost{}, Guestbook: []savedGuestbookEntry{}}
	if err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return err
		}
		if state.Version != 1 {
			return errors.New("invalid social store version")
		}
	}
	if state.Posts == nil {
		state.Posts = []diaryPost{}
	}
	for i := range state.Posts {
		normalizeDiaryPost(&state.Posts[i])
		post := state.Posts[i]
		seq, err := strconv.ParseUint(post.DiaryNo, 10, 64)
		if err == nil && seq > state.NextDiaryNo {
			state.NextDiaryNo = seq
		}
	}
	if state.Guestbook == nil {
		state.Guestbook = []savedGuestbookEntry{}
	}
	for i := range state.Guestbook {
		entry := &state.Guestbook[i]
		seq, err := strconv.ParseUint(entry.GuestHistNo, 10, 64)
		if err != nil || seq == 0 || !validAvatarID(entry.HostAvtNo) || !validAvatarID(entry.GuestAvtNo) {
			return errors.New("invalid guestbook entry")
		}
		if seq > state.NextGuestHistNo {
			state.NextGuestHistNo = seq
		}
		if entry.Reply == nil {
			entry.Reply = []any{}
		}
	}
	for _, f := range state.Friendships {
		if !validAvatarID(f.A) || !validAvatarID(f.B) || f.A == f.B || (f.State != "pending" && f.State != "accepted" && f.State != "removed") {
			return errors.New("invalid friendship")
		}
	}
	socialMu.Lock()
	socialStorePath = path
	friendships = state.Friendships
	friendRemoved = state.FriendRemoved
	friendBookmarked = state.FriendBookmarked
	nextDiaryNo = state.NextDiaryNo
	diaryPosts = state.Posts
	nextGuestHistNo = state.NextGuestHistNo
	guestbookEntries = state.Guestbook
	socialMu.Unlock()
	return nil
}

func resetSocial() {
	socialMu.Lock()
	socialStorePath = ""
	friendRemoved = false
	friendBookmarked = false
	nextDiaryNo = 0
	diaryPosts = nil
	nextGuestHistNo = 0
	guestbookEntries = nil
	friendships = nil
	socialMu.Unlock()
}

func saveSocialLocked() error {
	if socialStorePath == "" {
		return nil
	}
	posts := diaryPosts
	if posts == nil {
		posts = []diaryPost{}
	}
	guestbook := guestbookEntries
	if guestbook == nil {
		guestbook = []savedGuestbookEntry{}
	}
	data, err := json.Marshal(savedSocial{
		Version: 1, FriendRemoved: friendRemoved, FriendBookmarked: friendBookmarked,
		NextDiaryNo: nextDiaryNo, Posts: posts, NextGuestHistNo: nextGuestHistNo, Guestbook: guestbook,
		Friendships: friendships,
	})
	if err != nil {
		return err
	}
	dir := filepath.Dir(socialStorePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".social-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), socialStorePath)
}

func readNativeBody(r *http.Request) ([]byte, error) {
	body := io.Reader(r.Body)
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		body = zr
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxJSONBody+1))
	if err != nil || len(raw) > maxJSONBody {
		return nil, errors.New("bad body")
	}
	return raw, nil
}

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
		serveNotFound(w)
		return
	}
	actor, actorOK := accountForRequest(r)
	type relRow struct {
		aid    string
		status int
	}
	var rels []relRow
	socialMu.Lock()
	removed, marked := friendRemoved, friendBookmarked
	if actorOK {
		for _, f := range friendships {
			other := f.B
			if f.A != actor.aid && f.B != actor.aid {
				continue
			}
			if f.B == actor.aid {
				other = f.A
			}
			switch {
			case f.State == "accepted":
				rels = append(rels, relRow{other, 1})
			case f.State == "removed":
				rels = append(rels, relRow{other, -1})
			case f.State == "pending" && f.B == actor.aid:
				rels = append(rels, relRow{other, relReceived})
			case f.State == "pending":
				// Sent request: 3 lands in an unread per-state list (PushFriendData); harmless cache row.
				rels = append(rels, relRow{other, relSent})
			}
		}
	}
	socialMu.Unlock()
	// Native caches sync rows by avatarNo and never prunes omitted ones; see PLAN
	// "Stale friend-cache fix" before renumbering friendAID again.
	buddies := []buddyRow{}
	bookmarks := []string{}
	if !removed {
		// Native sorts by status: 1 is an accepted friend (My Friends); 0 lands in Received Requests.
		buddies = append(buddies, buddyRow{
			AvatarNo: friendAID, AvatarName: friendName, BuddyAvatarNo: friendAID,
			Status: 1, FriendStatus: 1, LineBuddyYn: "N", Mid: friendAID,
		})
	}
	for _, rel := range rels {
		name := rel.aid
		if acc, ok := accountByAvatarID(rel.aid); ok {
			name = acc.name
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
		bookmarks = []string{friendAID}
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
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(body))
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
	if aid == friendAID {
		if friendRemoved {
			return relNone
		}
		return relFriends
	}
	if !actorOK {
		return relNone
	}
	if i := friendshipIndex(actor, aid, ""); i >= 0 {
		switch f := friendships[i]; {
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
		serveNotFound(w)
		return
	}
	viewer, viewerOK := accountForRequest(r)
	rest := strings.TrimPrefix(r.URL.Path, "/v4/profile/")
	aid, _, _ := strings.Cut(rest, "/")
	if aid == friendAID {
		profileJSON(w, profileBodyFor(aid, friendName, true, viewer, viewerOK))
		return
	}
	if acc, ok := accountByAvatarID(aid); ok {
		profileJSON(w, profileBodyFor(aid, acc.name, true, viewer, viewerOK))
		return
	}
	writeJSON(w, http.StatusOK, profileBody)
}

func profileBodyFor(aid, name string, hasGarden bool, viewer accountSnapshot, viewerOK bool) map[string]any {
	socialMu.Lock()
	friendStatus := friendRelation(viewer.aid, viewerOK, aid)
	diaryCount, guestbookCount := 0, 0
	for _, post := range diaryPosts {
		if post.AvatarID == aid && canViewDiaryPost(post, viewer, viewerOK) {
			diaryCount++
		}
	}
	for _, entry := range guestbookEntries {
		if entry.HostAvtNo == aid {
			guestbookCount++
		}
	}
	socialMu.Unlock()
	return map[string]any{
		"avatarId": aid, "name": name, "nickname": name, "diaryName": name,
		"newbie": false, "friendStatus": friendStatus, "hasGarden": hasGarden,
		"showGuestBook": true, "showCmt": true, "showProfile": true,
		"diaryCount": diaryCount, "guestBookCount": guestbookCount,
	}
}

func canViewDiaryPost(post diaryPost, viewer accountSnapshot, viewerOK bool) bool {
	return post.OpenState == "public" || (viewerOK && viewer.aid == post.AvatarID)
}

func diaryImageLocationType(images []diaryImage) string {
	if len(images) == 0 {
		return "none"
	}
	for _, image := range images {
		if !validDiaryImage(image) {
			return "none"
		}
	}
	return "obs"
}

func normalizeDiaryPost(post *diaryPost) {
	if post.ImageLocationType == "" {
		post.ImageLocationType = diaryImageLocationType(post.Images)
	}
}

func profileJSON(w http.ResponseWriter, body any) {
	data, err := json.Marshal(map[string]any{"result": body})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(data))
}

func handleFriendBookmark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	if _, ok := accountForRequest(r); !ok {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v4/friend/bookmark/")
	remove := false
	if cut, ok := strings.CutPrefix(rest, "remove/"); ok {
		remove = true
		rest = cut
	}
	aid := strings.Trim(rest, "/")
	if strings.Contains(aid, "/") || aid != friendAID {
		serveNotFound(w)
		return
	}
	socialMu.Lock()
	prev := friendBookmarked
	friendBookmarked = !remove && !friendRemoved
	err := saveSocialLocked()
	if err != nil {
		friendBookmarked = prev
	}
	socialMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, `{"result":1}`)
}

func handleFriendRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	actor, ok := accountForRequest(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	raw, err := readNativeBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	var aids []string
	if _, err = unmarshalNativeJSON(raw, &aids); err != nil || aids == nil || len(aids) > 100 {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	for _, aid := range aids {
		if !validAvatarID(aid) {
			writeJSON(w, http.StatusBadRequest, badRequestBody)
			return
		}
	}
	removed := []string{}
	socialMu.Lock()
	prevRemoved, prevMarked := friendRemoved, friendBookmarked
	prevFriendships := append([]friendship(nil), friendships...)
	for _, aid := range aids {
		if i := friendshipIndex(actor.aid, aid, ""); i >= 0 && friendships[i].State != "removed" {
			friendships[i].State = "removed"
			removed = append(removed, aid)
		}
		if aid == friendAID && !friendRemoved {
			friendRemoved = true
			friendBookmarked = false
			removed = append(removed, friendAID)
		}
	}
	err = saveSocialLocked()
	if err != nil {
		friendRemoved, friendBookmarked = prevRemoved, prevMarked
		friendships = prevFriendships
		removed = nil
	}
	socialMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	if removed == nil {
		removed = []string{}
	}
	body, err := json.Marshal(map[string]any{"result": map[string]any{"success": removed}})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(body))
}

func handleDiaryCheckExist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v4/diary2/ext/checkExist/")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[2] != "" || !validAvatarID(parts[0]) || parts[1] != "0" || !knownSocialAvatar(parts[0]) {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, `{"result":true}`)
}

func handleDiaryIntro(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	viewer, viewerOK := accountForRequest(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/intro/"), "/")
	aid, _, _ := strings.Cut(rest, "/")
	if aid == "" {
		aid = "1"
	}
	nickname := "guest"
	if aid == friendAID {
		nickname = friendName
	} else if acc, ok := accountByAvatarID(aid); ok && acc.name != "" {
		nickname = acc.name
	}
	socialMu.Lock()
	count := 0
	for _, post := range diaryPosts {
		if post.AvatarID == aid && canViewDiaryPost(post, viewer, viewerOK) {
			count++
		}
	}
	socialMu.Unlock()
	body, err := json.Marshal(map[string]any{"result": map[string]any{
		"avatarId": aid, "nickname": nickname, "diaryName": nickname, "diaryCount": count,
		"showGuestBook": true, "showCmt": true, "showProfile": true,
	}})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(body))
}

func handleDiaryUnfold(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	viewer, viewerOK := accountForRequest(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/ext/unfold/"), "/")
	aid, _, _ := strings.Cut(rest, "/")
	lastSeq, size, ok := cursorPage(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	socialMu.Lock()
	items := make([]diaryPost, 0)
	for _, post := range diaryPosts {
		if (aid != "" && post.AvatarID != aid) || !canViewDiaryPost(post, viewer, viewerOK) {
			continue
		}
		seq, err := strconv.ParseUint(post.DiaryNo, 10, 64)
		if err != nil || (lastSeq >= 0 && seq >= uint64(lastSeq)) {
			continue
		}
		normalizeDiaryPost(&post)
		if post.Images == nil {
			post.Images = []diaryImage{}
		}
		items = append(items, post)
	}
	socialMu.Unlock()
	lastData := len(items) <= size
	if len(items) > size {
		items = items[:size]
	}
	body, err := json.Marshal(map[string]any{"result": map[string]any{"lastData": lastData, "items": items}})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(body))
}

func handleDiaryPhotoAlbum(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	viewer, viewerOK := accountForRequest(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/ext/unfold/photo/"), "/")
	if !validAvatarID(rest) {
		serveNotFound(w)
		return
	}
	lastSeq, size, ok := cursorPage(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	socialMu.Lock()
	items := make([]diaryPost, 0)
	for _, post := range diaryPosts {
		if post.AvatarID != rest || len(post.Images) == 0 || !canViewDiaryPost(post, viewer, viewerOK) {
			continue
		}
		seq, err := strconv.ParseUint(post.DiaryNo, 10, 64)
		if err != nil || (lastSeq >= 0 && seq >= uint64(lastSeq)) {
			continue
		}
		normalizeDiaryPost(&post)
		items = append(items, post)
	}
	socialMu.Unlock()
	lastData := len(items) <= size
	if len(items) > size {
		items = items[:size]
	}
	body, err := json.Marshal(map[string]any{"result": map[string]any{"lastData": lastData, "items": items}})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(body))
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
		serveNotFound(w)
		return
	}
	viewer, viewerOK := accountForRequest(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/ext/look/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || !validAvatarID(parts[0]) || !validAvatarID(parts[1]) {
		serveNotFound(w)
		return
	}
	socialMu.Lock()
	var found *diaryPost
	for i := range diaryPosts {
		if diaryPosts[i].AvatarID == parts[0] && diaryPosts[i].DiaryNo == parts[1] && canViewDiaryPost(diaryPosts[i], viewer, viewerOK) {
			post := diaryPosts[i]
			normalizeDiaryPost(&post)
			if post.Images == nil {
				post.Images = []diaryImage{}
			}
			found = &post
			break
		}
	}
	socialMu.Unlock()
	if found == nil {
		serveNotFound(w)
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
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(data))
}

func handleDiaryWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	actor, ok := accountForRequest(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	raw, err := readNativeBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	var req struct {
		AvatarID      string       `json:"avatarId"`
		Title         string       `json:"title"`
		Content       string       `json:"content"`
		ImageTypeKind string       `json:"imageTypeKind"`
		FeelingState  *int         `json:"feelingState"`
		OpenState     string       `json:"openState"`
		Images        []diaryImage `json:"images"`
	}
	if _, err = unmarshalNativeJSON(raw, &req); err != nil || req.AvatarID == "" || req.AvatarID != actor.aid || len(req.Title) > 200 || len(req.Content) > 16000 || len(req.ImageTypeKind) > 64 || len(req.OpenState) > 32 || len(req.Images) > 20 || !utf8.ValidString(req.Title) || !utf8.ValidString(req.Content) || !utf8.ValidString(req.ImageTypeKind) {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	for _, image := range req.Images {
		if !validDiaryImage(image) {
			writeJSON(w, http.StatusBadRequest, badRequestBody)
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
		images = []diaryImage{}
	}
	nickname := actor.name
	if nickname == "" {
		nickname = "guest"
	}
	socialMu.Lock()
	if nextDiaryNo == ^uint64(0) {
		socialMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	nextDiaryNo++
	post := diaryPost{
		DiaryNo: strconv.FormatUint(nextDiaryNo, 10), AvatarID: req.AvatarID, Nickname: nickname,
		RegDate: strconv.FormatInt(time.Now().UnixMilli(), 10), Title: req.Title, OpenState: openState,
		Content: req.Content, ImageTypeKind: req.ImageTypeKind,
		ImageLocationType: diaryImageLocationType(images), FeelingState: feeling, Images: images,
	}
	diaryPosts = append([]diaryPost{post}, diaryPosts...)
	err = saveSocialLocked()
	if err != nil {
		diaryPosts = diaryPosts[1:]
		nextDiaryNo--
	}
	socialMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	body, err := json.Marshal(map[string]any{"result": post})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(body))
}

func handleDiaryErase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	actor, ok := accountForRequest(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/erase/"), "/")
	diaryNo, _, _ := strings.Cut(rest, "/")
	socialMu.Lock()
	prev := append([]diaryPost(nil), diaryPosts...)
	kept := diaryPosts[:0]
	removed := false
	for _, post := range diaryPosts {
		if post.DiaryNo == diaryNo && post.AvatarID == actor.aid {
			removed = true
			continue
		}
		kept = append(kept, post)
	}
	if kept == nil {
		kept = []diaryPost{}
	}
	diaryPosts = kept
	var err error
	if removed {
		err = saveSocialLocked()
	}
	if err != nil {
		diaryPosts = prev
	}
	socialMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, `{"result":true}`)
}

func validDiaryImage(image diaryImage) bool {
	if len(image.ImageURL) == 0 || len(image.ImageURL) > 512 || !utf8.ValidString(image.ImageURL) || strings.ContainsAny(image.ImageURL, "\\\r\n#") {
		return false
	}
	u, err := url.Parse(image.ImageURL)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" {
		return false
	}
	variantPath := u.Path
	if !strings.HasPrefix(variantPath, "/") {
		variantPath = "/" + variantPath
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) < 3 || len(query) > 4 {
		return false
	}
	for key := range query {
		if key != "oid" && key != "ctime" && key != "userid" && key != "tid" {
			return false
		}
	}
	userID, userOK := oneDiaryMediaQueryValue(query, "userid")
	ctime, ctimeOK := oneDiaryMediaQueryValue(query, "ctime")
	oid, oidOK := oneDiaryMediaQueryValue(query, "oid")
	tid, tidOK := optionalDiaryMediaQueryValue(query, "tid")
	if !userOK || !ctimeOK || !oidOK || !tidOK || !validDiaryMediaTuple(userID, ctime, oid) {
		return false
	}
	return validDiaryMediaVariant(variantPath, tid)
}

func accountForRequest(r *http.Request) (accountSnapshot, bool) {
	token := cookieValue(r, "AV_AUTH")
	if token == "" {
		return accountSnapshot{}, false
	}
	accountsMu.Lock()
	acc := accounts[token]
	if acc == nil || !validAvatarID(acc.aid) || acc.aid == "0" {
		accountsMu.Unlock()
		return accountSnapshot{}, false
	}
	snapshot := accountSnapshot{
		aid: acc.aid, name: acc.name, gender: acc.gender, skin: acc.skin,
		country: acc.country, itemCodes: append([]string(nil), acc.itemCodes...),
		inventoryCodes: append([]string(nil), accountInventoryCodes(acc)...),
	}
	accountsMu.Unlock()
	return snapshot, true
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
		serveNotFound(w)
		return
	}
	raw, err := readNativeBody(r)
	var req friendSearchRequest
	if err != nil || func() bool { _, e := unmarshalNativeJSON(raw, &req); return e != nil }() || strings.TrimSpace(req.Country) == "" || len(req.Country) > 8 || len(req.CaricNickName) > 128 {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	searchType := strings.ToUpper(strings.TrimSpace(req.SearchType))
	query := strings.TrimSpace(req.CaricNickName)
	if (searchType != "CODE" && searchType != "NICK") || query == "" {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	page, size, ok := requestPage(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}

	items := []friendSearchItem{{AvatarID: friendAID, CaricNickName: friendName, FriendStatus: "1"}}
	socialMu.Lock()
	removed := friendRemoved
	socialMu.Unlock()
	if removed {
		items[0].FriendStatus = strconv.Itoa(relNone)
	}
	accountsMu.Lock()
	byID := make(map[string]friendSearchItem, len(accounts))
	for _, acc := range accounts {
		if acc == nil || !validAvatarID(acc.aid) || acc.aid == "0" || acc.aid == friendAID {
			continue
		}
		candidate := friendSearchItem{AvatarID: acc.aid, CaricNickName: acc.name, FriendStatus: strconv.Itoa(relNone)}
		if current, exists := byID[acc.aid]; exists && current.CaricNickName <= candidate.CaricNickName {
			continue
		}
		byID[acc.aid] = candidate
	}
	accountsMu.Unlock()
	searcher, searcherOK := accountForRequest(r)
	socialMu.Lock()
	for aid, item := range byID {
		if searcherOK {
			item.FriendStatus = strconv.Itoa(friendRelation(searcher.aid, true, aid))
			byID[aid] = item
		}
	}
	socialMu.Unlock()
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
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(body))
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
		serveNotFound(w)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/friend/status/"), "/")
	if !knownSocialAvatar(aid) {
		serveNotFound(w)
		return
	}
	actor, actorOK := accountForRequest(r)
	socialMu.Lock()
	status := friendRelation(actor.aid, actorOK, aid)
	socialMu.Unlock()
	body, _ := json.Marshal(map[string]int{"result": status})
	writeJSON(w, http.StatusOK, string(body))
}

func handleFriendApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	actor, ok := accountForRequest(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/friend/apply/"), "/")
	raw, err := readNativeBody(r)
	var req struct {
		ApplyAvatarID string `json:"applyAvatarId"`
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if _, err := unmarshalNativeJSON(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	// Native ReqFriendApplyWithAvatarID @0x1bcc6fc always POSTs /v4/friend/apply/ with an EMPTY path aid;
	// the target is only in the body.
	if aid == "" {
		aid = req.ApplyAvatarID
	}
	if !knownFriendTarget(aid) {
		serveNotFound(w)
		return
	}
	if req.ApplyAvatarID != aid {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if aid == friendAID {
		restoreFriend(w)
		return
	}
	if aid == actor.aid {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	setFriendship(w, actor.aid, aid, false)
}

func handleFriendAccept(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		serveNotFound(w)
		return
	}
	actor, ok := accountForRequest(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/friend/accept/"), "/")
	raw, err := readNativeBody(r)
	var body any
	if !knownFriendTarget(aid) {
		serveNotFound(w)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if _, err := unmarshalNativeJSON(raw, &body); err != nil || body != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if aid == friendAID {
		restoreFriend(w)
		return
	}
	setFriendship(w, actor.aid, aid, true)
}

func knownFriendTarget(aid string) bool {
	return aid == friendAID || (validAvatarID(aid) && knownSocialAvatar(aid))
}

// friendshipIndex finds the record for the unordered pair; state "" matches any. Caller holds socialMu.
func friendshipIndex(x, y, state string) int {
	for i, f := range friendships {
		if ((f.A == x && f.B == y) || (f.A == y && f.B == x)) && (state == "" || f.State == state) {
			return i
		}
	}
	return -1
}

// setFriendship handles a real-account apply (accept=false) or accept (accept=true) by actor toward other.
func setFriendship(w http.ResponseWriter, actor, other string, accept bool) {
	socialMu.Lock()
	prev := append([]friendship(nil), friendships...)
	i := friendshipIndex(actor, other, "")
	switch {
	case accept && (i < 0 || friendships[i].State != "pending" || friendships[i].B != actor):
		if i >= 0 && friendships[i].State == "accepted" {
			break // already friends: idempotent
		}
		socialMu.Unlock()
		serveNotFound(w)
		return
	case i < 0:
		friendships = append(friendships, friendship{A: actor, B: other, State: "pending"})
	case friendships[i].State == "removed":
		friendships[i] = friendship{A: actor, B: other, State: "pending"}
	case friendships[i].State == "pending" && friendships[i].A == other:
		friendships[i].State = "accepted"
	}
	err := saveSocialLocked()
	if err != nil {
		friendships = prev
	}
	socialMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, `{"result":1}`)
}

func knownSocialAvatar(aid string) bool {
	if aid == friendAID {
		return true
	}
	if !validAvatarID(aid) {
		return false
	}
	_, ok := accountByAvatarID(aid)
	return ok
}

func restoreFriend(w http.ResponseWriter) {
	socialMu.Lock()
	prevRemoved, prevBookmarked := friendRemoved, friendBookmarked
	friendRemoved = false
	err := saveSocialLocked()
	if err != nil {
		friendRemoved, friendBookmarked = prevRemoved, prevBookmarked
	}
	socialMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, `{"result":1}`)
}
