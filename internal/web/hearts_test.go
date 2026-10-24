package web

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// heartTestClock pins the economy clock to a fixed JST time for one test.
func heartTestClock(t *testing.T, now time.Time) {
	t.Helper()
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })
}

func heartTestAccounts(t *testing.T) {
	t.Helper()
	installSocialTestAccounts(t, map[string]*store.Account{
		"hq-tok":    {AccessToken: "hq-tok", SessionKey: "sk", Aid: "9901", Name: "Sender"},
		"hq-friend": {AccessToken: "hq-friend", SessionKey: "sk", Aid: "9902", Name: "Receiver"},
	})
}

func heartTestNow() time.Time {
	return time.Date(2026, time.October, 22, 12, 0, 0, 0, time.FixedZone("JST", 9*3600))
}

// TestQuestStatusHeartBlock: the JSON numbers are numbers and the progress and expiry strings are
// strings; the block follows the caller's own sends.
func TestQuestStatusHeartBlock(t *testing.T) {
	heartTestClock(t, heartTestNow())
	heartTestAccounts(t)
	rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/quest/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	wantNumbers := map[string]float64{"heart": 5, "heartBase": 5, "heartRewardCoin": 30}
	for k, want := range wantNumbers {
		if v, ok := got.Result[k].(float64); !ok || v != want {
			t.Fatalf("%s = %#v, want number %v (body %s)", k, got.Result[k], want, rec.Body.String())
		}
	}
	if got.Result["questProgress"] != "ONGOING" {
		t.Fatalf("questProgress = %#v, want ONGOING", got.Result["questProgress"])
	}
	expire, ok := got.Result["toExpire"].(string)
	if !ok || !regexp.MustCompile(`^[0-9]+$`).MatchString(expire) || expire != "64800" {
		t.Fatalf("toExpire = %#v, want decimal string 64800 (18 h to the 06:00 JST reset)", got.Result["toExpire"])
	}
	for range economy.HeartBase() {
		if !economy.HeartSend("9901", "9902", heartTestNow()) {
			t.Fatal("heart refused")
		}
	}
	rec = socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/quest/status", "")
	if want := `"heart":0`; !regexp.MustCompile(want).MatchString(rec.Body.String()) {
		t.Fatalf("after cap: %s", rec.Body.String())
	}
	if !regexp.MustCompile(`"questProgress":"COMPLETED"`).MatchString(rec.Body.String()) {
		t.Fatalf("after cap progress: %s", rec.Body.String())
	}
}

// TestQuestStatusUnknownSession keeps the zero body and rejects other methods.
func TestQuestStatusUnknownSession(t *testing.T) {
	heartTestClock(t, heartTestNow())
	if rec := serve(t, http.MethodGet, "/v4/quest/status"); rec.Body.String() != questStatusBody {
		t.Fatalf("unknown session body = %s", rec.Body.String())
	}
	if rec := serve(t, http.MethodPost, "/v4/quest/status"); rec.Code != http.StatusNotFound {
		t.Fatalf("POST = %d, want 404", rec.Code)
	}
}

// TestHeartRoutes covers heart/haveHeart and heart/receivable as {"result":<bool>}.
func TestHeartRoutes(t *testing.T) {
	heartTestClock(t, heartTestNow())
	heartTestAccounts(t)
	get := func(target string) map[string]bool {
		t.Helper()
		rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, target, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", target, rec.Code, rec.Body.String())
		}
		var out map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s body %s: %v", target, rec.Body.String(), err)
		}
		return out
	}
	if got := get("/v4/heart/receivable/9902"); !got["result"] {
		t.Fatalf("receivable real target = %v", got)
	}
	if got := get("/v4/heart/receivable/9901"); got["result"] {
		t.Fatalf("receivable self = %v", got)
	}
	if got := get("/v4/heart/receivable/9999"); got["result"] {
		t.Fatalf("receivable unknown = %v", got)
	}
	if got := get("/v4/heart/haveHeart/9902"); !got["result"] {
		t.Fatalf("haveHeart under cap = %v", got)
	}
	for range economy.HeartBase() {
		economy.HeartSend("9901", "9902", heartTestNow())
	}
	if got := get("/v4/heart/haveHeart/9902"); got["result"] {
		t.Fatalf("haveHeart at cap = %v", got)
	}
	if rec := socialTestRequestAs(t, "hq-tok", http.MethodPost, "/v4/heart/receivable/9902", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("POST receivable = %d, want 404", rec.Code)
	}
	if rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/heart/other/9902", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown heart kind = %d, want 404", rec.Code)
	}
}

// TestHeartSenderListRoute: the receiver sees today's senders with string fields and a bool;
// nobody who received nothing gets [].
func TestHeartSenderListRoute(t *testing.T) {
	heartTestClock(t, heartTestNow())
	heartTestAccounts(t)
	if rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/noti/heart/receive/list/9901?timestamp=0", ""); rec.Body.String() != `{"result":[]}` {
		t.Fatalf("empty list = %s", rec.Body.String())
	}
	economy.HeartSend("9901", "9902", heartTestNow())
	rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/noti/heart/receive/list/9902?timestamp=0", "")
	var got struct {
		Result []map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("list body %s: %v", rec.Body.String(), err)
	}
	if len(got.Result) != 1 {
		t.Fatalf("rows = %d, want 1 (%s)", len(got.Result), rec.Body.String())
	}
	row := got.Result[0]
	for _, k := range []string{"avatarId", "name", "avatarProfile", "obsProfileImagePath", "regDate", "introduce", "profileBackground"} {
		if _, ok := row[k].(string); !ok {
			t.Fatalf("%s = %#v, want string", k, row[k])
		}
	}
	if row["avatarId"] != "9901" || row["name"] != "Sender" {
		t.Fatalf("row = %v", row)
	}
	if v, ok := row["heartReceivable"].(bool); !ok || !v {
		t.Fatalf("heartReceivable = %#v, want true", row["heartReceivable"])
	}
}

// TestProfileHeartFields: the profile carries heartCount (number), haveHeart and heartReceivable
// (bools), haveHeartBalance (number, the viewer's sends left) and haveExtraHeartBalance 0.
func TestProfileHeartFields(t *testing.T) {
	heartTestClock(t, heartTestNow())
	heartTestAccounts(t)
	economy.HeartSend("9901", "9902", heartTestNow())
	rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/profile/9902", "")
	var got struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("profile body %s: %v", rec.Body.String(), err)
	}
	for k, want := range map[string]float64{"heartCount": 1, "haveHeartBalance": 4, "haveExtraHeartBalance": 0} {
		if v, ok := got.Result[k].(float64); !ok || v != want {
			t.Fatalf("%s = %#v, want number %v", k, got.Result[k], want)
		}
	}
	for k, want := range map[string]bool{"haveHeart": true, "heartReceivable": true} {
		if v, ok := got.Result[k].(bool); !ok || v != want {
			t.Fatalf("%s = %#v, want %v", k, got.Result[k], want)
		}
	}
	self := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/profile/9901", "")
	if regexp.MustCompile(`"heartReceivable":true`).MatchString(self.Body.String()) {
		t.Fatalf("own profile receivable: %s", self.Body.String())
	}
}
