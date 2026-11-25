package economy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// questAccount installs non-lab account aid (no store file unless questStore is called).
func questAccount(t *testing.T, aid string) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"qtok-" + aid: {AccessToken: "qtok-" + aid, SessionKey: "sk", Aid: aid, Name: "Quest"}})
	testutil.SetLab(t, aid, false)
}

// questStore points the account store at a temp dir so completions save and write ledger lines.
func questStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testutil.UseDatabase(t)
	return dir
}

func jstAt(day, hour, min int) time.Time {
	return time.Date(2026, time.October, day, hour, min, 0, 0, jst)
}

func questGems(t *testing.T, aid string) int64 {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return acc.Gems
}

func TestQuestCatalogTotalsOneHundredFifty(t *testing.T) {
	now := jstAt(11, 12, 0) // no season: the daily rows only
	total := int64(0)
	for _, def := range QuestCatalog(now) {
		if !def.Display { // the hearts row's Cash reward is HeartSend's, not the quest machinery's
			total += def.Gems
		}
	}
	if total != 150 || len(QuestCatalog(now)) != 5 {
		t.Fatalf("catalog = %d quests / %d Gems, want 5 / 150 credited here", len(QuestCatalog(now)), total)
	}
}

// TestQuestProgressCompletesEachTriggerOnce: each trigger completes its quest once a day,
// credits its Gems once, and the day's total is 150.
func TestQuestProgressCompletesEachTriggerOnce(t *testing.T) {
	questAccount(t, "9701")
	now := jstAt(11, 12, 0)
	completed := 0
	for _, def := range QuestCatalog(now) {
		if def.Display {
			continue
		}
		if got, done := MarkQuest("9701", def.Key, now); done && got.ID == def.ID {
			completed++
		}
		if _, done := MarkQuest("9701", def.Key, now); done { // repeat the same day: no second credit
			t.Fatalf("repeat completion of %d reported", def.ID)
		}
	}
	if completed != 4 {
		t.Fatalf("completions = %d, want 4", completed)
	}
	if _, done := MarkQuest("9701", "unknown-key", now); done { // unknown key changes nothing
		t.Fatal("unknown key completed a quest")
	}
	if _, done := MarkQuest("9999", "diary", now); done { // unknown aid changes nothing
		t.Fatal("unknown aid completed a quest")
	}
	rec := QuestProgress("9701", now)
	if !slices.Equal(rec.Done, []int64{9001, 9002, 9003, 9004}) {
		t.Fatalf("progress done = %v", rec.Done)
	}
	if got := questGems(t, "9701"); got != 150 {
		t.Fatalf("gems after all triggers = %d, want 150", got)
	}
	if got := QuestProgress("9999", now); len(got.Done) != 0 {
		t.Fatalf("unknown aid progress = %v", got.Done)
	}
}

// TestQuestCompletionCreditsOnceAndWritesOneLedgerLine: the first completion credits 30 Gems
// with one ledger line; a second completion and a claim afterwards credit nothing.
func TestQuestCompletionCreditsOnceAndWritesOneLedgerLine(t *testing.T) {
	questAccount(t, "9701")
	dir := questStore(t)
	now := jstAt(11, 12, 0)
	if def, done := MarkQuest("9701", "garden-own", now); !done || def.ID != 9001 {
		t.Fatalf("first completion = %+v/%v, want 9001/true", def, done)
	}
	if _, done := MarkQuest("9701", "garden-own", now); done {
		t.Fatal("second completion reported")
	}
	if gems, code := ClaimQuest("9701", 9001, now); gems != 0 || code != questErrRejected {
		t.Fatalf("claim after auto-credit = %d/%d, want 0/%d", gems, code, questErrRejected)
	}
	if got := questGems(t, "9701"); got != 30 {
		t.Fatalf("gems = %d, want 30", got)
	}
	lines := gachaLedger(t, dir)
	if len(lines) != 1 || lines[0].Delta != 30 || lines[0].Reason != "quest:9001:20261011" {
		t.Fatalf("ledger = %+v, want one quest:9001:20261011 +30 line", lines)
	}
	if rec := QuestProgress("9701", now); !slices.Equal(rec.Claimed, []int64{9001}) {
		t.Fatalf("claimed = %v", rec.Claimed)
	}
}

// TestQuestClaimLegacyDoneRecord: a record done but not credited (written before auto-credit)
// is paid once by ClaimQuest, and a second claim is refused.
func TestQuestClaimLegacyDoneRecord(t *testing.T) {
	questAccount(t, "9701")
	dir := questStore(t)
	now := jstAt(11, 12, 0)
	store.AccountsMu.Lock()
	store.AccountByAidLocked("9701").Quests = &store.QuestDay{Day: AttendDayKey(now), Done: []int64{9002}}
	store.AccountsMu.Unlock()
	if gems, code := ClaimQuest("9701", 9002, now); gems != 40 || code != 0 {
		t.Fatalf("legacy claim = %d/%d, want 40/0", gems, code)
	}
	if gems, code := ClaimQuest("9701", 9002, now); gems != 0 || code != questErrRejected {
		t.Fatalf("second legacy claim = %d/%d, want refused", gems, code)
	}
	if got := questGems(t, "9701"); got != 40 {
		t.Fatalf("gems = %d, want 40", got)
	}
	if lines := gachaLedger(t, dir); len(lines) != 1 || lines[0].Reason != "quest:9002:20261011" {
		t.Fatalf("ledger = %+v", lines)
	}
}

// TestQuestDayRolloverAtSixJST: progress belongs to the game day, so a completion at 05:59
// does not carry over the 06:00 boundary, and the new day credits again.
func TestQuestDayRolloverAtSixJST(t *testing.T) {
	questAccount(t, "9701")
	dir := questStore(t)
	if _, done := MarkQuest("9701", "garden-own", jstAt(11, 5, 59)); !done { // still game day 10-10
		t.Fatal("completion at 05:59 not reported")
	}
	if rec := QuestProgress("9701", jstAt(11, 6, 0)); len(rec.Done) != 0 {
		t.Fatalf("progress carried over the 06:00 boundary: %v", rec.Done)
	}
	if _, done := MarkQuest("9701", "garden-own", jstAt(11, 6, 0)); !done {
		t.Fatal("new game day did not complete again")
	}
	if rec := QuestProgress("9701", jstAt(12, 6, 0)); len(rec.Done) != 0 || len(rec.Claimed) != 0 {
		t.Fatalf("new day not empty: %+v", rec)
	}
	if got := questGems(t, "9701"); got != 60 {
		t.Fatalf("gems = %d, want 60 (one credit per game day)", got)
	}
	lines := gachaLedger(t, dir)
	if len(lines) != 2 || lines[0].Reason != "quest:9001:20261010" || lines[1].Reason != "quest:9001:20261011" {
		t.Fatalf("ledger = %+v, want 10-10 and 10-11 lines", lines)
	}
	want := time.Date(2026, time.October, 12, 6, 0, 0, 0, jst).UnixMilli()
	if got := QuestResetMillis(jstAt(11, 12, 0)); got != want {
		t.Fatalf("reset = %d, want %d", got, want)
	}
}

// TestQuestClaimRejectsUnknownAndNotDone: unknown ids and quests not done today credit nothing.
func TestQuestClaimRejectsUnknownAndNotDone(t *testing.T) {
	questAccount(t, "9701")
	dir := questStore(t)
	now := jstAt(11, 12, 0)
	MarkQuest("9701", "garden-own", now)
	if gems, code := ClaimQuest("9701", 1234, now); gems != 0 || code == 0 {
		t.Fatalf("unknown id claim = %d/%d", gems, code)
	}
	if gems, code := ClaimQuest("9701", 9002, now); gems != 0 || code == 0 {
		t.Fatalf("not-done claim = %d/%d", gems, code)
	}
	if got := questGems(t, "9701"); got != 30 {
		t.Fatalf("gems = %d, want 30 from the one completion", got)
	}
	if lines := gachaLedger(t, dir); len(lines) != 1 {
		t.Fatalf("ledger lines = %+v, want one", lines)
	}
}

// TestQuestMarkCreditsLegacyDoneRecord: a quest recorded done before completions credited
// (2026-10-11 first deploy) is credited by the next trigger, once.
func TestQuestMarkCreditsLegacyDoneRecord(t *testing.T) {
	questAccount(t, "9702")
	dir := questStore(t)
	now := jstAt(11, 12, 0)
	store.AccountsMu.Lock()
	store.AccountByAidLocked("9702").Quests = &store.QuestDay{Day: AttendDayKey(now), Done: []int64{9001}}
	store.AccountsMu.Unlock()
	if _, ok := MarkQuest("9702", "garden-own", now); !ok {
		t.Fatal("legacy done quest not credited")
	}
	if _, ok := MarkQuest("9702", "garden-own", now); ok {
		t.Fatal("second mark credited again")
	}
	rec := QuestProgress("9702", now)
	if got := questGems(t, "9702"); got != 30 || len(rec.Done) != 1 || len(rec.Claimed) != 1 {
		t.Fatalf("gems=%d rec=%+v, want 30 and one done/claimed", got, rec)
	}
	if lines := gachaLedger(t, dir); len(lines) != 1 || lines[0].Reason != "quest:9001:20261011" {
		t.Fatalf("ledger = %+v", lines)
	}
}

// TestHeartRowNeverPaysThroughQuests: the hearts row is display-only. MarkQuest and ClaimQuest
// refuse it, and the hearts sent to the quest count credit its Cash once, as one heart:<day> ledger line.
func TestHeartRowNeverPaysThroughQuests(t *testing.T) {
	questAccount(t, "9701")
	dir := questStore(t)
	now := jstAt(11, 12, 0)
	if _, done := MarkQuest("9701", "", now); done {
		t.Fatal("empty key completed the hearts row")
	}
	if gems, code := ClaimQuest("9701", HeartQuest().ID, now); gems != 0 || code != questErrRejected {
		t.Fatalf("claim before hearts = %d/%d, want refused", gems, code)
	}
	sendHearts(t, "9701", store.FriendAID, int(HeartBase()), now)
	if gems, code := ClaimQuest("9701", HeartQuest().ID, now); gems != 0 || code != questErrRejected {
		t.Fatalf("claim after hearts = %d/%d, want refused", gems, code)
	}
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("9701")
	gotGems, gotCash := acc.Gems, acc.Cash
	store.AccountsMu.Unlock()
	if gotCash != HeartRewardCash() || gotGems != 0 {
		t.Fatalf("cash/gems = %d/%d, want %d/0 from HeartSend only", gotCash, gotGems, HeartRewardCash())
	}
	if rec := QuestProgress("9701", now); len(rec.Done) != 0 || len(rec.Claimed) != 0 {
		t.Fatalf("quest record marked the hearts row: %+v", rec)
	}
	if lines := gachaLedger(t, dir); len(lines) != 1 || lines[0].Currency != "cash" || lines[0].Delta != HeartRewardCash() || lines[0].Reason != "heart:20261011" {
		t.Fatalf("ledger = %+v, want one cash +%d heart:20261011 line", lines, HeartRewardCash())
	}
}

func TestRewardsRequestsAndEpisodesValidation(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.RewardsJSON

	// Older override without requests/request_rules/episodes still loads and uses defaults.
	legacy := mutateDefault(t, raw,
		`"request_rules": {"max_daily": 10, "free_calls": 3, "call_gems": 10, "refresh_gems": 10},`, ``,
	)
	// Strip requests and episodes blocks via small targeted path.
	path := filepath.Join(t.TempDir(), "rewards.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rewardsFile.LoadFrom(path); err != nil {
		t.Fatalf("override without request_rules refused: %v", err)
	}
	if got := ActiveRequestRules(); got.MaxDaily != 10 || got.FreeCalls != 3 || got.CallGems != 10 || got.RefreshGems != 10 {
		t.Fatalf("default request rules = %+v", got)
	}

	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"id": 9401,`, `"id": 9001,`), "request 9001: id must be unique and inside the Cherry block 9001-9599"},
		{mutateDefault(t, raw, `"id": 9501,`, `"id": 9900,`), "episode 9900: id must be unique and inside the Cherry block 9001-9599"},
		{mutateDefault(t, raw, `"id": 9501,`, `"id": 9401,`), "episode 9401: id must be unique and inside the Cherry block 9001-9599"},
		{mutateDefault(t, raw, `"max_daily": 10`, `"max_daily": 0`), "request_rules.max_daily must be 1 to 100"},
		{mutateDefault(t, raw, `"call_gems": 10`, `"call_gems": -1`), "request_rules.call_gems must not be negative"},
		{mutateDefault(t, raw, `"id": 9401, "key": "garden-visit"`, `"id": 9401, "key": "bad-key"`), `request 9401: key "bad-key" is not a known trigger`},
	}
	for _, c := range cases {
		refuseFile(t, rewardsFile.LoadFrom, "rewards.json", c.text, c.want)
	}
}

func TestRequestsLifecycleAndLimits(t *testing.T) {
	questAccount(t, "9721")
	dir := questStore(t)
	now := jstAt(11, 12, 0)

	// Initial RequestList seeds 9401, 9402, 9403.
	view := RequestList("9721", now)
	if len(view.Rows) != 3 || view.Rows[0].ID != 9401 || view.Rows[1].ID != 9402 || view.Rows[2].ID != 9403 {
		t.Fatalf("seeded requests = %+v", view.Rows)
	}
	if view.MaxDaily != 10 || view.FreeCallsLeft != 3 || view.CanInvite {
		t.Fatalf("initial view = %+v", view)
	}

	// Completing daily Task "square" first, then triggering "square" again, still advances Request 9402 without auto-pay.
	MarkQuest("9721", "square", now)
	view = RequestList("9721", now)
	if view.Rows[1].Status != 3 || view.Rows[1].Current != 1 {
		t.Fatalf("9402 after square = %+v, want status 3 cur 1", view.Rows[1])
	}

	// Accept works on active Request, fails on daily task 9001.
	if code := AcceptQuest("9721", 9401, now); code != 0 {
		t.Fatalf("accept 9401 = %d", code)
	}
	if code := AcceptQuest("9721", 9001, now); code == 0 {
		t.Fatal("accept daily 9001 succeeded")
	}

	// Claim 9402 pays 50 Gems with quest:req:9402:20261011, increments TodayDone, and cycles in 9404.
	gems, cash, code := ClaimQuestReward("9721", 9402, now)
	if code != 0 || gems != 50 || cash != 0 {
		t.Fatalf("claim 9402 = %d/%d/%d", gems, cash, code)
	}
	view = RequestList("9721", now)
	if view.TodayDone != 1 || len(view.Rows) != 3 || view.Rows[2].ID != 9404 {
		t.Fatalf("after claim view = %+v", view)
	}

	// Reject 9401 removes it and enables CanInvite.
	if code := RejectQuest("9721", 9401, now); code != 0 {
		t.Fatalf("reject 9401 = %d", code)
	}
	view = RequestList("9721", now)
	if len(view.Rows) != 2 || !view.CanInvite {
		t.Fatalf("after reject view = %+v", view)
	}

	// PickNPCRequests lists unactive requests and charges RefreshGems when refresh=true.
	picks, refreshGems, pcode := PickNPCRequests("9721", true, now)
	if pcode != 0 || refreshGems != 10 || len(picks) != 4 {
		t.Fatalf("pick npc = code %d refresh %d len %d", pcode, refreshGems, len(picks))
	}

	// CallNPCRequest uses a free call first, then fails with 199428 if 3 are active.
	if code := CallNPCRequest("9721", 9405, false, now); code != 0 {
		t.Fatalf("free call 9405 = %d", code)
	}
	if view := RequestList("9721", now); view.FreeCallsLeft != 2 || len(view.Rows) != 3 {
		t.Fatalf("after free call = %+v", view)
	}
	if code := CallNPCRequest("9721", 9406, false, now); code != questErrCallInProgress {
		t.Fatalf("call while full = %d, want %d", code, questErrCallInProgress)
	}

	// Paid call after rejecting 9403 deducts CallGems (10).
	RejectQuest("9721", 9403, now)
	beforeGems := questGems(t, "9721")
	if code := CallNPCRequest("9721", 9406, true, now); code != 0 {
		t.Fatalf("paid call 9406 = %d", code)
	}
	if got := questGems(t, "9721"); got != beforeGems-10 {
		t.Fatalf("gems after paid call = %d, want %d", got, beforeGems-10)
	}

	// Ledger has quest:9003, quest:req:9402, quest:refresh, quest:call:9406.
	lines := gachaLedger(t, dir)
	if len(lines) < 4 {
		t.Fatalf("ledger lines = %+v", lines)
	}
}

func TestEpisodesSequentialProgression(t *testing.T) {
	questAccount(t, "9722")
	dir := questStore(t)
	day1 := jstAt(11, 12, 0)
	day2 := jstAt(12, 12, 0)

	eps := EpisodeList("9722")
	if len(eps) != 2 || eps[0].ID != 9501 || eps[0].Locked || !eps[1].Locked || eps[1].ID != 9502 {
		t.Fatalf("initial episodes = %+v", eps)
	}
	if code := RejectQuest("9722", 9501, day1); code == 0 {
		t.Fatal("episode reject succeeded")
	}
	if code := AcceptQuest("9722", 9502, day1); code == 0 {
		t.Fatal("locked episode accept succeeded")
	}
	if code := AcceptQuest("9722", 9501, day1); code != 0 {
		t.Fatalf("unlocked episode accept = %d", code)
	}

	// MarkQuest advances 9501 without auto-paying it.
	MarkQuest("9722", "garden-own", day1)
	eps = EpisodeList("9722")
	if eps[0].Status != 3 || eps[0].Current != 1 {
		t.Fatalf("episode 1 after trigger = %+v", eps[0])
	}

	// Claim 9501 unlocks 9502; survives next day rollover and StoredFrom/accountFromStored round-trip.
	gems, cash, code := ClaimQuestReward("9722", 9501, day1)
	if code != 0 || gems != 100 || cash != 0 {
		t.Fatalf("claim 9501 = %d/%d/%d", gems, cash, code)
	}
	eps = EpisodeList("9722")
	if len(eps) != 2 || eps[0].ID != 9502 || eps[0].Locked || eps[1].ID != 9503 {
		t.Fatalf("after claim 9501 = %+v", eps)
	}

	// Progress on day2 advances episode 2 ("garden-visit").
	MarkQuest("9722", "garden-visit", day2)
	if eps := EpisodeList("9722"); eps[0].ID != 9502 || eps[0].Status != 3 {
		t.Fatalf("day2 episode 2 = %+v", eps)
	}
	if lines := gachaLedger(t, dir); !slices.ContainsFunc(lines, func(l store.LedgerLine) bool { return l.Reason == "quest:ep:9501" && l.Delta == 100 }) {
		t.Fatalf("missing quest:ep:9501 ledger line in %+v", lines)
	}

	// StoredFrom preserves Episodes.
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("9722")
	stored := store.StoredFrom(acc)
	store.AccountsMu.Unlock()
	if stored.Episodes == nil || !slices.Equal(stored.Episodes.Claimed, []int64{9501}) || stored.Episodes.Progress[9502] != 1 {
		t.Fatalf("stored episodes = %+v", stored.Episodes)
	}

	// Complete remaining episodes -> EpisodeList becomes empty.
	for _, step := range []struct {
		id  int64
		key string
	}{
		{9502, "garden-visit"}, {9503, "square"}, {9504, "diary"}, {9505, "gather3"}, {9506, "atelier-craft"}, {9507, "square-fish"},
	} {
		MarkQuest("9722", step.key, day2)
		if _, _, c := ClaimQuestReward("9722", step.id, day2); c != 0 {
			t.Fatalf("claim episode %d = %d", step.id, c)
		}
	}
	if got := EpisodeList("9722"); len(got) != 0 {
		t.Fatalf("after all episodes claimed = %+v, want empty", got)
	}
}
