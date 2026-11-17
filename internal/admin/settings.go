package admin

import (
	"net/http"
	"strconv"
	"strings"

	"cherry/internal/auth"
	"cherry/internal/config"
	"cherry/internal/store"
)

// The Settings page (owner only): the chat log's retention in days, which the panel may set over the server's configured value,
// and the staff members the owner adds by Discord user id (or, in dev-fake mode, a dev name). Every change is a privileged POST
// that the owner sends and that writes its audit row (store.SetChatRetention, store.AddStaffMember, store.RemoveStaffMember).
// A staff member is admitted at sign-in (mayStaff); a removal also ends that identity's open sessions (endSessionsOf).

// settingsRow is one staff member of the table: the identity in full (this is the owner's page), the note, when it was added
// and who added it, masked.
type settingsRow struct {
	Identity, Note, Added, By string
}

// settingsPage is what the Settings page shows.
type settingsPage struct {
	ChatDays   int           // the retention in effect
	FromPanel  bool          // the retention is the panel's value (else the server's configuration)
	ConfigDays int           // the server's configured retention, the value the reset returns to
	Owner      string        // the owner's identity, from the server's configuration
	Members    []settingsRow // the staff members added here, in the order they were added
	Allowlist  bool          // a staff allowlist file is configured as well
	DevNames   bool          // dev-fake: the add field also takes dev-<name>
}

// settingsOwner answers the refusal for anyone but the owner and reports whether the request may go on. The log line names
// no value.
func (s *Server) settingsOwner(w http.ResponseWriter, sess session) bool {
	if !s.isOwner(sess) {
		s.logger.Printf("cherry: staff settings refused for a non-owner")
		s.fail(w, http.StatusForbidden, "Only the owner can change the settings.")
		return false
	}
	return true
}

// handleSettings: GET /settings. The owner's page; anyone else gets 403.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	days, set, err := store.ChatRetentionSetting()
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !set {
		days = s.cfg.ChatDays
	}
	members, err := store.StaffMembers()
	if err != nil {
		s.storeError(w, err)
		return
	}
	rows := make([]settingsRow, 0, len(members))
	for _, m := range members {
		rows = append(rows, settingsRow{Identity: m.Identity, Note: m.Note, Added: m.Added, By: auth.MaskIdentity(m.By)})
	}
	v := s.base(r, sess, "Settings")
	v.Settings = settingsPage{ChatDays: days, FromPanel: set, ConfigDays: s.cfg.ChatDays, Owner: s.owner, Members: rows,
		Allowlist: s.cfg.StaffAllowlistFile != "", DevNames: s.cfg.AuthMode == config.AuthDevFake}
	s.render(w, http.StatusOK, "settings", v)
}

// handleChatDays: POST /settings/chat-days (privileged, owner). The form: days, 0 to 3650.
func (s *Server) handleChatDays(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("days")))
	if err != nil {
		s.fail(w, http.StatusBadRequest, "The chat log keeps a whole number of days, from 0 to 3650.")
		return
	}
	if err := store.SetChatRetention(days, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=chat-days-saved")
}

// handleChatDaysReset: POST /settings/chat-days/reset (privileged, owner): the server's configured retention applies again.
func (s *Server) handleChatDaysReset(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	if err := store.ClearChatRetention(sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=chat-days-reset")
}

// bareDiscordID reports whether v is a Discord user id typed as digits alone (the form adds the "discord:" prefix).
func bareDiscordID(v string) bool {
	return v != "" && strings.Trim(v, "0123456789") == ""
}

// handleStaffAdd: POST /settings/staff/add (privileged, owner). The form: identity (a Discord user id, or dev-<name> in dev-fake
// mode) and an optional note. The store checks the identity, the note, the duplicate and the cap.
func (s *Server) handleStaffAdd(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	id := strings.TrimSpace(r.PostFormValue("identity"))
	if bareDiscordID(id) {
		id = "discord:" + id
	}
	if id == s.owner {
		s.fail(w, http.StatusBadRequest, "The owner already has access.")
		return
	}
	devNames := s.cfg.AuthMode == config.AuthDevFake
	if err := store.AddStaffMember(id, r.PostFormValue("note"), sess.identity, s.now(), devNames); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=staff-added")
}

// handleStaffRemove: POST /settings/staff/remove (privileged, owner). The form: identity, a staff member added here. Once the
// removal is audited, that identity's open sessions end.
func (s *Server) handleStaffRemove(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	id := r.PostFormValue("identity")
	if err := store.RemoveStaffMember(id, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	s.endSessionsOf(id)
	redirect(w, http.StatusSeeOther, "/settings?notice=staff-removed")
}
