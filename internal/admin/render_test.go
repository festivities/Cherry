package admin

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The staff pages hold no inline markup: no <script> without src, no <style> element, no style attribute and no on… event
// attribute, because the CSP is default-src 'self' (PLAN P4). The check reads the tags only, not the text between them.
var (
	tagRe     = regexp.MustCompile(`<[a-zA-Z][^>]*>`)
	scriptRe  = regexp.MustCompile(`(?i)^<script\b`)
	srcAttrRe = regexp.MustCompile(`(?i)\ssrc\s*=`)
	styleRe   = regexp.MustCompile(`(?i)^<style\b`)
	styleAttr = regexp.MustCompile(`(?i)\sstyle\s*=`)
	eventAttr = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
)

// inlineMarkup lists the tags of body that are inline script, inline style or an event handler. An empty list is the rule.
func inlineMarkup(body string) []string {
	var bad []string
	for _, tag := range tagRe.FindAllString(body, -1) {
		switch {
		case scriptRe.MatchString(tag) && !srcAttrRe.MatchString(tag):
			bad = append(bad, tag)
		case styleRe.MatchString(tag), styleAttr.MatchString(tag), eventAttr.MatchString(tag):
			bad = append(bad, tag)
		}
	}
	return bad
}

// TestInlineMarkupScanner: the scanner itself flags what it must and passes what the rule allows.
func TestInlineMarkupScanner(t *testing.T) {
	for _, bad := range []string{
		`<script>alert(1)</script>`,
		`<script type="text/javascript">x()</script>`,
		`<style>p{}</style>`,
		`<p style="color:red">x</p>`,
		`<button onclick="go()">x</button>`,
	} {
		if len(inlineMarkup(bad)) == 0 {
			t.Errorf("not flagged: %s", bad)
		}
	}
	for _, ok := range []string{
		`<script src="/static/staff.js?v=1" defer></script>`,
		`<link rel="stylesheet" href="/static/staff.css">`,
		`<p>a script tag in text: &lt;script&gt; and style=</p>`,
	} {
		if got := inlineMarkup(ok); len(got) != 0 {
			t.Errorf("flagged %v in %s", got, ok)
		}
	}
}

// TestPagesHaveNoInlineMarkup renders every kind of page and checks each one: the signed-in shell (with its one same-origin
// script), the sign-in pages, the error pages and the sign-in page.
func TestPagesHaveNoInlineMarkup(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	id := seedPlayer(t, "dev-render1", "7101", "Render <b>me</b>", 20)
	signedOut := c.get("/")
	expect(t, signedOut, http.StatusUnauthorized)
	checkPage(t, "/ (sign-in)", signedOut)
	checkPage(t, "error page", c.get("/auth/login?as=bad%20name"))

	c.signInDev("owner")
	if def := c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {"{}"}, "note": {"n"}}); def.StatusCode != http.StatusSeeOther {
		t.Fatalf("draft: status %d", def.StatusCode)
	}
	base := playerURL(id)
	targets := []string{
		"/", "/docs", "/docs/cherrytips.json", "/docs/cherrytips.json/1", "/audit", "/audit?action=draft&from=2000-01-01",
		"/players", "/players?q=render", base, "/docs?notice=draft-saved", "/players/999999",
		"/players/deleted", "/players/deleted/999999",
		"/reports", "/reports?status=all", "/reports/999999",
		"/chat", "/chat?kind=private&text=hi", "/chat?kind=whisper",
		"/diary", "/diary?filter=1&posts=1&photos=1",
		"/circles", "/circles/999999",
		"/photos", "/photos?kind=diary&player=7101",
		"/online", "/online?notice=kicked", "/economy", "/economy?window=7d", "/activity",
		"/mail", "/mail?player=" + strconv.FormatInt(id, 10), "/mail/sent", "/balances", "/broadcast", "/search?q=render", "/search",
		"/settings", "/settings?notice=staff-added", "/settings?notice=chat-days-saved",
		base + "?window=7d", base + "?window=all", base + "/avatar/move", base + "/avatar/move?to=dev-other",
		"/players/deleted/999999/restore",
	}
	for _, target := range targets {
		r := c.get(target)
		if r.StatusCode >= 500 {
			t.Errorf("%s: status %d", target, r.StatusCode)
		}
		checkPage(t, target, r)
	}
	if r := c.get("/docs/cherrytips.json/1"); !strings.Contains(r.body, `<script src="/static/staff.js?v=`) {
		t.Error("the signed-in shell does not load the staff script from its own origin")
	}
}

// checkPage asserts the inline-markup rule and that the reply is HTML.
func checkPage(t *testing.T, what string, r reply) {
	t.Helper()
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("%s: content type %q", what, ct)
	}
	if bad := inlineMarkup(r.body); len(bad) > 0 {
		t.Errorf("%s: inline markup %v", what, bad)
	}
}
