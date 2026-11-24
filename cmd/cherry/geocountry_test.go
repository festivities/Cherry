package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"cherry/internal/lpn"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestCountryLookupPostsOnlyUnknownPublicNetworks: a round asks the batch API only for the public networks of online players
// whose client sent no country (never a private network, never an avatar that has a country, each network once), keeps the
// answers so the staff pages see them, and does not ask about the same network again.
func TestCountryLookupPostsOnlyUnknownPublicNetworks(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"tok-a": {AccessToken: "tok-a", Aid: "701", Name: "NoCountry"},
		"tok-b": {AccessToken: "tok-b", Aid: "702", Name: "Lan"},
		"tok-c": {AccessToken: "tok-c", Aid: "703", Name: "Sent", Country: "th"},
		"tok-d": {AccessToken: "tok-d", Aid: "704", Name: "Unanswered"},
	})
	t.Cleanup(func() { store.SetGeoCountriesForTest(nil) })

	var mu sync.Mutex
	var asked [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var addrs []string
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&addrs) != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		mu.Lock()
		asked = append(asked, addrs)
		mu.Unlock()
		w.Write([]byte(`[{"ip":"203.0.113.0","country":"ID"}]`)) // 198.51.100.0 has no answer: left out
	}))
	defer srv.Close()

	g := newGeoLookup(srv.URL)
	conns := []lpn.Connection{
		{Aid: 701, Listener: "session", Remote: "203.0.113.0/24"},
		{Aid: 701, Listener: "gateway", Remote: "203.0.113.0/24"},
		{Aid: 702, Listener: "session", Remote: "10.0.2.0/24"},
		{Aid: 703, Listener: "session", Remote: "192.0.2.0/24"},
		{Aid: 704, Listener: "session", Remote: "198.51.100.0/24"},
	}
	n, err := g.round(context.Background(), conns, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("placed %d (%v), want 1", n, err)
	}
	if len(asked) != 1 || !slices.Equal(slices.Sorted(slices.Values(asked[0])), []string{"198.51.100.0", "203.0.113.0"}) {
		t.Fatalf("asked %v, want one request for the two unknown public networks", asked)
	}
	if left := store.CountryUnknownAids([]string{"701", "702", "703", "704"}); !slices.Equal(left, []string{"702", "704"}) {
		t.Fatalf("still unknown %v, want the LAN player and the unanswered one", left)
	}
	if p := store.PlayersOfAids([]string{"701"})["701"]; p.Country != "ID" || !p.CountryByIP {
		t.Fatalf("player 701 = %+v, want country ID by IP", p)
	}
	if _, err := g.round(context.Background(), conns, time.Now()); err != nil || len(asked) != 1 {
		t.Fatalf("second round asked again (%d requests, %v)", len(asked), err)
	}
}
