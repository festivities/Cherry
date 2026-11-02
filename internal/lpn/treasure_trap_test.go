package lpn

import (
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// treTrapRing is the economy's trap ring over the gateway: trap 4 and 6, the broom 5, coins 1, 7 and 9, exchange 2, 13 and 21.
var treTrapRing = []int{0, 1, 2, 3, 6, 8, 6, 1, 4, 1, 7, 1, 1, 2, 1, 1, 4, 1, 7, 1, 1, 2, 1, 1, 4, 1, 7, 1}

// treSetBoard saves aid 1001's board state for one test: the season, the tickets and the refill clock at treTime, plus rec.
func treSetBoard(t *testing.T, rec store.TreasureRecord) {
	t.Helper()
	rec.Season, rec.Tickets, rec.Charged = economy.ActiveTreasureData().Season.ID, 10, treTime.UnixMilli()
	if _, err := store.UpdateTreasure("1001", func(r *store.TreasureRecord) { *r = rec }); err != nil {
		t.Fatal(err)
	}
}

// treEnterBoard logs 1001 in, enters the board and relays the start, and returns the relay reply.
func treEnterBoard(t *testing.T, g *treGateway) []byte {
	t.Helper()
	g.login(1001)
	g.send(treasureCsRoomEnter, nil)
	g.readShape(treasureRcRoomEnter, treEnter)
	g.send(treasureCsRelayStart, nil)
	return g.readShape(treasureRcRelayStart, treRelay)
}

// treRollDie rolls one die over the gateway and returns the sc 4 reply.
func treRollDie(t *testing.T, g *treGateway, die int) []byte {
	t.Helper()
	treDice(t, die)
	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	return g.readShape(treasureRcRolling, treRoll)
}

// treIslandOf is the CellDetailIsland (f8) of a detail, checked for its four fields.
func treIslandOf(t *testing.T, detail []byte) []byte {
	t.Helper()
	island := cirBytes(detail, 8)
	if island == nil {
		t.Fatalf("detail %x has no island (f8)", detail)
	}
	return island
}

// TestTreasureTrapOverTheGateway: a roll onto trap 4 holds the token with the island detail (ExitResult 0, 3 rolls, count 0, die 5);
// a trapped roll below the die and short of the count stays with the count up; the third trapped roll frees the token onto coin 7.
func TestTreasureTrapOverTheGateway(t *testing.T) {
	treScene(t)
	treLoadRing(t, treTrapRing)
	treSetBoard(t, store.TreasureRecord{})
	g := newTreGateway(t)
	g.open()
	treEnterBoard(t, g)

	roll := treRollDie(t, g, 4) // landing on trap 4
	island := treIslandOf(t, cirBytes(roll, 7))
	if cirVar(cirBytes(roll, 6), 1) != 4 || cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellTrap ||
		cirVar(island, 1) != 0 || cirVar(island, 2) != 3 || cirVar(island, 3) != 0 || cirVar(island, 4) != 5 || cirVar(roll, 8) != 4 {
		t.Fatalf("trap landing = %x, want square 4 (type 6), island ExitResult 0, 3 rolls, count 0, die 5, dice 4", roll)
	}
	if !strings.Contains(g.logs.String(), "TREASURE msgid=4 trap landed aid=1001 dice=4 at=4 max=3 high=5") {
		t.Fatalf("trap landing log missing:\n%s", g.logs.String())
	}

	roll = treRollDie(t, g, 1) // trapped, count 1: the token stays on 4
	island = treIslandOf(t, cirBytes(roll, 7))
	if cirVar(cirBytes(roll, 6), 1) != 4 || cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellTrap || cirVar(island, 3) != 1 || cirVar(roll, 8) != 1 {
		t.Fatalf("trapped roll 1 = %x, want square 4 still, count 1", roll)
	}

	roll = treRollDie(t, g, 2) // trapped, count 2: still on 4
	if cirVar(cirBytes(roll, 6), 1) != 4 || cirVar(treIslandOf(t, cirBytes(roll, 7)), 3) != 2 {
		t.Fatalf("trapped roll 2 = %x, want square 4 still, count 2", roll)
	}

	roll = treRollDie(t, g, 3) // the third trapped roll frees the token: 3 steps to coin 7, paid
	if cirVar(cirBytes(roll, 6), 1) != 7 || cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellCoin || cirBytes(cirBytes(roll, 7), 3) == nil {
		t.Fatalf("escape roll = %x, want square 7 (coin) with its coin detail", roll)
	}
	if !strings.Contains(g.logs.String(), "TREASURE msgid=4 trap escape aid=1001 dice=3 from=4 to=7 cell=1") {
		t.Fatalf("escape log missing:\n%s", g.logs.String())
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Trapped || rec.TrapCount != 0 || rec.Position != 7 {
		t.Fatalf("saved board after the escape = %+v", rec)
	}
}

// TestTreasureTrapEscapeByHighDieOverTheGateway: the high die frees a trapped token at once, and the walk skips trap squares
// (a die of 2 from trap 4 would land on trap 6 and goes on to coin 7).
func TestTreasureTrapEscapeByHighDieOverTheGateway(t *testing.T) {
	treScene(t)
	treLoadRing(t, treTrapRing)
	treSetBoard(t, store.TreasureRecord{Position: 4, Trapped: true, TrapCount: 2})
	g := newTreGateway(t)
	g.open()
	treEnterBoard(t, g)

	roll := treRollDie(t, g, 2) // count 2 + 1 reaches max_tries 3: free, 4 + 2 is trap 6, so the walk goes on to coin 7
	if cirVar(cirBytes(roll, 6), 1) != 7 || cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellCoin {
		t.Fatalf("escape over trap 6 = %x, want square 7", roll)
	}
	g.send(treasureCsRelayStart, nil)
	relay := g.readShape(treasureRcRelayStart, treRelay)
	if cirVar(cirBytes(relay, 9), 1) != 7 || cirBytes(relay, 10) != nil {
		t.Fatalf("relay after the escape = %x, want square 7 and no island detail", relay)
	}
}

// TestTreasureTrapRelayOverTheGateway: a relay start on a held token re-shows its island with the saved count (f10, type 6);
// a trap flag that is not on its square is released at entry; a trap square with no trap is walked off it (to coin 7).
func TestTreasureTrapRelayOverTheGateway(t *testing.T) {
	treScene(t)
	treLoadRing(t, treTrapRing)
	treSetBoard(t, store.TreasureRecord{Position: 4, Trapped: true, TrapCount: 1})
	g := newTreGateway(t)
	g.open()
	relay := treEnterBoard(t, g)
	island := treIslandOf(t, cirBytes(relay, 10))
	if cirVar(cirBytes(relay, 9), 1) != 4 || cirVar(cirBytes(relay, 9), 3) != economy.TreasureCellTrap ||
		cirVar(island, 1) != 0 || cirVar(island, 2) != 3 || cirVar(island, 3) != 1 || cirVar(island, 4) != 5 {
		t.Fatalf("relay on the trap = %x, want square 4 (type 6) with island ExitResult 0, 3 rolls, count 1, die 5", relay)
	}

	t.Run("trap flag off its square", func(t *testing.T) {
		treScene(t)
		treLoadRing(t, treTrapRing)
		treSetBoard(t, store.TreasureRecord{Position: 1, Trapped: true, TrapCount: 1})
		g := newTreGateway(t)
		g.open()
		relay := treEnterBoard(t, g)
		if cirVar(cirBytes(relay, 9), 1) != 1 || cirBytes(relay, 10) != nil {
			t.Fatalf("relay on coin 1 with a stale trap = %x, want square 1 and no detail", relay)
		}
		if rec, _ := store.TreasureRecordOf("1001"); rec.Trapped || rec.TrapCount != 0 {
			t.Fatalf("stale trap kept: %+v", rec)
		}
	})

	t.Run("trap square, not trapped", func(t *testing.T) {
		treScene(t)
		treLoadRing(t, treTrapRing)
		treSetBoard(t, store.TreasureRecord{Position: 4})
		g := newTreGateway(t)
		g.open()
		relay := treEnterBoard(t, g)
		if cirVar(cirBytes(relay, 9), 1) != 7 || cirBytes(relay, 10) != nil {
			t.Fatalf("relay on trap 4 with no trap = %x, want square 7 and no detail", relay)
		}
		if !strings.Contains(g.logs.String(), "TREASURE recover aid=1001 trap square without a trap; moved from 4 to 7") {
			t.Fatalf("recovery log missing:\n%s", g.logs.String())
		}
	})
}

// TestTreasureTravelRefusalsOverTheGateway: cs 9 is never answered with a non-zero Result. A target that is not an exchange square,
// the square the token stands on, or a flat (pre-nested) position gets Result 0 with no move: f2 is the broom square, the tickets are
// sent, and the prompt is cleared. The relay start after that moves the token off the broom square (no prompt would be shown).
func TestTreasureTravelRefusalsOverTheGateway(t *testing.T) {
	cases := []struct {
		name string
		body func() []byte
	}{
		{"target not an exchange square", func() []byte { return treMoveCell(6) }},
		{"target the broom square itself", func() []byte { return treMoveCell(5) }},
		{"flat position, not nested", func() []byte { return treMoveFlat(2) }},
		{"no body", func() []byte { return nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			treScene(t)
			treLoadRing(t, treTrapRing)
			treSetBoard(t, store.TreasureRecord{})
			g := newTreGateway(t)
			g.open()
			treEnterBoard(t, g)
			roll := treRollDie(t, g, 5) // onto the broom square 5
			if cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellTravel {
				t.Fatalf("roll = %x, want the broom square", roll)
			}

			g.send(treasureCsFlyTravel, c.body())
			stay := g.readShape(treasureRcFlyTravel, treFlyStay)
			if cirVar(stay, 1) != 0 || cirVar(cirBytes(stay, 2), 1) != 5 || cirVar(cirBytes(stay, 2), 3) != economy.TreasureCellTravel ||
				cirVar(stay, 5) != 9 || cirVar(stay, 6) != 10 {
				t.Fatalf("refused travel = %x, want Result 0, the token on broom square 5, 9 of 10 tickets", stay)
			}
			if !strings.Contains(g.logs.String(), "TREASURE msgid=9 travel refused aid=1001") || !strings.Contains(g.logs.String(), "result=0 token stays") {
				t.Fatalf("refusal log missing:\n%s", g.logs.String())
			}

			g.send(treasureCsRelayStart, nil) // the prompt is gone: the entry releases the token to coin 7
			relay := g.readShape(treasureRcRelayStart, treRelay)
			if cirVar(cirBytes(relay, 9), 1) != 7 || cirVar(cirBytes(relay, 9), 3) != economy.TreasureCellCoin || cirBytes(relay, 10) != nil {
				t.Fatalf("relay after a refused travel = %x, want coin square 7 and no prompt", relay)
			}
		})
	}
}

// TestTreasureTravelReentryOverTheGateway: a relay start on a broom square with its prompt pending re-shows the prompt with its
// targets (f10: Availability 1 and the exchange squares 2, 13 and 21), so a tap can answer it.
func TestTreasureTravelReentryOverTheGateway(t *testing.T) {
	treScene(t)
	treLoadRing(t, treTrapRing)
	treSetBoard(t, store.TreasureRecord{Position: 5, TravelPending: true, TravelSince: treTime.UnixMilli()})
	g := newTreGateway(t)
	g.open()
	relay := treEnterBoard(t, g)
	fly := cirBytes(cirBytes(relay, 10), 10)
	rows := cirList(fly, 2)
	if cirVar(cirBytes(relay, 9), 1) != 5 || cirVar(cirBytes(relay, 9), 3) != economy.TreasureCellTravel || cirVar(fly, 1) != 1 || len(rows) != 3 ||
		cirVar(rows[0], 1) != 2 || cirVar(rows[1], 1) != 13 || cirVar(rows[2], 1) != 21 {
		t.Fatalf("relay on the pending broom = %x, want square 5 (type 8), Availability 1 and the targets 2, 13, 21", relay)
	}
}

// TestTreasureTravelTimeoutOverTheGateway: a broom prompt pending past travel.stale_minutes is cleared by the next entry, which
// moves the token to coin 7 before the relay start replies, so the prompt is never re-shown.
func TestTreasureTravelTimeoutOverTheGateway(t *testing.T) {
	treScene(t)
	treLoadRing(t, treTrapRing)
	stale := treTime.Add(-11 * time.Minute).UnixMilli()
	treSetBoard(t, store.TreasureRecord{Position: 5, TravelPending: true, TravelSince: stale})
	g := newTreGateway(t)
	g.open()
	relay := treEnterBoard(t, g)
	if cirVar(cirBytes(relay, 9), 1) != 7 || cirVar(cirBytes(relay, 9), 3) != economy.TreasureCellCoin || cirBytes(relay, 10) != nil {
		t.Fatalf("relay after the timeout = %x, want coin square 7 and no prompt", relay)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.TravelPending || rec.Position != 7 {
		t.Fatalf("saved board after the timeout = %+v", rec)
	}
	if !strings.Contains(g.logs.String(), "TREASURE recover aid=1001 broom prompt unanswered past travel.stale_minutes; moved from 5 to 7") {
		t.Fatalf("recovery log missing:\n%s", g.logs.String())
	}
}
