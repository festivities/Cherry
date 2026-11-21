package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/internal/config"
)

// Player reports (PLAN Roadmap 8, staff "Moderation"; schema v9 reports). A player reports a chat line, a diary post or
// comment, a guestbook entry or reply, or a Circle, Square or Room Party chat line. The game only stores the report and
// answers as the client expects; staff resolve or dismiss it in the report queue, and that change is audited in the same
// transaction. The reporter is always the authenticated account: callers pass it, never a field of the request.

// Report sources (reports.source): the route or gateway message the report came in on.
const (
	ReportChat           = "chat"            // 1:1 friend chat, POST /v4/report with categoryId chat
	ReportChatImg        = "chat_img"        // 1:1 friend chat, a picture (categoryId chat_img)
	ReportTChat          = "tchat"           // Lounge Chat, POST /v4/report with categoryId tchat
	ReportDiary          = "diary"           // POST /v4/report/diary
	ReportDiaryComment   = "diary_comment"   // POST /v4/report/diary/comment
	ReportGuestbook      = "guestbook"       // POST /v4/report/guestbook
	ReportGuestbookReply = "guestbook_reply" // POST /v4/report/guestbookreply
	ReportCircle         = "circle"          // gateway cs_report_chat (Circle chat)
	ReportSquare         = "square"          // gateway cr_report_chat, index 12 (Square chat)
	ReportRoomParty      = "roomparty"       // gateway cr_report_chat, index 13 (Room Party chat)
)

// ReportSources are the sources the table holds, in the order the queue's filter lists them.
var ReportSources = []string{ReportChat, ReportChatImg, ReportTChat, ReportDiary, ReportDiaryComment, ReportGuestbook,
	ReportGuestbookReply, ReportCircle, ReportSquare, ReportRoomParty}

// Report statuses (reports.status).
const (
	ReportOpen      = "open"
	ReportResolved  = "resolved"
	ReportDismissed = "dismissed"
)

const (
	// ReportDailyCap is the number of reports one account may store per game day. It applies in identity modes only (the lab
	// keeps every report). Over the cap the intake still answers success and stores nothing (store.FileReport).
	ReportDailyCap   = 50
	reportMessageMax = 500  // characters of the reporter's text kept
	reportRefMax     = 64   // characters of a reference id or policy string
	reportTextMax    = 2000 // characters of a reported item's text kept in its snapshot
	reportNoteMax    = 200  // characters of a staff note
	reportChatLines  = 10   // chat lines of the reported target in the snapshot
	reportChatWindow = time.Hour
)

// ErrNoReport is an unknown report id; ErrReportClosed a status change on a report that is already handled.
var (
	ErrNoReport     = errors.New("no such report")
	ErrReportClosed = errors.New("the report is already handled")
)

// ReportIn is one report as the intake reads it. Reporter is the authenticated account's avatar id. Message is the
// reporter's own text. RefID and RefDetail are the route's reference ids as the client sent them (RefDetail is the number
// the route names: refDtl for a diary comment, parentHistNo for a guestbook reply, the chat serial for a gateway report).
// Host is the guestbook's owner ("" when the route has none). Channel is the chat channel of a gateway report: the Circle
// id, the Square theme id or the Room Party host; "" for the HTTP routes, which look their channel up themselves.
type ReportIn struct {
	Source, Reporter, Target, Reason, Message string
	RefID, RefDetail, Host, Channel           string
}

// ReportRow is one stored report. Context is the snapshot JSON (ReportSnapshot); HandledBy, HandledAt and Note are set
// once a staff member resolves or dismisses it.
type ReportRow struct {
	ID                                              int64
	Created, Source, ReporterAid, TargetAid, Reason string
	Message, RefID, RefDetail, Context              string
	Status, HandledBy, HandledAt, Note              string
}

// ReportSnapshot is what a report carries from the server's own records, read when the report came in, so staff can judge it
// after the content is deleted or swept. Item is set for diary, comment, guestbook and reply reports; the chat fields for chat
// reports (Kind and Channel name the chat_log channel searched, Line the reported line when its serial identifies it, Lines
// the target's newest lines in that channel within the hour).
type ReportSnapshot struct {
	Item    *ReportItem  `json:"item,omitempty"`
	Kind    string       `json:"chatKind,omitempty"`
	Channel string       `json:"channel,omitempty"`
	Line    *ReportLine  `json:"line,omitempty"`
	Lines   []ReportLine `json:"lines,omitempty"`
	Host    string       `json:"host,omitempty"`
	Note    string       `json:"note,omitempty"`
}

// ReportItem is the reported diary post, comment, guestbook entry or reply as it is stored now: Found is false when it no
// longer exists (its No is the reference id the report named).
type ReportItem struct {
	Found    bool   `json:"found"`
	No       string `json:"no"`
	Post     string `json:"post,omitempty"`
	Author   string `json:"author,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Date     string `json:"date,omitempty"`
	Title    string `json:"title,omitempty"`
	Text     string `json:"text,omitempty"`
}

// ReportLine is one chat line in a snapshot: the time (RFC 3339, UTC), the sender's avatar id and name, and the text.
type ReportLine struct {
	Time   string `json:"time"`
	Sender string `json:"sender"`
	Name   string `json:"name,omitempty"`
	Text   string `json:"text"`
}

// Snapshot decodes the row's context. A row without one (or one that does not parse) gives the zero snapshot.
func (r ReportRow) Snapshot() ReportSnapshot {
	var s ReportSnapshot
	_ = json.Unmarshal([]byte(r.Context), &s)
	return s
}

// check validates in and cleans its message (invalid UTF-8 dropped, at most reportMessageMax characters). Every id is a
// decimal number; the reason is R and one or two digits; the references are at most reportRefMax characters.
func (in *ReportIn) check() error {
	bad := func(what string) error { return fmt.Errorf("%w: %s", ErrBadInput, what) }
	switch {
	case !slices.Contains(ReportSources, in.Source):
		return bad("unknown report source")
	case !ValidAvatarID(in.Reporter) || in.Reporter == "0":
		return bad("reporter is not an avatar id")
	case !ValidAvatarID(in.Target) || in.Target == "0":
		return bad("target is not an avatar id")
	case !validReportReason(in.Reason):
		return bad("reason is R and one or two digits")
	case utf8.RuneCountInString(in.RefID) > reportRefMax || utf8.RuneCountInString(in.RefDetail) > reportRefMax:
		return bad("reference longer than 64 characters")
	case in.RefDetail != "" && !isDigits(in.RefDetail):
		return bad("reference detail is a number")
	case in.Host != "" && !ValidAvatarID(in.Host):
		return bad("host is not an avatar id")
	case in.Channel != "" && !ValidAvatarID(in.Channel):
		return bad("channel is not a number")
	}
	in.Message = cutRunes(strings.ToValidUTF8(in.Message, ""), reportMessageMax)
	return nil
}

// validReportReason reports whether s is R followed by one or two digits (the client's R1 to R8, and room for more).
func validReportReason(s string) bool {
	return len(s) >= 2 && len(s) <= 3 && s[0] == 'R' && isDigits(s[1:])
}

// isDigits reports whether s is one or more ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// FileReport validates in, reads its snapshot and stores it. dayStart is the start of the reporter's game day
// (economy.GameDayStart): in identity modes an account with ReportDailyCap reports stored since then gets no new row. stored
// is false when the cap refused it, and that is not an error: the caller still answers the client with success. Without an
// attached database it fails with ErrNoDatabase; invalid input wraps ErrBadInput.
//
// ponytail: the snapshot is read before the write transaction (the diary lists are under SocialMu, which a write must not
// wait for while holding the database's one write connection); a capped report pays for that read and stores nothing.
func FileReport(in ReportIn, dayStart time.Time) (stored bool, err error) {
	if err := in.check(); err != nil {
		return false, err
	}
	d := attachedDB()
	if d == nil {
		return false, ErrNoDatabase
	}
	capped := func(tx Querier) (bool, error) {
		if !config.IdentityMode() {
			return false, nil
		}
		var n int
		err := tx.QueryRow(`SELECT COUNT(*) FROM reports WHERE reporter_aid = ? AND created >= ?`,
			in.Reporter, dayStart.UTC().Format(time.RFC3339)).Scan(&n)
		return n >= ReportDailyCap, err
	}
	var early bool
	if err := d.View(func(q Querier) error {
		var err error
		early, err = capped(q)
		return err
	}); err != nil {
		return false, err
	}
	if early {
		return false, nil
	}
	snap, err := reportSnapshotOf(d, in, time.Now())
	if err != nil {
		return false, err
	}
	ctx, err := json.Marshal(snap)
	if err != nil {
		return false, err
	}
	err = d.Update(func(tx *sql.Tx) error {
		full, err := capped(tx)
		if err != nil || full {
			return err
		}
		_, err = tx.Exec(`INSERT INTO reports (created, source, reporter_aid, target_aid, reason, message, ref_id, ref_detail, context)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, time.Now().UTC().Format(time.RFC3339), in.Source, in.Reporter, in.Target,
			in.Reason, in.Message, in.RefID, in.RefDetail, string(ctx))
		stored = err == nil
		return err
	})
	return stored, err
}

// reportSnapshotOf reads what a report of in's source names: the item (diary, comment, guestbook, reply) or the target's
// chat lines in the channel the report is about (chat, circle, square, roomparty).
func reportSnapshotOf(d *DB, in ReportIn, now time.Time) (ReportSnapshot, error) {
	snap := ReportSnapshot{Host: in.Host}
	switch in.Source {
	case ReportDiary, ReportDiaryComment, ReportGuestbook, ReportGuestbookReply:
		snap.Item = reportItemOf(in)
		return snap, nil
	case ReportChat, ReportChatImg:
		snap.Kind = ChatPrivate
		// The 1:1 room is the pair's; the room's own message store keeps each line's serial, so a line the target sent
		// under the report's serial is the reported one.
		err := d.View(func(q Querier) error {
			room, found, err := chatRoomBetween(q, in.Reporter, in.Target)
			if err != nil || !found {
				return err
			}
			snap.Channel = strconv.FormatInt(room, 10)
			if in.RefDetail != "" {
				if snap.Line, err = roomLineBySerial(q, room, in.RefDetail, in.Target); err != nil {
					return err
				}
			}
			snap.Lines, err = chatLinesOf(q, snap.Kind, snap.Channel, in.Target, now)
			return err
		})
		if err == nil && snap.Channel == "" {
			snap.Note = "no chat room between the reporter and the target"
		}
		return snap, err
	case ReportTChat:
		// The Lounge room of the report is not named by the request, so the lines are the target's in every lounge room.
		snap.Kind, snap.Note = ChatLounge, "lounge room not named by the report; lines are from every lounge room"
	case ReportCircle:
		snap.Kind, snap.Channel = ChatCircle, in.Channel
	case ReportSquare:
		snap.Kind, snap.Channel = ChatSquare, in.Channel
	case ReportRoomParty:
		snap.Kind, snap.Channel = ChatParty, in.Channel
	}
	if snap.Channel == "" && snap.Kind != ChatLounge {
		snap.Note = "the reporter was not in a room when the report came in"
		return snap, nil
	}
	err := d.View(func(q Querier) error {
		var err error
		snap.Lines, err = chatLinesOf(q, snap.Kind, snap.Channel, in.Target, now)
		return err
	})
	return snap, err
}

// reportItemOf reads the reported diary post, comment, guestbook entry or reply under SocialMu. Found is false when it is gone.
func reportItemOf(in ReportIn) *ReportItem {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	it := &ReportItem{No: in.RefID}
	switch in.Source {
	case ReportDiary:
		for _, p := range DiaryPosts {
			if p.DiaryNo == in.RefID {
				return &ReportItem{Found: true, No: p.DiaryNo, Author: p.AvatarID, Nickname: p.Nickname, Date: p.RegDate,
					Title: cutRunes(p.Title, reportTextMax), Text: cutRunes(p.Content, reportTextMax)}
			}
		}
	case ReportDiaryComment:
		for _, c := range DiaryComments {
			if c.CommentNo == in.RefID {
				return &ReportItem{Found: true, No: c.CommentNo, Post: c.DiaryNo, Author: c.CommentAvatarID, Nickname: c.Nickname,
					Date: c.Date, Text: cutRunes(c.Content, reportTextMax)}
			}
		}
	case ReportGuestbook:
		for _, e := range GuestbookEntries {
			if e.GuestHistNo == in.RefID {
				return &ReportItem{Found: true, No: e.GuestHistNo, Author: e.GuestAvtNo, Nickname: e.GuestNickname, Date: e.RegYmdt,
					Text: cutRunes(e.Content, reportTextMax)}
			}
		}
	case ReportGuestbookReply:
		for _, e := range GuestbookEntries {
			for _, r := range e.Reply {
				if r.ReplyHistNo == in.RefID {
					return &ReportItem{Found: true, No: r.ReplyHistNo, Post: r.ParentHistNo, Author: r.ReplyAvtNo,
						Nickname: r.ReplyNickname, Date: r.RegYmdt, Text: cutRunes(r.Content, reportTextMax)}
				}
			}
		}
	}
	return it
}

// chatRoomBetween returns the 1:1 chat room of two accounts, if they have one.
func chatRoomBetween(q Querier, a, b string) (int64, bool, error) {
	var room int64
	err := q.QueryRow(`SELECT room_id FROM chat_rooms WHERE (aid_a = ? AND aid_b = ?) OR (aid_a = ? AND aid_b = ?) LIMIT 1`,
		a, b, b, a).Scan(&room)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return room, err == nil, err
}

// roomLineBySerial returns the line of room with serial sn when the target sent it. Nothing is found when the line is no
// longer kept (the room keeps its newest messages only).
func roomLineBySerial(q Querier, room int64, sn, target string) (*ReportLine, error) {
	var sec int64
	var sender, text string
	err := q.QueryRow(`SELECT sec, sender, text FROM chat_messages WHERE room_id = ? AND sn = ? AND sender = ? LIMIT 1`,
		room, sn, target).Scan(&sec, &sender, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ReportLine{Time: time.Unix(sec, 0).UTC().Format(time.RFC3339), Sender: sender, Text: cutRunes(text, reportTextMax)}, nil
}

// chatLinesOf returns the newest lines of sender in the channel (kind, and channel unless it is "") from the last hour before
// now, newest first, at most reportChatLines. chat_log is written by a batching writer, so a line sent a moment before the
// report may not be in the table yet; the snapshot does not wait for it.
func chatLinesOf(q Querier, kind, channel, sender string, now time.Time) ([]ReportLine, error) {
	query := `SELECT time, sender, sender_name, text FROM chat_log WHERE sender = ? AND kind = ? AND time >= ?`
	args := []any{sender, kind, now.Add(-reportChatWindow).UnixMilli()}
	if channel != "" {
		query += ` AND channel = ?`
		args = append(args, channel)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, reportChatLines)
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReportLine
	for rows.Next() {
		var ms int64
		var l ReportLine
		if err := rows.Scan(&ms, &l.Sender, &l.Name, &l.Text); err != nil {
			return nil, err
		}
		l.Time = time.UnixMilli(ms).UTC().Format(time.RFC3339)
		out = append(out, l)
	}
	return out, rows.Err()
}

const reportCols = `SELECT id, created, source, reporter_aid, target_aid, reason, message, ref_id, ref_detail, context,
	status, handled_by, handled_at, note FROM reports`

// scanReport reads one row of reportCols.
func scanReport(sc interface{ Scan(...any) error }) (ReportRow, error) {
	var r ReportRow
	err := sc.Scan(&r.ID, &r.Created, &r.Source, &r.ReporterAid, &r.TargetAid, &r.Reason, &r.Message, &r.RefID, &r.RefDetail,
		&r.Context, &r.Status, &r.HandledBy, &r.HandledAt, &r.Note)
	return r, err
}

// ListReports returns up to limit reports, newest first, with ids below before (0 = from the newest). status "" lists every
// status; otherwise open, resolved or dismissed. ErrBadInput for another status or a limit outside 1 to 200.
func ListReports(status string, before int64, limit int) ([]ReportRow, error) {
	d := attachedDB()
	if d == nil {
		return nil, ErrNoDatabase
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: the report page is 1 to 200 rows", ErrBadInput)
	}
	// Only fixed clauses enter the SQL text; the status and the markers are bound arguments.
	where := `(? = 0 OR id < ?)`
	args := []any{before, before}
	switch status {
	case "":
	case ReportOpen, ReportResolved, ReportDismissed:
		where += ` AND status = ?`
		args = append(args, status)
	default:
		return nil, fmt.Errorf("%w: unknown report status", ErrBadInput)
	}
	args = append(args, limit)
	out := []ReportRow{}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(reportCols+` WHERE `+where+` ORDER BY id DESC LIMIT ?`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReport(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ReportByID returns the report with row id; found is false for an unknown id.
func ReportByID(id int64) (ReportRow, bool, error) {
	d := attachedDB()
	if d == nil {
		return ReportRow{}, false, ErrNoDatabase
	}
	var r ReportRow
	err := d.View(func(q Querier) error {
		var err error
		r, err = scanReport(q.QueryRow(reportCols+` WHERE id = ?`, id))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ReportRow{}, false, nil
	}
	return r, err == nil, err
}

// CountOpenReports returns how many reports are open (the queue's badge).
func CountOpenReports() (int, error) {
	d := attachedDB()
	if d == nil {
		return 0, ErrNoDatabase
	}
	var n int
	err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT COUNT(*) FROM reports WHERE status = ?`, ReportOpen).Scan(&n)
	})
	return n, err
}

// CountReportsAgainst returns how many reports, in any status, name aid as their target.
func CountReportsAgainst(aid string) (int, error) {
	d := attachedDB()
	if d == nil {
		return 0, ErrNoDatabase
	}
	var n int
	err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT COUNT(*) FROM reports WHERE target_aid = ?`, aid).Scan(&n)
	})
	return n, err
}

// SearchReports returns up to limit reports whose reporter or target is the avatar id q exactly, newest first, and whether more
// exist. A q that is not an avatar id finds nothing.
func SearchReports(q string, limit int) ([]ReportRow, bool, error) {
	d := attachedDB()
	if d == nil {
		return nil, false, ErrNoDatabase
	}
	out := []ReportRow{}
	if !ValidAvatarID(q) || limit < 1 {
		return out, false, nil
	}
	err := d.View(func(qr Querier) error {
		rows, err := qr.Query(reportCols+` WHERE reporter_aid = ? OR target_aid = ? ORDER BY id DESC LIMIT ?`, q, q, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReport(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if len(out) > limit {
		return out[:limit], true, err
	}
	return out, false, err
}

// DeletedAvatarOfAid returns the newest deleted avatar with avatar id aid, if any (the report's links to a deleted avatar).
func DeletedAvatarOfAid(aid string) (DeletedAvatar, bool, error) {
	if !ValidAvatarID(aid) {
		return DeletedAvatar{}, false, nil
	}
	rows, err := deletedQuery(`SELECT id, aid, identity, name, deleted_at FROM deleted_avatars
		WHERE aid = ? ORDER BY id DESC LIMIT 1`, aid)
	if err != nil || len(rows) == 0 {
		return DeletedAvatar{}, false, err
	}
	return rows[0], true, nil
}

// SetReportStatus resolves or dismisses the open report id with a staff note (1 to 200 characters) and writes its audit row
// (report-resolve or report-dismiss, target report:<id>, the reported avatar and the note) in the same transaction. A report
// already handled gives ErrReportClosed; an unknown one ErrNoReport.
func SetReportStatus(id int64, status, note, actor string) error {
	if status != ReportResolved && status != ReportDismissed {
		return fmt.Errorf("%w: unknown report status", ErrBadInput)
	}
	note = strings.TrimSpace(note)
	if n := utf8.RuneCountInString(note); n < 1 || n > reportNoteMax {
		return fmt.Errorf("%w: the note is required, 1 to %d characters", ErrBadInput, reportNoteMax)
	}
	d := attachedDB()
	if d == nil {
		return ErrNoDatabase
	}
	verb := "report-resolve"
	if status == ReportDismissed {
		verb = "report-dismiss"
	}
	return d.Update(func(tx *sql.Tx) error {
		var target, source, current string
		err := tx.QueryRow(`SELECT target_aid, source, status FROM reports WHERE id = ?`, id).Scan(&target, &source, &current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoReport
		}
		if err != nil {
			return err
		}
		if current != ReportOpen {
			return ErrReportClosed
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := tx.Exec(`UPDATE reports SET status = ?, handled_by = ?, handled_at = ?, note = ? WHERE id = ? AND status = ?`,
			status, actor, now, note, id, ReportOpen); err != nil {
			return err
		}
		return auditTx(tx, actor, verb, "report:"+strconv.FormatInt(id, 10),
			map[string]any{"aid": target, "source": source, "status": status, "note": note})
	})
}
