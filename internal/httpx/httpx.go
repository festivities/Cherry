// Package httpx holds the small HTTP/JSON helpers (JSON writers, error bodies, cookie
// and body parsing, request logging) shared by every route package.
package httpx

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"cherry/internal/metrics"
)

const NotFoundBody = `{"errorCode":"404","errorMessage":"cherry: unknown route"}`

const UnknownSessionBody = `{"errorCode":"404","errorMessage":"cherry: unknown session"}`

const BadRequestBody = `{"errorCode":"400","errorMessage":"cherry: bad request"}`

// RateLimitedBody answers a request over a token-bucket limit (429, internal/web edge.go).
const RateLimitedBody = `{"errorCode":"429","errorMessage":"cherry: rate limited"}`

func HandleJSONBody(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			ServeNotFound(w)
			return
		}
		WriteJSON(w, http.StatusOK, body)
	}
}

const MaxJSONBody = 1 << 20

// The native client appends one NUL after its JSON body. Accept that exact
// terminator while leaving every other trailing byte to json.Unmarshal.
func UnmarshalNativeJSON(raw []byte, dst any) (bool, error) {
	nativeTerminator := len(raw) > 0 && raw[len(raw)-1] == 0
	if nativeTerminator {
		raw = raw[:len(raw)-1]
	}
	return nativeTerminator, json.Unmarshal(raw, dst)
}

func IsAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func CookieValue(r *http.Request, name string) string {
	for _, part := range strings.Split(r.Header.Get("Cookie"), ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && key == name {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

func ServeNotFound(w http.ResponseWriter) {
	WriteJSON(w, http.StatusNotFound, NotFoundBody)
}

func WriteJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func WriteObj(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	WriteJSON(w, http.StatusOK, string(b))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
	}
	rec.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the connection (the asset stream lifts its write deadline).
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.ResponseWriter.Write(b)
}

// IsItemNameLookup reports whether r is an item-name lookup (POST /v4/items/dress/some or /v4/items/room/some,
// economy.HandleItemsSome): read-only catalog lookups of at most 100 codes, no account lock. Some clients send them 10+ times
// a second (2026-11-22: ~38,700 refused in one hour by the address/account buckets, shown to players as "Network Error"), so
// they share the per-address static bucket instead, which still caps a flood.
// ponytail: why the client re-asks for the same names is not known yet (PLAN handoff); tighten this once it is.
func IsItemNameLookup(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == "/v4/items/dress/some" || r.URL.Path == "/v4/items/room/some")
}

// staticPrefixes are the GET paths that serve files: the asset store, the Cherry pictures, the butler voice packs and the
// profile and diary pictures. Everything else is a game request.
var staticPrefixes = []string{"/arts_", "/img/read/", "/notice/", "/cherry/", "/web/", "/lineplay/", "/r/lineplay/", "/v4/resource/splash/", "/butler_voice_master/"}

// IsStaticGET reports whether r is a file read the limits exempt: a GET or HEAD of a static prefix, or a map pack ("/../").
func IsStaticGET(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if strings.Contains(p, "/../") {
		return true
	}
	for _, pre := range staticPrefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// WithLogging counts and logs every request. A successful one (below 400, 304 included) of an item-name lookup or a static
// read is counted (metrics.ItemLookups, metrics.StaticReads) but not logged: the client sends them 10+ a second
// (client-inherent, REFERENCE "Known client-inherent limits"), and journald keeps only 10k lines per 30 s, so they pushed
// the refusals out. Failures (4xx and 5xx, 429 included) are still logged.
func WithLogging(next http.Handler, logger *log.Logger) http.Handler {
	var seq atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics.Requests.Add(1)
		lookup := IsItemNameLookup(r)
		if lookup {
			metrics.ItemLookups.Add(1)
		}
		static := IsStaticGET(r)
		if static {
			metrics.StaticReads.Add(1)
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		if (lookup || static) && status < http.StatusBadRequest {
			return
		}
		sni := ""
		if r.TLS != nil {
			sni = r.TLS.ServerName
		}
		logger.Printf("REQ #%d remote=%s sni=%q host=%q method=%s url=%q status=%d duration=%s headers=%s",
			seq.Add(1), r.RemoteAddr, sni, r.Host, r.Method, logURL(r.URL), status, time.Since(start), FormatHeaders(r.Header))
	})
}

// logURL is the request target as the log shows it. An /auth/ query carries OAuth codes, tickets and server state, so
// only its path is kept.
func logURL(u *url.URL) string {
	if strings.HasPrefix(u.Path, "/auth/") && u.RawQuery != "" {
		return u.Path + "?[redacted]"
	}
	return u.RequestURI()
}

func FormatHeaders(h http.Header) string {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		for _, value := range h[name] {
			lower := strings.ToLower(name)
			// A referer can carry an /auth/ query (the server state of a sign-in), so it is redacted like a credential.
			if lower == "cookie" || lower == "referer" || strings.Contains(lower, "auth") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || lower == "x-lineplay-acnt" {
				value = "[redacted]"
			}
			fmt.Fprintf(&b, "%s=%q ", name, value)
		}
	}
	return strings.TrimSpace(b.String())
}

const SaveFailedBody = `{"errorCode":"500","errorMessage":"cherry: save failed"}`

func ReadNativeBody(r *http.Request) ([]byte, error) {
	body := io.Reader(r.Body)
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		body = zr
	}
	raw, err := io.ReadAll(io.LimitReader(body, MaxJSONBody+1))
	if err != nil || len(raw) > MaxJSONBody {
		return nil, errors.New("bad body")
	}
	return raw, nil
}
