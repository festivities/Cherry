package economy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"cherry/data"
)

// homeIconReply serves GET /v4/home/integrated/info and returns the raw body and its result object.
func homeIconReply(t *testing.T) (string, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleHomeIntegratedInfo(rec, httptest.NewRequest(http.MethodGet, "/v4/home/integrated/info", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("home info status %d", rec.Code)
	}
	var got struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("home info body %q: %v", rec.Body.String(), err)
	}
	return rec.Body.String(), got.Result
}

// TestHomeIconInSeason: the season's icon is one home_icon row with the five sHomeEventIconData keys; the
// package icons are the unavailable body; gacha_target_icon is not sent (its parser runs only when non-empty).
func TestHomeIconInSeason(t *testing.T) {
	pinPopupClock(t, jstAt(26, 12, 0))
	_, res := homeIconReply(t)
	rows, ok := res["home_icon"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("home_icon = %#v, want one row", res["home_icon"])
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("home_icon row = %#v", rows[0])
	}
	want := map[string]any{"iconSeq": 1.0, "iconId": "halloween_17s", "iconName": "Halloween",
		"linkTypeCode": "goSomewhere(fashionShop)", "linkDetail": ""}
	if len(row) != len(want) {
		t.Fatalf("row %v has %d keys, want %d", row, len(row), len(want))
	}
	for k, v := range want {
		if row[k] != v {
			t.Fatalf("%s = %#v, want %#v", k, row[k], v)
		}
	}
	var bill map[string]any
	if err := json.Unmarshal([]byte(unavailableHomeIcons), &bill); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res["bill_prod_icon"], bill) {
		t.Fatalf("bill_prod_icon = %#v, want the unavailable package body", res["bill_prod_icon"])
	}
	if _, has := res["gacha_target_icon"]; has {
		t.Fatal("gacha_target_icon must not be sent")
	}
}

// TestHomeIconOutOfSeason: outside every season the home_icon list is an empty array (the client reads it as an array).
func TestHomeIconOutOfSeason(t *testing.T) {
	pinPopupClock(t, jstAt(11, 12, 0)) // the normal calendar
	raw, res := homeIconReply(t)
	if rows, ok := res["home_icon"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("home_icon outside a season = %#v, want []", res["home_icon"])
	}
	if want := `"home_icon":[]`; !strings.Contains(raw, want) {
		t.Fatalf("body %q has no %s", raw, want)
	}
}

// TestHomeIconForcedSeason: /admin/season forces the Garden icon the same way it forces the calendar and popup.
func TestHomeIconForcedSeason(t *testing.T) {
	pinPopupClock(t, jstAt(11, 12, 0))
	forceSeason(t, "winter")
	_, res := homeIconReply(t)
	rows, _ := res["home_icon"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["iconId"] != "49_frozen_r" {
		t.Fatalf("forced winter home_icon = %#v", res["home_icon"])
	}
	forceSeason(t, "")
	if _, res := homeIconReply(t); len(res["home_icon"].([]any)) != 0 {
		t.Fatalf("forced normal calendar home_icon = %#v", res["home_icon"])
	}
}

// TestHomeIconRefusals: a season icon must name a plain symbol and a native scene. WebView links (html, event),
// other scenes and a "/" in the id are refused at startup, each naming the season.
func TestHomeIconRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.CalendarsJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"id": "halloween_17s"`, `"id": "halloween_17s/x"`),
			`season halloween: icon id "halloween_17s/x" must be a plain icon_effect symbol name`},
		{mutateDefault(t, raw, `"id": "49_frozen_r"`, `"id": ""`),
			`season winter: icon id "" must be a plain icon_effect symbol name`},
		{mutateDefault(t, raw, `"name": "Halloween", "link"`, `"name": "", "link"`),
			"season halloween: icon name must not be empty"},
		{mutateDefault(t, raw, `"name": "New Year", "link": "goSomewhere(fashionShop)"`, `"name": "New Year", "link": "html"`),
			`season newyear: icon link "html" is not a native scene`},
		{mutateDefault(t, raw, `"name": "Winter", "link": "goSomewhere(fashionShop)"`, `"name": "Winter", "link": "event"`),
			`season winter: icon link "event" is not a native scene`},
		{mutateDefault(t, raw, `"name": "Halloween", "link": "goSomewhere(fashionShop)"`, `"name": "Halloween", "link": "goSomewhere(square)"`),
			`season halloween: icon link "goSomewhere(square)" is not a native scene`},
	}
	for _, c := range cases {
		refuseFile(t, calendarsFile.LoadFrom, "calendars.json", c.text, c.want)
	}
}
