package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
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
	// The sidebar's own link to the deleted avatars is not a row.
	if n := strings.Count(crowd, `href="/players/`) - strings.Count(crowd, `href="/players/deleted"`); n != 50 {
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
	if loc := r.Header.Get("Location"); loc != playerURL(id)+"?notice=granted" {
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

// TestGrantExtraHearts: the grant form gives extra hearts (a balance: one audit row, no ledger line), a take-away below zero is
// refused and changes nothing, and the player page shows the count.
func TestGrantExtraHearts(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-g2", "5002", "Hana", 0)
	form := func(delta string) url.Values {
		return url.Values{"csrf": {c.csrf()}, "currency": {"extraHearts"}, "delta": {delta}, "reason": {"event"}}
	}
	expect(t, c.post(playerURL(id)+"/grant", form("3")), http.StatusSeeOther)
	if book, ok := economy.PlayerBookOf(id, time.Now()); !ok || book.ExtraHearts != 3 {
		t.Fatalf("extra hearts after the grant = %d (found %v), want 3", book.ExtraHearts, ok)
	}
	expect(t, c.post(playerURL(id)+"/grant", form("-4")), http.StatusBadRequest)
	if book, _ := economy.PlayerBookOf(id, time.Now()); book.ExtraHearts != 3 {
		t.Fatalf("a refused take-away left extra hearts at %d, want 3", book.ExtraHearts)
	}
	if lines, err := store.PlayerLedger(id, 20); err != nil || len(lines) != 0 {
		t.Fatalf("ledger = %+v, err %v; extra hearts write no ledger line", lines, err)
	}
	if !hasAudit(auditRows(t), "dev-owner", "grant", `"extraHearts":3`) {
		t.Fatal("no grant audit row for the extra hearts")
	}
	if page := c.get(playerURL(id)).body; !strings.Contains(page, "Extra hearts (to send)") {
		t.Fatal("the player page does not show the extra hearts")
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
	for _, action := range []string{"grant", "ban", "unban", "rename"} {
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

// seedPost appends a diary post by aid to the social store, as the game saves it, and returns its number. photo gives it a
// picture.
func seedPost(t *testing.T, aid, content string, photo bool) string {
	t.Helper()
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	store.NextDiaryNo++
	no := strconv.FormatUint(store.NextDiaryNo, 10)
	post := store.DiaryPost{DiaryNo: no, AvatarID: aid, Nickname: "Pat", RegDate: "1700000000000", Content: content, Images: []store.DiaryImage{}}
	if photo {
		post.Images = []store.DiaryImage{{ImageURL: "/lineplay/d/photo.png"}}
	}
	store.DiaryPosts = append([]store.DiaryPost{post}, store.DiaryPosts...)
	if err := store.SaveSocialLocked(store.SocialChange{Posts: []string{no}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
	return no
}

// seedEntry appends a guestbook entry that guest wrote in host's guestbook and returns its number.
func seedEntry(t *testing.T, host, guest, content string) string {
	t.Helper()
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	store.NextGuestHistNo++
	no := strconv.FormatUint(store.NextGuestHistNo, 10)
	entry := store.SavedGuestbookEntry{HostAvtNo: host, GuestHistNo: no, GuestAvtNo: guest, GuestNickname: "Pat", Content: content, RegYmdt: "1700000000000"}
	store.GuestbookEntries = append([]store.SavedGuestbookEntry{entry}, store.GuestbookEntries...)
	if err := store.SaveSocialLocked(store.SocialChange{Entries: []string{no}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
	return no
}

// seedComment appends a comment by writer on the post of owner (number post) and returns its comment number.
func seedComment(t *testing.T, owner, post, writer, content string) string {
	t.Helper()
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	store.NextCommentNo++
	no := strconv.FormatUint(store.NextCommentNo, 10)
	store.DiaryComments = append(store.DiaryComments, store.DiaryComment{CommentNo: no, DiaryNo: post, OwnerAvatarID: owner, CommentAvatarID: writer, Content: content, Date: "1700000000000"})
	if err := store.SaveSocialLocked(store.SocialChange{Comments: []string{no}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
	return no
}

// postExists, entryExists and commentExists report whether the social store still holds the row with number no.
func postExists(no string) bool {
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	return slices.ContainsFunc(store.DiaryPosts, func(p store.DiaryPost) bool { return p.DiaryNo == no })
}

func entryExists(no string) bool {
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	return slices.ContainsFunc(store.GuestbookEntries, func(e store.SavedGuestbookEntry) bool { return e.GuestHistNo == no })
}

func commentExists(no string) bool {
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	return slices.ContainsFunc(store.DiaryComments, func(c store.DiaryComment) bool { return c.CommentNo == no })
}

// TestRenameFromThePage: the rename applies the game's nickname rule (a refused name changes nothing), needs the CSRF token
// and a fresh sign-in, and writes its audit row with the old and new name.
func TestRenameFromThePage(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-rn1", "9001", "Old", 0)
	rename := playerURL(id) + "/rename"
	for _, name := range []string{"", "   ", strings.Repeat("n", 21), "tab\there"} {
		expect(t, c.post(rename, url.Values{"csrf": {c.csrf()}, "name": {name}, "reason": {"x"}}), http.StatusBadRequest)
	}
	expect(t, c.post(rename, url.Values{"name": {"Nope"}, "reason": {"x"}}), http.StatusForbidden)
	expect(t, c.post(rename, url.Values{"csrf": {c.csrf()}, "name": {"Fine"}, "reason": {"   "}}), http.StatusBadRequest)
	if p := playerOf(t, id); p.Name != "Old" {
		t.Fatalf("refused renames changed the name to %q", p.Name)
	}

	r := c.post(rename, url.Values{"csrf": {c.csrf()}, "name": {"Newname"}, "reason": {"impersonation"}})
	expect(t, r, http.StatusSeeOther)
	if loc := r.Header.Get("Location"); loc != playerURL(id)+"?notice=renamed" {
		t.Fatalf("rename redirects to %q", loc)
	}
	if p := playerOf(t, id); p.Name != "Newname" {
		t.Fatalf("name %q, want Newname", p.Name)
	}
	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "rename", `"old":"Old"`) || !hasAudit(rows, "dev-owner", "rename", `"new":"Newname"`) {
		t.Fatal("the rename has no audit row with the old and new name")
	}

	fx.clk.advance(6 * time.Minute)
	stale := c.post(rename, url.Values{"csrf": {c.csrf()}, "name": {"Late"}, "reason": {"x"}})
	expect(t, stale, http.StatusForbidden)
	if !strings.Contains(stale.body, "needs a Discord sign-in") {
		t.Fatal("the stale rename does not ask for a fresh sign-in")
	}
	if p := playerOf(t, id); p.Name != "Newname" {
		t.Fatalf("a stale rename changed the name to %q", p.Name)
	}
}

// TestWritingRemovalsFromThePage: the player page lists the player's newest posts, guestbook entries and comments with a
// remove form each; a removal takes its cascade, writes its audit row (number and reason, never the text) and refuses a
// number the player does not own, a wrong number, a GET, a missing CSRF token and a stale sign-in.
func TestWritingRemovalsFromThePage(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	writer := seedPlayer(t, "dev-wr1", "9021", "Writer", 0)
	other := seedPlayer(t, "dev-wr2", "9022", "Other", 0)
	post := seedPost(t, "9021", "the writer's post text", true)
	cut := seedPost(t, "9021", strings.Repeat("y", 80)+"CUTOFF", false)
	theirs := seedPost(t, "9022", "not the writer's post", false)
	entry := seedEntry(t, "9022", "9021", "a note in the other guestbook")
	comment := seedComment(t, "9022", theirs, "9021", "a comment by the writer")
	onPost := seedComment(t, "9021", post, "9022", "a comment by the other player on the writer's post")

	page := c.get(playerURL(writer)).body
	for _, want := range []string{
		"/posts/" + post + "/remove", "/posts/" + cut + "/remove", "/guestbook/" + entry + "/remove",
		"/comments/" + comment + "/remove", "the writer&#39;s post text", strings.Repeat("y", 80),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the player page does not show %q", want)
		}
	}
	if strings.Contains(page, "CUTOFF") || strings.Contains(page, "not the writer") {
		t.Error("the player page shows a text past 80 characters or another player's post")
	}

	base := playerURL(writer)
	expect(t, c.post(base+"/posts/"+theirs+"/remove", url.Values{"csrf": {c.csrf()}, "reason": {"x"}}), http.StatusNotFound)
	expect(t, c.post(base+"/posts/999999/remove", url.Values{"csrf": {c.csrf()}, "reason": {"x"}}), http.StatusNotFound)
	expect(t, c.post(playerURL(other)+"/guestbook/"+entry+"/remove", url.Values{"csrf": {c.csrf()}, "reason": {"x"}}), http.StatusNotFound)
	expect(t, c.post(base+"/comments/"+comment+"/remove", url.Values{"reason": {"x"}}), http.StatusForbidden)
	expect(t, c.post(base+"/comments/"+comment+"/remove", url.Values{"csrf": {c.csrf()}, "reason": {" "}}), http.StatusBadRequest)
	for _, path := range []string{"/posts/" + post + "/remove", "/guestbook/" + entry + "/remove", "/comments/" + comment + "/remove", "/rename"} {
		if r := c.get(base + path); r.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: status %d, want 405", path, r.StatusCode)
		}
	}
	if !postExists(post) || !entryExists(entry) || !commentExists(comment) || !commentExists(onPost) {
		t.Fatal("a refused removal changed the social rows")
	}

	expect(t, c.post(base+"/posts/"+post+"/remove", url.Values{"csrf": {c.csrf()}, "reason": {"spam"}}), http.StatusSeeOther)
	if postExists(post) || commentExists(onPost) || !postExists(cut) {
		t.Fatal("removing the post did not take its comment, or took more")
	}
	expect(t, c.post(base+"/guestbook/"+entry+"/remove", url.Values{"csrf": {c.csrf()}, "reason": {"tidy up"}}), http.StatusSeeOther)
	if entryExists(entry) {
		t.Fatal("the guestbook entry was not removed")
	}
	expect(t, c.post(base+"/comments/"+comment+"/remove", url.Values{"csrf": {c.csrf()}, "reason": {"rude"}}), http.StatusSeeOther)
	if commentExists(comment) || !postExists(theirs) {
		t.Fatal("the comment was not removed alone")
	}

	rows := auditRows(t)
	for _, want := range []struct{ action, detail string }{
		{"remove-post", `"number":"` + post + `"`}, {"remove-guestbook", `"number":"` + entry + `"`},
		{"remove-comment", `"number":"` + comment + `"`},
	} {
		if !hasAudit(rows, "dev-owner", want.action, want.detail) {
			t.Errorf("no %s audit row with %s", want.action, want.detail)
		}
	}
	if hasAudit(rows, "dev-owner", "remove-post", "the writer") {
		t.Fatal("a removal's audit row names the removed text")
	}

	fx.clk.advance(6 * time.Minute)
	expect(t, c.post(base+"/posts/"+cut+"/remove", url.Values{"csrf": {c.csrf()}, "reason": {"x"}}), http.StatusForbidden)
	if !postExists(cut) {
		t.Fatal("a stale removal removed the post")
	}
}

// TestPictureRemovalFromThePage: the player page lists the player's newest pictures; a removal takes the picture's row and file
// and the reference in the player's post (the post keeps its text) in one save, with a reasoned audit row that never names the
// picture. Refused removals change nothing.
func TestPictureRemovalFromThePage(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	pic := seedPlayer(t, "dev-pic", "9031", "Snap", 0)
	file := store.MediaFileName("9031", "1760000700", "9031_1760000700")
	dir, err := store.MediaDir(store.MediaDiary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte("snap"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMediaFile(store.MediaFile{Kind: store.MediaDiary, Owner: "9031", File: file, Size: 4}); err != nil {
		t.Fatal(err)
	}
	row, found, err := store.MediaRowByFile(file)
	if err != nil || !found {
		t.Fatalf("row of the picture: found=%t err=%v", found, err)
	}
	store.SocialMu.Lock()
	store.NextDiaryNo++
	no := strconv.FormatUint(store.NextDiaryNo, 10)
	store.DiaryPosts = append([]store.DiaryPost{{DiaryNo: no, AvatarID: "9031", Nickname: "Snap", RegDate: "1700000000000", Title: "snap",
		Content: "still here", OpenState: "public", ImageTypeKind: "obs", ImageLocationType: "obs",
		Images: []store.DiaryImage{{ImageURL: "/lineplay/d/download.nhn?oid=9031_1760000700&ctime=1760000700&userid=9031"}}}}, store.DiaryPosts...)
	store.SocialMu.Unlock()
	if err := store.SaveSocialLocked(store.SocialChange{Posts: []string{no}, Scalars: true}); err != nil {
		t.Fatal(err)
	}

	base := playerURL(pic)
	rowID := strconv.FormatInt(row.ID, 10)
	mediaRemove := base + "/media/" + rowID + "/remove"
	if page := c.get(base).body; !strings.Contains(page, mediaRemove) {
		t.Fatalf("the player page does not offer %s", mediaRemove)
	}
	expect(t, c.post(mediaRemove, url.Values{"reason": {"x"}}), http.StatusForbidden)
	expect(t, c.post(mediaRemove, url.Values{"csrf": {c.csrf()}, "reason": {" "}}), http.StatusBadRequest)
	expect(t, c.post(base+"/media/999999/remove", url.Values{"csrf": {c.csrf()}, "reason": {"x"}}), http.StatusNotFound)
	if r := c.get(mediaRemove); r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET of the removal: status %d, want 405", r.StatusCode)
	}
	if _, found, _ := store.MediaRowByFile(file); !found {
		t.Fatal("a refused removal deleted the row")
	}

	expect(t, c.post(mediaRemove, url.Values{"csrf": {c.csrf()}, "reason": {"stolen photo"}}), http.StatusSeeOther)
	if _, found, _ := store.MediaRowByFile(file); found {
		t.Fatal("the removed picture's row remains")
	}
	if _, err := os.Stat(filepath.Join(dir, file)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the removed picture's file remains: %v", err)
	}
	store.SocialMu.Lock()
	post := store.DiaryPosts[0]
	store.SocialMu.Unlock()
	if len(post.Images) != 0 || post.Content != "still here" || post.ImageLocationType != "none" {
		t.Fatalf("post after the removal: %d images, location %q, content %q", len(post.Images), post.ImageLocationType, post.Content)
	}
	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "remove-media", "stolen photo") {
		t.Fatal("no remove-media audit row with the reason")
	}
	for _, r := range rows {
		if r.Action == "remove-media" && strings.Contains(r.Detail, "download.nhn") {
			t.Fatal("the audit row names the picture's address")
		}
	}
}
