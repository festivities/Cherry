package economy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/testutil"
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
	for _, pos := range []int{5, 12, 19, 26} {
		if cells[pos].Type != TreasureCellGoldKey {
			t.Fatalf("cell %d is type %d, want a gold key (7)", pos, cells[pos].Type)
		}
	}
	cyc := TreasureCurrent(treasureNow)
	start, end := cyc.Start, cyc.End
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
		{"cell type 10 not built yet", func(d *TreasureData) { d.Board.Ring[1] = 10 }},
		{"ring without a coin square", func(d *TreasureData) {
			for i, c := range d.Board.Ring {
				if c == TreasureCellCoin {
					d.Board.Ring[i] = TreasureCellBonus
				}
			}
		}},
		{"broom square without an exchange square", func(d *TreasureData) {
			for i, c := range d.Board.Ring {
				if c == TreasureCellExchange {
					d.Board.Ring[i] = TreasureCellCoin
				}
			}
			d.Board.Ring[3] = TreasureCellTravel
		}},
		{"buff without items", func(d *TreasureData) { d.Buff.Items = nil }},
		{"buff percent above 40", func(d *TreasureData) { d.Buff.Items[0].Percent = 41 }},
		{"buff percent zero", func(d *TreasureData) { d.Buff.Items[1].Percent = 0 }},
		{"buff item not an item code", func(d *TreasureData) { d.Buff.Items[0].Code = "NOPE" }},
		{"buff cap zero", func(d *TreasureData) { d.Buff.Cap = 0 }},
		{"buff cap above 100", func(d *TreasureData) { d.Buff.Cap = 101 }},
		{"special reward above 50", func(d *TreasureData) { d.Special.RewardTickets = 51 }},
		{"special goal pulls zero", func(d *TreasureData) { d.Special.GoalPulls = 0 }},
		{"minigame max bet zero", func(d *TreasureData) { d.MiniGame.MaxBet = 0 }},
		{"minigame without multiples", func(d *TreasureData) { d.MiniGame.Multiples = nil }},
		{"minigame with two cups", func(d *TreasureData) { d.MiniGame.Multiples = []int64{1, 2} }},
		{"minigame multiple above 10", func(d *TreasureData) { d.MiniGame.Multiples = []int64{1, 2, 11} }},
		{"money unit for the bonus", func(d *TreasureData) { d.Rewards.Bonus.CoinUnit = "gem" }},
		{"empty bonus unit", func(d *TreasureData) { d.Rewards.Bonus.CoinUnit = "" }},
		{"coin range inverted", func(d *TreasureData) { d.Rewards.Coin = TreasureRange{Min: 3, Max: 1} }},
		{"start tickets above 100", func(d *TreasureData) { d.Rewards.Start.Tickets = 101 }},
		{"zero tickets per roll", func(d *TreasureData) { d.Tickets.PerRoll = 0 }},
		{"start tickets above the cap", func(d *TreasureData) { d.Tickets.Start = 11 }},
		{"one portrait", func(d *TreasureData) { d.NPCs = d.NPCs[:1] }},
		{"unknown portrait gender", func(d *TreasureData) { d.NPCs[0].Gender = "ANIMAL" }},
		{"gold key deck empty", func(d *TreasureData) { d.GoldKey.Cards = nil }},
		{"gold key warp card (type 3)", func(d *TreasureData) { d.GoldKey.Cards[0].Type = 3 }},
		{"gold key card type 8", func(d *TreasureData) { d.GoldKey.Cards[0].Type = 8 }},
		{"gold key zero weight", func(d *TreasureData) { d.GoldKey.Cards[0].Weight = 0 }},
		{"gold key min zero", func(d *TreasureData) { d.GoldKey.Cards[0].Min = 0 }},
		{"gold key max below min", func(d *TreasureData) { d.GoldKey.Cards[1].Max = 0 }},
		{"gold key steps above 12", func(d *TreasureData) { d.GoldKey.Cards[0].Max = 13 }},
		{"gold key jewels above 100", func(d *TreasureData) { d.GoldKey.Cards[2].Max = 101 }},
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

// treasureProbeRing is a board with the slice 3 probe squares in reach of one another: 3 buff, 5 special mission, 8 travel,
// 9 mini-game, and the exchange squares 2, 13 and 21 (the travel candidates from 8).
var treasureProbeRing = []int{0, 1, 2, 3, 5, 8, 9, 1, 4, 1, 7, 1, 1, 2, 1, 1, 4, 1, 7, 1, 1, 2, 1, 1, 4, 1, 7, 1}

// treasureRing loads the default treasure file with ring as its board and only square 2 as an exchange tile (the default tiles
// name 8, 15 and 22, which a probe ring does not keep as exchange squares), as a temp operator file, for one test.
func treasureRing(t *testing.T, ring []int) {
	t.Helper()
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(data.TreasureJSON, &doc); err != nil {
		t.Fatal(err)
	}
	doc["board"], _ = json.Marshal(map[string][]int{"ring": ring})
	var ex map[string]json.RawMessage
	if err := json.Unmarshal(doc["exchange"], &ex); err != nil {
		t.Fatal(err)
	}
	ex["tiles"], _ = json.Marshal([]map[string]int{{"square": 2, "times": 1}})
	doc["exchange"], _ = json.Marshal(ex)
	raw, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "treasure.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := treasureFile.LoadFrom(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(treasureFile.Reset)
}

// TestTreasureProbeCellsValidation: the slice 3 squares 3, 5, 6, 8 and 9 pass the ring check, so an operator file can place them.
func TestTreasureProbeCellsValidation(t *testing.T) {
	for _, c := range []int{3, 5, 6, 8, 9} {
		d, err := opdata.Decode[TreasureData](data.TreasureJSON, nil)
		if err != nil {
			t.Fatal(err)
		}
		d.Board.Ring[1] = c
		if err := checkTreasureData(d); err != nil {
			t.Fatalf("ring square 1 of type %d refused: %v", c, err)
		}
	}
}

// TestTreasureBuffItemNeedsArt: the buff item is checked against the item archive when the file loads (verifyTreasure).
func TestTreasureBuffItemNeedsArt(t *testing.T) {
	d, err := opdata.Decode[TreasureData](data.TreasureJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyTreasure(d); err != nil {
		t.Fatalf("default buff items refused with the default archive: %v", err)
	}
	d.Buff.Items = []TreasureBuffItem{{Code: "NOSUCHITEM", Percent: 10}} // not an item at all
	if err := verifyTreasure(d); err == nil {
		t.Fatal("buff item NOSUCHITEM accepted")
	}
	// The Mar dress has its art only in the lpbackup tree: with the santi root alone it is refused.
	testutil.SetArchive(t, testutil.ArchiveMounts(testutil.SantiItems()))
	d, _ = opdata.Decode[TreasureData](data.TreasureJSON, nil)
	if err := verifyTreasure(d); err == nil || !strings.Contains(err.Error(), "CUAH008A9") {
		t.Fatalf("buff pool without the lpbackup root: %v, want CUAH008A9 refused", err)
	}
}

// TestTreasureTravelLanding: a roll onto a travel square pays nothing and leaves the travel pending; the next roll clears it.
func TestTreasureTravelLanding(t *testing.T) {
	treasureRing(t, treasureProbeRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 5)
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil {
		t.Fatal(err)
	}
	if mv.To != 5 || mv.Cell != TreasureCellTravel || mv.Coins != 0 || mv.Bonus != 0 || mv.View.Jewels != 0 {
		t.Fatalf("roll onto square 5 = %+v, want a travel square with nothing paid", mv)
	}
	if rec, _ := store.TreasureRecordOf("1001"); !rec.TravelPending || rec.KeyPending {
		t.Fatalf("travel not pending after the landing: %+v", rec)
	}
	treasureDie(t, 1)
	if _, err := TreasureRollDice("1001", treasureNow); err != nil {
		t.Fatal(err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.TravelPending || rec.Position != 6 {
		t.Fatalf("a later roll left the travel pending or moved wrongly: %+v", rec)
	}
}

// TestTreasureTravelMoves: a pending travel moves the token to an exchange square with no lap and no payment, and spends the
// pending flag. The view lists the exchange squares that are full today.
func TestTreasureTravelMoves(t *testing.T) {
	treasureRing(t, treasureProbeRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 5, Tickets: 9, Charged: treasureNow.UnixMilli(),
		Jewels: 4, TravelPending: true, ExDay: treasureDayNumber(treasureNow), Exchanged: map[string]int64{"13": 60}})
	mv, err := TreasureTravel("1001", 13, treasureNow)
	if err != nil || mv.From != 5 || mv.To != 13 || mv.Lap || mv.Cell != TreasureCellExchange || mv.View.Position != 13 ||
		mv.View.Tickets != 9 || mv.View.Jewels != 4 {
		t.Fatalf("travel to 13 = %+v, %v; want 5 to 13, no lap, 9 tickets, 4 jewels", mv, err)
	}
	if len(mv.View.ExchangeFull) != 1 || mv.View.ExchangeFull[0] != 13 {
		t.Fatalf("full exchange squares = %v, want [13]", mv.View.ExchangeFull)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.TravelPending || rec.Position != 13 {
		t.Fatalf("saved board after the travel = %+v", rec)
	}
	if _, err := TreasureTravel("1001", 2, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("second travel on one square: %v", err)
	}
}

// TestTreasureTravelRefusals: a travel is refused unless a travel is pending on a travel square and the target is another
// exchange square. A refusal moves nothing and spends nothing, and it clears the pending prompt (the reply closes the client's
// alert with no move; the next board entry releases the token if it still stands on the broom square).
func TestTreasureTravelRefusals(t *testing.T) {
	treasureRing(t, treasureProbeRing)
	cases := []struct {
		name    string
		pos     int
		pending bool
		to      int
	}{
		{"no travel pending", 5, false, 2},
		{"pending on a mini-game square", 6, true, 2},
		{"target a coin square", 5, true, 1},
		{"target the buff square", 5, true, 3},
		{"target the square it stands on", 5, true, 5},
		{"target below the ring", 5, true, -1},
		{"target past the ring", 5, true, 28},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: c.pos, Tickets: 9, Charged: treasureNow.UnixMilli(),
				TravelPending: c.pending})
			if _, err := TreasureTravel("1001", c.to, treasureNow); !errors.Is(err, ErrTreasureRefused) {
				t.Fatalf("travel to %d: %v, want refused", c.to, err)
			}
			if rec, _ := store.TreasureRecordOf("1001"); rec.Position != c.pos || rec.TravelPending || rec.TravelSince != 0 || rec.Tickets != 9 {
				t.Fatalf("refused travel moved the token or kept the prompt: %+v", rec)
			}
		})
	}
}

// TestTreasureGoldKeyCardLandsOnTravel: a Mystery Card that ends on a travel square leaves the travel pending, as a roll does.
func TestTreasureGoldKeyCardLandsOnTravel(t *testing.T) {
	treasureRing(t, treasureProbeRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 2, Tickets: 10, Charged: treasureNow.UnixMilli(), KeyPending: true})
	treasureRandSeq(t, 0, 2, 0) // type 1, 3 steps: 3 (buff), 4 (special mission), 5 (travel)
	use, err := TreasureGoldKeyUse("1001", treasureNow)
	if err != nil || use.Move.To != 5 || use.Move.Cell != TreasureCellTravel {
		t.Fatalf("card = %+v, %v; want a walk to the travel square 5", use, err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); !rec.TravelPending || rec.KeyPending || rec.Position != 5 {
		t.Fatalf("saved board after the card = %+v", rec)
	}
}

// TestTreasureSeasonWindow: the season is open from its first instant (06:00 JST) to its last, and not outside.
func TestTreasureSeasonWindow(t *testing.T) {
	start := TreasureCurrent(treasureNow).Start
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
	treasureDie(t, 3)
	mv, _ = TreasureRollDice("1001", treasureNow)
	if mv.To != 6 || mv.Cell != TreasureCellCoin || mv.Coins != 1 || mv.View.Jewels != 6 || mv.View.Tickets != 8 {
		t.Fatalf("roll 3 = %+v, want square 6, one coin, 6 jewels, 8 tickets", mv)
	}
}

// TestTreasureGoldKeyLanding: a roll onto a gold key square pays nothing and leaves the key pending; the next roll clears it.
func TestTreasureGoldKeyLanding(t *testing.T) {
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 5)
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil {
		t.Fatal(err)
	}
	if mv.To != 5 || mv.Cell != TreasureCellGoldKey || mv.Coins != 0 || mv.Bonus != 0 || mv.View.Jewels != 0 || mv.View.Tickets != 9 {
		t.Fatalf("roll onto square 5 = %+v, want a gold key with nothing paid", mv)
	}
	if rec, _ := store.TreasureRecordOf("1001"); !rec.KeyPending {
		t.Fatal("the gold key is not pending after the landing")
	}
	treasureDie(t, 1)
	if _, err := TreasureRollDice("1001", treasureNow); err != nil {
		t.Fatal(err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.KeyPending || rec.Position != 6 {
		t.Fatalf("a later roll left the key pending or moved wrongly: %+v", rec)
	}
}

// treasureRandSeq makes TreasureRandIntn return vals in turn (the last one again once they run out) for one test.
func treasureRandSeq(t *testing.T, vals ...int) {
	t.Helper()
	prev := TreasureRandIntn
	t.Cleanup(func() { TreasureRandIntn = prev })
	next := 0
	TreasureRandIntn = func(int) int {
		v := vals[min(next, len(vals)-1)]
		next++
		return v
	}
}

// TestTreasureGoldKeyCards: each Mystery Card moves or pays as its type says. The draws come in order: the card by weight
// (the default weights 3, 2, 3, 2 give the picks 0-2 type 1, 3-4 type 2, 5-7 type 6, 8-9 type 7), the value offset in the
// card's range, then the coin amount of a coin landing.
func TestTreasureGoldKeyCards(t *testing.T) {
	cases := []struct {
		name    string
		pos     int
		draws   []int
		card    int64
		value   int64
		to      int
		lap     bool
		cell    int
		coins   int64
		bonus   int64
		jewels  int64
		tickets int64
	}{
		// Type 1, 3 steps from square 9 onto the gold key at 12: the card passes it, lands on 13 (a coin, 1 Jewel).
		{name: "forward past a gold key", pos: 9, draws: []int{0, 2, 0}, card: 1, value: 3, to: 13, cell: TreasureCellCoin, coins: 1, jewels: 1, tickets: 10},
		// Type 1, 3 steps from square 26 over the start line to square 1 (a coin, 2 Jewels): the lap pays 5 tickets.
		{name: "forward over the start", pos: 26, draws: []int{0, 2, 1}, card: 1, value: 3, to: 1, lap: true, cell: TreasureCellCoin, coins: 2, jewels: 2, tickets: 15},
		// Type 2, 1 step back from square 13 onto the gold key at 12: the card passes it, lands on 11 (a coin, 1 Jewel). No lap.
		{name: "back past a gold key", pos: 13, draws: []int{3, 0, 0}, card: 2, value: 1, to: 11, cell: TreasureCellCoin, coins: 1, jewels: 1, tickets: 10},
		// Type 2, 3 steps back from square 6 to square 3, a bonus square: 5 Jewels.
		{name: "back onto a bonus square", pos: 6, draws: []int{3, 2}, card: 2, value: 3, to: 3, cell: TreasureCellBonus, bonus: 5, jewels: 5, tickets: 10},
		// Type 6, 3 to 6 Jewels: the token stays on the gold key and the Jewels are paid there.
		{name: "jewels on the square", pos: 5, draws: []int{5, 0}, card: 6, value: 3, to: 5, cell: TreasureCellGoldKey, coins: 3, jewels: 3, tickets: 10},
		// Type 7, 5 to 10 Jewels: the top of the range, 10 Jewels.
		{name: "the top of the jewel range", pos: 19, draws: []int{8, 5}, card: 7, value: 10, to: 19, cell: TreasureCellGoldKey, coins: 10, jewels: 10, tickets: 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: c.pos, Tickets: 10, Charged: treasureNow.UnixMilli(), KeyPending: true})
			treasureRandSeq(t, c.draws...)
			use, err := TreasureGoldKeyUse("1001", treasureNow)
			if err != nil {
				t.Fatal(err)
			}
			mv := use.Move
			if use.Card != c.card || use.Value != c.value || mv.From != c.pos || mv.To != c.to || mv.Lap != c.lap || mv.Cell != c.cell ||
				mv.Coins != c.coins || mv.Bonus != c.bonus || mv.View.Jewels != c.jewels || mv.View.Tickets != c.tickets || mv.View.Position != c.to {
				t.Fatalf("use = %+v, want card %d value %d from %d to %d lap %v cell %d coins %d bonus %d jewels %d tickets %d",
					use, c.card, c.value, c.pos, c.to, c.lap, c.cell, c.coins, c.bonus, c.jewels, c.tickets)
			}
			if rec, _ := store.TreasureRecordOf("1001"); rec.KeyPending || rec.Position != c.to || rec.Jewels != c.jewels {
				t.Fatalf("saved board = %+v after the card", rec)
			}
		})
	}
}

// TestTreasureGoldKeyNeedsPendingKey: a card without a pending key is refused and the board does not move; a key used once
// cannot be used again.
func TestTreasureGoldKeyNeedsPendingKey(t *testing.T) {
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 5, Tickets: 10, Charged: treasureNow.UnixMilli()})
	if _, err := TreasureGoldKeyUse("1001", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("card without a pending key: %v", err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Position != 5 || rec.Jewels != 0 || rec.KeyPending {
		t.Fatalf("refused card changed the board: %+v", rec)
	}
	if _, err := store.UpdateTreasure("1001", func(r *store.TreasureRecord) { r.KeyPending = true }); err != nil {
		t.Fatal(err)
	}
	treasureRandSeq(t, 5, 0)
	if _, err := TreasureGoldKeyUse("1001", treasureNow); err != nil {
		t.Fatal(err)
	}
	if _, err := TreasureGoldKeyUse("1001", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("second card on one key: %v", err)
	}
}

// TestTreasureGoldKeyCountsGoldKeyMissions: a mission of kind goldkey counts each card use (the progress kind of the deck).
func TestTreasureGoldKeyCountsGoldKeyMissions(t *testing.T) {
	d, err := opdata.Decode[TreasureData](data.TreasureJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Missions.Pool[0].Kind = "goldkey"
	if err := checkTreasureData(d); err != nil {
		t.Fatalf("a goldkey mission is refused: %v", err)
	}
	r := store.TreasureRecord{Missions: store.TreasureList{Set: 1, Slots: []store.TreasureSlot{{Task: 0}}}}
	treasureProgress(&r, d, "goldkey", 1)
	treasureProgress(&r, d, "roll", 1)
	if r.Missions.Slots[0].Count != 1 {
		t.Fatalf("goldkey mission count = %d, want 1 (rolls do not count)", r.Missions.Slots[0].Count)
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
	if !mv.Lap || mv.To != 2 || mv.Cell != TreasureCellExchange || mv.View.Tickets != 7 || mv.View.Jewels != 0 {
		t.Fatalf("pass the start: %+v, want lap, exchange square 2, 7 tickets, no jewels", mv)
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
