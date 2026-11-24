package admin

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"cherry/internal/lpn"
	"cherry/internal/store"
)

// The Insights pages (PLAN Roadmap 8, staff "Insights"): Online now, Economy and Activity. The fixture's clock is 2026-11-12
// 09:00 UTC; the ledger, chat and account rows the tests seed are placed relative to it.

// fakeOnline replaces the live connections of the server with conns and returns a recorder of the kicks.
func fakeOnline(fx *fixture, conns ...lpn.Connection) *[]string {
	fx.s.online = func() []lpn.Connection { return conns }
	var closed []string
	fx.s.closeAid = func(aid string) { closed = append(closed, aid) }
	return &closed
}

// seedLedgerRows writes ledger lines straight into the attached database: time (RFC 3339), aid, currency, delta, reason.
func seedLedgerRows(t *testing.T, rows ...[5]any) {
	t.Helper()
	if err := store.AttachedDB().Update(func(tx *sql.Tx) error {
		for _, r := range rows {
			if _, err := tx.Exec(`INSERT INTO ledger (time, aid, currency, delta, balance, reason) VALUES (?, ?, ?, ?, 0, ?)`, r[0], r[1], r[2], r[3], r[4]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// setCreated moves an account's creation time (the seeded accounts are made at the real clock, not the fixture's).
func setCreated(t *testing.T, id int64, created string) {
	t.Helper()
	if err := store.AttachedDB().Update(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE accounts SET created = ? WHERE id = ?`, created, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestInsightPagesNeedASessionAndSitInTheNav: the four pages are the sign-in page without a session; with one they answer, and
// the sidebar lists the Insights group between Moderation and Content with the pages in order.
func TestInsightPagesNeedASessionAndSitInTheNav(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	fakeOnline(fx)
	for _, p := range []string{"/online", "/economy", "/activity", "/geography"} {
		expect(t, c.get(p), http.StatusUnauthorized)
	}
	c.signInDev("owner")
	for _, p := range []string{"/online", "/economy", "/economy?window=7d", "/economy?window=30d", "/activity", "/geography"} {
		r := c.get(p)
		expect(t, r, http.StatusOK)
		checkPage(t, p, r)
	}
	online := c.get("/online").body
	for _, want := range []string{`href="/online" aria-current="page"`, `href="/economy"`, `href="/activity"`, `href="/geography"`} {
		if !strings.Contains(online, want) {
			t.Errorf("the nav lacks %s", want)
		}
	}
	var titles []string
	for _, g := range navGroups {
		titles = append(titles, g.Title)
	}
	if got := strings.Join(titles, ","); got != "Overview,Players,Moderation,Insights,Content,System" {
		t.Errorf("nav groups %s", got)
	}
	if n := len(navGroups[3].Links); n != 4 {
		t.Errorf("Insights has %d pages, want 4", n)
	}
}

// TestGeographyPageRendersStatsAndCSP: Geography normalizes countries, aggregates regions and avatar types,
// cross-references live online connections, and renders strictly without inline markup.
func TestGeographyPageRendersStatsAndCSP(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	seedPlayer(t, "dev-geo1", "2301", "IndoLower", 0)
	seedPlayer(t, "dev-geo2", "2302", "IndoUpper", 0)
	seedPlayer(t, "dev-geo3", "2303", "ThaiPlayer", 0)
	seedPlayer(t, "dev-geo4", "2304", "UnknownCountry", 0)

	recentMs := fx.clk.t.Add(-24 * time.Hour).UnixMilli()
	oldMs := fx.clk.t.Add(-10 * 24 * time.Hour).UnixMilli()
	store.AccountsMu.Lock()
	for _, set := range []struct {
		id, country, gender string
		created             int64
	}{
		{"dev-geo1", "id", "FEMALE", recentMs},
		{"dev-geo2", "ID", "MALE", oldMs},
		{"dev-geo3", "th", "ANIMAL", recentMs},
		{"dev-geo4", "", "", oldMs},
	} {
		acc := store.IdentityAccountLocked(set.id)
		acc.Country = set.country
		acc.Gender = set.gender
		acc.AvatarCreated = set.created
		if err := store.SaveLocked(nil, acc); err != nil {
			store.AccountsMu.Unlock()
			t.Fatal(err)
		}
	}
	store.AccountsMu.Unlock()

	fakeOnline(fx,
		lpn.Connection{Aid: 2301, Listener: "session", Since: fx.clk.t},
		lpn.Connection{Aid: 2301, Listener: "gateway", Since: fx.clk.t},
		lpn.Connection{Aid: 2304, Listener: "session", Since: fx.clk.t},
	)

	r := c.get("/geography")
	expect(t, r, http.StatusOK)
	checkPage(t, "/geography", r)
	for _, want := range []string{
		`href="/geography" aria-current="page"`,
		"<h1>Geography</h1>",
		"<h2>Total avatars</h2>\n<p class=\"big\">4</p>",
		"<h2>Countries</h2>\n<p class=\"big\">2</p>",
		"<h2>Top country</h2>\n<p class=\"big\">Indonesia</p>",
		"<h2>Online now</h2>\n<p class=\"big\">1 / 2</p>",
		"<h2>Unset country</h2>\n<p class=\"big\">1</p>",
		"Southeast Asia",
		"Thailand",
		"bar-fill w-",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the Geography page lacks %q", want)
		}
	}
}

// TestOnlinePageListsConnectionsPlacesAndKicks: each row names its player (linked), its listener, its network (or a dash), its
// rooms and a kick form; a connection with no account has no kick. The counts per room count each player once.
func TestOnlinePageListsConnectionsPlacesAndKicks(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-online1", "2001", "Alpha", 0)
	since := fx.clk.t.Add(-90 * time.Minute)
	fakeOnline(fx,
		lpn.Connection{Aid: 2001, Listener: "session", Remote: "203.0.113.0/24", Since: since},
		lpn.Connection{Aid: 2001, Listener: "gateway", Remote: "203.0.113.0/24", Since: since.Add(time.Minute),
			Places: []lpn.Place{{Kind: "garden", ID: "2002"}}},
		lpn.Connection{Aid: 3001, Listener: "session", Remote: "", Since: since.Add(2 * time.Minute)},
	)
	body := c.get("/online").body
	for _, want := range []string{
		playerURL(id) + `">Alpha</a>`,
		"203.0.113.0/24",
		"Garden of 2002",
		`<td>In a Garden visit</td><td class="num">1</td>`,
		`<td>In a public Square</td><td class="num">0</td>`,
		`<td>In a private Square</td><td class="num">0</td>`,
		`<td>In none of those rooms</td><td class="num">1</td>`,
		`<span class="muted">3001</span>`,
		"no account",
		`action="` + playerURL(id) + `/kick"`,
		`name="reason" required maxlength="200"`,
		`data-confirm="Kick this player?`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Online page lacks %q", want)
		}
	}
	if n := strings.Count(body, "/kick\""); n != 2 {
		t.Errorf("%d kick forms, want 2 (one per connection of the player with an account)", n)
	}
	if !strings.Contains(body, "<h2>Players online</h2>\n<p class=\"big\">2</p>") {
		t.Error("the Players online card does not count the distinct players: two players, one with two connections, want 2")
	}
}

// TestOnlinePageEmptyAndDistinctPlayers: with nobody connected the page says so; the players card counts distinct players.
func TestOnlinePageEmptyAndDistinctPlayers(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	fakeOnline(fx)
	if body := c.get("/online").body; !strings.Contains(body, "Nobody is connected with a login right now.") {
		t.Error("the empty Online page lacks its line")
	}
	fakeOnline(fx,
		lpn.Connection{Aid: 2001, Listener: "session", Since: fx.clk.t},
		lpn.Connection{Aid: 2001, Listener: "gateway", Since: fx.clk.t},
	)
	onlineBody := c.get("/online").body
	if !strings.Contains(onlineBody, "<h2>Players online</h2>\n<p class=\"big\">1</p>") {
		t.Error("two connections of one player count as one player")
	}
}

// TestOnlinePageSplitsPublicAndPrivateSquares: public and private Square rooms are counted separately under "Where they are"
// (counting distinct players per bucket) and labeled in the connection list.
func TestOnlinePageSplitsPublicAndPrivateSquares(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	seedPlayer(t, "dev-sq1", "2011", "PubOne", 0)
	seedPlayer(t, "dev-sq2", "2012", "PubTwo", 0)
	seedPlayer(t, "dev-sq3", "2013", "PrivOne", 0)
	since := fx.clk.t.Add(-10 * time.Minute)
	fakeOnline(fx,
		lpn.Connection{Aid: 2011, Listener: "session", Since: since,
			Places: []lpn.Place{{Kind: "square", ID: "6", Room: "1"}}},
		lpn.Connection{Aid: 2011, Listener: "gateway", Since: since,
			Places: []lpn.Place{{Kind: "square", ID: "6", Room: "1"}}},
		lpn.Connection{Aid: 2012, Listener: "session", Since: since,
			Places: []lpn.Place{{Kind: "square", ID: "6", Room: "2"}}},
		lpn.Connection{Aid: 2013, Listener: "session", Since: since,
			Places: []lpn.Place{{Kind: "square", ID: "6", Room: "[0042]", Secret: true}}},
	)
	r := c.get("/online")
	expect(t, r, http.StatusOK)
	checkPage(t, "/online", r)
	for _, want := range []string{
		`<td>In a public Square</td><td class="num">2</td>`,
		`<td>In a private Square</td><td class="num">1</td>`,
		`<td>In none of those rooms</td><td class="num">0</td>`,
		"Treasure Island room 1",
		"Treasure Island room 2",
		"Treasure Island private room [0042]",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the Online page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "<td>In a Square</td>") {
		t.Error("the old combined \"In a Square\" row is still present")
	}
}

// TestKickClosesAndAuditsWithoutBanning: a kick with a reason closes the account's connections through the close hook, writes
// the audit row with its reason, saves no ban, and answers with the Online page and its notice.
func TestKickClosesAndAuditsWithoutBanning(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-kick1", "2001", "Kicked", 0)
	closed := fakeOnline(fx)

	r := c.post(playerURL(id)+"/kick", url.Values{"csrf": {c.csrf()}, "reason": {"  spam in the Square "}})
	expect(t, r, http.StatusSeeOther)
	if loc := r.Header.Get("Location"); loc != "/online?notice=kicked" {
		t.Errorf("kick redirects to %q", loc)
	}
	if len(*closed) != 1 || (*closed)[0] != "2001" {
		t.Fatalf("the close hook got %v, want [2001]", *closed)
	}
	if !hasAudit(auditRows(t), "dev-owner", "kick", "spam in the Square") {
		t.Error("no audit row for the kick with its reason")
	}
	if p := playerOf(t, id); p.Ban != nil {
		t.Errorf("a kick left a ban: %+v", p.Ban)
	}
	if body := c.get("/online?notice=kicked").body; !strings.Contains(body, "This is not a ban") {
		t.Error("the kick's notice is not shown")
	}
}

// TestKickIsRefusedWithoutItsGuards: a kick without the CSRF token, without a reason, for an unknown player, or without a
// Discord login in the last five minutes closes nothing and writes no audit row.
func TestKickIsRefusedWithoutItsGuards(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-kick2", "2002", "Guarded", 0)
	closed := fakeOnline(fx)
	before := len(auditRows(t))

	expect(t, c.post(playerURL(id)+"/kick", url.Values{"reason": {"no token"}}), http.StatusForbidden)
	expect(t, c.post(playerURL(id)+"/kick", url.Values{"csrf": {c.csrf()}, "reason": {"   "}}), http.StatusBadRequest)
	expect(t, c.post("/players/999999/kick", url.Values{"csrf": {c.csrf()}, "reason": {"nobody"}}), http.StatusNotFound)
	if len(*closed) != 0 || len(auditRows(t)) != before {
		t.Fatalf("a refused kick closed %v or wrote an audit row", *closed)
	}

	fx.clk.advance(freshWindow + time.Minute)
	r := c.post(playerURL(id)+"/kick", url.Values{"csrf": {c.csrf()}, "reason": {"too late"}})
	expect(t, r, http.StatusForbidden)
	if !strings.Contains(r.body, "needs a Discord sign-in in the last five minutes") {
		t.Error("the stale kick does not ask for a fresh sign-in")
	}
	if len(*closed) != 0 {
		t.Fatalf("a stale kick closed %v", *closed)
	}
}

// seedEconomy inserts the economy's ledger for the fixture's clock: two accounts that earn and spend, one reason per kind.
func seedEconomy(t *testing.T, fx *fixture) (alpha, beta int64) {
	t.Helper()
	alpha = seedPlayer(t, "dev-eco1", "2101", "Alpha", 120)
	beta = seedPlayer(t, "dev-eco2", "2102", "Beta", 40)
	now := fx.clk.t
	at := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339) }
	seedLedgerRows(t,
		[5]any{at(time.Hour), "2101", "gems", 100, "welcome gift"},
		[5]any{at(2 * time.Hour), "2101", "gems", -30, "shop:buy:CUAA"},
		[5]any{at(3 * time.Hour), "2102", "gems", 50, "butler:order:4:2"},
		[5]any{at(4 * time.Hour), "2102", "cash", 7, "butler:order:4:3"},
		[5]any{at(5 * time.Hour), "2102", "gems", -10, "gift send to 2101 CUEB"},
		[5]any{at(3 * 24 * time.Hour), "2101", "gems", 5, "heart:2026-11-09"},
		[5]any{at(40 * 24 * time.Hour), "2101", "gems", 999, "welcome gift"},
	)
	return alpha, beta
}

// TestEconomyFlowGroupsAndTopLists: the window cuts the ledger by time; the flow sums per currency; the reason groups and both
// top lists show, the players linked; an unknown window is refused.
func TestEconomyFlowGroupsAndTopLists(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	alpha, beta := seedEconomy(t, fx)

	day, err := economyAt(economyWindows[0], fx.clk.t)
	if err != nil {
		t.Fatal(err)
	}
	if len(day.Flow) != 2 || day.Flow[0].Currency != "cash" || day.Flow[1].Currency != "gems" {
		t.Fatalf("flow %+v, want cash and gems", day.Flow)
	}
	if g := day.Flow[1]; g.Minted != 150 || g.Spent != 40 || g.Net != 110 || g.Lines != 4 {
		t.Errorf("gems flow %+v, want minted 150, spent 40, net 110, 4 lines", g)
	}
	if day.Holdings.Gems != 160 || day.Holdings.Holders != 2 {
		t.Errorf("holdings %+v, want 160 Gems held by 2 accounts", day.Holdings)
	}
	if len(day.Earners) != 2 || day.Earners[0].Name != "Alpha" || day.Earners[0].Gems != 100 || day.Earners[1].Name != "Beta" || day.Earners[1].Cash != 7 {
		t.Errorf("earners %+v, want Alpha 100 Gems and Beta 50 Gems with 7 Cash", day.Earners)
	}
	if len(day.Spenders) != 2 || day.Spenders[0].Name != "Alpha" || day.Spenders[0].Gems != 30 {
		t.Errorf("spenders %+v, want Alpha 30 first", day.Spenders)
	}

	week, err := economyAt(economyWindows[1], fx.clk.t)
	if err != nil {
		t.Fatal(err)
	}
	if g := week.Flow[1]; g.Minted != 155 {
		t.Errorf("7-day gems minted %d, want 155 (the heart line from three days back counts, the one from 40 days does not)", g.Minted)
	}

	body := c.get("/economy").body
	for _, want := range []string{
		playerURL(alpha) + `">Alpha</a>`, playerURL(beta) + `">Beta</a>`,
		`class="mono">shop:buy</td>`, `class="mono">butler:order</td>`, `class="mono">gift send</td>`,
		`<td>gems</td><td class="num">150</td><td class="num">40</td><td class="num">110</td>`,
		`aria-current="page">24 hours</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Economy page lacks %q", want)
		}
	}
	if !strings.Contains(c.get("/economy?window=7d").body, `aria-current="page">7 days</a>`) {
		t.Error("the 7-day window is not the current one")
	}
	expect(t, c.get("/economy?window=1y"), http.StatusBadRequest)
}

// TestEconomyFiguresAreCachedForAMinute: a line written after a figure was read shows only once the minute has passed.
func TestEconomyFiguresAreCachedForAMinute(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	seedPlayer(t, "dev-cache1", "2201", "Cache", 0)
	seedLedgerRows(t, [5]any{fx.clk.t.Add(-time.Hour).UTC().Format(time.RFC3339), "2201", "gems", 100, "welcome gift"})
	minted := regexp.MustCompile(`<td>gems</td><td class="num">(\d+)</td>`)
	read := func() string {
		m := minted.FindStringSubmatch(c.get("/economy").body)
		if m == nil {
			t.Fatal("no gems row on the page")
		}
		return m[1]
	}
	if got := read(); got != "100" {
		t.Fatalf("minted %s, want 100", got)
	}
	seedLedgerRows(t, [5]any{fx.clk.t.Add(-30 * time.Minute).UTC().Format(time.RFC3339), "2201", "gems", 50, "welcome gift"})
	fx.clk.advance(30 * time.Second)
	if got := read(); got != "100" {
		t.Errorf("minted %s within the minute, want the cached 100", got)
	}
	fx.clk.advance(31 * time.Second)
	if got := read(); got != "150" {
		t.Errorf("minted %s after the minute, want 150", got)
	}
}

// TestMemoizedKeepsOnlySuccessesForAMinute: a value is reused for insightsTTL; a failed read is not kept.
func TestMemoizedKeepsOnlySuccessesForAMinute(t *testing.T) {
	fx := newFixture(t, devConfig())
	calls := 0
	read := func() (int, error) {
		calls++
		return calls, nil
	}
	first, _ := memoized(fx.s, "k", read)
	again, _ := memoized(fx.s, "k", read)
	if first != 1 || again != 1 || calls != 1 {
		t.Fatalf("within the minute: values %d, %d after %d calls", first, again, calls)
	}
	fx.clk.advance(insightsTTL)
	if v, _ := memoized(fx.s, "k", read); v != 2 || calls != 2 {
		t.Fatalf("after the minute: value %d after %d calls, want a fresh read", v, calls)
	}
	failures := 0
	fail := func() (int, error) {
		failures++
		if failures == 1 {
			return 0, errors.New("database busy")
		}
		return 7, nil
	}
	if _, err := memoized(fx.s, "f", fail); err == nil {
		t.Fatal("the error is lost")
	}
	if v, err := memoized(fx.s, "f", fail); err != nil || v != 7 || failures != 2 {
		t.Fatalf("after a failure: %d (err %v), failures %d: the failure was kept", v, err, failures)
	}
}

// TestEconomyWindowValues: the window names and spans, the default, and the refusal of an unknown value.
func TestEconomyWindowValues(t *testing.T) {
	w, ok := economyWindowOf("")
	if !ok || w.Key != "24h" || w.Span != 24*time.Hour {
		t.Errorf("default window %+v", w)
	}
	for key, span := range map[string]time.Duration{"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour} {
		if w, ok := economyWindowOf(key); !ok || w.Span != span {
			t.Errorf("window %s: %+v", key, w)
		}
	}
	if _, ok := economyWindowOf("1y"); ok {
		t.Error("an unknown window is accepted")
	}
}

// seedDays puts one chat message, one ledger line and one account on each day given, as the fixture's UTC days.
func seedDays(t *testing.T, fx *fixture, days ...int) {
	t.Helper()
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	for _, d := range days {
		day := base.AddDate(0, 0, d)
		if err := store.AttachedDB().Update(func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO chat_log (time, kind, channel, sender, sender_name, text) VALUES (?, 'lounge', '1960000001', '2101', 'A', 'hi')`,
				day.Add(12*time.Hour).UnixMilli()); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO ledger (time, aid, currency, delta, balance, reason) VALUES (?, '2101', 'gems', 1, 1, 'welcome gift')`,
				day.Add(13*time.Hour).UTC().Format(time.RFC3339)); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO accounts (aid, name, identity, guest_code_hash, created, updated, data_hash, data) VALUES (?, 'n', '', '', ?, ?, 'h', '{}')`,
				fmt.Sprintf("%d", 5000+d), day.Add(14*time.Hour).Format(time.RFC3339Nano), day.Format(time.RFC3339Nano))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestActivitySeriesIsThirtyUTCDaysWithGapsAsZero: the series is the last 30 UTC days oldest first, every day is present, a day
// with nothing is zero, and the day of a message is the UTC day it was sent.
func TestActivitySeriesIsThirtyUTCDaysWithGapsAsZero(t *testing.T) {
	fx := newFixture(t, devConfig())
	seedPlayer(t, "dev-act1", "2101", "Alpha", 0)
	seedDays(t, fx, 0, 1, 3, 28, 29) // 2026-10-14, 10-15, 10-17, 11-11 and 11-12 (the fixture's day)
	page, err := activityAt(fx.clk.t)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Days) != 30 || page.Days[0].Day != "2026-10-14" || page.Days[29].Day != "2026-11-12" {
		t.Fatalf("days run %s to %s (%d days)", page.Days[0].Day, page.Days[len(page.Days)-1].Day, len(page.Days))
	}
	got := map[string]int64{}
	for _, d := range page.Days {
		got[d.Day] = d.Chat
	}
	if got["2026-10-14"] != 1 || got["2026-10-15"] != 1 || got["2026-10-16"] != 0 || got["2026-10-17"] != 1 || got["2026-11-10"] != 0 || got["2026-11-11"] != 1 || got["2026-11-12"] != 1 {
		t.Errorf("chat per day %v", got)
	}
	if len(page.Charts) != 4 {
		t.Fatalf("%d charts, want 4", len(page.Charts))
	}
	for _, ch := range page.Charts {
		if len(ch.Bars) != 30 || len(ch.Ticks) != 3 {
			t.Errorf("chart %q: %d bars, %d ticks", ch.Title, len(ch.Bars), len(ch.Ticks))
		}
	}
	if bar := page.Charts[1].Bars[2]; bar.H != 0 || bar.Title != "2026-10-16: 0" {
		t.Errorf("a day with nothing: %+v", bar)
	}
	if bar := page.Charts[1].Bars[0]; bar.H == 0 || bar.Title != "2026-10-14: 1" {
		t.Errorf("a day with one message: %+v", bar)
	}
}

// TestActivityPageRendersChartsAndTable: the page has its four SVG charts with hover titles, and the table of the same 30 days.
func TestActivityPageRendersChartsAndTable(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	seedPlayer(t, "dev-act2", "2101", "Alpha", 0)
	seedDays(t, fx, 29)
	r := c.get("/activity")
	expect(t, r, http.StatusOK)
	checkPage(t, "/activity", r)
	if n := strings.Count(r.body, `<figure class="chart">`); n != 4 {
		t.Errorf("%d charts, want 4", n)
	}
	if n := strings.Count(r.body, `<rect class="bar"`); n != 120 {
		t.Errorf("%d bars, want 120 (30 per chart)", n)
	}
	for _, want := range []string{"Daily active players (proxy)", "Sign-ups per day", `aria-label="Chat messages per day"`, "<title>2026-11-12: 1</title>", "It counts activity, not logins"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the Activity page lacks %q", want)
		}
	}
	if n := strings.Count(r.body, `<tr><td>2026-`); n != 30 {
		t.Errorf("the table has %d day rows, want 30", n)
	}
}

// TestNiceMaxRoundsToOneTwoFiveTen: the top gridline is 1, 2, 5 or 10 times a power of ten, at least the peak.
func TestNiceMaxRoundsToOneTwoFiveTen(t *testing.T) {
	for in, want := range map[int64]int64{0: 1, 1: 1, 2: 2, 3: 5, 5: 5, 6: 10, 37: 50, 100: 100, 101: 200, 999: 1000} {
		if got := niceMax(in); got != want {
			t.Errorf("niceMax(%d) = %d, want %d", in, got, want)
		}
	}
}

// TestChartEscapesEveryLabel: a title, a note, a tick or a bar title that holds markup reaches the page as text, never as
// elements.
func TestChartEscapesEveryLabel(t *testing.T) {
	evil := `<img src=x onerror=alert(1)>`
	ch := chart{Title: evil, Note: evil, Width: chartWidth, Height: chartHeight, Left: plotLeft, Right: plotRight, Bottom: plotBottom,
		Ticks:  []chartTick{{Y: 10, Text: evil}},
		Bars:   []chartBar{{X: 1, Y: 2, W: 3, H: 4, Title: `"><script>alert(2)</script>`}},
		Labels: []chartLabel{{X: 5, Text: evil}}}
	var buf bytes.Buffer
	if err := pages["activity"].ExecuteTemplate(&buf, "chart", ch); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, bad := range []string{"<img", "<script"} {
		if strings.Contains(out, bad) {
			t.Errorf("the chart output holds %q as markup", bad)
		}
	}
	if !strings.Contains(out, "&lt;img") {
		t.Error("the escaped label is not in the output")
	}
}

// TestDashboardShowsTheInsightsRow: the dashboard links the three pages and shows the mini-stats (the economy and the activity
// are read through the same cache the pages use).
func TestDashboardShowsTheInsightsRow(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	fakeOnline(fx, lpn.Connection{Aid: 2001, Listener: "session", Since: fx.clk.t})
	seedPlayer(t, "dev-dash2", "2101", "Alpha", 75)
	body := c.get("/").body
	for _, want := range []string{`href="/online"`, `href="/economy"`, `href="/activity"`, "Players logged in", "Gems in circulation", "Sign-ups today (UTC)"} {
		if !strings.Contains(body, want) {
			t.Errorf("the dashboard lacks %q", want)
		}
	}
	if !strings.Contains(body, `<p class="big">75</p>`) {
		t.Error("the Gems card does not show the 75 Gems the account holds")
	}
}

// TestDashboardOnlineCountsPlayersOnce: the dashboard's "Players online now" counts distinct players, so a player on both the
// session and the gateway connection counts once (it used to add the two connection counts).
func TestDashboardOnlineCountsPlayersOnce(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	since := fx.clk.t.Add(-time.Minute)
	fakeOnline(fx,
		lpn.Connection{Aid: 2001, Listener: "session", Since: since},
		lpn.Connection{Aid: 2001, Listener: "gateway", Since: since},
		lpn.Connection{Aid: 3001, Listener: "gateway", Since: since})
	r := c.get("/stats.json")
	expect(t, r, http.StatusOK)
	var js map[string]any
	if err := json.Unmarshal([]byte(r.body), &js); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if n, _ := js["online"].(float64); n != 2 {
		t.Errorf("online = %v, want 2 (two players, three connections)", js["online"])
	}
}
