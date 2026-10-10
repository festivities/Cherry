package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// gachaWebSetup installs a non-lab account "gwtok" and a small item root holding the gacha pool
// (fashion CUHA00001 and CUON004TV, furniture RUWA000HR and RUGO00002) plus one pet that must stay out.
func gachaWebSetup(t *testing.T, gems int64) *store.Account {
	t.Helper()
	root, _ := thumbFixture(t)
	for _, f := range [][3]string{
		{"dress", "225000001", "a.png"}, {"dress", "225106259", "a.aniproj"}, {"custom", "224400001", "a.png"},
		{"interior", "121400639", "a.png"}, {"interior", "122000002", "a.aniproj"}, {"pet", "326400016", "skeleton.xml"},
	} {
		writeThumbItem(t, root, f[0], f[1], f[2], []byte("x"))
	}
	acc := &store.Account{Aid: "9701", Name: "GachaWeb", Gender: "FEMALE", Gems: gems,
		ItemCodes: []string{"CUHA00001"}, InventoryCodes: []string{"CUHA00001"}}
	installSocialTestAccounts(t, map[string]*store.Account{"gwtok": acc})
	testutil.SetLab(t, "9701", false)
	return acc
}

func gachaWebBody(id, price string) string {
	return `{"language":"en","deviceType":"Android","gachaId":` + id + `,"price":` + price + `,"timeMagicYn":"N","adFreeYn":"N"}` + "\x00"
}

func TestGachaRoutes(t *testing.T) {
	gachaWebSetup(t, 1000)

	rec := decorReq(t, "", http.MethodGet, "/v4/shop/gachaList2/top/all?deviceType=Android", "")
	var top struct {
		Result struct {
			TotalCount string `json:"totalCount"`
			Items      []struct {
				ItemCode    string `json:"itemCode"`
				GachaID     int    `json:"gachaId"`
				Price       int    `json:"price"`
				BundlePrice int    `json:"bundlePrice"`
			} `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &top); rec.Code != 200 || err != nil || len(top.Result.Items) != 1 {
		t.Fatalf("top = %d %s (%v)", rec.Code, rec.Body, err)
	}
	if it := top.Result.Items[0]; it.ItemCode != "CUON004TV" || it.GachaID != 1001 || it.Price != 100 || it.BundlePrice != 900 {
		t.Errorf("top row = %+v", it)
	}

	rec = decorReq(t, "", http.MethodGet, "/v4/shop/gacha/detail/target/1001", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"collectionNo":1001`) || strings.Contains(rec.Body.String(), "PUPE") {
		t.Errorf("detail = %d %s", rec.Code, rec.Body)
	}
	rec = decorReq(t, "", http.MethodGet, "/v4/gacha/bonus/info?language=en&gachaNo=1001", "")
	if rec.Code != 200 || rec.Body.String() != `{"result":{}}` {
		t.Errorf("bonus = %d %s", rec.Code, rec.Body)
	}
	for _, target := range []string{"/v4/shop/gachaList2/banner/all/ja/Android", "/v4/shop/gachaList2/banner/top/en/Android",
		"/v4/shop/gachaList2/banner/promotion/en/Android", "/v4/shop/gachaList2/keyword", "/v4/shop/gachaList2/catg/all/1",
		"/v4/shop/gachaList2/catg/all/-1", "/v4/likes/popular/item",
		"/v4/home/sublist/gacha/10.1.0.0/Android.nhn?parentIconId=&isMain=true"} {
		if rec := decorReq(t, "", http.MethodGet, target, ""); rec.Code != 200 {
			t.Errorf("GET %s = %d, want 200", target, rec.Code)
		}
	}

	// Single pull: the balance is 1000 - 100 + the refund for a duplicate (rewardedCoin).
	rec = decorReq(t, "gwtok", http.MethodPost, "/v4/purchase/gacha/coin", gachaWebBody("1001", "100"))
	var single struct {
		Result struct {
			Balance      int    `json:"balance"`
			RewardedCoin string `json:"rewardedCoin"`
			ItemCode     string `json:"itemCode"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &single); rec.Code != 200 || err != nil {
		t.Fatalf("single = %d %s (%v)", rec.Code, rec.Body, err)
	}
	refund, err := strconv.Atoi(single.Result.RewardedCoin) // a string on the wire (verified parser)
	if err != nil || single.Result.Balance != 900+refund {
		t.Errorf("single balance %d with refund %d, want %d", single.Result.Balance, refund, 900+refund)
	}

	// Ten pulls: balance = before - 900 + the refunds of the ten logs.
	before := single.Result.Balance
	rec = decorReq(t, "gwtok", http.MethodPost, "/v4/purchase/gacha/multi", gachaWebBody("1001", "900"))
	var bundle struct {
		Result struct {
			Balance   int `json:"balance"`
			GachaLogs []struct {
				RewardedCoin string `json:"rewardedCoin"`
				Grade        string `json:"grade"`
			} `json:"gachaRewardLogs"`
			Rows []json.RawMessage `json:"purchaseRewardLogList"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bundle); rec.Code != 200 || err != nil {
		t.Fatalf("bundle = %d %s (%v)", rec.Code, rec.Body, err)
	}
	sum := 0
	for _, l := range bundle.Result.GachaLogs {
		n, _ := strconv.Atoi(l.RewardedCoin)
		sum += n
	}
	if len(bundle.Result.GachaLogs) != 10 || len(bundle.Result.Rows) != 0 || bundle.Result.Balance != before-900+sum {
		t.Errorf("bundle logs %d rows %d balance %d, want %d (before %d, refunds %d)", len(bundle.Result.GachaLogs),
			len(bundle.Result.Rows), bundle.Result.Balance, before-900+sum, before, sum)
	}

	// The result screen's retry button is an ordinary pull at retryPrice (100).
	if rec := decorReq(t, "gwtok", http.MethodPost, "/v4/purchase/gacha/coinretry", gachaWebBody("1001", "100")); rec.Code != 200 {
		t.Errorf("coinretry = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := decorReq(t, "", http.MethodGet, "/v4/shop/gachaList2/top/all", ""); !strings.Contains(rec.Body.String(), `"retryPrice":100`) {
		t.Errorf("retryPrice not 100: %s", rec.Body)
	}
	for _, v := range []string{"coinfirst", "coinfree", "ticket", "categoryticket", "clover"} {
		rec := decorReq(t, "gwtok", http.MethodPost, "/v4/purchase/gacha/"+v, gachaWebBody("1001", "100"))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"errorCode":"61003"`) {
			t.Errorf("%s = %d %s, want 400 61003", v, rec.Code, rec.Body)
		}
	}
	if rec := decorReq(t, "nobody", http.MethodPost, "/v4/purchase/gacha/coin", gachaWebBody("1001", "100")); rec.Code != 404 {
		t.Errorf("unknown session = %d, want 404", rec.Code)
	}
	if rec := decorReq(t, "gwtok", http.MethodPost, "/v4/purchase/gacha/coin", gachaWebBody("1001", "150")); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"errorCode":"61003"`) {
		t.Errorf("wrong price = %d %s, want 400 61003", rec.Code, rec.Body)
	}

	rec = decorReq(t, "", http.MethodGet, "/web/probability/item/gacha/1001", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || !strings.Contains(rec.Body.String(), "Normal: 90%") {
		t.Errorf("odds = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
