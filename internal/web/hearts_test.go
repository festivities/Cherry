package web

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// heartTestClock pins the economy clock to a mutable JST time for one test.
func heartTestClock(t *testing.T, now time.Time) *time.Time {
	t.Helper()
	clock := now
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return clock }
	t.Cleanup(func() { economy.NowFunc = old })
	return &clock
}

func heartTestAccounts(t *testing.T) []string {
	t.Helper()
	accts := map[string]*store.Account{
		"hq-tok":    {AccessToken: "hq-tok", SessionKey: "sk", Aid: "9901", Name: "Sender"},
		"hq-friend": {AccessToken: "hq-friend", SessionKey: "sk", Aid: "9902", Name: "Receiver"},
	}
	targets := []string{"9902"}
	for i := 1; i < int(economy.HeartBase())+2; i++ {
		aid := strconv.Itoa(99100 + i)
		accts["hq-t-"+aid] = &store.Account{AccessToken: "hq-t-" + aid, SessionKey: "sk", Aid: aid, Name: "T" + aid}
		targets = append(targets, aid)
	}
	installSocialTestAccounts(t, accts)
	return targets
}

func heartTestNow() time.Time {
	return time.Date(2026, time.October, 22, 12, 0, 0, 0, time.FixedZone("JST", 9*3600))
}

// TestQuestStatusHeartBlock: the JSON numbers are numbers and the progress and expiry strings are
// strings; the block follows the caller's own sends.
func TestQuestStatusHeartBlock(t *testing.T) {
	heartTestClock(t, heartTestNow())
	targets := heartTestAccounts(t)
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
	wantNumbers := map[string]float64{"heart": float64(economy.HeartBase()), "heartBase": float64(economy.HeartBase()), "heartRewardCoin": 600}
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
	sentIdx := 0
	sendTo := func(n int64) {
		t.Helper()
		for range n {
			if sent, _ := economy.HeartSend("9901", targets[sentIdx], heartTestNow()); !sent {
				t.Fatalf("heart %d to %s refused", sentIdx+1, targets[sentIdx])
			}
			sentIdx++
		}
	}
	sendTo(economy.HeartQuestCount() - 1)
	if rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/quest/status", ""); !strings.Contains(rec.Body.String(), `"questProgress":"ONGOING"`) {
		t.Fatalf("one heart short of the quest: %s", rec.Body.String())
	}
	sendTo(1)
	if rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/quest/status", ""); !strings.Contains(rec.Body.String(), `"questProgress":"COMPLETED"`) {
		t.Fatalf("at the quest count: %s", rec.Body.String())
	}
	sendTo(economy.HeartBase() - economy.HeartQuestCount())
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
	for _, path := range []string{"/v4/quest/status", "/v4/quest/labor/status"} {
		if rec := serve(t, http.MethodGet, path); rec.Body.String() != questStatusBody {
			t.Fatalf("GET %s unknown session body = %s", path, rec.Body.String())
		}
		if rec := serve(t, http.MethodPost, path); rec.Code != http.StatusNotFound {
			t.Fatalf("POST %s = %d, want 404", path, rec.Code)
		}
	}
}

// TestQuestLaborStatus confirms GET /v4/quest/labor/status returns 200 and a string toExpire
// (used by NaQuestManager::ExpireTime for "Restart quest at HH:00").
func TestQuestLaborStatus(t *testing.T) {
	heartTestClock(t, heartTestNow())
	heartTestAccounts(t)
	rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/quest/labor/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	expire, ok := got.Result["toExpire"].(string)
	if !ok || expire != "64800" {
		t.Fatalf("toExpire = %#v (type %T), want string \"64800\"", got.Result["toExpire"], got.Result["toExpire"])
	}
}

// TestHeartRoutes covers heart/haveHeart and heart/receivable as {"result":<bool>}, including
// per-target cooldown gating.
func TestHeartRoutes(t *testing.T) {
	clock := heartTestClock(t, heartTestNow())
	targets := heartTestAccounts(t)
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
	// Sending to 9902 puts 9902 on cooldown while other targets remain receivable.
	if sent, _ := economy.HeartSend("9901", "9902", *clock); !sent {
		t.Fatal("first heart to 9902 refused")
	}
	if got := get("/v4/heart/receivable/9902"); got["result"] {
		t.Fatalf("receivable during cooldown = %v, want false", got)
	}
	if got := get("/v4/heart/receivable/" + targets[1]); !got["result"] {
		t.Fatalf("receivable other target = %v, want true", got)
	}
	for i := 1; i < int(economy.HeartBase()); i++ {
		economy.HeartSend("9901", targets[i], *clock)
	}
	if got := get("/v4/heart/haveHeart/9902"); got["result"] {
		t.Fatalf("haveHeart at cap = %v", got)
	}
	*clock = clock.Add(economy.HeartCooldown())
	if got := get("/v4/heart/receivable/9902"); !got["result"] {
		t.Fatalf("receivable after cooldown = %v, want true", got)
	}
	if rec := socialTestRequestAs(t, "hq-tok", http.MethodPost, "/v4/heart/receivable/9902", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("POST receivable = %d, want 404", rec.Code)
	}
	if rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/heart/other/9902", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown heart kind = %d, want 404", rec.Code)
	}
}

// TestHeartSenderListRoute: the receiver sees today's senders with string fields and a cooldown-aware
// heartReceivable bool; nobody who received nothing gets [].
func TestHeartSenderListRoute(t *testing.T) {
	clock := heartTestClock(t, heartTestNow())
	heartTestAccounts(t)
	if rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/noti/heart/receive/list/9901?timestamp=0", ""); rec.Body.String() != `{"result":[]}` {
		t.Fatalf("empty list = %s", rec.Body.String())
	}
	economy.HeartSend("9901", "9902", *clock)
	fetchRow := func() map[string]any {
		t.Helper()
		rec := socialTestRequestAs(t, "hq-friend", http.MethodGet, "/v4/noti/heart/receive/list/9902?timestamp=0", "")
		var got struct {
			Result []map[string]any `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("list body %s: %v", rec.Body.String(), err)
		}
		if len(got.Result) != 1 {
			t.Fatalf("rows = %d, want 1 (%s)", len(got.Result), rec.Body.String())
		}
		return got.Result[0]
	}
	row := fetchRow()
	for _, k := range []string{"avatarId", "name", "avatarProfile", "obsProfileImagePath", "regDate", "introduce", "profileBackground"} {
		if _, ok := row[k].(string); !ok {
			t.Fatalf("%s = %#v, want string", k, row[k])
		}
	}
	if row["avatarId"] != "9901" || row["name"] != "Sender" {
		t.Fatalf("row = %v", row)
	}
	if v, ok := row["heartReceivable"].(bool); !ok || !v {
		t.Fatalf("heartReceivable before hearting back = %#v, want true", row["heartReceivable"])
	}
	// Once 9902 hearts 9901 back, the sender list reflects cooldown (heartReceivable: false) until expiry.
	economy.HeartSend("9902", "9901", *clock)
	if v, ok := fetchRow()["heartReceivable"].(bool); !ok || v {
		t.Fatalf("heartReceivable during cooldown = %#v, want false", v)
	}
	*clock = clock.Add(economy.HeartCooldown())
	if v, ok := fetchRow()["heartReceivable"].(bool); !ok || !v {
		t.Fatalf("heartReceivable after cooldown = %#v, want true", v)
	}
}

// TestProfileHeartFields: the profile carries heartCount (number), haveHeart and heartReceivable
// (bools, with heartReceivable false during per-target cooldown), haveHeartBalance and haveExtraHeartBalance 0.
func TestProfileHeartFields(t *testing.T) {
	clock := heartTestClock(t, heartTestNow())
	heartTestAccounts(t)
	profileOf := func(path string) map[string]any {
		t.Helper()
		rec := socialTestRequestAs(t, "hq-tok", http.MethodGet, path, "")
		var got struct {
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("profile body %s: %v", rec.Body.String(), err)
		}
		return got.Result
	}
	before := profileOf("/v4/profile/9902")
	if before["heartReceivable"] != true || before["haveHeart"] != true {
		t.Fatalf("before send: haveHeart=%v heartReceivable=%v, want true/true", before["haveHeart"], before["heartReceivable"])
	}
	economy.HeartSend("9901", "9902", *clock)
	during := profileOf("/v4/profile/9902")
	for k, want := range map[string]float64{"heartCount": 1, "haveHeartBalance": float64(economy.HeartBase() - 1), "haveExtraHeartBalance": 0} {
		if v, ok := during[k].(float64); !ok || v != want {
			t.Fatalf("%s = %#v, want number %v", k, during[k], want)
		}
	}
	if during["haveHeart"] != true || during["heartReceivable"] != false {
		t.Fatalf("during cooldown: haveHeart=%v heartReceivable=%v, want true/false", during["haveHeart"], during["heartReceivable"])
	}
	*clock = clock.Add(economy.HeartCooldown())
	after := profileOf("/v4/profile/9902")
	if after["heartReceivable"] != true {
		t.Fatalf("after cooldown: heartReceivable=%v, want true", after["heartReceivable"])
	}
	self := socialTestRequestAs(t, "hq-tok", http.MethodGet, "/v4/profile/9901", "")
	if regexp.MustCompile(`"heartReceivable":true`).MatchString(self.Body.String()) {
		t.Fatalf("own profile receivable: %s", self.Body.String())
	}
}

func TestAdmobInit(t *testing.T) {
	heartTestClock(t, heartTestNow())
	rec := serve(t, http.MethodGet, "/v4/admob/0")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v4/admob/0 = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Result struct {
			PID               int    `json:"pid"`
			Viewable          bool   `json:"viewable"`
			RemainingCount    int    `json:"remainingCount"`
			MaxCount          int    `json:"maxCount"`
			NextViewTimeStamp string `json:"nextViewTimeStamp"`
			GainCirclePoint   int    `json:"gainCirclePoint"`
			CoppaApply        bool   `json:"coppaApply"`
			SenderName        string `json:"senderName"`
			LinkType          string `json:"linkType"`
			Reward            []any  `json:"reward"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantTS := "1792702800000" // 2026-10-23 06:00:00 JST in ms
	if got.Result.PID != 0 || got.Result.Viewable || got.Result.RemainingCount != 0 ||
		got.Result.MaxCount != 0 || got.Result.NextViewTimeStamp != wantTS ||
		got.Result.GainCirclePoint != 0 || got.Result.CoppaApply ||
		got.Result.SenderName != "" || got.Result.LinkType != "" || got.Result.Reward == nil || len(got.Result.Reward) != 0 {
		t.Fatalf("unexpected admob result: %+v", got.Result)
	}
	if post := serve(t, http.MethodPost, "/v4/admob/0"); post.Code != http.StatusNotFound {
		t.Fatalf("POST /v4/admob/0 = %d, want 404", post.Code)
	}
}
