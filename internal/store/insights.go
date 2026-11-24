package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
)

// The staff Insights (PLAN Roadmap 8, staff "Insights"): read-only aggregates over the ledger, the accounts and the chat log.
// Every query is bounded by a window in time (the ledger_time, accounts_created and chat_log_time indexes give the range) and
// by a LIMIT, and the staff pages cache the results for about a minute (admin/insights.go). Every read runs in ViewBounded, so
// a window grown too large fails the page instead of holding the WAL. Balances come from the accounts in memory; the ledger
// and the chat log are read from the database. The Economy windows read their finished days from the daily rollups
// (ledgerdays.go) and only the part days at their edges from raw lines (about 500k a day on 2026-11-25).
// ponytail: the 24-hour window is all edges (one day of raw lines, ~1.7 s on 2026-11-25); hourly rollups if traffic grows ~5x.

// Holdings is what the accounts hold now: the Gems and Cash summed over every account, and the number of accounts that hold
// any Gems or Cash.
type Holdings struct {
	Gems, Cash int64
	Holders    int
}

// CurrentHoldings sums the balances of the accounts in memory (a scan of the account list under AccountsMu).
func CurrentHoldings() Holdings {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	var h Holdings
	for _, acc := range accountList {
		h.Gems += acc.Gems
		h.Cash += acc.Cash
		if acc.Gems > 0 || acc.Cash > 0 {
			h.Holders++
		}
	}
	return h
}

// ledgerSince is the lower bound of a ledger window: the ledger's own time format (RFC 3339, UTC, to the second).
func ledgerSince(since time.Time) string { return since.UTC().Format(time.RFC3339) }

// CurrencyFlow is the ledger's movement of one currency in a window: Minted sums the credits, Spent the debits (both
// positive), Lines counts the ledger lines.
type CurrencyFlow struct {
	Currency             string
	Minted, Spent, Lines int64
}

// The Economy statements, constants so the store's tests can ask SQLite for their plans (insights_test.go). Each raw one reads
// one range of ledger_time, [from, to), and each roll one the rolled days fromDay..toDay; both return their key columns and
// then lines, plus and minus, which ledgerSumsIn adds up per key.
const (
	qLedgerFlow = `SELECT currency, COUNT(*), SUM(CASE WHEN delta > 0 THEN delta ELSE 0 END), SUM(CASE WHEN delta < 0 THEN -delta ELSE 0 END)
	FROM ledger INDEXED BY ledger_time WHERE time >= ? AND time < ? GROUP BY currency`
	qRollFlow = `SELECT currency, SUM(lines), SUM(plus), SUM(minus) FROM ledger_days WHERE day >= ? AND day <= ? GROUP BY currency`

	qLedgerReasons = `SELECT grp, currency, COUNT(*), SUM(CASE WHEN delta > 0 THEN delta ELSE 0 END), SUM(CASE WHEN delta < 0 THEN -delta ELSE 0 END)
	FROM (SELECT ` + ledgerGroupSQL + ` AS grp, currency, delta FROM ledger INDEXED BY ledger_time WHERE time >= ? AND time < ?)
	GROUP BY grp, currency`
	qRollReasons = `SELECT grp, currency, SUM(lines), SUM(plus), SUM(minus) FROM ledger_days WHERE day >= ? AND day <= ? GROUP BY grp, currency`

	qTopLedger = `SELECT aid, currency, COUNT(*), SUM(CASE WHEN delta > 0 THEN delta ELSE 0 END), SUM(CASE WHEN delta < 0 THEN -delta ELSE 0 END)
	FROM ledger INDEXED BY ledger_time WHERE time >= ? AND time < ? AND aid <> '0' AND currency IN ('gems', 'cash') GROUP BY aid, currency`
	qRollTop = `SELECT aid, currency, SUM(lines), SUM(plus), SUM(minus) FROM ledger_day_aids
	WHERE day >= ? AND day <= ? AND aid <> '0' AND currency IN ('gems', 'cash') GROUP BY aid, currency`
)

// economySums reads one Economy aggregate of the window from since on in one bounded snapshot (ledgerWindowOf, ledgerSumsIn).
func economySums(since time.Time, keys int, rawQuery, rollQuery string) (map[[2]string]ledgerSums, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	var out map[[2]string]ledgerSums
	err = d.ViewBounded(func(ctx context.Context, tx *sql.Tx) error {
		w, err := ledgerWindowOf(tx, since)
		if err != nil {
			return err
		}
		out, err = ledgerSumsIn(ctx, tx, w, keys, rawQuery, rollQuery)
		return err
	})
	return out, err
}

// LedgerFlow returns the movement of each currency since the time since, by currency name.
func LedgerFlow(since time.Time) ([]CurrencyFlow, error) {
	sums, err := economySums(since, 1, qLedgerFlow, qRollFlow)
	if err != nil {
		return nil, err
	}
	out := []CurrencyFlow{}
	for k, s := range sums {
		out = append(out, CurrencyFlow{Currency: k[0], Minted: s.plus, Spent: s.minus, Lines: s.lines})
	}
	slices.SortFunc(out, func(a, b CurrencyFlow) int { return cmp.Compare(a.Currency, b.Currency) })
	return out, nil
}

// ledgerGroupSQL is the group of a ledger reason, computed in SQL so that the aggregate is bounded by the number of groups.
// A "staff:<who>:<reason>" line is "staff grant". Otherwise a reason with a ':' is its first segment and the next one
// ("butler:order:12:3" is "butler:order"; a reason with one ':' and no second segment is the whole reason, "heart:2026-11-17").
// A reason with no ':' is its first two words ("voucher purchase FSV1" is "voucher purchase"; a reason with one word is that word).
const ledgerGroupSQL = `CASE
	WHEN reason LIKE 'staff:%' THEN 'staff grant'
	WHEN instr(reason, ':') > 0 AND instr(substr(reason, instr(reason, ':') + 1), ':') > 0
		THEN substr(reason, 1, instr(reason, ':') + instr(substr(reason, instr(reason, ':') + 1), ':') - 1)
	WHEN instr(reason, ':') = 0 AND instr(reason, ' ') > 0 AND instr(substr(reason, instr(reason, ' ') + 1), ' ') > 0
		THEN substr(reason, 1, instr(reason, ' ') + instr(substr(reason, instr(reason, ' ') + 1), ' ') - 1)
	ELSE reason END`

// ReasonGroup is one group of ledger reasons (ledgerGroupSQL): its lines, and the Gems and Cash it moved in and out.
type ReasonGroup struct {
	Group                            string
	Lines                            int64
	GemsIn, GemsOut, CashIn, CashOut int64
}

// LedgerReasons returns the limit largest reason groups since the time since, most lines first (ties by name). limit is 1 to 100.
func LedgerReasons(since time.Time, limit int) ([]ReasonGroup, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: the reason list is 1 to 100 groups", ErrBadInput)
	}
	sums, err := economySums(since, 2, qLedgerReasons, qRollReasons)
	if err != nil {
		return nil, err
	}
	byGroup := map[string]*ReasonGroup{}
	for k, s := range sums {
		g := byGroup[k[0]]
		if g == nil {
			g = &ReasonGroup{Group: k[0]}
			byGroup[k[0]] = g
		}
		g.Lines += s.lines
		switch k[1] {
		case "gems":
			g.GemsIn, g.GemsOut = g.GemsIn+s.plus, g.GemsOut+s.minus
		case "cash":
			g.CashIn, g.CashOut = g.CashIn+s.plus, g.CashOut+s.minus
		}
	}
	out := make([]ReasonGroup, 0, len(byGroup))
	for _, g := range byGroup {
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b ReasonGroup) int {
		if c := cmp.Compare(b.Lines, a.Lines); c != 0 {
			return c
		}
		return cmp.Compare(a.Group, b.Group)
	})
	return out[:min(limit, len(out))], nil
}

// LedgerTop is one account of a top list: its avatar id and the Gems and Cash it earned or spent in the window (positive).
type LedgerTop struct {
	Aid        string
	Gems, Cash int64
}

// TopLedger returns the limit accounts that earned (spent false) or spent (spent true) the most Gems since the time since, with
// their Cash in the same direction. Accounts are ranked by Gems (ties by avatar id); an account with no Gems in that direction
// is left out, and the accounts with aid "0" (no avatar yet) too. limit is 1 to 100.
func TopLedger(since time.Time, spent bool, limit int) ([]LedgerTop, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: the top list is 1 to 100 accounts", ErrBadInput)
	}
	sums, err := economySums(since, 2, qTopLedger, qRollTop)
	if err != nil {
		return nil, err
	}
	byAid := map[string]*LedgerTop{}
	for k, s := range sums {
		v := s.plus
		if spent {
			v = s.minus
		}
		t := byAid[k[0]]
		if t == nil {
			t = &LedgerTop{Aid: k[0]}
			byAid[k[0]] = t
		}
		if k[1] == "gems" {
			t.Gems += v
		} else {
			t.Cash += v
		}
	}
	out := []LedgerTop{}
	for _, t := range byAid {
		if t.Gems > 0 {
			out = append(out, *t)
		}
	}
	slices.SortFunc(out, func(a, b LedgerTop) int {
		if c := cmp.Compare(b.Gems, a.Gems); c != 0 {
			return c
		}
		return cmp.Compare(a.Aid, b.Aid)
	})
	return out[:min(limit, len(out))], nil
}

// PlayersOfAids returns the accounts with the avatar ids given, keyed by avatar id: one scan of the accounts in memory. An id
// with no account is missing from the map.
func PlayersOfAids(aids []string) map[string]Player {
	want := make(map[string]bool, len(aids))
	for _, aid := range aids {
		want[aid] = true
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	out := make(map[string]Player, len(aids))
	for _, acc := range accountList {
		if want[acc.Aid] {
			out[acc.Aid] = playerOf(acc)
		}
	}
	return out
}

const (
	qDaySignups = `SELECT COUNT(*) FROM accounts INDEXED BY accounts_created WHERE created >= ? AND created < ?`
	qDayChat    = `SELECT COUNT(*) FROM chat_log INDEXED BY chat_log_time WHERE time >= ? AND time < ?`
	qDayLines   = `SELECT COUNT(*) FROM ledger INDEXED BY ledger_time WHERE time >= ? AND time < ?`
	qDayActive  = `SELECT COUNT(*) FROM (
		SELECT aid FROM ledger INDEXED BY ledger_time WHERE time >= ? AND time < ? AND aid <> '0'
		UNION
		SELECT sender FROM chat_log INDEXED BY chat_log_time WHERE time >= ? AND time < ? AND sender <> '0')`
	// The same two for a day the ledger rollups hold (its raw lines may be gone): the first argument is the day.
	qDayLinesRolled  = `SELECT COALESCE(SUM(lines), 0) FROM ledger_days WHERE day = ?`
	qDayActiveRolled = `SELECT COUNT(*) FROM (
		SELECT aid FROM ledger_day_aids WHERE day = ? AND aid <> '0'
		UNION
		SELECT sender FROM chat_log INDEXED BY chat_log_time WHERE time >= ? AND time < ? AND sender <> '0')`
)

// Activity is the per-day counts of the staff Activity page, keyed by the UTC day (YYYY-MM-DD). A day with nothing has no key.
type Activity struct {
	Signups map[string]int64 // accounts created that day
	Chat    map[string]int64 // chat messages logged that day
	Lines   map[string]int64 // ledger lines written that day
	Active  map[string]int64 // avatars with a ledger line or a chat message that day: a daily-active proxy, not logins
}

// activityCounts is one UTC day of the Activity series. A finished day is kept in meta (activityKeyPrefix + day) as JSON.
type activityCounts struct {
	Signups int64 `json:"signups"`
	Chat    int64 `json:"chat"`
	Lines   int64 `json:"lines"`
	Active  int64 `json:"active"`
}

const (
	activityKeyPrefix = "activity.day."
	// activitySettle is how long after its end a day is final: the chat log writer flushes within 250 ms and a ledger line is
	// written with its balance, so nothing more lands in a day this long after it ended.
	activitySettle = 5 * time.Minute
)

// ActivityByDay returns the four series from the UTC day of since to the UTC day of now. Each day is read in its own bounded
// snapshot (ViewBounded), so a read never holds the WAL longer than one day's rows take; a finished day is then kept in meta
// and never read again, so a page load reads only today (and any day not kept yet). A kept day keeps its chat count after the
// retention sweep deletes those messages.
func ActivityByDay(since, now time.Time) (Activity, error) {
	d, err := docDB()
	if err != nil {
		return Activity{}, err
	}
	from := since.UTC().Truncate(24 * time.Hour)
	kept := map[string]activityCounts{}
	err = d.View(func(q Querier) error {
		rows, err := q.Query(`SELECT key, value FROM meta WHERE key >= ? AND key < ?`, activityKeyPrefix+from.Format(time.DateOnly), activityKeyPrefix+"~")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, value string
			var c activityCounts
			if err := rows.Scan(&key, &value); err != nil {
				return err
			}
			if json.Unmarshal([]byte(value), &c) == nil {
				kept[strings.TrimPrefix(key, activityKeyPrefix)] = c
			}
		}
		return rows.Err()
	})
	if err != nil {
		return Activity{}, err
	}
	act := Activity{Signups: map[string]int64{}, Chat: map[string]int64{}, Lines: map[string]int64{}, Active: map[string]int64{}}
	for day := from; !day.After(now); day = day.AddDate(0, 0, 1) {
		key := day.Format(time.DateOnly)
		c, ok := kept[key]
		if !ok {
			if c, err = readActivityDay(d, day); err != nil {
				return Activity{}, err
			}
			if !now.Before(day.AddDate(0, 0, 1).Add(activitySettle)) {
				keepActivityDay(d, key, c)
			}
		}
		for _, s := range []struct {
			dst map[string]int64
			n   int64
		}{{act.Signups, c.Signups}, {act.Chat, c.Chat}, {act.Lines, c.Lines}, {act.Active, c.Active}} {
			if s.n > 0 {
				s.dst[key] = s.n
			}
		}
	}
	return act, nil
}

// readActivityDay counts one UTC day in one bounded snapshot. The ledger and the accounts keep RFC 3339 UTC text, so the day's
// rows are the text range [day, next day); the chat log keeps Unix milliseconds. A day the ledger rollups hold takes its
// ledger counts from them.
func readActivityDay(d *DB, day time.Time) (activityCounts, error) {
	next := day.AddDate(0, 0, 1)
	d1, d2 := day.Format(time.DateOnly), next.Format(time.DateOnly)
	m1, m2 := day.UnixMilli(), next.UnixMilli()
	var c activityCounts
	err := d.ViewBounded(func(ctx context.Context, tx *sql.Tx) error {
		through, err := metaValue(tx, ledgerRolledKey)
		if err != nil {
			return err
		}
		lines, linesArgs := qDayLines, []any{d1, d2}
		active, activeArgs := qDayActive, []any{d1, d2, m1, m2}
		if through != "" && d1 <= through {
			lines, linesArgs = qDayLinesRolled, []any{d1}
			active, activeArgs = qDayActiveRolled, []any{d1, m1, m2}
		}
		for _, q := range []struct {
			dst   *int64
			query string
			args  []any
		}{
			{&c.Signups, qDaySignups, []any{d1, d2}},
			{&c.Chat, qDayChat, []any{m1, m2}},
			{&c.Lines, lines, linesArgs},
			{&c.Active, active, activeArgs},
		} {
			if err := tx.QueryRowContext(ctx, q.query, q.args...).Scan(q.dst); err != nil {
				return err
			}
		}
		return nil
	})
	return c, err
}

// keepActivityDay stores a finished day's counts. A failure is only logged: the day is read again on the next load.
func keepActivityDay(d *DB, key string, c activityCounts) {
	value, _ := json.Marshal(c)
	if err := d.Update(func(tx *sql.Tx) error { return setMetaTx(tx, activityKeyPrefix+key, string(value)) }); err != nil {
		log.Printf("cherry: activity day %s not kept: %v", key, err)
	}
}
