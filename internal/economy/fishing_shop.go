package economy

import (
	"image"
	"image/color"
	"image/png"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"cherry/internal/store"
)

// Square fish shop (Economy Phase D slice 3). The shop sells fishing rods and bait for EARNED Gems
// only: the client's PriceType 1 is Gem, and no billing code is ever sent. Each item is bought
// once and kept in accounts.json (store.FishingGear). One rod and one bait can be in use; they add
// rare-catch chance points. The daily Gem cap (daily_cap) is not changed by gear.
// Wire notes: %TEMP%\opencode\cherry\phaseD-20261011\gathering.txt, "Resolution (shop, book, bag, boss)".

// Fish gear kinds. They are the client's BagType (cr 88 / cr 91 / cr 90) and InvenItem.ItemType
// (NaFishingItemController::RefreshUI: 1 = bait, 2 = rod).
const (
	FishKindBait = 1
	FishKindRod  = 2
)

// FishingItem is one shop item (fishing.json shop[]). Price is in Gems. RareBonus adds percentage
// points to the rare catch chance while the item is in use. IDs are Cherry's own (not from the archive).
type FishingItem struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	KindName  string `json:"kind"`
	Kind      int64  `json:"-"` // FishKindRod or FishKindBait, from KindName at load
	Price     int64  `json:"price"`
	RareBonus int64  `json:"rare_bonus"`
}

// FishBuyStatus is the outcome of a shop purchase or an equip; the caller maps it to a reply.
type FishBuyStatus int

const (
	FishBuyOK      FishBuyStatus = iota
	FishBuyUnknown               // not a shop item, or not owned (equip)
	FishBuyOwned                 // already in the bag
	FishBuyGems                  // not enough Gems
	FishBuyFailed                // the save failed; nothing changed
)

// FishingShopItems lists the shop rows in display order.
func FishingShopItems() []FishingItem { return slices.Clone(ActiveFishing().Shop) }

// FishingItemByID returns the shop item with id.
func FishingItemByID(id int64) (FishingItem, bool) {
	for _, it := range ActiveFishing().Shop {
		if it.ID == id {
			return it, true
		}
	}
	return FishingItem{}, false
}

// fishingGearOf returns a copy of acc's gear (zero when none), so callers can change it and
// store the copy back as a new record.
func fishingGearOf(acc *store.Account) store.FishingGear {
	if acc.Gear == nil {
		return store.FishingGear{}
	}
	g := *acc.Gear
	g.Owned = slices.Clone(g.Owned)
	return g
}

// BuyFishingItem charges id's price in Gems and adds it to aid's bag. Gems are the only currency
// this path touches: Cash, billing and the coin charge pages are never involved.
func BuyFishingItem(aid string, id int64) (FishBuyStatus, int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return FishBuyFailed, 0
	}
	item, ok := FishingItemByID(id)
	if !ok {
		return FishBuyUnknown, acc.Gems
	}
	gear := fishingGearOf(acc)
	if slices.Contains(gear.Owned, id) {
		return FishBuyOwned, acc.Gems
	}
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -item.Price}, "fishshop:"+strconv.FormatInt(id, 10))
	if err != nil {
		*acc = prev
		return FishBuyGems, acc.Gems
	}
	gear.Owned = append(gear.Owned, id)
	// The bag cannot be shown (square_fishing.go, bag 88), so a purchase equips the item when it
	// beats the one in use (or nothing is in use). Gear changes only the rare chance.
	if item.Kind == FishKindRod {
		if cur, _ := FishingItemByID(gear.Rod); gear.Rod == 0 || item.RareBonus > cur.RareBonus {
			gear.Rod = id
		}
	} else if cur, _ := FishingItemByID(gear.Bait); gear.Bait == 0 || item.RareBonus > cur.RareBonus {
		gear.Bait = id
	}
	acc.Gear = &gear
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		return FishBuyFailed, prev.Gems
	}
	store.FlushLedgerLocked(lines)
	return FishBuyOK, acc.Gems
}

// FishingBag returns aid's owned items of kind and the id in use for that kind (0 = none).
func FishingBag(aid string, kind int64) ([]FishingItem, int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return nil, 0
	}
	gear := fishingGearOf(acc)
	var owned []FishingItem
	for _, id := range gear.Owned {
		if it, ok := FishingItemByID(id); ok && it.Kind == kind {
			owned = append(owned, it)
		}
	}
	inUse := gear.Rod
	if kind == FishKindBait {
		inUse = gear.Bait
	}
	return owned, inUse
}

// FishingGearBonus is the rare-chance bonus (percentage points) of the rod and bait in use.
func FishingGearBonus(aid string) int64 {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil || acc.Gear == nil {
		return 0
	}
	var bonus int64
	for _, id := range []int64{acc.Gear.Rod, acc.Gear.Bait} {
		if it, ok := FishingItemByID(id); ok && slices.Contains(acc.Gear.Owned, id) {
			bonus += it.RareBonus
		}
	}
	return bonus
}

// FishingItemImageURL is the picture for a shop or bag item, served by HandleFishingItemImage at
// /cherry/fishing/<id>.png. It is RELATIVE on purpose: the client's item-image loader
// (NaDataInvokerProtocol::staticResourceURLWithPath 0x1ac590c, used by NaDispatcherDiary::
// ReqDownloadImage_Diary 0x1bb6c8c for image type 7) appends the path to the static base, which
// ends in "/". An absolute URL was requested as ".../https://play-static...", and a leading "/"
// doubles the slash (307). Fish card pictures are different: they use the absolute URL.
func FishingItemImageURL(id int64) string {
	return "cherry/fishing/" + strconv.FormatInt(id, 10) + ".png"
}

// HandleFishingItemImage serves GET /cherry/fishing/<id>.png. The archive has no rod or bait art
// (the stock icons come from the item server), so each item gets a plain drawn icon.
// ponytail: drawn shapes, not art; swap in real icons if the archive ever has them.
func HandleFishingItemImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cherry/fishing/"), ".png"), 10, 64)
	it, ok := FishingItemByID(id)
	if err != nil || !ok || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	const n = 128
	out := image.NewNRGBA(image.Rect(0, 0, n, n))
	rim := color.NRGBA{200, 170, 140, 255}
	fill := color.NRGBA{255, 250, 240, 255}
	ink := color.NRGBA{120, 80, 45, 255}
	if it.Kind == FishKindBait {
		ink = color.NRGBA{90, 170, 80, 255}
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			c := fill
			if x < 3 || y < 3 || x >= n-3 || y >= n-3 {
				c = rim
			}
			out.SetNRGBA(x, y, c)
		}
	}
	for t := 0; t < n; t++ { // a thick diagonal: the rod shaft, or a bait trail
		for d := -4; d <= 4; d++ {
			out.SetNRGBA(t, n-1-t+d, ink)
		}
	}
	if it.Kind == FishKindBait { // the bait itself: a filled blob at the tip
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				if (x-92)*(x-92)+(y-36)*(y-36) <= 18*18 {
					out.SetNRGBA(x, y, ink)
				}
			}
		}
	}
	w.Header().Set("Content-Type", "image/png")
	_ = png.Encode(w, out)
}
