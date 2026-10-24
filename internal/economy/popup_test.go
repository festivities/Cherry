package economy

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cherry/data"
)

// pinPopupClock fixes NowFunc for one popup test.
func pinPopupClock(t *testing.T, now time.Time) {
	t.Helper()
	old := NowFunc
	NowFunc = func() time.Time { return now }
	t.Cleanup(func() { NowFunc = old })
}

// TestPopupRowShape: popup/list rows use the field types EventData::SetData reads (numbers for popupNo,
// cookieTime, the line counts and linkColorCode; strings for the rest), the season's window, a native
// link and a button skin. The client reads 25 keys; a missing key would take the client's default.
// linkColorCode 0 would drop the Button cell (EventData +209), so the row must carry the season's skin.
func TestPopupRowShape(t *testing.T) {
	now := jstAt(26, 12, 0)
	s, ok := activePopup(now)
	if !ok {
		t.Fatal("no popup in the halloween window")
	}
	raw, err := json.Marshal(popupRow(s, now))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	if len(row) != 25 {
		t.Fatalf("row has %d keys, want the 25 EventData keys", len(row))
	}
	for _, k := range []string{"popupNo", "cookieTime", "linkColorCode", "mainTitleMaxLine", "subTitleMaxLine"} {
		if _, isNum := row[k].(float64); !isNum {
			t.Fatalf("%s = %#v, want a number", k, row[k])
		}
	}
	for k, v := range row {
		if _, isNum := v.(float64); !isNum {
			if _, isStr := v.(string); !isStr {
				t.Fatalf("%s = %#v, want a string", k, v)
			}
		}
	}
	want := map[string]any{
		"popupNo": 9101.0, "cookieTime": 24.0, "linkType": "goSomewhere(fashionShop)", "linkData": "",
		"imageUrl": "cherry/event/halloween.png", "startYmdt": "1792875600000", "endYmdt": "1794085199000",
		"mainTitle": "Halloween is here!", "backgroundColorCode": "#3a1a08", "linkColorCode": 4.0,
	}
	for k, v := range want {
		if row[k] != v {
			t.Fatalf("%s = %#v, want %#v", k, row[k], v)
		}
	}
}

// TestPopupRoutesFollowTheSeason: the show check (no officialAvatarId) and the list answer only while the
// season is in force; the badge check (with officialAvatarId) never opens the popup.
func TestPopupRoutesFollowTheSeason(t *testing.T) {
	pinPopupClock(t, jstAt(26, 12, 0))
	show := httptest.NewRecorder()
	HandlePopupIsExists(show, httptest.NewRequest(http.MethodGet, "/v4/popup/isExists", nil))
	if show.Body.String() != `{"result":true}` {
		t.Fatalf("show check in the window = %q", show.Body.String())
	}
	badge := httptest.NewRecorder()
	HandlePopupIsExists(badge, httptest.NewRequest(http.MethodGet, "/v4/popup/isExists?officialAvatarId=1001", nil))
	if badge.Body.String() != `{"result":false}` {
		t.Fatalf("badge check = %q, want false", badge.Body.String())
	}
	list := httptest.NewRecorder()
	HandlePopupList(list, httptest.NewRequest(http.MethodGet, "/v4/popup/list", nil))
	var got struct {
		Result []map[string]any `json:"result"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil || len(got.Result) != 1 || got.Result[0]["popupNo"] != 9101.0 {
		t.Fatalf("list in the window = %q (%v)", list.Body.String(), err)
	}

	pinPopupClock(t, jstAt(11, 12, 0)) // the normal calendar
	show = httptest.NewRecorder()
	HandlePopupIsExists(show, httptest.NewRequest(http.MethodGet, "/v4/popup/isExists", nil))
	if show.Body.String() != `{"result":false}` {
		t.Fatalf("show check outside a season = %q", show.Body.String())
	}
	list = httptest.NewRecorder()
	HandlePopupList(list, httptest.NewRequest(http.MethodGet, "/v4/popup/list", nil))
	if list.Body.String() != `{"result":[]}` {
		t.Fatalf("list outside a season = %q", list.Body.String())
	}
	post := httptest.NewRecorder()
	HandlePopupIsExists(post, httptest.NewRequest(http.MethodPost, "/v4/popup/isExists", nil))
	if post.Code != http.StatusNotFound {
		t.Fatalf("POST isExists = %d, want 404", post.Code)
	}
}

// TestPopupImageServesPNG: the season's picture is a 640x320 PNG under the static base; a season without a
// popup and an unknown name are 404.
func TestPopupImageServesPNG(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleEventPopupImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/event/halloween.png", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("halloween image = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || img.Bounds().Dx() != 640 || img.Bounds().Dy() != 320 {
		t.Fatalf("halloween image decode = %v %v", img, err)
	}
	for _, path := range []string{"/cherry/event/newyear.png", "/cherry/event/nope.png"} {
		rec := httptest.NewRecorder()
		HandleEventPopupImage(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, rec.Code)
		}
	}
}

// TestPopupRefusals: calendars.json popup rows are checked at startup and each refusal names the season.
func TestPopupRefusals(t *testing.T) {
	requireArchive(t)
	t.Cleanup(resetOperatorData)
	raw := data.CalendarsJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"link": "goSomewhere(fashionShop)", "cookie_hours": 24, "background": "#1c3a5e"`,
			`"link": "goSomewhere(square)", "cookie_hours": 24, "background": "#1c3a5e"`),
			`season winter: popup link "goSomewhere(square)" is not a native scene`},
		{mutateDefault(t, raw, `"no": 9201`, `"no": 9101`), "season halloween: popup no must be unique and positive"},
		{mutateDefault(t, raw, `"cookie_hours": 24, "background": "#1c3a5e"`, `"cookie_hours": 0, "background": "#1c3a5e"`),
			"season winter: popup cookie_hours must be 1 to 8760"},
		{mutateDefault(t, raw, `"background": "#1c3a5e"`, `"background": "1c3a5e"`), `season winter: popup background: colour "1c3a5e" must be #rrggbb`},
		{mutateDefault(t, raw, `"background": "#1c3a5e", "text": "#ffffff", "button_skin": 4}`, `"background": "#1c3a5e", "text": "#ffffff", "button_skin": 0}`),
			"season winter: popup button_skin must be 1 to 15"},
		{mutateDefault(t, raw, `"background": "#1c3a5e", "text": "#ffffff", "button_skin": 4}`, `"background": "#1c3a5e", "text": "#ffffff", "button_skin": 16}`),
			"season winter: popup button_skin must be 1 to 15"},
		{mutateDefault(t, raw, `"sub": "Login calendar, limited shop items and event quests until Jan 5"`, `"sub": ""`),
			"season winter: popup title and sub must not be empty"},
	}
	for _, c := range cases {
		refuseFile(t, calendarsFile.LoadFrom, "calendars.json", c.text, c.want)
	}
}
