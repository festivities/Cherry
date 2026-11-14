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

// seedPlayer makes the account of identity in the fixture's database, with avatar id aid, name and gems, and returns its row id.
func seedPlayer(t *testing.T, identity, aid, name string, gems int64) int64 {
	t.Helper()
	if _, _, err := store.MintIdentityCode(identity, store.CodeHash(identity+" code"), true); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	acc := store.IdentityAccountLocked(identity)
	acc.Aid, acc.Name, acc.Gems = aid, name, gems
	err := store.SaveLocked(nil, acc)
	store.AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := store.FindPlayers(identity)
	if len(rows) != 1 {
		t.Fatalf("%d rows for %s, want 1", len(rows), identity)
	}
	return rows[0].ID
}

// playerOf returns the store's view of the player with row id.
func playerOf(t *testing.T, id int64) store.Player {
	t.Helper()
	p, ok := store.PlayerByID(id)
	if !ok {
		t.Fatalf("no player %d", id)
	}
	return p
}

// ledgerLines is the number of ledger lines in the database.
func ledgerLines(t *testing.T) int {
	t.Helper()
	lines, err := store.LedgerLines()
	if err != nil {
		t.Fatal(err)
	}
	return len(lines)
}

// playerURL is the page of a player.
func playerURL(id int64) string { return fmt.Sprintf("/players/%d", id) }

// TestPlayerPagesNeedASession: without a session the pages are the sign-in page, and the actions change nothing.
func TestPlayerPagesNeedASession(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	id := seedPlayer(t, "dev-pl", "1001", "Pat", 5)
	expect(t, c.get("/players"), http.StatusUnauthorized)
	expect(t, c.get(playerURL(id)), http.StatusUnauthorized)
	form := url.Values{"csrf": {"x"}, "currency": {"gems"}, "delta": {"5"}, "reason": {"x"}}
	expect(t, c.post(playerURL(id)+"/grant", form), http.StatusUnauthorized)
	expect(t, c.post(playerURL(id)+"/ban", url.Values{"csrf": {"x"}, "reason": {"x"}}), http.StatusUnauthorized)
	expect(t, c.post(playerURL(id)+"/unban", url.Values{"csrf": {"x"}}), http.StatusUnauthorized)
	if p := playerOf(t, id); p.Gems != 5 || p.Ban != nil {
		t.Fatalf("an action without a session changed the player: gems %d ban %v", p.Gems, p.Ban)
	}
}

// TestSearchFindsByAidNameAndIdentityAndCaps: the search matches the avatar id, the name in any case and the identity; it
// shows at most 50 rows and says so when there are more; an empty search shows only the form.
func TestSearchFindsByAidNameAndIdentityAndCaps(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	seedPlayer(t, "dev-s1", "4001", "Shiro", 0)
	seedPlayer(t, "dev-s2", "4002", "Mika", 0)

	empty := c.get("/players").body
	if strings.Contains(empty, "<th>Player</th>") || !strings.Contains(empty, `name="q"`) {
		t.Fatal("an empty search should show the form only")
	}
	for query, want := range map[string]string{"4001": "Shiro", "SHI": "Shiro", "dev-s2": "Mika", "mika": "Mika"} {
		r := c.get("/players?q=" + url.QueryEscape(query))
		expect(t, r, http.StatusOK)
		if !strings.Contains(r.body, want) {
			t.Errorf("search %q does not show %s", query, want)
		}
	}
	if r := c.get("/players?q=nobody-has-this"); !strings.Contains(r.body, "No player matches.") {
		t.Error("a search with no match did not say so")
	}

	for i := 0; i < 55; i++ {
		seedPlayer(t, fmt.Sprintf("dev-crowd%d", i), fmt.Sprintf("%d", 5000+i), "Crowd", 0)
	}
	crowd := c.get("/players?q=crowd").body
	if n := strings.Count(crowd, `href="/players/`); n != 50 {
		t.Fatalf("a search with 55 matches shows %d rows, want 50", n)
	}
	if !strings.Contains(crowd, "Only the first 50 matches are shown") {
		t.Error("a capped search does not say it is capped")
	}
	long := strings.Repeat("q", 101)
	expect(t, c.get("/players?q="+long), http.StatusBadRequest)
}

// TestGrantWritesBalanceLedgerAndAudit: a grant from the page changes the balance, writes one ledger line with the staff
// identity in its reason and one audit row, and the player page shows both.
func TestGrantWritesBalanceLedgerAndAudit(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-g1", "5001", "Gina", 100)
	form := url.Values{"csrf": {c.csrf()}, "currency": {"gems"}, "delta": {"+400"}, "reason": {"welcome back"}}
	r := c.post(playerURL(id)+"/grant", form)
	expect(t, r, http.StatusSeeOther)
	if loc := r.Header.Get("Location"); loc != playerURL(id) {
		t.Fatalf("grant redirects to %q", loc)
	}
	if p := playerOf(t, id); p.Gems != 500 {
		t.Fatalf("gems %d, want 500", p.Gems)
	}
	lines, err := store.PlayerLedger(id, 20)
	if err != nil || len(lines) != 1 || lines[0].Reason != "staff:dev-owner:welcome back" || lines[0].Delta != 400 {
		t.Fatalf("ledger = %+v, err %v", lines, err)
	}
	if !hasAudit(auditRows(t), "dev-owner", "grant", `"delta":400`) {
		t.Fatal("no grant audit row for the owner")
	}
	page := c.get(playerURL(id)).body
	if !strings.Contains(page, "staff:dev-owner:welcome back") || !strings.Contains(page, "500") {
		t.Fatal("the player page does not show the grant")
	}
}

// TestGrantRefusalsChangeNothingHTTP: each bad grant gets its refusal and changes nothing; a stale sign-in and a missing or
// wrong CSRF token are refused too.
func TestGrantRefusalsChangeNothingHTTP(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-g2", "5002", "Refuse", 500)
	grant := playerURL(id) + "/grant"
	ledgerBefore, auditBefore := ledgerLines(t), len(auditRows(t))
	csrf := c.csrf()
	refused := []struct {
		name   string
		form   url.Values
		status int
	}{
		{"bad currency", url.Values{"currency": {"gold"}, "delta": {"10"}, "reason": {"x"}}, http.StatusBadRequest},
		{"zero", url.Values{"currency": {"gems"}, "delta": {"0"}, "reason": {"x"}}, http.StatusBadRequest},
		{"too large", url.Values{"currency": {"gems"}, "delta": {"1000001"}, "reason": {"x"}}, http.StatusBadRequest},
		{"below zero", url.Values{"currency": {"gems"}, "delta": {"-501"}, "reason": {"x"}}, http.StatusBadRequest},
		{"not a number", url.Values{"currency": {"gems"}, "delta": {"ten"}, "reason": {"x"}}, http.StatusBadRequest},
		{"no reason", url.Values{"currency": {"gems"}, "delta": {"10"}}, http.StatusBadRequest},
		{"long reason", url.Values{"currency": {"gems"}, "delta": {"10"}, "reason": {strings.Repeat("r", 201)}}, http.StatusBadRequest},
		{"no csrf", url.Values{"currency": {"gems"}, "delta": {"10"}, "reason": {"x"}}, http.StatusForbidden},
		{"wrong csrf", url.Values{"csrf": {"wrong"}, "currency": {"gems"}, "delta": {"10"}, "reason": {"x"}}, http.StatusForbidden},
	}
	for _, tc := range refused {
		if tc.form.Get("csrf") == "" && tc.name != "no csrf" {
			tc.form.Set("csrf", csrf)
		}
		if got := c.post(grant, tc.form); got.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d", tc.name, got.StatusCode, tc.status)
		}
	}
	if p := playerOf(t, id); p.Gems != 500 {
		t.Fatalf("refused grants changed gems to %d", p.Gems)
	}
	if n := ledgerLines(t); n != ledgerBefore {
		t.Fatalf("refused grants wrote %d ledger lines", n-ledgerBefore)
	}
	if n := len(auditRows(t)); n != auditBefore {
		t.Fatalf("refused grants wrote %d audit rows", n-auditBefore)
	}

	// A sign-in older than the fresh window: the grant is refused and the sign-in page says so.
	fx.clk.advance(6 * time.Minute)
	stale := c.post(grant, url.Values{"csrf": {csrf}, "currency": {"gems"}, "delta": {"10"}, "reason": {"x"}})
	expect(t, stale, http.StatusForbidden)
	if !strings.Contains(stale.body, "needs a Discord sign-in") {
		t.Fatal("the stale grant does not ask for a fresh sign-in")
	}
	if p := playerOf(t, id); p.Gems != 500 {
		t.Fatalf("a stale grant changed gems to %d", p.Gems)
	}
}

// TestBanAndUnbanFromThePage: a ban needs a reason, is refused twice, shows on the page, and lifting it is audited; a
// banned player's page offers the unban.
func TestBanAndUnbanFromThePage(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-b1", "6001", "Bo", 0)

	expect(t, c.post(playerURL(id)+"/ban", url.Values{"csrf": {c.csrf()}, "reason": {"  "}}), http.StatusBadRequest)
	if p := playerOf(t, id); p.Ban != nil {
		t.Fatal("a ban without a reason banned the player")
	}
	ban := c.post(playerURL(id)+"/ban", url.Values{"csrf": {c.csrf()}, "reason": {"spam"}})
	expect(t, ban, http.StatusSeeOther)
	if p := playerOf(t, id); p.Ban == nil || p.Ban.Reason != "spam" || p.Ban.By != "dev-owner" {
		t.Fatalf("ban = %+v", p.Ban)
	}
	expect(t, c.post(playerURL(id)+"/ban", url.Values{"csrf": {c.csrf()}, "reason": {"again"}}), http.StatusConflict)
	page := c.get(playerURL(id)).body
	if !strings.Contains(page, "Lift the ban") || !strings.Contains(page, "banned on") {
		t.Fatal("a banned player's page does not offer the unban")
	}

	expect(t, c.post(playerURL(id)+"/unban", url.Values{"csrf": {c.csrf()}}), http.StatusSeeOther)
	if p := playerOf(t, id); p.Ban != nil {
		t.Fatal("unban left the ban")
	}
	expect(t, c.post(playerURL(id)+"/unban", url.Values{"csrf": {c.csrf()}}), http.StatusConflict)

	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "ban", "spam") || !hasAudit(rows, "dev-owner", "unban", "") {
		t.Fatal("the ban or the unban has no audit row for the owner")
	}
}

// TestPostPathsRefuseGET: a GET on a staff action gets 405 and changes nothing, even with a session.
func TestPostPathsRefuseGET(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-m1", "7001", "Get", 3)
	for _, action := range []string{"grant", "ban", "unban"} {
		if r := c.get(playerURL(id) + "/" + action); r.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET /players/%d/%s: status %d, want 405", id, action, r.StatusCode)
		}
	}
	if p := playerOf(t, id); p.Gems != 3 || p.Ban != nil {
		t.Fatalf("a GET changed the player: gems %d ban %v", p.Gems, p.Ban)
	}
}

// TestBadPlayerIDs: a malformed id is 400, an unknown one 404.
func TestBadPlayerIDs(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	expect(t, c.get("/players/abc"), http.StatusBadRequest)
	expect(t, c.get("/players/0"), http.StatusBadRequest)
	expect(t, c.get("/players/99999"), http.StatusNotFound)
	expect(t, c.post("/players/99999/ban", url.Values{"csrf": {c.csrf()}, "reason": {"x"}}), http.StatusNotFound)
}

// TestPlayerPageShowsNewestLedgerLines: the page lists the newest 20 ledger lines of the avatar id; an account with aid "0"
// has none of its own and says so.
func TestPlayerPageShowsNewestLedgerLines(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-l1", "8001", "Ledger", 0)
	for i := 0; i < 21; i++ {
		form := url.Values{"csrf": {c.csrf()}, "currency": {"gems"}, "delta": {"1"}, "reason": {fmt.Sprintf("grant-%02d", i)}}
		expect(t, c.post(playerURL(id)+"/grant", form), http.StatusSeeOther)
	}
	page := c.get(playerURL(id)).body
	if !strings.Contains(page, "grant-20") || strings.Contains(page, "grant-00") {
		t.Fatal("the ledger shows the wrong window: the newest must be in, the oldest out")
	}
	if n := strings.Count(page, "staff:dev-owner:grant-"); n != 20 {
		t.Fatalf("ledger rows on the page: %d, want 20", n)
	}

	zero := seedPlayer(t, "dev-l0", "0", "", 0)
	if !strings.Contains(c.get(playerURL(zero)).body, "This account has none yet") {
		t.Fatal("an account with aid 0 does not say it has no ledger lines")
	}
}
