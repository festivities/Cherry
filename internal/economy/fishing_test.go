package economy

import (
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// fishAccount installs aid with gems and an optional fishing record, and saves into a temp dir
// (ledger.jsonl sits next to accounts.json). The path is reset by testutil's cleanup.
func fishAccount(t *testing.T, aid string, gems int64, fishing *store.FishingDay) string {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"ftok-" + aid: {AccessToken: "ftok-" + aid, SessionKey: "sk", Aid: aid, Name: "Fish", Gems: gems, Fishing: fishing}})
	testutil.SetLab(t, aid, false)
	dir := t.TempDir()
	testutil.UseDatabase(t)
	return dir
}

func fishGems(t *testing.T, aid string) (int64, *store.FishingDay) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return acc.Gems, acc.Fishing
}

func TestChooseFishRollPaths(t *testing.T) {
	// roll(100)=5 is a rare catch (<10); the second roll picks rare entry 1 (4127 Rosy bitterling).
	seq := []int{5, 1}
	got := ChooseFish(func(n int) int { v := seq[0]; seq = seq[1:]; return v % n })
	if got.ID != 4127 || !got.Rare || got.Gems != 22 {
		t.Fatalf("rare pick = %+v", got)
	}
	// roll(100)=50 is common; roll(len(normal))=5 picks normal entry 5 (4106 Sea Bass, 7 Gems).
	seq = []int{50, 5}
	got = ChooseFish(func(n int) int { v := seq[0]; seq = seq[1:]; return v % n })
	if got.ID != 4106 || got.Rare || got.Gems != 7 {
		t.Fatalf("common pick = %+v", got)
	}
	for _, f := range append(append([]FishDef{}, ActiveFishing().normal...), ActiveFishing().rare...) {
		if f.Gems < 5 || (f.Rare && (f.Gems < 22 || f.Gems > 35)) || (!f.Rare && f.Gems > 14) {
			t.Fatalf("payout out of range: %+v", f)
		}
	}
}

func TestFishCatchCapsAtDailyLimit(t *testing.T) {
	fishAccount(t, "9901", 100, nil)
	now := jstAt(11, 12, 0)
	beakfish := ActiveFishing().normal[6] // 4107 Striped beakfish, 10 Gems
	for i := 0; i < 15; i++ {
		if credit, ok := FishCatch("9901", beakfish, now); !ok || credit != 10 {
			t.Fatalf("catch %d = (%d, %v), want (10, true)", i, credit, ok)
		}
	}
	if credit, ok := FishCatch("9901", beakfish, now); !ok || credit != 0 {
		t.Fatalf("catch past cap = (%d, %v), want (0, true)", credit, ok)
	}
	gems, rec := fishGems(t, "9901")
	if gems != 100+ActiveFishing().DailyCap || rec == nil || rec.Gems != ActiveFishing().DailyCap || rec.Day != AttendDayKey(now) {
		t.Fatalf("gems=%d record=%+v", gems, rec)
	}
}

func TestFishCatchPartialLastCatchIsCapped(t *testing.T) {
	today := AttendDayKey(jstAt(11, 12, 0))
	fishAccount(t, "9902", 0, &store.FishingDay{Day: today, Gems: 145})
	beakfish := ActiveFishing().normal[6] // 10 Gems
	if credit, ok := FishCatch("9902", beakfish, jstAt(11, 12, 0)); !ok || credit != 5 {
		t.Fatalf("partial = (%d, %v), want (5, true)", credit, ok)
	}
	if credit, ok := FishCatch("9902", beakfish, jstAt(11, 13, 0)); !ok || credit != 0 {
		t.Fatalf("after cap = (%d, %v), want (0, true)", credit, ok)
	}
}

func TestFishCatchDayRolloverResetsCap(t *testing.T) {
	yesterday := AttendDayKey(jstAt(10, 12, 0))
	fishAccount(t, "9903", 0, &store.FishingDay{Day: yesterday, Gems: ActiveFishing().DailyCap})
	beakfish := ActiveFishing().normal[6] // 10 Gems
	// 05:59 JST is still the previous game day, so the old record stays capped.
	if credit, ok := FishCatch("9903", beakfish, jstAt(11, 5, 59)); !ok || credit != 0 {
		t.Fatalf("before 06:00 = (%d, %v), want (0, true)", credit, ok)
	}
	if credit, ok := FishCatch("9903", beakfish, jstAt(11, 6, 0)); !ok || credit != 10 {
		t.Fatalf("after 06:00 = (%d, %v), want (10, true)", credit, ok)
	}
	if _, rec := fishGems(t, "9903"); rec == nil || rec.Day != AttendDayKey(jstAt(11, 6, 0)) || rec.Gems != 10 {
		t.Fatalf("record after rollover = %+v", rec)
	}
}

func TestFishCatchSaveFailureRollsBack(t *testing.T) {
	fishAccount(t, "9904", 40, nil)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file, not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.FailSaves(t)
	if credit, ok := FishCatch("9904", ActiveFishing().normal[6], jstAt(11, 12, 0)); ok || credit != 0 {
		t.Fatalf("failed save = (%d, %v), want (0, false)", credit, ok)
	}
	if gems, rec := fishGems(t, "9904"); gems != 40 || rec != nil {
		t.Fatalf("rolled back gems=%d record=%+v", gems, rec)
	}
}

func TestFishCatchLedgerReason(t *testing.T) {
	_ = fishAccount(t, "9905", 0, nil)
	if _, ok := FishCatch("9905", ActiveFishing().normal[6], jstAt(11, 12, 0)); !ok {
		t.Fatal("catch failed")
	}
	b, err := testutil.LedgerText(t)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(b))
	if !strings.Contains(line, `"reason":"fish:4107:20261011"`) || !strings.Contains(line, `"delta":10`) {
		t.Fatalf("ledger line = %s", line)
	}
}

func TestFishRetryChargesOutsideDailyCapAndRollsBack(t *testing.T) {
	now := jstAt(11, 12, 0)
	day := AttendDayKey(now)
	fishAccount(t, "9906", 2000, &store.FishingDay{Day: day, Gems: ActiveFishing().DailyCap})
	gems, ok := FishRetry("9906", now)
	if !ok || gems != 750 {
		t.Fatalf("retry = (%d, %v), want (750, true)", gems, ok)
	}
	bal, rec := fishGems(t, "9906")
	if bal != 750 || rec == nil || rec.Gems != ActiveFishing().DailyCap {
		t.Fatalf("after retry bal=%d rec=%+v", bal, rec)
	}
	b, err := testutil.LedgerText(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"reason":"fish:retry:20261011"`) || !strings.Contains(string(b), `"delta":-1250`) {
		t.Fatalf("ledger = %s", b)
	}
	// Insufficient Gems refuses without changing balance.
	if gems, ok := FishRetry("9906", now); ok || gems != 0 {
		t.Fatalf("short retry = (%d, %v), want (0, false)", gems, ok)
	}
	if bal, _ := fishGems(t, "9906"); bal != 750 {
		t.Fatalf("bal after short retry = %d, want 750", bal)
	}
	// Save failure rolls back Gems.
	fishAccount(t, "9907", 1500, nil)
	testutil.FailSaves(t)
	if gems, ok := FishRetry("9907", now); ok || gems != 0 {
		t.Fatalf("failed save retry = (%d, %v), want (0, false)", gems, ok)
	}
	if bal, _ := fishGems(t, "9907"); bal != 1500 {
		t.Fatalf("bal after failed save = %d, want 1500", bal)
	}
}

// TestFishCardNameAndImage: the card line carries the payout (stock fish card has no Gem line),
// and every catalogue fish has a tinted silhouette fallback; unknown ids 404.
func TestFishCardNameAndImage(t *testing.T) {
	testutil.RequireArchive(t)
	if got := FishCardName(ActiveFishing().normal[0], 5); got != "Mackerel +5 Gems" {
		t.Fatalf("name = %q", got)
	}
	if got := FishCardName(ActiveFishing().normal[0], 0); got != "Mackerel (daily Gem limit reached)" {
		t.Fatalf("capped name = %q", got)
	}
	if got := FishImageURL(4101); got != "https://play-static.line-scdn.net/cherry/fish/orig/fish_1_1613979947.png" {
		t.Fatalf("FishImageURL(4101) = %q", got)
	}
	if got := FishImageURL(4172); got != "https://play-static.line-scdn.net/cherry/fish/4172.png" {
		t.Fatalf("FishImageURL(4172) = %q", got)
	}
	for _, f := range append(slices.Clone(ActiveFishing().normal), ActiveFishing().rare...) {
		rec := httptest.NewRecorder()
		HandleFishImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/fish/"+strconv.FormatInt(f.ID, 10)+".png", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("fish %d image = %d", f.ID, rec.Code)
		}
		img, err := png.Decode(rec.Body)
		if err != nil || img.Bounds().Dx() != 206 || img.Bounds().Dy() != 104 {
			t.Fatalf("fish %d image decode err=%v bounds=%v", f.ID, err, img)
		}
	}
	rec := httptest.NewRecorder()
	HandleFishImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/fish/9999.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown fish = %d, want 404", rec.Code)
	}
}
