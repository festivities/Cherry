package economy

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// carrierFixture is a synthetic file in the shape of mainhome_button.aniproj (PROCESS_SYMBOL dialect, CRLF line ends);
// it is not the original's text.
const carrierFixture = "<?xml version=\"1.0\" encoding=\"UTF-8\" ?>\r\n<ANIMATIONS name=\"NHNArts Animation\" version=\"0.48.72.61\">\r\n" +
	"    <PLISTS />\r\n    <PNGS>\r\n        <png path=\"./box/pos.png\" />\r\n    </PNGS>\r\n    <SYMBOLS>\r\n" +
	"        <symbol szName=\"button_00\" dwTotalFrame=\"14l\" dwFrameTime=\"33l\">\r\n            <layer dwIndex=\"0\" szName=\"Layer000\">\r\n" +
	"                <PROCESS_SYMBOL dwFrame=\"0\" szSymbolKey=\"pos.png\" dwKeepFrame=\"13\" />\r\n            </layer>\r\n        </symbol>\r\n" +
	"        <symbol szName=\"button_01\" dwTotalFrame=\"14l\" dwFrameTime=\"33l\">\r\n            <layer dwIndex=\"0\" szName=\"Layer000\">\r\n" +
	"                <PROCESS_SYMBOL dwFrame=\"0\" szSymbolKey=\"pos.png\" dwKeepFrame=\"13\" />\r\n            </layer>\r\n        </symbol>\r\n" +
	"    </SYMBOLS>\r\n</ANIMATIONS>\r\n"

// TestGardenIconCarrier: the original text stays verbatim (only lines are inserted), the originals keep their order, each
// picture gets one PNG line under -cherry/ and one symbol in the file's dialect and line ends; odd shapes and a file that
// already holds Cherry symbols are refused.
func TestGardenIconCarrier(t *testing.T) {
	out, err := buildGardenIconCarrier([]byte(carrierFixture), []string{"0123456789ab", "ba9876543210"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	syms, err := aniprojSymbols(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"button_00", "button_01", "cherry_gb_0123456789ab", "cherry_gb_ba9876543210"}
	if strings.Join(syms, ",") != strings.Join(want, ",") {
		t.Fatalf("symbols %v, want %v", syms, want)
	}
	for _, w := range []string{`<png path="./-cherry/cherry_gb_0123456789ab.png" />` + "\r\n", `<PROCESS_SYMBOL dwFrame="0" szSymbolKey="cherry_gb_ba9876543210.png" dwKeepFrame="29" />` + "\r\n"} {
		if !strings.Contains(s, w) {
			t.Fatalf("built file lacks %q:\n%s", w, s)
		}
	}
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Fatal("built file mixes line ends")
	}
	// Removing the inserted lines gives the original back byte for byte.
	var kept []string
	for _, line := range strings.SplitAfter(s, "\r\n") {
		if !strings.Contains(line, "cherry_gb_") && !strings.Contains(line, "<layer") && !strings.Contains(line, "</layer>") && !strings.Contains(line, "</symbol>") {
			kept = append(kept, line)
		}
	}
	var orig []string
	for _, line := range strings.SplitAfter(carrierFixture, "\r\n") {
		if !strings.Contains(line, "<layer") && !strings.Contains(line, "</layer>") && !strings.Contains(line, "</symbol>") {
			orig = append(orig, line)
		}
	}
	if strings.Join(kept, "") != strings.Join(orig, "") {
		t.Fatalf("original lines changed:\n%s", strings.Join(kept, ""))
	}
	if _, err := buildGardenIconCarrier(out, []string{"0123456789ab"}); err == nil {
		t.Fatal("a file that already holds Cherry symbols was accepted as the original")
	}
	if _, err := buildGardenIconCarrier([]byte("<ANIMATIONS><PNGS /><SYMBOLS></SYMBOLS></ANIMATIONS>"), []string{"0123456789ab"}); err == nil {
		t.Fatal("a file without a PNGS block was accepted")
	}
	// The newer dialect gets process/property symbols.
	newer := strings.ReplaceAll(carrierFixture, "PROCESS_SYMBOL", "property")
	out, err = buildGardenIconCarrier([]byte(newer), []string{"0123456789ab"})
	if err != nil || !strings.Contains(string(out), `<process dwProcessKey="0" numChild="1">`) {
		t.Fatalf("newer dialect: %v\n%s", err, out)
	}
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 128})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestNormalizeGardenIcon: PNG only, at most 256 a side; the result is a plain PNG with a stable 12-hex id.
func TestNormalizeGardenIcon(t *testing.T) {
	b, id, w, h, err := NormalizeGardenIcon(testPNG(t, 120, 100))
	if err != nil || w != 120 || h != 100 || len(id) != 12 {
		t.Fatalf("normalise = %v %q %dx%d", err, id, w, h)
	}
	if _, id2, _, _, _ := NormalizeGardenIcon(b); id2 != id {
		t.Fatalf("normalising twice changed the id %s -> %s", id, id2)
	}
	if _, err := png.Decode(bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"jpeg": []byte("\xff\xd8\xff\xe0 not a png"), "big": testPNG(t, 257, 10), "broken": []byte("\x89PNG\r\n\x1a\nbroken")} {
		if _, _, _, _, err := NormalizeGardenIcon(raw); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// TestGardenIconsOffWithoutLists: without the patcher lists (no streaming store) custom pictures are off, and a button
// that names one is left out of home_icon while an original-picture button stays.
func TestGardenIconsOffWithoutLists(t *testing.T) {
	testutil.UseDatabase(t)
	pinPopupClock(t, jstAt(10, 12, 0)) // no season icon
	b, id, w, h, err := NormalizeGardenIcon(testPNG(t, 64, 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddGardenIcon(store.GardenIcon{ID: id, Name: "Mine", Width: w, Height: h}, b, "dev-owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := RefreshGardenIcons(); err == nil || GardenIconsOff() == "" || GardenIconLive(GardenIconKey(id)) {
		t.Fatalf("refresh without lists: err %v, off %q", err, GardenIconsOff())
	}
	for _, btn := range []store.GardenButton{{Icon: GardenIconKey(id), Label: "Custom", Link: "goSomewhere(closet)"}, {Icon: "24_event_icon", Label: "Original", Link: "goSomewhere(closet)"}} {
		if err := store.AddGardenButton(btn, "dev-owner", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	_, res := homeIconReply(t)
	rows, _ := res["home_icon"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["iconId"] != "24_event_icon" || rows[0].(map[string]any)["iconSeq"] != 1.0 {
		t.Fatalf("home_icon = %#v, want only the original-picture button", rows)
	}
}
