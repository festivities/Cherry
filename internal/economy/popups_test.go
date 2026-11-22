package economy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
)

// discordPopup is an operator popup in the shape of the Discord banner. The invite is the documentation host, never the
// server's real one.
const discordPopup = `{"no": 9350, "title": "Cherry Discord", "sub": "News, help and events on our Discord", "caution": "",
	"link": "openWithBrowser", "url": "https://discord.gg/example", "button_skin": 4,
	"background": "#1c3a5e", "text": "#ffffff", "image": "", "start": "", "end": ""}`

// popupsFileText is a popups.json document holding entries in order.
func popupsFileText(entries ...string) string {
	return `{"origin": "cherry", "note": "test popups", "popups": [` + strings.Join(entries, ",") + `]}`
}

// goPopup is a popup that opens a native scene, numbered no, shown in the game days start to end ("" = open).
func goPopup(no int, start, end string) string {
	return fmt.Sprintf(`{"no": %d, "title": "Scene %d", "sub": "Opens a scene", "caution": "", "link": "goSomewhere(gachaShop)",
		"url": "", "button_skin": 4, "background": "#1c3a5e", "text": "#ffffff", "image": "", "start": %q, "end": %q}`, no, no, start, end)
}

// TestOperatorPopupsDefaultIsEmpty: the embedded default passes the checks, is Cherry-made and lists nothing.
func TestOperatorPopupsDefaultIsEmpty(t *testing.T) {
	if _, err := opdata.Decode(data.PopupsJSON, checkOperatorPopups); err != nil {
		t.Fatalf("embedded default refused: %v", err)
	}
	p := ActiveOperatorPopups()
	if p.Origin != "cherry" || strings.TrimSpace(p.Note) == "" || len(p.Popups) != 0 {
		t.Fatalf("default origin %q, note %q, %d popups: want cherry, a note and no popup", p.Origin, p.Note, len(p.Popups))
	}
}

// TestOperatorPopupsValidation: the Discord entry is accepted; each broken variant is refused with its reason.
func TestOperatorPopupsValidation(t *testing.T) {
	if _, err := opdata.Decode([]byte(popupsFileText(discordPopup)), checkOperatorPopups); err != nil {
		t.Fatalf("good Discord entry refused: %v", err)
	}
	m := func(pairs ...string) string { return mutateDefault(t, []byte(discordPopup), pairs...) }
	cases := []struct{ name, entry, want string }{
		{"http URL", m(`"url": "https://discord.gg/example"`, `"url": "http://discord.gg/example"`), "url: must be an absolute https URL with a host"},
		{"no host", m(`"url": "https://discord.gg/example"`, `"url": "https:///example"`), "url: must be an absolute https URL with a host"},
		{"user information", m(`"url": "https://discord.gg/example"`, `"url": "https://user@discord.gg/example"`), "url: must not carry user information"},
		{"URL over 512 bytes", m(`"url": "https://discord.gg/example"`, `"url": "https://discord.gg/`+strings.Repeat("a", 600)+`"`), "url: must be at most 512 bytes"},
		{"openWithBrowser without url", m(`"url": "https://discord.gg/example", `, ``), "url: must be set for openWithBrowser"},
		{"unknown link", m(`"link": "openWithBrowser"`, `"link": "html"`), `link "html" must be openWithBrowser`},
		{"WebView scene", m(`"link": "openWithBrowser"`, `"link": "goSomewhere(square)"`), `link "goSomewhere(square)" must be openWithBrowser`},
		{"url with a scene", m(`"link": "openWithBrowser"`, `"link": "goSomewhere(gachaShop)"`), "url is only for openWithBrowser"},
		{"number below the operator block", m(`"no": 9350`, `"no": 9349`), "no must be inside the operator block 9350-9399"},
		{"number above the Cherry block", m(`"no": 9350`, `"no": 9400`), "no must be inside the operator block 9350-9399"},
		{"number of a season", m(`"no": 9350`, `"no": 9301`), "no must be inside the operator block 9350-9399"},
		{"colour without #", m(`"background": "#1c3a5e"`, `"background": "1c3a5e"`), `background: colour "1c3a5e" must be #rrggbb`},
		{"text colour", m(`"text": "#ffffff"`, `"text": "white"`), `text: colour "white" must be #rrggbb`},
		{"button skin 0", m(`"button_skin": 4`, `"button_skin": 0`), "button_skin must be 1 to 15"},
		{"button skin 16", m(`"button_skin": 4`, `"button_skin": 16`), "button_skin must be 1 to 15"},
		{"blank sub", m(`"sub": "News, help and events on our Discord"`, `"sub": "  "`), "title and sub must not be empty"},
		{"image with a folder", m(`"image": ""`, `"image": "../banner.png"`), "must be a file name in config/popups"},
		{"image not a picture", m(`"image": ""`, `"image": "banner.gif"`), "must be a file name in config/popups"},
		{"start after end", m(`"start": ""`, `"start": "2026-11-30"`, `"end": ""`, `"end": "2026-11-22"`), "start is after end"},
		{"date not YYYY-MM-DD", m(`"start": ""`, `"start": "2026-11-5"`), `start: "2026-11-5" must be a date as YYYY-MM-DD`},
		{"unknown key", m(`"sub": "News`, `"bogus": 1, "sub": "News`), "bogus"},
		{"duplicate number", popupsFileText(discordPopup, discordPopup), "no must be unique"},
		{"origin not cherry", strings.Replace(popupsFileText(discordPopup), `"origin": "cherry"`, `"origin": "mixed"`, 1), `origin must be "cherry"`},
	}
	for _, c := range cases {
		text := c.entry
		if !strings.HasPrefix(text, `{"origin"`) {
			text = popupsFileText(c.entry)
		}
		if _, err := opdata.Decode([]byte(text), checkOperatorPopups); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one mentioning %q", c.name, err, c.want)
		}
	}
}

// TestOperatorPopupsLoadFromFile: a refused file names itself and leaves the active list alone; a good file replaces it.
func TestOperatorPopupsLoadFromFile(t *testing.T) {
	t.Cleanup(popupsFile.Reset)
	refuseFile(t, popupsFile.LoadFrom, "popups.json", popupsFileText(mutateDefault(t, []byte(discordPopup),
		`"url": "https://discord.gg/example"`, `"url": "http://discord.gg/example"`)), "url: must be an absolute https URL")
	if n := len(ActiveOperatorPopups().Popups); n != 0 {
		t.Fatalf("a refused file changed the list: %d popups", n)
	}
	path := filepath.Join(t.TempDir(), "popups.json")
	if err := os.WriteFile(path, []byte(popupsFileText(discordPopup)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := popupsFile.LoadFrom(path); err != nil {
		t.Fatal(err)
	}
	if got := ActiveOperatorPopups().Popups; len(got) != 1 || got[0].No != 9350 || got[0].URL != "https://discord.gg/example" {
		t.Fatalf("loaded popups = %+v", got)
	}
}

// loadPopupsText makes text the operator popup file in use (the test resets it).
func loadPopupsText(t *testing.T, text string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "popups.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := popupsFile.LoadFrom(path); err != nil {
		t.Fatal(err)
	}
}

// TestOperatorPopupWindow: a popup is listed on the game days its start and end allow (06:00 JST day boundary), in file
// order; a popup with no dates is always listed.
func TestOperatorPopupWindow(t *testing.T) {
	t.Cleanup(popupsFile.Reset)
	loadPopupsText(t, popupsFileText(goPopup(9350, "2026-11-22", "2026-11-23"), goPopup(9351, "", "2026-11-22"),
		goPopup(9352, "2026-11-24", ""), goPopup(9353, "", "")))
	at := func(day, hour, min int) []int64 {
		var nos []int64
		for _, p := range operatorPopupsInForce(time.Date(2026, time.November, day, hour, min, 0, 0, jst)) {
			nos = append(nos, p.No)
		}
		return nos
	}
	for _, c := range []struct {
		name string
		got  []int64
		want []int64
	}{
		{"Nov 22 05:59 is still game day Nov 21", at(22, 5, 59), []int64{9351, 9353}},
		{"Nov 22 06:00 opens the day", at(22, 6, 0), []int64{9350, 9351, 9353}},
		{"Nov 24 12:00 after both ends", at(24, 12, 0), []int64{9352, 9353}},
	} {
		if fmt.Sprint(c.got) != fmt.Sprint(c.want) {
			t.Errorf("%s: listed %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestOperatorPopupRowShape: an operator row has the 25 EventData keys, the openWithBrowser link, its https URL, cookieTime 1, no
// date cell, and a picture path that is relative and named by its number and hash.
func TestOperatorPopupRowShape(t *testing.T) {
	t.Cleanup(popupsFile.Reset)
	loadPopupsText(t, popupsFileText(discordPopup))
	row := operatorRow(ActiveOperatorPopups().Popups[0])
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 25 {
		t.Fatalf("row has %d keys, want the 25 EventData keys", len(got))
	}
	img, _ := got["imageUrl"].(string)
	if !strings.HasPrefix(img, "cherry/popup/9350-") || !strings.HasSuffix(img, ".png") || strings.HasPrefix(img, "/") {
		t.Fatalf("imageUrl = %q, want a relative cherry/popup/9350-<hash>.png", img)
	}
	want := map[string]any{
		"popupNo": 9350.0, "cookieTime": 1.0, "linkType": "openWithBrowser", "linkData": "https://discord.gg/example",
		"linkColorCode": 4.0, "startYmdt": "0", "endYmdt": "0", "caution": "", "mainTitle": "Cherry Discord",
		"subTitle": "News, help and events on our Discord", "backgroundColorCode": "#1c3a5e", "mainTitleColorCode": "#ffffff",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

// pictureOf decodes the PNG bytes of an operator popup's picture and checks the format.
func pictureOf(t *testing.T, p OperatorPopup) (image.Image, string) {
	t.Helper()
	pic, hash := popupPicture(p)
	img, err := png.Decode(bytes.NewReader(pic))
	if err != nil {
		t.Fatalf("picture is not PNG: %v", err)
	}
	return img, hash
}

// writePicture writes a picture file into the operator's popups folder.
func writePicture(t *testing.T, configDir, name string, encode func(*bytes.Buffer) error) {
	t.Helper()
	dir := filepath.Join(configDir, "popups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := encode(&buf); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOperatorPopupPictures: without an image the drawn placeholder is 640x400; an operator PNG or JPEG is served as PNG at
// its size; a file that does not load falls back to the placeholder; the route serves the current hash only.
func TestOperatorPopupPictures(t *testing.T) {
	configDir := operatorDir(t)
	t.Cleanup(popupsFile.Reset)
	loadPopupsText(t, popupsFileText(discordPopup))
	p := ActiveOperatorPopups().Popups[0]
	img, placeholderHash := pictureOf(t, p)
	if img.Bounds().Dx() != 640 || img.Bounds().Dy() != 400 {
		t.Fatalf("placeholder is %v, want 640x400", img.Bounds())
	}

	writePicture(t, configDir, "banner.png", func(b *bytes.Buffer) error {
		src := image.NewNRGBA(image.Rect(0, 0, 640, 300))
		for x := range 640 {
			for y := range 300 {
				src.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
			}
		}
		return png.Encode(b, src)
	})
	p.Image = "banner.png"
	img, hash := pictureOf(t, p)
	if img.Bounds().Dx() != 640 || img.Bounds().Dy() != 300 || hash == placeholderHash {
		t.Fatalf("operator PNG is %v, hash %s (placeholder %s)", img.Bounds(), hash, placeholderHash)
	}

	writePicture(t, configDir, "banner.jpg", func(b *bytes.Buffer) error {
		return jpeg.Encode(b, image.NewNRGBA(image.Rect(0, 0, 320, 200)), &jpeg.Options{Quality: 80})
	})
	p.Image = "banner.jpg"
	if img, _ := pictureOf(t, p); img.Bounds().Dx() != 320 {
		t.Fatalf("JPEG operator picture is %v, want 320 wide as PNG", img.Bounds())
	}

	writePicture(t, configDir, "broken.png", func(b *bytes.Buffer) error {
		b.WriteString("not an image")
		return nil
	})
	p.Image = "broken.png"
	if _, hash := popupPicture(p); hash != placeholderHash {
		t.Fatalf("an undecodable file must fall back to the placeholder: hash %s, want %s", hash, placeholderHash)
	}

	// The route reads the loaded list: the list names banner.png, and its current hash is served as PNG; an older or
	// foreign hash and a wrong number are 404; POST is 404.
	loadPopupsText(t, popupsFileText(mutateDefault(t, []byte(discordPopup), `"image": ""`, `"image": "banner.png"`)))
	p = ActiveOperatorPopups().Popups[0]
	_, current := popupPicture(p)
	serve := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		HandlePopupImage(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	if rec := serve(http.MethodGet, "/cherry/popup/9350-"+current+".png"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("current picture = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/cherry/popup/9350-00000000.png"},
		{http.MethodGet, "/cherry/popup/9351-" + current + ".png"},
		{http.MethodGet, "/cherry/popup/9350.png"},
		{http.MethodPost, "/cherry/popup/9350-" + current + ".png"},
	} {
		if rec := serve(c.method, c.path); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c.method, c.path, rec.Code)
		}
	}
}
