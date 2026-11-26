package admin

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"cherry/internal/auth"
	"cherry/internal/config"
	"cherry/internal/httpx"
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

// maintWhitelistRow is one account on the maintenance whitelist table.
type maintWhitelistRow struct {
	Aid, Name, Note, Added, By string
}

// settingsPage is what the Settings page shows.
type settingsPage struct {
	ChatDays                  int                 // the retention in effect
	FromPanel                 bool                // the retention is the panel's value (else the server's configuration)
	ConfigDays                int                 // the server's configured retention, the value the reset returns to
	Owner                     string              // the owner's identity, from the server's configuration
	Members                   []settingsRow       // the staff members added here, in the order they were added
	MaintWhitelist            []maintWhitelistRow // accounts on the maintenance whitelist, in insertion order
	Allowlist                 bool                // a staff allowlist file is configured as well
	DevNames                  bool                // dev-fake: the add field also takes dev-<name>
	RoomCap                   int                 // the players a Square room holds in effect
	RoomCapFromPanel          bool                // the room size is the panel's value (else the default)
	RoomCapDefault            int                 // the default the reset returns to
	UploadTotalCapMiB         string              // total photo storage cap in effect, formatted in MiB
	UploadTotalCapFromPanel   bool                // the total cap is the panel's value
	UploadTotalCapConfigMiB   string              // server's configured total cap in MiB
	UploadQuotaMiB            string              // one player's picture quota (all kinds together) in effect, formatted in MiB
	UploadQuotaFromPanel      bool                // the quota is the panel's value
	UploadQuotaConfigMiB      string              // server's configured quota in MiB
	UploadReceiveCapMiB       string              // max upload receive size in effect, formatted in MiB
	UploadReceiveCapFromPanel bool                // the receive cap is the panel's value
	UploadReceiveCapConfigMiB string              // server's configured receive cap in MiB
	UploadJPEGCapKiB          string              // target JPEG filesize in effect, formatted in KiB
	UploadJPEGCapFromPanel    bool                // the JPEG cap is the panel's value
	UploadJPEGCapConfigKiB    string              // server's configured JPEG cap in KiB
	UploadPNGCapKiB           string              // target transparent PNG filesize in effect, formatted in KiB
	UploadPNGCapFromPanel     bool                // the PNG cap is the panel's value
	UploadPNGCapConfigKiB     string              // server's configured PNG cap in KiB
	MaxPlayers                int                 // concurrent player cap in effect (0 = unlimited)
	MaxPlayersFromPanel       bool                // the concurrent player cap is the panel's value
	MaxPlayersConfig          int                 // server's configured concurrent player cap (0 = unlimited)
	Limits                    []limitRow          // the Server limits table, in store.LimitKeys order
	OpenConns, OpenAddrs      int                 // the game connections open now and the addresses holding them
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
	maintList, err := store.MaintenanceWhitelist()
	if err != nil {
		s.storeError(w, err)
		return
	}
	maintRows := make([]maintWhitelistRow, 0, len(maintList))
	for _, e := range maintList {
		name := ""
		if snap, ok := store.AccountByAvatarID(e.Aid); ok {
			name = snap.Name
		}
		maintRows = append(maintRows, maintWhitelistRow{Aid: e.Aid, Name: name, Note: e.Note, Added: e.Added, By: auth.MaskIdentity(e.By)})
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
	quotaConfig := s.cfg.EffectiveUploadQuotaBytes()
	quotaBytes, quotaSet, err := store.UploadQuotaBytesSetting()
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !quotaSet {
		quotaBytes = quotaConfig
	}
	receiveConfig := s.cfg.EffectiveUploadMaxReceiveBytes()
	receiveBytes, receiveSet, err := store.UploadMaxReceiveBytesSetting()
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !receiveSet {
		receiveBytes = receiveConfig
	}
	jpegConfig := s.cfg.EffectiveUploadMaxJPEGBytes()
	jpegBytes, jpegSet, err := store.UploadMaxJPEGBytesSetting()
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !jpegSet {
		jpegBytes = jpegConfig
	}
	pngConfig := s.cfg.EffectiveUploadMaxPNGBytes()
	pngBytes, pngSet, err := store.UploadMaxPNGBytesSetting()
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !pngSet {
		pngBytes = pngConfig
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
	limits, err := s.limitRows()
	if err != nil {
		s.storeError(w, err)
		return
	}
	open, hosts := httpx.Conns.Counts()
	v := s.base(r, sess, "Settings")
	v.Settings = settingsPage{ChatDays: days, FromPanel: set, ConfigDays: s.cfg.ChatDays, Owner: s.owner, Members: rows,
		MaintWhitelist: maintRows,
		Allowlist:      s.cfg.StaffAllowlistFile != "", DevNames: s.cfg.AuthMode == config.AuthDevFake,
		RoomCap: roomCap, RoomCapFromPanel: roomSet, RoomCapDefault: store.SquareRoomCapDefault,
		UploadTotalCapMiB: formatMiB(totalBytes), UploadTotalCapFromPanel: totalSet, UploadTotalCapConfigMiB: formatMiB(totalConfig),
		UploadQuotaMiB: formatMiB(quotaBytes), UploadQuotaFromPanel: quotaSet, UploadQuotaConfigMiB: formatMiB(quotaConfig),
		UploadReceiveCapMiB: formatMiB(receiveBytes), UploadReceiveCapFromPanel: receiveSet, UploadReceiveCapConfigMiB: formatMiB(receiveConfig),
		UploadJPEGCapKiB: formatKiB(jpegBytes), UploadJPEGCapFromPanel: jpegSet, UploadJPEGCapConfigKiB: formatKiB(jpegConfig),
		UploadPNGCapKiB: formatKiB(pngBytes), UploadPNGCapFromPanel: pngSet, UploadPNGCapConfigKiB: formatKiB(pngConfig),
		MaxPlayers: players, MaxPlayersFromPanel: playersSet, MaxPlayersConfig: playersConfig,
		Limits: limits, OpenConns: open, OpenAddrs: hosts}
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

// handleUploadQuotaBytes: POST /settings/upload-quota-bytes (privileged, owner). Form field: mib, 1 MiB to 10 GiB.
func (s *Server) handleUploadQuotaBytes(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	n, ok := parseMiBBytes(r.PostFormValue("mib"))
	if !ok || n < store.UploadQuotaBytesMin || n > store.UploadQuotaBytesMax {
		s.fail(w, http.StatusBadRequest, "The picture quota per player must be a number in MiB from 1 to 10240 (10 GiB).")
		return
	}
	if err := store.SetUploadQuotaBytes(n, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-quota-bytes-saved")
}

// handleUploadQuotaBytesReset: POST /settings/upload-quota-bytes/reset (privileged, owner).
func (s *Server) handleUploadQuotaBytesReset(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	if err := store.ClearUploadQuotaBytes(sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-quota-bytes-reset")
}

func formatKiB(b int64) string {
	s := fmt.Sprintf("%.3f", float64(b)/float64(1<<10))
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

func parseKiBBytes(raw string) (int64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > float64(store.UploadMaxJPEGBytesMax)/float64(1<<10)+1 {
		return 0, false
	}
	return int64(math.Round(v * (1 << 10))), true
}

// handleUploadReceiveCap: POST /settings/upload-receive-cap (privileged, owner). Form field: mib, 1 to 100 MiB.
func (s *Server) handleUploadReceiveCap(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	n, ok := parseMiBBytes(r.PostFormValue("mib"))
	if !ok {
		s.fail(w, http.StatusBadRequest, "The maximum upload receive size must be a number in MiB from 1 to 100.")
		return
	}
	if err := store.SetUploadMaxReceiveBytes(n, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-receive-cap-saved")
}

// handleUploadReceiveCapReset: POST /settings/upload-receive-cap/reset (privileged, owner).
func (s *Server) handleUploadReceiveCapReset(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	if err := store.ClearUploadMaxReceiveBytes(sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-receive-cap-reset")
}

// handleUploadJPEGCap: POST /settings/upload-jpeg-cap (privileged, owner). Form field: kib, 16 to 102400 KiB.
func (s *Server) handleUploadJPEGCap(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	n, ok := parseKiBBytes(r.PostFormValue("kib"))
	if !ok {
		s.fail(w, http.StatusBadRequest, "The target JPEG filesize must be a number in KiB from 16 to 102400.")
		return
	}
	if err := store.SetUploadMaxJPEGBytes(n, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-jpeg-cap-saved")
}

// handleUploadJPEGCapReset: POST /settings/upload-jpeg-cap/reset (privileged, owner).
func (s *Server) handleUploadJPEGCapReset(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	if err := store.ClearUploadMaxJPEGBytes(sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-jpeg-cap-reset")
}

// handleUploadPNGCap: POST /settings/upload-png-cap (privileged, owner). Form field: kib, 16 to 102400 KiB.
func (s *Server) handleUploadPNGCap(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	n, ok := parseKiBBytes(r.PostFormValue("kib"))
	if !ok {
		s.fail(w, http.StatusBadRequest, "The target transparent PNG filesize must be a number in KiB from 16 to 102400.")
		return
	}
	if err := store.SetUploadMaxPNGBytes(n, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-png-cap-saved")
}

// handleUploadPNGCapReset: POST /settings/upload-png-cap/reset (privileged, owner).
func (s *Server) handleUploadPNGCapReset(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	if err := store.ClearUploadMaxPNGBytes(sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=upload-png-cap-reset")
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

// handleMaintWhitelistAdd: POST /settings/maintenance-whitelist/add (privileged, owner). The form: aid (positive whole
// number) and an optional note.
func (s *Server) handleMaintWhitelistAdd(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	aid := strings.TrimSpace(r.PostFormValue("aid"))
	if err := store.AddMaintenanceWhitelist(aid, r.PostFormValue("note"), sess.identity, s.now()); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=maint-whitelist-added")
}

// handleMaintWhitelistRemove: POST /settings/maintenance-whitelist/remove (privileged, owner). The form: aid.
func (s *Server) handleMaintWhitelistRemove(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	aid := strings.TrimSpace(r.PostFormValue("aid"))
	if err := store.RemoveMaintenanceWhitelist(aid, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/settings?notice=maint-whitelist-removed")
}
