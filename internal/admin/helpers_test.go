package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"cherry/internal/auth"
	"cherry/internal/config"
	_ "cherry/internal/economy" // declares cherrytips.json, the document the tests drive
	"cherry/internal/store"
)

// The fixture: a staff server built with New (no listener), a database, a clock and the log lines it writes.
const (
	testHost   = "staff.test"
	testOrigin = "https://staff.test"
	testRemote = "192.0.2.10:4000"
	ownerID    = "111111111111111111"
	friendID   = "222222222222222222"
	strangerID = "333333333333333333"
	testSecret = "s3cret"
	testRedir  = "https://staff.test/auth/callback"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

type fixture struct {
	t    *testing.T
	s    *Server
	clk  *clock
	logs *bytes.Buffer
}

// newFixture builds the server for cfg on a fresh database. The database is attached (OpenStore), so the audit trail and
// the documents work; the clock starts at a fixed time.
func newFixture(t *testing.T, cfg config.Config) *fixture {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	_ = store.CloseStore() // an earlier fixture in this test may still hold its database open
	if err := store.OpenStore(filepath.Join(base, store.DBFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.CloseStore() })
	logs := &bytes.Buffer{}
	s, err := New(cfg, log.New(logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	s.now = clk.now
	return &fixture{t: t, s: s, clk: clk, logs: logs}
}

// devConfig is the dev-fake staff UI: the owner is dev-owner.
func devConfig() config.Config {
	return config.Config{
		Staff: ":443", StaffHostname: testHost, StaffOwner: "dev-owner",
		AuthMode: config.AuthDevFake, Signup: config.SignupOpen, ServerName: "Cherry test",
	}
}

// discordConfig is the Discord staff UI: the owner is ownerID and the allowlist holds allowlist (file text, may be "").
func discordConfig(t *testing.T, allowlist string) config.Config {
	t.Helper()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte(testSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Staff: ":443", StaffHostname: testHost, StaffOwner: ownerID,
		AuthMode: config.AuthDiscord, DiscordClientID: "cid", DiscordSecretFile: secret,
		StaffRedirect: testRedir, Signup: config.SignupAllowlist, ServerName: "Cherry test",
	}
	if allowlist != "" {
		path := filepath.Join(t.TempDir(), "staff.txt")
		if err := os.WriteFile(path, []byte(allowlist), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.StaffAllowlistFile = path
	}
	return cfg
}

// fakeDiscord serves the three calls the sign-in makes. A code "c|<id>|<mfa>" is good (mfa is 1 or 0) and its token is
// "tok|<id>|<mfa>"; any other code is refused with 400, as Discord refuses an unknown one. Tokens that are revoked are
// counted in *revoked. auth.DiscordBase points here for the test.
func fakeDiscord(t *testing.T, revoked *int) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := r.PostForm.Get("code")
		switch {
		case r.PostForm.Get("client_secret") != testSecret || r.PostForm.Get("redirect_uri") != testRedir:
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		case !strings.HasPrefix(code, "c|"):
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		default:
			writeJSON(w, map[string]any{"access_token": "tok|" + strings.TrimPrefix(code, "c|")})
		}
	})
	mux.HandleFunc("/api/users/@me", func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer tok|")
		parts := strings.Split(tok, "|")
		if !ok || len(parts) != 2 {
			http.Error(w, `{"error":"401: Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"id": parts[0], "username": "name" + parts[0], "global_name": "Display Name", "mfa_enabled": parts[1] == "1"})
	})
	mux.HandleFunc("/api/oauth2/token/revoke", func(w http.ResponseWriter, r *http.Request) {
		*revoked++
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	old := auth.DiscordBase
	auth.DiscordBase = srv.URL
	t.Cleanup(func() {
		auth.DiscordBase = old
		srv.Close()
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// client is one browser: its cookies (Set-Cookie is kept, Max-Age=0 removes) and its address.
type client struct {
	fx      *fixture
	cookies map[string]string
	remote  string
}

func (fx *fixture) client() *client {
	return &client{fx: fx, cookies: map[string]string{}, remote: testRemote}
}

// reply is a response with its body read.
type reply struct {
	*http.Response
	body string
}

// req builds a request the way a browser sends it: the staff host, the client's cookies, and for a POST the staff Origin.
// A form is urlencoded. Tests change the request (drop the Origin, add a header) before sending it when they need to.
func (c *client) req(method, target string, form url.Values) *http.Request {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, target, body)
	r.Host = testHost
	r.RemoteAddr = c.remote
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodPost {
		r.Header.Set("Origin", testOrigin)
	}
	for name, v := range c.cookies {
		r.AddCookie(&http.Cookie{Name: name, Value: v})
	}
	return r
}

// send runs r through the staff handler and keeps the cookies the reply sets.
func (c *client) send(r *http.Request) reply {
	c.fx.t.Helper()
	rec := httptest.NewRecorder()
	c.fx.s.Handler().ServeHTTP(rec, r)
	resp := rec.Result()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.fx.t.Fatal(err)
	}
	for _, ck := range resp.Cookies() {
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck.Value
		}
	}
	return reply{Response: resp, body: string(raw)}
}

func (c *client) get(target string) reply { return c.send(c.req(http.MethodGet, target, nil)) }
func (c *client) post(target string, form url.Values) reply {
	if form == nil {
		form = url.Values{}
	}
	return c.send(c.req(http.MethodPost, target, form))
}

// csrf reads the CSRF token of the signed-in session from a page (every signed-in page carries it in the sign-out form).
var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (c *client) csrf() string {
	c.fx.t.Helper()
	m := csrfField.FindStringSubmatch(c.get("/").body)
	if m == nil {
		c.fx.t.Fatal("no CSRF token on the dashboard: not signed in?")
	}
	return m[1]
}

// signInDev signs in through the dev-fake provider as name and checks the sign-in ended on the dashboard.
func (c *client) signInDev(name string) {
	c.fx.t.Helper()
	r := c.get("/auth/login?as=" + name)
	expect(c.fx.t, r, http.StatusFound)
	c.follow(r)
}

// follow sends the browser to a redirect's location and returns that reply; the location must be on this server.
func (c *client) follow(r reply) reply {
	c.fx.t.Helper()
	loc := r.Header.Get("Location")
	if loc == "" {
		c.fx.t.Fatal("no Location header")
	}
	return c.get(loc)
}

// expect fails the test unless r has the status; the body is shown on failure (it never holds a secret).
func expect(t *testing.T, r reply, status int) {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("status %d, want %d; body: %.300s", r.StatusCode, status, r.body)
	}
}

// cookieOf returns the first Set-Cookie of name in r.
func cookieOf(r reply, name string) *http.Cookie {
	for _, ck := range r.Cookies() {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

// stateOf returns the state parameter of a redirect to Discord or to the callback.
func stateOf(t *testing.T, r reply) string {
	t.Helper()
	u, err := url.Parse(r.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

// discordLogin starts a sign-in and returns the callback reply for code, with the browser's cookies.
func (c *client) discordLogin(code string) reply {
	c.fx.t.Helper()
	start := c.get("/auth/login")
	expect(c.fx.t, start, http.StatusFound)
	state := stateOf(c.fx.t, start)
	return c.get("/auth/callback?code=" + url.QueryEscape(code) + "&state=" + state)
}

// auditRows returns the newest audit rows (the store's listing), for assertions.
func auditRows(t *testing.T) []store.AuditRow {
	t.Helper()
	rows, err := store.AuditRows(0, 200)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// hasAudit reports whether an audit row has the actor, action and a detail containing reason.
func hasAudit(rows []store.AuditRow, actor, action, reason string) bool {
	for _, r := range rows {
		if r.Actor == actor && r.Action == action && (reason == "" || strings.Contains(r.Detail, reason)) {
			return true
		}
	}
	return false
}
