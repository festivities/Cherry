package economy

import (
	"fmt"
	"hash/crc32"
	"image/color"
	"log"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"cherry/data"
	"cherry/internal/httpx"
	"cherry/internal/opdata"
	"cherry/internal/thumbs"
)

// Economy Phase E: seasonal login calendars. The 7-day count cycle of loginbonus.go is the normal
// calendar; a season replaces it inside its date window with its own rewards, title and skin.
// Dates are game days (the calendar day starts at 06:00 JST, AttendDayKey). A season is chosen by
// its MMDD window; the first match in the season list wins, so New Year (Jan 1-14) beats the Winter
// window (Dec 15-Jan 5) on Jan 1-5. Item picks are the user's (2026-10-22); New Year has none yet.

// attendDayStartHour is the calendar's day boundary in JST (06:00).
const attendDayStartHour = 6

// calSchedule is one calendar in force: the normal cycle (Name "") or a season.
type calSchedule struct {
	ID         int // scheduleId: the skin version key (the client re-downloads when it changes)
	Name       string
	Title      string
	Start, End int // JST game dates as MMDD; 0 for the normal calendar (wrapping when Start > End)
	Ink        color.NRGBA
	Cycle      [7]calDay
	Limited    []string         // limited shop items, on sale only inside Start..End (limitedSale)
	Popup      *seasonPopupJSON // the season's image popup (popup.go), nil when it has none
}

// Calendars is data/calendars.json: the normal 7-day login calendar and the seasonal calendars that
// replace it inside their game dates.
//
//	normal            ink (#rrggbb) and cycle: the 7 rewards of days 1 to 7
//	seasons[]         {name, title, start, end, ink, cycle?, limited?}. name is the skin's key and the
//	                  staff tool's choice (not "none" or "auto"). start and end are game dates "MM-DD"
//	                  (end before start wraps the year). A date in several seasons takes the first in
//	                  the file, so New Year sits before Winter for the Jan 1-5 overlap. cycle omitted =
//	                  the normal rewards. limited: shop item codes on sale only inside the season's
//	                  dates, each in one season only; a limited item is still obtainable from the
//	                  gacha, mail and calendar grants.
//	popup?            the season's image popup, shown once per cookie_hours while the season is in force (popup.go)
//	cycle row         {gems, cash, tickets, item}: the day pays gems, cash and Face Shop tickets. With
//	                  item it pays that shop item, or its sell-back Gems when already owned; an item
//	                  with no art pays the row's gems only. Items must have art in the item archive.
//
// The popup kind of a row follows from its columns: item = ITEM, tickets = VOUCH, else GEM. The skin
// version key (scheduleId) is derived from each calendar's rows, so editing a row re-downloads the skin.
type Calendars struct {
	opdata.Provenance
	Normal  calendarJSON `json:"normal"`
	Seasons []seasonJSON `json:"seasons"`

	normalSched  calSchedule
	seasonScheds []calSchedule
}

type calendarJSON struct {
	Ink   string        `json:"ink"`
	Cycle []calendarRow `json:"cycle"`
}

type calendarRow struct {
	Gems    int64  `json:"gems"`
	Cash    int64  `json:"cash"`
	Tickets int64  `json:"tickets"`
	Item    string `json:"item"`
}

type seasonJSON struct {
	Name    string           `json:"name"`
	Title   string           `json:"title"`
	Start   string           `json:"start"`
	End     string           `json:"end"`
	Ink     string           `json:"ink"`
	Cycle   []calendarRow    `json:"cycle"`
	Limited []string         `json:"limited"`
	Popup   *seasonPopupJSON `json:"popup"`
}

var calendarsFile = opdata.New("calendars.json", data.CalendarsJSON, checkCalendars, verifyCalendars)

// ActiveCalendars is the calendar file in use.
func ActiveCalendars() *Calendars { return calendarsFile.Get() }

// seasons is the season list in use, in file order (the first match wins).
func seasons() []calSchedule { return calendarsFile.Get().seasonScheds }

func checkCalendars(c *Calendars) error {
	if err := c.CheckOrigin("mixed"); err != nil {
		return err
	}
	ink, err := parseHexColor(c.Normal.Ink)
	if err != nil {
		return fmt.Errorf("normal: %w", err)
	}
	days, err := calendarDays(c.Normal.Cycle)
	if err != nil {
		return fmt.Errorf("normal: %w", err)
	}
	c.normalSched = calSchedule{Title: "Daily Login", Ink: ink, Cycle: days}
	c.normalSched.ID = attendVersion(days)
	c.seasonScheds = nil
	names := map[string]bool{}
	limitedIn := map[string]string{}
	popupSeen := map[int64]bool{}
	for _, s := range c.Seasons {
		switch {
		case s.Name == "" || s.Name == "none" || s.Name == "auto" || names[s.Name]:
			return fmt.Errorf("season %q: name must be unique, not empty, and not none or auto", s.Name)
		case s.Title == "":
			return fmt.Errorf("season %s: title must not be empty", s.Name)
		}
		names[s.Name] = true
		start, err := parseMonthDay(s.Start)
		if err != nil {
			return fmt.Errorf("season %s: start %w", s.Name, err)
		}
		end, err := parseMonthDay(s.End)
		if err != nil {
			return fmt.Errorf("season %s: end %w", s.Name, err)
		}
		sInk, err := parseHexColor(s.Ink)
		if err != nil {
			return fmt.Errorf("season %s: %w", s.Name, err)
		}
		cycle := days
		if s.Cycle != nil {
			if cycle, err = calendarDays(s.Cycle); err != nil {
				return fmt.Errorf("season %s: %w", s.Name, err)
			}
		}
		sc := calSchedule{Name: s.Name, Title: s.Title, Start: start, End: end, Ink: sInk, Cycle: cycle}
		for _, code := range s.Limited {
			if _, kind := classify(code); kind == 0 {
				return fmt.Errorf("season %s: limited %q is not a sellable item code", s.Name, code)
			}
			if prev, dup := limitedIn[code]; dup {
				return fmt.Errorf("season %s: limited %s is also listed in season %s", s.Name, code, prev)
			}
			limitedIn[code] = s.Name
			sc.Limited = append(sc.Limited, code)
		}
		if s.Popup != nil {
			if err := checkSeasonPopup(s.Name, s.Popup, popupSeen); err != nil {
				return err
			}
			sc.Popup = s.Popup
		}
		c.seasonScheds = append(c.seasonScheds, sc)
	}
	return nil
}

// verifyCalendars refuses a season's item day the item archive cannot serve (no art: the day would
// silently pay its Gems only). A limited item with no art is only logged: the archive may be missing
// assets (AGENTS "Policies"), and such an item is simply never listed.
func verifyCalendars(c *Calendars) error {
	for _, s := range c.Seasons {
		for i, r := range s.Cycle {
			if r.Item != "" && !itemAvailable(r.Item) {
				return fmt.Errorf("season %s day %d: item %s has no art in the item archive (%s)", s.Name, i+1, r.Item, thumbs.DpItemRoot)
			}
		}
		for _, code := range s.Limited {
			if !itemAvailable(code) {
				log.Printf("cherry: season %s limited item %s has no art in the item archive; it is not listed", s.Name, code)
			}
		}
	}
	return nil
}

// calendarDays converts the 7 rows of a cycle; a missing or extra row is an error.
func calendarDays(rows []calendarRow) ([7]calDay, error) {
	var out [7]calDay
	if len(rows) != len(out) {
		return out, fmt.Errorf("cycle must have 7 rows, not %d", len(rows))
	}
	for i, r := range rows {
		if r.Gems < 0 || r.Cash < 0 || r.Tickets < 0 {
			return out, fmt.Errorf("cycle row %d: gems, cash and tickets must not be negative", i+1)
		}
		if r.Item != "" {
			if _, kind := classify(r.Item); kind == 0 {
				return out, fmt.Errorf("cycle row %d: item %q is not a sellable item code", i+1, r.Item)
			}
		}
		d := calDay{Type: "GEM", Gems: r.Gems, Cash: r.Cash, Tickets: r.Tickets, Item: r.Item}
		switch {
		case r.Item != "":
			d.Type = itemRewardType
		case r.Tickets > 0:
			d.Type = "VOUCH"
		}
		out[i] = d
	}
	return out, nil
}

// parseMonthDay reads a game date "MM-DD" as MMDD.
func parseMonthDay(s string) (int, error) {
	t, err := time.Parse("01-02", s)
	if err != nil {
		return 0, fmt.Errorf("%q must be a date MM-DD", s)
	}
	return int(t.Month())*100 + t.Day(), nil
}

// seasonOverride is the staff tool's choice: nil = by date, "" = the normal calendar, else a season name.
var seasonOverride atomic.Pointer[string]

// normalSchedule is the 7-day cycle outside any season.
func normalSchedule() calSchedule { return calendarsFile.Get().normalSched }

// NormalScheduleID is the normal calendar's skin version key (its scheduleId).
func NormalScheduleID() int { return normalSchedule().ID }

// withID stamps the season's scheduleId: it covers the name and rewards, so two seasons never
// share a skin version even when their cycles match.
func (s calSchedule) withID() calSchedule {
	s.ID = 100 + int(crc32.ChecksumIEEE([]byte(fmt.Sprint(s.Name, s.Cycle, attendSkinRev)))%900)
	return s
}

func (s calSchedule) contains(md int) bool {
	if s.Start <= s.End {
		return md >= s.Start && md <= s.End
	}
	return md >= s.Start || md <= s.End
}

// reason is the ledger text of one attend.
func (s calSchedule) reason(day int) string {
	if s.Name == "" {
		return fmt.Sprintf("daily login day %d", day)
	}
	return fmt.Sprintf("daily login %s day %d", s.Name, day)
}

// window returns startYmdt/endYmdt of the season instance that holds now.
func (s calSchedule) window(now time.Time) (string, string) {
	if s.Start == 0 {
		return "20200101000000", "20991231235959"
	}
	gd := gameDay(now)
	y := gd.Year()
	md := int(gd.Month())*100 + gd.Day()
	sy, ey := y, y
	if s.Start > s.End {
		if md < s.Start {
			sy = y - 1
		} else {
			ey = y + 1
		}
	}
	return fmt.Sprintf("%04d%04d000000", sy, s.Start), fmt.Sprintf("%04d%04d235959", ey, s.End)
}

// gameDay is the game day holding t, as a JST midnight.
func gameDay(t time.Time) time.Time {
	j := t.In(jst).Add(-attendDayStartHour * time.Hour)
	return time.Date(j.Year(), j.Month(), j.Day(), 0, 0, 0, 0, jst)
}

// keyTime is the instant (06:00 JST) a day key starts; AttendDayKey(keyTime(k)) == k.
func keyTime(k int64) time.Time { return time.Unix(k*86400-attendBoundaryShift, 0) }

// seasonNamed returns the season with that name.
func seasonNamed(name string) (calSchedule, bool) {
	for _, s := range seasons() {
		if s.Name == name {
			return s.withID(), true
		}
	}
	return calSchedule{}, false
}

// scheduleAt returns the calendar in force at t.
func scheduleAt(t time.Time) calSchedule {
	if p := seasonOverride.Load(); p != nil {
		if *p == "" {
			return normalSchedule()
		}
		if s, ok := seasonNamed(*p); ok {
			return s
		}
	}
	md := gameMD(t)
	for _, s := range seasons() {
		if s.contains(md) {
			return s.withID()
		}
	}
	return normalSchedule()
}

// gameMD is the game day holding t as MMDD (JST).
func gameMD(t time.Time) int {
	gd := gameDay(t)
	return int(gd.Month())*100 + gd.Day()
}

// limitedSale is a code's limited state at now: limited (it is in some season's list), open (listed
// and buyable now) and end (its window's end, 06:00 JST after the last game day; zero when closed).
// A window is open on its own game dates, whichever calendar is in force, or while its season is
// forced by the staff tool (/admin/season opens that season's limited set too).
func limitedSale(code string, now time.Time) (limited, open bool, end time.Time) {
	forced := ""
	if p := seasonOverride.Load(); p != nil {
		forced = *p
	}
	for _, s := range seasons() {
		if !slices.Contains(s.Limited, code) {
			continue
		}
		if forced == s.Name || s.contains(gameMD(now)) {
			return true, true, s.limitedEnd(now)
		}
		return true, false, time.Time{}
	}
	return false, false, time.Time{}
}

// limitedEnd is the end of the window that holds now or comes next (a forced season may be out of
// its dates). Windows are shorter than a year, so this year's end or next year's decides.
func (s calSchedule) limitedEnd(now time.Time) time.Time {
	year := gameDay(now).Year()
	if end := s.endIn(year); end.After(now) {
		return end
	}
	return s.endIn(year + 1)
}

// endIn is 06:00 JST on the day after the window's last game day, in the given calendar year.
func (s calSchedule) endIn(year int) time.Time {
	return time.Date(year, time.Month(s.End/100), s.End%100+1, attendDayStartHour, 0, 0, 0, jst)
}

// onSale reports whether a catalog code is listed and buyable at now: limited items only inside their window.
func onSale(code string, now time.Time) bool {
	limited, open, _ := limitedSale(code, now)
	return !limited || open
}

// scheduleByID finds the calendar whose skin version key is id; unknown keys get the normal skin.
func scheduleByID(id int) calSchedule {
	if id == NormalScheduleID() {
		return normalSchedule()
	}
	for _, s := range seasons() {
		if sc := s.withID(); sc.ID == id {
			return sc
		}
	}
	return normalSchedule()
}

// POST /admin/season {"name":"halloween"} forces a calendar for lab tests: "none" is the normal
// calendar, "auto" the date table again. The choice is process-local and resets on restart.
//
//	curl -X POST http://127.0.0.1:8099/admin/season -d "{\"name\":\"halloween\"}"
func handleAdminSeason(w http.ResponseWriter, r *http.Request) {
	if !adminLoopback(r) {
		httpx.ServeNotFound(w)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !readAdminJSON(r, &req) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	switch req.Name {
	case "auto":
		seasonOverride.Store(nil)
	case "none":
		none := ""
		seasonOverride.Store(&none)
	default:
		if _, ok := seasonNamed(req.Name); !ok {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		name := req.Name
		seasonOverride.Store(&name)
	}
	httpx.WriteObj(w, map[string]any{"calendar": scheduleAt(NowFunc()).Name})
}
