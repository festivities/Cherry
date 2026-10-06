package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// setLab marks or unmarks aid as a lab account for the test.
func setLab(t *testing.T, aid string, lab bool) {
	t.Helper()
	old, had := labAids[aid]
	labAids[aid] = lab
	t.Cleanup(func() {
		if had {
			labAids[aid] = old
		} else {
			delete(labAids, aid)
		}
	})
}

// ecoSetup installs one account with a file-backed store so the ledger log is written.
func ecoSetup(t *testing.T, gems, cash, tickets int64) (*account, string) {
	t.Helper()
	acc := &account{accessToken: "etok", sessionKey: "sk", aid: "9401", name: "Eco", gender: "FEMALE", gems: gems, cash: cash, faceTickets: tickets}
	installSocialTestAccounts(t, map[string]*account{"etok": acc})
	setLab(t, "9401", false)
	dir := t.TempDir()
	accountsMu.Lock()
	accountStorePath = filepath.Join(dir, "accounts.json")
	nextAvatarID = 9401
	latestAcc = acc
	accountsMu.Unlock()
	return acc, dir
}

func readLedger(t *testing.T, dir string) []ledgerLine {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "ledger.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []ledgerLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l ledgerLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	return out
}

func ecoGet(t *testing.T, method, target, body string) (int, map[string]any) {
	t.Helper()
	rec := decorReq(t, "etok", method, target, body)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestLedgerApplyOverdraftAndLog(t *testing.T) {
	acc, dir := ecoSetup(t, 100, 5, 2)
	accountsMu.Lock()
	err := applyDeltasLocked(acc, ledgerDeltas{Gems: -40, FaceTickets: 3}, "test")
	g, c, ft := acc.gems, acc.cash, acc.faceTickets
	accountsMu.Unlock()
	if err != nil || g != 60 || c != 5 || ft != 5 {
		t.Fatalf("apply: %v %d %d %d", err, g, c, ft)
	}
	// Overdraft on one currency rejects the whole set, nothing changes, nothing logged.
	accountsMu.Lock()
	err = applyDeltasLocked(acc, ledgerDeltas{Gems: 10, Cash: -6}, "bad")
	g, c = acc.gems, acc.cash
	accountsMu.Unlock()
	var be *balanceError
	if !errors.As(err, &be) || be.Currency != "cash" || g != 60 || c != 5 {
		t.Fatalf("overdraft: %v %d %d", err, g, c)
	}
	accountsMu.Lock()
	err = applyDeltasLocked(acc, ledgerDeltas{Gems: maxBalance}, "cap")
	accountsMu.Unlock()
	if !errors.As(err, &be) {
		t.Fatalf("cap not enforced: %v", err)
	}
	lines := readLedger(t, dir)
	if len(lines) != 2 || lines[0].Currency != "gems" || lines[0].Delta != -40 || lines[0].Balance != 60 ||
		lines[1].Currency != "faceTickets" || lines[1].Balance != 5 || lines[0].Aid != "9401" || lines[0].Reason != "test" || lines[0].Time == "" {
		t.Fatalf("ledger = %+v", lines)
	}
	// Persisted additively and reloaded.
	accountsMu.Lock()
	accounts, latestAcc = map[string]*account{}, nil
	accountsMu.Unlock()
	if err := loadAccountsFrom(accountStorePath); err != nil {
		t.Fatal(err)
	}
	accountsMu.Lock()
	r := accounts["etok"]
	accountsMu.Unlock()
	if r == nil || r.gems != 60 || r.faceTickets != 5 {
		t.Fatalf("reloaded %+v", r)
	}
}

func TestLedgerSaveFailureRollsBackAndSkipsLog(t *testing.T) {
	acc, dir := ecoSetup(t, 100, 0, 0)
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	accountsMu.Lock()
	accountStorePath = filepath.Join(blocker, "accounts.json")
	err := applyDeltasLocked(acc, ledgerDeltas{Gems: -10}, "x")
	g := acc.gems
	accountsMu.Unlock()
	if err == nil || g != 100 {
		t.Fatalf("err %v gems %d", err, g)
	}
	if l := readLedger(t, dir); l != nil {
		t.Fatalf("logged after failed save: %v", l)
	}
}

func TestLedgerLogFailureIsNonFatal(t *testing.T) {
	acc, dir := ecoSetup(t, 100, 0, 0)
	if err := os.Mkdir(filepath.Join(dir, "ledger.jsonl"), 0o700); err != nil { // a directory: open fails
		t.Fatal(err)
	}
	accountsMu.Lock()
	err := applyDeltasLocked(acc, ledgerDeltas{Gems: -10}, "x")
	g := acc.gems
	accountsMu.Unlock()
	if err != nil || g != 90 {
		t.Fatalf("err %v gems %d", err, g)
	}
}

func TestOldStoreLoadsZeroBalances(t *testing.T) {
	ecoSetup(t, 0, 0, 0)
	if err := os.WriteFile(accountStorePath, []byte(`{"version":1,"accounts":{"etok":{"accessToken":"etok","sessionKey":"sk","aid":"9401","itemCodes":null,"inventoryCodes":null}},"aliases":{"etok":"etok"},"latest":"etok","nextAvatarId":9401}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadAccountsFrom(accountStorePath); err != nil {
		t.Fatal(err)
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	if a := accounts["etok"]; a == nil || a.gems != 0 || a.cash != 0 || a.faceTickets != 0 || a.welcomed {
		t.Fatalf("loaded %+v", a)
	}
}

func TestGoodbyeShape(t *testing.T) {
	rec := decorReq(t, "", "GET", "/v4/setting/goodbye/cherry", "")
	var v struct {
		Result map[string]any `json:"result"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if v.Result["billingEnd"] != true || v.Result["forceRefundPopup"] != false || v.Result["refundUrl"] != "" || len(v.Result) != 3 {
		t.Fatalf("goodbye %v", v.Result)
	}
}

func TestRealMoneyRoutesStay404(t *testing.T) {
	ecoSetup(t, 0, 0, 0)
	for _, p := range []string{
		"/v4/bill/prod/basic", "/v4/bill/prod/starter", "/v4/bill/prod/limited", "/v4/bill/prod/package",
		"/v4/bill/package2/result/", "/v4/coin/charge", "/v4/direct/reserveCash", "/v4/coin/ageover",
		"/v4/coin/period/list", "/v4/coin/period/remain", "/v4/seasonpass/bill/info", "/v4/admob/find",
		"/v4/quest/video/find/", "/v4/coin/use/reserve", "/v4/coin/checkChancePopup",
	} {
		for _, m := range []string{"GET", "POST"} {
			if rec := decorReq(t, "etok", m, p, "{}"); rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", m, p, rec.Code)
			}
		}
	}
}

func TestBalanceRoutes(t *testing.T) {
	ecoSetup(t, 1234, 56, 0)
	for unit, want := range map[string]string{"GEM": "1234", "CASH": "56", "": "1234"} {
		rec := decorReq(t, "etok", "GET", "/v4/coin/balance?deviceType=Android&coinUnit="+unit, "")
		exp := `{"result":{"totalCoinBalance":` + want + `,"freeCoinBalance":` + want + `,"payedCoinBalance":0}}`
		if rec.Code != 200 || rec.Body.String() != exp {
			t.Errorf("%q: %d %s", unit, rec.Code, rec.Body)
		}
	}
	if rec := decorReq(t, "nope", "GET", "/v4/coin/balance?coinUnit=GEM", ""); rec.Code != 404 || rec.Body.String() != unknownSessionBody {
		t.Errorf("unknown session %d %s", rec.Code, rec.Body)
	}
	_, vip := ecoGet(t, "GET", "/v4/vip/balance", "")
	r := vip["result"].(map[string]any)
	if r["grade"] != "NONE" || r["balance"] != float64(0) || r["isNewVip"] != false ||
		r["vipGradeTable"].(map[string]any)["NONE"] != float64(0) || r["vipGradeTableTotal"].(map[string]any)["ROYAL"] != float64(0) {
		t.Errorf("vip %v", r)
	}
	_, hb := ecoGet(t, "GET", "/v4/heart/extra/balance/detail", "")
	h := hb["result"].(map[string]any)
	if h["totalCount"] != float64(0) || h["freeTotalCount"] != float64(0) || h["paidCount"] != float64(0) {
		t.Errorf("hearts %v", h)
	}
}

func TestWelcomeGiftOnce(t *testing.T) {
	acc, dir := ecoSetup(t, 0, 0, 0)
	_, out := ecoGet(t, "POST", "/v4/create/complete", `{"inviteCode":""}`)
	r := out["result"].(map[string]any)
	if r["status"] != true || r["rewardCoin"] != float64(300) || len(r) != 2 {
		t.Fatalf("first %v", r)
	}
	_, out = ecoGet(t, "POST", "/v4/create/complete", `{"inviteCode":""}`)
	r = out["result"].(map[string]any)
	if r["status"] != true || r["rewardCoin"] != float64(0) { // never status:false: the client retries it
		t.Fatalf("repeat %v", r)
	}
	accountsMu.Lock()
	g := acc.gems
	accountsMu.Unlock()
	if g != 300 {
		t.Fatalf("gems %d", g)
	}
	if l := readLedger(t, dir); len(l) != 1 || l[0].Reason != "welcome gift" || l[0].Delta != 300 {
		t.Fatalf("ledger %+v", l)
	}
	if rec := decorReq(t, "etok", "GET", "/v4/create/complete", ""); rec.Code != 404 {
		t.Fatalf("GET %d", rec.Code)
	}
	// No AV_AUTH: the newest account is the requester (already welcomed here), never a 404.
	if rec := decorReq(t, "", "POST", "/v4/create/complete", "{}"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":true`) {
		t.Fatalf("anon %d %s", rec.Code, rec.Body)
	}
}

func TestFaceTicketConsumeAndInsufficient(t *testing.T) {
	acc, dir := ecoSetup(t, 0, 0, 1)
	acc.itemCodes, acc.inventoryCodes = []string{"CUEY00002"}, []string{"CUEY00002"}
	body := `{"saveItemList":[{"itemCode":"CUEY0030Q"}],"useVoucher":"true"}`
	rec := decorReq(t, "etok", "POST", faceShopPurchaseURL, body)
	if rec.Code != 200 || acc.faceTickets != 0 {
		t.Fatalf("buy: %d %s tickets %d", rec.Code, rec.Body, acc.faceTickets)
	}
	if l := readLedger(t, dir); len(l) != 1 || l[0].Currency != "faceTickets" || l[0].Delta != -1 || l[0].Balance != 0 {
		t.Fatalf("ledger %+v", l)
	}
	// No ticket left: clean error, no face granted or equipped.
	rec = decorReq(t, "etok", "POST", faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUMO001ET"}],"useVoucher":"false"}`)
	var e map[string]string
	if rec.Code != 400 || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e["errorCode"] != "61001" {
		t.Fatalf("insufficient: %d %s", rec.Code, rec.Body)
	}
	if strings.Join(acc.itemCodes, ",") != "CUEY0030Q" || hasCode(acc.inventoryCodes, "CUMO001ET") {
		t.Fatalf("mutated: %v %v", acc.itemCodes, acc.inventoryCodes)
	}
	_, c := ecoGet(t, "GET", "/v4/voucher/own/count/faceshop", "")
	if c["result"].(map[string]any)["voucherCount"] != float64(0) {
		t.Fatalf("count %v", c)
	}
}

func TestTicketShopListAndPurchase(t *testing.T) {
	acc, dir := ecoSetup(t, 700, 0, 0)
	_, out := ecoGet(t, "GET", "/v4/voucher/product/list/faceshop?isOnlyGemProduct=true", "")
	rows := out["result"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows %v", rows)
	}
	for _, rr := range rows {
		row := rr.(map[string]any)
		for _, k := range []string{"productCode", "productName", "productImagePath", "voucherCode", "voucherUsePlace", "paymentType", "sellType", "cpId", "discountStart", "discountEnd"} {
			if _, ok := row[k].(string); !ok {
				t.Errorf("%s not string: %v", k, row[k])
			}
		}
		for _, k := range []string{"voucherGiveCount", "baseDiscountPercentage", "standardPrice", "finalPrice"} {
			if _, ok := row[k].(float64); !ok {
				t.Errorf("%s not number: %v", k, row[k])
			}
		}
		if row["productCode"] == "" || row["cpId"] == "" || row["paymentType"] != "GEM" {
			t.Errorf("row %v", row)
		}
	}
	buy := func(body string) (int, map[string]any) { return ecoGet(t, "POST", "/v4/voucher/purchase", body) }
	// Wrong client price, wrong unit, unknown product: rejected without mutation.
	for _, b := range []string{
		`{"coinUnit":"GEM","productCode":"FSV5","price":"1","cpId":"x"}`,
		`{"coinUnit":"CASH","productCode":"FSV5","price":650}`,
		`{"coinUnit":"GEM","productCode":"NOPE","price":650}`,
		`{"coinUnit":"GEM","productCode":"FSV5"}`,
	} {
		if code, _ := buy(b); code != 400 {
			t.Errorf("%s: %d", b, code)
		}
	}
	if acc.gems != 700 || acc.faceTickets != 0 {
		t.Fatalf("mutated %d %d", acc.gems, acc.faceTickets)
	}
	code, resp := buy(`{"language":"en","deviceType":"Android","coinUnit":"GEM","productCode":"FSV5","price":"650","cpId":"cherry"}`)
	r := resp["result"].(map[string]any)
	if code != 200 || r["avatarId"] != "9401" || r["productCode"] != "FSV5" || r["paymentType"] != "GEM" || r["balance"] != float64(50) || acc.faceTickets != 5 {
		t.Fatalf("buy %d %v tickets %d", code, resp, acc.faceTickets)
	}
	if code, _ = buy(`{"coinUnit":"GEM","productCode":"FSV1","price":150}`); code != 400 || acc.gems != 50 || acc.faceTickets != 5 {
		t.Fatalf("insufficient %d gems %d", code, acc.gems)
	}
	if l := readLedger(t, dir); len(l) != 2 || l[0].Delta != -650 || l[1].Delta != 5 {
		t.Fatalf("ledger %+v", l)
	}
	if rec := decorReq(t, "etok", "GET", "/v4/voucher/own/list/faceshop", ""); rec.Body.String() != `{"result":[]}` {
		t.Fatalf("own list %s", rec.Body)
	}
}

func TestSeedingLabVersusNewAccount(t *testing.T) {
	lab := &account{aid: "1001", name: "Lab"}
	fresh := &account{aid: "9402", name: "New"}
	installSocialTestAccounts(t, map[string]*account{"ltok": lab, "ntok": fresh})
	setLab(t, "9402", false)
	for tok, aid := range map[string]string{"ltok": "1001", "ntok": "9402"} {
		if rec := decorReq(t, tok, "GET", "/v4/inven/interior/items/all", ""); rec.Code != 200 {
			t.Fatalf("%s %d", tok, rec.Code)
		}
		if rec := decorReq(t, tok, "GET", "/v4/pet/inven/represent?avatarId="+aid, ""); rec.Code != 200 {
			t.Fatalf("pet %d", rec.Code)
		}
	}
	if len(lab.roomItems) != len(roomShowcaseCodes) || len(lab.pets) != len(petShowcaseNums) {
		t.Fatalf("lab items %d pets %d", len(lab.roomItems), len(lab.pets))
	}
	var codes []string
	for _, it := range fresh.roomItems {
		codes = append(codes, it.Cd)
	}
	if strings.Join(codes, ",") != "RUTI000EW,RUWA000HR,RUDO0002Z,RUAB00002" || len(fresh.pets) != 0 {
		t.Fatalf("new account items %v pets %v", codes, fresh.pets)
	}
	lay := fresh.rooms[defaultLevel]
	if lay.Floor.Cd != "RUTI000EW" || lay.Wall.Cd != "RUWA000HR" || len(lay.Placed) != 2 {
		t.Fatalf("layout %+v", lay)
	}
	rec := decorReq(t, "ntok", "GET", "/v4/pet/room/arrange/list/9402/LEVEL_1", "")
	if rec.Body.String() != `{"result":{"list":[]}}` {
		t.Fatalf("arranged %s", rec.Body)
	}
	rec = decorReq(t, "ntok", "GET", "/v4/pet/inven/represent?avatarId=9402", "")
	if rec.Body.String() != `{"result":[]}` {
		t.Fatalf("represent %s", rec.Body)
	}
}

func adminReq(t *testing.T, remote, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/admin/grant", strings.NewReader(body))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	newAdminMux().ServeHTTP(rec, req)
	return rec
}

func TestAdminGrantLoopbackOnly(t *testing.T) {
	acc, dir := ecoSetup(t, 10, 0, 0)
	body := `{"aid":"9401","gems":100,"cash":7,"faceTickets":2,"reason":"qa"}`
	for _, remote := range []string{"192.168.1.9:5000", "10.0.2.2:5000", "8.8.8.8:1", "garbage"} {
		if rec := adminReq(t, remote, "POST", body); rec.Code != 404 || acc.gems != 10 {
			t.Fatalf("%s: %d gems %d", remote, rec.Code, acc.gems)
		}
	}
	if rec := adminReq(t, "127.0.0.1:5000", "GET", body); rec.Code != 404 {
		t.Fatalf("GET %d", rec.Code)
	}
	rec := adminReq(t, "127.0.0.1:5000", "POST", body)
	if rec.Code != 200 || acc.gems != 110 || acc.cash != 7 || acc.faceTickets != 2 {
		t.Fatalf("grant %d %s", rec.Code, rec.Body)
	}
	if rec := adminReq(t, "[::1]:5000", "POST", `{"aid":"9401","gems":-110}`); rec.Code != 200 || acc.gems != 0 {
		t.Fatalf("ipv6 %d", rec.Code)
	}
	for _, b := range []string{`{"aid":"9401","gems":-1}`, `{"aid":"nobody","gems":1}`, `{"gems":1}`, `bad`} {
		if rec := adminReq(t, "127.0.0.1:5000", "POST", b); rec.Code < 400 || acc.gems != 0 {
			t.Fatalf("%s: %d", b, rec.Code)
		}
	}
	if l := readLedger(t, dir); len(l) != 4 || !strings.HasPrefix(l[0].Reason, "staff grant: qa") {
		t.Fatalf("ledger %+v", l)
	}
	// Not reachable through the public mux.
	if rec := decorReq(t, "etok", "POST", "/admin/grant", body); rec.Code != 404 {
		t.Fatalf("public mux %d", rec.Code)
	}
}

func TestFaceShopResaveCurrentFaceIsFree(t *testing.T) {
	acc, _ := ecoSetup(t, 0, 0, 0)
	acc.itemCodes, acc.inventoryCodes = []string{"CUEY00002"}, []string{"CUEY00002"}
	rec := decorReq(t, "etok", "POST", faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUEY00002"}],"useVoucher":"true"}`)
	if rec.Code != 200 || acc.faceTickets != 0 {
		t.Fatalf("re-save current face: %d %s tickets %d", rec.Code, rec.Body, acc.faceTickets)
	}
}

func TestFaceShopOwnedFaceRefused(t *testing.T) {
	acc, _ := ecoSetup(t, 0, 0, 1)
	// CUEY0030Q is owned (bought earlier) but CUEY00002 is worn: switching back is refused.
	acc.itemCodes, acc.inventoryCodes = []string{"CUEY00002"}, []string{"CUEY00002", "CUEY0030Q"}
	rec := decorReq(t, "etok", "POST", faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUEY0030Q"}],"useVoucher":"true"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"61009"`) || acc.faceTickets != 1 || slices.Contains(acc.itemCodes, "CUEY0030Q") {
		t.Fatalf("owned face: %d %s tickets %d items %v", rec.Code, rec.Body, acc.faceTickets, acc.itemCodes)
	}
	// Owned + a new face together is a normal purchase of one ticket.
	rec = decorReq(t, "etok", "POST", faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUEY0030Q"},{"itemCode":"CUMO001ET"}],"useVoucher":"true"}`)
	if rec.Code != 200 || acc.faceTickets != 0 || !slices.Contains(acc.itemCodes, "CUMO001ET") {
		t.Fatalf("mixed: %d %s tickets %d items %v", rec.Code, rec.Body, acc.faceTickets, acc.itemCodes)
	}
}

func TestSpecialChanceEmpty(t *testing.T) {
	for _, unit := range []string{"gem", "cash", "heart"} {
		rec := decorReq(t, "", "GET", "/v4/bill/prod/spot/"+unit+"?deviceType=Android&marketLocale=", "")
		if rec.Code != 200 || rec.Body.String() != emptySpecialChanceBody {
			t.Fatalf("%s: %d %s", unit, rec.Code, rec.Body)
		}
	}
	if rec := decorReq(t, "", "GET", "/v4/bill/prod/spot/gem/x", ""); rec.Code != 404 {
		t.Fatalf("subpath %d", rec.Code)
	}
}
