package economy

import (
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

func installHighFiveAccounts(t *testing.T, aids ...string) {
	t.Helper()
	entries := make(map[string]*store.Account, len(aids))
	for _, aid := range aids {
		entries["hf-"+aid] = &store.Account{
			AccessToken: "hf-" + aid,
			SessionKey:  "sk",
			Aid:         aid,
			Name:        "P" + aid,
		}
	}
	testutil.InstallAccounts(t, entries)
}

func pinEconomyClock(t *testing.T, initial time.Time) *time.Time {
	t.Helper()
	now := initial
	prev := NowFunc
	NowFunc = func() time.Time { return now }
	t.Cleanup(func() { NowFunc = prev })
	return &now
}

func TestRecordHighFiveOneWayAndRollover(t *testing.T) {
	installHighFiveAccounts(t, "101", "202")
	// 05:59 JST on Nov 24 -> before 06:00 JST rollover.
	now := pinEconomyClock(t, time.Date(2026, 11, 24, 5, 59, 0, 0, jst))

	count, paid, ok := RecordHighFive("101", "202")
	if !ok || count != 1 || paid != 0 {
		t.Fatalf("first 101->202 = (%d, %d, %v), want (1, 0, true)", count, paid, ok)
	}

	// Repeat A -> B on the same game day fails.
	count, paid, ok = RecordHighFive("101", "202")
	if ok || count != 1 || paid != 0 {
		t.Fatalf("repeat 101->202 = (%d, %d, %v), want (1, 0, false)", count, paid, ok)
	}

	// Target B's state was untouched by A -> B, so B -> A succeeds once for B's own quest.
	if c, tgs, _, _ := HighFiveProgress("202"); c != 0 || tgs != nil {
		t.Fatalf("target 202 progress after 101->202 = (%d, %v), want (0, nil)", c, tgs)
	}
	count, paid, ok = RecordHighFive("202", "101")
	if !ok || count != 1 || paid != 0 {
		t.Fatalf("reverse 202->101 = (%d, %d, %v), want (1, 0, true)", count, paid, ok)
	}
	// Repeat B -> A on the same game day fails.
	count, paid, ok = RecordHighFive("202", "101")
	if ok || count != 1 || paid != 0 {
		t.Fatalf("repeat 202->101 = (%d, %d, %v), want (1, 0, false)", count, paid, ok)
	}

	// Self, empty, and unknown accounts fail.
	for _, tc := range [][2]string{{"", "202"}, {"101", ""}, {"101", "101"}, {"101", "999"}, {"999", "202"}} {
		if c, p, ok := RecordHighFive(tc[0], tc[1]); ok || p != 0 {
			t.Fatalf("invalid (%q, %q) = (%d, %d, %v), want ok=false paid=0", tc[0], tc[1], c, p, ok)
		}
	}

	// Cross 06:00 JST boundary: both A -> B and B -> A reset independently.
	*now = time.Date(2026, 11, 24, 6, 0, 0, 0, jst)
	if c, tgs, goal, reward := HighFiveProgress("101"); c != 0 || tgs != nil || goal != HighFiveDailyGoal || reward != HighFiveRewardGems {
		t.Fatalf("rolled-over progress = (%d, %v, %d, %d), want (0, nil, %d, %d)", c, tgs, goal, reward, HighFiveDailyGoal, HighFiveRewardGems)
	}
	count, paid, ok = RecordHighFive("202", "101")
	if !ok || count != 1 || paid != 0 {
		t.Fatalf("new-day 202->101 = (%d, %d, %v), want (1, 0, true)", count, paid, ok)
	}
	// 101 -> 202 succeeds on the new game day, then blocks repeat until next 06:00 JST rollover.
	count, paid, ok = RecordHighFive("101", "202")
	if !ok || count != 1 || paid != 0 {
		t.Fatalf("new-day 101->202 = (%d, %d, %v), want (1, 0, true)", count, paid, ok)
	}
	count, paid, ok = RecordHighFive("101", "202")
	if ok || count != 1 || paid != 0 {
		t.Fatalf("new-day repeat 101->202 = (%d, %d, %v), want (1, 0, false)", count, paid, ok)
	}
	// Next 06:00 JST boundary lets 101 -> 202 succeed again.
	*now = time.Date(2026, 11, 25, 6, 0, 0, 0, jst)
	count, paid, ok = RecordHighFive("101", "202")
	if !ok || count != 1 || paid != 0 {
		t.Fatalf("day3 101->202 = (%d, %d, %v), want (1, 0, true)", count, paid, ok)
	}
}

func TestRecordHighFiveDailyQuestRewardAndPersistence(t *testing.T) {
	aids := make([]string, 0, 23)
	aids = append(aids, "100")
	for i := 1; i <= 22; i++ {
		aids = append(aids, strconv.Itoa(200+i))
	}
	installHighFiveAccounts(t, aids...)
	testutil.UseDatabase(t)
	now := pinEconomyClock(t, time.Date(2026, 11, 24, 12, 0, 0, 0, jst))
	day := AttendDayKey(*now)

	// First 19 unique high-fives pay 0 Gems.
	for i := 1; i < HighFiveDailyGoal; i++ {
		target := strconv.Itoa(200 + i)
		count, paid, ok := RecordHighFive("100", target)
		if !ok || count != i || paid != 0 {
			t.Fatalf("highfive %d -> %s = (%d, %d, %v), want (%d, 0, true)", i, target, count, paid, ok, i)
		}
	}
	snap, _ := store.AccountByAvatarID("100")
	if snap.HighFive.Claimed || snap.HighFive.Count != 19 {
		t.Fatalf("at 19: HighFive = %+v", snap.HighFive)
	}

	// 20th unique high-five pays +4000 Gems once and writes ledger entry highfive:<day>.
	count, paid, ok := RecordHighFive("100", "220")
	if !ok || count != 20 || paid != HighFiveRewardGems {
		t.Fatalf("20th highfive = (%d, %d, %v), want (20, %d, true)", count, paid, ok, HighFiveRewardGems)
	}

	// 21st unique high-five still succeeds with no hard cap and no second Gem payout.
	count, paid, ok = RecordHighFive("100", "221")
	if !ok || count != 21 || paid != 0 {
		t.Fatalf("21st highfive = (%d, %d, %v), want (21, 0, true)", count, paid, ok)
	}

	// Verify ledger line.
	lines, err := store.LedgerLines()
	if err != nil {
		t.Fatal(err)
	}
	wantReason := fmt.Sprintf("highfive:%d", day)
	var found int
	for _, l := range lines {
		if l.Aid == "100" && l.Reason == wantReason {
			found++
			if l.Currency != "gems" || l.Delta != HighFiveRewardGems || l.Balance != HighFiveRewardGems {
				t.Fatalf("ledger line = %+v, want gems +%d bal %d", l, HighFiveRewardGems, HighFiveRewardGems)
			}
		}
	}
	if found != 1 {
		t.Fatalf("ledger entries with reason %q = %d, want 1", wantReason, found)
	}

	// Reload from SQLite (simulating server restart) and verify state persists.
	if err := store.ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	gotCount, gotTargets, goal, reward := HighFiveProgress("100")
	if gotCount != 21 || len(gotTargets) != 21 || goal != HighFiveDailyGoal || reward != HighFiveRewardGems {
		t.Fatalf("reloaded progress = (%d, len=%d, %d, %d)", gotCount, len(gotTargets), goal, reward)
	}
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("100")
	gems := acc.Gems
	claimed := acc.HighFive.Claimed
	store.AccountsMu.Unlock()
	if gems != HighFiveRewardGems || !claimed {
		t.Fatalf("reloaded account gems=%d claimed=%v, want %d true", gems, claimed, HighFiveRewardGems)
	}

	// Target 201 -> 100 succeeds once for 201's own quest after restart, then blocks repeat 201 -> 100.
	if c, p, ok := RecordHighFive("201", "100"); !ok || c != 1 || p != 0 {
		t.Fatalf("post-reload reverse 201->100 = (%d, %d, %v), want (1, 0, true)", c, p, ok)
	}
	if c, p, ok := RecordHighFive("201", "100"); ok || c != 1 || p != 0 {
		t.Fatalf("post-reload repeat 201->100 = (%d, %d, %v), want (1, 0, false)", c, p, ok)
	}
	// New target 222 still succeeds with no double payment.
	if c, p, ok := RecordHighFive("100", "222"); !ok || c != 22 || p != 0 {
		t.Fatalf("post-reload 22nd = (%d, %d, %v), want (22, 0, true)", c, p, ok)
	}

	// Next game day resets Claimed so 20 more high-fives can earn another 4000 Gems.
	*now = now.Add(24 * time.Hour)
	for i := 1; i <= HighFiveDailyGoal; i++ {
		target := strconv.Itoa(200 + i)
		c, p, ok := RecordHighFive("100", target)
		wantPaid := int64(0)
		if i == HighFiveDailyGoal {
			wantPaid = HighFiveRewardGems
		}
		if !ok || c != i || p != wantPaid {
			t.Fatalf("day2 highfive %d = (%d, %d, %v), want (%d, %d, true)", i, c, p, ok, i, wantPaid)
		}
	}
	store.AccountsMu.Lock()
	gemsDay2 := store.AccountByAidLocked("100").Gems
	store.AccountsMu.Unlock()
	if gemsDay2 != 2*HighFiveRewardGems {
		t.Fatalf("gems after day2 = %d, want %d", gemsDay2, 2*HighFiveRewardGems)
	}
	if c, tgs, _, _ := HighFiveProgress("201"); c != 0 || slices.Contains(tgs, "100") {
		t.Fatalf("target 201 progress on day2 = (%d, %v), want (0, without 100)", c, tgs)
	}
}
