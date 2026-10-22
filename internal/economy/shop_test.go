package economy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// shopFixture points the catalog at a temp item root: files maps "kind/id" to the file names in it.
// The 2014 archive and the thumbnail cache are pointed at empty temp paths too, so that the real
// backup cannot give a fixture item its art.
func shopFixture(t *testing.T, files map[string][]string) {
	t.Helper()
	root := t.TempDir()
	oldZip, oldCache := thumbs.DpZipPath, thumbs.DpCacheDir
	thumbs.DpZipPath, thumbs.DpCacheDir = filepath.Join(t.TempDir(), "none.zip"), t.TempDir()
	t.Cleanup(func() { thumbs.DpZipPath, thumbs.DpCacheDir = oldZip, oldCache })
	for dir, names := range files {
		d := filepath.Join(root, filepath.FromSlash(dir))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(d, n), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := thumbs.DpItemRoot
	thumbs.DpItemRoot = root
	t.Cleanup(func() { thumbs.DpItemRoot = old })
}

func TestItemCodeFromID(t *testing.T) {
	for id, want := range map[int64]string{
		225106259: "CUON004TV", 121400639: "RUWA000HR", 326400016: "PUPE0000G", 223800005: "CUHE00005", 235100001: "CAON00001",
	} {
		if got, ok := itemCodeFromID(id); !ok || got != want {
			t.Errorf("itemCodeFromID(%d) = %q %v, want %q", id, got, ok, want)
		}
	}
	for _, id := range []int64{0, 999, 225900001, 999999999} {
		if got, ok := itemCodeFromID(id); ok {
			t.Errorf("itemCodeFromID(%d) = %q, want unclassifiable", id, got)
		}
	}
}

func TestItemPriceTable(t *testing.T) {
	shopFixture(t, map[string][]string{
		"dress/225106259":    {"dp.png", "x.aniproj"},    // CUON004TV animated one-piece
		"dress/225000001":    {"dp.png"},                 // CUTO00001
		"dress/235100001":    {"dp.png"},                 // CAON00001 animal one-piece
		"custom/224400001":   {"dp.png"},                 // CUHA00001
		"custom/223800001":   {"dp.png"},                 // CUHE00001 face: not sold here
		"interior/121400639": {"dp.png"},                 // RUWA000HR wall
		"interior/121700005": {"dp.png"},                 // RUTD00005 floor decor
		"interior/122000002": {"dp.png", "a.aniproj"},    // RUGO00002 animated furniture
		"tile/121500101":     {"dp.png"},                 // RUTI0002T floor
		"pet/326400016":      {"skeleton.xml", "dp.png"}, // PUPE0000G
		"pet/326400230":      {"skeleton.xml", "r.aniproj", "dp.png"},
		"pet/326400300":      {"dp.png"}, // no render assets: not in the catalog
	})
	cases := []struct {
		code  string
		price int64
		cash  bool
		grade string
		ok    bool
	}{
		{"CUTO00001", 150, false, "N", true},
		{"CUHA00001", 200, false, "N", true},
		{"CUON004TV", 625, false, "R", true}, // 250 * 2.5
		{"CAON00001", 250, false, "N", true},
		{"CUSH00001", 120, false, "N", true}, // not in the fixture: plain table price
		{"CUHE00001", 0, false, "N", false},
		{"RUWA000HR", 100, false, "N", true},
		{"RUTI0002T", 100, false, "N", true},
		{"RUTD00005", 150, false, "N", true},
		{"RUGO00002", 375, false, "R", true},
		{"PUPE0000G", 1200, false, "N", true},
		{"PUPE0006E", 25, true, "R", true}, // ride pet (230 = 6E in base36)
		{"BOGUS", 0, false, "N", false},
	}
	for _, c := range cases {
		price, cash, grade, ok := ItemPrice(c.code)
		if price != c.price || cash != c.cash || grade != c.grade || ok != c.ok {
			t.Errorf("ItemPrice(%s) = %d %v %s %v, want %d %v %s %v", c.code, price, cash, grade, ok, c.price, c.cash, c.grade, c.ok)
		}
	}
	cat := loadCatalog()
	if cat.byCode["PUPE0000G"] == nil || cat.byCode["PUPE0008C"] != nil {
		t.Errorf("pet catalog = %v (render-less pet must be absent)", cat.pet)
	}
	if len(cat.fashion) != 4 || len(cat.interior) != 4 || len(cat.pet) != 2 {
		t.Errorf("catalog sizes fashion %d interior %d pet %d, want 4/4/2", len(cat.fashion), len(cat.interior), len(cat.pet))
	}
}

func TestMissingItemRootIsEmpty(t *testing.T) {
	old := thumbs.DpItemRoot
	thumbs.DpItemRoot = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { thumbs.DpItemRoot = old })
	if c := loadCatalog(); len(c.fashion)+len(c.interior)+len(c.pet) != 0 {
		t.Fatalf("catalog = %+v, want empty", c)
	}
	if price, _, _, ok := ItemPrice("CUTO00001"); price != 150 || !ok {
		t.Errorf("price without catalog = %d %v", price, ok)
	}
}

// noArtFixture has art items (dp.png) next to items with no art at all: no dp.png and nothing the
// generator can draw. CUON004SR is the archive's dress case (225106219), RUTD00005 a furniture one.
func noArtFixture(t *testing.T) {
	t.Helper()
	shopFixture(t, map[string][]string{
		"dress/225000001":    {"dp.png"},
		"dress/225106219":    {"a.png"},
		"dress/225106259":    {"x.aniproj"}, // CUON004TV animated, no art: still priced 625 R
		"interior/121400639": {"dp.png"},
		"interior/121700005": {"a.png"},
		"pet/326400016":      {"skeleton.xml", "dp.png"},
		"pet/326400017":      {"skeleton.xml"},
	})
}

// listedCodes returns the item codes of a shop itemlist reply.
func listedCodes(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var out struct {
		Result struct {
			Items []struct {
				ItemCode string `json:"itemCode"`
			} `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("itemlist body %q: %v", rec.Body.String(), err)
	}
	var codes []string
	for _, it := range out.Result.Items {
		codes = append(codes, it.ItemCode)
	}
	return codes
}

// TestNoArtItemsNotSoldOrGranted: items without art leave the catalogs, the gacha pool, buy, mail and
// season rewards, but keep their price so that an owned one still sells back.
func TestNoArtItemsNotSoldOrGranted(t *testing.T) {
	noArtFixture(t)
	cat := loadCatalog()
	if len(cat.fashion) != 1 || len(cat.interior) != 1 || len(cat.pet) != 1 || cat.byCode["CUON004SR"] != nil || cat.byCode["RUTD00005"] != nil {
		t.Fatalf("catalog fashion %d interior %d pet %d, want one art item each", len(cat.fashion), len(cat.interior), len(cat.pet))
	}
	if price, _, _, ok := ItemPrice("CUON004SR"); price != 250 || !ok {
		t.Errorf("ItemPrice(CUON004SR) = %d %v, want 250 for sell-back", price, ok)
	}
	if price, cash, grade, ok := ItemPrice("CUON004TV"); price != 625 || cash || grade != "R" || !ok {
		t.Errorf("ItemPrice(CUON004TV) = %d %v %s %v, want the animated 625 R for sell-back", price, cash, grade, ok)
	}

	list := func(h http.HandlerFunc, path string) []string {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return listedCodes(t, rec)
	}
	if got := list(handleShopList(kindFashion), "/v4/shop/fashion/itemlist/MDRESS"); len(got) != 0 {
		t.Errorf("no-art outfit listed: %v", got)
	}
	if got := list(handleShopList(kindFashion), "/v4/shop/fashion/itemlist/MTOPS"); !slices.Equal(got, []string{"CUTO00001"}) {
		t.Errorf("tops list = %v", got)
	}
	if got := list(handleShopList(kindInterior), "/v4/shop/interior/itemlist/"+room.RoomCategoryCode("RUTD00005")); len(got) != 0 {
		t.Errorf("no-art furniture listed: %v", got)
	}
	if got := list(handleShopList(kindPet), "/v4/shop/interior/petitemlist/UPETINT"); !slices.Equal(got, []string{"PUPE0000G"}) {
		t.Errorf("pet list = %v", got)
	}

	acc, _ := phaseEAccount(t, 5000, []string{"CUON004SR"})
	acc.ItemCodes = nil // owned, not worn: the closet may sell it
	buy := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Cookie", `AV_AUTH="ptok"`)
		rec := httptest.NewRecorder()
		handleShopBuy(kindFashion)(rec, req)
		return rec
	}
	if rec := buy("/v4/purchase/dress/buy", `{"items":[{"itemCode":"CUON004SR","price":250}]}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errUnknown) {
		t.Errorf("buy no-art = %d %s, want 400 %s", rec.Code, rec.Body.String(), errUnknown)
	}
	if rec := buy("/v4/purchase/dress/buy", `{"items":[{"itemCode":"CUTO00001","price":150}]}`); rec.Code != http.StatusOK {
		t.Errorf("buy art = %d %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/v4/inven/recycle/closet/v2", strings.NewReader(`{"recycleItems":[{"invenSeq":"0","itemCode":"CUON004SR","count":1}]}`))
	req.Header.Set("Cookie", `AV_AUTH="ptok"`)
	rec := httptest.NewRecorder()
	handleRecycle(false)(rec, req)
	if rec.Code != http.StatusOK || acc.Gems != 5000-150+250 {
		t.Errorf("sell-back of owned no-art item = %d gems %d, want 5100 after the 150 buy", rec.Code, acc.Gems)
	}

	if pool := gachaPoolNow(); !slices.Equal(gachaCodes(pool.normal), []string{"CUTO00001", "RUWA000HR"}) || len(pool.rare) != 0 {
		t.Errorf("gacha pool normal %v rare %v", gachaCodes(pool.normal), gachaCodes(pool.rare))
	}
	if validMailPayload(store.MailTypeItem, 0, "CUON004SR") || !validMailPayload(store.MailTypeItem, 0, "CUTO00001") {
		t.Error("mail item validation")
	}
	if seasonItemAvailable("RUTD00005") || !seasonItemAvailable("RUWA000HR") {
		t.Error("season item validation")
	}
	if _, err := itemGrantLocked(acc, "CUON004SR"); err != errItemUnknown {
		t.Errorf("grant of no-art item err = %v, want errItemUnknown", err)
	}

	mail := httptest.NewRequest(http.MethodPost, "/admin/mail", strings.NewReader(`{"aid":"9901","type":"item","item":"CUON004SR","message":"x"}`))
	mail.RemoteAddr = "127.0.0.1:5555"
	rec = httptest.NewRecorder()
	handleAdminMail(rec, mail)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("admin mail of no-art item = %d, want 400", rec.Code)
	}
}
