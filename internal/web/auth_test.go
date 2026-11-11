package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"cherry/internal/auth"
	"cherry/internal/config"
	"cherry/internal/httpx"
	"cherry/internal/store"
)

// useConfig activates cfg for one test (the guest handler reads the mode from it).
func useConfig(t *testing.T, cfg config.Config) {
	t.Helper()
	config.Activate(cfg)
	t.Cleanup(func() { config.Activate(config.Config{}) })
}

func devFakeConfig() config.Config {
	return config.Config{AuthMode: config.AuthDevFake, HTTP: ":80", HTTPS: ":443", Gateway: ":10000", Session: ":10123", ServerName: "Test"}
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// TestDevFakeRoundTripThroughRealMux: start → callback → exchange → guest/generate gives the minted identity's account
// token; a second guest/generate with the same code gives the same account; the request log shows no secret.
func TestDevFakeRoundTripThroughRealMux(t *testing.T) {
	cfg := devFakeConfig()
	useConfig(t, cfg)
	guestAccountTest(t)
	var logs bytes.Buffer
	reqLog := log.New(&logs, "", 0)
	svc, err := auth.New(cfg, reqLog)
	if err != nil {
		t.Fatal(err)
	}
	game := httpx.WithLogging(NewGameMux(svc), reqLog)
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		game.ServeHTTP(rec, req)
		return rec
	}

	const verifier = "web-verifier-0123456789-abcdefghijklmnopqrstuvwxyz"
	const state = "webstate0123456789ab"
	start := serve(httptest.NewRequest(http.MethodGet,
		"/auth/v1/discord/start?challenge="+pkceChallenge(verifier)+"&return=raspberry%3A%2F%2Fauth&state="+state+"&as=alice", nil))
	callback := start.Header().Get("Location")
	if start.Code != http.StatusFound || !strings.HasPrefix(callback, "/auth/v1/discord/callback?") {
		t.Fatalf("start = %d %q", start.Code, callback)
	}
	done := serve(httptest.NewRequest(http.MethodGet, callback, nil))
	ret, err := url.Parse(done.Header().Get("Location"))
	if err != nil || done.Code != http.StatusFound || ret.Scheme != "raspberry" || ret.Query().Get("state") != state {
		t.Fatalf("callback = %d %q", done.Code, done.Header().Get("Location"))
	}
	ticket := ret.Query().Get("ticket")

	body, _ := json.Marshal(map[string]string{"ticket": ticket, "verifier": verifier})
	ex := serve(httptest.NewRequest(http.MethodPost, "/auth/v1/exchange", bytes.NewReader(body)))
	var minted struct {
		UniqueCode string `json:"uniqueCode"`
		Aid        string `json:"aid"`
		New        bool   `json:"new"`
	}
	if ex.Code != http.StatusOK || json.Unmarshal(ex.Body.Bytes(), &minted) != nil || minted.Aid != "0" || !minted.New {
		t.Fatalf("exchange = %d %s", ex.Code, ex.Body.String())
	}

	token := guestGenerateCode(t, minted.UniqueCode)
	first := sessionOf(t, token)
	if first.Aid != "0" {
		t.Fatalf("new identity's session aid = %s, want 0", first.Aid)
	}
	created := createAvatar(t, avAuthValue(t, createSession(t, token)), []byte(`{"name":"Alice","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON00164"]}`), "")
	var avatar avatarResponse
	if err := json.Unmarshal(created.Body.Bytes(), &avatar); err != nil || avatar.Result == nil || avatar.Result.AvatarID == "0" {
		t.Fatalf("create avatar = %d %s", created.Code, created.Body.String())
	}

	again := guestGenerateCode(t, minted.UniqueCode)
	if got := sessionOf(t, again); got.Aid != avatar.Result.AvatarID || got.SessionKey != first.SessionKey {
		t.Fatalf("second guest/generate gave aid %s, want the same account %s", got.Aid, avatar.Result.AvatarID)
	}

	secrets := []string{ticket, verifier, minted.UniqueCode, "dev:alice", state}
	for _, s := range secrets {
		if strings.Contains(logs.String(), s) {
			t.Fatalf("request log carries %q: %s", s, logs.String())
		}
	}
	if !strings.Contains(logs.String(), `url="/auth/v1/discord/callback?[redacted]"`) {
		t.Fatalf("callback URL not redacted in the log: %s", logs.String())
	}
	t.Logf("sample request log line: %s", strings.SplitN(logs.String(), "\n", 2)[0])
}

// TestDevFakeRefusesCodesNobodyMinted: a device code (or any code the exchange did not mint) gets the same 400 as a bad
// header, and creates no account; the header check still applies.
func TestDevFakeRefusesCodesNobodyMinted(t *testing.T) {
	useConfig(t, devFakeConfig())
	guestAccountTest(t)
	const code = "00000000-0000-4000-8000-0000000000aa"
	wrong := guestRequest(code)
	wrong.Header.Set("X-LINEPLAY-ACNT", guestRequest(deviceB).Header.Get("X-LINEPLAY-ACNT"))
	for name, req := range map[string]*http.Request{"unminted code": guestRequest(code), "wrong header": wrong} {
		rec := serveRequest(t, req)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != httpx.BadRequestBody {
			t.Errorf("%s: %d %s, want the 400 of a bad header", name, rec.Code, rec.Body.String())
		}
	}
	store.AccountsMu.Lock()
	n := store.AccountCountLocked()
	store.AccountsMu.Unlock()
	if n != 0 {
		t.Fatalf("refused guest requests created %d accounts", n)
	}
}

// TestLabGuestKeepsDeviceGuests: lab-guest is unchanged: a device code creates or finds its account, no minting needed.
func TestLabGuestKeepsDeviceGuests(t *testing.T) {
	cfg := devFakeConfig()
	cfg.AuthMode = config.AuthLabGuest
	useConfig(t, cfg)
	guestAccountTest(t)
	token := guestGenerateCode(t, deviceA)
	again := guestGenerateCode(t, deviceA) // a rebind mints an alias (the own token is not kept in memory after a reload)
	store.AccountsMu.Lock()
	same := store.AccountByTokenLocked(token) != nil && store.AccountByTokenLocked(token) == store.AccountByTokenLocked(again)
	store.AccountsMu.Unlock()
	if !same {
		t.Fatal("lab-guest rebind: the new token does not resolve to the device's account")
	}
}

// TestSignInIsUnknownInLabGuest: in lab-guest the sign-in paths answer 404 on the game mux; discovery still answers.
func TestSignInIsUnknownInLabGuest(t *testing.T) {
	cfg := devFakeConfig()
	cfg.AuthMode = config.AuthLabGuest
	useConfig(t, cfg)
	svc, err := auth.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	game := NewGameMux(svc)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/auth/v1/discord/start?challenge=a&return=b&state=c", nil),
		httptest.NewRequest(http.MethodGet, "/auth/v1/discord/callback?code=dev:x&state=y", nil),
		httptest.NewRequest(http.MethodPost, "/auth/v1/exchange", strings.NewReader("{}")),
	} {
		rec := httptest.NewRecorder()
		game.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s in lab-guest = %d, want 404", req.Method, req.URL.Path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	game.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cherry/v1/info", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"auth":["lab-guest"]`) {
		t.Fatalf("discovery in lab-guest = %d %s", rec.Code, rec.Body.String())
	}
}

// TestPlainHTTPHasNoSignIn: the plain HTTP mux serves neither the sign-in routes nor discovery.
func TestPlainHTTPHasNoSignIn(t *testing.T) {
	useConfig(t, devFakeConfig())
	plain := NewMux()
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/auth/v1/exchange", strings.NewReader("{}")),
		httptest.NewRequest(http.MethodGet, "/cherry/v1/info", nil),
	} {
		rec := httptest.NewRecorder()
		plain.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s on plain HTTP = %d, want 404", req.Method, req.URL.Path, rec.Code)
		}
	}
}
