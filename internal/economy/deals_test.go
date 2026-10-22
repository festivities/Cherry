package economy

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

// dealFixture: six tops (CUTO00001-6), six walls (RUWA00001-6), chair RUCH00001 (the plain candidates),
// the limited CUON004SR (Halloween dress), CUSH002Z1 (winter shoes) and RUCH004RC (winter chair),
// a pet and a face (never sold).
func dealFixture(t *testing.T) {
	t.Helper()
	files := map[string][]string{
		"custom/223800001":   {"dp.png"}, // CUHE00001 face
		"pet/326400016":      {"skeleton.xml", "dp.png"},
		"dress/225106219":    {"dp.png"}, // CUON004SR
		"dress/225303853":    {"dp.png"}, // CUSH002Z1
		"interior/120406168": {"dp.png"}, // RUCH004RC
		"interior/120400001": {"dp.png"}, // RUCH00001
	}
	for i := 1; i <= 6; i++ {
		files[fmt.Sprintf("dress/22500000%d", i)] = []string{"dp.png"}
		files[fmt.Sprintf("interior/12140000%d", i)] = []string{"dp.png"}
	}
	shopFixture(t, files)
	for _, code := range []string{"CUTO00006", "RUWA00006", "CUON004SR", "CUSH002Z1", "RUCH004RC", "RUCH00001"} {
		if loadCatalog().byCode[code] == nil {
			t.Fatalf("fixture lacks %s", code)
		}
	}
}

// dealAt is a JST wall-clock instant.
func dealAt(year int, month time.Month, day, hour, min int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, jst)
}

// listRows serves a shop list and returns its item rows.
func listRows(t *testing.T, h http.HandlerFunc, target string) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var out struct {
		Result struct {
			Items []map[string]any `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s body %q: %v", target, rec.Body.String(), err)
	}
	return out.Result.Items
}

func rowOf(rows []map[string]any, code string) map[string]any {
	for _, r := range rows {
		if r["itemCode"] == code {
			return r
		}
	}
	return nil
}

// postAs posts a body to a shop or recycle handler as the phase E test account (token ptok).
func postAs(h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Cookie", `AV_AUTH="ptok"`)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func buyBody(code string, price int64) string {
	return fmt.Sprintf(`{"items":[{"itemCode":%q,"price":%d}]}`, code, price)
}

func TestDealPriceRounding(t *testing.T) {
	for price, want := range map[int64]int64{1: 1, 100: 70, 120: 84, 150: 105, 250: 175, 375: 263, 625: 438} {
		if got := dealPrice(price); got != want {
			t.Errorf("dealPrice(%d) = %d, want %d", price, got, want)
		}
	}
}

// TestDealPickRules: every game day has three clothing and three furniture deals, all catalog items,
// never limited, pets or faces; the set changes across days.
func TestDealPickRules(t *testing.T) {
	dealFixture(t)
	sets := map[string]bool{}
	for day := 0; day < 30; day++ {
		now := dealAt(2026, time.October, 22, 12, 0).AddDate(0, 0, day)
		deals := dealsOn(now)
		var clothing, furniture int
		for code, price := range deals {
			_, kind := classify(code)
			switch kind {
			case kindFashion:
				clothing++
			case kindInterior:
				furniture++
			default:
				t.Fatalf("day %d: deal %s is neither clothing nor furniture", day, code)
			}
			if limited, _, _ := limitedSale(code, now); limited {
				t.Fatalf("day %d: limited item %s is a deal", day, code)
			}
			if table, _, _, _ := ItemPrice(code); price != dealPrice(table) {
				t.Fatalf("day %d: %s deal price %d, want %d", day, code, price, dealPrice(table))
			}
		}
		if clothing != dealsPerKind || furniture != dealsPerKind {
			t.Fatalf("day %d: %d clothing and %d furniture deals, want 3 and 3", day, clothing, furniture)
		}
		sets[strings.Join(slices.Sorted(maps.Keys(deals)), ",")] = true
	}
	if len(sets) < 2 {
		t.Errorf("the deal set never changed over 30 game days")
	}
}

// TestDealsStableWithinGameDayAndRestart: one game day (06:00 to 06:00 JST) gives one deal set, and a
// fresh catalog load (a restart) gives the same set.
func TestDealsStableWithinGameDayAndRestart(t *testing.T) {
	dealFixture(t)
	a := dealsOn(dealAt(2026, time.October, 22, 6, 0))
	b := dealsOn(dealAt(2026, time.October, 22, 23, 59))
	if len(a) != 2*dealsPerKind || !maps.Equal(a, b) {
		t.Fatalf("deals at 06:00 %v and 23:59 %v differ or are short", a, b)
	}
	catalogMu.Lock()
	catalog = nil
	catalogMu.Unlock()
	if c := dealsOn(dealAt(2026, time.October, 22, 12, 0)); !maps.Equal(a, c) {
		t.Errorf("deals after reload %v, want %v", c, a)
	}
}

// TestSaleRowsAndDealBuy: a plain tops row carries no sale keys; a deal row carries SALE, the deal
// price and the next game day's end, both as epoch-millisecond strings. Only the deal price buys it.
func TestSaleRowsAndDealBuy(t *testing.T) {
	dealFixture(t)
	now := dealAt(2026, time.October, 22, 12, 0)
	phaseEClock(t, now)
	deals := dealsOn(now)
	nextDay := strconv.FormatInt(dealAt(2026, time.October, 23, 6, 0).UnixMilli(), 10)

	rows := listRows(t, handleShopList(kindFashion), "/v4/shop/fashion/itemlist/MTOPS")
	if len(rows) != 6 {
		t.Fatalf("tops rows = %d, want 6", len(rows))
	}
	var dealCode string
	seenPlain := false
	for _, r := range rows {
		if _, deal := deals[r["itemCode"].(string)]; !deal {
			seenPlain = true
		} else if seenPlain {
			t.Errorf("deal %v listed after a plain row; deals lead the tab", r["itemCode"])
		}
	}
	for _, r := range rows {
		code := r["itemCode"].(string)
		if d, ok := deals[code]; ok {
			if dealCode == "" {
				dealCode = code
			}
			if r["discount"] != true || r["discountedPrice"] != float64(d) || r["sellingIconType"] != "SALE" ||
				r["sellingEnd"] != nextDay || r["discountEnd"] != nextDay {
				t.Errorf("deal row %v", r)
			}
		} else if r["discount"] != false || r["discountedPrice"] != r["price"] || r["sellingIconType"] != "" ||
			r["sellingEnd"] != "" || r["discountEnd"] != "" {
			t.Errorf("plain row %v", r)
		}
	}
	if dealCode == "" {
		t.Fatal("no tops deal today")
	}
	table, _, _, _ := ItemPrice(dealCode)
	acc, _ := phaseEAccount(t, 5000, nil)
	buy := handleShopBuy(kindFashion)
	if rec := postAs(buy, buyBody(dealCode, table)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errUpdated) {
		t.Errorf("full price on a deal = %d %s, want %s", rec.Code, rec.Body.String(), errUpdated)
	}
	if acc.Gems != 5000 {
		t.Fatalf("refused buy changed gems to %d", acc.Gems)
	}
	if rec := postAs(buy, buyBody(dealCode, dealPrice(table))); rec.Code != http.StatusOK {
		t.Fatalf("deal buy = %d %s", rec.Code, rec.Body.String())
	}
	if acc.Gems != 5000-dealPrice(table) || acc.PaidPrices[dealCode] != dealPrice(table) {
		t.Errorf("after deal buy gems %d paid %v", acc.Gems, acc.PaidPrices)
	}
}

// TestLimitedWindows: a limited item is listed only inside its own season's game dates, with LIMIT and
// its window's end; winter is listed on Jan 3 while the New Year calendar runs.
func TestLimitedWindows(t *testing.T) {
	dealFixture(t)
	end := func(y int, m time.Month, d int) string {
		return strconv.FormatInt(dealAt(y, m, d, 6, 0).UnixMilli(), 10)
	}
	cases := []struct {
		name   string
		when   time.Time
		h      http.HandlerFunc
		target string
		code   string
		end    string // "" = not listed
	}{
		{"Halloween before the window", dealAt(2026, time.October, 22, 12, 0), handleShopList(kindFashion), "/v4/shop/fashion/itemlist/MDRESS", "CUON004SR", ""},
		{"Halloween opens Oct 25", dealAt(2026, time.October, 25, 6, 0), handleShopList(kindFashion), "/v4/shop/fashion/itemlist/MDRESS", "CUON004SR", end(2026, time.November, 8)},
		{"Halloween last game hour", dealAt(2026, time.November, 8, 5, 59), handleShopList(kindFashion), "/v4/shop/fashion/itemlist/MDRESS", "CUON004SR", end(2026, time.November, 8)},
		{"Halloween closed at 06:00 Nov 8", dealAt(2026, time.November, 8, 6, 0), handleShopList(kindFashion), "/v4/shop/fashion/itemlist/MDRESS", "CUON004SR", ""},
		{"winter opens Dec 15", dealAt(2026, time.December, 15, 12, 0), handleShopList(kindInterior), "/v4/shop/interior/itemlist/UCHAIR", "RUCH004RC", end(2027, time.January, 6)},
		{"winter before Dec 15", dealAt(2026, time.December, 14, 12, 0), handleShopList(kindInterior), "/v4/shop/interior/itemlist/UCHAIR", "RUCH004RC", ""},
		{"winter on Jan 3 (New Year calendar)", dealAt(2027, time.January, 3, 12, 0), handleShopList(kindInterior), "/v4/shop/interior/itemlist/UCHAIR", "RUCH004RC", end(2027, time.January, 6)},
		{"winter shoes on Jan 3", dealAt(2027, time.January, 3, 12, 0), handleShopList(kindFashion), "/v4/shop/fashion/itemlist/MSHOE", "CUSH002Z1", end(2027, time.January, 6)},
		{"winter last game hour Jan 6", dealAt(2027, time.January, 6, 5, 59), handleShopList(kindInterior), "/v4/shop/interior/itemlist/UCHAIR", "RUCH004RC", end(2027, time.January, 6)},
		{"winter closed at 06:00 Jan 6", dealAt(2027, time.January, 6, 6, 0), handleShopList(kindInterior), "/v4/shop/interior/itemlist/UCHAIR", "RUCH004RC", ""},
	}
	for _, c := range cases {
		phaseEClock(t, c.when)
		row := rowOf(listRows(t, c.h, c.target), c.code)
		switch {
		case c.end == "" && row != nil:
			t.Errorf("%s: %s listed outside its window: %v", c.name, c.code, row)
		case c.end != "" && (row == nil || row["sellingIconType"] != "LIMIT" || row["sellingEnd"] != c.end || row["discountEnd"] != "" || row["discount"] != false):
			t.Errorf("%s: %s row %v, want LIMIT ending %s", c.name, c.code, row, c.end)
		}
	}
}

// TestLimitedBuyNeedsWindow: buying a limited item outside its window is the unknown-item error; inside
// it buys at the table price (a limited item is never a deal).
func TestLimitedBuyNeedsWindow(t *testing.T) {
	dealFixture(t)
	acc, _ := phaseEAccount(t, 5000, nil)
	buy := handleShopBuy(kindFashion)
	phaseEClock(t, dealAt(2026, time.October, 22, 12, 0))
	if rec := postAs(buy, buyBody("CUON004SR", 250)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errUnknown) {
		t.Errorf("buy outside window = %d %s, want %s", rec.Code, rec.Body.String(), errUnknown)
	}
	phaseEClock(t, dealAt(2026, time.October, 26, 12, 0))
	if rec := postAs(buy, buyBody("CUON004SR", 250)); rec.Code != http.StatusOK || acc.Gems != 4750 {
		t.Errorf("buy inside window = %d %s gems %d", rec.Code, rec.Body.String(), acc.Gems)
	}
}

// TestForcedSeasonOpensItsLimitedSet: the staff override opens its own season's limited items on any
// date, with the window's next end; the other seasons stay closed.
func TestForcedSeasonOpensItsLimitedSet(t *testing.T) {
	dealFixture(t)
	phaseEClock(t, dealAt(2026, time.October, 22, 12, 0))
	force := func(name string) { seasonOverride.Store(&name) }
	force("halloween")
	t.Cleanup(func() { seasonOverride.Store(nil) })
	if row := rowOf(listRows(t, handleShopList(kindFashion), "/v4/shop/fashion/itemlist/MDRESS"), "CUON004SR"); row == nil ||
		row["sellingEnd"] != strconv.FormatInt(dealAt(2026, time.November, 8, 6, 0).UnixMilli(), 10) {
		t.Errorf("forced halloween row = %v", row)
	}
	if row := rowOf(listRows(t, handleShopList(kindInterior), "/v4/shop/interior/itemlist/UCHAIR"), "RUCH004RC"); row != nil {
		t.Errorf("forced halloween listed winter: %v", row)
	}
	force("winter")
	if row := rowOf(listRows(t, handleShopList(kindInterior), "/v4/shop/interior/itemlist/UCHAIR"), "RUCH004RC"); row == nil ||
		row["sellingEnd"] != strconv.FormatInt(dealAt(2027, time.January, 6, 6, 0).UnixMilli(), 10) {
		t.Errorf("forced winter row = %v", row)
	}
}

// TestDealSellBackPaysThePricePaid: a deal bought at 70% sells back at 70% (closet and Room Deco), a
// plain item at 100%, and the inventory price the client shows equals what the recycle pays. Buying and
// selling everything back returns exactly the starting balance.
func TestDealSellBackPaysThePricePaid(t *testing.T) {
	dealFixture(t)
	now := dealAt(2026, time.October, 22, 12, 0)
	phaseEClock(t, now)
	deals := dealsOn(now)
	var dealCloth, dealWall, plainCloth string
	for _, code := range slices.Sorted(maps.Keys(deals)) {
		_, kind := classify(code)
		if kind == kindFashion && dealCloth == "" {
			dealCloth = code
		}
		if kind == kindInterior && dealWall == "" {
			dealWall = code
		}
	}
	for i := 1; i <= 6 && plainCloth == ""; i++ {
		if code := fmt.Sprintf("CUTO%05d", i); deals[code] == 0 {
			plainCloth = code
		}
	}
	if dealCloth == "" || dealWall == "" || plainCloth == "" {
		t.Fatalf("deal picks cloth %q wall %q plain %q", dealCloth, dealWall, plainCloth)
	}
	acc, _ := phaseEAccount(t, 5000, nil)
	buy, sellCloset, sellRoom := handleShopBuy(kindFashion), handleRecycle(false), handleRecycle(true)

	clothTable, _, _, _ := ItemPrice(dealCloth)
	if rec := postAs(buy, buyBody(dealCloth, dealPrice(clothTable))); rec.Code != http.StatusOK {
		t.Fatalf("deal cloth buy = %d %s", rec.Code, rec.Body.String())
	}
	if rec := postAs(buy, buyBody(plainCloth, mustPrice(t, plainCloth))); rec.Code != http.StatusOK {
		t.Fatalf("plain cloth buy = %d %s", rec.Code, rec.Body.String())
	}
	if rec := postAs(handleShopBuy(kindInterior), buyBody(dealWall, dealPrice(mustPrice(t, dealWall)))); rec.Code != http.StatusOK {
		t.Fatalf("deal wall buy = %d %s", rec.Code, rec.Body.String())
	}
	if p, _ := ClosetPrice(acc.PaidPrices, dealCloth); p != dealPrice(clothTable) {
		t.Errorf("closet row for the deal cloth = %d, want the paid %d", p, dealPrice(clothTable))
	}
	var wall store.RoomItem
	for _, it := range acc.RoomItems {
		if it.Cd == dealWall {
			wall = it
		}
	}
	if p, _ := RoomItemPrice(wall); wall.Paid != dealPrice(mustPrice(t, dealWall)) || p != wall.Paid {
		t.Errorf("room row for the deal wall %+v = %d", wall, p)
	}

	gems := acc.Gems
	pays := func(rec *httptest.ResponseRecorder, want int64) {
		t.Helper()
		if rec.Code != http.StatusOK || acc.Gems-gems != want {
			t.Fatalf("sell = %d %s, paid %d, want %d", rec.Code, rec.Body.String(), acc.Gems-gems, want)
		}
		gems = acc.Gems
	}
	pays(postAs(sellCloset, `{"recycleItems":[{"invenSeq":"1","itemCode":"`+dealCloth+`","count":1,"exepectedPrice":"1","price":"1"}]}`), dealPrice(clothTable))
	if _, kept := acc.PaidPrices[dealCloth]; kept {
		t.Errorf("paid record of the sold cloth kept: %v", acc.PaidPrices)
	}
	pays(postAs(sellCloset, `{"recycleItems":[{"invenSeq":"2","itemCode":"`+plainCloth+`","count":1,"exepectedPrice":"1","price":"1"}]}`), mustPrice(t, plainCloth))
	pays(postAs(sellRoom, `{"recycleItems":[{"invenSeq":`+strconv.FormatInt(wall.Seq, 10)+`,"exepectedPrice":"1","price":1}]}`), wall.Paid)
	if acc.Gems != 5000 {
		t.Errorf("buy and sell back everything gave %d gems, want 5000", acc.Gems)
	}
}

func mustPrice(t *testing.T, code string) int64 {
	t.Helper()
	p, _, _, ok := ItemPrice(code)
	if !ok {
		t.Fatalf("%s is not priced", code)
	}
	return p
}
