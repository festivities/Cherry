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
	"time"

	"cherry/data"
	"cherry/internal/httpx"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Model House sets (home menu goSomewhere(modelhouse)): the Interior set shop list, the set detail the My Room
// purchase popup opens, and the set purchase. IDA 2026-10-28 (menu-audit rows-b-spec section 3): the list is
// GET shop/interior/setitems/list?isFull=true (NaDispatcherShop::ResInteriorSetShopItemList); a tap sends the
// set's list index as setItemCode to GET shop/interior/setitems/detail (GetInteriorSetIndex); the detail result
// is the set object (ResInteriorSetShopItemDetailInfo, 11738); buying posts POST purchase/set/buy with
// price = the set's discountedPrice and items[].itemCode = its element codes (ReqShopSetBuyURL, the body the
// client builds in ReqShopSetBuyURL and NaRoomSetPurchaseLayer::SendPurchaseSetItem).
//
// The Model House tap (NaInteriorSetShopLayer::CButtonHandler 0x1af2758) changes to My Room (scene 3, mode 12,
// id 305, the list index as text). NaMyRoomBaseScene::ReplaceBaseUI 0x22c8844 (case 305) then posts 12061 with
// the set and requests GET shop/interior/room/<setItemCode> (ReqInteriorShopSetRoomURL 0x1b98840, builder
// interiorShopSetRoomPath 0x1acf5d0). ResInteriorShopSetRoomURL 0x1b2b698 hands result to
// NaMyRoomMapManager::LoadMapFromJson 0x2289bd4 as the room layout (roomInfo members, posts 12060). ErrInteriorShopSetRoomURL
// 0x1a579b4 shows gErrMsg and returns to the Garden. The My Room BUY (a2 == 1003) then sends the detail request above.

// ModelHouse is the set list (data/modelhouse.json; an operator file in %LOCALAPPDATA%\Cherry\config replaces it).
// Strict JSON: unknown keys are an error. Cherry-made, not original game data.
//
//	origin       must be "cherry"
//	note         says the sets are Cherry-made; text for operators, not sent to the client
//	sets         zero or more sets; with none the list answers 404 (the client shows "Network Error.")
//	  code         GUMH and five base-36 characters (0-9, A-Z). The client parses it with SbItemTable::GetIntIdx and
//	               sends it back as setitemCode. MH is not one of the client's category letters, so no original item
//	               code equals it, and itemcode.ID refuses it.
//	  name         shown as the set's name
//	  description  shown under the name
//	  elements     1 to 12 Interior Shop item codes (RU...): on sale, with art, no duplicates
//	  price        Gems for the whole set (never Cash); the server checks the client's copy against it
type ModelHouse struct {
	Origin string          `json:"origin"`
	Note   string          `json:"note"`
	Sets   []ModelHouseSet `json:"sets"`
}

// ModelHouseSet is one Cherry-made set (see ModelHouse).
type ModelHouseSet struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Elements    []string `json:"elements"`
	Price       int64    `json:"price"`
}

var modelHouseFile = opdata.New("modelhouse.json", data.ModelHouseJSON, checkModelHouse, verifyModelHouse)

// ActiveModelHouse is the set list in use.
func ActiveModelHouse() *ModelHouse { return modelHouseFile.Get() }

// modelHouseCodePrefix is the reserved range: GUMH is the landscape attribute and the MH category, neither of
// which the client's category table or Cherry's codec knows for furniture, pets or landscapes.
const modelHouseCodePrefix = "GUMH"

// checkModelHouseCode accepts GUMH plus five base-36 characters that the item-code codec does not decode.
func checkModelHouseCode(code string) error {
	if len(code) != 9 || !strings.HasPrefix(code, modelHouseCodePrefix) {
		return fmt.Errorf("code %q must be %s and five base-36 characters", code, modelHouseCodePrefix)
	}
	for _, c := range code[len(modelHouseCodePrefix):] {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
			return fmt.Errorf("code %q must use 0-9 and A-Z only", code)
		}
	}
	if _, ok := itemcode.ID(code); ok {
		return fmt.Errorf("code %q decodes as a client item code: choose another", code)
	}
	return nil
}

// checkModelHouse validates the structure: origin, note, unique set codes in the reserved range, names,
// descriptions, 1 to 12 canonical room element codes with no duplicates, and a positive Gem price.
func checkModelHouse(m *ModelHouse) error {
	if m.Origin != "cherry" {
		return errors.New(`origin must be "cherry": the Model House sets are Cherry-made`)
	}
	if strings.TrimSpace(m.Note) == "" {
		return errors.New("note must say that the sets are Cherry-made")
	}
	seen := map[string]bool{}
	for i, s := range m.Sets {
		where := fmt.Sprintf("sets[%d] (%s)", i+1, s.Code)
		if err := checkModelHouseCode(s.Code); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		switch {
		case seen[s.Code]:
			return fmt.Errorf("%s: code is used twice", where)
		case strings.TrimSpace(s.Name) == "":
			return fmt.Errorf("%s: name must not be empty", where)
		case strings.TrimSpace(s.Description) == "":
			return fmt.Errorf("%s: description must not be empty", where)
		case s.Price < 1:
			return fmt.Errorf("%s: price must be at least 1 Gem", where)
		case len(s.Elements) < 1 || len(s.Elements) > 12:
			return fmt.Errorf("%s: elements must name 1 to 12 item codes", where)
		}
		seen[s.Code] = true
		elements := map[string]bool{}
		for _, code := range s.Elements {
			if _, ok := itemcode.ID(code); !ok || !strings.HasPrefix(code, "RU") {
				return fmt.Errorf("%s: element %q must be a canonical room item code (RU...)", where, code)
			}
			if elements[code] {
				return fmt.Errorf("%s: element %s is listed twice", where, code)
			}
			elements[code] = true
		}
	}
	return nil
}

// verifyModelHouse checks each element against the item archive at startup: an Interior Shop item with art,
// the same test a shop listing uses (itemAvailable) and the shop's category (classify).
func verifyModelHouse(m *ModelHouse) error {
	for _, s := range m.Sets {
		for _, code := range s.Elements {
			if _, k := classify(code); k != kindInterior || !itemAvailable(code) {
				return fmt.Errorf("sets (%s): element %s is not an Interior Shop item with art in the item archive (%s)", s.Code, code, thumbs.DpItemRoot)
			}
		}
	}
	return nil
}

// servedModelHouseSets are the sets offered now: every element is a catalog item and on sale. A list index
// (setItemCode of the detail request) counts in this slice, so the list and the detail agree.
func servedModelHouseSets(now time.Time) []ModelHouseSet {
	c := loadCatalog()
	var out []ModelHouseSet
	for _, s := range ActiveModelHouse().Sets {
		ok := true
		for _, code := range s.Elements {
			ok = ok && c.byCode[code] != nil && onSale(code, now)
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

// modelHouseRow is one set as the client parses it (sShopInteriorItem::SetParserJSON, ::SetParserJSONEX and
// sSetItemElement::SetParserJSON). Price and discountedPrice are the same: the purchase sends discountedPrice.
// Owned furniture marks existInven and purchased only when every element is owned; the purchase itself ignores
// owned state, as the Interior Shop does.
func modelHouseRow(s ModelHouseSet, index int, host string, owned map[string]bool) map[string]any {
	c := loadCatalog()
	elements := make([]map[string]any, 0, len(s.Elements))
	allOwned := true
	for _, code := range s.Elements {
		price, _, _, _ := ItemPrice(code)
		have := owned[code]
		allOwned = allOwned && have
		elements = append(elements, map[string]any{
			"itemId": c.byCode[code].ID, "itemCode": code, "price": price, "discountedPrice": price,
			"sellingIconType": "", "itemType": "ROOM", "grade": "N", "count": 1, "existInven": have,
			"discount": false, "selling": true, "soldout": false, "display": true, "specialEffects": "",
		})
	}
	img := itemImageURL(s.Elements[0], host)
	return map[string]any{
		"itemId": index + 1, "itemCode": s.Code, "name": s.Name, "description": s.Description, "itemType": "ROOM",
		"price": s.Price, "discountedPrice": s.Price, "discount": false, "sellingIconType": "", "specialEffects": "",
		"paymentType": "GEM", "sellingStart": "", "sellingEnd": "", "discountStart": "", "discountEnd": "",
		"selling": true, "soldout": false, "display": true, "existInven": allOwned, "purchased": allOwned,
		"representImageUrl": img, "thumbnailImageUrl": img,
		"elementItemCodes": s.Elements, "elementItems": elements,
	}
}

// handleModelHouseList serves GET shop/interior/setitems/list. With no set on sale it answers 404, never an
// empty items array: the client indexes that list and an empty one crashes it (rows-b-spec 3.2 and 3.3).
func handleModelHouseList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	served := servedModelHouseSets(NowFunc())
	if len(served) == 0 {
		log.Printf("cherry: modelhouse: %d of %d sets in modelhouse.json are on sale; answering 404", len(served), len(ActiveModelHouse().Sets))
		httpx.ServeNotFound(w)
		return
	}
	owned, _, _ := requesterOwned(r)
	rows := make([]map[string]any, 0, len(served))
	for i, s := range served {
		rows = append(rows, modelHouseRow(s, i, r.Host, owned))
	}
	body, _ := json.Marshal(map[string]any{"result": map[string]any{"items": rows}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// handleModelHouseDetail serves GET shop/interior/setitems/detail?setItemCode=N. The client sends the list index
// of the tapped set; a set code is accepted too. The result is the set object, not a list.
func handleModelHouseDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	served := servedModelHouseSets(NowFunc())
	key := r.URL.Query().Get("setItemCode")
	index := slices.IndexFunc(served, func(s ModelHouseSet) bool { return s.Code == key })
	if n, err := strconv.Atoi(key); index < 0 && err == nil && n >= 0 && n < len(served) {
		index = n
	}
	if index < 0 {
		httpx.ServeNotFound(w)
		return
	}
	owned, _, _ := requesterOwned(r)
	body, _ := json.Marshal(map[string]any{"result": modelHouseRow(served[index], index, r.Host, owned)})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// modelHouseBuyReq is the part of the purchase/set/buy body the server reads; the client also sends deviceType,
// isSetitem, language, arrangement, cpId, brandShopId and brandShopNo, which carry nothing the server prices.
type modelHouseBuyReq struct {
	ItemType    string          `json:"itemType"`
	SetItemCode string          `json:"setitemCode"`
	Price       json.RawMessage `json:"price"`
	CoinUnit    string          `json:"coinUnit"`
	Items       []struct {
		ItemCode string `json:"itemCode"`
	} `json:"items"`
}

// handleModelHouseBuy serves POST purchase/set/buy. The server price is authoritative: the client's price must
// equal the set's price, the client's element list must equal the set's, and only Gems are taken (Cash refused).
// Every element is granted, including ones the player already owns, as the Interior Shop grants furniture bought
// twice; the price is not reduced for owned pieces because the client shows one fixed price for the set.
func handleModelHouseBuy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req modelHouseBuyReq
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	served := servedModelHouseSets(NowFunc())
	i := slices.IndexFunc(served, func(s ModelHouseSet) bool { return s.Code == req.SetItemCode })
	if i < 0 {
		shopError(w, r, raw, errUnknown, "unknown set")
		return
	}
	s := served[i]
	if req.ItemType != "ROOM" || req.CoinUnit != "GEM" {
		shopError(w, r, raw, errUnknown, "room sets for Gems only")
		return
	}
	if cp, ok := lenientInt(req.Price); !ok || cp != s.Price {
		shopError(w, r, raw, errUpdated, "shop updated")
		return
	}
	codes := make([]string, 0, len(req.Items))
	for _, it := range req.Items {
		codes = append(codes, it.ItemCode)
	}
	if !slices.Equal(codes, s.Elements) {
		shopError(w, r, raw, errUnknown, "set elements changed")
		return
	}

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	if err := room.EnsureRoomLocked(acc); err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	previous := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -s.Price}, "modelhouse:set:"+s.Code)
	if err != nil {
		shopError(w, r, raw, errNoMoney, "insufficient balance")
		return
	}
	items := slices.Clone(acc.RoomItems)
	seq := max(acc.NextRoomSeq, store.FirstRoomSeq)
	for _, it := range items {
		seq = max(seq, it.Seq+1)
	}
	for _, code := range s.Elements {
		items = append(items, store.RoomItem{Seq: seq, Cd: code})
		seq++
	}
	acc.RoomItems, acc.NextRoomSeq = items, seq
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = previous
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	store.FlushLedgerLocked(lines)
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"balance": acc.Gems, "resultCode": 0, "buyItems": s.Elements}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// modelHouseRoom is the sample room a set's preview loads: the roomInfo members the client parses as a My Room
// layout (the shape room.roomLayoutInfo renders). Cherry-made placement, not verified in game: furniture on floor
// tiles at x and y in 2, 5, 8 (z 0, direction FR); wall decor on the back wall (direction BR, y 0, z 4, x in 2, 5,
// 8); the set's wallpaper as the wall and its flooring as the floor, otherwise the room defaults. Preview seqs
// start at 1001 and are not inventory seqs.
func modelHouseRoom(s ModelHouseSet) map[string]any {
	floor := map[string]any{"seq": "0", "cd": room.RoomFloorCode, "type": "floorTile"}
	wall := map[string]any{"seq": "0", "cd": room.RoomWallCode, "type": "wallTile"}
	floorItem, wallItem, item := []any{}, []any{}, []any{}
	var nFloor, nWall, nItem int
	for i, code := range s.Elements {
		seq := strconv.Itoa(1001 + i)
		switch code[2:4] {
		case "WA":
			wall = map[string]any{"seq": seq, "cd": code, "type": "wallTile"}
		case "TI":
			floor = map[string]any{"seq": seq, "cd": code, "type": "floorTile"}
		case "WD", "DO", "WI", "BW":
			wallItem = append(wallItem, map[string]any{"seq": seq, "cd": code, "direction": "BR",
				"x": 2 + 3*(nWall%3), "y": 0, "z": 4 + 3*(nWall/3)})
			nWall++
		case "TD":
			floorItem = append(floorItem, map[string]any{"seq": seq, "cd": code, "direction": "FR",
				"x": 2 + 3*(nFloor%3), "y": 2 + 3*(nFloor/3)})
			nFloor++
		default:
			item = append(item, map[string]any{"seq": seq, "cd": code, "direction": "FR",
				"x": 2 + 3*(nItem%3), "y": 2 + 3*(nItem/3), "superpose": map[string]any{"seq": []string{}}})
			nItem++
		}
	}
	return map[string]any{"groundLevel": "LEVEL_1", "tileSize": 12, "floor": floor, "wall": wall,
		"floorItem": floorItem, "wallItem": wallItem, "item": item, "memberAvatarIdList": []any{}}
}

// handleModelHouseRoom serves GET shop/interior/room/<setItemCode>, the preview room the My Room opens for a set
// (NaDispatcherShop::ReqInteriorShopSetRoomURL). An unknown or unsold set is 404: the client then shows the
// unexpected-error popup and returns to the Garden (ErrInteriorShopSetRoomURL).
func handleModelHouseRoom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	code := strings.TrimPrefix(r.URL.Path, "/v4/shop/interior/room/")
	served := servedModelHouseSets(NowFunc())
	i := slices.IndexFunc(served, func(s ModelHouseSet) bool { return s.Code == code })
	if i < 0 {
		httpx.ServeNotFound(w)
		return
	}
	body, _ := json.Marshal(map[string]any{"result": modelHouseRoom(served[i])})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// RegisterModelHouseRoutes serves the Model House list, set detail, set preview room and set purchase.
func RegisterModelHouseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/shop/interior/setitems/list", handleModelHouseList)
	mux.HandleFunc("/v4/shop/interior/setitems/detail", handleModelHouseDetail)
	mux.HandleFunc("/v4/shop/interior/room/", handleModelHouseRoom)
	mux.HandleFunc("/v4/purchase/set/buy", handleModelHouseBuy)
}
