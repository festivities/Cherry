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
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(dir, "accounts.json")
	store.AccountsMu.Unlock()
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
	// roll(100)=5 is a rare catch (<10); the second roll picks rare entry 1 (Crystal Square Ray).
	seq := []int{5, 1}
	got := ChooseFish(func(n int) int { v := seq[0]; seq = seq[1:]; return v % n })
	if got.ID != 4202 || !got.Rare || got.Gems != 25 {
		t.Fatalf("rare pick = %+v", got)
	}
	// roll(100)=50 is common; roll(6)=5 picks the last normal fish (Square Eel, 10 Gems).
	seq = []int{50, 5}
	got = ChooseFish(func(n int) int { v := seq[0]; seq = seq[1:]; return v % n })
	if got.ID != 4106 || got.Rare || got.Gems != 10 {
		t.Fatalf("common pick = %+v", got)
	}
	for _, f := range append(append([]FishDef{}, fishNormal...), fishRare...) {
		if f.Gems < 5 || (f.Rare && f.Gems != 25) || (!f.Rare && f.Gems > 10) {
			t.Fatalf("payout out of range: %+v", f)
		}
	}
}

func TestFishCatchCapsAtDailyLimit(t *testing.T) {
	fishAccount(t, "9901", 100, nil)
	now := jstAt(11, 12, 0)
	eel := fishNormal[5] // 10 Gems
	for i := 0; i < 15; i++ {
		if credit, ok := FishCatch("9901", eel, now); !ok || credit != 10 {
			t.Fatalf("catch %d = (%d, %v), want (10, true)", i, credit, ok)
		}
	}
	if credit, ok := FishCatch("9901", eel, now); !ok || credit != 0 {
		t.Fatalf("catch past cap = (%d, %v), want (0, true)", credit, ok)
	}
	gems, rec := fishGems(t, "9901")
	if gems != 100+FishingDailyCap || rec == nil || rec.Gems != FishingDailyCap || rec.Day != AttendDayKey(now) {
		t.Fatalf("gems=%d record=%+v", gems, rec)
	}
}

func TestFishCatchPartialLastCatchIsCapped(t *testing.T) {
	today := AttendDayKey(jstAt(11, 12, 0))
	fishAccount(t, "9902", 0, &store.FishingDay{Day: today, Gems: 145})
	eel := fishNormal[5]
	if credit, ok := FishCatch("9902", eel, jstAt(11, 12, 0)); !ok || credit != 5 {
		t.Fatalf("partial = (%d, %v), want (5, true)", credit, ok)
	}
	if credit, ok := FishCatch("9902", eel, jstAt(11, 13, 0)); !ok || credit != 0 {
		t.Fatalf("after cap = (%d, %v), want (0, true)", credit, ok)
	}
}

func TestFishCatchDayRolloverResetsCap(t *testing.T) {
	yesterday := AttendDayKey(jstAt(10, 12, 0))
	fishAccount(t, "9903", 0, &store.FishingDay{Day: yesterday, Gems: FishingDailyCap})
	eel := fishNormal[5]
	// 05:59 JST is still the previous game day, so the old record stays capped.
	if credit, ok := FishCatch("9903", eel, jstAt(11, 5, 59)); !ok || credit != 0 {
		t.Fatalf("before 06:00 = (%d, %v), want (0, true)", credit, ok)
	}
	if credit, ok := FishCatch("9903", eel, jstAt(11, 6, 0)); !ok || credit != 10 {
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
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json") // MkdirAll fails: the parent is a file
	store.AccountsMu.Unlock()
	if credit, ok := FishCatch("9904", fishNormal[5], jstAt(11, 12, 0)); ok || credit != 0 {
		t.Fatalf("failed save = (%d, %v), want (0, false)", credit, ok)
	}
	if gems, rec := fishGems(t, "9904"); gems != 40 || rec != nil {
		t.Fatalf("rolled back gems=%d record=%+v", gems, rec)
	}
}

func TestFishCatchLedgerReason(t *testing.T) {
	dir := fishAccount(t, "9905", 0, nil)
	if _, ok := FishCatch("9905", fishNormal[5], jstAt(11, 12, 0)); !ok {
		t.Fatal("catch failed")
	}
	b, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(b))
	if !strings.Contains(line, `"reason":"fish:4106:20261011"`) || !strings.Contains(line, `"delta":10`) {
		t.Fatalf("ledger line = %s", line)
	}
}

// TestFishCardNameAndImage: the card line carries the payout (stock fish card has no Gem line),
// and every catalogue fish has a tinted picture; unknown ids 404.
func TestFishCardNameAndImage(t *testing.T) {
	if got := FishCardName(fishNormal[0], 5); got != "Square Carp +5 Gems" {
		t.Fatalf("name = %q", got)
	}
	if got := FishCardName(fishNormal[0], 0); got != "Square Carp (daily Gem limit reached)" {
		t.Fatalf("capped name = %q", got)
	}
	for _, f := range append(slices.Clone(fishNormal), fishRare...) {
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
