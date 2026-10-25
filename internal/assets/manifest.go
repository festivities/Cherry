package assets

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// Manifest lists one bulk category as the client's update list needs it. The upload tool writes it to
// R2 as manifest/<cat>.json; Cherry reads it once per process.
type Manifest struct {
	Cat     string  `json:"cat"`
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Entry is one file of a category. Path is relative to the category root (no leading "/"); the R2
// key is static/<cat>/<path>. MD5 is the uppercase hex digest of the bytes.
type Entry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	MD5  string `json:"md5"`
}

// Version derives a manifest's checkresource2 version from its entries (in manifest order). Any path,
// size or MD5 change makes a new version, and identical content keeps the old one. The result is
// 1..99999 (the client compares it with atoi and builds the list URL with %05d).
func Version(entries []Entry) int {
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%d\x00%s\n", e.Path, e.Size, strings.ToUpper(e.MD5))
	}
	return int(binary.BigEndian.Uint32(h.Sum(nil)[:4])%99999) + 1
}

// PackDirs are the map pack directories. The client keeps a pack in files/<dir>/.
var PackDirs = []string{"garden", "world"}

// PackEntry is one map pack, stored at R2 key static/<dir>/<name>. Name is "<digits>.mworldpack" (ValidPack).
type PackEntry struct {
	Dir  string `json:"dir"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	MD5  string `json:"md5"`
}

// PackManifest is manifest/packs.json.
type PackManifest struct {
	Entries []PackEntry `json:"entries"`
}

// ValidPack reports whether dir/name is a map pack name: a PackDirs directory and a "<digits>.mworldpack" file
// name, so no name can carry a path.
func ValidPack(dir, name string) bool {
	base, ok := strings.CutSuffix(name, ".mworldpack")
	return ok && slices.Contains(PackDirs, dir) && isDigits(base)
}

// AnimationVersion is the checkresource2 version of the animation category: its entries plus every map pack, so
// adding, removing or changing a pack changes it. Without packs it is Version(anim).
func AnimationVersion(anim []Entry, packs []PackEntry) int {
	all := slices.Clone(anim)
	for _, p := range packs {
		all = append(all, Entry{Path: p.Dir + "/" + p.Name, Size: p.Size, MD5: p.MD5})
	}
	return Version(all)
}

// IniBody renders the update list in the client's grammar: one "[start=1,end=1,size=N,/path:version=MD5]"
// record per file (the client splits records on "]"). An empty category is "[]", never an empty body. The map
// packs follow the category's own records as "/../<dir>/<name>" records: the client writes them through
// files/animation/.., which exists only after one of its own records, so with no records there are no pack
// records either.
func (m *Manifest) IniBody(packs []PackEntry) string {
	if len(m.Entries) == 0 {
		return "[]"
	}
	lines := make([]string, 0, len(m.Entries)+len(packs)+1)
	if len(packs) > 0 {
		sum := md5.Sum([]byte(DirProbeBody))
		lines = append(lines, iniRecord(int64(len(DirProbeBody)), DirProbePath, hex.EncodeToString(sum[:])))
	}
	for _, e := range m.Entries {
		lines = append(lines, iniRecord(e.Size, e.Path, e.MD5))
	}
	for _, p := range packs {
		lines = append(lines, iniRecord(p.Size, "../"+p.Dir+"/"+p.Name, p.MD5))
	}
	return strings.Join(lines, "\n")
}

// DirProbePath is the first animation record whenever packs are listed. The client downloads the list in
// path order and opens each file under files/animation/ without creating that folder for "/../" records,
// so on a fresh install the first pack failed with a local Network Error (patchfail-spec.md). "-" sorts
// before "." and the subfolder makes the client create files/animation/-cherry/ (and files/animation/)
// before any pack. DirProbeBody is what the route serves for it.
const (
	DirProbePath = "-cherry/dir"
	DirProbeBody = "cherry"
)

func iniRecord(size int64, path, md5 string) string {
	return fmt.Sprintf("[start=1,end=1,size=%d,/%s:version=%s]", size, path, strings.ToUpper(md5))
}

// check refuses a pack manifest the routes must not serve: unsafe or duplicate names, sizes the client would
// drop, or bad digests.
func (p *PackManifest) check() error {
	seen := make(map[string]bool, len(p.Entries))
	for _, e := range p.Entries {
		key := e.Dir + "/" + e.Name
		switch {
		case !ValidPack(e.Dir, e.Name):
			return fmt.Errorf("pack manifest: unsafe pack %q", key)
		case seen[key]:
			return fmt.Errorf("pack manifest: duplicate pack %q", key)
		case e.Size <= 0:
			return fmt.Errorf("pack manifest: %s has size %d", key, e.Size)
		case !isMD5(e.MD5):
			return fmt.Errorf("pack manifest: %s has no MD5", key)
		}
		seen[key] = true
	}
	return nil
}

// check refuses a manifest the routes must not serve: wrong category, unsafe or duplicate paths, sizes the
// client would drop, bad digests, or a version that does not match its entries.
func (m *Manifest) check(cat string) error {
	if m.Cat != cat {
		return fmt.Errorf("manifest %s: names category %q", cat, m.Cat)
	}
	seen := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		switch {
		case !safeRel(e.Path):
			return fmt.Errorf("manifest %s: unsafe path %q", cat, e.Path)
		case seen[e.Path]:
			return fmt.Errorf("manifest %s: duplicate path %q", cat, e.Path)
		case e.Size <= 0:
			return fmt.Errorf("manifest %s: %s has size %d", cat, e.Path, e.Size)
		case !isMD5(e.MD5):
			return fmt.Errorf("manifest %s: %s has no MD5", cat, e.Path)
		}
		seen[e.Path] = true
	}
	if m.Version != Version(m.Entries) {
		return fmt.Errorf("manifest %s: version %d does not match its entries", cat, m.Version)
	}
	return nil
}

// checkResource2Body is checkresource2.json: the version of each bulk category. he, ch and squareui are
// never requested by the launcher flow and stay "0"; subui "0" would be a kill switch, so it is never 0 here.
func checkResource2Body(versions map[string]int) string {
	return fmt.Sprintf(`{"adr":{"10.1.0.0":"1"},"resource":{"1":{"animation":"%d","he":"0","ch":"0","sound":"%d","subui":"%d","tx":"%d","squareui":"0","ui":"%d"}}}`,
		versions["animation"], versions["sound"], versions["subui"], versions["tx"], versions["ui"])
}

// safeRel reports whether p is a clean relative path that cannot escape its category or break the
// update list grammar.
func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\:[]\r\n") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func isMD5(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
