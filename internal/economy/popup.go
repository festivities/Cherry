package economy

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Economy Phase E slice 3, Part 2: the native image event popup of a season. Research and addresses:
// %TEMP%\opencode\cherry\phaseDE-20261022\phaseE.md section 6. The client asks for it when the app comes
// back to the foreground in the Garden (AppDelegate::applicationWillEnterForeground posts 10847 →
// GET /v4/popup/isExists with no officialAvatarId → 10735 → NaGardenEventManager::ShowNextEventPopup,
// which needs the Garden main home scene). Then it fetches GET /v4/popup/list and the picture from the
// static host, and shows it once per game day (the PopupDay gate below; the operator popups share it, popups.go).
// The button opens a native scene (never a WebView) or, for an operator popup, a browser link.

// seasonPopupJSON is calendars.json seasons[].popup. It is optional: a season without one has no popup.
//
//	no              popupNo, unique across the seasons (the client keys its shown-state by it)
//	title, sub      the two text lines
//	link            the button: goSomewhere(<name>), <name> one of popupLinks (native scenes only)
//	cookie_hours    1 to 8760, still checked and accepted. It no longer reaches the client: every row goes out with
//	                cookieTime 1, and the once-per-game-day gate decides the showings (popupCookieHours)
//	background      #rrggbb behind the popup and the button; text: #rrggbb for the lines and date
//	button_skin     the button's skin, 1 to 15 (the frames of UIResource/Event_Popup_Cell_Btn.plist). The
//	                client draws the button only when it is non-zero. Its "GO NOW" label is a picture, not data.
type seasonPopupJSON struct {
	No          int64  `json:"no"`
	Title       string `json:"title"`
	Sub         string `json:"sub"`
	Link        string `json:"link"`
	CookieHours int64  `json:"cookie_hours"`
	Background  string `json:"background"`
	Text        string `json:"text"`
	ButtonSkin  int64  `json:"button_skin"`
}

// popupLinks are the goSomewhere names of the native scenes a popup button or a Garden icon may open. They are the home
// menu rows (web.homeIconRows) that Cherry already serves; the router maps an unknown name to index 0.
var popupLinks = []string{"closet", "diary", "myRoom", "interiorShop", "fashionShop", "gachaShop", "faceShop", "minipetShop"}

// isNativeLink reports whether link is goSomewhere(<name>) with <name> in popupLinks. The WebView links
// (html, event) and any other name fail it.
func isNativeLink(link string) bool {
	rest, hasOpen := strings.CutPrefix(link, "goSomewhere(")
	name, hasClose := strings.CutSuffix(rest, ")")
	return hasOpen && hasClose && slices.Contains(popupLinks, name)
}

// popupButtonSkins is the frame count of the button's MultiImage (Event_Popup_Cell_Btn.plist, filecount 15).
const popupButtonSkins = 15

// The popup numbers are Cherry's own. The season popups take CherryPopupFirst to CherryPopupSeasonLast (calendars.json
// popup.no: 9101 Halloween, 9201 Winter, 9301 New Year, the season's first event quest id). The operator popups
// (popups.go) take CherryPopupOperatorFirst to CherryPopupLast, so a season and an operator popup never share a number.
// No archive holds an original popup number that is readable here (eventPopup.db is not a SQLite file).
const (
	CherryPopupFirst         int64 = 9101
	CherryPopupSeasonLast    int64 = 9349
	CherryPopupOperatorFirst int64 = 9350
	CherryPopupLast          int64 = 9399
)

// checkSeasonPopup validates the popup of one season; seen holds the popup numbers already used.
func checkSeasonPopup(season string, p *seasonPopupJSON, seen map[int64]bool) error {
	switch {
	case !inCherryBlock(p.No, CherryPopupFirst, CherryPopupSeasonLast) || seen[p.No]:
		return fmt.Errorf("season %s: popup no must be unique and inside the season block %d-%d", season, CherryPopupFirst, CherryPopupSeasonLast)
	case p.Title == "" || p.Sub == "":
		return fmt.Errorf("season %s: popup title and sub must not be empty", season)
	case p.CookieHours < 1 || p.CookieHours > 8760:
		return fmt.Errorf("season %s: popup cookie_hours must be 1 to 8760", season)
	case p.ButtonSkin < 1 || p.ButtonSkin > popupButtonSkins:
		return fmt.Errorf("season %s: popup button_skin must be 1 to %d", season, popupButtonSkins)
	}
	if !isNativeLink(p.Link) {
		return fmt.Errorf("season %s: popup link %q is not a native scene (goSomewhere(<name>) with <name> in %s)", season, p.Link, strings.Join(popupLinks, ", "))
	}
	if _, err := parseHexColor(p.Background); err != nil {
		return fmt.Errorf("season %s: popup background: %w", season, err)
	}
	if _, err := parseHexColor(p.Text); err != nil {
		return fmt.Errorf("season %s: popup text: %w", season, err)
	}
	seen[p.No] = true
	return nil
}

// activePopup returns the calendar in force at now and whether it carries a popup. It is the season
// resolution of season.go, so /admin/season forces the popup as well.
func activePopup(now time.Time) (calSchedule, bool) {
	s := scheduleAt(now)
	return s, s.Popup != nil
}

// popupWindowMs is the season instance holding now as epoch-millisecond strings: from the first game
// day's 06:00 JST to one second before the game day after the last. The popup's startYmdt/endYmdt are
// epoch ms despite their names (device 2026-10-25: yyyymmddhhmmss digits drew as "Jan 18 2612").
func popupWindowMs(s calSchedule, now time.Time) (string, string) {
	a, b := s.window(now)
	start, err1 := time.ParseInLocation("20060102", a[:8], jst)
	last, err2 := time.ParseInLocation("20060102", b[:8], jst)
	if err1 != nil || err2 != nil {
		return "0", "0"
	}
	day := time.Duration(attendDayStartHour) * time.Hour
	end := last.AddDate(0, 0, 1).Add(day - time.Second)
	return strconv.FormatInt(start.Add(day).UnixMilli(), 10), strconv.FormatInt(end.UnixMilli(), 10)
}

// popupRow is the EventData row of the season popup (popup/list result). Field types follow
// NaImageEventPopupDataManager::EventData::SetData 0x217e940: popupNo, cookieTime and the line counts
// are numbers, the rest strings; startYmdt and endYmdt are atoll'd epoch milliseconds. popupType
// NORMAL matches neither flag name the client compares against (HOME_NOTI, OFFICIAL_ROOM).
// linkColorCode is the button skin, not a colour: SetData keeps it at +48 and sets +209 = (linkColorCode != 0),
// which RefreshUI 0x2152234 reads to push the Button cell, so 0 hides the button. The button frame is
// linkColorCode-1 (NaImageEventCell_Button::RefreshUI 0x20fc3d8). linkData is the scene detail (empty here).
func popupRow(s calSchedule, now time.Time) map[string]any {
	p := s.Popup
	start, end := popupWindowMs(s, now)
	return eventRow(popupCard{No: p.No, Link: p.Link, Skin: p.ButtonSkin, Background: p.Background, Text: p.Text,
		Title: p.Title, Sub: p.Sub, Image: "cherry/event/" + s.Name + ".png", Start: start, End: end})
}

// popupCookieHours is cookieTime of every row. The client hides a row for cookieTime hours after its last showing (a
// rolling window per popupNo). One hour keeps that window out of the way; the day gate (HandlePopupIsExists) decides how
// often a player sees the popups.
const popupCookieHours = 1

// popupCard is what one popup row shows: its link and button skin, its colours and words, its picture and its date
// text ("0" = no date cell).
type popupCard struct {
	No                  int64
	Link, LinkData      string
	Skin                int64
	Background, Text    string
	Title, Sub, Caution string
	Image               string
	Start, End          string
}

// eventRow is the EventData row of one popup (see popupRow for the field types).
func eventRow(c popupCard) map[string]any {
	bg, text := c.Background, c.Text
	return map[string]any{
		"popupNo": c.No, "popupType": "NORMAL", "dispRoomId": "", "cookieTime": popupCookieHours,
		"linkType": c.Link, "linkData": c.LinkData, "linkColorCode": c.Skin,
		"mainTitleMaxLine": 1, "subTitleMaxLine": 2,
		"linkBackgroundColorCode": bg, "imageUrl": c.Image,
		"mainTitle": c.Title, "mainTitleColorCode": text, "mainTitleBackgroundColorCode": bg,
		"subTitle": c.Sub, "subTitleColorCode": text, "subTitleBackgroundColorCode": bg,
		"caution": c.Caution, "cautionColorCode": text, "cautionBackgroundColorCode": bg,
		"startYmdt": c.Start, "endYmdt": c.End, "ymdtColorCode": text, "ymdtBackgroundColorCode": bg,
		"backgroundColorCode": bg,
	}
}

// HandlePopupIsExists serves GET /v4/popup/isExists. The check that shows the popup carries no
// officialAvatarId (NaDataInvokerProtocol::eventPopupIsExistsURL with a null aid). The badge check
// carries the aid and always gets false, so it never opens the popup.
//
// The popups are shown once per game day: while a popup is in force, the answer is true only when the requester's
// account has not been served this game day yet (PopupDay, set by HandlePopupList). A request without an account
// keeps the old answer (true while a popup is in force); the client's own window then limits it.
func HandlePopupIsExists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	now := NowFunc()
	show := popupInForce(now) && r.URL.Query().Get("officialAvatarId") == ""
	if show {
		store.AccountsMu.Lock()
		if acc := store.SessionAccountLocked(r); acc != nil {
			show = acc.PopupDay != AttendDayKey(now)
		}
		store.AccountsMu.Unlock()
	}
	httpx.WriteJSON(w, http.StatusOK, fmt.Sprintf(`{"result":%t}`, show))
}

// HandlePopupList serves GET /v4/popup/list: the rows of popupRows, the operator popups in force in file order and then
// the season popup, as an empty list when none is in force. The rows go out for any requester; a requester with an
// account is marked as served for this game day (markPopupDay) first.
func HandlePopupList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	now := NowFunc()
	rows := popupRows(now)
	if len(rows) > 0 {
		log.Printf("cherry: popups served aid=%s count=%d", markPopupDay(r, now), len(rows))
	}
	httpx.WriteObj(w, map[string]any{"result": rows})
}

// popupInForce reports whether any popup is listed at now: an operator popup in its window or the season popup.
func popupInForce(now time.Time) bool {
	_, season := activePopup(now)
	return season || len(operatorPopupsInForce(now)) > 0
}

// popupRows are the popup/list rows at now: the operator popups in force in file order, then the season popup.
func popupRows(now time.Time) []map[string]any {
	rows := []map[string]any{}
	for _, p := range operatorPopupsInForce(now) {
		rows = append(rows, operatorRow(p))
	}
	if s, ok := activePopup(now); ok {
		rows = append(rows, popupRow(s, now))
	}
	return rows
}

// markPopupDay records that the requester's account was served the popups of the game day of now, and returns its aid
// for the log ("-" without an account). The save is skipped when the day is already marked. A failed save is logged and
// the rows still go out; the next isExists then asks again.
func markPopupDay(r *http.Request, now time.Time) string {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		return "-"
	}
	if day := AttendDayKey(now); acc.PopupDay != day {
		prev := acc.PopupDay
		acc.PopupDay = day
		if err := store.SaveLocked(nil, acc); err != nil {
			acc.PopupDay = prev
			log.Printf("cherry: popups: day mark of aid=%s not saved: %v", acc.Aid, err)
		}
	}
	return acc.Aid
}

// HandleEventPopupImage serves GET /cherry/event/<season>.png, the picture of a season popup, relative
// to the static base (the client's staticResourceURLWithPath). It is drawn: a gradient from the season
// ink to the popup background with a light rim. The text is the client's, from the EventData row.
// ponytail: drawn gradient, not art; swap for an operator-supplied banner when one exists.
func HandleEventPopupImage(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cherry/event/"), ".png")
	s, ok := seasonNamed(name)
	if !ok || s.Popup == nil || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	bottom, _ := parseHexColor(s.Popup.Background) // checked at load
	const width, height, rim = 640, 320, 6
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		t := float64(y) / (height - 1)
		row := color.NRGBA{R: mix(s.Ink.R, bottom.R, t), G: mix(s.Ink.G, bottom.G, t), B: mix(s.Ink.B, bottom.B, t), A: 255}
		for x := range width {
			c := row
			if x < rim || y < rim || x >= width-rim || y >= height-rim {
				c = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	w.Header().Set("Content-Type", "image/png")
	_ = png.Encode(w, img)
}

// mix is a + (b-a)*t for one colour channel.
func mix(a, b uint8, t float64) uint8 {
	return uint8(float64(a) + (float64(b)-float64(a))*t)
}
