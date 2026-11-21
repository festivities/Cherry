package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"cherry/internal/config"
)

// reportIn is a valid chat report from reporter about target.
func reportIn(source, reporter, target string) ReportIn {
	return ReportIn{Source: source, Reporter: reporter, Target: target, Reason: "R3", Message: "rude"}
}

// dayAgo is a day start that every report made in this test counts in.
func dayAgo() time.Time { return time.Now().Add(-24 * time.Hour) }

// TestReportsSchemaV9: the table and its indexes exist, the schema version is the newest, and the CHECK constraints refuse an
// unknown source or status.
func TestReportsSchemaV9(t *testing.T) {
	d, _ := openTestDB(t)
	var v int
	if err := d.write.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d (err %v), want %d", v, err, len(migrations))
	}
	for _, idx := range []string{"reports_status", "reports_target"} {
		var n int
		if err := d.write.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s: %d (err %v)", idx, n, err)
		}
	}
	bad := []string{
		`INSERT INTO reports (created, source, reporter_aid, target_aid, reason) VALUES ('2026-11-21T10:00:00Z', 'bogus', '1', '2', 'R1')`,
		`INSERT INTO reports (created, source, reporter_aid, target_aid, reason, status) VALUES ('2026-11-21T10:00:00Z', 'chat', '1', '2', 'R1', 'maybe')`,
	}
	for _, q := range bad {
		if _, err := d.write.Exec(q); err == nil {
			t.Fatalf("CHECK let %q through", q)
		}
	}
}

// TestMigrationV8ToV9KeepsRows: a v8 database gains the reports table and keeps its rows.
func TestMigrationV8ToV9KeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), DBFileName)
	raw, err := sql.Open("sqlite", path+"?"+dbPragmas)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(raw, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO deleted_avatars (aid, deleted_at, data) VALUES ('9031', '2026-11-01T00:00:00Z', '{}')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	d, err := OpenDB(path)
	if err != nil {
		t.Fatalf("migrate v8 to v9: %v", err)
	}
	defer d.Close()
	var kept int
	if err := d.write.QueryRow(`SELECT COUNT(*) FROM deleted_avatars WHERE aid = '9031'`).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("deleted avatar row after the migration: %d (err %v)", kept, err)
	}
	if n, err := openReportsIn(d); err != nil || n != 0 {
		t.Fatalf("reports after the migration: %d (err %v)", n, err)
	}
}

// openReportsIn counts the open reports of an unattached database (the migration test has no store attached).
func openReportsIn(d *DB) (int, error) {
	var n int
	err := d.write.QueryRow(`SELECT COUNT(*) FROM reports WHERE status = 'open'`).Scan(&n)
	return n, err
}

// TestFileReportValidation: every field is checked before anything is read or written, and the message is capped.
func TestFileReportValidation(t *testing.T) {
	base := reportIn(ReportChat, "9001", "9002")
	cases := map[string]func(in *ReportIn){
		"source":        func(in *ReportIn) { in.Source = "bogus" },
		"reporter":      func(in *ReportIn) { in.Reporter = "" },
		"reporter text": func(in *ReportIn) { in.Reporter = "9001x" },
		"target zero":   func(in *ReportIn) { in.Target = "0" },
		"target long":   func(in *ReportIn) { in.Target = strings.Repeat("9", 21) },
		"reason":        func(in *ReportIn) { in.Reason = "X1" },
		"reason lower":  func(in *ReportIn) { in.Reason = "r1" },
		"reason three":  func(in *ReportIn) { in.Reason = "R123" },
		"reason bare R": func(in *ReportIn) { in.Reason = "R" },
		"ref long":      func(in *ReportIn) { in.RefID = strings.Repeat("a", 65) },
		"detail text":   func(in *ReportIn) { in.RefDetail = "12a" },
		"host text":     func(in *ReportIn) { in.Host = "host" },
		"channel text":  func(in *ReportIn) { in.Channel = "room" },
	}
	for name, mutate := range cases {
		in := base
		mutate(&in)
		stored, err := FileReport(in, dayAgo())
		if !errors.Is(err, ErrBadInput) || stored {
			t.Errorf("%s: stored=%t err=%v, want ErrBadInput", name, stored, err)
		}
	}
	// Without an attached database a valid report cannot be stored.
	if _, err := FileReport(base, dayAgo()); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("no database: %v, want ErrNoDatabase", err)
	}

	docStore(t)
	long := base
	long.Message = strings.Repeat("é", 600) + "\xff"
	long.RefID = strings.Repeat("r", 64)
	if stored, err := FileReport(long, dayAgo()); err != nil || !stored {
		t.Fatalf("valid report: stored=%t err=%v", stored, err)
	}
	rows, err := ListReports("", 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d (err %v)", len(rows), err)
	}
	if n := utf8.RuneCountInString(rows[0].Message); n != reportMessageMax || !utf8.ValidString(rows[0].Message) {
		t.Fatalf("message kept %d runes (valid %t), want %d valid", n, utf8.ValidString(rows[0].Message), reportMessageMax)
	}
	if rows[0].ReporterAid != "9001" || rows[0].TargetAid != "9002" || rows[0].Reason != "R3" || rows[0].Status != ReportOpen {
		t.Fatalf("stored row = %+v", rows[0])
	}
}

// TestFileReportDiarySnapshot: a diary report keeps the reported post's text and author as they were when it came in, and
// says so when the post is gone later.
func TestFileReportDiarySnapshot(t *testing.T) {
	docStore(t)
	SocialMu.Lock()
	DiaryPosts = []DiaryPost{{DiaryNo: "41", AvatarID: "9001", Nickname: "Ann", RegDate: "1700000000000", Title: "Hi", Content: "post text"}}
	DiaryComments = []DiaryComment{{CommentNo: "7", DiaryNo: "41", OwnerAvatarID: "9001", CommentAvatarID: "9003", Nickname: "Cy", Content: "comment text", Date: "1700000001000"}}
	SocialMu.Unlock()
	t.Cleanup(func() {
		SocialMu.Lock()
		DiaryPosts, DiaryComments = nil, nil
		SocialMu.Unlock()
	})
	in := ReportIn{Source: ReportDiary, Reporter: "9002", Target: "9001", Reason: "R2", Message: "m", RefID: "41"}
	if stored, err := FileReport(in, dayAgo()); err != nil || !stored {
		t.Fatalf("diary report: stored=%t err=%v", stored, err)
	}
	in.Source, in.RefID, in.RefDetail = ReportDiaryComment, "7", "41"
	if stored, err := FileReport(in, dayAgo()); err != nil || !stored {
		t.Fatalf("comment report: stored=%t err=%v", stored, err)
	}
	SocialMu.Lock()
	DiaryPosts, DiaryComments = nil, nil
	SocialMu.Unlock()
	in.Source, in.RefID, in.RefDetail = ReportDiary, "41", ""
	if stored, err := FileReport(in, dayAgo()); err != nil || !stored {
		t.Fatalf("report after the delete: stored=%t err=%v", stored, err)
	}

	rows, err := ListReports("", 0, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %d (err %v)", len(rows), err)
	}
	gone, comment, post := rows[0].Snapshot(), rows[1].Snapshot(), rows[2].Snapshot()
	if post.Item == nil || !post.Item.Found || post.Item.Author != "9001" || post.Item.Text != "post text" || post.Item.Nickname != "Ann" {
		t.Fatalf("post snapshot = %+v", post.Item)
	}
	if comment.Item == nil || !comment.Item.Found || comment.Item.Author != "9003" || comment.Item.Text != "comment text" || comment.Item.Post != "41" {
		t.Fatalf("comment snapshot = %+v", comment.Item)
	}
	if gone.Item == nil || gone.Item.Found {
		t.Fatalf("a removed post's snapshot = %+v, want not found", gone.Item)
	}
}

// TestFileReportChatSnapshot: a 1:1 report reads the pair's room; its serial names the reported line; the lines are the
// target's own, in that channel, within the hour. A gateway report reads its channel.
func TestFileReportChatSnapshot(t *testing.T) {
	docStore(t)
	d := attachedDB()
	if _, err := d.write.Exec(`INSERT INTO chat_rooms (room_id, aid_a, aid_b, data) VALUES (77, '9001', '9002', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.write.Exec(`INSERT INTO chat_messages (room_id, sn, sec, sender, text) VALUES (77, 5, 1700000000, '9001', 'the reported line')`); err != nil {
		t.Fatal(err)
	}
	StartChatLog()
	t.Cleanup(StopChatLog)
	RecordChat(ChatEntry{Kind: ChatPrivate, Channel: "77", Sender: "9001", SenderName: "Ann", Text: "one"})
	RecordChat(ChatEntry{Kind: ChatPrivate, Channel: "77", Sender: "9002", SenderName: "Bo", Text: "theirs"})
	RecordChat(ChatEntry{Kind: ChatPrivate, Channel: "78", Sender: "9001", SenderName: "Ann", Text: "elsewhere"})
	FlushChatLog()

	chat := ReportIn{Source: ReportChat, Reporter: "9002", Target: "9001", Reason: "R1", RefDetail: "5"}
	if stored, err := FileReport(chat, dayAgo()); err != nil || !stored {
		t.Fatalf("chat report: stored=%t err=%v", stored, err)
	}
	circle := ReportIn{Source: ReportCircle, Reporter: "9002", Target: "9001", Reason: "R5", Channel: "1940000001"}
	if stored, err := FileReport(circle, dayAgo()); err != nil || !stored {
		t.Fatalf("circle report: stored=%t err=%v", stored, err)
	}
	none := ReportIn{Source: ReportChat, Reporter: "9002", Target: "9003", Reason: "R1"}
	if stored, err := FileReport(none, dayAgo()); err != nil || !stored {
		t.Fatalf("report without a room: stored=%t err=%v", stored, err)
	}

	rows, err := ListReports("", 0, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %d (err %v)", len(rows), err)
	}
	noRoom, gateway, room := rows[0].Snapshot(), rows[1].Snapshot(), rows[2].Snapshot()
	if room.Kind != ChatPrivate || room.Channel != "77" || room.Line == nil || room.Line.Text != "the reported line" || room.Line.Sender != "9001" {
		t.Fatalf("room snapshot = %+v (line %+v)", room, room.Line)
	}
	if len(room.Lines) != 1 || room.Lines[0].Text != "one" {
		t.Fatalf("lines = %+v, want the target's line in channel 77 only", room.Lines)
	}
	if gateway.Kind != ChatCircle || gateway.Channel != "1940000001" || len(gateway.Lines) != 0 {
		t.Fatalf("circle snapshot = %+v", gateway)
	}
	if noRoom.Note == "" || noRoom.Kind != ChatPrivate {
		t.Fatalf("report without a room = %+v", noRoom)
	}
}

// TestFileReportDailyCap: in identity modes an account stores ReportDailyCap reports a game day and the next is answered
// without a row; the lab keeps every report.
func TestFileReportDailyCap(t *testing.T) {
	docStore(t)
	in := reportIn(ReportTChat, "9001", "9002")
	t.Run("identity", func(t *testing.T) {
		config.Activate(config.Config{AuthMode: config.AuthDevFake})
		t.Cleanup(func() { config.Activate(config.Config{}) })
		for i := range ReportDailyCap {
			if stored, err := FileReport(in, dayAgo()); err != nil || !stored {
				t.Fatalf("report %d: stored=%t err=%v", i+1, stored, err)
			}
		}
		if stored, err := FileReport(in, dayAgo()); err != nil || stored {
			t.Fatalf("report over the cap: stored=%t err=%v, want false and no error", stored, err)
		}
		other := reportIn(ReportTChat, "9004", "9002")
		if stored, err := FileReport(other, dayAgo()); err != nil || !stored {
			t.Fatalf("another reporter: stored=%t err=%v", stored, err)
		}
		if rows, _ := ListReports("", 0, 200); len(rows) != ReportDailyCap+1 {
			t.Fatalf("rows = %d, want %d", len(rows), ReportDailyCap+1)
		}
	})
	t.Run("lab", func(t *testing.T) {
		for range ReportDailyCap + 1 {
			if stored, err := FileReport(in, dayAgo()); err != nil || !stored {
				t.Fatalf("lab report: stored=%t err=%v", stored, err)
			}
		}
	})
}

// TestSetReportStatusAudits: resolve and dismiss take a note, write one audit row each in the same transaction, and refuse a
// report that is handled already, an unknown one and a bad note.
func TestSetReportStatusAudits(t *testing.T) {
	docStore(t)
	if _, err := FileReport(reportIn(ReportDiary, "9001", "9002"), dayAgo()); err != nil {
		t.Fatal(err)
	}
	if _, err := FileReport(reportIn(ReportDiary, "9003", "9002"), dayAgo()); err != nil {
		t.Fatal(err)
	}
	rows, err := ListReports("", 0, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %d (err %v)", len(rows), err)
	}
	first, second := rows[1].ID, rows[0].ID

	if err := SetReportStatus(first, ReportResolved, "   ", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("blank note: %v, want ErrBadInput", err)
	}
	if err := SetReportStatus(first, ReportOpen, "x", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("status open: %v, want ErrBadInput", err)
	}
	if err := SetReportStatus(9999, ReportResolved, "x", "dev-owner"); !errors.Is(err, ErrNoReport) {
		t.Fatalf("unknown report: %v, want ErrNoReport", err)
	}
	if err := SetReportStatus(first, ReportResolved, "warned in chat", "dev-owner"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := SetReportStatus(first, ReportDismissed, "again", "dev-owner"); !errors.Is(err, ErrReportClosed) {
		t.Fatalf("second decision: %v, want ErrReportClosed", err)
	}
	if err := SetReportStatus(second, ReportDismissed, "not a violation", "dev-two"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	row, found, err := ReportByID(first)
	if err != nil || !found || row.Status != ReportResolved || row.HandledBy != "dev-owner" || row.Note != "warned in chat" || row.HandledAt == "" {
		t.Fatalf("resolved row = %+v (found %t err %v)", row, found, err)
	}
	for action, target := range map[string]int64{"report-resolve": first, "report-dismiss": second} {
		audit, err := AuditSearch(AuditFilter{Action: action}, 0, 10)
		if err != nil || len(audit) != 1 || audit[0].Target != "report:"+strconv.FormatInt(target, 10) || !strings.Contains(audit[0].Detail, `"aid":"9002"`) {
			t.Fatalf("%s audit = %+v (err %v)", action, audit, err)
		}
	}
	if n, err := CountOpenReports(); err != nil || n != 0 {
		t.Fatalf("open after both = %d (err %v)", n, err)
	}
}

// TestReportListSearchAndCounts: the queue filters by status, pages by id, the counts and the search see every report.
func TestReportListSearchAndCounts(t *testing.T) {
	docStore(t)
	for _, r := range []ReportIn{reportIn(ReportChat, "9001", "9002"), reportIn(ReportChat, "9003", "9001"), reportIn(ReportSquare, "9005", "9006")} {
		if _, err := FileReport(r, dayAgo()); err != nil {
			t.Fatal(err)
		}
	}
	all, err := ListReports("", 0, 10)
	if err != nil || len(all) != 3 || all[0].ReporterAid != "9005" {
		t.Fatalf("all = %d rows, newest %+v (err %v)", len(all), all[0], err)
	}
	if err := SetReportStatus(all[1].ID, ReportDismissed, "fine", "dev-owner"); err != nil {
		t.Fatal(err)
	}
	open, _ := ListReports(ReportOpen, 0, 10)
	if len(open) != 2 || open[0].ID != all[0].ID || open[1].ID != all[2].ID {
		t.Fatalf("open = %+v", open)
	}
	if dismissed, _ := ListReports(ReportDismissed, 0, 10); len(dismissed) != 1 {
		t.Fatalf("dismissed = %d rows", len(dismissed))
	}
	if page, _ := ListReports("", all[0].ID, 1); len(page) != 1 || page[0].ID != all[1].ID {
		t.Fatalf("page after %d = %+v", all[0].ID, page)
	}
	for _, bad := range []struct {
		status string
		limit  int
	}{{"bogus", 10}, {"", 0}, {"", 201}} {
		if _, err := ListReports(bad.status, 0, bad.limit); !errors.Is(err, ErrBadInput) {
			t.Fatalf("ListReports(%q, %d): %v, want ErrBadInput", bad.status, bad.limit, err)
		}
	}
	if n, err := CountOpenReports(); err != nil || n != 2 {
		t.Fatalf("open count = %d (err %v)", n, err)
	}
	if n, err := CountReportsAgainst("9001"); err != nil || n != 1 {
		t.Fatalf("reports against 9001 = %d (err %v), want 1 (one names it as target)", n, err)
	}
	hits, more, err := SearchReports("9001", 10)
	if err != nil || more || len(hits) != 2 {
		t.Fatalf("search 9001: %d hits, more %t (err %v), want 2 (reporter or target)", len(hits), more, err)
	}
	if hits, _, _ := SearchReports("9005", 10); len(hits) != 1 {
		t.Fatalf("search 9005: %d hits, want 1", len(hits))
	}
	if hits, _, _ := SearchReports("chat", 10); len(hits) != 0 {
		t.Fatalf("a word is not an avatar id, but search found %d", len(hits))
	}
	if _, found, _ := ReportByID(9999); found {
		t.Fatal("ReportByID found an unknown id")
	}
}
