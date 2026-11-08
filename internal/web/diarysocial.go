package web

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Diary comments, likes and reports (client spec diary-social-spec.md sections 3 and 5). The counts and the viewer's
// own like type are computed per request from the stored lists; the count fields on a stored post are never trusted.

// diaryPageSize is the comment and like page size when the request sends no size (cursorPage defaults to 10).
const diaryPageSize = 20

// commentRow is one comment item (spec 3.3 "Comment item"). Profile and background are not set by Cherry.
type commentRow struct {
	CommentNo           string `json:"commentNo"`
	CommentAvatarID     string `json:"commentAvatarId"`
	CommentAvatarLocale string `json:"commentAvatarLocale"`
	Nickname            string `json:"nickname"`
	Content             string `json:"content"`
	Profile             string `json:"profile"`
	Bg                  string `json:"bg"`
	ObsProfileImagePath string `json:"obsProfileImagePath"`
	Blind               bool   `json:"blind"`
	Date                string `json:"date"`
}

// likeRow is one like item (spec 3.3 "Like item").
type likeRow struct {
	LikeNo              string `json:"likeNo"`
	LikeAvatarID        string `json:"likeAvatarId"`
	Nickname            string `json:"nickname"`
	LikeType            int    `json:"likeType"`
	Profile             string `json:"profile"`
	Bg                  string `json:"bg"`
	ObsProfileImagePath string `json:"obsProfileImagePath"`
}

func commentRowOf(c store.DiaryComment) commentRow {
	return commentRow{
		CommentNo: c.CommentNo, CommentAvatarID: c.CommentAvatarID, CommentAvatarLocale: c.CommentAvatarLocale,
		Nickname: c.Nickname, Content: c.Content, ObsProfileImagePath: profileImagePath(c.CommentAvatarID), Date: c.Date,
	}
}

// commentRows and likeRows always return a non-nil slice: the client indexes these arrays (spec 5.2).
func commentRows(items []store.DiaryComment) []commentRow {
	rows := make([]commentRow, 0, len(items))
	for _, c := range items {
		rows = append(rows, commentRowOf(c))
	}
	return rows
}

func likeRows(items []store.DiaryLike) []likeRow {
	rows := make([]likeRow, 0, len(items))
	for _, l := range items {
		rows = append(rows, likeRow{
			LikeNo: l.LikeNo, LikeAvatarID: l.LikeAvatarID, Nickname: l.Nickname, LikeType: l.LikeType,
			ObsProfileImagePath: profileImagePath(l.LikeAvatarID),
		})
	}
	return rows
}

// profileImagePath is the avatar's saved profile picture, "" when none. It takes AccountsMu: never call it under SocialMu.
func profileImagePath(aid string) string {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	for _, acc := range store.Accounts {
		if acc.Aid == aid {
			return acc.ProfileImage.Image
		}
	}
	return ""
}

// diaryPathParts splits the path after prefix into n segments. The first is an avatar id, the others digits.
func diaryPathParts(path, prefix string, n int) ([]string, bool) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, prefix), "/"), "/")
	if len(parts) != n || !store.ValidAvatarID(parts[0]) {
		return nil, false
	}
	for _, p := range parts[1:] {
		if !httpx.IsAllDigits(p) {
			return nil, false
		}
	}
	return parts, true
}

// splitIf returns the items drop does not reject, then the rejected ones. Both are non-nil.
func splitIf[T any](list []T, drop func(T) bool) (kept, dropped []T) {
	kept, dropped = []T{}, []T{}
	for _, v := range list {
		if drop(v) {
			dropped = append(dropped, v)
		} else {
			kept = append(kept, v)
		}
	}
	return kept, dropped
}

// findPostLocked returns a copy of owner's post diaryNo. Caller holds SocialMu.
func findPostLocked(owner, diaryNo string) (store.DiaryPost, bool) {
	for _, post := range store.DiaryPosts {
		if post.AvatarID == owner && post.DiaryNo == diaryNo {
			return post, true
		}
	}
	return store.DiaryPost{}, false
}

// diaryCountsLocked returns a post's comment count, like count and the viewer's like type (0 when none; an empty
// viewer matches no like). Caller holds SocialMu.
// ponytail: scans every comment and like per post; index them per post when the lists grow.
func diaryCountsLocked(owner, diaryNo, viewer string) (comments, likes, mine int) {
	for _, c := range store.DiaryComments {
		if c.OwnerAvatarID == owner && c.DiaryNo == diaryNo {
			comments++
		}
	}
	for _, l := range store.DiaryLikes {
		if l.OwnerAvatarID == owner && l.DiaryNo == diaryNo {
			likes++
			if viewer != "" && l.LikeAvatarID == viewer {
				mine = l.LikeType
			}
		}
	}
	return comments, likes, mine
}

// commentPageLocked returns the newest comments on one post with commentNo < lastSeq (lastSeq -1: no bound), at most
// size, and whether no older comment is left. Caller holds SocialMu. Comments are appended in commentNo order, so
// walking the list backwards is newest first.
func commentPageLocked(owner, diaryNo string, lastSeq int64, size int) ([]store.DiaryComment, bool) {
	var items []store.DiaryComment
	for i := len(store.DiaryComments) - 1; i >= 0; i-- {
		c := store.DiaryComments[i]
		if c.OwnerAvatarID != owner || c.DiaryNo != diaryNo || (lastSeq >= 0 && guestbookSeq(c.CommentNo) >= uint64(lastSeq)) {
			continue
		}
		items = append(items, c)
	}
	lastData := len(items) <= size
	if len(items) > size {
		items = items[:size]
	}
	return items, lastData
}

// likePageLocked is commentPageLocked for likes; total counts every like on the post. Caller holds SocialMu.
func likePageLocked(owner, diaryNo string, lastSeq int64, size int) (items []store.DiaryLike, total int, lastData bool) {
	for i := len(store.DiaryLikes) - 1; i >= 0; i-- {
		l := store.DiaryLikes[i]
		if l.OwnerAvatarID != owner || l.DiaryNo != diaryNo {
			continue
		}
		total++
		if lastSeq >= 0 && guestbookSeq(l.LikeNo) >= uint64(lastSeq) {
			continue
		}
		items = append(items, l)
	}
	lastData = len(items) <= size
	if len(items) > size {
		items = items[:size]
	}
	return items, total, lastData
}

// handleDiaryCommentWrite is POST /v4/diary2/comment/write/<owner> (spec 3.2). The writer must be able to see the post,
// else 404. The body's commentAvatarId is the owner (spec 3.2 reads it so) and tagAvatarIds is ignored.
func handleDiaryCommentWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	parts, ok := diaryPathParts(r.URL.Path, "/v4/diary2/comment/write/", 1)
	if !ok {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var req struct {
		DiaryNo  uint64 `json:"diaryNo"`
		AvatarID string `json:"avatarID"`
		Content  string `json:"content"`
	}
	if _, err := httpx.UnmarshalNativeJSON(raw, &req); err != nil || req.DiaryNo == 0 || req.AvatarID != actor.Aid || strings.TrimSpace(req.Content) == "" || len(req.Content) > 4000 || !utf8.ValidString(req.Content) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	nickname := actor.Name
	if nickname == "" {
		nickname = "guest"
	}
	locale := actor.Country
	if locale == "" {
		locale = "JP"
	}
	owner, diaryNo := parts[0], strconv.FormatUint(req.DiaryNo, 10)

	store.SocialMu.Lock()
	post, found := findPostLocked(owner, diaryNo)
	if !found || !canViewDiaryPost(post, actor, true) {
		store.SocialMu.Unlock()
		httpx.ServeNotFound(w)
		return
	}
	if store.NextCommentNo == ^uint64(0) {
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	store.NextCommentNo++
	comment := store.DiaryComment{
		CommentNo: strconv.FormatUint(store.NextCommentNo, 10), DiaryNo: diaryNo, OwnerAvatarID: owner,
		CommentAvatarID: actor.Aid, CommentAvatarLocale: locale, Nickname: nickname, Content: req.Content,
		Date: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	prev := store.DiaryComments
	store.DiaryComments = append(prev, comment)
	err = store.SaveSocialLocked()
	if err != nil {
		store.DiaryComments = prev
		store.NextCommentNo--
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	data, err := json.Marshal(map[string]any{"result": commentRowOf(comment)})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(data))
}

// handleDiaryCommentSearch is GET /v4/diary2/comment/search/<owner>/<diaryNo>?lastSeq=&size= (spec 3.1), newest first.
// A post the viewer cannot see answers an empty page rather than an error.
func handleDiaryCommentSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, viewerOK := store.AccountForRequest(r)
	parts, ok := diaryPathParts(r.URL.Path, "/v4/diary2/comment/search/", 2)
	if !ok {
		httpx.ServeNotFound(w)
		return
	}
	lastSeq, size, ok := cursorPage(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if r.URL.Query().Get("size") == "" {
		size = diaryPageSize
	}
	store.SocialMu.Lock()
	var items []store.DiaryComment
	lastData := true
	if post, found := findPostLocked(parts[0], parts[1]); found && canViewDiaryPost(post, viewer, viewerOK) {
		items, lastData = commentPageLocked(parts[0], parts[1], lastSeq, size)
	}
	store.SocialMu.Unlock()
	data, err := json.Marshal(map[string]any{"result": map[string]any{"lastData": lastData, "items": commentRows(items)}})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(data))
}

// handleDiaryCommentErase is POST /v4/diary2/comment/erase/<owner>/<diaryNo>/<commentNo> with body null (spec 3.2).
// The comment's writer or the post's owner may erase it; a comment that does not exist answers true (spec 5.3).
func handleDiaryCommentErase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	parts, ok := diaryPathParts(r.URL.Path, "/v4/diary2/comment/erase/", 3)
	if !ok {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var body any
	if _, err := httpx.UnmarshalNativeJSON(raw, &body); err != nil || body != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	owner, diaryNo, commentNo := parts[0], parts[1], parts[2]

	store.SocialMu.Lock()
	index := slices.IndexFunc(store.DiaryComments, func(c store.DiaryComment) bool {
		return c.OwnerAvatarID == owner && c.DiaryNo == diaryNo && c.CommentNo == commentNo
	})
	if index >= 0 && actor.Aid != owner && actor.Aid != store.DiaryComments[index].CommentAvatarID {
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if index >= 0 {
		prev := store.DiaryComments
		store.DiaryComments = slices.Delete(slices.Clone(prev), index, index+1)
		err = store.SaveSocialLocked()
		if err != nil {
			store.DiaryComments = prev
		}
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// handleDiaryLikeSave is POST /v4/diary2/like/save/<owner> (spec 3.2). likeType 1..6 sets the viewer's one like on the
// post, changing it in place. likeType 0 removes the viewer's like (Cherry rule: spec 6.3 is open on whether 0 is a
// real kind of like, so it is treated as an unlike). The body's avatarId (owner) is not read; the URL is the owner.
func handleDiaryLikeSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	parts, ok := diaryPathParts(r.URL.Path, "/v4/diary2/like/save/", 1)
	if !ok {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var req struct {
		DiaryNo      uint64 `json:"diaryNo"`
		LikeAvatarID string `json:"likeAvatarId"`
		LikeType     *int   `json:"likeType"`
	}
	if _, err := httpx.UnmarshalNativeJSON(raw, &req); err != nil || req.DiaryNo == 0 || req.LikeAvatarID != actor.Aid || req.LikeType == nil || *req.LikeType < 0 || *req.LikeType > 6 {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	nickname := actor.Name
	if nickname == "" {
		nickname = "guest"
	}
	owner, diaryNo, kind := parts[0], strconv.FormatUint(req.DiaryNo, 10), *req.LikeType

	store.SocialMu.Lock()
	post, found := findPostLocked(owner, diaryNo)
	if !found || !canViewDiaryPost(post, actor, true) {
		store.SocialMu.Unlock()
		httpx.ServeNotFound(w)
		return
	}
	if store.NextLikeNo == ^uint64(0) {
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	prev, prevNo := store.DiaryLikes, store.NextLikeNo
	index := slices.IndexFunc(prev, func(l store.DiaryLike) bool {
		return l.OwnerAvatarID == owner && l.DiaryNo == diaryNo && l.LikeAvatarID == actor.Aid
	})
	next := slices.Clone(prev)
	switch {
	case kind == 0 && index >= 0:
		next = slices.Delete(next, index, index+1)
	case kind > 0 && index >= 0:
		next[index].LikeType = kind
	case kind > 0:
		store.NextLikeNo++
		next = append(next, store.DiaryLike{
			LikeNo: strconv.FormatUint(store.NextLikeNo, 10), DiaryNo: diaryNo, OwnerAvatarID: owner,
			LikeAvatarID: actor.Aid, Nickname: nickname, LikeType: kind,
		})
	}
	store.DiaryLikes = next
	err = store.SaveSocialLocked()
	if err != nil {
		store.DiaryLikes, store.NextLikeNo = prev, prevNo
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// handleDiaryLikeSearch is GET /v4/diary2/like/search/<owner>/<diaryNo>?lastSeq=&size= (spec 3.1), newest first.
// totalCount is a JSON string holding every like on the post, not just this page.
func handleDiaryLikeSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, viewerOK := store.AccountForRequest(r)
	parts, ok := diaryPathParts(r.URL.Path, "/v4/diary2/like/search/", 2)
	if !ok {
		httpx.ServeNotFound(w)
		return
	}
	lastSeq, size, ok := cursorPage(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if r.URL.Query().Get("size") == "" {
		size = diaryPageSize
	}
	store.SocialMu.Lock()
	var items []store.DiaryLike
	total, lastData := 0, true
	if post, found := findPostLocked(parts[0], parts[1]); found && canViewDiaryPost(post, viewer, viewerOK) {
		items, total, lastData = likePageLocked(parts[0], parts[1], lastSeq, size)
	}
	store.SocialMu.Unlock()
	data, err := json.Marshal(map[string]any{"result": map[string]any{
		"totalCount": strconv.Itoa(total), "lastData": lastData, "items": likeRows(items),
	}})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(data))
}

// handleDiaryReport answers a report route (POST /v4/report/<kind>) with true and logs one line. Nothing is stored,
// and the report text is player input, so it is never logged.
func handleDiaryReport(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.ServeNotFound(w)
			return
		}
		if _, ok := store.AccountForRequest(r); !ok {
			httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
			return
		}
		raw, err := httpx.ReadNativeBody(r)
		if err != nil {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		var req map[string]any
		if _, err := httpx.UnmarshalNativeJSON(raw, &req); err != nil {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		log.Printf("cherry: diary report kind=%s target=%s ref=%s", kind, reportID(req["targetAvatarId"]), reportID(req["refId"]))
		httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
	}
}

// reportID is an id from a report body for the log: digits only, anything else logs as "-".
func reportID(v any) string {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	}
	if !httpx.IsAllDigits(s) || len(s) > 20 {
		return "-"
	}
	return s
}
