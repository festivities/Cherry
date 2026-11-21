package economy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cherry/internal/store"
)

// TestChooseFishWithBonusRaisesRare: a roll of 10 is a normal cast without gear and a rare one
// with one bonus point (the rare chance is 10% plus the points in use).
func TestChooseFishWithBonusRaisesRare(t *testing.T) {
	rolls := func(vals ...int) func(int) int {
		return func(n int) int {
			v := vals[0]
			vals = vals[1:]
			return v
		}
	}
	if got := ChooseFishWithBonus(rolls(10, 0), 0); got.Rare {
		t.Fatalf("roll 10 without gear = %+v, want normal", got)
	}
	if got := ChooseFishWithBonus(rolls(10, 0), 1); !got.Rare || got.ID != ActiveFishing().rare[0].ID {
		t.Fatalf("roll 10 with +1 = %+v, want rare %d", got, ActiveFishing().rare[0].ID)
	}
}

// TestFishingShopBuyAutoEquipsAndRepeats: bait packs (20 uses each) can be bought repeatedly up
// to MaxBaitUses, auto-equip when stronger, basic items cannot be bought, and worse packs do
// not replace better equipped bait.
func TestFishingShopBuyAutoEquipsAndRepeats(t *testing.T) {
	shop := FishingShopItems()
	if len(shop) != 3 || shop[0].ID != 9102 || shop[1].ID != 9103 || shop[2].ID != 9104 {
		t.Fatalf("shop items = %+v", shop)
	}
	fishAccount(t, "9931", 2000, nil)
	if st, gems, _ := BuyFishingItem("9931", 9001); st != FishBuyUnknown || gems != 2000 {
		t.Fatalf("buy basic rod = (%d, %d)", st, gems)
	}
	if st, gems, _ := BuyFishingItem("9931", 9101); st != FishBuyUnknown || gems != 2000 {
		t.Fatalf("buy basic bait = (%d, %d)", st, gems)
	}
	if st, gems, total := BuyFishingItem("9931", 9102); st != FishBuyOK || gems != 1850 || total != 20 { // B Bait, 150
		t.Fatalf("buy B bait = (%d, %d, %d)", st, gems, total)
	}
	if st, gems, total := BuyFishingItem("9931", 9102); st != FishBuyOK || gems != 1700 || total != 40 { // repeat buy
		t.Fatalf("repeat B bait = (%d, %d, %d)", st, gems, total)
	}
	if st, gems, total := BuyFishingItem("9931", 9104); st != FishBuyOK || gems != 1100 || total != 20 { // S Bait, 600: better, replaces
		t.Fatalf("buy S bait = (%d, %d, %d)", st, gems, total)
	}
	if st, gems, total := BuyFishingItem("9931", 9103); st != FishBuyOK || gems != 800 || total != 20 { // A Bait, 300: worse than S
		t.Fatalf("buy A bait = (%d, %d, %d)", st, gems, total)
	}
	if st, gems, _ := BuyFishingItem("9931", 9999); st != FishBuyUnknown || gems != 800 {
		t.Fatalf("unknown item = (%d, %d)", st, gems)
	}
	if bonus := FishingGearBonus("9931"); bonus != 35 {
		t.Fatalf("bonus = %d, want 35 (S Bait)", bonus)
	}
	entries, inUse := FishingBagEntries("9931", FishKindBait)
	if len(entries) != 4 || inUse != 9104 { // Basic Bait + B + A + S
		t.Fatalf("bait bag entries=%d inUse=%d", len(entries), inUse)
	}
	rods, rodInUse := FishingBag("9931", FishKindRod)
	if len(rods) != 1 || rods[0].ID != 9001 || rodInUse != 9001 {
		t.Fatalf("rods = %+v inUse=%d", rods, rodInUse)
	}
	if gems, _ := fishGems(t, "9931"); gems != 800 {
		t.Fatalf("gems = %d, want 800", gems)
	}
}

func TestFishingBaitCapEquipAndExhaustion(t *testing.T) {
	fishAccount(t, "9932", 1000, nil)
	if st, gems, total := BuyFishingItem("9932", 9102); st != FishBuyOK || gems != 850 || total != 20 {
		t.Fatalf("buy B bait = (%d, %d, %d)", st, gems, total)
	}
	// Equip basic bait (9101) -> bonus drops to 0, then re-equip 9102 -> bonus 10.
	if st, it := EquipFishingItem("9932", 9101, FishKindBait); st != FishBuyOK || it.ID != 9101 {
		t.Fatalf("equip basic bait = (%d, %+v)", st, it)
	}
	if bonus := FishingGearBonus("9932"); bonus != 0 {
		t.Fatalf("bonus with basic bait = %d, want 0", bonus)
	}
	// Wrong kind or unowned item is refused.
	if st, _ := EquipFishingItem("9932", 9102, FishKindRod); st != FishBuyUnknown {
		t.Fatalf("wrong kind equip = %d, want unknown", st)
	}
	if st, _ := EquipFishingItem("9932", 9104, FishKindBait); st != FishBuyUnknown {
		t.Fatalf("unowned equip = %d, want unknown", st)
	}
	if st, _ := EquipFishingItem("9932", 9102, FishKindBait); st != FishBuyOK {
		t.Fatalf("re-equip 9102 = %d", st)
	}
	if bonus := FishingGearBonus("9932"); bonus != 10 {
		t.Fatalf("bonus = %d, want 10", bonus)
	}

	// Consume 19 uses -> not exhausted yet (1 left); 20th -> exhausted, falls back to 9101.
	for i := 0; i < 19; i++ {
		if exhausted := ConsumeFishingBait("9932"); exhausted {
			t.Fatalf("exhausted early at cast %d", i+1)
		}
	}
	if entries, inUse := FishingBagEntries("9932", FishKindBait); len(entries) != 2 || entries[1].Count != 1 || inUse != 9102 {
		t.Fatalf("before final use: entries=%+v inUse=%d", entries, inUse)
	}
	if exhausted := ConsumeFishingBait("9932"); !exhausted {
		t.Fatal("expected exhaustion on 20th cast")
	}
	if entries, inUse := FishingBagEntries("9932", FishKindBait); len(entries) != 1 || entries[0].Item.ID != 9101 || inUse != 9101 {
		t.Fatalf("after exhaustion: entries=%+v inUse=%d", entries, inUse)
	}
	if bonus := FishingGearBonus("9932"); bonus != 0 {
		t.Fatalf("bonus after exhaustion = %d, want 0", bonus)
	}
	// Basic bait is never consumed.
	if exhausted := ConsumeFishingBait("9932"); exhausted {
		t.Fatal("basic bait should never exhaust")
	}

	// Cap at MaxBaitUses (999): 980 + 20 > 999 -> FishBuyOwned.
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("9932")
	acc.Gear = &store.FishingGear{Owned: []int64{9102}, Bait: 9102, BaitUses: map[int64]int64{9102: 980}}
	_ = store.SaveLocked(nil, acc)
	store.AccountsMu.Unlock()
	if st, gems, total := BuyFishingItem("9932", 9102); st != FishBuyOwned || gems != 850 || total != 980 {
		t.Fatalf("over cap buy = (%d, %d, %d), want FishBuyOwned", st, gems, total)
	}
}

func TestFishingItemImageServesPNG(t *testing.T) {
	if got := FishingItemImageURL(9102); got != "cherry/fishing/orig/shop_item_2_1613978375.png" {
		t.Fatalf("shop url = %q", got)
	}
	if got := FishingItemInvenImageURL(9102); got != "cherry/fishing/orig/inven_item_1002_1625560035.png" {
		t.Fatalf("inven url = %q", got)
	}
	if got := FishingItemImageURL(9001); got != "cherry/fishing/orig/inven_item_2001_1625560150.png" {
		t.Fatalf("basic rod fallback url = %q", got)
	}
	w := httptest.NewRecorder()
	HandleFishingItemImage(w, httptest.NewRequest(http.MethodGet, "/cherry/fishing/9101.png", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || w.Body.Len() == 0 {
		t.Fatalf("status %d type %q", w.Code, w.Header().Get("Content-Type"))
	}
	w = httptest.NewRecorder()
	HandleFishingItemImage(w, httptest.NewRequest(http.MethodGet, "/cherry/fishing/1.png", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown item status %d", w.Code)
	}
}
