package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	friendAID      = "2"
	friendName     = "Friend"
	saveFailedBody = `{"errorCode":"500","errorMessage":"cherry: save failed"}`
)

type diaryImage struct {
	ImageURL string `json:"imageUrl"`
}

type diaryPost struct {
	DiaryNo      string       `json:"diaryNo"`
	AvatarID     string       `json:"avatarId"`
	Nickname     string       `json:"nickname"`
	RegDate      string       `json:"regDate"`
	Title        string       `json:"title"`
	OpenState    string       `json:"openState"`
	Content      string       `json:"content"`
	FeelingState int          `json:"feelingState"`
	CommentCount int          `json:"commentCount"`
	LikeCount    int          `json:"likeCount"`
	MyLikeType   int          `json:"myLikeType"`
	Blind        bool         `json:"blind"`
	Images       []diaryImage `json:"images"`
}

type savedSocial struct {
	Version          int         `json:"version"`
	FriendRemoved    bool        `json:"friendRemoved"`
	FriendBookmarked bool        `json:"friendBookmarked"`
	NextDiaryNo      uint64      `json:"nextDiaryNo"`
	Posts            []diaryPost `json:"posts"`
}

var (
	socialMu         sync.Mutex
	socialStorePath  string
	friendRemoved    bool
	friendBookmarked bool
	nextDiaryNo      uint64
	diaryPosts       []diaryPost
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
	state := savedSocial{Version: 1, Posts: []diaryPost{}}
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
	socialMu.Lock()
	socialStorePath = path
	friendRemoved = state.FriendRemoved
	friendBookmarked = state.FriendBookmarked
	nextDiaryNo = state.NextDiaryNo
	diaryPosts = state.Posts
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
	data, err := json.Marshal(savedSocial{
		Version: 1, FriendRemoved: friendRemoved, FriendBookmarked: friendBookmarked,
		NextDiaryNo: nextDiaryNo, Posts: posts,
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
	socialMu.Lock()
	removed, marked := friendRemoved, friendBookmarked
	socialMu.Unlock()
	buddies := []buddyRow{}
	bookmarks := []string{}
	if !removed {
		buddies = []buddyRow{{
			AvatarNo: friendAID, AvatarName: friendName, FriendStatus: 1,
			LineBuddyYn: "N", Mid: friendAID,
		}}
	}
	if marked && !removed {
		bookmarks = []string{friendAID}
	}
	body, err := json.Marshal(map[string]any{
		"result": map[string]any{
			"existProfile": false, "nextCursor": 0, "timestamp": "0",
			"friendsCount": len(buddies), "buddyList": buddies,
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

func handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v4/profile/")
	aid, _, _ := strings.Cut(rest, "/")
	if aid == friendAID {
		writeJSON(w, http.StatusOK, `{"result":{"avatarId":"2","name":"Friend","friendStatus":1}}`)
		return
	}
	writeJSON(w, http.StatusOK, profileBody)
}

func handleFriendBookmark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v4/friend/bookmark/")
	remove := false
	if cut, ok := strings.CutPrefix(rest, "remove/"); ok {
		remove = true
		rest = cut
	}
	aid, _, _ := strings.Cut(rest, "/")
	if aid == friendAID {
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
	}
	writeJSON(w, http.StatusOK, `{"result":1}`)
}

func handleFriendRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	raw, err := readNativeBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	var aids []string
	if _, err = unmarshalNativeJSON(raw, &aids); err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	removed := []string{}
	socialMu.Lock()
	prevRemoved, prevMarked := friendRemoved, friendBookmarked
	for _, aid := range aids {
		if aid == friendAID && !friendRemoved {
			friendRemoved = true
			friendBookmarked = false
			removed = append(removed, friendAID)
		}
	}
	err = saveSocialLocked()
	if err != nil {
		friendRemoved, friendBookmarked = prevRemoved, prevMarked
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

func handleDiaryIntro(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/intro/"), "/")
	aid, _, _ := strings.Cut(rest, "/")
	if aid == "" {
		aid = "1"
	}
	socialMu.Lock()
	count := 0
	for _, post := range diaryPosts {
		if post.AvatarID == aid {
			count++
		}
	}
	socialMu.Unlock()
	body, err := json.Marshal(map[string]any{"result": map[string]any{"avatarId": aid, "diaryCount": count}})
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
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/ext/unfold/"), "/")
	aid, _, _ := strings.Cut(rest, "/")
	socialMu.Lock()
	items := make([]diaryPost, 0)
	for _, post := range diaryPosts {
		if aid != "" && post.AvatarID != aid {
			continue
		}
		if post.Images == nil {
			post.Images = []diaryImage{}
		}
		items = append(items, post)
		if len(items) == 10 {
			break
		}
	}
	socialMu.Unlock()
	body, err := json.Marshal(map[string]any{"result": map[string]any{"lastData": true, "items": items}})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(body))
}

func handleDiaryWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	raw, err := readNativeBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	var req struct {
		AvatarID     string       `json:"avatarId"`
		Title        string       `json:"title"`
		Content      string       `json:"content"`
		FeelingState *int         `json:"feelingState"`
		OpenState    string       `json:"openState"`
		Images       []diaryImage `json:"images"`
	}
	if _, err = unmarshalNativeJSON(raw, &req); err != nil || req.AvatarID == "" {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
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
	nickname := "Cherry"
	if req.AvatarID != "1" {
		nickname = friendName
	}
	socialMu.Lock()
	nextDiaryNo++
	post := diaryPost{
		DiaryNo: strconv.FormatUint(nextDiaryNo, 10), AvatarID: req.AvatarID, Nickname: nickname,
		RegDate: strconv.FormatInt(time.Now().UnixMilli(), 10), Title: req.Title, OpenState: openState,
		Content: req.Content, FeelingState: feeling, Images: images,
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
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary2/erase/"), "/")
	diaryNo, _, _ := strings.Cut(rest, "/")
	socialMu.Lock()
	prev := append([]diaryPost(nil), diaryPosts...)
	kept := diaryPosts[:0]
	for _, post := range diaryPosts {
		if post.DiaryNo != diaryNo {
			kept = append(kept, post)
		}
	}
	if kept == nil {
		kept = []diaryPost{}
	}
	diaryPosts = kept
	err := saveSocialLocked()
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
