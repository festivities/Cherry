package economy

import (
	"errors"
	"fmt"
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

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// Square fishing (Economy Phase D slice 2). The server picks the catch when a cast starts
// (SQUARE start 29) and pays it once when the client reports a hook (finish 30, Result
// true). The daily cap is daily_cap Gems per account per game day (06:00 JST, like
// the quests). Wire notes: %TEMP%\opencode\cherry\phaseD-20261011\gathering.txt, section 4
// and "Resolution (slice 2)".

// Fishing is data/fishing.json: the Square fish book, the daily Gem cap, the rare-catch chance and
// the fish shop (fishing_shop.go). The book order is the file order; the client sorts by it.
//
//	daily_cap         the most Gems fishing pays one account on one game day
//	rare_percent      rare-catch chance per cast, percent (rod and bait add to it)
//	fish[]            {id, name, level, gems, rare, tint}: id is the FishId the client shows (keep it
//	                  one the client knows); rare fish are the rare: true rows. A file needs at least
//	                  one normal and one rare fish. tint is the catch card colour (#rrggbb).
//	shop[]            {id, name, kind "rod"|"bait", price, rare_bonus}: Gem-priced gear, bought once
//	                  each; rare_bonus adds percentage points to the rare chance while in use.
type Fishing struct {
	DailyCap    int64         `json:"daily_cap"`
	RarePercent int           `json:"rare_percent"`
	Fish        []FishDef     `json:"fish"`
	Shop        []FishingItem `json:"shop"`

	normal, rare []FishDef
}

// FishDef is one catch. Names and levels are Cherry-made.
type FishDef struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Level int64  `json:"level"`
	Gems  int64  `json:"gems"`
	Rare  bool   `json:"rare"`
	Tint  string `json:"tint"`

	tint color.NRGBA
}

var fishingFile = opdata.New("fishing.json", data.FishingJSON, checkFishing, nil)

// ActiveFishing is the fishing file in use.
func ActiveFishing() *Fishing { return fishingFile.Get() }

func checkFishing(f *Fishing) error {
	if f.DailyCap < 0 {
		return errors.New("daily_cap must not be negative")
	}
	if f.RarePercent < 0 || f.RarePercent > 100 {
		return errors.New("rare_percent must be 0 to 100")
	}
	f.normal, f.rare = nil, nil
	ids := map[int64]bool{}
	for i := range f.Fish {
		fd := &f.Fish[i]
		if fd.ID < 1 || ids[fd.ID] {
			return fmt.Errorf("fish %d: id must be unique and positive", fd.ID)
		}
		if fd.Name == "" || fd.Gems < 0 || fd.Level < 0 {
			return fmt.Errorf("fish %d: name must not be empty, gems and level must not be negative", fd.ID)
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
		if it.ID < 1 || it.Name == "" || it.Price < 0 || it.RareBonus < 0 {
			return fmt.Errorf("shop item %d: id and name must be set, price and rare_bonus not negative", it.ID)
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
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: square fishing save failed aid=%s fish=%d err=%v", aid, fish.ID, err)
		return 0, false
	}
	store.FlushLedgerLocked(lines)
	return credit, true
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

// fishSilhouette is the client's own side-view fish shadow (2016 Android animation set), read in
// place from the read-only archive; the catch card picture is this shape tinted per fish.
var fishSilhouette = filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lineplay-original-assets/android-4.0.0.0-2016/animation/fish/fish_ani_0001.png`)

// FishImageURL is the catch card picture for fish id (served by HandleFishImage).
func FishImageURL(id int64) string {
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

// HandleFishImage serves GET /cherry/fish/<id>.png: the silhouette scaled 2x, tinted with the
// fish's colour (or the boss's, fishboss.go), with a white eye.
func HandleFishImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cherry/fish/"), ".png"), 10, 64)
	tint, found := fishPictureTint(id)
	if err != nil || !found || r.Method != http.MethodGet {
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
