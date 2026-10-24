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
// the hearts sent today (2 of 5 here) and is not claimable.
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
	if vars[10] != 2 || vars[11] != 5 || vars[13] != 1 {
		t.Fatalf("today/max/tutorial = %d/%d/%d, want 2/5/1", vars[10], vars[11], vars[13])
	}
	if want := uint64(economy.QuestResetMillis(now)); vars[12] != want {
		t.Fatalf("reset = %d, want %d", vars[12], want)
	}
	want := map[uint64]struct{ status, current, complete uint64 }{
		9001: {2, 1, 1}, // claimed
		9002: {1, 0, 1}, // in progress
		9003: {2, 1, 1}, // claimed
		9004: {1, 0, 1},
		9005: {1, 2, uint64(economy.HeartBase())}, // hearts: 2 of 5 sent
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
		if rv[1] != gardenQuestGemView || rv[3] != uint64(def.Gems) {
			t.Fatalf("quest %d reward view/count = %d/%d, want %d/%d", def.ID, rv[1], rv[3], gardenQuestGemView, def.Gems)
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

	// Gem reward shape for a successful claim: RewardType 11 and one coin row.
	rv, rm := pbFields(t, gardenQuestReward(30))
	item, _ := pbFields(t, getBytes(t, rm[6]))
	if rv[1] != gardenQuestRewardType || item[1] != gardenQuestGemView || item[4] != 30 {
		t.Fatalf("reward = type %d item %v, want type %d coin %d count 30", rv[1], item, gardenQuestRewardType, gardenQuestGemView)
	}

	// Info for an unknown id still carries QuestInfo (required) with error 1.
	msgid, body = gardenQuestReply(16, 9711, pbVar(nil, 1, 1234), now, logger)
	vars, msgs = pbFields(t, body)
	if msgid != 22 || vars[2] != 1 || len(msgs[3]) != 1 {
		t.Fatalf("unknown info reply = msgid %d vars %v infos %d", msgid, vars, len(msgs[3]))
	}

	// Accept and reject echo the id with error 0 and change nothing.
	for _, tc := range []struct {
		req, reply uint16
	}{{17, 24}, {19, 26}} {
		msgid, body = gardenQuestReply(tc.req, 9711, pbVar(nil, 1, 9002), now, logger)
		vars, _ = pbFields(t, body)
		if msgid != tc.reply || vars[1] != 9002 || vars[2] != 0 {
			t.Fatalf("req %d reply = msgid %d vars %v", tc.req, msgid, vars)
		}
	}

	// The hearts row is display-only: five hearts show it claimed (status 2, 5 of 5), and a claim
	// is refused with no reward row, because HeartSend already paid the 30 Gems.
	for range uint64(economy.HeartBase()) {
		economy.HeartSend("9711", store.FriendAID, now)
	}
	msgid, body = gardenQuestReply(16, 9711, pbVar(nil, 1, uint64(economy.HeartQuest().ID)), now, logger)
	vars, msgs = pbFields(t, body)
	if msgid != 22 || vars[2] != 0 || len(msgs[3]) != 1 {
		t.Fatalf("hearts info reply = msgid %d vars %v", msgid, vars)
	}
	iv, im := pbFields(t, getBytes(t, msgs[3]))
	dv, _ := pbFields(t, getBytes(t, im[5]))
	if iv[7] != 2 || dv[3] != uint64(economy.HeartBase()) || dv[4] != uint64(economy.HeartBase()) {
		t.Fatalf("hearts row status/current/complete = %d/%d/%d, want 2/5/5", iv[7], dv[3], dv[4])
	}
	msgid, body = gardenQuestReply(18, 9711, pbVar(nil, 1, uint64(economy.HeartQuest().ID)), now, logger)
	vars, msgs = pbFields(t, body)
	if msgid != 25 || vars[2] != 1 || len(msgs[4]) != 0 {
		t.Fatalf("hearts claim reply = msgid %d vars %v rewards %d, want refused", msgid, vars, len(msgs[4]))
	}
	store.AccountsMu.Lock()
	gems := store.AccountByAidLocked("9711").Gems
	store.AccountsMu.Unlock()
	if gems != economy.HeartRewardGems() {
		t.Fatalf("gems after hearts claim = %d, want %d (credited once by HeartSend)", gems, economy.HeartRewardGems())
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
