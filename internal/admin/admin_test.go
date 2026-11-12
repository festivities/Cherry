package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cherry/internal/opdata"
	"cherry/internal/store"
)

// TestDevFakeFlow walks the whole path through the real handler: sign in, dashboard, document list, history, draft,
// validate, publish, rollback, audit, sign out.
func TestDevFakeFlow(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	if r := c.get("/"); r.StatusCode != http.StatusOK || !strings.Contains(r.body, "dev-owner") {
		t.Fatalf("dashboard: status %d, identity not shown", r.StatusCode)
	}
	expect(t, c.get("/docs"), http.StatusOK)
	if r := c.get("/docs/cherrytips.json"); r.StatusCode != http.StatusOK || !strings.Contains(r.body, "<textarea") {
		t.Fatalf("history: status %d", r.StatusCode)
	}
	def, ok := opdata.Embedded("cherrytips.json")
	if !ok {
		t.Fatal("cherrytips.json has no embedded default")
	}
	draft := c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {string(def)}, "note": {"first"}})
	expect(t, draft, http.StatusSeeOther)
	if loc := draft.Header.Get("Location"); loc != "/docs/cherrytips.json/1" {
		t.Fatalf("draft redirects to %q", loc)
	}
	expect(t, c.get("/docs/cherrytips.json/1"), http.StatusOK)
	if r := c.post("/docs/cherrytips.json/1/validate", url.Values{"csrf": {c.csrf()}}); r.StatusCode != http.StatusOK || !strings.Contains(r.body, "Valid.") {
		t.Fatalf("validate: status %d", r.StatusCode)
	}
	pub := c.post("/docs/cherrytips.json/1/publish", url.Values{"csrf": {c.csrf()}})
	if pub.StatusCode != http.StatusOK || !strings.Contains(pub.body, "applies at the next restart") {
		t.Fatalf("publish: status %d body %.200s", pub.StatusCode, pub.body)
	}
	live, err := store.Live()
	if err != nil {
		t.Fatal(err)
	}
	if got := liveOf(live, "cherrytips.json"); got.Published != 1 || !got.Pending {
		t.Fatalf("after publish: published %d pending %v", got.Published, got.Pending)
	}
	audit := c.get("/audit")
	for _, want := range []string{"draft", "publish", "login"} {
		if !strings.Contains(audit.body, "<td>"+want+"</td>") {
			t.Errorf("audit page lacks a %s row", want)
		}
	}
	if !hasAudit(auditRows(t), "dev-owner", "publish", "") {
		t.Error("the publish is not in the audit trail with its actor")
	}
	if r := c.post("/docs/cherrytips.json/rollback", url.Values{"csrf": {c.csrf()}}); r.StatusCode != http.StatusOK || !strings.Contains(r.body, "no published version now") {
		t.Fatalf("rollback: status %d", r.StatusCode)
	}
	out := c.post("/auth/logout", url.Values{"csrf": {c.csrf()}})
	expect(t, out, http.StatusSeeOther)
	expect(t, c.get("/"), http.StatusUnauthorized)
}

// liveOf is the Live row of name.
func liveOf(docs []store.DocLive, name string) store.DocLive {
	for _, d := range docs {
		if d.Name == name {
			return d
		}
	}
	return store.DocLive{}
}

// TestCSRFAndOriginRefusals: a state change needs the session's CSRF token and the staff Origin (or Sec-Fetch-Site
// same-origin without an Origin). Each refusal is a 403 and stores nothing.
func TestCSRFAndOriginRefusals(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	token := c.csrf()
	body := url.Values{"csrf": {token}, "body": {"{}"}, "note": {"x"}}

	refused := []struct {
		name   string
		mutate func(r *http.Request)
		form   url.Values
	}{
		{"no token", nil, url.Values{"body": {"{}"}}},
		{"wrong token", nil, url.Values{"csrf": {"wrong"}, "body": {"{}"}}},
		{"empty token", nil, url.Values{"csrf": {""}, "body": {"{}"}}},
		{"wrong origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }, body},
		{"origin null", func(r *http.Request) { r.Header.Set("Origin", "null") }, body},
		{"two origins", func(r *http.Request) { r.Header.Add("Origin", testOrigin) }, body},
		{"origin http scheme", func(r *http.Request) { r.Header.Set("Origin", "http://staff.test") }, body},
		{"no origin, no fetch metadata", func(r *http.Request) { r.Header.Del("Origin") }, body},
		{"no origin, cross-site", func(r *http.Request) { r.Header.Del("Origin"); r.Header.Set("Sec-Fetch-Site", "cross-site") }, body},
	}
	for _, tc := range refused {
		r := c.req(http.MethodPost, "/docs/cherrytips.json/draft", tc.form)
		if tc.mutate != nil {
			tc.mutate(r)
		}
		if got := c.send(r); got.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", tc.name, got.StatusCode)
		}
	}
	if versions, _ := store.History("cherrytips.json"); len(versions) != 0 {
		t.Fatalf("a refused request stored %d draft(s)", len(versions))
	}

	// Without an Origin, Sec-Fetch-Site same-origin is accepted (the form is valid).
	ok := c.req(http.MethodPost, "/docs/cherrytips.json/draft", body)
	ok.Header.Del("Origin")
	ok.Header.Set("Sec-Fetch-Site", "same-origin")
	expect(t, c.send(ok), http.StatusSeeOther)
	if versions, _ := store.History("cherrytips.json"); len(versions) != 1 {
		t.Fatalf("the accepted draft: %d versions", len(versions))
	}
}

// TestStateChangesRefuseGET: a state change reached by GET is 405 (the mux, or the route's own answer), never a change.
func TestStateChangesRefuseGET(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	for _, target := range []string{"/auth/logout", "/docs/cherrytips.json/draft", "/docs/cherrytips.json/rollback", "/docs/cherrytips.json/1/publish", "/docs/cherrytips.json/1/validate"} {
		r := c.get(target)
		if r.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: status %d, want 405", target, r.StatusCode)
		}
	}
	if _, ok := c.cookies["__Host-cherry-staff"]; !ok {
		t.Fatal("a GET to /auth/logout ended the session")
	}
}

// TestEveryResponseHasTheHeaders: the headers of PLAN P4 on every kind of reply, and no script or inline style in any page.
func TestEveryResponseHasTheHeaders(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	paths := []struct{ method, target string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/docs"},
		{http.MethodGet, "/static/staff.css"},
		{http.MethodGet, "/auth/login?as=bad%20name"},
		{http.MethodGet, "/no-such-page"},
		{http.MethodPost, "/auth/logout"},
		{http.MethodGet, "/auth/callback?code=x&state=y"},
	}
	for _, p := range paths {
		r := c.send(c.req(p.method, p.target, nil))
		checkHeaders(t, p.target, r)
	}
	c.signInDev("owner")
	for _, target := range []string{"/", "/docs", "/docs/cherrytips.json", "/audit"} {
		r := c.get(target)
		checkHeaders(t, target, r)
		if r.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", target, r.StatusCode)
		}
	}
}

// checkHeaders asserts the fixed header set and the absence of script and inline style.
func checkHeaders(t *testing.T, target string, r reply) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy":    "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "no-referrer",
		"Cache-Control":              "no-store",
		"Cross-Origin-Opener-Policy": "same-origin",
		"X-Frame-Options":            "DENY",
	}
	for k, v := range want {
		if got := r.Header.Get(k); got != v {
			t.Errorf("%s: %s = %q, want %q", target, k, got, v)
		}
	}
	if r.Header.Get("Strict-Transport-Security") != "" {
		t.Errorf("%s: HSTS on a server that is not public", target)
	}
	lower := strings.ToLower(r.body)
	if strings.Contains(lower, "<script") || strings.Contains(lower, "style=") {
		t.Errorf("%s: page holds a script or an inline style", target)
	}
}

// TestPublicAddsHSTS: a public server sends Strict-Transport-Security.
func TestPublicAddsHSTS(t *testing.T) {
	cfg := devConfig()
	cfg.Public = true
	fx := newFixture(t, cfg)
	r := fx.client().get("/auth/login?as=owner")
	if got := r.Header.Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Fatalf("HSTS = %q", got)
	}
}

// TestHostMismatchIs421: with staff.hostname set, another Host gets 421 and no body; the port of the listener is allowed.
func TestHostMismatchIs421(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	for _, host := range []string{"other.test", "", "staff.test.evil", "staff.test:8443"} {
		r := c.req(http.MethodGet, "/", nil)
		r.Host = host
		got := c.send(r)
		if got.StatusCode != http.StatusMisdirectedRequest || got.body != "" {
			t.Errorf("Host %q: status %d body %q, want 421 and nothing", host, got.StatusCode, got.body)
		}
		checkHeaders(t, "421", got)
	}
	r := c.req(http.MethodGet, "/", nil)
	r.Host = "staff.test:443"
	if got := c.send(r); got.StatusCode != http.StatusUnauthorized {
		t.Errorf("Host staff.test:443: status %d, want the sign-in page", got.StatusCode)
	}
}

// TestGameRoutesAreNotStaffRoutes: the staff mux answers game paths with 404, never with a game reply.
func TestGameRoutesAreNotStaffRoutes(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	for _, target := range []string{"/v4/createSession", "/v4/checkSession", "/cherry/v1/info", "/auth/v1/discord/start", "/auth/v1/exchange", "/v4/account/guest/generate", "/admin/docs"} {
		if r := c.get(target); r.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s on the staff mux: status %d, want 404", target, r.StatusCode)
		}
	}
}

// TestBodyCaps: a document over the store's 2 MiB cap is 413; a form over the request cap is 413 before it is parsed.
func TestBodyCaps(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	big := `{"a":"` + strings.Repeat("x", store.MaxDocumentBytes) + `"}`
	if r := c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {big}}); r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a document over 2 MiB: status %d, want 413", r.StatusCode)
	}
	huge := url.Values{"csrf": {c.csrf()}, "body": {strings.Repeat("y", docBodyCap)}}
	if r := c.post("/docs/cherrytips.json/draft", huge); r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a form over the cap: status %d, want 413", r.StatusCode)
	}
	if r := c.post("/docs/cherrytips.json/1/publish", url.Values{"csrf": {c.csrf()}, "pad": {strings.Repeat("p", maxBodySmall)}}); r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a small action with a big body: status %d, want 413", r.StatusCode)
	}
}

// TestUnknownDocumentAndVersion: names and versions are checked by the store; a bad version is 400, a missing one 404.
func TestUnknownDocumentAndVersion(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	expect(t, c.get("/docs/not-a-document.json"), http.StatusNotFound)
	expect(t, c.get("/docs/cherrytips.json/0"), http.StatusBadRequest)
	expect(t, c.get("/docs/cherrytips.json/9"), http.StatusNotFound)
	expect(t, c.get("/audit?before=abc"), http.StatusBadRequest)
	expect(t, c.get("/audit?before=5&before=6"), http.StatusBadRequest)
}

// TestConfigOwnerIdentities: the owner of a dev instance is the configured dev name; a discord owner is the discord identity.
func TestConfigOwnerIdentities(t *testing.T) {
	if got := identityOf("dev-owner"); got != "dev-owner" {
		t.Errorf("dev owner = %q", got)
	}
	if got := identityOf(ownerID); got != "discord:"+ownerID {
		t.Errorf("discord owner = %q", got)
	}
	if got := originFor("staff.test", "443"); got != "https://staff.test" {
		t.Errorf("origin on 443 = %q", got)
	}
	if got := originFor("127.0.0.1", "18444"); got != "https://127.0.0.1:18444" {
		t.Errorf("origin on 18444 = %q", got)
	}
}
