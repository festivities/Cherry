package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"

	"cherry/internal/httpx"
)

type SavedGuestbookEntry struct {
	HostAvtNo                    string           `json:"hostAvtNo"`
	GuestHistNo                  string           `json:"guestHistNo"`
	GuestAvtNo                   string           `json:"guestAvtNo"`
	GuestAvtLocale               string           `json:"guestAvtLocale"`
	GuestNickname                string           `json:"guestNickname"`
	GuestProfile                 string           `json:"guestProfile"`
	GuestBg                      string           `json:"guestBg"`
	GuestWholeProfile            string           `json:"guestWholeProfile"`
	ObsProfileImagePath          string           `json:"obsProfileImagePath"`
	ObsWholeBodyProfileImagePath string           `json:"obsWholeBodyProfileImagePath"`
	Content                      string           `json:"content"`
	RegYmdt                      string           `json:"regYmdt"`
	Secret                       bool             `json:"secret"`
	Blind                        bool             `json:"blind"`
	IsPenalty                    bool             `json:"isPenalty"`
	GuestIsExist                 bool             `json:"guestIsExist"`
	Reply                        []GuestbookReply `json:"reply"`
}

type GuestbookRow struct {
	GuestHistNo             string           `json:"guestHistNo"`
	GuestAvtNo              string           `json:"guestAvtNo"`
	GuestAvtLocale          string           `json:"guestAvtLocale"`
	GuestNickname           string           `json:"guestNickname"`
	GuestProfile            string           `json:"guestProfile"`
	GuestBg                 string           `json:"guestBg"`
	GuestWholeProfile       string           `json:"guestWholeProfile"`
	ObsProfileImagePath     string           `json:"obsProfileImagePath"`
	ObsWholeBodyProfilePath string           `json:"obsWholeBodyProfileImagePath"`
	Content                 string           `json:"content"`
	RegYmdt                 string           `json:"regYmdt"`
	Secret                  bool             `json:"secret"`
	Blind                   bool             `json:"blind"`
	IsPenalty               bool             `json:"isPenalty"`
	GuestIsExist            bool             `json:"guestIsExist"`
	Reply                   []GuestbookReply `json:"reply"`
}

func (e SavedGuestbookEntry) Row() GuestbookRow {
	reply := e.Reply
	if reply == nil {
		reply = []GuestbookReply{}
	}
	return GuestbookRow{
		GuestHistNo: e.GuestHistNo, GuestAvtNo: e.GuestAvtNo, GuestAvtLocale: e.GuestAvtLocale,
		GuestNickname: e.GuestNickname, GuestProfile: e.GuestProfile, GuestBg: e.GuestBg,
		GuestWholeProfile: e.GuestWholeProfile, ObsProfileImagePath: e.ObsProfileImagePath,
		ObsWholeBodyProfilePath: e.ObsWholeBodyProfileImagePath, Content: e.Content, RegYmdt: e.RegYmdt,
		Secret: e.Secret, Blind: e.Blind, IsPenalty: e.IsPenalty, GuestIsExist: e.GuestIsExist, Reply: reply,
	}
}

// GuestbookReply is one reply inside a guestbook entry: the 8 row keys the client reads (spec 2.3).
// The entry it answers is ParentHistNo; replies are kept oldest first.
type GuestbookReply struct {
	ReplyHistNo    string `json:"replyHistNo"`
	ParentHistNo   string `json:"parentHistNo"`
	ReplyAvtNo     string `json:"replyAvtNo"`
	ReplyAvtLocale string `json:"replyAvtLocale"`
	ReplyNickname  string `json:"replyNickname"`
	Content        string `json:"content"`
	Blind          bool   `json:"blind"`
	RegYmdt        string `json:"regYmdt"`
}

// DiaryComment is one comment on a Diary post. OwnerAvatarID is the post's owner; CommentAvatarID is the writer.
// Comments are kept in commentNo order (oldest first).
type DiaryComment struct {
	CommentNo           string `json:"commentNo"`
	DiaryNo             string `json:"diaryNo"`
	OwnerAvatarID       string `json:"ownerAvatarId"`
	CommentAvatarID     string `json:"commentAvatarId"`
	CommentAvatarLocale string `json:"commentAvatarLocale"`
	Nickname            string `json:"nickname"`
	Content             string `json:"content"`
	Date                string `json:"date"`
}

// DiaryLike is one player's like on a Diary post. A player has at most one per post; LikeType 1..6 is changed in place.
type DiaryLike struct {
	LikeNo        string `json:"likeNo"`
	DiaryNo       string `json:"diaryNo"`
	OwnerAvatarID string `json:"ownerAvatarId"`
	LikeAvatarID  string `json:"likeAvatarId"`
	Nickname      string `json:"nickname"`
	LikeType      int    `json:"likeType"`
}

func ValidAvatarID(id string) bool {
	return len(id) > 0 && len(id) <= 20 && httpx.IsAllDigits(id)
}

// Synthetic friend lives outside the account allocator's range; it was "2"
// until a real phone guest was also assigned aid 2 and got shadowed. Its display
// name is operator data (profiles.json friend_name, FriendName()).
const FriendAID = "100000"

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
	// Achieve fields are set only on Share to Diary posts (POST /v4/achieve/write); omitted otherwise.
	AchieveType       string `json:"achieveType,omitempty"`
	AchieveTypeLevel  string `json:"achieveTypeLevel,omitempty"`
	AchieveDataType   string `json:"achieveDataType,omitempty"`
	AchieveData       string `json:"achieveData,omitempty"`
	AchieveLinkValue  string `json:"achieveLinkValue,omitempty"`
	AchieveDataDetail string `json:"achieveDataDetail,omitempty"`
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
	Comments         []DiaryComment        `json:"comments"`
	NextCommentNo    uint64                `json:"nextCommentNo"`
	Likes            []DiaryLike           `json:"likes"`
	NextLikeNo       uint64                `json:"nextLikeNo"`
	NextReplyHistNo  uint64                `json:"nextReplyHistNo"`
	GuestbookRead    map[string]uint64     `json:"guestbookRead"`
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
	// SocialMu guards the social store below. Lock order: AccountsMu, then CirclesMu, then SocialMu.
	SocialMu         sync.Mutex
	FriendRemoved    bool
	FriendBookmarked bool
	NextDiaryNo      uint64
	DiaryPosts       []DiaryPost
	NextGuestHistNo  uint64
	GuestbookEntries []SavedGuestbookEntry
	GuestbookRead    map[string]uint64 // host aid -> highest GuestHistNo the host cleared (guestbookcount/clear)
	Friendships      []Friendship
	DiaryComments    []DiaryComment
	NextCommentNo    uint64
	DiaryLikes       []DiaryLike
	NextLikeNo       uint64
	NextReplyHistNo  uint64
)

// SocialChange names what one social save changed. A named key is written from memory when memory holds its row, and the
// row is removed when memory does not, so a removed row is named by the key it had. Scalars writes the friend flags and
// the number counters, which every new row moves. A guestbook reply is named by its reply history number, a friendship by
// its pair (either order), a read marker by its host aid.
type SocialChange struct {
	Posts    []string    // diary numbers
	Comments []string    // comment numbers
	Likes    []string    // like numbers
	Entries  []string    // guestbook entries, by GuestHistNo
	Replies  []string    // reply history numbers
	Friends  [][2]string // friendship pairs
	Reads    []string    // host aids whose read marker changed
	Scalars  bool
}

// LoadSocialLocked reads the social store of the attached database into memory (OpenStore, ReloadStores). Without a
// database the store is emptied.
func loadSocialLocked(d *DB) error {
	var s savedSocial
	if d != nil {
		if err := d.View(func(q Querier) error {
			var err error
			s, err = readSocial(q)
			return err
		}); err != nil {
			return err
		}
	} else {
		s = savedSocial{}
	}
	SocialMu.Lock()
	defer SocialMu.Unlock()
	installSocialLocked(s)
	return nil
}

// ResetSocial empties the social store in memory (tests). It does not touch an attached database: a test that resets
// while one is attached must reload from it or detach first.
func ResetSocial() {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	installSocialLocked(savedSocial{})
}

// ReloadSocial replaces the social store in memory with the attached database's rows: the restart check of a test, the
// social half of ReloadStores.
func ReloadSocial() error {
	d := attachedDB()
	if d == nil {
		return errors.New("no database attached")
	}
	return loadSocialLocked(d)
}

// installSocialLocked makes s the social store in memory. Caller holds SocialMu.
func installSocialLocked(s savedSocial) {
	FriendRemoved, FriendBookmarked = s.FriendRemoved, s.FriendBookmarked
	NextDiaryNo, NextGuestHistNo, NextCommentNo = s.NextDiaryNo, s.NextGuestHistNo, s.NextCommentNo
	NextLikeNo, NextReplyHistNo = s.NextLikeNo, s.NextReplyHistNo
	DiaryPosts, GuestbookEntries, GuestbookRead = s.Posts, s.Guestbook, s.GuestbookRead
	Friendships, DiaryComments, DiaryLikes = s.Friendships, s.Comments, s.Likes
}

// socialSnapshotLocked is the social store as savedSocial holds it, for the safety net (no copy). Caller holds SocialMu.
func socialSnapshotLocked() savedSocial {
	return savedSocial{
		Version: 1, FriendRemoved: FriendRemoved, FriendBookmarked: FriendBookmarked,
		NextDiaryNo: NextDiaryNo, Posts: DiaryPosts, NextGuestHistNo: NextGuestHistNo, Guestbook: GuestbookEntries,
		Friendships: Friendships, Comments: DiaryComments, NextCommentNo: NextCommentNo, Likes: DiaryLikes,
		NextLikeNo: NextLikeNo, NextReplyHistNo: NextReplyHistNo, GuestbookRead: GuestbookRead,
	}
}

// rows is how many rows the store holds (the startup guard asks whether a file is worth importing).
func (s savedSocial) rows() int {
	return len(s.Posts) + len(s.Comments) + len(s.Likes) + len(s.Guestbook) + len(s.Friendships) + len(s.GuestbookRead)
}

// parseSocialFile reads a social.json the way the server used to load it, for cmd/cherry-import and the startup guard.
// Files written before comments, likes and guestbook replies existed load with empty lists, and the counters are raised
// to the highest number already in use. A missing file is an empty store.
func parseSocialFile(path string) (savedSocial, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return savedSocial{}, err
	}
	state := savedSocial{Version: 1, Posts: []DiaryPost{}, Guestbook: []SavedGuestbookEntry{}}
	if err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return state, err
		}
		if state.Version != 1 {
			return state, errors.New("invalid social store version")
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
			return state, errors.New("invalid guestbook entry")
		}
		if seq > state.NextGuestHistNo {
			state.NextGuestHistNo = seq
		}
		if entry.Reply == nil {
			entry.Reply = []GuestbookReply{}
		}
		for _, reply := range entry.Reply {
			if n, err := strconv.ParseUint(reply.ReplyHistNo, 10, 64); err == nil && n > state.NextReplyHistNo {
				state.NextReplyHistNo = n
			}
		}
	}
	if state.GuestbookRead == nil {
		state.GuestbookRead = map[string]uint64{}
	}
	// A read marker is a number in use: NextGuestHistNo is recomputed from the entries present, so a marker raises it,
	// or a new entry could land at or below a marker whose entry was erased.
	for _, seq := range state.GuestbookRead {
		state.NextGuestHistNo = max(state.NextGuestHistNo, seq)
	}
	if state.Comments == nil {
		state.Comments = []DiaryComment{}
	}
	for _, c := range state.Comments {
		if n, err := strconv.ParseUint(c.CommentNo, 10, 64); err == nil && n > state.NextCommentNo {
			state.NextCommentNo = n
		}
	}
	if state.Likes == nil {
		state.Likes = []DiaryLike{}
	}
	for _, l := range state.Likes {
		if n, err := strconv.ParseUint(l.LikeNo, 10, 64); err == nil && n > state.NextLikeNo {
			state.NextLikeNo = n
		}
	}
	for _, f := range state.Friendships {
		if !ValidAvatarID(f.A) || !ValidAvatarID(f.B) || f.A == f.B || (f.State != "pending" && f.State != "accepted" && f.State != "removed") {
			return state, errors.New("invalid friendship")
		}
	}
	return state, nil
}

// SaveSocialLocked writes the rows c names in ONE transaction. Without an attached database it writes nothing. The caller
// holds SocialMu and has already changed memory; on error the caller restores what it changed. A row that is new must sit
// at an end of its list in memory (a diary post or guestbook entry at the front, the others at the back, and a reply at the
// end of its entry); any other place is refused, so the saved order never differs from memory. In test builds the database
// is read back and compared with memory after the write (dbnet.go).
func SaveSocialLocked(c SocialChange) error {
	d := attachedDB()
	if d == nil {
		return nil
	}
	site := callerSite()
	if err := d.Update(func(tx *sql.Tx) error { return saveSocialRowsTx(tx, c) }); err != nil {
		return err
	}
	if testing.Testing() {
		netCheckSocial(d, site)
	}
	return nil
}

// saveSocialRowsTx writes the rows c names from memory, inside the caller's transaction. Caller holds SocialMu.
func saveSocialRowsTx(tx *sql.Tx, c SocialChange) error {
	for _, no := range c.Posts {
		i := slices.IndexFunc(DiaryPosts, func(p DiaryPost) bool { return p.DiaryNo == no })
		if i < 0 {
			if err := deleteRow(tx, "diary_posts", "diary_no = ?", no); err != nil {
				return err
			}
			continue
		}
		data, err := rowJSON(DiaryPosts[i])
		if err != nil {
			return err
		}
		if err := upsertRow(tx, "diary_posts", slotOf(i, len(DiaryPosts)), []string{"diary_no", "avatar_id", "data"},
			[]any{no, DiaryPosts[i].AvatarID, data}, "diary_no = ?", no); err != nil {
			return err
		}
	}
	for _, no := range c.Comments {
		i := slices.IndexFunc(DiaryComments, func(x DiaryComment) bool { return x.CommentNo == no })
		if i < 0 {
			if err := deleteRow(tx, "diary_comments", "comment_no = ?", no); err != nil {
				return err
			}
			continue
		}
		data, err := rowJSON(DiaryComments[i])
		if err != nil {
			return err
		}
		if err := upsertRow(tx, "diary_comments", slotOf(i, len(DiaryComments)), []string{"comment_no", "diary_no", "data"},
			[]any{no, DiaryComments[i].DiaryNo, data}, "comment_no = ?", no); err != nil {
			return err
		}
	}
	for _, no := range c.Likes {
		i := slices.IndexFunc(DiaryLikes, func(l DiaryLike) bool { return l.LikeNo == no })
		if i < 0 {
			if err := deleteRow(tx, "diary_likes", "like_no = ?", no); err != nil {
				return err
			}
			continue
		}
		data, err := rowJSON(DiaryLikes[i])
		if err != nil {
			return err
		}
		if err := upsertRow(tx, "diary_likes", slotOf(i, len(DiaryLikes)), []string{"like_no", "diary_no", "like_avatar_id", "data"},
			[]any{no, DiaryLikes[i].DiaryNo, DiaryLikes[i].LikeAvatarID, data}, "like_no = ?", no); err != nil {
			return err
		}
	}
	for _, no := range c.Entries {
		i := slices.IndexFunc(GuestbookEntries, func(e SavedGuestbookEntry) bool { return e.GuestHistNo == no })
		if i < 0 {
			// The entry's replies go with it (ON DELETE CASCADE).
			if err := deleteRow(tx, "guestbook_entries", "guest_hist_no = ?", no); err != nil {
				return err
			}
			continue
		}
		e := GuestbookEntries[i]
		e.Reply = nil // the replies are rows of their own
		data, err := rowJSON(e)
		if err != nil {
			return err
		}
		if err := upsertRow(tx, "guestbook_entries", slotOf(i, len(GuestbookEntries)), []string{"guest_hist_no", "host_avt_no", "data"},
			[]any{no, e.HostAvtNo, data}, "guest_hist_no = ?", no); err != nil {
			return err
		}
	}
	for _, no := range c.Replies {
		entry, j := findReplyLocked(no)
		if entry < 0 {
			if err := deleteRow(tx, "guestbook_replies", "reply_hist_no = ?", no); err != nil {
				return err
			}
			continue
		}
		// Replies are appended, and their row ids are one global order: a new reply must be the last of its entry.
		slot := slotBack
		if j != len(GuestbookEntries[entry].Reply)-1 {
			slot = slotNone
		}
		var entryID int64
		parent := GuestbookEntries[entry].GuestHistNo
		if err := tx.QueryRow(`SELECT id FROM guestbook_entries WHERE guest_hist_no = ?`, parent).Scan(&entryID); err != nil {
			return fmt.Errorf("guestbook reply %s: its entry is not in the database", no)
		}
		data, err := rowJSON(GuestbookEntries[entry].Reply[j])
		if err != nil {
			return err
		}
		if err := upsertRow(tx, "guestbook_replies", slot, []string{"reply_hist_no", "entry_id", "data"},
			[]any{no, entryID, data}, "reply_hist_no = ?", no); err != nil {
			return err
		}
	}
	for _, host := range c.Reads {
		hist, ok := GuestbookRead[host]
		if !ok {
			if err := deleteRow(tx, "guestbook_read", "host_aid = ?", host); err != nil {
				return err
			}
			continue
		}
		if err := upsertRow(tx, "guestbook_read", slotBack, []string{"host_aid", "hist_no"},
			[]any{host, int64(hist)}, "host_aid = ?", host); err != nil {
			return err
		}
	}
	for _, pair := range c.Friends {
		lo, hi := min(pair[0], pair[1]), max(pair[0], pair[1])
		i := FriendshipIndex(pair[0], pair[1], "")
		if i < 0 {
			if err := deleteRow(tx, "friendships", "min(a, b) = ? AND max(a, b) = ?", lo, hi); err != nil {
				return err
			}
			continue
		}
		f := Friendships[i]
		if err := upsertRow(tx, "friendships", slotOf(i, len(Friendships)), []string{"a", "b", "state"},
			[]any{f.A, f.B, f.State}, "min(a, b) = ? AND max(a, b) = ?", lo, hi); err != nil {
			return err
		}
	}
	if c.Scalars {
		return setSocialScalarsTx(tx, FriendRemoved, FriendBookmarked, NextDiaryNo, NextGuestHistNo, NextCommentNo, NextLikeNo, NextReplyHistNo)
	}
	return nil
}

// findReplyLocked finds a guestbook reply by its history number: the entry index and the reply index, -1 and -1 when none.
// Caller holds SocialMu.
func findReplyLocked(replyHistNo string) (entry, reply int) {
	for i, e := range GuestbookEntries {
		for j, rep := range e.Reply {
			if rep.ReplyHistNo == replyHistNo {
				return i, j
			}
		}
	}
	return -1, -1
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
