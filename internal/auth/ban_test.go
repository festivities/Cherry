package auth

import (
	"net/http"
	"net/url"
	"testing"

	"cherry/internal/store"
)

// identityPlayer returns the row id of the account with identity, found by its exact identity.
func identityPlayer(t *testing.T, identity string) int64 {
	t.Helper()
	rows, _ := store.FindPlayers(identity)
	if len(rows) != 1 {
		t.Fatalf("%d accounts for %s, want 1", len(rows), identity)
	}
	return rows[0].ID
}

// TestBannedIdentityIsRefusedAtCallbackAndExchange: a ban refuses the callback (error=banned, no ticket) and the exchange of a
// ticket minted before the ban (403 banned, no code minted); lifting the ban restores both.
func TestBannedIdentityIsRefusedAtCallbackAndExchange(t *testing.T) {
	freshAccounts(t)
	env := newEnv(t, discordConfig(t)) // the allowlist holds discord:123456789012345678
	const identity = "discord:123456789012345678"
	signIn := func() url.Values { return discordSignIn(t, env, "123456789012345678") }

	if rec := env.exchange(signIn().Get("ticket"), testVerifier, testIP); rec.Code != http.StatusOK {
		t.Fatalf("first sign-in = %d %s", rec.Code, rec.Body.String())
	}
	early := signIn().Get("ticket") // a ticket the callback gave before the ban
	if early == "" {
		t.Fatal("no ticket before the ban")
	}
	id := identityPlayer(t, identity)
	if err := store.BanPlayer(id, "spam", "discord:999"); err != nil {
		t.Fatal(err)
	}

	rec := env.exchange(early, testVerifier, testIP)
	if rec.Code != http.StatusForbidden || errorOf(t, rec) != "banned" {
		t.Fatalf("exchange of an early ticket = %d %s, want 403 banned", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"error":"banned"}` {
		t.Fatalf("exchange body = %s", rec.Body.String())
	}
	if q := signIn(); q.Get("error") != "banned" || q.Get("ticket") != "" {
		t.Fatalf("callback of a banned identity = %v, want error=banned and no ticket", q)
	}
	store.AccountsMu.Lock()
	codes := len(store.IdentityAccountLocked(identity).CodeHashes)
	store.AccountsMu.Unlock()
	if codes != 1 {
		t.Fatalf("a refused exchange minted a code: %d hashes, want the one from the first sign-in", codes)
	}

	if err := store.UnbanPlayer(id, "discord:999"); err != nil {
		t.Fatal(err)
	}
	if rec := env.exchange(signIn().Get("ticket"), testVerifier, testIP); rec.Code != http.StatusOK {
		t.Fatalf("sign-in after the unban = %d %s", rec.Code, rec.Body.String())
	}
}
