package web

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/itemcode"
	"cherry/internal/store"
)

// convertedFaceIndex lists the given custom items in a temp converted index (entries only) and points the
// generated item root at it for the test.
func convertedFaceIndex(t *testing.T, codes ...string) {
	t.Helper()
	m := &assets.ItemManifest{}
	for _, c := range codes {
		id, ok := itemcode.ID(c)
		if !ok {
			t.Fatalf("no item id for %s", c)
		}
		m.Entries = append(m.Entries, assets.ItemEntry{Kind: "custom", ID: id, Name: "dp.png", Size: 1,
			MD5: strings.Repeat("A", 32), Origin: assets.OriginCherry})
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
}

// TestFaceShopSellsConvertedFace: a converted face the index lists is in the shop data, buys with one ticket,
// is granted and worn; a face the index does not list is refused with 400 and changes nothing.
func TestFaceShopSellsConvertedFace(t *testing.T) {
	convertedFaceIndex(t, "CUEY0000A")
	acc := faceShopSetup(t, "FEMALE", []string{"CUEY00002"}, []string{"CUEY00002"})

	code, body := faceShopReq(t, "ftok", http.MethodGet, "/v4/faceshop/v2/shop/1?shopLocation=HOME", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"cd":"CUEY0000A"`) {
		t.Fatalf("shop data status %d, converted face missing: %.200s", code, body)
	}

	if code, resp := faceShopReq(t, "ftok", http.MethodPost, faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUEY0000A"}]}`); code != http.StatusOK {
		t.Fatalf("buy converted face: status %d: %s", code, resp)
	}
	if acc.FaceTickets != 9 {
		t.Errorf("tickets = %d, want 9 after one face", acc.FaceTickets)
	}
	if !slices.Contains(acc.ItemCodes, "CUEY0000A") || slices.Contains(acc.ItemCodes, "CUEY00002") {
		t.Errorf("worn = %v, want the converted eye face in place of CUEY00002", acc.ItemCodes)
	}
	if !slices.Contains(store.AccountOwnedCodes(acc), "CUEY0000A") {
		t.Errorf("owned = %v, want the converted eye face granted", store.AccountOwnedCodes(acc))
	}

	if code, resp := faceShopReq(t, "ftok", http.MethodPost, faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUEY0000B"}]}`); code != http.StatusBadRequest {
		t.Errorf("unlisted face: status %d (%s), want 400", code, resp)
	}
	if code, resp := faceShopReq(t, "ftok", http.MethodPost, faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUEY0000A"}]}`); code != http.StatusOK {
		t.Fatalf("repeat of the worn face: status %d: %s", code, resp)
	}
	if acc.FaceTickets != 9 {
		t.Errorf("tickets = %d after a repeat of the worn face, want 9 (nothing new bought)", acc.FaceTickets)
	}
}
