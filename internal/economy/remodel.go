package economy

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/httpx"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Remodeling Shop (home MENU goSomewhere(remodelingShop), NaRemodelingShopLayer; spec remodel-3f-expand-20261124\spec.md).
//
// Floor tabs 1F/2F/3F request GET /v4/shop/interior/remodelingroomlist/<URMEXT|URM2F|URM3F>?isFull=false.
// Buying a row sends POST /v4/purchase/interior/buy with itemType="ROOM" and items=[{itemCode, price, cpId:"avtplay"}]
// (remodelBuy, auto-applied on purchase because the client marks inUseYn=true and advances ownMaxLevel locally).
// Switching between owned sizes on a floor sends GET /v4/shop/interior/apply/<invenSeq> (strict boolean result).
type Remodel struct {
	Origin string       `json:"origin"`
	Note   string       `json:"note"`
	Rows   []RemodelRow `json:"rows"`
}

// RemodelRow is one room-size option on a floor tab (URMEXT=1F, URM2F=2F, URM3F=3F).
//
//	tab       URMEXT (1F), URM2F (2F), or URM3F (3F)
//	code      canonical room item code (RULA...): distinct across all tabs because buy sends only itemCode
//	name      display name (served by POST /v4/items/room/some via ItemName)
//	tileSize  even floor grid size (12..54; when >= 36, divisible by ceil(tileSize/18) so MpManager::LoadFromJson never truncates)
//	roomSize  Room Party capacity tier written to NaSettingManager::SetCurrentRoomSize (<=23->5, 24..29->7, 30..35->10, >=36->20)
//	price     cost in Currency (0 = free starter on owned floors)
//	currency  "GEM" (default) or "CASH"
//	artFrom   optional original interior item id (e.g. "122300054") whose art ServeRemodelFile aliases for Cherry-made 2F/3F codes
type RemodelRow struct {
	Tab      string `json:"tab"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	TileSize int    `json:"tileSize"`
	RoomSize int    `json:"roomSize"`
	Price    int64  `json:"price"`
	Currency string `json:"currency,omitempty"`
	ArtFrom  string `json:"artFrom,omitempty"`
}

var remodelFile = opdata.New("remodel.json", data.RemodelJSON, checkRemodel, nil)

// ActiveRemodel is the row list in use.
func ActiveRemodel() *Remodel { return remodelFile.Get() }

// remodelTabFloor maps a Remodeling Shop tab code to its 1-based floor number.
func remodelTabFloor(tab string) (int, bool) {
	switch tab {
	case "URMEXT":
		return 1, true
	case "URM2F":
		return 2, true
	case "URM3F":
		return 3, true
	}
	return 0, false
}

// remodelTileSizeValid reports whether sz is an even tileSize that MpManager::LoadFromJson renders without truncation.
func remodelTileSizeValid(sz int) bool {
	if sz < store.DefaultRoomTileSize || sz > 54 || sz%2 != 0 {
		return false
	}
	if sz >= 36 {
		sectors := sz/18 + boolInt(sz%18 != 0)
		return sz%sectors == 0
	}
	return true
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// checkRemodel validates data/remodel.json at startup.
func checkRemodel(m *Remodel) error {
	if m.Origin != "cherry" && m.Origin != "mixed" {
		return errors.New(`origin must be "cherry" or "mixed"`)
	}
	if strings.TrimSpace(m.Note) == "" {
		return errors.New("note must not be empty")
	}
	seenCode := map[string]bool{}
	seenTabSize := map[string]bool{}
	for i, r := range m.Rows {
		where := fmt.Sprintf("rows[%d] (%s %s)", i+1, r.Tab, r.Code)
		if _, ok := remodelTabFloor(r.Tab); !ok {
			return fmt.Errorf("%s: tab must be URMEXT, URM2F or URM3F", where)
		}
		if _, ok := itemcode.ID(r.Code); !ok || !strings.HasPrefix(r.Code, "RU") {
			return fmt.Errorf("%s: code %q must be a canonical room item code (RU...)", where, r.Code)
		}
		if seenCode[r.Code] {
			return fmt.Errorf("%s: duplicate item code", where)
		}
		seenCode[r.Code] = true
		if strings.TrimSpace(r.Name) == "" {
			return fmt.Errorf("%s: name must not be empty", where)
		}
		if !remodelTileSizeValid(r.TileSize) {
			return fmt.Errorf("%s: tileSize %d must be even in 12..54 (and exact for sector split when >=36)", where, r.TileSize)
		}
		key := fmt.Sprintf("%s:%d", r.Tab, r.TileSize)
		if seenTabSize[key] {
			return fmt.Errorf("%s: duplicate tileSize %d in tab %s", where, r.TileSize, r.Tab)
		}
		seenTabSize[key] = true
		if r.RoomSize < 1 || r.RoomSize > 100 {
			return fmt.Errorf("%s: roomSize must be 1 to 100", where)
		}
		if r.Price < 0 {
			return fmt.Errorf("%s: price must not be negative", where)
		}
		if cur := strings.ToUpper(r.Currency); cur != "" && cur != "GEM" && cur != "CASH" {
			return fmt.Errorf("%s: currency %q must be GEM or CASH", where, r.Currency)
		}
		if r.ArtFrom != "" && !httpx.IsAllDigits(r.ArtFrom) {
			return fmt.Errorf("%s: artFrom %q must be digits", where, r.ArtFrom)
		}
	}
	return nil
}

func remodelByCode(code string) (RemodelRow, int, bool) {
	for _, r := range ActiveRemodel().Rows {
		if r.Code == code {
			floor, _ := remodelTabFloor(r.Tab)
			return r, floor, true
		}
	}
	return RemodelRow{}, 0, false
}

// defaultRemodelItemName returns the remodel.json name of code without any staff override.
func defaultRemodelItemName(code string) (string, bool) {
	r, _, ok := remodelByCode(code)
	return r.Name, ok
}

// RemodelItemName returns the display name of a Remodeling Shop item code (used by ItemName / items/room/some),
// preferring any live staff override.
func RemodelItemName(code string) (string, bool) {
	r, _, ok := remodelByCode(code)
	if !ok {
		return "", false
	}
	if custom, ok := store.ItemNameOverride(code); ok {
		return custom, true
	}
	return r.Name, true
}

func remodelOwnsRow(rem *store.RoomRemodel, r RemodelRow, floor int) bool {
	if r.Price == 0 && floor <= rem.RoomMaxFloor() {
		return true
	}
	return rem.Owns(r.Code)
}

func remodelCurrency(r RemodelRow) (paymentType string, cash bool) {
	if strings.EqualFold(r.Currency, "CASH") {
		return "CASH", true
	}
	return "GEM", false
}

// RegisterRemodelRoutes serves the Remodeling Shop list and apply routes (purchase goes through purchase/interior/buy).
// At runtime applyRemodelingURL is GET /v4/room/inventory/change/<invenSeq> (live-verified 2026-11-24).
func RegisterRemodelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/shop/interior/remodelingroomlist/", handleRemodelingRoomList)
	mux.HandleFunc("/v4/room/inventory/change/", handleRemodelingApply)
}

// handleRemodelingRoomList serves GET /v4/shop/interior/remodelingroomlist/<URMEXT|URM2F|URM3F>?isFull=false.
func handleRemodelingRoomList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	tab := path.Base(r.URL.Path)
	floor, ok := remodelTabFloor(tab)
	if !ok {
		httpx.WriteObj(w, map[string]any{"result": map[string]any{"items": []any{}}})
		return
	}
	var rem *store.RoomRemodel
	if snap, ok := store.AccountForRequest(r); ok {
		rem = snap.Remodel
	}
	level := "LEVEL_" + strconv.Itoa(floor)
	maxFloor := rem.RoomMaxFloor()
	activeSize := rem.FloorTileSize(level)
	items := []map[string]any{}
	for _, row := range ActiveRemodel().Rows {
		if row.Tab != tab {
			continue
		}
		owned := remodelOwnsRow(rem, row, floor)
		inUse := owned && floor <= maxFloor && activeSize == row.TileSize
		items = append(items, remodelRowJSON(row, owned, inUse))
	}
	httpx.WriteObj(w, map[string]any{"result": map[string]any{"items": items}})
}

// remodelRowJSON builds one sShopItem / ParseRemodelingTable entry. Owned rows carry a non-zero invenSeq (the item's
// numeric id) so NaRemodelingShopLayer::IsApplyRemodelingCheck enables GET /v4/shop/interior/apply/<invenSeq>.
func remodelRowJSON(row RemodelRow, owned, inUse bool) map[string]any {
	id, _ := itemcode.ID(row.Code)
	payType, _ := remodelCurrency(row)
	invenSeq := ""
	if owned && id > 0 {
		invenSeq = strconv.FormatInt(id, 10)
	}
	return map[string]any{
		"itemID": id, "itemCode": row.Code, "name": ItemName(row.Code), "representImageURL": "", "bigRepresentImageURL": "",
		"price": row.Price, "discountedPrice": row.Price, "paymentType": payType, "discount": false, "soldout": false,
		"selling": true, "display": true, "sellingIconType": "", "specialEffects": "", "sellingEnd": "", "discountEnd": "",
		"dyeType": 0, "roomSize": row.RoomSize, "elementItems": []any{}, "elementItemCodes": []any{},
		"existInven": owned, "inUseYn": inUse, "invenSeq": invenSeq,
	}
}

// remodelBuy serves POST /v4/purchase/interior/buy for a Remodeling Shop item code (spec 2.2).
// Auto-applies the purchased room size and advances ownMaxGroundLevel when unlocking a new floor.
func remodelBuy(w http.ResponseWriter, r *http.Request, raw []byte, req shopBuyReq) {
	if len(req.Items) != 1 {
		shopError(w, r, raw, errUnknown, "bad product")
		return
	}
	it := req.Items[0]
	row, floor, ok := remodelByCode(it.ItemCode)
	if !ok {
		shopError(w, r, raw, errUnknown, "unknown remodel item")
		return
	}
	if cp, ok := lenientInt(it.Price); !ok || cp != row.Price {
		shopError(w, r, raw, errUpdated, "price mismatch")
		return
	}
	_, cash := remodelCurrency(row)

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	curMax := acc.RoomMaxFloor()
	if floor > curMax+1 {
		shopError(w, r, raw, errUpdated, "unlock previous floor first")
		return
	}
	if remodelOwnsRow(acc.Remodel, row, floor) {
		shopError(w, r, raw, errOwned, "already owned")
		return
	}
	previous := *acc
	var lines []store.LedgerLine
	if row.Price > 0 {
		deltas := store.LedgerDeltas{Gems: -row.Price}
		if cash {
			deltas = store.LedgerDeltas{Cash: -row.Price}
		}
		var err error
		lines, err = store.SpendLocked(acc, deltas, "remodel:"+row.Code)
		if err != nil {
			shopError(w, r, raw, errNoMoney, "not enough currency")
			return
		}
	}

	nextRem := cloneRoomRemodel(acc.Remodel)
	if floor > nextRem.MaxFloor {
		nextRem.MaxFloor = floor
	}
	if nextRem.MaxFloor < curMax {
		nextRem.MaxFloor = curMax
	}
	level := "LEVEL_" + strconv.Itoa(floor)
	nextRem.FloorSizes[level] = row.TileSize
	if !slices.Contains(nextRem.Owned, row.Code) {
		nextRem.Owned = append(nextRem.Owned, row.Code)
	}
	acc.Rooms = clipRoomLayoutForSize(acc.Rooms, level, row.TileSize)
	acc.Remodel = nextRem
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = previous
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	bal := acc.Gems
	if cash {
		bal = acc.Cash
	}
	httpx.WriteObj(w, map[string]any{"result": map[string]any{"balance": bal}})
}

// clipRoomLayoutForSize keeps placed furniture within [0, tileSize) when a floor is resized smaller.
// Dropped items remain in acc.RoomItems (inventory); on LEVEL_1 a door or diary outside bounds is moved
// back to a safe default spot inside 12x12 so the floor stays saveable.
func clipRoomLayoutForSize(rooms map[string]store.RoomLayout, level string, tileSize int) map[string]store.RoomLayout {
	lay, ok := rooms[level]
	if !ok || len(lay.Placed) == 0 {
		return rooms
	}
	changed := false
	kept := make([]store.RoomPlaced, 0, len(lay.Placed))
	keptSeq := make(map[int64]bool, len(lay.Placed))
	for _, p := range lay.Placed {
		if p.X >= 0 && p.X < tileSize && p.Y >= 0 && p.Y < tileSize {
			kept = append(kept, p)
			keptSeq[p.Seq] = true
			continue
		}
		changed = true
		if level == "LEVEL_1" && len(p.Cd) >= 4 {
			switch p.Cd[2:4] {
			case "DO":
				p.X, p.Y, p.Z, p.Dir = 5, 0, 3, "FL"
				kept = append(kept, p)
				keptSeq[p.Seq] = true
			case "AB":
				p.X, p.Y, p.Z, p.Dir = 2, 2, 0, "FR"
				kept = append(kept, p)
				keptSeq[p.Seq] = true
			}
		}
	}
	if !changed {
		return rooms
	}
	for i := range kept {
		if len(kept[i].Superpose) == 0 {
			continue
		}
		kept[i].Superpose = slices.DeleteFunc(slices.Clone(kept[i].Superpose), func(s int64) bool { return !keptSeq[s] })
	}
	next := maps.Clone(rooms)
	lay.Placed = kept
	next[level] = lay
	return next
}

func cloneRoomRemodel(r *store.RoomRemodel) *store.RoomRemodel {
	out := &store.RoomRemodel{
		MaxFloor:   r.RoomMaxFloor(),
		FloorSizes: map[string]int{},
	}
	if r != nil {
		if r.FloorSizes != nil {
			out.FloorSizes = maps.Clone(r.FloorSizes)
		}
		out.Owned = slices.Clone(r.Owned)
	}
	return out
}

// handleRemodelingApply serves GET /v4/room/inventory/change/<invenSeq> (live-verified 2026-11-24; spec 2.3):
// applies an owned remodeling item to its floor and returns {"result":true} or {"result":false}.
func handleRemodelingApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	seqText := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/room/inventory/change/"), "/")
	id, err := strconv.ParseInt(seqText, 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteObj(w, map[string]any{"result": false})
		return
	}
	code, ok := itemcode.Code(id)
	if !ok {
		httpx.WriteObj(w, map[string]any{"result": false})
		return
	}
	row, floor, ok := remodelByCode(code)
	if !ok {
		httpx.WriteObj(w, map[string]any{"result": false})
		return
	}

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	if !remodelOwnsRow(acc.Remodel, row, floor) {
		httpx.WriteObj(w, map[string]any{"result": false})
		return
	}
	prevRem := acc.Remodel
	prevRooms := acc.Rooms
	nextRem := cloneRoomRemodel(prevRem)
	if floor > nextRem.MaxFloor {
		nextRem.MaxFloor = floor
	}
	level := "LEVEL_" + strconv.Itoa(floor)
	nextRem.FloorSizes[level] = row.TileSize
	acc.Remodel = nextRem
	acc.Rooms = clipRoomLayoutForSize(prevRooms, level, row.TileSize)
	if err := store.SaveLocked(nil, acc); err != nil {
		acc.Remodel, acc.Rooms = prevRem, prevRooms
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteObj(w, map[string]any{"result": true})
}

// ServeRemodelFile answers item asset requests for Cherry-made 2F/3F remodeling codes that alias an original room-grid
// folder via ArtFrom (e.g. 122399201 -> 122300054), and bridges arts_item_custom_ requests for RULA... remodeling items
// whose originals live under static/item/interior/. Runs before ServeRoute in handleRoot so the asset stream is never
// asked for synthetic ids. Returns true if it answered.
func ServeRemodelFile(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	p := strings.TrimPrefix(r.URL.Path, "/img/read")
	var rest string
	var ok, isCustom bool
	if rest, ok = strings.CutPrefix(p, "/arts_item_interior_"); !ok {
		rest, ok = strings.CutPrefix(p, "/arts_item_custom_")
		isCustom = ok
	}
	if !ok {
		return false
	}
	idText, file, ok := strings.Cut(rest, "/")
	if !ok || !httpx.IsAllDigits(idText) {
		return false
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		return false
	}
	code, ok := itemcode.Code(id)
	if !ok {
		return false
	}
	row, _, ok := remodelByCode(code)
	if !ok {
		return false
	}
	srcID := row.ArtFrom
	if srcID == "" {
		if !isCustom {
			return false
		}
		srcID = idText
	}
	switch file {
	case "dp.png":
		thumbs.ServeDpPNG(w, "interior", srcID)
		return true
	case "1409_iteminfo.artsitem", "fl_l0_a00.png":
		if assets.Archive.ServeKey(w, r, "static/item/interior/"+srcID+"/"+file) {
			return true
		}
		httpx.ServeNotFound(w)
		return true
	default:
		httpx.ServeNotFound(w)
		return true
	}
}
