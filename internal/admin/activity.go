package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"cherry/internal/store"
)

// The Activity page (PLAN Roadmap 8, staff "Insights"): the last 30 UTC days as four SVG bar charts (sign-ups, chat messages,
// ledger lines, daily active players) and one table of the same numbers. The chart geometry is computed here, so the template
// prints numbers only; every label reaches the page through html/template.

const (
	activityDays    = 30
	activityKey     = "activity" // the memo key of the series
	chartWidth      = 720        // the SVG viewBox, in user units
	chartHeight     = 220
	plotLeft        = 52 // the plot area inside the viewBox
	plotRight       = 712
	plotTop         = 14
	plotBottom      = 180
	chartLabelEvery = 5 // a day label every this many days
)

// activityDay is one day of the series: its UTC day and the four counts.
type activityDay struct {
	Day                          string
	Signups, Chat, Lines, Active int64
}

// chart is one bar chart: its title and note, the viewBox, the gridlines with their labels, one bar per day (with its hover title)
// and the day labels along the bottom.
type chart struct {
	Title, Note   string
	Width, Height int
	Left, Right   int
	Bottom        int
	Ticks         []chartTick
	Bars          []chartBar
	Labels        []chartLabel
}

// chartTick is a gridline at height Y with its value label.
type chartTick struct {
	Y    int
	Text string
}

// chartBar is one day's bar: its top-left corner (X, Y), its size, and its hover title.
type chartBar struct {
	X, Y, W, H int
	Title      string
}

// chartLabel is a day label centred on X.
type chartLabel struct {
	X    int
	Text string
}

// activityPage is what the Activity page shows: the series oldest first, and the four charts.
type activityPage struct {
	Days   []activityDay
	Charts []chart
}

// activityAt reads the series of the 30 UTC days up to and including the day of now.
func activityAt(now time.Time) (activityPage, error) {
	today := now.UTC().Truncate(24 * time.Hour)
	from := today.AddDate(0, 0, -(activityDays - 1))
	act, err := store.ActivityByDay(from, now)
	if err != nil {
		return activityPage{}, err
	}
	days := make([]activityDay, activityDays)
	for i := range days {
		key := from.AddDate(0, 0, i).Format(time.DateOnly)
		days[i] = activityDay{Day: key, Signups: act.Signups[key], Chat: act.Chat[key], Lines: act.Lines[key], Active: act.Active[key]}
	}
	return activityPage{Days: days, Charts: []chart{
		newChart("Sign-ups per day", "Accounts created that UTC day.", days, func(d activityDay) int64 { return d.Signups }),
		newChart("Chat messages per day", "Chat messages logged that UTC day. A finished day keeps its count after the retention set in Settings deletes the messages.", days, func(d activityDay) int64 { return d.Chat }),
		newChart("Ledger lines per day", "Balance changes written that UTC day.", days, func(d activityDay) int64 { return d.Lines }),
		newChart("Daily active players (proxy)", "Avatars with a ledger line or a chat message that UTC day. It counts activity, not logins: an avatar that only played a game without a balance change is not in it.", days, func(d activityDay) int64 { return d.Active }),
	}}, nil
}

// newChart lays out one bar chart of value over days. The scale is the largest value rounded up to a round number, so the top
// gridline is a clean figure; a day with a value gets at least one unit of height.
func newChart(title, note string, days []activityDay, value func(activityDay) int64) chart {
	c := chart{Title: title, Note: note, Width: chartWidth, Height: chartHeight, Left: plotLeft, Right: plotRight, Bottom: plotBottom}
	var peak int64
	for _, d := range days {
		peak = max(peak, value(d))
	}
	top := niceMax(peak)
	plotH := int64(plotBottom - plotTop)
	for _, v := range []int64{0, top / 2, top} {
		c.Ticks = append(c.Ticks, chartTick{Y: plotBottom - int(v*plotH/top), Text: strconv.FormatInt(v, 10)})
	}
	slot := (plotRight - plotLeft) / len(days)
	barW := slot * 2 / 3
	for i, d := range days {
		v := value(d)
		h := int(v * plotH / top)
		if v > 0 && h == 0 {
			h = 1
		}
		x := plotLeft + i*slot + (slot-barW)/2
		c.Bars = append(c.Bars, chartBar{X: x, Y: plotBottom - h, W: barW, H: h, Title: fmt.Sprintf("%s: %d", d.Day, v)})
		if i%chartLabelEvery == 0 || i == len(days)-1 {
			c.Labels = append(c.Labels, chartLabel{X: plotLeft + i*slot + slot/2, Text: d.Day[5:]})
		}
	}
	return c
}

// niceMax is the smallest of 1, 2, 5 or 10 times a power of ten that is at least v (1 for v < 1).
func niceMax(v int64) int64 {
	if v < 1 {
		return 1
	}
	p := int64(1)
	for p*10 <= v {
		p *= 10
	}
	for _, m := range []int64{1, 2, 5, 10} {
		if m*p >= v {
			return m * p
		}
	}
	return 10 * p
}

// handleActivity: GET /activity.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request, sess session) {
	page, err := memoized(s, activityKey, func() (activityPage, error) { return activityAt(s.now()) })
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Activity")
	v.Activity = page
	s.render(w, http.StatusOK, "activity", v)
}
