package economy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/store"
)

// birthdayAt is a wall-clock time at GMT+9.
func birthdayAt(year int, month time.Month, day, hour, min int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, birthdayZone)
}

// birthdayMux mounts the birthday routes and the Garden home info as web/http.go does.
func birthdayMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/birthday/info/year", HandleBirthdayInfoYear)
	mux.HandleFunc("/v4/birthday/info/day", HandleBirthdayInfoDay)
	mux.HandleFunc("/v4/birthday/home", HandleBirthdayHome)
	mux.HandleFunc("/v4/birthday/reward/year/", HandleBirthdayRewardYear)
	mux.HandleFunc("/v4/home/integrated/info", HandleHomeIntegratedInfo)
	return mux
}

// birthdayCall serves one GET for token (empty = no cookie) and returns the status and the trimmed body.
func birthdayCall(t *testing.T, target, token string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	}
	rec := httptest.NewRecorder()
	birthdayMux().ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

// birthdayAccount is the gacha test account (token gtok, aid 9601) with an avatar created at created and the given claims.
func birthdayAccount(t *testing.T, gems, created int64, claims map[int]int64) *store.Account {
	t.Helper()
	acc := gachaAccount(t, gems, nil)
	store.AccountsMu.Lock()
	acc.AvatarCreated, acc.BirthdayClaims = created, claims
	store.AccountsMu.Unlock()
	return acc
}

func birthdayClaims(acc *store.Account) map[int]int64 {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return acc.BirthdayClaims
}

// TestBirthdayTurnWindow: the turn is the full years since creation at GMT+9, its window is claimDays days from that
// midnight, and the creation day is not a birthday.
func TestBirthdayTurnWindow(t *testing.T) {
	created := birthdayAt(2024, time.May, 10, 15, 0).UnixMilli()
	for _, c := range []struct {
		at   time.Time
		turn int
		end  time.Time // the window end; zero = not checked
	}{
		{birthdayAt(2024, time.May, 10, 23, 59), 0, time.Time{}}, // the creation day
		{birthdayAt(2025, time.May, 9, 23, 59), 0, time.Time{}},
		{birthdayAt(2025, time.May, 10, 0, 0), 1, birthdayAt(2025, time.May, 17, 0, 0)},
		{birthdayAt(2025, time.May, 16, 23, 59), 1, birthdayAt(2025, time.May, 17, 0, 0)},
		{birthdayAt(2025, time.May, 17, 0, 0), 1, birthdayAt(2025, time.May, 17, 0, 0)}, // the window is closed
		{birthdayAt(2026, time.May, 12, 9, 0), 2, birthdayAt(2026, time.May, 17, 0, 0)},
		{time.Date(2025, time.May, 9, 14, 59, 59, 0, time.UTC), 0, time.Time{}},                        // 23:59:59 GMT+9
		{time.Date(2025, time.May, 9, 15, 0, 0, 0, time.UTC), 1, birthdayAt(2025, time.May, 17, 0, 0)}, // 00:00 GMT+9
	} {
		turn, end := birthdayTurn(created, c.at, 7)
		if turn != c.turn || (!c.end.IsZero() && end != c.end.UnixMilli()) {
			t.Errorf("at %v: turn %d end %d, want turn %d end %d", c.at, turn, end, c.turn, c.end.UnixMilli())
		}
	}
	if turn, _ := birthdayTurn(0, birthdayAt(2025, time.May, 10, 12, 0), 7); turn != 0 {
		t.Errorf("no creation time gives turn %d", turn)
	}
}

// TestBirthdayLeapDayCreation: a 29 February avatar has its birthday only in leap years (the same rule as the hat), and
// its turn counts the years to that day.
func TestBirthdayLeapDayCreation(t *testing.T) {
	created := birthdayAt(2024, time.February, 29, 0, 30).UnixMilli()
	for _, c := range []struct {
		at   time.Time
		turn int
	}{
		{birthdayAt(2025, time.February, 28, 12, 0), 0},
		{birthdayAt(2025, time.March, 1, 12, 0), 0},
		{birthdayAt(2028, time.February, 28, 12, 0), 0},
		{birthdayAt(2028, time.February, 29, 0, 0), 4},
		{birthdayAt(2028, time.March, 6, 12, 0), 4},
	} {
		if turn, _ := birthdayTurn(created, c.at, 7); turn != c.turn {
			t.Errorf("at %v: turn %d, want %d", c.at, turn, c.turn)
		}
	}
	if !IsAvatarBirthday(created, birthdayAt(2028, time.February, 29, 12, 0)) || IsAvatarBirthday(created, birthdayAt(2025, time.March, 1, 12, 0)) {
		t.Error("the hat follows the 29 February birthdays only")
	}
}

// TestBirthdayInfoAndHome: the receive list and the Garden icon in each phase, and the no-account replies.
func TestBirthdayInfoAndHome(t *testing.T) {
	created := birthdayAt(2024, time.May, 10, 15, 0).UnixMilli()
	claimed := birthdayAt(2025, time.May, 12, 12, 0).UnixMilli()
	end := birthdayAt(2025, time.May, 17, 0, 0).UnixMilli()
	birthdayAccount(t, 0, created, nil)
	memberOf := func(on bool, turn int) string {
		return fmt.Sprintf(`"birthday_home":{"displayYn":%v,"birthdayType":"year","nextPushDay":"0","nextPushYear":"0","nextPushYearTurn":%d,"nextPushDayTurn":%d}`, on, turn, turn)
	}
	for _, c := range []struct {
		name   string
		at     time.Time
		claims map[int]int64
		info   string
		icon   int // the turn of the Garden icon; 0 = no icon
		member int // the turn birthday_home names in home/integrated/info; 0 = no member (no birthday yet)
	}{
		{"younger than a year", birthdayAt(2025, time.May, 9, 12, 0), nil,
			`{"result":{"avatarTurn":0,"gachaDiscount":0,"expireDate":"0","status":"UNQUALIFIED"}}`, 0, 0},
		{"receivable", birthdayAt(2025, time.May, 12, 12, 0), nil,
			fmt.Sprintf(`{"result":{"avatarTurn":1,"gachaDiscount":30,"expireDate":"%d","status":"RECEIVABLE"}}`, end), 1, 1},
		{"received", birthdayAt(2025, time.May, 12, 13, 0), map[int]int64{1: claimed},
			fmt.Sprintf(`{"result":{"avatarTurn":1,"gachaDiscount":30,"expireDate":"%d","status":"RECEIVED"}}`, birthdayAt(2025, time.May, 19, 12, 0).UnixMilli()), 0, 1},
		{"expired unclaimed", birthdayAt(2025, time.May, 17, 12, 0), nil,
			fmt.Sprintf(`{"result":{"avatarTurn":1,"gachaDiscount":0,"expireDate":"%d","status":"EXPIRED"}}`, end), 0, 1},
		{"next turn receivable", birthdayAt(2026, time.May, 11, 12, 0), map[int]int64{1: claimed},
			fmt.Sprintf(`{"result":{"avatarTurn":2,"gachaDiscount":30,"expireDate":"%d","status":"RECEIVABLE"}}`, birthdayAt(2026, time.May, 17, 0, 0).UnixMilli()), 2, 2},
	} {
		pinPopupClock(t, c.at)
		birthdayAccount(t, 0, created, c.claims)
		if code, body := birthdayCall(t, "/v4/birthday/info/year", "gtok"); code != 200 || body != c.info {
			t.Errorf("%s: info = %d %s, want %s", c.name, code, body, c.info)
		}
		_, home := birthdayCall(t, "/v4/home/integrated/info", "gtok")
		if has := strings.Contains(home, `"birthday_home"`); has != (c.member != 0) || (c.member != 0 && !strings.Contains(home, memberOf(c.icon != 0, c.member))) {
			t.Errorf("%s: home info %s, want birthday_home turn %d on %v", c.name, home, c.member, c.icon != 0)
		}
		_, reach := birthdayCall(t, "/v4/birthday/home", "gtok")
		if want := fmt.Sprintf(`{"result":{"displayYn":%v,`, c.icon != 0); !strings.HasPrefix(reach, want) {
			t.Errorf("%s: birthday/home %s, want prefix %s", c.name, reach, want)
		}
	}
	// No avatar: the day list, the year list and the Garden icon all answer as unqualified; the requests never 404.
	pinPopupClock(t, birthdayAt(2025, time.May, 12, 12, 0))
	const unqualified = `{"result":{"avatarTurn":0,"gachaDiscount":0,"expireDate":"0","status":"UNQUALIFIED"}}`
	for _, token := range []string{"", "nobody"} {
		if code, body := birthdayCall(t, "/v4/birthday/info/year", token); code != 200 || body != unqualified {
			t.Errorf("token %q: info = %d %s", token, code, body)
		}
		if code, body := birthdayCall(t, "/v4/birthday/info/day", token); code != 200 || body != unqualified {
			t.Errorf("token %q: day info = %d %s", token, code, body)
		}
		if _, home := birthdayCall(t, "/v4/home/integrated/info", token); strings.Contains(home, "birthday_home") {
			t.Errorf("token %q: home info has birthday_home: %s", token, home)
		}
	}
}

// TestBirthdayClaimOnce: the claim is recorded once, only for the current turn, only inside its window.
func TestBirthdayClaimOnce(t *testing.T) {
	created := birthdayAt(2024, time.May, 10, 15, 0).UnixMilli()
	claim := func(target, token string) string {
		t.Helper()
		code, body := birthdayCall(t, target, token)
		if code != 200 {
			t.Fatalf("%s = %d %s", target, code, body)
		}
		return body
	}
	acc := birthdayAccount(t, 0, created, nil)
	pinPopupClock(t, birthdayAt(2025, time.May, 9, 12, 0))
	if got := claim("/v4/birthday/reward/year/1", "gtok"); got != `{"result":false}` {
		t.Errorf("before the window = %s", got)
	}
	pinPopupClock(t, birthdayAt(2025, time.May, 12, 12, 0))
	for _, target := range []string{"/v4/birthday/reward/year/0", "/v4/birthday/reward/year/2", "/v4/birthday/reward/year/x"} {
		if got := claim(target, "gtok"); got != `{"result":false}` {
			t.Errorf("%s = %s, want false", target, got)
		}
	}
	if got := claim("/v4/birthday/reward/year/1", ""); got != `{"result":false}` {
		t.Errorf("no account = %s", got)
	}
	if len(birthdayClaims(acc)) != 0 {
		t.Fatal("a refused claim was recorded")
	}
	if got := claim("/v4/birthday/reward/year/1", "gtok"); got != `{"result":true}` {
		t.Fatalf("claim = %s, want true", got)
	}
	if claimed := birthdayClaims(acc)[1]; claimed != birthdayAt(2025, time.May, 12, 12, 0).UnixMilli() {
		t.Errorf("claim time = %d", claimed)
	}
	if got := claim("/v4/birthday/reward/year/1", "gtok"); got != `{"result":false}` {
		t.Errorf("second claim = %s, want false", got)
	}
	if _, home := birthdayCall(t, "/v4/home/integrated/info", "gtok"); !strings.Contains(home, `"birthday_home":{"displayYn":false,`) {
		t.Errorf("claimed turn: home info %s, want birthday_home with displayYn false (clears the icon)", home)
	}
	// After the window closes unclaimed nothing is recorded.
	acc = birthdayAccount(t, 0, created, nil)
	pinPopupClock(t, birthdayAt(2025, time.May, 17, 0, 0))
	if got := claim("/v4/birthday/reward/year/1", "gtok"); got != `{"result":false}` || len(birthdayClaims(acc)) != 0 {
		t.Errorf("after the window = %s (claims %v), want false", got, birthdayClaims(acc))
	}
}

// TestBirthdayGachaDiscount: while the claim's discount lasts, the Gem gacha rows show the discounted prices and a pull
// or a bundle with either the table or the discounted price is charged the discounted amount; after it the strict check
// is back.
func TestBirthdayGachaDiscount(t *testing.T) {
	p := gachaFixture(t)
	claimed := birthdayAt(2025, time.May, 12, 12, 0).UnixMilli()
	acc := birthdayAccount(t, 1000, birthdayAt(2024, time.May, 10, 15, 0).UnixMilli(), map[int]int64{1: claimed})
	pinPopupClock(t, birthdayAt(2025, time.May, 12, 13, 0))

	rec := gachaCall(t, http.MethodGet, "/v4/shop/gachaList2/top/all", "", "gtok")
	var list struct {
		Result struct {
			Items []map[string]any `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Result.Items) != 1 {
		t.Fatalf("list = %d %s (%v)", rec.Code, rec.Body, err)
	}
	row := list.Result.Items[0]
	want := map[string]any{"price": 100.0, "discountedPrice": 70.0, "retryPrice": 70.0, "firstBuyPrice": 100.0,
		"bundlePrice": 900.0, "bundleDiscountedPrice": 630.0, "discount": true,
		"discountStart": strconv.FormatInt(claimed, 10), "discountEnd": strconv.FormatInt(birthdayAt(2025, time.May, 19, 12, 0).UnixMilli(), 10)}
	for k, v := range want {
		if row[k] != v {
			t.Errorf("row %s = %v, want %v", k, row[k], v)
		}
	}

	// A single pull at the table price and one at the discounted price are both charged 70.
	gachaScript(t, pullVals(t, p, "CUHA00001")...)
	if rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "100"), "gtok"); rec.Code != 200 {
		t.Fatalf("table price during the discount = %d %s", rec.Code, rec.Body)
	}
	gachaScript(t, pullVals(t, p, "CUTO00001")...)
	if rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "70"), "gtok"); rec.Code != 200 {
		t.Fatalf("discounted price = %d %s", rec.Code, rec.Body)
	}
	if got := gachaGems(acc); got != 860 {
		t.Fatalf("gems after two discounted pulls = %d, want 860", got)
	}
	if rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "90"), "gtok"); rec.Code != 400 {
		t.Fatalf("an unlisted price = %d %s, want 400", rec.Code, rec.Body)
	}

	// A ten-pull at the table price 900 is charged 630 less the duplicate refunds, each cut by the same 30 % (a refund near the
	// full pull price would otherwise pay more than a discounted pull costs). Picks: CUHA00001 and CUTO00001 are owned, so the
	// copies refund; the last two (RUTD00005 twice) are new then duplicate.
	picks := []string{"RUTD00005", "RUWA000HR", "CUON004TV", "RUGO00002", "CUHA00001", "CUTO00001", "RUTD00005", "CUHA00001", "RUGO00002", "CUON004TV"}
	var vals []int
	var refund int64
	seen := map[string]bool{"CUHA00001": true, "CUTO00001": true}
	pool := gachaPoolNow()
	for _, code := range picks {
		vals = append(vals, pullVals(t, pool, code)...)
		if seen[code] {
			full := gachaRefund(gachaEntryFor(pool, code).price)
			cut := birthdayPriceOff(full, 30)
			if cut >= full {
				t.Fatalf("refund %d not cut by the discount (%d)", full, cut)
			}
			refund += cut
		}
		seen[code] = true
	}
	before := gachaGems(acc)
	gachaScript(t, vals...)
	if rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/multi", gachaBody("1001", "900"), "gtok"); rec.Code != 200 {
		t.Fatalf("bundle at the table price = %d %s", rec.Code, rec.Body)
	}
	if got := gachaGems(acc); got != before-630+refund {
		t.Fatalf("bundle charged %d, want 630 less refunds %d", before-got+refund, refund)
	}

	// Once the discount ends (claim + 7 days) the table prices are the only ones accepted again.
	pinPopupClock(t, birthdayAt(2025, time.May, 19, 12, 0))
	if rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "70"), "gtok"); rec.Code != 400 {
		t.Fatalf("discounted price after the discount = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/multi", gachaBody("1001", "630"), "gtok"); rec.Code != 400 {
		t.Fatalf("bundle discounted price after the discount = %d %s, want 400", rec.Code, rec.Body)
	}
	// Every fixture item is owned by now, so this pull is a duplicate: charged 100, refunded its duplicate value.
	gachaScript(t, pullVals(t, pool, "CUHA00001")...)
	before = gachaGems(acc)
	if rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "100"), "gtok"); rec.Code != 200 {
		t.Fatalf("table price after the discount = %d %s", rec.Code, rec.Body)
	}
	if got := gachaGems(acc); got != before-100+gachaRefund(gachaEntryFor(pool, "CUHA00001").price) {
		t.Fatalf("table price after the discount: gems %d, want the table price charged", got)
	}
}

// gachaEntryFor is the pool entry of code.
func gachaEntryFor(p gachaPool, code string) gachaEntry {
	for _, e := range append(append([]gachaEntry(nil), p.normal...), p.rare...) {
		if e.code == code {
			return e
		}
	}
	return gachaEntry{}
}

// TestBirthdayPriceOff: discounted prices round down and stay at least 1 Gem; a free price stays free.
func TestBirthdayPriceOff(t *testing.T) {
	for _, c := range []struct{ price, percent, want int64 }{
		{100, 30, 70}, {900, 30, 630}, {149, 30, 104}, {99, 90, 9}, {1, 30, 1}, {0, 30, 0}, {100, 0, 100},
	} {
		if got := birthdayPriceOff(c.price, c.percent); got != c.want {
			t.Errorf("birthdayPriceOff(%d, %d) = %d, want %d", c.price, c.percent, got, c.want)
		}
	}
}

// TestBirthdayRewardsRefusals: birthday values are checked at load, each refusal names the value; a valid override loads.
func TestBirthdayRewardsRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.RewardsJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"claim_days": 7`, `"claim_days": 0`), "birthday.claim_days must be 1 to 30"},
		{mutateDefault(t, raw, `"claim_days": 7`, `"claim_days": 31`), "birthday.claim_days must be 1 to 30"},
		{mutateDefault(t, raw, `"gacha_discount_percent": 30`, `"gacha_discount_percent": 0`), "birthday.gacha_discount_percent must be 1 to 90"},
		{mutateDefault(t, raw, `"gacha_discount_percent": 30`, `"gacha_discount_percent": 91`), "birthday.gacha_discount_percent must be 1 to 90"},
		{mutateDefault(t, raw, `"discount_days": 7`, `"discount_days": 0`), "birthday.discount_days must be 1 to 365"},
		{mutateDefault(t, raw, `"discount_days": 7`, `"discount_days": 366`), "birthday.discount_days must be 1 to 365"},
		{mutateDefault(t, raw, `"claim_days": 7`, `"claim_day": 7`), "unknown field"},
	}
	for _, c := range cases {
		refuseFile(t, rewardsFile.LoadFrom, "rewards.json", c.text, c.want)
	}
	valid := mutateDefault(t, raw, `"gacha_discount_percent": 30`, `"gacha_discount_percent": 50`)
	path := filepath.Join(t.TempDir(), "rewards.json")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rewardsFile.LoadFrom(path); err != nil || ActiveRewards().Birthday.GachaDiscountPercent != 50 {
		t.Fatalf("valid override: err %v, percent %d", err, ActiveRewards().Birthday.GachaDiscountPercent)
	}
}
