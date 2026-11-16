package admin

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// The staff pages are html/template files in templates/. layout.html defines "layout" (the shell of a signed-in page or the
// plain frame of a sign-in page) and "shell"; every other file defines "page" for its own body, so one page is one file.
// html/template escapes every value, and no template holds a script, a style attribute or template.HTML.

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/staff.css
var staffCSS []byte

//go:embed static/staff.js
var staffJS []byte

// staticFile is one entry of the static allowlist: its content type, its bytes and the ETag of the bytes.
type staticFile struct {
	ctype string
	data  []byte
	etag  string
}

// staticFiles is the allowlist of /static/<file>. Nothing else is served and nothing is listed.
var staticFiles = map[string]staticFile{
	"staff.css": newStaticFile("text/css; charset=utf-8", staffCSS),
	"staff.js":  newStaticFile("text/javascript; charset=utf-8", staffJS),
}

func newStaticFile(ctype string, data []byte) staticFile {
	sum := sha256.Sum256(data)
	return staticFile{ctype: ctype, data: data, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
}

// assetURL is the URL of an allowlisted file with its hash as the version (/static/staff.css?v=…): a changed file gets a new
// URL, so the long cache of serveStatic never shows an old stylesheet with new pages.
func assetURL(name string) string {
	f, ok := staticFiles[name]
	if !ok {
		return "/static/" + name
	}
	return "/static/" + name + "?v=" + strings.Trim(f.etag, `"`)
}

// serveStatic serves an allowlisted file with its content type, a day of cache and its ETag. Any other name is 404: the
// pattern takes one path segment, and only the map's keys are opened.
func serveStatic(w http.ResponseWriter, r *http.Request) {
	f, ok := staticFiles[r.PathValue("file")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.ctype)
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("ETag", f.etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
}

// templateFuncs are the functions the templates may call.
var templateFuncs = template.FuncMap{"asset": assetURL, "utc": rfcDate}

// pages is one template set per page file: the layout and the page, parsed together. A template error fails at start.
var pages = loadPages()

func loadPages() map[string]*template.Template {
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil || len(files) == 0 {
		panic("admin: no staff templates")
	}
	sets := map[string]*template.Template{}
	for _, f := range files {
		name := strings.TrimSuffix(path.Base(f), ".html")
		if name == "layout" {
			continue
		}
		sets[name] = template.Must(template.New(name).Funcs(templateFuncs).ParseFS(templateFS, "templates/layout.html", f))
	}
	return sets
}

// render writes one page. The page is built in full first, so a template error leaves no half page.
func (s *Server) render(w http.ResponseWriter, status int, name string, v view) {
	set := pages[name]
	if set == nil {
		s.logger.Printf("cherry: staff page %s has no template", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if v.Server == "" {
		v.Server = s.cfg.ServerName
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, "layout", v); err != nil {
		s.logger.Printf("cherry: staff page %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
