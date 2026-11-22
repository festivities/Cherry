package economy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // operator pictures may be JPEG
	"image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
)

// Operator login popups (promo and event banners such as a Discord link). data/popups.json is the operator file: an
// operator copy in %LOCALAPPDATA%\Cherry\config replaces it (opdata). They join the season popup in popup/list (popup.go):
// the rows in file order, the season row last. A popup is listed while its game-day window holds today.
//
// Protocol (popups-spec.md, 2026-11-22): the client prefixes imageUrl with its static base by plain concatenation, and
// caches the picture by that path. So the picture is named by its popup number and a hash of the bytes served:
// cherry/popup/<no>-<hash8>.png. linkType "openWithBrowser" opens linkData in the phone's browser; the client checks
// nothing, so the URL is checked here: https, a host, no user information, at most 512 bytes.

// OperatorPopups is data/popups.json.
//
//	origin, note  must be "cherry" and say that the popups are Cherry-made; text for operators, not sent to the client
//	popups        the popups in display order; may be empty (the embedded default)
//	  no          the popup number: 9350 to 9399 (CherryPopupOperatorFirst/Last), unique. The season popups use 9101 to 9349.
//	  title, sub  the two text lines (required)
//	  caution     an optional notice line; "" = none
//	  link        "openWithBrowser" (then url is required) or goSomewhere(<name>) with <name> a native scene (popupLinks)
//	  url         the https URL the button opens in the phone's browser; only with openWithBrowser
//	  button_skin the button's skin, 1 to 15 (the frames of UIResource/Event_Popup_Cell_Btn.plist; 4 is the orange one)
//	  background  #rrggbb behind the popup; text: #rrggbb for the lines
//	  image       "" = the drawn placeholder, or a file name (.png, .jpg or .jpeg) in config/popups/: at most 2 MiB, at most
//	              1280 px wide and 4096 px high. A file that does not load is replaced by the placeholder, with one log line
//	  start, end  optional first and last game day, YYYY-MM-DD (JST; a game day starts at 06:00). start after end is refused
type OperatorPopups struct {
	opdata.Provenance
	Popups []OperatorPopup `json:"popups"`
}

// OperatorPopup is one entry of OperatorPopups (see there).
type OperatorPopup struct {
	No         int64  `json:"no"`
	Title      string `json:"title"`
	Sub        string `json:"sub"`
	Caution    string `json:"caution"`
	Link       string `json:"link"`
	URL        string `json:"url"`
	ButtonSkin int64  `json:"button_skin"`
	Background string `json:"background"`
	Text       string `json:"text"`
	Image      string `json:"image"`
	Start      string `json:"start"`
	End        string `json:"end"`

	from, until time.Time // the game-day window the check derives from start and end; zero = open
}

const (
	linkBrowser        = "openWithBrowser"
	popupURLMax        = 512
	popupPictureMax    = 2 << 20
	popupPictureWidth  = 1280
	popupPictureHeight = 4096
	placeholderWidth   = 640
	placeholderHeight  = 400
	placeholderRim     = 6
)

var popupsFile = opdata.New("popups.json", data.PopupsJSON, checkOperatorPopups, nil)

// ActiveOperatorPopups is the operator popup file in use.
func ActiveOperatorPopups() *OperatorPopups { return popupsFile.Get() }

// checkOperatorPopups validates the file: its origin, then each popup (checkOperatorPopup) with a number used once.
func checkOperatorPopups(p *OperatorPopups) error {
	if err := p.CheckOrigin(opdata.OriginCherry); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for i := range p.Popups {
		e := &p.Popups[i]
		where := fmt.Sprintf("popups[%d] (no %d)", i+1, e.No)
		if err := checkOperatorPopup(e, where); err != nil {
			return err
		}
		if seen[e.No] {
			return fmt.Errorf("%s: no must be unique", where)
		}
		seen[e.No] = true
	}
	return nil
}

// checkOperatorPopup checks one popup and derives its game-day window.
func checkOperatorPopup(e *OperatorPopup, where string) error {
	switch {
	case !inCherryBlock(e.No, CherryPopupOperatorFirst, CherryPopupLast):
		return fmt.Errorf("%s: no must be inside the operator block %d-%d", where, CherryPopupOperatorFirst, CherryPopupLast)
	case strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Sub) == "":
		return fmt.Errorf("%s: title and sub must not be empty", where)
	case e.ButtonSkin < 1 || e.ButtonSkin > popupButtonSkins:
		return fmt.Errorf("%s: button_skin must be 1 to %d", where, popupButtonSkins)
	}
	if _, err := parseHexColor(e.Background); err != nil {
		return fmt.Errorf("%s: background: %w", where, err)
	}
	if _, err := parseHexColor(e.Text); err != nil {
		return fmt.Errorf("%s: text: %w", where, err)
	}
	switch {
	case e.Link == linkBrowser:
		if err := checkPopupURL(e.URL); err != nil {
			return fmt.Errorf("%s: url: %w", where, err)
		}
	case isNativeLink(e.Link):
		if e.URL != "" {
			return fmt.Errorf("%s: url is only for %s", where, linkBrowser)
		}
	default:
		return fmt.Errorf("%s: link %q must be %s (with url) or goSomewhere(<name>) with a native scene name", where, e.Link, linkBrowser)
	}
	if e.Image != "" {
		if err := checkPopupImageName(e.Image); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	var err error
	if e.from, err = parseGameDay(e.Start); err != nil {
		return fmt.Errorf("%s: start: %w", where, err)
	}
	if e.until, err = parseGameDay(e.End); err != nil {
		return fmt.Errorf("%s: end: %w", where, err)
	}
	if !e.from.IsZero() && !e.until.IsZero() && e.from.After(e.until) {
		return fmt.Errorf("%s: start is after end", where)
	}
	return nil
}

// checkPopupURL accepts an absolute https URL with a host and no user information, at most popupURLMax bytes. The
// error does not repeat the URL.
func checkPopupURL(raw string) error {
	if raw == "" {
		return errors.New("must be set for " + linkBrowser)
	}
	if len(raw) > popupURLMax {
		return fmt.Errorf("must be at most %d bytes", popupURLMax)
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Scheme != "https" || u.Host == "":
		return errors.New("must be an absolute https URL with a host")
	case u.User != nil:
		return errors.New("must not carry user information")
	}
	return nil
}

// checkPopupImageName accepts a bare file name ending in .png, .jpg or .jpeg (the file lives in config/popups).
func checkPopupImageName(name string) error {
	ext := strings.ToLower(filepath.Ext(name))
	if len(name) > 128 || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) || (ext != ".png" && ext != ".jpg" && ext != ".jpeg") {
		return fmt.Errorf("image %q must be a file name in config/popups ending in .png, .jpg or .jpeg", name)
	}
	return nil
}

// parseGameDay reads a game day YYYY-MM-DD as the JST midnight of that day. "" is zero (no bound).
func parseGameDay(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, jst)
	if err != nil || len(s) != len("2006-01-02") {
		return time.Time{}, fmt.Errorf("%q must be a date as YYYY-MM-DD", s)
	}
	return t, nil
}

// operatorPopupsInForce lists the operator popups whose window holds the game day of now, in file order.
func operatorPopupsInForce(now time.Time) []OperatorPopup {
	day := gameDay(now)
	var out []OperatorPopup
	for _, p := range ActiveOperatorPopups().Popups {
		if (p.from.IsZero() || !day.Before(p.from)) && (p.until.IsZero() || !day.After(p.until)) {
			out = append(out, p)
		}
	}
	return out
}

// operatorRow is the popup/list row of one operator popup. The date texts are "0": no date cell.
func operatorRow(p OperatorPopup) map[string]any {
	_, hash := popupPicture(p)
	return eventRow(popupCard{No: p.No, Link: p.Link, LinkData: p.URL, Skin: p.ButtonSkin, Background: p.Background,
		Text: p.Text, Title: p.Title, Sub: p.Sub, Caution: p.Caution, Image: popupPicturePath(p.No, hash), Start: "0", End: "0"})
}

// popupPicturePath is the relative imageUrl of a popup picture: cherry/popup/<no>-<hash>.png.
func popupPicturePath(no int64, hash string) string {
	return "cherry/popup/" + strconv.FormatInt(no, 10) + "-" + hash + ".png"
}

// popupPicture is the picture of an operator popup and the hash its path carries (the first 4 bytes of SHA-256 of the
// bytes served, as 8 hex digits). The operator's file is served as PNG when it loads; without one, or when it does not
// load, the drawn placeholder of its background is served.
func popupPicture(p OperatorPopup) ([]byte, string) {
	var pic []byte
	if p.Image != "" {
		pic = operatorFilePicture(p.Image)
	}
	if pic == nil {
		pic = placeholderPicture(p.Background)
	}
	sum := sha256.Sum256(pic)
	return pic, hex.EncodeToString(sum[:4])
}

// popupPics memoizes the pictures by source: "file:<path>" with the file's size and time (sig), "drawn:<background>".
// The map grows only with the operator's file names and colours.
var popupPics = struct {
	sync.Mutex
	m map[string]popupPic
}{m: map[string]popupPic{}}

type popupPic struct {
	sig string // the file's size and modification time; "" for a drawn picture or a missing file
	pic []byte // nil: the file did not load
}

// operatorFilePicture is the PNG of an operator picture file, or nil when it does not load (logged once per change).
func operatorFilePicture(name string) []byte {
	dir := opdata.Dir()
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, "popups", name)
	sig := ""
	if fi, err := os.Stat(path); err == nil {
		sig = strconv.FormatInt(fi.Size(), 10) + "|" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
	}
	key := "file:" + path
	popupPics.Lock()
	hit, ok := popupPics.m[key]
	popupPics.Unlock()
	if ok && hit.sig == sig {
		return hit.pic
	}
	pic, err := loadOperatorPicture(path)
	if err != nil {
		log.Printf("cherry: popup picture %s not used, the drawn placeholder is served: %v", name, err)
	}
	popupPics.Lock()
	popupPics.m[key] = popupPic{sig: sig, pic: pic}
	popupPics.Unlock()
	return pic
}

// loadOperatorPicture decodes a PNG or JPEG file within the size limits and returns it as PNG.
func loadOperatorPicture(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, popupPictureMax+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > popupPictureMax {
		return nil, fmt.Errorf("larger than %d bytes", popupPictureMax)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if format != "png" && format != "jpeg" {
		return nil, fmt.Errorf("format %s is not PNG or JPEG", format)
	}
	if cfg.Width > popupPictureWidth || cfg.Height > popupPictureHeight {
		return nil, fmt.Errorf("%dx%d is larger than %dx%d", cfg.Width, cfg.Height, popupPictureWidth, popupPictureHeight)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// placeholderPicture is the drawn banner of a background colour: a gradient from a lighter tone at the top to the colour
// at the bottom, with a white rim. The text is the client's (the row's lines).
func placeholderPicture(bg string) []byte {
	key := "drawn:" + bg
	popupPics.Lock()
	defer popupPics.Unlock()
	if e, ok := popupPics.m[key]; ok {
		return e.pic
	}
	c, _ := parseHexColor(bg) // checked at load
	img := image.NewNRGBA(image.Rect(0, 0, placeholderWidth, placeholderHeight))
	for y := range placeholderHeight {
		t := float64(y) / (placeholderHeight - 1)
		row := color.NRGBA{R: mix(c.R, 255, 0.3*(1-t)), G: mix(c.G, 255, 0.3*(1-t)), B: mix(c.B, 255, 0.3*(1-t)), A: 255}
		for x := range placeholderWidth {
			px := row
			if x < placeholderRim || y < placeholderRim || x >= placeholderWidth-placeholderRim || y >= placeholderHeight-placeholderRim {
				px = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
			}
			img.SetNRGBA(x, y, px)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img) // an in-memory encode of a valid image cannot fail
	popupPics.m[key] = popupPic{pic: buf.Bytes()}
	return buf.Bytes()
}

// HandlePopupImage serves GET /cherry/popup/<no>-<hash>.png, the picture of an operator popup. The hash must be the one
// the list names for the picture as it is now (the rule of /cherry/gacha/): an older picture's path is 404, so a client
// never keeps an old picture under a current path. The picture is served whether or not the popup is in its window.
func HandlePopupImage(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cherry/popup/"), ".png")
	noText, hash, ok := strings.Cut(name, "-")
	no, err := strconv.ParseInt(noText, 10, 64)
	if r.Method != http.MethodGet || !ok || err != nil {
		http.NotFound(w, r)
		return
	}
	for _, p := range ActiveOperatorPopups().Popups {
		if p.No != no {
			continue
		}
		pic, h := popupPicture(p)
		if h != hash {
			break
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pic)
		return
	}
	http.NotFound(w, r)
}
