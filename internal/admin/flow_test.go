package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cherry/internal/opdata"
	"cherry/internal/store"
)

// TestDiscordFlowEndToEnd: the walk of TestDevFakeFlow through the fake Discord: the owner signs in (no file lists them),
// drafts, validates, publishes, rolls back and signs out, and the audit trail names the Discord identity.
func TestDiscordFlowEndToEnd(t *testing.T) {
	fakeDiscord(t, new(int))
	fx := newFixture(t, discordConfig(t, ""))
	c := fx.client()
	expect(t, c.discordLogin("c|"+ownerID+"|1"), http.StatusOK)

	def, _ := opdata.Embedded("cherrytips.json")
	expect(t, c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {string(def)}}), http.StatusSeeOther)
	if r := c.post("/docs/cherrytips.json/1/validate", url.Values{"csrf": {c.csrf()}}); !strings.Contains(r.body, "Valid.") {
		t.Fatalf("validate: status %d", r.StatusCode)
	}
	expect(t, c.post("/docs/cherrytips.json/1/publish", url.Values{"csrf": {c.csrf()}}), http.StatusOK)
	expect(t, c.post("/docs/cherrytips.json/rollback", url.Values{"csrf": {c.csrf()}}), http.StatusOK)
	expect(t, c.post("/auth/logout", url.Values{"csrf": {c.csrf()}}), http.StatusSeeOther)
	expect(t, c.get("/"), http.StatusUnauthorized)

	rows := auditRows(t)
	for _, action := range []string{"login", "draft", "publish", "rollback", "logout"} {
		if !hasAudit(rows, "discord:"+ownerID, action, "") {
			t.Errorf("no %s row for the owner", action)
		}
	}
	live, err := store.Live()
	if err != nil {
		t.Fatal(err)
	}
	if got := liveOf(live, "cherrytips.json"); got.Published != 0 {
		t.Errorf("after the rollback, v%d is still published", got.Published)
	}
}

// TestCookieAttributes: the pre-login cookie and the session cookie carry exactly the attributes of PLAN P4, and no Domain.
func TestCookieAttributes(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	start := c.get("/auth/login?as=owner")
	pre := rawCookie(t, start, "__Host-cherry-prelogin")
	wantAll(t, "pre-login", pre, "Path=/", "Max-Age=600", "HttpOnly", "Secure", "SameSite=Lax")
	done := c.get(start.Header.Get("Location"))
	staff := rawCookie(t, done, "__Host-cherry-staff")
	wantAll(t, "session", staff, "Path=/", "Max-Age=28800", "HttpOnly", "Secure", "SameSite=Strict")
	for _, raw := range []string{pre, staff} {
		if strings.Contains(strings.ToLower(raw), "domain=") || strings.Contains(strings.ToLower(raw), "expires=") {
			t.Errorf("cookie carries Domain or Expires: %s", raw)
		}
	}
}

// rawCookie is the Set-Cookie line of name as sent.
func rawCookie(t *testing.T, r reply, name string) string {
	t.Helper()
	for _, line := range r.Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, name+"=") {
			return line
		}
	}
	t.Fatalf("no Set-Cookie for %s", name)
	return ""
}

// wantAll asserts every attribute appears in the raw Set-Cookie line.
func wantAll(t *testing.T, what, line string, attrs ...string) {
	t.Helper()
	for _, a := range attrs {
		if !strings.Contains(line, a) {
			t.Errorf("%s cookie lacks %q: %s", what, a, line)
		}
	}
}
