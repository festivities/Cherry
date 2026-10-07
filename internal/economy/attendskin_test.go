package economy

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"cherry/internal/httpx"
)

// skinGet mirrors the web mux's attendance-skin branch (GET path, 404 otherwise).
func skinGet(p string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	if !ServeAttendanceSkin(rec, httptest.NewRequest("GET", p, nil).URL.Path) {
		httpx.ServeNotFound(rec)
	}
	return rec
}

func TestAttendanceSkinServed(t *testing.T) {
	v := "/arts_attendance_en_00134_1/"
	if rec := skinGet(v + "DailybonusMainPopupNew.plist"); rec.Code != 200 || !bytes.HasPrefix(rec.Body.Bytes(), []byte("<?xml")) {
		t.Fatalf("plist %d", rec.Code)
	}
	ini := skinGet(v + "ini/DailybonusMainPopupNew.ini")
	if ini.Code != 200 || strings.Count(ini.Body.String(), "[") != len(attendFrameFiles)+len(attendCycle) {
		t.Fatalf("ini %d %q", ini.Code, ini.Body.String())
	}
	for _, n := range attendNames() {
		rec := skinGet(v + "UIImage_hd/" + n)
		if rec.Code != 200 || !bytes.HasPrefix(rec.Body.Bytes(), []byte("\x89PNG")) {
			t.Fatalf("%s %d", n, rec.Code)
		}
	}
	for _, p := range []string{v + "UIImage_hd/missing.png", v + "UIImage_hd/cherry_day08.png", v + "UIImage_hd/cherry_day1.png",
		v + "UIImage_hd/heart41_off.png", v + "other.plist", "/arts_attendance_en"} {
		if rec := skinGet(p); rec.Code != 404 {
			t.Fatalf("%s %d", p, rec.Code)
		}
	}
}

func TestAttendancePlistRepointed(t *testing.T) {
	b, err := attendPlist()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	served := map[string]bool{}
	for _, n := range attendNames() {
		served[n] = true
	}
	for _, m := range regexp.MustCompile(`UIResource/UIImage/([^<]+\.png)`).FindAllStringSubmatch(s, -1) {
		if !served[m[1]] {
			t.Errorf("plist references unserved %s", m[1])
		}
	}
	for _, old := range []string{"sp_cash_41", "heart41_off", "magic10_off", "faceshop.png", "vip_gold", "gachaticket"} {
		if strings.Contains(s, old) {
			t.Errorf("stock reward art %s still referenced", old)
		}
	}
	for d := 1; d <= len(attendCycle); d++ {
		if !strings.Contains(s, "UIResource/UIImage/"+attendTileName(d)) {
			t.Errorf("day %d tile missing", d)
		}
	}
	if len(s) < 100000 {
		t.Fatalf("plist suspiciously small: %d", len(s))
	}
}

func TestAttendanceImages(t *testing.T) {
	for d := 1; d <= len(attendCycle); d++ {
		b, err := attendFile(attendTileName(d))
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != 137 || img.Bounds().Dy() != 136 {
			t.Fatalf("day %d tile %v %v", d, img.Bounds(), err)
		}
	}
	b, err := attendFile("dailybonus_bg.png")
	if err != nil {
		t.Fatal(err)
	}
	if img, err := png.Decode(bytes.NewReader(b)); err != nil || img.Bounds().Dx() != 640 || img.Bounds().Dy() != 1746 {
		t.Fatalf("bg %v %v", img.Bounds(), err)
	}
	// different amounts give different art
	a, _ := attendFile(attendTileName(1))
	c, _ := attendFile(attendTileName(2))
	if bytes.Equal(a, c) {
		t.Fatal("day 1 and day 2 tiles identical")
	}
}

func TestAttendanceVersionFollowsCycle(t *testing.T) {
	old := attendCycle
	defer func() { attendCycle = old }()
	base := AttendScheduleID
	if base < 100 || base > 999 {
		t.Fatalf("schedule id %d not 3 digits", base)
	}
	attendCycle[0].Gems++
	if v := attendVersion(attendCycle); v == base {
		t.Fatalf("version did not change with the cycle: %d", v)
	}
}

// TestAttendancePreview writes the 7 day cells laid out like the client (set
// CHERRY_CALENDAR_PREVIEW to an output path).
func TestAttendancePreview(t *testing.T) {
	out := os.Getenv("CHERRY_CALENDAR_PREVIEW")
	if out == "" {
		t.Skip("CHERRY_CALENDAR_PREVIEW not set")
	}
	bgb, _ := attendFile("dailybonus_bg.png")
	bg, _ := png.Decode(bytes.NewReader(bgb))
	const W, H = 640, 1136
	// the 568pt layer is assumed bottom-left anchored; the bg is cropped to fit
	canvas := image.NewNRGBA(image.Rect(0, 0, W, H))
	draw.Draw(canvas, canvas.Bounds(), bg, image.Pt(0, 0), draw.Src)
	for i := 0; i < len(attendCycle); i++ {
		px, py := 11+60*(i%5), 342-60*(i/5) // cocos points, origin bottom-left
		tile, _ := png.Decode(bytes.NewReader(mustFile(t, attendTileName(i+1))))
		top := H - (py+5)*2 - 136
		draw.Draw(canvas, image.Rect(px*2, top, px*2+137, top+136), tile, image.Point{}, draw.Over)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(out, ".png")+"_bg.png", bgb, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustFile(t *testing.T, n string) []byte {
	t.Helper()
	b, err := attendFile(n)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
