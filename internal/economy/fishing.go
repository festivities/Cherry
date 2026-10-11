package economy

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"cherry/internal/store"
)

// Square fishing (Economy Phase D slice 2). The server picks the catch when a cast starts
// (SQUARE start 29) and pays it once when the client reports a hook (finish 30, Result
// true). The daily cap is FishingDailyCap Gems per account per game day (06:00 JST, like
// the quests). Wire notes: %TEMP%\opencode\cherry\phaseD-20261011\gathering.txt, section 4
// and "Resolution (slice 2)".

// FishingDailyCap is the most Gems fishing pays one account on one game day.
const FishingDailyCap = 150

// fishRareChance is the rare-catch chance in percent (1 in 10 casts).
const fishRareChance = 10

// FishDef is one catch. ID is the FishId the client shows; names and levels are Cherry-made.
type FishDef struct {
	ID    int64
	Name  string
	Level int64
	Gems  int64
	Rare  bool
}

var fishNormal = []FishDef{
	{4101, "Square Carp", 1, 5, false},
	{4102, "Square Sunfish", 1, 6, false},
	{4103, "Square Goldfish", 1, 7, false},
	{4104, "Square Bream", 1, 8, false},
	{4105, "Square Trout", 1, 9, false},
	{4106, "Square Eel", 1, 10, false},
}

var fishRare = []FishDef{
	{4201, "Golden Square Koi", 3, 25, true},
	{4202, "Crystal Square Ray", 3, 25, true},
}

// ChooseFish picks the next catch. roll(n) returns an int in [0, n); the caller passes
// math/rand/v2.IntN, and tests pass a fixed sequence.
func ChooseFish(roll func(n int) int) FishDef {
	return ChooseFishWithBonus(roll, 0)
}

// ChooseFishWithBonus is ChooseFish with the rod and bait in use adding bonus percentage points to
// the rare chance (FishingGearBonus). The daily Gem cap does not depend on it.
func ChooseFishWithBonus(roll func(n int) int, bonus int64) FishDef {
	if roll(100) < fishRareChance+int(bonus) {
		return fishRare[roll(len(fishRare))]
	}
	return fishNormal[roll(len(fishNormal))]
}

// fishingRecord returns acc's fishing record for day; a record from another day counts as empty.
func fishingRecord(acc *store.Account, day int64) store.FishingDay {
	if acc.Fishing == nil || acc.Fishing.Day != day {
		return store.FishingDay{Day: day}
	}
	return *acc.Fishing
}

// FishCatch records one hooked catch in aid's fishing book and credits it for now's game day,
// capped at FishingDailyCap. It returns the Gems actually credited and ok=false only when the
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
	credit := min(fish.Gems, FishingDailyCap-rec.Gems)
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
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: square fishing save failed aid=%s fish=%d err=%v", aid, fish.ID, err)
		return 0, false
	}
	store.FlushLedgerLocked(lines)
	return credit, true
}

// FishCatalog lists every Square fish in book order: the normal fish, then the rare ones.
func FishCatalog() []FishDef {
	return append(slices.Clone(fishNormal), fishRare...)
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

// fishSilhouette is the client's own side-view fish shadow (2016 Android animation set), read in
// place from the read-only archive; the catch card picture is this shape tinted per fish.
var fishSilhouette = filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lineplay-original-assets/android-4.0.0.0-2016/animation/fish/fish_ani_0001.png`)

// fishColors tints each fish's card picture.
var fishColors = map[int64]color.NRGBA{
	4101: {240, 130, 40, 255}, 4102: {90, 170, 90, 255}, 4103: {250, 190, 40, 255},
	4104: {170, 180, 195, 255}, 4105: {120, 150, 210, 255}, 4106: {95, 105, 60, 255},
	4201: {255, 205, 30, 255}, 4202: {120, 225, 255, 255},
}

// FishImageURL is the catch card picture for fish id (served by HandleFishImage).
func FishImageURL(id int64) string {
	return "https://play-static.line-scdn.net/cherry/fish/" + strconv.FormatInt(id, 10) + ".png"
}

// HandleFishImage serves GET /cherry/fish/<id>.png: the silhouette scaled 2x, tinted with the
// fish's colour, with a white eye.
func HandleFishImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cherry/fish/"), ".png"), 10, 64)
	tint, ok := fishColors[id]
	if err != nil || !ok || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(fishSilhouette)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	src, err := png.Decode(f)
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
