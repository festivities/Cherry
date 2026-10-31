package economy

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Garden gathering (Economy Phase DE). A touch spot in a Garden pays one material into the Bag per
// successful gather, smiles Smiles and a small Gem bonus. The caps reset at 06:00 JST (the
// game day, AttendDayKey): the account's own Garden allows own_cap gathers a day (all kinds
// together); any other Garden allows friend_cap, and friend_gardens different other
// Gardens a day. Wire and spot notes: %TEMP%\opencode\cherry\phaseDE-20261022\gather-craft-spec.md.

// Gathering is data/gathering.json: the Garden gathering's drops, caps and rewards.
//
//	own_cap           gathers a day in the account's own Garden, all kinds together
//	friend_cap        gathers a day in one other Garden; friend_gardens different other Gardens a day
//	smiles            Smiles per successful gather (never capped); gems is the Gem bonus per gather,
//	                  gems_cap the bonus a day from gathering
//	bag_slots         Bag capacity (slot_size)
//	fish_length_min/max  a caught fish's length in cm, drawn uniformly from min to max
//	drops             {"fish"|"essence"|"ore": [{item, name, weight}]}: every kind needs at least one
//	                  drop, the weights of a kind sum above 0, and an item id is one material only.
//	                  Names are Cherry-made (the client has no name table for these ids).
type Gathering struct {
	opdata.Provenance
	OwnCap        int64                   `json:"own_cap"`
	FriendCap     int64                   `json:"friend_cap"`
	FriendGardens int64                   `json:"friend_gardens"`
	Smiles        int64                   `json:"smiles"`
	Gems          int64                   `json:"gems"`
	GemsCap       int64                   `json:"gems_cap"`
	BagSlots      int64                   `json:"bag_slots"`
	FishLengthMin int64                   `json:"fish_length_min"`
	FishLengthMax int64                   `json:"fish_length_max"`
	Drops         map[string][]GatherDrop `json:"drops"`
}

// GatherDrop is one material and its weight among its kind's drops.
type GatherDrop struct {
	Item   int64  `json:"item"`
	Name   string `json:"name"`
	Weight int    `json:"weight"`
}

var gatheringFile = opdata.New("gathering.json", data.GatheringJSON, checkGathering, nil)

// ActiveGathering is the gathering file in use.
func ActiveGathering() *Gathering { return gatheringFile.Get() }

func checkGathering(g *Gathering) error {
	if err := g.CheckOrigin("mixed"); err != nil {
		return err
	}
	for _, v := range []struct {
		key string
		val int64
	}{{"own_cap", g.OwnCap}, {"friend_cap", g.FriendCap}, {"friend_gardens", g.FriendGardens}, {"smiles", g.Smiles},
		{"gems", g.Gems}, {"gems_cap", g.GemsCap}} {
		if v.val < 0 {
			return fmt.Errorf("%s must not be negative", v.key)
		}
	}
	switch {
	case g.BagSlots < 1:
		return errors.New("bag_slots must be at least 1")
	case g.FishLengthMin < 1 || g.FishLengthMax < g.FishLengthMin:
		return errors.New("fish_length_min must be at least 1 and fish_length_max at least fish_length_min")
	}
	for _, k := range slices.Sorted(maps.Keys(g.Drops)) {
		if !slices.Contains(slices.Collect(maps.Values(gatherKindNames)), k) {
			return fmt.Errorf("drops: kind %q is not fish, essence or ore", k)
		}
	}
	items := map[int64]bool{}
	for _, kind := range []GatherKind{GatherFish, GatherEssence, GatherOre} {
		name := gatherKindNames[kind]
		drops := g.Drops[name]
		total := 0
		for _, d := range drops {
			switch {
			case d.Item < 1 || items[d.Item]:
				return fmt.Errorf("drops %s: item %d must be unique and positive", name, d.Item)
			case d.Name == "":
				return fmt.Errorf("drops %s: item %d name must not be empty", name, d.Item)
			case d.Weight < 0:
				return fmt.Errorf("drops %s: item %d weight must not be negative", name, d.Item)
			}
			items[d.Item] = true
			total += d.Weight
		}
		if total == 0 {
			return fmt.Errorf("drops %s: weights must sum above 0", name)
		}
	}
	return nil
}

// GatherKind is the client's GatheringType: 1 fish, 2 essence, 3 ore.
type GatherKind int64

const (
	GatherFish    GatherKind = 1
	GatherEssence GatherKind = 2
	GatherOre     GatherKind = 3
)

// gatherKindNames are the data keys of the drop table, one per client kind.
var gatherKindNames = map[GatherKind]string{GatherFish: "fish", GatherEssence: "essence", GatherOre: "ore"}

// GatherPick is the reward a begin pre-rolls: the material and, for fish, its length in cm.
type GatherPick struct {
	Drop   GatherDrop
	Length int64
}

// PickGather rolls one gather of kind. roll(n) returns an int in [0, n); the caller passes
// math/rand/v2.IntN. ok is false for a kind with no drops.
func PickGather(kind GatherKind, roll func(n int) int) (GatherPick, bool) {
	g := ActiveGathering()
	drops := g.Drops[gatherKindNames[kind]]
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
		span := int(g.FishLengthMax-g.FishLengthMin) + 1
		p.Length = g.FishLengthMin + int64(roll(span))
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
	for _, drops := range ActiveGathering().Drops {
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
// friend_gardens other Gardens are in use today.
func gatherLimitOf(rec store.GatherDay, owner string, own bool) GatherLimit {
	g := ActiveGathering()
	if own {
		return GatherLimit{Rest: max(g.OwnCap-rec.Own, 0), Max: g.OwnCap}
	}
	_, used := rec.Gardens[owner]
	if !used && int64(len(rec.Gardens)) >= g.FriendGardens {
		return GatherLimit{Max: g.FriendCap}
	}
	return GatherLimit{Rest: max(g.FriendCap-rec.Gardens[owner], 0), Max: g.FriendCap}
}

// GatherStatus is the viewer's limit in owner's Garden now. An unknown viewer gets the full limit.
func GatherStatus(viewer, owner string, now time.Time) GatherLimit {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	rec := gatherRecordOf(store.AccountByAidLocked(viewer), AttendDayKey(now))
	return gatherLimitOf(rec, owner, viewer == owner)
}

// GatherCount is how many successful gathers aid made today, in any Garden (the gather3 quest reads it).
func GatherCount(aid string, now time.Time) int64 {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	rec := gatherRecordOf(store.AccountByAidLocked(aid), AttendDayKey(now))
	n := rec.Own
	for _, g := range rec.Gardens {
		n += g
	}
	return n
}

// GatherCredit pays one successful gather of item from kind in owner's Garden to aid, as one saved
// change: the material into the Bag, smiles Smiles, and the Gem bonus up to gems_cap a
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
	g := ActiveGathering()
	gems := max(min(g.Gems, g.GemsCap-rec.Gems), 0)
	next.Gems += gems
	reason := "gather:" + gatherKindNames[kind] + ":" + questDate(day)
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: gems, Smiles: g.Smiles}, reason)
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

// ServeGardenCraftArt answers GET /garden01/…, /craft01/… and /photozone/frame/… (the static-base files of
// assets.CraftDirs: the Bag and Factory art, the Avatar Pic frames) from the archive, in place, when streaming is
// off. The craft folder is read first, then the download cache: the uploader (cmd/cherry-assets) takes the same
// order, so the two never disagree. A miss returns false, so the caller's 404 applies.
func ServeGardenCraftArt(w http.ResponseWriter, r *http.Request) bool {
	rel, ok := assets.CraftRel(r.URL.Path)
	if !ok || r.Method != http.MethodGet {
		return false
	}
	files := filepath.Dir(thumbs.DpItemRoot) // the device backup's files/ tree
	for _, p := range []string{
		filepath.Join(files, "item", "craftitem", filepath.FromSlash(rel)),
		filepath.Join(files, "cache", filepath.FromSlash(rel)),
	} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			http.ServeFile(w, r, p)
			return true
		}
	}
	return false
}
