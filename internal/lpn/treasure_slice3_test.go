package lpn

import (
	"strings"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// Strict shapes of the slice 3 replies (treasure-spec 3.2 to 3.5, slice3b-spec sections A to C): the mini-game result, the buff
// rows and lists, the secret quest enable and the claim.
var (
	treBuffItemRow   = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true}, 3: {wire: 0, req: true}, 4: {wire: 2, req: true}, 5: {wire: 2, req: true}, 8: {wire: 0, req: true}, 9: {wire: 2, req: true}}
	treBuffList      = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: treBuffItemRow}, 3: {wire: 0, req: true}}
	treBuffWear      = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 2, rep: true, msg: treBuffItemRow}, 4: {wire: 2, req: true, msg: treOwn}}
	treMiniRes       = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}, 4: {wire: 2, req: true, msg: treMyRank}, 5: {wire: 2, req: true}, 6: {wire: 2, req: true, msg: treDetail}}
	treAvail         = lpMsg{1: {wire: 0, req: true}}
	treSpecialEnable = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true, msg: treDetail}}
	treSpecialDone   = lpMsg{1: {wire: 2, req: true, msg: treReward}, 7: {wire: 2, req: true, msg: treAvail}}
	treSpecialClear  = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true, msg: treSpecialDone}}
)

// treBuffBody is the cs 22 list: one BuffItem (f1 ItemCode) per code, repeated in field 1.
func treBuffBody(codes ...string) []byte {
	var b []byte
	for _, code := range codes {
		b = pbLen(b, 1, pbLen(nil, 1, []byte(code)))
	}
	return b
}

// treBuffScene installs a non-lab account 1003 that owns the given items, an empty board book and the clock inside the season.
func treBuffScene(t *testing.T, owned ...string) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{
		"t-1003": {AccessToken: "t-1003", SessionKey: "sk", Aid: "1003", Name: "Buffs", Gender: "FEMALE", InventoryCodes: owned},
	})
	testutil.SetLab(t, "1003", false)
	store.ResetTreasure()
	t.Cleanup(store.ResetTreasure)
	treAt(t, treTime)
}

// treSeedBoard puts aid's board in the state the test needs: the season's start, the tickets and the Jewels.
func treSeedBoard(t *testing.T, aid string, jewels int64) {
	t.Helper()
	if _, err := store.UpdateTreasure(aid, func(r *store.TreasureRecord) {
		*r = store.TreasureRecord{Season: economy.ActiveTreasureData().Season.ID, Tickets: 10, Charged: treTime.UnixMilli(), Jewels: jewels}
	}); err != nil {
		t.Fatal(err)
	}
}

// TestTreasureMiniGameOverTheGateway: landing on the mini-game square shows the allowance (the Jewels held, up to 20); a bet over the
// Jewels is refused; a bet of 4 pays 40 Ranking Points (the first cup, which the test's die draws) and the reply carries the square
// with the Jewels and the allowance left.
func TestTreasureMiniGameOverTheGateway(t *testing.T) {
	treScene(t)
	treLoadRing(t, treProbeRing)
	treSeedBoard(t, "1001", 5)
	treDice(t, 6) // square 0 to 6, the mini-game
	g := newTreGateway(t)
	g.open()
	g.login(1001)
	g.send(treasureCsRoomEnter, nil)
	g.readShape(treasureRcRoomEnter, treEnter)

	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	roll := g.readShape(treasureRcRolling, treRoll)
	game := cirBytes(cirBytes(roll, 7), 11)
	if cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellMiniGame || cirVar(game, 2) != 5 || cirVar(game, 3) != 20 || cirVar(game, 7) != 5 {
		t.Fatalf("mini-game landing = %x, want 5 Jewels held, max 20 and 5 to use", roll)
	}

	g.send(treasureCsMiniGame, pbVar(nil, 1, 9)) // more than the Jewels held: refused
	if cirVar(g.readShape(treasureRcMiniGame, treResult), 1) != treasureRefused {
		t.Fatal("a bet above the Jewels was not refused")
	}

	g.send(treasureCsMiniGame, pbVar(nil, 1, 4))
	body := g.readShape(treasureRcMiniGame, treMiniRes)
	after := cirBytes(cirBytes(body, 6), 11)
	if cirVar(body, 1) != 0 || cirVar(body, 2) != 40 || cirVar(body, 3) != 1 || string(cirBytes(body, 5)) != "+40 Ranking Point(s)" ||
		cirVar(after, 2) != 1 || cirVar(after, 7) != 1 {
		t.Fatalf("mini-game reply = %x, want result 0, 40 points, cup 1, 1 Jewel left and 1 to use", body)
	}
	if !strings.Contains(g.logs.String(), "TREASURE msgid=8 minigame aid=1001 bet=4 multiple=1 added=40 points=40 jewels=1 left=16 result=0") {
		t.Fatalf("mini-game log line missing:\n%s", g.logs.String())
	}
}

// TestTreasureSecretQuestOverTheGateway: a landing on the secret quest square (5) shows the quest; cs 26 answers with the same
// detail, enabled on the square; the Gacha pull (economy.TreasureGachaPulled, the buyer's pull) makes it claimable; the next relay
// start carries the square's detail with EnableReward set, and cs 25 pays the tickets once and clears the square.
func TestTreasureSecretQuestOverTheGateway(t *testing.T) {
	treScene(t)
	treLoadRing(t, treProbeRing)
	treDice(t, 4) // square 0 to 4, the secret quest square (type 5)
	g := newTreGateway(t)
	g.open()
	g.login(1001)
	g.send(treasureCsRoomEnter, nil)
	g.readShape(treasureRcRoomEnter, treEnter)

	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	roll := g.readShape(treasureRcRolling, treRoll)
	quest := cirBytes(cirBytes(cirBytes(roll, 7), 7), 2)
	if cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellSpecial || cirVar(quest, 3) != 1 || cirVar(quest, 5) != 3 || cirVar(quest, 7) != 0 ||
		string(cirBytes(cirBytes(quest, 6), 1)) != treasureShopLink {
		t.Fatalf("secret quest landing = %x, want goal count 1, reward 3, not enabled, the Gacha Shop link", roll)
	}
	goal := cirBytes(cirBytes(quest, 2), 1)
	if string(cirBytes(goal, 1)) != "Normal" || cirVar(goal, 2) != 1 {
		t.Fatalf("goal = %x, want a Normal goal of 1", goal)
	}

	g.send(treasureCsSpecialEnable, nil)
	enable := g.readShape(treasureRcSpecialEnable, treSpecialEnable)
	if cirVar(enable, 1) != 0 || cirVar(cirBytes(cirBytes(enable, 2), 7), 1) != 1 || cirVar(cirBytes(cirBytes(cirBytes(enable, 2), 7), 2), 7) != 0 {
		t.Fatalf("enable reply = %x, want result 0 and the square's quest", enable)
	}
	if !strings.Contains(g.logs.String(), "TREASURE msgid=26 secret enable aid=1001 square=4 count=0 goal=1 result=0") {
		t.Fatalf("enable log line missing:\n%s", g.logs.String())
	}

	economy.TreasureGachaPulled("1001", 1)
	g.send(treasureCsRelayStart, nil)
	relay := g.readShape(treasureRcRelayStart, treRelay)
	detail := cirBytes(cirBytes(relay, 10), 7)
	if cirVar(cirBytes(relay, 9), 3) != economy.TreasureCellSpecial || cirVar(cirBytes(detail, 2), 4) != 1 || cirVar(cirBytes(detail, 2), 7) != 1 {
		t.Fatalf("relay start = %x, want the square's detail with count 1 and EnableReward 1", relay)
	}

	g.send(treasureCsSpecialReward, nil)
	claim := g.readShape(treasureRcSpecialReward, treSpecialClear)
	reward := cirBytes(cirBytes(claim, 2), 1)
	rows := cirList(reward, 2)
	if cirVar(claim, 1) != 0 || len(rows) != 1 || cirVar(rows[0], 1) != 9 || cirVar(rows[0], 4) != 3 || string(cirBytes(reward, 5)) != "+3 Jewel Carriage(s)" ||
		cirVar(cirBytes(cirBytes(claim, 2), 7), 1) != 0 {
		t.Fatalf("claim = %x, want result 0, 3 tickets in the reward and the square cleared", claim)
	}

	g.send(treasureCsRelayStart, nil) // back on the board: 9 tickets after the roll, plus the 3 of the claim
	if relay2 := g.readShape(treasureRcRelayStart, treRelay); cirVar(relay2, 2) != 12 {
		t.Fatalf("tickets after the claim = %d, want 12", cirVar(relay2, 2))
	}
	g.send(treasureCsSpecialReward, nil) // a second claim: refused
	if cirVar(g.readShape(treasureRcSpecialReward, treResult), 1) != treasureRefused {
		t.Fatal("a second claim was not refused")
	}
}

// TestTreasureBuffOverTheGateway: a buff square draws an item the account does not own (CUSH003EY here, the one not owned) with its
// LinkInfo; cs 22 wears the owned CUAH006FG (the relay start then carries 10 percent, and the reply and the list show it worn);
// refused and empty outfits are answered; the buff percent follows the saved outfit.
func TestTreasureBuffOverTheGateway(t *testing.T) {
	treBuffScene(t, "CUAH006FG")
	treLoadRing(t, treProbeRing)
	treDice(t, 3) // square 0 to 3, the buff square
	g := newTreGateway(t)
	g.open()
	g.login(1003)
	g.send(treasureCsRoomEnter, nil)
	g.readShape(treasureRcRoomEnter, treEnter)

	g.send(treasureCsBuffList, nil)
	list := g.readShape(treasureRcBuffList, treBuffList)
	rows := cirList(list, 2)
	if len(rows) != 1 || string(cirBytes(rows[0], 4)) != "CUAH006FG" || string(cirBytes(rows[0], 9)) != "INVEN" || cirVar(rows[0], 8) != 10 || cirVar(list, 3) != 0 {
		t.Fatalf("buff list = %x, want the owned CUAH006FG, INVEN, 10 percent and no worn total", list)
	}

	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	roll := g.readShape(treasureRcRolling, treRoll)
	buff := cirBytes(cirBytes(roll, 7), 5)
	if cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellBuff || string(cirBytes(buff, 2)) != "CUSH003EY" || cirVar(buff, 3) != 15 ||
		string(cirBytes(buff, 6)) != "NEW" || string(cirBytes(cirBytes(buff, 4), 1)) != treasureShopLink {
		t.Fatalf("buff landing = %x, want CUSH003EY at 15 percent, NEW, with the Gacha Shop link", roll)
	}

	g.send(treasureCsBuffWear, treBuffBody("CUAH006FG"))
	wear := g.readShape(treasureRcBuffWear, treBuffWear)
	worn := cirList(wear, 3)
	if cirVar(wear, 1) != 0 || cirVar(wear, 2) != 10 || len(worn) != 1 || string(cirBytes(worn[0], 9)) != "WEAR" ||
		cirVar(cirBytes(wear, 4), 1) != 1003 {
		t.Fatalf("wear reply = %x, want result 0, 10 percent, CUAH006FG worn and the own avatar", wear)
	}
	if !strings.Contains(g.logs.String(), "TREASURE msgid=22 buff wear aid=1003 outfit=1 percent=10 result=0") {
		t.Fatalf("wear log line missing:\n%s", g.logs.String())
	}

	g.send(treasureCsRelayStart, nil)
	if relay := g.readShape(treasureRcRelayStart, treRelay); cirVar(relay, 7) != 10 {
		t.Fatalf("relay start buff percent = %d, want 10", cirVar(relay, 7))
	}

	g.send(treasureCsBuffList, nil)
	if list := g.readShape(treasureRcBuffList, treBuffList); cirVar(list, 3) != 10 || string(cirBytes(cirList(list, 2)[0], 9)) != "WEAR" {
		t.Fatalf("buff list after the wear = %x, want WEAR and 10 percent", list)
	}

	for _, bad := range [][]byte{treBuffBody("CUSH003EY"), treBuffBody("CUNOPE0000", "CUSH003EY")} { // a pool item not owned
		g.send(treasureCsBuffWear, bad)
		if cirVar(g.readShape(treasureRcBuffWear, treResult), 1) != treasureRefused {
			t.Fatalf("wear %x was not refused", bad)
		}
	}

	g.send(treasureCsBuffWear, nil) // an empty outfit is refused and the saved outfit stays
	if cirVar(g.readShape(treasureRcBuffWear, treResult), 1) != treasureRefused {
		t.Fatal("an empty outfit was not refused")
	}
	g.send(treasureCsBuffList, nil)
	if list := g.readShape(treasureRcBuffList, treBuffList); cirVar(list, 3) != 10 {
		t.Fatalf("buff list after the refused outfit = %x, want 10 percent still worn", list)
	}
}
