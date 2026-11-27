package assets

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// The animation overlay: Cherry-made files the patcher's animation list carries beside the originals (the custom
// Garden button pictures, economy/gardenicons.go; spec garden-buttons-20261128). An overlay file either replaces a listed
// original with a Cherry-made superset of it (OriginalAnimation gives the listed original's bytes to build from) or adds
// a new file under -cherry/, Cherry's own device folder (DirProbePath). Cherry serves the files itself from memory: never
// from R2, never redirected (egress.go), whatever the bucket. R2 static/ keeps the original. The animation version covers
// the overlay, so a change makes each client fetch the list again and download exactly the changed files (the client
// diffs path + MD5; streaming-spec section 1). With no overlay the list names the original again, and clients download
// it back. The file URL carries the list version (arts_animation_<lang>_<v>/<path>), so a client is served the overlay
// of the list it fetched while that version is one of the last overlayKeep.

// OverlayFile is one Cherry-made animation file: its list path (relative to the category, no leading "/") and bytes.
type OverlayFile struct {
	Path string
	Data []byte
}

// overlayKeep is how many overlay versions stay servable after a change (a client mid-patch keeps its list's bytes).
const overlayKeep = 8

// overlaySet is one overlay: the bytes by path and the list entries, in the order given.
type overlaySet struct {
	files   map[string][]byte
	entries []Entry
}

func newOverlaySet(files []OverlayFile) (*overlaySet, error) {
	if len(files) == 0 {
		return nil, nil
	}
	set := &overlaySet{files: make(map[string][]byte, len(files))}
	for _, f := range files {
		switch {
		case !safeRel(f.Path) || strings.HasPrefix(f.Path, "../"):
			return nil, fmt.Errorf("overlay: unsafe path %q", f.Path)
		case len(f.Data) == 0:
			return nil, fmt.Errorf("overlay: %s is empty", f.Path)
		case set.files[f.Path] != nil:
			return nil, fmt.Errorf("overlay: duplicate path %q", f.Path)
		}
		sum := md5.Sum(f.Data)
		set.files[f.Path] = f.Data
		set.entries = append(set.entries, Entry{Path: f.Path, Size: int64(len(f.Data)), MD5: strings.ToUpper(hex.EncodeToString(sum[:]))})
	}
	return set, nil
}

// withOverlay is the animation entries with the overlay applied: a listed path takes the overlay's size and MD5, a
// -cherry/ path is appended. Any other path names no original and is left out (logged).
func withOverlay(anim []Entry, set *overlaySet) []Entry {
	if set == nil {
		return anim
	}
	out := slices.Clone(anim)
	for _, e := range set.entries {
		if i := slices.IndexFunc(out, func(a Entry) bool { return a.Path == e.Path }); i >= 0 {
			out[i] = e
		} else if strings.HasPrefix(e.Path, "-cherry/") {
			out = append(out, e)
		} else {
			log.Printf("cherry: animation overlay: %s replaces no listed file; left out", e.Path)
		}
	}
	return out
}

// SetAnimationOverlay replaces the overlay (nil or empty clears it). The lists and checkresource2 are rebuilt from the
// loaded manifests at once; before the first load the overlay waits for it.
func (s *Store) SetAnimationOverlay(files []OverlayFile) error {
	if s == nil {
		return nil
	}
	set, err := newOverlaySet(files)
	if err != nil {
		return err
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	s.overlay = set
	if cur := s.state.Load(); cur != nil {
		s.state.Store(s.buildState(cur.mans, cur.pm))
	}
	return nil
}

// OriginalAnimation returns the original bytes of the animation file the list names at rel, checked against the
// manifest's MD5, so an overlay is built on exactly what the clients hold. It needs streaming (the lists).
func (s *Store) OriginalAnimation(rel string) ([]byte, error) {
	if !s.Streaming() {
		return nil, errors.New("streaming is off: this server does not serve the client's patcher lists")
	}
	st, err := s.load()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(st.mans[0].Entries, func(e Entry) bool { return e.Path == rel })
	if i < 0 {
		return nil, fmt.Errorf("the animation list has no %s", rel)
	}
	b, err := s.ReadFile("static/animation/" + rel)
	if err != nil {
		return nil, err
	}
	sum := md5.Sum(b)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), st.mans[0].Entries[i].MD5) {
		return nil, fmt.Errorf("static/animation/%s does not match its manifest MD5", rel)
	}
	return b, nil
}

// serveOverlay answers /arts_animation_<lang>_<v>/<rel> when rel is an overlay file: the overlay of list version v
// while it is kept, else the current one. It reports whether it wrote the response.
func (s *Store) serveOverlay(w http.ResponseWriter, head, rel string) bool {
	st := s.state.Load()
	if st == nil {
		return false
	}
	set := st.overlay
	if i := strings.LastIndexByte(head, '_'); i >= 0 {
		if v, err := strconv.Atoi(head[i+1:]); err == nil {
			s.loadMu.Lock()
			if old, ok := s.overlayByVer[v]; ok {
				set = old
			}
			s.loadMu.Unlock()
		}
	}
	if set == nil {
		return false
	}
	b, ok := set.files[rel]
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
	return true
}

// rememberOverlay keeps set servable under animation version v (the last overlayKeep versions). Caller holds loadMu.
func (s *Store) rememberOverlay(v int, set *overlaySet) {
	if s.overlayByVer == nil {
		s.overlayByVer = map[int]*overlaySet{}
	}
	if _, ok := s.overlayByVer[v]; !ok {
		s.overlayVers = append(s.overlayVers, v)
	}
	s.overlayByVer[v] = set
	for len(s.overlayVers) > overlayKeep {
		delete(s.overlayByVer, s.overlayVers[0])
		s.overlayVers = s.overlayVers[1:]
	}
}
