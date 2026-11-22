package economy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"cherry/data"
	"cherry/internal/httpx"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// Closet styling slots (saved outfits): the list, detail, save, modify, delete, purchase and bookmark routes under
// /v4/style/slot/ (leftovers spec topic 4; REFERENCE "Closet and appearance"). Every reply is {"result": ...}. The base keys
// (totalSlotCount, usedSlotCount, maxSlotCount, price, coinUnit) ride on every reply but detail. data/styling.json holds the
// Cherry-made numbers. A slot purchase is charged in the configured currency through the ledger (reason styling:slot); the
// client's price and currency are checked against the file, never trusted.
//
// A refusal is HTTP 400 with errorCode and errorMessage (the shop shape, shopError), logged without credentials. It reuses
// the shop's codes: 61001 incorrect information (an unowned item, no free slot, an unknown slot), 61003 the price changed
// and 60001 not enough Gems or Cash.

// Styling is data/styling.json. Strict JSON: unknown keys are an error.
//
//	origin, note  cherry (opdata.Provenance)
//	free_slots    the styling slots every account has before it buys any: 0 to 100
//	max_slots     the most slots an account can hold: free_slots to 100
//	slot_price    the price of one extra slot: 0 or more
//	coin_unit     the currency of slot_price: GEM or CASH
type Styling struct {
	opdata.Provenance
	FreeSlots int64  `json:"free_slots"`
	MaxSlots  int64  `json:"max_slots"`
	SlotPrice int64  `json:"slot_price"`
	CoinUnit  string `json:"coin_unit"`
}

const (
	stylingSlotCap      = 100 // the largest free_slots and max_slots the file may name
	stylingNameMax      = 40  // the longest slot name, in runes
	stylingItemMax      = 40  // the most garments in one slot
	stylingLedgerReason = "styling:slot"
)

var stylingFile = opdata.New("styling.json", data.StylingJSON, checkStyling, nil)

// ActiveStyling is the styling file in use.
func ActiveStyling() *Styling { return stylingFile.Get() }

// checkStyling validates the structure at startup: the origin and note, the slot counts and the price.
func checkStyling(s *Styling) error {
	if err := s.CheckOrigin(opdata.OriginCherry); err != nil {
		return err
	}
	switch {
	case s.FreeSlots < 0 || s.FreeSlots > stylingSlotCap:
		return fmt.Errorf("free_slots must be 0 to %d", stylingSlotCap)
	case s.MaxSlots < s.FreeSlots || s.MaxSlots > stylingSlotCap:
		return fmt.Errorf("max_slots must be free_slots to %d", stylingSlotCap)
	case s.SlotPrice < 0:
		return errors.New("slot_price must be 0 or more")
	case s.CoinUnit != "GEM" && s.CoinUnit != "CASH":
		return errors.New(`coin_unit must be "GEM" or "CASH"`)
	}
	return nil
}

// styleBase is the base keys every styling reply carries.
type styleBase struct {
	TotalSlotCount int64  `json:"totalSlotCount"`
	UsedSlotCount  int64  `json:"usedSlotCount"`
	MaxSlotCount   int64  `json:"maxSlotCount"`
	Price          int64  `json:"price"`
	CoinUnit       string `json:"coinUnit"`
}

// styleBaseLocked is the base of acc: the free slots plus the bought ones, the slots in use and the purchase terms.
func styleBaseLocked(acc *store.Account) styleBase {
	cfg := ActiveStyling()
	return styleBase{
		TotalSlotCount: cfg.FreeSlots + acc.StyleBought, UsedSlotCount: int64(len(acc.StyleSlots)),
		MaxSlotCount: cfg.MaxSlots, Price: cfg.SlotPrice, CoinUnit: cfg.CoinUnit,
	}
}

// styleItemOut is one garment of a slot as the client parses it (sClosetStylingItem::ParseJson).
type styleItemOut struct {
	ItemCode                      string           `json:"itemCode"`
	InventorySeq                  string           `json:"inventorySeq"`
	DyeType                       int              `json:"dyeType"`
	ColorAndTransparencies        []store.DyeColor `json:"colorAndTransparencies"`
	ChangedColorAndTransparencies []store.DyeColor `json:"changedColorAndTransparencies"`
}

// styleSlotOut is one slot as the client parses it (sClosetStylingSlot::ParseJson).
type styleSlotOut struct {
	SlotID         int64          `json:"slotId"`
	BookmarkYn     bool           `json:"bookmarkYn"`
	StyleName      string         `json:"styleName"`
	StyleSlotItems []styleItemOut `json:"styleSlotItems"`
	ItemDeleteYn   bool           `json:"itemDeleteYn"`
}

// styleView is what the replies read of an account: its owned codes and the closet serial and dye of each owned code.
type styleView struct {
	acc     *store.Account
	owned   []string
	serials map[string]string
}

func newStyleView(acc *store.Account) styleView {
	owned := store.AccountOwnedCodes(acc)
	return styleView{acc: acc, owned: owned, serials: store.ItemSerials(store.ClosetOrder(owned))}
}

// has reports whether the account may wear code: it owns it, or it is a base item (store.IsAvatarBaseItem). The base clothes
// are never sold and the client's outfit carries them, so they are never "deleted".
func (v styleView) has(code string) bool {
	return slices.Contains(v.owned, code) || store.IsAvatarBaseItem(v.acc.Gender, code)
}

// slot builds the reply form of s. An owned garment takes its closet serial and its dye from the account now (the closet
// inventory does the same), so a slot follows its garment; the stored serial answers for a garment no longer owned.
// changedColorAndTransparencies is always an empty array: the dye is never stored in the slot.
func (v styleView) slot(s store.StyleSlot) styleSlotOut {
	out := styleSlotOut{SlotID: s.ID, BookmarkYn: s.Bookmark, StyleName: s.Name, StyleSlotItems: make([]styleItemOut, 0, len(s.Items))}
	for _, it := range s.Items {
		row := styleItemOut{ItemCode: it.ItemCode, InventorySeq: strconv.FormatInt(it.InventorySeq, 10),
			ColorAndTransparencies: []store.DyeColor{}, ChangedColorAndTransparencies: []store.DyeColor{}}
		if serial, owned := v.serials[it.ItemCode]; owned {
			row.InventorySeq = serial
			if colors, dyed := v.acc.DyeColors[it.ItemCode]; dyed {
				row.DyeType = store.DyeTypeTwoLayers
				row.ColorAndTransparencies = store.DyeLayers(colors)
			}
		}
		if !v.has(it.ItemCode) {
			out.ItemDeleteYn = true
		}
		out.StyleSlotItems = append(out.StyleSlotItems, row)
	}
	return out
}

type styleListReply struct {
	styleBase
	StyleSlotResponses []styleSlotOut `json:"styleSlotResponses"`
}

type styleSaveReply struct {
	styleBase
	Detail styleSlotOut `json:"styleSlotDetailResponse"`
}

type styleModifyReply struct {
	styleBase
	OldSlotID int64        `json:"oldSlotId"`
	Detail    styleSlotOut `json:"styleSlotDetailResponse"`
}

// styleDetailReply is the detail reply: the slot under styleSlotDetailResponse, the key the client reads first (NaClosetLayer
// MsgProc case 11080, IDA). It carries no base keys.
type styleDetailReply struct {
	Detail styleSlotOut `json:"styleSlotDetailResponse"`
}

type styleOldReply struct { // delete and bookmark
	styleBase
	OldSlotID int64 `json:"oldSlotId"`
}

type stylePurchaseReply struct {
	styleBase
	Balance     int64  `json:"balance"`
	BalanceUnit string `json:"balanceUnit"`
}

type styleItemReq struct {
	ItemCode     string          `json:"itemCode"`
	InventorySeq json.RawMessage `json:"inventorySeq"`
}

// styleSlotReq is the body of save and modify (modify also names the slot).
type styleSlotReq struct {
	SlotID    json.RawMessage `json:"slotId"`
	StyleName string          `json:"styleName"`
	Items     []styleItemReq  `json:"items"`
}

type stylePurchaseReq struct {
	CoinUnit string          `json:"coinUnit"`
	Price    json.RawMessage `json:"price"`
}

// HandleStyleSlot serves /v4/style/slot/<op>[/<id>]: list and detail/<id> (GET), save, modify and purchase (POST), and
// delete/<id> and bookmark/<id> (POST, body null). Any other method or shape is 404.
func HandleStyleSlot(w http.ResponseWriter, r *http.Request) {
	op, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v4/style/slot/"), "/")
	id, idOK := styleSlotID(rest)
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	switch {
	case op == "list" && get && rest == "":
		styleList(w, r)
	case op == "detail" && get && idOK:
		styleDetail(w, r, id)
	case op == "save" && post && rest == "":
		styleSave(w, r)
	case op == "modify" && post && rest == "":
		styleModify(w, r)
	case op == "purchase" && post && rest == "":
		stylePurchase(w, r)
	case op == "delete" && post && idOK:
		styleDelete(w, r, id)
	case op == "bookmark" && post && idOK:
		styleBookmark(w, r, id)
	default:
		httpx.ServeNotFound(w)
	}
}

// styleSlotID reads the id in a route's last segment: digits, above 0.
func styleSlotID(s string) (int64, bool) {
	if !httpx.IsAllDigits(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}

// writeStyleResult writes {"result": result} with status 200.
func writeStyleResult(w http.ResponseWriter, result any) {
	httpx.WriteObj(w, struct {
		Result any `json:"result"`
	}{result})
}

// stylingError refuses a styling request with the shop shape and a credential-free log line.
func stylingError(w http.ResponseWriter, r *http.Request, code, msg string) {
	log.Printf("cherry: styling refused %s errorCode=%s (%s)", r.URL.Path, code, msg)
	httpx.WriteJSON(w, http.StatusBadRequest, fmt.Sprintf(`{"errorCode":%q,"errorMessage":"cherry: %s"}`, code, msg))
}

// styleAccount is the account of the AV_AUTH cookie, the closet handlers' identity. Without one it writes the 404 and
// returns nil. The caller holds store.AccountsMu.
func styleAccount(w http.ResponseWriter, r *http.Request) *store.Account {
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
	}
	return acc
}

// styleFind returns the slot with id and its index in acc.StyleSlots (-1 when there is none).
func styleFind(acc *store.Account, id int64) (store.StyleSlot, int) {
	i := slices.IndexFunc(acc.StyleSlots, func(s store.StyleSlot) bool { return s.ID == id })
	if i < 0 {
		return store.StyleSlot{}, -1
	}
	return acc.StyleSlots[i], i
}

// readStyleBody decodes a styling body: the native JSON with its NUL terminator, optionally gzipped.
func readStyleBody(r *http.Request, dst any) bool {
	raw, err := httpx.ReadNativeBody(r)
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, dst)
	}
	return err == nil
}

// styleSerial reads an inventorySeq: a number or a numeric string; absent or null is 0. Anything else is a bad request.
func styleSerial(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, true
	}
	n, ok := lenientInt(raw)
	return n, ok && n >= 0
}

// styleShapeOK checks a slot name and item count against their caps (refused, not truncated).
func styleShapeOK(w http.ResponseWriter, r *http.Request, name string, items []styleItemReq) bool {
	switch {
	case utf8.RuneCountInString(name) > stylingNameMax:
		stylingError(w, r, errUnknown, "name too long")
	case len(items) > stylingItemMax:
		stylingError(w, r, errUnknown, "too many items")
	default:
		return true
	}
	return false
}

// styleSlotItems turns a request's items into slot items, under the lock. Each code must be owned or a base item: the
// client's outfit carries the base clothes (save/v2 skips them), so those need no purchase. An unowned code is refused,
// not dropped, so the slot saved is the outfit the client showed.
func styleSlotItems(w http.ResponseWriter, r *http.Request, v styleView, items []styleItemReq) ([]store.StyleItem, bool) {
	out := make([]store.StyleItem, 0, len(items))
	for _, it := range items {
		if it.ItemCode == "" || !v.has(it.ItemCode) {
			stylingError(w, r, errUnknown, "item not owned")
			return nil, false
		}
		seq, ok := styleSerial(it.InventorySeq)
		if !ok {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return nil, false
		}
		out = append(out, store.StyleItem{ItemCode: it.ItemCode, InventorySeq: seq})
	}
	return out, true
}

func styleList(w http.ResponseWriter, r *http.Request) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := styleAccount(w, r)
	if acc == nil {
		return
	}
	v := newStyleView(acc)
	slots := make([]styleSlotOut, 0, len(acc.StyleSlots))
	for _, s := range acc.StyleSlots {
		slots = append(slots, v.slot(s))
	}
	writeStyleResult(w, styleListReply{styleBase: styleBaseLocked(acc), StyleSlotResponses: slots})
}

func styleDetail(w http.ResponseWriter, r *http.Request, id int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := styleAccount(w, r)
	if acc == nil {
		return
	}
	s, i := styleFind(acc, id)
	if i < 0 {
		stylingError(w, r, errUnknown, "no such slot")
		return
	}
	writeStyleResult(w, styleDetailReply{Detail: newStyleView(acc).slot(s)})
}

func styleSave(w http.ResponseWriter, r *http.Request) {
	var req styleSlotReq
	if !readStyleBody(r, &req) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if !styleShapeOK(w, r, req.StyleName, req.Items) {
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := styleAccount(w, r)
	if acc == nil {
		return
	}
	if int64(len(acc.StyleSlots)) >= styleBaseLocked(acc).TotalSlotCount {
		stylingError(w, r, errUnknown, "no free styling slot")
		return
	}
	v := newStyleView(acc)
	items, ok := styleSlotItems(w, r, v, req.Items)
	if !ok {
		return
	}
	prev := *acc
	acc.StyleNextID++
	slot := store.StyleSlot{ID: acc.StyleNextID, Name: req.StyleName, Items: items}
	acc.StyleSlots = append(slices.Clone(acc.StyleSlots), slot)
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	writeStyleResult(w, styleSaveReply{styleBase: styleBaseLocked(acc), Detail: v.slot(slot)})
}

func styleModify(w http.ResponseWriter, r *http.Request) {
	var req styleSlotReq
	if !readStyleBody(r, &req) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	id, ok := lenientInt(req.SlotID)
	if !ok || id <= 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if !styleShapeOK(w, r, req.StyleName, req.Items) {
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := styleAccount(w, r)
	if acc == nil {
		return
	}
	old, i := styleFind(acc, id)
	if i < 0 {
		stylingError(w, r, errUnknown, "no such slot")
		return
	}
	v := newStyleView(acc)
	items, ok := styleSlotItems(w, r, v, req.Items)
	if !ok {
		return
	}
	next := store.StyleSlot{ID: id, Name: req.StyleName, Bookmark: old.Bookmark, Items: items}
	prev := *acc
	acc.StyleSlots = slices.Clone(acc.StyleSlots)
	acc.StyleSlots[i] = next
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	writeStyleResult(w, styleModifyReply{styleBase: styleBaseLocked(acc), OldSlotID: id, Detail: v.slot(next)})
}

func styleDelete(w http.ResponseWriter, r *http.Request, id int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := styleAccount(w, r)
	if acc == nil {
		return
	}
	_, i := styleFind(acc, id)
	if i < 0 {
		stylingError(w, r, errUnknown, "no such slot")
		return
	}
	prev := *acc
	acc.StyleSlots = slices.Delete(slices.Clone(acc.StyleSlots), i, i+1)
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	writeStyleResult(w, styleOldReply{styleBase: styleBaseLocked(acc), OldSlotID: id})
}

func styleBookmark(w http.ResponseWriter, r *http.Request, id int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := styleAccount(w, r)
	if acc == nil {
		return
	}
	s, i := styleFind(acc, id)
	if i < 0 {
		stylingError(w, r, errUnknown, "no such slot")
		return
	}
	s.Bookmark = !s.Bookmark
	prev := *acc
	acc.StyleSlots = slices.Clone(acc.StyleSlots)
	acc.StyleSlots[i] = s
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	writeStyleResult(w, styleOldReply{styleBase: styleBaseLocked(acc), OldSlotID: id})
}

// stylePurchase buys one extra slot. The price and currency must be the file's (a mismatch is 61003 "shop updated" or
// 61001), the account must have a slot left to buy and the Gems or Cash must cover it. The spend and the new count are one save.
func stylePurchase(w http.ResponseWriter, r *http.Request) {
	var req stylePurchaseReq
	if !readStyleBody(r, &req) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	cfg := ActiveStyling()
	if req.CoinUnit != cfg.CoinUnit {
		stylingError(w, r, errUnknown, "coin unit differs from the shop")
		return
	}
	if p, ok := lenientInt(req.Price); !ok || p != cfg.SlotPrice {
		stylingError(w, r, errUpdated, "shop updated")
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := styleAccount(w, r)
	if acc == nil {
		return
	}
	if styleBaseLocked(acc).TotalSlotCount >= cfg.MaxSlots {
		stylingError(w, r, errUnknown, "all styling slots are bought")
		return
	}
	deltas := store.LedgerDeltas{Gems: -cfg.SlotPrice}
	if cfg.CoinUnit == "CASH" {
		deltas = store.LedgerDeltas{Cash: -cfg.SlotPrice}
	}
	prev := *acc
	lines, err := store.SpendLocked(acc, deltas, stylingLedgerReason)
	if err != nil {
		stylingError(w, r, errNoMoney, "insufficient balance")
		return
	}
	acc.StyleBought++
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	reply := stylePurchaseReply{styleBase: styleBaseLocked(acc), Balance: acc.Gems, BalanceUnit: "GEM"}
	if cfg.CoinUnit == "CASH" {
		reply.Balance, reply.BalanceUnit = acc.Cash, "CASH"
	}
	writeStyleResult(w, reply)
}
