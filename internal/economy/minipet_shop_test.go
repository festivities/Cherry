package economy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// miniPetShopTab is the Minipet Shop tab (button) that lists a pet category.
var miniPetShopTab = map[string]string{"UDOG": "UDOGINT", "UTOY": "UDOGINT", "UCAT": "UCATINT", "URIDE": "URIDEINT"}

// TestMiniPetShopRowsAndBuy: the Minipet Shop lists each paired pet by its room item at the pet's price; the item is
// not in the interior shop; a buy grants the room item once (a second copy is refused with 61009).
func TestMiniPetShopRowsAndBuy(t *testing.T) {
	testutil.UseArchive(t)
	testutil.InstallAccounts(t, map[string]*store.Account{"mp-shop": {Aid: "7005", Gems: 100000}})
	testutil.SetLab(t, "7005", false)

	c := loadCatalog()
	if len(c.minipets) == 0 {
		t.Fatal("no Minipet with its item and art in the archive")
	}
	mp := c.minipets[0]
	item := mp.item.Code
	pet := mp.pet.Code
	price, _ := miniPetPrice(pet)
	for _, it := range c.interior {
		if it.Code == item {
			t.Fatalf("Minipet item %s is in the interior shop", item)
		}
	}

	tab := miniPetShopTab[mp.pet.Catg]
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v4/shop/interior/petitemlist/"+tab+"?isFull=false", nil)
	req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: "mp-shop"})
	handleShopList(kindPet)(rec, req)
	row := shopRowOf(t, rec, item)
	want := map[string]any{"itemCode": item, "petSkinCode": pet, "petSkinId": float64(room.PetSkinID(pet)),
		"raceId": float64(1), "catgCd": mp.pet.Catg, "price": float64(price), "paymentType": "GEM", "itemType": "ROOM",
		"purchasedYn": false, "existInven": false, "linkPosX": float64(0), "linkPosY": float64(0), "linkScale": float64(100)}
	for k, v := range want {
		if row[k] != v {
			t.Fatalf("shop row %s = %v, want %v (row %v)", k, row[k], v, row)
		}
	}

	buy := `{"deviceType":"x","itemType":"ROOM","items":[{"itemCode":"` + item + `","price":` + itoaShop(price) + `,"cpId":"avtplay"}],"language":"en"}`
	bought := shopBuy(t, "mp-shop", buy)
	if bought.Code != http.StatusOK || !strings.Contains(bought.Body.String(), `"resultCode":0`) {
		t.Fatalf("buy: %d %s", bought.Code, bought.Body)
	}
	if rec := shopBuy(t, "mp-shop", buy); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errOwned) {
		t.Fatalf("second copy: %d %s, want %s", rec.Code, rec.Body, errOwned)
	}
	store.AccountsMu.Lock()
	granted := false
	for _, it := range store.AccountByTokenLocked("mp-shop").RoomItems {
		granted = granted || it.Cd == item
	}
	store.AccountsMu.Unlock()
	if !granted {
		t.Fatalf("buy did not grant room item %s", item)
	}

	rec = httptest.NewRecorder()
	handleShopList(kindPet)(rec, req)
	if row := shopRowOf(t, rec, item); row["purchasedYn"] != true || row["existInven"] != true {
		t.Fatalf("owned row %v", row)
	}
	if got, _ := MiniPetOfItem(item); got != pet {
		t.Fatalf("MiniPetOfItem(%s) = %s, want %s", item, got, pet)
	}
}

// shopRowOf finds the row of code in a shop list reply.
func shopRowOf(t *testing.T, rec *httptest.ResponseRecorder, code string) map[string]any {
	t.Helper()
	var env struct {
		Result struct {
			Items []map[string]any `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("shop list %s: %v", rec.Body, err)
	}
	for _, row := range env.Result.Items {
		if row["itemCode"] == code {
			return row
		}
	}
	t.Fatalf("no shop row for %s in %s", code, rec.Body)
	return nil
}

// shopBuy posts an interior buy for the token's account.
func shopBuy(t *testing.T, tok, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v4/purchase/interior/buy", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: tok})
	handleShopBuy(kindInterior)(rec, req)
	return rec
}

// itoaShop is the decimal form of n for a JSON body.
func itoaShop(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestMiniPetItemNameAndCategory verifies RUPI item names use the pet's display name and
// RoomItemCategory maps paired RUPI items to their pet-subtype *INT categoryCode.
func TestMiniPetItemNameAndCategory(t *testing.T) {
	root := shopFixture(t, map[string][]string{
		"interior/122900007": {"dp.png"},
		"pet/326400016":      {"skeleton.xml", "dp.png", "behavior.xml"}, // PUPE0000G (Mixed Breed)
	})
	if err := os.WriteFile(filepath.Join(root, "pet/326400016/behavior.xml"), []byte(`{"sndindex":215}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ItemName("RUPI00007"); got != "Mixed Breed" {
		t.Fatalf("ItemName(RUPI00007) = %q, want %q", got, "Mixed Breed")
	}
	if got := RoomItemCategory("RUPI00007"); got != "UDOGINT" {
		t.Fatalf("RoomItemCategory(RUPI00007) = %q, want %q", got, "UDOGINT")
	}
	if got, want := RoomItemCategory("RUCH0011X"), room.RoomCategoryCode("RUCH0011X"); got != want {
		t.Fatalf("RoomItemCategory(RUCH0011X) = %q, want %q", got, want)
	}

	testutil.InstallAccounts(t, map[string]*store.Account{"mp-inven": {
		Aid:       "7010",
		RoomItems: []store.RoomItem{{Seq: 100000, Cd: "RUPI00007"}, {Seq: 100001, Cd: "RUCH0011X"}},
	}})
	testutil.SetLab(t, "7010", false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v4/inven/interior/items/all", nil)
	req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: "mp-inven"})
	room.InvenInteriorHandlerWithCategory(RoomItemPrice, RoomItemCategory)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inven status %d: %s", rec.Code, rec.Body)
	}
	var env struct {
		Result []struct {
			Cd           string `json:"cd"`
			CategoryCode string `json:"categoryCode"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	cats := map[string]string{}
	for _, r := range env.Result {
		cats[r.Cd] = r.CategoryCode
	}
	if cats["RUPI00007"] != "UDOGINT" || cats["RUCH0011X"] != "UCHAIR" {
		t.Fatalf("inventory categoryCodes = %v, want RUPI00007:UDOGINT and RUCH0011X:UCHAIR", cats)
	}
}
