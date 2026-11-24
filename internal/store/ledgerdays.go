package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// The ledger rolled up by finished UTC day (schema v12: ledger_days, ledger_day_aids) and the ledger's retention. The hourly
// sweep rolls up every finished day not rolled up yet, then deletes the raw lines older than retention.ledger-days whose day
// is rolled up. The Economy page reads the finished days of its window from the rollups and only the edges from raw lines, so
// its cost follows one day's traffic, not the history kept; the totals outlive the deleted lines.

const (
	ledgerRolledKey = "ledger.rolled-through" // meta: the last UTC day rolled up (YYYY-MM-DD); absent before the first
	// LedgerMinDays is the shortest ledger retention: the Economy page's widest window (30 days) reads its partial first day
	// from raw lines, and MigrateButlerRoster refunds an unexpired 30-day butler hire from its charge line.
	LedgerMinDays    = 30
	ledgerPruneChunk = 5000 // lines one delete transaction removes, so a backlog never holds the write lock for long
)

// qLedgerHourRollup sums one hour of the ledger per avatar, currency and reason group.
const qLedgerHourRollup = `SELECT aid, currency, ` + ledgerGroupSQL + `, COUNT(*),
	SUM(CASE WHEN delta > 0 THEN delta ELSE 0 END), SUM(CASE WHEN delta < 0 THEN -delta ELSE 0 END)
	FROM ledger INDEXED BY ledger_time WHERE time >= ? AND time < ? GROUP BY 1, 2, 3`

// ledgerSums are the totals of a set of ledger lines: how many, and the credits and debits (both positive).
type ledgerSums struct{ lines, plus, minus int64 }

func (s ledgerSums) add(o ledgerSums) ledgerSums {
	return ledgerSums{s.lines + o.lines, s.plus + o.plus, s.minus + o.minus}
}

// RollupLedgerDays rolls up every finished UTC day after the last one rolled up (from the ledger's first day the first time)
// and returns how many it rolled up. A day is finished activitySettle after its end.
func RollupLedgerDays(now time.Time) (int, error) {
	d := attachedDB()
	if d == nil {
		return 0, ErrNoDatabase
	}
	var through, first string
	if err := d.View(func(q Querier) error {
		var err error
		if through, err = metaValue(q, ledgerRolledKey); err != nil || through != "" {
			return err
		}
		return q.QueryRow(`SELECT COALESCE(MIN(time), '') FROM ledger`).Scan(&first)
	}); err != nil {
		return 0, err
	}
	var day time.Time
	switch {
	case through != "":
		t, err := time.Parse(time.DateOnly, through)
		if err != nil {
			return 0, fmt.Errorf("ledger rollup mark %q: %w", through, err)
		}
		day = t.AddDate(0, 0, 1)
	case first != "":
		t, err := time.Parse(time.RFC3339, first)
		if err != nil {
			return 0, fmt.Errorf("first ledger line time %q: %w", first, err)
		}
		day = t.UTC().Truncate(24 * time.Hour)
	default:
		return 0, nil // no ledger line yet
	}
	n := 0
	for ; !now.Before(day.AddDate(0, 0, 1).Add(activitySettle)); day = day.AddDate(0, 0, 1) {
		if err := rollupLedgerDay(d, day); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// rollupLedgerDay reads one UTC day in 24 one-hour snapshots (each bounded, so no read holds the WAL for long) and writes
// its rows of both rollups with the new progress mark in one transaction.
func rollupLedgerDay(d *DB, day time.Time) error {
	type groupKey struct{ currency, grp string }
	type aidKey struct{ aid, currency string }
	groups := map[groupKey]ledgerSums{}
	aids := map[aidKey]ledgerSums{}
	for h := range 24 {
		from := day.Add(time.Duration(h) * time.Hour)
		if err := d.ViewBounded(func(ctx context.Context, tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, qLedgerHourRollup, ledgerSince(from), ledgerSince(from.Add(time.Hour)))
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var aid, currency, grp string
				var s ledgerSums
				if err := rows.Scan(&aid, &currency, &grp, &s.lines, &s.plus, &s.minus); err != nil {
					return err
				}
				gk, ak := groupKey{currency, grp}, aidKey{aid, currency}
				groups[gk] = groups[gk].add(s)
				aids[ak] = aids[ak].add(s)
			}
			return rows.Err()
		}); err != nil {
			return fmt.Errorf("ledger rollup of %s: %w", day.Format(time.DateOnly), err)
		}
	}
	key := day.Format(time.DateOnly)
	return d.Update(func(tx *sql.Tx) error {
		gs, err := tx.Prepare(`INSERT OR REPLACE INTO ledger_days (day, currency, grp, lines, plus, minus) VALUES (?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer gs.Close()
		for k, s := range groups {
			if _, err := gs.Exec(key, k.currency, k.grp, s.lines, s.plus, s.minus); err != nil {
				return err
			}
		}
		as, err := tx.Prepare(`INSERT OR REPLACE INTO ledger_day_aids (day, aid, currency, lines, plus, minus) VALUES (?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer as.Close()
		for k, s := range aids {
			if _, err := as.Exec(key, k.aid, k.currency, s.lines, s.plus, s.minus); err != nil {
				return err
			}
		}
		return setMetaTx(tx, ledgerRolledKey, key)
	})
}

// PruneLedger deletes the raw ledger lines older than days (at least LedgerMinDays) whose UTC day is rolled up, in
// transactions of ledgerPruneChunk lines, and returns how many it deleted. Zero days keeps every line.
func PruneLedger(now time.Time, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	d := attachedDB()
	if d == nil {
		return 0, ErrNoDatabase
	}
	var through string
	if err := d.View(func(q Querier) error {
		var err error
		through, err = metaValue(q, ledgerRolledKey)
		return err
	}); err != nil || through == "" {
		return 0, err
	}
	rolled, err := time.Parse(time.DateOnly, through)
	if err != nil {
		return 0, fmt.Errorf("ledger rollup mark %q: %w", through, err)
	}
	cutoff := now.Add(-time.Duration(max(days, LedgerMinDays)) * 24 * time.Hour)
	if end := rolled.AddDate(0, 0, 1); end.Before(cutoff) {
		cutoff = end // never a line whose day is not rolled up
	}
	bound := ledgerSince(cutoff)
	var total int64
	for {
		var n int64
		err := d.Update(func(tx *sql.Tx) error {
			res, err := tx.Exec(`DELETE FROM ledger WHERE id IN (SELECT id FROM ledger INDEXED BY ledger_time WHERE time < ? LIMIT ?)`, bound, ledgerPruneChunk)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		total += n
		if err != nil || n < ledgerPruneChunk {
			return total, err
		}
	}
}

// ledgerWindow is an Economy window [since, now] split into the finished days the rollups hold (fromDay to toDay, both
// included; rolled false when none) and the raw ledger ranges around them, [from, to) in the ledger's time text.
type ledgerWindow struct {
	raw            [][2]string
	fromDay, toDay string
	rolled         bool
}

// ledgerOpenEnd sorts after every RFC 3339 time: the upper bound of the last raw range.
const ledgerOpenEnd = "~"

// ledgerWindowOf splits the window from since on: the whole days from the first midnight at or after since through the last
// day rolled up come from the rollups, the rest (the part day before, and everything after the last rolled day) from raw lines.
func ledgerWindowOf(q Querier, since time.Time) (ledgerWindow, error) {
	start := ledgerSince(since)
	through, err := metaValue(q, ledgerRolledKey)
	if err != nil {
		return ledgerWindow{}, err
	}
	first := since.UTC().Truncate(24 * time.Hour)
	if first.Before(since) {
		first = first.AddDate(0, 0, 1)
	}
	last, perr := time.Parse(time.DateOnly, through)
	if through == "" || perr != nil || last.Before(first) {
		return ledgerWindow{raw: [][2]string{{start, ledgerOpenEnd}}}, nil
	}
	w := ledgerWindow{fromDay: first.Format(time.DateOnly), toDay: through, rolled: true}
	if head := ledgerSince(first); start < head {
		w.raw = append(w.raw, [2]string{start, head})
	}
	w.raw = append(w.raw, [2]string{ledgerSince(last.AddDate(0, 0, 1)), ledgerOpenEnd})
	return w, nil
}

// ledgerSumsIn adds up a window: rawQuery runs over each raw range (its first two arguments are the range) and rollQuery
// over the rolled days (its first two arguments are fromDay and toDay). Both return their key columns (one or two text
// columns, keys of them) and then lines, plus and minus; every row is added into the map under its key.
func ledgerSumsIn(ctx context.Context, tx *sql.Tx, w ledgerWindow, keys int, rawQuery, rollQuery string) (map[[2]string]ledgerSums, error) {
	out := map[[2]string]ledgerSums{}
	read := func(query string, args ...any) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k [2]string
			var s ledgerSums
			dst := []any{&k[0]}
			if keys == 2 {
				dst = append(dst, &k[1])
			}
			if err := rows.Scan(append(dst, &s.lines, &s.plus, &s.minus)...); err != nil {
				return err
			}
			out[k] = out[k].add(s)
		}
		return rows.Err()
	}
	for _, r := range w.raw {
		if err := read(rawQuery, r[0], r[1]); err != nil {
			return nil, err
		}
	}
	if w.rolled {
		if err := read(rollQuery, w.fromDay, w.toDay); err != nil {
			return nil, err
		}
	}
	return out, nil
}
