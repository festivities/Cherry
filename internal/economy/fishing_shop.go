package economy

import (
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"cherry/internal/store"
)

// Square fish shop (Economy Phase D slice 3; updated 2026-11-21 with basic free rod/bait,
// 20-use consumable bait packs, equip, exhaustion, and original gear PNGs). The shop sells
// fishing gear for EARNED Gems only: the fish shop's wire PriceType 1 is Gem (2 = Cash),
// and no billing code is ever sent.

// Fish gear kinds. They are the client's BagType (cr 88 / cr 91 / cr 90) and InvenItem.ItemType
// (NaFishingItemController::RefreshUI: 1 = bait, 2 = rod).
const (
	FishKindBait = 1
	FishKindRod  = 2

	MaxBaitUses int64 = 999
)

// FishingItem is one shop or basic gear item (fishing.json shop[]). Price is in Gems. RareBonus
// adds percentage points to the rare catch chance while the item is in use.
type FishingItem struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	KindName   string `json:"kind"`
	Kind       int64  `json:"-"` // FishKindRod or FishKindBait, from KindName at load
	Basic      bool   `json:"basic,omitempty"`
	Price      int64  `json:"price"`
	Uses       int64  `json:"uses,omitempty"`
	RareBonus  int64  `json:"rare_bonus"`
	ShopImage  string `json:"shop_image,omitempty"`
	InvenImage string `json:"inven_image,omitempty"`
}

// FishingBagEntry is an item in an account's fishing bag together with its stack/use count.
type FishingBagEntry struct {
	Item  FishingItem
	Count int64
}

// FishBuyStatus is the outcome of a shop purchase or an equip; the caller maps it to a reply.
type FishBuyStatus int

const (
	FishBuyOK      FishBuyStatus = iota
	FishBuyUnknown               // not a shop item, basic item refused for buy, or not owned (equip)
	FishBuyOwned                 // already in the bag (permanent) or at MaxBaitUses cap
	FishBuyGems                  // not enough Gems
	FishBuyFailed                // the save failed; nothing changed
)

// FishingShopItems lists the non-basic shop items in display order.
func FishingShopItems() []FishingItem {
	var out []FishingItem
	for _, it := range ActiveFishing().Shop {
		if !it.Basic {
			out = append(out, it)
		}
	}
	return out
}

// FishingItemByID returns the shop or basic item with id.
func FishingItemByID(id int64) (FishingItem, bool) {
	for _, it := range ActiveFishing().Shop {
		if it.ID == id {
			return it, true
		}
	}
	return FishingItem{}, false
}

// FishingBasicItem returns the default free basic item for kind (rod or bait).
func FishingBasicItem(kind int64) (FishingItem, bool) {
	for _, it := range ActiveFishing().Shop {
		if it.Basic && it.Kind == kind {
			return it, true
		}
	}
	return FishingItem{}, false
}

func fishingBasicID(kind int64) int64 {
	if it, ok := FishingBasicItem(kind); ok {
		return it.ID
	}
	return 0
}

// fishingGearOf returns a copy of acc's gear (zero when none), so callers can change it and
// store the copy back as a new record.
func fishingGearOf(acc *store.Account) store.FishingGear {
	if acc.Gear == nil {
		return store.FishingGear{}
	}
	g := *acc.Gear
	g.Owned = slices.Clone(g.Owned)
	g.BaitUses = maps.Clone(g.BaitUses)
	return g
}

func fishingItemAvailableLocked(gear store.FishingGear, it FishingItem) bool {
	if it.Basic {
		return true
	}
	if it.Uses > 0 {
		return gear.BaitUses[it.ID] > 0
	}
	return slices.Contains(gear.Owned, it.ID)
}

// BuyFishingItem charges id's price in Gems and adds it (or its uses) to aid's bag.
// Returns (status, newGems, totalCount).
func BuyFishingItem(aid string, id int64) (FishBuyStatus, int64, int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return FishBuyFailed, 0, 0
	}
	item, ok := FishingItemByID(id)
	if !ok || item.Basic {
		return FishBuyUnknown, acc.Gems, 0
	}
	gear := fishingGearOf(acc)
	if item.Uses > 0 {
		curUses := gear.BaitUses[id]
		if curUses+item.Uses > MaxBaitUses {
			return FishBuyOwned, acc.Gems, curUses
		}
		prev := *acc
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -item.Price}, "fishshop:"+strconv.FormatInt(id, 10))
		if err != nil {
			*acc = prev
			return FishBuyGems, acc.Gems, curUses
		}
		if gear.BaitUses == nil {
			gear.BaitUses = map[int64]int64{}
		}
		gear.BaitUses[id] = curUses + item.Uses
		if !slices.Contains(gear.Owned, id) {
			gear.Owned = append(gear.Owned, id)
		}
		if item.Kind == FishKindBait {
			cur, curOK := FishingItemByID(gear.Bait)
			if gear.Bait == 0 || !curOK || cur.Basic || (cur.Uses > 0 && gear.BaitUses[cur.ID] <= 0) || item.RareBonus > cur.RareBonus {
				gear.Bait = id
			}
		}
		acc.Gear = &gear
		if err := store.SaveLocked(lines, acc); err != nil {
			*acc = prev
			return FishBuyFailed, prev.Gems, curUses
		}
		return FishBuyOK, acc.Gems, gear.BaitUses[id]
	}

	if slices.Contains(gear.Owned, id) {
		return FishBuyOwned, acc.Gems, 1
	}
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -item.Price}, "fishshop:"+strconv.FormatInt(id, 10))
	if err != nil {
		*acc = prev
		return FishBuyGems, acc.Gems, 0
	}
	gear.Owned = append(gear.Owned, id)
	if item.Kind == FishKindRod {
		if cur, ok := FishingItemByID(gear.Rod); gear.Rod == 0 || !ok || cur.Basic || item.RareBonus > cur.RareBonus {
			gear.Rod = id
		}
	} else {
		cur, curOK := FishingItemByID(gear.Bait)
		if gear.Bait == 0 || !curOK || cur.Basic || (cur.Uses > 0 && gear.BaitUses[cur.ID] <= 0) || item.RareBonus > cur.RareBonus {
			gear.Bait = id
		}
	}
	acc.Gear = &gear
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = prev
		return FishBuyFailed, prev.Gems, 0
	}
	return FishBuyOK, acc.Gems, 1
}

// EquipFishingItem equips id of kind (FishKindRod or FishKindBait) when aid owns it or it is basic.
func EquipFishingItem(aid string, id int64, kind int64) (FishBuyStatus, FishingItem) {
	if kind != FishKindRod && kind != FishKindBait {
		return FishBuyUnknown, FishingItem{}
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return FishBuyFailed, FishingItem{}
	}
	it, ok := FishingItemByID(id)
	if !ok || it.Kind != kind {
		return FishBuyUnknown, FishingItem{}
	}
	gear := fishingGearOf(acc)
	if !fishingItemAvailableLocked(gear, it) {
		return FishBuyUnknown, FishingItem{}
	}
	prev := *acc
	if kind == FishKindRod {
		gear.Rod = id
	} else {
		gear.Bait = id
	}
	acc.Gear = &gear
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		return FishBuyFailed, FishingItem{}
	}
	return FishBuyOK, it
}

// ConsumeFishingBait consumes 1 use of aid's equipped consumable bait. When the last use is
// spent, the equipped bait falls back to the basic bait and exhausted returns true.
func ConsumeFishingBait(aid string) (exhausted bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil || acc.Gear == nil {
		return false
	}
	it, ok := FishingItemByID(acc.Gear.Bait)
	if !ok || it.Basic || it.Uses <= 0 {
		return false
	}
	prev := *acc
	gear := fishingGearOf(acc)
	rem := gear.BaitUses[it.ID] - 1
	if rem <= 0 {
		delete(gear.BaitUses, it.ID)
		if len(gear.BaitUses) == 0 {
			gear.BaitUses = nil
		}
		gear.Bait = fishingBasicID(FishKindBait)
		exhausted = true
	} else {
		if gear.BaitUses == nil {
			gear.BaitUses = map[int64]int64{}
		}
		gear.BaitUses[it.ID] = rem
	}
	acc.Gear = &gear
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		log.Printf("cherry: square fishing bait consume save failed aid=%s item=%d err=%v", aid, it.ID, err)
		return false
	}
	return exhausted
}

// FishingBagEntries returns aid's available items of kind (always including the basic item) with
// their counts, plus the id currently in use (falling back to the basic item when none).
func FishingBagEntries(aid string, kind int64) ([]FishingBagEntry, int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return nil, 0
	}
	gear := fishingGearOf(acc)
	var entries []FishingBagEntry
	for _, it := range ActiveFishing().Shop {
		if it.Kind != kind || !fishingItemAvailableLocked(gear, it) {
			continue
		}
		count := int64(1)
		if !it.Basic && it.Uses > 0 {
			count = gear.BaitUses[it.ID]
		}
		entries = append(entries, FishingBagEntry{Item: it, Count: count})
	}
	inUse := gear.Rod
	if kind == FishKindBait {
		inUse = gear.Bait
	}
	if cur, ok := FishingItemByID(inUse); !ok || cur.Kind != kind || !fishingItemAvailableLocked(gear, cur) {
		inUse = fishingBasicID(kind)
	}
	return entries, inUse
}

// FishingBag returns aid's available items of kind and the id in use for that kind.
func FishingBag(aid string, kind int64) ([]FishingItem, int64) {
	entries, inUse := FishingBagEntries(aid, kind)
	if entries == nil {
		return nil, inUse
	}
	out := make([]FishingItem, len(entries))
	for i, e := range entries {
		out[i] = e.Item
	}
	return out, inUse
}

// FishingGearBonus is the rare-chance bonus (percentage points) of the rod and bait in use.
func FishingGearBonus(aid string) int64 {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil || acc.Gear == nil {
		return 0
	}
	gear := fishingGearOf(acc)
	var bonus int64
	if it, ok := FishingItemByID(gear.Rod); ok && it.Kind == FishKindRod && fishingItemAvailableLocked(gear, it) {
		bonus += it.RareBonus
	}
	if it, ok := FishingItemByID(gear.Bait); ok && it.Kind == FishKindBait && fishingItemAvailableLocked(gear, it) {
		bonus += it.RareBonus
	}
	return bonus
}

// FishingItemImageURL is the shop picture for a gear item, served by HandleFishingItemImage.
// It is RELATIVE on purpose: the client's item-image loader prefixes its static base.
func FishingItemImageURL(id int64) string {
	if it, ok := FishingItemByID(id); ok {
		if it.ShopImage != "" {
			return "cherry/fishing/orig/" + it.ShopImage
		}
		if it.InvenImage != "" {
			return "cherry/fishing/orig/" + it.InvenImage
		}
	}
	return "cherry/fishing/" + strconv.FormatInt(id, 10) + ".png"
}

// FishingItemInvenImageURL is the bag/equip picture for a gear item, served by HandleFishingItemImage.
func FishingItemInvenImageURL(id int64) string {
	if it, ok := FishingItemByID(id); ok {
		if it.InvenImage != "" {
			return "cherry/fishing/orig/" + it.InvenImage
		}
		if it.ShopImage != "" {
			return "cherry/fishing/orig/" + it.ShopImage
		}
	}
	return "cherry/fishing/" + strconv.FormatInt(id, 10) + ".png"
}

// HandleFishingItemImage serves GET /cherry/fishing/orig/<name> (allowlisted original gear PNGs
// from cache/fishing) and GET /cherry/fishing/<id>.png (drawn fallback icons).
func HandleFishingItemImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if rel, ok := strings.CutPrefix(r.URL.Path, "/cherry/fishing/orig/"); ok {
		if rel == "" || !cleanFishingPNG(rel) || !slices.ContainsFunc(ActiveFishing().Shop, func(it FishingItem) bool {
			return it.ShopImage == rel || it.InvenImage == rel
		}) {
			http.NotFound(w, r)
			return
		}
		b, err := fs.ReadFile(fishingCacheFS, rel)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cherry/fishing/"), ".png"), 10, 64)
	it, ok := FishingItemByID(id)
	if err != nil || !ok {
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
