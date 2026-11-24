package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

func seedCircles(t *testing.T, nextID int64, circles ...store.Circle) {
	t.Helper()
	store.ResetCircles()
	t.Cleanup(store.ResetCircles)
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	next := store.CircleBook{NextID: nextID, Circles: circles}
	if err := store.SaveCirclesLocked(store.CircleState, next); err != nil {
		t.Fatalf("save circles: %v", err)
	}
	store.CircleState = next
}

func TestCirclesRequireSession(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	for _, target := range []string{"/circles", "/circles/1940000001"} {
		expect(t, c.get(target), http.StatusUnauthorized)
	}
	for _, target := range []string{"/circles/1940000001/edit", "/circles/1940000001/kick-member", "/circles/1940000001/disband"} {
		expect(t, c.post(target, url.Values{"reason": {"x"}}), http.StatusUnauthorized)
	}
}

func TestCirclesListSearchEscapeAndLinks(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	pLeader1 := seedPlayer(t, "dev-cl1", "8101", "Alice", 0)
	pMember1 := seedPlayer(t, "dev-cm1", "8102", "Bob", 0)
	pLeader2 := seedPlayer(t, "dev-cl2", "8103", "<b>Mallory</b>", 0)

	tOld := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC).UnixMilli()
	tNew := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC).UnixMilli()

	seedCircles(t, 1940000002,
		store.Circle{
			ID: 1940000001, Code: "000001", Name: "Sushi Club", Intro: "fresh rolls", JoinType: "OPEN",
			Created: tOld, Modified: tOld,
			Members: []store.CircleMember{
				{Aid: "8102", Role: "member", Joined: tOld + 1000},
				{Aid: "8101", Role: "leader", Joined: tOld},
			},
		},
		store.Circle{
			ID: 1940000002, Code: "000002", Name: "<script>alert(1)</script>", Intro: "<img src=x>", JoinType: "INVITE",
			Created: tNew, Modified: tNew, MaxMembers: 30,
			Members: []store.CircleMember{
				{Aid: "8103", Role: "leader", Joined: tNew},
			},
		},
	)

	r := c.get("/circles")
	expect(t, r, http.StatusOK)
	checkPage(t, "/circles", r)

	// Escaped XSS, newest-first order, chat link, player link, member count / cap.
	if strings.Contains(r.body, "<script>alert(1)") || strings.Contains(r.body, "<img src=x>") || strings.Contains(r.body, "<b>Mallory</b>") {
		t.Fatal("unescaped markup reached /circles")
	}
	for _, want := range []string{
		"&lt;script&gt;alert(1)&lt;/script&gt;",
		"&lt;img src=x&gt;",
		"&lt;b&gt;Mallory&lt;/b&gt;",
		`href="/circles/1940000001"`,
		`href="/circles/1940000002"`,
		`href="/chat?kind=circle&amp;channel=1940000001"`,
		`href="/chat?kind=circle&amp;channel=1940000002"`,
		fmt.Sprintf(`href="/players/%d"`, pLeader1),
		fmt.Sprintf(`href="/players/%d"`, pLeader2),
		"1 / 30",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("/circles missing %q", want)
		}
	}
	if strings.Index(r.body, `href="/circles/1940000002"`) > strings.Index(r.body, `href="/circles/1940000001"`) {
		t.Fatal("circles are not sorted newest-first")
	}

	// Search by name, code, ID, intro, member AID, member name.
	for _, q := range []string{"sushi", "000001", "1940000001", "FRESH ROLLS", "8102", "bob"} {
		sr := c.get("/circles?q=" + url.QueryEscape(q))
		expect(t, sr, http.StatusOK)
		if !strings.Contains(sr.body, `href="/circles/1940000001"`) || strings.Contains(sr.body, `href="/circles/1940000002"`) {
			t.Errorf("search %q did not isolate circle 1", q)
		}
	}
	expect(t, c.get("/circles?q="+strings.Repeat("x", playerQueryMax+1)), http.StatusBadRequest)

	// Detail page: roster sorted leader -> vice -> member, links to player and chat.
	det := c.get("/circles/1940000001")
	expect(t, det, http.StatusOK)
	checkPage(t, "/circles/1940000001", det)
	for _, want := range []string{
		"Sushi Club",
		`href="/chat?kind=circle&amp;channel=1940000001"`,
		fmt.Sprintf(`href="/players/%d"`, pLeader1),
		fmt.Sprintf(`href="/players/%d"`, pMember1),
		`action="/circles/1940000001/edit"`,
		`action="/circles/1940000001/kick-member"`,
		`action="/circles/1940000001/disband"`,
	} {
		if !strings.Contains(det.body, want) {
			t.Errorf("circle detail missing %q", want)
		}
	}
	if strings.Index(det.body, "8101") > strings.Index(det.body, "8102") {
		t.Fatal("leader is not sorted before member")
	}
	expect(t, c.get("/circles/0"), http.StatusBadRequest)
	expect(t, c.get("/circles/abc"), http.StatusBadRequest)
	expect(t, c.get("/circles/1940000999"), http.StatusNotFound)
}

func TestCirclesEditKickAndDisband(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	seedPlayer(t, "dev-ce1", "8201", "Leader", 0)
	seedPlayer(t, "dev-ce2", "8202", "Vice", 0)
	seedPlayer(t, "dev-ce3", "8203", "Plain", 0)

	t0 := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	seedCircles(t, 1940000002,
		store.Circle{
			ID: 1940000001, Code: "000001", Name: "Alpha", Intro: "old intro", JoinType: "OPEN",
			Created: t0, Modified: t0,
			Members: []store.CircleMember{
				{Aid: "8203", Role: "member", Joined: t0 + 2000},
				{Aid: "8201", Role: "leader", Joined: t0},
				{Aid: "8202", Role: "vice", Joined: t0 + 1000},
			},
		},
		store.Circle{
			ID: 1940000002, Code: "000002", Name: "Beta", Intro: "", JoinType: "INVITE",
			Created: t0, Modified: t0,
			Members: []store.CircleMember{{Aid: "8204", Role: "leader", Joined: t0}},
		},
	)

	var events []economy.CircleRoomEvent
	oldHook := economy.CircleRoomHook
	economy.CircleRoomHook = func(ev economy.CircleRoomEvent) { events = append(events, ev) }
	t.Cleanup(func() { economy.CircleRoomHook = oldHook })

	// CSRF and fresh-login checks on POST actions.
	expect(t, c.post("/circles/1940000001/edit", url.Values{"name": {"Alpha2"}, "intro": {"x"}, "reason": {"r"}}), http.StatusForbidden)
	fx.clk.advance(6 * time.Minute)
	stale := c.post("/circles/1940000001/edit", url.Values{"csrf": {c.csrf()}, "name": {"Alpha2"}, "intro": {"x"}, "reason": {"r"}})
	expect(t, stale, http.StatusForbidden)
	if !strings.Contains(stale.body, "/auth/login?next=%2Fcircles%2F1940000001") {
		t.Fatalf("stale sign-in redirect = %.300s", stale.body)
	}
	c.signInDev("owner")

	// Validation refusals on edit.
	expect(t, c.post("/circles/1940000001/edit", url.Values{"csrf": {c.csrf()}, "name": {"Beta"}, "intro": {"x"}, "reason": {"dup"}}), http.StatusBadRequest)
	expect(t, c.post("/circles/1940000001/edit", url.Values{"csrf": {c.csrf()}, "name": {""}, "intro": {"x"}, "reason": {"empty"}}), http.StatusBadRequest)
	expect(t, c.post("/circles/1940000999/edit", url.Values{"csrf": {c.csrf()}, "name": {"Nope"}, "intro": {"x"}, "reason": {"missing"}}), http.StatusNotFound)

	// Successful edit.
	ed := c.post("/circles/1940000001/edit", url.Values{"csrf": {c.csrf()}, "name": {"Renamed"}, "intro": {""}, "reason": {"clean up"}})
	expect(t, ed, http.StatusSeeOther)
	if loc := ed.Header.Get("Location"); loc != "/circles/1940000001?notice=circle-edited" {
		t.Fatalf("edit redirect = %q", loc)
	}
	if page := c.follow(ed); !strings.Contains(page.body, notices["circle-edited"]) || !strings.Contains(page.body, "Renamed") {
		t.Fatalf("after edit page missing notice or updated name")
	}

	// Refuse kicking leader (400), kick non-leader member (303 + hook + audit).
	expect(t, c.post("/circles/1940000001/kick-member", url.Values{"csrf": {c.csrf()}, "aid": {"8201"}, "reason": {"leader"}}), http.StatusBadRequest)
	expect(t, c.post("/circles/1940000001/kick-member", url.Values{"csrf": {c.csrf()}, "aid": {"8999"}, "reason": {"nobody"}}), http.StatusNotFound)
	kick := c.post("/circles/1940000001/kick-member", url.Values{"csrf": {c.csrf()}, "aid": {"8202"}, "reason": {"spamming"}})
	expect(t, kick, http.StatusSeeOther)
	if loc := kick.Header.Get("Location"); loc != "/circles/1940000001?notice=circle-member-removed" {
		t.Fatalf("kick redirect = %q", loc)
	}
	if page := c.follow(kick); !strings.Contains(page.body, notices["circle-member-removed"]) || strings.Contains(page.body, `value="8202"`) {
		t.Fatalf("after kick page still has 8202 or lacks notice")
	}
	if len(events) != 1 || events[0].Circle != 1940000001 || len(events[0].Left) != 1 || events[0].Left[0] != "8202" || events[0].Closed {
		t.Fatalf("kick events = %+v", events)
	}

	// Disband circle: redirects to /circles?notice=circle-disbanded, subsequent GET is 404.
	dis := c.post("/circles/1940000001/disband", url.Values{"csrf": {c.csrf()}, "reason": {"shut down"}})
	expect(t, dis, http.StatusSeeOther)
	if loc := dis.Header.Get("Location"); loc != "/circles?notice=circle-disbanded" {
		t.Fatalf("disband redirect = %q", loc)
	}
	if page := c.follow(dis); !strings.Contains(page.body, notices["circle-disbanded"]) || strings.Contains(page.body, "Renamed") {
		t.Fatalf("after disband page still shows Renamed or lacks notice")
	}
	expect(t, c.get("/circles/1940000001"), http.StatusNotFound)
	if len(events) != 2 || !events[1].Closed || len(events[1].Left) != 2 {
		t.Fatalf("disband events = %+v", events)
	}

	rows := auditRows(t)
	for _, want := range []struct {
		action, reason string
	}{
		{"circle-edit", "clean up"},
		{"circle-kick", "spamming"},
		{"circle-disband", "shut down"},
	} {
		if !hasAudit(rows, "dev-owner", want.action, want.reason) {
			t.Errorf("missing audit row action=%s reason=%q", want.action, want.reason)
		}
	}
}
