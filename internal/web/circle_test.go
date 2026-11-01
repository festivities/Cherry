package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// circleSession installs account aid with Gems under token "wtok-<aid>" and an empty circle book.
func circleSession(t *testing.T, aid string, gems int64) string {
	t.Helper()
	token := "wtok-" + aid
	testutil.InstallAccounts(t, map[string]*store.Account{token: {AccessToken: token, SessionKey: "sk", Aid: aid, Name: "W" + aid, Gems: gems}})
	store.ResetCircles()
	t.Cleanup(store.ResetCircles)
	return token
}

// circleCall sends one request through the full mux; token "" sends no session cookie.
func circleCall(t *testing.T, method, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: token})
	}
	rec := httptest.NewRecorder()
	NewMux().ServeHTTP(rec, req)
	return rec
}

// decodeResult decodes a {"result": ...} reply into a generic tree (the field types are what the client reads).
func decodeResult(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("reply %q: %v", rec.Body.String(), err)
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %s", rec.Body.String())
	}
	return res
}

// TestLpCirGameInfo: the MENU entry's game info is executable, not in maintenance, and names lp_cir (spec 3.4-3.6).
func TestLpCirGameInfo(t *testing.T) {
	rec := circleCall(t, http.MethodGet, "/v4/playhome/games/lp_cir?deviceType=Android", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env struct {
		Result struct {
			GameInfo map[string]any `json:"gameInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	g := env.Result.GameInfo
	if g["gameId"] != "lp_cir" || g["executable"] != true || g["underMaintenance"] != false || g["minLinePlayVersion"] != "" {
		t.Fatalf("gameInfo = %v", g)
	}
	if _, ok := g["startDate"].(string); !ok {
		t.Fatalf("startDate must be a string: %v", g["startDate"])
	}
}

// TestCircleJoinInfoShapes: join/info never errors. Without a session or outside a circle it is {invitedCount}; in a
// circle it carries the circle's keys, circleMasterExt, circleMemberBySelf.level and circleInterestList, with ids as
// strings and counts as numbers.
func TestCircleJoinInfoShapes(t *testing.T) {
	rec := circleCall(t, http.MethodGet, "/v4/circle/join/info", "", "")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"invitedCount":0}}` {
		t.Fatalf("no session: %d %s", rec.Code, rec.Body.String())
	}

	token := circleSession(t, "1101", 6000)
	if rec := circleCall(t, http.MethodGet, "/v4/circle/join/info", "", token); rec.Code != http.StatusOK || decodeResult(t, rec)["invitedCount"] != float64(0) {
		t.Fatalf("outside a circle: %d %s", rec.Code, rec.Body.String())
	}

	body := createBody("Sushi Club", "OPEN", "")
	if rec := circleCall(t, http.MethodPost, "/v4/circle/create", body, token); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec = circleCall(t, http.MethodGet, "/v4/circle/join/info", "", token)
	res := decodeResult(t, rec)
	if res["circleId"] != "1940000001" || res["circleName"] != "Sushi Club" || res["masterAid"] != "1101" || res["joinType"] != "OPEN" {
		t.Fatalf("circle keys: %v", res)
	}
	if res["memberCount"] != float64(1) || res["maxMemberCount"] != float64(30) || res["logoId"] != float64(1940001001) {
		t.Fatalf("numbers: %v", res)
	}
	if res["logoImgPath"] != "cherry/circle/circle_logo_sd_dp.png" || res["logoBgImgPath"] != "cherry/circle/circle_logo_bg_dp.png" {
		t.Fatalf("logo paths: %v", res)
	}
	master, ok := res["circleMasterExt"].(map[string]any)
	if !ok || master["circleId"] != "1940000001" {
		t.Fatalf("circleMasterExt must be the circle object: %v", res["circleMasterExt"])
	}
	self, ok := res["circleMemberBySelf"].(map[string]any)
	if !ok || self["level"] != "MASTER" {
		t.Fatalf("circleMemberBySelf = %v, want level MASTER", res["circleMemberBySelf"])
	}
	if list, ok := res["circleInterestList"].([]any); !ok || len(list) != 2 {
		t.Fatalf("circleInterestList = %v", res["circleInterestList"])
	}
}

// createBody is a native create request (with its NUL terminator) for the default product.
func createBody(name, joinType, cpID string) string {
	cp := ""
	if cpID != "" {
		cp = `,"cpId":"` + cpID + `"`
	}
	return `{"coinUnit":0,"deviceType":"Android"` + cp + `,"productCode":"cherry_circle_create","price":5000,` +
		`"circleName":"` + name + `","circleIntroduce":"hi","logoId":1940001001,"logoBgId":1940001101,` +
		`"joinType":"` + joinType + `","interestCodeList":[101,301]}` + "\x00"
}

// TestCircleCreateFlowOverHTTP: the create screen's reads (price row, logo lists), a create that charges 5,000 Gems,
// the refusal of a real-money id, and the reply shape (circleMaster is an object with circleId).
func TestCircleCreateFlowOverHTTP(t *testing.T) {
	token := circleSession(t, "1101", 5000)

	item := decodeResult(t, circleCall(t, http.MethodGet, "/v4/circle/create/default/item", "", token))
	if item["finalPrice"] != float64(5000) || item["paymentType"] != "GEM" || item["cpId"] != "" || item["purchasable"] != true {
		t.Fatalf("default item = %v", item)
	}
	wantID := map[string]float64{"sb": 1940001001, "bg": 1940001101}
	for _, list := range []string{"sb", "bg"} {
		rec := circleCall(t, http.MethodGet, "/v4/circle/logo/"+list+"/list", "", token)
		var env struct {
			Result []map[string]any `json:"result"` // the parser indexes an array (CircleImageInfoParser)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || len(env.Result) != 1 {
			t.Fatalf("%s list: %v %s", list, err, rec.Body.String())
		}
		if row := env.Result[0]; row["logoId"] != wantID[list] || row["logoImagePath"] != "cherry/circle/"+map[string]string{"sb": "circle_logo_sd_dp.png", "bg": "circle_logo_bg_dp.png"}[list] {
			t.Fatalf("%s row = %v", list, row)
		}
	}

	if rec := circleCall(t, http.MethodPost, "/v4/circle/create", createBody("Real", "OPEN", "CP-7"), token); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"errorCode":"400"`) {
		t.Fatalf("cpId: %d %s", rec.Code, rec.Body.String())
	}
	if g := gemsFor(t, "1101"); g != 5000 {
		t.Fatalf("refused create changed gems to %d", g)
	}

	rec := circleCall(t, http.MethodPost, "/v4/circle/create", createBody("Sushi Club", "OPEN", ""), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	res := decodeResult(t, rec)
	master, ok := res["circleMaster"].(map[string]any)
	if !ok || master["circleId"] != "1940000001" || res["circleId"] != "1940000001" {
		t.Fatalf("create reply = %v", res)
	}
	if g := gemsFor(t, "1101"); g != 0 {
		t.Fatalf("gems after create = %d, want 0", g)
	}
	if rec := circleCall(t, http.MethodPost, "/v4/circle/create", createBody("Again", "OPEN", ""), token); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"errorCode":"190202"`) {
		t.Fatalf("second circle: %d %s", rec.Code, rec.Body.String())
	}
}

// gemsFor reads aid's Gem balance.
func gemsFor(t *testing.T, aid string) int64 {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("account %s missing", aid)
	}
	return acc.Gems
}

// TestCircleArtServesOnlyTheListedPictures: the two original logo pictures are served as PNG; any other name is not.
func TestCircleArtServesOnlyTheListedPictures(t *testing.T) {
	for _, name := range []string{"circle_logo_sd_dp.png", "circle_logo_bg_dp.png"} {
		rec := circleCall(t, http.MethodGet, "/cherry/circle/"+name, "", "")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(rec.Body.String(), "\x89PNG") {
			t.Fatalf("%s: status %d type %q", name, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if rec := circleCall(t, http.MethodGet, "/cherry/circle/circle_btn_dummy.png", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unlisted name: status %d, want 404", rec.Code)
	}
	// ServeMux cleans a ".." path with a redirect before any handler runs; the file is never served either way.
	if rec := circleCall(t, http.MethodGet, "/cherry/circle/../circle_logo_sd_dp.png", "", ""); rec.Code == http.StatusOK {
		t.Fatal("traversal path served a file")
	}
}

// TestCirclePointAndRouteShapes: the ranking routes answer empty, shape-correct replies for a player in no circle (its
// myCircle is an empty object); season info of a circle that does not exist is refused (190201); a wrong method is a 404.
func TestCirclePointAndRouteShapes(t *testing.T) {
	token := circleSession(t, "1101", 0)
	total := decodeResult(t, circleCall(t, http.MethodGet, "/v4/circle/point/rank/total/3", "", token))
	if rows, ok := total["topRankCircleList"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("total rank = %v", total)
	}
	if _, ok := total["myCircle"].(map[string]any); !ok {
		t.Fatalf("myCircle must be an object: %v", total["myCircle"])
	}
	if rec := circleCall(t, http.MethodGet, "/v4/circle/point/season/info/3", "", token); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"190201"`) {
		t.Fatalf("season info of a missing circle: %d %s", rec.Code, rec.Body.String())
	}
	if top := decodeResult(t, circleCall(t, http.MethodGet, "/v4/circle/point/rank/entry/top3", "", token)); top["entryList"] == nil {
		t.Fatalf("top3 = %v", top)
	}
	if rec := circleCall(t, http.MethodGet, "/v4/circle/create", "", token); rec.Code != http.StatusNotFound {
		t.Fatalf("GET create: status %d, want 404", rec.Code)
	}
	if rec := circleCall(t, http.MethodPost, "/v4/circle/join/info", "", token); rec.Code != http.StatusNotFound {
		t.Fatalf("POST join/info: status %d, want 404", rec.Code)
	}
}

// TestCirclePointReplyShapesOverHTTP: the season info carries the popup's keys (the step list, the next step and the
// member rows), and the reward page's gachaDetail is the Cherry Gacha row with its collection.
func TestCirclePointReplyShapesOverHTTP(t *testing.T) {
	token := circleSession(t, "1101", 5000)
	created := circleCall(t, http.MethodPost, "/v4/circle/create", createBody("Points", "OPEN", ""), token)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	id, _ := decodeResult(t, created)["circleId"].(string)
	info := decodeResult(t, circleCall(t, http.MethodGet, "/v4/circle/point/season/info/"+id, "", token))
	if steps, ok := info["seasonRewardList"].([]any); !ok || len(steps) != 4 {
		t.Fatalf("seasonRewardList = %v", info["seasonRewardList"])
	}
	if next, ok := info["currentReward"].(map[string]any); !ok || next["rewardSeq"] != float64(1) || next["rewardType"] != "GEM" {
		t.Fatalf("currentReward = %v", info["currentReward"])
	}
	members, ok := info["memberGainPointList"].([]any)
	if !ok || len(members) != 1 || members[0].(map[string]any)["memberAid"] != "1101" {
		t.Fatalf("memberGainPointList = %v", info["memberGainPointList"])
	}
	season, _ := info["seasonId"].(float64)
	reward := decodeResult(t, circleCall(t, http.MethodGet, "/v4/circle/point/season/reward/"+strconv.FormatFloat(season, 'f', 0, 64)+"/1", "", token))
	if list, ok := reward["rewardAchieveList"].([]any); !ok || len(list) != 1 {
		t.Fatalf("rewardAchieveList = %v", reward["rewardAchieveList"])
	}
	gacha, ok := reward["gachaDetail"].(map[string]any)
	if !ok || gacha["gachaId"] != float64(1001) || gacha["collection"] == nil {
		t.Fatalf("gachaDetail = %v", reward["gachaDetail"])
	}
}
