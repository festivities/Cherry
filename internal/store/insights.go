package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// The staff Insights (PLAN Roadmap 8, staff "Insights"): read-only aggregates over the ledger, the accounts and the chat log.
// Every query is bounded by a window in time (the ledger_time, accounts_created and chat_log_time indexes give the range) and
// by a LIMIT, and the staff pages cache the results for about a minute (admin/insights.go). Every read runs in ViewBounded, so
// a window grown too large fails the page instead of holding the WAL. Balances come from the accounts in memory; the ledger
// and the chat log are read from the database.
// ponytail: the Economy windows read every ledger line in them (about 500k a day on 2026-11-25, no retention); the 30-day
// window will reach the read cap. Day rollups like ActivityByDay's are the upgrade.

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

// qLedgerFlow, qLedgerReasons, qTopLedger and the activity queries are the statements the functions below run. They are
// constants so the store's tests can ask SQLite for their plans (insights_test.go).
const qLedgerFlow = `SELECT currency,
	SUM(CASE WHEN delta > 0 THEN delta ELSE 0 END), SUM(CASE WHEN delta < 0 THEN -delta ELSE 0 END), COUNT(*)
	FROM ledger INDEXED BY ledger_time WHERE time >= ? GROUP BY currency ORDER BY currency`

// LedgerFlow returns the movement of each currency since the time since, by currency name.
func LedgerFlow(since time.Time) ([]CurrencyFlow, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	out := []CurrencyFlow{}
	err = d.ViewBounded(func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, qLedgerFlow, ledgerSince(since))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f CurrencyFlow
			if err := rows.Scan(&f.Currency, &f.Minted, &f.Spent, &f.Lines); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
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

const qLedgerReasons = `SELECT grp, COUNT(*),
	SUM(CASE WHEN currency = 'gems' AND delta > 0 THEN delta ELSE 0 END), SUM(CASE WHEN currency = 'gems' AND delta < 0 THEN -delta ELSE 0 END),
	SUM(CASE WHEN currency = 'cash' AND delta > 0 THEN delta ELSE 0 END), SUM(CASE WHEN currency = 'cash' AND delta < 0 THEN -delta ELSE 0 END)
	FROM (SELECT ` + ledgerGroupSQL + ` AS grp, currency, delta FROM ledger INDEXED BY ledger_time WHERE time >= ?)
	GROUP BY grp ORDER BY COUNT(*) DESC, grp LIMIT ?`

// ReasonGroup is one group of ledger reasons (ledgerGroupSQL): its lines, and the Gems and Cash it moved in and out.
type ReasonGroup struct {
	Group                            string
	Lines                            int64
	GemsIn, GemsOut, CashIn, CashOut int64
}

// LedgerReasons returns the limit largest reason groups since the time since, most lines first (ties by name). limit is 1 to 100.
func LedgerReasons(since time.Time, limit int) ([]ReasonGroup, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: the reason list is 1 to 100 groups", ErrBadInput)
	}
	out := []ReasonGroup{}
	err = d.ViewBounded(func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, qLedgerReasons, ledgerSince(since), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g ReasonGroup
			if err := rows.Scan(&g.Group, &g.Lines, &g.GemsIn, &g.GemsOut, &g.CashIn, &g.CashOut); err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

// LedgerTop is one account of a top list: its avatar id and the Gems and Cash it earned or spent in the window (positive).
type LedgerTop struct {
	Aid        string
	Gems, Cash int64
}

const qTopLedger = `SELECT aid,
	SUM(CASE WHEN currency = 'gems' THEN ABS(delta) ELSE 0 END) AS gems,
	SUM(CASE WHEN currency = 'cash' THEN ABS(delta) ELSE 0 END) AS cash
	FROM ledger INDEXED BY ledger_time WHERE time >= ? AND aid <> '0' AND currency IN ('gems', 'cash') AND (delta > 0) = ?
	GROUP BY aid HAVING gems > 0 ORDER BY gems DESC, aid LIMIT ?`

// TopLedger returns the limit accounts that earned (spent false) or spent (spent true) the most Gems since the time since, with
// their Cash in the same direction. Accounts are ranked by Gems; an account with no Gems in that direction is left out, and the
// accounts with aid "0" (no avatar yet) too. limit is 1 to 100.
func TopLedger(since time.Time, spent bool, limit int) ([]LedgerTop, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: the top list is 1 to 100 accounts", ErrBadInput)
	}
	earned := 1
	if spent {
		earned = 0
	}
	out := []LedgerTop{}
	err = d.ViewBounded(func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, qTopLedger, ledgerSince(since), earned, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t LedgerTop
			if err := rows.Scan(&t.Aid, &t.Gems, &t.Cash); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
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
// rows are the text range [day, next day); the chat log keeps Unix milliseconds.
func readActivityDay(d *DB, day time.Time) (activityCounts, error) {
	next := day.AddDate(0, 0, 1)
	d1, d2 := day.Format(time.DateOnly), next.Format(time.DateOnly)
	m1, m2 := day.UnixMilli(), next.UnixMilli()
	var c activityCounts
	err := d.ViewBounded(func(ctx context.Context, tx *sql.Tx) error {
		for _, q := range []struct {
			dst   *int64
			query string
			args  []any
		}{
			{&c.Signups, qDaySignups, []any{d1, d2}},
			{&c.Chat, qDayChat, []any{m1, m2}},
			{&c.Lines, qDayLines, []any{d1, d2}},
			{&c.Active, qDayActive, []any{d1, d2, m1, m2}},
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
