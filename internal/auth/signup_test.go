package auth

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cherry/internal/config"
	"cherry/internal/store"
)

// freshAccounts empties the in-memory accounts before a test's database is attached: useAccountFile writes whatever is in
// memory into the new database, so an account another test left would otherwise be here.
func freshAccounts(t *testing.T) {
	t.Helper()
	t.Cleanup(store.ClearMemoryForTest())
}

// discordSignIn runs start and the callback for a fake Discord user id and returns the callback's query (ticket or error).
func discordSignIn(t *testing.T, env *testEnv, id string) url.Values {
	t.Helper()
	fake := httptest.NewServer(fakeDiscord(t, func(string) {}, id))
	t.Cleanup(fake.Close)
	oldBase := DiscordBase
	DiscordBase = fake.URL
	t.Cleanup(func() { DiscordBase = oldBase })
	rec := env.get(startTarget(nil), testIP)
	loc, _ := location(t, rec)
	u, _ := url.Parse(loc)
	rec = env.get("/auth/v1/discord/callback?code="+testGoodCode+"&state="+u.Query().Get("state"), testIP)
	_, q := location(t, rec)
	return q
}

// writeAllowlist writes the file and sets its modification time, so a change is seen whatever the file system's clock.
func writeAllowlist(t *testing.T, path, text string, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// TestAllowlistGatesTheFirstSignUp: an identity off the list gets error=denied at the callback (no ticket, no account) and
// one masked log line; a listed identity signs up and exchanges as before.
func TestAllowlistGatesTheFirstSignUp(t *testing.T) {
	freshAccounts(t)
	env := newEnv(t, discordConfig(t)) // the list holds discord:123456789012345678 only
	if q := discordSignIn(t, env, "223456789012345678"); q.Get("error") != "denied" || q.Get("ticket") != "" {
		t.Fatalf("an unlisted identity's sign-in = %v, want error=denied and no ticket", q)
	}
	if got := env.logs.String(); !strings.Contains(got, "sign-up refused identity=discord:…5678 at=callback") || strings.Contains(got, "223456789012345678") {
		t.Fatalf("refusal log = %q, want the masked identity and no full id", got)
	}
	store.AccountsMu.Lock()
	none := store.IdentityAccountLocked("discord:223456789012345678") == nil
	store.AccountsMu.Unlock()
	if !none {
		t.Fatal("a refused identity got an account")
	}
	q := discordSignIn(t, env, "123456789012345678")
	if q.Get("error") != "" || q.Get("ticket") == "" {
		t.Fatalf("a listed identity's sign-in = %v, want a ticket", q)
	}
	if rec := env.exchange(q.Get("ticket"), testVerifier, testIP); rec.Code != http.StatusOK {
		t.Fatalf("a listed identity's exchange = %d %s", rec.Code, rec.Body.String())
	}
}

// TestSignupClosedRefusesNewButSignsInKnown: with auth.signup closed an identity that has an account still signs in, a new
// one is refused at the callback, and an exchange for a new one (a ticket that got past the callback) is 403 signup_closed
// and creates nothing.
func TestSignupClosedRefusesNewButSignsInKnown(t *testing.T) {
	freshAccounts(t)
	env := newEnv(t, devConfig()) // open: erin makes her account
	if rec := env.exchange(env.devSignIn(testIP, "erin"), testVerifier, testIP); rec.Code != http.StatusOK {
		t.Fatalf("open exchange = %d", rec.Code)
	}
	closed := devConfig()
	closed.Signup = config.SignupClosed
	logs := &bytes.Buffer{}
	svc, err := New(closed, log.New(logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return env.now }
	mux := http.NewServeMux()
	svc.Register(mux)
	closedEnv := &testEnv{t: t, s: svc, mux: mux, now: env.now, logs: logs}

	if rec := closedEnv.exchange(closedEnv.devSignIn(testIP, "erin"), testVerifier, testIP); rec.Code != http.StatusOK {
		t.Fatalf("a known identity under closed = %d %s, want it to sign in", rec.Code, rec.Body.String())
	}
	start := closedEnv.get(startTarget(url.Values{"as": {"frank"}}), testIP)
	rec := closedEnv.get(start.Header().Get("Location"), testIP)
	if _, q := location(t, rec); q.Get("error") != "denied" || q.Get("ticket") != "" {
		t.Fatalf("a new identity under closed = %v, want error=denied", q)
	}

	// The exchange gate: a ticket for a new identity, as if the callback had let it through.
	s := closedEnv.s
	tok := randomToken(32)
	s.mu.Lock()
	s.tickets[tok] = &ticket{identity: "dev-gina", challenge: pkce(testVerifier), created: env.now}
	s.mu.Unlock()
	rec = closedEnv.exchange(tok, testVerifier, testIP)
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusForbidden || body.Error != "signup_closed" {
		t.Fatalf("exchange for a new identity under closed = %d %s, want 403 signup_closed", rec.Code, rec.Body.String())
	}
	store.AccountsMu.Lock()
	gone := store.IdentityAccountLocked("dev-gina") == nil
	store.AccountsMu.Unlock()
	if !gone {
		t.Fatal("a refused exchange created an account")
	}
	if !strings.Contains(logs.String(), "signup_closed identity=dev-gina") {
		t.Fatalf("exchange refusal log = %q", logs.String())
	}
}

// TestAllowlistReloadsWhenTheFileChanges: an edit (a new modification time) is read on the next check; a file that does not
// parse admits no new identity until it is fixed.
func TestAllowlistReloadsWhenTheFileChanges(t *testing.T) {
	freshAccounts(t)
	cfg := discordConfig(t)
	env := newEnv(t, cfg)
	base := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

	writeAllowlist(t, cfg.AllowlistFile, "discord:123456789012345678\ndiscord:223456789012345678\n", base.Add(time.Minute))
	if q := discordSignIn(t, env, "223456789012345678"); q.Get("ticket") == "" {
		t.Fatalf("an identity added by an edit = %v, want a ticket", q)
	}

	writeAllowlist(t, cfg.AllowlistFile, "discord:123456789012345678\nnot an id\n", base.Add(2*time.Minute))
	if q := discordSignIn(t, env, "323456789012345678"); q.Get("error") != "denied" {
		t.Fatalf("a new identity while the file does not parse = %v, want error=denied", q)
	}
	if !strings.Contains(env.logs.String(), "allowlist file: line 2 is not discord:<id> or a numeric id") {
		t.Fatalf("parse error not logged: %q", env.logs.String())
	}

	writeAllowlist(t, cfg.AllowlistFile, "# fixed\ndiscord:323456789012345678 # the same person\n", base.Add(3*time.Minute))
	if q := discordSignIn(t, env, "323456789012345678"); q.Get("ticket") == "" {
		t.Fatalf("the fixed file = %v, want a ticket", q)
	}
}

// TestAllowlistParsing: comments, bare and prefixed ids, blank lines and CRLF are read; a bad line is named by number only.
func TestAllowlistParsing(t *testing.T) {
	ids, err := parseAllowlist([]byte("# header\r\n\r\n123\r\ndiscord:456 # trailing\r\n  discord:789  \r\n"))
	if err != nil || len(ids) != 3 || !ids["discord:123"] || !ids["discord:456"] || !ids["discord:789"] {
		t.Fatalf("parsed %v, %v", ids, err)
	}
	for _, bad := range []string{"discord:", "discord:12a", "dev-alice", "discord:discord:1", strings.Repeat("9", 21)} {
		if _, err := parseAllowlist([]byte("1\n" + bad + "\n")); err == nil || err.Error() != "line 2 is not discord:<id> or a numeric id" {
			t.Errorf("%q: err = %v, want the line-2 error alone (the line is never echoed)", bad, err)
		}
	}
	if ids, err := parseAllowlist(nil); err != nil || len(ids) != 0 {
		t.Fatalf("an empty file = %v, %v; want no identities and no error", ids, err)
	}
}

// TestAllowlistMustLoadAtStart: a missing file refuses the start; the discord default needs one.
func TestAllowlistMustLoadAtStart(t *testing.T) {
	cfg := discordConfig(t)
	cfg.AllowlistFile = filepath.Join(t.TempDir(), "absent.txt")
	if _, err := New(cfg, nil); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("a missing allowlist file: %v", err)
	}
	cfg.AllowlistFile = ""
	if _, err := New(cfg, nil); err == nil {
		t.Fatal("an allowlist without a file was accepted")
	}
}
