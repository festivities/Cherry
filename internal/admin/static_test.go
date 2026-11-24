package admin

import (
	"net/http"
	"strings"
	"testing"
)

// TestStaticAllowlist: the two allowlisted files are served with their types, a day of cache and an ETag; every other
// name is 404, and no directory or traversal path reaches a file.
func TestStaticAllowlist(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	for name, ctype := range map[string]string{
		"staff.css": "text/css; charset=utf-8", "staff.js": "text/javascript; charset=utf-8",
		"editor.js": "text/javascript; charset=utf-8", "chat.js": "text/javascript; charset=utf-8",
		"diary.js": "text/javascript; charset=utf-8", "favicon.svg": "image/svg+xml",
	} {
		r := c.get("/static/" + name)
		expect(t, r, http.StatusOK)
		if got := r.Header.Get("Content-Type"); got != ctype {
			t.Errorf("%s: content type %q, want %q", name, got, ctype)
		}
		if got := r.Header.Get("Cache-Control"); got != "public, max-age=86400" {
			t.Errorf("%s: cache control %q", name, got)
		}
		if r.Header.Get("ETag") == "" || r.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no ETag or no nosniff", name)
		}
		if r.body == "" {
			t.Errorf("%s: empty body", name)
		}
	}
	for _, target := range []string{
		"/static/nope.css", "/static/", "/static/staff.css/", "/static/staff.css.bak", "/static/staff.go",
		"/static/templates/layout.html", "/static/static/staff.css", "/static/%2e%2e%2fadmin.go", "/static/..%2fcherry.db",
	} {
		if r := c.get(target); r.StatusCode == http.StatusOK {
			t.Errorf("GET %s: status 200, want anything but a file", target)
		}
	}
	expect(t, c.get("/static/nope.css"), http.StatusNotFound)
	expect(t, c.get("/static/%2e%2e%2fadmin.go"), http.StatusNotFound)
}

// TestStaticIsConditional: a repeat request with the ETag gets 304 and no body.
func TestStaticIsConditional(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	etag := c.get("/static/staff.css").Header.Get("ETag")
	r := c.req(http.MethodGet, "/static/staff.css", nil)
	r.Header.Set("If-None-Match", etag)
	got := c.send(r)
	expect(t, got, http.StatusNotModified)
	if got.body != "" {
		t.Errorf("304 with a body of %d bytes", len(got.body))
	}
}

// TestAssetURLIsVersioned: the pages link the files with the hash of their bytes, so a changed file has a new URL.
func TestAssetURLIsVersioned(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	page := c.get("/auth/login?as=bad%20name").body
	for _, name := range []string{"staff.css"} {
		if want := assetURL(name); !strings.Contains(page, want) {
			t.Errorf("the page does not link %s", want)
		}
	}
	if got := assetURL("staff.css"); !strings.HasPrefix(got, "/static/staff.css?v=") || len(got) < len("/static/staff.css?v=")+8 {
		t.Errorf("asset URL %q", got)
	}
	if got := assetURL("missing.css"); got != "/static/missing.css" {
		t.Errorf("unknown asset URL %q", got)
	}
	expect(t, c.get(assetURL("staff.css")), http.StatusOK)
	expect(t, c.get(assetURL("staff.js")), http.StatusOK)
	expect(t, c.get(assetURL("chat.js")), http.StatusOK)
	expect(t, c.get(assetURL("diary.js")), http.StatusOK)
}
