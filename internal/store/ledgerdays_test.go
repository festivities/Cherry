package store

import (
	"database/sql"
	"reflect"
	"testing"
	"time"
)

// economyOf is everything the Economy page reads for one window.
type economyOf struct {
	Flow                []CurrencyFlow
	Reasons             []ReasonGroup
	EarnedTop, SpentTop []LedgerTop
}

func readEconomy(t *testing.T, since time.Time) economyOf {
	t.Helper()
	var e economyOf
	var err error
	if e.Flow, err = LedgerFlow(since); err != nil {
		t.Fatal(err)
	}
	if e.Reasons, err = LedgerReasons(since, 100); err != nil {
		t.Fatal(err)
	}
	if e.EarnedTop, err = TopLedger(since, false, 100); err != nil {
		t.Fatal(err)
	}
	if e.SpentTop, err = TopLedger(since, true, 100); err != nil {
		t.Fatal(err)
	}
	return e
}

// TestLedgerRollupKeepsEconomyAndPrunes: rolling the finished days up changes no Economy total, for a window that starts at
// a midnight, one that starts mid-day (its part day is read raw) and the last 24 hours (all raw); the totals stay the same
// once the raw lines of the rolled days are gone. PruneLedger keeps at least LedgerMinDays and never deletes a day that is not
// rolled up, and the Activity page counts a pruned day from the rollups.
func TestLedgerRollupKeepsEconomyAndPrunes(t *testing.T) {
	d := attachTestDB(t)
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	d1, d2, d3 := today.AddDate(0, 0, -3), today.AddDate(0, 0, -2), today.AddDate(0, 0, -1)
	at := func(t time.Time) string { return t.Format(time.RFC3339) }
	seedLedger(t, d,
		[5]any{at(d1.Add(time.Hour)), "1001", "gems", 100, "welcome gift"},
		[5]any{at(d1.Add(5 * time.Hour)), "1002", "gems", -30, "shop:dress:CUAA"},
		[5]any{at(d1.Add(22 * time.Hour)), "1003", "gems", 5, "heart:x"},
		[5]any{at(d2.Add(2 * time.Hour)), "1001", "cash", 7, "butler:order:12:3"},
		[5]any{at(d2.Add(20 * time.Hour)), "1003", "gems", -15, "gift send to 5 X"},
		[5]any{at(d3.Add(3 * time.Hour)), "1002", "gems", 50, "staff:discord:1:prize"},
		[5]any{at(d3.Add(23 * time.Hour)), "0", "gems", 500, "staff:discord:1:no avatar"},
		[5]any{at(today), "1001", "gems", -10, "shop:dress:CUAB"},
	)
	windows := map[string]time.Time{"aligned": d1, "mid-day": d2.Add(-6 * time.Hour), "24h": now.Add(-24 * time.Hour)}
	before := map[string]economyOf{}
	for name, since := range windows {
		before[name] = readEconomy(t, since)
	}
	if len(before["aligned"].Flow) != 2 || before["aligned"].EarnedTop[0].Aid != "1001" {
		t.Fatalf("seeded economy = %+v", before["aligned"])
	}

	if n, err := RollupLedgerDays(now); err != nil || n != 3 {
		t.Fatalf("rolled up %d days (%v), want the three finished days", n, err)
	}
	if n, err := RollupLedgerDays(now); err != nil || n != 0 {
		t.Fatalf("a second rollup rolled %d days (%v), want none", n, err)
	}
	check := func(stage string) {
		t.Helper()
		for name, since := range windows {
			if got := readEconomy(t, since); !reflect.DeepEqual(got, before[name]) {
				t.Errorf("%s, %s window:\n got %+v\nwant %+v", stage, name, got, before[name])
			}
		}
	}
	check("after the rollup")
	// The raw lines of d2, a rolled day the aligned and mid-day windows hold whole, go: those windows must read it from the
	// rollups. (The mid-day window's part day d1 22:00 and the 24-hour window's part of d3 are read raw, as they must be.)
	if err := d.Update(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM ledger WHERE time >= ? AND time < ?`, at(d2), at(d3))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check("without the rolled days' raw lines")

	if n, err := PruneLedger(now, 30); err != nil || n != 0 {
		t.Fatalf("pruned %d lines (%v) at 30 days, want none (every line is newer)", n, err)
	}
	// 40 days on, a 7-day retention is raised to LedgerMinDays (30), and the cut stops at the end of the last rolled day: the
	// three d1 lines and the two d3 lines go, today's line (not rolled up) stays.
	if n, err := PruneLedger(today.AddDate(0, 0, 40), 7); err != nil || n != 5 {
		t.Fatalf("pruned %d lines (%v), want the d1 and d3 lines", n, err)
	}
	var left int
	if err := d.read.QueryRow(`SELECT COUNT(*) FROM ledger`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("%d raw lines left (%v), want today's", left, err)
	}
	if got := readEconomy(t, d1); !reflect.DeepEqual(got, before["aligned"]) {
		t.Errorf("aligned window after the prune:\n got %+v\nwant %+v", got, before["aligned"])
	}
	act, err := ActivityByDay(d1, now)
	if err != nil {
		t.Fatal(err)
	}
	if key := d1.Format(time.DateOnly); act.Lines[key] != 3 || act.Active[key] != 3 {
		t.Errorf("activity of the pruned day: %d lines, %d active, want 3 and 3 from the rollups", act.Lines[key], act.Active[key])
	}
}
