package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

// shopSetup installs a non-lab account and a small fake item root:
// CUTO00001 top, CUON004TV animated one-piece, CUHA00001 hair, RUWA000HR wall, RUCH00001 chair,
// RUGO00002 animated furniture, pets PUPE0000G dog, PUPE0000H cat, PUPE0000I toy, PUPE0006E animated (ride).
func shopSetup(t *testing.T, gems, cash int64) *store.Account {
	t.Helper()
	root, _ := thumbFixture(t)
	for _, f := range [][3]string{
		{"dress", "225000001", "a.png"}, {"dress", "225106259", "a.aniproj"}, {"custom", "224400001", "a.png"},
		{"interior", "121400639", "a.png"}, {"interior", "120400001", "a.png"}, {"interior", "122000002", "a.aniproj"},
		{"pet", "326400016", "skeleton.xml"}, {"pet", "326400230", "skeleton.xml"}, {"pet", "326400230", "r.aniproj"},
		{"pet", "326400017", "skeleton.xml"}, {"pet", "326400018", "skeleton.xml"},
	} {
		writeThumbItem(t, root, f[0], f[1], f[2], []byte("x"))
		writeThumbItem(t, root, f[0], f[1], "dp.png", []byte("x")) // art marker: no-art items are not sold or pooled
	}
	// species sound decides dog (215) or cat (214); no sound = toy
	writeThumbItem(t, root, "pet", "326400016", "behavior.xml", []byte(`{"type": "action_sound", "sndindex": 215}`))
	writeThumbItem(t, root, "pet", "326400017", "behavior.xml", []byte(`{"type": "action_sound",   "sndindex":214}`))
	acc := &store.Account{Aid: "9501", Name: "Shopper", Gender: "FEMALE", Gems: gems, Cash: cash,
		ItemCodes: []string{"CUHA00001"}, InventoryCodes: []string{"CUHA00001"}}
	installSocialTestAccounts(t, map[string]*store.Account{"stok": acc})
	testutil.SetLab(t, "9501", false)
	return acc
}

func shopCall(t *testing.T, method, target, body string) (int, map[string]any) {
	t.Helper()
	rec := decorReq(t, "stok", method, target, body)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s = %d, bad JSON %q", method, target, rec.Code, rec.Body.String())
	}
	return rec.Code, out
}

func shopItems(t *testing.T, target string) []any {
	t.Helper()
	code, out := shopCall(t, http.MethodGet, target, "")
	items, ok := out["result"].(map[string]any)["items"].([]any)
	if code != 200 || !ok || items == nil {
		t.Fatalf("GET %s = %d %v, want items array", target, code, out)
	}
	return items
}

func codesOf(items []any) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.(map[string]any)["itemCode"].(string))
	}
	return out
}

func wantErr(t *testing.T, code int, out map[string]any, status int, errCode string) {
	t.Helper()
	if code != status || out["errorCode"] != errCode {
		t.Fatalf("got %d %v, want %d errorCode %s", code, out, status, errCode)
	}
}

func TestShopCatalogShapes(t *testing.T) {
	shopSetup(t, 0, 0)
	if got := codesOf(shopItems(t, "/v4/shop/fashion/itemlist/MDHAIR?isFull=false&paymentType=ALL")); !slices.Equal(got, []string{"CUHA00001"}) {
		t.Errorf("MDHAIR = %v", got)
	}
	items := shopItems(t, "/v4/shop/fashion/itemlist/FDRESS")
	row := items[0].(map[string]any)
	// The only one-piece is a daily deal (every clothing item of this fixture is one: 3 candidates).
	if len(items) != 1 || row["itemCode"] != "CUON004TV" || row["price"] != 625.0 || row["discountedPrice"] != 438.0 ||
		row["paymentType"] != "GEM" || row["sellingIconType"] != "SALE" || row["specialEffects"] != "ANIMATION" ||
		row["discount"] != true || row["sellingEnd"] == "" || row["discountEnd"] != row["sellingEnd"] ||
		row["selling"] != true || row["display"] != true || row["soldout"] != false {
		t.Errorf("dress row = %v", row)
	}
	if _, ok := row["itemID"]; !ok {
		t.Error("fashion row lacks itemID")
	}
	for _, cat := range []string{"SET", "all", "APTRN", "ADRESS", "BOGUS", "UNKNOWN"} {
		if got := shopItems(t, "/v4/shop/fashion/itemlist/"+cat); len(got) != 0 {
			t.Errorf("%s = %v, want empty array", cat, got)
		}
	}
	if got := codesOf(shopItems(t, "/v4/shop/interior/itemlist/UWLPP")); !slices.Equal(got, []string{"RUWA000HR"}) {
		t.Errorf("UWLPP = %v", got)
	}
	if got := codesOf(shopItems(t, "/v4/shop/interior/itemlist/UCHAIR")); !slices.Equal(got, []string{"RUCH00001"}) {
		t.Errorf("UCHAIR = %v", got)
	}
	if got := shopItems(t, "/v4/shop/interior/itemlist/UBOGUS"); len(got) != 0 {
		t.Errorf("UBOGUS = %v", got)
	}
	pets := shopItems(t, "/v4/shop/interior/petitemlist/UDOGINT")
	if got := codesOf(pets); !slices.Equal(got, []string{"PUPE0000G", "PUPE0000I"}) { // dog + toy
		t.Fatalf("UDOGINT = %v", got)
	}
	for i, want := range []string{"UDOG", "UTOY"} {
		if got := pets[i].(map[string]any)["catgCd"]; got != want {
			t.Errorf("catgCd[%d] = %v, want %s", i, got, want)
		}
	}
	if got := codesOf(shopItems(t, "/v4/shop/interior/petitemlist/UPETINT")); len(got) != 4 {
		t.Errorf("UPETINT = %v, want all four pets", got)
	}
	ride := shopItems(t, "/v4/shop/interior/petitemlist/URIDEINT")
	prow := ride[0].(map[string]any)
	if len(ride) != 1 || prow["itemCode"] != "PUPE0006E" || prow["paymentType"] != "CASH" || prow["price"] != 25.0 || prow["purchasedYn"] != false {
		t.Errorf("ride row = %v", prow)
	}
	cats := shopItems(t, "/v4/shop/interior/petitemlist/UCATINT")
	if len(cats) != 1 || cats[0].(map[string]any)["itemCode"] != "PUPE0000H" || cats[0].(map[string]any)["catgCd"] != "UCAT" {
		t.Errorf("UCATINT = %v", cats)
	}
	if prow["catgCd"] != "URIDE" {
		t.Errorf("ride catgCd = %v", prow["catgCd"])
	}
	for _, p := range []string{"fashion", "fashion/SET", "interior", "pet"} {
		if code, out := shopCall(t, http.MethodPost, "/v4/shop/"+p+"/cart/", "[]\x00"); code != 200 || out["result"].(map[string]any)["items"] == nil {
			t.Errorf("cart %s = %d %v", p, code, out)
		}
	}
	if code, out := shopCall(t, http.MethodPost, "/v4/shop/status", "[]"); code != 200 || len(out["result"].([]any)) != 0 {
		t.Errorf("status = %d %v", code, out)
	}
	if code, out := shopCall(t, http.MethodGet, "/v4/items/modified/list/1459004400000", ""); code != 200 ||
		out["result"].(map[string]any)["lastTimestamp"] == "" || len(out["result"].(map[string]any)["modifiedItemList"].([]any)) != 0 {
		t.Errorf("modified = %d %v", code, out)
	}
}

func buyDress(price int, code string) string {
	return `{"deviceType":"Android","itemType":"DRESS","coinUnit":"GEM","items":[{"itemCode":"` + code + `","price":` + strconv.Itoa(price) + `}]}` + "\x00"
}

func TestDressBuy(t *testing.T) {
	acc := shopSetup(t, 400, 0)
	code, out := shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(100, "CUTO00001"))
	wantErr(t, code, out, 400, "61003") // client price differs from the table
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(150, "CUFAKE000"))
	wantErr(t, code, out, 400, "61001")
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(438, "CUON004TV"))
	wantErr(t, code, out, 400, "71001") // the deal price, but 400 gems < 438
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(625, "CUON004TV"))
	wantErr(t, code, out, 400, "61003") // full price on a daily deal
	if acc.Gems != 400 {
		t.Fatalf("failed buys changed gems to %d", acc.Gems)
	}
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(150, "CUTO00001"))
	wantErr(t, code, out, 400, "61003") // full price on a daily deal (tops deal: 105)
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(105, "CUTO00001"))
	res, _ := out["result"].(map[string]any)
	if code != 200 || res["balance"] != 295.0 || res["resultCode"] != 0.0 || !slices.Equal(codesFromAny(res["buyItems"]), []string{"CUTO00001"}) {
		t.Fatalf("buy = %d %v", code, out)
	}
	if acc.Gems != 295 || !slices.Contains(acc.InventoryCodes, "CUTO00001") {
		t.Fatalf("gems %d inventory %v", acc.Gems, acc.InventoryCodes)
	}
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(105, "CUTO00001"))
	wantErr(t, code, out, 400, "61009")
	// The immediate avatar/save/v2 with the new item equipped must pass ownership.
	if rec := decorReq(t, "stok", http.MethodPut, "/v4/avatar/save/v2", `[{"itemCode":"CUTO00001","invenSeq":1},{"itemCode":"CUHA00001","invenSeq":2}]`+"\x00"); rec.Code != 200 {
		t.Fatalf("save/v2 = %d %s", rec.Code, rec.Body.String())
	}
	// Closet rows carry the grade, the table price of a plain item and the paid price of a deal.
	rec := decorReq(t, "stok", http.MethodPost, "/v4/inven/closet/items/all", "{}")
	var inv struct {
		Result struct {
			InventoryList []struct {
				ItemCode string
				Price    int
				Grade    string
			}
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	for _, r := range inv.Result.InventoryList {
		if r.Grade != "N" || r.ItemCode == "CUHA00001" && r.Price != 200 || r.ItemCode == "CUTO00001" && r.Price != 105 {
			t.Errorf("closet row %+v, want grade N, the hair price 200 and the deal top's paid 105", r)
		}
	}
	if len(inv.Result.InventoryList) != 2 {
		t.Errorf("closet rows = %v", inv.Result.InventoryList)
	}
}

func codesFromAny(v any) []string {
	var out []string
	for _, c := range v.([]any) {
		out = append(out, c.(string))
	}
	return out
}

func TestInteriorAndPetBuy(t *testing.T) {
	acc := shopSetup(t, 1000, 30)
	// RUCH00001 (150) and RUGO00002 (375) are today's furniture deals: 105 and 263 (ceil 70%).
	body := `{"itemType":"ROOM","items":[{"itemCode":"RUCH00001","price":105,"cpId":"avtplay"},{"itemCode":"RUCH00001","price":105}]}` + "\x00"
	code, out := shopCall(t, http.MethodPost, "/v4/purchase/interior/buy", body)
	res, _ := out["result"].(map[string]any)
	if code != 200 || res["balance"] != 790.0 || len(res) != 1 {
		t.Fatalf("interior buy = %d %v", code, out)
	}
	n := 0
	for _, it := range acc.RoomItems {
		if it.Cd == "RUCH00001" {
			n++
		}
	}
	if n != 2 || acc.Gems != 790 {
		t.Fatalf("copies %d gems %d", n, acc.Gems)
	}
	if got := shopItems(t, "/v4/shop/interior/itemlist/UCHAIR")[0].(map[string]any); got["existInven"] != true {
		t.Errorf("owned row = %v", got)
	}
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/interior/buy", `{"items":[{"itemCode":"RUGO00002","price":100}]}`)
	wantErr(t, code, out, 400, "61003")
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/interior/buy", `{"items":[{"itemCode":"RUGO00002","price":375}]}`)
	wantErr(t, code, out, 400, "61003") // full price on a deal
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/interior/buy", `{"items":[{"itemCode":"RUGO00002","price":263}]}`)
	if code != 200 || acc.Gems != 527 {
		t.Fatalf("animated furniture = %d %v gems %d", code, out, acc.Gems)
	}
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/interior/buy", `{"items":[{"itemCode":"RUGO00002","price":263},{"itemCode":"RUGO00002","price":263},{"itemCode":"RUGO00002","price":263}]}`)
	wantErr(t, code, out, 400, "60001")

	// Animated pet: 25 Cash, balance is the Cash balance.
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/pet/buy", `{"items":[{"itemCode":"PUPE0006E","price":25}],"giveNo":"5"}`)
	wantErr(t, code, out, 400, "61001")
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/interior/buy", `{"items":[{"itemCode":"PUPE0006E","price":25}],"giveNo":""}`)
	res, _ = out["result"].(map[string]any)
	if code != 200 || res["balance"] != 5.0 || res["resultCode"] != 0.0 || acc.Cash != 5 || acc.Gems != 527 || len(acc.Pets) != 1 || acc.Pets[0].Cd != "PUPE0006E" || acc.Pets[0].ID != 950101 {
		t.Fatalf("pet buy = %d %v cash %d gems %d pets %v", code, out, acc.Cash, acc.Gems, acc.Pets)
	}
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/pet/buy", `{"items":[{"itemCode":"PUPE0006E","price":25}]}`)
	wantErr(t, code, out, 400, "61009")
	// Plain pet: 1,200 Gems, not enough.
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/pet/buy", `{"items":[{"itemCode":"PUPE0000G","price":1200}]}`)
	wantErr(t, code, out, 400, "60001")
	if got := shopItems(t, "/v4/shop/interior/petitemlist/UPETINT"); !slices.ContainsFunc(got, func(v any) bool {
		r := v.(map[string]any)
		return r["itemCode"] == "PUPE0006E" && r["purchasedYn"] == true
	}) {
		t.Errorf("owned pet not flagged: %v", got)
	}
}

func TestRecycleCfgAndSellBack(t *testing.T) {
	acc := shopSetup(t, 1000, 0)
	code, out := shopCall(t, http.MethodGet, "/v4/inven/recycle/cfg", "")
	res := out["result"].(map[string]any)
	cfg := res["config"].([]any)
	if code != 200 || len(cfg) != 21 || res["recycleMultiple"] != 1.0 || res["vipGrade"] != "NONE" {
		t.Fatalf("cfg = %d %v", code, out)
	}
	for _, r := range cfg {
		if r.(map[string]any)["rate"] != 100.0 {
			t.Fatalf("rate row %v", r)
		}
	}

	shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(105, "CUTO00001")) // tops deal
	shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(438, "CUON004TV")) // one-piece deal
	if acc.Gems != 457 {
		t.Fatalf("gems %d, want 457", acc.Gems)
	}
	sell := func(items string) (int, map[string]any) {
		return shopCall(t, http.MethodPost, "/v4/inven/recycle/closet/v2",
			`{"usePurchasePrice":true,"appliedRecycleMultiple":1.0,"recycleItems":[`+items+`]}`+"\x00")
	}
	row := func(seq int, code, price string) string {
		return `{"invenSeq":` + strconv.Itoa(seq) + `,"itemCode":"` + code + `","count":1,"exepectedPrice":"` + price + `","price":"` + price + `"}`
	}
	// Worn (hair) and unowned items are refused; nothing changes.
	code, out = sell(row(1, "CUHA00001", "200"))
	wantErr(t, code, out, 400, "50024")
	code, out = sell(row(1, "CUSH00001", "120"))
	wantErr(t, code, out, 400, "50001")
	// The client's expected price is ignored: payout is the price paid at the deal (105 + 438).
	code, out = sell(row(2, "CUTO00001", "999999") + "," + row(3, "CUON004TV", "1"))
	res = out["result"].(map[string]any)
	if _, has := res["config"]; has || code != 200 || res["balance"] != 1000.0 || len(res["invalidItems"].([]any)) != 0 {
		t.Fatalf("sell = %d %v", code, out)
	}
	if acc.Gems != 1000 || slices.Contains(acc.InventoryCodes, "CUTO00001") || !slices.Contains(acc.InventoryCodes, "CUHA00001") {
		t.Fatalf("gems %d inventory %v", acc.Gems, acc.InventoryCodes)
	}

	// Interior: a placed item is refused, an unplaced one sells by seq and code.
	shopCall(t, http.MethodPost, "/v4/purchase/interior/buy", `{"items":[{"itemCode":"RUCH00001","price":105}]}`)
	rec := decorReq(t, "stok", http.MethodGet, "/v4/inven/interior/items/all", "")
	var inv struct {
		Result []struct {
			Seq, Cd, Grade string
			Price          int
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	var chair, wall string
	for _, r := range inv.Result {
		switch r.Cd {
		case "RUCH00001":
			chair = r.Seq
			if r.Grade != "N" || r.Price != 105 { // bought at the deal price
				t.Errorf("chair row %+v", r)
			}
		case "RUWA000HR":
			wall = r.Seq
		}
	}
	irow := func(seq, code string) string {
		return `{"invenSeq":` + seq + `,"itemCode":"` + code + `","count":1,"exepectedPrice":"100","price":"100"}`
	}
	sellRoom := func(items string) (int, map[string]any) {
		return shopCall(t, http.MethodPost, "/v4/inven/recycle/interior", `{"recycleItems":[`+items+`]}`+"\x00")
	}
	code, out = sellRoom(irow(wall, "RUWA000HR")) // the starter wall is placed
	wantErr(t, code, out, 400, "50024")
	code, out = sellRoom(irow(chair, "RUWA000HR")) // seq/code mismatch
	wantErr(t, code, out, 400, "50001")
	// The stock client's ReqRecycleInterior row: invenSeq (number), exepectedPrice and price only.
	code, out = sellRoom(`{"invenSeq":` + chair + `,"exepectedPrice":"150","price":150}`)
	res = out["result"].(map[string]any)
	if _, has := res["config"]; has || code != 200 || res["balance"] != 1000.0 || acc.Gems != 1000 {
		t.Fatalf("room sell = %d %v gems %d", code, out, acc.Gems)
	}
	for _, it := range acc.RoomItems {
		if it.Cd == "RUCH00001" {
			t.Fatalf("chair still owned: %v", acc.RoomItems)
		}
	}

	// An unpriced face is kept and reported back in invalidItems; the priced item still sells.
	acc.InventoryCodes = append(acc.InventoryCodes, "CUEY00002", "CUTO00001")
	code, out = sell(row(4, "CUEY00002", "0") + "," + row(5, "CUTO00001", "150"))
	res = out["result"].(map[string]any)
	inv2 := res["invalidItems"].([]any)
	if code != 200 || len(inv2) != 1 || inv2[0].(map[string]any)["itemCode"] != "CUEY00002" || acc.Gems != 1150 ||
		!slices.Contains(acc.InventoryCodes, "CUEY00002") || slices.Contains(acc.InventoryCodes, "CUTO00001") {
		t.Fatalf("face sell = %d %v gems %d inv %v", code, out, acc.Gems, acc.InventoryCodes)
	}

	// A sold starter item is not re-granted on the next room load (sell-back loop).
	layout := acc.Rooms["LEVEL_1"]
	layout.Wall = store.RoomItem{}
	acc.Rooms = map[string]store.RoomLayout{"LEVEL_1": layout}
	if code, out = sellRoom(irow(wall, "RUWA000HR")); code != 200 {
		t.Fatalf("unplaced wall sell = %d %v", code, out)
	}
	decorReq(t, "stok", http.MethodGet, "/v4/inven/interior/items/all", "")
	for _, it := range acc.RoomItems {
		if it.Cd == "RUWA000HR" {
			t.Fatalf("starter wall re-granted: %v", acc.RoomItems)
		}
	}
}

// saleClock pins the shop clock to a JST wall time for the test.
func saleClock(t *testing.T, y int, m time.Month, d, h, min int) {
	t.Helper()
	old := economy.NowFunc
	jst := time.FixedZone("JST", 9*3600)
	economy.NowFunc = func() time.Time { return time.Date(y, m, d, h, min, 0, 0, jst) }
	t.Cleanup(func() { economy.NowFunc = old })
}

// TestSaleFlagsOnRoutes: limited items are listed only in their season's game dates (winter on Jan 3,
// while the New Year calendar runs), with LIMIT and the window's end; deals carry SALE.
func TestSaleFlagsOnRoutes(t *testing.T) {
	shopSetup(t, 0, 0)
	root := thumbs.DpItemRoot
	writeThumbItem(t, root, "dress", "225106219", "dp.png", []byte("x"))    // CUON004SR: Halloween
	writeThumbItem(t, root, "dress", "225303853", "dp.png", []byte("x"))    // CUSH002Z1: winter
	writeThumbItem(t, root, "interior", "120406168", "dp.png", []byte("x")) // RUCH004RC: winter

	saleClock(t, 2026, time.October, 22, 12, 0)
	if got := codesOf(shopItems(t, "/v4/shop/fashion/itemlist/MDRESS")); !slices.Equal(got, []string{"CUON004TV"}) {
		t.Errorf("Oct 22 MDRESS = %v, want the one-piece only", got)
	}
	tops := shopItems(t, "/v4/shop/fashion/itemlist/MTOPS")
	if row := tops[0].(map[string]any); row["discount"] != true || row["sellingIconType"] != "SALE" {
		t.Errorf("tops row = %v, want a deal", row)
	}

	saleClock(t, 2026, time.October, 26, 12, 0)
	dress := shopItems(t, "/v4/shop/fashion/itemlist/MDRESS")
	if got := codesOf(dress); !slices.Equal(got, []string{"CUON004SR", "CUON004TV"}) {
		t.Fatalf("Oct 26 MDRESS = %v", got)
	}
	if row := dress[0].(map[string]any); row["sellingIconType"] != "LIMIT" || row["discount"] != false || row["discountEnd"] != "" ||
		row["sellingEnd"] != strconv.FormatInt(time.Date(2026, time.November, 8, 6, 0, 0, 0, time.FixedZone("JST", 9*3600)).UnixMilli(), 10) {
		t.Errorf("limited row = %v", row)
	}

	saleClock(t, 2026, time.November, 8, 6, 0)
	if got := codesOf(shopItems(t, "/v4/shop/fashion/itemlist/MDRESS")); !slices.Equal(got, []string{"CUON004TV"}) {
		t.Errorf("Nov 8 06:00 MDRESS = %v, want the window closed", got)
	}

	saleClock(t, 2027, time.January, 3, 12, 0)
	if got := codesOf(shopItems(t, "/v4/shop/interior/itemlist/UCHAIR")); !slices.Contains(got, "RUCH004RC") {
		t.Errorf("Jan 3 UCHAIR = %v, want the winter chair", got)
	}
	if got := codesOf(shopItems(t, "/v4/shop/fashion/itemlist/MSHOE")); !slices.Equal(got, []string{"CUSH002Z1"}) {
		t.Errorf("Jan 3 MSHOE = %v, want the winter shoes", got)
	}
}

// TestDealAndLimitedSellBackRoutes: a deal sells back at the price paid; a limited item bought at its
// table price sells back at the table price.
func TestDealAndLimitedSellBackRoutes(t *testing.T) {
	acc := shopSetup(t, 400, 0)
	writeThumbItem(t, thumbs.DpItemRoot, "dress", "225303853", "dp.png", []byte("x")) // CUSH002Z1: winter
	saleClock(t, 2027, time.January, 3, 12, 0)

	code, out := shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(120, "CUSH002Z1"))
	if code != 200 || acc.Gems != 280 {
		t.Fatalf("limited buy = %d %v gems %d", code, out, acc.Gems)
	}
	code, out = shopCall(t, http.MethodPost, "/v4/purchase/dress/buy", buyDress(105, "CUTO00001"))
	if code != 200 || acc.Gems != 175 || acc.PaidPrices["CUTO00001"] != 105 {
		t.Fatalf("deal buy = %d %v gems %d paid %v", code, out, acc.Gems, acc.PaidPrices)
	}
	sell := func(seq int, code string, price int) (int, map[string]any) {
		return shopCall(t, http.MethodPost, "/v4/inven/recycle/closet/v2",
			`{"recycleItems":[{"invenSeq":`+strconv.Itoa(seq)+`,"itemCode":"`+code+`","count":1,"exepectedPrice":"`+strconv.Itoa(price)+`","price":"`+strconv.Itoa(price)+`"}]}`+"\x00")
	}
	code, out = sell(2, "CUSH002Z1", 120)
	if code != 200 || out["result"].(map[string]any)["balance"] != 295.0 {
		t.Fatalf("limited sell-back = %d %v", code, out)
	}
	code, out = sell(3, "CUTO00001", 105)
	if code != 200 || out["result"].(map[string]any)["balance"] != 400.0 || acc.Gems != 400 {
		t.Fatalf("deal sell-back = %d %v gems %d", code, out, acc.Gems)
	}
	if _, kept := acc.PaidPrices["CUTO00001"]; kept {
		t.Errorf("paid record of the sold deal kept: %v", acc.PaidPrices)
	}
}
