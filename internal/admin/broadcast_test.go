package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBroadcastOwnerOnly(t *testing.T) {
	fx := newFixture(t, devConfigWithStaff(t))
	var bannerCalls, maintCalls int
	fx.s.sessionCount = func() int { return 3 }
	fx.s.broadcastBanner = func(msg string) (int, int) { bannerCalls++; return 0, 0 }
	fx.s.broadcastMaint = func(msg string, d time.Duration) (int, int) { maintCalls++; return 0, 0 }

	st := fx.client()
	st.signInDev("staff")
	expect(t, st.get("/broadcast"), http.StatusForbidden)
	expect(t, st.post("/broadcast/send", url.Values{
		"csrf":    {st.csrf()},
		"kind":    {"banner"},
		"message": {"hello"},
		"reason":  {"test"},
	}), http.StatusForbidden)
	if bannerCalls != 0 || maintCalls != 0 {
		t.Fatalf("non-owner triggered broadcast: banner=%d maint=%d", bannerCalls, maintCalls)
	}
	if strings.Contains(st.get("/").body, `href="/broadcast"`) {
		t.Error("the staff sidebar links to /broadcast")
	}

	owner := fx.client()
	owner.signInDev("owner")
	r := owner.get("/broadcast")
	expect(t, r, http.StatusOK)
	checkHeaders(t, "/broadcast", r)
	checkPage(t, "/broadcast", r)
	for _, want := range []string{
		"Broadcast announcement",
		"Online sessions now: 3",
		`name="kind"`,
		`name="message"`,
		`name="duration"`,
		`name="reason"`,
		"Send announcement",
		"Recent broadcasts",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("/broadcast missing %q", want)
		}
	}
	if !strings.Contains(owner.get("/").body, `href="/broadcast"`) {
		t.Error("the owner sidebar lacks /broadcast")
	}
}

func TestBroadcastSendBannerAndMaint(t *testing.T) {
	fx := newFixture(t, devConfig())
	var (
		gotBannerMsg string
		gotMaintMsg  string
		gotMaintDur  time.Duration
	)
	fx.s.sessionCount = func() int { return 2 }
	fx.s.broadcastBanner = func(msg string) (int, int) {
		gotBannerMsg = msg
		return 2, 0
	}
	fx.s.broadcastMaint = func(msg string, d time.Duration) (int, int) {
		gotMaintMsg = msg
		gotMaintDur = d
		return 1, 1
	}

	c := fx.client()
	c.signInDev("owner")

	// Banner send (CRLF normalized to LF, trimmed).
	res := c.post("/broadcast/send", url.Values{
		"csrf":     {c.csrf()},
		"kind":     {"banner"},
		"message":  {"  Hello\r\nworld  "},
		"duration": {"5"},
		"reason":   {"  announce event  "},
	})
	expect(t, res, http.StatusSeeOther)
	if loc := res.Header.Get("Location"); loc != "/broadcast?notice=broadcast-sent" {
		t.Fatalf("banner redirect = %q", loc)
	}
	if gotBannerMsg != "Hello\nworld" {
		t.Fatalf("banner message = %q, want %q", gotBannerMsg, "Hello\nworld")
	}
	if gotMaintMsg != "" {
		t.Fatalf("banner send also called maint: %q", gotMaintMsg)
	}

	// Maint send.
	res2 := c.post("/broadcast/send", url.Values{
		"csrf":     {c.csrf()},
		"kind":     {"maint"},
		"message":  {"Emergency maintenance"},
		"duration": {"10"},
		"reason":   {"hotfix deploy"},
	})
	expect(t, res2, http.StatusSeeOther)
	if loc := res2.Header.Get("Location"); loc != "/broadcast?notice=broadcast-sent" {
		t.Fatalf("maint redirect = %q", loc)
	}
	if gotMaintMsg != "Emergency maintenance" || gotMaintDur != 10*time.Minute {
		t.Fatalf("maint call = (%q, %s), want (%q, 10m)", gotMaintMsg, gotMaintDur, "Emergency maintenance")
	}

	// The reason is optional (owner, 2026-11-28): a banner without one is sent.
	expect(t, c.post("/broadcast/send", url.Values{"csrf": {c.csrf()}, "kind": {"banner"}, "message": {"No reason given"}}), http.StatusSeeOther)
	if gotBannerMsg != "No reason given" {
		t.Fatalf("banner without a reason: message = %q", gotBannerMsg)
	}

	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "broadcast-banner", `"reason":"announce event"`) {
		t.Errorf("missing broadcast-banner audit row: %+v", rows)
	}
	if !hasAudit(rows, "dev-owner", "broadcast-maint", `"duration_minutes":10`) {
		t.Errorf("missing broadcast-maint audit row: %+v", rows)
	}

	// Follow redirect renders flash and recent broadcasts table.
	page := c.follow(res2)
	expect(t, page, http.StatusOK)
	checkPage(t, "/broadcast?notice=broadcast-sent", page)
	for _, want := range []string{
		"The announcement was sent to all currently online players. The audit trail has the line.",
		"broadcast-banner",
		"broadcast-maint",
		"hotfix deploy",
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("broadcast page after send missing %q", want)
		}
	}
}

func TestBroadcastSendValidation(t *testing.T) {
	fx := newFixture(t, devConfig())
	var called int
	fx.s.broadcastBanner = func(msg string) (int, int) { called++; return 0, 0 }
	fx.s.broadcastMaint = func(msg string, d time.Duration) (int, int) { called++; return 0, 0 }

	c := fx.client()
	c.signInDev("owner")
	csrf := c.csrf()

	tooLongMsg := strings.Repeat("a", 401)
	tooLongReason := strings.Repeat("r", 201)

	cases := []struct {
		name string
		form url.Values
	}{
		{"empty message", url.Values{"csrf": {csrf}, "kind": {"banner"}, "message": {"   "}, "reason": {"ok"}}},
		{"message > 400 runes", url.Values{"csrf": {csrf}, "kind": {"banner"}, "message": {tooLongMsg}, "reason": {"ok"}}},
		{"reason > 200 runes", url.Values{"csrf": {csrf}, "kind": {"banner"}, "message": {"hi"}, "reason": {tooLongReason}}},
		{"invalid kind", url.Values{"csrf": {csrf}, "kind": {"popup"}, "message": {"hi"}, "reason": {"ok"}}},
		{"maint duration 0", url.Values{"csrf": {csrf}, "kind": {"maint"}, "message": {"hi"}, "duration": {"0"}, "reason": {"ok"}}},
		{"maint duration 2000", url.Values{"csrf": {csrf}, "kind": {"maint"}, "message": {"hi"}, "duration": {"2000"}, "reason": {"ok"}}},
		{"maint duration non-int", url.Values{"csrf": {csrf}, "kind": {"maint"}, "message": {"hi"}, "duration": {"abc"}, "reason": {"ok"}}},
	}
	for _, tc := range cases {
		res := c.post("/broadcast/send", tc.form)
		expect(t, res, http.StatusBadRequest)
		checkPage(t, tc.name, res)
	}
	if called != 0 {
		t.Fatalf("validation failure still invoked broadcast %d times", called)
	}
}
