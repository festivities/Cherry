package httpx

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/metrics"
)

// TestItemNameLookupLogging: a successful item-name lookup is counted but not logged; a refused one (429) and any other
// request are logged.
func TestItemNameLookupLogging(t *testing.T) {
	var buf bytes.Buffer
	status := http.StatusOK
	h := WithLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }), log.New(&buf, "", 0))
	before := metrics.ItemLookups.Load()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v4/items/dress/some", nil))
	if buf.Len() != 0 {
		t.Fatalf("a 200 item-name lookup was logged: %s", buf.String())
	}
	if got := metrics.ItemLookups.Load() - before; got != 1 {
		t.Fatalf("item-name lookups counted = %d, want 1", got)
	}
	status = http.StatusTooManyRequests
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v4/items/room/some", nil))
	if !strings.Contains(buf.String(), "status=429") {
		t.Fatalf("a 429 item-name lookup was not logged: %q", buf.String())
	}
	buf.Reset()
	status = http.StatusOK
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v4/other", nil))
	if !strings.Contains(buf.String(), `url="/v4/other"`) {
		t.Fatalf("a 200 GET was not logged: %q", buf.String())
	}
}

// TestStaticReadLogging: a successful static GET is counted (metrics.StaticReads) but not logged; a failed one is logged.
func TestStaticReadLogging(t *testing.T) {
	var buf bytes.Buffer
	status := http.StatusOK
	h := WithLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }), log.New(&buf, "", 0))
	before := metrics.StaticReads.Load()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/arts_item_custom_1/dp.png", nil))
	if buf.Len() != 0 {
		t.Fatalf("a 200 static read was logged: %s", buf.String())
	}
	if got := metrics.StaticReads.Load() - before; got != 1 {
		t.Fatalf("static reads counted = %d, want 1", got)
	}
	status = http.StatusNotFound
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/arts_item_custom_1/dp.png", nil))
	if !strings.Contains(buf.String(), "status=404") {
		t.Fatalf("a 404 static read was not logged: %q", buf.String())
	}
}

// TestAuthQueryIsRedacted:the request log keeps an /auth/ path but never its query or a referer, and keeps other queries.
func TestAuthQueryIsRedacted(t *testing.T) {
	var buf bytes.Buffer
	h := WithLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), log.New(&buf, "", 0))
	req := httptest.NewRequest(http.MethodGet, "/auth/v1/discord/callback?code=SECRETCODE&state=SERVERSTATE", nil)
	req.Header.Set("Referer", "https://discord.example/oauth2/authorize?state=SERVERSTATE")
	h.ServeHTTP(httptest.NewRecorder(), req)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v4/checkSession?x=kept", nil))
	line := buf.String()
	for _, secret := range []string{"SECRETCODE", "SERVERSTATE"} {
		if strings.Contains(line, secret) {
			t.Fatalf("log leaks %s: %s", secret, line)
		}
	}
	for _, want := range []string{`url="/auth/v1/discord/callback?[redacted]"`, `url="/v4/checkSession?x=kept"`, `Referer="[redacted]"`} {
		if !strings.Contains(line, want) {
			t.Fatalf("log lacks %s: %s", want, line)
		}
	}
	t.Logf("sample line: %s", strings.SplitN(line, "\n", 2)[0])
}
