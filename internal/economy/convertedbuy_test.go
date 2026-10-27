package economy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/itemcode"
	"cherry/internal/store"
)

// convertedDressIndex lists converted dress items (ids) in a temp index and resets the catalog, so the next
// catalog read scans it. The item folders are not written: a converted item needs no folder in the item root.
func convertedDressIndex(t *testing.T, ids ...int64) {
	t.Helper()
	m := &assets.ItemManifest{}
	for _, id := range ids {
		m.Entries = append(m.Entries, assets.ItemEntry{Kind: "dress", ID: id, Name: "dp.png", Size: 1,
			MD5: strings.Repeat("B", 32), Origin: assets.OriginCherry})
	}
	body, err := assets.EncodeItemManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "items.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	old := assets.GeneratedItemRoot
	assets.GeneratedItemRoot = root
	t.Cleanup(func() { assets.GeneratedItemRoot = old })
	catalog = nil
	t.Cleanup(func() { catalog = nil })
}

// TestConvertedTopsBuy: a converted tops item (CUTO0006W, 225000248) is listed in the Tops tab at the TO table
// price, buys at that price (or the day's deal price), and is granted to the inventory. Its folder is absent, so
// the item only exists through the converted index.
func TestConvertedTopsBuy(t *testing.T) {
	shopFixture(t, map[string][]string{})
	const code = "CUTO0006W"
	id, ok := itemcode.ID(code)
	if !ok || id != 225000248 {
		t.Fatalf("itemcode.ID(%s) = %d %v, want 225000248", code, id, ok)
	}
	convertedDressIndex(t, id)

	rec := httptest.NewRecorder()
	handleShopList(kindFashion)(rec, httptest.NewRequest(http.MethodGet, "/v4/shop/fashion/itemlist/MTOPS", nil))
	if got := listedCodes(t, rec); !slices.Contains(got, code) {
		t.Fatalf("Tops tab = %v, want %s", got, code)
	}
	price, cash, grade, ok := ItemPrice(code)
	if !ok || cash || grade != "N" || price != ActivePrices().ClothingPrices["TO"] {
		t.Fatalf("ItemPrice(%s) = %d %v %s %v, want the TO table price", code, price, cash, grade, ok)
	}

	acc, _ := phaseEAccount(t, 1000, nil)
	acc.ItemCodes = nil
	want := price
	if d, deal := dealsOn(NowFunc())[code]; deal {
		want = d
	}
	rec = postAs(handleShopBuy(kindFashion), buyBody(code, want))
	if rec.Code != http.StatusOK {
		t.Fatalf("buy %s at %d: status %d %s", code, want, rec.Code, rec.Body.String())
	}
	if acc.Gems != 1000-want {
		t.Errorf("gems = %d, want %d after the buy", acc.Gems, 1000-want)
	}
	if !slices.Contains(store.AccountOwnedCodes(acc), code) {
		t.Errorf("owned = %v, want %s granted", store.AccountOwnedCodes(acc), code)
	}
}
