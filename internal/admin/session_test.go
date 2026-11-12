package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cherry/internal/opdata"
	"cherry/internal/store"
)

const sessionCookie = "__Host-cherry-staff"

func TestIdleTimeout(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	fx.clk.advance(29 * time.Minute)
	expect(t, c.get("/"), http.StatusOK)
	fx.clk.advance(30 * time.Minute)
	expect(t, c.get("/"), http.StatusUnauthorized)
	expect(t, c.get("/"), http.StatusUnauthorized) // gone, not revived
}

// TestAbsoluteLifetime: activity keeps the idle clock off, but the session ends eight hours after the login.
func TestAbsoluteLifetime(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	for i := 1; i <= 23; i++ { // up to 7h40, every 20 minutes
		fx.clk.advance(20 * time.Minute)
		expect(t, c.get("/"), http.StatusOK)
	}
	fx.clk.advance(20 * time.Minute) // 8h00
	expect(t, c.get("/"), http.StatusUnauthorized)
}

// TestRotationOnLogin: a new login gets a new session id; the old one stops working.
func TestRotationOnLogin(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	old := c.cookies[sessionCookie]
	c.signInDev("owner")
	if c.cookies[sessionCookie] == "" || c.cookies[sessionCookie] == old {
		t.Fatal("a new login did not rotate the session id")
	}
	stolen := fx.client()
	stolen.cookies[sessionCookie] = old
	expect(t, stolen.get("/"), http.StatusUnauthorized)
}

// TestLogoutEndsSession: sign-out needs the CSRF token, then ends the session on the server and clears the cookie.
func TestLogoutEndsSession(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	old := c.cookies[sessionCookie]
	expect(t, c.post("/auth/logout", url.Values{}), http.StatusForbidden)
	expect(t, c.get("/"), http.StatusOK)
	out := c.post("/auth/logout", url.Values{"csrf": {c.csrf()}})
	expect(t, out, http.StatusSeeOther)
	if _, ok := c.cookies[sessionCookie]; ok {
		t.Fatal("the logout reply kept the session cookie")
	}
	ck := cookieOf(out, sessionCookie)
	if ck == nil || ck.MaxAge >= 0 {
		t.Fatalf("the logout reply does not expire the cookie: %+v", ck)
	}
	stolen := fx.client()
	stolen.cookies[sessionCookie] = old
	expect(t, stolen.get("/"), http.StatusUnauthorized)
}

// TestStalePrivilegedActionIsRefused: publish and roll back need a Discord login in the last five minutes. After six
// minutes the action is refused, nothing is published, and the page sends the user through the sign-in and back.
func TestStalePrivilegedActionIsRefused(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	def, _ := opdata.Embedded("cherrytips.json")
	expect(t, c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {string(def)}}), http.StatusSeeOther)
	fx.clk.advance(6 * time.Minute)

	pub := c.post("/docs/cherrytips.json/1/publish", url.Values{"csrf": {c.csrf()}})
	expect(t, pub, http.StatusForbidden)
	if want := `href="/auth/login?next=%2Fdocs%2Fcherrytips.json%2F1"`; !strings.Contains(pub.body, want) {
		t.Errorf("the refusal does not link the sign-in back to the version page: %s", want)
	}
	roll := c.post("/docs/cherrytips.json/rollback", url.Values{"csrf": {c.csrf()}})
	expect(t, roll, http.StatusForbidden)
	live, err := store.Live()
	if err != nil {
		t.Fatal(err)
	}
	if got := liveOf(live, "cherrytips.json"); got.Published != 0 {
		t.Fatalf("a stale publish published v%d", got.Published)
	}

	// The sign-in brings the fresh login; the same action then goes through.
	c.signInDev("owner")
	expect(t, c.post("/docs/cherrytips.json/1/publish", url.Values{"csrf": {c.csrf()}}), http.StatusOK)
}

// TestPrivilegedNeedsSession: without a session a state change is the sign-in page, never an action.
func TestPrivilegedNeedsSession(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	expect(t, c.post("/docs/cherrytips.json/1/publish", url.Values{"csrf": {"x"}}), http.StatusUnauthorized)
	expect(t, c.post("/docs/cherrytips.json/rollback", url.Values{"csrf": {"x"}}), http.StatusUnauthorized)
}
