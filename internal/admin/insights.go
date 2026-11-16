package admin

import (
	"log"
	"sync"
	"time"

	"cherry/internal/lpn"
)

// The staff Insights (PLAN Roadmap 8, staff "Insights"): the Online now, Economy and Activity pages, and the row of links on the
// dashboard. The aggregates behind the economy and activity pages are read from the database; each one is kept for insightsTTL,
// so a page reload within the minute shows the same numbers and costs no query.

// insightsTTL is how long an aggregate is kept. A failed read is not kept.
const insightsTTL = time.Minute

// memo keeps the aggregates by key. The keys are a fixed set (one per economy window, and the activity series), so the map
// stays small.
type memo struct {
	mu    sync.Mutex
	items map[string]memoItem
}

type memoItem struct {
	at time.Time
	v  any
}

// memoized returns the value kept under key when it was computed less than insightsTTL ago at s's clock; otherwise it runs fn,
// keeps its value and returns it. Two readers that miss together both run fn; the result is the same either way.
func memoized[T any](s *Server, key string, fn func() (T, error)) (T, error) {
	now := s.now()
	s.memo.mu.Lock()
	it, ok := s.memo.items[key]
	s.memo.mu.Unlock()
	if ok && now.Sub(it.at) < insightsTTL {
		return it.v.(T), nil
	}
	v, err := fn()
	if err != nil {
		var zero T
		return zero, err
	}
	s.memo.mu.Lock()
	s.memo.items[key] = memoItem{at: now, v: v}
	s.memo.mu.Unlock()
	return v, nil
}

// insightsSummary is the row of links and mini-stats on the dashboard. Known is false when the database could not be read:
// the row is then left out, not shown with zeros.
type insightsSummary struct {
	Known        bool
	Players      int // players with a login now, distinct
	Gems, Cash   int64
	Holders      int
	SignupsToday int64
}

// insightsSummary reads the numbers the dashboard row shows, through the same cached aggregates the pages use.
func (s *Server) insightsSummary(now time.Time) insightsSummary {
	var sum insightsSummary
	econ, err := memoized(s, "economy:"+economyWindows[0].Key, func() (economyPage, error) { return economyAt(economyWindows[0], now) })
	if err != nil {
		log.Printf("cherry: staff dashboard: economy: %v", err)
		return sum
	}
	act, err := memoized(s, activityKey, func() (activityPage, error) { return activityAt(now) })
	if err != nil {
		log.Printf("cherry: staff dashboard: activity: %v", err)
		return sum
	}
	sum.Known = true
	sum.Gems, sum.Cash, sum.Holders = econ.Holdings.Gems, econ.Holdings.Cash, econ.Holdings.Holders
	sum.Players = len(distinctAids(s.online()))
	if n := len(act.Days); n > 0 {
		sum.SignupsToday = act.Days[n-1].Signups
	}
	return sum
}

// distinctAids is the set of avatar ids the connections belong to: a player with two connections counts once.
func distinctAids(conns []lpn.Connection) map[uint64]bool {
	out := map[uint64]bool{}
	for _, c := range conns {
		out[c.Aid] = true
	}
	return out
}
