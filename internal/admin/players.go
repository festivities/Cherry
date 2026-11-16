package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// The player pages (PLAN P4, P11; slices 8.7 parts 2a and 2b): a search over the accounts, one player with its newest ledger
// lines and its own writing, the grant, ban and unban actions, the rename, and the removal of a post, guestbook entry or
// comment the player wrote. A player is addressed by its store row id, which stays with the account; the avatar id is "0"
// until the avatar is made.

const (
	playerQueryMax = 100 // characters of a search
	ledgerPage     = 20  // ledger lines on a player page
	writingPage    = 20  // posts, guestbook entries and comments of each kind on a player page
	mediaPage      = 20  // pictures on a player page
	writingText    = 80  // characters of a post's or comment's text in a list
)

// playerPost, playerEntry and playerComment are the rows of the player page's lists: the text cut, the time shown in UTC.
type playerPost struct {
	No, Date, Text string
	Photo          bool
}

type playerEntry struct {
	No, Host, Date, Text string
}

type playerComment struct {
	No, Host, Post, Date, Text string
}

// playerMedia is a row of the player's pictures: the kind and the size in KiB; the picture itself is never shown.
type playerMedia struct {
	ID         int64
	Kind, Date string
	KiB        int64
}

// playerPath is the page of one player.
func playerPath(id int64) string { return "/players/" + strconv.FormatInt(id, 10) }

// playerNotice is the page of one player with the flash code of the action that was just taken (see notices).
func playerNotice(id int64, code string) string { return playerPath(id) + "?notice=" + code }

// playerID reads the {id} of a player route; ok is false (and the reply written) for a malformed one.
func (s *Server) playerID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.fail(w, http.StatusBadRequest, "The player id is not valid.")
		return 0, false
	}
	return id, true
}

// handlePlayers: the search form and, for a non-empty search, its rows (at most 50).
func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request, sess session) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(query) > playerQueryMax {
		s.fail(w, http.StatusBadRequest, "The search is too long.")
		return
	}
	v := s.base(r, sess, "Players")
	v.Query = query
	v.Players, v.More = store.FindPlayers(query)
	s.render(w, http.StatusOK, "players", v)
}

// mediaRows turns the player's pictures into the rows of the page.
func mediaRows(files []store.MediaFile) []playerMedia {
	rows := []playerMedia{}
	for _, f := range files {
		rows = append(rows, playerMedia{ID: f.ID, Kind: f.Kind, KiB: (f.Size + 1023) / 1024, Date: rfcDate(f.Created)})
	}
	return rows
}

// rfcDate shows a stored RFC 3339 time in UTC; a value that does not parse is shown as it is.
func rfcDate(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// writingRows turns the player's own writing into the rows of the page.
func writingRows(w store.PlayerWriting) ([]playerPost, []playerEntry, []playerComment) {
	posts := []playerPost{}
	for _, p := range w.Posts {
		posts = append(posts, playerPost{No: p.DiaryNo, Date: msDate(p.RegDate), Text: cutText(p.Content, writingText), Photo: len(p.Images) > 0})
	}
	entries := []playerEntry{}
	for _, e := range w.Guestbook {
		entries = append(entries, playerEntry{No: e.GuestHistNo, Host: e.HostAvtNo, Date: msDate(e.RegYmdt), Text: cutText(e.Content, writingText)})
	}
	comments := []playerComment{}
	for _, c := range w.Comments {
		comments = append(comments, playerComment{No: c.CommentNo, Host: c.OwnerAvatarID, Post: c.DiaryNo, Date: msDate(c.Date), Text: cutText(c.Content, writingText)})
	}
	return posts, entries, comments
}

// msDate shows a stored time (Unix milliseconds, as the game writes it) in UTC; a value that is not a number is shown as it is.
func msDate(ms string) string {
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return ms
	}
	return time.UnixMilli(n).UTC().Format("2006-01-02 15:04")
}

// cutText is the first n characters of s.
func cutText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// handleGrant: POST /players/{id}/grant (privileged). The form: currency (a grantKinds value), item (the Garden material's
// item id, for "material"), delta (a signed whole number) and reason. The economy checks the values and writes the balances,
// the ledger lines and the audit row in one save.
func (s *Server) handleGrant(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.playerID(w, r)
	if !ok {
		return
	}
	delta, err := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("delta")), 10, 64)
	if err != nil {
		s.fail(w, http.StatusBadRequest, "The amount must be a whole number.")
		return
	}
	g, err := grantOf(r.PostFormValue("currency"), r.PostFormValue("item"), delta)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if err := economy.StaffGrantPlayer(id, g, r.PostFormValue("reason"), sess.identity, economy.NowFunc()); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, playerNotice(id, "granted"))
}

// handleBan: POST /players/{id}/ban (privileged). The form: reason.
func (s *Server) handleBan(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.playerID(w, r)
	if !ok {
		return
	}
	if err := store.BanPlayer(id, r.PostFormValue("reason"), sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, playerNotice(id, "banned"))
}

// handleUnban: POST /players/{id}/unban (privileged).
func (s *Server) handleUnban(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.playerID(w, r)
	if !ok {
		return
	}
	if err := store.UnbanPlayer(id, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, playerNotice(id, "unbanned"))
}

// handleRename: POST /players/{id}/rename (privileged). The form: name (the in-game nickname rule) and reason.
func (s *Server) handleRename(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.playerID(w, r)
	if !ok {
		return
	}
	if err := store.RenamePlayer(id, r.PostFormValue("name"), r.PostFormValue("reason"), sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, playerNotice(id, "renamed"))
}

// handleMediaRemove: POST /players/{id}/media/{mid}/remove (privileged). The form: reason. The store clears every reference to the
// picture, deletes its row and file, and writes the audit row in one save.
func (s *Server) handleMediaRemove(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.playerID(w, r)
	if !ok {
		return
	}
	mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil || mid < 1 {
		s.fail(w, http.StatusBadRequest, "The picture number is not valid.")
		return
	}
	if err := store.RemovePlayerMedia(id, mid, r.PostFormValue("reason"), sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, playerNotice(id, "picture-removed"))
}

// removal is the handler of one removal of the player's own writing (POST /players/{id}/posts/{no}/remove and its
// siblings): the number is the route's {no}, the form is the reason, and notice is the flash code of the removal.
func (s *Server) removal(remove func(id int64, no, reason, actor string) error, notice string) handler {
	return func(w http.ResponseWriter, r *http.Request, sess session) {
		id, ok := s.playerID(w, r)
		if !ok {
			return
		}
		if err := remove(id, r.PathValue("no"), r.PostFormValue("reason"), sess.identity); err != nil {
			s.storeError(w, err)
			return
		}
		redirect(w, http.StatusSeeOther, playerNotice(id, notice))
	}
}
