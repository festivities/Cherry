package economy

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"cherry/internal/httpx"
)

// Economy Phase E slice 3, Part 2: the native image event popup of a season. Research and addresses:
// %TEMP%\opencode\cherry\phaseDE-20261022\phaseE.md section 6. The client asks for it when the app comes
// back to the foreground in the Garden (AppDelegate::applicationWillEnterForeground posts 10847 →
// GET /v4/popup/isExists with no officialAvatarId → 10735 → NaGardenEventManager::ShowNextEventPopup,
// which needs the Garden main home scene). Then it fetches GET /v4/popup/list and the picture from the
// static host, and shows it once per cookie_hours. The button opens a native scene (never a WebView).

// seasonPopupJSON is calendars.json seasons[].popup. It is optional: a season without one has no popup.
//
//	no              popupNo, unique across the seasons (the client keys its shown-state by it)
//	title, sub      the two text lines
//	link            the button: goSomewhere(<name>), <name> one of popupLinks (native scenes only)
//	cookie_hours    hours between two showings, 1 to 8760 (the client's cookieTime)
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

// popupLinks are the goSomewhere names of the native scenes a popup button may open. They are the home
// menu rows (web.homeIconRows) that Cherry already serves; the router maps an unknown name to index 0.
var popupLinks = []string{"closet", "diary", "myRoom", "interiorShop", "fashionShop", "gachaShop", "faceShop", "minipetShop"}

// popupButtonSkins is the frame count of the button's MultiImage (Event_Popup_Cell_Btn.plist, filecount 15).
const popupButtonSkins = 15

// checkSeasonPopup validates the popup of one season; seen holds the popup numbers already used.
// CherryPopupFirst and CherryPopupLast bound the season popup numbers (calendars.json popup.no): Cherry's own
// numbers, the season's first event quest id (9101 Halloween, 9201 Winter, 9301 New Year). No archive holds an
// original popup number that is readable here (eventPopup.db is not a SQLite file).
const (
	CherryPopupFirst int64 = 9101
	CherryPopupLast  int64 = 9399
)

func checkSeasonPopup(season string, p *seasonPopupJSON, seen map[int64]bool) error {
	switch {
	case !inCherryBlock(p.No, CherryPopupFirst, CherryPopupLast) || seen[p.No]:
		return fmt.Errorf("season %s: popup no must be unique and inside the Cherry block %d-%d", season, CherryPopupFirst, CherryPopupLast)
	case p.Title == "" || p.Sub == "":
		return fmt.Errorf("season %s: popup title and sub must not be empty", season)
	case p.CookieHours < 1 || p.CookieHours > 8760:
		return fmt.Errorf("season %s: popup cookie_hours must be 1 to 8760", season)
	case p.ButtonSkin < 1 || p.ButtonSkin > popupButtonSkins:
		return fmt.Errorf("season %s: popup button_skin must be 1 to %d", season, popupButtonSkins)
	}
	rest, hasOpen := strings.CutPrefix(p.Link, "goSomewhere(")
	name, hasClose := strings.CutSuffix(rest, ")")
	if !hasOpen || !hasClose || !slices.Contains(popupLinks, name) {
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
	bg, text := p.Background, p.Text
	return map[string]any{
		"popupNo": p.No, "popupType": "NORMAL", "dispRoomId": "", "cookieTime": p.CookieHours,
		"linkType": p.Link, "linkData": "", "linkColorCode": p.ButtonSkin,
		"mainTitleMaxLine": 1, "subTitleMaxLine": 2,
		"linkBackgroundColorCode": bg, "imageUrl": "cherry/event/" + s.Name + ".png",
		"mainTitle": p.Title, "mainTitleColorCode": text, "mainTitleBackgroundColorCode": bg,
		"subTitle": p.Sub, "subTitleColorCode": text, "subTitleBackgroundColorCode": bg,
		"caution": "", "cautionColorCode": text, "cautionBackgroundColorCode": bg,
		"startYmdt": start, "endYmdt": end, "ymdtColorCode": text, "ymdtBackgroundColorCode": bg,
		"backgroundColorCode": bg,
	}
}

// HandlePopupIsExists serves GET /v4/popup/isExists. The check that shows the popup carries no
// officialAvatarId (NaDataInvokerProtocol::eventPopupIsExistsURL with a null aid). The badge check
// carries the aid and always gets false, so it never opens the popup.
func HandlePopupIsExists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	_, active := activePopup(NowFunc())
	show := active && r.URL.Query().Get("officialAvatarId") == ""
	httpx.WriteJSON(w, http.StatusOK, fmt.Sprintf(`{"result":%t}`, show))
}

// HandlePopupList serves GET /v4/popup/list: the season popup as a one-row EventData list while its
// season is in force, an empty list otherwise.
func HandlePopupList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	rows := []map[string]any{}
	if s, ok := activePopup(NowFunc()); ok {
		rows = append(rows, popupRow(s, NowFunc()))
	}
	httpx.WriteObj(w, map[string]any{"result": rows})
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
