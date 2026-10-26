package thumbs

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"cherry/internal/itemcode"
)

// Garden map packs and their item art (the generated landscapes' inputs). Both come from the read-only
// device backups: santi first, then lpbackup, the order the item archive uses too.

// lsMagic opens a .mworldpack.
const lsMagic = 0x20190712

// lsArchiveRoots are the device-backup files/ trees, santi first (DpItemRoot is santi's item folder). Fixed at
// start: a test that moves DpItemRoot does not move the landscape inputs.
var lsArchiveRoots = []string{
	filepath.Dir(DpItemRoot),
	filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lpbackup/jp.naver.lineplay.android/files`),
}

// mapPackPath is the pack of Garden map stem under the first archive root that has it.
func mapPackPath(stem string) (string, bool) {
	for _, root := range lsArchiveRoots {
		p := filepath.Join(root, "garden", stem+".mworldpack")
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// mapPack is a parsed .mworldpack: its msector records by name, and its tile atlas PNGs in file order.
type mapPack struct {
	sectors   map[string][]byte
	tileNames []string
	tiles     map[string][]byte
}

// readMapPack parses a pack: the magic word, the world record, the sector records and the tile records.
// Each record is a name, the uncompressed and the compressed size, and a zlib stream (landscape-gen objs.py).
func readMapPack(path string) (*mapPack, error) {
	d, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(d) < 4 || binary.LittleEndian.Uint32(d) != lsMagic {
		return nil, fmt.Errorf("%s: not a map pack", path)
	}
	_, _, pos, err := packRecord(d, 4) // the world record
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pk := &mapPack{sectors: map[string][]byte{}, tiles: map[string][]byte{}}
	n, pos, err := packU32(d, pos)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for range n {
		var name string
		var data []byte
		if name, data, pos, err = packRecord(d, pos); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		pk.sectors[name] = data
	}
	n, pos, err = packU32(d, pos)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for range n {
		var name string
		var data []byte
		if name, data, pos, err = packRecord(d, pos); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, seen := pk.tiles[name]; !seen {
			pk.tileNames = append(pk.tileNames, name)
		}
		pk.tiles[name] = data
	}
	return pk, nil
}

// packU32 reads a little-endian uint32 at pos and returns it with the position after it.
func packU32(d []byte, pos int) (int, int, error) {
	if pos < 0 || pos+4 > len(d) {
		return 0, 0, errors.New("truncated pack")
	}
	return int(binary.LittleEndian.Uint32(d[pos:])), pos + 4, nil
}

// packRecord reads one named, zlib-compressed record at pos and returns its data and the next position.
func packRecord(d []byte, pos int) (string, []byte, int, error) {
	nl, pos, err := packU32(d, pos)
	if err != nil || pos+nl > len(d) {
		return "", nil, 0, errors.New("truncated pack record name")
	}
	name := d[pos : pos+nl]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	pos += nl
	us, pos, err := packU32(d, pos)
	if err != nil {
		return "", nil, 0, err
	}
	cs, pos, err := packU32(d, pos)
	if err != nil || pos+cs > len(d) {
		return "", nil, 0, errors.New("truncated pack record data")
	}
	zr, err := zlib.NewReader(bytes.NewReader(d[pos : pos+cs]))
	if err != nil {
		return "", nil, 0, err
	}
	var buf bytes.Buffer
	buf.Grow(us)
	if _, err := io.Copy(&buf, zr); err != nil {
		return "", nil, 0, err
	}
	if buf.Len() != us {
		return "", nil, 0, fmt.Errorf("record %q: %d bytes, want %d", name, buf.Len(), us)
	}
	return string(name), buf.Bytes(), pos + cs, nil
}

// floorAtlas is the first tile PNG of the pack, the atlas of iso floor diamonds.
func (pk *mapPack) floorAtlas() ([]byte, error) {
	if len(pk.tileNames) == 0 {
		return nil, errors.New("map pack has no tile atlas")
	}
	return pk.tiles[pk.tileNames[0]], nil
}

// lsObject is a placed object: its item code and its tile, rounded to two decimals as the generator reads it.
type lsObject struct {
	code   string
	tx, ty float64
}

// objects reads every msector in name order, the order the generator sees the objects in.
func (pk *mapPack) objects() ([]lsObject, error) {
	names := make([]string, 0, len(pk.sectors))
	for name := range pk.sectors {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []lsObject
	for _, name := range names {
		objs, err := sectorObjects(pk.sectors[name])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, objs...)
	}
	return out, nil
}

// sectorObjects reads one msector: the object count at 413, then 40-byte records from 417 (a dword, the item
// id, the code length, the code at +12, the tile x and y as float32 at +30 and +34).
func sectorObjects(d []byte) ([]lsObject, error) {
	if len(d) < 417 {
		return nil, errors.New("msector too short")
	}
	n := int(binary.LittleEndian.Uint32(d[413:]))
	out := make([]lsObject, 0, min(n, len(d)/40))
	for k := range n {
		o := 417 + 40*k
		if o+38 > len(d) {
			return nil, errors.New("msector record past its end")
		}
		ln := int(binary.LittleEndian.Uint32(d[o+8:]))
		code := d[o+12 : min(o+12+ln, len(d))]
		if i := bytes.IndexByte(code, 0); i >= 0 {
			code = code[:i]
		}
		tx := float64(math.Float32frombits(binary.LittleEndian.Uint32(d[o+30:])))
		ty := float64(math.Float32frombits(binary.LittleEndian.Uint32(d[o+34:])))
		out = append(out, lsObject{code: string(code), tx: roundPy(tx, 2), ty: roundPy(ty, 2)})
	}
	return out, nil
}

// roundPy is Python's round(x, digits) for floats: the correctly rounded decimal, ties to even.
func roundPy(x float64, digits int) float64 {
	v, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', digits, 64), 64)
	if err != nil {
		return x
	}
	return v
}

// lsItemDir is the archive folder of item id under the first root that has one; "" when none does.
func lsItemDir(id int64) string {
	for _, root := range lsArchiveRoots {
		d := filepath.Join(root, "item", "interior", strconv.FormatInt(id, 10))
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return ""
}

// lsArt is what the generator reads from one item code's folder, read once per code.
type lsArt struct {
	wall        bool         // no folder, or cloud_/star_ backdrop parts: never a prop, an edge piece or a fence
	main        *image.NRGBA // the main layer; nil when the folder has no drawable part
	cloud, star *image.NRGBA // the backdrop sprites of a RUTD code
}

// loadLsArt reads the folder of a code (the item's art under interior/<id>).
func loadLsArt(code string) *lsArt {
	a := &lsArt{wall: true}
	id, ok := itemcode.ID(code)
	if !ok {
		return a
	}
	dir := lsItemDir(id)
	if dir == "" {
		return a
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return a
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	a.wall = false
	cloud, star := "", ""
	for _, n := range names {
		if strings.HasPrefix(n, "cloud_") || strings.HasPrefix(n, "star_") {
			a.wall = true
		}
		if strings.HasPrefix(n, "cloud_") && strings.HasSuffix(n, ".png") && cloud == "" {
			cloud = n // the first one
		}
		if strings.HasPrefix(n, "star_") && strings.HasSuffix(n, ".png") {
			star = n // the last one
		}
	}
	a.cloud = loadLsPNG(dir, cloud)
	a.star = loadLsPNG(dir, star)
	a.main = loadLsPNG(dir, lsMainLayer(dir, names))
	return a
}

// lsMainLayer picks the main layer file: fl_l0_a00.png, else the first *_l0_a00.png, else wall_l_1.png, else
// the largest png that is not a dp preview. "" when none is there.
func lsMainLayer(dir string, names []string) string {
	has := map[string]bool{}
	for _, n := range names {
		has[n] = true
	}
	if has["fl_l0_a00.png"] {
		return "fl_l0_a00.png"
	}
	for _, n := range names {
		if strings.HasSuffix(n, "_l0_a00.png") {
			return n
		}
	}
	if has["wall_l_1.png"] {
		return "wall_l_1.png"
	}
	best, bestArea := "", -1
	for _, n := range names {
		if !strings.HasSuffix(strings.ToLower(n), ".png") || strings.HasPrefix(n, "dp") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		cfg, err := png.DecodeConfig(f)
		f.Close()
		if err != nil {
			continue
		}
		if area := cfg.Width * cfg.Height; area > bestArea {
			best, bestArea = n, area
		}
	}
	return best
}

// loadLsPNG reads dir/name as 8-bit straight RGBA; nil for an empty name or a file that is not a PNG.
func loadLsPNG(dir, name string) *image.NRGBA {
	if name == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil
	}
	img, err := decodeNRGBA(b)
	if err != nil {
		return nil
	}
	return img
}

// decodeNRGBA reads a PNG as 8-bit straight RGBA at (0, 0), like PIL's convert('RGBA').
func decodeNRGBA(b []byte) (*image.NRGBA, error) {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	bd := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, bd.Dx(), bd.Dy()))
	draw.Draw(out, out.Bounds(), img, bd.Min, draw.Src)
	return out, nil
}
