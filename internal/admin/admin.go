// Package admin is the staff UI (PLAN Roadmap 8, P4; slice 8.7 part 1): its own HTTPS listener on its own hostname, Discord
// sign-in for the owner and the staff allowlist (dev-fake for tests), server-side sessions with CSRF and Origin checks on
// every change, and server-rendered pages for the operator documents and the audit log. No game route is mounted here and
// no staff route on the game listeners (cmd/cherry's tests walk both muxes).
//
// ponytail: sessions, sign-in state and blocks live in memory, so one process holds them and a restart signs every staff
// member out. The caps bound them; a second process needs shared storage (a sessions table, schema v4).
package admin

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"cherry/internal/auth"
	"cherry/internal/config"
	"cherry/internal/store"
)

// Timeouts and limits (PLAN P4). A session ends after idleTimeout without a request or absoluteTimeout after its login.
const (
	idleTimeout     = 30 * time.Minute
	absoluteTimeout = 8 * time.Hour
	freshWindow     = 5 * time.Minute // a privileged action needs a Discord login this recent
	maxSessions     = 256
	maxBodySmall    = 4 << 10
	// docBodyCap bounds a draft's form: a document is at most store.MaxDocumentBytes, and a form-encoded byte can take
	// three bytes (%XX), so the cap is three times that plus the note and the token.
	docBodyCap = 3*store.MaxDocumentBytes + 64<<10
)

// Server is the staff listener: its routes, its TLS configuration and the state of its sign-ins and sessions. New builds
// it without listening (tests use it that way); Start listens too.
type Server struct {
	cfg    config.Config
	logger *log.Logger
	now    func() time.Time
	port   string          // listen.staff's port: the Host check and the origin
	origin string          // the expected Origin when staff.hostname is set
	owner  string          // the owner's identity: "discord:<id>" or "dev-<name>"
	allow  *auth.Allowlist // the other staff (staff.allowlist-file); nil without a file
	app    auth.DiscordApp // the Discord client (auth.mode discord)
	certs  *certStore
	limits limits

	mu       sync.Mutex
	logins   map[[32]byte]*loginState // by the SHA-256 of the sign-in state
	sessions map[[32]byte]*session    // by the SHA-256 of the session id
	blocked  map[string]time.Time     // client address -> blocked until

	srv *http.Server
}

// limits are the per-address limiters of the sign-in routes and the counter of refused sign-ins that blocks an address.
type limits struct {
	loginMinute, loginHour, callbackMinute, refusals *auth.Limiter
}

// staffCipherSuites: ECDHE with AEAD only, for TLS 1.2 (TLS 1.3 has its own fixed suites). No static RSA: those belong to
// the game listener, which the native client needs (CONTRACT §6).
var staffCipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
}

// New checks cfg and builds the server: the owner and the allowlist, the Discord client (discord mode), the certificates
// and the routes. It does not listen. logger gets the staff UI's log lines; nil means log.Default.
func New(cfg config.Config, logger *log.Logger) (*Server, error) {
	if logger == nil {
		logger = log.Default()
	}
	_, port, err := net.SplitHostPort(cfg.Staff)
	if err != nil {
		return nil, fmt.Errorf("admin: listen.staff %q: %w", cfg.Staff, err)
	}
	s := &Server{
		cfg: cfg, logger: logger, now: time.Now, port: port,
		logins:   map[[32]byte]*loginState{},
		sessions: map[[32]byte]*session{},
		blocked:  map[string]time.Time{},
		limits: limits{
			loginMinute:    auth.NewLimiter(10, time.Minute),
			loginHour:      auth.NewLimiter(60, time.Hour),
			callbackMinute: auth.NewLimiter(30, time.Minute),
			refusals:       auth.NewLimiter(5, 15*time.Minute),
		},
	}
	if cfg.StaffHostname != "" {
		s.origin = originFor(cfg.StaffHostname, port)
	}
	if cfg.StaffOwner != "" {
		s.owner = identityOf(cfg.StaffOwner)
	}
	if cfg.StaffAllowlistFile != "" {
		al, err := auth.LoadAllowlist(cfg.StaffAllowlistFile, cfg.AuthMode == config.AuthDevFake, logger)
		if err != nil {
			return nil, fmt.Errorf("admin: staff allowlist: %w", err)
		}
		s.allow = al
	}
	if cfg.AuthMode == config.AuthDiscord {
		secret, err := auth.ReadSecret(cfg.DiscordSecretFile)
		if err != nil {
			return nil, fmt.Errorf("admin: %w", err)
		}
		s.app = auth.DiscordApp{ClientID: cfg.DiscordClientID, Secret: secret, Redirect: cfg.StaffRedirect}
	}
	certs, err := newCertStore(cfg.StaffCert, cfg.StaffKey, cfg.StaffHostname, s.now, logger)
	if err != nil {
		return nil, err
	}
	s.certs = certs
	s.srv = &http.Server{
		Handler:           s.wrap(s.routes()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          logger,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){}, // HTTP/1.1 only
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			CipherSuites:   staffCipherSuites,
			GetCertificate: certs.get,
			NextProtos:     []string{"http/1.1"},
		},
	}
	return s, nil
}

// Start builds the server and listens on listen.staff. With listen.staff empty it does nothing and returns nil: the staff
// UI is off, which is the lab's state.
func Start(cfg config.Config, logger *log.Logger) (*Server, error) {
	if cfg.Staff == "" {
		return nil, nil
	}
	if logger == nil {
		logger = log.Default()
	}
	s, err := New(cfg, logger)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", cfg.Staff)
	if err != nil {
		return nil, fmt.Errorf("admin: listen %s: %w", cfg.Staff, err)
	}
	host := cfg.StaffHostname
	if host == "" {
		host = "any"
	}
	logger.Printf("cherry: staff UI listening addr=%s host=%s owner=%s public=%t", ln.Addr(), host, auth.MaskIdentity(s.owner), cfg.Public)
	go func() {
		if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("cherry: staff UI stopped: %v", err)
		}
	}()
	return s, nil
}

// Serve serves TLS on ln until Shutdown.
func (s *Server) Serve(ln net.Listener) error { return s.srv.ServeTLS(ln, "", "") }

// Shutdown stops the listener and waits for the open requests, up to ctx.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

// Handler is the whole staff handler: the security headers, the Host check and the routes. Tests call it directly.
func (s *Server) Handler() http.Handler { return s.srv.Handler }

// routes is the staff mux. A method the path does not take gets 405 from the mux itself, so a GET on a POST route never
// reaches its handler.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/staff.css", serveCSS)
	mux.HandleFunc("GET /auth/login", s.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("POST /auth/logout", s.action(false, maxBodySmall, s.handleLogout))
	mux.HandleFunc("GET /{$}", s.page(s.handleDashboard))
	mux.HandleFunc("GET /docs", s.page(s.handleDocsList))
	mux.HandleFunc("GET /docs/{name}", s.page(s.handleDocHistory))
	mux.HandleFunc("POST /docs/{name}/draft", s.action(false, docBodyCap, s.handleDraft))
	mux.HandleFunc("GET /docs/{name}/{version}", s.page(s.handleDocVersion))
	mux.HandleFunc("POST /docs/{name}/{version}/validate", s.action(false, maxBodySmall, s.handleValidate))
	mux.HandleFunc("POST /docs/{name}/{version}/publish", s.action(true, maxBodySmall, s.handlePublish))
	mux.HandleFunc("POST /docs/{name}/rollback", s.action(true, maxBodySmall, s.handleRollback))
	mux.HandleFunc("GET /audit", s.page(s.handleAudit))
	// A state change reached by GET gets 405: a literal path beats the {version} wildcard of the GET page.
	mux.HandleFunc("GET /docs/{name}/draft", methodNotAllowed("POST"))
	mux.HandleFunc("GET /docs/{name}/rollback", methodNotAllowed("POST"))
	return mux
}

// methodNotAllowed answers 405 with the method the route takes.
func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// wrap sets the security headers of every response (PLAN P4, "Requests") and refuses a Host that is not the staff hostname
// with 421 and no body. The check runs after the headers are set, so the 421 carries them too.
func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if s.cfg.Public {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if !s.hostOK(r) {
			w.WriteHeader(http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostOK: with staff.hostname set, the Host header is that name, with the listen port when it is not the default.
func (s *Server) hostOK(r *http.Request) bool {
	if s.cfg.StaffHostname == "" {
		return true
	}
	h := strings.ToLower(r.Host)
	return h == s.cfg.StaffHostname || h == s.cfg.StaffHostname+":"+s.port
}

// expectedOrigin is the staff origin an Origin header must equal: https://<staff.hostname>[:port] when the hostname is set,
// else the origin of the request itself (the Host header is then not checked, and a browser sends its own).
func (s *Server) expectedOrigin(r *http.Request) string {
	if s.origin != "" {
		return s.origin
	}
	return "https://" + r.Host
}

// originFor is the https origin of host on port.
func originFor(host, port string) string {
	if port == "443" {
		return "https://" + host
	}
	return "https://" + host + ":" + port
}

// identityOf is the audit identity of a configured owner: a Discord id, or a dev-fake name that is already dev-<name>.
func identityOf(owner string) string {
	if strings.HasPrefix(owner, "dev-") {
		return owner
	}
	return "discord:" + owner
}

// clientIP is the connection's address. Nothing sits in front of the staff listener, so forwarded headers are not read.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
