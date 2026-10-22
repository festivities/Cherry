package economy

import (
	"fmt"
	"hash/crc32"
	"image/color"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"cherry/internal/httpx"
)

// Economy Phase E: seasonal login calendars. The 7-day count cycle of loginbonus.go is the normal
// calendar; a season replaces it inside its date window with its own rewards, title and skin.
// Dates are game days (the calendar day starts at 06:00 JST, AttendDayKey). A season is chosen by
// its MMDD window; the first match in seasonTable wins, so New Year (Jan 1-14) beats the Winter
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
	Limited    []string // limited shop items, on sale only inside Start..End (limitedSale)
}

// seasonTable is checked in order (New Year before Winter for the Jan 1-5 overlap). A zero Cycle
// means the normal calendar's rewards (attendCycle, filled in by withID). An item day pays its
// normal reward (Gems, and Cash/tickets on day 7) plus Item, or the item's sell-back Gems when the
// account already owns it; a missing item pays the normal reward only (see itemAvailable). Candidates
// for the picks: %TEMP%\opencode\cherry\phaseDE-20261022\season-candidates.txt (idx in comments).
//
// Limited lists (user 2026-10-22, slice 2): each is on sale in the shops only inside its own Start..End
// game dates, whichever calendar is in force (Winter's Jan 1-5 overlap with New Year included), and
// is still obtainable from the gacha, mail and calendar grants. New Year has none yet.
var seasonTable = []calSchedule{
	{Name: "newyear", Title: "New Year Login", Start: 101, End: 114, Ink: color.NRGBA{170, 40, 40, 255}}, // no item days yet
	{Name: "winter", Title: "Winter Login", Start: 1215, End: 105, Ink: color.NRGBA{40, 100, 170, 255}, Cycle: [7]calDay{
		{Type: "GEM", Gems: 90}, {Type: "GEM", Gems: 120},
		{Type: itemRewardType, Gems: 150, Item: "CUAE00737"}, // candidate 29: Christmas tree accessory
		{Type: "GEM", Gems: 180}, {Type: "GEM", Gems: 240}, {Type: "GEM", Gems: 300},
		{Type: itemRewardType, Gems: 450, Cash: 15, Tickets: 3, Item: "RUCH004RC"}, // candidate 39: Shining Christmas (room)
	}, Limited: []string{"CAAE008Z2", "CUAE0072Z", "CUAE00737", "CUAE0076K", "CUAE0085C", "CUAE0093S", "CUAE009EA",
		"CUAE009RR", "CUAE009T6", "CUAE009W5", "CUSH002Z1", "CUSH003E1", "RUCH004RC"}},
	{Name: "halloween", Title: "Halloween Login", Start: 1025, End: 1107, Ink: color.NRGBA{200, 90, 20, 255}, Cycle: [7]calDay{
		{Type: "GEM", Gems: 90}, {Type: "GEM", Gems: 120},
		{Type: itemRewardType, Gems: 150, Item: "CUAE00B9R"}, // candidate 14: VIP Halloween accessory
		{Type: "GEM", Gems: 180}, {Type: "GEM", Gems: 240}, {Type: "GEM", Gems: 300},
		{Type: itemRewardType, Gems: 450, Cash: 15, Tickets: 3, Item: "CUON0062I"}, // candidate 18: petit dark-witch one-piece
	}, Limited: []string{"CAAE007BS", "CAAE008J2", "CAAE00AFM", "CUAE007T4", "CUAE007Z8", "CUAE0084K", "CUAE008NH",
		"CUAE008QU", "CUAE009CL", "CUAE009NQ", "CUAE009R5", "CUAE00A66", "CUAE00A8A", "CUAE00AMI", "CUAE00B9R",
		"CUAH0075G", "CUAH0075V", "CUON004SR", "CUON0062I"}},
}

// seasonOverride is the staff tool's choice: nil = by date, "" = the normal calendar, else a season name.
var seasonOverride atomic.Pointer[string]

// normalSchedule is the 7-day cycle outside any season.
func normalSchedule() calSchedule {
	return calSchedule{ID: AttendScheduleID, Title: "Daily Login", Ink: attendInk, Cycle: attendCycle}
}

// withID stamps the season's scheduleId: it covers the name and rewards, so two seasons never
// share a skin version even when their cycles match.
func (s calSchedule) withID() calSchedule {
	if s.Cycle[0].Type == "" { // no rewards of its own: the normal calendar's
		s.Cycle = attendCycle
	}
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
	for _, s := range seasonTable {
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
	for _, s := range seasonTable {
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
	for _, s := range seasonTable {
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
	if id == AttendScheduleID {
		return normalSchedule()
	}
	for _, s := range seasonTable {
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
