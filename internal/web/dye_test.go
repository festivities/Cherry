package web

import (
	"encoding/json"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/itemcode"
	"cherry/internal/store"
	"cherry/internal/thumbs"
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
	if n := len(out.Result.List["ALL"]); n != 11 || len(out.Result.List["SINGLE"]) != 9 || len(out.Result.List["RANDOM"]) != 1 || len(out.Result.List["CUSTOM"]) != 1 {
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

// TestDyeApplyCustomTakesAnyHexColour: a CUSTOM dye applies the #RRGGBB the player picks, palette or not, upper-cased, with
// the requested transparency on both layers. A colour that is not #RRGGBB answers 400 and uses no dye.
func TestDyeApplyCustomTakesAnyHexColour(t *testing.T) {
	face := dyeCodeOf(t, 224102309)
	acc, _ := dyeSetup(t, 0, []string{face}, []string{face}, map[string]int{"9510": 3})
	layers := func(c string) []store.DyeColor {
		return []store.DyeColor{{ColorCode: c, Transparency: 30}, {ColorCode: c, Transparency: 30}}
	}
	for _, c := range []struct{ sent, applied string }{{"#8e24aa", "#8E24AA"}, {"#fbd7d7", "#FBD7D7"}} {
		rec := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/dye/item/use/custom", dyeUseBody(9510, 1, []bool{true, true}, 30, true, c.sent))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", c.sent, rec.Code, rec.Body.String())
		}
		if _, _, colors := dyeState(t, acc); !slices.Equal(colors[face], layers(c.applied)) {
			t.Fatalf("%s: colours = %v, want %v", c.sent, colors[face], layers(c.applied))
		}
	}
	for _, bad := range []string{"FBD7D7", "#FBD7D", "#GG0000", "#FBD7D7F"} {
		rec := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/dye/item/use/custom", dyeUseBody(9510, 1, []bool{true, true}, 30, true, bad))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", bad, rec.Code, rec.Body.String())
		}
		_, dyes, colors := dyeState(t, acc)
		if dyes["9510"] != 1 || !slices.Equal(colors[face], layers("#FBD7D7")) {
			t.Errorf("%s: changed the account (dyes %v, colours %v)", bad, dyes, colors[face])
		}
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
		{"colour not #rrggbb", dyeUseBody(9510, 2, []bool{true, true}, 0, true, "#12345")},
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

// TestDyeMaskIsAPictureForDyeableGarments: a dyeable garment the archive has no mask for gets a composed dp_mask.png (the
// dye cell's picture: 224102378, 224407224, 225409234). The archive's original mask (224102309) is served as is when no
// stream answered it; a garment that is not dyeable, another kind or another file keeps its 404. Needs the device
// archive; the cache is a temp dir.
func TestDyeMaskIsAPictureForDyeableGarments(t *testing.T) {
	if _, err := os.Stat(filepath.Join(thumbs.DpItemRoot, "custom")); err != nil {
		t.Skip("device archive absent")
	}
	oldCache := thumbs.DpCacheDir
	thumbs.DpCacheDir = t.TempDir()
	t.Cleanup(func() { thumbs.DpCacheDir = oldCache })
	for _, path := range []string{"/arts_item_custom_224102378/dp_mask.png", "/arts_item_custom_224407224/dp_mask.png",
		"/img/read/arts_item_dress_225409234/dp_mask.png"} {
		rec := decorReq(t, "", http.MethodGet, path, "")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("%s: status %d type %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		if _, err := png.Decode(rec.Body); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	// the original mask is served as is when no stream answered it
	if rec := decorReq(t, "", http.MethodGet, "/arts_item_custom_224102309/dp_mask.png", ""); rec.Code != http.StatusOK {
		t.Fatalf("original mask: status %d", rec.Code)
	}
	for _, path := range []string{"/arts_item_custom_224999999/dp_mask.png",
		"/arts_item_pet_225001650/dp_mask.png", "/arts_item_custom_224102378/dp_mask.jpg"} {
		if rec := decorReq(t, "", http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", path, rec.Code)
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

// dyeBaseFixture installs the garments the Dye Base Shop can list: a top, an accessory, a CA accessory and two hairs, plus
// an eye. The eye has no clothing price (its category is not in data/prices.json), so the buy refuses it and it is never listed.
func dyeBaseFixture(t *testing.T) {
	t.Helper()
	root, _ := thumbFixture(t)
	for _, f := range [][2]string{{"dress", "225001650"}, {"dress", "225409234"}, {"dress", "235411996"},
		{"custom", "224406975"}, {"custom", "224407224"}, {"custom", "224102309"}} {
		writeThumbItem(t, root, f[0], f[1], "a.png", []byte("x"))
		writeThumbItem(t, root, f[0], f[1], "dp.png", []byte("x")) // art marker
	}
}

// dyeBaseRow is one sShopItem row of the Dye Base Shop (the fields the client reads; colorant-spec.md 3.7).
type dyeBaseRow struct {
	ItemID       int64  `json:"itemID"`
	ItemCode     string `json:"itemCode"`
	Price        int64  `json:"price"`
	Discounted   int64  `json:"discountedPrice"`
	Discount     bool   `json:"discount"`
	PaymentType  string `json:"paymentType"`
	Selling      bool   `json:"selling"`
	DyeType      int    `json:"dyeType"`
	Name         string `json:"name"`
	Elements     []any  `json:"elementItems"`
	ElementCodes []any  `json:"elementItemCodes"`
}

// dyeBaseRows reads a Dye Base Shop list: result.items must be an array.
func dyeBaseRows(t *testing.T, path string) []dyeBaseRow {
	t.Helper()
	rec := decorReq(t, dyeToken, http.MethodGet, path, "")
	var out struct {
		Result struct {
			Items []dyeBaseRow `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d err %v: %s", path, rec.Code, err, rec.Body.String())
	}
	if out.Result.Items == nil {
		t.Fatalf("GET %s: items is not an array: %s", path, rec.Body.String())
	}
	return out.Result.Items
}

// dyeBaseBuyBody is the purchase/dress/buy body the Dye Base Shop sends for one row (SendPurchaseItem; colorant-open-routes.md 1.4).
func dyeBaseBuyBody(code string, price int64) string {
	return `{"deviceType":"android","itemType":"DRESS","coinUnit":"GEM","items":[{"itemCode":"` + code +
		`","price":` + strconv.FormatInt(price, 10) + `}],"brandShopId":"","brandShopNo":"","cpId":"avtplay","language":"en"}` + "\x00"
}

// TestDyeBaseShopListsWhatTheBuyWouldSell: the unowned dyeable garments the buy sells, in sShopItem shape with the price the
// buy charges; the owned hat, the eye (no clothing price) and any other garment are left out.
func TestDyeBaseShopListsWhatTheBuyWouldSell(t *testing.T) {
	dyeBaseFixture(t)
	dyeSetup(t, 0, []string{dyeCodeOf(t, 225409234)}, nil, nil)
	rows := dyeBaseRows(t, "/v4/shop/dye/baseitem/list/DRESS/all")
	want := map[string]int64{dyeCodeOf(t, 224406975): 224406975, dyeCodeOf(t, 224407224): 224407224,
		dyeCodeOf(t, 225001650): 225001650, dyeCodeOf(t, 235411996): 235411996}
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for _, row := range rows {
		id, ok := want[row.ItemCode]
		if !ok || row.ItemID != id {
			t.Fatalf("unexpected row %+v", row)
		}
		price, _, _, _ := economy.ItemPrice(row.ItemCode)
		if row.PaymentType != "GEM" || !row.Selling || row.DyeType != 2 || row.Name == "" || row.Elements == nil ||
			row.ElementCodes == nil || row.Price != price || (!row.Discount && row.Discounted != price) {
			t.Fatalf("row %+v: want GEM at table price %d, two layers, arrays for the element lists", row, price)
		}
	}
	// the other categories have no rows (the client's FACE tab)
	if face := dyeBaseRows(t, "/v4/shop/dye/baseitem/list/FACE/all"); len(face) != 0 {
		t.Fatalf("FACE rows = %+v", face)
	}
}

// TestDyeBaseShopAnimalsAndBuy: an animal is not offered the human hair; the base body buys a listed garment through
// purchase/dress/buy (balance, resultCode and buyItems as the client reads them), a second buy is refused as owned, a
// stale price is refused, and every refusal leaves the Gems alone.
func TestDyeBaseShopAnimalsAndBuy(t *testing.T) {
	dyeBaseFixture(t)
	acc, _ := dyeSetup(t, 500, nil, nil, nil)
	store.AccountsMu.Lock()
	acc.Gender = "ANIMAL"
	store.AccountsMu.Unlock()
	rows := dyeBaseRows(t, "/v4/shop/dye/baseitem/list/DRESS/all")
	for _, row := range rows {
		if row.ItemCode == dyeCodeOf(t, 224406975) || row.ItemCode == dyeCodeOf(t, 224407224) {
			t.Fatalf("an animal is offered human hair: %+v", row)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("animal rows = %d, want 3 (top, accessory, CA accessory)", len(rows))
	}
	hair := dyeCodeOf(t, 224406975)
	if rec := decorReq(t, dyeToken, http.MethodPost, "/v4/purchase/dress/buy", dyeBaseBuyBody(hair, 200)); rec.Code != http.StatusBadRequest {
		t.Fatalf("animal bought human hair: status %d: %s", rec.Code, rec.Body.String())
	}

	store.AccountsMu.Lock()
	acc.Gender = "FEMALE"
	store.AccountsMu.Unlock()
	rows = dyeBaseRows(t, "/v4/shop/dye/baseitem/list/DRESS/all")
	var top dyeBaseRow
	for _, row := range rows {
		if row.ItemCode == dyeCodeOf(t, 225001650) {
			top = row
		}
	}
	effective := top.Price
	if top.Discount {
		effective = top.Discounted
	}
	if rec := decorReq(t, dyeToken, http.MethodPost, "/v4/purchase/dress/buy", dyeBaseBuyBody(top.ItemCode, effective+1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("stale price: status %d", rec.Code)
	}
	rec := decorReq(t, dyeToken, http.MethodPost, "/v4/purchase/dress/buy", dyeBaseBuyBody(top.ItemCode, effective))
	var out struct {
		Result struct {
			Balance    *float64 `json:"balance"`
			ResultCode *int     `json:"resultCode"`
			BuyItems   []string `json:"buyItems"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("buy: status %d err %v: %s", rec.Code, err, rec.Body.String())
	}
	if out.Result.Balance == nil || *out.Result.Balance != float64(500-effective) || out.Result.ResultCode == nil ||
		*out.Result.ResultCode != 0 || !slices.Equal(out.Result.BuyItems, []string{top.ItemCode}) {
		t.Fatalf("buy reply = %s", rec.Body.String())
	}
	if gems, _, _ := dyeState(t, acc); gems != 500-effective {
		t.Fatalf("gems %d after the buy", gems)
	}
	if rec := decorReq(t, dyeToken, http.MethodPost, "/v4/purchase/dress/buy", dyeBaseBuyBody(top.ItemCode, effective)); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), `"errorCode":"61009"`) {
		t.Fatalf("second buy: status %d: %s", rec.Code, rec.Body.String())
	}
	for _, row := range dyeBaseRows(t, "/v4/shop/dye/baseitem/list/DRESS/all") {
		if row.ItemCode == top.ItemCode {
			t.Fatalf("a bought garment is still listed: %+v", row)
		}
	}
}

// TestDyeBaseShopMenuRow: the Dye Base Shop row is the last MENU row, labelled and pictured.
func TestDyeBaseShopMenuRow(t *testing.T) {
	rows := homeIconRows()
	last := rows[len(rows)-1]
	if last.LinkType != "goSomewhere(colorantBaseShop)" || last.Name != "Dye Base Shop" || last.Image != "cherry/menu/palette.png" {
		t.Fatalf("last menu row = %+v", last)
	}
	if rec := serve(t, http.MethodGet, "/"+last.Image); rec.Code != http.StatusOK {
		t.Fatalf("GET /%s: %d", last.Image, rec.Code)
	}
}

// TestBleachIsAOneColourDyeInTheLists: the bleach is a 60-Gem row with bleach true and one colour (no RANDOM padding, so the
// client's colour preview reads one entry), it is owned by count, and its picture is served.
func TestBleachIsAOneColourDyeInTheLists(t *testing.T) {
	dyeSetup(t, 0, nil, nil, map[string]int{"9511": 2})
	shop := decorReq(t, dyeToken, http.MethodGet, "/v4/shop/dye/item/list", "")
	var list struct {
		Result struct {
			List map[string][]struct {
				ItemNo int64      `json:"itemNo"`
				Price  int64      `json:"price"`
				Bleach bool       `json:"bleach"`
				Kind   string     `json:"dyeItemType"`
				Colors []dyeLayer `json:"dyeItemColorList"`
				Count  int        `json:"count"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(shop.Body.Bytes(), &list); err != nil || shop.Code != http.StatusOK {
		t.Fatalf("shop list: status %d err %v", shop.Code, err)
	}
	found := false
	for _, row := range list.Result.List["ALL"] {
		if row.ItemNo == 9511 {
			found = true
			if row.Price != 60 || !row.Bleach || row.Kind != "SINGLE" || len(row.Colors) != 1 || row.Count != 2 {
				t.Fatalf("bleach row = %+v", row)
			}
		} else if row.Bleach {
			t.Fatalf("dye %d is marked bleach", row.ItemNo)
		}
	}
	if !found {
		t.Fatal("the bleach is not in the shop list")
	}
	inven := decorReq(t, dyeToken, http.MethodGet, "/v4/inven/dye/item/list", "")
	if body := inven.Body.String(); !strings.Contains(body, `"itemName":"Bleach"`) || !strings.Contains(body, `"itemNo":9511`) ||
		!strings.Contains(body, `"count":2`) || !strings.Contains(body, `"bleach":true`) {
		t.Fatalf("inventory = %s", inven.Body.String())
	}
	if rec := decorReq(t, "", http.MethodGet, "/cherry/dye/9511.png", ""); rec.Code != http.StatusOK {
		t.Fatalf("bleach picture: status %d", rec.Code)
	}
}

// dyeBleachState sets the account's dyes and dyed colours (the test setup of a dyed garment or a bleach count).
func dyeBleachState(t *testing.T, acc *store.Account, dyes map[string]int, colors map[string][]store.DyeColor) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc.Dyes, acc.DyeColors = dyes, colors
}

// TestBleachApplyRemovesTheDye: a bleach on a dyed garment uses one bleach, removes the garment's colour (empty colour
// arrays in the reply, as the client reads them), the closet shows the garment undyed, and the other layers' flags are not
// needed.
func TestBleachApplyRemovesTheDye(t *testing.T) {
	top := dyeCodeOf(t, 225001650)
	acc, _ := dyeSetup(t, 0, []string{top}, []string{top}, map[string]int{"9511": 2})
	dyeBleachState(t, acc, map[string]int{"9511": 2}, map[string][]store.DyeColor{top: {{ColorCode: "#E53935"}, {ColorCode: "#FFFFFF"}}})
	rec := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/dye/item/use/custom", dyeUseBody(9511, 1, []bool{true, false}, 0, false, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		Result struct {
			Count      *int       `json:"count"`
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
	if r.Count == nil || *r.Count != 1 || r.Before != "1" || r.After != "1" || !slices.Equal(r.ColorCodes, []string{"", ""}) ||
		len(r.Layers) != 2 || r.Layers[0] != (dyeLayer{}) || r.Layers[1] != (dyeLayer{}) {
		t.Fatalf("reply = %s", rec.Body.String())
	}
	gems, dyes, colors := dyeState(t, acc)
	if _, has := colors[top]; has || dyes["9511"] != 1 || gems != 0 || len(colors) != 0 {
		t.Fatalf("after the bleach: dyes %v colours %v", dyes, colors)
	}
	closet := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/closet/items/all", "")
	if strings.Contains(closet.Body.String(), `"dyeType":2`) || strings.Contains(closet.Body.String(), `"colorAndTransparencies"`) {
		t.Fatalf("the closet still shows the dye: %s", closet.Body.String())
	}
	// the last bleach is used up and the dye count leaves the inventory
	dyeBleachState(t, acc, map[string]int{"9511": 1}, map[string][]store.DyeColor{top: {{ColorCode: "#1E88E5"}, {ColorCode: "#FFFFFF"}}})
	if rec := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/dye/item/use/custom", dyeUseBody(9511, 1, []bool{true, true}, 0, false, "")); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"count":0`) {
		t.Fatalf("last bleach: status %d: %s", rec.Code, rec.Body.String())
	}
	if _, dyes, _ := dyeState(t, acc); len(dyes) != 0 {
		t.Fatalf("dyes after the last bleach = %v", dyes)
	}
}

// TestBleachRefusals: a bleach on an undyed garment, or with no bleach owned, answers 400 and changes nothing.
func TestBleachRefusals(t *testing.T) {
	top := dyeCodeOf(t, 225001650)
	acc, _ := dyeSetup(t, 0, []string{top}, []string{top}, map[string]int{"9511": 1})
	if rec := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/dye/item/use/custom", dyeUseBody(9511, 1, []bool{true, false}, 0, false, "")); rec.Code != http.StatusBadRequest {
		t.Fatalf("bleach on an undyed garment: status %d: %s", rec.Code, rec.Body.String())
	}
	if _, dyes, colors := dyeState(t, acc); dyes["9511"] != 1 || len(colors) != 0 {
		t.Fatalf("refused bleach changed the account: dyes %v colours %v", dyes, colors)
	}
	dyeBleachState(t, acc, map[string]int{"9502": 1}, map[string][]store.DyeColor{top: {{ColorCode: "#E53935"}, {ColorCode: "#FFFFFF"}}})
	if rec := decorReq(t, dyeToken, http.MethodPost, "/v4/inven/dye/item/use/custom", dyeUseBody(9511, 1, []bool{true, false}, 0, false, "")); rec.Code != http.StatusBadRequest {
		t.Fatalf("bleach with none owned: status %d: %s", rec.Code, rec.Body.String())
	}
	if _, dyes, colors := dyeState(t, acc); dyes["9502"] != 1 || len(colors[top]) != 2 || colors[top][0].ColorCode != "#E53935" {
		t.Fatalf("refused bleach changed the account: dyes %v colours %v", dyes, colors)
	}
}

// TestBleachBuysLikeADye: a bleach costs 60 Gems through purchase/dye/item and is logged as a dye purchase.
func TestBleachBuysLikeADye(t *testing.T) {
	acc, dir := dyeSetup(t, 60, nil, nil, nil)
	rec := decorReq(t, dyeToken, http.MethodPost, "/v4/purchase/dye/item",
		`{"deviceType":"android","coinUnit":"GEM","itemNo":9511,"itemCount":1,"price":60,"cpId":"avtplay"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	gems, dyes, _ := dyeState(t, acc)
	if gems != 0 || dyes["9511"] != 1 {
		t.Fatalf("gems %d dyes %v", gems, dyes)
	}
	if lines := readLedger(t, dir); len(lines) != 1 || lines[0].Reason != "dye:buy:9511" || lines[0].Delta != -60 {
		t.Fatalf("ledger = %+v", lines)
	}
}
