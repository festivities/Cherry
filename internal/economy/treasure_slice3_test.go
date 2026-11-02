package economy

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"cherry/data"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// treasureOwner installs a non-lab account aid that owns the given item codes (its inventory), for buff and wear tests.
func treasureOwner(t *testing.T, aid string, owned ...string) {
	t.Helper()
	acc := &store.Account{AccessToken: "t-" + aid, SessionKey: "sk", Aid: aid, Name: "Owner", Gender: "FEMALE",
		InventoryCodes: owned}
	testutil.InstallAccounts(t, map[string]*store.Account{"t-" + aid: acc})
	testutil.SetLab(t, aid, false)
}

// treasureWearer installs a non-lab account aid that owns the owned codes and has worn as its saved outfit (the buffs worn are
// the pool items among them), for the buff tests that start from a worn buff.
func treasureWearer(t *testing.T, aid string, owned, worn []string) {
	t.Helper()
	acc := &store.Account{AccessToken: "t-" + aid, SessionKey: "sk", Aid: aid, Name: "Owner", Gender: "FEMALE",
		InventoryCodes: owned, ItemCodes: worn}
	testutil.InstallAccounts(t, map[string]*store.Account{"t-" + aid: acc})
	testutil.SetLab(t, aid, false)
}

// treasureSaved is the outfit aid's account saves (its equipped codes).
func treasureSaved(aid string) []string {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return slices.Clone(store.AccountByAidLocked(aid).ItemCodes)
}

// treasureDieAndPick makes the die show die and every other draw return pick, for one test (buff squares draw one pick).
func treasureDieAndPick(t *testing.T, die, pick int) {
	t.Helper()
	prev := TreasureRandIntn
	TreasureRandIntn = func(n int) int {
		if n == 6 {
			return die - 1
		}
		return pick
	}
	t.Cleanup(func() { TreasureRandIntn = prev })
}

// treasureBuffCap loads the default treasure file with buff.cap set to cap, for one test (the cleanup restores the default).
// It edits a copy on disk: the embedded default must never change.
func treasureBuffCap(t *testing.T, cap int64) {
	t.Helper()
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(data.TreasureJSON, &doc); err != nil {
		t.Fatal(err)
	}
	var buff map[string]json.RawMessage
	if err := json.Unmarshal(doc["buff"], &buff); err != nil {
		t.Fatal(err)
	}
	buff["cap"], _ = json.Marshal(cap)
	doc["buff"], _ = json.Marshal(buff)
	raw, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "treasure.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := treasureFile.LoadFrom(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(treasureFile.Reset)
}

// TestTreasureBoostRoundsUp: a credit grows by its percent and rounds up (3 at 40 percent is 5).
func TestTreasureBoostRoundsUp(t *testing.T) {
	for _, c := range []struct{ n, pct, want int64 }{{3, 40, 5}, {1, 10, 2}, {5, 10, 6}, {10, 0, 10}, {3, 0, 3}, {1, 40, 2}, {0, 40, 0}, {100, 40, 140}} {
		if got := treasureBoost(c.n, c.pct); got != c.want {
			t.Fatalf("boost(%d, %d%%) = %d, want %d", c.n, c.pct, got, c.want)
		}
	}
}

// TestTreasureBuffPercentCap: the worn items add up and stop at buff.cap. An item counts only when it is equipped and owned.
func TestTreasureBuffPercentCap(t *testing.T) {
	d := ActiveTreasureData()
	both := &store.Account{InventoryCodes: []string{"CUAH006FG", "CUSH003EY"}, ItemCodes: []string{"CUAH006FG", "CUSH003EY"}}
	if got := treasureBuffPercent(both, d); got != 25 {
		t.Fatalf("both worn = %d%%, want 25", got)
	}
	treasureBuffCap(t, 20)
	if got := treasureBuffPercent(both, ActiveTreasureData()); got != 20 {
		t.Fatalf("both worn under a cap of 20 = %d%%, want 20", got)
	}
	if got := treasureBuffPercent(&store.Account{InventoryCodes: []string{"CUAH008A9"}, ItemCodes: []string{"CUAH008A9"}}, d); got != 0 {
		t.Fatalf("an item outside the pool adds %d%%", got)
	}
	if got := treasureBuffPercent(&store.Account{InventoryCodes: []string{"CUSH003EY"}, ItemCodes: []string{"CUAH006FG"}}, d); got != 0 {
		t.Fatalf("an owned buff that is not equipped, or an equipped one not owned, adds %d%%", got)
	}
}

// TestTreasureBuffBoostsCredits: a worn buff grows the coin and the bonus of a roll (1 Jewel at 10 percent is 2, 5 at 10 is 6).
func TestTreasureBuffBoostsCredits(t *testing.T) {
	treasureOwner(t, "1001", "CUAH006FG")
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	if _, err := TreasureBuffWear("1001", []string{"CUAH006FG"}, treasureNow); err != nil {
		t.Fatal(err)
	}
	treasureDie(t, 1)
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil || mv.Cell != TreasureCellCoin || mv.Coins != 2 || mv.View.Jewels != 2 || mv.View.BuffPercent != 10 {
		t.Fatalf("coin roll = %+v, %v; want a coin of 2 Jewels at 10 percent", mv, err)
	}
	treasureDie(t, 2)
	mv, err = TreasureRollDice("1001", treasureNow)
	if err != nil || mv.Cell != TreasureCellBonus || mv.Bonus != 6 || mv.View.Jewels != 8 {
		t.Fatalf("bonus roll = %+v, %v; want a bonus of 6 Jewels (5 at 10 percent)", mv, err)
	}
}

// TestTreasureBuffBoostsGoldKeyJewels: a Mystery Card that pays Jewels pays the boosted amount, and its value shows it.
func TestTreasureBuffBoostsGoldKeyJewels(t *testing.T) {
	treasureWearer(t, "1001", []string{"CUAH006FG"}, []string{"CUAH006FG"})
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 5, Tickets: 10, Charged: treasureNow.UnixMilli(),
		KeyPending: true})
	treasureRandSeq(t, 8, 0) // card 8 of 10 by weight: type 7 (5 to 10 Jewels), value 5
	use, err := TreasureGoldKeyUse("1001", treasureNow)
	if err != nil || use.Card != 7 || use.Value != 6 || use.Move.Coins != 6 || use.Move.View.Jewels != 6 {
		t.Fatalf("card = %+v, %v; want type 7 worth 6 Jewels (5 at 10 percent)", use, err)
	}
}

// TestTreasureBuffBoostsMissionJewels: a mission's Jewels are boosted by the worn buffs when claimed.
func TestTreasureBuffBoostsMissionJewels(t *testing.T) {
	treasureWearer(t, "1001", []string{"CUAH006FG"}, []string{"CUAH006FG"})
	nowMs := treasureNow.UnixMilli()
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: nowMs,
		Missions: store.TreasureList{Set: 1, Posted: nowMs, Slots: []store.TreasureSlot{{Task: 0, Count: 10, State: 1}}}})
	claim, err := TreasureMissionClaim("1001", 101, treasureNow)
	if err != nil || claim.Paid != 4 || claim.View.Jewels != 4 { // the mission pays 3 Jewels; 3 at 10 percent is 4
		t.Fatalf("mission claim = %+v, %v; want 4 Jewels", claim, err)
	}
}

// TestTreasureBuffLandingPicksUnowned: a buff square draws a pool item the account does not own (the first when it owns all),
// and the state follows ownership and wear.
func TestTreasureBuffLandingPicksUnowned(t *testing.T) {
	treasureRing(t, treasureProbeRing)
	land := func(t *testing.T, aid string, pick int) TreasureBuffLanding {
		t.Helper()
		treasureState(t, aid, store.TreasureRecord{Season: treasureSeasonID(), Position: 2, Tickets: 10, Charged: treasureNow.UnixMilli()})
		treasureDieAndPick(t, 1, pick) // the die shows 1: square 3, the buff square
		mv, err := TreasureRollDice(aid, treasureNow)
		if err != nil || mv.Cell != TreasureCellBuff {
			t.Fatalf("roll = %+v, %v; want a buff square", mv, err)
		}
		return mv.Buff
	}
	if got := land(t, "1001", 0); got.Code != "CUAH006FG" || got.Percent != 10 || got.State != "NEW" { // no account: owns none
		t.Fatalf("landing without an account = %+v", got)
	}
	if got := land(t, "1001", 1); got.Code != "CUSH003EY" || got.Percent != 15 || got.State != "NEW" {
		t.Fatalf("second pick = %+v, want CUSH003EY at 15 percent, NEW", got)
	}
	treasureOwner(t, "1001", "CUAH006FG")
	if got := land(t, "1001", 0); got.Code != "CUSH003EY" || got.State != "NEW" {
		t.Fatalf("owning the first = %+v, want the one not owned, NEW", got)
	}
	treasureOwner(t, "1001", "CUAH006FG", "CUSH003EY")
	if got := land(t, "1001", 0); got.Code != "CUAH006FG" || got.State != "INVEN" {
		t.Fatalf("owning both = %+v, want the first, INVEN", got)
	}
	treasureWearer(t, "1001", []string{"CUAH006FG", "CUSH003EY"}, []string{"CUAH006FG"})
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 2, Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 1)
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil || mv.Buff.Code != "CUAH006FG" || mv.Buff.State != "WEAR" {
		t.Fatalf("worn first = %+v, %v; want WEAR", mv.Buff, err)
	}
}

// TestTreasureBuffWearAndList: cs 5 lists the owned buffs; cs 22 saves the outfit it sends (it replaces the saved one), and the worn
// buffs are the pool items in it.
func TestTreasureBuffWearAndList(t *testing.T) {
	treasureOwner(t, "1001", "CUAH006FG", "CUSH003EY", "CUTO0006W")
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	list, err := TreasureBuffList("1001", treasureNow)
	if err != nil || len(list.Items) != 2 || list.Percent != 0 || list.Items[0].Worn {
		t.Fatalf("list = %+v, %v; want both owned, none worn", list, err)
	}
	wear, err := TreasureBuffWear("1001", []string{"CUAH006FG", "CUSH003EY"}, treasureNow)
	if err != nil || wear.Percent != 25 || !wear.Items[0].Worn || !wear.Items[1].Worn {
		t.Fatalf("wear both = %+v, %v; want 25 percent, both worn", wear, err)
	}
	// The client sends the whole outfit and it replaces the saved one: CUAH006FG is taken off, so only CUSH003EY counts.
	outfit, err := TreasureBuffWear("1001", []string{"CUTO0006W", "CUSH003EY"}, treasureNow)
	if err != nil || outfit.Percent != 15 || outfit.Outfit != 2 || outfit.Items[0].Worn || !outfit.Items[1].Worn {
		t.Fatalf("wear outfit = %+v, %v; want only CUSH003EY worn (15 percent) in a 2-code outfit", outfit, err)
	}
	if got := treasureSaved("1001"); !slices.Equal(got, []string{"CUTO0006W", "CUSH003EY"}) {
		t.Fatalf("saved outfit = %v, want the sent outfit", got)
	}
	// Skin and base items are skipped as a Closet save skips them: only CUAH006FG is kept.
	base, err := TreasureBuffWear("1001", []string{"SKN002", "CUHA00004", "CUAH006FG"}, treasureNow)
	if err != nil || base.Percent != 10 || base.Outfit != 1 || !slices.Equal(treasureSaved("1001"), []string{"CUAH006FG"}) {
		t.Fatalf("wear with skin and base = %+v, %v; saved %v", base, err, treasureSaved("1001"))
	}
	// An empty outfit is refused (the client always sends the whole outfit): nothing changes.
	if _, err := TreasureBuffWear("1001", nil, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("empty outfit: %v, want refused", err)
	}
	if got := treasureSaved("1001"); !slices.Equal(got, []string{"CUAH006FG"}) {
		t.Fatalf("saved outfit after the empty one = %v", got)
	}
}

// TestTreasureBuffWearRefusesBadOutfit: an outfit that names an item the account does not own is refused whole, and an outfit
// the species cannot wear (an ANIMAL gets no human hair or face, a human no animal face) is refused too; nothing changes.
func TestTreasureBuffWearRefusesBadOutfit(t *testing.T) {
	treasureWearer(t, "1001", []string{"CUTO0006W", "CUAH006FG", "CAEY0000G"}, []string{"CUTO0006W"})
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	for _, outfit := range [][]string{{"CUTO0006W", "CUNOPE0000"}, {"CUAH006FG", "CAEY0000G"}} {
		if _, err := TreasureBuffWear("1001", outfit, treasureNow); !errors.Is(err, ErrTreasureRefused) {
			t.Fatalf("outfit %v: %v, want refused", outfit, err)
		}
		if got := treasureSaved("1001"); !slices.Equal(got, []string{"CUTO0006W"}) {
			t.Fatalf("outfit %v changed the saved outfit to %v", outfit, got)
		}
	}
	store.AccountsMu.Lock()
	store.AccountByAidLocked("1001").Gender = "ANIMAL"
	store.AccountsMu.Unlock()
	if _, err := TreasureBuffWear("1001", []string{"CUAH006FG", "CUHA00002"}, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("an animal in human hair: %v, want refused", err)
	}
	if _, err := TreasureBuffWear("1001", []string{"CUAH006FG", "CATO000L8"}, treasureNow); err != nil {
		t.Fatalf("an animal in its own base and a pool item: %v", err)
	}
}

// TestTreasureBuffFollowsOutfit: the buff follows the saved outfit: taking the item off ends it, and wearing it again starts it.
// A book saved with a worn list (before the outfit rule) counts only the items still equipped, and the next write drops the list.
func TestTreasureBuffFollowsOutfit(t *testing.T) {
	treasureWearer(t, "1001", []string{"CUAH006FG"}, []string{"CUAH006FG"})
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli(),
		Worn: []string{"CUSH003EY"}})
	view, err := TreasureOpen("1001", treasureNow)
	if err != nil || view.BuffPercent != 10 {
		t.Fatalf("worn by the outfit = %+v, %v; want 10 percent", view, err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Worn != nil {
		t.Fatalf("legacy worn list kept after the write: %v", rec.Worn)
	}
	if _, err := TreasureBuffWear("1001", []string{"CUTO0006W"}, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("an outfit not owned: %v, want refused", err)
	}
	store.AccountsMu.Lock()
	store.AccountByAidLocked("1001").InventoryCodes = []string{"CUAH006FG", "CUTO0006W"}
	store.AccountsMu.Unlock()
	if _, err := TreasureBuffWear("1001", []string{"CUTO0006W"}, treasureNow); err != nil {
		t.Fatal(err)
	}
	if view, err := TreasureOpen("1001", treasureNow); err != nil || view.BuffPercent != 0 {
		t.Fatalf("taken off = %+v, %v; want no buff", view, err)
	}
	list, err := TreasureBuffList("1001", treasureNow)
	if err != nil || len(list.Items) != 1 || list.Items[0].Worn || list.Items[0].Code != "CUAH006FG" {
		t.Fatalf("list after the take-off = %+v, %v; want CUAH006FG owned, not worn", list, err)
	}
	if _, err := TreasureBuffWear("1001", []string{"CUTO0006W", "CUAH006FG"}, treasureNow); err != nil {
		t.Fatal(err)
	}
	if view, err := TreasureOpen("1001", treasureNow); err != nil || view.BuffPercent != 10 {
		t.Fatalf("worn again = %+v, %v; want 10 percent", view, err)
	}
}

// TestTreasureBuffWearPersistsOutfit: cs 22 saves the outfit to accounts.json, as a Closet save does.
func TestTreasureBuffWearPersistsOutfit(t *testing.T) {
	treasureOwner(t, "1001", "CUAH006FG", "CUTO0006W")
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	path := filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Lock()
	store.AccountStorePath = path // restored by the install's cleanup
	store.AccountsMu.Unlock()
	if _, err := TreasureBuffWear("1001", []string{"CUTO0006W", "CUAH006FG"}, treasureNow); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Accounts map[string]struct {
			Aid       string   `json:"aid"`
			ItemCodes []string `json:"itemCodes"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	for _, acc := range saved.Accounts {
		if acc.Aid == "1001" && slices.Equal(acc.ItemCodes, []string{"CUTO0006W", "CUAH006FG"}) {
			return
		}
	}
	t.Fatalf("accounts.json does not hold the saved outfit: %s", raw)
}

// TestTreasureBuffSavedByClosetRule: a Closet save (store.SetAvatarOutfitLocked) that wears the pool item starts the buff, and one
// that takes it off ends it, as cs 22 does; the web test drives the real handler.
func TestTreasureBuffSavedByClosetRule(t *testing.T) {
	treasureWearer(t, "1001", []string{"CUAH006FG"}, nil)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	save := func(outfit []string) {
		store.AccountsMu.Lock()
		defer store.AccountsMu.Unlock()
		if err := store.SetAvatarOutfitLocked(store.AccountByAidLocked("1001"), outfit); err != nil {
			t.Fatal(err)
		}
	}
	save([]string{"CUAH006FG"})
	if view, err := TreasureOpen("1001", treasureNow); err != nil || view.BuffPercent != 10 {
		t.Fatalf("after the Closet wear = %+v, %v; want 10 percent", view, err)
	}
	save(nil)
	if view, err := TreasureOpen("1001", treasureNow); err != nil || view.BuffPercent != 0 {
		t.Fatalf("after the Closet take-off = %+v, %v; want 0 percent", view, err)
	}
}

// TestTreasureBuffWearNeedsOwnership: a buff the account does not own cannot be worn, and a worn one stops counting once it is
// gone from the inventory (sold back).
func TestTreasureBuffWearNeedsOwnership(t *testing.T) {
	treasureOwner(t, "1001", "CUAH006FG")
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	if _, err := TreasureBuffWear("1001", []string{"CUSH003EY"}, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("wear an unowned buff: %v, want refused", err)
	}
	if _, err := TreasureBuffWear("1001", []string{"CUAH006FG"}, treasureNow); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	store.Accounts["t-1001"].InventoryCodes = []string{} // nil would fall back to the equipped codes (AccountInventoryCodes)
	store.AccountsMu.Unlock()
	view, err := TreasureOpen("1001", treasureNow)
	if err != nil || view.BuffPercent != 0 {
		t.Fatalf("after the sale = %+v, %v; want the worn buff gone", view, err)
	}
}

// TestTreasureMiniGamePlay: the bet takes Jewels and the allowance, and the points are bet × 10 × the cup drawn (one of the first
// three multiples). A bet beyond the Jewels, the allowance or the square, or with no allowance left, is refused.
func TestTreasureMiniGamePlay(t *testing.T) {
	treasureRing(t, treasureProbeRing)
	mini := func(pos int, jewels, left int64) store.TreasureRecord {
		return store.TreasureRecord{Season: treasureSeasonID(), Position: pos, Tickets: 10, Charged: treasureNow.UnixMilli(),
			Jewels: jewels, MiniGameLeft: left}
	}
	for _, c := range []struct {
		name string
		rec  store.TreasureRecord
		bet  int64
	}{
		{"bet zero", mini(6, 5, 4), 0},
		{"bet above the allowance", mini(6, 5, 4), 5},
		{"bet above the Jewels", mini(6, 3, 4), 4},
		{"no allowance left", mini(6, 5, 0), 1},
		{"not on the mini-game square", mini(5, 5, 4), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			treasureState(t, "1001", c.rec)
			if _, err := TreasureMiniGamePlay("1001", c.bet, treasureNow); !errors.Is(err, ErrTreasureRefused) {
				t.Fatalf("bet %d: %v, want refused", c.bet, err)
			}
			if got, _ := store.TreasureRecordOf("1001"); got.Jewels != c.rec.Jewels || got.MiniGameLeft != c.rec.MiniGameLeft || got.Points != 0 {
				t.Fatalf("refused bet changed the board: %+v", got)
			}
		})
	}
	for i, want := range []int64{1, 2, 3} {
		t.Run("cup", func(t *testing.T) {
			treasureState(t, "1001", mini(6, 5, 4))
			treasureRandSeq(t, i)
			res, err := TreasureMiniGamePlay("1001", 4, treasureNow)
			if err != nil || res.Multiple != want || res.Added != 4*10*want || res.View.Points != 4*10*want {
				t.Fatalf("cup %d = %+v, %v; want multiple %d and %d points", i, res, err, want, 4*10*want)
			}
			if res.View.Jewels != 1 || res.View.MiniGameLeft != 0 || res.View.Position != 6 {
				t.Fatalf("board after the game = %+v, want 1 Jewel, no allowance, still on 6", res.View)
			}
		})
	}
}

// TestTreasureMiniGameAllowanceClears: landing on the mini-game square allows its max bet; any later move clears the allowance.
func TestTreasureMiniGameAllowanceClears(t *testing.T) {
	treasureRing(t, treasureProbeRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 6) // square 0 to 6, the mini-game
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil || mv.Cell != TreasureCellMiniGame || mv.View.MiniGameLeft != 20 {
		t.Fatalf("landing = %+v, %v; want an allowance of 20", mv.View, err)
	}
	treasureDie(t, 1)
	mv, err = TreasureRollDice("1001", treasureNow)
	if err != nil || mv.View.MiniGameLeft != 0 {
		t.Fatalf("the next roll left the allowance: %+v, %v", mv.View, err)
	}
}

// TestTreasureSecretQuestLifecycle: a landing on a secret quest square starts its quest; cs 26 enables it on the square; the Gacha
// pulls count while it is enabled; the goal makes it done; cs 25 pays the tickets on the square and ends the quest.
func TestTreasureSecretQuestLifecycle(t *testing.T) {
	treasureRing(t, treasureProbeRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	treasureDie(t, 4) // square 0 to 4, the secret quest square
	mv, err := TreasureRollDice("1001", treasureNow)
	if err != nil || mv.Cell != TreasureCellSpecial || mv.View.Secret == nil || mv.View.Secret.Square != 4 || mv.View.Secret.Goal != 1 ||
		mv.View.Secret.Enabled || mv.View.Secret.Done {
		t.Fatalf("landing = %+v, %v; want a quest on 4, goal 1, not enabled", mv.View.Secret, err)
	}
	if _, _, err := TreasureSecretClaim("1001", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("claim before the goal: %v, want refused", err)
	}
	view, err := TreasureSecretEnable("1001", treasureNow)
	if err != nil || view.Secret == nil || !view.Secret.Enabled {
		t.Fatalf("enable = %+v, %v; want enabled", view.Secret, err)
	}
	TreasureGachaPulled("1001", 1)
	if rec, _ := store.TreasureRecordOf("1001"); rec.Secret == nil || rec.Secret.Count != 1 || !rec.Secret.Done {
		t.Fatalf("after one pull = %+v, want count 1 and done", rec.Secret)
	}
	paid, view, err := TreasureSecretClaim("1001", treasureNow)
	if err != nil || paid != 3 || view.Tickets != 12 || view.Secret != nil {
		t.Fatalf("claim = %d, %+v, %v; want 3 tickets (9 + 3 = 12) and no quest", paid, view, err)
	}
	if _, _, err := TreasureSecretClaim("1001", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("second claim: %v, want refused", err)
	}
}

// TestTreasureSecretQuestRefusals: enabling needs the token on the quest's square; a quest of another square does nothing.
func TestTreasureSecretQuestRefusals(t *testing.T) {
	treasureRing(t, treasureProbeRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 0, Tickets: 10, Charged: treasureNow.UnixMilli(),
		Secret: &store.TreasureSecret{Square: 4, Goal: 1}})
	if _, err := TreasureSecretEnable("1001", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("enable off the square: %v, want refused", err)
	}
	TreasureGachaPulled("1001", 1)
	if rec, _ := store.TreasureRecordOf("1001"); rec.Secret.Count != 0 || rec.Secret.Done {
		t.Fatalf("a pull counted without an enabled quest: %+v", rec.Secret)
	}
}

// TestTreasureSecretGachaCountCaps: the pulls add up to the goal and stop there.
func TestTreasureSecretGachaCountCaps(t *testing.T) {
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Secret: &store.TreasureSecret{Square: 4, Enabled: true, Goal: 3}})
	TreasureGachaPulled("1001", 2)
	if rec, _ := store.TreasureRecordOf("1001"); rec.Secret.Count != 2 || rec.Secret.Done {
		t.Fatalf("after two pulls = %+v", rec.Secret)
	}
	TreasureGachaPulled("1001", 10)
	if rec, _ := store.TreasureRecordOf("1001"); rec.Secret.Count != 3 || !rec.Secret.Done {
		t.Fatalf("after ten more = %+v, want the goal 3 and done", rec.Secret)
	}
}

// TestTreasureSecretLandRules: a landing keeps the quest of its square, replaces an unfinished quest of another square, and keeps a
// done quest until it is claimed.
func TestTreasureSecretLandRules(t *testing.T) {
	d := ActiveTreasureData()
	r := store.TreasureRecord{Secret: &store.TreasureSecret{Square: 4, Goal: 1, Count: 1}}
	treasureSecretLand(&r, d, 4)
	if r.Secret.Square != 4 || r.Secret.Count != 1 {
		t.Fatalf("landing on the same square = %+v, want the quest kept", r.Secret)
	}
	treasureSecretLand(&r, d, 9)
	if r.Secret.Square != 9 || r.Secret.Count != 0 || r.Secret.Goal != d.Special.GoalPulls {
		t.Fatalf("landing on another square = %+v, want a fresh quest on 9", r.Secret)
	}
	r.Secret.Done = true
	treasureSecretLand(&r, d, 12)
	if r.Secret.Square != 9 {
		t.Fatalf("a done quest was replaced: %+v", r.Secret)
	}
}

// TestTreasureSecretCountsGachaPulls: the Gacha Shop's pulls (single and ten) count toward the enabled secret quest of the buyer.
func TestTreasureSecretCountsGachaPulls(t *testing.T) {
	p := gachaFixture(t)
	gachaAccount(t, 5000, []string{"CUHA00001"}, "RUWA000HR")
	gachaLedgerDir(t)
	treasureState(t, "9601", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli(),
		Secret: &store.TreasureSecret{Square: 4, Enabled: true, Goal: 11}})
	gachaScript(t, pullVals(t, p, "CUTO00001")...)
	if rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "100"), "gtok"); rec.Code != 200 {
		t.Fatalf("single pull = %d %s", rec.Code, rec.Body)
	}
	if rec, _ := store.TreasureRecordOf("9601"); rec.Secret.Count != 1 || rec.Secret.Done {
		t.Fatalf("after one pull = %+v", rec.Secret)
	}
	picks := []string{"CUTO00001", "CUTO00001", "CUTO00001", "CUHA00001", "RUWA000HR", "RUTD00005", "CUHA00001", "RUWA000HR", "RUTD00005", "CUTO00001"}
	var vals []int
	for _, c := range picks {
		vals = append(vals, pullVals(t, p, c)...)
	}
	vals = append(vals, gachaRareIndex(p, "CUON004TV"))
	gachaScript(t, vals...)
	if rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/multi", gachaBody("1001", "900"), "gtok"); rec.Code != 200 {
		t.Fatalf("ten pull = %d %s", rec.Code, rec.Body)
	}
	if rec, _ := store.TreasureRecordOf("9601"); rec.Secret.Count != 11 || !rec.Secret.Done {
		t.Fatalf("after ten more pulls = %+v, want 11 and done", rec.Secret)
	}
}

// TestTreasureBuffItemsAreGachaPulls: the buff items are clothing with art, so Cherry's gacha pool already holds them and a pull
// can grant them (no change to gacha.json).
func TestTreasureBuffItemsAreGachaPulls(t *testing.T) {
	p := gachaPoolNow()
	pool := append(gachaCodes(p.normal), gachaCodes(p.rare)...)
	for _, it := range ActiveTreasureData().Buff.Items {
		if !slices.Contains(pool, it.Code) {
			t.Fatalf("buff item %s is not in the gacha pool", it.Code)
		}
	}
}
