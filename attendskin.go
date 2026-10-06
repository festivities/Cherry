package main

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ---- calendar skin ----
//
// The calendar layer loads its UI from files/DailyBonus/. NaAttendanceMainLayer::InitList
// @0x19194dc only toggles visibility per cell: for bonus row i (sDataDailyAttendance is 224
// bytes, +4 = attendYN) it shows node tag 164+3i (IMG_DAYnn_ON, the check stamp) when
// attended, else tag 165+3i (IMG_DAYnn_OFF, the reward tile); nodes 163+3i.. for i >= rows
// are hidden (the layout holds 25 cells, 5 per row). No amount label is read from info.bonus:
// every reward is baked into the OFF image named by the plist. So the amounts can only be
// changed by serving a different skin. CheckVersion (NaAttendanceManager @0x191b870) compares
// CCUserDefault "AttendanceVersion" with "arts_attendance_<lang>_<%05d dailyBonusId>_<scheduleId>/"
// (32-byte snprintf buffer, so keep scheduleId <= 3 digits); on a mismatch the client downloads
// <ver>DailybonusMainPopupNew.plist, <ver>ini/DailybonusMainPopupNew.ini (names in brackets,
// rebuilt into the sqlite attendancersc table, previous rows are deleted from disk) and
// <ver>UIImage_hd/<name> for each listed name, then saves the key.
//
// Cherry serves a generated skin: the stock plist with every IMG_DAYnn_OFF re-pointed at a
// Cherry tile (cherry_dayNN.png) built from attendCycle (stock white tile + the game's own
// gem/cash/ticket icons + a bitmap-digit amount), the stock frame images, and the background
// with its baked-in "4" painted out. The version key is derived from attendCycle, so changing
// an amount changes scheduleId and every client re-downloads once.

// attendSkinRev: bump when the generated art changes without attendCycle changing.
const attendSkinRev = "2" // 2: "DAY n" header on every tile

// attendScheduleID is the skin version (3 digits, see above), derived from the reward table.
var attendScheduleID = attendVersion(attendCycle)

func attendVersion(c [7]struct {
	Type    string
	Gems    int64
	Cash    int64
	Tickets int64
}) int {
	return 100 + int(crc32.ChecksumIEEE([]byte(fmt.Sprint(c, attendSkinRev)))%900)
}

// Game assets are read in place from read-only archives; never committed (AGENTS.md).
var (
	attendanceSkin fs.FS = os.DirFS(filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lpbackup/jp.naver.lineplay.android/files/DailyBonus`))
	attendIconFS   fs.FS = os.DirFS(filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lpbackup/jp.naver.lineplay.android/files/UIResource/UIImage_hd`))
)

const (
	attendanceIni  = "DailybonusMainPopupNew.ini"
	attendanceBase = "UIImage_hd/heart41_off.png" // white tile, heart + "41" are painted out
	attendTilePfx  = "cherry_day"
	attendUIPrefix = "UIResource/UIImage/"
)

// attendFrameFiles are the stock images the plist still references, served unchanged
// (dailybonus_bg.png is patched).
var attendFrameFiles = []string{
	"1505_common_deco_black1px.png", "alpha1px.png", "check_type_1.png", "check_type_2.png",
	"common_top_back_x_01.png", "common_top_close_x.png", "dailybonus_bg.png", "tag_complete.png",
	"text_complete.png", "text_fail.png", "text_tap.png", "text_under.png",
}

func attendTileName(day int) string { return fmt.Sprintf("%s%02d.png", attendTilePfx, day) }

// attendNames lists every file of the skin (the ini content).
func attendNames() []string {
	names := append([]string(nil), attendFrameFiles...)
	for d := 1; d <= len(attendCycle); d++ {
		names = append(names, attendTileName(d))
	}
	sort.Strings(names)
	return names
}

// attendPlist rewrites the stock plist: each IMG_DAYnn_OFF filepath becomes the Cherry tile
// for that day, or the 1px transparent image for cells beyond the cycle (they are hidden).
// In the plist XML a node's "filepath" key precedes its "nameTag".
func attendPlist() ([]byte, error) {
	raw, err := fs.ReadFile(attendanceSkin, "DailybonusMainPopupNew.plist")
	if err != nil {
		return nil, err
	}
	const marker = "<key>filepath</key><string>"
	parts := strings.Split(string(raw), marker)
	for i := 1; i < len(parts); i++ {
		seg := parts[i]
		tag := strings.Index(seg, "<key>nameTag</key><string>IMG_DAY")
		if tag < 0 {
			continue
		}
		rest := seg[tag+len("<key>nameTag</key><string>IMG_DAY"):]
		if len(rest) < 6 || rest[2:6] != "_OFF" {
			continue
		}
		day, err := strconv.Atoi(rest[:2])
		if err != nil {
			continue
		}
		name := "alpha1px.png"
		if day <= len(attendCycle) {
			name = attendTileName(day)
		}
		end := strings.Index(seg, "</string>")
		parts[i] = attendUIPrefix + name + seg[end:]
	}
	return []byte(strings.Join(parts, marker)), nil
}

// attendFile returns one generated or passthrough image of the skin.
func attendFile(name string) ([]byte, error) {
	if strings.HasPrefix(name, attendTilePfx) {
		d, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, attendTilePfx), ".png"))
		if err != nil || d < 1 || d > len(attendCycle) || name != attendTileName(d) {
			return nil, fs.ErrNotExist
		}
		return encodePNG(attendTile(d))
	}
	if name == "dailybonus_bg.png" {
		return attendBG()
	}
	for _, f := range attendFrameFiles {
		if f == name {
			return fs.ReadFile(attendanceSkin, "UIImage_hd/"+name)
		}
	}
	return nil, fs.ErrNotExist
}

func serveAttendanceSkin(w http.ResponseWriter, p string) bool {
	rest, ok := strings.CutPrefix(p, "/arts_attendance_")
	if !ok {
		return false
	}
	_, rel, ok := strings.Cut(rest, "/")
	if !ok {
		return false
	}
	var data []byte
	var err error
	switch {
	case rel == "DailybonusMainPopupNew.plist":
		data, err = attendPlist()
	case rel == "ini/"+attendanceIni:
		data = []byte("[" + strings.Join(attendNames(), "][") + "]")
	case strings.HasPrefix(rel, "UIImage_hd/") && path.Base(rel) == rel[len("UIImage_hd/"):]:
		data, err = attendFile(rel[len("UIImage_hd/"):])
	default:
		return false
	}
	if err != nil {
		serveNotFound(w)
		return true
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return true
}

// ---- image generation (stdlib only) ----

func encodePNG(img image.Image, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func loadNRGBA(fsys fs.FS, name string) (*image.NRGBA, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	dst := image.NewNRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst, nil
}

// scaleTo resamples with a premultiplied box filter (at least one source pixel per output
// pixel, so upscaling degrades to nearest neighbour; icons here are only shrunk).
func scaleTo(src image.Image, w, h int) *image.NRGBA {
	b := src.Bounds()
	sw, sh := float64(b.Dx())/float64(w), float64(b.Dy())/float64(h)
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0 := int(math.Floor(float64(y) * sh))
		y1 := max(int(math.Ceil(float64(y+1)*sh)), y0+1)
		for x := 0; x < w; x++ {
			x0 := int(math.Floor(float64(x) * sw))
			x1 := max(int(math.Ceil(float64(x+1)*sw)), x0+1)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1 && sy < b.Dy(); sy++ {
				for sx := x0; sx < x1 && sx < b.Dx(); sx++ {
					pr, pg, pb, pa := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r, g, bl, a, n = r+uint64(pr), g+uint64(pg), bl+uint64(pb), a+uint64(pa), n+1
				}
			}
			if n == 0 || a == 0 {
				continue
			}
			c := color.RGBA64{uint16(r / n), uint16(g / n), uint16(bl / n), uint16(a / n)}
			dst.Set(x, y, color.NRGBAModel.Convert(c))
		}
	}
	return dst
}

// fitInto shrinks src to fit a w x h box, keeping the aspect ratio.
func fitInto(src image.Image, w, h int) *image.NRGBA {
	b := src.Bounds()
	f := math.Min(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	return scaleTo(src, max(1, int(float64(b.Dx())*f)), max(1, int(float64(b.Dy())*f)))
}

// digit5x7 is a 5x7 bitmap font for 0-9, one string per row.
var digit5x7 = [10][7]string{
	{"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	{"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	{"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	{"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	{"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	{"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	{"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	{"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	{"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	{"01110", "10001", "10001", "01111", "00001", "00010", "01100"},
}

// letter5x7 adds the glyphs needed for the "DAY n" tile header (space = blank).
var letter5x7 = map[rune][7]string{
	'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
	' ': {"00000", "00000", "00000", "00000", "00000", "00000", "00000"},
}

func glyph5x7(ch rune) [7]string {
	if ch >= '0' && ch <= '9' {
		return digit5x7[ch-'0']
	}
	return letter5x7[ch]
}

var attendInk = color.NRGBA{96, 96, 148, 255}

// textSize is the pixel size of n drawn at digit scale sc (1px gap = sc).
func textSize(n int64, sc int) (w, h int) { return strSize(strconv.FormatInt(n, 10), sc) }

func strSize(t string, sc int) (w, h int) { return len(t)*6*sc - sc, 7 * sc }

// drawNumber draws n with its top-left at (x, y); it renders 4x larger and shrinks for
// soft edges.
func drawNumber(dst *image.NRGBA, n int64, x, y, sc int) {
	drawText(dst, strconv.FormatInt(n, 10), x, y, sc)
}

// drawText draws t (digits, D, A, Y, space) with its top-left at (x, y).
func drawText(dst *image.NRGBA, t string, x, y, sc int) {
	const ss = 4
	w, h := strSize(t, sc)
	big := image.NewNRGBA(image.Rect(0, 0, w*ss, h*ss))
	px := sc * ss
	for i, ch := range t {
		g := glyph5x7(ch)
		for r, row := range g {
			for c, bit := range row {
				if bit == '1' {
					x0, y0 := (i*6+c)*px, r*px
					draw.Draw(big, image.Rect(x0, y0, x0+px, y0+px), image.NewUniform(attendInk), image.Point{}, draw.Src)
				}
			}
		}
	}
	small := scaleTo(big, w, h)
	draw.Draw(dst, image.Rect(x, y, x+w, y+h), small, image.Point{}, draw.Over)
}

type attendReward struct {
	icon string
	n    int64
}

func attendRewards(day int) []attendReward {
	r := attendCycle[day-1]
	var out []attendReward
	if r.Gems > 0 {
		out = append(out, attendReward{"common_rewardpopup_gem_2.png", r.Gems})
	}
	if r.Cash > 0 {
		out = append(out, attendReward{"common_rewardpopup_cash_2.png", r.Cash})
	}
	if r.Tickets > 0 {
		out = append(out, attendReward{"settings_list_deco_faceshop_ticket.png", r.Tickets})
	}
	return out
}

// Tile geometry in the 137x136 stock canvas: the opaque tile spans x 6..112, y 22..127.
const tileCX = 59

// attendTile builds the reward tile of one day: one reward is drawn as a big icon with the
// amount below; several rewards are stacked as rows of small icon + amount.
func attendTile(day int) (*image.NRGBA, error) {
	img, err := loadNRGBA(attendanceSkin, attendanceBase)
	if err != nil {
		return nil, err
	}
	// Paint the old heart/number out: opaque pixels inside the tile interior become white.
	for y := 28; y <= 119; y++ {
		for x := 10; x <= 108; x++ {
			if img.NRGBAAt(x, y).A > 200 {
				img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
			}
		}
	}
	label := fmt.Sprintf("DAY %d", day)
	lw, _ := strSize(label, 2)
	drawText(img, label, tileCX-lw/2, 30, 2)
	rw := attendRewards(day)
	place := func(icon *image.NRGBA, cx, cy int) {
		b := icon.Bounds()
		draw.Draw(img, image.Rect(cx-b.Dx()/2, cy-b.Dy()/2, cx-b.Dx()/2+b.Dx(), cy-b.Dy()/2+b.Dy()), icon, b.Min, draw.Over)
	}
	for i, r := range rw {
		src, err := loadNRGBA(attendIconFS, r.icon)
		if err != nil {
			return nil, err
		}
		if len(rw) == 1 {
			place(fitInto(src, 50, 50), tileCX, 72)
			w, _ := textSize(r.n, 3)
			drawNumber(img, r.n, tileCX-w/2, 98, 3)
			continue
		}
		cy := 58 + 22*i
		place(fitInto(src, 22, 22), 28, cy)
		_, h := textSize(r.n, 2)
		drawNumber(img, r.n, 48, cy-h/2, 2)
	}
	return img, nil
}

// attendBG returns the stock background with the event's baked-in giant "4" painted over:
// each row is a horizontal blend of the clean strips left (x 228..243) and right (x 388..393)
// of the digit, feathered into the original at the edges.
func attendBG() ([]byte, error) {
	img, err := loadNRGBA(attendanceSkin, "UIImage_hd/dailybonus_bg.png")
	if err != nil {
		return nil, err
	}
	const x0, x1, y0, y1, feather = 248, 388, 430, 596, 10
	orig := image.NewNRGBA(img.Bounds())
	copy(orig.Pix, img.Pix)
	strip := func(y, xa, xb int) (s [3]float64) {
		for x := xa; x < xb; x++ {
			c := orig.NRGBAAt(x, y)
			s[0] += float64(c.R) / float64(xb-xa)
			s[1] += float64(c.G) / float64(xb-xa)
			s[2] += float64(c.B) / float64(xb-xa)
		}
		return s
	}
	for y := y0 - feather; y < y1+feather; y++ {
		l, r := strip(y, 228, 244), strip(y, 388, 394)
		if y >= 585 { // the right girl's hair reaches x 390 down here
			r = strip(y, 378, 384)
		}
		if math.Abs(l[0]-r[0])+math.Abs(l[1]-r[1])+math.Abs(l[2]-r[2]) > 60 {
			l = r // the left strip caught the girl's hair; use the clean side only
		}
		for x := x0 - feather; x < x1+feather; x++ {
			d := min(x-(x0-feather), x1+feather-1-x, y-(y0-feather), y1+feather-1-y)
			t := float64(min(d+1, feather)) / float64(feather)
			u := float64(x-(x0-feather)) / float64(x1-x0+2*feather)
			o := orig.NRGBAAt(x, y)
			mix := func(a uint8, i int) uint8 {
				return uint8(float64(a)*(1-t) + (l[i]*(1-u)+r[i]*u)*t + 0.5)
			}
			img.SetNRGBA(x, y, color.NRGBA{mix(o.R, 0), mix(o.G, 1), mix(o.B, 2), 255})
		}
	}
	return encodePNG(img, nil)
}
