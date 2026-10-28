package economy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/itemcode"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// modelHouseFixture gives every element of the default sets an interior folder with a dp.png in a temp item
// root, so each element is an Interior Shop item with art. The operator file is reset when the test ends.
func modelHouseFixture(t *testing.T) {
	t.Helper()
	files := map[string][]string{}
	for _, s := range modelHouseFile.Get().Sets {
		for _, code := range s.Elements {
			id, ok := itemcode.ID(code)
			if !ok {
				t.Fatalf("element %s is not a canonical item code", code)
			}
			files[fmt.Sprintf("interior/%d", id)] = []string{"dp.png"}
		}
	}
	shopFixture(t, files)
	t.Cleanup(modelHouseFile.Reset)
}

// loadModelHouseJSON writes an operator file and loads it, returning the LoadFrom error.
func loadModelHouseJSON(t *testing.T, body string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "modelhouse.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return modelHouseFile.LoadFrom(path)
}

// mhRow and mhElement are the list's typed shape: decoding into these types is the JSON type check.
type mhElement struct {
	ItemID          int64  `json:"itemId"`
	ItemCode        string `json:"itemCode"`
	Price           int64  `json:"price"`
	DiscountedPrice int64  `json:"discountedPrice"`
	SellingIconType string `json:"sellingIconType"`
	ItemType        string `json:"itemType"`
	Grade           string `json:"grade"`
	Count           int64  `json:"count"`
	ExistInven      bool   `json:"existInven"`
	Discount        bool   `json:"discount"`
	Selling         bool   `json:"selling"`
	Soldout         bool   `json:"soldout"`
	Display         bool   `json:"display"`
	SpecialEffects  string `json:"specialEffects"`
}

type mhRow struct {
	ItemID            int64       `json:"itemId"`
	ItemCode          string      `json:"itemCode"`
	Name              string      `json:"name"`
	Description       string      `json:"description"`
	ItemType          string      `json:"itemType"`
	Price             int64       `json:"price"`
	DiscountedPrice   int64       `json:"discountedPrice"`
	Discount          bool        `json:"discount"`
	SellingIconType   string      `json:"sellingIconType"`
	SpecialEffects    string      `json:"specialEffects"`
	PaymentType       string      `json:"paymentType"`
	SellingStart      string      `json:"sellingStart"`
	SellingEnd        string      `json:"sellingEnd"`
	DiscountStart     string      `json:"discountStart"`
	DiscountEnd       string      `json:"discountEnd"`
	Selling           bool        `json:"selling"`
	Soldout           bool        `json:"soldout"`
	Display           bool        `json:"display"`
	ExistInven        bool        `json:"existInven"`
	Purchased         bool        `json:"purchased"`
	RepresentImageURL string      `json:"representImageUrl"`
	ThumbnailImageURL string      `json:"thumbnailImageUrl"`
	ElementItemCodes  []string    `json:"elementItemCodes"`
	ElementItems      []mhElement `json:"elementItems"`
}

// decodeModelHouseRows reads {"result":{"items":[...]}} into typed rows.
func decodeModelHouseRows(t *testing.T, rec *httptest.ResponseRecorder) []mhRow {
	t.Helper()
	var out struct {
		Result struct {
			Items []mhRow `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("list body %q: %v", rec.Body.String(), err)
	}
	return out.Result.Items
}

func TestModelHouseListShape(t *testing.T) {
	modelHouseFixture(t)
	rec := httptest.NewRecorder()
	handleModelHouseList(rec, httptest.NewRequest(http.MethodGet, "/v4/shop/interior/setitems/list?isFull=true", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d %s", rec.Code, rec.Body.String())
	}
	rows := decodeModelHouseRows(t, rec)
	sets := modelHouseFile.Get().Sets
	if len(rows) != len(sets) {
		t.Fatalf("rows = %d, want %d", len(rows), len(sets))
	}
	for i, row := range rows {
		s := sets[i]
		if row.ItemCode != s.Code || row.Name != s.Name || row.Description != s.Description || row.ItemType != "ROOM" {
			t.Errorf("row %d = %+v, want set %s", i, row, s.Code)
		}
		if row.Price != s.Price || row.DiscountedPrice != s.Price || row.PaymentType != "GEM" || row.Discount {
			t.Errorf("row %d price %d/%d %s discount=%v, want %d Gems", i, row.Price, row.DiscountedPrice, row.PaymentType, row.Discount, s.Price)
		}
		if !slices.Equal(row.ElementItemCodes, s.Elements) || len(row.ElementItems) != len(s.Elements) {
			t.Fatalf("row %d elements %v / %d, want %v", i, row.ElementItemCodes, len(row.ElementItems), s.Elements)
		}
		for j, el := range row.ElementItems {
			want, _, _, _ := ItemPrice(s.Elements[j])
			if el.ItemCode != s.Elements[j] || el.Price != want || el.DiscountedPrice != want || el.ItemType != "ROOM" ||
				el.Grade != "N" || el.Count != 1 || el.ExistInven || !el.Selling || el.ItemID == 0 {
				t.Errorf("row %d element %d = %+v, want %s at %d Gems", i, j, el, s.Elements[j], want)
			}
		}
		if !strings.HasPrefix(row.RepresentImageURL, "https://example.com/arts_item_interior_") || row.ThumbnailImageURL != row.RepresentImageURL {
			t.Errorf("row %d images %q / %q: want the first element's dp.png on the request host", i, row.RepresentImageURL, row.ThumbnailImageURL)
		}
		if row.ExistInven || row.Purchased {
			t.Errorf("row %d owned without an account", i)
		}
	}
}

// TestModelHouseListNeverEmpty: with no set on sale the list answers 404, never an empty items array (the client
// crashes on an empty one). A file with no sets is valid and gives the same 404; a refused file keeps the old list.
func TestModelHouseListNeverEmpty(t *testing.T) {
	modelHouseFixture(t)
	if err := loadModelHouseJSON(t, `{"origin":"cherry","note":"x","sets":[]}`); err != nil {
		t.Fatalf("an empty set list must load: %v", err)
	}
	rec := httptest.NewRecorder()
	handleModelHouseList(rec, httptest.NewRequest(http.MethodGet, "/v4/shop/interior/setitems/list?isFull=true", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("empty file: status %d %s, want 404", rec.Code, rec.Body.String())
	}

	modelHouseFile.Reset()
	if err := loadModelHouseJSON(t, `{"origin":"cherry","note":"x","sets":[{"code":"GUMH00001","name":"n","description":"d","elements":["RUCH0011X"],"price":0}]}`); err == nil {
		t.Fatal("a zero price must refuse the file")
	}
	rec = httptest.NewRecorder()
	handleModelHouseList(rec, httptest.NewRequest(http.MethodGet, "/v4/shop/interior/setitems/list?isFull=true", nil))
	if rec.Code != http.StatusOK || len(decodeModelHouseRows(t, rec)) != len(modelHouseFile.Get().Sets) {
		t.Fatalf("refused file: status %d %s, want the default list", rec.Code, rec.Body.String())
	}
}

// TestModelHouseNoArtNoList: the default sets with no item art in the archive give 404 at request time too.
func TestModelHouseNoArtNoList(t *testing.T) {
	shopFixture(t, map[string][]string{})
	t.Cleanup(modelHouseFile.Reset)
	rec := httptest.NewRecorder()
	handleModelHouseList(rec, httptest.NewRequest(http.MethodGet, "/v4/shop/interior/setitems/list", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no sellable set: status %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// TestModelHouseLoadRefuses: each broken operator file is refused at load with the reason.
func TestModelHouseLoadRefuses(t *testing.T) {
	modelHouseFixture(t)
	set := func(code, element string, price string) string {
		return fmt.Sprintf(`{"code":%q,"name":"n","description":"d","elements":[%q],"price":%s}`, code, element, price)
	}
	wrap := func(sets ...string) string {
		return `{"origin":"cherry","note":"x","sets":[` + strings.Join(sets, ",") + `]}`
	}
	cases := map[string]string{
		"origin":         `{"origin":"ops","note":"x","sets":[]}`,
		"no note":        `{"origin":"cherry","note":"","sets":[]}`,
		"prefix":         wrap(set("RUCH0011X", "RUCH0011X", "10")),
		"item code":      wrap(set("GULS00001", "RUCH0011X", "10")),
		"duplicate code": wrap(set("GUMH00001", "RUCH0011X", "10"), set("GUMH00001", "RUTB000GB", "10")),
		"zero price":     wrap(set("GUMH00001", "RUCH0011X", "0")),
		"not room":       wrap(set("GUMH00001", "CUHA0036Z", "10")),
		"no art":         wrap(set("GUMH00001", "RUCH0099Z", "10")),
		"unknown key":    `{"origin":"cherry","note":"x","sets":[],"extra":1}`,
		"element twice":  wrap(`{"code":"GUMH00001","name":"n","description":"d","elements":["RUCH0011X","RUCH0011X"],"price":10}`),
	}
	for name, body := range cases {
		modelHouseFile.Reset()
		if err := loadModelHouseJSON(t, body); err == nil {
			t.Errorf("%s: loaded, want a refusal", name)
		}
	}
}

// TestModelHouseDetail: the detail takes the list index the client sends, or a set code; an unknown one is 404.
func TestModelHouseDetail(t *testing.T) {
	modelHouseFixture(t)
	get := func(key string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handleModelHouseDetail(rec, httptest.NewRequest(http.MethodGet, "/v4/shop/interior/setitems/detail?setItemCode="+key+"&isFull=true", nil))
		return rec
	}
	rec := get("2")
	var out struct {
		Result mhRow `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Result.ItemCode != "GUMH00003" {
		t.Fatalf("detail index 2: status %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("GUMH00004"); rec.Code != http.StatusOK {
		t.Fatalf("detail by code: status %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("9"); rec.Code != http.StatusNotFound {
		t.Fatalf("detail index 9: status %d, want 404", rec.Code)
	}
}

// modelHouseBody is the purchase/set/buy body the client sends for a set (price and items as the popup holds them).
func modelHouseBody(code string, price int64, coin string, elements []string) string {
	items := make([]string, 0, len(elements))
	for _, e := range elements {
		items = append(items, fmt.Sprintf(`{"itemCode":%q,"price":150}`, e))
	}
	return fmt.Sprintf(`{"deviceType":"Android","isSetitem":"1","itemType":"ROOM","language":"ja","price":%d,`+
		`"setitemCode":%q,"arrangement":"false","cpId":"avtplay","brandShopId":"","brandShopNo":"","coinUnit":%q,`+
		`"items":[%s]}`, price, code, coin, strings.Join(items, ","))
}

// TestModelHouseBuy: the happy path spends the set's Gems through the ledger and grants every element.
func TestModelHouseBuy(t *testing.T) {
	modelHouseFixture(t)
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	s := modelHouseFile.Get().Sets[0]
	acc, _ := phaseEAccount(t, 1000, nil)
	before := len(acc.RoomItems)

	rec := postAs(handleModelHouseBuy, modelHouseBody(s.Code, s.Price, "GEM", s.Elements))
	if rec.Code != http.StatusOK {
		t.Fatalf("buy: status %d %s", rec.Code, rec.Body.String())
	}
	if acc.Gems != 1000-s.Price {
		t.Errorf("gems = %d, want %d", acc.Gems, 1000-s.Price)
	}
	if got := len(acc.RoomItems) - before; got != len(s.Elements) {
		t.Fatalf("granted %d room items, want %d", got, len(s.Elements))
	}
	for i, code := range s.Elements {
		if acc.RoomItems[before+i].Cd != code {
			t.Errorf("granted %s at %d, want %s", acc.RoomItems[before+i].Cd, i, code)
		}
	}
	var out struct {
		Result struct {
			Balance    int64    `json:"balance"`
			BuyItems   []string `json:"buyItems"`
			ResultCode int64    `json:"resultCode"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Result.Balance != 1000-s.Price || !slices.Equal(out.Result.BuyItems, s.Elements) {
		t.Errorf("buy reply %q", rec.Body.String())
	}
	ledger, err := os.ReadFile(filepath.Join(filepath.Dir(store.AccountStorePath), "ledger.jsonl"))
	if err != nil || !strings.Contains(string(ledger), "modelhouse:set:"+s.Code) {
		t.Errorf("ledger = %q (%v), want a line for %s", ledger, err, s.Code)
	}
}

// TestModelHouseBuyRefusals: a wrong price, Cash, an element list that differs and too few Gems are refused
// with nothing spent and nothing granted. Owned elements are granted again (the Interior Shop rule).
func TestModelHouseBuyRefusals(t *testing.T) {
	modelHouseFixture(t)
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	s := modelHouseFile.Get().Sets[1]

	acc, _ := phaseEAccount(t, 1000, nil)
	before := len(acc.RoomItems)
	for name, body := range map[string]string{
		"wrong price": modelHouseBody(s.Code, s.Price-1, "GEM", s.Elements),
		"cash":        modelHouseBody(s.Code, s.Price, "CASH", s.Elements),
		"elements":    modelHouseBody(s.Code, s.Price, "GEM", s.Elements[:1]),
		"unknown set": modelHouseBody("GUMH0ZZZZ", s.Price, "GEM", s.Elements),
	} {
		rec := postAs(handleModelHouseBuy, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d %s, want 400", name, rec.Code, rec.Body.String())
		}
		if acc.Gems != 1000 || len(acc.RoomItems) != before {
			t.Errorf("%s: gems %d, %d items: a refused buy changed the account", name, acc.Gems, len(acc.RoomItems))
		}
	}

	poor, _ := phaseEAccount(t, s.Price-1, nil)
	rec := postAs(handleModelHouseBuy, modelHouseBody(s.Code, s.Price, "GEM", s.Elements))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errNoMoney) {
		t.Errorf("not enough gems: status %d %s, want %s", rec.Code, rec.Body.String(), errNoMoney)
	}
	if poor.Gems != s.Price-1 {
		t.Errorf("not enough gems: gems = %d, want %d", poor.Gems, s.Price-1)
	}

	// Owned elements are granted again at the set price (the Interior Shop grants furniture bought twice).
	rich, _ := phaseEAccount(t, 2000, nil)
	for i := 0; i < 2; i++ {
		if rec := postAs(handleModelHouseBuy, modelHouseBody(s.Code, s.Price, "GEM", s.Elements)); rec.Code != http.StatusOK {
			t.Fatalf("buy %d: status %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if rich.Gems != 2000-2*s.Price || len(rich.RoomItems) != 1+2*len(s.Elements) {
		t.Errorf("two buys: gems %d, %d items", rich.Gems, len(rich.RoomItems))
	}
}

// TestModelHouseRoom: the preview room of a set on sale is the layout the client loads. The set's wallpaper is the
// wall, furniture is in item, wall decor in wallItem, every preview seq is unique, and an unknown code is 404.
func TestModelHouseRoom(t *testing.T) {
	modelHouseFixture(t)
	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handleModelHouseRoom(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	type rowT struct {
		Seq       string `json:"seq"`
		Cd        string `json:"cd"`
		Direction string `json:"direction"`
		X         int64  `json:"x"`
		Y         int64  `json:"y"`
		Z         int64  `json:"z"`
	}
	type roomT struct {
		Result struct {
			GroundLevel string                         `json:"groundLevel"`
			TileSize    int64                          `json:"tileSize"`
			Floor       struct{ Seq, Cd, Type string } `json:"floor"`
			Wall        struct{ Seq, Cd, Type string } `json:"wall"`
			FloorItem   []rowT                         `json:"floorItem"`
			WallItem    []rowT                         `json:"wallItem"`
			Item        []rowT                         `json:"item"`
		} `json:"result"`
	}
	sets := modelHouseFile.Get().Sets
	for _, idx := range []int{0, 3} {
		s := sets[idx]
		rec := get(http.MethodGet, "/v4/shop/interior/room/"+s.Code)
		if rec.Code != http.StatusOK {
			t.Fatalf("room %s: status %d %s", s.Code, rec.Code, rec.Body.String())
		}
		var out roomT
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("room body %q: %v", rec.Body.String(), err)
		}
		r := out.Result
		if r.GroundLevel != "LEVEL_1" || r.TileSize != 12 || r.Floor.Type != "floorTile" || r.Wall.Type != "wallTile" {
			t.Errorf("%s: room header %+v", s.Code, r)
		}
		seqs := map[string]bool{}
		check := func(rows []rowT, where string) {
			for _, row := range rows {
				if seqs[row.Seq] || row.Seq == "0" || row.X < 0 || row.X > 11 || row.Y < 0 || row.Y > 11 {
					t.Errorf("%s %s: row %+v repeats a seq or is out of the room", s.Code, where, row)
				}
				seqs[row.Seq] = true
			}
		}
		for _, code := range s.Elements {
			switch code[2:4] {
			case "WA":
				if r.Wall.Cd != code || r.Wall.Seq == "0" {
					t.Errorf("%s: wall %+v, want the set's wallpaper %s", s.Code, r.Wall, code)
				}
			}
		}
		check(r.Item, "item")
		check(r.WallItem, "wallItem")
		check(r.FloorItem, "floorItem")
		if idx == 0 && (len(r.Item) != 4 || len(r.WallItem) != 1 || r.WallItem[0].Direction != "BR") {
			t.Errorf("%s: %d items and %d wall items, want 4 and 1", s.Code, len(r.Item), len(r.WallItem))
		}
		if idx == 3 && (len(r.Item) != 2 || len(r.WallItem) != 0) {
			t.Errorf("%s: %d items and %d wall items, want 2 and 0", s.Code, len(r.Item), len(r.WallItem))
		}
	}
	if rec := get(http.MethodGet, "/v4/shop/interior/room/GUMH0ZZZZ"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown set: status %d, want 404", rec.Code)
	}
	if rec := get(http.MethodPost, "/v4/shop/interior/room/"+sets[0].Code); rec.Code != http.StatusNotFound {
		t.Errorf("POST: status %d, want 404", rec.Code)
	}
}

// TestModelHouseDefaultsHaveArt: the shipped sets pass the archive check when the device archive is present.
func TestModelHouseDefaultsHaveArt(t *testing.T) {
	if _, err := os.Stat(thumbs.DpItemRoot); err != nil {
		t.Skipf("no item archive at %s", thumbs.DpItemRoot)
	}
	catalog = nil
	t.Cleanup(func() { catalog = nil })
	if err := verifyModelHouse(ActiveModelHouse()); err != nil {
		t.Fatal(err)
	}
}
