package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/room"
)

// Shape checks through the full mux for the menu rows of audit section rows-d (rows 45, 49, 58, 64).
// Each reply is typed as the client's parser reads it (rows-d-spec 2.3, 4.2, 14.2, 16.2).

// topLevelKinds decodes the top-level keys of a reply into raw values.
func topLevelKinds(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("reply %s: %v", body, err)
	}
	return top
}

// TestGachaKeywordPrefixShape: keyword/<n> is the object list (sDataGachaShopList); the exact keyword path
// stays the keyword list (an array, ResGachaKeywords) that the Gacha Shop top page asks for.
func TestGachaKeywordPrefixShape(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/shop/gachaList2/keyword/2")
	if rec.Code != http.StatusOK {
		t.Fatalf("keyword/2 status %d", rec.Code)
	}
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("reply %s: %v", rec.Body, err)
	}
	checkKinds(t, env.Result, map[string]string{
		"nextCursor": "string", "previousCursor": "string", "totalCount": "string", "vipGrade": "string",
		"keywordNm": "string", "categroyNm": "string", "vipGachaDiscountRate": "number",
		"items": "array", "bannerList": "array",
	})

	rec = serve(t, http.MethodGet, "/v4/shop/gachaList2/keyword")
	if got := jsonKind(topLevelKinds(t, rec.Body.Bytes())["result"]); got != "array" {
		t.Fatalf("keyword list result is %s, want array", got)
	}
}

// TestGroundLevelStatusShape: the Remodeling Shop reads ownMaxGroundLevel as a "LEVEL_<n>" string.
func TestGroundLevelStatusShape(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/room/groundlevel/status/1001")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("reply %s: %v", rec.Body, err)
	}
	checkKinds(t, env.Result, map[string]string{"ownMaxGroundLevel": "string"})
	if want := fmt.Sprintf(`"LEVEL_%d"`, room.RoomPartyFloors); string(env.Result["ownMaxGroundLevel"]) != want {
		t.Fatalf("ownMaxGroundLevel = %s, want %s", env.Result["ownMaxGroundLevel"], want)
	}
}

// TestRemodelingRoomListRoute: the floor tabs of the Remodeling Shop reach the mux (no "Network Error." popup from a 404).
// The owned 1F and 2F tabs answer their row (an empty tab leaves the screen's touch skip up, IDA 2026-10-28); 3F stays empty.
func TestRemodelingRoomListRoute(t *testing.T) {
	for code, row := range map[string]string{"URMEXT": `"itemCode":"RUTI0005D"`, "URM2F": `"itemCode":"RUWA0006D"`} {
		rec := serve(t, http.MethodGet, "/v4/shop/interior/remodelingroomlist/"+code+"?isFull=false")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), row) {
			t.Fatalf("%s: status %d body %s", code, rec.Code, rec.Body)
		}
	}
	rec := serve(t, http.MethodGet, "/v4/shop/interior/remodelingroomlist/URM3F?isFull=false")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"items":[]}}` {
		t.Fatalf("URM3F: status %d body %s", rec.Code, rec.Body)
	}
}

// TestPetBookListRouteShape: the race tab answers 200 with result.list as an array (rows are typed in room).
func TestPetBookListRouteShape(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/pet/book/race/list/UDOG")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("reply %s: %v", rec.Body, err)
	}
	if got := jsonKind(env.Result["list"]); got != "array" {
		t.Fatalf("list is %s, want array", got)
	}
}

// TestCherryTipListShape: result is an array of eight-key tips (strings and one number) and the root has a
// string timestamp; one element per tip in the operator list.
func TestCherryTipListShape(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/cherrytip/list/10.1.0.0/Android")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	top := topLevelKinds(t, rec.Body.Bytes())
	if got := jsonKind(top["timestamp"]); got != "string" {
		t.Fatalf("timestamp is %s, want string", got)
	}
	if got := jsonKind(top["result"]); got != "array" {
		t.Fatalf("result is %s, want array", got)
	}
	rows := moreRows(t, rec.Body.Bytes())
	if want := len(economy.ActiveCherryTips().Tips); len(rows) != want {
		t.Fatalf("%d rows, want %d", len(rows), want)
	}
	for _, row := range rows {
		checkKinds(t, row, map[string]string{
			"bannerId": "number", "title": "string", "description": "string", "image": "string",
			"eventPeriodTxt": "string", "linkType": "string", "link": "string", "dispStartYmdt": "string",
		})
	}
}
