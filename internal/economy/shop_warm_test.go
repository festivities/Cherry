package economy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cherry/internal/testutil"
)

// TestShopListReadsWarmCatalog fails if a shop list request rebuilds the catalog or finds a pet unclassified: the first
// Fashion Shop request of 2026-11-02 took 37 s because the pet categories were read from the store inside the request.
// A list request reads the catalog that startup built (WarmArchive) and nothing else.
func TestShopListReadsWarmCatalog(t *testing.T) {
	testutil.UseArchive(t)
	catalog = nil
	t.Cleanup(func() { catalog = nil })

	built := loadCatalog() // what startup does
	if len(built.pet) == 0 {
		t.Fatal("the archive lists no pets; the test has nothing to check")
	}
	for _, it := range built.pet {
		if it.Catg == "" {
			t.Fatalf("pet %s is not classified when the catalog is built", it.Code)
		}
	}
	for _, list := range []struct {
		kind byte
		path string
	}{
		{kindFashion, "/v4/shop/fashion/itemlist/SET?isFull=false&paymentType=ALL"},
		{kindFashion, "/v4/shop/fashion/itemlist/MTOPS?isFull=false&paymentType=ALL"},
		{kindInterior, "/v4/shop/interior/itemlist/URCMD"},
		{kindPet, "/v4/shop/pet/itemlist/UDOGINT"},
	} {
		rec := httptest.NewRecorder()
		handleShopList(list.kind)(rec, httptest.NewRequest(http.MethodGet, list.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", list.path, rec.Code)
		}
		if loadCatalog() != built {
			t.Fatalf("%s rebuilt the shop catalog", list.path)
		}
	}
}
