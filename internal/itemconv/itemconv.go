// Package itemconv converts the 2014 iOS items of item.zip (the ios-mini-4.3-2014 archive) into LINE PLAY 10.1
// item folders: 1409_iteminfo.artsitem plus part PNGs. The rules are the pilot's tool/convert.py ported exactly
// (golden test against its output). Build runs once, in cmd/cherry-assets convert; the output is Cherry-made
// (the generated tree, uploaded under cherry/item/), never an original.
package itemconv

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"cherry/internal/itemcode"
)

// Recipe names the generator and its source (the D1 generated table and cherry/manifest/items.json).
const Recipe = "itemconv-v1 ios-mini-4.3-2014/item.zip"

// DpRecipe is the recipe of a dp.png: rendered by Cherry's item thumbnail generator from the converted parts.
const DpRecipe = Recipe + "; dp.png rendered by thumbs"

// Kinds are the item kinds the iOS archive has and the converter handles.
var Kinds = []string{"custom", "dress"}

// scale is the iOS-to-10.1 pixel and anchor factor (convert.py SCALE).
const scale = 3.5

// Item converts one iOS item. read returns the bytes of a file in the item folder (iteminfo.artsitem, the part
// PNGs). The result maps each output name (1409_iteminfo.artsitem, <side>_<k>.png) to its bytes. Any error
// (a missing part, an unreadable header) fails the whole item, as the pilot's convert did.
func Item(kind string, read func(name string) ([]byte, error)) (map[string][]byte, error) {
	raw, err := read("iteminfo.artsitem")
	if err != nil {
		return nil, err
	}
	ios, err := parseIOS(string(raw), kind)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	var info strings.Builder
	if kind == "dress" {
		fmt.Fprintf(&info, "0;0;%d;%d;0;", ios.hdr[0], ios.hdr[1])
	} else {
		info.WriteString("0;0;0;")
	}
	for side, rows := range [][]iosRow{ios.front, ios.back} {
		// Stable: parts with equal z keep their iOS order (convert.py sorted(..., key=-z)).
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].z > rows[j].z })
		fmt.Fprintf(&info, "%d;", len(rows))
		for k, r := range rows {
			name := fmt.Sprintf("%d_%d.png", side, k)
			src, err := read(r.name)
			if err != nil {
				return nil, fmt.Errorf("part %s: %w", r.name, err)
			}
			im, err := png.Decode(bytes.NewReader(src))
			if err != nil {
				return nil, fmt.Errorf("part %s: %w", r.name, err)
			}
			b := im.Bounds()
			w, h := scaledSize(b.Dx()), scaledSize(b.Dy())
			var buf bytes.Buffer
			if err := png.Encode(&buf, boxResize(toNRGBA(im), w, h)); err != nil {
				return nil, err
			}
			files[name] = buf.Bytes()
			fmt.Fprintf(&info, "%d;%d;0;0;%s;%s;%s;0;", r.anchor, r.z,
				strconv.FormatFloat(snapHalf(r.x/scale), 'f', 6, 64), strconv.FormatFloat(snapHalf(r.y/scale), 'f', 6, 64), name)
		}
	}
	files["1409_iteminfo.artsitem"] = []byte(info.String())
	return files, nil
}

// iosRow is one iOS part record: anchor;z;x;y;name.
type iosRow struct {
	anchor, z int
	x, y      float64
	name      string
}

type iosItem struct {
	hdr         []int // dress only: the iOS header pair a, b
	front, back []iosRow
}

// parseIOS reads an iOS iteminfo.artsitem (convert.py parse_ios). dress: a;b then per side a count and rows;
// custom: per side a count and rows. Trailing fields are an error.
func parseIOS(text, kind string) (iosItem, error) {
	f := strings.Split(text, ";")
	if f[len(f)-1] == "" {
		f = f[:len(f)-1]
	}
	var it iosItem
	i := 0
	if kind == "dress" {
		if len(f) < 2 {
			return it, fmt.Errorf("iOS iteminfo: short header")
		}
		a, err1 := pyInt(f[0])
		b, err2 := pyInt(f[1])
		if err1 != nil || err2 != nil {
			return it, fmt.Errorf("iOS iteminfo: bad header")
		}
		it.hdr = []int{a, b}
		i = 2
	}
	for side := 0; side < 2; side++ {
		if i >= len(f) {
			return it, fmt.Errorf("iOS iteminfo: missing side count")
		}
		n, err := pyInt(f[i])
		if err != nil {
			return it, err
		}
		i++
		var rows []iosRow
		for k := 0; k < n; k++ {
			if i+5 > len(f) {
				return it, fmt.Errorf("iOS iteminfo: short row")
			}
			anchor, err1 := pyInt(f[i])
			z, err2 := pyInt(f[i+1])
			x, err3 := pyFloat(f[i+2])
			y, err4 := pyFloat(f[i+3])
			if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
				return it, fmt.Errorf("iOS iteminfo: bad row")
			}
			rows = append(rows, iosRow{anchor: anchor, z: z, x: x, y: y, name: f[i+4]})
			i += 5
		}
		if side == 0 {
			it.front = rows
		} else {
			it.back = rows
		}
	}
	if i != len(f) {
		return it, fmt.Errorf("iOS iteminfo: %d trailing fields", len(f)-i)
	}
	return it, nil
}

// pyInt and pyFloat accept what Python's int() and float() accept for these fields (surrounding space).
func pyInt(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }

func pyFloat(s string) (float64, error) { return strconv.ParseFloat(strings.TrimSpace(s), 64) }

// snapHalf rounds up to the next half pixel (convert.py snap_half: every 10.1 anchor is a multiple of 0.5).
// Python's math.ceil returns an int, so a result of zero is +0.0 here, never -0.0 (the artsitem prints -0.000000 otherwise).
func snapHalf(v float64) float64 {
	c := math.Ceil(v*2 - 1e-9)
	if c == 0 {
		c = 0
	}
	return c / 2
}

// scaledSize is ceil(n / 3.5), at least 1 (convert.py scaled_size).
func scaledSize(n int) int { return max(1, int(math.Ceil(float64(n)/scale))) }

// toNRGBA copies any decoded PNG into a zero-origin NRGBA (PIL's convert('RGBA')).
func toNRGBA(im image.Image) *image.NRGBA {
	b := im.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), im, b.Min, draw.Src)
	return out
}

// boxResize is PIL's Image.resize((w, h), BOX) of an RGBA image: premultiplied, a horizontal then a vertical
// box pass, each rounded to 8 bits, then unpremultiplied. src has its origin at zero.
func boxResize(src *image.NRGBA, w, h int) *image.NRGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	pre := make([]uint8, sw*sh*4)
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			si := y*src.Stride + x*4
			a := src.Pix[si+3]
			di := (y*sw + x) * 4
			for c := 0; c < 3; c++ {
				pre[di+c] = muldiv255(src.Pix[si+c], a)
			}
			pre[di+3] = a
		}
	}
	mid := resample(pre, sw, sh, w, true)
	out := resample(mid, w, sh, h, false)
	res := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		a := out[i*4+3]
		res.Pix[i*4+3] = a
		if a == 0 {
			continue
		}
		for c := 0; c < 3; c++ {
			res.Pix[i*4+c] = uint8(min(255, int(math.Floor(float64(out[i*4+c])*255/float64(a)+0.5))))
		}
	}
	return res
}

// muldiv255 is round(a*b/255) the way Pillow's MULDIV255 computes it.
func muldiv255(a, b uint8) uint8 {
	t := int(a)*int(b) + 128
	return uint8(((t >> 8) + t) >> 8)
}

// resample runs one box pass over the premultiplied RGBA buffer in (inW x inH), producing outN columns
// (horizontal) or rows (vertical).
func resample(in []uint8, inW, inH, outN int, horizontal bool) []uint8 {
	inN, outW, outH := inH, inW, outN
	if horizontal {
		inN, outW, outH = inW, outN, inH
	}
	lo, cnt := boxTaps(inN, outN)
	out := make([]uint8, outW*outH*4)
	for y := 0; y < outH; y++ {
		for x := 0; x < outW; x++ {
			k := x
			if !horizontal {
				k = y
			}
			for c := 0; c < 4; c++ {
				sum := 0.0
				for t := 0; t < cnt[k]; t++ {
					if horizontal {
						sum += float64(in[(y*inW+lo[k]+t)*4+c])
					} else {
						sum += float64(in[((lo[k]+t)*inW+x)*4+c])
					}
				}
				v := 0.0
				if cnt[k] > 0 {
					v = sum / float64(cnt[k])
				}
				out[(y*outW+x)*4+c] = uint8(math.Floor(v + 0.5))
			}
		}
	}
	return out
}

// boxTaps gives, for each output index, the first input index and the number of inputs it averages. Pillow's
// precompute_coeffs with the BOX filter (support 0.5): an input counts when its centre lies in the output's
// span, so 3.5:1 keeps 3 or 4 inputs with equal weight.
func boxTaps(inN, outN int) (lo, cnt []int) {
	lo, cnt = make([]int, outN), make([]int, outN)
	scale := float64(inN) / float64(outN)
	filterscale := max(scale, 1.0)
	support := 0.5 * filterscale
	ss := 1.0 / filterscale
	for xx := 0; xx < outN; xx++ {
		center := (float64(xx) + 0.5) * scale
		xmin := max(int(center-support+0.5), 0)
		xmax := min(int(center+support+0.5), inN) - xmin
		first := xmin
		for x := 0; x < xmax; x++ {
			t := (float64(x+xmin) - center + 0.5) * ss
			if t > -0.5 && t <= 0.5 {
				if cnt[xx] == 0 {
					first = x + xmin
				}
				cnt[xx]++
			}
		}
		lo[xx] = first
	}
	return lo, cnt
}

// Options is one batch run of Build.
type Options struct {
	Zip      string                                // item.zip, the read-only 2014 archive
	Out      string                                // output root: Out/<kind>/<id>/ folders, rebuilt from scratch
	Original func(kind string, id int64) bool      // true when a device backup already has the item (originals win)
	Render   func(dir, kind string) ([]byte, bool) // dp.png of a converted folder; ok is false when it has no drawable art
}

// Output is one file of the converted tree.
type Output struct {
	Kind, Name string
	ID         int64
	Size       int64
	MD5        string // uppercase hex
	Recipe     string
}

// Report counts what Build did, per kind.
type Report struct {
	Converted, Originals, Undecodable, NoArt, Failed map[string]int
	Errors                                           []string
	Files                                            []Output // sorted by kind, id, name
	Bytes                                            int64
}

// Build converts every iOS item of Kinds that no device backup has. Items whose category itemcode cannot decode
// are counted and skipped (the 2014 category letters are unknown to the client), as are items with no drawable
// art. Output folders are deterministic: a rerun writes the same bytes.
func Build(opt Options) (*Report, error) {
	zr, err := zip.OpenReader(opt.Zip)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	entries := map[string]*zip.File{}
	ids := map[string][]int64{}
	for _, f := range zr.File {
		p := strings.Split(f.Name, "/")
		if len(p) != 4 || p[3] == "" || p[0] != "item" {
			continue
		}
		entries[f.Name] = f
		id, err := strconv.ParseInt(p[2], 10, 64)
		if p[3] == "iteminfo.artsitem" && err == nil && strconv.FormatInt(id, 10) == p[2] && slices.Contains(Kinds, p[1]) {
			ids[p[1]] = append(ids[p[1]], id)
		}
	}
	rep := &Report{Converted: map[string]int{}, Originals: map[string]int{}, Undecodable: map[string]int{},
		NoArt: map[string]int{}, Failed: map[string]int{}}
	for _, kind := range Kinds {
		if err := os.RemoveAll(filepath.Join(opt.Out, kind)); err != nil {
			return nil, err
		}
		sort.Slice(ids[kind], func(i, j int) bool { return ids[kind][i] < ids[kind][j] })
		for _, id := range ids[kind] {
			if opt.Original != nil && opt.Original(kind, id) {
				rep.Originals[kind]++
				continue
			}
			if _, ok := itemcode.Code(id); !ok {
				rep.Undecodable[kind]++
				continue
			}
			idText := strconv.FormatInt(id, 10)
			read := func(name string) ([]byte, error) {
				f := entries["item/"+kind+"/"+idText+"/"+name]
				if f == nil || strings.Contains(name, "/") {
					return nil, fmt.Errorf("no %s in the archive", name)
				}
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
			files, err := Item(kind, read)
			if err != nil {
				rep.Failed[kind]++
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s/%s: %v", kind, idText, err))
				continue
			}
			dir := filepath.Join(opt.Out, kind, idText)
			if err := writeFolder(dir, files); err != nil {
				return nil, err
			}
			if opt.Render != nil {
				dp, ok := opt.Render(dir, kind)
				if !ok {
					rep.NoArt[kind]++
					if err := os.RemoveAll(dir); err != nil {
						return nil, err
					}
					continue
				}
				files["dp.png"] = dp
				if err := os.WriteFile(filepath.Join(dir, "dp.png"), dp, 0o644); err != nil {
					return nil, err
				}
			}
			rep.Converted[kind]++
			names := make([]string, 0, len(files))
			for name := range files {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				b := files[name]
				sum := md5.Sum(b)
				out := Output{Kind: kind, Name: name, ID: id, Size: int64(len(b)), MD5: strings.ToUpper(hex.EncodeToString(sum[:])), Recipe: Recipe}
				if name == "dp.png" {
					out.Recipe = DpRecipe
				}
				rep.Files = append(rep.Files, out)
				rep.Bytes += out.Size
			}
		}
	}
	return rep, nil
}

func writeFolder(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
