package lpn

import (
	"io"
	"log"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// pbFields splits one protobuf message into varint fields and length-delimited children.
func pbFields(t *testing.T, data []byte) (vars map[uint64]uint64, msgs map[uint64][][]byte) {
	t.Helper()
	vars, msgs = map[uint64]uint64{}, map[uint64][][]byte{}
	if !pbScan(data, func(f, v uint64, d []byte) {
		if d != nil {
			msgs[f] = append(msgs[f], d)
		} else {
			vars[f] = v
		}
	}) {
		t.Fatalf("malformed protobuf %x", data)
	}
	return vars, msgs
}

// TestGardenQuestListEncoding checks hc_quest_list_res field numbers, the required error_code,
// and each row's status, progress and Gem reward (quests.md section 10). The hearts row shows
// the hearts sent today (2 of the quest count here) and is not claimable.
func TestGardenQuestListEncoding(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"qlist": {AccessToken: "qlist", SessionKey: "sk", Aid: "9716", Name: "List"}})
	testutil.SetLab(t, "9716", false)
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	economy.MarkQuest("9716", "garden-own", now) // completion credits and claims at once
	economy.MarkQuest("9716", "square", now)
	for range 2 {
		economy.HeartSend("9716", store.FriendAID, now)
	}
	vars, msgs := pbFields(t, gardenQuestList("9716", now))
	if v, ok := vars[1]; !ok || v != 0 {
		t.Fatalf("error_code = %d present=%v, want present 0", v, ok)
	}
	if len(msgs[2]) != len(economy.QuestCatalog(now)) {
		t.Fatalf("regular quests = %d, want %d", len(msgs[2]), len(economy.QuestCatalog(now)))
	}
	if vars[10] != 0 || vars[11] != uint64(economy.ActiveRequestRules().MaxDaily) || vars[13] != 1 {
		t.Fatalf("today/max/tutorial = %d/%d/%d, want 0/%d/1", vars[10], vars[11], vars[13], economy.ActiveRequestRules().MaxDaily)
	}
	if vars[5] != 3 || vars[6] != 3 || vars[7] != gardenQuestCallGem || vars[8] != 10 || vars[9] != 0 {
		t.Fatalf("call fields = free %d/%d type %d price %d next %d", vars[5], vars[6], vars[7], vars[8], vars[9])
	}
	if len(msgs[3]) != 3 {
		t.Fatalf("repeat_quest_info = %d, want 3", len(msgs[3]))
	}
	// 9402 ("square") got progress from MarkQuest("9716", "square", now) above -> status 3.
	r2v, r2m := pbFields(t, msgs[3][1])
	r2d, _ := pbFields(t, getBytes(t, r2m[5]))
	if r2v[1] != 9402 || r2v[7] != 3 || r2d[3] != 1 || r2d[4] != 1 {
		t.Fatalf("repeat 9402 = id %d status %d cur/comp %d/%d", r2v[1], r2v[7], r2d[3], r2d[4])
	}
	// Episode: 9501 unlocked (status 3 from "garden-own"), 9502 locked with quest_prerequire.
	if len(msgs[4]) != 2 {
		t.Fatalf("story_quest_info = %d, want 2", len(msgs[4]))
	}
	e1v, _ := pbFields(t, msgs[4][0])
	if e1v[1] != 9501 || e1v[7] != 3 {
		t.Fatalf("episode 1 = id %d status %d, want 9501/3", e1v[1], e1v[7])
	}
	e2v, e2m := pbFields(t, msgs[4][1])
	preV, preM := pbFields(t, getBytes(t, e2m[3]))
	if e2v[1] != 9502 || string(getBytes(t, preM[1])) != "Complete: Episode 1: Home Sweet Garden" || preV[2] != 0 || preV[3] != 1 {
		t.Fatalf("episode 2 prereq = id %d title %q cur/max %d/%d", e2v[1], preM[1], preV[2], preV[3])
	}
	if want := uint64(economy.QuestResetMillis(now)); vars[12] != want {
		t.Fatalf("reset = %d, want %d", vars[12], want)
	}
	want := map[uint64]struct{ status, current, complete uint64 }{
		9001: {2, 1, 1}, // claimed
		9002: {1, 0, 1}, // in progress
		9003: {2, 1, 1}, // claimed
		9004: {1, 0, 1},
		9005: {1, 2, uint64(economy.HeartQuestCount())}, // hearts: 2 of the quest count sent
	}
	for i, def := range economy.QuestCatalog(now) {
		qv, qm := pbFields(t, msgs[2][i])
		if qv[1] != uint64(def.ID) || string(getBytes(t, qm[2])) != def.Name {
			t.Fatalf("row %d id/name = %d/%q, want %d/%q", i, qv[1], getBytes(t, qm[2]), def.ID, def.Name)
		}
		if qv[7] != want[uint64(def.ID)].status {
			t.Fatalf("quest %d status = %d, want %d", def.ID, qv[7], want[uint64(def.ID)].status)
		}
		dv, _ := pbFields(t, getBytes(t, qm[5]))
		if dv[3] != want[uint64(def.ID)].current || dv[4] != want[uint64(def.ID)].complete {
			t.Fatalf("quest %d detail current/complete = %d/%d", def.ID, dv[3], dv[4])
		}
		rv, _ := pbFields(t, getBytes(t, qm[6]))
		wantView, wantCount := uint64(gardenQuestGemView), uint64(def.Gems)
		if def.ID == 9005 {
			wantView, wantCount = gardenQuestCashView, 600
		}
		if rv[1] != wantView || rv[3] != wantCount {
			t.Fatalf("quest %d reward view/count = %d/%d, want %d/%d", def.ID, rv[1], rv[3], wantView, wantCount)
		}
	}
}

// getBytes returns the single length-delimited child that the caller expects to be present.
func getBytes(t *testing.T, list [][]byte) []byte {
	t.Helper()
	if len(list) != 1 {
		t.Fatalf("child count = %d, want 1", len(list))
	}
	return list[0]
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// TestGardenQuestCompleteAndInfoReplies covers the required fields of the claim, info and
// accept replies: an unknown id gets error 1, and a QuestInfo that is still present.
func TestGardenQuestCompleteAndInfoReplies(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"qlpn": {AccessToken: "qlpn", SessionKey: "sk", Aid: "9711", Name: "Lpn"}})
	testutil.SetLab(t, "9711", false)
	now := time.Now()
	logger := quietLogger()

	// Unknown id: error 1, no reward row.
	msgid, body := gardenQuestReply(18, 9711, pbVar(nil, 1, 1234), now, logger)
	vars, msgs := pbFields(t, body)
	if msgid != 25 || vars[1] != 1234 || vars[2] != 1 || len(msgs[4]) != 0 {
		t.Fatalf("unknown complete reply = msgid %d vars %v rewards %d", msgid, vars, len(msgs[4]))
	}

	// Gem reward shape for a successful claim: RewardType 11 and coin rows.
	rv, rm := pbFields(t, gardenQuestReward(30, 0))
	item, _ := pbFields(t, getBytes(t, rm[6]))
	if rv[1] != gardenQuestRewardType || item[1] != gardenQuestGemView || item[4] != 30 {
		t.Fatalf("reward = type %d item %v, want type %d coin %d count 30", rv[1], item, gardenQuestRewardType, gardenQuestGemView)
	}
	_, rmBoth := pbFields(t, gardenQuestReward(10, 50))
	if len(rmBoth[6]) != 2 {
		t.Fatalf("both gem+cash reward items = %d, want 2", len(rmBoth[6]))
	}

	// Info for an unknown id still carries QuestInfo (required) with error 1.
	msgid, body = gardenQuestReply(16, 9711, pbVar(nil, 1, 1234), now, logger)
	vars, msgs = pbFields(t, body)
	if msgid != 22 || vars[2] != 1 || len(msgs[3]) != 1 {
		t.Fatalf("unknown info reply = msgid %d vars %v infos %d", msgid, vars, len(msgs[3]))
	}

	// Accept and reject on an active Request (9401) succeed; rejecting an Episode (9501) fails.
	for _, tc := range []struct {
		req, reply uint16
	}{{17, 24}, {19, 26}} {
		msgid, body = gardenQuestReply(tc.req, 9711, pbVar(nil, 1, 9401), now, logger)
		vars, _ = pbFields(t, body)
		if msgid != tc.reply || vars[1] != 9401 || vars[2] != 0 {
			t.Fatalf("req %d reply = msgid %d vars %v", tc.req, msgid, vars)
		}
	}
	msgid, body = gardenQuestReply(19, 9711, pbVar(nil, 1, 9501), now, logger)
	if vars, _ = pbFields(t, body); msgid != 26 || vars[2] != 1 {
		t.Fatalf("reject episode reply = msgid %d vars %v, want error 1", msgid, vars)
	}

	// The hearts row is display-only: the quest count of hearts shows it claimed (status 2, the quest
	// count sent), and a claim is refused with no reward row, because HeartSend already paid the 600 Cash.
	for range uint64(economy.HeartQuestCount()) {
		economy.HeartSend("9711", store.FriendAID, now)
	}
	msgid, body = gardenQuestReply(16, 9711, pbVar(nil, 1, uint64(economy.HeartQuest().ID)), now, logger)
	vars, msgs = pbFields(t, body)
	if msgid != 22 || vars[2] != 0 || len(msgs[3]) != 1 {
		t.Fatalf("hearts info reply = msgid %d vars %v", msgid, vars)
	}
	iv, im := pbFields(t, getBytes(t, msgs[3]))
	dv, _ := pbFields(t, getBytes(t, im[5]))
	if q := uint64(economy.HeartQuestCount()); iv[7] != 2 || dv[3] != q || dv[4] != q {
		t.Fatalf("hearts row status/current/complete = %d/%d/%d, want 2/%d/%d", iv[7], dv[3], dv[4], q, q)
	}
	rv, _ = pbFields(t, getBytes(t, im[6]))
	if rv[1] != gardenQuestCashView || rv[3] != 600 {
		t.Fatalf("hearts row reward view/count = %d/%d, want %d/600", rv[1], rv[3], gardenQuestCashView)
	}
	msgid, body = gardenQuestReply(18, 9711, pbVar(nil, 1, uint64(economy.HeartQuest().ID)), now, logger)
	vars, msgs = pbFields(t, body)
	if msgid != 25 || vars[2] != 1 || len(msgs[4]) != 0 {
		t.Fatalf("hearts claim reply = msgid %d vars %v rewards %d, want refused", msgid, vars, len(msgs[4]))
	}
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("9711")
	cash, gems := acc.Cash, acc.Gems
	store.AccountsMu.Unlock()
	if cash != economy.HeartRewardCash() || gems != 0 {
		t.Fatalf("balances after hearts claim = cash %d gems %d, want %d/0 (credited once by HeartSend)", cash, gems, economy.HeartRewardCash())
	}
}

// TestGardenVisitKeys checks that a room-enter marks the own-Garden quest for the viewer's
// own Garden and the visit quest for anyone else's.
func TestGardenVisitKeys(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"qvis": {AccessToken: "qvis", SessionKey: "sk", Aid: "9712", Name: "Visit"}})
	testutil.SetLab(t, "9712", false)
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })

	gardenQuestVisit(9712, 9712)
	gardenQuestVisit(9712, 1001)
	MarkQuest(9712, "square")
	got := economy.QuestProgress("9712", now).Done
	if len(got) != 3 || got[0] != 9001 || got[1] != 9002 || got[2] != 9003 {
		t.Fatalf("progress after own, visit and square = %v, want [9001 9002 9003]", got)
	}
	gardenQuestVisit(0, 1001) // no viewer aid yet: nothing marked
	// Each completion queues one notice for the next relay-start; taking them clears the queue.
	if notes := takeQuestNotices(9712); len(notes) != 3 || notes[2].ID != 9003 {
		t.Fatalf("queued notices = %+v, want 9001 9002 9003", notes)
	}
	MarkQuest(9712, "square") // already done today: no new notice
	if notes := takeQuestNotices(9712); len(notes) != 0 {
		t.Fatalf("notices after repeat = %+v, want none", notes)
	}
}

// TestGardenQuestNPCCallAndTabsLPN covers msgids 15..22 wire shapes for Requests & Episodes.
func TestGardenQuestNPCCallAndTabsLPN(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"qnpc": {AccessToken: "qnpc", SessionKey: "sk", Aid: "9717", Name: "Npc", Gems: 100}})
	testutil.SetLab(t, "9717", false)
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	logger := quietLogger()

	if !gardenQuestMsg(15) || !gardenQuestMsg(20) || !gardenQuestMsg(22) || gardenQuestMsg(14) || gardenQuestMsg(23) {
		t.Fatal("gardenQuestMsg range wrong")
	}

	// Seed list (3 active: 9401..9403).
	gardenQuestReply(15, 9717, nil, now, logger)

	// Calling while 3 are active returns 199428.
	msgid, body := gardenQuestReply(22, 9717, pbVar(pbVar(nil, 1, 9404), 2, 0), now, logger)
	vars, _ := pbFields(t, body)
	if msgid != 28 || vars[1] != 9404 || vars[2] != 199428 {
		t.Fatalf("call while full = msgid %d vars %v, want 28/199428", msgid, vars)
	}

	// Pick NPC (20 -> 27): returns 9404, 9405, 9406 with refresh_type=1, refresh_price=10.
	msgid, body = gardenQuestReply(20, 9717, pbVar(nil, 1, 0), now, logger)
	vars, msgs := pbFields(t, body)
	if msgid != 27 || vars[5] != 0 || vars[2] != 1 || vars[3] != 10 || len(msgs[1]) != 3 {
		t.Fatalf("pick npc = msgid %d vars %v count %d", msgid, vars, len(msgs[1]))
	}

	// Info pick NPC (21 -> 23): valid and invalid id.
	msgid, body = gardenQuestReply(21, 9717, pbVar(nil, 1, 9404), now, logger)
	vars, msgs = pbFields(t, body)
	if msgid != 23 || vars[1] != 9404 || vars[2] != 0 || len(msgs[3]) != 1 || vars[4] != 3 || vars[5] != 3 || vars[6] != 1 || vars[7] != 10 {
		t.Fatalf("info pick npc = msgid %d vars %v infos %d", msgid, vars, len(msgs[3]))
	}
	msgid, body = gardenQuestReply(21, 9717, pbVar(nil, 1, 9999), now, logger)
	vars, msgs = pbFields(t, body)
	if msgid != 23 || vars[2] != 1 || len(msgs[3]) != 1 {
		t.Fatalf("invalid info pick npc = msgid %d vars %v infos %d", msgid, vars, len(msgs[3]))
	}

	// Reject 9401, then invite 9404 for free (call_type=0).
	gardenQuestReply(19, 9717, pbVar(nil, 1, 9401), now, logger)
	msgid, body = gardenQuestReply(22, 9717, pbVar(pbVar(nil, 1, 9404), 2, 0), now, logger)
	if vars, _ = pbFields(t, body); msgid != 28 || vars[1] != 9404 || vars[2] != 0 {
		t.Fatalf("free call npc = msgid %d vars %v", msgid, vars)
	}

	// Complete garden-own -> unlocks claim on Episode 9501.
	economy.MarkQuest("9717", "garden-own", now)
	msgid, body = gardenQuestReply(18, 9717, pbVar(nil, 1, 9501), now, logger)
	vars, msgs = pbFields(t, body)
	if msgid != 25 || vars[1] != 9501 || vars[2] != 0 || len(msgs[4]) != 1 {
		t.Fatalf("complete episode 9501 = msgid %d vars %v rewards %d", msgid, vars, len(msgs[4]))
	}
}
