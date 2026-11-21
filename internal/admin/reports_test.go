package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

// seedReport files one report through the store (the game's intake) and returns its row. The admin fixture has no identity
// cap, so every report is stored.
func seedReport(t *testing.T, in store.ReportIn) store.ReportRow {
	t.Helper()
	stored, err := store.FileReport(in, time.Now().Add(-24*time.Hour))
	if err != nil || !stored {
		t.Fatalf("file report: stored=%t err=%v", stored, err)
	}
	rows, err := store.ListReports("", 0, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("newest report: %d rows (err %v)", len(rows), err)
	}
	return rows[0]
}

// reportIn is a chat report from reporter about target with reason R3.
func reportIn(source, reporter, target, message string) store.ReportIn {
	return store.ReportIn{Source: source, Reporter: reporter, Target: target, Reason: "R3", Message: message}
}

// TestReportPagesNeedASession: the queue, a report and the two decisions answer without a session.
func TestReportPagesNeedASession(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	expect(t, c.get("/reports"), http.StatusUnauthorized)
	expect(t, c.get("/reports/1"), http.StatusUnauthorized)
	if r := c.post("/reports/1/resolve", url.Values{"note": {"x"}}); r.StatusCode == http.StatusSeeOther {
		t.Fatal("a decision without a session was accepted")
	}
}

// TestReportQueueFiltersAndOpensOnOpen: the queue lists the open reports, newest first, with the reason's label and the
// filters; a bad status or page marker is refused; the sidebar shows the open count.
func TestReportQueueFiltersAndOpensOnOpen(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	seedReport(t, reportIn(store.ReportChat, "4101", "4102", "first message"))
	second := seedReport(t, reportIn(store.ReportSquare, "4103", "4102", "second message"))
	third := seedReport(t, reportIn(store.ReportDiary, "4104", "4105", "third message"))
	if err := store.SetReportStatus(second.ID, store.ReportDismissed, "not a violation", "dev-owner"); err != nil {
		t.Fatal(err)
	}

	open := getOK(t, c, "/reports")
	if strings.Contains(open, "second message") {
		t.Error("the open queue lists a dismissed report")
	}
	newest, oldest := strings.Index(open, "third message"), strings.Index(open, "first message")
	if newest < 0 || oldest < 0 || newest > oldest {
		t.Fatalf("open queue order: third at %d, first at %d", newest, oldest)
	}
	for _, want := range []string{"Chat (1:1)", "R3: Nuisance/Harassment", "Reports (2)", fmt.Sprintf(`href="/reports/%d"`, third.ID)} {
		if !strings.Contains(open, want) {
			t.Errorf("queue does not show %q", want)
		}
	}
	if resolved := c.get("/reports?status=dismissed").body; !strings.Contains(resolved, "second message") || strings.Contains(resolved, "third message") {
		t.Error("the dismissed filter does not show the dismissed report alone")
	}
	if all := c.get("/reports?status=all").body; !strings.Contains(all, "first message") || !strings.Contains(all, "second message") || !strings.Contains(all, "third message") {
		t.Error("the all filter does not show every report")
	}
	for _, bad := range []string{"/reports?status=bogus", "/reports?before=x", "/reports?before=1&before=2"} {
		expect(t, c.get(bad), http.StatusBadRequest)
	}
}

// TestReportQueuePagesOlderReports: a page holds 50 reports and links to the older ones with the marker.
func TestReportQueuePagesOlderReports(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	for range reportPage + 1 {
		seedReport(t, reportIn(store.ReportChat, "4101", "4102", "paged"))
	}
	page := c.get("/reports").body
	if n := strings.Count(page, "<td>Chat (1:1)</td>"); n != reportPage {
		t.Fatalf("first page has %d rows, want %d", n, reportPage)
	}
	older := hrefsWith(page, "/reports?before=")
	if len(older) != 1 {
		t.Fatalf("older link = %v, want one", older)
	}
	if rest := c.get(older[0]).body; strings.Count(rest, "<td>Chat (1:1)</td>") != 1 {
		t.Fatal("the older page does not hold the last report")
	}
}

// TestReportDetailShowsTheSnapshotAndTheLinks: the detail shows the facts, the item as stored (or that it is gone), the
// chat-free snapshot, links to the player pages of the reporter and the target, and the decision forms while it is open.
func TestReportDetailShowsTheSnapshotAndTheLinks(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	reporter := seedPlayer(t, "dev-rep", "4101", "Reporter", 0)
	target := seedPlayer(t, "dev-tgt", "4102", "Target", 0)

	store.SocialMu.Lock()
	store.DiaryPosts = []store.DiaryPost{{DiaryNo: "41", AvatarID: "4102", Nickname: "Target", RegDate: "1700000000000", Content: "the post words"}}
	store.SocialMu.Unlock()
	t.Cleanup(func() {
		store.SocialMu.Lock()
		store.DiaryPosts = nil
		store.SocialMu.Unlock()
	})
	found := seedReport(t, store.ReportIn{Source: store.ReportDiary, Reporter: "4101", Target: "4102", Reason: "R1", Message: "why", RefID: "41"})
	gone := seedReport(t, store.ReportIn{Source: store.ReportDiary, Reporter: "4101", Target: "4102", Reason: "R1", RefID: "99"})

	page := getOK(t, c, fmt.Sprintf("/reports/%d", found.ID))
	for _, want := range []string{"the post words", fmt.Sprintf(`href="/players/%d"`, target), fmt.Sprintf(`href="/players/%d"`, reporter),
		"R1: Dating/Meeting purposes", "why", "2 reports about this avatar, any status", fmt.Sprintf(`action="/reports/%d/resolve"`, found.ID), `name="csrf"`} {
		if !strings.Contains(page, want) {
			t.Errorf("detail of the found report does not show %q", want)
		}
	}
	if goneBody := c.get(fmt.Sprintf("/reports/%d", gone.ID)).body; !strings.Contains(goneBody, "The item is gone") {
		t.Error("a report whose post is gone does not say so")
	}
	expect(t, c.get("/reports/999999"), http.StatusNotFound)
	expect(t, c.get("/reports/0"), http.StatusBadRequest)
	checkPage(t, "report detail", c.get(fmt.Sprintf("/reports/%d", found.ID)))
	checkPage(t, "report queue", c.get("/reports?status=all"))
}

// TestReportLinksToADeletedAvatar: a target with no account but a deleted avatar links to that record.
func TestReportLinksToADeletedAvatar(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	did := seedDeleted(t, "4555", "", "Gone", store.AvatarRecord{})
	seedReport(t, reportIn(store.ReportChat, "4101", "4555", "gone target"))
	if page := c.get("/reports").body; !strings.Contains(page, fmt.Sprintf(`href="/players/deleted/%d"`, did)) {
		t.Fatal("the queue does not link the deleted avatar's record")
	}
}

// TestResolveAndDismissAreAudited: a decision needs its CSRF token and a note, is audited with the note, can happen once, and
// the GET form of the route is refused.
func TestResolveAndDismissAreAudited(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	a := seedReport(t, reportIn(store.ReportGuestbook, "4101", "4102", "to resolve"))
	b := seedReport(t, reportIn(store.ReportCircle, "4101", "4102", "to dismiss"))
	resolve := fmt.Sprintf("/reports/%d/resolve", a.ID)

	expect(t, c.post(resolve, url.Values{"note": {"no token"}}), http.StatusForbidden)
	expect(t, c.post(resolve, url.Values{"csrf": {c.csrf()}, "note": {"   "}}), http.StatusBadRequest)
	expect(t, c.post(resolve, url.Values{"csrf": {c.csrf()}, "note": {strings.Repeat("n", 201)}}), http.StatusBadRequest)
	ok := c.post(resolve, url.Values{"csrf": {c.csrf()}, "note": {"warned the player"}})
	expect(t, ok, http.StatusSeeOther)
	if loc := ok.Header.Get("Location"); loc != fmt.Sprintf("/reports/%d?notice=report-resolved", a.ID) {
		t.Fatalf("resolve redirects to %q", loc)
	}
	if shown := c.get(fmt.Sprintf("/reports/%d?notice=report-resolved", a.ID)).body; !strings.Contains(shown, "The report is resolved.") {
		t.Error("the resolved report does not show its notice")
	}
	expect(t, c.post(resolve, url.Values{"csrf": {c.csrf()}, "note": {"again"}}), http.StatusConflict)
	expect(t, c.get(resolve), http.StatusMethodNotAllowed)

	dismiss := fmt.Sprintf("/reports/%d/dismiss", b.ID)
	expect(t, c.post(dismiss, url.Values{"csrf": {c.csrf()}, "note": {"not a violation"}}), http.StatusSeeOther)

	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "report-resolve", "warned the player") {
		t.Error("no report-resolve audit row with the note")
	}
	if !hasAudit(rows, "dev-owner", "report-dismiss", "not a violation") {
		t.Error("no report-dismiss audit row with the note")
	}
	if n, _ := store.CountOpenReports(); n != 0 {
		t.Fatalf("%d reports still open after both decisions", n)
	}
}

// TestSearchAndDashboardShowReports: the header search finds the reports an avatar id names, and the dashboard shows the open
// count.
func TestSearchAndDashboardShowReports(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	r := seedReport(t, reportIn(store.ReportRoomParty, "4201", "4202", "searched"))
	search := c.get("/search?q=4202").body
	if !strings.Contains(search, "Reports (1)") || !strings.Contains(search, fmt.Sprintf(`href="/reports/%d"`, r.ID)) {
		t.Error("search by the target's avatar id does not find the report")
	}
	if none := c.get("/search?q=4299").body; !strings.Contains(none, "No report names this avatar id") {
		t.Error("search for an avatar with no report does not say so")
	}
	if dash := c.get("/").body; !strings.Contains(dash, "Open player reports: <strong>1</strong>") {
		t.Error("the dashboard does not show the open report count")
	}
}

// getOK is a GET that must answer 200; it returns the body.
func getOK(t *testing.T, c *client, target string) string {
	t.Helper()
	r := c.get(target)
	expect(t, r, http.StatusOK)
	return r.body
}

// TestChatReportDetailShowsTheLines: a chat report's page shows the target's own lines in the channel it names, and a link to
// the chat log around the report's time.
func TestChatReportDetailShowsTheLines(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	chatWriter(t)
	say(store.ChatCircle, "1940000001", "4102", "Target", "the circle line")
	say(store.ChatCircle, "1940000001", "4103", "Other", "not the target")
	store.FlushChatLog()
	r := seedReport(t, store.ReportIn{Source: store.ReportCircle, Reporter: "4101", Target: "4102", Reason: "R5", Channel: "1940000001"})
	page := getOK(t, c, fmt.Sprintf("/reports/%d", r.ID))
	for _, want := range []string{"the circle line", "Chat channel", "Chat log around the time of the report"} {
		if !strings.Contains(page, want) {
			t.Errorf("chat report page does not show %q", want)
		}
	}
	if strings.Contains(page, "not the target") {
		t.Error("the page shows a line of another player")
	}
	checkPage(t, "chat report detail", c.get(fmt.Sprintf("/reports/%d", r.ID)))
}
