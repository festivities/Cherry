package economy

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
	if got := ChooseFishWithBonus(rolls(10, 0), 1); !got.Rare || got.ID != fishRare[0].ID {
		t.Fatalf("roll 10 with +1 = %+v, want rare %d", got, fishRare[0].ID)
	}
}

// TestFishingShopBuyAutoEquips: a purchase puts the item in use when it beats the one in use,
// the bonus follows, owned items stay owned, and an unknown or repeated item is refused without a
// charge. The bag is never shown, so there is no equip step.
func TestFishingShopBuyAutoEquips(t *testing.T) {
	fishAccount(t, "9931", 1000, nil)
	if st, gems := BuyFishingItem("9931", 9001); st != FishBuyOK || gems != 850 { // Square Rod, 150
		t.Fatalf("buy rod = (%d, %d)", st, gems)
	}
	if st, gems := BuyFishingItem("9931", 9002); st != FishBuyOK || gems != 400 { // Pro Rod, 450: better, replaces
		t.Fatalf("buy pro rod = (%d, %d)", st, gems)
	}
	if st, gems := BuyFishingItem("9931", 9101); st != FishBuyOK || gems != 360 { // Worm Bait, 40
		t.Fatalf("buy bait = (%d, %d)", st, gems)
	}
	if st, gems := BuyFishingItem("9931", 9102); st != FishBuyOK || gems != 240 { // Golden Bait, 120: better, replaces
		t.Fatalf("buy golden bait = (%d, %d)", st, gems)
	}
	if st, gems := BuyFishingItem("9931", 9999); st != FishBuyUnknown || gems != 240 {
		t.Fatalf("unknown item = (%d, %d)", st, gems)
	}
	if st, _ := BuyFishingItem("9931", 9002); st != FishBuyOwned {
		t.Fatalf("second pro rod = %d, want owned", st)
	}
	if bonus := FishingGearBonus("9931"); bonus != 6+2 {
		t.Fatalf("bonus = %d, want 8", bonus)
	}
	owned, inUse := FishingBag("9931", FishKindRod)
	if len(owned) != 2 || inUse != 9002 {
		t.Fatalf("rods owned %d in use %d", len(owned), inUse)
	}
	owned, inUse = FishingBag("9931", FishKindBait)
	if len(owned) != 2 || inUse != 9102 {
		t.Fatalf("bait owned %d in use %d", len(owned), inUse)
	}
	if gems, _ := fishGems(t, "9931"); gems != 240 {
		t.Fatalf("gems = %d, want 240", gems)
	}
}

// TestFishingShopWorseItemDoesNotReplace: a cheaper, weaker rod bought after a better one stays owned
// but is not put in use.
func TestFishingShopWorseItemDoesNotReplace(t *testing.T) {
	fishAccount(t, "9932", 1000, nil)
	BuyFishingItem("9932", 9002)                                // Pro Rod, +6
	if st, _ := BuyFishingItem("9932", 9001); st != FishBuyOK { // Square Rod, +3
		t.Fatalf("buy = %d", st)
	}
	if _, inUse := FishingBag("9932", FishKindRod); inUse != 9002 {
		t.Fatalf("rod in use = %d, want 9002", inUse)
	}
	if bonus := FishingGearBonus("9932"); bonus != 6 {
		t.Fatalf("bonus = %d, want 6", bonus)
	}
}

func TestFishingItemImageServesPNG(t *testing.T) {
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
