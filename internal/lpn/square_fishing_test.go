package lpn

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// squareNested follows length-delimited fields in order, failing the test when one is missing.
func squareNested(t *testing.T, data []byte, fields ...uint64) []byte {
	t.Helper()
	for _, f := range fields {
		d, found, ok := pbBytesField(data, f)
		if !found || !ok {
			t.Fatalf("field %d missing", f)
		}
		data = d
	}
	return data
}

func squareUint(t *testing.T, data []byte, field uint64) uint64 {
	t.Helper()
	v, found := protobufUintField(data, field)
	if !found {
		t.Fatalf("varint field %d missing in %x", field, data)
	}
	return v
}

func TestSquareFishingSpotEncoding(t *testing.T) {
	// The first marker on Treasure Island (N=160): tile (70,105), game px (5000,2880).
	if x, y := tilePx(squareIslandSize, squareSpotTiles[0]); x != 5000 || y != 2880 {
		t.Fatalf("spot px = (%d,%d), tile (70,105) = (5000,2880)", x, y)
	}
	// The 10082 gate needs the host's kind (CInObject::GetType = interior id / 1e5 mod 100) to be 4.
	if kind := (squareSpotInteriorID % 10000000) / 100000; kind != 4 {
		t.Fatalf("host kind = %d, want 4", kind)
	}
	list := squareTreasureIslandNpcList()
	npc := squareFirstInteriorNpc(t, list, 0) // NPCEnumList.NpcList[0] -> NPCEnum.InteriorNpc: the first spot
	px, py := tilePx(squareIslandSize, squareSpotTiles[0])
	interior := squareNested(t, npc, 2)    // _SQInteriorNpc.Appearance
	npcApp := squareNested(t, interior, 1) // _SQInteriorAppearance.Apperance
	if got := squareUint(t, npcApp, 1); got != 0x3010007 {
		t.Fatalf("NpcIndex = %#x, want the fishing spot type 0x3010007", got)
	}
	if got := squareUint(t, interior, 5); got != squareSpotInteriorID {
		t.Fatalf("InteriorID = %d", got)
	}
	if squareUint(t, interior, 3) != 1 || squareUint(t, interior, 4) != 1 {
		t.Fatalf("SizeX/SizeY = %d/%d", squareUint(t, interior, 3), squareUint(t, interior, 4))
	}
	if got := squareUint(t, npc, 1); got != squareSpotSerial {
		t.Fatalf("Serial = %d", got)
	}
	pos := squareNested(t, npc, 3)
	if squareUint(t, pos, 1) != px || squareUint(t, pos, 2) != py {
		t.Fatalf("PositionInfo = (%d,%d)", squareUint(t, pos, 1), squareUint(t, pos, 2))
	}
	if squareUint(t, npc, 10) != px || squareUint(t, npc, 11) != py {
		t.Fatalf("Px/Py = (%d,%d)", squareUint(t, npc, 10), squareUint(t, npc, 11))
	}
	for _, f := range []uint64{42, 43, 44} { // IsShow, Attackable, IsVisible
		if squareUint(t, npc, f) != 1 {
			t.Fatalf("flag %d not set", f)
		}
	}
}

// fishingRoomAccount installs aid with gems, saves into a temp dir (ledger next to accounts.json),
// and returns a client that has entered Square.
func fishingRoomAccount(t *testing.T, aid string, gems int64) *sqClient {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"fish-" + aid: {AccessToken: "fish-" + aid, SessionKey: "sk", Aid: aid, Name: "Fisher", Gems: gems}})
	testutil.UseDatabase(t)
	c := newSqClient(t, &squareRoom{}, 1)
	n, err := strconv.ParseUint(aid, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	c.join(n)
	return c
}

func fishingBalance(t *testing.T, aid string) int64 {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return acc.Gems
}

// fishingReply sends one request and checks the reply msgid, the Aid and the success Result.
func fishingReply(t *testing.T, c *sqClient, req uint16, body []byte, wantID uint16, wantResult uint64, aid uint64) []byte {
	t.Helper()
	c.send(req, body)
	if (req == 29 && wantResult == squareFishOK) || req == 28 {
		// successful start and end first broadcast rc_player_status_all (10) to the room, before the reply.
		want := uint64(squareStatusFishing)
		if req == 28 {
			want = squareStatusIdle
		}
		sid, status := c.read()
		if sid != squareRcPlayerStatus || squareUint(t, status, 1) != aid || squareUint(t, status, 2) != want {
			t.Fatalf("request %d: status frame %d/%x, want %d aid=%d status=%d", req, sid, status, squareRcPlayerStatus, aid, want)
		}
	}
	id, reply := c.read()
	if id != wantID {
		t.Fatalf("request %d: reply msgid %d, want %d", req, id, wantID)
	}
	if got := squareUint(t, reply, 1); got != aid {
		t.Fatalf("reply Aid = %d, want %d", got, aid)
	}
	if got := squareUint(t, reply, 2); got != wantResult {
		t.Fatalf("reply Result = %d, want %d", got, wantResult)
	}
	return reply
}

func TestSquareFishingCastCredits(t *testing.T) {
	// Pinned outside the seasons: the exact balances below must not include an event quest credit.
	pinEventClock(t, time.Date(2026, time.October, 11, 12, 0, 0, 0, eventJST))
	const aidText = "9801"
	aid, err := strconv.ParseUint(aidText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	c := fishingRoomAccount(t, aidText, 0)
	// Entering Square pays the daily "Hang out in Square" quest (40 Gems), so count fishing from here.
	base := fishingBalance(t, aidText)

	// begin: the serial is echoed and RetryGem matches ActiveFishing().RetryGems (1250).
	begin := fishingReply(t, c, 27, pbVar(nil, 1, squareSpotSerial), 30, squareFishOK, aid)
	if squareUint(t, begin, 3) != squareSpotSerial || squareUint(t, begin, 4) != uint64(economy.ActiveFishing().RetryGems) {
		t.Fatalf("begin serial/retry = %d/%d", squareUint(t, begin, 3), squareUint(t, begin, 4))
	}

	// start: the timing values are fixed; the catch itself is chosen here and paid at finish.
	start := fishingReply(t, c, 29, pbVar(nil, 1, 0), 32, squareFishOK, aid)
	if squareUint(t, start, 3) != squareHitTimeMs || squareUint(t, start, 5) != squareDifficult {
		t.Fatalf("start HitTime/Difficult = %d/%d", squareUint(t, start, 3), squareUint(t, start, 5))
	}

	// finish(true) after a start: exactly one credit, equal to the Gem field.
	fin := fishingReply(t, c, 30, pbVar(nil, 1, 1), 33, squareFishOK, aid)
	gems := squareUint(t, fin, 4)
	if squareUint(t, fin, 3) != squareRewardFish || squareUint(t, fin, 15) != 1 || squareUint(t, fin, 6) == 0 || len(squareNested(t, fin, 7)) == 0 {
		t.Fatalf("finish card fields = %x", fin)
	}
	if gems < 5 || gems > 35 || (gems > 14 && gems < 22) {
		t.Fatalf("payout = %d outside the fish table", gems)
	}
	if got := fishingBalance(t, aidText); got != base+int64(gems) {
		t.Fatalf("balance after one catch = %d, want %d", got, base+int64(gems))
	}

	// a second finish(true) without a new start pays nothing.
	fishingReply(t, c, 30, pbVar(nil, 1, 1), 33, 0, aid)
	if got := fishingBalance(t, aidText); got != base+int64(gems) {
		t.Fatalf("balance after repeat finish = %d, want %d", got, base+int64(gems))
	}

	// start then finish(false): no credit, and the pending catch is dropped.
	fishingReply(t, c, 29, pbVar(nil, 1, 0), 32, squareFishOK, aid)
	fishingReply(t, c, 30, pbVar(nil, 1, 0), 33, 0, aid)
	fishingReply(t, c, 30, pbVar(nil, 1, 1), 33, 0, aid) // a hook with no pending start is not paid
	if got := fishingBalance(t, aidText); got != base+int64(gems) {
		t.Fatalf("balance after miss = %d, want %d", got, base+int64(gems))
	}

	// a begin clears a pending start, so the finish after it pays nothing.
	fishingReply(t, c, 29, pbVar(nil, 1, 0), 32, squareFishOK, aid)
	fishingReply(t, c, 27, pbVar(nil, 1, squareSpotSerial), 30, squareFishOK, aid)
	fishingReply(t, c, 30, pbVar(nil, 1, 1), 33, 0, aid)
	if got := fishingBalance(t, aidText); got != base+int64(gems) {
		t.Fatalf("balance after begin reset = %d, want %d", got, base+int64(gems))
	}

	// end: acknowledged with the success Result and no credit.
	end := fishingReply(t, c, 28, nil, 31, squareFishOK, aid)
	if !bytes.Equal(end, pbVar(pbVar(nil, 1, aid), 2, squareFishOK)) {
		t.Fatalf("end body = %x", end)
	}
}

func TestSquareFishingRareRetry(t *testing.T) {
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, eventJST)
	pinEventClock(t, now)
	const aidText = "9804"
	aid, err := strconv.ParseUint(aidText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	c := fishingRoomAccount(t, aidText, 2000)
	// Put account at daily Gem cap so retry + rare catch tests independence from daily cap.
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked(aidText)
	acc.Fishing = &store.FishingDay{Day: economy.AttendDayKey(now), Gems: economy.ActiveFishing().DailyCap}
	_ = store.SaveLocked(nil, acc)
	store.AccountsMu.Unlock()
	startBal := fishingBalance(t, aidText)

	cat := economy.FishCatalog()
	normalFish := cat[0] // 4101 Mackerel (not rare)
	rareFish := cat[25]  // 4126 Piranha (rare)

	// Missing a normal fish sets RetryPossible = 0 and omits TryCountResetCost.
	c.sess.fish = &squareFish{def: normalFish, active: true}
	missNormal := fishingReply(t, c, 30, pbVar(nil, 1, 0), 33, squareFishMiss, aid)
	if v, found := protobufUintField(missNormal, 23); found && v != 0 {
		t.Fatalf("normal miss RetryPossible = %d, want 0", v)
	}
	if _, found, _ := pbBytesField(missNormal, 31); found {
		t.Fatalf("normal miss unexpectedly included TryCountResetCost: %x", missNormal)
	}
	// Retry without a missed rare fish is refused.
	fishingReply(t, c, 29, pbVar(nil, 1, 1), 32, squareFishMiss, aid)

	// Missing a rare fish sets RetryPossible = 1 and TryCountResetCost = {CoinType: 1, CoinCount: 1250}.
	c.sess.fish = &squareFish{def: rareFish, active: true}
	missRare := fishingReply(t, c, 30, pbVar(nil, 1, 0), 33, squareFishMiss, aid)
	if squareUint(t, missRare, 23) != 1 {
		t.Fatalf("rare miss RetryPossible = %x, want 1", missRare)
	}
	cost := squareNested(t, missRare, 31)
	if squareUint(t, cost, 1) != 1 || squareUint(t, cost, 2) != 1250 {
		t.Fatalf("TryCountResetCost = %x, want {1, 1250}", cost)
	}

	// Retry charges 1250 Gems outside the daily cap and restarts the same rare fish.
	retryStart := fishingReply(t, c, 29, pbVar(nil, 1, 1), 32, squareFishOK, aid)
	if squareUint(t, retryStart, 4) != 1 {
		t.Fatalf("retry IsRare = %x, want 1", retryStart)
	}
	if got := fishingBalance(t, aidText); got != startBal-1250 {
		t.Fatalf("balance after retry = %d, want %d", got, startBal-1250)
	}
	finRetry := fishingReply(t, c, 30, pbVar(nil, 1, 1), 33, squareFishOK, aid)
	if squareUint(t, finRetry, 6) != uint64(rareFish.ID) {
		t.Fatalf("retry catch fish = %d, want %d", squareUint(t, finRetry, 6), rareFish.ID)
	}

	// Insufficient Gems refuses retry without charging.
	c.sess.fish = &squareFish{def: rareFish, active: true}
	fishingReply(t, c, 30, pbVar(nil, 1, 0), 33, squareFishMiss, aid)
	balBefore := fishingBalance(t, aidText) // < 1250
	fishingReply(t, c, 29, pbVar(nil, 1, 1), 32, squareFishMiss, aid)
	if got := fishingBalance(t, aidText); got != balBefore {
		t.Fatalf("balance changed on refused retry: %d != %d", got, balBefore)
	}
}

func TestSquareFishingBaitConsumeAndExhaust(t *testing.T) {
	pinEventClock(t, time.Date(2026, time.October, 11, 12, 0, 0, 0, eventJST))
	const aidText = "9805"
	aid, err := strconv.ParseUint(aidText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	c := fishingRoomAccount(t, aidText, 500)
	if st, _, _ := economy.BuyFishingItem(aidText, 9102); st != economy.FishBuyOK {
		t.Fatalf("buy B bait = %d", st)
	}
	// Set remaining uses to 2 so two casts exercise non-exhausted and exhausted transitions.
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked(aidText)
	acc.Gear.BaitUses[9102] = 2
	_ = store.SaveLocked(nil, acc)
	store.AccountsMu.Unlock()

	fishingReply(t, c, 29, pbVar(nil, 1, 0), 32, squareFishOK, aid)
	fin1 := fishingReply(t, c, 30, pbVar(nil, 1, 1), 33, squareFishOK, aid)
	if v, found := protobufUintField(fin1, 17); found && v != 0 {
		t.Fatalf("cast 1 BaitExaust = %d, want unset", v)
	}
	if _, inUse := economy.FishingBag(aidText, economy.FishKindBait); inUse != 9102 {
		t.Fatalf("bait in use after cast 1 = %d, want 9102", inUse)
	}

	fishingReply(t, c, 29, pbVar(nil, 1, 0), 32, squareFishOK, aid)
	fin2 := fishingReply(t, c, 30, pbVar(nil, 1, 0), 33, squareFishMiss, aid)
	if squareUint(t, fin2, 17) != 1 {
		t.Fatalf("cast 2 BaitExaust missing: %x", fin2)
	}
	if _, inUse := economy.FishingBag(aidText, economy.FishKindBait); inUse != 9101 {
		t.Fatalf("bait in use after exhaustion = %d, want 9101", inUse)
	}
	if bonus := economy.FishingGearBonus(aidText); bonus != 0 {
		t.Fatalf("gear bonus after exhaustion = %d, want 0", bonus)
	}
}

func TestSquareFishingRejectsOutsideRoom(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"fish-out": {AccessToken: "fish-out", SessionKey: "sk", Aid: "9802", Name: "Outside"}})
	c := newSqClient(t, &squareRoom{}, 2)
	c.send(0, loginBody(9802)) // logged in, but never entered Square
	c.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	c.send(27, pbVar(nil, 1, squareSpotSerial))
	c.none() // no reply before relaystart, like every other room request
}
