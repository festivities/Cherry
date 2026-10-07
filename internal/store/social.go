package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"cherry/internal/httpx"
)

type SavedGuestbookEntry struct {
	HostAvtNo                    string `json:"hostAvtNo"`
	GuestHistNo                  string `json:"guestHistNo"`
	GuestAvtNo                   string `json:"guestAvtNo"`
	GuestAvtLocale               string `json:"guestAvtLocale"`
	GuestNickname                string `json:"guestNickname"`
	GuestProfile                 string `json:"guestProfile"`
	GuestBg                      string `json:"guestBg"`
	GuestWholeProfile            string `json:"guestWholeProfile"`
	ObsProfileImagePath          string `json:"obsProfileImagePath"`
	ObsWholeBodyProfileImagePath string `json:"obsWholeBodyProfileImagePath"`
	Content                      string `json:"content"`
	RegYmdt                      string `json:"regYmdt"`
	Secret                       bool   `json:"secret"`
	Blind                        bool   `json:"blind"`
	IsPenalty                    bool   `json:"isPenalty"`
	GuestIsExist                 bool   `json:"guestIsExist"`
	Reply                        []any  `json:"reply"`
}

type GuestbookRow struct {
	GuestHistNo             string `json:"guestHistNo"`
	GuestAvtNo              string `json:"guestAvtNo"`
	GuestAvtLocale          string `json:"guestAvtLocale"`
	GuestNickname           string `json:"guestNickname"`
	GuestProfile            string `json:"guestProfile"`
	GuestBg                 string `json:"guestBg"`
	GuestWholeProfile       string `json:"guestWholeProfile"`
	ObsProfileImagePath     string `json:"obsProfileImagePath"`
	ObsWholeBodyProfilePath string `json:"obsWholeBodyProfileImagePath"`
	Content                 string `json:"content"`
	RegYmdt                 string `json:"regYmdt"`
	Secret                  bool   `json:"secret"`
	Blind                   bool   `json:"blind"`
	IsPenalty               bool   `json:"isPenalty"`
	GuestIsExist            bool   `json:"guestIsExist"`
	Reply                   []any  `json:"reply"`
}

func (e SavedGuestbookEntry) Row() GuestbookRow {
	reply := e.Reply
	if reply == nil {
		reply = []any{}
	}
	return GuestbookRow{
		GuestHistNo: e.GuestHistNo, GuestAvtNo: e.GuestAvtNo, GuestAvtLocale: e.GuestAvtLocale,
		GuestNickname: e.GuestNickname, GuestProfile: e.GuestProfile, GuestBg: e.GuestBg,
		GuestWholeProfile: e.GuestWholeProfile, ObsProfileImagePath: e.ObsProfileImagePath,
		ObsWholeBodyProfilePath: e.ObsWholeBodyProfileImagePath, Content: e.Content, RegYmdt: e.RegYmdt,
		Secret: e.Secret, Blind: e.Blind, IsPenalty: e.IsPenalty, GuestIsExist: e.GuestIsExist, Reply: reply,
	}
}

func ValidAvatarID(id string) bool {
	return len(id) > 0 && len(id) <= 20 && httpx.IsAllDigits(id)
}

const (
	// Synthetic friend lives outside the account allocator's range; it was "2"
	// until a real phone guest was also assigned aid 2 and got shadowed.
	FriendAID  = "100000"
	FriendName = "Friend"
)

var RetiredAvatarIDs = []string{"1", "2"}

type DiaryImage struct {
	ImageURL string `json:"imageUrl"`
}

type DiaryPost struct {
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
	Images            []DiaryImage `json:"images"`
}

type savedSocial struct {
	Version          int                   `json:"version"`
	FriendRemoved    bool                  `json:"friendRemoved"`
	FriendBookmarked bool                  `json:"friendBookmarked"`
	NextDiaryNo      uint64                `json:"nextDiaryNo"`
	Posts            []DiaryPost           `json:"posts"`
	NextGuestHistNo  uint64                `json:"nextGuestHistNo"`
	Guestbook        []SavedGuestbookEntry `json:"guestbook"`
	Friendships      []Friendship          `json:"friendships,omitempty"`
}

// Friendship is one record per unordered pair of real accounts. "pending":
// A applied to B; "accepted": mutual; "removed": tombstone so both clients'
// never-pruned native caches get status -1.
type Friendship struct {
	A     string `json:"a"`
	B     string `json:"b"`
	State string `json:"state"`
}

var (
	SocialMu         sync.Mutex
	SocialStorePath  string
	FriendRemoved    bool
	FriendBookmarked bool
	NextDiaryNo      uint64
	DiaryPosts       []DiaryPost
	NextGuestHistNo  uint64
	GuestbookEntries []SavedGuestbookEntry
	Friendships      []Friendship
)

func LoadSocial() error {
	dir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	return LoadSocialFrom(filepath.Join(dir, "Cherry", "social.json"))
}

func LoadSocialFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := savedSocial{Version: 1, Posts: []DiaryPost{}, Guestbook: []SavedGuestbookEntry{}}
	if err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return err
		}
		if state.Version != 1 {
			return errors.New("invalid social store version")
		}
	}
	if state.Posts == nil {
		state.Posts = []DiaryPost{}
	}
	for i := range state.Posts {
		NormalizeDiaryPost(&state.Posts[i])
		post := state.Posts[i]
		seq, err := strconv.ParseUint(post.DiaryNo, 10, 64)
		if err == nil && seq > state.NextDiaryNo {
			state.NextDiaryNo = seq
		}
	}
	if state.Guestbook == nil {
		state.Guestbook = []SavedGuestbookEntry{}
	}
	for i := range state.Guestbook {
		entry := &state.Guestbook[i]
		seq, err := strconv.ParseUint(entry.GuestHistNo, 10, 64)
		if err != nil || seq == 0 || !ValidAvatarID(entry.HostAvtNo) || !ValidAvatarID(entry.GuestAvtNo) {
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
		if !ValidAvatarID(f.A) || !ValidAvatarID(f.B) || f.A == f.B || (f.State != "pending" && f.State != "accepted" && f.State != "removed") {
			return errors.New("invalid friendship")
		}
	}
	SocialMu.Lock()
	SocialStorePath = path
	Friendships = state.Friendships
	FriendRemoved = state.FriendRemoved
	FriendBookmarked = state.FriendBookmarked
	NextDiaryNo = state.NextDiaryNo
	DiaryPosts = state.Posts
	NextGuestHistNo = state.NextGuestHistNo
	GuestbookEntries = state.Guestbook
	SocialMu.Unlock()
	return nil
}

func ResetSocial() {
	SocialMu.Lock()
	SocialStorePath = ""
	FriendRemoved = false
	FriendBookmarked = false
	NextDiaryNo = 0
	DiaryPosts = nil
	NextGuestHistNo = 0
	GuestbookEntries = nil
	Friendships = nil
	SocialMu.Unlock()
}

func SaveSocialLocked() error {
	if SocialStorePath == "" {
		return nil
	}
	posts := DiaryPosts
	if posts == nil {
		posts = []DiaryPost{}
	}
	guestbook := GuestbookEntries
	if guestbook == nil {
		guestbook = []SavedGuestbookEntry{}
	}
	data, err := json.Marshal(savedSocial{
		Version: 1, FriendRemoved: FriendRemoved, FriendBookmarked: FriendBookmarked,
		NextDiaryNo: NextDiaryNo, Posts: posts, NextGuestHistNo: NextGuestHistNo, Guestbook: guestbook,
		Friendships: Friendships,
	})
	if err != nil {
		return err
	}
	dir := filepath.Dir(SocialStorePath)
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
	return os.Rename(tmp.Name(), SocialStorePath)
}

func KnownFriendTarget(aid string) bool {
	return aid == FriendAID || (ValidAvatarID(aid) && KnownSocialAvatar(aid))
}

// FriendshipIndex finds the record for the unordered pair; state "" matches any. Caller holds socialMu.
func FriendshipIndex(x, y, state string) int {
	for i, f := range Friendships {
		if ((f.A == x && f.B == y) || (f.A == y && f.B == x)) && (state == "" || f.State == state) {
			return i
		}
	}
	return -1
}

func KnownSocialAvatar(aid string) bool {
	if aid == FriendAID {
		return true
	}
	if !ValidAvatarID(aid) {
		return false
	}
	_, ok := AccountByAvatarID(aid)
	return ok
}
