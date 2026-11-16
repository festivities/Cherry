package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"
)

// Cookie names. The __Host- prefix makes a browser accept them only with Secure, Path=/ and no Domain: they stay on the
// staff host and never reach the game listener's name.
const (
	cookieSession  = "__Host-cherry-staff"
	cookiePrelogin = "__Host-cherry-prelogin"
	sessionMaxAge  = int(absoluteTimeout / time.Second) // the absolute lifetime, in seconds
	preloginMaxAge = int(loginTTL / time.Second)
)

// session is one signed-in staff member. The cookie carries the session id; the map is keyed by its SHA-256, so the id is
// never kept. The CSRF token is per session and rides in every form of it.
type session struct {
	identity string    // "discord:<id>" or "dev-<name>": the actor of every audit row and of every store call
	name     string    // the display name Discord gave (dev-fake: the identity)
	csrf     string    // the CSRF token every form of this session carries
	created  time.Time // the absolute lifetime counts from here
	lastSeen time.Time // the idle timeout counts from here
	fresh    time.Time // the last Discord login: a privileged action needs one within freshWindow
	ip       string
}

// handler is a route that runs with the session of the request.
type handler func(w http.ResponseWriter, r *http.Request, sess session)

// page guards a GET page: a live session, or the sign-in page (401). A GET never changes the session's state except its
// idle clock.
func (s *Server) page(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.sessionOf(r)
		if !ok {
			s.signInPage(w)
			return
		}
		h(w, r, sess)
	}
}

// action guards a POST. In order: the Origin (or, without an Origin, Sec-Fetch-Site same-origin); a live session; a body
// within maxBody; the CSRF token, compared in constant time; and, when privileged, a Discord login within freshWindow.
// Only then does h run. Every refusal is one log line that names the reason and the path, never a value.
func (s *Server) action(privileged bool, maxBody int64, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.originOK(r) {
			s.refuseRequest(w, r, "origin")
			return
		}
		sess, ok := s.sessionOf(r)
		if !ok {
			s.signInPage(w)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			var big *http.MaxBytesError
			if errors.As(err, &big) {
				s.fail(w, http.StatusRequestEntityTooLarge, "The form is too large.")
				return
			}
			s.fail(w, http.StatusBadRequest, "The form could not be read.")
			return
		}
		if !csrfOK(r.PostFormValue("csrf"), sess.csrf) {
			s.refuseRequest(w, r, "csrf")
			return
		}
		if privileged && !s.freshLogin(sess) {
			s.logger.Printf("cherry: staff action needs a fresh sign-in path=%s", r.URL.Path)
			// The sign-in returns to the page the action was taken from: the POST path's parent (…/1/publish → …/1).
			s.render(w, http.StatusForbidden, "error", view{Title: "Sign in again", Text: "This action needs a Discord sign-in in the last five minutes. Sign in again, then repeat it.", Login: loginPath(path.Dir(r.URL.Path))})
			return
		}
		h(w, r, sess)
	}
}

// originOK is the Origin check: the Origin must be the staff origin exactly, once. An absent Origin is accepted only with
// Sec-Fetch-Site same-origin (browsers always send Origin on a POST; this covers the rest).
func (s *Server) originOK(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		fetch := r.Header.Values("Sec-Fetch-Site")
		return len(fetch) == 1 && fetch[0] == "same-origin"
	}
	return len(origins) == 1 && origins[0] == s.expectedOrigin(r)
}

// csrfOK compares the form's token with the session's in constant time. An empty token never matches.
func csrfOK(got, want string) bool {
	return got != "" && want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// freshLogin reports whether the session's Discord login is within freshWindow.
func (s *Server) freshLogin(sess session) bool {
	return s.now().Sub(sess.fresh) <= freshWindow
}

// sessionOf returns the live session of the request. An expired session is removed here, so it cannot come back.
func (s *Server) sessionOf(r *http.Request) (session, bool) {
	c, err := r.Cookie(cookieSession)
	if err != nil || !validToken(c.Value) {
		return session{}, false
	}
	key := sha256.Sum256([]byte(c.Value))
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[key]
	if sess == nil {
		return session{}, false
	}
	if now.Sub(sess.created) >= absoluteTimeout || now.Sub(sess.lastSeen) >= idleTimeout {
		delete(s.sessions, key)
		return session{}, false
	}
	sess.lastSeen = now
	return *sess, true
}

// openSession makes a session for a staff login and returns its id (the cookie value). A full table refuses: the caps
// bound the memory, and an expired session is dropped first.
func (s *Server) openSession(identity, name, ip string, now time.Time) (string, bool) {
	id := randomToken(32)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeSessionsLocked(now)
	if len(s.sessions) >= maxSessions {
		return "", false
	}
	s.sessions[sha256.Sum256([]byte(id))] = &session{
		identity: identity, name: name, csrf: randomToken(32),
		created: now, lastSeen: now, fresh: now, ip: ip,
	}
	return id, true
}

// endSession removes the session the request's cookie names, if any: logout, and the rotation at a new login (a
// pre-login value is never reused as a session id).
func (s *Server) endSession(r *http.Request) {
	c, err := r.Cookie(cookieSession)
	if err != nil || !validToken(c.Value) {
		return
	}
	key := sha256.Sum256([]byte(c.Value))
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, key)
}

// dropSession removes a session by its id (used when a login cannot be recorded after its session was made).
func (s *Server) dropSession(id string) {
	key := sha256.Sum256([]byte(id))
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, key)
}

// purgeSessionsLocked removes the sessions past either lifetime. Caller holds s.mu.
func (s *Server) purgeSessionsLocked(now time.Time) {
	for k, sess := range s.sessions {
		if now.Sub(sess.created) >= absoluteTimeout || now.Sub(sess.lastSeen) >= idleTimeout {
			delete(s.sessions, k)
		}
	}
}

// setCookie sets a cookie with the staff attributes: Secure, HttpOnly, Path=/, no Domain.
func setCookie(w http.ResponseWriter, name, value string, maxAge int, same http.SameSite) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: same, MaxAge: maxAge})
}

// clearCookie expires a cookie with the same attributes it was set with.
func clearCookie(w http.ResponseWriter, name string, same http.SameSite) {
	setCookie(w, name, "", -1, same)
}

// randomToken returns n CSPRNG bytes as base64url without padding.
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system random source is gone: nothing here may proceed
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// validToken: the 43-character form randomToken gives for 32 bytes.
func validToken(v string) bool {
	return len(v) == 43 && onlyChars(v, "-_")
}

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
