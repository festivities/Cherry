package economy

import (
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"cherry/data"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Garden butlers (Garden LPN agent 10; spec butler-spec.md). The Shop hires a butler for earned Cash, the Book
// lists the hired ones and sets the chief (the butler the Garden shows), and a hired butler's gathering pays
// Garden materials and a few Gems. The butlers are Cherry-made, not original game data: no archive holds an
// original butler id or skin code, so the ids sit in a reserved block and the skin codes start with cherry_.
// The roster, prices and gathering amounts are data/butlers.json (operator-editable).

// CherryButlerFirst and CherryButlerLast bound the butler ids Cherry makes. No original butler id is in
// this block, and checkButlers refuses an id outside it.
const (
	CherryButlerFirst int64 = 1900000000
	CherryButlerLast  int64 = 1900000999
)

// IsCherryButler reports whether id is in Cherry's own butler block.
func IsCherryButler(id int64) bool { return id >= CherryButlerFirst && id <= CherryButlerLast }

// ButlerCoinCash is the CoinType of a hire price (earned Cash). [I] 2 is the Cash type of the Square exchange
// (lpn square_exchange.go); the spec (section 4) says the client special-cases CoinType 2 in ParsePacket and
// its use is unresolved, so this needs a device check.
const ButlerCoinCash int64 = 2

// ErrButlerRefused is a hire, chief change or gathering the rules do not allow now. Nothing changed.
var ErrButlerRefused = errors.New("butler: refused")

const dayMillis = 86400000

// Butlers is data/butlers.json. Strict JSON: unknown keys are an error.
//
//	origin      must be "cherry"
//	note        says the butlers are Cherry-made; text for operators, not sent to the client
//	free_days   days of the account's first hire, free once per account (a first hire, paid or free, uses it)
//	hire        hire periods and their earned-Cash prices: days unique, each at least 1, cash at least 1
//	gathering   kinds: one drop of each listed kind ("fish", "essence" or "ore", data/gathering.json) pays a
//	            gather; gems: Gems per gather; gems_cap: Gems a game day from butler gathering, per account;
//	            gathers_per_day: gathers a game day per account (0 turns butler gathering off)
//	butlers     one or more: id (in the Cherry block), name, gender "MALE" or "FEMALE", skin ("cherry_" + code)
type Butlers struct {
	Origin    string          `json:"origin"`
	Note      string          `json:"note"`
	FreeDays  int64           `json:"free_days"`
	Hire      []ButlerPrice   `json:"hire"`
	Gathering ButlerGathering `json:"gathering"`
	Butlers   []ButlerDef     `json:"butlers"`
}

// ButlerPrice is one hire period and its earned-Cash price.
type ButlerPrice struct {
	Days int64 `json:"days"`
	Cash int64 `json:"cash"`
}

// ButlerGathering is the butler gathering (see Butlers).
type ButlerGathering struct {
	Kinds         []string `json:"kinds"`
	Gems          int64    `json:"gems"`
	GemsCap       int64    `json:"gems_cap"`
	GathersPerDay int64    `json:"gathers_per_day"`
}

// ButlerDef is one roster butler. Outfit lists the item codes it wears (original catalog items with art, checked
// at load against the shop catalog); SkinColor is avatarSkinColor (0 = the default tone [I]).
type ButlerDef struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Gender    string   `json:"gender"`
	Skin      string   `json:"skin"`
	Outfit    []string `json:"outfit"`
	SkinColor int64    `json:"skin_color"`
}

var butlersFile = opdata.New("butlers.json", data.ButlersJSON, checkButlers, verifyButlers)

// ActiveButlers is the butler file in use.
func ActiveButlers() *Butlers { return butlersFile.Get() }

func checkButlers(b *Butlers) error {
	if b.Origin != "cherry" {
		return errors.New(`origin must be "cherry": the butlers are Cherry-made`)
	}
	if strings.TrimSpace(b.Note) == "" {
		return errors.New("note must say that the butlers are Cherry-made")
	}
	if b.FreeDays < 1 {
		return errors.New("free_days must be at least 1")
	}
	if len(b.Hire) < 1 {
		return errors.New("hire must list at least one period")
	}
	days := map[int64]bool{}
	for _, p := range b.Hire {
		if p.Days < 1 || p.Cash < 1 || days[p.Days] {
			return fmt.Errorf("hire: days %d must be unique and at least 1, cash at least 1", p.Days)
		}
		days[p.Days] = true
	}
	g := b.Gathering
	if g.Gems < 0 || g.GemsCap < 0 || g.GathersPerDay < 0 {
		return errors.New("gathering: gems, gems_cap and gathers_per_day must not be negative")
	}
	kinds := map[string]bool{}
	for _, k := range g.Kinds {
		if _, ok := gatherKindOf(k); !ok || kinds[k] {
			return fmt.Errorf("gathering: kind %q must be fish, essence or ore, listed once", k)
		}
		kinds[k] = true
	}
	if len(b.Butlers) < 1 {
		return errors.New("butlers must list at least one butler")
	}
	ids := map[int64]bool{}
	for _, d := range b.Butlers {
		switch {
		case !IsCherryButler(d.ID):
			return fmt.Errorf("butler %d is outside the Cherry block %d-%d", d.ID, CherryButlerFirst, CherryButlerLast)
		case ids[d.ID]:
			return fmt.Errorf("butler %d is listed twice", d.ID)
		case strings.TrimSpace(d.Name) == "":
			return fmt.Errorf("butler %d: name must not be empty", d.ID)
		case d.Gender != "MALE" && d.Gender != "FEMALE":
			return fmt.Errorf(`butler %d: gender must be "MALE" or "FEMALE"`, d.ID)
		case !strings.HasPrefix(d.Skin, "cherry_") || len(d.Skin) == len("cherry_"):
			return fmt.Errorf(`butler %d: skin must start with "cherry_"`, d.ID)
		case d.SkinColor < 0:
			return fmt.Errorf("butler %d: skin_color must not be negative", d.ID)
		case len(d.Outfit) == 0:
			return fmt.Errorf("butler %d: outfit must list item codes", d.ID)
		}
		for _, code := range d.Outfit {
			if _, ok := itemcode.ID(code); !ok {
				return fmt.Errorf("butler %d: outfit code %q is not an item code", d.ID, code)
			}
		}
		ids[d.ID] = true
	}
	return nil
}

// verifyButlers runs at load (not for the embedded default alone): every outfit code must be a fashion item of the
// shop catalog (so it has art), and each outfit needs hair (HA), shoes (SH) and a one-piece (ON) or a top and a
// bottom (TO, PA). The archive is read here, as the shop catalog reads it.
func verifyButlers(b *Butlers) error {
	catalog := map[string]bool{}
	for _, it := range loadCatalog().fashion {
		catalog[it.Code] = true
	}
	for _, d := range b.Butlers {
		var cats []string
		for _, code := range d.Outfit {
			if !catalog[code] {
				return fmt.Errorf("butler %d: outfit item %s is not a shop item with art", d.ID, code)
			}
			cats = append(cats, code[2:4])
		}
		hasCat := func(c string) bool { return slices.Contains(cats, c) }
		if !hasCat("HA") || !hasCat("SH") || !(hasCat("ON") || hasCat("TO") && hasCat("PA")) {
			return fmt.Errorf("butler %d: outfit needs hair, shoes and a one-piece or a top and a bottom", d.ID)
		}
	}
	return nil
}

// ButlerOutfitIDs returns the numeric item ids of d's outfit, in the outfit's order.
func ButlerOutfitIDs(d ButlerDef) []int64 {
	ids := make([]int64, 0, len(d.Outfit))
	for _, code := range d.Outfit {
		if id, ok := itemcode.ID(code); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// butlerItemDir is the item folder of numeric id under the archive's dress or custom tree, with its kind.
func butlerItemDir(id int64) (dir, kind string, ok bool) {
	for _, k := range []string{"dress", "custom"} {
		dir = filepath.Join(thumbs.DpItemRoot, k, strconv.FormatInt(id, 10))
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir, k, true
		}
	}
	return "", "", false
}

// ButlerPortrait returns the portrait PNG of roster butler id: its outfit on the idle rig (thumbs.RenderButler), keyed
// by the outfit so a changed outfit gets a new cache file. ok is false for a butler outside the roster or an outfit
// item whose folder is missing.
func ButlerPortrait(id int64) ([]byte, bool) {
	d, ok := ButlerByID(id)
	if !ok {
		return nil, false
	}
	var parts []thumbs.ButlerPart
	h := fnv.New32a()
	fmt.Fprintf(h, "%d|%s", d.SkinColor, strings.Join(d.Outfit, ","))
	for _, item := range ButlerOutfitIDs(d) {
		dir, kind, ok := butlerItemDir(item)
		if !ok {
			return nil, false
		}
		parts = append(parts, thumbs.ButlerPart{Dir: dir, Kind: kind})
	}
	return thumbs.RenderButler(fmt.Sprintf("%d_%08x", id, h.Sum32()), parts)
}

// HandleButlerImage serves GET /cherry/butler/<id>.png: the butler's portrait (ButlerPortrait). It is Cherry-made.
func HandleButlerImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cherry/butler/"), ".png"), 10, 64)
	if err != nil || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	b, ok := ButlerPortrait(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

// ButlerByID returns the roster butler with id.
func ButlerByID(id int64) (ButlerDef, bool) {
	for _, b := range ActiveButlers().Butlers {
		if b.ID == id {
			return b, true
		}
	}
	return ButlerDef{}, false
}

// ButlerGatherType is the client's GatheringType of a butler's gathering: the first configured kind (1 fish,
// 2 essence, 3 ore), or 0 when none is configured.
func ButlerGatherType() int64 {
	kinds := ActiveButlers().Gathering.Kinds
	if len(kinds) == 0 {
		return 0
	}
	k, _ := gatherKindOf(kinds[0])
	return int64(k)
}

// gatherKindOf maps a data key ("fish", "essence", "ore") to its GatherKind.
func gatherKindOf(name string) (GatherKind, bool) {
	for k, v := range gatherKindNames {
		if v == name {
			return k, true
		}
	}
	return 0, false
}

// butlerHireOf returns book's contract of butler id when it runs at now.
func butlerHireOf(book *store.ButlerBook, id int64, now time.Time) (store.ButlerHire, bool) {
	for _, h := range book.Hired {
		if h.ID == id && h.End > now.UnixMilli() {
			return h, true
		}
	}
	return store.ButlerHire{}, false
}

// butlerChiefOf is the chief of book at now: the stored chief when its contract runs, else the running contract that
// began first. A book with running contracts and no running chief (a hire before the chief existed, or a chief that
// ran out) reads its earliest running contract as chief [I].
func butlerChiefOf(book *store.ButlerBook, now time.Time) (int64, bool) {
	if _, ok := butlerHireOf(book, book.Leader, now); ok {
		return book.Leader, true
	}
	var first store.ButlerHire
	found := false
	for _, h := range book.Hired {
		if h.End > now.UnixMilli() && (!found || h.Begin < first.Begin) {
			first, found = h, true
		}
	}
	return first.ID, found
}

// latestEnding is the id of the hire that ends last, or 0 for none: the chief a book falls back to when its chief
// ran out and other contracts remain [I].
func latestEnding(hires []store.ButlerHire) int64 {
	var id, end int64
	for _, h := range hires {
		if id == 0 || h.End > end {
			id, end = h.ID, h.End
		}
	}
	return id
}

// butlerDayOf is book's gathering on game day day; a record from another day counts as none.
func butlerDayOf(book *store.ButlerBook, day int64) (gems, gathers int64) {
	if book.Day != day {
		return 0, 0
	}
	return book.Gems, book.Gathers
}

// butlerBookOf returns a copy of acc's butler book (zero when none). The legacy per-butler free list is folded into
// FreeUsed and dropped, so every write made from the copy saves the account-wide flag. Lists in it are shared, so
// callers replace them rather than edit them.
func butlerBookOf(acc *store.Account) store.ButlerBook {
	if acc.Butlers == nil {
		return store.ButlerBook{}
	}
	b := *acc.Butlers
	b.FreeUsed = b.FreeUsed || len(b.Free) > 0
	b.Free = nil
	return b
}

// butlerPrice is the earned-Cash price of the hire period of days days.
func butlerPrice(days int64) (int64, bool) {
	for _, p := range ActiveButlers().Hire {
		if p.Days == days {
			return p.Cash, true
		}
	}
	return 0, false
}

// ButlerView is one account's butler state, copied under AccountsMu: the Cash balance, the butler book and the
// time it is read at.
type ButlerView struct {
	Cash int64
	book store.ButlerBook
	now  time.Time
}

// ButlerViewOf reads aid's butler state at now (an unknown account has no butlers and no Gems).
func ButlerViewOf(aid string, now time.Time) ButlerView {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	v := ButlerView{now: now}
	if acc := store.AccountByAidLocked(aid); acc != nil {
		v.Cash = acc.Cash
		v.book = butlerBookOf(acc)
	}
	return v
}

// Hire returns butler id's contract when it runs now.
func (v ButlerView) Hire(id int64) (store.ButlerHire, bool) { return butlerHireOf(&v.book, id, v.now) }

// FreeOpen reports whether the account's free first hire (once per account) is still open. While it is, the
// client shows only the free option on every row.
func (v ButlerView) FreeOpen() bool { return !v.book.FreeUsed }

// Chief is the chief butler's id at the view's time (butlerChiefOf), else 0.
func (v ButlerView) Chief() int64 {
	id, _ := butlerChiefOf(&v.book, v.now)
	return id
}

// Today returns the Gems and the gathers of the butler gathering on the current game day.
func (v ButlerView) Today() (gems, gathers int64) { return butlerDayOf(&v.book, AttendDayKey(v.now)) }

// ExpireButlers drops aid's contracts that ran out by now and the chief if his ran out. It returns the roster
// butlers that left (the Book's expiry notice) and saves; nothing changes when the save fails.
func ExpireButlers(aid string, now time.Time) ([]ButlerDef, error) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil || acc.Butlers == nil {
		return nil, nil
	}
	var gone []ButlerDef
	var keep []store.ButlerHire
	for _, h := range acc.Butlers.Hired {
		if h.End > now.UnixMilli() {
			keep = append(keep, h)
		} else if d, ok := ButlerByID(h.ID); ok {
			gone = append(gone, d)
		}
	}
	if len(gone) == 0 {
		return nil, nil
	}
	next := butlerBookOf(acc)
	next.Hired = keep
	if _, ok := butlerHireOf(&next, next.Leader, now); !ok {
		next.Leader = latestEnding(keep) // the chief ran out: the contract that ends last takes over [I]
	}
	prev := *acc
	acc.Butlers = &next
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		return nil, err
	}
	return gone, nil
}

// ButlerHired is one hire: the roster butler, its contract, the Cash charged and whether it was the free first hire.
type ButlerHired struct {
	Butler ButlerDef
	Hire   store.ButlerHire
	Price  int64
	Free   bool
}

// HireButler hires butler id for aid for employDays days. The price is the server's: the earned Cash of that
// period, or nothing for the free first hire (wantFree, employDays = free_days, the account's first hire only).
// costType must be ButlerCoinCash. A hire of a butler under contract extends it. The Cash goes through the ledger
// (reason butler:hire:<id>:<days>) and the save happens before the reply: a short Cash balance, an unknown period
// or a failed save refuses with nothing spent.
func HireButler(aid string, id, costType, employDays int64, wantFree bool, now time.Time) (ButlerHired, error) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	butler, ok := ButlerByID(id)
	if acc == nil || !ok || costType != ButlerCoinCash {
		return ButlerHired{}, ErrButlerRefused
	}
	book := butlerBookOf(acc)
	first := !book.FreeUsed // the account's first hire, paid or free, uses the free offer
	free := wantFree && first && employDays == ActiveButlers().FreeDays
	var price int64
	if !free {
		p, ok := butlerPrice(employDays)
		if !ok {
			return ButlerHired{}, ErrButlerRefused
		}
		price = p
	}
	h := store.ButlerHire{ID: id, Begin: now.UnixMilli(), End: now.UnixMilli() + employDays*dayMillis}
	if cur, ok := butlerHireOf(&book, id, now); ok {
		h = store.ButlerHire{ID: id, Begin: cur.Begin, End: cur.End + employDays*dayMillis}
	}
	next := book
	next.Hired = append(slices.DeleteFunc(slices.Clone(book.Hired), func(x store.ButlerHire) bool { return x.ID == id }), h)
	if _, ok := butlerChiefOf(&book, now); !ok {
		next.Leader = id // no running chief: the first hire becomes the chief
	}
	if first {
		next.FreeUsed = true
	}
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Cash: -price}, fmt.Sprintf("butler:hire:%d:%d", id, employDays))
	if err != nil {
		*acc = prev
		return ButlerHired{}, ErrButlerRefused
	}
	acc.Butlers = &next
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		return ButlerHired{}, err
	}
	store.FlushLedgerLocked(lines)
	return ButlerHired{Butler: butler, Hire: h, Price: price, Free: free}, nil
}

// SetButlerChief makes butler id the chief of aid's Garden. The butler must be under contract now.
func SetButlerChief(aid string, id int64, now time.Time) error {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return ErrButlerRefused
	}
	book := butlerBookOf(acc)
	if _, ok := butlerHireOf(&book, id, now); !ok {
		return ErrButlerRefused
	}
	book.Leader = id
	prev := *acc
	acc.Butlers = &book
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		return err
	}
	return nil
}

// ButlerChief returns aid's chief and its contract when the contract runs at now. The Garden shows it to every
// visitor, so the relay start reads the owner's chief.
func ButlerChief(aid string, now time.Time) (ButlerDef, store.ButlerHire, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil || acc.Butlers == nil {
		return ButlerDef{}, store.ButlerHire{}, false
	}
	book := butlerBookOf(acc)
	id, ok := butlerChiefOf(&book, now)
	if !ok {
		return ButlerDef{}, store.ButlerHire{}, false
	}
	h, _ := butlerHireOf(&book, id, now)
	d, ok := ButlerByID(id)
	return d, h, ok
}

// ButlerGathered is one butler gathering: the Garden materials paid into the Bag (one per configured kind) and
// the Gems paid.
type ButlerGathered struct {
	Drops []GatherDrop
	Gems  int64
}

// GatherButler pays one gathering of butler id for aid: one drop of each configured kind into the Bag, and Gems
// up to gems_cap a game day. The butler must be under contract and today's gathers below gathers_per_day, else
// ErrButlerRefused with nothing changed. The Gems go through the ledger (reason butler:gather:<date>) and the
// save happens before the reply. roll is math/rand/v2.IntN in production.
func GatherButler(aid string, id int64, now time.Time, roll func(n int) int) (ButlerGathered, error) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return ButlerGathered{}, ErrButlerRefused
	}
	book := butlerBookOf(acc)
	g := ActiveButlers().Gathering
	day := AttendDayKey(now)
	gems, gathers := butlerDayOf(&book, day)
	if _, ok := butlerHireOf(&book, id, now); !ok || gathers >= g.GathersPerDay {
		return ButlerGathered{}, ErrButlerRefused
	}
	var got ButlerGathered
	bag := acc.GardenBag
	for _, name := range g.Kinds {
		kind, _ := gatherKindOf(name)
		if pick, ok := PickGather(kind, roll); ok {
			got.Drops = append(got.Drops, pick.Drop)
			bag = gardenBagChange(bag, pick.Drop.Item, 1, now)
		}
	}
	got.Gems = max(min(g.Gems, g.GemsCap-gems), 0)
	next := store.ButlerBook{Hired: book.Hired, Leader: book.Leader, FreeUsed: book.FreeUsed, Day: day, Gems: gems + got.Gems, Gathers: gathers + 1}
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: got.Gems}, "butler:gather:"+questDate(day))
	if err != nil {
		*acc = prev
		return ButlerGathered{}, ErrButlerRefused
	}
	acc.GardenBag = bag
	acc.Butlers = &next
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		return ButlerGathered{}, err
	}
	store.FlushLedgerLocked(lines)
	return got, nil
}
