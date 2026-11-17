package lpn

import (
	"strconv"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestSquareFishingRefusedElsewhere: a cast in LINE City (36) or LINE Park (35) gets the miss result, and no Gems are paid.
func TestSquareFishingRefusedElsewhere(t *testing.T) {
	pinEventClock(t, time.Date(2026, time.October, 11, 12, 0, 0, 0, eventJST))
	const aidText = "9803"
	aid, err := strconv.ParseUint(aidText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	testutil.InstallAccounts(t, map[string]*store.Account{"fish-" + aidText: {AccessToken: "fish-" + aidText, SessionKey: "sk", Aid: aidText, Name: "Elsewhere"}})
	testutil.UseDatabase(t)
	for _, theme := range []uint64{squareCityThemeID, 35} {
		c := newSqClient(t, &squareRoom{}, 1)
		c.joinTheme(aid, theme)
		base := fishingBalance(t, aidText)
		fishingReply(t, c, 27, pbVar(nil, 1, squareSpotSerial), 30, squareFishMiss, aid)
		c.send(29, pbVar(nil, 1, 0))
		c.expect(32, pbVar(pbVar(nil, 1, aid), 2, squareFishMiss))
		fishingReply(t, c, 30, pbVar(nil, 1, 1), 33, squareFishMiss, aid)
		if got := fishingBalance(t, aidText); got != base {
			t.Fatalf("theme %d: balance %d after refused cast, want %d", theme, got, base)
		}
	}
}

// TestSquareRideShopRefusedOnIsland: the Ride Point shop is LINE City's. On Treasure Island the list is ignored and the
// purchase gets the refusal; in LINE City the list answers.
func TestSquareRideShopRefusedOnIsland(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11))
	c := newSqClientIn(t, &squareRooms{}, 1)
	c.join(11)
	c.send(61, nil)
	c.none()
	c.send(62, pbVar(pbVar(nil, 1, 1), 2, 1))
	c.expect(81, pbVar(nil, 1, squareRideShopRefused))

	city := newSqClientIn(t, &squareRooms{}, 2)
	city.joinTheme(11, squareCityThemeID)
	city.send(61, nil)
	if id, _ := city.read(); id != 80 {
		t.Fatalf("LINE City list msgid = %d, want 80", id)
	}
}

// TestSquareBossTryRefusedInLineCity: a boss try in LINE City gets the refused try (started false), not a lobby.
func TestSquareBossTryRefusedInLineCity(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11))
	c := newSqClientIn(t, &squareRooms{}, 1)
	c.joinTheme(11, squareCityThemeID)
	c.send(45, nil)
	if id, _ := c.read(); id != 57 {
		t.Fatalf("boss try msgid = %d, want the refused try 57", id)
	}
}

// TestSquareSpotsOnMarkers: one fishing spot per marker tile, serials from squareSpotSerial, each on its tile's game px and
// hosted by the marker item.
func TestSquareSpotsOnMarkers(t *testing.T) {
	list := squareTreasureIslandNpcList()
	for i, tile := range squareSpotTiles {
		npc := squareFirstInteriorNpc(t, list, i)
		px, py := tilePx(squareIslandSize, tile)
		if got := squareUint(t, npc, 1); got != squareSpotSerial+uint64(i) {
			t.Fatalf("spot %d serial = %d", i, got)
		}
		if squareUint(t, npc, 10) != px || squareUint(t, npc, 11) != py {
			t.Fatalf("spot %d Px/Py = (%d,%d), tile %v = (%d,%d)", i, squareUint(t, npc, 10), squareUint(t, npc, 11), tile, px, py)
		}
		if got := squareUint(t, squareNested(t, npc, 2), 5); got != squareSpotInteriorID {
			t.Fatalf("spot %d InteriorID = %d", i, got)
		}
	}
}

// TestSquareTreasureIslandNpcKeys: every NPC of the Treasure Island list has its own (serial, InteriorID) key, and the
// Treasure World entry is the thirteenth: serial 6 on host RUTD000R8, with its link and button flag.
func TestSquareTreasureIslandNpcKeys(t *testing.T) {
	list := squareTreasureIslandNpcList()
	n := len(squareRepeated(t, list, 1))
	if n != len(squareSpotTiles)+3 {
		t.Fatalf("Treasure Island has %d NPCs, want the spots, the shop, the office and the entry", n)
	}
	keys := map[[2]uint64]bool{}
	for i := range n {
		npc := squareFirstInteriorNpc(t, list, i)
		key := [2]uint64{squareUint(t, npc, 1), squareUint(t, squareNested(t, npc, 2), 5)}
		if keys[key] {
			t.Fatalf("NPC %d repeats serial %d / InteriorID %d", i, key[0], key[1])
		}
		keys[key] = true
	}
	npc := squareFirstInteriorNpc(t, list, n-1)
	interior := squareNested(t, npc, 2)
	if got := squareUint(t, squareNested(t, interior, 1), 1); got != 0x3010033 {
		t.Fatalf("entry NpcIndex = %#x, want 0x3010033", got)
	}
	if squareUint(t, npc, 1) != squareTreasureNpcSerial || squareUint(t, interior, 5) != 121700980 {
		t.Fatalf("entry serial/host = %d/%d", squareUint(t, npc, 1), squareUint(t, interior, 5))
	}
	if x, y := tilePx(squareIslandSize, squareTreasureTile); squareUint(t, npc, 10) != x || squareUint(t, npc, 11) != y || x != 5120 || y != 2820 {
		t.Fatalf("entry Px/Py = (%d,%d), tile (73,105) = (5120,2820)", squareUint(t, npc, 10), squareUint(t, npc, 11))
	}
	link, found, _ := pbBytesField(npc, 73)
	if !found || string(link) != "goSomewhere(treasureWorld)" {
		t.Fatalf("entry KeyLink = %q, want goSomewhere(treasureWorld)", link)
	}
	if _, found, _ := pbBytesField(npc, 74); found {
		t.Fatal("entry KeyLinkParam is set, want none")
	}
	if squareUint(t, npc, 75) != 1 {
		t.Fatalf("entry AniButtonVisible = %d, want 1", squareUint(t, npc, 75))
	}
}

// TestSquareLineCityNpcs: LINE City keeps the race entrance and the Ride Point shop, with their stock hosts, and nothing else.
func TestSquareLineCityNpcs(t *testing.T) {
	list := squareLineCityNpcList()
	if n := len(squareRepeated(t, list, 1)); n != 2 {
		t.Fatalf("LINE City has %d NPCs, want the race entrance and the Ride Point shop", n)
	}
	if got := squareUint(t, squareNested(t, squareFirstInteriorNpc(t, list, 0), 2), 5); got != squareRacingInteriorID {
		t.Fatalf("first LINE City NPC host = %d, want the race balloon %d", got, squareRacingInteriorID)
	}
}
