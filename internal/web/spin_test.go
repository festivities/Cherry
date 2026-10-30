package web

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// spinCacheTemp keeps the spin pictures' dp-cache out of the user's cache for one test.
func spinCacheTemp(t *testing.T) {
	t.Helper()
	old := thumbs.DpCacheDir
	thumbs.DpCacheDir = t.TempDir()
	t.Cleanup(func() { thumbs.DpCacheDir = old })
}

// spinResultOf decodes a spin reply and returns its "result" value.
func spinResultOf(t *testing.T, rec *httptest.ResponseRecorder) any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body)
	}
	res, ok := env["result"]
	if !ok {
		t.Fatalf("no result key (a reply without result zeroes the client's HUD): %s", rec.Body)
	}
	return res
}

// spinRowOf returns result[0] of a list reply; result must be an array of one object.
func spinRowOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	arr, ok := spinResultOf(t, rec).([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("result must be a one-element array: %s", rec.Body)
	}
	row, ok := arr[0].(map[string]any)
	if !ok {
		t.Fatalf("result[0] must be an object: %s", rec.Body)
	}
	return row
}

func spinArray(t *testing.T, row map[string]any, key string) []any {
	t.Helper()
	arr, ok := row[key].([]any)
	if !ok {
		t.Fatalf("%s must be a JSON array, got %T", key, row[key])
	}
	return arr
}

func spinInt(t *testing.T, row map[string]any, key string) float64 {
	t.Helper()
	n, ok := row[key].(float64)
	if !ok {
		t.Fatalf("%s must be a JSON number, got %v (%T)", key, row[key], row[key])
	}
	return n
}

func spinStr(t *testing.T, row map[string]any, key string) string {
	t.Helper()
	s, ok := row[key].(string)
	if !ok {
		t.Fatalf("%s must be a JSON string, got %v (%T)", key, row[key], row[key])
	}
	return s
}

// spinCheckResources checks a resourceList: n entries in slot order, each a served relative picture.
func spinCheckResources(t *testing.T, row map[string]any, n int) {
	t.Helper()
	res := spinArray(t, row, "resourceList")
	if len(res) != n {
		t.Fatalf("resourceList has %d entries, want %d", len(res), n)
	}
	for i, e := range res {
		r := e.(map[string]any)
		if int(spinInt(t, r, "resourceType")) != i || spinStr(t, r, "language") != "common" {
			t.Fatalf("resourceList[%d] = %v, want resourceType %d language common", i, r, i)
		}
		p := spinStr(t, r, "imagePath")
		if p == "" || strings.HasPrefix(p, "/") || !strings.HasPrefix(p, "cherry/spin/") {
			t.Fatalf("resourceList[%d] path %q must be a relative cherry/spin/ path", i, p)
		}
		rec := serve(t, http.MethodGet, "/"+p)
		if _, err := png.Decode(bytes.NewReader(rec.Body.Bytes())); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("GET /%s: status %d, not a PNG (%v)", p, rec.Code, err)
		}
	}
}

// spinCheckGroups checks a rewardList: groups keyed by reward type 3 (Gems) or 1 (items), and the children's slots 1 to 8.
func spinCheckGroups(t *testing.T, row map[string]any, key string) {
	t.Helper()
	var slots []int
	for _, g := range spinArray(t, row, key) {
		grp := g.(map[string]any)
		if rt := spinInt(t, grp, "rewardType"); rt != 1 && rt != 3 {
			t.Fatalf("%s group type %v, want 1 or 3", key, rt)
		}
		spinStr(t, grp, "rewardGradeType")
		for _, c := range spinArray(t, grp, "detailList") {
			child := c.(map[string]any)
			slots = append(slots, int(spinInt(t, child, "showPriority")))
			spinInt(t, child, "rewardNo")
			spinInt(t, child, "rewardValue")
			spinInt(t, child, "probability")
			spinStr(t, child, "itemCd")
			spinStr(t, child, "specialEffects")
			if p := spinStr(t, child, "getImagePath"); !strings.HasPrefix(p, "cherry/spin/") {
				t.Fatalf("getImagePath %q must be a relative cherry/spin/ path", p)
			}
		}
	}
	sort.Ints(slots)
	if len(slots) != 8 {
		t.Fatalf("%s holds %d slots, want 8: %v", key, len(slots), slots)
	}
	for i, s := range slots {
		if s != i+1 {
			t.Fatalf("%s slots %v, want 1 to 8", key, slots)
		}
	}
}

func TestSpinLuckyListShape(t *testing.T) {
	spinCacheTemp(t)
	ecoSetup(t, 500, 0, 0)
	row := spinRowOf(t, decorReq(t, "etok", http.MethodGet, "/v4/luckyspin/list/inprogress", ""))
	if spinInt(t, row, "spinSeq") != 1920000001 || spinStr(t, row, "paymentType") != "GEM" || spinInt(t, row, "price") != 100 {
		t.Fatalf("row %v", row)
	}
	if free := spinArray(t, row, "freeChanceList"); len(free) != 1 || spinInt(t, free[0].(map[string]any), "freeChangeType") != 1 {
		t.Fatalf("a new account has today's free play: %v", row["freeChanceList"])
	}
	if spinStr(t, row, "startYmdt") != "0" || spinStr(t, row, "endYmdt") != "0" || len(spinArray(t, row, "setPurchaseList")) != 0 {
		t.Fatalf("no expiry and no bundle: %v", row)
	}
	coupons := spinArray(t, row, "couponList")
	if len(coupons) < 1 || spinInt(t, coupons[0].(map[string]any), "price") != 100 {
		t.Fatalf("couponList[0] must carry the 100-Gem price: %v", coupons)
	}
	spinCheckResources(t, row, 6)
	spinCheckGroups(t, row, "rewardList")
	locales := spinArray(t, row, "localeList")
	seen := map[string]bool{}
	for _, l := range locales {
		lo := l.(map[string]any)
		spinStr(t, lo, "color")
		seen[spinStr(t, lo, "language")] = spinStr(t, lo, "title") != ""
	}
	if !seen["en"] || !seen["ja"] {
		t.Fatalf("localeList must hold en and ja: %v", locales)
	}
}

func TestSpinJackpotListShape(t *testing.T) {
	spinCacheTemp(t)
	ecoSetup(t, 500, 25, 0)
	row := spinRowOf(t, decorReq(t, "etok", http.MethodGet, "/v4/luckyspin/jackpot/list/inprogress", ""))
	if spinInt(t, row, "spinSeq") != 1920000002 || spinStr(t, row, "paymentType") != "GEM" || spinInt(t, row, "price") != 100 {
		t.Fatalf("tier 1: %v", row)
	}
	if spinStr(t, row, "paymentTypeHigh") != "CASH" || spinInt(t, row, "priceHigh") != 10 || spinStr(t, row, "payItemCdHigh") != "" {
		t.Fatalf("tier 2 must be 10 Cash: %v", row)
	}
	if free := spinArray(t, row, "freeChanceList"); len(free) != 1 || spinInt(t, free[0].(map[string]any), "freeChangeType") != 0 {
		t.Fatalf("no free Jackpot play: %v", row["freeChanceList"])
	}
	spinCheckResources(t, row, 7)
	spinCheckGroups(t, row, "rewardList")
	spinCheckGroups(t, row, "rewardListHigh")
}

// TestSpinListFreeChanceFollowsTheCounter: the free play shows until a game day's free Lucky Spin is used; a reply with
// no account offers none.
func TestSpinListFreeChanceFollowsTheCounter(t *testing.T) {
	spinCacheTemp(t)
	ecoSetup(t, 500, 0, 0)
	free := func(rec *httptest.ResponseRecorder) float64 {
		t.Helper()
		list := spinArray(t, spinRowOf(t, rec), "freeChanceList")
		return spinInt(t, list[0].(map[string]any), "freeChangeType")
	}
	if got := free(decorReq(t, "etok", http.MethodGet, "/v4/luckyspin/list/inprogress", "")); got != 1 {
		t.Fatalf("before the free play: %v, want 1", got)
	}
	if rec := decorReq(t, "etok", http.MethodPost, "/v4/luckyspin/purchasenplay",
		`{"spinSeq":1920000001,"setNo":1,"coinUnit":"GEM","payItemCd":"","price":100,"freeChance":1,"language":"en","deviceType":"Android"}`+"\x00"); rec.Code != http.StatusOK {
		t.Fatalf("free play: %d %s", rec.Code, rec.Body)
	}
	if got := free(decorReq(t, "etok", http.MethodGet, "/v4/luckyspin/list/inprogress", "")); got != 0 {
		t.Fatalf("after the free play: %v, want 0", got)
	}
	if got := free(serve(t, http.MethodGet, "/v4/luckyspin/list/inprogress")); got != 0 {
		t.Fatalf("no account: %v, want 0", got)
	}
}

// TestSpinPlayReplyKeepsBothBalances: a play answers with both balances, one history row, and never bill/prod/spot/gem
// (the real-money upsell stays empty).
func TestSpinPlayReplyKeepsBothBalances(t *testing.T) {
	spinCacheTemp(t)
	acc, _ := ecoSetup(t, 500, 25, 0)
	rec := decorReq(t, "etok", http.MethodPost, "/v4/luckyspin/purchasenplay",
		`{"spinSeq":1920000002,"setNo":0,"coinUnit":"CASH","payItemCd":"","price":10,"freeChance":0,"spinType":"JACKPOT","isHighPrice":true,"language":"en","deviceType":"Android"}`+"\x00")
	if rec.Code != http.StatusOK {
		t.Fatalf("jackpot tier 2: %d %s", rec.Code, rec.Body)
	}
	res := spinResultOf(t, rec).(map[string]any)
	store.AccountsMu.Lock()
	gems, cash := acc.Gems, acc.Cash
	store.AccountsMu.Unlock()
	if spinInt(t, res["gemBalance"].(map[string]any), "totalCoinBalance") != float64(gems) ||
		spinInt(t, res["cashBalance"].(map[string]any), "totalCoinBalance") != float64(cash) || cash != 15 {
		t.Fatalf("balances in the reply %v, account gems %d cash %d", res, gems, cash)
	}
	hist := spinArray(t, res, "historyList")
	if len(hist) != 1 {
		t.Fatalf("historyList %v, want one row", hist)
	}
	if p := spinInt(t, hist[0].(map[string]any), "showPriority"); p < 1 || p > 8 {
		t.Fatalf("showPriority %v outside 1 to 8", p)
	}
	spinStr(t, hist[0].(map[string]any), "getImagePath")
	if rec := serve(t, http.MethodGet, "/v4/bill/prod/spot/gem"); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("bill/prod/spot/gem must stay an empty list: %s", rec.Body)
	}
}

// TestSpinPlayRefusesWithoutChange: a short balance answers 400 60001, a stale price 400 61003; neither gives a result.
func TestSpinPlayRefusesWithoutChange(t *testing.T) {
	spinCacheTemp(t)
	acc, _ := ecoSetup(t, 50, 0, 0)
	rec := decorReq(t, "etok", http.MethodPost, "/v4/luckyspin/purchasenplay",
		`{"spinSeq":1920000001,"setNo":1,"coinUnit":"GEM","payItemCd":"","price":100,"freeChance":0,"language":"en","deviceType":"Android"}`+"\x00")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"errorCode":"60001"`) {
		t.Fatalf("short balance: %d %s", rec.Code, rec.Body)
	}
	rec = decorReq(t, "etok", http.MethodPost, "/v4/luckyspin/purchasenplay",
		`{"spinSeq":1920000001,"setNo":1,"coinUnit":"GEM","payItemCd":"","price":90,"freeChance":0,"language":"en","deviceType":"Android"}`+"\x00")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"errorCode":"61003"`) {
		t.Fatalf("stale price: %d %s", rec.Code, rec.Body)
	}
	store.AccountsMu.Lock()
	gems := acc.Gems
	store.AccountsMu.Unlock()
	if gems != 50 {
		t.Fatalf("refused plays changed gems: %d", gems)
	}
}

// TestSpinMenuRowsAndPictures: the two MENU rows come last, with their Cherry-made labels and existing icons.
func TestSpinMenuRowsAndPictures(t *testing.T) {
	rows := homeIconRows()
	n := len(rows)
	// The spin rows were appended last; Lounge Chat was appended after them (the last row).
	if n < 3 || rows[n-3].LinkType != "goSomewhere(luckyspin)" || rows[n-3].Name != "Lucky Spin" ||
		rows[n-2].LinkType != "goSomewhere(jackpotspin)" || rows[n-2].Name != "Jackpot Spin" {
		t.Fatalf("the spin rows are %v, want Lucky Spin then Jackpot Spin before the last row", rows[n-3:])
	}
	for _, row := range rows[n-3 : n-1] {
		if row.Image == "" {
			t.Fatalf("%s has no picture", row.LinkType)
		}
		if rec := serve(t, http.MethodGet, "/"+row.Image); rec.Code != http.StatusOK {
			t.Fatalf("GET /%s: %d", row.Image, rec.Code)
		}
	}
}
