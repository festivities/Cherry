package economy

import (
	"slices"
	"testing"
	"time"

	"cherry/data"
)

// forceSeason pins the calendar in force for one test, as /admin/season does ("" is the normal calendar).
func forceSeason(t *testing.T, name string) {
	t.Helper()
	seasonOverride.Store(&name)
	t.Cleanup(func() { seasonOverride.Store(nil) })
}

func questIDs(now time.Time) []int64 {
	var ids []int64
	for _, def := range QuestCatalog(now) {
		ids = append(ids, def.ID)
	}
	return ids
}

// TestEventQuestWindows: event quests are listed and creditable only on their season's game dates
// (the day starts at 06:00 JST). New Year (Jan 1-14) takes Jan 1-5 from Winter, as season.go does.
func TestEventQuestWindows(t *testing.T) {
	daily := []int64{9001, 9002, 9003, 9004, 9005}
	halloween := []int64{9101, 9102, 9103}
	winter := []int64{9201, 9202, 9203}
	newyear := []int64{9301, 9302, 9303}
	cases := []struct {
		at   time.Time
		want []int64
	}{
		{jstAt(24, 23, 59), daily},
		{jstAt(25, 5, 59), daily}, // game day 10-24 still
		{jstAt(25, 6, 0), slices.Concat(daily, halloween)},
		{jstAt(26, 12, 0), slices.Concat(daily, halloween)},
		{time.Date(2026, time.November, 8, 5, 59, 0, 0, jst), slices.Concat(daily, halloween)}, // game day 11-07
		{time.Date(2026, time.November, 8, 6, 0, 0, 0, jst), daily},
		{time.Date(2026, time.December, 20, 12, 0, 0, 0, jst), slices.Concat(daily, winter)},
		{time.Date(2027, time.January, 3, 12, 0, 0, 0, jst), slices.Concat(daily, newyear)},
		{time.Date(2027, time.January, 8, 12, 0, 0, 0, jst), slices.Concat(daily, newyear)},
		{time.Date(2027, time.January, 15, 12, 0, 0, 0, jst), daily},
	}
	for _, c := range cases {
		if got := questIDs(c.at); !slices.Equal(got, c.want) {
			t.Fatalf("catalog at %s = %v, want %v", c.at, got, c.want)
		}
	}
	if _, ok := QuestByID(9101, jstAt(24, 12, 0)); ok {
		t.Fatal("halloween quest known before its window")
	}
	if def, ok := QuestByID(9101, jstAt(26, 12, 0)); !ok || def.Gems != 15 || def.Key != "gather3" {
		t.Fatalf("halloween quest in window = %+v/%v", def, ok)
	}
}

// TestEventQuestForcedSeason: /admin/season forces the event list the same way it forces the calendar.
func TestEventQuestForcedSeason(t *testing.T) {
	now := jstAt(11, 12, 0) // the normal calendar by date
	forceSeason(t, "halloween")
	if got := questIDs(now); len(got) != 8 {
		t.Fatalf("forced halloween catalog = %v, want the 5 daily rows and 3 event rows", got)
	}
	forceSeason(t, "")
	if got := questIDs(jstAt(26, 12, 0)); len(got) != 5 {
		t.Fatalf("forced normal calendar in halloween dates = %v, want the daily rows only", got)
	}
}

// TestEventQuestCreditsOncePerDay: each event trigger completes its quest once per game day with one
// ledger line, the next game day credits again, and a trigger outside the window credits nothing.
func TestEventQuestCreditsOncePerDay(t *testing.T) {
	questAccount(t, "9703")
	dir := questStore(t)
	before := jstAt(24, 12, 0)
	for _, key := range []string{"gather3", "atelier-craft", "square-fish"} {
		if _, done := MarkQuest("9703", key, before); done {
			t.Fatalf("%s credited before the halloween window", key)
		}
	}
	if gems, code := ClaimQuest("9703", 9101, before); gems != 0 || code != questErrRejected {
		t.Fatalf("claim before the window = %d/%d, want refused", gems, code)
	}

	day := jstAt(26, 12, 0)
	for _, c := range []struct {
		key string
		id  int64
	}{{"gather3", 9101}, {"atelier-craft", 9102}, {"square-fish", 9103}} {
		if def, done := MarkQuest("9703", c.key, day); !done || def.ID != c.id {
			t.Fatalf("%s = %+v/%v, want quest %d", c.key, def, done, c.id)
		}
		if _, done := MarkQuest("9703", c.key, day); done {
			t.Fatalf("%s credited twice on one game day", c.key)
		}
	}
	if got := questGems(t, "9703"); got != 45 {
		t.Fatalf("gems after three event quests = %d, want 45", got)
	}
	lines := gachaLedger(t, dir)
	if len(lines) != 3 || lines[0].Reason != "quest:9101:20261026" || lines[2].Reason != "quest:9103:20261026" || lines[0].Delta != 15 {
		t.Fatalf("ledger = %+v, want three 15-Gem quest lines dated 20261026", lines)
	}

	if _, done := MarkQuest("9703", "gather3", jstAt(27, 12, 0)); !done {
		t.Fatal("the next game day did not credit gather3 again")
	}
	if got := questGems(t, "9703"); got != 60 {
		t.Fatalf("gems after the next day = %d, want 60", got)
	}
}

// TestWinterEventQuestUsesItsOwnRow: a key shared by both seasons (square-fish) credits the row of the
// season in force, and the winter row credits in winter.
func TestWinterEventQuestUsesItsOwnRow(t *testing.T) {
	questAccount(t, "9704")
	dir := questStore(t)
	when := time.Date(2026, time.December, 20, 12, 0, 0, 0, jst)
	if def, done := MarkQuest("9704", "square-fish", when); !done || def.ID != 9203 {
		t.Fatalf("winter square-fish = %+v/%v, want 9203", def, done)
	}
	if lines := gachaLedger(t, dir); len(lines) != 1 || lines[0].Reason != "quest:9203:20261220" {
		t.Fatalf("ledger = %+v, want one quest:9203:20261220 line", lines)
	}
}

// TestGatherCountCountsEveryGarden: GatherCount is the day's successful gathers, own and friend Gardens.
func TestGatherCountCountsEveryGarden(t *testing.T) {
	questAccount(t, "9705")
	questStore(t)
	now := jstAt(26, 12, 0)
	item := ActiveGathering().Drops["essence"][0].Item
	if n := GatherCount("9705", now); n != 0 {
		t.Fatalf("count before any gather = %d", n)
	}
	GatherCredit("9705", "9705", GatherEssence, item, now)
	GatherCredit("9705", "9705", GatherEssence, item, now)
	GatherCredit("9705", "9999", GatherOre, item, now)
	if n := GatherCount("9705", now); n != 3 {
		t.Fatalf("count after two own and one friend gather = %d, want 3", n)
	}
	if n := GatherCount("9705", jstAt(27, 12, 0)); n != 0 {
		t.Fatalf("count on the next game day = %d, want 0", n)
	}
}

// TestEventQuestRefusals: rewards.json event rows are checked at startup, and each refusal names the
// file and the entry.
func TestEventQuestRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.RewardsJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"season": "halloween", "name": "Halloween: Gather`, `"season": "spring", "name": "Halloween: Gather`),
			`event quest 9101: season "spring" is not a season in calendars.json`},
		{mutateDefault(t, raw, `"season": "halloween", "name": "Halloween: Gather`, `"season": "", "name": "Halloween: Gather`),
			`event quest 9101: season "" is not a season in calendars.json`},
		{mutateDefault(t, raw, `"id": 9101,`, `"id": 9001,`), "event quest 9001: id must be unique and positive"},
		{mutateDefault(t, raw, `"key": "gather3", "season": "halloween"`, `"key": "gather9", "season": "halloween"`),
			`event quest 9101: key "gather9" is not a known trigger`},
		{mutateDefault(t, raw, `"key": "atelier-craft", "season": "halloween"`, `"key": "gather3", "season": "halloween"`),
			`event quest 9102: key "gather3" must be unique within its season`},
		{mutateDefault(t, raw, `"key": "gather3", "season": "halloween"`, `"key": "diary", "season": "halloween"`),
			`event quest 9101: key "diary" must be unique within its season and not a daily quest key`},
		{mutateDefault(t, raw, `"gems": 15}`, `"gems": -1}`), "event quest 9101: gems must not be negative"},
		{mutateDefault(t, raw, `"key": "garden-own"`, `"key": "garden-owned"`), `quest 9001: key "garden-owned" is not a known trigger`},
	}
	for _, c := range cases {
		refuseFile(t, rewardsFile.LoadFrom, "rewards.json", c.text, c.want)
	}
}
