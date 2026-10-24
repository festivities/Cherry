package economy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

// gatherAccount installs aid with gems and an optional gathering record, and saves into a temp dir
// (ledger.jsonl sits next to accounts.json).
func gatherAccount(t *testing.T, aid string, gems int64, gather *store.GatherDay) string {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"gtok-" + aid: {AccessToken: "gtok-" + aid, SessionKey: "sk", Aid: aid, Name: "Gather", Gems: gems, Gather: gather}})
	testutil.SetLab(t, aid, false)
	dir := t.TempDir()
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(dir, "accounts.json")
	store.AccountsMu.Unlock()
	return dir
}

// seqRoll returns a roll function that yields vals in order, each reduced modulo n.
func seqRoll(vals ...int) func(n int) int {
	return func(n int) int {
		v := vals[0]
		vals = vals[1:]
		return v % n
	}
}

func gatherState(t *testing.T, aid string) (gems, smiles int64, bag []store.GardenBagStack, rec *store.GatherDay) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return acc.Gems, acc.Smiles, acc.GardenBag, acc.Gather
}

// TestGatherDropTables checks the material tables: weights sum to 100 per kind, every id has a name
// and the spec's ids, and PickGather returns a drop of its own kind.
func TestGatherDropTables(t *testing.T) {
	want := map[GatherKind][]int64{
		GatherFish:    {110000, 111000, 120000, 130000, 131000, 132000},
		GatherEssence: {210000, 220000, 230000},
		GatherOre:     {310000, 321000, 330000, 331000},
	}
	for kind, ids := range want {
		total := 0
		for i, d := range gatherDrops[kind] {
			if d.Item != ids[i] || d.Name == "" || d.Weight <= 0 {
				t.Fatalf("kind %d drop %d = %+v, want item %d", kind, i, d, ids[i])
			}
			total += d.Weight
			if got, ok := GatherMaterialByID(d.Item); !ok || got.Item != d.Item {
				t.Fatalf("GatherMaterialByID(%d) = %+v, %v", d.Item, got, ok)
			}
		}
		if total != 100 {
			t.Fatalf("kind %d weights sum to %d, want 100", kind, total)
		}
	}
	if _, ok := GatherMaterialByID(999999); ok {
		t.Fatal("unknown material found")
	}
}

// TestPickGatherWeights checks the roll boundaries: a roll inside a drop's weight picks it, and
// fish add a 10 to 70 cm length from a second roll.
func TestPickGatherWeights(t *testing.T) {
	// Essence weights 60/30/10: rolls 59, 60 and 90 pick 210000, 220000 and 230000.
	for _, tc := range []struct {
		roll int
		item int64
	}{{0, 210000}, {59, 210000}, {60, 220000}, {89, 220000}, {90, 230000}, {99, 230000}} {
		p, ok := PickGather(GatherEssence, seqRoll(tc.roll))
		if !ok || p.Drop.Item != tc.item || p.Length != 0 {
			t.Fatalf("roll %d = %+v, want item %d", tc.roll, p, tc.item)
		}
	}
	// Fish: roll 0 picks Pond Minnow, then the length roll 0 gives 10 cm.
	p, ok := PickGather(GatherFish, seqRoll(0, 0))
	if !ok || p.Drop.Item != 110000 || p.Length != 10 {
		t.Fatalf("fish = %+v", p)
	}
	// Rarest fish: roll 99 picks Rainbow Koi, and length roll 60 gives 70 cm.
	p, _ = PickGather(GatherFish, seqRoll(99, 60))
	if p.Drop.Item != 132000 || p.Length != 70 {
		t.Fatalf("rare fish = %+v", p)
	}
}

// TestGatherCapsOwnAndFriendGardens checks the own cap (20 gathers, all kinds together), the friend
// cap (5 per Garden) and the three-Garden limit, and that the caps reset at 06:00 JST.
func TestGatherCapsOwnAndFriendGardens(t *testing.T) {
	gatherAccount(t, "9901", 0, nil)
	now := jstAt(11, 12, 0)
	kinds := []GatherKind{GatherEssence, GatherOre, GatherFish}
	for i := range GatherOwnCap {
		if _, _, ok := GatherCredit("9901", "9901", kinds[i%3], 210000, now); !ok {
			t.Fatalf("own gather %d refused", i+1)
		}
	}
	if lim, _, ok := GatherCredit("9901", "9901", GatherOre, 310000, now); ok || lim.Rest != 0 || lim.Max != GatherOwnCap {
		t.Fatalf("own gather past cap = %+v, ok %v", lim, ok)
	}
	if lim := GatherStatus("9901", "9901", now); lim.Rest != 0 {
		t.Fatalf("own status after cap = %+v", lim)
	}

	for _, owner := range []string{"7001", "7002", "7003"} {
		for range GatherFriendCap {
			if _, _, ok := GatherCredit("9901", owner, GatherOre, 310000, now); !ok {
				t.Fatalf("friend %s gather refused", owner)
			}
		}
		if _, _, ok := GatherCredit("9901", owner, GatherOre, 310000, now); ok {
			t.Fatalf("friend %s gather past its cap was paid", owner)
		}
	}
	// A fourth friend Garden is refused outright, and the three used stay at their cap.
	if lim := GatherStatus("9901", "7004", now); lim.Rest != 0 || lim.Max != GatherFriendCap {
		t.Fatalf("fourth friend garden = %+v, want refused", lim)
	}
	if lim := GatherStatus("9901", "7002", now); lim.Rest != 0 {
		t.Fatalf("used friend garden = %+v", lim)
	}

	// The next game day starts at 06:00 JST: the own Garden and friend Gardens are open again.
	next := jstAt(12, 6, 0)
	if lim := GatherStatus("9901", "9901", next); lim.Rest != GatherOwnCap {
		t.Fatalf("own after reset = %+v", lim)
	}
	if lim := GatherStatus("9901", "7004", next); lim.Rest != GatherFriendCap {
		t.Fatalf("friend garden after reset = %+v", lim)
	}
}

// TestGatherCreditRewardsAndLedger checks one gather: the material into the Bag, +3 Smiles, +2 Gems,
// and one ledger line per currency with the gather reason.
func TestGatherCreditRewardsAndLedger(t *testing.T) {
	dir := gatherAccount(t, "9902", 100, nil)
	now := jstAt(11, 12, 0)
	lim, gems, ok := GatherCredit("9902", "9902", GatherEssence, 210000, now)
	if !ok || gems != 2 || lim.Rest != GatherOwnCap-1 {
		t.Fatalf("credit = %+v gems %d ok %v", lim, gems, ok)
	}
	g, s, bag, rec := gatherState(t, "9902")
	if g != 102 || s != GatherSmiles || len(bag) != 1 || bag[0].Item != 210000 || bag[0].Count != 1 || bag[0].Updated == 0 {
		t.Fatalf("gems %d smiles %d bag %+v", g, s, bag)
	}
	if rec == nil || rec.Own != 1 || rec.Gems != 2 || rec.Day != AttendDayKey(now) {
		t.Fatalf("record = %+v", rec)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []store.LedgerLine
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var line store.LedgerLine
		if err := json.Unmarshal([]byte(l), &line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	want := map[string]int64{"gems": 2, "smiles": 3}
	if len(lines) != len(want) {
		t.Fatalf("ledger lines = %+v", lines)
	}
	for _, l := range lines {
		if want[l.Currency] != l.Delta || l.Reason != "gather:essence:20261011" {
			t.Fatalf("ledger line = %+v", l)
		}
	}

	// A second gather of the same material stacks it.
	if _, _, ok := GatherCredit("9902", "9902", GatherEssence, 210000, now); !ok {
		t.Fatal("second gather refused")
	}
	if _, _, bag, _ := gatherState(t, "9902"); len(bag) != 1 || bag[0].Count != 2 {
		t.Fatalf("stack after two gathers = %+v", bag)
	}
}

// TestGatherGemCapThirtyPerDay checks the Gem bonus stops at 30 Gems a day while Smiles keep paying,
// and that the next day's bonus starts again.
func TestGatherGemCapThirtyPerDay(t *testing.T) {
	gatherAccount(t, "9903", 0, nil)
	now := jstAt(11, 12, 0)
	for i := range GatherOwnCap {
		_, gems, ok := GatherCredit("9903", "9903", GatherFish, 110000, now)
		if !ok {
			t.Fatalf("gather %d refused", i+1)
		}
		want := int64(GatherGems)
		if i >= GatherGemsCap/GatherGems {
			want = 0
		}
		if gems != want {
			t.Fatalf("gather %d paid %d Gems, want %d", i+1, gems, want)
		}
	}
	g, s, bag, rec := gatherState(t, "9903")
	if g != GatherGemsCap || s != GatherOwnCap*GatherSmiles || bag[0].Count != GatherOwnCap {
		t.Fatalf("gems %d smiles %d bag %+v", g, s, bag)
	}
	if rec == nil || rec.Gems != GatherGemsCap {
		t.Fatalf("record = %+v", rec)
	}
	if _, gems, ok := GatherCredit("9903", "9903", GatherFish, 110000, jstAt(12, 6, 0)); !ok || gems != GatherGems {
		t.Fatalf("next day gems = %d ok %v", gems, ok)
	}
}

// TestGatherSaveFailureRollsBack checks that a failed save leaves the Gems, Smiles, Bag and record
// as they were.
func TestGatherSaveFailureRollsBack(t *testing.T) {
	gatherAccount(t, "9904", 5, nil)
	// A regular file in the place of the accounts directory makes the save fail.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json")
	store.AccountsMu.Unlock()
	if _, _, ok := GatherCredit("9904", "9904", GatherOre, 310000, jstAt(11, 12, 0)); ok {
		t.Fatal("gather paid despite a failed save")
	}
	g, s, bag, rec := gatherState(t, "9904")
	if g != 5 || s != 0 || len(bag) != 0 || rec != nil {
		t.Fatalf("after failed save: gems %d smiles %d bag %+v record %+v", g, s, bag, rec)
	}
}

// TestGardenBagDelete checks the stack removal and that a missing stack is refused.
func TestGardenBagDelete(t *testing.T) {
	gatherAccount(t, "9905", 0, &store.GatherDay{})
	now := jstAt(11, 12, 0)
	GatherCredit("9905", "9905", GatherEssence, 210000, now)
	GatherCredit("9905", "9905", GatherOre, 310000, now)
	if !GardenBagDelete("9905", 210000) {
		t.Fatal("delete of an owned stack refused")
	}
	if GardenBagDelete("9905", 210000) {
		t.Fatal("delete of a removed stack accepted")
	}
	if _, _, bag, _ := gatherState(t, "9905"); len(bag) != 1 || bag[0].Item != 310000 {
		t.Fatalf("bag after delete = %+v", bag)
	}
}

// TestGardenBagChangeIsCopyOnWrite checks that a change never edits the slice it was given, so a
// rollback keeps the old bag, and that a stack reaching zero is removed.
func TestGardenBagChangeIsCopyOnWrite(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	old := []store.GardenBagStack{{Item: 210000, Count: 1}, {Item: 110000, Count: 2}}
	next := gardenBagChange(old, 210000, 1, now)
	if old[0].Count != 1 || next[0].Count != 2 || next[0].Updated != now.UnixMilli() {
		t.Fatalf("old %+v next %+v", old, next)
	}
	gone := gardenBagChange(old, 210000, -1, now)
	if len(gone) != 1 || gone[0].Item != 110000 || old[0].Count != 1 {
		t.Fatalf("removed %+v old %+v", gone, old)
	}
	if got := gardenBagChange(old, 330000, -1, now); len(got) != 2 {
		t.Fatalf("missing stack changed the bag: %+v", got)
	}
}

// TestGatherImageRoute checks GET /cherry/garden/items/item<id>.png: a PNG for a material, 404 for
// anything else. The icon is read from the archive, so the test skips without it.
func TestGatherImageRoute(t *testing.T) {
	src := filepath.Join(thumbs.DpItemRoot, "craftitem", "garden01", "items", "item110000.png")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("archive icon not present: %v", err)
	}
	rec := httptest.NewRecorder()
	HandleGatherImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/garden/items/item110000.png", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() == 0 {
		t.Fatalf("material icon = %d %q len %d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	for _, path := range []string{"/cherry/garden/items/item999999.png", "/cherry/garden/items/itemx.png", "/cherry/garden/items/item110000.jpg"} {
		rec := httptest.NewRecorder()
		HandleGatherImage(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, rec.Code)
		}
	}
	if GatherImageURL(110000) != "cherry/garden/items/item110000.png" {
		t.Fatalf("relative URL = %q", GatherImageURL(110000))
	}
}

// TestGatherPersistRoundTrip checks that Smiles, the Garden bag and the gathering record survive a
// save and a load of accounts.json.
func TestGatherPersistRoundTrip(t *testing.T) {
	gatherAccount(t, "9906", 0, nil)
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("9906")
	acc.Smiles = 12
	acc.GardenBag = []store.GardenBagStack{{Item: 330000, Count: 4, Updated: 1234}, {Item: 110000, Count: 1, Updated: 5678}}
	acc.Gather = &store.GatherDay{Day: 20470, Own: 7, Gardens: map[string]int64{"7001": 2}, Gems: 14}
	store.NextAvatarID = 9999
	err := store.SaveAccountsLocked()
	path := store.AccountStorePath
	store.AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LoadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	g, s, bag, rec := gatherState(t, "9906")
	if g != 0 || s != 12 || len(bag) != 2 || bag[0] != (store.GardenBagStack{Item: 330000, Count: 4, Updated: 1234}) || bag[1].Item != 110000 {
		t.Fatalf("reloaded smiles %d bag %+v", s, bag)
	}
	if rec == nil || rec.Day != 20470 || rec.Own != 7 || rec.Gardens["7001"] != 2 || rec.Gems != 14 {
		t.Fatalf("reloaded record = %+v", rec)
	}
}
