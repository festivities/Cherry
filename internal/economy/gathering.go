package economy

import (
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Garden gathering (Economy Phase DE). A touch spot in a Garden pays one material into the Bag per
// successful gather, GatherSmiles Smiles and a small Gem bonus. The caps reset at 06:00 JST (the
// game day, AttendDayKey): the account's own Garden allows GatherOwnCap gathers a day (all kinds
// together); any other Garden allows GatherFriendCap, and GatherFriendGardens different other
// Gardens a day. Wire and spot notes: %TEMP%\opencode\cherry\phaseDE-20261022\gather-craft-spec.md.

const (
	GatherOwnCap        = 20  // gathers a day in the account's own Garden, all kinds together
	GatherFriendCap     = 5   // gathers a day in one other Garden
	GatherFriendGardens = 3   // different other Gardens a day
	GatherSmiles        = 3   // Smiles per successful gather
	GatherGems          = 2   // Gem bonus per successful gather
	GatherGemsCap       = 30  // Gem bonus a day from gathering
	GatherBagSlots      = 200 // Bag capacity (slot_size); the 13 materials can never fill it
)

// GatherKind is the client's GatheringType: 1 fish, 2 essence, 3 ore.
type GatherKind int64

const (
	GatherFish    GatherKind = 1
	GatherEssence GatherKind = 2
	GatherOre     GatherKind = 3
)

var gatherKindNames = map[GatherKind]string{GatherFish: "fish", GatherEssence: "essence", GatherOre: "ore"}

// GatherDrop is one material and its weight among its kind's drops. Names are Cherry-made
// (the client has no name table for these ids).
type GatherDrop struct {
	Item   int64
	Name   string
	Weight int
}

// gatherDrops lists each kind's materials; the weights sum to 100, and the rarer ids weigh less.
var gatherDrops = map[GatherKind][]GatherDrop{
	GatherFish: {
		{110000, "Pond Minnow", 40}, {111000, "Pond Perch", 25}, {120000, "Lily Carp", 15},
		{130000, "Golden Carp", 10}, {131000, "Moonlit Koi", 6}, {132000, "Rainbow Koi", 4},
	},
	GatherEssence: {{210000, "Dew Essence", 60}, {220000, "Bloom Essence", 30}, {230000, "Star Essence", 10}},
	GatherOre: {
		{310000, "Star Fragment", 50}, {321000, "Glow Fragment", 25}, {330000, "Crystal Shard", 15}, {331000, "Moon Crystal", 10},
	},
}

// GatherPick is the reward a begin pre-rolls: the material and, for fish, its length in cm.
type GatherPick struct {
	Drop   GatherDrop
	Length int64
}

// PickGather rolls one gather of kind. roll(n) returns an int in [0, n); the caller passes
// math/rand/v2.IntN. ok is false for a kind with no drops.
func PickGather(kind GatherKind, roll func(n int) int) (GatherPick, bool) {
	drops := gatherDrops[kind]
	total := 0
	for _, d := range drops {
		total += d.Weight
	}
	if total == 0 {
		return GatherPick{}, false
	}
	var p GatherPick
	pick := roll(total)
	for _, d := range drops {
		if pick < d.Weight {
			p.Drop = d
			break
		}
		pick -= d.Weight
	}
	if kind == GatherFish {
		p.Length = int64(10 + roll(61)) // 10 to 70 cm
	}
	return p, true
}

// GatherMaterialByID returns the Garden material with item id: a gathering drop, or a crafted good
// of the Atelier recipe file (its name comes from there).
func GatherMaterialByID(item int64) (GatherDrop, bool) {
	if d, ok := gatherDropByID(item); ok {
		return d, true
	}
	if g, ok := ActiveAtelier().Good(item); ok {
		return GatherDrop{Item: g.ID, Name: g.Name}, true
	}
	return GatherDrop{}, false
}

// gatherDropByID returns the gathering drop with item id.
func gatherDropByID(item int64) (GatherDrop, bool) {
	for _, drops := range gatherDrops {
		for _, d := range drops {
			if d.Item == item {
				return d, true
			}
		}
	}
	return GatherDrop{}, false
}

// GatherLimit is what one Garden allows the viewer today: Rest gathers left there and its cap Max.
type GatherLimit struct{ Rest, Max int64 }

// gatherRecordOf returns acc's gathering record for day; a record from another day counts as empty.
func gatherRecordOf(acc *store.Account, day int64) store.GatherDay {
	if acc == nil || acc.Gather == nil || acc.Gather.Day != day {
		return store.GatherDay{Day: day}
	}
	return *acc.Gather
}

// gatherLimitOf is the limit in owner's Garden for a record rec. Another Garden is refused once
// GatherFriendGardens other Gardens are in use today.
func gatherLimitOf(rec store.GatherDay, owner string, own bool) GatherLimit {
	if own {
		return GatherLimit{Rest: max(GatherOwnCap-rec.Own, 0), Max: GatherOwnCap}
	}
	_, used := rec.Gardens[owner]
	if !used && int64(len(rec.Gardens)) >= GatherFriendGardens {
		return GatherLimit{Max: GatherFriendCap}
	}
	return GatherLimit{Rest: max(GatherFriendCap-rec.Gardens[owner], 0), Max: GatherFriendCap}
}

// GatherStatus is the viewer's limit in owner's Garden now. An unknown viewer gets the full limit.
func GatherStatus(viewer, owner string, now time.Time) GatherLimit {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	rec := gatherRecordOf(store.AccountByAidLocked(viewer), AttendDayKey(now))
	return gatherLimitOf(rec, owner, viewer == owner)
}

// GatherCredit pays one successful gather of item from kind in owner's Garden to aid, as one saved
// change: the material into the Bag, GatherSmiles Smiles, and the Gem bonus up to GatherGemsCap a
// day (the Smiles are not capped). It returns the Garden's limit afterwards and the Gems paid. ok is
// false, with nothing changed, when the account is unknown, the Garden's cap is used up or the save
// fails.
func GatherCredit(aid, owner string, kind GatherKind, item int64, now time.Time) (GatherLimit, int64, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return GatherLimit{}, 0, false
	}
	day := AttendDayKey(now)
	rec := gatherRecordOf(acc, day)
	own := aid == owner
	lim := gatherLimitOf(rec, owner, own)
	if lim.Rest == 0 {
		return lim, 0, false
	}
	next := rec
	next.Gardens = maps.Clone(rec.Gardens)
	if own {
		next.Own++
	} else {
		if next.Gardens == nil {
			next.Gardens = map[string]int64{}
		}
		next.Gardens[owner]++
	}
	gems := max(min(GatherGems, GatherGemsCap-rec.Gems), 0)
	next.Gems += gems
	reason := "gather:" + gatherKindNames[kind] + ":" + questDate(day)
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: gems, Smiles: GatherSmiles}, reason)
	if err != nil {
		*acc = prev
		return lim, 0, false
	}
	acc.Gather = &next
	acc.GardenBag = gardenBagChange(acc.GardenBag, item, 1, now)
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		return lim, 0, false
	}
	store.FlushLedgerLocked(lines)
	return gatherLimitOf(next, owner, own), gems, true
}

// gardenBagChange returns bag with delta more of item (fewer when negative). A stack that reaches
// zero is removed, and a gain stamps Updated. It is a new slice, so a failed save can roll back.
// Callers check the counts first.
func gardenBagChange(bag []store.GardenBagStack, item, delta int64, now time.Time) []store.GardenBagStack {
	out := slices.Clone(bag)
	i := slices.IndexFunc(out, func(s store.GardenBagStack) bool { return s.Item == item })
	if i < 0 {
		if delta > 0 {
			out = append(out, store.GardenBagStack{Item: item, Count: delta, Updated: now.UnixMilli()})
		}
		return out
	}
	out[i].Count += delta
	if delta > 0 {
		out[i].Updated = now.UnixMilli()
	}
	if out[i].Count <= 0 {
		out = slices.Delete(out, i, i+1)
	}
	return out
}

// GardenBag returns a copy of aid's material stacks and its Smiles (zero when the account is unknown).
func GardenBag(aid string) ([]store.GardenBagStack, int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return nil, 0
	}
	return slices.Clone(acc.GardenBag), acc.Smiles
}

// GardenBagDelete removes aid's whole stack of item. It reports false when there is no such stack
// or the save fails.
func GardenBagDelete(aid string, item int64) bool {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return false
	}
	i := slices.IndexFunc(acc.GardenBag, func(s store.GardenBagStack) bool { return s.Item == item })
	if i < 0 {
		return false
	}
	prev := *acc
	acc.GardenBag = slices.Delete(slices.Clone(acc.GardenBag), i, i+1)
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		return false
	}
	return true
}

// GatherImageURL is the material's picture. It is RELATIVE, like FishingItemImageURL: the client
// prefixes its static base to the path. HandleGatherImage serves it.
func GatherImageURL(item int64) string {
	return "cherry/garden/items/item" + strconv.FormatInt(item, 10) + ".png"
}

// GatherCraftImage is the material's picture as the craft and Bag cells read AssetItem.img_url: a path
// under the client's item/craftitem/ folder. The client checks that folder on disk first
// (NaDpCraftSprite::InitWithPath 0x1ef1524), so a played install draws the icon without a download.
// The download route (GatherImageURL) is served (200) but the sprite stays on the spinner or "!".
func GatherCraftImage(item int64) string {
	return "garden01/items/item" + strconv.FormatInt(item, 10) + ".png"
}

// HandleGatherImage serves GET /cherry/garden/items/item<id>.png with the client's own craft icon,
// read in place from the read-only archive (thumbs.DpItemRoot/craftitem/garden01/items).
func HandleGatherImage(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/cherry/garden/items/")
	id, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(name, "item"), ".png"), 10, 64)
	if _, ok := GatherMaterialByID(id); err != nil || !ok || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	file := "item" + strconv.FormatInt(id, 10) + ".png"
	http.ServeFile(w, r, filepath.Join(thumbs.DpItemRoot, "craftitem", "garden01", "items", file))
}
