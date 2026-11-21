package web

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"

	"cherry/internal/httpx"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// reportFixture attaches a database for the test and installs the two diary accounts: friend-token is aid 3 (the reporter in
// these tests) and social-test-token is aid 1 (the host of the guestbook routes).
func reportFixture(t *testing.T) {
	t.Helper()
	testutil.UseDatabase(t)
	diarySocialAccounts(t)
}

// storedReports lists every report row, newest first.
func storedReports(t *testing.T) []store.ReportRow {
	t.Helper()
	rows, err := store.ListReports("", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// captureLog sends the standard logger to a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// TestChatReportStoresTheAuthenticatedReporter: POST /v4/report stores the three chat categories as the account of the
// request's cookie. A reporter named in the body is ignored; the answer is {"result":true}.
func TestChatReportStoresTheAuthenticatedReporter(t *testing.T) {
	reportFixture(t)
	for _, category := range []string{"chat", "chat_img", "tchat"} {
		body := fmt.Sprintf(`{"targetAvatarId":"4","categoryId":%q,"reportType":"R3","reportMsg":"secret words","refId":"ref-1","refDtl":1234,"reporterAvatarId":"1"}`, category)
		rec := diaryCall(t, "friend-token", dsPost, "/v4/report", body)
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
			t.Fatalf("%s report = %d %s", category, rec.Code, rec.Body.String())
		}
	}
	rows := storedReports(t)
	if len(rows) != 3 {
		t.Fatalf("stored %d reports, want 3", len(rows))
	}
	for _, r := range rows {
		if r.ReporterAid != "3" || r.TargetAid != "4" || r.Reason != "R3" || r.Message != "secret words" || r.RefID != "ref-1" || r.RefDetail != "1234" {
			t.Fatalf("stored row = %+v", r)
		}
	}
	if rows[0].Source != store.ReportTChat || rows[1].Source != store.ReportChatImg || rows[2].Source != store.ReportChat {
		t.Fatalf("sources = %s %s %s", rows[0].Source, rows[1].Source, rows[2].Source)
	}
}

// TestChatReportRefusesWhatItCannotStore: an unknown category, a bad reason, a non-numeric refDtl or body, an anonymous
// request and a GET are refused; nothing is stored.
func TestChatReportRefusesWhatItCannotStore(t *testing.T) {
	reportFixture(t)
	bad := map[string]string{
		"category":  `{"targetAvatarId":"4","categoryId":"lounge","reportType":"R1","reportMsg":"x","refId":"r"}`,
		"reason":    `{"targetAvatarId":"4","categoryId":"chat","reportType":"Rx","reportMsg":"x","refId":"r"}`,
		"refDtl":    `{"targetAvatarId":"4","categoryId":"chat","reportType":"R1","reportMsg":"x","refId":"r","refDtl":"detail"}`,
		"target":    `{"targetAvatarId":"","categoryId":"chat","reportType":"R1","reportMsg":"x","refId":"r"}`,
		"not JSON":  `{"targetAvatarId":`,
		"ref width": `{"targetAvatarId":"4","categoryId":"chat","reportType":"R1","reportMsg":"x","refId":"` + strings.Repeat("r", 65) + `"}`,
	}
	for name, body := range bad {
		rec := diaryCall(t, "friend-token", dsPost, "/v4/report", body)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != httpx.BadRequestBody {
			t.Fatalf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	good := `{"targetAvatarId":"4","categoryId":"chat","reportType":"R1","reportMsg":"x","refId":"r"}`
	if rec := diaryCall(t, "", dsPost, "/v4/report", good); rec.Code != http.StatusNotFound || rec.Body.String() != httpx.UnknownSessionBody {
		t.Fatalf("anonymous report = %d %s", rec.Code, rec.Body.String())
	}
	if rec := diaryCall(t, "friend-token", http.MethodGet, "/v4/report", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET report = %d", rec.Code)
	}
	if rows := storedReports(t); len(rows) != 0 {
		t.Fatalf("refused reports stored %d rows", len(rows))
	}
}

// TestDiaryReportRoutesStoreTheirSource: each diary route stores its source with the fields its body names (the comment's
// refDtl, the guestbook's host, the reply's parent), and answers {"result":true}. Its log line names no text.
func TestDiaryReportRoutesStoreTheirSource(t *testing.T) {
	reportFixture(t)
	logs := captureLog(t)
	cases := []struct {
		path, body, source, refDetail, host string
	}{
		{"/v4/report/diary", `{"targetAvatarId":"4","reportType":"R1","reportMsg":"post words","refId":"41"}`, store.ReportDiary, "", ""},
		{"/v4/report/diary/comment", `{"targetAvatarId":"4","reportType":"R2","reportMsg":"comment words","refId":"7","refDtl":41}`, store.ReportDiaryComment, "41", ""},
		{"/v4/report/guestbook", `{"hostAvatarId":"1","targetAvatarId":"4","reportType":"R3","reportMsg":"entry words","refId":"8"}`, store.ReportGuestbook, "", "1"},
		{"/v4/report/guestbookreply", `{"hostAvatarId":"1","targetAvatarId":"4","reportType":"R4","reportMsg":"reply words","parentHistNo":"8","refId":"9"}`, store.ReportGuestbookReply, "8", "1"},
	}
	for _, c := range cases {
		rec := diaryCall(t, "friend-token", dsPost, c.path, c.body)
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
			t.Fatalf("%s = %d %s", c.path, rec.Code, rec.Body.String())
		}
	}
	rows := storedReports(t)
	if len(rows) != len(cases) {
		t.Fatalf("stored %d reports, want %d", len(rows), len(cases))
	}
	for i, c := range cases {
		r := rows[len(cases)-1-i] // oldest first
		if r.Source != c.source || r.ReporterAid != "3" || r.TargetAid != "4" || r.RefDetail != c.refDetail {
			t.Fatalf("%s row = %+v", c.path, r)
		}
		if got := r.Snapshot().Host; got != c.host {
			t.Fatalf("%s host = %q, want %q", c.path, got, c.host)
		}
	}
	for _, text := range []string{"post words", "comment words", "entry words", "reply words"} {
		if strings.Contains(logs.String(), text) {
			t.Fatalf("report text %q reached the log: %q", text, logs.String())
		}
	}
	if rec := diaryCall(t, "", dsPost, "/v4/report/diary", cases[0].body); rec.Code != http.StatusNotFound {
		t.Fatalf("anonymous diary report = %d %s", rec.Code, rec.Body.String())
	}
}

// TestReportDailyCapAnswersTrueAndStoresNothing: identity modes store ReportDailyCap reports a game day; the rest are
// answered true, store nothing and leave a count line only.
func TestReportDailyCapAnswersTrueAndStoresNothing(t *testing.T) {
	reportFixture(t)
	useConfig(t, devFakeConfig())
	logs := captureLog(t)
	body := `{"targetAvatarId":"4","categoryId":"chat","reportType":"R5","reportMsg":"capped words","refId":"r"}`
	for i := range store.ReportDailyCap + 2 {
		rec := diaryCall(t, "friend-token", dsPost, "/v4/report", body)
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
			t.Fatalf("report %d = %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if rows := storedReports(t); len(rows) != store.ReportDailyCap {
		t.Fatalf("stored %d reports, want the cap of %d", len(rows), store.ReportDailyCap)
	}
	if n := strings.Count(logs.String(), "report over the daily cap, not stored source=chat"); n != 2 {
		t.Fatalf("cap count lines = %d, want 2: %q", n, logs.String())
	}
	if strings.Contains(logs.String(), "capped words") {
		t.Fatalf("report text reached the log: %q", logs.String())
	}
}
