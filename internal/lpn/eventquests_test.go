package lpn

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// The event quest tests pin economy.NowFunc: the triggers read it, and the window depends on the date.

var eventJST = time.FixedZone("JST", 9*3600)

// pinEventClock fixes economy.NowFunc (the clock the triggers read) for one test.
func pinEventClock(t *testing.T, now time.Time) {
	t.Helper()
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })
}

// questRowIDs returns the QuestId of every row of a hc_quest_list_res body, in order.
func questRowIDs(t *testing.T, body []byte) []uint64 {
	t.Helper()
	_, msgs := pbFields(t, body)
	var ids []uint64
	for _, row := range msgs[2] {
		v, _ := pbFields(t, row)
		ids = append(ids, v[1])
	}
	return ids
}

// TestGardenQuestPanelEventRows: the QUEST panel lists the halloween event quests only inside the
// window, counts them in MaxTodayQuestAmount, and answers an info request only for a listed quest.
func TestGardenQuestPanelEventRows(t *testing.T) {
	gatherAccount(t, "9731", nil)
	daily := []uint64{9001, 9002, 9003, 9004, 9005}
	halloween := []uint64{9101, 9102, 9103}
	before := time.Date(2026, time.October, 24, 12, 0, 0, 0, eventJST)
	during := time.Date(2026, time.October, 26, 12, 0, 0, 0, eventJST)

	vars, _ := pbFields(t, gardenQuestList("9731", before))
	if vars[11] != 5 || !slices.Equal(questRowIDs(t, gardenQuestList("9731", before)), daily) {
		t.Fatalf("panel before the window = max %d rows %v", vars[11], questRowIDs(t, gardenQuestList("9731", before)))
	}
	rows := slices.Concat(daily, halloween)
	vars, msgs := pbFields(t, gardenQuestList("9731", during))
	if vars[11] != 8 || !slices.Equal(questRowIDs(t, gardenQuestList("9731", during)), rows) {
		t.Fatalf("panel in the window = max %d rows %v, want %v", vars[11], questRowIDs(t, gardenQuestList("9731", during)), rows)
	}
	if _, row := pbFields(t, msgs[2][5]); string(getBytes(t, row[2])) != "Halloween: Gather 3 Garden materials" {
		t.Fatalf("first event row name = %q", getBytes(t, row[2]))
	}

	msgid, body := gardenQuestReply(16, 9731, pbVar(nil, 1, 9101), before, quietLogger())
	if vars, _ := pbFields(t, body); msgid != 22 || vars[2] != 1 {
		t.Fatalf("info for an event quest outside its window = msgid %d vars %v, want error 1", msgid, vars)
	}
	msgid, body = gardenQuestReply(16, 9731, pbVar(nil, 1, 9101), during, quietLogger())
	if vars, _ := pbFields(t, body); msgid != 22 || vars[2] != 0 {
		t.Fatalf("info for an event quest in its window = msgid %d vars %v, want error 0", msgid, vars)
	}
}

// gatherOnce plays one successful essence gather (begin, two touches, end) in aid's own Garden.
func gatherOnce(t *testing.T, aid uint64, now time.Time) {
	t.Helper()
	g := &gardenGather{}
	logger := quietLogger()
	gardenGatherReply(12, aid, aid, gatherTouch(3, 101, 41, true), g, now, logger)
	gardenGatherReply(11, aid, aid, gatherTouch(3, 101, 41, true), g, now, logger)
	gardenGatherReply(11, aid, aid, gatherTouch(3, 101, 41, true), g, now, logger)
	msgs := gardenGatherReply(13, aid, aid, gatherTouch(0, 0, 41, false), g, now, logger)
	if len(msgs) == 0 || pbFieldsOf(t, msgs[0].body).vars[1] != 0 {
		t.Fatalf("gather end = %+v, want a success", msgs)
	}
}

// TestGatherThirdCreditsGather3: the gather3 quest completes on the third successful gather of the game
// day and not before; the Gems credit once.
func TestGatherThirdCreditsGather3(t *testing.T) {
	gatherAccount(t, "9732", nil)
	now := time.Date(2026, time.October, 26, 12, 0, 0, 0, eventJST)
	pinEventClock(t, now)
	const aid = 9732
	gatherOnce(t, aid, now)
	gatherOnce(t, aid, now)
	if done := economy.QuestProgress("9732", now).Done; slices.Contains(done, 9101) {
		t.Fatalf("gather3 credited after two gathers: %v", done)
	}
	gatherOnce(t, aid, now)
	if done := economy.QuestProgress("9732", now).Done; !slices.Contains(done, 9101) {
		t.Fatalf("gather3 not credited after three gathers: %v", done)
	}
	g := economy.ActiveGathering()
	if gems, _, _ := gatherGems(t, "9732"); gems != min(3*g.Gems, g.GemsCap)+15 {
		t.Fatalf("gems after the third gather = %d, want the gather bonus plus the quest's 15", gems)
	}
}

// TestAtelierMakeCreditsAtelierCraft: a successful material make completes atelier-craft (9102) once.
func TestAtelierMakeCreditsAtelierCraft(t *testing.T) {
	requireCraftArchive(t)
	now := time.Date(2026, time.October, 26, 12, 0, 0, 0, eventJST)
	pinEventClock(t, now)
	const viewer = 9733
	craftAccount(t, "9733", 15, []store.GardenBagStack{{Item: 110000, Count: 3}, {Item: 210000, Count: 1}, {Item: 490000, Count: 2}, {Item: 490001, Count: 1}})
	craftFirst(t, craftCall(t, viewer, 33, pbVar(pbVar(nil, 1, 101201), 2, 1)), 38)
	if done := economy.QuestProgress("9733", now).Done; !slices.Equal(done, []int64{9102}) {
		t.Fatalf("progress after one make = %v, want [9102]", done)
	}
	if gems, _, _ := gatherGems(t, "9733"); gems != 15 {
		t.Fatalf("gems after one make = %d, want 15", gems)
	}
}

// TestSquareCatchCreditsSquareFish: a hooked Square catch in the halloween window completes square-fish
// (9103), on top of the catch's own Gems.
func TestSquareCatchCreditsSquareFish(t *testing.T) {
	now := time.Date(2026, time.October, 26, 12, 0, 0, 0, eventJST)
	pinEventClock(t, now)
	const aidText = "9734"
	aid, err := strconv.ParseUint(aidText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	c := fishingRoomAccount(t, aidText, 0)
	base := fishingBalance(t, aidText) // 40 from entering Square
	fishingReply(t, c, 27, pbVar(nil, 1, squareSpotSerial), 30, squareFishOK, aid)
	fishingReply(t, c, 29, pbVar(nil, 1, 0), 32, squareFishOK, aid)
	fin := fishingReply(t, c, 30, pbVar(nil, 1, 1), 33, squareFishOK, aid)
	gems := squareUint(t, fin, 4)
	if got := fishingBalance(t, aidText); got != base+int64(gems)+15 {
		t.Fatalf("balance after one hooked catch = %d, want %d (catch %d + quest 15)", got, base+int64(gems)+15, gems)
	}
	if done := economy.QuestProgress(aidText, now).Done; !slices.Contains(done, 9103) {
		t.Fatalf("square-fish not credited: %v", done)
	}
}
