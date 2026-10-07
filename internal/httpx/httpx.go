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
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const NotFoundBody = `{"errorCode":"404","errorMessage":"cherry: unknown route"}`

const UnknownSessionBody = `{"errorCode":"404","errorMessage":"cherry: unknown session"}`

const BadRequestBody = `{"errorCode":"400","errorMessage":"cherry: bad request"}`

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

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.ResponseWriter.Write(b)
}

func WithLogging(next http.Handler, logger *log.Logger) http.Handler {
	var seq atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		sni := ""
		if r.TLS != nil {
			sni = r.TLS.ServerName
		}
		logger.Printf("REQ #%d remote=%s sni=%q host=%q method=%s url=%q status=%d duration=%s headers=%s",
			seq.Add(1), r.RemoteAddr, sni, r.Host, r.Method, r.URL.RequestURI(), status, time.Since(start), FormatHeaders(r.Header))
	})
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
			if lower == "cookie" || strings.Contains(lower, "auth") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || lower == "x-lineplay-acnt" {
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
