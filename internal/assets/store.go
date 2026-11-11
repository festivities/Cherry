package assets

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"cherry/internal/config"
	"cherry/internal/opdata"
)

// Categories are the bulk categories the patcher downloads; each has an index in R2 (cherry/manifest/<cat>.json).
var Categories = []string{"animation", "sound", "ui", "tx", "subui"}

// langs are the languages the patcher asks update lists and files for. The files are the same in every
// language, so the language only has to be a valid one.
var langs = []string{"en", "tw", "cn", "kr", "jp", "th", "ja"}

// ItemKinds are the item folders the client requests by id (craftitem holds no numeric ids and is never requested).
var ItemKinds = []string{"custom", "dress", "interior", "tile", "pet", "garden"}

// CraftDirs are the static-base folders outside the update lists that the client downloads by relative path:
// garden01 (the item icons the Bag and Factory cells read, plus the quest and NPC pictures), craft01 (the Factory
// theme art) and photozone/frame (the Avatar Pic frames, five files per frame). The client asks for each file by its
// relative path ("garden01/items/item110000.png"); the archive keeps the same path under files/item/craftitem/ or
// files/cache/.
var CraftDirs = []string{"garden01", "craft01", "photozone/frame"}

// craftExts are the file kinds served under CraftDirs: PNG art and the .aniproj animation of a photozone frame.
var craftExts = []string{".png", ".aniproj"}

// missTTL is how long a 404 from R2 is remembered, so a missing dp.png is not fetched on every request.
const missTTL = 10 * time.Minute

type streamingFlag struct {
	Enabled bool `json:"enabled"`
}

// flagFile is streaming.json (embedded default off; %LOCALAPPDATA%\Cherry\config\streaming.json replaces it).
var flagFile = opdata.NewSwitch[streamingFlag]("streaming.json", data.StreamingJSON)

// Store reads Cherry's original assets: from R2 through the read-through cache (New, Open), or from local folders
// (NewLocal, for tests and the upload tool). Its streamed routes answer only when streaming.json enables them
// (ServeRoute, WriteCheckResource2); the reads answer whenever the store exists. A nil *Store answers nothing.
type Store struct {
	client       *Client             // nil for a store that reads only mounts (NewLocal)
	cacheDir     string              // local copies: cacheDir/<key without its static/ prefix>
	mounts       map[string][]string // NewLocal: key prefix -> local folders holding the keys under it, first match wins
	streaming    bool                // streaming.json: the patcher is told about R2 and the streamed routes answer
	state        atomic.Pointer[state]
	loadMu       sync.Mutex
	missing      sync.Map // R2 key -> time.Time until which the key counts as absent
	legacyLogged sync.Map // index file -> true once its legacy manifest/ fallback has been logged
	itemsMu      sync.Mutex
	items        *ItemIndex // the originals index (Items); nil until read
	itemsFailed  time.Time  // when reading the index last failed; retried after missTTL
}

// Archive is the store the server reads original assets from (economy, thumbs, web). It is nil when
// %LOCALAPPDATA%\Cherry\cloudflare.json is absent: the features that need originals then answer nothing. Set once at
// startup by LoadArchive; tests swap it.
var Archive *Store

// LoadArchive opens the store into Archive (Open) and fetches the converted-items index (FetchItemIndex). It starts
// reading the originals index in the background. The startup checks that ask about item art wait for that read: on the
// first start after a deploy it is one bucket listing (saved under the cache dir); later starts read the saved copy.
func LoadArchive() error {
	s, err := Open()
	if err != nil {
		return err
	}
	Archive = s
	if s == nil {
		return nil
	}
	if err := s.FetchItemIndex(); err != nil {
		log.Printf("cherry: converted items index: %v", err)
	}
	go s.Items()
	return nil
}

// NewLocal returns a store with no bucket: each key is read from the folders of the longest mount prefix that matches
// it, the first folder holding the file winning (mounts maps "static/item/" to the folders holding item/<kind>/<id>/...,
// in the uploader's order: santi, then lpbackup). Tests and the upload tool use it.
func NewLocal(mounts map[string][]string) *Store {
	return &Store{mounts: mounts}
}

// Streaming reports whether streaming.json turns on the patcher lists and the streamed routes.
func (s *Store) Streaming() bool { return s != nil && s.streaming }

// state is what the manifests produce: the checkresource2 body, one update list per category, and the map
// packs the update lists name ("<dir>/<name>", mapped to the R2 key that holds the pack).
type state struct {
	check string
	ini   map[string]string
	packs map[string]string
}

// New returns a streaming store that reads cfg's bucket and keeps local copies under cacheDir.
func New(cfg Config, cacheDir string) *Store {
	return &Store{client: NewClient(cfg), cacheDir: cacheDir, streaming: true}
}

// Open returns the store for %LOCALAPPDATA%\Cherry\cloudflare.json, or nil when that file is absent (the original
// assets are then unavailable). A present but unusable file is an error. streaming.json only sets Streaming: the
// reads work either way.
func Open() (*Store, error) {
	if err := flagFile.Load(); err != nil {
		return nil, err
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	cacheDir, err := config.CacheDir()
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(dataDir, "cloudflare.json")
	if _, err := os.Stat(cfgPath); err != nil {
		if flagFile.Get().Enabled {
			log.Printf("cherry: streaming is enabled but %s is not readable; original assets are unavailable", cfgPath)
		}
		return nil, nil
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	s := New(cfg, filepath.Join(cacheDir, "static"))
	s.streaming = flagFile.Get().Enabled
	log.Printf("cherry: R2 bucket %s, streaming %v", cfg.Bucket, s.streaming)
	return s, nil
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
	if !s.Streaming() || r.Method != http.MethodGet {
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
	if rel, ok := CraftRel(r.URL.Path); ok {
		return s.serve(w, r, "static/"+rel)
	}
	if key, ok := generatedItemKey(r.URL.Path); ok { // a converted item is Cherry-made: cherry/item/, never static/
		return s.serve(w, r, key)
	}
	if key, ok := fileKey(r.URL.Path); ok {
		return s.serve(w, r, key)
	}
	return false
}

// load fetches the five category indexes and the pack index once and keeps the result. Concurrent callers
// share one fetch; a failure is not kept, so the next request tries again.
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
	st := &state{ini: make(map[string]string, len(Categories)), packs: make(map[string]string, len(pm.Entries))}
	for _, p := range pm.Entries {
		st.packs[p.Dir+"/"+p.Name] = p.Key()
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

// fetchPacks reads the pack index. A bucket without it has no map packs, and the animation list keeps only
// its own records.
func (s *Store) fetchPacks() (*PackManifest, error) {
	resp, err := s.getManifest("packs.json")
	if errors.Is(err, ErrNotFound) {
		log.Printf("cherry: the bucket has no %s; no map packs", ManifestKey("packs.json"))
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

// servePack answers a map pack request for rel ("<dir>/<name>") when the pack index lists it, from the key the
// entry names (static/ for an original, cherry/ for a generated pack). Any other name is not handled, so the
// caller's 404 applies.
func (s *Store) servePack(w http.ResponseWriter, r *http.Request, rel string) bool {
	st, err := s.load()
	if err != nil {
		log.Printf("cherry: map pack %s: %v", rel, err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	}
	key, ok := st.packs[rel]
	return ok && s.serve(w, r, key)
}

// getManifest opens the index file at its cherry/manifest/ key. While the migration has not moved it, the
// legacy manifest/ key answers, and the fallback is logged once per file.
// ponytail: legacy fallback; remove it with legacyManifestKey once migrate-manifests has run on the live bucket.
func (s *Store) getManifest(file string) (*http.Response, error) {
	resp, err := s.client.Get(context.Background(), ManifestKey(file))
	if !errors.Is(err, ErrNotFound) {
		return resp, err
	}
	if _, logged := s.legacyLogged.LoadOrStore(file, true); !logged {
		log.Printf("cherry: %s is not at %s yet; reading the legacy key %s", file, ManifestKey(file), legacyManifestKey(file))
	}
	return s.client.Get(context.Background(), legacyManifestKey(file))
}

func (s *Store) fetchManifest(cat string) (*Manifest, error) {
	resp, err := s.getManifest(cat + ".json")
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
	// The servers' 30 s WriteTimeout cut a 101 MB animation file off for a slow emulator (AVD Jerry, 2026-10-26:
	// "Network Error" at 92 MB). An asset copy has no deadline; a stalled client still closes on its own.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	http.ServeContent(w, r, path.Base(key), fi.ModTime(), f)
	return true
}

// Local returns a local file holding the original at key, downloading it from R2 on a miss. A key that does not
// exist, or a nil store, gives fs.ErrNotExist.
func (s *Store) Local(key string) (string, error) {
	if s == nil {
		return "", fs.ErrNotExist
	}
	local, err := s.fetch(context.Background(), key)
	if errors.Is(err, ErrNotFound) {
		return "", fs.ErrNotExist
	}
	return local, err
}

// prefetchSlots bounds the reads Prefetch runs at once in the process: a cold thumbnail reads its item's files, and a shop
// tab asks for dozens of thumbnails together.
var prefetchSlots = make(chan struct{}, 16)

// Prefetch copies the originals at keys into the local cache, several at a time, so the reads that follow find them
// local: a set of files costs about one round-trip instead of one each. A key that fails is left for the read to report.
func (s *Store) Prefetch(keys []string) {
	var wg sync.WaitGroup
	for _, key := range keys {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			prefetchSlots <- struct{}{}
			defer func() { <-prefetchSlots }()
			_, _ = s.Local(key)
		}(key)
	}
	wg.Wait()
}

// ReadFile returns the original at key (see Local).
func (s *Store) ReadFile(key string) ([]byte, error) {
	local, err := s.Local(key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(local)
}

// ServeKey writes the original at key to w, like a streamed file, and reports whether it did. A missing key or a
// nil store returns false, so the caller's own 404 applies.
func (s *Store) ServeKey(w http.ResponseWriter, r *http.Request, key string) bool {
	return s != nil && s.serve(w, r, key)
}

// ServeDiarySkin answers the two diary skin originals (diarySkinKey) with or without streaming: the client downloads
// what its skin_file.ini lists, and that list is served either way. ServeRoute answers them too when streaming is on.
// A miss returns false.
func (s *Store) ServeDiarySkin(w http.ResponseWriter, r *http.Request) bool {
	key, ok := diarySkinKey(r.URL.Path)
	return ok && r.Method == http.MethodGet && s.ServeKey(w, r, key)
}

// diarySkinFiles are the two diary skin originals the default skin's skin_file.ini lists (web: diarySkinLists), keyed by
// the path under /arts_diaryskin_<lang>_00007/. The rest of the diary skin is not in R2 and has no key.
var diarySkinFiles = map[string]string{
	"common/UIImage_hd/avatar_photo_bg.png": "static/DiarySkin/common/UIImage_hd/avatar_photo_bg.png",
	"00001/UIImage_hd/skin_info.json":       "static/DiarySkin/00001/UIImage_hd/skin_info.json",
}

// diarySkinKey maps /arts_diaryskin_{en,jp}_00007/<file> to its R2 key for the two files in diarySkinFiles.
func diarySkinKey(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, "/arts_diaryskin_")
	if !ok {
		return "", false
	}
	lang, tail, ok := strings.Cut(rest, "_00007/")
	if !ok || (lang != "en" && lang != "jp") {
		return "", false
	}
	key, ok := diarySkinFiles[tail]
	return key, ok
}

// DeviceFiles is the device files/ tree under sub (for example "UIResource/UIImage_hd") as an fs.FS over the store. It
// reads Archive at each Open, so a package variable can hold it before the store is set. The keys follow the
// uploader: files/UIResource/<rel> is static/ui/<rel>, every other folder is static/<folder>/<rel>.
func DeviceFiles(sub string) fs.FS { return deviceFS{sub: sub} }

type deviceFS struct{ sub string }

func (d deviceFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	rel := path.Join(d.sub, name)
	if rel == "." {
		rel = ""
	}
	local, err := Archive.Local(staticKey(rel))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return os.Open(local)
}

// staticKey maps a device files/ path to its R2 key, the rule the uploader uses: UIResource/<rel> is ui/<rel>.
func staticKey(rel string) string {
	if r, ok := strings.CutPrefix(rel, "UIResource/"); ok {
		return "static/ui/" + r
	}
	return "static/" + rel
}

// mounted returns the local file of key: the first folder of its longest mount prefix that holds it (NewLocal stores).
func (s *Store) mounted(key string) (string, error) {
	best := s.mountPrefix(key)
	if best == "" {
		return "", ErrNotFound
	}
	for _, folder := range s.mounts[best] {
		local := filepath.Join(folder, filepath.FromSlash(key[len(best):]))
		if fi, err := os.Stat(local); err == nil && fi.Mode().IsRegular() {
			return local, nil
		}
	}
	return "", ErrNotFound
}

// mountPrefix is the longest mount prefix that matches key ("" for none).
func (s *Store) mountPrefix(key string) string {
	best := ""
	for prefix := range s.mounts {
		if strings.HasPrefix(key, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	return best
}

// fetch returns the local copy of key, downloading it on a miss. A mounted store (no client) answers from its folders.
// ponytail: a cached copy is never revalidated against R2 and concurrent misses for one key each download it. Re-upload changed content into a new cache dir, or add per-key singleflight if logs show duplicates.
func (s *Store) fetch(ctx context.Context, key string) (string, error) {
	if s.client == nil {
		return s.mounted(key)
	}
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

// CraftRel maps a request path to its relative path under the static base when it names a craftExts file in one of
// CraftDirs ("/garden01/items/item110000.png" -> "garden01/items/item110000.png"). The R2 key is static/<rel>, and
// the archive file is the same path (see CraftDirs). The uploader lists the same set.
func CraftRel(p string) (string, bool) {
	rel, ok := strings.CutPrefix(p, "/")
	if !ok || !safeRel(rel) || !slices.Contains(craftExts, path.Ext(rel)) {
		return "", false
	}
	for _, dir := range CraftDirs {
		if strings.HasPrefix(rel, dir+"/") {
			return rel, true
		}
	}
	return "", false
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
// (device 2026-10-26: /img/read/arts_item_custom_<id>/1409_iteminfo.artsitem). The two default diary skin files
// (/arts_diaryskin_<lang>_00007/…) are static/DiarySkin/… (diarySkinKey); they are not a category.
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
		// The client asks for the item animation as lpitem.aniproj and saves it as <id>.aniproj; any other animation it names
		// <name>.aniproj and saves as <id>_<name>.aniproj (santi: 1,590 plain + 257 prefixed, no other form). A missing one 404ed
		// and the client then crashed drawing the item (225408332, device 2026-11-02 18:05).
		if file == "lpitem.aniproj" {
			file = id + ".aniproj"
		} else if strings.HasSuffix(file, ".aniproj") && !strings.Contains(file, "/") && file != id+".aniproj" && !strings.HasPrefix(file, id+"_") {
			file = id + "_" + file
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
	if key, ok := diarySkinKey(p); ok {
		return key, true
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
