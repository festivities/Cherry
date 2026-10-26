package economy

import (
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/store"
	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

// TestLandscapeCodeFormula: a landscape's numeric id is 4e8+2e7+84e5 plus the base-36 value of its tail,
// and itemCodeFromID inverts it. Only canonical codes parse: lower case, other characters and any other
// length fail the round trip.
func TestLandscapeCodeFormula(t *testing.T) {
	for _, l := range listedLandscapes() {
		tail, err := strconv.ParseInt(l.Code[4:], 36, 64)
		if err != nil || l.ID != 428400000+tail {
			t.Errorf("%s id = %d, want 428400000 + base36(%s) = %d", l.Code, l.ID, l.Code[4:], 428400000+tail)
		}
		if back, ok := itemCodeFromID(l.ID); !ok || back != l.Code {
			t.Errorf("itemCodeFromID(%d) = %q %v, want %s", l.ID, back, ok, l.Code)
		}
	}
	for code, want := range map[string]int64{"GULS00003": 428400003, "GULS0005U": 428400210, "GULS0255E": 428499986, "GULS0255R": 428499999} {
		if id, ok := landscapeItemID(code); !ok || id != want {
			t.Errorf("landscapeItemID(%s) = %d %v, want %d", code, id, ok, want)
		}
	}
	for _, code := range []string{"GULS0255r", "GULS0000!", "GULS00000Z", "GULS000", "GULS0000", "GUTI00003", "RUGO00B05"} {
		if id, ok := landscapeItemID(code); ok {
			t.Errorf("landscapeItemID(%q) = %d, want rejected", code, id)
		}
	}
}

// TestGardensLandscapeData: the shipped gardens.json lists 24 landscapes, 10 original and 14 generated,
// each once and named. GULS0004V and GULS0005B (no map) are not sold.
func TestGardensLandscapeData(t *testing.T) {
	var g Gardens
	if err := json.Unmarshal(data.GardensJSON, &g); err != nil {
		t.Fatal(err)
	}
	if err := checkGardens(&g); err != nil {
		t.Fatalf("shipped gardens.json: %v", err)
	}
	lands := listedLandscapes()
	var generated []int64
	for _, l := range lands {
		if l.Name == "" {
			t.Errorf("%s has no name", l.Code)
		}
		if l.Generated {
			generated = append(generated, l.ID)
		}
	}
	if len(lands) != 24 || len(generated) != 14 || len(lands)-len(generated) != 10 {
		t.Fatalf("%d landscapes, %d generated, want 24 and 14", len(lands), len(generated))
	}
	slices.Sort(generated)
	if generated[0] != 428499986 || generated[13] != 428499999 {
		t.Errorf("generated ids %v, want 428499986..428499999", generated)
	}
	for _, code := range []string{"GULS0004V", "GULS0005B"} {
		if _, ok := listedLandscape(code); ok {
			t.Errorf("%s is listed, want it unsold", code)
		}
	}
	if l, ok := listedLandscape("GULS00003"); !ok || l.Map != DefaultGardenMap() || l.Name != "Classic meadow" {
		t.Errorf("GULS00003 = %+v, want the default map's Classic meadow", l)
	}
}

// TestCheckGardensRejectsBadLandscapes: a landscape code listed twice, one that is not a GULS code, a
// lower-case code, a landscape without a name, a generated map without a landscape, a generated landscape
// outside Cherry's id block and an original inside it are refused.
func TestCheckGardensRejectsBadLandscapes(t *testing.T) {
	base := GardenEntry{Map: "1908231803", Label: "x", Name: "A", Landscape: "GULS00003"}
	for name, e := range map[string]GardenEntry{
		"duplicate code":             {Map: "1908231804", Label: "x", Name: "B", Landscape: "GULS00003"},
		"not GULS":                   {Map: "1908231804", Label: "x", Name: "B", Landscape: "GUTI00003"},
		"lower case":                 {Map: "1908231804", Label: "x", Name: "B", Landscape: "GULS0255r"},
		"no name":                    {Map: "1908231804", Label: "x", Landscape: "GULS0255R"},
		"generated only":             {Map: "1908231804", Label: "x", Name: "B", Generated: true},
		"generated original id":      {Map: "1908231804", Label: "x", Name: "B", Landscape: "GULS0000Y", Generated: true},
		"original in Cherry's block": {Map: "1908231804", Label: "x", Name: "B", Landscape: "GULS0255R"},
	} {
		if err := checkGardens(&Gardens{Default: "1908231803", Gardens: []GardenEntry{base, e}}); err == nil {
			t.Errorf("%s: checkGardens accepted %+v", name, e)
		}
	}
}

// landscapeRows is a shop reply's rows for the landscapes, by item code.
func landscapeRows(t *testing.T, rec *httptest.ResponseRecorder) map[string]map[string]any {
	t.Helper()
	var out struct {
		Result struct {
			Items []map[string]any `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("itemlist body %q: %v", rec.Body.String(), err)
	}
	rows := map[string]map[string]any{}
	for _, row := range out.Result.Items {
		if code, _ := row["itemCode"].(string); strings.HasPrefix(code, "GU") {
			rows[code] = row
		}
	}
	return rows
}

// TestLandscapeShopRows: the Interior Shop's default tab lists the configured landscapes the account does
// not own, at 30 Cash; the default map's landscape (owned for free) is left out; other tabs list none.
func TestLandscapeShopRows(t *testing.T) {
	shopFixture(t, nil)
	phaseEAccount(t, 0, nil)
	list := func(cat string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v4/shop/interior/itemlist/"+cat, nil)
		req.Header.Set("Cookie", `AV_AUTH="ptok"`)
		rec := httptest.NewRecorder()
		handleShopList(kindInterior)(rec, req)
		return rec
	}
	rows := landscapeRows(t, list("URCMD"))
	if len(rows) != 23 {
		t.Fatalf("landscape rows = %d, want the 23 unowned gardens.json landscapes", len(rows))
	}
	for code, row := range rows {
		if row["price"] != float64(30) || row["discountedPrice"] != float64(30) || row["paymentType"] != "CASH" {
			t.Errorf("%s row = %v, want 30 Cash", code, row)
		}
	}
	if row, ok := rows["GULS00003"]; ok {
		t.Errorf("owned default landscape is listed: %v", row)
	}
	if row := rows["GULS0255R"]; row["purchased"] != false {
		t.Errorf("for-sale landscape row = %v", row)
	}
	if rows := landscapeRows(t, list("UPROP")); len(rows) != 0 {
		t.Errorf("UPROP tab lists %d landscapes", len(rows))
	}
}

// TestLandscapeBuy: a landscape costs 30 Cash once, the ledger records one Cash line, and the landscape
// joins the account. Owned, unlisted, short-of-Cash and wrong-price buys are refused, Gems never pay.
func TestLandscapeBuy(t *testing.T) {
	shopFixture(t, nil)
	acc, dir := phaseEAccount(t, 0, nil)
	acc.Cash = 60
	buy := handleShopBuy(kindInterior)
	roomItems := len(acc.RoomItems)

	if rec := postAs(buy, buyBody("GULS0255R", 30)); rec.Code != http.StatusOK || acc.Cash != 30 {
		t.Fatalf("buy = %d %s, cash %d", rec.Code, rec.Body.String(), acc.Cash)
	}
	if !slices.Equal(acc.Landscapes, []string{"GULS0255R"}) || len(acc.RoomItems) != roomItems || acc.Gems != 0 {
		t.Errorf("after buy: landscapes %v, room items %d, gems %d", acc.Landscapes, len(acc.RoomItems), acc.Gems)
	}
	if lines := gachaLedger(t, dir); len(lines) != 1 || lines[0].Currency != "cash" || lines[0].Delta != -30 || lines[0].Reason != "shop:landscape:GULS0255R" {
		t.Errorf("ledger = %+v, want one cash line of -30", lines)
	}

	owned := []string{buyBody("GULS0255R", 30), buyBody("GULS00003", 30), `{"items":[{"itemCode":"GULS0255Q","price":30},{"itemCode":"GULS0255Q","price":30}]}`}
	for _, body := range owned {
		if rec := postAs(buy, body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errOwned) {
			t.Errorf("owned buy %s = %d %s, want %s", body, rec.Code, rec.Body.String(), errOwned)
		}
	}
	if rec := postAs(buy, buyBody("GULS0004V", 30)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errUnknown) {
		t.Errorf("unlisted landscape = %d %s, want %s", rec.Code, rec.Body.String(), errUnknown)
	}
	if rec := postAs(buy, buyBody("GULS0255Q", 1)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errUpdated) {
		t.Errorf("wrong price = %d %s, want %s", rec.Code, rec.Body.String(), errUpdated)
	}
	acc.Cash = 29
	if rec := postAs(buy, buyBody("GULS0255Q", 30)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errNoMoney) || acc.Cash != 29 || len(acc.Landscapes) != 1 {
		t.Errorf("short of cash = %d %s, cash %d, landscapes %v", rec.Code, rec.Body.String(), acc.Cash, acc.Landscapes)
	}
	acc.Cash, acc.Gems = 0, 500
	if rec := postAs(buy, buyBody("GULS0255Q", 30)); rec.Code != http.StatusBadRequest || acc.Gems != 500 {
		t.Errorf("gems paid for a landscape: %d %s, gems %d", rec.Code, rec.Body.String(), acc.Gems)
	}
	acc.Cash = 30
	if rec := postAs(buy, buyBody("GULS0255Q", 30)); rec.Code != http.StatusOK || acc.Cash != 0 {
		t.Errorf("second landscape = %d %s, cash %d", rec.Code, rec.Body.String(), acc.Cash)
	}
}

// TestLandscapeName: a listed landscape is named by gardens.json; the shop's name route serves it.
func TestLandscapeName(t *testing.T) {
	if got := ItemName("GULS0255R"); got != "Classic meadow night" {
		t.Errorf("ItemName(GULS0255R) = %q", got)
	}
	if p, cash, grade, ok := ItemPrice("GULS0255R"); p != 30 || !cash || grade != "N" || !ok {
		t.Errorf("ItemPrice(GULS0255R) = %d %v %s %v, want 30 Cash", p, cash, grade, ok)
	}
	if _, _, _, ok := ItemPrice("GULS0004V"); ok {
		t.Error("ItemPrice(GULS0004V) priced an unlisted landscape")
	}
}

// TestOwnedLandscapes: an account owns the landscapes of its default map and of its current map, plus
// the ones it bought, in gardens.json order. An unknown account owns none.
func TestOwnedLandscapes(t *testing.T) {
	acc := &store.Account{AccessToken: "lstok", SessionKey: "sk", Aid: "9601", Name: "Own"}
	testutil.InstallAccounts(t, map[string]*store.Account{"lstok": acc})
	codes := func() []string {
		var out []string
		for _, l := range OwnedLandscapes("9601") {
			out = append(out, l.Code)
		}
		return out
	}
	if got := codes(); !slices.Equal(got, []string{"GULS00003"}) {
		t.Fatalf("default owned = %v", got)
	}
	acc.GardenMap = "2401290846"
	if got := codes(); !slices.Equal(got, []string{"GULS00003", "GULS0005U"}) {
		t.Fatalf("with the current map owned = %v", got)
	}
	acc.Landscapes = []string{"GULS0255R"}
	if got := codes(); !slices.Equal(got, []string{"GULS00003", "GULS0255R", "GULS0005U"}) {
		t.Fatalf("with a bought landscape owned = %v", got)
	}
	if stem, ok := OwnedLandscapeMap("9601", "GULS0005U"); !ok || stem != "2401290846" {
		t.Errorf("OwnedLandscapeMap(GULS0005U) = %q %v", stem, ok)
	}
	if _, ok := OwnedLandscapeMap("9601", "GULS0255E"); ok {
		t.Error("an unowned landscape previews")
	}
	if got := OwnedLandscapes("9999"); got != nil {
		t.Errorf("unknown account owns %v", got)
	}
}

// TestSetGardenMap: the editor's change sets and saves the owner's map; an unlisted map and an unknown
// account are refused and change nothing.
func TestSetGardenMap(t *testing.T) {
	acc := &store.Account{AccessToken: "smtok", SessionKey: "sk", Aid: "9602", Name: "Set"}
	testutil.InstallAccounts(t, map[string]*store.Account{"smtok": acc})
	store.AccountsMu.Lock()
	store.AccountStorePath, store.NextAvatarID = filepath.Join(t.TempDir(), "accounts.json"), 9602
	store.AccountsMu.Unlock()
	if err := SetGardenMap("9602", "2303271138"); err != nil || GardenMap("9602") != "2303271138" {
		t.Fatalf("set: %v, map %s", err, GardenMap("9602"))
	}
	if err := store.LoadAccountsFrom(store.AccountStorePath); err != nil || GardenMap("9602") != "2303271138" {
		t.Fatalf("reload: %v, map %s", err, GardenMap("9602"))
	}
	if err := SetGardenMap("9602", "1234567890"); err == nil {
		t.Error("an unlisted map was accepted")
	}
	if err := SetGardenMap("9999", "1908231803"); err == nil {
		t.Error("an unknown account was accepted")
	}
	if GardenMap("9602") != "2303271138" {
		t.Errorf("map after refusals = %s", GardenMap("9602"))
	}
}

// TestServeLandscapeFiles: a generated landscape's item files come from Cherry: the artsitem is the
// archive template's bytes and dp.png is a 130x100 PNG. Original landscapes, other kinds and other
// files fall through or 404 here, and a POST is never answered.
func TestServeLandscapeFiles(t *testing.T) {
	if !thumbs.LandscapeArchived("1908231804") {
		t.Skip("no map pack in the archive: the dp.png renders from it")
	}
	oldCache := thumbs.DpCacheDir
	thumbs.DpCacheDir = t.TempDir()
	t.Cleanup(func() { thumbs.DpCacheDir = oldCache })
	root := t.TempDir()
	const template = "1409 artsitem template bytes"
	if err := os.MkdirAll(filepath.Join(root, "garden", "428400149"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "garden", "428400149", "1409_iteminfo.artsitem"), []byte(template), 0o644); err != nil {
		t.Fatal(err)
	}
	old := thumbs.DpItemRoot
	thumbs.DpItemRoot = root
	t.Cleanup(func() { thumbs.DpItemRoot = old })

	get := func(method, path string) (*httptest.ResponseRecorder, bool) {
		rec := httptest.NewRecorder()
		answered := ServeLandscapeFile(rec, httptest.NewRequest(method, path, nil))
		return rec, answered
	}
	rec, ok := get(http.MethodGet, "/img/read/arts_item_garden_428499999/1409_iteminfo.artsitem")
	if !ok || rec.Code != http.StatusOK || rec.Body.String() != template {
		t.Fatalf("artsitem = %v %d %q", ok, rec.Code, rec.Body.String())
	}
	rec, ok = get(http.MethodGet, "/arts_item_garden_428499999/dp.png")
	if !ok || rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("dp.png = %v %d %s", ok, rec.Code, rec.Header().Get("Content-Type"))
	}
	if cfg, err := png.DecodeConfig(rec.Body); err != nil || cfg.Width != 130 || cfg.Height != 100 {
		t.Fatalf("dp.png decode: %v %+v", err, cfg)
	}
	if rec, ok = get(http.MethodGet, "/img/read/arts_item_garden_428499999/lpitem.aniproj"); !ok || rec.Code != http.StatusNotFound {
		t.Errorf("unknown file of a landscape = %v %d, want 404", ok, rec.Code)
	}
	for _, path := range []string{
		"/img/read/arts_item_garden_428400003/dp.png", // original landscape: the normal path
		"/img/read/arts_item_custom_428499999/dp.png", // not the garden kind
		"/img/read/arts_item_garden_428499999",        // no file
		"/img/read/arts_item_garden_42849999x/dp.png", // not digits
		"/img/read/arts_item_garden_428400149/dp.png", // the template's own item: an original, not generated
	} {
		if _, ok := get(http.MethodGet, path); ok {
			t.Errorf("%s answered by the landscape route", path)
		}
	}
	if _, ok := get(http.MethodPost, "/arts_item_garden_428499999/dp.png"); ok {
		t.Error("POST answered by the landscape route")
	}
}
