package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/httpx"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

const (
	deviceA = "5b1d2c3e-0000-4000-8000-00000000000a"
	deviceB = "5b1d2c3e-0000-4000-8000-00000000000b"
)

// guestAccountTest installs an empty account table backed by a temp accounts.json.
func guestAccountTest(t *testing.T) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{})
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Unlock()
}

func sessionOf(t *testing.T, token string) sessionResult {
	t.Helper()
	rec := createSession(t, token)
	var body sessionResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Result == nil {
		t.Fatalf("createSession = %d %s", rec.Code, rec.Body.String())
	}
	return *body.Result
}

// TestGuestGenerateReturnsDeviceAccount: a device that generates again gets its own account (same aid and
// session key) under its own access token, another device gets a new account, and the device binding is saved.
func TestGuestGenerateReturnsDeviceAccount(t *testing.T) {
	guestAccountTest(t)
	first := guestGenerateCode(t, deviceA)
	created := createAvatar(t, avAuthValue(t, createSession(t, first)), []byte(`{"name":"Dev","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON00164"]}`), "")
	var avatar avatarResponse
	if err := json.Unmarshal(created.Body.Bytes(), &avatar); err != nil || avatar.Result == nil || avatar.Result.AvatarID == "0" {
		t.Fatalf("create avatar = %d %s", created.Code, created.Body.String())
	}
	before := sessionOf(t, first)
	if before.Aid != avatar.Result.AvatarID {
		t.Fatalf("session aid = %s, want %s", before.Aid, avatar.Result.AvatarID)
	}

	second := guestGenerateCode(t, deviceA)
	if second != first {
		t.Fatal("same device should get its account's own access token back (the 32-byte shape the client already sent as cc)")
	}
	if again := sessionOf(t, second); again.Aid != before.Aid || again.SessionKey != before.SessionKey {
		t.Fatalf("device got aid %s, want its own %s", again.Aid, before.Aid)
	}
	if other := sessionOf(t, guestGenerateCode(t, deviceB)); other.Aid != "0" || other.SessionKey == before.SessionKey {
		t.Fatalf("new device got an existing account: %+v", other)
	}

	store.AccountsMu.Lock()
	path := store.AccountStorePath
	store.AccountsMu.Unlock()
	if err := store.LoadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	reloaded := store.GuestAccountLocked(deviceA)
	store.AccountsMu.Unlock()
	if reloaded == nil || reloaded.Aid != before.Aid {
		t.Fatal("device binding not persisted with the account")
	}
}

// TestGuestGenerateRefusesBadRequests: a body or header that does not check out is 400 and creates nothing.
func TestGuestGenerateRefusesBadRequests(t *testing.T) {
	guestAccountTest(t)
	long := strings.Repeat("a", 65)
	wrongHeader := guestRequest(deviceA)
	wrongHeader.Header.Set("X-LINEPLAY-ACNT", guestRequest(deviceB).Header.Get("X-LINEPLAY-ACNT"))
	noHeader := guestRequest(deviceA)
	noHeader.Header.Del("X-LINEPLAY-ACNT")
	cases := map[string]*http.Request{
		"wrong header":   wrongHeader,
		"missing header": noHeader,
		"space in code":  guestRequest("bad code"),
		"empty code":     guestRequest(""),
		"long code":      guestRequest(long),
		"not json":       httptest.NewRequest(http.MethodPost, "/v4/account/guest/generate", strings.NewReader("not json")),
	}
	for name, req := range cases {
		rec := serveRequest(t, req)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != httpx.BadRequestBody {
			t.Errorf("%s: status = %d body = %q, want 400", name, rec.Code, rec.Body.String())
		}
	}
	store.AccountsMu.Lock()
	n := len(store.Accounts)
	store.AccountsMu.Unlock()
	if n != 0 {
		t.Fatalf("refused requests created %d accounts", n)
	}
}

// TestGuestHeaderVector pins the header key: lowercase md5 hex of uniqueCode + the APK secret.
func TestGuestHeaderVector(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v4/account/guest/generate", nil)
	req.Header.Set("X-LINEPLAY-ACNT", "51718048f7d4f52da49b4ceca9a4b249")
	if !guestHeaderOK(req, "00000000-0000-0000-0000-000000000000") {
		t.Fatal("pinned md5 vector rejected")
	}
	if guestHeaderOK(req, "00000000-0000-0000-0000-000000000001") {
		t.Fatal("header accepted for another uniqueCode")
	}
}

// TestGuestGenerateSkipsDeletedAccount: Delete Avatar takes the account out of the table, so its device
// code is free again and the device starts fresh.
func TestGuestGenerateSkipsDeletedAccount(t *testing.T) {
	guestAccountTest(t)
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	if err := store.LoadSocialFrom(filepath.Join(t.TempDir(), "social.json")); err != nil {
		t.Fatal(err)
	}
	old := guestGenerateCode(t, deviceA)
	created := createAvatar(t, avAuthValue(t, createSession(t, old)), []byte(`{"name":"Gone","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON00164"]}`), "")
	if created.Code != http.StatusOK {
		t.Fatalf("create avatar = %d %s", created.Code, created.Body.String())
	}
	store.AccountsMu.Lock()
	acc := store.Accounts[old]
	store.AccountsMu.Unlock()
	if acc == nil {
		t.Fatal("device token has no account")
	}
	if err := deleteAccount(acc); err != nil {
		t.Fatal(err)
	}
	if rec := createSession(t, old); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted account's token = %d, want 404", rec.Code)
	}
	fresh := sessionOf(t, guestGenerateCode(t, deviceA))
	if fresh.Aid != "0" || fresh.SessionKey == acc.SessionKey {
		t.Fatalf("device after delete got %+v, want a fresh account", fresh)
	}
}
