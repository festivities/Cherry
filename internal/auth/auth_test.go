package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"cherry/internal/config"
	"cherry/internal/store"
)

const (
	testVerifier = "verifier-0123456789-abcdefghijklmnopqrstuvwxyz"
	testState    = "state0123456789abcdef"
	testRedirect = "https://cherry.example/auth/v1/discord/callback"
	testSecret   = "client-secret-value"
	testToken    = "discord-access-token-value"
	testGoodCode = "good-discord-code"
	testIP       = "192.0.2.10"
)

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// pkce is the challenge of a verifier: base64url of SHA-256, no padding.
func pkce(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// useAccountFile points the account table at a fresh file for one test and restores the old state after it.
func useAccountFile(t *testing.T) {
	t.Helper()
	store.AccountsMu.Lock()
	oldAccounts, oldLatest, oldNext, oldPath := store.Accounts, store.LatestAcc, store.NextAvatarID, store.AccountStorePath
	store.AccountsMu.Unlock()
	t.Cleanup(func() {
		store.AccountsMu.Lock()
		store.Accounts, store.LatestAcc, store.NextAvatarID, store.AccountStorePath = oldAccounts, oldLatest, oldNext, oldPath
		store.AccountsMu.Unlock()
	})
	if err := store.LoadAccountsFrom(filepath.Join(t.TempDir(), "accounts.json")); err != nil {
		t.Fatal(err)
	}
}

// testEnv is a service on a fake clock with its own mux and log buffer.
type testEnv struct {
	t    *testing.T
	s    *Service
	mux  *http.ServeMux
	now  time.Time
	logs *bytes.Buffer
}

func ports() config.Config {
	return config.Config{HTTP: ":80", HTTPS: ":443", Gateway: ":10000", Session: ":10123", ServerName: "Test"}
}

func devConfig() config.Config {
	c := ports()
	c.AuthMode = config.AuthDevFake
	return c
}

// discordConfig writes a 0600 secret file for the test and returns the discord settings.
func discordConfig(t *testing.T) config.Config {
	t.Helper()
	file := filepath.Join(t.TempDir(), "discord.secret")
	if err := os.WriteFile(file, []byte(testSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := ports()
	c.AuthMode = config.AuthDiscord
	c.DiscordClientID = "123"
	c.DiscordSecretFile = file
	c.DiscordRedirect = testRedirect
	return c
}

func newEnv(t *testing.T, cfg config.Config) *testEnv {
	t.Helper()
	useAccountFile(t)
	logs := &bytes.Buffer{}
	s, err := New(cfg, log.New(logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	env := &testEnv{t: t, s: s, mux: http.NewServeMux(), logs: logs,
		now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	s.Now = func() time.Time { return env.now }
	s.Register(env.mux)
	return env
}

func (e *testEnv) do(method, target, body, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = ip + ":40000"
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) get(target, ip string) *httptest.ResponseRecorder {
	return e.do(http.MethodGet, target, "", ip)
}

// startTarget is a well-formed start request.
func startTarget(q url.Values) string {
	v := url.Values{"challenge": {pkce(testVerifier)}, "return": {"raspberry://auth"}, "state": {testState}}
	for k, vals := range q {
		v[k] = vals
	}
	return "/auth/v1/discord/start?" + v.Encode()
}

// location parses a redirect's Location and returns its query.
func location(t *testing.T, rec *httptest.ResponseRecorder) (string, url.Values) {
	t.Helper()
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Location %q: %v", loc, err)
	}
	return loc, u.Query()
}

// devSignIn runs start and the dev-fake callback and returns the ticket the app would get.
func (e *testEnv) devSignIn(ip, as string) string {
	e.t.Helper()
	start := startTarget(url.Values{"as": {as}})
	rec := e.get(start, ip)
	if rec.Code != http.StatusFound {
		e.t.Fatalf("start = %d", rec.Code)
	}
	rec = e.get(rec.Header().Get("Location"), ip)
	if rec.Code != http.StatusFound {
		e.t.Fatalf("callback = %d %s", rec.Code, rec.Body.String())
	}
	_, q := location(e.t, rec)
	if q.Get("error") != "" || q.Get("ticket") == "" || q.Get("state") != testState {
		e.t.Fatalf("callback redirect = %v, want a ticket", q)
	}
	return q.Get("ticket")
}

func (e *testEnv) exchange(ticket, verifier, ip string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"ticket": ticket, "verifier": verifier})
	return e.do(http.MethodPost, "/auth/v1/exchange", string(body), ip)
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return body.Error
}

// TestStartRefusesEveryMalformedRequest: each shape of bad input is a 400 page with no redirect and a strict policy.
func TestStartRefusesEveryMalformedRequest(t *testing.T) {
	env := newEnv(t, devConfig())
	canonicalBad := []byte(pkce(testVerifier))
	canonicalBad[42] = 'B' // a 43-character challenge whose last character carries non-zero padding bits
	cases := map[string]string{
		"challenge short":          startTarget(url.Values{"challenge": {pkce(testVerifier)[:42]}}),
		"challenge padded":         startTarget(url.Values{"challenge": {pkce(testVerifier) + "="}}),
		"challenge not base64url":  startTarget(url.Values{"challenge": {strings.Repeat("+", 43)}}),
		"challenge not canonical":  startTarget(url.Values{"challenge": {string(canonicalBad)}}),
		"state short":              startTarget(url.Values{"state": {"short"}}),
		"state long":               startTarget(url.Values{"state": {strings.Repeat("s", 65)}}),
		"state bad character":      startTarget(url.Values{"state": {"state with spaces!!"}}),
		"return https":             startTarget(url.Values{"return": {"https://evil.example/cb"}}),
		"return low port":          startTarget(url.Values{"return": {"http://127.0.0.1:80/raspberry/cb"}}),
		"return high port":         startTarget(url.Values{"return": {"http://127.0.0.1:65536/raspberry/cb"}}),
		"return leading zero":      startTarget(url.Values{"return": {"http://127.0.0.1:05000/raspberry/cb"}}),
		"return plus sign":         startTarget(url.Values{"return": {"http://127.0.0.1:+5000/raspberry/cb"}}),
		"return other host":        startTarget(url.Values{"return": {"http://localhost:5000/raspberry/cb"}}),
		"return other path":        startTarget(url.Values{"return": {"http://127.0.0.1:5000/raspberry/cb2"}}),
		"return with user info":    startTarget(url.Values{"return": {"http://evil@127.0.0.1:5000/raspberry/cb"}}),
		"return app with a path":   startTarget(url.Values{"return": {"raspberry://auth/x"}}),
		"return open redirect":     startTarget(url.Values{"return": {"raspberry://auth?next=https://evil.example"}}),
		"state missing":            "/auth/v1/discord/start?challenge=" + pkce(testVerifier) + "&return=raspberry%3A%2F%2Fauth",
		"extra key":                startTarget(url.Values{"extra": {"1"}}),
		"duplicate challenge":      startTarget(url.Values{"challenge": {pkce(testVerifier), pkce(testVerifier)}}),
		"dev name with a space":    startTarget(url.Values{"as": {"bad name"}}),
		"dev name empty":           startTarget(url.Values{"as": {""}}),
		"dev name too long":        startTarget(url.Values{"as": {strings.Repeat("a", 25)}}),
		"dev name with a colon":    startTarget(url.Values{"as": {"a:b"}}),
		"dev name with a redirect": startTarget(url.Values{"as": {"//evil.example"}}),
	}
	i := 0
	for name, target := range cases {
		i++ // one address per case: the start budget is 10 a minute, and refused requests count too
		rec := env.get(target, fmt.Sprintf("198.51.100.%d", i))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Errorf("%s: redirected to %q", name, loc)
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") || strings.Contains(rec.Body.String(), "<script") {
			t.Errorf("%s: page policy or content is not strict", name)
		}
	}
	if rec := env.do(http.MethodPost, startTarget(nil), "", testIP); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Location") != "" {
		t.Errorf("POST start = %d, want 405 without a redirect", rec.Code)
	}
	if n := len(env.s.attempts); n != 0 {
		t.Fatalf("refused requests left %d attempts", n)
	}
}

// TestValidReturnsOnly pins the return rule: exactly the two forms, with canonical ports in range.
func TestValidReturnsOnly(t *testing.T) {
	for ret, want := range map[string]bool{
		"raspberry://auth":                       true,
		"http://127.0.0.1:1024/raspberry/cb":     true,
		"http://127.0.0.1:65535/raspberry/cb":    true,
		"http://127.0.0.1:1023/raspberry/cb":     false,
		"http://127.0.0.1:65536/raspberry/cb":    false,
		"http://127.0.0.1:5000/raspberry/cb?x=1": false,
		"https://127.0.0.1:5000/raspberry/cb":    false,
		"raspberry://auth ":                      false,
		"RASPBERRY://auth":                       false,
		"http://127.0.0.1:5000/raspberry/cb/":    false,
	} {
		if got := validReturn(ret); got != want {
			t.Errorf("validReturn(%q) = %v, want %v", ret, got, want)
		}
	}
}

// TestDevFakeSignInExchangeAndSingleUse: the whole dev-fake path; the ticket and the verifier are single-use and the
// exchange mints a uniqueCode with aid "0" and new true for a fresh identity.
func TestDevFakeSignInExchangeAndSingleUse(t *testing.T) {
	env := newEnv(t, devConfig())
	if rec := env.get(startTarget(nil), "192.0.2.50"); !strings.Contains(rec.Header().Get("Location"), "code=dev:dev&state=") {
		t.Fatalf("start without as = %q, want the default name dev", rec.Header().Get("Location"))
	}
	ticket := env.devSignIn(testIP, "alice")
	rec := env.exchange(ticket, testVerifier, testIP)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		UniqueCode string `json:"uniqueCode"`
		Aid        string `json:"aid"`
		New        bool   `json:"new"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !uuidShape.MatchString(out.UniqueCode) || out.Aid != "0" || !out.New {
		t.Fatalf("exchange body = %s (err %v)", rec.Body.String(), err)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("exchange headers = %v", rec.Header())
	}
	if again := env.exchange(ticket, testVerifier, testIP); again.Code != http.StatusUnauthorized || errorOf(t, again) != "invalid_ticket" {
		t.Fatalf("reused ticket = %d %s, want 401 invalid_ticket", again.Code, again.Body.String())
	}
	store.AccountsMu.Lock()
	acc := store.IdentityAccountLocked("dev-alice")
	store.AccountsMu.Unlock()
	if acc == nil || len(acc.CodeHashes) != 1 || acc.CodeHashes[0] != store.CodeHash(out.UniqueCode) {
		t.Fatal("the identity does not hold the hash of the minted code")
	}
}

// TestWrongVerifierBurnsTheTicket: a wrong verifier is refused, and the right one afterwards is refused too.
func TestWrongVerifierBurnsTheTicket(t *testing.T) {
	env := newEnv(t, devConfig())
	ticket := env.devSignIn(testIP, "bob")
	if rec := env.exchange(ticket, testVerifier+"x", testIP); rec.Code != http.StatusUnauthorized || errorOf(t, rec) != "invalid_ticket" {
		t.Fatalf("wrong verifier = %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.exchange(ticket, testVerifier, testIP); rec.Code != http.StatusUnauthorized {
		t.Fatalf("right verifier after a wrong one = %d, want the ticket burned", rec.Code)
	}
}

// TestTicketExpires: a ticket is refused after 120 seconds.
func TestTicketExpires(t *testing.T) {
	env := newEnv(t, devConfig())
	ticket := env.devSignIn(testIP, "carol")
	env.now = env.now.Add(ticketTTL + time.Second)
	if rec := env.exchange(ticket, testVerifier, testIP); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired ticket = %d, want 401", rec.Code)
	}
}

// TestAttemptExpiresAndIsUsedOnce: an attempt older than 10 minutes returns error=expired to its return address, and
// a second callback for the same state is an unknown page.
func TestAttemptExpiresAndIsUsedOnce(t *testing.T) {
	env := newEnv(t, devConfig())
	rec := env.get(startTarget(nil), testIP)
	callback := rec.Header().Get("Location")
	env.now = env.now.Add(attemptTTL + time.Second)
	rec = env.get(callback, testIP)
	if _, q := location(t, rec); rec.Code != http.StatusFound || q.Get("error") != "expired" || q.Get("state") != testState {
		t.Fatalf("expired attempt = %d %v, want error=expired to the return", rec.Code, q)
	}
	if rec := env.get(callback, testIP); rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
		t.Fatalf("second callback = %d, want a 400 page", rec.Code)
	}
}

// TestCallbackWithoutAStateIsAPage: no state means no return, so the answer is the page and never a redirect.
func TestCallbackWithoutAStateIsAPage(t *testing.T) {
	env := newEnv(t, devConfig())
	for _, target := range []string{"/auth/v1/discord/callback?code=dev:x", "/auth/v1/discord/callback?code=dev:x&state=a&state=b", "/auth/v1/discord/callback?code=dev:x&state=no"} {
		if rec := env.get(target, testIP); rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("%s = %d, want a 400 page", target, rec.Code)
		}
	}
}

// TestDevCodeIsRefusedInDiscordMode: a dev: code reaches the callback in discord mode and is denied to the return.
func TestDevCodeIsRefusedInDiscordMode(t *testing.T) {
	env := newEnv(t, discordConfig(t))
	rec := env.get(startTarget(nil), testIP)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://discord.com/oauth2/authorize?") {
		t.Fatalf("start = %d %q, want a redirect to Discord", rec.Code, rec.Header().Get("Location"))
	}
	loc, _ := location(t, rec)
	u, _ := url.Parse(loc)
	if got := u.Query().Get("redirect_uri"); got != testRedirect || u.Query().Get("client_id") != "123" || u.Query().Get("scope") != "identify" {
		t.Fatalf("authorize parameters = %v", u.Query())
	}
	serverState := u.Query().Get("state")
	rec = env.get("/auth/v1/discord/callback?code=dev:mallory&state="+serverState, testIP)
	if _, q := location(t, rec); q.Get("error") != "denied" {
		t.Fatalf("dev code in discord mode = %d %v, want error=denied", rec.Code, q)
	}
}

// TestDiscordRoundTrip runs the discord path against a fake Discord: token, users/@me, then a best-effort revoke.
func TestDiscordRoundTrip(t *testing.T) {
	var mu sync.Mutex
	revoked := 0
	fake := httptest.NewServer(fakeDiscord(t, func(id string) { mu.Lock(); revoked++; mu.Unlock() }, "123456789012345678"))
	defer fake.Close()
	oldBase := DiscordBase
	DiscordBase = fake.URL
	t.Cleanup(func() { DiscordBase = oldBase })

	cfg := discordConfig(t)
	env := newEnv(t, cfg)
	rec := env.get(startTarget(nil), testIP)
	loc, _ := location(t, rec)
	u, _ := url.Parse(loc)
	if !strings.HasPrefix(loc, fake.URL+"/oauth2/authorize?response_type=code&client_id=123&scope=identify&redirect_uri=") {
		t.Fatalf("authorize URL = %s", loc)
	}
	serverState := u.Query().Get("state")
	rec = env.get("/auth/v1/discord/callback?code="+testGoodCode+"&state="+serverState, testIP)
	_, q := location(t, rec)
	if q.Get("error") != "" || q.Get("ticket") == "" {
		t.Fatalf("callback = %d %v, want a ticket", rec.Code, q)
	}
	mu.Lock()
	n := revoked
	mu.Unlock()
	if n != 1 {
		t.Fatalf("revoke calls = %d, want 1", n)
	}
	rec = env.exchange(q.Get("ticket"), testVerifier, testIP)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d %s", rec.Code, rec.Body.String())
	}
	store.AccountsMu.Lock()
	acc := store.IdentityAccountLocked("discord:123456789012345678")
	store.AccountsMu.Unlock()
	if acc == nil {
		t.Fatal("the Discord identity has no account")
	}
	for name, text := range map[string]string{"response": rec.Body.String(), "location": rec.Header().Get("Location"), "logs": env.logs.String()} {
		for _, secret := range []string{testSecret, testToken, testGoodCode} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s contains a Discord secret or token", name)
			}
		}
	}
}

// fakeDiscord serves the three Discord calls. Its id is returned for users/@me; onRevoke runs on each revoke.
func fakeDiscord(t *testing.T, onRevoke func(string), id string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("client_id") != "123" || r.PostForm.Get("client_secret") != testSecret ||
			r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != testRedirect {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.PostForm.Get("code") {
		case testGoodCode:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer"}`, testToken)
		case "down":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/api/users/@me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"username":"player"}`, id)
	})
	mux.HandleFunc("/api/oauth2/token/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("token") == testToken && r.PostForm.Get("client_secret") == testSecret {
			onRevoke(testToken)
		}
	})
	return mux
}

// TestDiscordErrorsReachTheReturn: a rejected code is "expired", a Discord outage or a bad user id is "server".
func TestDiscordErrorsReachTheReturn(t *testing.T) {
	fake := httptest.NewServer(fakeDiscord(t, func(string) {}, "not-a-snowflake"))
	defer fake.Close()
	oldBase := DiscordBase
	DiscordBase = fake.URL
	t.Cleanup(func() { DiscordBase = oldBase })
	env := newEnv(t, discordConfig(t))
	for code, want := range map[string]string{"stale": "expired", "down": "server", testGoodCode: "server"} {
		rec := env.get(startTarget(nil), testIP)
		loc, _ := location(t, rec)
		u, _ := url.Parse(loc)
		rec = env.get("/auth/v1/discord/callback?code="+code+"&state="+u.Query().Get("state"), testIP)
		if _, q := location(t, rec); q.Get("error") != want {
			t.Errorf("code %q: error = %q, want %q", code, q.Get("error"), want)
		}
	}
}

// TestDiscordDenialReachesTheReturn: Discord's error parameter (the user said no) is "denied".
func TestDiscordDenialReachesTheReturn(t *testing.T) {
	env := newEnv(t, discordConfig(t))
	rec := env.get(startTarget(nil), testIP)
	loc, _ := location(t, rec)
	u, _ := url.Parse(loc)
	rec = env.get("/auth/v1/discord/callback?error=access_denied&state="+u.Query().Get("state"), testIP)
	if _, q := location(t, rec); q.Get("error") != "denied" || q.Get("state") != testState {
		t.Fatalf("denial = %v, want error=denied with the client state", q)
	}
}

// TestExchangeRefusesMalformedBodies: every bad body is invalid_request; the method is 405.
func TestExchangeRefusesMalformedBodies(t *testing.T) {
	env := newEnv(t, devConfig())
	ticket := env.devSignIn(testIP, "dan")
	cases := map[string]string{
		"not json":          "not json",
		"unknown field":     fmt.Sprintf(`{"ticket":%q,"verifier":%q,"x":1}`, ticket, testVerifier),
		"trailing data":     fmt.Sprintf(`{"ticket":%q,"verifier":%q} {}`, ticket, testVerifier),
		"short verifier":    fmt.Sprintf(`{"ticket":%q,"verifier":"short"}`, ticket),
		"bad verifier char": fmt.Sprintf(`{"ticket":%q,"verifier":%q}`, ticket, strings.Repeat("a", 42)+"/"),
		"short ticket":      fmt.Sprintf(`{"ticket":"abc","verifier":%q}`, testVerifier),
		"oversized":         `{"ticket":"` + strings.Repeat("a", maxExchangeBody) + `"}`,
	}
	for name, body := range cases {
		rec := env.do(http.MethodPost, "/auth/v1/exchange", body, testIP)
		if rec.Code != http.StatusBadRequest || errorOf(t, rec) != "invalid_request" {
			t.Errorf("%s: %d %s, want 400 invalid_request", name, rec.Code, rec.Body.String())
		}
	}
	if rec := env.get("/auth/v1/exchange", testIP); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET exchange = %d, want 405", rec.Code)
	}
	// The malformed requests above did not touch the ticket: it still works.
	if rec := env.exchange(ticket, testVerifier, testIP); rec.Code != http.StatusOK {
		t.Fatalf("ticket after malformed bodies = %d, want 200", rec.Code)
	}
}

// TestRateLimitsTrip: start is 10 a minute and 60 an hour per address, exchange 20 a minute, discovery 30 a minute.
func TestRateLimitsTrip(t *testing.T) {
	env := newEnv(t, devConfig())
	for i := 0; i < startPerMinute; i++ {
		if rec := env.get(startTarget(nil), testIP); rec.Code != http.StatusFound {
			t.Fatalf("start %d = %d, want a redirect", i, rec.Code)
		}
	}
	if rec := env.get(startTarget(nil), testIP); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th start in a minute = %d, want 429", rec.Code)
	}
	if rec := env.get(startTarget(nil), "192.0.2.99"); rec.Code != http.StatusFound {
		t.Fatalf("another address = %d, want its own budget", rec.Code)
	}

	hourly := newEnv(t, devConfig())
	for i := 0; i < startPerHour; i++ {
		if rec := hourly.get(startTarget(nil), testIP); rec.Code != http.StatusFound {
			t.Fatalf("hourly start %d = %d", i, rec.Code)
		}
		hourly.now = hourly.now.Add(59 * time.Second) // 60 hits fit in one hour, and at most 2 in one minute
	}
	if rec := hourly.get(startTarget(nil), testIP); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("61st start in an hour = %d, want 429", rec.Code)
	}

	for i := 0; i < exchangePerMinute; i++ {
		env.exchange(strings.Repeat("A", 43), testVerifier, testIP)
	}
	if rec := env.exchange(strings.Repeat("A", 43), testVerifier, testIP); rec.Code != http.StatusTooManyRequests || errorOf(t, rec) != "rate_limited" {
		t.Fatalf("21st exchange = %d, want 429 rate_limited", rec.Code)
	}

	for i := 0; i < infoPerMinute; i++ {
		env.get("/cherry/v1/info", testIP)
	}
	if rec := env.get("/cherry/v1/info", testIP); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("31st discovery = %d, want 429", rec.Code)
	}
}

// TestLimiterWindowAndKeyCap: a limit resets as its window slides, and a new key beyond the cap is refused until old
// keys age out.
func TestLimiterWindowAndKeyCap(t *testing.T) {
	l := newLimiter(2, time.Minute)
	now := time.Unix(1000, 0)
	if !l.allow("a", now) || !l.allow("a", now) || l.allow("a", now) {
		t.Fatal("two hits allowed, the third refused")
	}
	if !l.allow("a", now.Add(time.Minute)) {
		t.Fatal("the window did not slide")
	}
	for i := 0; i < maxLimiterKeys; i++ {
		l.hits[fmt.Sprint("k", i)] = []time.Time{now}
	}
	if l.allow("new", now) {
		t.Fatal("a new key beyond the cap was admitted")
	}
	if !l.allow("new", now.Add(2*time.Minute)) {
		t.Fatal("old keys did not age out at the cap")
	}
}

// TestOpenAttemptCaps: 20 open attempts from one address, or the table's total, refuse the next start.
func TestOpenAttemptCaps(t *testing.T) {
	env := newEnv(t, devConfig())
	for i := 0; i < maxAttemptsPerIP; i++ {
		env.s.attempts[fmt.Sprint("s", i)] = &attempt{ip: testIP, created: env.now}
	}
	if rec := env.get(startTarget(nil), testIP); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("start past the per-address cap = %d, want 429", rec.Code)
	}
	if rec := env.get(startTarget(nil), "192.0.2.77"); rec.Code != http.StatusFound {
		t.Fatalf("another address under the total cap = %d, want a redirect", rec.Code)
	}
	env.s.attempts = make(map[string]*attempt)
	for i := 0; i < maxAttempts; i++ {
		env.s.attempts[fmt.Sprint("t", i)] = &attempt{ip: fmt.Sprint("10.0.", i), created: env.now}
	}
	if rec := env.get(startTarget(nil), "192.0.2.78"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("start past the total cap = %d, want 429", rec.Code)
	}
}

// TestExpiredEntriesArePurged: an expired attempt or ticket does not stay in memory.
func TestExpiredEntriesArePurged(t *testing.T) {
	env := newEnv(t, devConfig())
	ticket := env.devSignIn(testIP, "erin")
	env.now = env.now.Add(attemptTTL + time.Minute)
	env.get(startTarget(nil), "192.0.2.80")
	env.s.mu.Lock()
	left := len(env.s.attempts) + len(env.s.tickets)
	env.s.mu.Unlock()
	if left != 1 { // only the attempt started just now
		t.Fatalf("entries left = %d, want 1 (the new attempt)", left)
	}
	if rec := env.exchange(ticket, testVerifier, testIP); rec.Code != http.StatusUnauthorized {
		t.Fatalf("purged ticket = %d, want 401", rec.Code)
	}
}

// TestCodeCapHoldsTenPerIdentity: the eleventh exchange for one identity drops its oldest code.
func TestCodeCapHoldsTenPerIdentity(t *testing.T) {
	env := newEnv(t, devConfig())
	var first string
	for i := 0; i < 11; i++ {
		ip := fmt.Sprintf("192.0.2.%d", 100+i)
		rec := env.exchange(env.devSignIn(ip, "frank"), testVerifier, ip)
		if rec.Code != http.StatusOK {
			t.Fatalf("exchange %d = %d", i, rec.Code)
		}
		var out struct {
			UniqueCode string `json:"uniqueCode"`
			New        bool   `json:"new"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if i == 0 {
			first = out.UniqueCode
		}
	}
	if _, err := store.CodeLogin(first); err != store.ErrUnknownCode {
		t.Fatalf("the oldest code = %v, want it dropped", err)
	}
}

// TestDiscoveryDocument: the fields, the ports from the listen addresses, no cookie, no secrets.
func TestDiscoveryDocument(t *testing.T) {
	cfg := devConfig()
	cfg.ServerName = "Operator Name"
	cfg.HTTPS = "127.0.0.1:18443"
	env := newEnv(t, cfg)
	rec := env.get("/cherry/v1/info", testIP)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("discovery = %d headers %v", rec.Code, rec.Header())
	}
	const want = `{"contract":1,"name":"Operator Name","server":"cherry dev","auth":["dev-fake"],"ports":{"80":80,"443":18443,"10000":10000,"10123":10123}}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("discovery body\n got %s\nwant %s", got, want)
	}
	if lab := newEnv(t, ports()); lab.get("/cherry/v1/info", testIP).Code != http.StatusOK {
		t.Fatal("discovery is not served in lab-guest")
	}
}

// TestLabGuestHasNoSignIn: lab-guest registers no sign-in path, so each one is unknown.
func TestLabGuestHasNoSignIn(t *testing.T) {
	cfg := ports()
	cfg.AuthMode = config.AuthLabGuest
	env := newEnv(t, cfg)
	for _, target := range []string{"/auth/v1/discord/start", "/auth/v1/discord/callback", "/auth/v1/exchange"} {
		if rec := env.do(http.MethodPost, target, "{}", testIP); rec.Code != http.StatusNotFound {
			t.Errorf("%s in lab-guest = %d, want 404", target, rec.Code)
		}
	}
}

// TestSecretFileIsReadOnce: a missing or empty secret file stops New, and the error never shows content.
func TestSecretFileIsReadOnce(t *testing.T) {
	useAccountFile(t)
	cfg := discordConfig(t)
	cfg.DiscordSecretFile = filepath.Join(t.TempDir(), "absent")
	if _, err := New(cfg, nil); err == nil {
		t.Fatal("a missing secret file was accepted")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.DiscordSecretFile = empty
	if _, err := New(cfg, nil); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty secret file: err = %v", err)
	}
}

// TestLogsCarryNoSecrets: a full dev-fake sign-in writes no ticket, verifier, uniqueCode or dev code to the log, and the
// refusal lines name only the reason.
func TestLogsCarryNoSecrets(t *testing.T) {
	env := newEnv(t, devConfig())
	ticket := env.devSignIn(testIP, "gina")
	rec := env.exchange(ticket, testVerifier, testIP)
	var out struct {
		UniqueCode string `json:"uniqueCode"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	env.exchange(ticket, testVerifier+"x", testIP)
	logs := env.logs.String()
	for _, secret := range []string{ticket, testVerifier, out.UniqueCode, "dev:gina", "gina", testState} {
		if secret != "" && strings.Contains(logs, secret) {
			t.Fatalf("log carries %q: %s", secret, logs)
		}
	}
	if !strings.Contains(logs, "cherry: auth exchange ok") {
		t.Fatalf("log lacks the success line: %s", logs)
	}
}

// TestSignInAnswersAreNoStore: every sign-in answer, page or redirect, is no-store with no referrer.
func TestSignInAnswersAreNoStore(t *testing.T) {
	env := newEnv(t, devConfig())
	for _, rec := range []*httptest.ResponseRecorder{
		env.get(startTarget(url.Values{"as": {"x y"}}), testIP),
		env.get(startTarget(nil), testIP),
	} {
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("answer %d lacks the no-store or no-referrer headers: %v", rec.Code, rec.Header())
		}
	}
}

// TestAccountFileRoundTripKeepsIdentity: an identity written by exchange survives a reload of the account file.
func TestAccountFileRoundTripKeepsIdentity(t *testing.T) {
	env := newEnv(t, devConfig())
	env.exchange(env.devSignIn(testIP, "hana"), testVerifier, testIP)
	store.AccountsMu.Lock()
	path := store.AccountStorePath
	store.AccountsMu.Unlock()
	if err := store.LoadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	acc := store.IdentityAccountLocked("dev-hana")
	store.AccountsMu.Unlock()
	if acc == nil || len(acc.CodeHashes) != 1 {
		t.Fatalf("reloaded identity = %+v", acc)
	}
}
