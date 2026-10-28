package economy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
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
)

// Garden butlers (Garden LPN agent 10; spec butler-spec.md). The Shop hires a butler for earned Cash, the Book
// lists the hired ones and sets the chief (the butler the Garden shows), a hired butler's gathering pays Garden
// materials and a few Gems, and the Profile popup shows a butler's details. The seven roster butlers are the original
// ones: names, abilities, portraits and profile texts come from the client's files, and so do the outfit items except
// the look-alikes Cherry picked (ButlerDef.lookalikes). data/butlers.json says which field is which. Their ids, skin
// codes, skin colours, prices and gathering amounts are Cherry's own, in a reserved block.

// CherryButlerFirst and CherryButlerLast bound the butler ids Cherry makes. checkButlers refuses an id outside it.
const (
	CherryButlerFirst int64 = 1900000000
	CherryButlerLast  int64 = 1900000999
)

// retiredButlerIDs are the ids of the first Cherry roster (Hazel, Wren and Oswin). They are never reused: a contract
// that names one is retired by MigrateButlerRoster.
var retiredButlerIDs = []int64{CherryButlerFirst + 1, CherryButlerFirst + 2, CherryButlerFirst + 3}

// IsCherryButler reports whether id is in Cherry's own butler block.
func IsCherryButler(id int64) bool { return id >= CherryButlerFirst && id <= CherryButlerLast }

// ButlerCoinCash is the CoinType of a hire price (earned Cash). [I] 2 is the Cash type of the Square exchange
// (lpn square_exchange.go); the spec (section 4) says the client special-cases CoinType 2 in ParsePacket and
// its use is unresolved, so this needs a device check.
const ButlerCoinCash int64 = 2

// ErrButlerRefused is a hire, chief change or gathering the rules do not allow now. Nothing changed.
var ErrButlerRefused = errors.New("butler: refused")

const dayMillis = 86400000

// butlerArtFS is the original UI art tree (lpbackup UIResource/UIImage_hd). The portraits are read from it in place.
var butlerArtFS fs.FS = os.DirFS(filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lpbackup/jp.naver.lineplay.android/files/UIResource/UIImage_hd`))

// Butlers is data/butlers.json. Strict JSON: unknown keys are an error.
//
//	origin      must be "mixed": the roster is original; the ids, skin codes, prices and gathering amounts are Cherry-made
//	note        says per field which are original and which are Cherry-made; text for operators, not sent to the client
//	free_days   days of the account's first hire, free once per account (a first hire, paid or free, uses it)
//	hire        the one 30-day period and its earned-Cash price (the client's contract picker offers 30 days only)
//	gathering   gems: Gems per gather; gems_cap: Gems a game day from butler gathering, per account; gathers_per_day:
//	            gathers a game day per account (0 turns butler gathering off). A gather pays one drop of each of the
//	            butler's abilities (ButlerDef.Abilities)
//	butlers     one or more roster butlers
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
	Gems          int64 `json:"gems"`
	GemsCap       int64 `json:"gems_cap"`
	GathersPerDay int64 `json:"gathers_per_day"`
}

// ButlerDef is one roster butler.
//
//	id         in the Cherry block, never a retired id (Cherry-made)
//	name       the original name (string table 9854-9860)
//	gender     "MALE", "FEMALE" or "ANIMAL" (the client's sex enum 0, 1, 2); from the portrait [I]
//	skin       the one default skin code, "cherry_" + a name (Cherry-made)
//	skin_color avatarSkinColor: an index into the client's skin palette g_akHBC (0 to 16; 0 is a tan tint, 1 the
//	           untinted base). Cherry chose the values from the portraits (original palette indices). ANIMAL is ignored
//	           by the client. Sent as is; an absent field would read as 0
//	outfit     item codes the skin wears: original items (checked at load: a shop fashion item with art, or a Face
//	           Shop part, human CU or animal CA). At most one item per 2-letter slot (EY, ON, AH, ...). A slot no
//	           archive item matches is left out, or filled by a look-alike (lookalikes), and the note says so
//	lookalikes Cherry-picked look-alikes: codes of outfit (checked at load) that stand in for a true item no archive
//	           has. Cherry-made choice, not the true item. Optional; absent means none. Added 2026-10-29
//	abilities  gather kinds ("fish", "essence", "ore") of the original abilities: Fishing, collecting Drops, mining
//	art        the original portrait slot b1 to b7: UIResource/UIImage_hd/butler_event_profile_<art>.png, served as is
//	profile    the original blood type, birthday, keywords and quote: kept for the record (no verified protocol field)
//	note       what is empty or uncertain in this butler (optional)
type ButlerDef struct {
	ID         int64         `json:"id"`
	Name       string        `json:"name"`
	Gender     string        `json:"gender"`
	Skin       string        `json:"skin"`
	SkinColor  int64         `json:"skin_color"`
	Outfit     []string      `json:"outfit"`
	Lookalikes []string      `json:"lookalikes,omitempty"`
	Abilities  []string      `json:"abilities"`
	Art        string        `json:"art"`
	Profile    ButlerProfile `json:"profile"`
	Note       string        `json:"note,omitempty"`
}

// ButlerProfile is the original profile text of a roster butler (its name card and quote in the client files).
type ButlerProfile struct {
	BloodType string   `json:"blood_type"`
	Birthday  string   `json:"birthday"`
	Keywords  []string `json:"keywords"`
	Quote     string   `json:"quote"`
}

var butlersFile = opdata.New("butlers.json", data.ButlersJSON, checkButlers, verifyButlers)

// ActiveButlers is the butler file in use.
func ActiveButlers() *Butlers { return butlersFile.Get() }

func checkButlers(b *Butlers) error {
	if b.Origin != "mixed" {
		return errors.New(`origin must be "mixed": the roster is original, the ids, skins, prices and gathering amounts are Cherry-made`)
	}
	if strings.TrimSpace(b.Note) == "" {
		return errors.New("note must say which fields are original and which are Cherry-made")
	}
	if b.FreeDays < 1 {
		return errors.New("free_days must be at least 1")
	}
	// The client's contract picker (CButtonHandler 0x22cfb84, cmp w2,#0x1E) builds rows only for 30-day entries and
	// labels them "+30day": any other period would buy the wrong entry of the price vector.
	if len(b.Hire) != 1 || b.Hire[0].Days != 30 {
		return errors.New("hire must list exactly one 30-day period: the client's contract picker (CButtonHandler 0x22cfb84) offers only 30 days")
	}
	if b.Hire[0].Cash < 1 {
		return errors.New("hire: cash must be at least 1")
	}
	g := b.Gathering
	if g.Gems < 0 || g.GemsCap < 0 || g.GathersPerDay < 0 {
		return errors.New("gathering: gems, gems_cap and gathers_per_day must not be negative")
	}
	if len(b.Butlers) < 1 {
		return errors.New("butlers must list at least one butler")
	}
	ids := map[int64]bool{}
	for _, d := range b.Butlers {
		switch {
		case !IsCherryButler(d.ID):
			return fmt.Errorf("butler %d is outside the Cherry block %d-%d", d.ID, CherryButlerFirst, CherryButlerLast)
		case slices.Contains(retiredButlerIDs, d.ID):
			return fmt.Errorf("butler %d is retired (Hazel, Wren or Oswin): its id is never reused", d.ID)
		case ids[d.ID]:
			return fmt.Errorf("butler %d is listed twice", d.ID)
		case strings.TrimSpace(d.Name) == "":
			return fmt.Errorf("butler %d: name must not be empty", d.ID)
		case d.Gender != "MALE" && d.Gender != "FEMALE" && d.Gender != "ANIMAL":
			return fmt.Errorf(`butler %d: gender must be "MALE", "FEMALE" or "ANIMAL"`, d.ID)
		case !strings.HasPrefix(d.Skin, "cherry_") || len(d.Skin) == len("cherry_"):
			return fmt.Errorf(`butler %d: skin must start with "cherry_"`, d.ID)
		case d.SkinColor < 0:
			return fmt.Errorf("butler %d: skin_color must not be negative", d.ID)
		case len(d.Outfit) == 0:
			return fmt.Errorf("butler %d: outfit must list item codes", d.ID)
		case len(d.Abilities) == 0:
			return fmt.Errorf("butler %d: abilities must list fish, essence or ore", d.ID)
		case d.Art == "":
			return fmt.Errorf("butler %d: art must name the original portrait slot (b1 to b7)", d.ID)
		}
		seen := map[string]bool{}
		for _, a := range d.Abilities {
			if _, ok := gatherKindOf(a); !ok || seen[a] {
				return fmt.Errorf("butler %d: ability %q must be fish, essence or ore, listed once", d.ID, a)
			}
			seen[a] = true
		}
		slots := map[string]bool{}
		for _, code := range d.Outfit {
			if _, ok := itemcode.ID(code); !ok {
				return fmt.Errorf("butler %d: outfit code %q is not an item code", d.ID, code)
			}
			slot := store.ItemSlot(code)
			if slots[slot] {
				return fmt.Errorf("butler %d: outfit wears two %s items (one item per slot): %s", d.ID, slot, code)
			}
			slots[slot] = true
		}
		for _, code := range d.Lookalikes {
			if !slices.Contains(d.Outfit, code) {
				return fmt.Errorf("butler %d: look-alike %s is not in outfit", d.ID, code)
			}
		}
		ids[d.ID] = true
	}
	return nil
}

// verifyButlers runs at load (not for the embedded default alone): every outfit code must be a shop fashion item with
// art, or a part the Face Shop lists (faceForSale: human faces, animal parts, converted ones), and every portrait must be
// in the archive. No outfit slot is required: an archive without an item for a slot leaves it empty. The archive is read
// here, as the shop catalog reads it.
func verifyButlers(b *Butlers) error {
	fashion := map[string]bool{}
	for _, it := range loadCatalog().fashion {
		fashion[it.Code] = true
	}
	for _, d := range b.Butlers {
		for _, code := range d.Outfit {
			if !fashion[code] && !faceForSale(code) {
				return fmt.Errorf("butler %d: outfit item %s is not a shop item with art or a Face Shop part", d.ID, code)
			}
		}
		if _, err := fs.Stat(butlerArtFS, butlerArtFile(d)); err != nil {
			return fmt.Errorf("butler %d: portrait %s is not in the archive", d.ID, butlerArtFile(d))
		}
	}
	return nil
}

// butlerArtFile is the original portrait file of a roster butler.
func butlerArtFile(d ButlerDef) string { return "butler_event_profile_" + d.Art + ".png" }

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

// ButlerPortrait returns the portrait of roster butler id: the original art, read as it is from the archive. ok is false
// for a butler outside the roster or a missing file. Nothing is generated.
func ButlerPortrait(id int64) ([]byte, bool) {
	d, ok := ButlerByID(id)
	if !ok {
		return nil, false
	}
	b, err := fs.ReadFile(butlerArtFS, butlerArtFile(d))
	return b, err == nil
}

// HandleButlerImage serves GET /cherry/butler/<id>.png: the roster butler's original portrait (ButlerPortrait). The
// route is Cherry's; the picture is the client's.
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

// ButlerAbilities returns the client's GatheringType (1 fish, 2 essence, 3 ore) of each ability of d, in the data's order.
func ButlerAbilities(d ButlerDef) []GatherKind {
	out := make([]GatherKind, 0, len(d.Abilities))
	for _, name := range d.Abilities {
		if k, ok := gatherKindOf(name); ok {
			out = append(out, k)
		}
	}
	return out
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

// ButlerGathered is one butler gathering: the Garden materials paid into the Bag (one per ability) and the Gems paid.
type ButlerGathered struct {
	Drops []GatherDrop
	Gems  int64
}

// GatherButler pays one gathering of butler id for aid: one drop of each of the butler's abilities into the Bag, and
// Gems up to gems_cap a game day. The butler must be under contract and today's gathers below gathers_per_day, else
// ErrButlerRefused with nothing changed. The Gems go through the ledger (reason butler:gather:<date>) and the save
// happens before the reply. roll is math/rand/v2.IntN in production.
func GatherButler(aid string, id int64, now time.Time, roll func(n int) int) (ButlerGathered, error) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	d, ok := ButlerByID(id)
	if acc == nil || !ok {
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
	for _, kind := range ButlerAbilities(d) {
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

// butlerDebit is one Cash charge of a butler hire: when (epoch ms; the ledger keeps whole seconds) and how much.
type butlerDebit struct {
	at, amount int64
}

// butlerHireDebits reads the butler hire charges of every account from ledger.jsonl next to the account store, by
// account and butler id. A missing ledger has none.
func butlerHireDebits() (map[string]map[int64][]butlerDebit, error) {
	out := map[string]map[int64][]butlerDebit{}
	if store.AccountStorePath == "" {
		return out, nil
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(store.AccountStorePath), "ledger.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		var l store.LedgerLine
		if json.Unmarshal([]byte(line), &l) != nil || l.Currency != "cash" || l.Delta >= 0 {
			continue
		}
		rest, ok := strings.CutPrefix(l.Reason, "butler:hire:")
		if !ok {
			continue
		}
		idText, _, _ := strings.Cut(rest, ":")
		id, idErr := strconv.ParseInt(idText, 10, 64)
		at, timeErr := time.Parse(time.RFC3339, l.Time)
		if idErr != nil || timeErr != nil {
			continue
		}
		if out[l.Aid] == nil {
			out[l.Aid] = map[int64][]butlerDebit{}
		}
		out[l.Aid][id] = append(out[l.Aid][id], butlerDebit{at: at.UnixMilli(), amount: -l.Delta})
	}
	return out, nil
}

// paidSince is the Cash that the hires of one butler charged for a contract beginning at begin (epoch ms): the charges
// at or after begin, with begin cut to the second the ledger keeps. An earlier contract of the same butler is not counted.
func paidSince(ds []butlerDebit, begin int64) int64 {
	var sum int64
	for _, d := range ds {
		if d.at >= begin/1000*1000 {
			sum += d.amount
		}
	}
	return sum
}

// MigrateButlerRoster retires the butlers that left the roster (data/butlers.json) from every account, once. Their
// contracts are dropped, and a chief among them is re-picked from the contracts left (the one that ends last, as at
// expiry). An unexpired contract is refunded the Cash its hires charged, as the ledger shows (reason butler:refund:<id>):
// a free hire charged nothing and refunds nothing, and an expired contract refunds nothing. The free-hire flag stays
// as it is. An account with no retired contract is not written, so a second run refunds nothing. cmd/cherry calls it at
// startup, after the operator data loads. A failed save restores every account and returns the error.
func MigrateButlerRoster(now time.Time) error {
	debits, err := butlerHireDebits()
	if err != nil {
		return err
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	retired := func(id int64) bool { _, ok := ButlerByID(id); return !ok }
	type change struct {
		acc  *store.Account
		prev store.Account
	}
	var changes []change
	var lines []store.LedgerLine
	restore := func() {
		for _, c := range changes {
			*c.acc = c.prev
		}
	}
	seen := map[*store.Account]bool{} // token aliases share one account
	for _, acc := range store.Accounts {
		if seen[acc] || acc.Butlers == nil {
			seen[acc] = true
			continue
		}
		seen[acc] = true
		book := butlerBookOf(acc)
		changed := slices.ContainsFunc(acc.Butlers.Free, retired)
		var keep []store.ButlerHire
		var gone []int64
		type refundOf struct{ id, amount int64 }
		var refunds []refundOf
		for _, h := range book.Hired {
			if !retired(h.ID) {
				keep = append(keep, h)
				continue
			}
			changed = true
			gone = append(gone, h.ID)
			if h.End > now.UnixMilli() {
				if amount := paidSince(debits[acc.Aid][h.ID], h.Begin); amount > 0 {
					refunds = append(refunds, refundOf{h.ID, amount})
				}
			}
		}
		if !changed {
			continue
		}
		changes = append(changes, change{acc, *acc})
		var refund int64
		for _, r := range refunds {
			ls, err := store.SpendLocked(acc, store.LedgerDeltas{Cash: r.amount}, fmt.Sprintf("butler:refund:%d", r.id))
			if err != nil {
				restore()
				return fmt.Errorf("butler roster: refund to aid %s: %w", acc.Aid, err)
			}
			lines = append(lines, ls...)
			refund += r.amount
		}
		var running []store.ButlerHire
		for _, h := range keep {
			if h.End > now.UnixMilli() {
				running = append(running, h)
			}
		}
		book.Hired = keep
		if _, ok := butlerHireOf(&book, book.Leader, now); !ok {
			book.Leader = latestEnding(running)
		}
		acc.Butlers = &book
		log.Printf("cherry: butler roster: aid=%s retired contracts %v, refund %d Cash", acc.Aid, gone, refund)
	}
	if len(changes) == 0 {
		return nil
	}
	if err := store.SaveAccountsLocked(); err != nil {
		restore()
		return err
	}
	store.FlushLedgerLocked(lines)
	return nil
}
