package admin

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"cherry/internal/store"
)

// The player pages (PLAN P4, P11; slice 8.7 part 2a): a search over the accounts, one player with its newest ledger lines,
// and the grant, ban and unban actions. A player is addressed by its store row id, which stays with the account; the avatar
// id is "0" until the avatar is made.

const (
	playerQueryMax = 100 // characters of a search
	ledgerPage     = 20  // ledger lines on a player page
)

// playerPath is the page of one player.
func playerPath(id int64) string { return "/players/" + strconv.FormatInt(id, 10) }

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
	v := s.base(sess, "Players")
	v.Query = query
	v.Players, v.More = store.FindPlayers(query)
	s.render(w, http.StatusOK, "players", v)
}

// handlePlayer: one player, its newest ledger lines and the actions on it.
func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.playerID(w, r)
	if !ok {
		return
	}
	p, found := store.PlayerByID(id)
	if !found {
		s.storeError(w, store.ErrNoPlayer)
		return
	}
	lines, err := store.PlayerLedger(id, ledgerPage)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(sess, "Player")
	v.Player, v.Ledger = p, lines
	s.render(w, http.StatusOK, "player", v)
}

// handleGrant: POST /players/{id}/grant (privileged). The form: currency (gems or cash), delta (a signed whole number) and
// reason. The store checks the values and writes the balance, its ledger line and the audit row in one save.
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
	if err := store.GrantPlayer(id, r.PostFormValue("currency"), delta, r.PostFormValue("reason"), sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, playerPath(id))
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
	redirect(w, http.StatusSeeOther, playerPath(id))
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
	redirect(w, http.StatusSeeOther, playerPath(id))
}
