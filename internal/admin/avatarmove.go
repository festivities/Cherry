package admin

import (
	"net/http"
	"strconv"
	"strings"

	"cherry/internal/store"
)

// The owner's avatar actions (2026-11-21, user: a player who comes back on a new Discord account gets the old avatar assigned to
// it, and a deleted avatar can be restored): move a live avatar to another identity from its player page, and restore a deleted
// avatar from its record page. Both are owner only: the review pages answer 403 to anyone else and the confirm POSTs are privileged
// (a fresh Discord sign-in). Review first; the confirm form takes the target identity again and the avatar id typed back.

// avatarSkipMax is how many skipped rows the restore review lists.
const avatarSkipMax = 50

// avatarPage is what the move and restore review pages show: the form's identity, the refusal (empty when the action may go on),
// the plan (the store's), and the confirm form's target.
type avatarPage struct {
	Player      store.Player      // the source account (move) or its record's avatar (restore, from the record)
	To          string            // the identity in the form
	Refusal     string            // why the action is refused; "" when it may go on
	Confirm     bool              // the confirm form is shown
	Action      string            // the POST path of the confirm form
	Move        store.MovePlan    // the move's plan
	Restore     store.RestorePlan // the restore's plan
	Skipped     []store.RestoreSkip
	SkippedMore int
}

// avatarOwner answers 403 to anyone but the owner and reports whether the request may go on.
func (s *Server) avatarOwner(w http.ResponseWriter, sess session) bool {
	if !s.isOwner(sess) {
		s.logger.Printf("cherry: avatar move or restore refused for a non-owner")
		s.fail(w, http.StatusForbidden, "Only the owner can move or restore an avatar.")
		return false
	}
	return true
}

// identityForm is the identity a form names: a Discord user id typed as digits gets the "discord:" prefix.
func identityForm(v string) string {
	v = strings.TrimSpace(v)
	if bareDiscordID(v) {
		return "discord:" + v
	}
	return v
}

// refusalText is the reason of a refused action, "" for any other error (which the caller reports as a store error).
func refusalText(err error) string {
	if store.IsRefused(err) {
		return err.Error()
	}
	return ""
}

// recordID is the deleted avatar's record id in the path.
func (s *Server) recordID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.fail(w, http.StatusBadRequest, "The deleted avatar's number is not valid.")
		return 0, false
	}
	return id, true
}

// handleMoveReview: GET /players/{id}/move, the review of a move to the identity in the query (to). Without one, the page asks for it.
func (s *Server) handleMoveReview(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.avatarOwner(w, sess) {
		return
	}
	id, ok := s.playerID(w, r)
	if !ok {
		return
	}
	p, found := store.PlayerByID(id)
	if !found {
		s.storeError(w, store.ErrNoPlayer)
		return
	}
	page := avatarPage{Player: p, To: identityForm(r.URL.Query().Get("to")), Action: playerPath(id) + "/avatar/move"}
	if page.To != "" {
		plan, err := store.PlanMove(id, page.To)
		if err != nil && !store.IsRefused(err) {
			s.storeError(w, err)
			return
		}
		page.Move, page.Refusal = plan, refusalText(err)
		page.Confirm = page.Refusal == ""
	}
	v := s.base(r, sess, "Move avatar #"+strconv.FormatInt(id, 10))
	v.Crumbs = []link{{Href: "/players", Label: "Players"}, {Href: playerPath(id), Label: "#" + strconv.FormatInt(id, 10)}, {Label: "Move avatar"}}
	v.Avatar = page
	s.render(w, http.StatusOK, "avatarmove", v)
}

// handleMoveConfirm: POST /players/{id}/move (privileged, owner). The form: to (the identity) and confirm (the avatar id typed back).
func (s *Server) handleMoveConfirm(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.avatarOwner(w, sess) {
		return
	}
	id, ok := s.playerID(w, r)
	if !ok {
		return
	}
	to := identityForm(r.PostFormValue("to"))
	plan, err := store.PlanMove(id, to)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if strings.TrimSpace(r.PostFormValue("confirm")) != plan.Player.Aid {
		s.fail(w, http.StatusBadRequest, "Type the avatar id "+plan.Player.Aid+" to confirm the move.")
		return
	}
	if err := store.MoveAvatar(id, to, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, playerNotice(id, "avatar-moved"))
}

// handleRestoreReview: GET /players/deleted/{id}/restore, the review of a restore to the identity in the query (to). Without one, the
// record's own identity is the target.
func (s *Server) handleRestoreReview(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.avatarOwner(w, sess) {
		return
	}
	recID, ok := s.recordID(w, r)
	if !ok {
		return
	}
	to := identityForm(r.URL.Query().Get("to"))
	det, found, err := store.DeletedAvatarByID(recID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !found {
		s.storeError(w, store.ErrNoRecord)
		return
	}
	if to == "" {
		to = det.Identity
	}
	plan, err := store.PlanRestore(recID, to)
	if err != nil && !store.IsRefused(err) {
		s.storeError(w, err)
		return
	}
	page := avatarPage{To: to, Restore: plan, Action: deletedPath(recID) + "/restore", Refusal: refusalText(err)}
	page.Confirm = page.Refusal == ""
	page.Skipped, page.SkippedMore = plan.Skipped, 0
	if len(page.Skipped) > avatarSkipMax {
		page.SkippedMore = len(page.Skipped) - avatarSkipMax
		page.Skipped = page.Skipped[:avatarSkipMax]
	}
	v := s.base(r, sess, "Restore avatar #"+det.Aid)
	v.Crumbs = []link{{Href: "/players", Label: "Players"}, {Href: "/players/deleted", Label: "Deleted avatars"}, {Href: deletedPath(recID), Label: "#" + strconv.FormatInt(recID, 10)}, {Label: "Restore"}}
	v.Avatar = page
	s.render(w, http.StatusOK, "avatarrestore", v)
}

// handleRestoreConfirm: POST /players/deleted/{id}/restore (privileged, owner). The form: to and confirm (the avatar id typed back).
func (s *Server) handleRestoreConfirm(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.avatarOwner(w, sess) {
		return
	}
	recID, ok := s.recordID(w, r)
	if !ok {
		return
	}
	to := identityForm(r.PostFormValue("to"))
	plan, err := store.PlanRestore(recID, to)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if strings.TrimSpace(r.PostFormValue("confirm")) != plan.Record.Aid {
		s.fail(w, http.StatusBadRequest, "Type the avatar id "+plan.Record.Aid+" to confirm the restore.")
		return
	}
	if _, err := store.RestoreAvatar(recID, to, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, deletedPath(recID)+"?notice=avatar-restored")
}
