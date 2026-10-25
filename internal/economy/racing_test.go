package economy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// racingAcc installs one account (no pets) for the racing tests.
func racingAcc(t *testing.T, aid string) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"race-" + aid: {AccessToken: "race-" + aid, SessionKey: "sk", Aid: aid, Name: "Racer"}})
}

// racingGems is aid's Gem balance.
func racingGems(t *testing.T, aid string) int64 {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return acc.Gems
}

// TestRacingOperatorFile: the embedded default loads, and every bad value names its file and entry.
func TestRacingOperatorFile(t *testing.T) {
	operatorDir(t)
	if err := racingFile.LoadFrom(""); err != nil {
		t.Fatalf("default: %v", err)
	}
	refuse := func(old, new, want string) {
		t.Helper()
		refuseFile(t, racingFile.LoadFrom, "racing.json", mutateDefault(t, data.RacingJSON, old, new), want)
	}
	refuse(`"max_tickets": 5`, `"max_tickets": 0`, "max_tickets")
	refuse(`"roster_delay_ms": 3000`, `"roster_delay_ms": 999`, "roster_delay_ms")
	refuse(`"countdown_sec": 3`, `"countdown_sec": 0`, "countdown_sec")
	refuse(`"pos_key_count": 1024`, `"pos_key_count": 10`, "pos_key_count")
	refuse(`"begin_x": 112`, `"begin_x": 130`, "track finish")
	refuse(`{"name": "Rookie", "mmr_min": 0}`, `{"name": "Rookie", "mmr_min": 5}`, "first mmr_min")
	refuse(`"mmr_min": 50}`, `"mmr_min": 150}`, "grades[3]")
	refuse(`"gems": 10`, `"gems": -1`, "reward")
	refuse(`"max_tickets": 5`, `"max_tickets": 5, "bogus": 1`, "bogus")

	// A valid operator file replaces the default.
	path := writeOperatorFile(t, "racing.json", mutateDefault(t, data.RacingJSON, `"max_tickets": 5`, `"max_tickets": 7`))
	if err := racingFile.LoadFrom(path); err != nil || ActiveRacing().MaxTickets != 7 {
		t.Fatalf("operator file: err %v, max_tickets %d", err, ActiveRacing().MaxTickets)
	}
}

// writeOperatorFile writes text as the named file in a temp dir and returns its path.
func writeOperatorFile(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRaceTicketsPerGameDay: the day's tickets are spent one by one, refused at zero, and come back on the
// next game day.
func TestRaceTicketsPerGameDay(t *testing.T) {
	operatorDir(t)
	racingAcc(t, "7101")
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, jst)
	limit := ActiveRacing().MaxTickets
	for i := range limit {
		if left, ok := RaceStart("7101", now); !ok || left != limit-i-1 {
			t.Fatalf("start %d = left %d ok %v", i+1, left, ok)
		}
	}
	if left, ok := RaceStart("7101", now); ok || left != 0 {
		t.Fatalf("start past the day's tickets = left %d ok %v, want refused", left, ok)
	}
	if st := RaceStatusOf("7101", now); st.Tickets != 0 || st.MaxTickets != limit || st.ResetSec <= 0 {
		t.Fatalf("status = %+v, want no tickets and a reset ahead", st)
	}
	next := now.Add(24 * time.Hour)
	if st := RaceStatusOf("7101", next); st.Tickets != limit {
		t.Fatalf("next day tickets = %d, want %d", st.Tickets, limit)
	}
	if _, ok := RaceStart("7101", next); !ok {
		t.Fatal("start on the next game day refused")
	}
}

// TestRaceRewardsPaidOncePerArrivalWithDailyCap: an arrival pays Gems up to the day's cap, plus points, exp
// and MMR; the level and the grade follow the exp and MMR; a time-out pays nothing.
func TestRaceRewardsPaidOncePerArrivalWithDailyCap(t *testing.T) {
	operatorDir(t)
	racingAcc(t, "7102")
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, jst)
	reward := ActiveRacing().Reward
	var paid int64
	for i := range 5 {
		out, ok := RaceFinish("7102", true, now)
		if !ok {
			t.Fatalf("arrival %d refused", i+1)
		}
		want := min(reward.Gems, max(reward.GemsDailyCap-paid, 0))
		if out.Gems != want {
			t.Fatalf("arrival %d paid %d Gems, want %d", i+1, out.Gems, want)
		}
		paid += out.Gems
		if i == 4 && (out.Before.Level != 1 || out.After.Level != 2 || out.After.LevelExp != 0) {
			t.Fatalf("fifth arrival level %d -> %d (exp %d), want 1 -> 2 (exp 0)", out.Before.Level, out.After.Level, out.After.LevelExp)
		}
		if i == 4 && (out.After.Grade != 1 || out.After.GradeName != "Silver") {
			t.Fatalf("fifth arrival grade %d %q, want Silver", out.After.Grade, out.After.GradeName)
		}
	}
	if got := racingGems(t, "7102"); got != paid || paid != reward.GemsDailyCap {
		t.Fatalf("Gems = %d, paid %d, want the day's cap %d", got, paid, reward.GemsDailyCap)
	}
	if st := RaceStatusOf("7102", now); st.Points != 5*reward.StorePoints || st.Mmr != 5*reward.Mmr {
		t.Fatalf("status after five arrivals = points %d mmr %d", st.Points, st.Mmr)
	}

	// A race the time runs out on pays nothing and changes nothing.
	out, ok := RaceFinish("7102", false, now)
	if !ok || out.Gems != 0 || racingGems(t, "7102") != paid {
		t.Fatalf("time-out = %+v ok %v, Gems %d", out, ok, racingGems(t, "7102"))
	}

	// The next game day pays again (its own cap), and the career totals carry over.
	next := now.Add(24 * time.Hour)
	out, ok = RaceFinish("7102", true, next)
	if !ok || out.Gems != reward.Gems || out.Before.Level != 2 {
		t.Fatalf("next day arrival = %+v ok %v, want %d Gems at level 2", out, ok, reward.Gems)
	}
}

// TestRaceUnknownAccount: racing for an account that does not exist is refused, and has no ride pets.
func TestRaceUnknownAccount(t *testing.T) {
	operatorDir(t)
	racingAcc(t, "7103")
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, jst)
	if _, ok := RaceStart("9999", now); ok {
		t.Fatal("start for an unknown account accepted")
	}
	if _, ok := RaceFinish("9999", true, now); ok {
		t.Fatal("arrival for an unknown account paid")
	}
	if pets := RidePets("9999"); len(pets) != 0 {
		t.Fatalf("pets of an unknown account = %v", pets)
	}
}

// TestRidePetsAreTheRideCatalogPets: only owned pets the catalog files as URIDE are Riding Pets; a pet
// without art is not listed.
func TestRidePetsAreTheRideCatalogPets(t *testing.T) {
	operatorDir(t)
	shopFixture(t, map[string][]string{
		"pet/326400259": {"skeleton.xml", "r.aniproj", "dp.png"}, // PUPE00077: a Riding Pet (animated)
		"pet/326400016": {"skeleton.xml", "dp.png"},              // PUPE0000G: a dog
	})
	testutil.InstallAccounts(t, map[string]*store.Account{"race-7104": {AccessToken: "race-7104", SessionKey: "sk", Aid: "7104", Name: "Racer",
		Pets: []store.PetItem{{ID: 1, Cd: "PUPE0000G"}, {ID: 2, Cd: "PUPE00077"}, {ID: 3, Cd: "PUPE00099"}}}})
	if got := RidePets("7104"); len(got) != 1 || got[0].Cd != "PUPE00077" {
		t.Fatalf("ride pets = %v, want PUPE00077 only", got)
	}
}
