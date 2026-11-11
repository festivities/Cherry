package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/web"
)

// TestSeedRoundTrip: a seeded database reopens with its accounts, a second seed into it is refused, and a seeded device
// logs in through the real handlers (guest login, then createSession) and gets the avatar the seed wrote.
func TestSeedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := runSeed([]string{"-data-dir", dir, "-accounts", "5"}); err != nil {
		t.Fatal(err)
	}
	if err := runSeed([]string{"-data-dir", dir, "-accounts", "5"}); err == nil {
		t.Fatal("a seed into a non-empty directory must be refused without -force")
	}
	if err := store.OpenStore(filepath.Join(dir, store.DBFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.CloseStore() })

	store.AccountsMu.Lock()
	count := store.AccountCountLocked()
	heavy := store.GuestAccountLocked(guestCode(0))
	ordinary := store.GuestAccountLocked(guestCode(3))
	store.AccountsMu.Unlock()
	if count != 5 || heavy == nil || ordinary == nil {
		t.Fatalf("reopened %d accounts, heavy=%v ordinary=%v", count, heavy != nil, ordinary != nil)
	}
	if len(heavy.InventoryCodes) != heavyOwned || len(ordinary.InventoryCodes) != 40 {
		t.Fatalf("owned codes heavy=%d ordinary=%d", len(heavy.InventoryCodes), len(ordinary.InventoryCodes))
	}
	mux := web.NewMux()
	code := guestCode(3)
	req := httptest.NewRequest(http.MethodPost, "/v4/account/guest/generate", strings.NewReader(`{"uniqueCode":"`+code+`"}`+"\x00"))
	req.Header.Set("X-LINEPLAY-ACNT", guestHeader(code))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("guest login = %d: %s", rec.Code, rec.Body.String())
	}
	var lr struct {
		Result struct {
			AccessToken string `json:"accessToken"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil || lr.Result.AccessToken == "" {
		t.Fatalf("guest login body: %v %s", err, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v4/createSession", nil)
	req.Header.Set("Cookie", `AV_AUTH="`+lr.Result.AccessToken+`"`)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var sr struct {
		Result struct {
			SessionKey string `json:"sessionKey"`
			Aid        string `json:"aid"`
		} `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &sr) != nil {
		t.Fatalf("createSession = %d: %s", rec.Code, rec.Body.String())
	}
	if sr.Result.SessionKey == "" || sr.Result.Aid != ordinary.Aid {
		t.Fatalf("createSession gave aid %q key %q, want aid %s", sr.Result.Aid, sr.Result.SessionKey, ordinary.Aid)
	}
}
