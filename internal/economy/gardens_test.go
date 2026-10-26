package economy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestGardenMap: an owner's override applies while it is listed; no override, an unlisted stem and an
// unknown owner all get the default.
func TestGardenMap(t *testing.T) {
	def := DefaultGardenMap()
	acc := &store.Account{AccessToken: "gmtok", SessionKey: "sk", Aid: "9501"}
	testutil.InstallAccounts(t, map[string]*store.Account{"gmtok": acc})
	if got := GardenMap("9501"); got != def {
		t.Fatalf("no override = %s, want the default %s", got, def)
	}
	acc.GardenMap = "1908231803"
	if got := GardenMap("9501"); got != "1908231803" {
		t.Fatalf("override = %s", got)
	}
	acc.GardenMap = "1234567890" // not in gardens.json
	if got := GardenMap("9501"); got != def {
		t.Fatalf("stale override = %s, want the default", got)
	}
	if got := GardenMap("9999"); got != def {
		t.Fatalf("unknown owner = %s, want the default", got)
	}
}

// TestGardenFishSpot: each fish spot comes from gardens.json; a listed map without one and an unlisted
// stem have none. checkGardens refuses a fish spot with no host, a dword@0 of 0 or a tile off the grid.
func TestGardenFishSpot(t *testing.T) {
	if f, ok := GardenFishSpot("2401290846"); !ok || f != (GardenFish{Host: "RUGO00B05", Idx: 131, X: 33, Y: 57}) {
		t.Fatalf("2401290846 fish = %+v, %v", f, ok)
	}
	if f, ok := GardenFishSpot("1908231803"); !ok || f != (GardenFish{Host: "RUPL0018L", Idx: 21, X: 33, Y: 54}) {
		t.Fatalf("1908231803 fish = %+v, %v", f, ok)
	}
	if _, ok := GardenFishSpot("1908231804"); ok {
		t.Fatal("listed map without a fish spot reports one")
	}
	if _, ok := GardenFishSpot("1234567890"); ok {
		t.Fatal("unlisted map reports a fish spot")
	}
	for _, f := range []*GardenFish{{Idx: 9, X: 1, Y: 1}, {Host: "RUTD0035A", X: 1, Y: 1}, {Host: "RUTD0035A", Idx: 9, X: 60, Y: 1}, {Host: "RUTD0035A", Idx: 9, X: 1, Y: -1}} {
		g := &Gardens{Default: "1908231803", Gardens: []GardenEntry{{Map: "1908231803", Fish: f}}}
		if err := checkGardens(g); err == nil {
			t.Fatalf("checkGardens accepted fish %+v", f)
		}
	}
}

// TestAdminGarden: the staff tool sets an owner's Garden map and clears it with "". A map not in
// gardens.json or an unknown aid is 400, and a non-loopback caller gets 404. The override is saved, so
// a reload (the next start) reads it back.
func TestAdminGarden(t *testing.T) {
	acc := &store.Account{AccessToken: "gmtok", SessionKey: "sk", Aid: "9501"}
	testutil.InstallAccounts(t, map[string]*store.Account{"gmtok": acc})
	store.AccountsMu.Lock()
	store.AccountStorePath, store.NextAvatarID = filepath.Join(t.TempDir(), "accounts.json"), 9501
	store.AccountsMu.Unlock()
	post := func(remote, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/garden", strings.NewReader(body))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		NewAdminMux().ServeHTTP(rec, req)
		return rec
	}
	if rec := post("192.168.1.9:5000", `{"aid":"9501","map":"1908231803"}`); rec.Code != http.StatusNotFound || acc.GardenMap != "" {
		t.Fatalf("non-loopback = %d", rec.Code)
	}
	if rec := post("127.0.0.1:5000", `{"aid":"9501","map":"1908231803"}`); rec.Code != http.StatusOK ||
		acc.GardenMap != "1908231803" || !strings.Contains(rec.Body.String(), `"map":"1908231803"`) {
		t.Fatalf("set = %d %s", rec.Code, rec.Body)
	}
	if rec := post("127.0.0.1:5000", `{"aid":"9501","map":"1234567890"}`); rec.Code != http.StatusBadRequest || acc.GardenMap != "1908231803" {
		t.Fatalf("unlisted stem = %d", rec.Code)
	}
	if rec := post("127.0.0.1:5000", `{"aid":"9999","map":"1908231803"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown aid = %d", rec.Code)
	}
	if rec := post("127.0.0.1:5000", `{"aid":"9501","map":""}`); rec.Code != http.StatusOK || acc.GardenMap != "" ||
		!strings.Contains(rec.Body.String(), `"map":"`+DefaultGardenMap()+`"`) {
		t.Fatalf("clear = %d %s", rec.Code, rec.Body)
	}
	if rec := post("127.0.0.1:5000", `{"aid":"9501","map":"1908231803"}`); rec.Code != http.StatusOK {
		t.Fatalf("set again = %d", rec.Code)
	}
	if err := store.LoadAccountsFrom(store.AccountStorePath); err != nil || GardenMap("9501") != "1908231803" {
		t.Fatalf("reload: %v, map %s", err, GardenMap("9501"))
	}
}
