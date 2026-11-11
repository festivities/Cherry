package httpx

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"cherry/internal/metrics"
)

// TestWithLoggingCountsRequests: every request through the logging wrapper moves the request counter.
func TestWithLoggingCountsRequests(t *testing.T) {
	h := WithLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), log.New(io.Discard, "", 0))
	before := metrics.Requests.Load()
	for range 3 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}
	if got := metrics.Requests.Load() - before; got != 3 {
		t.Fatalf("requests counted = %d, want 3", got)
	}
}
