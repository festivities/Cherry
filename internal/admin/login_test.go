package admin

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

// TestAuthorizeURL: the Discord redirect carries the contract's parameters exactly, with prompt=consent.
func TestAuthorizeURL(t *testing.T) {
	fakeDiscord(t, new(int))
	fx := newFixture(t, discordConfig(t, ""))
	c := fx.client()
	r := c.get("/auth/login")
	expect(t, r, http.StatusFound)
	loc, err := url.Parse(r.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	want := map[string]string{"response_type": "code", "client_id": "cid", "scope": "identify", "redirect_uri": testRedir, "prompt": "consent"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("authorize %s = %q, want %q", k, q.Get(k), v)
		}
	}
	if len(q.Get("state")) != 43 {
		t.Errorf("state is %d characters, want 43 (256 bits)", len(q.Get("state")))
	}
	if loc.Path != "/oauth2/authorize" {
		t.Errorf("authorize path %q", loc.Path)
	}
}

// TestDiscordOwnerAdmittedWithoutFile: the owner is admitted without being in any file, and the session is set.
func TestDiscordOwnerAdmittedWithoutFile(t *testing.T) {
	fakeDiscord(t, new(int))
	fx := newFixture(t, discordConfig(t, ""))
	c := fx.client()
	r := c.discordLogin("c|" + ownerID + "|1")
	expect(t, r, http.StatusOK)
	if !strings.Contains(r.body, `content="0;url=/"`) {
		t.Fatalf("after sign-in: no refresh to the dashboard in %.300s", r.body)
	}
	if cookieOf(r, "__Host-cherry-staff") == nil {
		t.Fatal("no staff session cookie")
	}
	if d := c.get("/"); !strings.Contains(d.body, "Display Name") {
		t.Error("the dashboard does not show the Discord display name")
	}
	if !hasAudit(auditRows(t), "discord:"+ownerID, "login", "") {
		t.Error("the sign-in is not in the audit trail")
	}
}

// TestAllowlistAndRefusals: an allowlisted id gets in; a stranger, a two-factor-off owner and a bad code are refused with
// the same page, and each refusal is audited with its reason (never on the page).
func TestAllowlistAndRefusals(t *testing.T) {
	fakeDiscord(t, new(int))
	fx := newFixture(t, discordConfig(t, friendID+"\n"))
	friend := fx.client()
	expect(t, friend.discordLogin("c|"+friendID+"|1"), http.StatusOK)

	stranger := fx.client()
	r := stranger.discordLogin("c|" + strangerID + "|1")
	expect(t, r, http.StatusForbidden)
	if !strings.Contains(r.body, "Not authorised.") || strings.Contains(strings.ToLower(r.body), "allowlist") || strings.Contains(r.body, "mfa") {
		t.Error("the refusal page names a check")
	}
	if cookieOf(r, "__Host-cherry-staff") != nil {
		t.Error("a refused sign-in set a session cookie")
	}

	noMFA := fx.client()
	expect(t, noMFA.discordLogin("c|"+ownerID+"|0"), http.StatusForbidden)

	bad := fx.client()
	expect(t, bad.discordLogin("bad"), http.StatusForbidden)

	rows := auditRows(t)
	for _, tc := range []struct{ actor, reason string }{
		{"discord:" + strangerID, "not-staff"},
		{"discord:" + ownerID, "mfa-off"},
		{"unknown", "expired"},
	} {
		if !hasAudit(rows, tc.actor, "login-refused", tc.reason) {
			t.Errorf("no login-refused row for %s with reason %s", tc.actor, tc.reason)
		}
	}
	if !hasAudit(rows, "discord:"+friendID, "login", "") {
		t.Error("the allowlisted sign-in is not audited")
	}
}

// TestStateAndCookieChecks: a sign-in needs its own pre-login cookie and an unused, unexpired state from the same address.
func TestStateAndCookieChecks(t *testing.T) {
	fakeDiscord(t, new(int))
	fx := newFixture(t, discordConfig(t, ""))
	code := "c|" + ownerID + "|1"

	// State reuse: a successful sign-in consumes its state.
	c := fx.client()
	start := c.get("/auth/login")
	state := stateOf(t, start)
	expect(t, c.get("/auth/callback?code="+code+"&state="+state), http.StatusOK)
	c2 := fx.client()
	c2.cookies["__Host-cherry-prelogin"] = start.cookieValue(t, "__Host-cherry-prelogin")
	expect(t, c2.get("/auth/callback?code="+code+"&state="+state), http.StatusForbidden)

	// Missing pre-login cookie.
	noCookie := fx.client()
	start = noCookie.get("/auth/login")
	delete(noCookie.cookies, "__Host-cherry-prelogin")
	expect(t, noCookie.get("/auth/callback?code="+code+"&state="+stateOf(t, start)), http.StatusForbidden)

	// Another browser's pre-login cookie.
	other := fx.client()
	start = other.get("/auth/login")
	wrong := fx.client()
	wrong.cookies["__Host-cherry-prelogin"] = strings.Repeat("A", 43)
	expect(t, wrong.get("/auth/callback?code="+code+"&state="+stateOf(t, start)), http.StatusForbidden)

	// Expired state.
	old := fx.client()
	start = old.get("/auth/login")
	fx.clk.advance(11 * time.Minute)
	expect(t, old.get("/auth/callback?code="+code+"&state="+stateOf(t, start)), http.StatusForbidden)
	if !hasAudit(auditRows(t), "unknown", "login-refused", "expired") {
		t.Error("the expired state is not audited")
	}
	fx.clk.advance(-11 * time.Minute)

	// The address changed between the start and the callback.
	moved := fx.client()
	start = moved.get("/auth/login")
	moved.remote = "198.51.100.7:1000"
	expect(t, moved.get("/auth/callback?code="+code+"&state="+stateOf(t, start)), http.StatusForbidden)

	// Discord says the user refused, and a state parameter given twice.
	denied := fx.client()
	start = denied.get("/auth/login")
	expect(t, denied.get("/auth/callback?error=access_denied&state="+stateOf(t, start)), http.StatusForbidden)
	twice := fx.client()
	start = twice.get("/auth/login")
	expect(t, twice.get("/auth/callback?state="+stateOf(t, start)+"&state=x"), http.StatusForbidden)
}

// cookieValue returns the value of a Set-Cookie of name in the reply (the test needs it to play a second browser).
func (r reply) cookieValue(t *testing.T, name string) string {
	t.Helper()
	ck := cookieOf(r, name)
	if ck == nil {
		t.Fatalf("no %s cookie in the reply", name)
	}
	return ck.Value
}

// TestLoginRateLimitAndBlock: the sign-in start is limited per address; repeated refusals block the address for a while
// (and the block lifts when its time has passed).
func TestLoginRateLimitAndBlock(t *testing.T) {
	fakeDiscord(t, new(int))
	fx := newFixture(t, discordConfig(t, ""))
	c := fx.client()
	for i := 0; i < 10; i++ {
		expect(t, c.get("/auth/login"), http.StatusFound)
	}
	expect(t, c.get("/auth/login"), http.StatusTooManyRequests)

	// Six refused callbacks (unknown states) in a quarter hour block the address.
	fx.clk.advance(time.Hour) // the start limit has passed
	b := fx.client()
	for i := 0; i < 6; i++ {
		r := b.get("/auth/callback?code=x&state=" + strings.Repeat("B", 43))
		if r.StatusCode != http.StatusForbidden {
			t.Fatalf("refusal %d: status %d", i, r.StatusCode)
		}
	}
	blocked := b.get("/auth/login")
	if blocked.StatusCode != http.StatusTooManyRequests || !strings.Contains(blocked.body, "Too many attempts") {
		t.Fatalf("a blocked address: status %d", blocked.StatusCode)
	}
	fx.clk.advance(16 * time.Minute)
	expect(t, b.get("/auth/login"), http.StatusFound)
}

// TestNextIsValidated: only a path of this server is an allowed return; anything else is a 400 with no cookie. A good
// path comes back after the sign-in.
func TestNextIsValidated(t *testing.T) {
	fx := newFixture(t, devConfig())
	bad := []string{"//evil.test", "/\\evil.test", "https://evil.test", "http:/x", "docs", "/docs/../x", "/a b", "/%2f%2fevil", "/x?y=\u0000", "/" + strings.Repeat("a", 200)}
	for _, p := range bad {
		fx.clk.advance(time.Minute) // every start counts toward the per-minute limit
		c := fx.client()
		r := c.get("/auth/login?as=owner&next=" + url.QueryEscape(p))
		if r.StatusCode != http.StatusBadRequest || cookieOf(r, "__Host-cherry-prelogin") != nil {
			t.Errorf("next %q: status %d, want 400 without a cookie", p, r.StatusCode)
		}
	}
	for _, p := range []string{"/", "/docs", "/docs/cherrytips.json/1", "/audit?before=40"} {
		if !validNext(p) {
			t.Errorf("validNext(%q) refused a safe path", p)
		}
	}
	c := fx.client()
	start := c.get("/auth/login?as=owner&next=" + url.QueryEscape("/docs?x=1"))
	expect(t, start, http.StatusFound)
	back := c.get(start.Header.Get("Location"))
	expect(t, back, http.StatusOK)
	if !strings.Contains(back.body, `content="0;url=/docs?x=1"`) {
		t.Fatalf("after sign-in: no refresh to /docs?x=1 in %.300s", back.body)
	}
}

// TestLoginNeedsValidNames: a dev name must be a dev name; discord mode has no as parameter.
func TestLoginNeedsValidNames(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	for _, target := range []string{"/auth/login?as=", "/auth/login?as=a%20b", "/auth/login?as=" + strings.Repeat("x", 25), "/auth/login?other=1", "/auth/login?next=/&next=/"} {
		expect(t, c.get(target), http.StatusBadRequest)
	}
	fakeDiscord(t, new(int))
	dc := newFixture(t, discordConfig(t, ""))
	expect(t, dc.client().get("/auth/login?as=owner"), http.StatusBadRequest)
}

// TestAuditBeforeCookie: a sign-in that cannot be recorded does not happen: no session, a 503.
func TestAuditBeforeCookie(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	start := c.get("/auth/login?as=owner")
	expect(t, start, http.StatusFound)
	store.CloseStore()
	r := c.get(start.Header.Get("Location"))
	if r.StatusCode != http.StatusServiceUnavailable || cookieOf(r, "__Host-cherry-staff") != nil {
		t.Fatalf("no database: status %d, session cookie %v", r.StatusCode, cookieOf(r, "__Host-cherry-staff") != nil)
	}
}

// TestDevAllowlistNames: the staff allowlist file may name dev identities in dev-fake; a name not on it is refused.
func TestDevAllowlistNames(t *testing.T) {
	cfg := devConfig()
	path := filepath.Join(t.TempDir(), "staff.txt")
	if err := os.WriteFile(path, []byte("# staff\ndev-friend\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.StaffAllowlistFile = path
	fx := newFixture(t, cfg)
	friend := fx.client()
	friend.signInDev("friend")
	if d := friend.get("/"); d.StatusCode != http.StatusOK {
		t.Fatalf("the allowlisted dev name: dashboard status %d", d.StatusCode)
	}
	stranger := fx.client()
	start := stranger.get("/auth/login?as=stranger")
	expect(t, start, http.StatusFound)
	expect(t, stranger.get(start.Header.Get("Location")), http.StatusForbidden)
}

// TestFullLoginTableEvictsInsteadOfRefusing: with the table full, a new sign-in still starts (the oldest is dropped), so a
// few dozen requests cannot lock staff out for the TTL.
func TestFullLoginTableEvictsInsteadOfRefusing(t *testing.T) {
	fx := newFixture(t, devConfig())
	for i := 0; i < maxLogins; i++ {
		var k [32]byte
		k[0], k[1] = byte(i), byte(i>>8)
		fx.s.logins[k] = &loginState{ip: "198.51.100.1", created: fx.clk.t.Add(-time.Duration(maxLogins-i) * time.Second)}
	}
	expect(t, fx.client().get("/auth/login?as=owner"), http.StatusFound)
	if len(fx.s.logins) != maxLogins {
		t.Fatalf("table holds %d sign-ins, want %d", len(fx.s.logins), maxLogins)
	}
}
