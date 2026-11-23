package assets

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"cherry/data"
	"cherry/internal/metrics"
	"cherry/internal/opdata"
)

// egressFlag is egress.json: the operator switch that answers a streamed original's GET with a redirect to the public
// copy of the object (a custom domain on the same R2 bucket, cached at the edge), so Cherry sends no bytes for it. Off
// by default. Base is the public URL prefix ("https://<host>/", trailing slash); the Location is base plus the R2 key
// ("static/item/interior/<id>/dp.png"). Origin and Note are the operator-data marking; they are optional here.
type egressFlag struct {
	Origin     string   `json:"origin,omitempty"`
	Note       string   `json:"note,omitempty"`
	Enabled    bool     `json:"enabled"`
	Base       string   `json:"base"`
	Status     int      `json:"status"`
	Extensions []string `json:"extensions"`
}

var egressFile = opdata.NewSwitch[egressFlag]("egress.json", data.EgressJSON)

// egressRule is the switch while it is on; nil when it is off. Every streamed GET reads it.
type egressRule struct {
	base   string
	status int
	exts   map[string]bool
}

var egress atomic.Pointer[egressRule]

// LoadEgress is the startup load of egress.json; LoadArchive calls it first. A refused file is an error, and cmd/cherry
// stops before its listeners open.
func LoadEgress() error { return applyEgress(egressFile.Load()) }

// loadEgressFrom is LoadEgress for the file at p (tests).
func loadEgressFrom(p string) error { return applyEgress(egressFile.LoadFrom(p)) }

func applyEgress(loadErr error) error {
	if loadErr != nil {
		return loadErr
	}
	f := egressFile.Get()
	rule, err := newEgressRule(f)
	if err != nil {
		return fmt.Errorf("egress.json: %w", err)
	}
	egress.Store(rule)
	state := "disabled"
	if rule != nil {
		state = "enabled"
	}
	log.Printf("cherry: static redirect %s, %d extensions", state, len(f.Extensions))
	return nil
}

// newEgressRule checks the switch and returns the rule it makes, nil when it is off. The base is checked only when the
// switch is on, and no error names it (logs keep the public domain out).
func newEgressRule(f *egressFlag) (*egressRule, error) {
	switch f.Status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return nil, fmt.Errorf("status %d: want 301, 302, 307 or 308", f.Status)
	}
	exts := make(map[string]bool, len(f.Extensions))
	for _, e := range f.Extensions {
		if len(e) < 2 || e[0] != '.' || strings.ContainsAny(e, "/\\ \t") {
			return nil, fmt.Errorf("extension %q: want a dot and a name, such as .png", e)
		}
		exts[strings.ToLower(e)] = true
	}
	if !f.Enabled {
		return nil, nil
	}
	u, err := url.Parse(f.Base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(f.Base, "/") {
		return nil, fmt.Errorf("base must be an absolute http or https URL ending in /, with no query, fragment or user")
	}
	return &egressRule{base: f.Base, status: f.Status, exts: exts}, nil
}

// streamFile answers a streamed original that ServeRoute resolved to key: a redirect to the public copy when the switch
// allows it (egressTarget), else the bytes (serve). Converted items, generated files and update lists never come here.
func (s *Store) streamFile(w http.ResponseWriter, r *http.Request, key string) bool {
	loc, status, ok := s.egressTarget(key)
	if !ok {
		return s.serve(w, r, key)
	}
	w.Header().Set("Location", loc)
	w.WriteHeader(status)
	// Flushed before the body: Go then sends the empty body chunked, with no Content-Length. libcurl hands the 302's
	// headers to the client before the final reply's, and the client's size check reads a Content-Length it finds there.
	_ = http.NewResponseController(w).Flush()
	metrics.CountStaticRedirect(staticKind(key))
	return true
}

// egressTarget is the public URL of key when the switch is on, key is an original (static/) whose extension the switch
// lists, and R2 is known to hold it (inR2). Any other key is served by Cherry, so a key that may 404 is never redirected.
func (s *Store) egressTarget(key string) (string, int, bool) {
	rule := egress.Load()
	if rule == nil || !strings.HasPrefix(key, "static/") || !rule.exts[strings.ToLower(path.Ext(key))] || !s.inR2(key) {
		return "", 0, false
	}
	return rule.base + escapeKey(key), rule.status, true
}

// inR2 reports whether the object at key is known to exist in R2, without waiting for a read: an index already read lists
// it (the originals items index, the manifests, the pack index), or a local copy holds it (a copy is written only after
// R2 answered 200).
func (s *Store) inR2(key string) bool {
	if kind, id, name, ok := parseOriginalItemKey(key); ok && s.readyItems.Load().HasFile(kind, id, name) {
		return true
	}
	if st := s.state.Load(); st != nil && st.keys[key] {
		return true
	}
	local, ok := s.localPath(key)
	if !ok {
		return false
	}
	fi, err := os.Stat(local)
	return err == nil && fi.Mode().IsRegular()
}

// warm reads the manifests and the originals index in the background while the switch is on. A request never waits for
// them: until both are read a key is served by Cherry as before. A failed read is retried each minute.
func (s *Store) warm() {
	for {
		_, err := s.load()
		if err == nil && s.Items() != nil {
			return
		}
		if err != nil {
			log.Printf("cherry: static redirect waits for the manifests: %v", err)
		}
		time.Sleep(time.Minute)
	}
}

// staticKind is the metrics kind of a streamed key.
func staticKind(key string) metrics.StaticKind {
	switch {
	case strings.HasPrefix(key, "static/item/"), strings.HasPrefix(key, CherryPrefix+"item/"):
		return metrics.StaticItem
	case strings.HasSuffix(key, ".mworldpack"):
		return metrics.StaticPack
	}
	return metrics.StaticBulk
}

// escapeKey percent-encodes each segment of an R2 key for a URL path.
func escapeKey(key string) string {
	segs := strings.Split(key, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return strings.Join(segs, "/")
}
