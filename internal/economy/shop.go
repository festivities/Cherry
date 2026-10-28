package economy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/httpx"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Economy Phase C (first pass): Fashion / Interior / Pet shops and sell-back, priced from
// Cherry's own table. Protocol notes: .agents/PLAN.md "Economy", static research
// shops-phaseC.md (NaFashionShopLayer, ResShopItemWithShopType @0x1a87c30, buy replies
// @0x1b36238 / @0x1b2b7d8 / @0x1b2bba0, ResRecycleCloset @0x1c1f1e4).

// ---- price table ----

// Prices is data/prices.json: the shops' price rules, sell-back and daily deals, the Face Shop ticket
// packs and the item names Cherry shows. Keys are the category letters of an item code (code[2:4])
// or a room category code, as the client sends them.
//
//	clothing_prices    {letters: Gems}: base price of clothing (CU and CA codes)
//	interior_price     Gems for furniture; interior_prices {letters: Gems} overrides it per category
//	pet_price          Gems for a pet; animated_pet_cash is the Cash price of an animated pet
//	landscape_cash     Cash price of every Garden landscape (gardens.json, landscape.go)
//	animated_percent   animated (RARE) clothing and furniture cost this percent of base, halves up
//	sell_back_percent  an owned item pays back this percent of what it cost (0 to 100); a deal item
//	                   counts what it was bought for
//	face_ticket_cost   Face Shop tickets one face change costs (0 or more)
//	deal_paid_percent  a daily deal charges this percent of the table price (1 to 100; 70 = 30% off)
//	deals_per_kind     daily deals per shop kind (clothing, furniture)
//	clothing_names     {letters: name}, room_names {category code: name}: the names Cherry shows
//	face_tickets[]     {code, tickets, standard, price, name}: Face Shop ticket packs sold for Gems
//	                   (price 0 to standard; standard at least 1)
type Prices struct {
	ClothingPrices  map[string]int64  `json:"clothing_prices"`
	InteriorPrice   int64             `json:"interior_price"`
	InteriorPrices  map[string]int64  `json:"interior_prices"`
	PetPrice        int64             `json:"pet_price"`
	AnimatedPercent int64             `json:"animated_percent"`
	AnimatedPetCash int64             `json:"animated_pet_cash"`
	LandscapeCash   int64             `json:"landscape_cash"`
	SellBackPercent int64             `json:"sell_back_percent"`
	FaceTicketCost  int64             `json:"face_ticket_cost"`
	DealPaidPercent int64             `json:"deal_paid_percent"`
	DealsPerKind    int               `json:"deals_per_kind"`
	ClothingNames   map[string]string `json:"clothing_names"`
	RoomNames       map[string]string `json:"room_names"`
	FaceTickets     []FaceTicket      `json:"face_tickets"`
}

// FaceTicket is one Face Shop ticket pack, bought with Gems (the client never sends a price).
type FaceTicket struct {
	Code     string `json:"code"`
	Tickets  int    `json:"tickets"`
	Standard int64  `json:"standard"` // list price, Gems
	Price    int64  `json:"price"`    // charged, Gems
	Name     string `json:"name"`
}

var pricesFile = opdata.New("prices.json", data.PricesJSON, checkPrices, nil)

// ActivePrices is the price file in use.
func ActivePrices() *Prices { return pricesFile.Get() }

func checkPrices(p *Prices) error {
	if err := checkCategoryMap("clothing_prices", p.ClothingPrices); err != nil {
		return err
	}
	if err := checkCategoryMap("interior_prices", p.InteriorPrices); err != nil {
		return err
	}
	for _, v := range []struct {
		key string
		val int64
	}{{"interior_price", p.InteriorPrice}, {"pet_price", p.PetPrice}, {"animated_percent", p.AnimatedPercent}, {"animated_pet_cash", p.AnimatedPetCash}, {"landscape_cash", p.LandscapeCash}} {
		if v.val < 0 {
			return fmt.Errorf("%s must not be negative", v.key)
		}
	}
	if p.SellBackPercent < 0 || p.SellBackPercent > 100 {
		return errors.New("sell_back_percent must be 0 to 100")
	}
	if p.DealPaidPercent < 1 || p.DealPaidPercent > 100 {
		return errors.New("deal_paid_percent must be 1 to 100")
	}
	if p.FaceTicketCost < 0 {
		return errors.New("face_ticket_cost must not be negative")
	}
	if p.DealsPerKind < 0 {
		return errors.New("deals_per_kind must not be negative")
	}
	for _, names := range []map[string]string{p.ClothingNames, p.RoomNames} {
		for _, code := range slices.Sorted(maps.Keys(names)) {
			if names[code] == "" {
				return fmt.Errorf("name for %s must not be empty", code)
			}
		}
	}
	seen := map[string]bool{}
	for _, t := range p.FaceTickets {
		switch {
		case t.Code == "" || seen[t.Code]:
			return fmt.Errorf("face ticket %q: code must be unique and not empty", t.Code)
		case t.Tickets < 1:
			return fmt.Errorf("face ticket %s: tickets must be at least 1", t.Code)
		case t.Standard < 1 || t.Price < 0 || t.Price > t.Standard:
			return fmt.Errorf("face ticket %s: price must be from 0 to standard, and standard at least 1", t.Code)
		case t.Name == "":
			return fmt.Errorf("face ticket %s: name must not be empty", t.Code)
		}
		seen[t.Code] = true
	}
	return nil
}

// checkCategoryMap checks a {letters: Gems} table: two capital letters per key, no negative price.
func checkCategoryMap(key string, m map[string]int64) error {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if len(k) != 2 || k != strings.ToUpper(k) {
			return fmt.Errorf("%s: key %q must be two capital letters", key, k)
		}
		if m[k] < 0 {
			return fmt.Errorf("%s: %s must not be negative", key, k)
		}
	}
	return nil
}

const (
	kindFashion  = 'F'
	kindInterior = 'I'
	kindPet      = 'P'
	kindGarden   = 'G' // Garden landscapes (landscape.go): Cash, bought through purchase/interior/buy
)

// classify returns the Gem base price and shop kind of a code (kind 0 = not sellable:
// faces, which the Face Shop sells, and anything the server cannot classify).
func classify(code string) (base int64, kind byte) {
	if len(code) != 9 {
		return 0, 0
	}
	p := ActivePrices()
	switch code[:2] {
	case "CU", "CA":
		if v, ok := p.ClothingPrices[code[2:4]]; ok {
			return v, kindFashion
		}
	case "RU":
		if v, ok := p.InteriorPrices[code[2:4]]; ok {
			return v, kindInterior
		}
		return p.InteriorPrice, kindInterior
	case "PU":
		if code[2:4] == "PE" {
			return p.PetPrice, kindPet
		}
	case "GU": // only the landscapes gardens.json lists are sold; any other GULS code is unsellable
		if _, ok := listedLandscape(code); ok {
			return p.LandscapeCash, kindGarden
		}
	}
	return 0, 0
}

// ItemPrice is the one server price table (catalog, buy, inventory rows, recycle).
// Animated items (an .aniproj in the item dir) are premium: grade "R", animated_percent of the base
// price for clothing and furniture, animated_pet_cash for pets. Landscapes cost landscape_cash Cash.
// Unsellable codes return ok=false, price 0.
// Items without art keep their price here so that owned ones still sell back; the catalog
// (scanCatalog) is what keeps them out of shops, gacha, mail and season rewards.
func ItemPrice(code string) (price int64, cash bool, grade string, ok bool) {
	base, kind := classify(code)
	if kind == 0 {
		return 0, false, "N", false
	}
	if kind == kindGarden {
		return base, true, "N", true
	}
	if loadCatalog().animated[code] {
		if kind == kindPet {
			return ActivePrices().AnimatedPetCash, true, "R", true
		}
		return (base*ActivePrices().AnimatedPercent + 50) / 100, false, "R", true
	}
	return base, false, "N", true
}

// ---- catalog (item dirs the client has) ----

type shopItem struct {
	Code     string
	ID       int64
	Animated bool
	Catg     string // pets only: the client's pet categories UDOG, UCAT, UTOY, URIDE
}

type shopCatalog struct {
	root                   string
	generated              *assets.ItemIndex    // the converted items the catalog was scanned with (nil: switched off)
	fashion, interior, pet []shopItem           // sellable: art, so listed, pooled and granted
	landscape              []shopItem           // Garden landscapes gardens.json lists (no archive folder; landscape.go)
	byCode                 map[string]*shopItem // the same items by code
	animated               map[string]bool      // every priced code, art or not (ItemPrice; sell-back needs it)
}

var (
	catalogMu sync.Mutex
	catalog   *shopCatalog
)

// loadCatalog scans thumbs.DpItemRoot once (again only if the root changes, as tests do).
func loadCatalog() *shopCatalog {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if root := thumbs.DpItemRoot; catalog == nil || catalog.root != root || catalog.generated != assets.GeneratedItems() {
		catalog = scanCatalog(root)
	}
	return catalog
}

// PetSpecies is the Minipet Shop catalog (the sellable pets with art) for the Pet Book (room.HandlePetBook).
func PetSpecies() []room.PetSpecies {
	c := loadCatalog()
	out := make([]room.PetSpecies, 0, len(c.pet))
	for _, it := range c.pet {
		out = append(out, room.PetSpecies{Code: it.Code, ID: it.ID, Catg: it.Catg})
	}
	return out
}

// itemCodeFromID is the item code of numeric id n (itemcode.Code: SbItemTable::GetIntIdx inverted).
func itemCodeFromID(n int64) (string, bool) { return itemcode.Code(n) }

func hasFile(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

var sndIndexRE = regexp.MustCompile(`"sndindex":\s*(\d+)`)

// petCategory classifies a pet folder. The item data has no category field, but the pet's
// behavior.xml plays its species sound: sndindex 214 = fx_pet_cat, 215 = fx_pet_dog (adjacent
// entries of the sound table, cat first; 36 cats and 20 dogs in the santi dump, e.g. 326400031
// is a cat, 326400016 a dog). Rides carry .aniproj animations; the 41 pets left over with no
// sound are toys (teddy bears and the like). The category codes are the pet book's table at
// 0x3bed130: MYPET, UDOG, UCAT, UTOY, URIDE.
func petCategory(dir string, ride bool) string {
	if ride {
		return "URIDE"
	}
	if b, err := os.ReadFile(filepath.Join(dir, "behavior.xml")); err == nil {
		for _, m := range sndIndexRE.FindAllSubmatch(b, -1) {
			switch string(m[1]) {
			case "214":
				return "UCAT"
			case "215":
				return "UDOG"
			}
		}
	}
	return "UTOY"
}

func scanCatalog(root string) *shopCatalog {
	c := &shopCatalog{root: root, generated: assets.GeneratedItems(), byCode: map[string]*shopItem{}, animated: map[string]bool{}}
	for _, sub := range []string{"custom", "dress", "interior", "tile", "pet"} {
		entries, _ := os.ReadDir(filepath.Join(root, sub)) // a missing root or kind is an empty folder list
		for _, e := range entries {
			if n, err := strconv.ParseInt(e.Name(), 10, 64); err == nil && e.IsDir() {
				c.add(sub, n, filepath.Join(root, sub, e.Name()))
			}
		}
		// Converted iOS-only items (assets.GeneratedItems): Cherry-made, with art by construction, and no folder in root.
		for _, n := range c.generated.IDs(sub) {
			c.add(sub, n, "")
		}
	}
	c.landscape = landscapeCatalog()
	for _, list := range []*[]shopItem{&c.fashion, &c.interior, &c.pet, &c.landscape} {
		sort.Slice(*list, func(i, j int) bool { return (*list)[i].Code < (*list)[j].Code })
		for i := range *list {
			c.byCode[(*list)[i].Code] = &(*list)[i]
		}
	}
	return c
}

// add lists one item: the folder dir, or a converted item when dir is "". Faces, unclassifiable ids, duplicates
// and pets without render assets are not listed; a folder without art is priced but not listed.
func (c *shopCatalog) add(sub string, n int64, dir string) {
	code, ok := itemCodeFromID(n)
	if !ok {
		return
	}
	_, kind := classify(code)
	if kind == 0 || c.byCode[code] != nil || kind == kindPet && !hasFile(dir, "skeleton.xml") {
		return // faces, unclassifiable, duplicate, or a pet with no render assets
	}
	it := shopItem{Code: code, ID: n}
	if dir != "" {
		if files, err := os.ReadDir(dir); err == nil {
			it.Animated = slices.ContainsFunc(files, func(f os.DirEntry) bool {
				return strings.HasSuffix(strings.ToLower(f.Name()), ".aniproj")
			})
		}
	}
	if kind == kindPet {
		it.Catg = petCategory(dir, it.Animated)
	}
	// Priced whether or not it has art, so an owned copy sells back at its real price.
	c.animated[code] = it.Animated
	if dir != "" && !thumbs.HasArt(sub, n) {
		// No art: the client would show the grey placeholder, so it is not listed, pooled or granted.
		return
	}
	switch kind {
	case kindFashion:
		c.fashion = append(c.fashion, it)
	case kindInterior:
		c.interior = append(c.interior, it)
	default:
		c.pet = append(c.pet, it)
	}
}

// ---- catalog routes ----

const emptyItems = `{"result":{"items":[]}}`

func itemsBody(rows []map[string]any) string {
	if rows == nil {
		rows = []map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{"result": map[string]any{"items": rows}})
	return string(b)
}

// priceKeys are the keys every row kind shares; deals and limited items add their sale keys (deals.go).
func priceKeys(it shopItem, deals dealSet, now time.Time) map[string]any {
	price, cash, _, _ := ItemPrice(it.Code)
	row := map[string]any{
		"itemCode": it.Code, "name": "", "price": price, "discount": false, "discountedPrice": price,
		"paymentType": "GEM", "sellingIconType": "", "specialEffects": "",
		"soldout": false, "selling": true, "display": true, "sellingEnd": "", "discountEnd": "",
	}
	if cash {
		row["paymentType"] = "CASH"
	}
	if it.Animated {
		row["sellingIconType"], row["specialEffects"] = "RARE", "ANIMATION"
	}
	saleKeys(row, it.Code, deals, now)
	return row
}

// fashionSlots maps a fashion tab (M|F|A + DHAIR/HAIR, DRESS, TOPS, BTTM, SHOE, ACC, PTRN)
// to the code prefix and category letters. Anything else, including SET and all, is empty.
func fashionSlots(cat string) (prefix string, slots []string) {
	if len(cat) < 2 || !strings.Contains("MFA", cat[:1]) {
		return "", nil
	}
	prefix = "CU"
	if cat[0] == 'A' {
		prefix = "CA"
	}
	switch cat[1:] {
	case "DHAIR", "HAIR":
		return prefix, []string{"HA"}
	case "DRESS":
		return prefix, []string{"ON"}
	case "TOPS":
		return prefix, []string{"TO"}
	case "BTTM":
		return prefix, []string{"PA"}
	case "SHOE":
		return prefix, []string{"SH"}
	case "ACC":
		return prefix, []string{"AH", "AE", "FE"}
	}
	return "", nil // PTRN: animal category 57/58 letters are unverified
}

// requesterOwned returns the requester's room item codes, pet codes and landscape codes (empty if unknown).
func requesterOwned(r *http.Request) (rooms, pets, landscapes map[string]bool) {
	rooms, pets, landscapes = map[string]bool{}, map[string]bool{}, map[string]bool{}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	if acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]; acc != nil {
		for _, it := range acc.RoomItems {
			rooms[it.Cd] = true
		}
		for _, p := range acc.Pets {
			pets[p.Cd] = true
		}
		for _, l := range ownedLandscapesLocked(acc) {
			landscapes[l.Code] = true
		}
	}
	return
}

func handleShopList(kind byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.ServeNotFound(w)
			return
		}
		cat := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		c := loadCatalog()
		now := NowFunc()
		deals := dealsOn(now)
		var rows []map[string]any
		switch kind {
		case kindFashion:
			prefix, slots := fashionSlots(cat)
			for _, it := range c.fashion {
				if it.Code[:2] == prefix && slices.Contains(slots, it.Code[2:4]) && onSale(it.Code, now) {
					row := priceKeys(it, deals, now)
					row["itemID"], row["representImageURL"], row["bigRepresentImageURL"] = it.ID, "", ""
					rows = append(rows, row)
				}
			}
		case kindInterior:
			owned, _, landscapes := requesterOwned(r)
			if cat == "URCMD" { // the stock client has no Garden tab: landscapes lead the default tab
				for _, it := range c.landscape {
					// The Interior Shop ignores soldout/purchased (furniture can be bought twice), so an owned
					// landscape is left out; the editor lists it, and a stale buy is refused with 61009.
					if landscapes[it.Code] || !onSale(it.Code, now) {
						continue
					}
					rows = append(rows, interiorRow(it, false, deals, now))
				}
			}
			for _, it := range c.interior {
				if cat == "URCMD" && !slices.Contains(room.RoomShowcaseCodes, it.Code) || cat != "URCMD" && room.RoomCategoryCode(it.Code) != cat || !onSale(it.Code, now) {
					continue
				}
				rows = append(rows, interiorRow(it, owned[it.Code], deals, now))
			}
		default:
			_, owned, _ := requesterOwned(r)
			for _, it := range c.pet {
				// The client has three tabs (button ids 101/1/46 select UDOGINT/UCATINT/URIDEINT,
				// @0x1cf5690); UPETINT is only reachable by a link argument and lists everything.
				// ponytail: the toys (UTOY) have no tab of their own, so they ride in the default
				// Dog tab; move them to UPETINT only if the stock UI turns out to have a Toy tab.
				if cat == "UPETINT" || cat == "UDOGINT" && (it.Catg == "UDOG" || it.Catg == "UTOY") ||
					cat == "UCATINT" && it.Catg == "UCAT" || cat == "URIDEINT" && it.Catg == "URIDE" {
					row := interiorRow(it, owned[it.Code], deals, now)
					row["purchasedYn"] = owned[it.Code]
					for k, v := range map[string]any{"raceId": 1, "catgCd": it.Catg, "petSkinId": room.PetSkinID(it.Code), "petSkinCode": it.Code,
						"linkPosX": 0, "linkPosY": 0, "linkScale": 100, "linkPosScale": 100, "speed": 0, "power": 0, "stamina": 0, "itemSlot": 0} {
						row[k] = v
					}
					rows = append(rows, row)
				}
			}
		}
		// Deals and limited items lead their tab; tabs hold hundreds of rows.
		featured := func(row map[string]any) bool {
			return row["sellingIconType"] == "SALE" || row["sellingIconType"] == "LIMIT"
		}
		slices.SortStableFunc(rows, func(a, b map[string]any) int {
			switch fa, fb := featured(a), featured(b); {
			case fa && !fb:
				return -1
			case fb && !fa:
				return 1
			}
			return 0
		})
		httpx.WriteJSON(w, http.StatusOK, itemsBody(rows))
	}
}

func interiorRow(it shopItem, owned bool, deals dealSet, now time.Time) map[string]any {
	row := priceKeys(it, deals, now)
	for k, v := range map[string]any{"itemId": it.ID, "description": "", "itemType": "ROOM", "existInven": owned, "purchased": owned,
		"sellingStart": "", "discountStart": ""} {
		row[k] = v
	}
	return row
}

// postJSON answers a POST with a fixed body.
func postJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.ServeNotFound(w)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, body)
	}
}

// ---- buy ----

const (
	errUnknown   = "61001" // "Incorrect purchase information"
	errUpdated   = "61003" // "The shop has been updated"
	errOwned     = "61009" // "Item already purchased"
	errNoGemsFas = "71001" // fashion: "Error with gems" (the client pre-checks, so this is a backstop)
	errNoMoney   = "60001" // interior/pet: native "Not enough gems/Cash"
	errRecycle   = "50001" // "refund price changed"
	errInUse     = "50024" // "in use"
	errTooMany   = "50025"
	maxShopBatch = 100
)

func shopError(w http.ResponseWriter, r *http.Request, raw []byte, code, msg string) {
	log.Printf("cherry: shop reject %s errorCode=%s (%s) body=%q", r.URL.Path, code, msg, raw) // item codes and seqs only
	httpx.WriteJSON(w, http.StatusBadRequest, fmt.Sprintf(`{"errorCode":%q,"errorMessage":"cherry: %s"}`, code, msg))
}

type shopBuyReq struct {
	Items []struct {
		ItemCode string          `json:"itemCode"`
		Price    json.RawMessage `json:"price"`
	} `json:"items"`
	GiveNo json.RawMessage `json:"giveNo"`
}

func firstCode(req shopBuyReq) string {
	if len(req.Items) == 0 {
		return ""
	}
	return req.Items[0].ItemCode
}

// handleShopBuy serves purchase/dress/buy, purchase/interior/buy and purchase/pet/buy. The server
// price is authoritative and the currency comes from the table, never from coinUnit.
func handleShopBuy(kind byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.ServeNotFound(w)
			return
		}
		raw, err := httpx.ReadNativeBody(r)
		var req shopBuyReq
		if err == nil {
			_, err = httpx.UnmarshalNativeJSON(raw, &req)
		}
		if err != nil {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		kind := kind
		if _, k := classify(firstCode(req)); kind == kindInterior && (k == kindPet || k == kindGarden) {
			kind = k // the stock pet shop (runtime 2026-10-07) and the landscapes buy through purchase/interior/buy
		}
		give := strings.Trim(strings.TrimSpace(string(req.GiveNo)), `"`)
		if len(req.Items) == 0 || len(req.Items) > maxShopBatch || kind == kindPet && give != "" && give != "null" && give != "0" {
			shopError(w, r, raw, errUnknown, "bad product")
			return
		}
		c := loadCatalog()
		now := NowFunc()
		deals := dealsOn(now)
		codes := make([]string, 0, len(req.Items))
		paid := map[string]int64{} // deal items bought below their table price: the Gems paid
		var total int64
		cash := false
		for i, it := range req.Items {
			price, c2, _, ok := ItemPrice(it.ItemCode)
			if _, k := classify(it.ItemCode); !ok || k != kind || c.byCode[it.ItemCode] == nil || !onSale(it.ItemCode, now) || i > 0 && c2 != cash {
				shopError(w, r, raw, errUnknown, "unknown item")
				return
			}
			// A daily deal takes exactly its deal price; any other item exactly the table price (deals.go).
			want := price
			if d, deal := deals[it.ItemCode]; deal {
				want = d
			}
			if cp, ok := lenientInt(it.Price); !ok || cp != want {
				shopError(w, r, raw, errUpdated, "shop updated")
				return
			}
			if want < price {
				paid[it.ItemCode] = want
			}
			cash = c2
			total += want
			codes = append(codes, it.ItemCode)
		}
		noMoney, label := errNoMoney, "interior"
		switch kind {
		case kindFashion:
			noMoney, label = errNoGemsFas, "dress"
		case kindPet:
			label = "pet"
		case kindGarden:
			label = "landscape"
		}

		store.AccountsMu.Lock()
		defer store.AccountsMu.Unlock()
		acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
		if acc == nil {
			httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
			return
		}
		switch kind { // lab accounts get their showcase seeded first, new accounts their starter room
		case kindInterior:
			err = room.EnsureRoomLocked(acc)
		case kindPet:
			err = room.EnsurePetsLocked(acc)
		}
		if err != nil {
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
			return
		}
		previous := *acc
		switch kind {
		case kindFashion:
			owned := store.AccountOwnedCodes(acc)
			for i, code := range codes {
				if slices.Contains(owned, code) || slices.Contains(codes[:i], code) {
					shopError(w, r, raw, errOwned, "already owned")
					return
				}
			}
		case kindPet:
			for i, code := range codes {
				if slices.ContainsFunc(acc.Pets, func(p store.PetItem) bool { return p.Cd == code }) || slices.Contains(codes[:i], code) {
					shopError(w, r, raw, errOwned, "already owned")
					return
				}
			}
		case kindGarden:
			for i, code := range codes {
				if itemOwnedLocked(acc, code) || slices.Contains(codes[:i], code) {
					shopError(w, r, raw, errOwned, "already owned")
					return
				}
			}
		}
		deltas := store.LedgerDeltas{Gems: -total}
		if cash {
			deltas = store.LedgerDeltas{Cash: -total}
		}
		lines, err := store.SpendLocked(acc, deltas, "shop:"+label+":"+strings.Join(codes, ","))
		if err != nil {
			shopError(w, r, raw, noMoney, "insufficient balance")
			return
		}
		switch kind {
		case kindFashion:
			acc.InventoryCodes = store.AppendUniqueItemCodes(store.AccountInventoryCodes(acc), codes)
			acc.PaidPrices = withPaid(acc.PaidPrices, paid)
		case kindInterior:
			items := slices.Clone(acc.RoomItems)
			seq := max(acc.NextRoomSeq, store.FirstRoomSeq)
			for _, it := range items {
				seq = max(seq, it.Seq+1)
			}
			for _, code := range codes {
				items = append(items, store.RoomItem{Seq: seq, Cd: code, Paid: paid[code]})
				seq++
			}
			acc.RoomItems, acc.NextRoomSeq = items, seq
		case kindGarden:
			acc.Landscapes = append(slices.Clone(acc.Landscapes), codes...)
		case kindPet:
			for _, code := range codes {
				acc.Pets = append(slices.Clone(acc.Pets), room.NewPetLocked(acc, code))
			}
		}
		if err := store.SaveAccountsLocked(); err != nil {
			*acc = previous
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
			return
		}
		store.FlushLedgerLocked(lines)
		balance := acc.Gems
		if cash {
			balance = acc.Cash
		}
		result := map[string]any{"balance": balance}
		if kind != kindInterior {
			result["resultCode"], result["buyItems"] = 0, codes
		}
		body, _ := json.Marshal(map[string]any{"result": result})
		httpx.WriteJSON(w, http.StatusOK, string(body))
	}
}

// ---- sell-back ----

// recycleCfgBody is the sell-back table: every grade of every kind at the sell-back rate (prices.json).
func recycleCfgBody() string {
	var rows []map[string]any
	for _, typ := range []string{"DRESS", "ROOM", "GARDEN"} {
		for _, g := range []string{"NORMAL", "RARE", "SUPER_RARE", "ULTRA_RARE", "EPIC", "SUPER_EPIC", "ULTRA_EPIC"} {
			rows = append(rows, map[string]any{"itemType": typ, "grade": g, "rate": ActivePrices().SellBackPercent})
		}
	}
	b, _ := json.Marshal(map[string]any{"result": map[string]any{
		"recycleMultiple": 1.0, "multipledRecycleLimit": 0, "vipGrade": "NONE", "config": rows}})
	return string(b)
}

// handleRecycle serves inven/recycle/closet/v2 and inven/recycle/interior. Payout is 100% of the
// table price per item (the client's exepectedPrice is ignored). The reply must never carry
// config: rates that differ from the cached ones make the client drop the credit (msg 1028).
// Garden recycle is not served.
// ponytail: closet invenSeq is an ordinal that shifts after each sale (the client sends several
// requests with its stale list), so clothing is matched by itemCode only; room items match by seq
// (the interior request carries no itemCode).
func handleRecycle(interior bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.ServeNotFound(w)
			return
		}
		raw, err := httpx.ReadNativeBody(r)
		var req struct {
			RecycleItems []struct {
				InvenSeq json.RawMessage `json:"invenSeq"`
				ItemCode string          `json:"itemCode"`
				Count    json.RawMessage `json:"count"`
			} `json:"recycleItems"`
		}
		if err == nil {
			_, err = httpx.UnmarshalNativeJSON(raw, &req)
		}
		if err != nil {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		if len(req.RecycleItems) == 0 || len(req.RecycleItems) > maxShopBatch {
			shopError(w, r, raw, errRecycle, "bad recycle list")
			return
		}
		store.AccountsMu.Lock()
		defer store.AccountsMu.Unlock()
		acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
		if acc == nil {
			httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
			return
		}
		if interior {
			if err := room.EnsureRoomLocked(acc); err != nil {
				httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
				return
			}
		}
		previous := *acc
		inUse := map[int64]bool{} // placed room seqs
		for _, lay := range acc.Rooms {
			inUse[lay.Floor.Seq], inUse[lay.Wall.Seq] = true, true
			for _, p := range lay.Placed {
				inUse[p.Seq] = true
				for _, s := range p.Superpose {
					inUse[s] = true
				}
			}
		}
		sellCodes, sellSeqs := map[string]bool{}, map[int64]bool{}
		var total int64
		var names []string
		invalid := []map[string]any{}
		for _, it := range req.RecycleItems {
			seq, seqOK := lenientInt(it.InvenSeq)
			count := int64(1)
			if len(it.Count) > 0 {
				count, _ = lenientInt(it.Count)
			}
			if !seqOK || count != 1 {
				shopError(w, r, raw, errRecycle, "bad item")
				return
			}
			var paid int64 // the Gems the item was bought for at a deal (0 = none): pays back instead of the table price
			if interior {
				// ReqRecycleInterior @0x1bdeab0 sends only invenSeq, exepectedPrice and price per row
				// (no itemCode, no count): the item is found by seq alone. A code, if present, must match.
				i := slices.IndexFunc(acc.RoomItems, func(ri store.RoomItem) bool { return ri.Seq == seq })
				if sellSeqs[seq] || i < 0 || it.ItemCode != "" && acc.RoomItems[i].Cd != it.ItemCode {
					shopError(w, r, raw, errRecycle, "unknown item")
					return
				}
				it.ItemCode = acc.RoomItems[i].Cd
				paid = acc.RoomItems[i].Paid
				sellSeqs[seq] = true
			} else {
				if sellCodes[it.ItemCode] || !slices.Contains(store.AccountInventoryCodes(acc), it.ItemCode) {
					shopError(w, r, raw, errRecycle, "unknown item")
					return
				}
				sellCodes[it.ItemCode] = true
				paid = acc.PaidPrices[it.ItemCode]
			}
			if interior && inUse[seq] || !interior && slices.Contains(acc.ItemCodes, it.ItemCode) {
				shopError(w, r, raw, errInUse, "item in use")
				return
			}
			price := soldPrice(it.ItemCode, paid)
			if price == 0 { // faces/unpriced: kept, reported back so the client keeps them too
				delete(sellCodes, it.ItemCode)
				delete(sellSeqs, seq)
				invalid = append(invalid, map[string]any{"invenSeq": strconv.FormatInt(seq, 10), "itemCode": it.ItemCode, "representPrice": 0, "purchasePrice": 0})
				continue
			}
			total += price
			names = append(names, it.ItemCode)
		}
		label := "dress"
		if interior {
			label = "interior"
		}
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: total}, "recycle:"+label+":"+strings.Join(names, ","))
		if err != nil {
			shopError(w, r, raw, errTooMany, "balance limit")
			return
		}
		if interior {
			acc.RoomItems = slices.DeleteFunc(slices.Clone(acc.RoomItems), func(it store.RoomItem) bool { return sellSeqs[it.Seq] })
		} else {
			kept := make([]string, 0, len(acc.ItemCodes))
			for _, c := range store.AccountInventoryCodes(acc) {
				if !sellCodes[c] {
					kept = append(kept, c)
				}
			}
			acc.InventoryCodes = kept
			if len(acc.PaidPrices) > 0 { // a sold item's paid price goes with it
				left := make(map[string]int64, len(acc.PaidPrices))
				for code, p := range acc.PaidPrices {
					if !sellCodes[code] {
						left[code] = p
					}
				}
				acc.PaidPrices = left
			}
		}
		if err := store.SaveAccountsLocked(); err != nil {
			*acc = previous
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
			return
		}
		store.FlushLedgerLocked(lines)
		body, _ := json.Marshal(map[string]any{"result": map[string]any{"balance": acc.Gems, "invalidItems": invalid}})
		httpx.WriteJSON(w, http.StatusOK, string(body))
	}
}

func registerShopRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/shop/fashion/itemlist/", handleShopList(kindFashion))
	mux.HandleFunc("/v4/shop/interior/itemlist/", handleShopList(kindInterior))
	mux.HandleFunc("/v4/shop/interior/petitemlist/", handleShopList(kindPet))
	for _, p := range []string{"fashion/cart/", "fashion/SET/cart/", "interior/cart/", "pet/cart/"} {
		mux.HandleFunc("/v4/shop/"+p, postJSON(emptyItems)) // last-viewed lists; nothing is tracked
	}
	mux.HandleFunc("/v4/shop/status", postJSON(`{"result":[]}`))
	mux.HandleFunc("/v4/items/modified/list/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.ServeNotFound(w)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, fmt.Sprintf(`{"result":{"modifiedItemList":[],"lastTimestamp":"%d"}}`, time.Now().UnixMilli()))
	})
	mux.HandleFunc("/v4/purchase/dress/buy", handleShopBuy(kindFashion))
	mux.HandleFunc("/v4/purchase/interior/buy", handleShopBuy(kindInterior))
	mux.HandleFunc("/v4/purchase/pet/buy", handleShopBuy(kindPet))
	mux.HandleFunc("/v4/inven/recycle/cfg", httpx.HandleJSONBody(recycleCfgBody()))
	mux.HandleFunc("/v4/inven/recycle/closet/v2", handleRecycle(false))
	mux.HandleFunc("/v4/inven/recycle/interior", handleRecycle(true))
	registerGachaRoutes(mux)
}

// ---- item names ----

// ItemName is a synthetic display name ("Top 00001"): the stock client shows the placeholder
// "Item Name" on every cell that resolves a name through items/<dress|room>/some, and the
// archive has no code-to-name table (itemName.txt in the 2014 item.zip holds resource names such
// as top_mk1207_0001642, and arts_strings.ast has only UI strings). The category names are
// Cherry-made (prices.json).
func ItemName(code string) string {
	if len(code) != 9 {
		return "Item"
	}
	if l, ok := listedLandscape(code); ok {
		return l.Name
	}
	label := "Item"
	switch code[:2] {
	case "CU", "CA":
		if n, ok := ActivePrices().ClothingNames[code[2:4]]; ok {
			label = n
		}
	case "RU":
		label = "Room Item"
		if n, ok := ActivePrices().RoomNames[room.RoomCategoryCode(code)]; ok {
			label = n
		}
	case "PU":
		label = "Pet"
	}
	return label + " " + code[4:]
}

// HandleItemsSome serves POST items/dress/some and items/room/some: body ["<itemCode>", ...],
// reply result[{cd, names{lang: text}, isRare}] (ResItemInfoWithType @0x1b7e48c reads names[ja]
// for Japanese and names[en] otherwise; isRare marks the item rare).
func HandleItemsSome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var codes []string
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &codes)
	}
	if err != nil || len(codes) > maxShopBatch {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	rows := []map[string]any{}
	for _, code := range codes {
		if len(code) != 9 || strings.ContainsFunc(code, func(c rune) bool { return c < '0' || c > 'Z' || c > '9' && c < 'A' }) {
			continue
		}
		name := ItemName(code)
		_, _, grade, _ := ItemPrice(code)
		rows = append(rows, map[string]any{"cd": code, "names": map[string]string{"en": name, "ja": name}, "isRare": grade != "N"})
	}
	body, _ := json.Marshal(map[string]any{"result": rows})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}
