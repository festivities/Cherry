package store

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// DiaryModFilter narrows the staff Diary entries moderation view (Moderation -> Diary entries).
type DiaryModFilter struct {
	Posts      bool   // include diary posts
	Comments   bool   // include diary comments
	WithPhotos bool   // when Posts is true: include posts with len(Images) > 0
	TextOnly   bool   // when Posts is true: include posts with len(Images) == 0
	Player     string // author avatar id (exact) or author name (case-insensitive substring); for comments also matches owner avatar id (exact) or owner name (case-insensitive substring)
	Text       string // case-insensitive substring of title or content
}

// DiaryModRow is one diary entry or comment returned by the moderation search.
type DiaryModRow struct {
	Kind           string // "post" or "comment"
	No             string // DiaryNo for post, CommentNo for comment
	PostNo         string // DiaryNo of parent post for comment (empty for post)
	Time           int64  // Unix milliseconds UTC (parsed from RegDate / Date; 0 if unparseable)
	RawDate        string // original RegDate / Date fallback when Time == 0
	AuthorAid      string
	AuthorName     string
	AuthorPlayerID int64  // accounts row id (0 if no account row now)
	OwnerAid       string // for comments: diary owner's avatar id
	OwnerName      string // for comments: diary owner's current account name ("" if unknown)
	OwnerPlayerID  int64  // for comments: diary owner's accounts row id (0 if none)
	Title          string // for posts
	Content        string
	OpenState      string  // for posts ("public", "friend", "private", etc.)
	HasPhotos      bool    // for posts: len(Images) > 0
	PhotoIDs       []int64 // resolved media_files.id values for inline /photos/{id}/file thumbnails
}

type diaryAccInfo struct {
	id   int64
	name string
}

type diaryModItem struct {
	row        DiaryModRow
	noNum      int64
	noValid    bool
	photoFiles []string
}

type diaryModMatcher struct {
	filter      DiaryModFilter
	lowerPlayer string
	lowerText   string
	byAid       map[string]diaryAccInfo
}

func newDiaryModMatcher(f DiaryModFilter) diaryModMatcher {
	byAid := make(map[string]diaryAccInfo, len(accountList))
	for _, acc := range accountList {
		if acc.Aid != "" && acc.Aid != "0" {
			byAid[acc.Aid] = diaryAccInfo{id: accountRows[acc].id, name: acc.Name}
		}
	}
	return diaryModMatcher{
		filter:      f,
		lowerPlayer: strings.ToLower(f.Player),
		lowerText:   strings.ToLower(f.Text),
		byAid:       byAid,
	}
}

func parseDiaryTime(raw string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func parseDiaryNo(no string) (int64, bool) {
	n, err := strconv.ParseInt(no, 10, 64)
	return n, err == nil
}

func (m diaryModMatcher) resolveAuthor(aid, fallback string) (int64, string) {
	if info, ok := m.byAid[aid]; ok {
		name := info.name
		if name == "" {
			name = fallback
		}
		return info.id, name
	}
	return 0, fallback
}

func (m diaryModMatcher) resolveOwner(aid string) (int64, string) {
	if info, ok := m.byAid[aid]; ok {
		return info.id, info.name
	}
	return 0, ""
}

func (m diaryModMatcher) matchPost(p DiaryPost) (diaryModItem, bool) {
	if !m.filter.Posts {
		return diaryModItem{}, false
	}
	hasPhotos := len(p.Images) > 0
	if !((hasPhotos && m.filter.WithPhotos) || (!hasPhotos && m.filter.TextOnly)) {
		return diaryModItem{}, false
	}
	authorID, authorName := m.resolveAuthor(p.AvatarID, p.Nickname)
	if m.filter.Player != "" {
		if p.AvatarID != m.filter.Player && !strings.Contains(strings.ToLower(authorName), m.lowerPlayer) {
			return diaryModItem{}, false
		}
	}
	if m.filter.Text != "" {
		if !strings.Contains(strings.ToLower(p.Title), m.lowerText) && !strings.Contains(strings.ToLower(p.Content), m.lowerText) {
			return diaryModItem{}, false
		}
	}
	t := parseDiaryTime(p.RegDate)
	rawDate := ""
	if t == 0 {
		rawDate = p.RegDate
	}
	var photoFiles []string
	if hasPhotos {
		photoFiles = make([]string, 0, len(p.Images))
		for _, img := range p.Images {
			if file, ok := MediaRefFile(img.ImageURL); ok {
				photoFiles = append(photoFiles, file)
			}
		}
	}
	noNum, noValid := parseDiaryNo(p.DiaryNo)
	return diaryModItem{
		row: DiaryModRow{
			Kind:           "post",
			No:             p.DiaryNo,
			Time:           t,
			RawDate:        rawDate,
			AuthorAid:      p.AvatarID,
			AuthorName:     authorName,
			AuthorPlayerID: authorID,
			Title:          p.Title,
			Content:        p.Content,
			OpenState:      p.OpenState,
			HasPhotos:      hasPhotos,
		},
		noNum:      noNum,
		noValid:    noValid,
		photoFiles: photoFiles,
	}, true
}

func (m diaryModMatcher) matchComment(c DiaryComment) (diaryModItem, bool) {
	if !m.filter.Comments {
		return diaryModItem{}, false
	}
	authorID, authorName := m.resolveAuthor(c.CommentAvatarID, c.Nickname)
	ownerID, ownerName := m.resolveOwner(c.OwnerAvatarID)
	if m.filter.Player != "" {
		matchAuthor := c.CommentAvatarID == m.filter.Player || strings.Contains(strings.ToLower(authorName), m.lowerPlayer)
		matchOwner := c.OwnerAvatarID == m.filter.Player || (ownerName != "" && strings.Contains(strings.ToLower(ownerName), m.lowerPlayer))
		if !matchAuthor && !matchOwner {
			return diaryModItem{}, false
		}
	}
	if m.filter.Text != "" && !strings.Contains(strings.ToLower(c.Content), m.lowerText) {
		return diaryModItem{}, false
	}
	t := parseDiaryTime(c.Date)
	rawDate := ""
	if t == 0 {
		rawDate = c.Date
	}
	noNum, noValid := parseDiaryNo(c.CommentNo)
	return diaryModItem{
		row: DiaryModRow{
			Kind:           "comment",
			No:             c.CommentNo,
			PostNo:         c.DiaryNo,
			Time:           t,
			RawDate:        rawDate,
			AuthorAid:      c.CommentAvatarID,
			AuthorName:     authorName,
			AuthorPlayerID: authorID,
			OwnerAid:       c.OwnerAvatarID,
			OwnerName:      ownerName,
			OwnerPlayerID:  ownerID,
			Content:        c.Content,
		},
		noNum:   noNum,
		noValid: noValid,
	}, true
}

// compareDiaryModNewestFirst sorts newest-first: Time DESC, No parsed as int64 DESC (falling back to No DESC), Kind DESC ("post" before "comment").
func compareDiaryModNewestFirst(a, b diaryModItem) int {
	if a.row.Time != b.row.Time {
		return cmp.Compare(b.row.Time, a.row.Time)
	}
	switch {
	case a.noValid && b.noValid:
		if a.noNum != b.noNum {
			return cmp.Compare(b.noNum, a.noNum)
		}
		if a.row.No != b.row.No {
			return cmp.Compare(b.row.No, a.row.No)
		}
	case a.noValid != b.noValid:
		if a.noValid {
			return -1
		}
		return 1
	default:
		if a.row.No != b.row.No {
			return cmp.Compare(b.row.No, a.row.No)
		}
	}
	return cmp.Compare(b.row.Kind, a.row.Kind)
}

func resolveDiaryPagePhotos(items []diaryModItem) ([]DiaryModRow, error) {
	rows := make([]DiaryModRow, len(items))
	if len(items) == 0 {
		return rows, nil
	}
	seen := make(map[string]struct{})
	var uniqueFiles []string
	for _, it := range items {
		for _, f := range it.photoFiles {
			if _, ok := seen[f]; !ok {
				seen[f] = struct{}{}
				uniqueFiles = append(uniqueFiles, f)
			}
		}
	}
	var fileToID map[string]int64
	if len(uniqueFiles) > 0 {
		if d := attachedDB(); d != nil {
			fileToID = make(map[string]int64, len(uniqueFiles))
			args := make([]any, len(uniqueFiles))
			for i, f := range uniqueFiles {
				args[i] = f
			}
			placeholders := strings.Repeat("?,", len(uniqueFiles)-1) + "?"
			query := `SELECT file, id FROM media_files WHERE file IN (` + placeholders + `)`
			if err := d.View(func(q Querier) error {
				qrows, err := q.Query(query, args...)
				if err != nil {
					return err
				}
				defer qrows.Close()
				for qrows.Next() {
					var f string
					var id int64
					if err := qrows.Scan(&f, &id); err != nil {
						return err
					}
					fileToID[f] = id
				}
				return qrows.Err()
			}); err != nil {
				return nil, err
			}
		}
	}
	for i, it := range items {
		rows[i] = it.row
		if len(it.photoFiles) > 0 && len(fileToID) > 0 {
			for _, f := range it.photoFiles {
				if id, ok := fileToID[f]; ok && id > 0 {
					rows[i].PhotoIDs = append(rows[i].PhotoIDs, id)
				}
			}
		}
	}
	return rows, nil
}

// DiaryModMaxIDs returns the highest numeric DiaryNo and CommentNo currently in memory.
func DiaryModMaxIDs() (maxPost, maxComment int64) {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	for _, p := range DiaryPosts {
		if n, ok := parseDiaryNo(p.DiaryNo); ok && n > maxPost {
			maxPost = n
		}
	}
	for _, c := range DiaryComments {
		if n, ok := parseDiaryNo(c.CommentNo); ok && n > maxComment {
			maxComment = n
		}
	}
	return maxPost, maxComment
}

// DiaryModSearch searches in-memory DiaryPosts and DiaryComments matching f, ordered newest-first,
// returning the requested [offset : offset+limit] slice, total matches, and whether more follow.
// PhotoIDs are resolved for the returned page only in a single SQLite query after releasing locks.
func DiaryModSearch(f DiaryModFilter, offset, limit int) (rows []DiaryModRow, total int, hasMore bool, err error) {
	if limit < 1 || limit > 200 || offset < 0 {
		return nil, 0, false, fmt.Errorf("%w: invalid diary search offset or limit", ErrBadInput)
	}
	AccountsMu.Lock()
	SocialMu.Lock()
	m := newDiaryModMatcher(f)
	var items []diaryModItem
	if f.Posts {
		for _, p := range DiaryPosts {
			if it, ok := m.matchPost(p); ok {
				items = append(items, it)
			}
		}
	}
	if f.Comments {
		for _, c := range DiaryComments {
			if it, ok := m.matchComment(c); ok {
				items = append(items, it)
			}
		}
	}
	SocialMu.Unlock()
	AccountsMu.Unlock()

	slices.SortFunc(items, compareDiaryModNewestFirst)
	total = len(items)
	if offset >= total {
		return []DiaryModRow{}, total, false, nil
	}
	end := min(offset+limit, total)
	page := items[offset:end]
	hasMore = end < total
	rows, err = resolveDiaryPagePhotos(page)
	if err != nil {
		return nil, 0, false, err
	}
	return rows, total, hasMore, nil
}

// DiaryModBefore returns up to limit items matching f that fall strictly after (beforeTime, beforeNo, beforeKind)
// in the newest-first sort order, along with whether more items follow.
func DiaryModBefore(f DiaryModFilter, beforeTime, beforeNo int64, beforeKind string, limit int) (rows []DiaryModRow, hasMore bool, err error) {
	if limit < 1 || limit > 200 || beforeTime < 0 || beforeNo < 0 || (beforeKind != "post" && beforeKind != "comment") {
		return nil, false, fmt.Errorf("%w: invalid diary cursor or limit", ErrBadInput)
	}
	cursor := diaryModItem{
		row:     DiaryModRow{Time: beforeTime, No: strconv.FormatInt(beforeNo, 10), Kind: beforeKind},
		noNum:   beforeNo,
		noValid: true,
	}
	AccountsMu.Lock()
	SocialMu.Lock()
	m := newDiaryModMatcher(f)
	var items []diaryModItem
	if f.Posts {
		for _, p := range DiaryPosts {
			if it, ok := m.matchPost(p); ok && compareDiaryModNewestFirst(it, cursor) > 0 {
				items = append(items, it)
			}
		}
	}
	if f.Comments {
		for _, c := range DiaryComments {
			if it, ok := m.matchComment(c); ok && compareDiaryModNewestFirst(it, cursor) > 0 {
				items = append(items, it)
			}
		}
	}
	SocialMu.Unlock()
	AccountsMu.Unlock()

	slices.SortFunc(items, compareDiaryModNewestFirst)
	hasMore = len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	rows, err = resolveDiaryPagePhotos(items)
	if err != nil {
		return nil, false, err
	}
	return rows, hasMore, nil
}

// DiaryModAfter returns newly created matching posts (with numeric DiaryNo > afterPost) and comments (with numeric CommentNo > afterComment),
// each capped at limit, combined and sorted ascending (Time ASC, NoNum ASC, Kind ASC) for real-time prepending.
func DiaryModAfter(f DiaryModFilter, afterPost, afterComment int64, limit int) (rows []DiaryModRow, latestPost, latestComment int64, hasMore bool, err error) {
	if limit < 1 || limit > 200 || afterPost < 0 || afterComment < 0 {
		return nil, 0, 0, false, fmt.Errorf("%w: invalid diary after cursor or limit", ErrBadInput)
	}
	AccountsMu.Lock()
	SocialMu.Lock()
	m := newDiaryModMatcher(f)
	var maxPost, maxComment int64
	var postItems []diaryModItem
	for _, p := range DiaryPosts {
		if n, ok := parseDiaryNo(p.DiaryNo); ok {
			if n > maxPost {
				maxPost = n
			}
			if n > afterPost {
				if it, matched := m.matchPost(p); matched {
					postItems = append(postItems, it)
				}
			}
		}
	}
	var commentItems []diaryModItem
	for _, c := range DiaryComments {
		if n, ok := parseDiaryNo(c.CommentNo); ok {
			if n > maxComment {
				maxComment = n
			}
			if n > afterComment {
				if it, matched := m.matchComment(c); matched {
					commentItems = append(commentItems, it)
				}
			}
		}
	}
	SocialMu.Unlock()
	AccountsMu.Unlock()

	slices.SortFunc(postItems, func(a, b diaryModItem) int {
		return cmp.Compare(a.noNum, b.noNum)
	})
	slices.SortFunc(commentItems, func(a, b diaryModItem) int {
		return cmp.Compare(a.noNum, b.noNum)
	})

	postsCapped := len(postItems) > limit
	if postsCapped {
		postItems = postItems[:limit]
		latestPost = postItems[len(postItems)-1].noNum
	} else {
		latestPost = max(afterPost, maxPost)
	}
	commentsCapped := len(commentItems) > limit
	if commentsCapped {
		commentItems = commentItems[:limit]
		latestComment = commentItems[len(commentItems)-1].noNum
	} else {
		latestComment = max(afterComment, maxComment)
	}
	hasMore = postsCapped || commentsCapped

	combined := make([]diaryModItem, 0, len(postItems)+len(commentItems))
	combined = append(combined, postItems...)
	combined = append(combined, commentItems...)
	slices.SortFunc(combined, func(a, b diaryModItem) int {
		return compareDiaryModNewestFirst(b, a)
	})

	rows, err = resolveDiaryPagePhotos(combined)
	if err != nil {
		return nil, 0, 0, false, err
	}
	return rows, latestPost, latestComment, hasMore, nil
}
