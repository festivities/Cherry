package economy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"cherry/internal/itemcode"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

func remodelSetupAccount(t *testing.T, tok, aid string, gems int64) *store.Account {
	t.Helper()
	acc := &store.Account{
		Aid: aid, Name: "Remodeler", Gems: gems,
		Rooms: map[string]store.RoomLayout{
			"LEVEL_1": {
				Placed: []store.RoomPlaced{
					{Seq: 1001, Cd: "RUDO0002Z", X: 18, Y: 0, Z: 3, Dir: "FL"},
					{Seq: 1002, Cd: "RUAB00002", X: 15, Y: 15, Dir: "FR"},
					{Seq: 1003, Cd: "RUCH0011X", X: 4, Y: 4, Dir: "FR"},
					{Seq: 1004, Cd: "RUCH0011Y", X: 20, Y: 20, Dir: "FR"},
				},
			},
		},
	}
	testutil.InstallAccounts(t, map[string]*store.Account{tok: acc})
	return acc
}

func remodelListItems(t *testing.T, tok, tab string) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v4/shop/interior/remodelingroomlist/"+tab+"?isFull=false", nil)
	if tok != "" {
		req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: tok})
	}
	rec := httptest.NewRecorder()
	handleRemodelingRoomList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d body %s", tab, rec.Code, rec.Body)
	}
	var env struct {
		Result struct {
			Items []map[string]any `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s: reply %s: %v", tab, rec.Body, err)
	}
	return env.Result.Items
}

func remodelBuyReq(t *testing.T, tok, code string, price int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"itemType": "ROOM",
		"items":    []map[string]any{{"itemCode": code, "price": price, "cpId": "avtplay"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v4/purchase/interior/buy", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: tok})
	rec := httptest.NewRecorder()
	handleShopBuy(kindInterior)(rec, req)
	return rec
}

func remodelApplyReq(t *testing.T, tok, seq string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v4/shop/interior/apply/"+seq, nil)
	req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: tok})
	rec := httptest.NewRecorder()
	handleRemodelingApply(rec, req)
	return rec
}

// TestRemodelingRoomListRows verifies all three floor tabs (1F/2F/3F) return their operator-configured rows with
// starter 1F/2F 12x12 owned+inUse (with non-zero invenSeq) and 3F unowned by default.
func TestRemodelingRoomListRows(t *testing.T) {
	remodelSetupAccount(t, "rtok", "9301", 20000)
	for _, tab := range []string{"URMEXT", "URM2F", "URM3F"} {
		items := remodelListItems(t, "rtok", tab)
		if len(items) != 4 {
			t.Fatalf("%s: %d rows, want 4", tab, len(items))
		}
		first := items[0]
		code := first["itemCode"].(string)
		id, ok := itemcode.ID(code)
		if back, _ := itemcode.Code(id); !ok || back != code {
			t.Fatalf("%s: code %s does not round-trip", tab, code)
		}
		wantOwned := tab != "URM3F"
		if first["itemID"] != float64(id) || first["existInven"] != wantOwned || first["inUseYn"] != wantOwned {
			t.Fatalf("%s first row = %v, want owned=%v inUse=%v", tab, first, wantOwned, wantOwned)
		}
		if wantOwned && first["invenSeq"] != fmt.Sprint(id) {
			t.Fatalf("%s owned invenSeq = %v, want %d", tab, first["invenSeq"], id)
		}
		if !wantOwned && first["invenSeq"] != "" {
			t.Fatalf("%s unowned invenSeq = %v, want empty", tab, first["invenSeq"])
		}
		if name := ItemName(code); name == "" {
			t.Fatalf("ItemName(%s) = %q", code, name)
		}
	}
	// Unknown tab answers empty list; non-GET is 404.
	if items := remodelListItems(t, "rtok", "UNKNOWN"); len(items) != 0 {
		t.Fatalf("UNKNOWN tab: %d rows, want 0", len(items))
	}
	rec := httptest.NewRecorder()
	handleRemodelingRoomList(rec, httptest.NewRequest(http.MethodPost, "/v4/shop/interior/remodelingroomlist/URMEXT", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST status %d, want 404", rec.Code)
	}
}

// TestRemodelingBuyAndApply verifies buying 1F expansion and 3F unlock, ledger debit, auto-apply, duplicate/price/funds
// guards, and switching back to a smaller size via apply (which clips out-of-bounds furniture while keeping LEVEL_1 door/diary).
func TestRemodelingBuyAndApply(t *testing.T) {
	acc := remodelSetupAccount(t, "btok", "9302", 12000)

	// Wrong price -> 61003
	if rec := remodelBuyReq(t, "btok", "RULA0001H", 1); !bytes.Contains(rec.Body.Bytes(), []byte(`"61003"`)) {
		t.Fatalf("wrong price = %s", rec.Body)
	}
	// Buy 1F 24x24 (4000 Gems): succeeds, auto-applies, balance 8000.
	rec := remodelBuyReq(t, "btok", "RULA0001H", 4000)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"balance":8000}}` {
		t.Fatalf("buy 1F 24x24 = %d %s", rec.Code, rec.Body)
	}
	if acc.FloorTileSize("LEVEL_1") != 24 || acc.RoomMaxFloor() != 2 {
		t.Fatalf("after 1F buy: maxFloor=%d size1=%d", acc.RoomMaxFloor(), acc.FloorTileSize("LEVEL_1"))
	}
	// Duplicate buy -> 61009
	if rec := remodelBuyReq(t, "btok", "RULA0001H", 4000); !bytes.Contains(rec.Body.Bytes(), []byte(`"61009"`)) {
		t.Fatalf("duplicate buy = %s", rec.Body)
	}
	// Buy 3F 18x18 (5000 Gems): unlocks 3F, sets LEVEL_3 tileSize=18, balance 3000.
	rec = remodelBuyReq(t, "btok", "RULA024ME", 5000)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"balance":3000}}` {
		t.Fatalf("buy 3F 18x18 = %d %s", rec.Code, rec.Body)
	}
	if acc.RoomMaxFloor() != 3 || acc.FloorTileSize("LEVEL_3") != 18 {
		t.Fatalf("after 3F buy: maxFloor=%d size3=%d", acc.RoomMaxFloor(), acc.FloorTileSize("LEVEL_3"))
	}
	// Insufficient gems for 3F 36x36 (10000 > 3000) -> 60001
	if rec := remodelBuyReq(t, "btok", "RULA024MG", 10000); !bytes.Contains(rec.Body.Bytes(), []byte(`"60001"`)) {
		t.Fatalf("short funds = %s", rec.Body)
	}
	// Verify list reflects owned + inUse on 1F and 3F.
	items1 := remodelListItems(t, "btok", "URMEXT")
	if items1[0]["inUseYn"] != false || items1[2]["existInven"] != true || items1[2]["inUseYn"] != true {
		t.Fatalf("URMEXT after buy = %v", items1)
	}
	items3 := remodelListItems(t, "btok", "URM3F")
	if items3[1]["existInven"] != true || items3[1]["inUseYn"] != true {
		t.Fatalf("URM3F after buy = %v", items3)
	}

	// Apply unowned 1F 36x36 -> result:false
	id36, _ := itemcode.ID("RULA0001G")
	if rec := remodelApplyReq(t, "btok", fmt.Sprint(id36)); rec.Body.String() != `{"result":false}` {
		t.Fatalf("apply unowned = %s", rec.Body)
	}
	// Apply owned starter 1F 12x12 -> result:true, shrinks LEVEL_1 back to 12, drops out-of-bounds chair 1004,
	// keeps in-bounds chair 1003, and relocates out-of-bounds door/diary back into 12x12.
	id12, _ := itemcode.ID("RULA0001I")
	if rec := remodelApplyReq(t, "btok", fmt.Sprint(id12)); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("apply 1F 12x12 = %d %s", rec.Code, rec.Body)
	}
	if acc.FloorTileSize("LEVEL_1") != 12 {
		t.Fatalf("LEVEL_1 tileSize = %d, want 12", acc.FloorTileSize("LEVEL_1"))
	}
	placed := acc.Rooms["LEVEL_1"].Placed
	if len(placed) != 3 {
		t.Fatalf("LEVEL_1 placed after shrink = %+v, want 3 (door, diary, in-bounds chair)", placed)
	}
	for _, p := range placed {
		if p.Seq == 1004 || p.X >= 12 || p.Y >= 12 {
			t.Fatalf("out-of-bounds item survived shrink: %+v", p)
		}
	}
	// Bad apply inputs -> result:false, POST -> 404.
	for _, bad := range []string{"0", "abc", "999999999"} {
		if rec := remodelApplyReq(t, "btok", bad); rec.Body.String() != `{"result":false}` {
			t.Fatalf("apply %q = %s", bad, rec.Body)
		}
	}
	rec = httptest.NewRecorder()
	handleRemodelingApply(rec, httptest.NewRequest(http.MethodPost, "/v4/shop/interior/apply/122300054", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("apply POST status %d, want 404", rec.Code)
	}
}

// TestRemodelCatalogExcludedAndAssetAlias verifies LA remodeling items stay out of the regular Interior Shop and
// ServeRemodelFile serves aliased 2F/3F art from the original 1F yellow-grid folder.
func TestRemodelCatalogExcludedAndAssetAlias(t *testing.T) {
	shopFixture(t, map[string][]string{
		"interior/122300054": {"1409_iteminfo.artsitem", "fl_l0_a00.png"},
		"interior/121400639": {"dp.png", "wall_l_1.png"},
	})
	cat := loadCatalog()
	for _, it := range cat.interior {
		if it.Code == "RULA0001I" {
			t.Fatalf("remodeling item %s leaked into regular interior shop", it.Code)
		}
	}
	// Synthetic 2F starter id (122399201 -> ArtFrom 122300054) answers artsitem and fl_l0_a00.png on both prefixes.
	for _, p := range []string{
		"/img/read/arts_item_interior_122399201/1409_iteminfo.artsitem",
		"/img/read/arts_item_custom_122399201/fl_l0_a00.png",
		"/img/read/arts_item_custom_122300054/fl_l0_a00.png",
	} {
		rec := httptest.NewRecorder()
		if !ServeRemodelFile(rec, httptest.NewRequest(http.MethodGet, p, nil)) || rec.Code != http.StatusOK {
			t.Fatalf("ServeRemodelFile(%s) = %d", p, rec.Code)
		}
	}
	// Original 1F arts_item_interior_ path defers to the normal asset stream (returns false).
	rec := httptest.NewRecorder()
	if ServeRemodelFile(rec, httptest.NewRequest(http.MethodGet, "/img/read/arts_item_interior_122300054/fl_l0_a00.png", nil)) {
		t.Fatalf("original 1F interior path should fall through to asset stream")
	}
}

// TestRemodelDataChecks validates checkRemodel rules on data/remodel.json and rejected inputs.
func TestRemodelDataChecks(t *testing.T) {
	if err := checkRemodel(ActiveRemodel()); err != nil {
		t.Fatalf("embedded data/remodel.json invalid: %v", err)
	}
	ok := RemodelRow{Tab: "URM2F", Code: "RULA024JL", Name: "2F Standard (12x12)", TileSize: 12, RoomSize: 18, Price: 0, ArtFrom: "122300054"}
	good := func(rows ...RemodelRow) *Remodel { return &Remodel{Origin: "mixed", Note: "test", Rows: rows} }
	if err := checkRemodel(good(ok)); err != nil {
		t.Fatalf("valid file refused: %v", err)
	}
	bad := map[string]*Remodel{
		"origin":      {Origin: "", Note: "test", Rows: []RemodelRow{ok}},
		"note":        {Origin: "cherry", Rows: []RemodelRow{ok}},
		"tab":         good(RemodelRow{Tab: "URM9F", Code: "RULA024JL", Name: "x", TileSize: 12, RoomSize: 18}),
		"dupCode":     good(ok, RemodelRow{Tab: "URM3F", Code: "RULA024JL", Name: "y", TileSize: 18, RoomSize: 24}),
		"dupTabSize":  good(ok, RemodelRow{Tab: "URM2F", Code: "RULA024JM", Name: "y", TileSize: 12, RoomSize: 24}),
		"code":        good(RemodelRow{Tab: "URM2F", Code: "CUTO0006W", Name: "x", TileSize: 12, RoomSize: 18}),
		"name":        good(RemodelRow{Tab: "URM2F", Code: "RULA024JL", Name: " ", TileSize: 12, RoomSize: 18}),
		"oddTile":     good(RemodelRow{Tab: "URM2F", Code: "RULA024JL", Name: "x", TileSize: 15, RoomSize: 18}),
		"truncTile38": good(RemodelRow{Tab: "URM2F", Code: "RULA024JL", Name: "x", TileSize: 38, RoomSize: 36}),
		"smallTile":   good(RemodelRow{Tab: "URM2F", Code: "RULA024JL", Name: "x", TileSize: 10, RoomSize: 18}),
		"roomSize":    good(RemodelRow{Tab: "URM2F", Code: "RULA024JL", Name: "x", TileSize: 12, RoomSize: 0}),
		"negPrice":    good(RemodelRow{Tab: "URM2F", Code: "RULA024JL", Name: "x", TileSize: 12, RoomSize: 18, Price: -1}),
		"currency":    good(RemodelRow{Tab: "URM2F", Code: "RULA024JL", Name: "x", TileSize: 12, RoomSize: 18, Currency: "GOLD"}),
		"artFrom":     good(RemodelRow{Tab: "URM2F", Code: "RULA024JL", Name: "x", TileSize: 12, RoomSize: 18, ArtFrom: "abc"}),
	}
	for name, m := range bad {
		if err := checkRemodel(m); err == nil {
			t.Fatalf("%s: refused nothing", name)
		}
	}
}
