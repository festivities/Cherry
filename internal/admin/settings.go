package admin

import (
	"fmt"
	"math"
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
	ChatDays                int           // the retention in effect
	FromPanel               bool          // the retention is the panel's value (else the server's configuration)
	ConfigDays              int           // the server's configured retention, the value the reset returns to
	Owner                   string        // the owner's identity, from the server's configuration
	Members                 []settingsRow // the staff members added here, in the order they were added
	Allowlist               bool          // a staff allowlist file is configured as well
	DevNames                bool          // dev-fake: the add field also takes dev-<name>
	RoomCap                 int           // the players a Square room holds in effect
	RoomCapFromPanel        bool          // the room size is the panel's value (else the default)
	RoomCapDefault          int           // the default the reset returns to
	UploadTotalCapMiB       string        // total photo storage cap in effect, formatted in MiB
	UploadTotalCapFromPanel bool          // the total cap is the panel's value
	UploadTotalCapConfigMiB string        // server's configured total cap in MiB
	UploadFileCapMiB        string        // max filesize per upload in effect, formatted in MiB
	UploadFileCapFromPanel  bool          // the max upload filesize is the panel's value
	UploadFileCapConfigMiB  string        // server's configured max upload filesize in MiB
	MaxPlayers              int           // concurrent player cap in effect (0 = unlimited)
	MaxPlayersFromPanel     bool          // the concurrent player cap is the panel's value
	MaxPlayersConfig        int           // server's configured concurrent player cap (0 = unlimited)
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
	roomCap, roomSet, err := store.SquareRoomCapSetting()
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !roomSet {
		roomCap = store.SquareRoomCapDefault
	}
	totalConfig := s.cfg.EffectiveUploadMaxTotalBytes()
	totalBytes, totalSet, err := store.UploadMaxTotalBytesSetting()
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !totalSet {
		totalBytes = totalConfig
	}
	fileConfig := s.cfg.EffectiveUploadMaxFileBytes()
	fileBytes, fileSet, err := store.UploadMaxFileBytesSetting()
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !fileSet {
		fileBytes = fileConfig
	}
	playersConfig := s.cfg.EffectiveMaxPlayers()
	players, playersSet, err := store.MaxPlayersSetting()
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !playersSet {
		players = playersConfig
	}
	v := s.base(r, sess, "Settings")
	v.Settings = settingsPage{ChatDays: days, FromPanel: set, ConfigDays: s.cfg.ChatDays, Owner: s.owner, Members: rows,
		Allowlist: s.cfg.StaffAllowlistFile != "", DevNames: s.cfg.AuthMode == config.AuthDevFake,
		RoomCap: roomCap, RoomCapFromPanel: roomSet, RoomCapDefault: store.SquareRoomCapDefault,
		UploadTotalCapMiB: formatMiB(totalBytes), UploadTotalCapFromPanel: totalSet, UploadTotalCapConfigMiB: formatMiB(totalConfig),
		UploadFileCapMiB: formatMiB(fileBytes), UploadFileCapFromPanel: fileSet, UploadFileCapConfigMiB: formatMiB(fileConfig),
		MaxPlayers: players, MaxPlayersFromPanel: playersSet, MaxPlayersConfig: playersConfig}
	s.render(w, http.StatusOK, "settings", v)
}

// handleSquareRoomCap: POST /settings/square-room-cap (privileged, owner). The form: players, 2 to 100 (the store refuses
// anything else). A lower size never moves anyone already inside a room.
func (s *Server) handleSquareRoomCap(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("players")))
	if err != nil {
		s.fail(w, http.StatusBadRequest, "A Square room holds a whole number of players, from 2 to 100.")
		return
	}
	if err := store.SetSquareRoomCap(n, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=square-room-cap-saved")
}

// handleSquareRoomCapReset: POST /settings/square-room-cap/reset (privileged, owner): the default room size applies again.
func (s *Server) handleSquareRoomCapReset(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	if err := store.ClearSquareRoomCap(sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=square-room-cap-reset")
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

func formatMiB(b int64) string {
	s := fmt.Sprintf("%.3f", float64(b)/float64(1<<20))
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

func parseMiBBytes(raw string) (int64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > float64(store.UploadMaxTotalBytesMax)/float64(1<<20)+1 {
		return 0, false
	}
	return int64(math.Round(v * (1 << 20))), true
}

// handleUploadTotalCap: POST /settings/upload-total-cap (privileged, owner). Form field: mib, 1 MiB to 100 GiB.
func (s *Server) handleUploadTotalCap(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	n, ok := parseMiBBytes(r.PostFormValue("mib"))
	if !ok {
		s.fail(w, http.StatusBadRequest, "The total photo storage cap must be a number in MiB from 1 to 102400.")
		return
	}
	if err := store.SetUploadMaxTotalBytes(n, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-total-cap-saved")
}

// handleUploadTotalCapReset: POST /settings/upload-total-cap/reset (privileged, owner).
func (s *Server) handleUploadTotalCapReset(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	if err := store.ClearUploadMaxTotalBytes(sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-total-cap-reset")
}

// handleUploadFileCap: POST /settings/upload-file-cap (privileged, owner). Form field: mib, 100 KiB to 100 MiB.
func (s *Server) handleUploadFileCap(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	n, ok := parseMiBBytes(r.PostFormValue("mib"))
	if !ok {
		s.fail(w, http.StatusBadRequest, "The maximum upload filesize must be a number in MiB from 0.098 (100 KiB) to 100.")
		return
	}
	if err := store.SetUploadMaxFileBytes(n, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-file-cap-saved")
}

// handleUploadFileCapReset: POST /settings/upload-file-cap/reset (privileged, owner).
func (s *Server) handleUploadFileCapReset(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	if err := store.ClearUploadMaxFileBytes(sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-file-cap-reset")
}

// handleMaxPlayers: POST /settings/max-players (privileged, owner). Form field: players, 0 to 10000 (0 = unlimited).
func (s *Server) handleMaxPlayers(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("players")))
	if err != nil {
		s.fail(w, http.StatusBadRequest, "The concurrent player cap must be a whole number from 0 to 10000 (0 means unlimited).")
		return
	}
	if err := store.SetMaxPlayers(n, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=max-players-saved")
}

// handleMaxPlayersReset: POST /settings/max-players/reset (privileged, owner).
func (s *Server) handleMaxPlayersReset(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	if err := store.ClearMaxPlayers(sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=max-players-reset")
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
