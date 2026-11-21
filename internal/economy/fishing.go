package economy

import (
	"bytes"
	"errors"
	"fmt"
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
	"time"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// Square fishing (Economy Phase D slice 2; rebuilt 2026-11-21 with the original 81 species,
// 9-page collection book + per-page rewards, original gear art + 20-use bait packs, and rare
// fish retry). The server picks the catch when a cast starts (SQUARE start 29) and pays it once
// when the client reports a hook (finish 30, Result true). The daily cap is daily_cap Gems per
// account per game day (06:00 JST, like the quests).

// Fishing is data/fishing.json: the Square fish book, the daily Gem cap, the rare-catch chance,
// per-page book reward, rare retry price, and the fish shop (fishing_shop.go). The book order is
// the file order (9 species per page); the client sorts by ItemIndex.
type Fishing struct {
	opdata.Provenance
	DailyCap       int64         `json:"daily_cap"`
	RarePercent    int           `json:"rare_percent"`
	PageRewardGems int64         `json:"page_reward_gems"`
	RetryGems      int64         `json:"retry_gems"`
	Fish           []FishDef     `json:"fish"`
	Shop           []FishingItem `json:"shop"`

	normal, rare []FishDef
}

// FishDef is one catch. Species names and cache PNGs are original; ids, levels, Gems, rarity and
// tints are Cherry-made.
type FishDef struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Level int64  `json:"level"`
	Gems  int64  `json:"gems"`
	Rare  bool   `json:"rare"`
	Tint  string `json:"tint"`
	Image string `json:"image,omitempty"`

	tint color.NRGBA
}

// FishBookPageSize is the number of fish slots on each page of the Square fishing book.
const FishBookPageSize = 9

var (
	fishingCacheFS fs.FS = assets.DeviceFiles("cache/fishing")
	fishingFile          = opdata.New("fishing.json", data.FishingJSON, checkFishing, verifyFishing)
)

// ActiveFishing is the fishing file in use.
func ActiveFishing() *Fishing { return fishingFile.Get() }

// CherryFishFirst and CherryFishLast bound the Square catch ids, fish and boss fish alike (fishing.json,
// fishboss.json). CherryFishShopFirst and CherryFishShopLast bound the fish shop item ids (rods and baits).
// Their numbers 9001-9199 overlap the Cherry quest block, but the shop ids live in FishingGear and the quest
// ids in QuestDay, never in the same table.
const (
	CherryFishFirst     int64 = 4101
	CherryFishLast      int64 = 4999
	CherryFishShopFirst int64 = 9001
	CherryFishShopLast  int64 = 9199
)

func cleanFishingPNG(name string) bool {
	return len(name) > 4 && !strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..") && strings.HasSuffix(name, ".png")
}

func checkFishing(f *Fishing) error {
	if err := f.CheckOrigin(opdata.OriginMixed); err != nil {
		return err
	}
	if f.DailyCap < 0 {
		return errors.New("daily_cap must not be negative")
	}
	if f.RarePercent < 0 || f.RarePercent > 100 {
		return errors.New("rare_percent must be 0 to 100")
	}
	if f.PageRewardGems < 0 {
		return errors.New("page_reward_gems must not be negative")
	}
	if f.RetryGems < 0 {
		return errors.New("retry_gems must not be negative")
	}
	if len(f.Fish) == 0 {
		return errors.New("fish must not be empty")
	}
	f.normal, f.rare = nil, nil
	ids := map[int64]bool{}
	for i := range f.Fish {
		fd := &f.Fish[i]
		if !inCherryBlock(fd.ID, CherryFishFirst, CherryFishLast) || ids[fd.ID] {
			return fmt.Errorf("fish %d: id must be unique and inside the Cherry block %d-%d", fd.ID, CherryFishFirst, CherryFishLast)
		}
		if fd.Name == "" || fd.Gems < 0 || fd.Level < 0 {
			return fmt.Errorf("fish %d: name must not be empty, gems and level must not be negative", fd.ID)
		}
		if fd.Image != "" && !cleanFishingPNG(fd.Image) {
			return fmt.Errorf("fish %d: image %q must be a clean .png filename", fd.ID, fd.Image)
		}
		c, err := parseHexColor(fd.Tint)
		if err != nil {
			return fmt.Errorf("fish %d: %w", fd.ID, err)
		}
		fd.tint = c
		ids[fd.ID] = true
		if fd.Rare {
			f.rare = append(f.rare, *fd)
		} else {
			f.normal = append(f.normal, *fd)
		}
	}
	if len(f.normal) == 0 || len(f.rare) == 0 {
		return errors.New("fish needs at least one normal and one rare fish")
	}
	shopIDs := map[int64]bool{}
	for i := range f.Shop {
		it := &f.Shop[i]
		switch it.KindName {
		case "rod":
			it.Kind = FishKindRod
		case "bait":
			it.Kind = FishKindBait
		default:
			return fmt.Errorf("shop item %d: kind must be rod or bait, not %q", it.ID, it.KindName)
		}
		if !inCherryBlock(it.ID, CherryFishShopFirst, CherryFishShopLast) || shopIDs[it.ID] || it.Name == "" || it.Price < 0 || it.RareBonus < 0 || it.Uses < 0 {
			return fmt.Errorf("shop item %d: id and name must be set, price, uses and rare_bonus not negative", it.ID)
		}
		shopIDs[it.ID] = true
		if it.Basic && it.Price != 0 {
			return fmt.Errorf("shop item %d: basic item must have price 0", it.ID)
		}
		if !it.Basic && it.Price < 1 {
			return fmt.Errorf("shop item %d: non-basic item must have price at least 1", it.ID)
		}
		if it.ShopImage != "" && !cleanFishingPNG(it.ShopImage) {
			return fmt.Errorf("shop item %d: shop_image %q must be a clean .png filename", it.ID, it.ShopImage)
		}
		if it.InvenImage != "" && !cleanFishingPNG(it.InvenImage) {
			return fmt.Errorf("shop item %d: inven_image %q must be a clean .png filename", it.ID, it.InvenImage)
		}
	}
	return nil
}

func verifyFishing(f *Fishing) error {
	if !artVerifiable() {
		return nil
	}
	for _, fd := range f.Fish {
		if fd.Image == "" {
			continue
		}
		if _, err := fs.Stat(fishingCacheFS, fd.Image); err != nil {
			return fmt.Errorf("fish %d: image %s is not in the archive", fd.ID, fd.Image)
		}
	}
	for _, it := range f.Shop {
		if it.ShopImage != "" {
			if _, err := fs.Stat(fishingCacheFS, it.ShopImage); err != nil {
				return fmt.Errorf("shop item %d: shop_image %s is not in the archive", it.ID, it.ShopImage)
			}
		}
		if it.InvenImage != "" {
			if _, err := fs.Stat(fishingCacheFS, it.InvenImage); err != nil {
				return fmt.Errorf("shop item %d: inven_image %s is not in the archive", it.ID, it.InvenImage)
			}
		}
	}
	return nil
}

// ChooseFish picks the next catch. roll(n) returns an int in [0, n); the caller passes
// math/rand/v2.IntN, and tests pass a fixed sequence.
func ChooseFish(roll func(n int) int) FishDef {
	return ChooseFishWithBonus(roll, 0)
}

// ChooseFishWithBonus is ChooseFish with the rod and bait in use adding bonus percentage points to
// the rare chance (FishingGearBonus). The daily Gem cap does not depend on it.
func ChooseFishWithBonus(roll func(n int) int, bonus int64) FishDef {
	f := ActiveFishing()
	if roll(100) < f.RarePercent+int(bonus) {
		return f.rare[roll(len(f.rare))]
	}
	return f.normal[roll(len(f.normal))]
}

// fishingRecord returns acc's fishing record for day; a record from another day counts as empty.
func fishingRecord(acc *store.Account, day int64) store.FishingDay {
	if acc.Fishing == nil || acc.Fishing.Day != day {
		return store.FishingDay{Day: day}
	}
	return *acc.Fishing
}

// FishCatch records one hooked catch in aid's fishing book and credits it for now's game day,
// capped at the daily cap. It returns the Gems actually credited and ok=false only when the
// account is unknown or the change could not be saved. Once the cap is reached the catch still
// counts in the book and pays 0 with no ledger line. A partial last catch is capped (140 of 150
// plus a 10-Gem fish pays 10). A failed save rolls the balance, the day record and the book back.
func FishCatch(aid string, fish FishDef, now time.Time) (int64, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, false
	}
	day := AttendDayKey(now)
	rec := fishingRecord(acc, day)
	credit := min(fish.Gems, ActiveFishing().DailyCap-rec.Gems)
	prev := *acc
	caught := maps.Clone(acc.FishCaught) // a copy, so the rollback in *acc = prev restores the old book
	if caught == nil {
		caught = map[int64]int64{}
	}
	caught[fish.ID]++
	acc.FishCaught = caught
	var lines []store.LedgerLine
	if credit > 0 {
		reason := "fish:" + strconv.FormatInt(fish.ID, 10) + ":" + questDate(day)
		var err error
		lines, err = store.SpendLocked(acc, store.LedgerDeltas{Gems: credit}, reason)
		if err != nil {
			*acc = prev
			log.Printf("cherry: square fishing credit failed aid=%s fish=%d err=%v", aid, fish.ID, err)
			return 0, false
		}
		acc.Fishing = &store.FishingDay{Day: day, Gems: rec.Gems + credit}
	}
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = prev
		log.Printf("cherry: square fishing save failed aid=%s fish=%d err=%v", aid, fish.ID, err)
		return 0, false
	}
	return credit, true
}

// FishRetry charges RetryGems to retry a missed rare fish. It does NOT touch the FishingDay daily
// Gem cap and rolls back on save failure.
func FishRetry(aid string, now time.Time) (int64, bool) {
	cost := ActiveFishing().RetryGems
	if cost <= 0 {
		return 0, false
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, false
	}
	day := AttendDayKey(now)
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -cost}, "fish:retry:"+questDate(day))
	if err != nil {
		*acc = prev
		return 0, false
	}
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = prev
		log.Printf("cherry: square fishing retry save failed aid=%s err=%v", aid, err)
		return 0, false
	}
	return acc.Gems, true
}

// FishCatalog lists every Square fish in book order (the file order).
func FishCatalog() []FishDef {
	return slices.Clone(ActiveFishing().Fish)
}

// FishCollection returns aid's catch counts per fish id and their sum (the book's version).
func FishCollection(aid string) (map[int64]int64, int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return nil, 0
	}
	var total int64
	for _, n := range acc.FishCaught {
		total += n
	}
	return maps.Clone(acc.FishCaught), total
}

// FishBookPageCount returns the number of pages in the Square fishing book.
func FishBookPageCount() int {
	return max((len(ActiveFishing().Fish)+FishBookPageSize-1)/FishBookPageSize, 1)
}

// FishBookPage returns the up-to-9 fish on page (1-based, clamped), aid's catch counts, total
// catches, whether the page reward can be claimed now, whether it was already claimed, and the
// per-page Gem reward.
func FishBookPage(aid string, page int) (fish []FishDef, counts map[int64]int64, total int64, possibleComplete, completeAlready bool, rewardGems int64) {
	f := ActiveFishing()
	maxPage := max((len(f.Fish)+FishBookPageSize-1)/FishBookPageSize, 1)
	page = min(max(page, 1), maxPage)
	start := (page - 1) * FishBookPageSize
	end := min(start+FishBookPageSize, len(f.Fish))
	if start < len(f.Fish) {
		fish = slices.Clone(f.Fish[start:end])
	}
	rewardGems = f.PageRewardGems

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return fish, nil, 0, false, false, rewardGems
	}
	counts = maps.Clone(acc.FishCaught)
	for _, n := range acc.FishCaught {
		total += n
	}
	completeAlready = slices.Contains(acc.FishBookClaimed, int64(page))
	if len(fish) > 0 && !completeAlready {
		allCaught := true
		for _, fd := range fish {
			if counts[fd.ID] <= 0 {
				allCaught = false
				break
			}
		}
		possibleComplete = allCaught
	}
	return fish, counts, total, possibleComplete, completeAlready, rewardGems
}

// ClaimFishBookPage claims the per-page completion reward once every fish on page is caught.
func ClaimFishBookPage(aid string, page int) (int64, bool) {
	f := ActiveFishing()
	maxPage := max((len(f.Fish)+FishBookPageSize-1)/FishBookPageSize, 1)
	if page < 1 || page > maxPage {
		return 0, false
	}
	start := (page - 1) * FishBookPageSize
	end := min(start+FishBookPageSize, len(f.Fish))
	if start >= len(f.Fish) || start == end {
		return 0, false
	}
	slice := f.Fish[start:end]

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, false
	}
	if slices.Contains(acc.FishBookClaimed, int64(page)) {
		return 0, false
	}
	for _, fd := range slice {
		if acc.FishCaught[fd.ID] <= 0 {
			return 0, false
		}
	}
	reward := f.PageRewardGems
	prev := *acc
	var lines []store.LedgerLine
	if reward > 0 {
		var err error
		lines, err = store.SpendLocked(acc, store.LedgerDeltas{Gems: reward}, "fishbook:page:"+strconv.Itoa(page))
		if err != nil {
			*acc = prev
			log.Printf("cherry: square fish book claim failed aid=%s page=%d err=%v", aid, page, err)
			return 0, false
		}
	}
	claimed := slices.Clone(acc.FishBookClaimed)
	claimed = append(claimed, int64(page))
	acc.FishBookClaimed = claimed
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = prev
		log.Printf("cherry: square fish book save failed aid=%s page=%d err=%v", aid, page, err)
		return 0, false
	}
	return reward, true
}

// fishSilhouette is the store key of the client's own side-view fish shadow (2016 Android animation set, an original APK
// asset uploaded by cmd/cherry-assets); the catch card picture for fish without an original PNG (Porcupinefish and boss)
// is this shape tinted per fish.
const fishSilhouette = "static/android-4.0.0.0-2016/animation/fish/fish_ani_0001.png"

// FishImageURL is the catch card picture for fish id (served by HandleFishImage).
func FishImageURL(id int64) string {
	for _, fd := range ActiveFishing().Fish {
		if fd.ID == id {
			if fd.Image != "" {
				return "https://play-static.line-scdn.net/cherry/fish/orig/" + fd.Image
			}
			break
		}
	}
	return "https://play-static.line-scdn.net/cherry/fish/" + strconv.FormatInt(id, 10) + ".png"
}

// fishPictureTint is the colour of the picture with id: a fish of the book, or the boss.
func fishPictureTint(id int64) (color.NRGBA, bool) {
	for _, fd := range ActiveFishing().Fish {
		if fd.ID == id {
			return fd.tint, true
		}
	}
	return fishBossTint(id)
}

// HandleFishImage serves GET /cherry/fish/orig/<name> (allowlisted original PNGs from cache/fishing)
// and GET /cherry/fish/<id>.png (the tinted silhouette for Porcupinefish and boss fish).
func HandleFishImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if rel, ok := strings.CutPrefix(r.URL.Path, "/cherry/fish/orig/"); ok {
		if rel == "" || !cleanFishingPNG(rel) || !slices.ContainsFunc(ActiveFishing().Fish, func(fd FishDef) bool { return fd.Image == rel }) {
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
	id, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cherry/fish/"), ".png"), 10, 64)
	tint, found := fishPictureTint(id)
	if err != nil || !found {
		http.NotFound(w, r)
		return
	}
	raw, err := assets.Archive.ReadFile(fishSilhouette)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx()*2, b.Dy()*2))
	for y := 0; y < b.Dy()*2; y++ {
		for x := 0; x < b.Dx()*2; x++ {
			_, _, _, a := src.At(b.Min.X+x/2, b.Min.Y+y/2).RGBA()
			c := tint
			c.A = uint8(a >> 8)
			out.SetNRGBA(x, y, c)
		}
	}
	// Eye: the head points right on this silhouette (frame 1); a 6 px white dot near the snout.
	ex, ey := b.Dx()*2*86/100, b.Dy()*2*60/100
	for y := ey - 3; y <= ey+3; y++ {
		for x := ex - 3; x <= ex+3; x++ {
			if (x-ex)*(x-ex)+(y-ey)*(y-ey) <= 9 && out.NRGBAAt(x, y).A > 0 {
				out.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
			}
		}
	}
	w.Header().Set("Content-Type", "image/png")
	_ = png.Encode(w, out)
}

// FishCardName is the catch card's name line: the fish plus what it paid, since the stock fish
// card has no Gem line.
func FishCardName(fish FishDef, gems int64) string {
	if gems > 0 {
		return fish.Name + " +" + strconv.FormatInt(gems, 10) + " Gems"
	}
	return fish.Name + " (daily Gem limit reached)"
}
