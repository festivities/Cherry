package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The settings the owner changes from the staff panel (the Settings page): the chat log's retention, the staff members
// added there and the players a Square room holds. All live in the meta table of cherry.db, so they need no schema change.
// Each change writes its audit row in the same transaction as the change. A read that finds no database, or a stored value
// that does not parse, gives the configured answer (retention), no member (staff) or the default room size.

const (
	chatDaysKey              = "retention.chat-days"
	staffMembersKey          = "staff.members"
	squareRoomCapKey         = "square.room-cap"
	uploadMaxTotalBytesKey   = "uploads.max-total-bytes"
	uploadQuotaBytesKey      = "uploads.quota-bytes"
	uploadMaxReceiveBytesKey = "uploads.max-receive-bytes"
	uploadMaxJPEGBytesKey    = "uploads.max-jpeg-bytes"
	uploadMaxPNGBytesKey     = "uploads.max-png-bytes"
	maxPlayersKey            = "limits.max-players"
	// ChatDaysMax is the longest retention the panel takes: ten years.
	ChatDaysMax  = 3650
	staffMax     = 50  // staff members the panel keeps
	staffNoteMax = 100 // characters of a member's note
	// SquareRoomCapDefault is the players a Square room holds when the panel sets no value; the panel takes
	// SquareRoomCapMin to SquareRoomCapMax.
	SquareRoomCapDefault = 20
	SquareRoomCapMin     = 2
	SquareRoomCapMax     = 100

	// UploadMaxTotalBytesMin and UploadMaxTotalBytesMax bound the global photo storage cap (1 MiB to 100 GiB).
	UploadMaxTotalBytesMin int64 = 1 << 20
	UploadMaxTotalBytesMax int64 = 100 << 30
	// UploadQuotaBytesMin and UploadQuotaBytesMax bound the bytes of one player's pictures, all kinds together (1 MiB to 10 GiB).
	UploadQuotaBytesMin int64 = 1 << 20
	UploadQuotaBytesMax int64 = 10 << 30
	// UploadMaxReceiveBytesMin and UploadMaxReceiveBytesMax bound the hard max receive size per upload (1 MiB to 100 MiB).
	UploadMaxReceiveBytesMin int64 = 1 << 20
	UploadMaxReceiveBytesMax int64 = 100 << 20
	// UploadMaxJPEGBytesMin and UploadMaxJPEGBytesMax bound the target JPEG filesize (16 KiB to 100 MiB).
	UploadMaxJPEGBytesMin int64 = 16 << 10
	UploadMaxJPEGBytesMax int64 = 100 << 20
	// UploadMaxPNGBytesMin and UploadMaxPNGBytesMax bound the target transparent PNG filesize (16 KiB to 100 MiB).
	UploadMaxPNGBytesMin int64 = 16 << 10
	UploadMaxPNGBytesMax int64 = 100 << 20
	// MaxPlayersMax is the highest concurrent player cap (0 means unlimited).
	MaxPlayersMax = 10000
)

// The identities a staff member may have: a Discord user id (17 to 20 digits), or in dev-fake mode a dev name (the sign-in
// takes at most 24 characters after "dev-", config.ValidDevName).
var (
	staffDiscordRe = regexp.MustCompile(`^discord:[0-9]{17,20}$`)
	staffDevRe     = regexp.MustCompile(`^dev-[A-Za-z0-9_-]{1,24}$`)
)

// StaffMember is one staff member added from the panel: the identity, the owner's note, when it was added (RFC 3339, UTC) and
// the identity that added it.
type StaffMember struct {
	Identity string `json:"identity"`
	Note     string `json:"note"`
	Added    string `json:"added"`
	By       string `json:"by"`
}

// chatDaysOverride reads the stored retention: the days and whether a valid value is stored. A stored value that is not a
// whole number from 0 to ChatDaysMax counts as no value.
func chatDaysOverride(q Querier) (int, bool, error) {
	v, err := metaValue(q, chatDaysKey)
	if err != nil || v == "" {
		return 0, false, err
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > ChatDaysMax {
		return 0, false, nil
	}
	return n, true, nil
}

// ChatRetention is the days the chat log keeps a message: the panel's value when one is stored, else fallback (the server's
// retention.chat-days). Without a database, or when the read fails, it is fallback.
func ChatRetention(fallback int) int {
	d := attachedDB()
	if d == nil {
		return fallback
	}
	var days int
	var set bool
	if err := d.View(func(q Querier) error {
		var err error
		days, set, err = chatDaysOverride(q)
		return err
	}); err != nil || !set {
		return fallback
	}
	return days
}

// ChatRetentionSetting is the panel's retention as stored: the days and whether one is stored (false means the server's
// configuration applies). ErrNoDatabase without a database.
func ChatRetentionSetting() (int, bool, error) {
	d, err := docDB()
	if err != nil {
		return 0, false, err
	}
	var days int
	var set bool
	err = d.View(func(q Querier) error {
		var err error
		days, set, err = chatDaysOverride(q)
		return err
	})
	return days, set, err
}

// chatDaysBefore is the stored retention as an audit row names it: the value, or "default" when none is stored.
func chatDaysBefore(tx *sql.Tx) (string, error) {
	v, err := metaValue(tx, chatDaysKey)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "default", nil
	}
	return v, nil
}

// SetChatRetention stores days as the chat log's retention and writes the audit row, in one transaction. days is 0 to
// ChatDaysMax (0 keeps every message); anything else is ErrBadInput.
func SetChatRetention(days int, actor string) error {
	if days < 0 || days > ChatDaysMax {
		return fmt.Errorf("%w: the chat log keeps 0 to %d days", ErrBadInput, ChatDaysMax)
	}
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		old, err := chatDaysBefore(tx)
		if err != nil {
			return err
		}
		if err := setMetaTx(tx, chatDaysKey, strconv.Itoa(days)); err != nil {
			return err
		}
		return auditTx(tx, actor, "setting", chatDaysKey, map[string]any{"old": old, "new": strconv.Itoa(days)})
	})
}

// ClearChatRetention deletes the panel's retention, so the server's configuration applies again, and writes the audit row,
// in one transaction.
func ClearChatRetention(actor string) error {
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		old, err := chatDaysBefore(tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM meta WHERE key = ?`, chatDaysKey); err != nil {
			return err
		}
		return auditTx(tx, actor, "setting", chatDaysKey, map[string]any{"old": old, "new": "default"})
	})
}

// squareCapOverride reads the stored room size: the players and whether a valid value is stored. A stored value that is not a
// whole number from SquareRoomCapMin to SquareRoomCapMax counts as no value.
func squareCapOverride(q Querier) (int, bool, error) {
	v, err := metaValue(q, squareRoomCapKey)
	if err != nil || v == "" {
		return 0, false, err
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < SquareRoomCapMin || n > SquareRoomCapMax {
		return 0, false, nil
	}
	return n, true, nil
}

// SquareRoomCap is the players a Square room holds: the panel's value when one is stored, else fallback. Without a database,
// or when the read fails, it is fallback.
func SquareRoomCap(fallback int) int {
	d := attachedDB()
	if d == nil {
		return fallback
	}
	var n int
	var set bool
	if err := d.View(func(q Querier) error {
		var err error
		n, set, err = squareCapOverride(q)
		return err
	}); err != nil || !set {
		return fallback
	}
	return n
}

// SquareRoomCapSetting is the panel's room size as stored: the players and whether one is stored (false means the default
// applies). ErrNoDatabase without a database.
func SquareRoomCapSetting() (int, bool, error) {
	d, err := docDB()
	if err != nil {
		return 0, false, err
	}
	var n int
	var set bool
	err = d.View(func(q Querier) error {
		var err error
		n, set, err = squareCapOverride(q)
		return err
	})
	return n, set, err
}

// squareCapBefore is the stored room size as an audit row names it: the value, or "default" when none is stored.
func squareCapBefore(tx *sql.Tx) (string, error) {
	v, err := metaValue(tx, squareRoomCapKey)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "default", nil
	}
	return v, nil
}

// SetSquareRoomCap stores n as the players a Square room holds and writes the audit row, in one transaction. n is
// SquareRoomCapMin to SquareRoomCapMax; anything else is ErrBadInput. A change never moves anyone already inside a room.
func SetSquareRoomCap(n int, actor string) error {
	if n < SquareRoomCapMin || n > SquareRoomCapMax {
		return fmt.Errorf("%w: a Square room holds %d to %d players", ErrBadInput, SquareRoomCapMin, SquareRoomCapMax)
	}
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		old, err := squareCapBefore(tx)
		if err != nil {
			return err
		}
		if err := setMetaTx(tx, squareRoomCapKey, strconv.Itoa(n)); err != nil {
			return err
		}
		return auditTx(tx, actor, "setting", squareRoomCapKey, map[string]any{"old": old, "new": strconv.Itoa(n)})
	})
}

// ClearSquareRoomCap deletes the panel's room size, so the default applies again, and writes the audit row, in one transaction.
func ClearSquareRoomCap(actor string) error {
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		old, err := squareCapBefore(tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM meta WHERE key = ?`, squareRoomCapKey); err != nil {
			return err
		}
		return auditTx(tx, actor, "setting", squareRoomCapKey, map[string]any{"old": old, "new": "default"})
	})
}

// staffListOf reads the staff list; an absent value is an empty list.
func staffListOf(q Querier) ([]StaffMember, error) {
	v, err := metaValue(q, staffMembersKey)
	if err != nil || v == "" {
		return nil, err
	}
	var list []StaffMember
	if err := json.Unmarshal([]byte(v), &list); err != nil {
		return nil, err
	}
	return list, nil
}

// writeStaffListTx stores the staff list as JSON; an empty list is "[]".
func writeStaffListTx(tx *sql.Tx, list []StaffMember) error {
	if list == nil {
		list = []StaffMember{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return setMetaTx(tx, staffMembersKey, string(b))
}

// StaffMembers returns the staff members added from the panel, in the order they were added. ErrNoDatabase without a database.
func StaffMembers() ([]StaffMember, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	var list []StaffMember
	err = d.View(func(q Querier) error {
		var err error
		list, err = staffListOf(q)
		return err
	})
	return list, err
}

// IsStaffMember reports whether identity was added from the panel. Without a database, or when the list cannot be read, it is
// false.
func IsStaffMember(identity string) bool {
	d, err := docDB()
	if err != nil {
		return false
	}
	found := false
	err = d.View(func(q Querier) error {
		list, err := staffListOf(q)
		found = slices.ContainsFunc(list, func(m StaffMember) bool { return m.Identity == identity })
		return err
	})
	return err == nil && found
}

// AddStaffMember adds identity to the staff list with its note, the time and the actor, and writes the audit row, in one
// transaction. identity is discord:<17 to 20 digits>, or with devNames (dev-fake) dev-<name>. The note is trimmed and at most
// 100 characters. A duplicate, a list of 50 and a bad identity or note are ErrBadInput.
func AddStaffMember(identity, note, actor string, now time.Time, devNames bool) error {
	if !validStaffIdentity(identity, devNames) {
		want := "discord:<17 to 20 digits>"
		if devNames {
			want += " or dev-<name>"
		}
		return fmt.Errorf("%w: the identity is %s", ErrBadInput, want)
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > staffNoteMax {
		return fmt.Errorf("%w: the note is at most %d characters", ErrBadInput, staffNoteMax)
	}
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		list, err := staffListOf(tx)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(list, func(m StaffMember) bool { return m.Identity == identity }) {
			return fmt.Errorf("%w: already a staff member", ErrBadInput)
		}
		if len(list) >= staffMax {
			return fmt.Errorf("%w: at most %d staff members", ErrBadInput, staffMax)
		}
		list = append(list, StaffMember{Identity: identity, Note: note, Added: now.UTC().Format(time.RFC3339), By: actor})
		if err := writeStaffListTx(tx, list); err != nil {
			return err
		}
		return auditTx(tx, actor, "staff-add", "staff", map[string]any{"identity": identity, "note": note})
	})
}

// RemoveStaffMember takes identity off the staff list and writes the audit row, in one transaction. An identity that is not on
// the list is ErrBadInput.
func RemoveStaffMember(identity, actor string) error {
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		list, err := staffListOf(tx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(list, func(m StaffMember) bool { return m.Identity == identity })
		if i < 0 {
			return fmt.Errorf("%w: not a staff member", ErrBadInput)
		}
		list = slices.Delete(list, i, i+1)
		if err := writeStaffListTx(tx, list); err != nil {
			return err
		}
		return auditTx(tx, actor, "staff-remove", "staff", map[string]any{"identity": identity})
	})
}

// validStaffIdentity: discord:<17 to 20 digits>, or with devNames dev-<1 to 24 letters, digits, dashes or underscores>.
func validStaffIdentity(identity string, devNames bool) bool {
	return staffDiscordRe.MatchString(identity) || devNames && staffDevRe.MatchString(identity)
}

func int64MetaOverride(q Querier, key string, min, max int64) (int64, bool, error) {
	v, err := metaValue(q, key)
	if err != nil || v == "" {
		return 0, false, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < min || n > max {
		return 0, false, nil
	}
	return n, true, nil
}

func int64MetaEffective(key string, min, max, fallback int64) int64 {
	d := attachedDB()
	if d == nil {
		return fallback
	}
	var n int64
	var set bool
	if err := d.View(func(q Querier) error {
		var err error
		n, set, err = int64MetaOverride(q, key, min, max)
		return err
	}); err != nil || !set {
		return fallback
	}
	return n
}

func int64MetaSetting(key string, min, max int64) (int64, bool, error) {
	d, err := docDB()
	if err != nil {
		return 0, false, err
	}
	var n int64
	var set bool
	err = d.View(func(q Querier) error {
		var err error
		n, set, err = int64MetaOverride(q, key, min, max)
		return err
	})
	return n, set, err
}

func metaBefore(tx *sql.Tx, key string) (string, error) {
	v, err := metaValue(tx, key)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "default", nil
	}
	return v, nil
}

func setInt64Meta(key string, n, min, max int64, badMsg string, actor string) error {
	if n < min || n > max {
		return fmt.Errorf("%w: %s", ErrBadInput, badMsg)
	}
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		old, err := metaBefore(tx, key)
		if err != nil {
			return err
		}
		val := strconv.FormatInt(n, 10)
		if err := setMetaTx(tx, key, val); err != nil {
			return err
		}
		return auditTx(tx, actor, "setting", key, map[string]any{"old": old, "new": val})
	})
}

func clearMetaSetting(key, actor string) error {
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		old, err := metaBefore(tx, key)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM meta WHERE key = ?`, key); err != nil {
			return err
		}
		return auditTx(tx, actor, "setting", key, map[string]any{"old": old, "new": "default"})
	})
}

// UploadMaxTotalBytes returns the global photo storage cap across all kinds: the panel's value when stored, else fallback.
func UploadMaxTotalBytes(fallback int64) int64 {
	return int64MetaEffective(uploadMaxTotalBytesKey, UploadMaxTotalBytesMin, UploadMaxTotalBytesMax, fallback)
}

// UploadMaxTotalBytesSetting returns the panel's stored global photo storage cap and whether one is stored.
func UploadMaxTotalBytesSetting() (int64, bool, error) {
	return int64MetaSetting(uploadMaxTotalBytesKey, UploadMaxTotalBytesMin, UploadMaxTotalBytesMax)
}

// SetUploadMaxTotalBytes stores the global photo storage cap and writes an audit row in one transaction.
func SetUploadMaxTotalBytes(n int64, actor string) error {
	return setInt64Meta(uploadMaxTotalBytesKey, n, UploadMaxTotalBytesMin, UploadMaxTotalBytesMax,
		"the total photo storage cap must be 1 MiB to 100 GiB", actor)
}

// ClearUploadMaxTotalBytes clears the panel override for the global photo storage cap and writes an audit row.
func ClearUploadMaxTotalBytes(actor string) error {
	return clearMetaSetting(uploadMaxTotalBytesKey, actor)
}

// UploadQuotaBytes returns one player's byte quota (all picture kinds together): the panel's value when stored, else fallback.
func UploadQuotaBytes(fallback int64) int64 {
	return int64MetaEffective(uploadQuotaBytesKey, UploadQuotaBytesMin, UploadQuotaBytesMax, fallback)
}

// UploadQuotaBytesSetting returns the panel's stored per-player byte quota and whether one is stored.
func UploadQuotaBytesSetting() (int64, bool, error) {
	return int64MetaSetting(uploadQuotaBytesKey, UploadQuotaBytesMin, UploadQuotaBytesMax)
}

// SetUploadQuotaBytes stores the per-player byte quota and writes an audit row in one transaction.
func SetUploadQuotaBytes(n int64, actor string) error {
	return setInt64Meta(uploadQuotaBytesKey, n, UploadQuotaBytesMin, UploadQuotaBytesMax,
		"the per-player picture quota must be 1 MiB to 10 GiB", actor)
}

// ClearUploadQuotaBytes clears the panel override for the per-player byte quota and writes an audit row.
func ClearUploadQuotaBytes(actor string) error {
	return clearMetaSetting(uploadQuotaBytesKey, actor)
}

// UploadMaxReceiveBytes returns the hard max receive size per individual upload: the panel's value when stored, else fallback.
func UploadMaxReceiveBytes(fallback int64) int64 {
	return int64MetaEffective(uploadMaxReceiveBytesKey, UploadMaxReceiveBytesMin, UploadMaxReceiveBytesMax, fallback)
}

// UploadMaxReceiveBytesSetting returns the panel's stored max upload receive size and whether one is stored.
func UploadMaxReceiveBytesSetting() (int64, bool, error) {
	return int64MetaSetting(uploadMaxReceiveBytesKey, UploadMaxReceiveBytesMin, UploadMaxReceiveBytesMax)
}

// SetUploadMaxReceiveBytes stores the max upload receive size and writes an audit row in one transaction.
func SetUploadMaxReceiveBytes(n int64, actor string) error {
	return setInt64Meta(uploadMaxReceiveBytesKey, n, UploadMaxReceiveBytesMin, UploadMaxReceiveBytesMax,
		"the max upload receive size must be 1 MiB to 100 MiB", actor)
}

// ClearUploadMaxReceiveBytes clears the panel override for the max upload receive size and writes an audit row.
func ClearUploadMaxReceiveBytes(actor string) error {
	return clearMetaSetting(uploadMaxReceiveBytesKey, actor)
}

// UploadMaxJPEGBytes returns the target JPEG filesize per individual upload: the panel's value when stored, else fallback.
func UploadMaxJPEGBytes(fallback int64) int64 {
	return int64MetaEffective(uploadMaxJPEGBytesKey, UploadMaxJPEGBytesMin, UploadMaxJPEGBytesMax, fallback)
}

// UploadMaxJPEGBytesSetting returns the panel's stored target JPEG filesize and whether one is stored.
func UploadMaxJPEGBytesSetting() (int64, bool, error) {
	return int64MetaSetting(uploadMaxJPEGBytesKey, UploadMaxJPEGBytesMin, UploadMaxJPEGBytesMax)
}

// SetUploadMaxJPEGBytes stores the target JPEG filesize and writes an audit row in one transaction.
func SetUploadMaxJPEGBytes(n int64, actor string) error {
	return setInt64Meta(uploadMaxJPEGBytesKey, n, UploadMaxJPEGBytesMin, UploadMaxJPEGBytesMax,
		"the target JPEG filesize must be 16 KiB to 100 MiB", actor)
}

// ClearUploadMaxJPEGBytes clears the panel override for the target JPEG filesize and writes an audit row.
func ClearUploadMaxJPEGBytes(actor string) error {
	return clearMetaSetting(uploadMaxJPEGBytesKey, actor)
}

// UploadMaxPNGBytes returns the target transparent PNG filesize per individual upload: the panel's value when stored, else fallback.
func UploadMaxPNGBytes(fallback int64) int64 {
	return int64MetaEffective(uploadMaxPNGBytesKey, UploadMaxPNGBytesMin, UploadMaxPNGBytesMax, fallback)
}

// UploadMaxPNGBytesSetting returns the panel's stored target transparent PNG filesize and whether one is stored.
func UploadMaxPNGBytesSetting() (int64, bool, error) {
	return int64MetaSetting(uploadMaxPNGBytesKey, UploadMaxPNGBytesMin, UploadMaxPNGBytesMax)
}

// SetUploadMaxPNGBytes stores the target transparent PNG filesize and writes an audit row in one transaction.
func SetUploadMaxPNGBytes(n int64, actor string) error {
	return setInt64Meta(uploadMaxPNGBytesKey, n, UploadMaxPNGBytesMin, UploadMaxPNGBytesMax,
		"the target transparent PNG filesize must be 16 KiB to 100 MiB", actor)
}

// ClearUploadMaxPNGBytes clears the panel override for the target transparent PNG filesize and writes an audit row.
func ClearUploadMaxPNGBytes(actor string) error {
	return clearMetaSetting(uploadMaxPNGBytesKey, actor)
}

// MaxPlayers returns the concurrent player cap: the panel's value when stored (0 means unlimited), else fallback.
func MaxPlayers(fallback int) int {
	return int(int64MetaEffective(maxPlayersKey, 0, MaxPlayersMax, int64(fallback)))
}

// MaxPlayersSetting returns the panel's stored concurrent player cap and whether one is stored.
func MaxPlayersSetting() (int, bool, error) {
	n, set, err := int64MetaSetting(maxPlayersKey, 0, MaxPlayersMax)
	return int(n), set, err
}

// SetMaxPlayers stores the concurrent player cap (0 means unlimited) and writes an audit row in one transaction.
func SetMaxPlayers(n int, actor string) error {
	return setInt64Meta(maxPlayersKey, int64(n), 0, MaxPlayersMax,
		fmt.Sprintf("the concurrent player cap must be 0 to %d", MaxPlayersMax), actor)
}

// ClearMaxPlayers clears the panel override for the concurrent player cap and writes an audit row.
func ClearMaxPlayers(actor string) error {
	return clearMetaSetting(maxPlayersKey, actor)
}
