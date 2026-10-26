package economy

import (
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/store"
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
		{mutateDefault(t, raw, `"max_players": 5`, `"max_players": 0`), "max_players must be 1 to 5"},
		{mutateDefault(t, raw, `"max_players": 5`, `"max_players": 6`), "max_players must be 1 to 5"},
		{mutateDefault(t, raw, `"tint": "#3c5aa0"`, `"tint": "blue"`), "must be #rrggbb"},
		{mutateDefault(t, raw, `"id": 4901`, `"id": 4101`), "also a fish id"},
		{mutateDefault(t, raw, `"gems": 50`, `"gems": -1`), "book_reward amounts must not be negative"},
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

// setBossCaught sets aid's catch count of the boss.
func setBossCaught(t *testing.T, aid string, n int64) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	acc.BossCaught = map[int64]int64{ActiveFishBoss().Boss.ID: n}
}

// bookAccount returns aid's Boss Coins and whether the boss book reward is claimed.
func bookAccount(t *testing.T, aid string) (coins int64, claimed bool) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return acc.BossCoins, acc.BossBook[ActiveFishBoss().Boss.ID]
}

// bookLedgerLines returns the ledger lines of the boss book reward (reason fishboss:book:<bossId>).
func bookLedgerLines(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, `"reason":"fishboss:book:`) {
			out = append(out, l)
		}
	}
	return out
}

// TestFishBossBookClaimPaysOnce: a caught boss pays the book reward once: Gems through the ledger under
// the boss book reason, Boss Coins on the account, and the claim is kept. A repeat pays nothing.
func TestFishBossBookClaimPaysOnce(t *testing.T) {
	operatorDir(t)
	dir := fishAccount(t, "9906", 10, nil)
	setBossCaught(t, "9906", 1)
	boss := ActiveFishBoss()
	if !BossBookClaim("9906") {
		t.Fatal("first claim refused")
	}
	if gems, _ := fishGems(t, "9906"); gems != 10+boss.BookReward.Gems {
		t.Fatalf("Gems after the claim = %d, want %d", gems, 10+boss.BookReward.Gems)
	}
	if coins, claimed := bookAccount(t, "9906"); coins != boss.BookReward.BossCoins || !claimed {
		t.Fatalf("after the claim: Boss Coins %d, claimed %v", coins, claimed)
	}
	if caught, claimed := BossBookState("9906"); caught != 1 || !claimed {
		t.Fatalf("book page = caught %d, claimed %v", caught, claimed)
	}
	if BossBookClaim("9906") {
		t.Fatal("repeat claim paid")
	}
	if gems, _ := fishGems(t, "9906"); gems != 10+boss.BookReward.Gems {
		t.Fatalf("Gems after the repeat = %d, want %d", gems, 10+boss.BookReward.Gems)
	}
	if coins, _ := bookAccount(t, "9906"); coins != boss.BookReward.BossCoins {
		t.Fatalf("Boss Coins after the repeat = %d, want %d", coins, boss.BookReward.BossCoins)
	}

	lines := bookLedgerLines(t, dir)
	reason := `"reason":"fishboss:book:` + strconv.FormatInt(boss.Boss.ID, 10) + `"`
	if len(lines) != 1 || !strings.Contains(lines[0], reason) ||
		!strings.Contains(lines[0], `"delta":`+strconv.FormatInt(boss.BookReward.Gems, 10)) || !strings.Contains(lines[0], `"currency":"gems"`) {
		t.Fatalf("book ledger lines = %q, want one %s line", lines, reason)
	}
}

// TestFishBossBookClaimNeedsACatch: without a catch the claim is refused and nothing is paid.
func TestFishBossBookClaimNeedsACatch(t *testing.T) {
	operatorDir(t)
	fishAccount(t, "9907", 10, nil)
	if BossBookClaim("9907") {
		t.Fatal("claim without a catch paid")
	}
	if BossBookClaim("9999") {
		t.Fatal("claim for an unknown account paid")
	}
	if gems, _ := fishGems(t, "9907"); gems != 10 {
		t.Fatalf("Gems = %d, want 10", gems)
	}
	if coins, claimed := bookAccount(t, "9907"); coins != 0 || claimed {
		t.Fatalf("Boss Coins %d, claimed %v; want 0, not claimed", coins, claimed)
	}
}

// TestFishBossBookClaimSaveFailureRollsBack: a failed save pays nothing and keeps the claim open, so the
// claim pays once when the save works again.
func TestFishBossBookClaimSaveFailureRollsBack(t *testing.T) {
	operatorDir(t)
	fishAccount(t, "9908", 10, nil)
	setBossCaught(t, "9908", 1)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file, not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json") // MkdirAll fails: the parent is a file
	store.AccountsMu.Unlock()
	if BossBookClaim("9908") {
		t.Fatal("failed save reported a claim")
	}
	if gems, _ := fishGems(t, "9908"); gems != 10 {
		t.Fatalf("rolled back Gems = %d, want 10", gems)
	}
	if coins, claimed := bookAccount(t, "9908"); coins != 0 || claimed {
		t.Fatalf("rolled back Boss Coins %d, claimed %v; want 0, not claimed", coins, claimed)
	}

	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Unlock()
	if !BossBookClaim("9908") {
		t.Fatal("claim refused after the save works again")
	}
	if gems, _ := fishGems(t, "9908"); gems != 10+ActiveFishBoss().BookReward.Gems {
		t.Fatalf("Gems after the retry = %d, want %d", gems, 10+ActiveFishBoss().BookReward.Gems)
	}
}

// TestFishBossBookClaimSurvivesReload: the claim and its Boss Coins are saved in accounts.json, so after
// a reload the claim still stands and a repeat pays nothing.
func TestFishBossBookClaimSurvivesReload(t *testing.T) {
	operatorDir(t)
	fishAccount(t, "9909", 10, nil)
	setBossCaught(t, "9909", 1)
	store.AccountsMu.Lock()
	store.NextAvatarID = 9999 // saved and checked on load: the account id must be below it
	store.AccountsMu.Unlock()
	if !BossBookClaim("9909") {
		t.Fatal("claim refused")
	}
	store.AccountsMu.Lock()
	path := store.AccountStorePath
	store.AccountsMu.Unlock()
	if err := store.LoadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	if coins, claimed := bookAccount(t, "9909"); coins != ActiveFishBoss().BookReward.BossCoins || !claimed {
		t.Fatalf("reloaded: Boss Coins %d, claimed %v", coins, claimed)
	}
	if BossBookClaim("9909") {
		t.Fatal("repeat after a reload paid")
	}
}
