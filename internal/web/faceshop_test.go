package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/store"
)

func faceShopReq(t *testing.T, token, method, target, body string) (int, []byte) {
	t.Helper()
	rec := decorReq(t, token, method, target, body)
	return rec.Code, rec.Body.Bytes()
}

func faceShopSetup(t *testing.T, gender string, equipped, inventory []string) *store.Account {
	t.Helper()
	acc := &store.Account{AccessToken: "ftok", SessionKey: "sk", Aid: "9301", Name: "Face", Gender: gender, Skin: "2", Country: "JP", ItemCodes: equipped, InventoryCodes: inventory, Gems: 500, FaceTickets: 10}
	installSocialTestAccounts(t, map[string]*store.Account{"ftok": acc})
	store.AccountsMu.Lock()
	store.NextAvatarID = 9301
	store.AccountsMu.Unlock()
	return acc
}

func TestFaceCatalogCounts(t *testing.T) {
	counts := map[string]int{}
	for _, c := range economy.FaceCatalog {
		if len(c) != 9 || !store.IsBasicFaceItemCode(c) {
			t.Fatalf("bad catalog code %q", c)
		}
		counts[c[2:4]]++
	}
	want := map[string]int{"HE": 14, "EB": 200, "EY": 507, "NO": 54, "MO": 398}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("%s = %d, want %d", k, counts[k], v)
		}
	}
	if len(economy.FaceCatalog) != 1173 || len(economy.FaceCatalogSet) != 1173 {
		t.Fatalf("catalog = %d unique %d, want 1173", len(economy.FaceCatalog), len(economy.FaceCatalogSet))
	}
}

// Checks every type the native parsers read (jsoncpp asInt/asString/asBool throw on mismatch).
func TestFaceShopDataShapes(t *testing.T) {
	faceShopSetup(t, "FEMALE", nil, nil)
	code, body := faceShopReq(t, "ftok", http.MethodGet, "/v4/faceshop/v2/shop/1?shopLocation=HOME", "")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var top struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	var avatarType string
	if json.Unmarshal(top.Result["avatarType"], &avatarType) != nil || avatarType != "F" {
		t.Fatalf("avatarType = %s", top.Result["avatarType"])
	}
	total := 0
	for key, slot := range map[string]string{"FFACE": "HE", "FBROW": "EB", "FEYE": "EY", "FNOSE": "NO", "FMOUTH": "MO"} {
		var cat map[string][]map[string]json.RawMessage
		if json.Unmarshal(top.Result[key], &cat) != nil || cat["items"] == nil {
			t.Fatalf("%s = %.80s, want object with items array", key, top.Result[key])
		}
		for _, it := range cat["items"] {
			for _, f := range []string{"cd", "sellingIconType", "specialEffects"} {
				if v := it[f]; len(v) == 0 || v[0] != '"' {
					t.Fatalf("%s item %s = %s, want string", key, f, v)
				}
			}
			var cd string
			_ = json.Unmarshal(it["cd"], &cd)
			if cd[2:4] != slot {
				t.Fatalf("%s holds %s", key, cd)
			}
		}
		total += len(cat["items"])
	}
	if total != 1173 {
		t.Fatalf("catalog items = %d", total)
	}
	var set map[string][]json.RawMessage
	if json.Unmarshal(top.Result["SET"], &set) != nil || set["items"] == nil {
		t.Fatalf("SET = %s", top.Result["SET"])
	}
	var shop map[string]json.RawMessage
	if json.Unmarshal(top.Result["shop"], &shop) != nil {
		t.Fatalf("shop = %s", top.Result["shop"])
	}
	for _, f := range []string{"id", "price", "discountedPrice"} {
		var n int
		if json.Unmarshal(shop[f], &n) != nil {
			t.Errorf("shop.%s = %s, want int", f, shop[f])
		}
	}
	for _, f := range []string{"name", "cpId", "itemCd", "shopType", "displayCoinUnit"} {
		if v := shop[f]; len(v) == 0 || v[0] != '"' {
			t.Errorf("shop.%s = %s, want string", f, v)
		}
	}
	var discount bool
	var rate float64
	if json.Unmarshal(shop["discount"], &discount) != nil || json.Unmarshal(shop["dcRate"], &rate) != nil {
		t.Errorf("discount/dcRate = %s/%s", shop["discount"], shop["dcRate"])
	}
	if string(shop["price"]) != "0" || string(shop["discountedPrice"]) != "0" || string(shop["id"]) != "1" {
		t.Errorf("shop = %v", shop)
	}
	var gown []map[string]string
	if json.Unmarshal(top.Result["pGown"], &gown) != nil || len(gown) != 3 || gown[0]["cd"] != "CUTO0011X" {
		t.Errorf("pGown = %s", top.Result["pGown"])
	}
	var basic []map[string]string
	if json.Unmarshal(top.Result["basicFaceList"], &basic) != nil || basic == nil {
		t.Errorf("basicFaceList = %s", top.Result["basicFaceList"])
	}
}

func TestFaceShopDataGenderRoutesAndAuth(t *testing.T) {
	faceShopSetup(t, "MALE", nil, nil)
	_, body := faceShopReq(t, "ftok", http.MethodGet, "/v4/faceshop/v2/shop/7?shopLocation=SQUARE", "")
	if !strings.Contains(string(body), `"avatarType":"M"`) || !strings.Contains(string(body), `"MFACE":{"items":[{"cd":"CUHE`) || strings.Contains(string(body), `"FFACE"`) || !strings.Contains(string(body), `"id":7`) {
		t.Fatalf("male shop body: %.200s", body)
	}
	faceShopSetup(t, "ANIMAL", nil, nil)
	_, body = faceShopReq(t, "ftok", http.MethodGet, "/v4/faceshop/v2/shop/1", "")
	if !strings.Contains(string(body), `"avatarType":"A"`) || !strings.Contains(string(body), `"AFACE":{"items":[]}`) || strings.Contains(string(body), "CUEY") {
		t.Fatalf("animal shop body: %.200s", body)
	}
	for _, c := range []struct {
		token, method, target string
		want                  int
	}{
		{"", http.MethodGet, "/v4/faceshop/v2/shop/1", 404},
		{"ftok", http.MethodPost, "/v4/faceshop/v2/shop/1", 404},
		{"ftok", http.MethodGet, "/v4/faceshop/v2/shop/", 404},
		{"ftok", http.MethodGet, "/v4/faceshop/v2/shop/x", 404},
		{"ftok", http.MethodGet, "/v4/faceshop/v2/shop/1/2", 404},
		{"ftok", http.MethodPost, "/v4/voucher/own/count/faceshop", 404},
	} {
		if code, _ := faceShopReq(t, c.token, c.method, c.target, ""); code != c.want {
			t.Errorf("%s %s token=%q: %d, want %d", c.method, c.target, c.token, code, c.want)
		}
	}
	code, body := faceShopReq(t, "ftok", http.MethodGet, "/v4/voucher/own/count/faceshop", "")
	var v struct {
		Result struct {
			VoucherCount int `json:"voucherCount"`
		} `json:"result"`
	}
	if code != 200 || json.Unmarshal(body, &v) != nil || string(body) != `{"result":{"voucherCount":10}}` {
		t.Fatalf("voucher count %d %s", code, body)
	}
}

const faceShopPurchaseURL = "/v4/faceshop/saveAndPurchase/v2"

func TestFaceShopPurchaseEquipsAndGrants(t *testing.T) {
	// Phone-like account: only starter faces owned, faces-only outfit plus one wearable.
	acc := faceShopSetup(t, "FEMALE",
		[]string{"CUEY00002", "CUMO00002", "CUEB00001", "CUNO00001", "CUHE0000L", "CUON004TV"},
		[]string{"CUEY00002", "CUMO00002", "CUEB00001", "CUNO00001", "CUHE0000L", "CUON004TV"})
	path := filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Lock()
	store.AccountStorePath = path
	store.AccountsMu.Unlock()

	body := `{"brandId":"","shopid":"1","price":"0","displayCoinUnit":"G","itemCd":"","cpId":"",` +
		`"saveItemList":[{"itemCode":"CUEY0030Q","invenSeq":0},{"itemCode":"CUMO001ET"},{"itemCode":"SKN002"},` +
		`{"itemCode":"CUTO0011X"},{"itemCode":"CUON004TV","invenSeq":3},{"itemCode":"CUSH0009Q"}],` +
		`"invenItemList":[3],"language":"ja","deviceType":"android","useVoucher":"false"}`
	code, resp := faceShopReq(t, "ftok", http.MethodPost, faceShopPurchaseURL, body+"\x00")
	if code != 200 {
		t.Fatalf("status %d: %s", code, resp)
	}
	var out struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"coin": "500", "cash": "0"} {
		if string(out.Result[f]) != want {
			t.Errorf("%s = %s, want int %s", f, out.Result[f], want)
		}
	}
	var avatar store.AvatarInfoResult
	if err := json.Unmarshal(resp[len(`{"result":`):len(resp)-1], &avatar); err != nil {
		t.Fatal(err)
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(out.Result["items"], &items) != nil || len(items) == 0 {
		t.Fatalf("items must be a non-empty array (client treats empty as failure): %s", out.Result["items"])
	}
	for _, cd := range []string{"CUEY0030Q", "CUMO001ET", "CUEB00001", "CUNO00001", "CUHE0000L", "CUON004TV", "CUSH0009Q", "CUHA00004"} {
		if !avatarHasCD(avatar.Items, cd) {
			t.Errorf("response missing %s: %+v", cd, avatar.Items)
		}
	}
	if avatarHasCD(avatar.Items, "CUEY00002") || avatarHasCD(avatar.Items, "CUMO00002") {
		t.Errorf("old faces still equipped: %+v", avatar.Items)
	}
	if avatar.Skin != "2" {
		t.Errorf("skin changed to %q", avatar.Skin)
	}

	store.AccountsMu.Lock()
	gotItems := append([]string(nil), acc.ItemCodes...)
	gotInv := append([]string(nil), acc.InventoryCodes...)
	store.AccountsMu.Unlock()
	wantItems := []string{"CUEY0030Q", "CUMO001ET", "CUEB00001", "CUNO00001", "CUHE0000L", "CUON004TV"}
	if strings.Join(gotItems, ",") != strings.Join(wantItems, ",") {
		t.Errorf("equipped = %v, want %v", gotItems, wantItems)
	}
	if !hasCode(gotInv, "CUEY0030Q") || !hasCode(gotInv, "CUMO001ET") || !hasCode(gotInv, "CUEY00002") || len(gotInv) != 8 {
		t.Errorf("inventory = %v", gotInv)
	}

	// Persisted: reload the store and the faces survive (Garden/Closet/relaunch source).
	store.AccountsMu.Lock()
	store.Accounts, store.LatestAcc = map[string]*store.Account{}, nil
	store.AccountsMu.Unlock()
	if err := store.LoadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	var reloaded *store.Account
	for _, a := range store.Accounts {
		reloaded = a
	}
	reloadedItems := strings.Join(reloaded.ItemCodes, ",")
	store.AccountsMu.Unlock()
	if reloadedItems != strings.Join(wantItems, ",") {
		t.Errorf("reloaded equipped = %s", reloadedItems)
	}

	// Closet now lists the purchased face as an owned selectable row.
	req := decorReq(t, "ftok", http.MethodPost, "/v4/inven/closet/items/all", "")
	if !strings.Contains(req.Body.String(), `"itemCode":"CUEY0030Q"`) {
		t.Errorf("closet missing purchased face")
	}

	// Idempotent repeat.
	if code, _ := faceShopReq(t, "ftok", http.MethodPost, faceShopPurchaseURL, body); code != 200 {
		t.Errorf("repeat status %d", code)
	}
	store.AccountsMu.Lock()
	n := len(reloaded.InventoryCodes)
	store.AccountsMu.Unlock()
	if n != 8 {
		t.Errorf("inventory grew on repeat: %d", n)
	}
}

func TestFaceShopPurchaseAppendsMissingCategory(t *testing.T) {
	acc := faceShopSetup(t, "MALE", []string{"CUON004TV"}, []string{"CUON004TV"})
	code, resp := faceShopReq(t, "ftok", http.MethodPost, faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUHE0000L"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, resp)
	}
	store.AccountsMu.Lock()
	got := strings.Join(acc.ItemCodes, ",")
	store.AccountsMu.Unlock()
	if got != "CUON004TV,CUHE0000L" {
		t.Errorf("equipped = %s", got)
	}
}

func TestFaceShopPurchaseRejectsWithoutMutation(t *testing.T) {
	acc := faceShopSetup(t, "FEMALE", []string{"CUEY00002"}, []string{"CUEY00002", "CUON004TV"})
	for name, body := range map[string]string{
		"unknown face":           `{"saveItemList":[{"itemCode":"CUEY99999"}]}`,
		"unknown HE after valid": `{"saveItemList":[{"itemCode":"CUEY0030Q"},{"itemCode":"CUHE99999"}]}`,
		"two per category":       `{"saveItemList":[{"itemCode":"CUEY0030Q"},{"itemCode":"CUEY00002"}]}`,
		"missing list":           `{"shopid":"1"}`,
		"null list":              `{"saveItemList":null}`,
		"list is object":         `{"saveItemList":{"itemCode":"CUEY0030Q"}}`,
		"empty code":             `{"saveItemList":[{"itemCode":""}]}`,
		"no code":                `{"saveItemList":[{"invenSeq":1}]}`,
		"code not string":        `{"saveItemList":[{"itemCode":5}]}`,
		"seq string":             `{"saveItemList":[{"itemCode":"CUEY0030Q","invenSeq":"1"}]}`,
		"seq null":               `{"saveItemList":[{"itemCode":"CUEY0030Q","invenSeq":null}]}`,
		"inven item string":      `{"saveItemList":[{"itemCode":"CUEY0030Q"}],"invenItemList":["3"]}`,
		"inven item object":      `{"saveItemList":[{"itemCode":"CUEY0030Q"}],"invenItemList":{}}`,
		"bad json":               `{"saveItemList":[`,
		"top-level array":        `[{"itemCode":"CUEY0030Q"}]`,
	} {
		if code, resp := faceShopReq(t, "ftok", http.MethodPost, faceShopPurchaseURL, body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d (%s), want 400", name, code, resp)
		}
	}
	store.AccountsMu.Lock()
	items, inv := strings.Join(acc.ItemCodes, ","), strings.Join(acc.InventoryCodes, ",")
	store.AccountsMu.Unlock()
	if items != "CUEY00002" || inv != "CUEY00002,CUON004TV" {
		t.Errorf("mutated by rejected requests: %s | %s", items, inv)
	}
	for _, c := range []struct {
		token, method string
		want          int
	}{{"", http.MethodPost, 404}, {"nope", http.MethodPost, 404}, {"ftok", http.MethodGet, 404}, {"ftok", http.MethodPut, 404}} {
		if code, _ := faceShopReq(t, c.token, c.method, faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUEY0030Q"}]}`); code != c.want {
			t.Errorf("%s token=%q: %d, want %d", c.method, c.token, code, c.want)
		}
	}
}

func TestFaceShopPurchaseRollsBackOnSaveFailure(t *testing.T) {
	acc := faceShopSetup(t, "FEMALE", []string{"CUEY00002"}, []string{"CUEY00002"})
	// Make the store directory a regular file so MkdirAll/CreateTemp fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json")
	store.AccountsMu.Unlock()
	code, _ := faceShopReq(t, "ftok", http.MethodPost, faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUEY0030Q"}]}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", code)
	}
	store.AccountsMu.Lock()
	items, inv := strings.Join(acc.ItemCodes, ","), strings.Join(acc.InventoryCodes, ",")
	store.AccountsMu.Unlock()
	if items != "CUEY00002" || inv != "CUEY00002" || acc.FaceTickets != 10 {
		t.Errorf("not rolled back: %s | %s | tickets %d", items, inv, acc.FaceTickets)
	}
}

func hasCode(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
