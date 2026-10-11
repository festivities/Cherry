package economy

import (
	"slices"
	"testing"
	"time"

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
	store.AccountsMu.Lock()
	store.AccountStorePath = dir + "/accounts.json"
	store.AccountsMu.Unlock()
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
	total := int64(0)
	for _, def := range QuestCatalog {
		total += def.Gems
	}
	if total != 150 || len(QuestCatalog) != 4 {
		t.Fatalf("catalog = %d quests / %d Gems, want 4 / 150", len(QuestCatalog), total)
	}
}

// TestQuestProgressCompletesEachTriggerOnce: each trigger completes its quest once a day,
// credits its Gems once, and the day's total is 150.
func TestQuestProgressCompletesEachTriggerOnce(t *testing.T) {
	questAccount(t, "9701")
	now := jstAt(11, 12, 0)
	completed := 0
	for _, def := range QuestCatalog {
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
