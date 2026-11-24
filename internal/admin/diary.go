package admin

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"cherry/internal/store"
)

const (
	diaryPageSize = 50
	diaryMaxPage  = 1_000_000
)

type diaryPage struct {
	Filter          store.DiaryModFilter
	Rows            []diaryRow
	Total           int
	Start           int
	End             int
	NewerHref       string
	OlderHref       string
	NewestHref      string
	Live            bool
	LiveHref        string
	Query           string
	NewestPostID    int64
	NewestCommentID int64
	OldestTime      int64
	OldestNo        int64
	OldestKind      string
	HasMore         bool
}

type diaryRow struct {
	Kind           string  `json:"kind"`
	No             string  `json:"no"`
	NoNum          int64   `json:"no_num"`
	PostNo         string  `json:"post_no"`
	Time           string  `json:"time"`
	TimeMs         int64   `json:"time_ms"`
	AuthorAid      string  `json:"author_aid"`
	AuthorName     string  `json:"author_name"`
	AuthorHref     string  `json:"author_href"`
	AuthorPlayerID int64   `json:"author_player_id"`
	OwnerAid       string  `json:"owner_aid"`
	OwnerName      string  `json:"owner_name"`
	OwnerHref      string  `json:"owner_href"`
	Title          string  `json:"title"`
	Content        string  `json:"content"`
	OpenState      string  `json:"open_state"`
	HasPhotos      bool    `json:"has_photos"`
	PhotoIDs       []int64 `json:"photo_ids"`
}

func diaryFilterOf(q url.Values) (store.DiaryModFilter, bool, string) {
	explicit := q.Get("filter") == "1"
	f := store.DiaryModFilter{
		Posts:      true,
		Comments:   true,
		WithPhotos: true,
		TextOnly:   true,
		Player:     strings.TrimSpace(q.Get("player")),
		Text:       strings.TrimSpace(q.Get("text")),
	}
	if explicit {
		f.Posts = q.Get("posts") == "1"
		f.Comments = q.Get("comments") == "1"
		f.WithPhotos = q.Get("photos") == "1"
		f.TextOnly = q.Get("textonly") == "1"
	}
	if utf8.RuneCountInString(f.Player) > playerQueryMax || utf8.RuneCountInString(f.Text) > chatTextMax {
		return f, explicit, "The player and text filters are at most 100 characters."
	}
	return f, explicit, ""
}

func diaryFilterValues(f store.DiaryModFilter, explicit bool) url.Values {
	q := url.Values{}
	if explicit || !(f.Posts && f.Comments && f.WithPhotos && f.TextOnly) {
		q.Set("filter", "1")
		if f.Posts {
			q.Set("posts", "1")
		}
		if f.Comments {
			q.Set("comments", "1")
		}
		if f.WithPhotos {
			q.Set("photos", "1")
		}
		if f.TextOnly {
			q.Set("textonly", "1")
		}
	}
	if f.Player != "" {
		q.Set("player", f.Player)
	}
	if f.Text != "" {
		q.Set("text", f.Text)
	}
	return q
}

func diaryHref(f store.DiaryModFilter, explicit bool, page int) string {
	q := diaryFilterValues(f, explicit)
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "/diary"
	}
	return "/diary?" + q.Encode()
}

func diaryRows(in []store.DiaryModRow) []diaryRow {
	rows := make([]diaryRow, 0, len(in))
	for _, r := range in {
		ts := r.RawDate
		if r.Time > 0 {
			ts = unixMsDate(r.Time)
		}
		noNum, err := strconv.ParseInt(r.No, 10, 64)
		if err != nil || noNum < 0 {
			noNum = 0
		}
		photoIDs := r.PhotoIDs
		if photoIDs == nil {
			photoIDs = []int64{}
		}
		authorName := r.AuthorName
		var authorHref string
		if r.AuthorPlayerID > 0 {
			authorHref = playerPath(r.AuthorPlayerID)
			if authorName == "" {
				authorName = r.AuthorAid
			}
		}
		ownerName := r.OwnerName
		var ownerHref string
		if r.OwnerPlayerID > 0 {
			ownerHref = playerPath(r.OwnerPlayerID)
			if ownerName == "" {
				ownerName = r.OwnerAid
			}
		}
		rows = append(rows, diaryRow{
			Kind:           r.Kind,
			No:             r.No,
			NoNum:          noNum,
			PostNo:         r.PostNo,
			Time:           ts,
			TimeMs:         r.Time,
			AuthorAid:      r.AuthorAid,
			AuthorName:     authorName,
			AuthorHref:     authorHref,
			AuthorPlayerID: r.AuthorPlayerID,
			OwnerAid:       r.OwnerAid,
			OwnerName:      ownerName,
			OwnerHref:      ownerHref,
			Title:          r.Title,
			Content:        r.Content,
			OpenState:      r.OpenState,
			HasPhotos:      r.HasPhotos,
			PhotoIDs:       photoIDs,
		})
	}
	return rows
}

// handleDiary: GET /diary. Moderation view over diary entries and diary comments, newest first, with inline photos.
func (s *Server) handleDiary(w http.ResponseWriter, r *http.Request, sess session) {
	q := r.URL.Query()
	page := 1
	if raw := q["page"]; len(raw) > 0 {
		n, err := strconv.Atoi(raw[0])
		if len(raw) != 1 || err != nil || n < 1 || n > diaryMaxPage {
			s.fail(w, http.StatusBadRequest, "The page number is not valid.")
			return
		}
		page = n
	}
	f, explicit, problem := diaryFilterOf(q)
	if problem != "" {
		s.fail(w, http.StatusBadRequest, problem)
		return
	}
	// Read max IDs before DiaryModSearch so NewestPostID/NewestCommentID never exceed the search snapshot.
	maxPost, maxComment := store.DiaryModMaxIDs()
	res, total, hasMore, err := store.DiaryModSearch(f, (page-1)*diaryPageSize, diaryPageSize)
	if err != nil {
		s.storeError(w, err)
		return
	}
	rows := diaryRows(res)
	newestPost, newestComment := maxPost, maxComment
	for _, row := range rows {
		if row.Kind == "post" && row.NoNum > newestPost {
			newestPost = row.NoNum
		} else if row.Kind == "comment" && row.NoNum > newestComment {
			newestComment = row.NoNum
		}
	}
	dp := diaryPage{
		Filter:          f,
		Rows:            rows,
		Total:           total,
		NewestHref:      diaryHref(f, explicit, 1),
		Live:            page == 1,
		LiveHref:        diaryHref(f, explicit, 1),
		Query:           diaryFilterValues(f, explicit).Encode(),
		NewestPostID:    newestPost,
		NewestCommentID: newestComment,
		HasMore:         hasMore,
	}
	if len(rows) > 0 {
		dp.Start = (page-1)*diaryPageSize + 1
		dp.End = dp.Start + len(rows) - 1
		last := rows[len(rows)-1]
		dp.OldestTime = last.TimeMs
		dp.OldestNo = last.NoNum
		dp.OldestKind = last.Kind
	}
	if page > 1 {
		dp.NewerHref = diaryHref(f, explicit, page-1)
	}
	if hasMore {
		dp.OlderHref = diaryHref(f, explicit, page+1)
	}
	v := s.base(r, sess, "Diary entries")
	v.Scripts = []string{"diary.js"}
	v.Diary = dp
	s.render(w, http.StatusOK, "diary", v)
}

func (s *Server) diaryJSONStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNoDatabase):
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "The database is not available."})
	case errors.Is(err, store.ErrBadInput):
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		s.logger.Printf("cherry: staff diary json failed: %v", err)
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong."})
	}
}

func parseNonNegInt64Param(vals []string) (int64, bool) {
	if len(vals) != 1 {
		return 0, false
	}
	n, err := strconv.ParseInt(vals[0], 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// handleDiaryJSON: GET /diary.json. Live polling (?after_post=&after_comment=) and infinite scroll (?before_time=&before_no=&before_kind=).
func (s *Server) handleDiaryJSON(w http.ResponseWriter, r *http.Request, _ session) {
	q := r.URL.Query()
	f, _, problem := diaryFilterOf(q)
	if problem != "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": problem})
		return
	}
	afterPostRaw, afterCommentRaw := q["after_post"], q["after_comment"]
	beforeTimeRaw, beforeNoRaw, beforeKindRaw := q["before_time"], q["before_no"], q["before_kind"]
	hasAfter := len(afterPostRaw) > 0 || len(afterCommentRaw) > 0
	hasBefore := len(beforeTimeRaw) > 0 || len(beforeNoRaw) > 0 || len(beforeKindRaw) > 0

	switch {
	case hasAfter && !hasBefore:
		afterPost, okP := parseNonNegInt64Param(afterPostRaw)
		afterComment, okC := parseNonNegInt64Param(afterCommentRaw)
		if !okP || !okC {
			respondJSON(w, http.StatusBadRequest, map[string]string{"error": "The after_post and after_comment cursors are not valid."})
			return
		}
		rows, latestPost, latestComment, hasMore, err := store.DiaryModAfter(f, afterPost, afterComment, 100)
		if err != nil {
			s.diaryJSONStoreError(w, err)
			return
		}
		respondJSON(w, http.StatusOK, struct {
			Rows          []diaryRow `json:"rows"`
			LatestPost    int64      `json:"latest_post"`
			LatestComment int64      `json:"latest_comment"`
			HasMore       bool       `json:"has_more"`
		}{
			Rows:          diaryRows(rows),
			LatestPost:    latestPost,
			LatestComment: latestComment,
			HasMore:       hasMore,
		})
	case hasBefore && !hasAfter:
		beforeTime, okT := parseNonNegInt64Param(beforeTimeRaw)
		beforeNo, okN := parseNonNegInt64Param(beforeNoRaw)
		if !okT || !okN || len(beforeKindRaw) != 1 || (beforeKindRaw[0] != "post" && beforeKindRaw[0] != "comment") {
			respondJSON(w, http.StatusBadRequest, map[string]string{"error": "The before_time, before_no and before_kind cursors are not valid."})
			return
		}
		rows, hasMore, err := store.DiaryModBefore(f, beforeTime, beforeNo, beforeKindRaw[0], diaryPageSize)
		if err != nil {
			s.diaryJSONStoreError(w, err)
			return
		}
		respondJSON(w, http.StatusOK, struct {
			Rows    []diaryRow `json:"rows"`
			HasMore bool       `json:"has_more"`
		}{
			Rows:    diaryRows(rows),
			HasMore: hasMore,
		})
	default:
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Specify either after_post and after_comment, or before_time, before_no and before_kind."})
	}
}

func (s *Server) diaryRemoval(remove func(id int64, no, reason, actor string) error, notice string) handler {
	return func(w http.ResponseWriter, r *http.Request, sess session) {
		pid, err := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("player_id")), 10, 64)
		if err != nil || pid < 1 {
			s.fail(w, http.StatusBadRequest, "The player id is not valid.")
			return
		}
		no := strings.TrimSpace(r.PostFormValue("no"))
		if len(no) == 0 || len(no) > 40 || !allDigits(no) {
			s.fail(w, http.StatusBadRequest, "The item number is not valid.")
			return
		}
		if err := remove(pid, no, r.PostFormValue("reason"), sess.identity); err != nil {
			s.storeError(w, err)
			return
		}
		redirect(w, http.StatusSeeOther, "/diary?notice="+notice)
	}
}

// handleDiaryRemovePost: POST /diary/remove-post (privileged).
func (s *Server) handleDiaryRemovePost(w http.ResponseWriter, r *http.Request, sess session) {
	s.diaryRemoval(store.RemovePlayerPost, "post-removed")(w, r, sess)
}

// handleDiaryRemoveComment: POST /diary/remove-comment (privileged).
func (s *Server) handleDiaryRemoveComment(w http.ResponseWriter, r *http.Request, sess session) {
	s.diaryRemoval(store.RemovePlayerComment, "comment-removed")(w, r, sess)
}
