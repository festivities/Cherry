package web

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// setClock pins nowFunc for the test.
func setClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	cur := at
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return cur }
	t.Cleanup(func() { economy.NowFunc = old })
	return &cur
}

// 2026-10-07 12:00 UTC = 21:00 JST; the day boundary is 21:00 UTC.
var day0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func obj(t *testing.T, v any, key string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object in %v", key, v)
	}
	return m
}

func TestAttendTodayInfoShapes(t *testing.T) {
	ecoSetup(t, 0, 0, 0)
	clock := setClock(t, day0)
	_, today := ecoGet(t, "GET", "/v4/dailybonus/today", "")
	res := obj(t, today, "result")
	if res["todayCount"] != 1.0 || res["attendable"] != true || res["avtNo"] == "" || res["dailyBonusId"] != "134" {
		t.Fatalf("today %v", today)
	}
	// ResTodayData reads nextAttdFrom from the root as ints; 9 h to 21:00 UTC.
	next := obj(t, today, "nextAttdFrom")
	if next["hour"] != 9.0 || next["min"] != 0.0 {
		t.Fatalf("nextAttdFrom %v", next)
	}
	_, info := ecoGet(t, "GET", "/v4/dailybonus/info", "")
	res = obj(t, info, "result")
	if res["bonusCount"] != 7.0 || res["todayCount"] != 1.0 || res["attendable"] != true || res["lastDay"] != false || res["scheduleId"] != float64(economy.AttendScheduleID) {
		t.Fatalf("info %v", res)
	}
	// ResData: hour/min are asString.
	if n := obj(t, res, "nextAttdFrom"); n["hour"] != "9" || n["min"] != "0" {
		t.Fatalf("info nextAttdFrom %v", n)
	}
	bonus, ok := res["bonus"].([]any)
	if !ok || len(bonus) != 7 {
		t.Fatalf("bonus %v", res["bonus"])
	}
	wantGems := []float64{90, 120, 150, 180, 240, 300, 450}
	for i, b := range bonus {
		row := b.(map[string]any)
		if row["count"] != float64(i+1) || row["attendYN"] != false || row["gem"] != wantGems[i] {
			t.Fatalf("row %d %v", i, row)
		}
		for _, k := range []string{"bonusType", "bonusCategory", "itemCd", "voucherCode", "voucherUsePlace", "attdYmdt"} {
			if _, ok := row[k].(string); !ok {
				t.Fatalf("row %d %s not string", i, k)
			}
		}
		for _, k := range []string{"cash", "heart", "vipPoint", "dyeType"} {
			if _, ok := row[k].(float64); !ok {
				t.Fatalf("row %d %s not int", i, k)
			}
		}
		if _, ok := row["dyeItemBleach"].(bool); !ok {
			t.Fatal("dyeItemBleach not bool")
		}
	}
	if bonus[0].(map[string]any)["bonusType"] != "GEM" || bonus[6].(map[string]any)["bonusType"] != "VOUCH" ||
		bonus[6].(map[string]any)["cash"] != 15.0 || bonus[6].(map[string]any)["voucherUsePlace"] != "FACESHOP" {
		t.Fatalf("types %v", bonus)
	}
	*clock = day0.Add(11*time.Hour + 30*time.Minute) // 23:30 UTC, 21:30 past boundary, 21.5h left
	_, today = ecoGet(t, "GET", "/v4/dailybonus/today", "")
	if n := obj(t, today, "nextAttdFrom"); n["hour"] != 21.0 || n["min"] != 30.0 {
		t.Fatalf("remaining %v", n)
	}
	if code, _ := ecoGet(t, "GET", "/v4/dailybonus/today", ""); code != 200 {
		t.Fatal(code)
	}
	if rec := decorReq(t, "", "GET", "/v4/dailybonus/today", ""); rec.Code != 404 {
		t.Fatalf("no session %d", rec.Code)
	}
}

func TestAttendCreditsOnceAndWraps(t *testing.T) {
	acc, dir := ecoSetup(t, 0, 0, 0)
	clock := setClock(t, day0)
	attend := func() map[string]any {
		code, out := ecoGet(t, "GET", "/v4/dailybonus/attend", "")
		if code != 200 {
			t.Fatalf("attend %d", code)
		}
		return obj(t, out, "result")
	}
	res := attend()
	if res["totalCoinBalance"] != 90.0 || res["freeCoinBalance"] != 90.0 || res["payedCoinBalance"] != 0.0 ||
		res["totalCashBalance"] != 0.0 || res["reviewable"] != false {
		t.Fatalf("attend %v", res)
	}
	if n := obj(t, res, "nextAttdFrom"); n["hour"] != "9" {
		t.Fatalf("attend next %v", n)
	}
	// Same day: nothing more, no error, state reads attended.
	if res = attend(); res["totalCoinBalance"] != 90.0 {
		t.Fatalf("repeat credited %v", res)
	}
	*clock = day0.Add(time.Hour) // still before 21:00 UTC
	attend()
	_, info := ecoGet(t, "GET", "/v4/dailybonus/info", "")
	r := obj(t, info, "result")
	row0 := r["bonus"].([]any)[0].(map[string]any)
	if r["attendable"] != false || r["todayCount"] != 1.0 || row0["attendYN"] != true {
		t.Fatalf("after attend %v", r)
	}
	if l := readLedger(t, dir); len(l) != 1 || l[0].Delta != 90 || l[0].Reason != "daily login day 1" {
		t.Fatalf("ledger %+v", l)
	}
	// Days 2..7 on consecutive days, then wrap to day 1.
	want := []int64{120, 150, 180, 240, 300, 450}
	total := int64(90)
	for i, g := range want {
		*clock = clock.Add(24 * time.Hour)
		attend()
		total += g
		if acc.Gems != total {
			t.Fatalf("day %d gems %d want %d", i+2, acc.Gems, total)
		}
	}
	if acc.Cash != 15 || acc.FaceTickets != 3 || acc.AttendDay != 7 {
		t.Fatalf("day7 cash %d tickets %d day %d", acc.Cash, acc.FaceTickets, acc.AttendDay)
	}
	_, info = ecoGet(t, "GET", "/v4/dailybonus/info", "")
	r = obj(t, info, "result")
	if r["lastDay"] != true || r["attendable"] != false {
		t.Fatalf("day7 info %v", r)
	}
	*clock = clock.Add(24 * time.Hour)
	_, info = ecoGet(t, "GET", "/v4/dailybonus/info", "")
	r = obj(t, info, "result")
	if r["todayCount"] != 1.0 || r["attendable"] != true {
		t.Fatalf("wrap info %v", r)
	}
	for _, b := range r["bonus"].([]any) {
		if b.(map[string]any)["attendYN"] != false {
			t.Fatalf("wrap row %v", b)
		}
	}
	before := acc.Gems
	attend()
	if acc.Gems != before+90 || acc.AttendDay != 1 {
		t.Fatalf("wrap credit %d day %d", acc.Gems-before, acc.AttendDay)
	}
}

func TestAttendDayBoundaryAndMissedDays(t *testing.T) {
	acc, _ := ecoSetup(t, 0, 0, 0)
	setClock(t, time.Date(2026, 10, 7, 20, 59, 59, 0, time.UTC))
	ecoGet(t, "GET", "/v4/dailybonus/attend", "")
	if acc.AttendDay != 1 {
		t.Fatal("day1")
	}
	clock := setClock(t, time.Date(2026, 10, 7, 21, 0, 0, 0, time.UTC)) // 06:00 JST: new day
	if _, a, _, _ := economy.AttendState(acc, *clock); !a {
		t.Fatal("21:00 UTC must open a new day")
	}
	ecoGet(t, "GET", "/v4/dailybonus/attend", "")
	if acc.AttendDay != 2 || acc.Gems != 90+120 {
		t.Fatalf("day2 %d gems %d", acc.AttendDay, acc.Gems)
	}
	// Missing days does not reset: five days later the next attend is day 3.
	*clock = clock.Add(5 * 24 * time.Hour)
	ecoGet(t, "GET", "/v4/dailybonus/attend", "")
	if acc.AttendDay != 3 || acc.Gems != 90+120+150 {
		t.Fatalf("after gap %d gems %d", acc.AttendDay, acc.Gems)
	}
}

func TestAttendPersistsAndRollsBack(t *testing.T) {
	acc, dir := ecoSetup(t, 0, 0, 0)
	setClock(t, day0)
	ecoGet(t, "GET", "/v4/dailybonus/attend", "")
	if err := store.LoadAccountsFrom(filepath.Join(dir, "accounts.json")); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	got := store.Accounts["etok"]
	store.AccountsMu.Unlock()
	if got == nil || got.AttendDay != 1 || got.AttendKey != economy.AttendDayKey(day0) || got.Gems != 90 {
		t.Fatalf("reload %+v", got)
	}
	// A failing save leaves the account untouched.
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(dir, "no-such-dir", "x", "\x00bad")
	store.AccountsMu.Unlock()
	setClock(t, day0.Add(24*time.Hour))
	acc = got
	if code, _ := ecoGet(t, "GET", "/v4/dailybonus/attend", ""); code != 500 || acc.Gems != 90 || acc.AttendDay != 1 {
		t.Fatalf("rollback %d gems %d day %d", code, acc.Gems, acc.AttendDay)
	}
}

func TestAdminAttendShiftAndReset(t *testing.T) {
	acc, _ := ecoSetup(t, 0, 0, 0)
	setClock(t, day0)
	ecoGet(t, "GET", "/v4/dailybonus/attend", "")
	post := func(remote, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/admin/attend", strings.NewReader(body))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		economy.NewAdminMux().ServeHTTP(rec, req)
		return rec
	}
	if rec := post("192.168.0.5:1", `{"aid":"9401"}`); rec.Code != 404 {
		t.Fatalf("remote %d", rec.Code)
	}
	if rec := post("127.0.0.1:1", `{"aid":"9401","shiftDays":-1}`); rec.Code != 400 {
		t.Fatalf("neg %d", rec.Code)
	}
	if rec := post("127.0.0.1:1", `{"aid":"nobody"}`); rec.Code != 404 {
		t.Fatalf("unknown %d", rec.Code)
	}
	if rec := post("127.0.0.1:1", `{"aid":"9401"}`); rec.Code != 200 {
		t.Fatalf("shift %d %s", rec.Code, rec.Body)
	}
	if _, a, _, _ := economy.AttendState(acc, day0); !a {
		t.Fatal("shifted account should be attendable")
	}
	ecoGet(t, "GET", "/v4/dailybonus/attend", "")
	if acc.AttendDay != 2 {
		t.Fatalf("day %d", acc.AttendDay)
	}
	if rec := post("[::1]:1", `{"aid":"9401","reset":true}`); rec.Code != 200 || acc.AttendDay != 0 {
		t.Fatalf("reset %d day %d", rec.Code, acc.AttendDay)
	}
}

// ---- mailbox ----

func adminMail(t *testing.T, remote, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/mail", strings.NewReader(body))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	economy.NewAdminMux().ServeHTTP(rec, req)
	return rec
}

func TestMailboxLifecycle(t *testing.T) {
	acc, dir := ecoSetup(t, 10, 0, 0)
	clock := setClock(t, day0)
	if _, out := ecoGet(t, "GET", "/v4/postbox/exist", ""); out["result"] != false {
		t.Fatalf("empty exist %v", out)
	}
	_, out := ecoGet(t, "GET", "/v4/postbox/list", "")
	res := obj(t, out, "result")
	if items, ok := res["items"].([]any); !ok || len(items) != 0 || res["totalCount"] != "0" || res["nextCursor"] != "0" {
		t.Fatalf("empty list %v", res)
	}
	for _, b := range []string{
		`{"aid":"9401","type":"gems","amount":500,"message":"hello","expiresDays":7}`,
		`{"aid":"9401","type":"cash","amount":5}`,
		`{"aid":"9401","type":"faceTickets","amount":2,"message":"tickets"}`,
	} {
		if rec := adminMail(t, "127.0.0.1:9", b); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"mailed":1`) {
			t.Fatalf("%s: %d %s", b, rec.Code, rec.Body)
		}
	}
	if _, out := ecoGet(t, "GET", "/v4/postbox/exist", ""); out["result"] != true {
		t.Fatalf("exist %v", out)
	}
	_, out = ecoGet(t, "GET", "/v4/postbox/list", "")
	res = obj(t, out, "result")
	items := res["items"].([]any)
	if len(items) != 3 || res["totalCount"] != "3" {
		t.Fatalf("list %v", res)
	}
	// Newest first: voucher, cash, gem. Every Post field has the native type.
	types := []string{"voucher", "cash", "gem"}
	for i, it := range items {
		row := it.(map[string]any)
		if row["postType"] != types[i] {
			t.Fatalf("row %d type %v", i, row["postType"])
		}
		for _, k := range []string{"postSeq", "postType", "productData", "productDataDetail", "productImageUrl", "title", "content",
			"expireDate", "itemType", "itemGradeType", "itemSpecialEffects", "sent", "flagType"} {
			if _, ok := row[k].(string); !ok {
				t.Fatalf("row %d %s not string: %v", i, k, row[k])
			}
		}
		for _, k := range []string{"dyeType", "recycleReward"} {
			if _, ok := row[k].(float64); !ok {
				t.Fatalf("row %d %s not int", i, k)
			}
		}
		for _, k := range []string{"fromBlocked", "canReply"} {
			if _, ok := row[k].(bool); !ok {
				t.Fatalf("row %d %s not bool", i, k)
			}
		}
		if obj(t, row, "senderAvatar")["name"] != economy.MailSender {
			t.Fatal("sender")
		}
	}
	gem, ticket := items[2].(map[string]any), items[0].(map[string]any)
	if gem["productData"] != "500" || gem["content"] != "hello" || ticket["productData"] != "0" || ticket["productDataDetail"] != "2" {
		t.Fatalf("amounts %v %v", gem, ticket)
	}
	if gem["expireDate"] != strconv.FormatInt(day0.Add(7*24*time.Hour).UnixMilli(), 10) || gem["sent"] != strconv.FormatInt(day0.UnixMilli(), 10) {
		t.Fatalf("times %v %v", gem["expireDate"], gem["sent"])
	}
	// Receive credits via the ledger and removes the row; a second receive is a clean error.
	gemSeq := gem["postSeq"].(string)
	code, body := ecoGet(t, "POST", "/v4/postbox/receive/"+gemSeq, "")
	if code != 200 || body["result"] != true || acc.Gems != 510 || len(acc.Mail) != 2 {
		t.Fatalf("receive %d %v gems %d mail %d", code, body, acc.Gems, len(acc.Mail))
	}
	code, body = ecoGet(t, "POST", "/v4/postbox/receive/"+gemSeq, "")
	if code != 400 || body["errorCode"] != "62107" || acc.Gems != 510 {
		t.Fatalf("double receive %d %v gems %d", code, body, acc.Gems)
	}
	if code, _ := ecoGet(t, "POST", "/v4/postbox/receive/junk", ""); code != 400 {
		t.Fatalf("junk %d", code)
	}
	if code, _ := ecoGet(t, "GET", "/v4/postbox/receive/"+gemSeq, ""); code != 404 {
		t.Fatalf("GET receive %d", code)
	}
	// Ticket and cash, plus reject without credit.
	tSeq := ticket["postSeq"].(string)
	ecoGet(t, "POST", "/v4/postbox/receive/"+tSeq, "")
	if acc.FaceTickets != 2 {
		t.Fatalf("tickets %d", acc.FaceTickets)
	}
	cashSeq := items[1].(map[string]any)["postSeq"].(string)
	if code, body := ecoGet(t, "POST", "/v4/postbox/reject/"+cashSeq, ""); code != 200 || body["result"] != true || acc.Cash != 0 || len(acc.Mail) != 0 {
		t.Fatalf("reject %d %v cash %d mail %d", code, body, acc.Cash, len(acc.Mail))
	}
	if code, body := ecoGet(t, "POST", "/v4/postbox/reject/"+cashSeq, ""); code != 200 || body["result"] != true {
		t.Fatalf("repeat reject %d %v", code, body)
	}
	if _, out := ecoGet(t, "GET", "/v4/postbox/exist", ""); out["result"] != false {
		t.Fatalf("exist after %v", out)
	}
	l := readLedger(t, dir)
	if len(l) != 2 || l[0].Reason != "mail receive 1" || l[0].Delta != 500 || l[1].Currency != "faceTickets" {
		t.Fatalf("ledger %+v", l)
	}
	// Persisted.
	if err := store.LoadAccountsFrom(filepath.Join(dir, "accounts.json")); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	again := store.Accounts["etok"]
	store.AccountsMu.Unlock()
	if again.NextMailSeq != 3 || len(again.Mail) != 0 || again.Gems != 510 {
		t.Fatalf("reload %+v", again)
	}
	_ = clock
}

func TestMailboxExpiry(t *testing.T) {
	acc, dir := ecoSetup(t, 0, 0, 0)
	clock := setClock(t, day0)
	adminMail(t, "127.0.0.1:9", `{"aid":"9401","type":"gems","amount":100,"expiresDays":2}`)
	adminMail(t, "127.0.0.1:9", `{"aid":"9401","type":"gems","amount":200,"expiresDays":10}`)
	*clock = day0.Add(3 * 24 * time.Hour)
	_, out := ecoGet(t, "GET", "/v4/postbox/list", "")
	if items := obj(t, out, "result")["items"].([]any); len(items) != 1 || items[0].(map[string]any)["productData"] != "200" {
		t.Fatalf("expired still listed %v", items)
	}
	code, body := ecoGet(t, "POST", "/v4/postbox/receive/1", "")
	if code != 400 || body["errorCode"] != "62102" || acc.Gems != 0 || len(acc.Mail) != 1 {
		t.Fatalf("expired receive %d %v gems %d mail %d", code, body, acc.Gems, len(acc.Mail))
	}
	*clock = day0.Add(11 * 24 * time.Hour)
	if _, out := ecoGet(t, "GET", "/v4/postbox/exist", ""); out["result"] != false {
		t.Fatal("all expired should clear the badge")
	}
	if l := readLedger(t, dir); len(l) != 0 {
		t.Fatalf("ledger %+v", l)
	}
}

func TestAdminMailRulesAndAll(t *testing.T) {
	acc, _ := ecoSetup(t, 0, 0, 0)
	other := &store.Account{AccessToken: "o2", SessionKey: "sk", Aid: "9402", Name: "Two"}
	store.AccountsMu.Lock()
	store.Accounts["o2"] = other
	store.AccountsMu.Unlock()
	setClock(t, day0)
	good := `{"aid":"9401","type":"gems","amount":5}`
	for _, remote := range []string{"192.168.1.9:5000", "10.0.2.2:1", "garbage"} {
		if rec := adminMail(t, remote, good); rec.Code != 404 || len(acc.Mail) != 0 {
			t.Fatalf("%s: %d", remote, rec.Code)
		}
	}
	if rec := decorReq(t, "etok", "POST", "/admin/mail", good); rec.Code != 404 {
		t.Fatalf("public mux %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/admin/mail", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	economy.NewAdminMux().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("GET %d", rec.Code)
	}
	for _, b := range []string{`{"aid":"9401","type":"items","amount":5}`, `{"aid":"9401","type":"gems","amount":0}`,
		`{"aid":"9401","type":"gems","amount":-3}`, `{"type":"gems","amount":3}`, `{"aid":"9401","type":"gems","amount":1,"expiresDays":-1}`, `bad`} {
		if rec := adminMail(t, "127.0.0.1:1", b); rec.Code != 400 {
			t.Fatalf("%s: %d", b, rec.Code)
		}
	}
	if rec := adminMail(t, "127.0.0.1:1", `{"aid":"nobody","type":"gems","amount":3}`); rec.Code != 404 {
		t.Fatalf("unknown %d", rec.Code)
	}
	if rec := adminMail(t, "127.0.0.1:1", `{"aid":"all","type":"cash","amount":4,"message":"all hands"}`); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"mailed":2`) || len(acc.Mail) != 1 || len(other.Mail) != 1 {
		t.Fatalf("all %d %s", rec.Code, rec.Body)
	}
	// The mailbox is capped.
	for i := 0; i < economy.MailMaxRows; i++ {
		adminMail(t, "127.0.0.1:1", good)
	}
	if rec := adminMail(t, "[::1]:1", good); rec.Code != 400 || len(acc.Mail) != economy.MailMaxRows {
		t.Fatalf("cap %d len %d", rec.Code, len(acc.Mail))
	}
	var m store.MailRow
	if err := json.Unmarshal([]byte(`{"postSeq":7,"type":"gems","amount":1,"expires":2}`), &m); err != nil || m.Seq != 7 {
		t.Fatal("mailRow json")
	}
}
