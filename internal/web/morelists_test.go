package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// moreRows decodes a {"result":[...]} reply into raw key maps, so each value's JSON kind can be checked.
func moreRows(t *testing.T, body []byte) []map[string]json.RawMessage {
	t.Helper()
	var env struct {
		Result []map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("reply %s: %v", body, err)
	}
	return env.Result
}

// jsonKind names the JSON type of a raw value: string, number, bool, object, array or null.
func jsonKind(raw json.RawMessage) string {
	switch {
	case len(raw) == 0:
		return "missing"
	case raw[0] == '"':
		return "string"
	case raw[0] == '{':
		return "object"
	case raw[0] == '[':
		return "array"
	case raw[0] == 't' || raw[0] == 'f':
		return "bool"
	case raw[0] == 'n':
		return "null"
	default:
		return "number"
	}
}

// checkKinds fails unless row has exactly the keys in want, each with its JSON kind.
func checkKinds(t *testing.T, row map[string]json.RawMessage, want map[string]string) {
	t.Helper()
	if len(row) != len(want) {
		t.Fatalf("row has %d keys, want %d: %v", len(row), len(want), row)
	}
	for key, kind := range want {
		if got := jsonKind(row[key]); got != kind {
			t.Fatalf("%s is %s, want %s (row %s)", key, got, kind, row[key])
		}
	}
}

func serveWithBody(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	NewMux().ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

func TestMoreGameListShapeAndTapTargets(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/home/sublist/game/Android")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	rows := moreRows(t, rec.Body.Bytes())
	wantIDs := []string{"lp_sq", "lp_rmchat"}
	if len(rows) != len(wantIDs) {
		t.Fatalf("%d rows, want %d", len(rows), len(wantIDs))
	}
	for i, row := range rows {
		checkKinds(t, row, map[string]string{
			"id": "string", "name": "string", "image": "string", "flag": "string",
			"gameType": "string", "desc": "string", "dispPeriod": "string", "fileSize": "number",
		})
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil || id != wantIDs[i] {
			t.Fatalf("row %d id %s, want %s", i, row["id"], wantIDs[i])
		}
		if gt := string(row["gameType"]); gt != `"gamelink"` {
			t.Fatalf("row %s gameType %s, want gamelink", id, gt)
		}
		// A tap requests playhome/games/<id>?deviceType=Android with the same id string; Cherry must answer it.
		if tap := serve(t, http.MethodGet, "/v4/playhome/games/"+id+"?deviceType=Android"); tap.Code != http.StatusOK {
			t.Fatalf("tap target for %s: status %d", id, tap.Code)
		}
	}
	if rec := serve(t, http.MethodPost, "/v4/home/sublist/game/Android"); rec.Code != http.StatusNotFound {
		t.Fatalf("POST status %d, want 404", rec.Code)
	}
}

func TestMoreShopListShapeAndLinks(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/home/sublist/shop/10.1.0.0/Android.nhn?parentIconId=")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	rows := moreRows(t, rec.Body.Bytes())
	if len(rows) != len(moreShopLinks) {
		t.Fatalf("%d rows, want %d", len(rows), len(moreShopLinks))
	}
	for i, row := range rows {
		checkKinds(t, row, map[string]string{
			"id": "number", "name": "string", "image": "string", "flag": "string",
			"linkType": "string", "link": "string", "nMarkTimestamp": "string",
			"nMark": "bool", "delimiter": "bool", "desc": "string",
		})
		var link string
		if err := json.Unmarshal(row["linkType"], &link); err != nil || link != moreShopLinks[i] {
			t.Fatalf("row %d linkType %s, want %s", i, row["linkType"], moreShopLinks[i])
		}
		if !slices.Contains(homeLinks, link) {
			t.Fatalf("row %d linkType %s is not a home menu row", i, link)
		}
		if string(row["link"]) != `""` || string(row["nMarkTimestamp"]) != `""` {
			t.Fatalf("row %d link %s nMarkTimestamp %s, want empty", i, row["link"], row["nMarkTimestamp"])
		}
	}
}

func TestMoreEventListIsEmpty(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		rec := serve(t, http.MethodGet, "/v4/noti/event/"+lang+"/10.1.0.0/Android.nhn")
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":[]}` {
			t.Fatalf("%s: status %d body %s", lang, rec.Code, rec.Body)
		}
	}
	if rec := serve(t, http.MethodGet, "/v4/noti/event/en/Android.nhn"); rec.Code != http.StatusNotFound {
		t.Fatalf("path without version: status %d, want 404", rec.Code)
	}
}

func TestInviteInfoHasEmptyURLString(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/friends/invitation/info")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("reply %s: %v", rec.Body, err)
	}
	checkKinds(t, env.Result, map[string]string{"inviteUrl": "string"})
	if string(env.Result["inviteUrl"]) != `""` {
		t.Fatalf("inviteUrl %s, want empty", env.Result["inviteUrl"])
	}
}

func TestTimemagicCountHasNumbers(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/timemagic/count")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("reply %s: %v", rec.Body, err)
	}
	checkKinds(t, env.Result, map[string]string{"magicPowderCount": "number", "stampCount": "number"})
}

func TestSessionStatusEchoesAidsInOrder(t *testing.T) {
	rec := serveWithBody(t, http.MethodPost, "/v4/session/status", `["1002","1001"]`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	rows := moreRows(t, rec.Body.Bytes())
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	for i, want := range []string{"1002", "1001"} {
		checkKinds(t, rows[i], map[string]string{"avatarId": "string", "busy": "bool", "online": "bool"})
		if string(rows[i]["avatarId"]) != `"`+want+`"` {
			t.Fatalf("row %d avatarId %s, want %s", i, rows[i]["avatarId"], want)
		}
	}
	if rec := serveWithBody(t, http.MethodPost, "/v4/session/status", `[]`); rec.Body.String() != `{"result":[]}` {
		t.Fatalf("empty request body %s", rec.Body)
	}
	if rec := serveWithBody(t, http.MethodPost, "/v4/session/status", `{"avatarId":"1002"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("object body status %d, want 400", rec.Code)
	}
	if rec := serveWithBody(t, http.MethodPost, "/v4/session/status", `["10x2"]`); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric aid status %d, want 400", rec.Code)
	}
	if rec := serve(t, http.MethodGet, "/v4/session/status"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET status %d, want 404", rec.Code)
	}
}
