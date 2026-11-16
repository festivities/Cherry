package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cherry/internal/store"
)

// TestSearchGroupsAndEscapes: a query finds players, chat text, audit rows (by their reason) and documents, each in its own group,
// and every value is shown escaped.
func TestSearchGroupsAndEscapes(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	chatWriter(t)
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-s1", "6701", "<b>Zed</b>", 0)
	say(store.ChatLounge, "1930000005", "6701", "<b>Zed</b>", "zebra crossing <i>now</i>")
	store.FlushChatLog()
	if r := c.post(playerURL(id)+"/grant", url.Values{"csrf": {c.csrf()}, "currency": {"gems"}, "delta": {"5"}, "reason": {"zebra stripes"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("grant: status %d", r.StatusCode)
	}

	zebra := c.get("/search?q=zebra")
	expect(t, zebra, http.StatusOK)
	checkPage(t, "/search?q=zebra", zebra)
	for _, want := range []string{"Chat messages (1)", "zebra crossing &lt;i&gt;now&lt;/i&gt;", "Audit rows (1)", "zebra stripes", "No player matches."} {
		if !strings.Contains(zebra.body, want) {
			t.Errorf("zebra search does not show %q", want)
		}
	}

	zed := c.get("/search?q=zed")
	expect(t, zed, http.StatusOK)
	if !strings.Contains(zed.body, fmt.Sprintf(`href="/players/%d"`, id)) || !strings.Contains(zed.body, "&lt;b&gt;Zed&lt;/b&gt;") {
		t.Error("a player hit does not link to the player, or its name is not escaped")
	}
	if strings.Contains(zed.body, "<b>Zed</b>") {
		t.Error("a player's name reaches the page unescaped")
	}

	docs := c.get("/search?q=cherrytips")
	expect(t, docs, http.StatusOK)
	if !strings.Contains(docs.body, `href="/docs/cherrytips.json"`) {
		t.Error("a document name hit does not link to the document")
	}

	script := c.get("/search?q=" + url.QueryEscape("<script>alert(1)</script>"))
	expect(t, script, http.StatusOK)
	checkPage(t, "escaped search", script)
	if strings.Contains(script.body, "<script>alert") || !strings.Contains(script.body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the query is not shown escaped")
	}

	blank := c.get("/search")
	expect(t, blank, http.StatusOK)
	if !strings.Contains(blank.body, "Search for a player's number") {
		t.Error("an empty search does not explain what to search for")
	}
}

// TestSearchBounds: a query of more than 100 characters is refused, and each group stops at 20 hits and says so.
func TestSearchBounds(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	chatWriter(t)
	c := fx.client()
	c.signInDev("owner")
	for i := 0; i < 25; i++ {
		say(store.ChatLounge, "1930000006", "6711", "Bulk", fmt.Sprintf("bulk line %d", i))
	}
	store.FlushChatLog()

	if r := c.get("/search?q=" + strings.Repeat("q", 101)); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("a 101-character search: status %d, want 400", r.StatusCode)
	}
	r := c.get("/search?q=bulk")
	expect(t, r, http.StatusOK)
	if !strings.Contains(r.body, "Chat messages (20+)") || !strings.Contains(r.body, "More messages match") {
		t.Fatal("the chat group is not cut at 20 hits with a note")
	}
	if n := strings.Count(r.body, "bulk line "); n != 20 {
		t.Fatalf("the page shows %d chat hits, want 20", n)
	}
}

// TestShellHasKeyboardMenuSearchAndHint: the header has a keyboard-operable menu button (with the checkbox label kept for the
// CSS-only fallback), the search box, the session hint, a skip link and the Mail entry under Players; each page has its title.
func TestShellHasKeyboardMenuSearchAndHint(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	r := c.get("/")
	expect(t, r, http.StatusOK)
	for _, want := range []string{
		`<button type="button" class="nav-button nav-js" id="nav-button" aria-controls="sidebar" aria-expanded="false">Menu</button>`,
		`<label for="nav-toggle" class="nav-button nav-nojs">Menu</label>`,
		`<form method="get" action="/search" class="topsearch" role="search">`,
		`<a class="skip-link" href="#main">Skip to content</a>`,
		"Signed in as ", "session ends in ",
		`<a href="/mail">Mail</a>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the shell does not have %q", want)
		}
	}
	if !strings.Contains(r.body, "<title>Dashboard | Cherry staff</title>") {
		t.Error("the dashboard has no page title")
	}
	if !strings.Contains(c.get("/mail").body, "<title>Mail | Cherry staff</title>") {
		t.Error("the mail page has no page title")
	}
	if !strings.Contains(c.get("/search?q=x").body, "<title>Search | Cherry staff</title>") {
		t.Error("the search page has no page title")
	}
	script := string(staffJS)
	if !strings.Contains(script, "nav-button") || !strings.Contains(script, "'Escape'") {
		t.Error("the staff script does not drive the menu button from the keyboard")
	}
}
