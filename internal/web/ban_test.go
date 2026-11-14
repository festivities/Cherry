package web

import (
	"net/http"
	"testing"

	"cherry/internal/config"
	"cherry/internal/httpx"
	"cherry/internal/store"
)

// TestBannedIdentityGetsNoTokenFromGuestGenerate: in dev-fake a banned identity's uniqueCode gets the same 400 as a code nobody
// minted, its token stops signing it in, and the ban is kept by the store.
func TestBannedIdentityGetsNoTokenFromGuestGenerate(t *testing.T) {
	useConfig(t, devFakeConfig())
	guestAccountTest(t)
	const code = "00000000-0000-4000-8000-0000000000bb"
	if _, _, err := store.MintIdentityCode("dev-banned", store.CodeHash(code), true); err != nil {
		t.Fatal(err)
	}
	token := guestGenerateCode(t, code)
	if rec := createSession(t, token); rec.Code != http.StatusOK {
		t.Fatalf("before the ban: createSession = %d", rec.Code)
	}
	rows, _ := store.FindPlayers("dev-banned")
	if len(rows) != 1 {
		t.Fatalf("%d accounts for dev-banned, want 1", len(rows))
	}
	if err := store.BanPlayer(rows[0].ID, "spam", "discord:9"); err != nil {
		t.Fatal(err)
	}

	rec := serveRequest(t, guestRequest(code))
	if rec.Code != http.StatusBadRequest || rec.Body.String() != httpx.BadRequestBody {
		t.Fatalf("guest/generate of a banned code = %d %q, want the bad-code 400", rec.Code, rec.Body.String())
	}
	if rec := createSession(t, token); rec.Code == http.StatusOK {
		t.Fatal("a token of the banned identity still creates a session")
	}
}

// TestBannedDeviceGetsNoTokenInLabGuest: lab-guest refuses a banned device account the same way and makes it no new account.
func TestBannedDeviceGetsNoTokenInLabGuest(t *testing.T) {
	useConfig(t, config.Config{})
	guestAccountTest(t)
	if token := guestGenerateCode(t, deviceA); token == "" {
		t.Fatal("the first guest/generate gave no token")
	}
	rows, _ := store.FindPlayers("0")
	if len(rows) != 1 {
		t.Fatalf("%d accounts with aid 0, want the device's one", len(rows))
	}
	if err := store.BanPlayer(rows[0].ID, "spam", "discord:9"); err != nil {
		t.Fatal(err)
	}
	rec := serveRequest(t, guestRequest(deviceA))
	if rec.Code != http.StatusBadRequest || rec.Body.String() != httpx.BadRequestBody {
		t.Fatalf("lab-guest guest/generate of a banned device = %d %q, want the bad-code 400", rec.Code, rec.Body.String())
	}
	if again, _ := store.FindPlayers("0"); len(again) != 1 {
		t.Fatalf("the refused login made %d accounts with aid 0, want 1", len(again))
	}
}
