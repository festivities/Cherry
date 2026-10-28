package economy

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"cherry/data"
	"cherry/internal/httpx"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
)

// Remodeling Shop (home MENU goSomewhere(remodelingShop), NaRemodelingShopLayer). Floor tabs 1F/2F/3F
// request GET shop/interior/remodelingroomlist/<URMEXT|URM2F|URM3F>?isFull=false
// (NaRemodelingShopLayer::SelectCategory → NaDispatcherShop::ReqRemodelingShopItemList, IDA 2026-10-28).
// The reply is parsed by NaDispatcherShop::ResRemodelingShopItemList 0x1a83a28: result must be non-empty,
// then SbShopItemTable::ParseRemodelingTable reads result.items as an array of shop rows.
//
// Why a tab needs a row (IDA 2026-10-28, rows 58 root cause): SelectCategory shows CNaSundries indicator
// (message 10011 → touch skip, s_bTouchSkip = 1, NaSystemIndicator as the top keypad delegate). The reply hides it
// in InitItemList 0x1fba7bc, but that function skips the hide when IsEndTooltip(4) is 0 (tip
// RemodelingShopTooltip_Inven not yet seen on this device, a CCUserDefault flag) and no item is selected. Only a
// first row selects one (MoveToSelectedItemPage → ResetTable → SelectItem). An empty tab therefore leaves the touch
// skip up: the back arrow and the tabs are ignored, and Android back is swallowed by the indicator.
//
// The rows (data/remodel.json) are the floors the player owns, served as existInven and inUseYn, so BUY is disabled
// and apply is refused locally (message 8164). No purchase or apply request is sent. Cherry-made names; see the note there.
type Remodel struct {
	Origin string       `json:"origin"`
	Note   string       `json:"note"`
	Rows   []RemodelRow `json:"rows"`
}

// RemodelRow is one row of a floor tab (see Remodel).
//
//	tab       URMEXT (1F), URM2F or URM3F; a tab without a row answers an empty list
//	code      canonical room item code (RU...): the client finds its art and item id by it
//	name      the row's name, Cherry-made
//	roomSize  the floor count the row stands for (1 to 3)
type RemodelRow struct {
	Tab      string `json:"tab"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	RoomSize int    `json:"roomSize"`
}

var remodelFile = opdata.New("remodel.json", data.RemodelJSON, checkRemodel, nil)

// ActiveRemodel is the row list in use.
func ActiveRemodel() *Remodel { return remodelFile.Get() }

// remodelTabs are the floor tabs the client requests.
var remodelTabs = map[string]bool{"URMEXT": true, "URM2F": true, "URM3F": true}

// checkRemodel validates the structure: origin, note, known and unique tabs, canonical room codes, names and sizes.
func checkRemodel(m *Remodel) error {
	if m.Origin != "cherry" {
		return errors.New(`origin must be "cherry": the Remodeling Shop rows are Cherry-made`)
	}
	if strings.TrimSpace(m.Note) == "" {
		return errors.New("note must say that the rows are Cherry-made")
	}
	seen := map[string]bool{}
	for i, r := range m.Rows {
		where := fmt.Sprintf("rows[%d] (%s)", i+1, r.Tab)
		switch {
		case !remodelTabs[r.Tab]:
			return fmt.Errorf("%s: tab must be URMEXT, URM2F or URM3F", where)
		case seen[r.Tab]:
			return fmt.Errorf("%s: tab is listed twice", where)
		case strings.TrimSpace(r.Name) == "":
			return fmt.Errorf("%s: name must not be empty", where)
		case r.RoomSize < 1 || r.RoomSize > 3:
			return fmt.Errorf("%s: roomSize must be 1 to 3", where)
		}
		if _, ok := itemcode.ID(r.Code); !ok || !strings.HasPrefix(r.Code, "RU") {
			return fmt.Errorf("%s: code %q must be a canonical room item code (RU...)", where, r.Code)
		}
		seen[r.Tab] = true
	}
	return nil
}

// RegisterRemodelRoutes serves the floor tab lists.
func RegisterRemodelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/shop/interior/remodelingroomlist/", handleRemodelingRoomList)
}

// handleRemodelingRoomList answers the floor tab named in the path with its rows (none for a tab without a row).
func handleRemodelingRoomList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	tab := path.Base(r.URL.Path)
	items := []map[string]any{}
	for _, row := range ActiveRemodel().Rows {
		if row.Tab == tab {
			items = append(items, remodelRow(row))
		}
	}
	httpx.WriteObj(w, map[string]any{"result": map[string]any{"items": items}})
}

// remodelRow is the shop row the client parses (sShopItem::SetParserJSON). Owned and in use: BUY stays disabled
// (UpdateBuyButtonState) and the apply check refuses it locally. The price fields are zero and paymentType is GEM.
func remodelRow(row RemodelRow) map[string]any {
	id, _ := itemcode.ID(row.Code)
	return map[string]any{
		"itemID": id, "itemCode": row.Code, "name": row.Name, "representImageURL": "", "bigRepresentImageURL": "",
		"price": 0, "discountedPrice": 0, "paymentType": "GEM", "discount": false, "soldout": false, "selling": true,
		"display": true, "sellingIconType": "", "specialEffects": "", "sellingEnd": "", "discountEnd": "", "dyeType": 0,
		"roomSize": row.RoomSize, "elementItems": []any{}, "elementItemCodes": []any{},
		"existInven": true, "inUseYn": true, "invenSeq": "",
	}
}
