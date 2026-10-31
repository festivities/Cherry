package economy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"maps"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"cherry/data"
	"cherry/internal/httpx"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Colorant (dye): the Dye Shop (home menu goSomewhere(colorantShop)) sells dye items for Gems, the Dye Inventory
// (goSomewhere(colorantInven)) lists the dyes an account owns, and applying a dye recolours the two tone layers of one
// owned garment (spec menu-audit-20261028/colorant-spec.md: sections 3, 5, 6, 7 and 11). Which garments can be dyed is
// the client's (dyeFlagged); the dye items, prices, colours and palette are Cherry-made (data/dyes.json).

// CherryDyeFirst and CherryDyeLast bound the dye item numbers Cherry makes (data/dyes.json). No original dye item
// number is in the archives, and the block is clear of the other Cherry blocks.
const (
	CherryDyeFirst int64 = 9501
	CherryDyeLast  int64 = 9599
)

// dyeFlagged are the garments whose artsitem carries the dye flag and <part>_mask.png (colorant-spec.md 16.B), so the
// only garments that can be dyed. The value is the dye base-item list the garment is in: FACE for the custom kind,
// DRESS for the dress kind.
var dyeFlagged = map[int64]string{
	224102309: "FACE", 224102378: "FACE", 224102536: "FACE", 224102579: "FACE", 224102653: "FACE",
	224102775: "FACE", 224102816: "FACE", 224102858: "FACE", 224102932: "FACE", 224103060: "FACE",
	224406975: "FACE", 224407224: "FACE",
	225001650: "DRESS", 225409234: "DRESS", 235411996: "DRESS",
}

// dyeCoinUnit is the only currency of the dye shop: the client's GEM balance.
const dyeCoinUnit = "GEM"

// dyeSaleEnd is the saleEndYmdt every dye row carries (the client parses it as a number; the spec's example value).
const dyeSaleEnd = "20991231235959"

// DyeCatalog is data/dyes.json. Strict JSON: unknown keys are an error.
//
//	origin, note  Cherry-made (opdata.Provenance)
//	dyeable       numeric item ids of the garments that can be dyed; each must be one of the 15 flagged garments
//	              (dyeFlagged), so an operator can only narrow the list (itemcode decodes each id to its item code)
//	palette       "#RRGGBB" colours that shop/dye/preview serves and the CUSTOM dye picture is drawn in
//	dyes          the catalog: item_no inside 9501-9599 and unique; name; kind SINGLE, RANDOM or CUSTOM; price in Gems
//	              (above 0); colors: SINGLE one colour, RANDOM two or more colours drawn by prob (each above 0, summing
//	              to 1), CUSTOM none (the player picks any #RRGGBB colour when applying); bleach (optional): applying
//	              it removes the garment's dye instead of recolouring it, so it must be kind SINGLE
type DyeCatalog struct {
	opdata.Provenance
	Dyeable []int64   `json:"dyeable"`
	Palette []string  `json:"palette"`
	Dyes    []DyeItem `json:"dyes"`
}

// DyeItem is one dye of the catalog (the shop row and the inventory row).
type DyeItem struct {
	ItemNo int64         `json:"item_no"`
	Name   string        `json:"name"`
	Kind   string        `json:"kind"`
	Price  int64         `json:"price"`
	Bleach bool          `json:"bleach"`
	Colors []DyeColorDef `json:"colors"`
}

// DyeColorDef is one colour a dye applies, and the chance a RANDOM dye draws it.
type DyeColorDef struct {
	Color string  `json:"color"`
	Prob  float64 `json:"prob"`
}

var dyesFile = opdata.New("dyes.json", data.DyesJSON, checkDyes, nil)

// ActiveDyes is the dye catalog in use.
func ActiveDyes() *DyeCatalog { return dyesFile.Get() }

// checkDyes validates the structure at startup: the origin and note, the dyeable ids (each decodes to an item code
// and is one of the flagged garments), the palette and every dye.
func checkDyes(d *DyeCatalog) error {
	if err := d.CheckOrigin("cherry"); err != nil {
		return err
	}
	if len(d.Dyeable) == 0 {
		return errors.New("dyeable must list at least one garment")
	}
	seen := map[int64]bool{}
	for _, id := range d.Dyeable {
		code, ok := itemcode.Code(id)
		if back, decoded := itemcode.ID(code); !ok || !decoded || back != id {
			return fmt.Errorf("dyeable %d does not decode to an item code", id)
		}
		if _, flagged := dyeFlagged[id]; !flagged {
			return fmt.Errorf("dyeable %d is not one of the garments the client flags for dye", id)
		}
		if seen[id] {
			return fmt.Errorf("dyeable %d is listed twice", id)
		}
		seen[id] = true
	}
	if len(d.Palette) == 0 {
		return errors.New("palette must list at least one colour")
	}
	for _, c := range d.Palette {
		if _, err := parseHexColor(c); err != nil {
			return fmt.Errorf("palette: %w", err)
		}
	}
	if len(d.Dyes) == 0 {
		return errors.New("dyes must list at least one dye")
	}
	nos := map[int64]bool{}
	for _, it := range d.Dyes {
		switch {
		case !inCherryBlock(it.ItemNo, CherryDyeFirst, CherryDyeLast):
			return fmt.Errorf("dye %d: item_no must be inside the Cherry block %d-%d", it.ItemNo, CherryDyeFirst, CherryDyeLast)
		case nos[it.ItemNo]:
			return fmt.Errorf("dye %d: item_no is listed twice", it.ItemNo)
		case strings.TrimSpace(it.Name) == "":
			return fmt.Errorf("dye %d: name must not be empty", it.ItemNo)
		case it.Price < 1:
			return fmt.Errorf("dye %d: price must be at least 1 Gem", it.ItemNo)
		case it.Bleach && it.Kind != "SINGLE":
			return fmt.Errorf("dye %d: a bleach must be kind SINGLE", it.ItemNo)
		}
		nos[it.ItemNo] = true
		if err := checkDyeColors(it); err != nil {
			return fmt.Errorf("dye %d: %w", it.ItemNo, err)
		}
	}
	return nil
}

// checkDyeColors checks the colours of one dye by its kind.
func checkDyeColors(it DyeItem) error {
	switch it.Kind {
	case "SINGLE":
		if len(it.Colors) != 1 {
			return errors.New("kind SINGLE needs exactly one colour")
		}
	case "RANDOM":
		if len(it.Colors) < 2 {
			return errors.New("kind RANDOM needs at least two colours")
		}
	case "CUSTOM":
		if len(it.Colors) != 0 {
			return errors.New("kind CUSTOM takes the player's colour, so colors must be empty")
		}
		return nil
	default:
		return fmt.Errorf("kind %q must be SINGLE, RANDOM or CUSTOM", it.Kind)
	}
	sum := 0.0
	for _, c := range it.Colors {
		if _, err := parseHexColor(c.Color); err != nil {
			return err
		}
		if !(c.Prob > 0) {
			return fmt.Errorf("colour %s: prob must be above 0", c.Color)
		}
		sum += c.Prob
	}
	if math.Abs(sum-1) > 1e-6 {
		return fmt.Errorf("colour probabilities sum to %g, want 1", sum)
	}
	return nil
}

// dyeItemNo returns the catalog dye with item number no.
func dyeItemNo(no int64) (DyeItem, bool) {
	for _, it := range ActiveDyes().Dyes {
		if it.ItemNo == no {
			return it, true
		}
	}
	return DyeItem{}, false
}

// dyeableKind is the dye base-item list (DRESS or FACE) of garment code, when the dye file lets it be dyed.
func dyeableKind(code string) (string, bool) {
	for _, id := range ActiveDyes().Dyeable {
		if c, ok := itemcode.Code(id); ok && c == code {
			return dyeFlagged[id], true
		}
	}
	return "", false
}

// dyeImagePath is the picture of dye no. It is RELATIVE on purpose: the dye cell loads it as image type 7, and that
// request appends the path to the client's static base (NaDispatcherDiary::ReqDownloadImage_Diary, both branches call
// staticResourceURLWithPath), like the fishing gear pictures.
func dyeImagePath(no int64) string { return "cherry/dye/" + strconv.FormatInt(no, 10) + ".png" }

// dyeView is the requester's dye state, copied under AccountsMu. Its maps are replaced, never edited, so they may be read
// after the lock is released. ok is false for an unknown session (no dyes, no garments).
type dyeView struct {
	ok     bool
	dyes   map[string]int
	colors map[string][]store.DyeColor
	owned  []string
	paid   map[string]int64
}

func dyeViewOf(r *http.Request) dyeView {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		return dyeView{}
	}
	return dyeView{ok: true, dyes: acc.Dyes, colors: acc.DyeColors, owned: store.AccountOwnedCodes(acc), paid: acc.PaidPrices}
}

// dyeRandomSlots is how many colours of a RANDOM dye the client cycles through in its colour preview:
// UpdateDyeColor 0x1fe0890 reads dyeItemColorList[0..11] with no size check (colorant-spec.md 18), so a shorter list
// crashed the client on the GL thread.
const dyeRandomSlots = 12

// dyeWireColors is a dye's dyeItemColorList. A RANDOM dye with fewer than dyeRandomSlots colours repeats them in order
// and splits each colour's prob over its copies, so the list is long enough and every colour keeps its chance.
func dyeWireColors(it DyeItem) []DyeColorDef {
	if it.Kind != "RANDOM" || len(it.Colors) >= dyeRandomSlots {
		return it.Colors
	}
	copies := make([]int, len(it.Colors))
	for i := range dyeRandomSlots {
		copies[i%len(it.Colors)]++
	}
	out := make([]DyeColorDef, 0, dyeRandomSlots)
	for i := range dyeRandomSlots {
		c := it.Colors[i%len(it.Colors)]
		out = append(out, DyeColorDef{Color: c.Color, Prob: c.Prob / float64(copies[i%len(it.Colors)])})
	}
	return out
}

// dyeRow is one dye as sDyeItemInfo reads it, with the requester's owned count.
func dyeRow(it DyeItem, count int) map[string]any {
	wire := dyeWireColors(it)
	colors := make([]map[string]any, 0, len(wire))
	for _, c := range wire {
		colors = append(colors, map[string]any{"colorCode": c.Color, "prob": c.Prob})
	}
	return map[string]any{
		"itemNo": it.ItemNo, "itemName": it.Name, "coinUnit": dyeCoinUnit, "originalPrice": it.Price, "price": it.Price,
		"count": count, "image1Path": dyeImagePath(it.ItemNo), "image2Path": dyeImagePath(it.ItemNo),
		"saleEndYmdt": dyeSaleEnd, "event": false, "newItem": false, "bleach": it.Bleach, "dyeItemType": it.Kind,
		"dyeItemColorList": colors, "discountRate": 0, "discountStartYmdt": "0", "discountEndYmdt": "0",
	}
}

// dyeKindLists groups the dyes by kind: ALL, then SINGLE, RANDOM and CUSTOM (sDyeShopInfo and sDyeInvenInfo). Every key is
// an array, empty when nothing is listed. ownedOnly keeps the dyes the requester owns (the inventory).
func dyeKindLists(view dyeView, ownedOnly bool) map[string][]map[string]any {
	lists := map[string][]map[string]any{"ALL": {}, "SINGLE": {}, "RANDOM": {}, "CUSTOM": {}}
	for _, it := range ActiveDyes().Dyes {
		n := view.dyes[strconv.FormatInt(it.ItemNo, 10)]
		if ownedOnly && n < 1 {
			continue
		}
		row := dyeRow(it, n)
		lists["ALL"] = append(lists["ALL"], row)
		lists[it.Kind] = append(lists[it.Kind], row)
	}
	return lists
}

// handleDyeShopList serves GET shop/dye/item/list. isComplete must be true: the client reopens the tutorial on every visit
// while it is false, and no route completes it.
func handleDyeShopList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"tutorialInfo": map[string]any{"isComplete": true, "reward": []any{}},
		"list":         dyeKindLists(dyeViewOf(r), false),
	}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// handleDyeInvenList serves GET inven/dye/item/list: the dyes the requester owns.
func handleDyeInvenList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	body, _ := json.Marshal(map[string]any{"result": dyeKindLists(dyeViewOf(r), true)})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// handleDyePreview serves GET shop/dye/preview: the dye palette (data/dyes.json), an array of colour strings.
func handleDyePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	body, _ := json.Marshal(map[string]any{"result": ActiveDyes().Palette})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// handleDyeBaseItems serves GET inven/closet/baseitem (the query only repeats one flag; it changes nothing here): the
// requester's owned dyeable garments in the DRESS or FACE list and in ALL. invenSeq is the closet number of the garment
// (store.ClosetOrder), so the apply names the same garment the closet and the avatar show.
func handleDyeBaseItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	view := dyeViewOf(r)
	lists := map[string][]map[string]any{"ALL": {}, "DRESS": {}, "FACE": {}}
	seen := map[string]bool{}
	for i, code := range store.ClosetOrder(view.owned) {
		kind, ok := dyeableKind(code)
		if !ok || seen[code] {
			continue
		}
		seen[code] = true
		price, grade := ClosetPrice(view.paid, code)
		row := map[string]any{
			"invenSeq": strconv.Itoa(i + 1), "itemCode": code, "count": 1, "categoryCode": code[2:4],
			"price": price, "grade": grade, "dyeType": store.DyeTypeTwoLayers, "specialEffects": "", "rare": false,
			"newArrival": false, "colorAndTransparencies": store.DyeLayers(view.colors[code]),
		}
		lists["ALL"] = append(lists["ALL"], row)
		lists[kind] = append(lists[kind], row)
	}
	body, _ := json.Marshal(map[string]any{"result": lists})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// handleDyeBaseShop serves GET shop/dye/baseitem/list/DRESS/<any>, the Dye Base Shop (goSomewhere(colorantBaseShop)): the
// dyeable garments the requester does not own and purchase/dress/buy sells. The rows are sShopItem (the shop list reads
// result.items, colorant-spec.md 3.7) and use the Fashion Shop's price, deal and limited-sale keys, so a row is only
// listed when the buy would accept it: a garment with no clothing price (the eyes) or no art is left out, and an animal
// is not offered human-only items. The FACE category has no rows.
func handleDyeBaseShop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v4/shop/dye/baseitem/list/DRESS/") {
		httpx.WriteJSON(w, http.StatusOK, emptyItems)
		return
	}
	store.AccountsMu.Lock()
	var owned []string
	animal := false
	if acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]; acc != nil {
		owned, animal = store.AccountOwnedCodes(acc), acc.Gender == "ANIMAL"
	}
	store.AccountsMu.Unlock()
	c := loadCatalog()
	now := NowFunc()
	deals := dealsOn(now)
	rows := []map[string]any{}
	for _, id := range ActiveDyes().Dyeable {
		code, ok := itemcode.Code(id)
		if !ok || slices.Contains(owned, code) || animal && itemcode.HumanOnly(code) {
			continue
		}
		it := c.byCode[code]
		if _, kind := classify(code); it == nil || kind != kindFashion || !onSale(code, now) {
			continue // the buy refuses it: no clothing price, no art, or a limited item out of season
		}
		row := priceKeys(*it, deals, now)
		row["itemID"], row["name"], row["representImageURL"], row["bigRepresentImageURL"] = it.ID, ItemName(code), "", ""
		row["roomSize"], row["elementItems"], row["elementItemCodes"] = 0, []any{}, []any{}
		row["dyeType"] = store.DyeTypeTwoLayers
		rows = append(rows, row)
	}
	httpx.WriteJSON(w, http.StatusOK, itemsBody(rows))
}

// dyeBuyReq is the part of the purchase/dye/item body the server reads; deviceType and cpId price nothing.
type dyeBuyReq struct {
	CoinUnit  string          `json:"coinUnit"`
	ItemNo    json.RawMessage `json:"itemNo"`
	ItemCount json.RawMessage `json:"itemCount"`
	Price     json.RawMessage `json:"price"`
}

// handleDyeBuy serves POST purchase/dye/item: one dye for its catalog price in Gems. Every refusal leaves the account as
// it was and answers 400 (the client shows its own purchase error for any non-200 reply).
func handleDyeBuy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req dyeBuyReq
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	no, noOK := lenientInt(req.ItemNo)
	it, known := dyeItemNo(no)
	count, countOK := lenientInt(req.ItemCount)
	price, priceOK := lenientInt(req.Price)
	switch {
	case !noOK || !known:
		shopError(w, r, raw, errUnknown, "unknown dye item")
		return
	case req.CoinUnit != dyeCoinUnit:
		shopError(w, r, raw, errUnknown, "dyes for Gems only")
		return
	case !countOK || count != 1:
		shopError(w, r, raw, errUnknown, "one dye per purchase")
		return
	case !priceOK || price != it.Price:
		shopError(w, r, raw, errUpdated, "shop updated")
		return
	}

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		shopError(w, r, raw, errUnknown, "unknown session")
		return
	}
	key := strconv.FormatInt(no, 10)
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -it.Price}, "dye:buy:"+key)
	if err != nil {
		shopError(w, r, raw, errNoMoney, "insufficient balance")
		return
	}
	dyes := make(map[string]int, len(acc.Dyes)+1)
	maps.Copy(dyes, acc.Dyes)
	dyes[key]++
	acc.Dyes = dyes
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	store.FlushLedgerLocked(lines)
	body, _ := json.Marshal(map[string]any{"result": map[string]any{"balanceUnit": dyeCoinUnit, "balance": acc.Gems}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// dyeUseReq is the body of inven/dye/item/use/custom (sDyeUseRequestInfo::SetData): the dye, the garment's invenSeq, one
// flag per tone layer, and for a CUSTOM dye its transparency and colour.
type dyeUseReq struct {
	ItemNo        json.RawMessage `json:"itemNo"`
	InvenSeq      json.RawMessage `json:"invenSeq"`
	SelectedLayer []bool          `json:"selectedLayer"`
	Transparency  json.RawMessage `json:"transparency"`
	Custom        bool            `json:"custom"`
	ColorCode     string          `json:"colorCode"`
}

// drawDyeColor is the colour a RANDOM dye draws for f in [0,1): the first colour whose cumulative prob passes f.
func drawDyeColor(colors []DyeColorDef, f float64) string {
	for _, c := range colors {
		if f -= c.Prob; f < 0 {
			return c.Color
		}
	}
	return colors[len(colors)-1].Color
}

// dyeChoice is the colour and transparency a dye applies: SINGLE its colour, RANDOM a drawn one, CUSTOM the picked #RRGGBB
// colour (upper-cased, any colour the client's picker can send) with the requested transparency (0-100). ok is false for a
// request the dye cannot take.
func dyeChoice(it DyeItem, req dyeUseReq) (colorCode string, transparency int, ok bool) {
	switch it.Kind {
	case "SINGLE":
		return it.Colors[0].Color, 0, true
	case "RANDOM":
		return drawDyeColor(it.Colors, rand.Float64()), 0, true
	case "CUSTOM":
		t, tOK := lenientInt(req.Transparency)
		if !req.Custom || !tOK || t < 0 || t > 100 {
			return "", 0, false
		}
		if _, err := parseHexColor(req.ColorCode); err != nil {
			return "", 0, false
		}
		return strings.ToUpper(req.ColorCode), int(t), true
	}
	return "", 0, false
}

// dyeOwnedBySeq is the code of the owned garment numbered seq in the closet order (store.ClosetOrder); ok is false when
// no owned code has that number.
func dyeOwnedBySeq(owned []string, seq int64) (string, bool) {
	order := store.ClosetOrder(owned)
	if seq < 1 || seq > int64(len(order)) {
		return "", false
	}
	return order[seq-1], true
}

// handleDyeUse serves POST inven/dye/item/use/custom: one owned dye recolours the layers the request flags on one owned
// dyeable garment and is used up. The garment keeps its invenSeq (before == after), so the client does not re-equip it.
// Every refusal leaves the account as it was and answers 400.
func handleDyeUse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req dyeUseReq
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	no, noOK := lenientInt(req.ItemNo)
	seq, seqOK := lenientInt(req.InvenSeq)
	it, known := dyeItemNo(no)
	switch {
	case !noOK || !known:
		shopError(w, r, raw, errUnknown, "unknown dye item")
		return
	case !seqOK:
		shopError(w, r, raw, errUnknown, "garment not named")
		return
	case !it.Bleach && (len(req.SelectedLayer) < 1 || len(req.SelectedLayer) > store.DyeTypeTwoLayers || !slices.Contains(req.SelectedLayer, true)):
		// one flag when the client treats the garment as one layer (dyeType != 2, spec 6.1), else one per layer.
		// A bleach takes every layer, so its flags are not checked.
		shopError(w, r, raw, errUnknown, "select a layer")
		return
	}

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		shopError(w, r, raw, errUnknown, "unknown session")
		return
	}
	key := strconv.FormatInt(no, 10)
	if acc.Dyes[key] < 1 {
		shopError(w, r, raw, errUnknown, "no such dye owned")
		return
	}
	code, owned := dyeOwnedBySeq(store.AccountOwnedCodes(acc), seq)
	if !owned {
		shopError(w, r, raw, errUnknown, "garment not owned")
		return
	}
	if _, dyeable := dyeableKind(code); !dyeable {
		shopError(w, r, raw, errUnknown, "garment cannot be dyed")
		return
	}
	var layers []store.DyeColor
	if it.Bleach {
		// the garment goes back to undyed: no colour, and its entry is removed below (empty colours in the reply)
		if _, dyed := acc.DyeColors[code]; !dyed {
			shopError(w, r, raw, errUnknown, "garment has no dye")
			return
		}
		layers = []store.DyeColor{{}, {}}
	} else {
		colorCode, transparency, ok := dyeChoice(it, req)
		if !ok {
			shopError(w, r, raw, errUnknown, "colour not allowed")
			return
		}
		layers = store.DyeLayers(acc.DyeColors[code])
		for i, on := range req.SelectedLayer {
			if on {
				layers[i] = store.DyeColor{ColorCode: colorCode, Transparency: transparency}
			}
		}
	}
	dyes := make(map[string]int, len(acc.Dyes))
	maps.Copy(dyes, acc.Dyes)
	remaining := dyes[key] - 1
	if remaining > 0 {
		dyes[key] = remaining
	} else {
		delete(dyes, key)
	}
	colors := make(map[string][]store.DyeColor, len(acc.DyeColors)+1)
	maps.Copy(colors, acc.DyeColors)
	if it.Bleach {
		delete(colors, code)
	} else {
		colors[code] = layers
	}

	prev := *acc
	acc.Dyes, acc.DyeColors = dyes, colors
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	codes := []string{layers[0].ColorCode, layers[1].ColorCode}
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"count": remaining, "beforeInvenSeq": strconv.FormatInt(seq, 10), "afterInvenSeq": strconv.FormatInt(seq, 10),
		"colorAndTransparencies": layers, "colorCodes": codes,
	}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// dyeIconGeometry: a drop in a dyeIconSize square, a round bulb of radius dyeIconR centred on dyeIconCY, under a tip at
// dyeIconTipY.
const (
	dyeIconSize = 96
	dyeIconR    = 26
	dyeIconTipY = 12
	dyeIconCY   = 62
)

// dyeIconPixel reports whether (x, y) lies in the drop.
func dyeIconPixel(x, y int) bool {
	dx, dy := x-dyeIconSize/2, y-dyeIconCY
	if dx*dx+dy*dy <= dyeIconR*dyeIconR {
		return true
	}
	if y < dyeIconTipY || y > dyeIconCY {
		return false
	}
	half := dyeIconR * (y - dyeIconTipY) / (dyeIconCY - dyeIconTipY)
	return dx >= -half && dx <= half
}

// dyeIconPNG draws the drop in hexes (one colour: solid; more: vertical bands in order). The picture is Cherry-made.
func dyeIconPNG(hexes []string) ([]byte, error) {
	palette := make([]color.NRGBA, 0, len(hexes))
	for _, h := range hexes {
		c, err := parseHexColor(h)
		if err != nil {
			return nil, err
		}
		palette = append(palette, c)
	}
	img := image.NewNRGBA(image.Rect(0, 0, dyeIconSize, dyeIconSize))
	left := dyeIconSize/2 - dyeIconR
	for y := 0; y < dyeIconSize; y++ {
		for x := 0; x < dyeIconSize; x++ {
			if dyeIconPixel(x, y) {
				band := min((x-left)*len(palette)/(2*dyeIconR), len(palette)-1)
				img.SetNRGBA(x, y, palette[band])
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// bleachPictureColor is the grey the bleach's picture is drawn in (its own colour is white, so it would not show).
const bleachPictureColor = "#B0BEC5"

// HandleDyeImage serves GET /cherry/dye/<itemNo>.png: the dye's picture, drawn in its colours (RANDOM and CUSTOM in bands
// of all their colours; a bleach in bleachPictureColor). The archive has no dye pictures.
// ponytail: drawn shapes, not art; swap in real icons if the archive ever has them.
func HandleDyeImage(w http.ResponseWriter, r *http.Request) {
	no, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cherry/dye/"), ".png"), 10, 64)
	it, known := dyeItemNo(no)
	if err != nil || !known || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	hexes := ActiveDyes().Palette
	switch {
	case it.Bleach: // its colour is white (no tint), which would draw an empty picture
		hexes = []string{bleachPictureColor}
	case it.Kind != "CUSTOM":
		hexes = nil
		for _, c := range it.Colors {
			hexes = append(hexes, c.Color)
		}
	}
	b, err := dyeIconPNG(hexes)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

// ServeDyeMask answers GET <arts_item_<kind>_<id>/dp_mask.png> (and the /img/read form) for a dyeable garment when the
// streamed files had no such file; handleRoot calls it after the stream. The mask is composed from the garment's part
// masks (thumbs.ServeDyeMask, Cherry-made); the archive's original masks are never answered here. false leaves the
// request to the rest of handleRoot, which answers 404 (the dye cell then shows its warning icon).
func ServeDyeMask(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/img/read/arts_item_")
	if !ok {
		rest, ok = strings.CutPrefix(r.URL.Path, "/arts_item_")
		if !ok {
			return false
		}
	}
	kind, tail, ok := strings.Cut(rest, "_")
	if !ok || (kind != "custom" && kind != "dress") {
		return false
	}
	idText, file, ok := strings.Cut(tail, "/")
	if !ok || file != "dp_mask.png" || !httpx.IsAllDigits(idText) {
		return false
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || !slices.Contains(ActiveDyes().Dyeable, id) {
		return false
	}
	return thumbs.ServeDyeMask(w, kind, idText)
}

// RegisterDyeRoutes serves the Dye Shop, the Dye Base Shop and Dye Inventory lists, the palette, the dyeable garment list of
// the dye popups, the dye purchase and the apply. Routes under /cherry/dye/ are registered with the other Cherry pictures.
func RegisterDyeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/shop/dye/item/list", handleDyeShopList)
	mux.HandleFunc("/v4/shop/dye/baseitem/list/", handleDyeBaseShop)
	mux.HandleFunc("/v4/shop/dye/preview", handleDyePreview)
	mux.HandleFunc("/v4/inven/dye/item/list", handleDyeInvenList)
	mux.HandleFunc("/v4/inven/closet/baseitem", handleDyeBaseItems)
	mux.HandleFunc("/v4/purchase/dye/item", handleDyeBuy)
	mux.HandleFunc("/v4/inven/dye/item/use/custom", handleDyeUse)
}
