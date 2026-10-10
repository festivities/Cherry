package economy

import (
	"encoding/json"
	"fmt"
	"log"
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

	"cherry/internal/httpx"
	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Economy Phase C (first pass): Fashion / Interior / Pet shops and sell-back, priced from
// Cherry's own table. Protocol notes: .agents/PLAN.md "Economy", static research
// shops-phaseC.md (NaFashionShopLayer, ResShopItemWithShopType @0x1a87c30, buy replies
// @0x1b36238 / @0x1b2b7d8 / @0x1b2bba0, ResRecycleCloset @0x1c1f1e4).

// ---- price table ----

var clothingPrice = map[string]int64{"HA": 200, "ON": 250, "TO": 150, "PA": 150, "SH": 120, "AH": 120, "FE": 120, "AE": 100}

const (
	kindFashion  = 'F'
	kindInterior = 'I'
	kindPet      = 'P'
)

// classify returns the Gem base price and shop kind of a code (kind 0 = not sellable:
// faces, which the Face Shop sells, and anything the server cannot classify).
func classify(code string) (base int64, kind byte) {
	if len(code) != 9 {
		return 0, 0
	}
	switch code[:2] {
	case "CU", "CA":
		if p, ok := clothingPrice[code[2:4]]; ok {
			return p, kindFashion
		}
	case "RU":
		if code[2:4] == "WA" || code[2:4] == "TI" {
			return 100, kindInterior
		}
		return 150, kindInterior
	case "PU":
		if code[2:4] == "PE" {
			return 1200, kindPet
		}
	}
	return 0, 0
}

// ItemPrice is the one server price table (catalog, buy, inventory rows, recycle).
// Animated items (an .aniproj in the item dir) are premium: grade "R", 2.5x Gems for
// clothing and furniture, 25 Cash for pets. Unsellable codes return ok=false, price 0.
func ItemPrice(code string) (price int64, cash bool, grade string, ok bool) {
	base, kind := classify(code)
	if kind == 0 {
		return 0, false, "N", false
	}
	if it := loadCatalog().byCode[code]; it != nil && it.Animated {
		if kind == kindPet {
			return 25, true, "R", true
		}
		return (base*5 + 1) / 2, false, "R", true
	}
	return base, false, "N", true
}

// InteriorPrice adapts ItemPrice for the room inventory rows.
func InteriorPrice(code string) (int64, string) {
	price, _, grade, _ := ItemPrice(code)
	return price, grade
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
	fashion, interior, pet []shopItem
	byCode                 map[string]*shopItem
}

var (
	catalogMu sync.Mutex
	catalog   *shopCatalog
)

// loadCatalog scans thumbs.DpItemRoot once (again only if the root changes, as tests do).
func loadCatalog() *shopCatalog {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if root := thumbs.DpItemRoot; catalog == nil || catalog.root != root {
		catalog = scanCatalog(root)
	}
	return catalog
}

const roomLetters = "TA CH CL DR BE KI TB BA EL PR BG WA TI WD TD DO WI GO PL FD LA AB GC FU SL PT PI ZA BW BT ZB MP VO" // catIdx 3..35

var clothLetters = map[int64]string{38: "HE", 40: "EB", 41: "EY", 42: "NO", 43: "MO", 44: "HA", 50: "TO", 51: "ON", 52: "PA", 53: "SH", 54: "AH", 55: "AE", 56: "FE"}

// itemCodeFromID inverts SbItemTable::GetIntIdx: id = attr*1e8 + type*1e7 + catIdx*1e5 +
// base36(code[4:9]), attr in "?RCPG", type in "MFUA".
func itemCodeFromID(n int64) (string, bool) {
	attr, typ, cat, base := n/1e8, n/1e7%10, n/1e5%100, n%1e5
	if attr < 1 || attr > 4 || typ > 3 {
		return "", false
	}
	letters := ""
	switch attr {
	case 1:
		if f := strings.Fields(roomLetters); cat >= 3 && int(cat-3) < len(f) {
			letters = f[cat-3]
		}
	case 2:
		letters = clothLetters[cat]
	case 3:
		if cat == 64 {
			letters = "PE"
		}
	}
	if letters == "" {
		return "", false
	}
	b := strings.ToUpper(strconv.FormatInt(base, 36))
	return string("?RCPG"[attr]) + string("MFUA"[typ]) + letters + strings.Repeat("0", 5-len(b)) + b, true
}

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
	c := &shopCatalog{root: root, byCode: map[string]*shopItem{}}
	for _, sub := range []string{"custom", "dress", "interior", "tile", "pet"} {
		entries, err := os.ReadDir(filepath.Join(root, sub))
		if err != nil {
			continue // missing root or kind: empty catalog
		}
		for _, e := range entries {
			n, err := strconv.ParseInt(e.Name(), 10, 64)
			if err != nil || !e.IsDir() {
				continue
			}
			code, ok := itemCodeFromID(n)
			if !ok {
				continue
			}
			_, kind := classify(code)
			dir := filepath.Join(root, sub, e.Name())
			if kind == 0 || c.byCode[code] != nil || kind == kindPet && !hasFile(dir, "skeleton.xml") {
				continue // faces, unclassifiable, duplicate, or a pet with no render assets
			}
			it := shopItem{Code: code, ID: n}
			if files, err := os.ReadDir(dir); err == nil {
				it.Animated = slices.ContainsFunc(files, func(f os.DirEntry) bool {
					return strings.HasSuffix(strings.ToLower(f.Name()), ".aniproj")
				})
			}
			if kind == kindPet {
				it.Catg = petCategory(dir, it.Animated)
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
	}
	for _, list := range []*[]shopItem{&c.fashion, &c.interior, &c.pet} {
		sort.Slice(*list, func(i, j int) bool { return (*list)[i].Code < (*list)[j].Code })
		for i := range *list {
			c.byCode[(*list)[i].Code] = &(*list)[i]
		}
	}
	return c
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

// priceKeys are the keys every row kind shares.
func priceKeys(it shopItem) map[string]any {
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

// requesterOwned returns the requester's room item codes and pet codes (empty if unknown).
func requesterOwned(r *http.Request) (rooms, pets map[string]bool) {
	rooms, pets = map[string]bool{}, map[string]bool{}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	if acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]; acc != nil {
		for _, it := range acc.RoomItems {
			rooms[it.Cd] = true
		}
		for _, p := range acc.Pets {
			pets[p.Cd] = true
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
		var rows []map[string]any
		switch kind {
		case kindFashion:
			prefix, slots := fashionSlots(cat)
			for _, it := range c.fashion {
				if it.Code[:2] == prefix && slices.Contains(slots, it.Code[2:4]) {
					row := priceKeys(it)
					row["itemID"], row["representImageURL"], row["bigRepresentImageURL"] = it.ID, "", ""
					rows = append(rows, row)
				}
			}
		case kindInterior:
			owned, _ := requesterOwned(r)
			for _, it := range c.interior {
				if cat == "URCMD" && !slices.Contains(room.RoomShowcaseCodes, it.Code) || cat != "URCMD" && room.RoomCategoryCode(it.Code) != cat {
					continue
				}
				rows = append(rows, interiorRow(it, owned[it.Code]))
			}
		default:
			_, owned := requesterOwned(r)
			for _, it := range c.pet {
				// The client has three tabs (button ids 101/1/46 select UDOGINT/UCATINT/URIDEINT,
				// @0x1cf5690); UPETINT is only reachable by a link argument and lists everything.
				// ponytail: the toys (UTOY) have no tab of their own, so they ride in the default
				// Dog tab; move them to UPETINT only if the stock UI turns out to have a Toy tab.
				if cat == "UPETINT" || cat == "UDOGINT" && (it.Catg == "UDOG" || it.Catg == "UTOY") ||
					cat == "UCATINT" && it.Catg == "UCAT" || cat == "URIDEINT" && it.Catg == "URIDE" {
					row := interiorRow(it, owned[it.Code])
					row["purchasedYn"] = owned[it.Code]
					for k, v := range map[string]any{"raceId": 1, "catgCd": it.Catg, "petSkinId": room.PetSkinID(it.Code), "petSkinCode": it.Code,
						"linkPosX": 0, "linkPosY": 0, "linkScale": 100, "linkPosScale": 100, "speed": 0, "power": 0, "stamina": 0, "itemSlot": 0} {
						row[k] = v
					}
					rows = append(rows, row)
				}
			}
		}
		httpx.WriteJSON(w, http.StatusOK, itemsBody(rows))
	}
}

func interiorRow(it shopItem, owned bool) map[string]any {
	row := priceKeys(it)
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
		if _, k := classify(firstCode(req)); kind == kindInterior && k == kindPet {
			kind = kindPet // the stock pet shop buys through purchase/interior/buy (runtime 2026-10-07)
		}
		give := strings.Trim(strings.TrimSpace(string(req.GiveNo)), `"`)
		if len(req.Items) == 0 || len(req.Items) > maxShopBatch || kind == kindPet && give != "" && give != "null" && give != "0" {
			shopError(w, r, raw, errUnknown, "bad product")
			return
		}
		c := loadCatalog()
		codes := make([]string, 0, len(req.Items))
		var total int64
		cash := false
		for i, it := range req.Items {
			price, c2, _, ok := ItemPrice(it.ItemCode)
			if _, k := classify(it.ItemCode); !ok || k != kind || c.byCode[it.ItemCode] == nil || i > 0 && c2 != cash {
				shopError(w, r, raw, errUnknown, "unknown item")
				return
			}
			if cp, ok := lenientInt(it.Price); !ok || cp != price {
				shopError(w, r, raw, errUpdated, "shop updated")
				return
			}
			cash = c2
			total += price
			codes = append(codes, it.ItemCode)
		}
		noMoney, label := errNoMoney, "interior"
		switch kind {
		case kindFashion:
			noMoney, label = errNoGemsFas, "dress"
		case kindPet:
			label = "pet"
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
		case kindInterior:
			items := slices.Clone(acc.RoomItems)
			seq := max(acc.NextRoomSeq, store.FirstRoomSeq)
			for _, it := range items {
				seq = max(seq, it.Seq+1)
			}
			for _, code := range codes {
				items = append(items, store.RoomItem{Seq: seq, Cd: code})
				seq++
			}
			acc.RoomItems, acc.NextRoomSeq = items, seq
		default:
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

var recycleCfgBody = func() string {
	var rows []map[string]any
	for _, typ := range []string{"DRESS", "ROOM", "GARDEN"} {
		for _, g := range []string{"NORMAL", "RARE", "SUPER_RARE", "ULTRA_RARE", "EPIC", "SUPER_EPIC", "ULTRA_EPIC"} {
			rows = append(rows, map[string]any{"itemType": typ, "grade": g, "rate": 100})
		}
	}
	b, _ := json.Marshal(map[string]any{"result": map[string]any{
		"recycleMultiple": 1.0, "multipledRecycleLimit": 0, "vipGrade": "NONE", "config": rows}})
	return string(b)
}()

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
			if interior {
				// ReqRecycleInterior @0x1bdeab0 sends only invenSeq, exepectedPrice and price per row
				// (no itemCode, no count): the item is found by seq alone. A code, if present, must match.
				i := slices.IndexFunc(acc.RoomItems, func(ri store.RoomItem) bool { return ri.Seq == seq })
				if sellSeqs[seq] || i < 0 || it.ItemCode != "" && acc.RoomItems[i].Cd != it.ItemCode {
					shopError(w, r, raw, errRecycle, "unknown item")
					return
				}
				it.ItemCode = acc.RoomItems[i].Cd
				sellSeqs[seq] = true
			} else {
				if sellCodes[it.ItemCode] || !slices.Contains(store.AccountInventoryCodes(acc), it.ItemCode) {
					shopError(w, r, raw, errRecycle, "unknown item")
					return
				}
				sellCodes[it.ItemCode] = true
			}
			if interior && inUse[seq] || !interior && slices.Contains(acc.ItemCodes, it.ItemCode) {
				shopError(w, r, raw, errInUse, "item in use")
				return
			}
			price, _, _, _ := ItemPrice(it.ItemCode)
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
	mux.HandleFunc("/v4/inven/recycle/cfg", httpx.HandleJSONBody(recycleCfgBody))
	mux.HandleFunc("/v4/inven/recycle/closet/v2", handleRecycle(false))
	mux.HandleFunc("/v4/inven/recycle/interior", handleRecycle(true))
	registerGachaRoutes(mux)
}

// ---- item names ----

var clothingName = map[string]string{"HA": "Hair", "ON": "Outfit", "TO": "Top", "PA": "Bottoms", "SH": "Shoes",
	"AH": "Accessory", "AE": "Accessory", "FE": "Accessory", "HE": "Face", "EB": "Eyebrows", "EY": "Eyes", "NO": "Nose", "MO": "Mouth"}

var roomName = map[string]string{"UWLPP": "Wallpaper", "UFLOR": "Flooring", "UTABL": "Table", "UCHAIR": "Chair", "UDRES": "Dresser",
	"UBAD": "Bed", "UKICH": "Kitchen Item", "UBATH": "Bath Item", "UMEDIA": "Electronics", "UPLANT": "Plant", "UFOOD": "Food",
	"UALBM": "Album", "UWNDW": "Window", "UDOOR": "Door", "UWLDC": "Wall Decor", "UFLDC": "Floor Decor"}

// ItemName is a synthetic display name ("Top 00001"): the stock client shows the placeholder
// "Item Name" on every cell that resolves a name through items/<dress|room>/some, and the
// archive has no code-to-name table (itemName.txt in the 2014 item.zip holds resource names such
// as top_mk1207_0001642, and arts_strings.ast has only UI strings).
func ItemName(code string) string {
	if len(code) != 9 {
		return "Item"
	}
	label := "Item"
	switch code[:2] {
	case "CU", "CA":
		if n, ok := clothingName[code[2:4]]; ok {
			label = n
		}
	case "RU":
		label = "Room Item"
		if n, ok := roomName[room.RoomCategoryCode(code)]; ok {
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
