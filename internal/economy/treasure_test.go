package economy

import (
	"errors"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// treasureNow is 2026-11-10 12:00 JST, inside the default season (2026-11-01 06:00 JST for 28 days).
var treasureNow = time.Date(2026, 11, 10, 12, 0, 0, 0, treasureJST)

// treasureDie makes the die show d (1 to 6) for one test; the coin amounts take their minimum.
func treasureDie(t *testing.T, d int) {
	t.Helper()
	prev := TreasureRandIntn
	TreasureRandIntn = func(n int) int {
		if n == 6 {
			return d - 1
		}
		return 0
	}
	t.Cleanup(func() { TreasureRandIntn = prev })
}

// treasureState sets aid's saved board state for one test and clears it afterwards.
func treasureState(t *testing.T, aid string, rec store.TreasureRecord) {
	t.Helper()
	store.ResetTreasure()
	t.Cleanup(store.ResetTreasure)
	if _, err := store.UpdateTreasure(aid, func(r *store.TreasureRecord) { *r = rec }); err != nil {
		t.Fatal(err)
	}
}

func treasureSeasonID() int64 { return ActiveTreasureData().Season.ID }

// TestTreasureDataDefaults: the embedded ring has four equal sides, the start line first, and the sides face FR, BR, BL,
// FL in walking order.
func TestTreasureDataDefaults(t *testing.T) {
	d := ActiveTreasureData()
	if len(d.Board.Ring) != 28 || d.Season.ID != 1950000001 || len(d.NPCs) != 2 {
		t.Fatalf("defaults = %d squares, season %d, %d npcs", len(d.Board.Ring), d.Season.ID, len(d.NPCs))
	}
	cells := d.Cells()
	if cells[0].Type != TreasureCellStart || cells[0].Direction != "FR" {
		t.Fatalf("cell 0 = %+v, want the start line facing FR", cells[0])
	}
	want := map[int]string{6: "FR", 7: "BR", 13: "BR", 14: "BL", 20: "BL", 21: "FL", 27: "FL"}
	for pos, dir := range want {
		if cells[pos].Direction != dir {
			t.Fatalf("cell %d faces %s, want %s", pos, cells[pos].Direction, dir)
		}
	}
	start, end := TreasureSeasonWindow()
	if start.Format(time.RFC3339) != "2026-11-01T06:00:00+09:00" || end.Sub(start) != 28*24*time.Hour {
		t.Fatalf("season window %s to %s", start, end)
	}
}

// TestTreasureDataRefusals: each structural error stops the file at startup (the strict loader's checks).
func TestTreasureDataRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(*TreasureData)
	}{
		{"origin cherry", func(d *TreasureData) { d.Origin = opdata.OriginCherry }},
		{"season id outside the block", func(d *TreasureData) { d.Season.ID = 5 }},
		{"season start not a date", func(d *TreasureData) { d.Season.Start = "2026-13-40" }},
		{"season length zero", func(d *TreasureData) { d.Season.Days = 0 }},
		{"ring not in four sides", func(d *TreasureData) { d.Board.Ring = d.Board.Ring[:10] }},
		{"ring not starting at the start line", func(d *TreasureData) { d.Board.Ring[0] = 1 }},
		{"cell type not built yet", func(d *TreasureData) { d.Board.Ring[1] = 2 }},
		{"money unit for the bonus", func(d *TreasureData) { d.Rewards.Bonus.CoinUnit = "gem" }},
		{"empty bonus unit", func(d *TreasureData) { d.Rewards.Bonus.CoinUnit = "" }},
		{"coin range inverted", func(d *TreasureData) { d.Rewards.Coin = TreasureRange{Min: 3, Max: 1} }},
		{"start tickets above 100", func(d *TreasureData) { d.Rewards.Start.Tickets = 101 }},
		{"zero tickets per roll", func(d *TreasureData) { d.Tickets.PerRoll = 0 }},
		{"start tickets above the cap", func(d *TreasureData) { d.Tickets.Start = 11 }},
		{"one portrait", func(d *TreasureData) { d.NPCs = d.NPCs[:1] }},
		{"unknown portrait gender", func(d *TreasureData) { d.NPCs[0].Gender = "ANIMAL" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := opdata.Decode[TreasureData](data.TreasureJSON, nil)
			if err != nil {
				t.Fatal(err)
			}
			c.edit(d)
			if err := checkTreasureData(d); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// TestTreasureSeasonWindow: the season is open from its first instant (06:00 JST) to its last, and not outside.
func TestTreasureSeasonWindow(t *testing.T) {
	start, _ := TreasureSeasonWindow()
	treasureState(t, "1001", store.TreasureRecord{})
	for _, at := range []time.Time{start.Add(-time.Second), start.AddDate(0, 0, 28)} {
		if _, err := TreasureOpen("1001", at); !errors.Is(err, ErrTreasureNoSeason) {
			t.Fatalf("open at %s: %v, want no season", at, err)
		}
	}
	if _, err := TreasureOpen("1001", start); err != nil {
		t.Fatalf("open at the first instant: %v", err)
	}
}

// TestTreasureRefillAndCap: one ticket every 600 s from the charge timer, the timer idle at the cap, and a spent
// ticket below the cap starts it again.
func TestTreasureRefillAndCap(t *testing.T) {
	t0 := treasureNow
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 5, Charged: t0.UnixMilli()})
	v, err := TreasureOpen("1001", t0.Add(1500*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if v.Tickets != 7 || v.RemainSeconds != 300 || v.ChargeSeconds != 600 {
		t.Fatalf("after 1500 s: %+v, want 7 tickets, 300 s to the next", v)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Charged != t0.Add(1200*time.Second).UnixMilli() {
		t.Fatalf("timer = %d, want the last refill instant", rec.Charged)
	}
	v, _ = TreasureOpen("1001", t0.Add(100000*time.Second))
	if v.Tickets != 10 || v.RemainSeconds != 0 {
		t.Fatalf("at the cap: %+v, want 10 tickets and no timer", v)
	}

	t1 := t0.Add(100000 * time.Second)
	treasureDie(t, 1)
	if _, err := TreasureRollDice("1001", t1); err != nil {
		t.Fatal(err)
	}
	if v, _ = TreasureOpen("1001", t1.Add(599*time.Second)); v.Tickets != 9 || v.RemainSeconds != 1 {
		t.Fatalf("599 s after a spent ticket: %+v, want 9 tickets and 1 s", v)
	}
	if v, _ = TreasureOpen("1001", t1.Add(600*time.Second)); v.Tickets != 10 {
		t.Fatalf("600 s after a spent ticket: %d tickets, want 10", v.Tickets)
	}
}

// TestTreasureRollMoves: the die moves the token round the ring, debits one ticket and pays the square landed on.
func TestTreasureRollMoves(t *testing.T) {
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 3)
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil {
		t.Fatal(err)
	}
	if mv.Dice != 3 || mv.From != 0 || mv.To != 3 || mv.Lap || mv.Cell != TreasureCellBonus || mv.Bonus != 5 {
		t.Fatalf("roll 3 = %+v, want 0 to 3 on a bonus square paying 5", mv)
	}
	if mv.View.Tickets != 9 || mv.View.Jewels != 5 || mv.View.Position != 3 {
		t.Fatalf("state after roll 3 = %+v, want 9 tickets, 5 jewels, square 3", mv.View)
	}
	treasureDie(t, 2)
	mv, _ = TreasureRollDice("1001", treasureNow)
	if mv.To != 5 || mv.Cell != TreasureCellCoin || mv.Coins != 1 || mv.View.Jewels != 6 || mv.View.Tickets != 8 {
		t.Fatalf("roll 2 = %+v, want square 5, one coin, 6 jewels, 8 tickets", mv)
	}
}

// TestTreasureLapPaysTickets: passing the start line pays the lap reward (not capped), and landing on it pays too.
func TestTreasureLapPaysTickets(t *testing.T) {
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 26, Tickets: 3, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 4)
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil {
		t.Fatal(err)
	}
	if !mv.Lap || mv.To != 2 || mv.View.Tickets != 7 || mv.View.Jewels != 1 {
		t.Fatalf("pass the start: %+v, want lap, square 2, 7 tickets, 1 jewel", mv)
	}

	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 24, Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 4)
	mv, _ = TreasureRollDice("1001", treasureNow)
	if !mv.Lap || mv.To != 0 || mv.Cell != TreasureCellStart || mv.View.Tickets != 14 || mv.View.Jewels != 0 {
		t.Fatalf("land on the start: %+v, want lap, square 0, 14 tickets (uncapped), no jewels", mv)
	}
}

// TestTreasureNoTicketsRefused: without a ticket nothing moves and the state comes back with ErrTreasureNoTickets.
func TestTreasureNoTicketsRefused(t *testing.T) {
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 0, Position: 4, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 6)
	mv, err := TreasureRollDice("1001", treasureNow)
	if !errors.Is(err, ErrTreasureNoTickets) || mv.View.Tickets != 0 || mv.View.Position != 4 || mv.View.Jewels != 0 {
		t.Fatalf("roll with no tickets: %+v, %v", mv, err)
	}
}

// TestTreasureNewSeasonStartsOver: a record of another season restarts (first square, start tickets, no jewels), and
// the tutorial flag stays with the account.
func TestTreasureNewSeasonStartsOver(t *testing.T) {
	treasureState(t, "1001", store.TreasureRecord{Season: 1, Position: 9, Tickets: 2, Jewels: 40, Tutorial: true})
	v, err := TreasureOpen("1001", treasureNow)
	if err != nil {
		t.Fatal(err)
	}
	if v.Position != 0 || v.Jewels != 0 || v.Tickets != 10 || !v.Tutorial {
		t.Fatalf("new season: %+v, want square 0, no jewels, 10 tickets, tutorial kept", v)
	}
}

// TestTreasureClockBackwards: a timer that is ahead of the clock restarts at now instead of granting a refill.
func TestTreasureClockBackwards(t *testing.T) {
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 4, Charged: treasureNow.Add(time.Hour).UnixMilli()})
	v, _ := TreasureOpen("1001", treasureNow)
	if v.Tickets != 4 || v.RemainSeconds != 600 {
		t.Fatalf("clock moved back: %+v, want 4 tickets and a fresh 600 s", v)
	}
}

// TestTreasureTutorialAndJewels: the tutorial flag persists and the jewel count reads back.
func TestTreasureTutorialAndJewels(t *testing.T) {
	treasureState(t, "1002", store.TreasureRecord{})
	if err := TreasureTutorialDone("1002"); err != nil {
		t.Fatal(err)
	}
	v, _ := TreasureOpen("1002", treasureNow)
	if !v.Tutorial || TreasureJewels("1002") != 0 {
		t.Fatalf("after the tutorial: %+v, jewels %d", v, TreasureJewels("1002"))
	}
}
