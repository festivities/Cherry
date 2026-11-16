package admin

import (
	"net/http"
	"time"

	"cherry/internal/store"
)

// The Economy page (PLAN Roadmap 8, staff "Insights"): the Gems and Cash the accounts hold now, and what the ledger moved in a
// window (24 hours, 7 or 30 days): minted and spent per currency, the largest reason groups, and the accounts that earned or
// spent the most Gems. The figures are cached for insightsTTL (insights.go).

const (
	economyReasons = 15 // reason groups listed
	economyTop     = 10 // accounts in each top list
)

// economyWindow is one choice of the window selector: its query value, its span and its name.
type economyWindow struct {
	Key  string
	Span time.Duration
	Name string
}

// economyWindows are the windows the page offers; the first is the default.
var economyWindows = []economyWindow{
	{"24h", 24 * time.Hour, "24 hours"},
	{"7d", 7 * 24 * time.Hour, "7 days"},
	{"30d", 30 * 24 * time.Hour, "30 days"},
}

// economyWindowOf reads the window query value. An absent value is the default; any other unknown value is refused.
func economyWindowOf(key string) (economyWindow, bool) {
	if key == "" {
		return economyWindows[0], true
	}
	for _, w := range economyWindows {
		if w.Key == key {
			return w, true
		}
	}
	return economyWindow{}, false
}

// flowRow is one currency of the flow table: what was minted, what was spent, the difference and the lines.
type flowRow struct {
	Currency                  string
	Minted, Spent, Net, Lines int64
}

// reasonRow is one reason group: its lines and the Gems and Cash it moved in and out.
type reasonRow struct {
	Group                            string
	Lines                            int64
	GemsIn, GemsOut, CashIn, CashOut int64
}

// balanceRow is one account of a top list: its player page (when the account has one), its name and its Gems and Cash in the window.
type balanceRow struct {
	Href, Name, Aid string
	Gems, Cash      int64
}

// economyPage is what the Economy page shows for one window.
type economyPage struct {
	Window   economyWindow
	Holdings store.Holdings
	Flow     []flowRow
	Reasons  []reasonRow
	Earners  []balanceRow
	Spenders []balanceRow
	Windows  []link
}

// economyAt reads the page for window w ending at now. Every query starts at the window's first instant.
func economyAt(w economyWindow, now time.Time) (economyPage, error) {
	since := now.Add(-w.Span)
	flow, err := store.LedgerFlow(since)
	if err != nil {
		return economyPage{}, err
	}
	reasons, err := store.LedgerReasons(since, economyReasons)
	if err != nil {
		return economyPage{}, err
	}
	earned, err := store.TopLedger(since, false, economyTop)
	if err != nil {
		return economyPage{}, err
	}
	spent, err := store.TopLedger(since, true, economyTop)
	if err != nil {
		return economyPage{}, err
	}
	aids := make([]string, 0, len(earned)+len(spent))
	for _, t := range earned {
		aids = append(aids, t.Aid)
	}
	for _, t := range spent {
		aids = append(aids, t.Aid)
	}
	players := store.PlayersOfAids(aids)

	page := economyPage{Window: w, Holdings: store.CurrentHoldings()}
	for _, f := range flow {
		page.Flow = append(page.Flow, flowRow{Currency: f.Currency, Minted: f.Minted, Spent: f.Spent, Net: f.Minted - f.Spent, Lines: f.Lines})
	}
	for _, g := range reasons {
		page.Reasons = append(page.Reasons, reasonRow{Group: g.Group, Lines: g.Lines, GemsIn: g.GemsIn, GemsOut: g.GemsOut, CashIn: g.CashIn, CashOut: g.CashOut})
	}
	page.Earners = balanceRows(earned, players)
	page.Spenders = balanceRows(spent, players)
	return page, nil
}

// balanceRows turns a top list into rows; an account with no player row (no avatar yet, or gone) has no link.
func balanceRows(tops []store.LedgerTop, players map[string]store.Player) []balanceRow {
	rows := []balanceRow{}
	for _, t := range tops {
		row := balanceRow{Aid: t.Aid, Gems: t.Gems, Cash: t.Cash}
		if p, ok := players[t.Aid]; ok {
			row.Href, row.Name = playerPath(p.ID), p.Name
		}
		rows = append(rows, row)
	}
	return rows
}

// handleEconomy: GET /economy?window=24h|7d|30d.
func (s *Server) handleEconomy(w http.ResponseWriter, r *http.Request, sess session) {
	win, ok := economyWindowOf(r.URL.Query().Get("window"))
	if !ok {
		s.fail(w, http.StatusBadRequest, "The window is 24h, 7d or 30d.")
		return
	}
	page, err := memoized(s, "economy:"+win.Key, func() (economyPage, error) { return economyAt(win, s.now()) })
	if err != nil {
		s.storeError(w, err)
		return
	}
	for _, c := range economyWindows {
		page.Windows = append(page.Windows, link{Href: "/economy?window=" + c.Key, Label: c.Name, Current: c.Key == win.Key})
	}
	v := s.base(r, sess, "Economy")
	v.Economy = page
	s.render(w, http.StatusOK, "economy", v)
}
