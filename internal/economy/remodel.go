package economy

import (
	"net/http"

	"cherry/internal/httpx"
)

// Remodeling Shop (home MENU goSomewhere(remodelingShop), NaRemodelingShopLayer). Floor tabs 1F/2F/3F
// request GET shop/interior/remodelingroomlist/<URMEXT|URM2F|URM3F>?isFull=false
// (NaRemodelingShopLayer::SelectCategory → NaDispatcherShop::ReqRemodelingShopItemList, IDA 2026-10-28).
// The reply is parsed by NaDispatcherShop::ResRemodelingShopItemList 0x1a83a28: result must be non-empty,
// then SbShopItemTable::ParseRemodelingTable reads result.items as an array of shop rows. An empty array
// runs the normal path (InitItemList 0x1fba7bc accepts 0 rows). The rows are rooms ("Purchase selected room
// for N Gems?", roomSize), and extra floors are free in Cherry (PLAN Economy), so nothing is for sale.
// ponytail: empty tabs; add rows when remodel rooms exist as Cherry-made operator data and the apply route
// (room apply, NaDispatcherRoom::ReqApplyRemodeling) is served.
func RegisterRemodelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/shop/interior/remodelingroomlist/", handleRemodelingRoomList)
}

func handleRemodelingRoomList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":{"items":[]}}`)
}
