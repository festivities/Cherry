package assets

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
)

// Categories are the bulk categories the patcher downloads; each has a manifest in R2.
var Categories = []string{"animation", "sound", "ui", "tx", "subui"}

// langs are the languages the patcher asks update lists and files for. The files are the same in every
// language, so the language only has to be a valid one.
var langs = []string{"en", "tw", "cn", "kr", "jp", "th", "ja"}

// ItemKinds are the item folders the client requests by id (craftitem holds no numeric ids and is never requested).
var ItemKinds = []string{"custom", "dress", "interior", "tile", "pet", "garden"}

// missTTL is how long a 404 from R2 is remembered, so a missing dp.png is not fetched on every request.
const missTTL = 10 * time.Minute

type streamingFlag struct {
	Enabled bool `json:"enabled"`
}

// flagFile is streaming.json (embedded default off; %LOCALAPPDATA%\Cherry\config\streaming.json replaces it).
var flagFile = opdata.New[streamingFlag]("streaming.json", data.StreamingJSON, nil, nil)

// Store answers the streamed asset routes. ServeRoute on a nil *Store answers nothing (streaming off).
type Store struct {
	client   *Client
	cacheDir string
	state    atomic.Pointer[state]
	loadMu   sync.Mutex
	missing  sync.Map // R2 key -> time.Time until which the key counts as absent
}

// state is what the manifests produce: the checkresource2 body, one update list per category, and the map
// packs the update lists name ("<dir>/<name>").
type state struct {
	check string
	ini   map[string]string
	packs map[string]bool
}

// New returns a store that reads cfg's bucket and keeps local copies under cacheDir.
func New(cfg Config, cacheDir string) *Store {
	return &Store{client: NewClient(cfg), cacheDir: cacheDir}
}

// Open returns the store when streaming is on, or nil when it is off: streaming.json must enable it and
// %LOCALAPPDATA%\Cherry\cloudflare.json must exist. A present but unusable cloudflare.json is an error.
func Open() (*Store, error) {
	if err := flagFile.Load(); err != nil {
		return nil, err
	}
	if !flagFile.Get().Enabled {
		return nil, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "Cherry")
	cfgPath := filepath.Join(dir, "cloudflare.json")
	if _, err := os.Stat(cfgPath); err != nil {
		log.Printf("cherry: streaming is enabled but %s is not readable; serving local assets only", cfgPath)
		return nil, nil
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	log.Printf("cherry: asset streaming on (bucket %s)", cfg.Bucket)
	return New(cfg, filepath.Join(dir, "static")), nil
}

// WriteCheckResource2 writes checkresource2.json from the manifests. While they cannot be read it
// answers 503, so the patcher retries instead of recording stale versions.
func (s *Store) WriteCheckResource2(w http.ResponseWriter) {
	st, err := s.load()
	if err != nil {
		log.Printf("cherry: checkresource2: %v", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, st.check)
}

// ServeRoute answers the streamed GETs: the update lists (/arts_<cat>_ini_<v>/update_<lang>.ini), the
// files (/arts_<cat>_<lang>_<v>/<path>, /arts_item_<kind>_<id>/<file>, the chat action icons
// /arts_chatting_<ch>/action_hd/<file>, and the /img/read dp.png form) and the
// listed map packs (/<dir>/<name>, or /arts_animation_<lang>_<v>/../<dir>/<name>). It reports whether it wrote
// the response. A file that R2 does not have returns false, so the caller's own handling (generated
// thumbnails, the local string table, 404) still applies.
func (s *Store) ServeRoute(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || r.Method != http.MethodGet {
		return false
	}
	if cat, ok := iniRoute(r.URL.Path); ok {
		st, err := s.load()
		if err != nil {
			log.Printf("cherry: update list %s: %v", cat, err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, st.ini[cat])
		return true
	}
	if head, rest, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/"); ok && rest == DirProbePath && animationDir("/"+head) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, DirProbeBody)
		return true
	}
	if rel, ok := packRel(r.URL.Path); ok {
		return s.servePack(w, r, rel)
	}
	if key, ok := fileKey(r.URL.Path); ok {
		return s.serve(w, r, key)
	}
	return false
}

// load fetches the five manifests once and keeps the result. Concurrent callers share one fetch; a failure
// is not kept, so the next request tries again.
func (s *Store) load() (*state, error) {
	if st := s.state.Load(); st != nil {
		return st, nil
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if st := s.state.Load(); st != nil {
		return st, nil
	}
	mans := make([]*Manifest, len(Categories))
	errs := make([]error, len(Categories))
	var wg sync.WaitGroup
	for i, cat := range Categories {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mans[i], errs[i] = s.fetchManifest(cat)
		}()
	}
	wg.Wait()
	pm, err := s.fetchPacks()
	if err != nil {
		return nil, err
	}
	st := &state{ini: make(map[string]string, len(Categories)), packs: make(map[string]bool, len(pm.Entries))}
	for _, p := range pm.Entries {
		st.packs[p.Dir+"/"+p.Name] = true
	}
	versions := make(map[string]int, len(Categories))
	for i, cat := range Categories {
		if errs[i] != nil {
			return nil, errs[i]
		}
		var packs []PackEntry
		versions[cat] = mans[i].Version
		if cat == "animation" { // the map packs are listed with the animation category only
			packs = pm.Entries
			versions[cat] = AnimationVersion(mans[i].Entries, packs)
		}
		st.ini[cat] = mans[i].IniBody(packs)
	}
	st.check = checkResource2Body(versions)
	s.state.Store(st)
	return st, nil
}

// fetchPacks reads manifest/packs.json. A bucket without it has no map packs, and the animation list keeps
// only its own records.
func (s *Store) fetchPacks() (*PackManifest, error) {
	resp, err := s.client.Get(context.Background(), "manifest/packs.json")
	if errors.Is(err, ErrNotFound) {
		log.Printf("cherry: the bucket has no manifest/packs.json; no map packs")
		return &PackManifest{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("manifest packs: %w", err)
	}
	defer resp.Body.Close()
	var pm PackManifest
	if err := json.NewDecoder(resp.Body).Decode(&pm); err != nil {
		return nil, fmt.Errorf("manifest packs: %w", err)
	}
	if err := pm.check(); err != nil {
		return nil, err
	}
	return &pm, nil
}

// servePack answers a map pack request for rel ("<dir>/<name>") when the pack manifest lists it. Any other name
// is not handled, so the caller's 404 applies.
func (s *Store) servePack(w http.ResponseWriter, r *http.Request, rel string) bool {
	st, err := s.load()
	if err != nil {
		log.Printf("cherry: map pack %s: %v", rel, err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	}
	return st.packs[rel] && s.serve(w, r, "static/"+rel)
}

func (s *Store) fetchManifest(cat string) (*Manifest, error) {
	resp, err := s.client.Get(context.Background(), "manifest/"+cat+".json")
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", cat, err)
	}
	defer resp.Body.Close()
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", cat, err)
	}
	if err := m.check(cat); err != nil {
		return nil, err
	}
	return &m, nil
}

// serve writes the R2 object at key (from the local copy when there is one) and reports true, or false
// when the object is absent or cannot be fetched.
func (s *Store) serve(w http.ResponseWriter, r *http.Request, key string) bool {
	local, err := s.fetch(r.Context(), key)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			log.Printf("cherry: asset %s: %v", key, err)
		}
		return false
	}
	f, err := os.Open(local)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	http.ServeContent(w, r, path.Base(key), fi.ModTime(), f)
	return true
}

// fetch returns the local copy of key, downloading it on a miss.
// ponytail: a cached copy is never revalidated against R2 and concurrent misses for one key each download it. Re-upload changed content into a new cache dir, or add per-key singleflight if logs show duplicates.
func (s *Store) fetch(ctx context.Context, key string) (string, error) {
	local := filepath.Join(s.cacheDir, filepath.FromSlash(strings.TrimPrefix(key, "static/")))
	if fi, err := os.Stat(local); err == nil && fi.Mode().IsRegular() {
		return local, nil
	}
	if until, ok := s.missing.Load(key); ok && time.Now().Before(until.(time.Time)) {
		return "", ErrNotFound
	}
	resp, err := s.client.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		s.missing.Store(key, time.Now().Add(missTTL))
		return "", err
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := writeCopy(local, resp); err != nil {
		return "", err
	}
	return local, nil
}

// writeCopy stores a downloaded body at local. It is checked against Content-Length and, for single-part
// uploads, the ETag (the MD5). It goes through a temp file and a rename, so a partial copy never serves.
func writeCopy(local string, resp *http.Response) error {
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(local), ".fetch-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := md5.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && resp.ContentLength >= 0 && n != resp.ContentLength {
		err = fmt.Errorf("short body: %d of %d bytes", n, resp.ContentLength)
	}
	if etag := strings.Trim(resp.Header.Get("ETag"), `"`); err == nil && isMD5(etag) && !strings.EqualFold(etag, hex.EncodeToString(h.Sum(nil))) {
		err = errors.New("MD5 does not match the ETag")
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), local)
}

// iniRoute matches /arts_<cat>_ini_<v>/update_<lang>.ini for a streamed category. The version is ignored:
// the list is the current one whatever version the patcher names.
func iniRoute(p string) (string, bool) {
	dir, name := path.Split(p)
	head, ok := strings.CutPrefix(dir, "/arts_")
	if !ok {
		return "", false
	}
	head, ok = strings.CutSuffix(head, "/")
	if !ok {
		return "", false
	}
	cat, ver, ok := strings.Cut(head, "_ini_")
	if !ok || !isDigits(ver) || !slices.Contains(Categories, cat) {
		return "", false
	}
	lang, ok := strings.CutPrefix(name, "update_")
	if !ok {
		return "", false
	}
	lang, ok = strings.CutSuffix(lang, ".ini")
	if !ok || !slices.Contains(langs, lang) {
		return "", false
	}
	return cat, true
}

// packRel maps a map pack request to its manifest name "<dir>/<name>". The client asks for
// /arts_animation_<lang>_<v>/../<dir>/<name> and libcurl removes the dot segments, so the request reaches the
// server as /<dir>/<name>; the un-normalised form is accepted too. Any other path is not a pack.
func packRel(p string) (string, bool) {
	if i := strings.Index(p, "/../"); i >= 0 {
		if !animationDir(p[:i]) {
			return "", false
		}
		p = p[i+3:]
	}
	rest, ok := strings.CutPrefix(p, "/")
	if !ok {
		return "", false
	}
	dir, name, ok := strings.Cut(rest, "/")
	if !ok || !ValidPack(dir, name) {
		return "", false
	}
	return dir + "/" + name, true
}

// animationDir reports whether head is /arts_animation_<lang>_<v>, the directory the animation files are
// requested from.
func animationDir(head string) bool {
	rest, ok := strings.CutPrefix(head, "/arts_animation_")
	if !ok {
		return false
	}
	lang, ver, ok := strings.Cut(rest, "_")
	return ok && slices.Contains(langs, lang) && isDigits(ver)
}

// ChatActionFile reports whether name is a chat action icon, chatting_deco_action_<name>.png, as one file name.
// The route serves it and the uploader stores it under the same key.
func ChatActionFile(name string) bool {
	ok, _ := path.Match("chatting_deco_action_*.png", name)
	return ok && safeRel(name)
}

// fileKey maps a streamed file route to its R2 key. /arts_<cat>_<lang>_<v>/<path> is static/<cat>/<path>;
// /arts_item_<kind>_<id>/<file> is static/item/<kind>/<id>/<file>; /arts_chatting_<ch>/action_hd/<file> is
// static/chatting/action_hd/<file> whatever the ch digits (the client reads the icon version raw, and the
// uploader stores them without it); the item loader also asks for every item file under /img/read/
// (device 2026-10-26: /img/read/arts_item_custom_<id>/1409_iteminfo.artsitem).
func fileKey(p string) (string, bool) {
	if rest, ok := strings.CutPrefix(p, "/img/read"); ok {
		if !strings.HasPrefix(rest, "/arts_item_") {
			return "", false
		}
		p = rest
	}
	if rest, ok := strings.CutPrefix(p, "/arts_item_"); ok {
		kind, tail, ok := strings.Cut(rest, "_")
		if !ok || !slices.Contains(ItemKinds, kind) {
			return "", false
		}
		id, file, ok := strings.Cut(tail, "/")
		if !ok || !isDigits(id) || !safeRel(file) {
			return "", false
		}
		if file == "lpitem.aniproj" { // the client asks for the item animation by this name and saves it as <id>.aniproj
			file = id + ".aniproj"
		}
		return "static/item/" + kind + "/" + id + "/" + file, true
	}
	if rest, ok := strings.CutPrefix(p, "/arts_chatting_"); ok {
		ver, tail, ok := strings.Cut(rest, "/")
		if !ok || !isDigits(ver) {
			return "", false
		}
		file, ok := strings.CutPrefix(tail, "action_hd/")
		if !ok || !ChatActionFile(file) {
			return "", false
		}
		return "static/chatting/action_hd/" + file, true
	}
	rest, ok := strings.CutPrefix(p, "/arts_")
	if !ok {
		return "", false
	}
	head, file, ok := strings.Cut(rest, "/")
	if !ok || !safeRel(file) {
		return "", false
	}
	parts := strings.Split(head, "_")
	if len(parts) != 3 || !slices.Contains(Categories, parts[0]) || !slices.Contains(langs, parts[1]) || !isDigits(parts[2]) {
		return "", false
	}
	return "static/" + parts[0] + "/" + file, true
}
