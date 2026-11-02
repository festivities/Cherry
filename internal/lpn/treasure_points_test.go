package lpn

import (
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

// Strict shapes of the replies of treasure_points.go and treasure_tasks.go (lpCheck). Field numbers are the wire tables of
// cells.md 2.1, 3.1 to 3.3, 4.3 and 6.4, transport-money.md 4 and the decompiles lp_LP_sc_*.c.
var (
	treMyRank       = lpMsg{1: {wire: 0}, 2: {wire: 0}, 3: {wire: 2}, 5: {wire: 0}}
	treSixRes       = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0}, 3: {wire: 2, msg: treMyRank}, 4: {wire: 2, msg: treDetail}, 5: {wire: 2}}
	treSevenRes     = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2}, 3: {wire: 0}, 4: {wire: 0}, 5: {wire: 2, msg: treDetail}}
	treResetInfoRes = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2}, 3: {wire: 0}, 4: {wire: 0}}
	treRankRow      = lpMsg{1: {wire: 0}, 2: {wire: 0}, 3: {wire: 0}, 4: {wire: 2}, 7: {wire: 2}, 8: {wire: 0}}
	treRankingRes   = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, msg: treMyRank}, 3: {wire: 2, rep: true, msg: treRankRow}, 4: {wire: 0}}
	treRewardRow    = lpMsg{1: {wire: 0}, 3: {wire: 0}, 4: {wire: 2}, 5: {wire: 2}, 7: {wire: 2}, 9: {wire: 0}, 10: {wire: 0}, 11: {wire: 0}, 12: {wire: 2}}
	treRewardList   = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: treRewardRow}}
	treRewardItem   = lpMsg{1: {wire: 0}, 4: {wire: 0}}
	treRewardInfo   = lpMsg{2: {wire: 2, rep: true, req: true, msg: treRewardItem}, 5: {wire: 2}}
	treClaimRes     = lpMsg{1: {wire: 0, req: true}, 3: {wire: 2, msg: treRewardInfo}, 4: {wire: 0}}
	treTierItem     = lpMsg{1: {wire: 0}, 2: {wire: 0}, 4: {wire: 2}, 6: {wire: 2}, 8: {wire: 0}, 9: {wire: 2}}
	treTier         = lpMsg{1: {wire: 0}, 2: {wire: 0}, 3: {wire: 2, rep: true, msg: treTierItem}}
	treClassRes     = lpMsg{1: {wire: 0, req: true}, 3: {wire: 2, rep: true, msg: treTier}, 4: {wire: 0}}
	treClassClaim   = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, msg: treRewardInfo}}
	treQuest        = lpMsg{1: {wire: 0}, 2: {wire: 0}, 3: {wire: 0}, 4: {wire: 0}, 6: {wire: 0}, 7: {wire: 0}, 8: {wire: 2}, 9: {wire: 2}}
	treAch          = lpMsg{1: {wire: 0}, 2: {wire: 0}, 3: {wire: 0}, 4: {wire: 0}, 5: {wire: 0}, 6: {wire: 0}, 8: {wire: 0}}
	treListRes      = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: treQuest}, 3: {wire: 2, rep: true, msg: treAch}, 4: {wire: 0}, 5: {wire: 0}}
	treAchClaim     = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, msg: treRewardInfo}, 3: {wire: 2, msg: treAch}}
	treQuestClaim   = lpMsg{1: {wire: 0, req: true}, 3: {wire: 2, msg: treRewardInfo}, 4: {wire: 0}, 5: {wire: 0}, 6: {wire: 0}, 7: {wire: 0}, 8: {wire: 0}, 9: {wire: 0}, 10: {wire: 2, rep: true, msg: treQuest}}
	treRefresh      = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: treQuest}, 3: {wire: 0}, 4: {wire: 0}}
	treCapInfo      = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0}, 3: {wire: 0}, 4: {wire: 2}, 5: {wire: 0}, 6: {wire: 0}}
	treCapBuy       = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0}, 3: {wire: 0}, 4: {wire: 0}, 5: {wire: 0}, 6: {wire: 0}, 7: {wire: 0}, 8: {wire: 2}, 9: {wire: 0}, 10: {wire: 0}}
)

// treSeed replaces the account's board state for one test (the book is empty after treScene).
func treSeed(t *testing.T, rec store.TreasureRecord) {
	t.Helper()
	if _, err := store.UpdateTreasure("1001", func(r *store.TreasureRecord) { *r = rec }); err != nil {
		t.Fatal(err)
	}
}

// treGems sets the Gems of account 1001.
func treGems(t *testing.T, gems int64) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	store.AccountByAidLocked("1001").Gems = gems
}

// treAccount returns account 1001's Gems and Mailbox rows.
func treAccount(t *testing.T) (int64, []store.MailRow) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked("1001")
	return acc.Gems, append([]store.MailRow(nil), acc.Mail...)
}

// treOpenBoard opens the gateway, logs in 1001, enters the board and starts the relay, as a player does.
func treOpenBoard(t *testing.T) *treGateway {
	t.Helper()
	g := newTreGateway(t)
	g.open()
	g.login(1001)
	g.send(treasureCsRoomEnter, nil)
	g.readShape(treasureRcRoomEnter, treEnter)
	g.send(treasureCsRelayStart, nil)
	g.readShape(treasureRcRelayStart, treRelay)
	return g
}

// treDayOfTest is the game day of the test clock (06:00 JST day rule of economy.treasureDay).
var treDayOfTest = (treTime.Unix() + 3*3600) / 86400

// TestTreasureExchangeAndResetOverTheGateway: the exchange square's counts, the exchange and its limit, the reset offer, a
// CASH reset refused, a GEM reset that spends Gems, and the ranking board that lists the board.
func TestTreasureExchangeAndResetOverTheGateway(t *testing.T) {
	treScene(t)
	treSeed(t, store.TreasureRecord{Season: 1950000001, Tickets: 10, Charged: treTime.UnixMilli(), Position: 2, Jewels: 40, ExDay: treDayOfTest, Exchanged: map[string]int64{"2": 30}})
	treGems(t, 50)
	g := treOpenBoard(t)

	g.send(treasureCsResetInfo, nil) // the square is not full: no offer
	if cirVar(g.readShape(treasureRcResetInfo, treResult), 1) != treasureRefused {
		t.Fatal("reset offer for a square that is not full")
	}

	g.send(treasureCsPointChange, pbVar(nil, 1, 30)) // 30 more Jewels at 10 points each: the square (cap 60) is full
	ex := g.readShape(treasureRcPointChange, treSixRes)
	if cirVar(ex, 1) != 0 || cirVar(ex, 2) != 300 || string(cirBytes(ex, 5)) != "+300 Ranking Point(s)" {
		t.Fatalf("exchange reply = %x, want 300 points", ex)
	}
	if me := cirBytes(ex, 3); cirVar(me, 1) != 1 || cirVar(me, 2) != 300 || string(cirBytes(me, 3)) != "Gold" {
		t.Fatalf("my ranking in the exchange reply = %x", me)
	}
	if sq := cirBytes(cirBytes(ex, 4), 4); cirVar(sq, 3) != 0 || cirVar(sq, 10) != 1 || cirVar(sq, 5) != 60 {
		t.Fatalf("square counts after the exchange = %x, want nothing left and the reset flag", sq)
	}

	g.send(treasureCsPointChange, pbVar(nil, 1, 1)) // the square takes no more today
	if cirVar(g.readShape(treasureRcPointChange, treResult), 1) != treasureRefused {
		t.Fatal("exchange past the daily limit was not refused")
	}

	g.send(treasureCsResetInfo, nil) // full now: 30 Gems, 50 held
	off := g.readShape(treasureRcResetInfo, treResetInfoRes)
	if string(cirBytes(off, 2)) != "GEM" || cirVar(off, 3) != 30 || cirVar(off, 4) != 50 {
		t.Fatalf("reset offer = %x, want GEM 30 of 50", off)
	}

	g.send(treasureCsPointReset, pbLen(nil, 1, []byte("CASH"))) // never CASH
	if cirVar(g.readShape(treasureRcPointReset, treResult), 1) != treasureRefused {
		t.Fatal("reset paid in CASH was not refused")
	}
	g.send(treasureCsPointReset, pbLen(nil, 1, []byte("GEM")))
	reset := g.readShape(treasureRcPointReset, treSevenRes)
	if string(cirBytes(reset, 2)) != "GEM" || cirVar(reset, 3) != 30 || cirVar(reset, 4) != 20 {
		t.Fatalf("reset reply = %x, want GEM 30, 20 left", reset)
	}
	if sq := cirBytes(cirBytes(reset, 5), 4); cirVar(sq, 3) != 60 || cirVar(sq, 10) != 0 {
		t.Fatalf("square counts after the reset = %x, want the limit back", sq)
	}
	if gems, _ := treAccount(t); gems != 20 {
		t.Fatalf("Gems after the reset = %d, want 20", gems)
	}

	g.send(treasureCsRankingInfo, pbVar(pbVar(nil, 2, 1), 3, 20)) // ranks 1 to 20
	ranking := g.readShape(treasureRcRankingInfo, treRankingRes)
	rows := cirList(ranking, 3)
	if len(rows) != 1 || cirVar(rows[0], 1) != 1001 || cirVar(rows[0], 2) != 1 || cirVar(rows[0], 3) != 300 ||
		string(cirBytes(rows[0], 4)) != "Cherry" || string(cirBytes(rows[0], 7)) != "Gold" || cirVar(ranking, 4) != 1 {
		t.Fatalf("ranking = %x, want one row: 1001 at rank 1 with 300 points, Gold, one page", ranking)
	}

	for _, want := range []string{"TREASURE msgid=6 exchange aid=1001 jewels=30 added=300", "TREASURE msgid=7 reset aid=1001 price=30 gems=20 result=0",
		"TREASURE msgid=27 reset info refused", "TREASURE msgid=11 ranking aid=1001 start=1 end=20 rows=1 pages=1 result=0"} {
		if !strings.Contains(g.logs.String(), want) {
			t.Fatalf("log lacks %q:\n%s", want, g.logs.String())
		}
	}
}

// TestTreasureRewardsAndQuestsOverTheGateway: the point rewards (Gems by Mailbox, the item with them), the rank rewards
// inside the season (none), the quest and mission lists with their timers, and every claim or refresh that is not due.
func TestTreasureRewardsAndQuestsOverTheGateway(t *testing.T) {
	treScene(t)
	treSeed(t, store.TreasureRecord{Season: 1950000001, Tickets: 10, Charged: treTime.UnixMilli(), Points: 300})
	g := treOpenBoard(t)

	g.send(treasureCsPointRewardInfo, nil)
	rows := cirList(g.readShape(treasureRcPointRewardInfo, treRewardList), 2)
	if len(rows) != 4 || cirVar(rows[0], 1) != 1 || cirVar(rows[0], 11) != 100 || cirVar(rows[0], 10) != 0 ||
		cirVar(rows[0], 9) != 50 || string(cirBytes(rows[0], 12)) != "gem" || string(cirBytes(rows[1], 4)) != "CUAH006FG" {
		t.Fatalf("point rewards = %x", rows)
	}

	g.send(treasureCsPointRewardClaim, pbVar(nil, 1, 1)) // 100 points' reward: 50 Gems by Mailbox
	claim := g.readShape(treasureRcPointReward, treClaimRes)
	if !strings.Contains(string(cirBytes(cirBytes(claim, 3), 5)), "50 Gems") || cirVar(claim, 4) != 1 {
		t.Fatalf("point reward claim = %x", claim)
	}
	if _, mail := treAccount(t); len(mail) != 1 || mail[0].Amount != 50 {
		t.Fatalf("mail after reward 1 = %+v", mail)
	}
	g.send(treasureCsPointRewardClaim, pbVar(nil, 1, 1))
	if cirVar(g.readShape(treasureRcPointReward, treResult), 1) != treasureRefused {
		t.Fatal("reward 1 claimed twice")
	}
	g.send(treasureCsPointRewardClaim, pbVar(nil, 1, 2)) // 300 points: 100 Gems and the dress item, two rows
	g.readShape(treasureRcPointReward, treClaimRes)
	if _, mail := treAccount(t); len(mail) != 3 || mail[2].Item != "CUAH006FG" {
		t.Fatalf("mail after reward 2 = %+v", mail)
	}

	g.send(treasureCsClassRewardInfo, nil) // inside the season: no rank tiers, no active reward
	if body := g.readShape(treasureRcClassRewardInfo, treClassRes); len(cirList(body, 3)) != 0 || cirVar(body, 4) != 0 {
		t.Fatalf("rank rewards inside the season = %x", body)
	}
	g.send(treasureCsClassRewardClaim, nil)
	if cirVar(g.readShape(treasureRcClassReward, treResult), 1) != treasureRefused {
		t.Fatal("rank reward claimed inside the season")
	}

	g.send(treasureCsQuestList, nil) // the first list posts both sets
	lists := g.readShape(treasureRcQuestList, treListRes)
	quests, missions := cirList(lists, 2), cirList(lists, 3)
	if len(quests) != 5 || len(missions) != 4 || cirVar(quests[0], 1) != 1 || cirVar(quests[0], 7) != 2 ||
		string(cirBytes(quests[0], 8)) != "Roll the dice 3 times" || string(cirBytes(quests[0], 9)) != "CUAH006FG" {
		t.Fatalf("quest list = %x", lists)
	}
	if cirVar(missions[0], 1) != 101 || cirVar(missions[0], 6) != 3 || cirVar(lists, 4) != 0 ||
		cirVar(lists, 5) != uint64(treTime.Add(24*time.Hour).Unix()) {
		t.Fatalf("mission list = %x, free refresh and reset time", missions[0])
	}

	g.send(treasureCsQuestRefresh, pbVar(nil, 1, 1)) // free, but 24 h have not passed
	if cirVar(g.readShape(treasureRcQuestRefresh, treResult), 1) != treasureRefused {
		t.Fatal("refresh before 24 h")
	}
	g.send(treasureCsQuestRefresh, pbVar(nil, 1, 0)) // paid refreshes are not sold
	if cirVar(g.readShape(treasureRcQuestRefresh, treResult), 1) != treasureRefused {
		t.Fatal("paid refresh was not refused")
	}
	g.send(treasureCsQuestClaim, pbVar(nil, 1, 1)) // not complete
	if cirVar(g.readShape(treasureRcQuestReward, treResult), 1) != treasureRefused {
		t.Fatal("claim of an unfinished quest")
	}
	g.send(treasureCsMissionClaim, pbVar(nil, 1, 101)) // not complete
	if cirVar(g.readShape(treasureRcAchievementReward, treResult), 1) != treasureRefused {
		t.Fatal("claim of an unfinished mission")
	}
	for _, want := range []string{"TREASURE msgid=14 point reward aid=1001 id=1 gems=50", "TREASURE msgid=16 quest list aid=1001 quests=5 missions=4 free=false result=0",
		"TREASURE msgid=18 quest refused aid=1001 slot=1", "TREASURE msgid=17 mission refused aid=1001 seq=101"} {
		if !strings.Contains(g.logs.String(), want) {
			t.Fatalf("log lacks %q:\n%s", want, g.logs.String())
		}
	}
}

// TestTreasureCapAndMoneyOverTheGateway: the ticket cap raise offer and buy in earned Gems; a raise the Gems do not cover is
// refused and spends nothing.
func TestTreasureCapAndMoneyOverTheGateway(t *testing.T) {
	treScene(t)
	treSeed(t, store.TreasureRecord{Season: 1950000001, Tickets: 10, Charged: treTime.UnixMilli()})
	treGems(t, 100)
	g := treOpenBoard(t)

	g.send(treasureCsCapInfo, nil)
	info := g.readShape(treasureRcLimitInfo, treCapInfo)
	if cirVar(info, 2) != 10 || cirVar(info, 3) != 25 || string(cirBytes(info, 4)) != "GEM" || cirVar(info, 5) != 40 || cirVar(info, 6) != 100 {
		t.Fatalf("cap info = %x, want cap 10 of 25, GEM 40, 100 held", info)
	}
	g.send(treasureCsCapBuy, nil)
	buy := g.readShape(treasureRcLimitBuy, treCapBuy)
	if cirVar(buy, 2) != 15 || cirVar(buy, 3) != 25 || cirVar(buy, 5) != 15 || string(cirBytes(buy, 8)) != "GEM" ||
		cirVar(buy, 9) != 40 || cirVar(buy, 10) != 60 {
		t.Fatalf("cap buy = %x, want cap 15 for 40 GEM, 60 left", buy)
	}
	if gems, _ := treAccount(t); gems != 60 {
		t.Fatalf("Gems after the raise = %d, want 60", gems)
	}

	treGems(t, 10) // the next raise costs 80
	g.send(treasureCsCapBuy, nil)
	if cirVar(g.readShape(treasureRcLimitBuy, treResult), 1) != treasureRefused {
		t.Fatal("cap raise without the Gems was not refused")
	}
	if gems, _ := treAccount(t); gems != 10 {
		t.Fatalf("Gems after the refused raise = %d, want 10", gems)
	}
	if !strings.Contains(g.logs.String(), "TREASURE msgid=21 cap buy aid=1001 cap=15 paid=40 gems=60 result=0") ||
		!strings.Contains(g.logs.String(), "TREASURE msgid=21 cap buy refused aid=1001") {
		t.Fatalf("cap log lines missing:\n%s", g.logs.String())
	}
}
