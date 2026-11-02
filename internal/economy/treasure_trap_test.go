package economy

import (
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// treasureTrapRing is a board with the island and the broom in reach: traps 4 and 6, the broom 5, coins 1, 7 and 9, the exchange
// squares 2, 13 and 21 (the broom's targets). The default tiles are replaced by square 2 (treasureRing).
var treasureTrapRing = []int{0, 1, 2, 3, 6, 8, 6, 1, 4, 1, 7, 1, 1, 2, 1, 1, 4, 1, 7, 1, 1, 2, 1, 1, 4, 1, 7, 1}

// treasureDice makes the rolls show values in turn (1 to 6); the coin amounts take their minimum.
func treasureDice(t *testing.T, values ...int) {
	t.Helper()
	prev := TreasureRandIntn
	t.Cleanup(func() { TreasureRandIntn = prev })
	next := 0
	TreasureRandIntn = func(n int) int {
		if n != 6 {
			return 0
		}
		v := values[min(next, len(values)-1)]
		next++
		return v - 1
	}
}

// treasureEscapeSlot is a posted mission set whose one slot is the escape mission (the pool's kind escape).
func treasureEscapeSlot(t *testing.T) []store.TreasureSlot {
	t.Helper()
	for i, task := range ActiveTreasureData().Missions.Pool {
		if task.Kind == "escape" {
			return []store.TreasureSlot{{Task: int64(i)}}
		}
	}
	t.Fatal("the shipped mission pool has no escape mission")
	return nil
}

// TestTreasureTrapLanding: a roll onto a trap square holds the token there, pays nothing and starts the count at zero.
func TestTreasureTrapLanding(t *testing.T) {
	treasureRing(t, treasureTrapRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 4)
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil || mv.To != 4 || mv.Cell != TreasureCellTrap || mv.Escaped || mv.Coins != 0 || mv.Bonus != 0 ||
		mv.Trap != (TreasureTrap{Count: 0, Max: 3, High: 5}) {
		t.Fatalf("roll onto trap 4 = %+v, %v; want a held token on 4 with the island counts 0 of 3, high die 5", mv, err)
	}
	rec, _ := store.TreasureRecordOf("1001")
	if !rec.Trapped || rec.TrapCount != 0 || rec.Position != 4 || rec.TravelPending || rec.KeyPending || rec.Jewels != 0 {
		t.Fatalf("saved board after the trap landing = %+v", rec)
	}
}

// TestTreasureTrapStaysThenEscapesByCount: a trapped roll below the die and short of the count keeps the token and counts; the
// roll that reaches max_tries frees it, walks by its die and pays the square it reaches.
func TestTreasureTrapStaysThenEscapesByCount(t *testing.T) {
	treasureRing(t, treasureTrapRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 4, Trapped: true, Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDice(t, 2, 1, 3)
	for want := int64(1); want <= 2; want++ {
		mv, err := TreasureRollDice("1001", treasureNow)
		if err != nil || mv.To != 4 || mv.Cell != TreasureCellTrap || mv.Escaped || mv.Trap.Count != want || mv.Lap {
			t.Fatalf("trapped roll %d = %+v, %v; want the token held on 4 with count %d", want, mv, err, want)
		}
		if rec, _ := store.TreasureRecordOf("1001"); !rec.Trapped || rec.TrapCount != want || rec.Position != 4 || rec.Tickets != 10-want {
			t.Fatalf("saved board after trapped roll %d = %+v", want, rec)
		}
	}
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil || !mv.Escaped || mv.From != 4 || mv.To != 7 || mv.Cell != TreasureCellCoin || mv.Coins != 1 || mv.Lap {
		t.Fatalf("third trapped roll = %+v, %v; want the escape to coin square 7 (3 steps), one Jewel", mv, err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Trapped || rec.TrapCount != 0 || rec.Position != 7 || rec.Jewels != 1 {
		t.Fatalf("saved board after the escape = %+v", rec)
	}
}

// TestTreasureTrapEscapesByHighDie: a trapped roll of the high die frees the token at once, whatever the count.
func TestTreasureTrapEscapesByHighDie(t *testing.T) {
	treasureRing(t, treasureTrapRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 4, Trapped: true, Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 5)
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil || !mv.Escaped || mv.To != 9 || mv.Cell != TreasureCellCoin {
		t.Fatalf("high die 5 on trap 4 = %+v, %v; want the escape to coin square 9", mv, err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Trapped || rec.TrapCount != 0 || rec.Position != 9 {
		t.Fatalf("saved board after the high-die escape = %+v", rec)
	}
}

// TestTreasureTrapEscapeSkipsTrapSquares: an escape never lands on a trap square; the walk goes on to the next square that is not one.
func TestTreasureTrapEscapeSkipsTrapSquares(t *testing.T) {
	treasureRing(t, treasureTrapRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 4, Trapped: true, TrapCount: 2, Tickets: 10,
		Charged: treasureNow.UnixMilli()})
	treasureDie(t, 2) // the third trapped roll: 4 + 2 is trap 6, so the token goes on to coin square 7
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil || !mv.Escaped || mv.To != 7 || mv.Cell != TreasureCellCoin {
		t.Fatalf("escape by 2 over trap 6 = %+v, %v; want coin square 7", mv, err)
	}
}

// TestTreasureTrapEscapeCountsOnce: only the escape counts ("Escape traps"): a trapped roll that stays counts nothing, the escape
// counts one on the posted escape mission, and the mission's goal is what the pool says.
func TestTreasureTrapEscapeCountsOnce(t *testing.T) {
	treasureRing(t, treasureTrapRing)
	slots := treasureEscapeSlot(t)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 4, Trapped: true, Tickets: 10,
		Charged: treasureNow.UnixMilli(), Missions: store.TreasureList{Set: 1, Slots: slots}})
	treasureDie(t, 1)
	if _, err := TreasureRollDice("1001", treasureNow); err != nil {
		t.Fatal(err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Missions.Slots[0].Count != 0 {
		t.Fatalf("a trapped roll that stays counted an escape: %+v", rec.Missions.Slots)
	}
	treasureDie(t, 5)
	if _, err := TreasureRollDice("1001", treasureNow); err != nil {
		t.Fatal(err)
	}
	rec, _ := store.TreasureRecordOf("1001")
	if rec.Missions.Slots[0].Count != 1 || rec.Missions.Slots[0].State != 0 {
		t.Fatalf("escape mission after one escape = %+v, want count 1, in progress", rec.Missions.Slots)
	}
}

// TestTreasureRollWhileTrappedOffTheSquare: a roll with a trapped flag but no trap under the token releases the flag first and
// walks the token as an ordinary roll does.
func TestTreasureRollWhileTrappedOffTheSquare(t *testing.T) {
	treasureRing(t, treasureTrapRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 1, Trapped: true, TrapCount: 1, Tickets: 10,
		Charged: treasureNow.UnixMilli()})
	treasureDie(t, 1)
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil || mv.Escaped || mv.To != 2 || mv.Cell != TreasureCellExchange {
		t.Fatalf("roll from coin square 1 with a stale trap = %+v, %v; want an ordinary walk to 2", mv, err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Trapped || rec.TrapCount != 0 {
		t.Fatalf("stale trap kept: %+v", rec)
	}
}

// TestTreasureTrapRecovery: a board entry releases a trap that is not on its square or whose count reached max_tries, and moves a
// token left on a trap square with no trap to the next coin square; a trap that is still in its count is left alone.
func TestTreasureTrapRecovery(t *testing.T) {
	cases := []struct {
		name     string
		rec      store.TreasureRecord
		pos      int
		trapped  bool
		why      string
		wantMove bool
	}{
		{"trapped on a coin square", store.TreasureRecord{Position: 1, Trapped: true, TrapCount: 1}, 1, false, "trap released", false},
		{"count reached max tries", store.TreasureRecord{Position: 4, Trapped: true, TrapCount: 3}, 7, false, "moved from 4 to 7", true},
		{"on a trap square, not trapped", store.TreasureRecord{Position: 4}, 7, false, "trap square without a trap", true},
		{"trapped, count in range", store.TreasureRecord{Position: 4, Trapped: true, TrapCount: 1}, 4, true, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			treasureRing(t, treasureTrapRing)
			rec := c.rec
			rec.Season, rec.Tickets, rec.Charged = treasureSeasonID(), 10, treasureNow.UnixMilli()
			treasureState(t, "1001", rec)
			view, err := TreasureOpen("1001", treasureNow)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := store.TreasureRecordOf("1001")
			if got.Position != c.pos || got.Trapped != c.trapped {
				t.Fatalf("entry left the board at %d trapped=%v, want %d trapped=%v (%+v)", got.Position, got.Trapped, c.pos, c.trapped, got)
			}
			if strings.Contains(view.Recovery, "moved") != c.wantMove || (c.why != "" && !strings.Contains(view.Recovery, c.why)) ||
				(c.why == "" && view.Recovery != "") {
				t.Fatalf("recovery = %q, want %q (move %v)", view.Recovery, c.why, c.wantMove)
			}
		})
	}
}

// TestTreasureTravelLandingStampsThePrompt: a landing on the broom square records when its prompt was set (the timeout counts from it).
func TestTreasureTravelLandingStampsThePrompt(t *testing.T) {
	treasureRing(t, treasureTrapRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 5)
	if _, err := TreasureRollDice("1001", treasureNow); err != nil {
		t.Fatal(err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); !rec.TravelPending || rec.TravelSince != treasureNow.UnixMilli() {
		t.Fatalf("broom landing = %+v, want the prompt pending since now", rec)
	}
}

// TestTreasureTravelRecovery: a board entry clears a broom prompt that is off a broom square or unanswered past travel.stale_minutes,
// and moves a token left on a broom square with no prompt to the next coin square. A fresh prompt is left alone.
func TestTreasureTravelRecovery(t *testing.T) {
	stale := treasureNow.Add(-11 * time.Minute).UnixMilli()
	cases := []struct {
		name    string
		pos     int
		pending bool
		since   int64
		pos2    int
		why     string
	}{
		{"fresh prompt", 5, true, treasureNow.UnixMilli(), 5, ""},
		{"prompt past the timeout", 5, true, stale, 7, "unanswered past travel.stale_minutes"},
		{"prompt with no stamp", 5, true, 0, 7, "unanswered past travel.stale_minutes"},
		{"prompt off a broom square", 1, true, treasureNow.UnixMilli(), 1, "broom prompt off a broom square"},
		{"broom square, no prompt", 5, false, 0, 7, "broom square without a prompt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			treasureRing(t, treasureTrapRing)
			treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: c.pos, TravelPending: c.pending,
				TravelSince: c.since, Tickets: 10, Charged: treasureNow.UnixMilli()})
			view, err := TreasureOpen("1001", treasureNow)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := store.TreasureRecordOf("1001")
			if got.Position != c.pos2 || (c.why != "" && !strings.Contains(view.Recovery, c.why)) || (c.why == "" && view.Recovery != "") {
				t.Fatalf("entry left %+v with recovery %q; want square %d and %q", got, view.Recovery, c.pos2, c.why)
			}
			if c.why == "" && (!got.TravelPending || got.TravelSince != c.since) {
				t.Fatalf("a fresh prompt was changed: %+v", got)
			}
			if c.why != "" && got.TravelPending {
				t.Fatalf("the prompt survived the recovery: %+v", got)
			}
			if view.TravelPending != got.TravelPending || view.Trap.Max != 3 || view.Trap.High != 5 {
				t.Fatalf("view = %+v, want the prompt flag and the island rules", view)
			}
		})
	}
}

// TestTreasureDefaultRingCorners: the shipped board has no island (6) and no broom (8) square; they are for an
// operator's board, after a device probe.
func TestTreasureDefaultRingCorners(t *testing.T) {
	d, err := opdata.Decode[TreasureData](data.TreasureJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Board.Ring[7] != TreasureCellTrap {
		t.Fatalf("default ring square 7 = %d, want the trap corner (bottom-right, type 6, device-verified 2026-11-03)", d.Board.Ring[7])
	}
	if d.Board.Ring[21] != TreasureCellTravel {
		t.Fatalf("default ring square 21 = %d, want the broom corner (top-left, type 8, device-verified 2026-11-02)", d.Board.Ring[21])
	}
	if d.Trap.HighDice != 5 || d.Trap.MaxTries != 3 || d.Travel.StaleMinutes != 10 {
		t.Fatalf("shipped trap and travel rules = %+v %+v, want 5 / 3 and 10 minutes", d.Trap, d.Travel)
	}
}

// TestTreasureLabProbeRingValidates: the device probe board (broom 8 on square 23 and trap 6 on square 25, the default board
// otherwise, the exchange squares 2, 8, 15 and 22 kept on type 2) passes the startup check that an operator file gets.
func TestTreasureLabProbeRingValidates(t *testing.T) {
	d, err := opdata.Decode[TreasureData](data.TreasureJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Board.Ring[23] = TreasureCellTravel
	d.Board.Ring[25] = TreasureCellTrap
	if err := checkTreasureData(d); err != nil {
		t.Fatalf("lab probe ring refused: %v", err)
	}
	for _, pos := range []int{2, 8, 15, 22} {
		if d.Board.Ring[pos] != TreasureCellExchange {
			t.Fatalf("exchange square %d is type %d in the probe ring", pos, d.Board.Ring[pos])
		}
	}
}
