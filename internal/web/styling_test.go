package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

const (
	stylingToken = "styling-token"
	stylingAID   = "9701"
)

// stylingSetup installs one account that is not a lab account (it owns only inventory), file-backed in a fresh folder, so the
// saves, the purchases and their ledger lines are written. It returns the account and the database file.
func stylingSetup(t *testing.T, gems int64, inventory []string, dyes map[string][]store.DyeColor) (*store.Account, string) {
	t.Helper()
	acc := &store.Account{AccessToken: stylingToken, SessionKey: "sk", Aid: stylingAID, Name: "Style", Gender: "FEMALE",
		Skin: "1", Country: "JP", Gems: gems, InventoryCodes: inventory, DyeColors: dyes}
	installSocialTestAccounts(t, map[string]*store.Account{stylingToken: acc})
	setLab(t, stylingAID, false)
	dir := t.TempDir()
	testutil.UseDatabaseAt(t, dir)
	return acc, filepath.Join(dir, store.DBFileName)
}

// stylingCall sends one styling request as the account (the native client's trailing NUL is part of body when given).
func stylingCall(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return decorReq(t, stylingToken, method, target, body)
}

// stylingResult decodes a 200 reply's result into out. A "null" anywhere in the reply fails: every array must be an array.
func stylingResult(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("reply carries null: %s", rec.Body.String())
	}
	var reply struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(reply.Result, out); err != nil {
		t.Fatalf("result shape: %v: %s", err, reply.Result)
	}
}

// stylingRefused checks a 400 refusal with the errorCode.
func stylingRefused(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	want := `"errorCode":"` + code + `"`
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("status %d body %s, want 400 with %s", rec.Code, rec.Body.String(), want)
	}
}

type stylingBase struct {
	TotalSlotCount int64  `json:"totalSlotCount"`
	UsedSlotCount  int64  `json:"usedSlotCount"`
	MaxSlotCount   int64  `json:"maxSlotCount"`
	Price          int64  `json:"price"`
	CoinUnit       string `json:"coinUnit"`
}

type stylingItem struct {
	ItemCode                      string           `json:"itemCode"`
	InventorySeq                  string           `json:"inventorySeq"`
	DyeType                       int              `json:"dyeType"`
	ColorAndTransparencies        []store.DyeColor `json:"colorAndTransparencies"`
	ChangedColorAndTransparencies []store.DyeColor `json:"changedColorAndTransparencies"`
}

type stylingSlot struct {
	SlotID         int64         `json:"slotId"`
	BookmarkYn     bool          `json:"bookmarkYn"`
	StyleName      string        `json:"styleName"`
	StyleSlotItems []stylingItem `json:"styleSlotItems"`
	ItemDeleteYn   bool          `json:"itemDeleteYn"`
}

// stylingState reads the account's purchase count, saved slots and Gems under the lock.
func stylingState(acc *store.Account) (bought int64, slots []store.StyleSlot, gems int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return acc.StyleBought, append([]store.StyleSlot(nil), acc.StyleSlots...), acc.Gems
}

func TestStylingListStartsWithFreeSlots(t *testing.T) {
	stylingSetup(t, 0, nil, nil)
	rec := stylingCall(t, http.MethodGet, "/v4/style/slot/list", "")
	t.Logf("list: %s", rec.Body.String())
	var list struct {
		stylingBase
		StyleSlotResponses []stylingSlot `json:"styleSlotResponses"`
	}
	stylingResult(t, rec, &list)
	want := stylingBase{TotalSlotCount: 3, UsedSlotCount: 0, MaxSlotCount: 20, Price: 300, CoinUnit: "GEM"}
	if list.stylingBase != want || list.StyleSlotResponses == nil || len(list.StyleSlotResponses) != 0 {
		t.Fatalf("list = %+v %v, want %+v and no slots", list.stylingBase, list.StyleSlotResponses, want)
	}
	if !strings.Contains(rec.Body.String(), `"styleSlotResponses":[]`) {
		t.Fatalf("styleSlotResponses is not an empty array: %s", rec.Body.String())
	}
}

// TestStylingLifecycle: save, list, detail, modify (id kept, oldSlotId), bookmark toggles, delete (oldSlotId), and the next save
// takes a new id (ids are never reused). The slot's garment takes its closet serial (the closet order) and a base item keeps 0.
func TestStylingLifecycle(t *testing.T) {
	_, dbPath := stylingSetup(t, 0, []string{"CUTO0019U"}, nil)

	rec := stylingCall(t, http.MethodPost, "/v4/style/slot/save",
		`{"styleName":"Summer","items":[{"itemCode":"CUTO0019U","inventorySeq":1},{"itemCode":"CUHA00004"}]}`+"\x00")
	t.Logf("save: %s", rec.Body.String())
	var saved struct {
		stylingBase
		Detail stylingSlot `json:"styleSlotDetailResponse"`
	}
	stylingResult(t, rec, &saved)
	if saved.TotalSlotCount != 3 || saved.UsedSlotCount != 1 || saved.Detail.SlotID != 1 || saved.Detail.StyleName != "Summer" {
		t.Fatalf("save = %+v", saved)
	}
	if items := saved.Detail.StyleSlotItems; len(items) != 2 || items[0].ItemCode != "CUTO0019U" || items[0].InventorySeq != "1" ||
		items[0].DyeType != 0 || items[1].InventorySeq != "0" || saved.Detail.ItemDeleteYn {
		t.Fatalf("saved items = %+v itemDeleteYn=%v", items, saved.Detail.ItemDeleteYn)
	}

	var list struct {
		stylingBase
		StyleSlotResponses []stylingSlot `json:"styleSlotResponses"`
	}
	stylingResult(t, stylingCall(t, http.MethodGet, "/v4/style/slot/list", ""), &list)
	if list.UsedSlotCount != 1 || len(list.StyleSlotResponses) != 1 || list.StyleSlotResponses[0].StyleName != "Summer" {
		t.Fatalf("list after save = %+v", list)
	}

	var detail stylingSlot
	stylingResult(t, stylingCall(t, http.MethodGet, "/v4/style/slot/detail/1", ""), &detail)
	if detail.SlotID != 1 || detail.StyleName != "Summer" || len(detail.StyleSlotItems) != 2 {
		t.Fatalf("detail = %+v", detail)
	}
	if rec := stylingCall(t, http.MethodGet, "/v4/style/slot/detail/99", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("detail of no slot = %d %s", rec.Code, rec.Body.String())
	}

	var modified struct {
		stylingBase
		OldSlotID int64       `json:"oldSlotId"`
		Detail    stylingSlot `json:"styleSlotDetailResponse"`
	}
	rec = stylingCall(t, http.MethodPost, "/v4/style/slot/modify",
		`{"slotId":1,"styleName":"Summer 2","items":[{"itemCode":"CUTO0019U"}]}`+"\x00")
	stylingResult(t, rec, &modified)
	if modified.OldSlotID != 1 || modified.Detail.SlotID != 1 || modified.Detail.StyleName != "Summer 2" ||
		len(modified.Detail.StyleSlotItems) != 1 || modified.UsedSlotCount != 1 {
		t.Fatalf("modify = %+v", modified)
	}

	for _, want := range []bool{true, false} {
		var bm struct {
			stylingBase
			OldSlotID int64 `json:"oldSlotId"`
		}
		stylingResult(t, stylingCall(t, http.MethodPost, "/v4/style/slot/bookmark/1", "null"), &bm)
		if bm.OldSlotID != 1 {
			t.Fatalf("bookmark reply = %+v", bm)
		}
		var got stylingSlot
		stylingResult(t, stylingCall(t, http.MethodGet, "/v4/style/slot/detail/1", ""), &got)
		if got.BookmarkYn != want {
			t.Fatalf("bookmarkYn = %v, want %v", got.BookmarkYn, want)
		}
	}

	var deleted struct {
		stylingBase
		OldSlotID int64 `json:"oldSlotId"`
	}
	stylingResult(t, stylingCall(t, http.MethodPost, "/v4/style/slot/delete/1", "null"), &deleted)
	if deleted.OldSlotID != 1 || deleted.UsedSlotCount != 0 || deleted.TotalSlotCount != 3 {
		t.Fatalf("delete = %+v", deleted)
	}
	var again struct {
		stylingBase
		Detail stylingSlot `json:"styleSlotDetailResponse"`
	}
	stylingResult(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save", `{"styleName":"Again","items":[]}`), &again)
	if again.Detail.SlotID != 2 || again.UsedSlotCount != 1 {
		t.Fatalf("save after delete = %+v, want the new id 2", again)
	}

	// The slots are in the database row (the account's data column), not only in memory.
	d, err := store.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var data string
	if err := d.View(func(q store.Querier) error {
		return q.QueryRow(`SELECT data FROM accounts WHERE aid = ?`, stylingAID).Scan(&data)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, `"styleNextId":2`) || !strings.Contains(data, `"name":"Again"`) {
		t.Fatalf("account row lacks the saved slots: %s", data)
	}
}

func TestStylingSaveRefusedWhenFull(t *testing.T) {
	acc, _ := stylingSetup(t, 0, nil, nil)
	for i := 0; i < 3; i++ {
		stylingResult(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save", `{"styleName":"s","items":[]}`), &struct{}{})
	}
	stylingRefused(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save", `{"styleName":"s","items":[]}`), "61001")
	if _, slots, _ := stylingState(acc); len(slots) != 3 {
		t.Fatalf("slots after the refused save = %d, want 3", len(slots))
	}
}

func TestStylingPurchaseSpendsGemsThroughTheLedger(t *testing.T) {
	acc, _ := stylingSetup(t, 1000, nil, nil)
	rec := stylingCall(t, http.MethodPost, "/v4/style/slot/purchase", `{"coinUnit":"GEM","price":300,"deviceType":"ANDROID"}`+"\x00")
	t.Logf("purchase: %s", rec.Body.String())
	var reply struct {
		stylingBase
		Balance     int64  `json:"balance"`
		BalanceUnit string `json:"balanceUnit"`
	}
	stylingResult(t, rec, &reply)
	want := stylingBase{TotalSlotCount: 4, UsedSlotCount: 0, MaxSlotCount: 20, Price: 300, CoinUnit: "GEM"}
	if reply.stylingBase != want || reply.Balance != 700 || reply.BalanceUnit != "GEM" {
		t.Fatalf("purchase = %+v", reply)
	}
	if bought, _, gems := stylingState(acc); bought != 1 || gems != 700 {
		t.Fatalf("account after purchase: bought=%d gems=%d", bought, gems)
	}
	var found bool
	for _, line := range readLedger(t) {
		if line.Aid == stylingAID && line.Reason == "styling:slot" {
			found = line.Currency == "gems" && line.Delta == -300 && line.Balance == 700
		}
	}
	if !found {
		t.Fatalf("no ledger line for the slot purchase: %+v", readLedger(t))
	}
}

func TestStylingPurchaseRefusals(t *testing.T) {
	acc, _ := stylingSetup(t, 299, nil, nil)
	stylingRefused(t, stylingCall(t, http.MethodPost, "/v4/style/slot/purchase", `{"coinUnit":"GEM","price":300}`), "60001")
	store.AccountsMu.Lock()
	acc.Gems = 1000
	store.AccountsMu.Unlock()
	stylingRefused(t, stylingCall(t, http.MethodPost, "/v4/style/slot/purchase", `{"coinUnit":"GEM","price":299}`), "61003")
	stylingRefused(t, stylingCall(t, http.MethodPost, "/v4/style/slot/purchase", `{"coinUnit":"CASH","price":300}`), "61001")
	if bought, _, gems := stylingState(acc); bought != 0 || gems != 1000 {
		t.Fatalf("refused purchases changed the account: bought=%d gems=%d", bought, gems)
	}
}

func TestStylingPurchaseStopsAtTheMaximum(t *testing.T) {
	acc, _ := stylingSetup(t, 6000, nil, nil)
	for i := 0; i < 17; i++ { // 3 free + 17 bought = max_slots 20
		stylingResult(t, stylingCall(t, http.MethodPost, "/v4/style/slot/purchase", `{"coinUnit":"GEM","price":300}`), &struct{}{})
	}
	stylingRefused(t, stylingCall(t, http.MethodPost, "/v4/style/slot/purchase", `{"coinUnit":"GEM","price":300}`), "61001")
	var list struct {
		stylingBase
	}
	stylingResult(t, stylingCall(t, http.MethodGet, "/v4/style/slot/list", ""), &list)
	if list.TotalSlotCount != 20 || list.MaxSlotCount != 20 {
		t.Fatalf("list at the maximum = %+v", list.stylingBase)
	}
	if bought, _, gems := stylingState(acc); bought != 17 || gems != 900 {
		t.Fatalf("bought=%d gems=%d, want 17 and 900", bought, gems)
	}
}

// TestStylingItemsMustBeOwned: an unowned garment or skin is refused and nothing is saved; a base item is allowed (the client
// wears its base clothes too); a serial that is not a number is a bad request.
func TestStylingItemsMustBeOwned(t *testing.T) {
	acc, _ := stylingSetup(t, 0, []string{"CUTO0019U"}, nil)
	stylingRefused(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save", `{"styleName":"x","items":[{"itemCode":"CUPA0099Z"}]}`), "61001")
	stylingRefused(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save", `{"styleName":"x","items":[{"itemCode":"SKN002"}]}`), "61001")
	rec := stylingCall(t, http.MethodPost, "/v4/style/slot/save", `{"styleName":"x","items":[{"itemCode":"CUTO0019U","inventorySeq":"abc"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad serial = %d %s", rec.Code, rec.Body.String())
	}
	if _, slots, _ := stylingState(acc); len(slots) != 0 {
		t.Fatalf("refused saves left %d slots", len(slots))
	}
	stylingResult(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save", `{"styleName":"base","items":[{"itemCode":"CUSH0009Q"}]}`), &struct{}{})
}

// TestStylingItemDeleteYnAfterASale: a garment sold after the save leaves the slot with itemDeleteYn true, and the slot still
// names it; the closet serial of the garments still owned follows the closet order.
func TestStylingItemDeleteYnAfterASale(t *testing.T) {
	acc, _ := stylingSetup(t, 0, []string{"CUTO0019U", "CUPA0001A"}, nil)
	stylingResult(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save",
		`{"styleName":"pair","items":[{"itemCode":"CUTO0019U","inventorySeq":1},{"itemCode":"CUPA0001A","inventorySeq":2}]}`), &struct{}{})
	store.AccountsMu.Lock()
	acc.InventoryCodes = []string{"CUPA0001A"}
	store.AccountsMu.Unlock()
	var list struct {
		stylingBase
		StyleSlotResponses []stylingSlot `json:"styleSlotResponses"`
	}
	stylingResult(t, stylingCall(t, http.MethodGet, "/v4/style/slot/list", ""), &list)
	slot := list.StyleSlotResponses[0]
	if !slot.ItemDeleteYn || len(slot.StyleSlotItems) != 2 || slot.StyleSlotItems[0].ItemCode != "CUTO0019U" {
		t.Fatalf("slot after the sale = %+v", slot)
	}
	if slot.StyleSlotItems[1].InventorySeq != "1" {
		t.Fatalf("serial of the remaining garment = %s, want the closet place 1", slot.StyleSlotItems[1].InventorySeq)
	}
}

// TestStylingDyeFollowsTheGarment: the dye of a garment is read from the account when the slot is served, so a dye change shows and
// a bleach clears it (dyeType 0 and empty arrays, never null). The serial a client sent is replaced by the closet place.
func TestStylingDyeFollowsTheGarment(t *testing.T) {
	acc, _ := stylingSetup(t, 0, []string{"CUTO0019U", "CUPA0001A"}, map[string][]store.DyeColor{
		"CUPA0001A": {{ColorCode: "#112233"}, {ColorCode: "#445566", Transparency: 20}},
	})
	var saved struct {
		Detail stylingSlot `json:"styleSlotDetailResponse"`
	}
	stylingResult(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save",
		`{"styleName":"dyed","items":[{"itemCode":"CUPA0001A","inventorySeq":9}]}`), &saved)
	item := saved.Detail.StyleSlotItems[0]
	if item.InventorySeq != "2" || item.DyeType != 2 || len(item.ColorAndTransparencies) != 2 ||
		item.ColorAndTransparencies[1] != (store.DyeColor{ColorCode: "#445566", Transparency: 20}) {
		t.Fatalf("dyed item = %+v", item)
	}
	store.AccountsMu.Lock()
	delete(acc.DyeColors, "CUPA0001A")
	store.AccountsMu.Unlock()
	var got stylingSlot
	stylingResult(t, stylingCall(t, http.MethodGet, "/v4/style/slot/detail/1", ""), &got)
	if got.StyleSlotItems[0].DyeType != 0 || len(got.StyleSlotItems[0].ColorAndTransparencies) != 0 {
		t.Fatalf("bleached item = %+v", got.StyleSlotItems[0])
	}
}

func TestStylingCapsAndEmptyArrays(t *testing.T) {
	stylingSetup(t, 0, []string{"CUTO0019U"}, nil)
	stylingRefused(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save", `{"styleName":"`+strings.Repeat("あ", 41)+`","items":[]}`), "61001")
	stylingRefused(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save", `{"styleName":"x","items":[`+strings.TrimSuffix(strings.Repeat(`{"itemCode":"CUTO0019U"},`, 41), ",")+`]}`), "61001")
	var saved struct {
		Detail stylingSlot `json:"styleSlotDetailResponse"`
	}
	stylingResult(t, stylingCall(t, http.MethodPost, "/v4/style/slot/save", `{"styleName":"`+strings.Repeat("あ", 40)+`","items":[]}`), &saved)
	if saved.Detail.StyleName != strings.Repeat("あ", 40) || saved.Detail.StyleSlotItems == nil || len(saved.Detail.StyleSlotItems) != 0 {
		t.Fatalf("40-rune name with no items = %+v", saved.Detail)
	}
}

func TestStylingNeedsAnAccount(t *testing.T) {
	stylingSetup(t, 0, nil, nil)
	for _, c := range []struct{ method, target, body string }{
		{http.MethodGet, "/v4/style/slot/list", ""},
		{http.MethodPost, "/v4/style/slot/save", `{"styleName":"x","items":[]}`},
	} {
		if rec := decorReq(t, "", c.method, c.target, c.body); rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s without an account = %d %s", c.method, c.target, rec.Code, rec.Body.String())
		}
	}
}

func TestStylingRoutesRejectOtherMethods(t *testing.T) {
	stylingSetup(t, 0, nil, nil)
	for _, c := range []struct{ method, target string }{
		{http.MethodPost, "/v4/style/slot/list"},
		{http.MethodGet, "/v4/style/slot/save"},
		{http.MethodGet, "/v4/style/slot/delete/1"},
		{http.MethodPost, "/v4/style/slot/detail/1"},
		{http.MethodGet, "/v4/style/slot/unknown"},
	} {
		if rec := stylingCall(t, c.method, c.target, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", c.method, c.target, rec.Code)
		}
	}
}
