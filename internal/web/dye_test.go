package web

import (
	"encoding/json"
	"image/png"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/itemcode"
	"cherry/internal/store"
)

const (
	dyeToken = "dye-token"
	dyeAID   = "9601"
)

// dyeCodeOf is the item code of client item id n (the test garments are the client's own dyeable ids).
func dyeCodeOf(t *testing.T, n int64) string {
	t.Helper()
	code, ok := itemcode.Code(n)
	if !ok {
		t.Fatalf("item %d has no code", n)
	}
	return code
}

// dyeSetup installs one account with the given garments, equipped codes and dyes, file-backed so saves and the ledger are
// written. It returns the account and the directory of its files.
func dyeSetup(t *testing.T, gems int64, inventory, equipped []string, dyes map[string]int) (*store.Account, string) {
	t.Helper()
	acc := &store.Account{AccessToken: dyeToken, SessionKey: "sk", Aid: dyeAID, Name: "Dye", Gender: "FEMALE", Skin: "1",
		Country: "JP", Gems: gems, InventoryCodes: inventory, ItemCodes: equipped, Dyes: dyes}
	installSocialTestAccounts(t, map[string]*store.Account{dyeToken: acc})
	setLab(t, dyeAID, false)
	dir := t.TempDir()
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(dir, "accounts.json")
	store.NextAvatarID = 9601
	store.LatestAcc = acc
	store.AccountsMu.Unlock()
	return acc, dir
}

// dyeState copies the account's Gems, dye counts and dyed colours under the lock.
func dyeState(t *testing.T, acc *store.Account) (int64, map[string]int, map[string][]store.DyeColor) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	dyes := map[string]int{}
	for k, v := range acc.Dyes {
		dyes[k] = v
	}
	colors := map[string][]store.DyeColor{}
	for k, v := range acc.DyeColors {
		colors[k] = v
	}
	return acc.Gems, dyes, colors
}

// dyeLayer is one tone layer as the client's JSON carries it.
type dyeLayer struct {
	ColorCode    string `json:"colorCode"`
	Transparency int    `json:"transparency"`
}

// dyeGarmentRow is a closet or base-item row; ColorAndTransparencies is nil when the row has no dye data.
type dyeGarmentRow struct {
	InvenSeq               string     `json:"invenSeq"`
	ItemCode               string     `json:"itemCode"`
	DyeType                int        `json:"dyeType"`
	ColorAndTransparencies []dyeLayer `json:"colorAndTransparencies"`
}

// dyeUseBody is the apply body the client sends for one garment and dye.
func dyeUseBody(no, seq int64, layers []bool, transparency int, custom bool, colorCode string) string {
	b, _ := json.Marshal(map[string]any{"itemNo": no, "invenSeq": seq, "selectedLayer": layers,
		"transparency": transparency, "custom": custom, "colorCode": colorCode})
	return string(b)
}

// TestDyeShopListShape: every key is an array (the client indexes them), the tutorial reads complete, and count is what the
// requester owns.
func TestDyeShopListShape(t *testing.T) {
	dyeSetup(t, 0, nil, nil, map[string]int{"9501": 2, "9509": 1})
	rec := decorReq(t, dyeToken, http.MethodGet, "/v4/shop/dye/item/list", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Result struct {
			TutorialInfo struct {
				IsComplete bool  `json:"isComplete"`
				Reward     []any `json:"reward"`
			} `json:"tutorialInfo"`
			List map[string][]struct {
				ItemNo int64  `json:"itemNo"`
				Count  int    `json:"count"`
				Price  int64  `json:"price"`
				Coin   string `json:"coinUnit"`
				Kind   string `json:"dyeItemType"`
				Colors []any  `json:"dyeItemColorList"`
				Image  string `json:"image1Path"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Result.TutorialInfo.IsComplete || out.Result.TutorialInfo.Reward == nil {
		t.Fatalf("tutorialInfo = %+v", out.Result.TutorialInfo)
	}
	for _, k := range []string{"ALL", "SINGLE", "RANDOM", "CUSTOM"} {
		if out.Result.List[k] == nil {
			t.Fatalf("list.%s is null, want an array", k)
		}
	}
	if n := len(out.Result.List["ALL"]); n != 10 || len(out.Result.List["SINGLE"]) != 8 || len(out.Result.List["RANDOM"]) != 1 || len(out.Result.List["CUSTOM"]) != 1 {
		t.Fatalf("kind counts ALL %d SINGLE %d RANDOM %d CUSTOM %d", n, len(out.Result.List["SINGLE"]), len(out.Result.List["RANDOM"]), len(out.Result.List["CUSTOM"]))
	}
	for _, row := range out.Result.List["ALL"] {
		switch row.ItemNo {
		case 9501:
			if row.Count != 2 || row.Price != 100 || row.Coin != "GEM" || row.Kind != "SINGLE" || len(row.Colors) != 1 || row.Image != "cherry/dye/9501.png" {
				t.Fatalf("red dye row = %+v", row)
			}
		case 9510:
			if row.Colors == nil || len(row.Colors) != 0 || row.Price != 200 || row.Kind != "CUSTOM" {
				t.Fatalf("custom dye row = %+v (colours must be an empty array)", row)
			}
		}
	}
}

// TestDyeInventoryListsOnlyOwned: the inventory keeps the dyes with a count above zero, in ALL and in their kind.
func TestDyeInventoryListsOnlyOwned(t *testing.T) {
	dyeSetup(t, 0, nil, nil, map[string]int{"9502": 1})
	rec := decorReq(t, dyeToken, http.MethodGet, "/v4/inven/dye/item/list", "")
	var out struct {
		Result map[string][]struct {
			ItemNo int64 `json:"itemNo"`
			Count  int   `json:"count"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d err %v: %s", rec.Code, err, rec.Body.String())
	}
	if len(out.Result["ALL"]) != 1 || out.Result["ALL"][0].ItemNo != 9502 || out.Result["ALL"][0].Count != 1 || len(out.Result["SINGLE"]) != 1 {
		t.Fatalf("inventory = %+v", out.Result)
	}
	for _, k := range []string{"RANDOM", "CUSTOM"} {
		if out.Result[k] == nil || len(out.Result[k]) != 0 {
			t.Fatalf("%s = %v, want an empty array", k, out.Result[k])
		}
	}
}

// TestDyeBaseItemsAreOwnedDyeables: the closet lists only the owned dyeable garments, numbered as the closet numbers them
// (wearables first, then faces), in DRESS (dress kind) or FACE (custom kind) and ALL, with two layers, white when undyed.
func TestDyeBaseItemsAreOwnedDyeables(t *testing.T) {
	base, dress, face := "CUTO0011X", dyeCodeOf(t, 225001650), dyeCodeOf(t, 224102309)
	dyeSetup(t, 0, []string{base, face, dress}, []string{base, dress}, nil)
	rec := decorReq(t, dyeToken, http.MethodGet, "/v4/inven/closet/baseitem?isPreview=false&usePurchasePrice=false", "")
	var out struct {
		Result struct {
			ALL   []dyeGarmentRow `json:"ALL"`
			DRESS []dyeGarmentRow `json:"DRESS"`
			FACE  []dyeGarmentRow `json:"FACE"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d err %v: %s", rec.Code, err, rec.Body.String())
	}
	if len(out.Result.ALL) != 2 || len(out.Result.DRESS) != 1 || len(out.Result.FACE) != 1 {
		t.Fatalf("lists ALL %d DRESS %d FACE %d: %s", len(out.Result.ALL), len(out.Result.DRESS), len(out.Result.FACE), rec.Body.String())
	}
	d, f := out.Result.DRESS[0], out.Result.FACE[0]
	if d.ItemCode != dress || d.InvenSeq != "2" || d.DyeType != 2 || len(d.ColorAndTransparencies) != 2 ||
		d.ColorAndTransparencies[0] != (dyeLayer{ColorCode: "#FFFFFF"}) {
		t.Fatalf("dress row = %+v", d)
	}
	if f.ItemCode != face || f.InvenSeq != "3" {
		t.Fatalf("face row = %+v (faces are numbered after the wearables)", f)
	}
}

// TestDyeBuySpendsGems: one dye costs its catalog price in Gems, is added to the count and is logged; the reply carries the
// new Gem total.
func TestDyeBuySpendsGems(t *testing.T) {
	acc, dir := dyeSetup(t, 150, nil, nil, nil)
	rec := decorReq(t, dyeToken, http.MethodPost, "/v4/purchase/dye/item",
		`{"deviceType":"android","coinUnit":"GEM","itemNo":9501,"itemCount":1,"price":100,"cpId":"avtplay"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Result struct {
			BalanceUnit string `json:"balanceUnit"`
			Balance     int64  `json:"balance"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Result.BalanceUnit != "GEM" || out.Result.Balance != 50 {
		t.Fatalf("reply = %s (err %v)", rec.Body.String(), err)
	}
	gems, dyes, _ := dyeState(t, acc)
	if gems != 50 || dyes["9501"] != 1 {
		t.Fatalf("gems %d dyes %v", gems, dyes)
	}
	lines := readLedger(t, dir)
	if len(lines) != 1 || lines[0].Reason != "dye:buy:9501" || lines[0].Delta != -100 {
		t.Fatalf("ledger = %+v", lines)
	}
}

// TestDyeBuyRefusals: every refusal answers 400 and changes neither the Gems nor the dyes.
func TestDyeBuyRefusals(t *testing.T) {
	acc, _ := dyeSetup(t, 0, nil, nil, nil)
	for _, c := range []struct {
		name, body string
		gems       int64
	}{
		{"short balance", `{"coinUnit":"GEM","itemNo":9501,"itemCount":1,"price":100}`, 50},
		{"wrong price", `{"coinUnit":"GEM","itemNo":9501,"itemCount":1,"price":90}`, 150},
		{"unknown item", `{"coinUnit":"GEM","itemNo":9599,"itemCount":1,"price":100}`, 150},
		{"Cash", `{"coinUnit":"CASH","itemNo":9501,"itemCount":1,"price":100}`, 150},
		{"two at once", `{"coinUnit":"GEM","itemNo":9501,"itemCount":2,"price":100}`, 150},
	} {
		store.AccountsMu.Lock()
		acc.Gems = c.gems
		store.AccountsMu.Unlock()
		rec := decorReq(t, dyeToken, http.MethodPost, "/v4/purchase/dye/item", c.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", c.name, rec.Code)
		}
		gems, dyes, _ := dyeState(t, acc)
		if gems != c.gems || len(dyes) != 0 {
			t.Errorf("%s: changed the account (gems %d, dyes %v)", c.name, gems, dyes)
		}
	}
}

// TestDyeApplySingleShowsOnTheGarment: a SINGLE dye recolours the flagged layer, is used up, keeps the invenSeq, and the
// colour shows on the closet row, the avatar info and after an avatar save.
func TestDyeApplySingleShowsOnTheGarment(t *testing.T) {
	base, dress, face := "CUTO0011X", dyeCodeOf(t, 225001650), dyeCodeOf(t, 224102309)
	acc, _ := dyeSetup(t, 0, []string{base, face, dress}, []string{base, dress}, map[string]int{"9501": 1})
	rec := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/dye/item/use/custom", dyeUseBody(9501, 2, []bool{true, false}, 0, false, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		Result struct {
			Count      int        `json:"count"`
			Before     string     `json:"beforeInvenSeq"`
			After      string     `json:"afterInvenSeq"`
			Layers     []dyeLayer `json:"colorAndTransparencies"`
			ColorCodes []string   `json:"colorCodes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	r := reply.Result
	if r.Count != 0 || r.Before != "2" || r.After != "2" || !slices.Equal(r.ColorCodes, []string{"#E53935", "#FFFFFF"}) ||
		r.Layers[0] != (dyeLayer{ColorCode: "#E53935"}) {
		t.Fatalf("reply = %s", rec.Body.String())
	}
	_, dyes, colors := dyeState(t, acc)
	if _, owned := dyes["9501"]; owned || colors[dress][0].ColorCode != "#E53935" || colors[dress][1].ColorCode != "#FFFFFF" {
		t.Fatalf("dyes %v colours %v", dyes, colors)
	}

	closet := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/closet/items/all", "")
	var cl struct {
		Result struct {
			Inventory []struct {
				ItemCode string     `json:"itemCode"`
				InvenSeq string     `json:"invenSeq"`
				DyeType  int        `json:"dyeType"`
				Layers   []dyeLayer `json:"colorAndTransparencies"`
			} `json:"inventoryList"`
		} `json:"result"`
	}
	if err := json.Unmarshal(closet.Body.Bytes(), &cl); err != nil {
		t.Fatal(err)
	}
	for _, row := range cl.Result.Inventory {
		switch row.ItemCode {
		case dress:
			if row.InvenSeq != "2" || row.DyeType != 2 || len(row.Layers) != 2 || row.Layers[0].ColorCode != "#E53935" {
				t.Fatalf("closet dress row = %+v", row)
			}
		case base:
			if row.DyeType != 0 || row.Layers != nil {
				t.Fatalf("undyed closet row changed: %+v", row)
			}
		}
	}

	avatar := decorReq(t, dyeToken, http.MethodGet, "/v4/avatar/"+dyeAID, "")
	var info struct {
		Result struct {
			Items []struct {
				CD      string     `json:"cd"`
				DyeType int        `json:"dyeType"`
				Layers  []dyeLayer `json:"colorAndTransparencies"`
			} `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(avatar.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	shown := false
	for _, it := range info.Result.Items {
		if it.CD == dress {
			shown = it.DyeType == 2 && len(it.Layers) == 2 && it.Layers[0].ColorCode == "#E53935"
		}
	}
	if !shown {
		t.Fatalf("avatar info does not carry the dye: %s", avatar.Body.String())
	}

	save := decorReq(t, dyeToken, http.MethodPut, "/v4/avatar/save/v2",
		`[{"itemCode":"`+base+`","invenSeq":1},{"itemCode":"`+dress+`","invenSeq":2}]`)
	if save.Code != http.StatusOK || !strings.Contains(save.Body.String(), `"colorCode":"#E53935"`) {
		t.Fatalf("save status %d, reply %s", save.Code, save.Body.String())
	}
	_, _, colors = dyeState(t, acc)
	if colors[dress][0].ColorCode != "#E53935" {
		t.Fatalf("an avatar save wiped the dye: %v", colors)
	}
}

// TestDyeApplyRandomDrawsOneOfItsColours: a RANDOM dye applies one colour of its list to the layers flagged; the others stay.
func TestDyeApplyRandomDrawsOneOfItsColours(t *testing.T) {
	dress := dyeCodeOf(t, 225001650)
	dyeSetup(t, 0, []string{dress}, []string{dress}, map[string]int{"9509": 1})
	rec := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/dye/item/use/custom", dyeUseBody(9509, 1, []bool{false, true}, 0, false, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		Result struct {
			ColorCodes []string `json:"colorCodes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	var list []string
	for _, it := range economy.ActiveDyes().Dyes {
		if it.ItemNo == 9509 {
			for _, c := range it.Colors {
				list = append(list, c.Color)
			}
		}
	}
	if len(reply.Result.ColorCodes) != 2 || reply.Result.ColorCodes[0] != "#FFFFFF" || !slices.Contains(list, reply.Result.ColorCodes[1]) {
		t.Fatalf("colours %v, drawn from %v", reply.Result.ColorCodes, list)
	}
}

// TestDyeApplyCustomUsesThePalette: a CUSTOM dye applies a palette colour with the requested transparency to both layers.
func TestDyeApplyCustomUsesThePalette(t *testing.T) {
	face := dyeCodeOf(t, 224102309)
	acc, _ := dyeSetup(t, 0, []string{face}, []string{face}, map[string]int{"9510": 1})
	rec := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/dye/item/use/custom", dyeUseBody(9510, 1, []bool{true, true}, 30, true, "#8e24aa"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	_, _, colors := dyeState(t, acc)
	want := []store.DyeColor{{ColorCode: "#8E24AA", Transparency: 30}, {ColorCode: "#8E24AA", Transparency: 30}}
	if !slices.Equal(colors[face], want) {
		t.Fatalf("colours = %v, want %v", colors[face], want)
	}
}

// TestDyeApplyRefusals: a garment that cannot be dyed, a garment not owned, a dye not owned, a bad layer selection and a bad
// custom colour all answer 400 and change nothing.
func TestDyeApplyRefusals(t *testing.T) {
	base, face := "CUTO0011X", dyeCodeOf(t, 224102309)
	acc, _ := dyeSetup(t, 0, []string{base, face}, []string{base, face}, map[string]int{"9510": 1, "9502": 0})
	for _, c := range []struct{ name, body string }{
		{"garment that cannot be dyed", dyeUseBody(9510, 1, []bool{true, true}, 0, true, "#E53935")},
		{"garment not owned", dyeUseBody(9510, 9, []bool{true, true}, 0, true, "#E53935")},
		{"dye not owned", dyeUseBody(9502, 2, []bool{true, true}, 0, false, "")},
		{"three layer flags", dyeUseBody(9510, 2, []bool{true, true, true}, 0, true, "#E53935")},
		{"no layer flagged", dyeUseBody(9510, 2, []bool{false, false}, 0, true, "#E53935")},
		{"colour not in the palette", dyeUseBody(9510, 2, []bool{true, true}, 0, true, "#123456")},
		{"custom flag missing", dyeUseBody(9510, 2, []bool{true, true}, 0, false, "#E53935")},
		{"transparency above 100", dyeUseBody(9510, 2, []bool{true, true}, 101, true, "#E53935")},
	} {
		rec := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/dye/item/use/custom", c.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", c.name, rec.Code, rec.Body.String())
		}
		_, dyes, colors := dyeState(t, acc)
		if dyes["9510"] != 1 || len(colors) != 0 {
			t.Errorf("%s: changed the account (dyes %v, colours %v)", c.name, dyes, colors)
		}
	}
}

// TestDyeImageIsAPicture: /cherry/dye/<itemNo>.png is a PNG; an unknown dye is 404.
func TestDyeImageIsAPicture(t *testing.T) {
	rec := decorReq(t, "", http.MethodGet, "/cherry/dye/9501.png", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("status %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if _, err := png.Decode(rec.Body); err != nil {
		t.Fatal(err)
	}
	if rec := decorReq(t, "", http.MethodGet, "/cherry/dye/9599.png", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown dye status %d", rec.Code)
	}
}
