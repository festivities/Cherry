package economy

import (
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"cherry/data"
)

// TestFishBossFileRefusals: a chart or boss that cannot work is refused at startup, naming the entry.
func TestFishBossFileRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.FishbossJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"play_note": "111111111111111111111111111111"`, `"play_note": ""`), "play_note must not be empty"},
		{mutateDefault(t, raw, `"max_hp": 1000`, `"max_hp": 0`), "boss max_hp must be positive"},
		{mutateDefault(t, raw, `"play_note": "111111111111111111111111111111"`, `"play_note": "111111111111111111111111111121"`), "play_note[28] digit 2 has no note_detail entry"},
		{mutateDefault(t, raw, `"damage_per_hit": 50`, `"damage_per_hit": 10`), "cannot be dealt by 30 notes"},
		{mutateDefault(t, raw, `"note_arrive_ms": 10000`, `"note_arrive_ms": 500`), "note_arrive_ms must be at least 1000"},
		{mutateDefault(t, raw, `"max_tries": 3`, `"max_tries": 0`), "max_tries must be at least 1"},
		{mutateDefault(t, raw, `"tint": "#3c5aa0"`, `"tint": "blue"`), "must be #rrggbb"},
		{mutateDefault(t, raw, `"id": 4901`, `"id": 4101`), "also a fish id"},
	}
	for _, c := range cases {
		refuseFile(t, fishbossFile.LoadFrom, "fishboss.json", c.text, c.want)
	}
	if err := fishbossFile.LoadFrom(""); err != nil { // the embedded default still passes
		t.Fatalf("default: %v", err)
	}
}

// TestFishBossTriesAndResetFollowGameDay: tries are per game day (06:00 JST), and a reset charges
// the Gems once tries are gone, then refills them.
func TestFishBossTriesAndResetFollowGameDay(t *testing.T) {
	operatorDir(t)
	fishAccount(t, "9901", 100, nil)
	boss := ActiveFishBoss()
	boss.ResetGems = 50 // the default offers no reset
	t.Cleanup(func() { boss.ResetGems = 0 })
	day1 := time.Date(2026, time.October, 25, 12, 0, 0, 0, jst)
	spend := func(day time.Time) {
		t.Helper()
		for i := range boss.MaxTries {
			if left, ok := BossTryStart("9901", day); !ok || left != boss.MaxTries-i-1 {
				t.Fatalf("try %d = left %d ok %v", i+1, left, ok)
			}
		}
		if _, ok := BossTryStart("9901", day); ok {
			t.Fatal("a try past the day's limit started")
		}
	}
	spend(day1)

	// Out of tries, the reset charges the Gems and refills the tries.
	if left, ok := BossReset("9901", day1); !ok || left != boss.MaxTries {
		t.Fatalf("reset out of tries = left %d ok %v", left, ok)
	}
	if gems, _ := fishGems(t, "9901"); gems != 100-boss.ResetGems {
		t.Fatalf("Gems after reset = %d, want %d", gems, 100-boss.ResetGems)
	}
	spend(day1)

	// The next game day (06:00 JST) gives the tries back; a reset is refused while some are left.
	day2 := day1.Add(24 * time.Hour)
	if left, ok := BossTryStart("9901", day2); !ok || left != boss.MaxTries-1 {
		t.Fatalf("first try of the next day = left %d ok %v", left, ok)
	}
	if left, ok := BossReset("9901", day2); ok || left != boss.MaxTries-1 {
		t.Fatalf("reset with tries left = left %d ok %v", left, ok)
	}
	if gems, _ := fishGems(t, "9901"); gems != 100-boss.ResetGems {
		t.Fatalf("Gems after refused reset = %d, want %d", gems, 100-boss.ResetGems)
	}
	if left, ok := BossTryStart("9901", day2); !ok || left != boss.MaxTries-2 {
		t.Fatalf("second try of the next day = left %d ok %v", left, ok)
	}
}

// TestFishBossResetOffWhenDataSaysNo: reset_gems 0 offers no reset, so the tries stay spent.
func TestFishBossResetOffWhenDataSaysNo(t *testing.T) {
	operatorDir(t)
	if ActiveFishBoss().ResetGems != 0 {
		t.Fatal("the default fishboss.json should offer no reset")
	}
	fishAccount(t, "9902", 100, nil)
	day := time.Date(2026, time.October, 25, 12, 0, 0, 0, jst)
	for range ActiveFishBoss().MaxTries {
		BossTryStart("9902", day)
	}
	if left, ok := BossReset("9902", day); ok || left != 0 {
		t.Fatalf("reset with reset_gems 0 = left %d ok %v, want refused", left, ok)
	}
	if gems, _ := fishGems(t, "9902"); gems != 100 {
		t.Fatalf("Gems = %d, want 100 (nothing charged)", gems)
	}
}

// TestFishBossPictureIsServed: the boss picture comes from the fish picture route, and the boss id is
// not a fish id (checked at startup, verifyFishBoss).
func TestFishBossPictureIsServed(t *testing.T) {
	id := ActiveFishBoss().Boss.ID
	for _, f := range ActiveFishing().Fish {
		if f.ID == id {
			t.Fatalf("boss id %d is a fish id", id)
		}
	}
	rec := httptest.NewRecorder()
	HandleFishImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/fish/"+strconv.FormatInt(id, 10)+".png", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("boss image = %d", rec.Code)
	}
	if _, err := png.Decode(rec.Body); err != nil {
		t.Fatalf("boss image decode: %v", err)
	}
}
