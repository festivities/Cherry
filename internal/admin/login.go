package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"cherry/internal/auth"
	"cherry/internal/config"
	"cherry/internal/store"
)

// The sign-in (PLAN P4): GET /auth/login starts it with a pre-login cookie and a server-side state bound to that cookie;
// GET /auth/callback finishes it. The state is single use and expires; the pre-login cookie is single use too. A person is
// admitted when their identity is the owner or on the staff allowlist AND Discord reports two-factor (mfa_enabled). Refused
// sign-ins say "Not authorised." and nothing else; the reason goes to the log and, once the state was found, to the audit
// trail with the Discord id.
//
// ponytail: the sign-in state and the blocks are in memory (see admin.go).

const (
	loginTTL       = 10 * time.Minute
	maxLogins      = 256
	maxLoginsPerIP = 20 // open sign-ins of one address: a memory bound; the per-minute limit governs the rate
	blockFor       = 15 * time.Minute
	maxCodeLen     = 512
	auditPage      = 200
	loginURL       = "/auth/login"
)

// loginState is one sign-in in progress: the SHA-256 of its pre-login cookie value, the address it started from, the page to
// open after it and when it started. The map is keyed by the SHA-256 of the state.
type loginState struct {
	nonce   [32]byte
	ip      string
	next    string
	created time.Time
}

// Texts of the sign-in pages. None of them says which check failed.
const (
	textBlocked     = "Too many attempts from this network. Wait a few minutes and try again."
	textRefused     = "Not authorised."
	textBadRequest  = "This sign-in link is not valid."
	textUnavailable = "Sign-in is not available right now. Try again later."
)

// handleLogin: GET /auth/login[?next=<path>][&as=<name>, dev-fake only]. It sets the pre-login cookie and the state, then
// sends the browser to Discord (or, in dev-fake, straight to the callback with the test code).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip, now := clientIP(r), s.now()
	if s.blockedNow(ip, now) {
		s.logger.Printf("cherry: staff sign-in blocked addr=%s", ip)
		s.fail(w, http.StatusTooManyRequests, textBlocked)
		return
	}
	if !s.limits.loginMinute.Allow(ip, now) || !s.limits.loginHour.Allow(ip, now) {
		s.logger.Printf("cherry: staff sign-in rate limited addr=%s", ip)
		s.fail(w, http.StatusTooManyRequests, textBlocked)
		return
	}
	dev := s.cfg.AuthMode == config.AuthDevFake
	q := r.URL.Query()
	if !onlyKeys(q, dev) {
		s.fail(w, http.StatusBadRequest, textBadRequest)
		return
	}
	next := "/"
	if q.Has("next") {
		if !validNext(q.Get("next")) {
			s.fail(w, http.StatusBadRequest, textBadRequest)
			return
		}
		next = q.Get("next")
	}
	as := "dev"
	if q.Has("as") {
		if !config.ValidDevName(q.Get("as")) {
			s.fail(w, http.StatusBadRequest, textBadRequest)
			return
		}
		as = q.Get("as")
	}
	nonce, state := randomToken(32), randomToken(32)
	s.mu.Lock()
	s.purgeLoginsLocked(now)
	// A full table drops its oldest sign-in rather than refusing a new one: refusing would let a few dozen requests from
	// different addresses lock every staff member out for the TTL.
	if len(s.logins) >= maxLogins {
		s.evictOldestLoginLocked()
	}
	if s.loginsFromLocked(ip) >= maxLoginsPerIP {
		s.mu.Unlock()
		s.logger.Printf("cherry: staff sign-in refused: open sign-in cap addr=%s", ip)
		s.fail(w, http.StatusTooManyRequests, textBlocked)
		return
	}
	s.logins[sha256.Sum256([]byte(state))] = &loginState{nonce: sha256.Sum256([]byte(nonce)), ip: ip, next: next, created: now}
	s.mu.Unlock()

	// Lax, so the cookie comes back on Discord's top-level redirect to the callback.
	setCookie(w, cookiePrelogin, nonce, preloginMaxAge, http.SameSiteLaxMode)
	if dev {
		redirect(w, http.StatusFound, "/auth/callback?code=dev:"+as+"&state="+state)
		return
	}
	redirect(w, http.StatusFound, auth.AuthorizeURL(s.app.ClientID, s.app.Redirect, state, true))
}

// handleCallback: GET /auth/callback?code=&state= (Discord's redirect). Every outcome clears the pre-login cookie.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	ip, now := clientIP(r), s.now()
	clearCookie(w, cookiePrelogin, http.SameSiteLaxMode)
	if s.blockedNow(ip, now) {
		s.logger.Printf("cherry: staff sign-in blocked addr=%s", ip)
		s.fail(w, http.StatusTooManyRequests, textBlocked)
		return
	}
	if !s.limits.callbackMinute.Allow(ip, now) {
		s.logger.Printf("cherry: staff callback rate limited addr=%s", ip)
		s.fail(w, http.StatusTooManyRequests, textBlocked)
		return
	}
	q := r.URL.Query()
	if len(q["state"]) != 1 {
		s.refuse(w, ip, "", "state", false)
		return
	}
	ls := s.takeLogin(q.Get("state"))
	if ls == nil {
		s.refuse(w, ip, "", "unknown-state", false)
		return
	}
	// The state is found and consumed: from here on a refusal is audited.
	nonce, err := r.Cookie(cookiePrelogin)
	if err != nil || !validToken(nonce.Value) {
		s.refuse(w, ip, "", "no-prelogin", true)
		return
	}
	got := sha256.Sum256([]byte(nonce.Value))
	if subtle.ConstantTimeCompare(got[:], ls.nonce[:]) != 1 {
		s.refuse(w, ip, "", "prelogin-mismatch", true)
		return
	}
	if now.Sub(ls.created) >= loginTTL {
		s.refuse(w, ip, "", "expired", true)
		return
	}
	if ls.ip != ip {
		s.refuse(w, ip, "", "address-changed", true)
		return
	}
	if q.Has("error") {
		s.refuse(w, ip, "", "denied", true)
		return
	}
	identity, name, mfa, reason := s.identify(q.Get("code"))
	if reason != "" {
		s.refuse(w, ip, identity, reason, true)
		return
	}
	if !s.mayStaff(identity) {
		s.refuse(w, ip, identity, "not-staff", true)
		return
	}
	if !mfa {
		s.refuse(w, ip, identity, "mfa-off", true)
		return
	}

	// Admitted. The session is made first and the audit row must be written before the cookie is set: a sign-in that cannot
	// be recorded does not happen. The request's old session, if any, ends (rotation).
	id, ok := s.openSession(identity, name, ip, now)
	if !ok {
		s.logger.Printf("cherry: staff sign-in refused: session cap")
		s.fail(w, http.StatusServiceUnavailable, textUnavailable)
		return
	}
	if err := store.Audit(identity, "login", "staff", map[string]any{"ip": ip}); err != nil {
		s.dropSession(id)
		s.logger.Printf("cherry: staff sign-in not recorded: %v", err)
		s.fail(w, http.StatusServiceUnavailable, textUnavailable)
		return
	}
	s.endSession(r)
	setCookie(w, cookieSession, id, sessionMaxAge, http.SameSiteStrictMode)
	s.logger.Printf("cherry: staff signed in identity=%s", auth.MaskIdentity(identity))
	loc := ls.next
	if !validNext(loc) {
		loc = "/"
	}
	// A page that moves on by itself, not a 302. A redirect here would keep the navigation chain on Discord's site, and a
	// browser may then withhold the SameSite=Strict session cookie from the page it lands on; the refresh is this page's own
	// same-site navigation.
	s.render(w, http.StatusOK, "signedin", view{Title: "Signed in", Next: loc})
}

// identify turns the callback's code into the identity, its display name and whether two-factor is on. reason is "" or the
// refusal. dev-fake takes dev:<name> (the test provider, and mfa is assumed); discord mode asks Discord.
func (s *Server) identify(code string) (identity, name string, mfa bool, reason string) {
	if code == "" || len(code) > maxCodeLen {
		return "", "", false, "bad-code"
	}
	if s.cfg.AuthMode == config.AuthDevFake {
		dn, ok := strings.CutPrefix(code, "dev:")
		if !ok || !config.ValidDevName(dn) {
			return "", "", false, "bad-code"
		}
		return "dev-" + dn, "dev-" + dn, true, ""
	}
	if strings.HasPrefix(code, "dev:") {
		return "", "", false, "bad-code"
	}
	u, failure := s.app.Identify(code, s.logger)
	if failure != "" {
		return "", "", false, failure
	}
	display := u.GlobalName
	if display == "" {
		display = u.Username
	}
	return "discord:" + u.ID, cleanName(display), u.MFAEnabled, ""
}

// mayStaff: the owner, or an identity on the staff allowlist.
func (s *Server) mayStaff(identity string) bool {
	return identity != "" && (identity == s.owner || s.allow.Has(identity))
}

// cleanName is a display name a page may show: no control characters, at most 64 characters.
func cleanName(n string) string {
	var out []rune
	for _, r := range n {
		if unicode.IsControl(r) || len(out) >= 64 {
			continue
		}
		out = append(out, r)
	}
	return strings.TrimSpace(string(out))
}

// refuse answers a refused sign-in with the generic page, logs the reason, counts it toward the address's block and, when
// audited, writes a login-refused row with the identity (or "unknown") and the reason.
func (s *Server) refuse(w http.ResponseWriter, ip, identity, reason string, audited bool) {
	now := s.now()
	shown := auth.MaskIdentity(identity)
	if shown == "" {
		shown = "-"
	}
	s.logger.Printf("cherry: staff sign-in refused reason=%s identity=%s addr=%s", reason, shown, ip)
	if audited {
		actor := identity
		if actor == "" {
			actor = "unknown"
		}
		if err := store.Audit(actor, "login-refused", "staff", map[string]any{"reason": reason, "ip": ip}); err != nil {
			s.logger.Printf("cherry: refused sign-in not recorded: %v", err)
		}
	}
	s.countRefusal(ip, now)
	s.fail(w, http.StatusForbidden, textRefused)
}

// countRefusal counts a refused sign-in from ip. The sixth within 15 minutes blocks the address for 15 minutes.
func (s *Server) countRefusal(ip string, now time.Time) {
	if s.limits.refusals.Allow(ip, now) {
		return
	}
	s.mu.Lock()
	s.blocked[ip] = now.Add(blockFor)
	if len(s.blocked) > 1024 {
		for k, until := range s.blocked {
			if !now.Before(until) {
				delete(s.blocked, k)
			}
		}
	}
	s.mu.Unlock()
	s.logger.Printf("cherry: staff sign-in blocked addr=%s for %s after refusals", ip, blockFor)
}

// blockedNow reports whether ip is blocked now, and forgets an expired block.
func (s *Server) blockedNow(ip string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.blocked[ip]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(s.blocked, ip)
	return false
}

// takeLogin consumes the open sign-in whose state is state, or returns nil. It compares the state's hash with every open
// key in constant time, so a match takes the same time whichever entry holds it.
func (s *Server) takeLogin(state string) *loginState {
	if !validToken(state) {
		return nil
	}
	want := sha256.Sum256([]byte(state))
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *loginState
	var foundKey [32]byte
	for k, ls := range s.logins {
		if subtle.ConstantTimeCompare(k[:], want[:]) == 1 {
			found, foundKey = ls, k
		}
	}
	if found != nil {
		delete(s.logins, foundKey)
	}
	return found
}

// purgeLoginsLocked drops the sign-ins past their TTL. Caller holds s.mu.
func (s *Server) purgeLoginsLocked(now time.Time) {
	for k, ls := range s.logins {
		if now.Sub(ls.created) >= loginTTL {
			delete(s.logins, k)
		}
	}
}

// evictOldestLoginLocked drops the oldest open sign-in. Caller holds s.mu. The table is at most maxLogins long, so the scan
// is short.
func (s *Server) evictOldestLoginLocked() {
	var oldest [32]byte
	var first time.Time
	found := false
	for k, ls := range s.logins {
		if !found || ls.created.Before(first) {
			oldest, first, found = k, ls.created, true
		}
	}
	if found {
		delete(s.logins, oldest)
	}
}

// loginsFromLocked counts the open sign-ins of one address. Caller holds s.mu.
func (s *Server) loginsFromLocked(ip string) int {
	n := 0
	for _, ls := range s.logins {
		if ls.ip == ip {
			n++
		}
	}
	return n
}

// handleLogout: POST /auth/logout ends the session and writes the audit row.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess session) {
	s.endSession(r)
	if err := store.Audit(sess.identity, "logout", "staff", map[string]any{}); err != nil {
		s.logger.Printf("cherry: logout not recorded: %v", err)
	}
	clearCookie(w, cookieSession, http.SameSiteStrictMode)
	redirect(w, http.StatusSeeOther, "/")
}

// onlyKeys reports whether q holds only next (and as, when allowAs), each exactly once.
func onlyKeys(q url.Values, allowAs bool) bool {
	for k, v := range q {
		switch {
		case k == "next":
		case k == "as" && allowAs:
		default:
			return false
		}
		if len(v) != 1 {
			return false
		}
	}
	return true
}

// validNext accepts a path of this server only: one leading slash, letters, digits and -._~/ in the path, an optional query
// of letters, digits and -._~=&, at most 200 characters. No scheme, host, backslash, "//" or "..": nothing sends the browser
// off the staff host.
func validNext(p string) bool {
	if len(p) < 1 || len(p) > 200 || p[0] != '/' || strings.Contains(p, "//") || strings.Contains(p, "..") {
		return false
	}
	path, query, hasQuery := strings.Cut(p, "?")
	if !onlyChars(path, "-._~/") {
		return false
	}
	return !hasQuery || onlyChars(query, "-._~=&")
}

// loginPath is the sign-in link for a page that needs a fresh login: the page itself comes back after it, when the path is
// a safe one.
func loginPath(page string) string {
	if !validNext(page) {
		return loginURL
	}
	return loginURL + "?next=" + url.QueryEscape(page)
}
