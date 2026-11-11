// Package auth is Cherry's player sign-in for the discord and dev-fake modes (CONTRACT §3, §4, §9): the start, callback
// and exchange endpoints and the discovery document. Attempts, tickets and rate limits live in memory and die with the
// process; the identity and the hashes of minted codes live on the account (store/identity.go).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"cherry/internal/config"
	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Version is the server version in discovery. A build may set it; "dev" otherwise.
var Version = "dev"

// Limits and lifetimes (CONTRACT §3 starting values).
// ponytail: state is per process; a second process needs shared storage (slice 8.3b). Caps bound the memory.
const (
	attemptTTL        = 10 * time.Minute
	ticketTTL         = 120 * time.Second
	maxAttempts       = 4096 // open sign-ins, all clients
	maxAttemptsPerIP  = 20   // open sign-ins, one client address
	maxTickets        = 4096
	startPerMinute    = 10
	startPerHour      = 60
	exchangePerMinute = 20
	infoPerMinute     = 30
	maxLimiterKeys    = 65536 // client addresses tracked per limiter; a new one beyond this is refused
	maxExchangeBody   = 4 << 10
	maxCodeLen        = 512 // the callback's code parameter
)

// The error codes a failed sign-in returns to the app (CONTRACT §3).
const (
	codeDenied  = "denied"
	codeExpired = "expired"
	codeServer  = "server"
)

// Service holds the sign-in state of one server. Build it once with New and mount it with Register.
type Service struct {
	// Now is the clock. Tests replace it; production leaves time.Now.
	Now func() time.Time

	mode, clientID, redirect, secret string
	info                             []byte // the discovery body, built once
	logger                           *log.Logger

	mu       sync.Mutex
	attempts map[string]*attempt // by server state
	tickets  map[string]*ticket  // by ticket

	startMinute, startHour, exchangeMinute, infoMinute *limiter
}

// attempt is one open sign-in: the client's challenge, its return address and state, and where it came from.
type attempt struct {
	challenge, ret, state, ip string
	created                   time.Time
}

// ticket is a sign-in the provider accepted, waiting for its verifier. It keeps the challenge it was bound to.
type ticket struct {
	identity, challenge string
	created             time.Time
}

// New checks what cfg needs for the mode and returns the service. The Discord secret is read here, once; a missing or
// empty file is an error. logger gets one line per refused request and never a value; nil means log.Default.
func New(cfg config.Config, logger *log.Logger) (*Service, error) {
	if logger == nil {
		logger = log.Default()
	}
	s := &Service{
		Now: time.Now, mode: cfg.AuthMode, logger: logger,
		attempts:       make(map[string]*attempt),
		tickets:        make(map[string]*ticket),
		startMinute:    newLimiter(startPerMinute, time.Minute),
		startHour:      newLimiter(startPerHour, time.Hour),
		exchangeMinute: newLimiter(exchangePerMinute, time.Minute),
		infoMinute:     newLimiter(infoPerMinute, time.Minute),
	}
	if cfg.AuthMode == config.AuthDiscord {
		if cfg.DiscordClientID == "" || cfg.DiscordRedirect == "" {
			return nil, errors.New("auth: discord mode needs a client id and a redirect (config checks these at start)")
		}
		secret, err := readSecret(cfg.DiscordSecretFile)
		if err != nil {
			return nil, err
		}
		s.secret, s.clientID, s.redirect = secret, cfg.DiscordClientID, cfg.DiscordRedirect
	}
	info, err := discoveryBody(cfg)
	if err != nil {
		return nil, err
	}
	s.info = info
	return s, nil
}

// readSecret returns the client secret from its file. The error names the file, never its content.
func readSecret(path string) (string, error) {
	if path == "" {
		return "", errors.New("auth: discord secret file is not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("auth: discord secret: %w", err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", errors.New("auth: discord secret file is empty")
	}
	return secret, nil
}

// portMap is the discovery ports: the client's destination port → the server's listen port (CONTRACT §1, §4).
type portMap struct {
	P80    int `json:"80"`
	P443   int `json:"443"`
	P10000 int `json:"10000"`
	P10123 int `json:"10123"`
}

type discovery struct {
	Contract int      `json:"contract"`
	Name     string   `json:"name"`
	Server   string   `json:"server"`
	Auth     []string `json:"auth"`
	Ports    portMap  `json:"ports"`
}

// discoveryBody builds GET /cherry/v1/info. auth lists the active mode; lab-guest and dev-fake cannot start on a public
// server (config), so the list never names them there.
func discoveryBody(cfg config.Config) ([]byte, error) {
	var ports portMap
	for _, p := range []struct {
		addr string
		dst  *int
	}{{cfg.HTTP, &ports.P80}, {cfg.HTTPS, &ports.P443}, {cfg.Gateway, &ports.P10000}, {cfg.Session, &ports.P10123}} {
		n, err := listenPort(p.addr)
		if err != nil {
			return nil, err
		}
		*p.dst = n
	}
	return json.Marshal(discovery{Contract: 1, Name: cfg.ServerName, Server: "cherry " + Version, Auth: []string{cfg.AuthMode}, Ports: ports})
}

func listenPort(addr string) (int, error) {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("auth: listen address %q: %w", addr, err)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("auth: listen address %q has no usable port", addr)
	}
	return n, nil
}

// Register mounts the discovery route in every mode and, in discord and dev-fake, the three sign-in routes. In lab-guest
// the sign-in paths are not registered, so they answer as unknown routes. Every response here is no-store and carries no
// referrer.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("/cherry/v1/info", s.headers(s.handleInfo))
	if s.mode == config.AuthDiscord || s.mode == config.AuthDevFake {
		mux.HandleFunc("/auth/v1/discord/start", s.headers(s.handleStart))
		mux.HandleFunc("/auth/v1/discord/callback", s.headers(s.handleCallback))
		mux.HandleFunc("/auth/v1/exchange", s.headers(s.handleExchange))
	}
}

func (s *Service) headers(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		next(w, r)
	}
}

// page writes a fixed HTML page with a strict policy and no script. No request data reaches it.
func page(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>Cherry</title></head><body><p>"+text+"</p></body></html>")
}

// redirect sends the client to loc. Every loc here is built from a fixed origin or a validated return address.
func redirect(w http.ResponseWriter, loc string) {
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusFound)
}

// clientIP is the connection's address. Forwarded headers are not trusted: nothing sits in front of the game port.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

const (
	textRateLimited = "Too many sign-in attempts from this network. Wait a minute and try again."
	textBadLink     = "The sign-in link is not valid. Go back to the app and try again."
	textUnknown     = "This sign-in has already been used or has expired. Go back to the app and try again."
	textMethod      = "This address takes GET."
)

// handleStart: GET /auth/v1/discord/start?challenge=&return=&state= (CONTRACT §3). Only a well-formed request gets a
// redirect; everything else is a 400 page.
func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		page(w, http.StatusMethodNotAllowed, textMethod)
		return
	}
	ip := clientIP(r)
	now := s.Now()
	if !s.startMinute.allow(ip, now) || !s.startHour.allow(ip, now) {
		s.logger.Printf("cherry: auth start rate limited")
		page(w, http.StatusTooManyRequests, textRateLimited)
		return
	}
	dev := s.mode == config.AuthDevFake
	q := r.URL.Query()
	optional := []string{}
	if dev {
		optional = append(optional, "as")
	}
	if !exactly(q, []string{"challenge", "return", "state"}, optional) {
		s.logger.Printf("cherry: auth start refused")
		page(w, http.StatusBadRequest, textBadLink)
		return
	}
	challenge, ret, state := q.Get("challenge"), q.Get("return"), q.Get("state")
	as := "dev"
	if q.Has("as") {
		as = q.Get("as")
	}
	if !validChallenge(challenge) || !validState(state) || !validReturn(ret) || (dev && !validDevName(as)) {
		s.logger.Printf("cherry: auth start refused")
		page(w, http.StatusBadRequest, textBadLink)
		return
	}

	s.mu.Lock()
	s.purgeLocked(now)
	if len(s.attempts) >= maxAttempts || s.openFor(ip) >= maxAttemptsPerIP {
		s.mu.Unlock()
		s.logger.Printf("cherry: auth start refused: open sign-in cap")
		page(w, http.StatusTooManyRequests, textRateLimited)
		return
	}
	serverState := randomToken(32)
	s.attempts[serverState] = &attempt{challenge: challenge, ret: ret, state: state, ip: ip, created: now}
	s.mu.Unlock()

	if dev { // CONTRACT §9 spells the callback literally; as and the server state need no escaping
		redirect(w, "/auth/v1/discord/callback?code=dev:"+as+"&state="+serverState)
		return
	}
	redirect(w, authorizeURL(s.clientID, s.redirect, serverState))
}

// exactly reports whether q holds each required key once and nothing but the required and optional keys.
func exactly(q url.Values, required, optional []string) bool {
	for k, v := range q {
		if len(v) != 1 || !slices.Contains(required, k) && !slices.Contains(optional, k) {
			return false
		}
	}
	for _, k := range required {
		if len(q[k]) != 1 {
			return false
		}
	}
	return true
}

// openFor counts the open attempts of one client address. Caller holds s.mu.
func (s *Service) openFor(ip string) int {
	n := 0
	for _, a := range s.attempts {
		if a.ip == ip {
			n++
		}
	}
	return n
}

// purgeLocked drops expired attempts and tickets. Caller holds s.mu. The maps are capped, so the scan stays small.
// ponytail: a full scan on each request at the caps; a timer or a heap if the caps grow.
func (s *Service) purgeLocked(now time.Time) {
	for k, a := range s.attempts {
		if now.Sub(a.created) > attemptTTL {
			delete(s.attempts, k)
		}
	}
	for k, t := range s.tickets {
		if now.Sub(t.created) > ticketTTL {
			delete(s.tickets, k)
		}
	}
}

// handleCallback: GET /auth/v1/discord/callback?code=&state= (Discord → Cherry). The attempt is consumed on arrival, so
// a second visit is unknown. Failures go back to the app's return address when the attempt is known.
func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		page(w, http.StatusMethodNotAllowed, textMethod)
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if len(q["state"]) != 1 || !validState(state) {
		s.logger.Printf("cherry: auth callback refused")
		page(w, http.StatusBadRequest, textUnknown)
		return
	}
	s.mu.Lock()
	a := s.attempts[state]
	delete(s.attempts, state)
	s.mu.Unlock()
	if a == nil {
		s.logger.Printf("cherry: auth callback: unknown or used state")
		page(w, http.StatusBadRequest, textUnknown)
		return
	}
	now := s.Now()
	if now.Sub(a.created) > attemptTTL {
		s.fail(w, a, codeExpired)
		return
	}
	if q.Has("error") {
		s.fail(w, a, codeDenied) // the user refused Discord's consent screen
		return
	}
	identity, failure := s.identify(q.Get("code"))
	if failure != "" {
		s.fail(w, a, failure)
		return
	}
	s.mu.Lock()
	s.purgeLocked(now)
	if len(s.tickets) >= maxTickets {
		s.mu.Unlock()
		s.fail(w, a, codeServer)
		return
	}
	tok := randomToken(32)
	s.tickets[tok] = &ticket{identity: identity, challenge: a.challenge, created: now}
	s.mu.Unlock()
	redirect(w, a.ret+"?ticket="+url.QueryEscape(tok)+"&state="+url.QueryEscape(a.state))
}

// fail sends a sign-in error to the app. Only the code travels; the log names the code too.
func (s *Service) fail(w http.ResponseWriter, a *attempt, code string) {
	s.logger.Printf("cherry: auth sign-in failed reason=%s", code)
	redirect(w, a.ret+"?error="+code+"&state="+url.QueryEscape(a.state))
}

// identify turns the callback's code into the provider identity, or the error code to return.
func (s *Service) identify(code string) (identity, failure string) {
	if code == "" || len(code) > maxCodeLen {
		return "", codeDenied
	}
	if s.mode == config.AuthDevFake {
		name, ok := strings.CutPrefix(code, "dev:")
		if !ok || !validDevName(name) {
			return "", codeDenied
		}
		return "dev-" + name, ""
	}
	if strings.HasPrefix(code, "dev:") { // the test provider is off in discord mode
		return "", codeDenied
	}
	id, failure := s.discordUserID(code)
	if failure != "" {
		return "", failure
	}
	return "discord:" + id, ""
}

// handleExchange: POST /auth/v1/exchange {"ticket","verifier"} (CONTRACT §3). The ticket is taken out of the table before
// the verifier is checked, so a wrong verifier burns it too.
func (s *Service) handleExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteJSON(w, http.StatusMethodNotAllowed, `{"error":"invalid_request"}`)
		return
	}
	now := s.Now()
	if !s.exchangeMinute.allow(clientIP(r), now) {
		s.logger.Printf("cherry: auth exchange refused reason=rate_limited")
		httpx.WriteJSON(w, http.StatusTooManyRequests, `{"error":"rate_limited"}`)
		return
	}
	var body struct {
		Ticket   string `json:"ticket"`
		Verifier string `json:"verifier"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.Decode(&struct{}{}) != io.EOF || !validTicket(body.Ticket) || !validVerifier(body.Verifier) {
		s.logger.Printf("cherry: auth exchange refused reason=invalid_request")
		httpx.WriteJSON(w, http.StatusBadRequest, `{"error":"invalid_request"}`)
		return
	}

	s.mu.Lock()
	s.purgeLocked(now)
	tk := s.tickets[body.Ticket]
	delete(s.tickets, body.Ticket)
	s.mu.Unlock()
	if tk == nil || now.Sub(tk.created) > ticketTTL {
		s.logger.Printf("cherry: auth exchange refused reason=invalid_ticket")
		httpx.WriteJSON(w, http.StatusUnauthorized, `{"error":"invalid_ticket"}`)
		return
	}
	sum := sha256.Sum256([]byte(body.Verifier))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(tk.challenge)) != 1 {
		s.logger.Printf("cherry: auth exchange refused reason=invalid_ticket")
		httpx.WriteJSON(w, http.StatusUnauthorized, `{"error":"invalid_ticket"}`)
		return
	}

	code := newUUID()
	aid, isNew, err := store.MintIdentityCode(tk.identity, store.CodeHash(code))
	if err != nil {
		s.logger.Printf("cherry: auth exchange: save failed: %v", err)
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"error":"server"}`)
		return
	}
	s.logger.Printf("cherry: auth exchange ok aid=%s new=%t", aid, isNew)
	out, _ := json.Marshal(struct {
		UniqueCode string `json:"uniqueCode"`
		Aid        string `json:"aid"`
		New        bool   `json:"new"`
	}{code, aid, isNew})
	httpx.WriteJSON(w, http.StatusOK, string(out))
}

// handleInfo: GET /cherry/v1/info, the discovery document. Rate limited per address; no cookie, no secret.
func (s *Service) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpx.WriteJSON(w, http.StatusMethodNotAllowed, `{"error":"invalid_request"}`)
		return
	}
	if !s.infoMinute.allow(clientIP(r), s.Now()) {
		httpx.WriteJSON(w, http.StatusTooManyRequests, `{"error":"rate_limited"}`)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(s.info))
}

// authorizeURL is Discord's authorize page with the contract's parameters, in the contract's order. prompt is left out.
func authorizeURL(clientID, redirectURI, state string) string {
	return DiscordBase + "/oauth2/authorize?response_type=code&client_id=" + url.QueryEscape(clientID) +
		"&scope=identify&redirect_uri=" + url.QueryEscape(redirectURI) + "&state=" + url.QueryEscape(state)
}

// randomToken returns n CSPRNG bytes as base64url without padding.
func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// newUUID is a version 4 UUID from the CSPRNG: the shape of a device uniqueCode (store.ValidGuestCode accepts it).
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// validChallenge: 43 characters that decode, strictly, to a SHA-256 digest (base64url, no padding).
func validChallenge(c string) bool {
	if len(c) != 43 {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(c)
	return err == nil && len(b) == sha256.Size
}

// validState: 16 to 64 base64url characters.
func validState(s string) bool {
	return len(s) >= 16 && len(s) <= 64 && onlyChars(s, "-_")
}

// validTicket: the 43-character form randomToken gives.
func validTicket(t string) bool { return len(t) == 43 && onlyChars(t, "-_") }

// validVerifier: RFC 7636's 43 to 128 unreserved characters.
func validVerifier(v string) bool { return len(v) >= 43 && len(v) <= 128 && onlyChars(v, "-._~") }

// validDevName: a short name for the test provider's identity dev-<name>.
func validDevName(n string) bool { return len(n) >= 1 && len(n) <= 24 && onlyChars(n, "-_") }

// onlyChars reports whether s is letters and digits plus the extra characters.
func onlyChars(s, extra string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.IndexByte(extra, c) >= 0) {
			return false
		}
	}
	return true
}

// validReturn: exactly raspberry://auth, or http://127.0.0.1:<1024..65535>/raspberry/cb with a canonical port.
func validReturn(ret string) bool {
	if ret == "raspberry://auth" {
		return true
	}
	rest, ok := strings.CutPrefix(ret, "http://127.0.0.1:")
	if !ok {
		return false
	}
	port, ok := strings.CutSuffix(rest, "/raspberry/cb")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && strconv.Itoa(n) == port && n >= 1024 && n <= 65535
}

// limiter allows limit hits per key in any window of the given length (a sliding log). Keys are capped: a new key over
// the cap is refused until old keys age out.
type limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{limit: limit, window: window, hits: make(map[string][]time.Time)}
}

// allow records a hit for key at now and reports whether it is within the limit.
func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	old := l.hits[key]
	i := 0
	for i < len(old) && !old[i].After(cutoff) {
		i++
	}
	hits := old[i:]
	if len(hits) == 0 && len(l.hits) >= maxLimiterKeys {
		l.sweep(cutoff)
		if len(l.hits) >= maxLimiterKeys {
			return false
		}
	}
	if len(hits) >= l.limit {
		l.hits[key] = hits
		return false
	}
	l.hits[key] = append(hits, now)
	return true
}

// sweep forgets the keys whose hits have all aged out. Caller holds l.mu.
func (l *limiter) sweep(cutoff time.Time) {
	for key, hits := range l.hits {
		if len(hits) == 0 || !hits[len(hits)-1].After(cutoff) {
			delete(l.hits, key)
		}
	}
}
