package economy

import (
	"errors"
	"testing"
	"time"

	"cherry/internal/store"
)

// treasureAt is a JST instant on the 2026 season calendar.
func treasureAt(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, treasureJST)
}

// TestTreasureCycleMath: the default season repeats. Season 0 plays 28 days, its rank claim days (7) follow as the reward
// period, and season 1 starts the moment that period ends, with the next id. The cycle is the same at any later season.
func TestTreasureCycleMath(t *testing.T) {
	first := treasureAt(2026, 11, 1, 6, 0)
	cases := []struct {
		name  string
		at    time.Time
		id    int64
		phase TreasurePhase
		start time.Time
	}{
		{"before the first season", treasureAt(2026, 10, 31, 12, 0), 1950000001, TreasurePhaseClosed, first},
		{"first instant of season 0", first, 1950000001, TreasurePhasePlay, first},
		{"last play minute of season 0", treasureAt(2026, 11, 29, 5, 59), 1950000001, TreasurePhasePlay, first},
		{"reward period of season 0", treasureAt(2026, 11, 29, 6, 0), 1950000001, TreasurePhaseReward, first},
		{"last reward minute of season 0", treasureAt(2026, 12, 6, 5, 59), 1950000001, TreasurePhaseReward, first},
		{"season 1 starts", treasureAt(2026, 12, 6, 6, 0), 1950000002, TreasurePhasePlay, first.AddDate(0, 0, 35)},
		{"season 3 starts", first.AddDate(0, 0, 3*35), 1950000004, TreasurePhasePlay, first.AddDate(0, 0, 3*35)},
		{"reward of season 3", first.AddDate(0, 0, 3*35+28), 1950000004, TreasurePhaseReward, first.AddDate(0, 0, 3*35)},
	}
	for _, c := range cases {
		got := TreasureCurrent(c.at)
		if got.ID != c.id || got.Phase != c.phase || !got.Start.Equal(c.start) {
			t.Fatalf("%s: at %s = id %d phase %d start %s, want id %d phase %d start %s",
				c.name, c.at, got.ID, got.Phase, got.Start, c.id, c.phase, c.start)
		}
		if want := c.start.Add(28 * 24 * time.Hour); !got.End.Equal(want) {
			t.Fatalf("%s: play end %s, want %s", c.name, got.End, want)
		}
		if want := c.start.AddDate(0, 0, 35); !got.ClaimEnd.Equal(want) {
			t.Fatalf("%s: reward end %s, want %s", c.name, got.ClaimEnd, want)
		}
	}
}

// TestTreasureCycleWithoutRepeat: with season.repeat off only season 0 exists. Its reward period is followed by a closed
// cycle that keeps season 0's id, and the cycle is closed before the first season too.
func TestTreasureCycleWithoutRepeat(t *testing.T) {
	d := *ActiveTreasureData()
	d.Season.Repeat = false
	cases := []struct {
		at    time.Time
		id    int64
		phase TreasurePhase
	}{
		{treasureAt(2026, 10, 31, 12, 0), 1950000001, TreasurePhaseClosed},
		{treasureAt(2026, 11, 15, 12, 0), 1950000001, TreasurePhasePlay},
		{treasureAt(2026, 12, 1, 12, 0), 1950000001, TreasurePhaseReward},
		{treasureAt(2026, 12, 6, 6, 0), 1950000001, TreasurePhaseClosed},
		{treasureAt(2027, 3, 1, 12, 0), 1950000001, TreasurePhaseClosed},
	}
	for _, c := range cases {
		got := d.at(c.at)
		if got.ID != c.id || got.Phase != c.phase {
			t.Fatalf("repeat off at %s = id %d phase %d, want id %d phase %d", c.at, got.ID, got.Phase, c.id, c.phase)
		}
	}
}

// TestTreasureCycleStopsAtBlockEnd: a repeating cycle stops at the last id the Cherry block holds. Past that the phase is
// closed and the id stays the last one.
func TestTreasureCycleStopsAtBlockEnd(t *testing.T) {
	d := *ActiveTreasureData()
	d.Season.ID = CherryTreasureLast - 1
	first := treasureAt(2026, 11, 1, 6, 0)
	if got := d.at(first.AddDate(0, 0, 35)); got.ID != CherryTreasureLast || got.Phase != TreasurePhasePlay {
		t.Fatalf("last season = id %d phase %d, want %d in play", got.ID, got.Phase, CherryTreasureLast)
	}
	for _, at := range []time.Time{first.AddDate(0, 0, 2*35), first.AddDate(0, 0, 9*35+3)} {
		if got := d.at(at); got.ID != CherryTreasureLast || got.Phase != TreasurePhaseClosed {
			t.Fatalf("past the block at %s = id %d phase %d, want %d closed", at, got.ID, got.Phase, CherryTreasureLast)
		}
	}
}

// TestTreasureRewardPeriodClaims: during a season's reward period the board is closed to actions, while the point claim and the
// rank claim still pay, and a roll changes nothing.
func TestTreasureRewardPeriodClaims(t *testing.T) {
	butlerAccount(t, "1001", 0, 0)
	treasureState(t, "1001", store.TreasureRecord{Season: 1950000001, Tickets: 10, Charged: treasureNow.UnixMilli(), Points: 300})
	reward := treasureAt(2026, 12, 1, 12, 0)
	if _, err := TreasureOpen("1001", reward); !errors.Is(err, ErrTreasureNoSeason) {
		t.Fatalf("open in the reward period: %v, want no season", err)
	}
	before, _ := store.TreasureRecordOf("1001")
	if _, err := TreasureRollDice("1001", reward); !errors.Is(err, ErrTreasureNoSeason) {
		t.Fatalf("roll in the reward period: %v, want no season", err)
	}
	if after, _ := store.TreasureRecordOf("1001"); after.Tickets != before.Tickets || after.Position != before.Position {
		t.Fatalf("a refused roll changed the record: %+v -> %+v", before, after)
	}
	if offer := TreasureRankInfo("1001", reward); !offer.Ended || !offer.Open || !offer.Claimable || offer.Rank != 1 {
		t.Fatalf("rank offer in the reward period = %+v", offer)
	}
	if gems, err := TreasureRankClaim("1001", reward); err != nil || gems != 300 {
		t.Fatalf("rank claim in the reward period = %d, %v; want 300", gems, err)
	}
	if _, err := TreasureRankClaim("1001", reward); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("second rank claim: %v", err)
	}
	if _, _, err := TreasurePointClaim("1001", 1, reward); err != nil {
		t.Fatalf("point claim in the reward period: %v", err)
	}
	if _, mail := treasureAccount(t, "1001"); len(mail) != 2 {
		t.Fatalf("mail after the two claims = %+v", mail)
	}
}

// TestTreasureRewardPeriodRecords: a season's records stay with it through its reward period, so the point claims and the
// ranking read that season. The next season's first action starts the record over.
func TestTreasureRewardPeriodRecords(t *testing.T) {
	butlerAccount(t, "1001", 0, 0)
	treasureState(t, "1001", store.TreasureRecord{Season: 1950000001, Tickets: 10, Charged: treasureNow.UnixMilli(),
		Points: 100, PointClaimed: []int64{1}})
	reward := treasureAt(2026, 12, 1, 12, 0)
	if rows := TreasurePointRows("1001", reward); !rows[0].Claimed || rows[1].Claimed {
		t.Fatalf("point rows in the reward period = %+v", rows)
	}
	if rows, _ := TreasureRankingPage(1, 20, reward); len(rows) != 1 || rows[0].Aid != "1001" {
		t.Fatalf("ranking in the reward period = %+v", rows)
	}
	if _, _, err := TreasurePointClaim("1001", 2, reward); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("claim of 300 with 100 points in the reward period: %v", err)
	}
	// Season 2's reward period: season 1's record is not season 2's, so it claims nothing and ranks nothing.
	later := treasureAt(2027, 1, 3, 12, 0)
	if rows := TreasurePointRows("1001", later); rows[0].Claimed {
		t.Fatalf("an earlier season's claim shows in a later reward period: %+v", rows)
	}
	if rows, _ := TreasureRankingPage(1, 20, later); len(rows) != 0 {
		t.Fatalf("an earlier season ranks in a later reward period: %+v", rows)
	}
	// Season 1's first action starts its record over: no points, no claims.
	next := treasureAt(2026, 12, 6, 12, 0)
	if _, err := TreasureOpen("1001", next); err != nil {
		t.Fatalf("open the next season: %v", err)
	}
	rec, _ := store.TreasureRecordOf("1001")
	if rec.Season != 1950000002 || rec.Points != 0 || len(rec.PointClaimed) != 0 {
		t.Fatalf("record of the next season = %+v", rec)
	}
	if rows := TreasurePointRows("1001", next); rows[0].Claimed {
		t.Fatalf("next season shows a claim: %+v", rows)
	}
}

// TestTreasurePeekChangesNothing: the status 2 replies read the board without a write, closed or in the reward period.
func TestTreasurePeekChangesNothing(t *testing.T) {
	treasureState(t, "1001", store.TreasureRecord{Season: 1950000001, Tickets: 4, Charged: treasureNow.UnixMilli(), Jewels: 7})
	before, _ := store.TreasureRecordOf("1001")
	for _, at := range []time.Time{treasureAt(2026, 10, 31, 12, 0), treasureAt(2026, 12, 1, 12, 0)} {
		view := TreasurePeek("1001", at)
		if view.Jewels != 7 || view.Max != 10 {
			t.Fatalf("peek at %s = %+v", at, view)
		}
	}
	after, _ := store.TreasureRecordOf("1001")
	if after.Tickets != before.Tickets || after.Charged != before.Charged || after.Season != before.Season {
		t.Fatalf("peek wrote the record: %+v -> %+v", before, after)
	}
	if _, err := TreasureOpen("1001", treasureNow); err != nil {
		t.Fatalf("open after the peeks: %v", err)
	}
}
