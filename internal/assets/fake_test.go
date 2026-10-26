package assets

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeR2 is an in-memory S3 endpoint for the bucket "cherry-assets". It checks that requests are signed
// and records GET counts. Per-key switches inject failures for the cache tests.
type fakeR2 struct {
	srv     *httptest.Server
	mu      sync.Mutex
	objs    map[string][]byte // key -> body
	gets    map[string]int
	code    map[string]int           // key -> HTTP status to answer with
	etag    map[string]string        // key -> ETag to answer with instead of the MD5
	short   map[string]bool          // key -> declare 5 more bytes than are sent
	stall   map[string]chan struct{} // key -> block before the response headers until closed
	dribble map[string]chan struct{} // key -> send headers and one byte, then block until closed
}

func newFakeR2(t *testing.T) *fakeR2 {
	t.Helper()
	f := &fakeR2{
		objs:    map[string][]byte{},
		gets:    map[string]int{},
		code:    map[string]int{},
		etag:    map[string]string{},
		short:   map[string]bool{},
		stall:   map[string]chan struct{}{},
		dribble: map[string]chan struct{}{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeR2) cfg() Config {
	return Config{Endpoint: f.srv.URL, Bucket: "cherry-assets", AccessKey: "AKTEST", SecretKey: "SECRETTEST"}
}

func (f *fakeR2) put(key string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[key] = body
}

func (f *fakeR2) getCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets[key]
}

func (f *fakeR2) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objs[key]
	return ok
}

func (f *fakeR2) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKTEST/") {
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	key, ok := strings.CutPrefix(r.URL.Path, "/cherry-assets/")
	if !ok {
		http.Error(w, "bad bucket", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodDelete {
		f.mu.Lock()
		delete(f.objs, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPut {
		b, _ := io.ReadAll(r.Body)
		sum := md5.Sum(b)
		if r.Header.Get("Content-Md5") != base64.StdEncoding.EncodeToString(sum[:]) {
			http.Error(w, "bad Content-MD5", http.StatusBadRequest)
			return
		}
		f.put(key, b)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		return
	}
	f.mu.Lock()
	f.gets[key]++
	body, found := f.objs[key]
	code, short, stall, dribble := f.code[key], f.short[key], f.stall[key], f.dribble[key]
	etag, hasETag := f.etag[key]
	f.mu.Unlock()
	if stall != nil {
		<-stall
		return
	}
	if code != 0 {
		w.WriteHeader(code)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	sum := md5.Sum(body)
	if !hasETag {
		etag = hex.EncodeToString(sum[:])
	}
	w.Header().Set("ETag", `"`+etag+`"`)
	length := len(body)
	if short {
		length += 5
	}
	w.Header().Set("Content-Length", strconv.Itoa(length))
	w.WriteHeader(http.StatusOK)
	if dribble != nil && len(body) > 0 {
		_, _ = w.Write(body[:1])
		w.(http.Flusher).Flush()
		<-dribble
		return
	}
	if r.Method == http.MethodGet {
		_, _ = io.Copy(w, strings.NewReader(string(body)))
	}
}
