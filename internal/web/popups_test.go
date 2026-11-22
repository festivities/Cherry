package web

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// webPopupsText is an operator popups.json: the Discord banner (in the game day from the start) and a scene popup that
// starts on Nov 25.
const webPopupsText = `{"origin": "cherry", "note": "test popups", "popups": [
  {"no": 9350, "title": "Cherry Discord", "sub": "News, help and events on our Discord", "caution": "",
   "link": "openWithBrowser", "url": "https://discord.gg/example", "button_skin": 4,
   "background": "#1c3a5e", "text": "#ffffff", "image": "", "start": "", "end": ""},
  {"no": 9351, "title": "Later", "sub": "Opens the gacha shop", "caution": "", "link": "goSomewhere(gachaShop)", "url": "",
   "button_skin": 4, "background": "#1c3a5e", "text": "#ffffff", "image": "", "start": "2026-11-25", "end": ""}
]}`

// popupsFromOperatorFolder writes text as the operator's popups.json (under a fresh %LOCALAPPDATA%) and loads the operator
// files, as a start does. The cleanup removes the file and loads the embedded defaults again.
func popupsFromOperatorFolder(t *testing.T, text string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	dir := filepath.Join(base, "Cherry", "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "popups.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := economy.LoadOperatorData(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(path)
		_ = economy.LoadOperatorData()
	})
}

// popupRequest is a GET on the full mux, as the account with token (no cookie when token is "").
func popupRequest(t *testing.T, token, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	}
	return serveRequest(t, req)
}

// popupRowsOf decodes a popup/list reply.
func popupRowsOf(t *testing.T, body string) []map[string]any {
	t.Helper()
	var got struct {
		Result []map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("list body %s: %v", body, err)
	}
	return got.Result
}

// popupDayOf is the PopupDay the account holds now.
func popupDayOf(t *testing.T, token string) int64 {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByTokenLocked(token)
	if acc == nil {
		t.Fatalf("no account for %s", token)
	}
	return acc.PopupDay
}

// TestOperatorPopupsOverTheWire: the Discord popup is listed once per game day for an account, first in the list; the
// picture is reachable on the HTTPS static base and on the plain port; a scene popup waits for its start day.
func TestOperatorPopupsOverTheWire(t *testing.T) {
	popupsFromOperatorFolder(t, webPopupsText)
	testutil.UseDatabase(t) // the day mark is saved: a restart (ReloadAccounts) must keep it
	installSocialTestAccounts(t, map[string]*store.Account{"pop-tok": {AccessToken: "pop-tok", SessionKey: "sk", Aid: "9911", Name: "Popper"}})
	at := func(day, hour, min int) {
		heartTestClock(t, time.Date(2026, time.November, day, hour, min, 0, 0, time.FixedZone("JST", 9*3600)))
	}

	at(22, 12, 0)
	if rec := popupRequest(t, "", "/v4/popup/isExists"); rec.Body.String() != `{"result":true}` {
		t.Fatalf("isExists without an account = %q", rec.Body.String())
	}
	if rec := popupRequest(t, "pop-tok", "/v4/popup/isExists"); rec.Body.String() != `{"result":true}` {
		t.Fatalf("first isExists of the day = %q", rec.Body.String())
	}
	list := popupRequest(t, "pop-tok", "/v4/popup/list")
	t.Logf("list (Nov 22, first show): %s", list.Body.String())
	rows := popupRowsOf(t, list.Body.String())
	if len(rows) != 1 || rows[0]["popupNo"] != 9350.0 || rows[0]["linkType"] != "openWithBrowser" ||
		rows[0]["linkData"] != "https://discord.gg/example" || rows[0]["cookieTime"] != 1.0 || rows[0]["startYmdt"] != "0" {
		t.Fatalf("list rows = %v", rows)
	}
	img, _ := rows[0]["imageUrl"].(string)
	if !strings.HasPrefix(img, "cherry/popup/9350-") || strings.HasPrefix(img, "/") {
		t.Fatalf("imageUrl = %q, want relative", img)
	}
	wantDay := economy.AttendDayKey(time.Date(2026, time.November, 22, 12, 0, 0, 0, time.FixedZone("JST", 9*3600)))
	if day := popupDayOf(t, "pop-tok"); day != wantDay {
		t.Fatalf("PopupDay = %d after the list, want %d", day, wantDay)
	}
	if err := store.ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	if day := popupDayOf(t, "pop-tok"); day != wantDay {
		t.Fatalf("PopupDay after a reload from the database = %d, want %d", day, wantDay)
	}
	if rec := popupRequest(t, "pop-tok", "/v4/popup/isExists"); rec.Body.String() != `{"result":false}` {
		t.Fatalf("second isExists the same game day = %q", rec.Body.String())
	}
	if rec := popupRequest(t, "", "/v4/popup/isExists"); rec.Body.String() != `{"result":true}` {
		t.Fatalf("isExists without an account after the mark = %q (no account keeps the old answer)", rec.Body.String())
	}

	// The picture: the HTTPS static base and the iOS plain port both serve the placeholder as a 640-wide PNG.
	pic := httptest.NewRecorder()
	NewMux().ServeHTTP(pic, httptest.NewRequest(http.MethodGet, "/"+img, nil))
	if pic.Code != http.StatusOK || pic.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("picture = %d %q", pic.Code, pic.Header().Get("Content-Type"))
	}
	if decoded, err := png.Decode(bytes.NewReader(pic.Body.Bytes())); err != nil || decoded.Bounds().Dx() != 640 {
		t.Fatalf("picture decode = %v %v", decoded, err)
	}
	plain := httptest.NewRecorder()
	NewPlainMux().ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/img/read/"+img, nil))
	if plain.Code != http.StatusOK || plain.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("plain-port picture = %d %q", plain.Code, plain.Header().Get("Content-Type"))
	}
	season := httptest.NewRecorder()
	NewPlainMux().ServeHTTP(season, httptest.NewRequest(http.MethodGet, "/img/read/cherry/event/halloween.png", nil))
	if season.Code != http.StatusOK {
		t.Fatalf("plain-port season picture = %d, want 200 (the season art is always served)", season.Code)
	}

	// The next game day (06:00 JST): shown again. The day before its boundary is still the day it was marked on.
	at(23, 5, 59)
	if rec := popupRequest(t, "pop-tok", "/v4/popup/isExists"); rec.Body.String() != `{"result":false}` {
		t.Fatalf("Nov 23 05:59 is still game day Nov 22: isExists = %q", rec.Body.String())
	}
	at(23, 6, 0)
	if rec := popupRequest(t, "pop-tok", "/v4/popup/isExists"); rec.Body.String() != `{"result":true}` {
		t.Fatalf("Nov 23 06:00 isExists = %q", rec.Body.String())
	}
	popupRequest(t, "pop-tok", "/v4/popup/list")
	if rec := popupRequest(t, "pop-tok", "/v4/popup/isExists"); rec.Body.String() != `{"result":false}` {
		t.Fatalf("Nov 23 after the list = %q", rec.Body.String())
	}

	// The scene popup starts on Nov 25: the list then holds both, the Discord row first.
	at(25, 12, 0)
	both := popupRowsOf(t, popupRequest(t, "pop-tok", "/v4/popup/list").Body.String())
	if len(both) != 2 || both[0]["popupNo"] != 9350.0 || both[1]["popupNo"] != 9351.0 || both[1]["linkType"] != "goSomewhere(gachaShop)" {
		t.Fatalf("Nov 25 list = %v", both)
	}

	// A picture named by an older hash is 404 on the route.
	stale := popupRequest(t, "", "/cherry/popup/9350-00000000.png")
	if stale.Code != http.StatusNotFound {
		t.Fatalf("stale picture path = %d, want 404", stale.Code)
	}
}
