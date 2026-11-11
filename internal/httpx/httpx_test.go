package httpx

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAuthQueryIsRedacted: the request log keeps an /auth/ path but never its query or a referer, and keeps other queries.
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
