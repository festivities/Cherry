package economy

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// PLAN 8.6b part 5: a face shop purchase with one list over faceShopMaxList is refused as malformed, and the Circle aid
// lists are deduplicated in order with a set (not a scan per aid).
func TestFaceShopPurchaseListCap(t *testing.T) {
	repeat := func(n int, one string) string {
		return "[" + strings.TrimSuffix(strings.Repeat(one+",", n), ",") + "]"
	}
	for name, body := range map[string]string{
		"saveItemList":  `{"saveItemList":` + repeat(faceShopMaxList+1, `{"itemCode":"HE14000AA","invenSeq":0}`) + `,"invenItemList":[]}`,
		"invenItemList": `{"saveItemList":[],"invenItemList":` + repeat(faceShopMaxList+1, `1`) + `}`,
	} {
		rec := httptest.NewRecorder()
		HandleFaceShopPurchase(rec, httptest.NewRequest(http.MethodPost, "/v4/faceshop/saveAndPurchase/v2", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s of %d = %d, want 400", name, faceShopMaxList+1, rec.Code)
		}
	}
}

func TestUniqueAidsKeepsFirstOrder(t *testing.T) {
	got := uniqueAids([]string{"3", "1", "3", "2", "1"})
	if !slices.Equal(got, []string{"3", "1", "2"}) {
		t.Fatalf("uniqueAids = %v, want [3 1 2]", got)
	}
	if got := uniqueAids(nil); got == nil || len(got) != 0 {
		t.Fatalf("uniqueAids(nil) = %#v, want an empty non-nil list", got)
	}
}
