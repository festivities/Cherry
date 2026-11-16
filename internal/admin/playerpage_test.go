package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// rosterButler is the id of the first butler of the embedded roster.
func rosterButler() int64 {
	return economy.ActiveButlers().Butlers[0].ID
}

// seedBook gives the account with row id a closet, a Garden bag, a hired butler, racing points, Smiles and Boss Coins, a friend
// and a circle, so the player page has something in every section. The friendship and the circle live in memory only.
func seedBook(t *testing.T, id int64, aid string) {
	t.Helper()
	store.ResetSocial()
	store.ResetCircles()
	t.Cleanup(func() {
		store.ResetSocial()
		store.ResetCircles()
	})
	store.AccountsMu.Lock()
	acc := store.PlayerAccountLocked(id)
	codes := []string{"CUAE0001A", "CUAE0002B", "CUAE0003C"}
	acc.ItemCodes, acc.InventoryCodes = codes, codes
	acc.GardenBag = []store.GardenBagStack{{Item: 110000, Count: 3, Updated: 1}}
	acc.Butlers = &store.ButlerBook{Hired: []store.ButlerHire{{ID: rosterButler(), Begin: time.Now().UnixMilli(), End: time.Now().Add(72 * time.Hour).UnixMilli()}},
		Leader: rosterButler()}
	acc.Racing = &store.RacingRecord{Points: 42}
	acc.Smiles, acc.BossCoins, acc.FaceTickets = 7, 9, 2
	err := store.SaveLocked(nil, acc)
	store.AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	store.SocialMu.Lock()
	store.Friendships = append(store.Friendships, store.Friendship{A: aid, B: "6602", State: "accepted"})
	store.SocialMu.Unlock()
	store.CirclesMu.Lock()
	store.CircleState.Circles = append(store.CircleState.Circles, store.Circle{ID: 7, Name: "Test circle",
		Members: []store.CircleMember{{Aid: aid, Role: "LEADER"}}})
	store.CirclesMu.Unlock()
}

// TestPlayerPageSectionsRender: the page has each section with its anchor and what the account holds in it.
func TestPlayerPageSectionsRender(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-pp1", "6601", "Pia", 40)
	seedBook(t, id, "6601")

	r := c.get(playerURL(id))
	expect(t, r, http.StatusOK)
	checkPage(t, "player page", r)
	for _, anchor := range []string{"overview", "inventory", "garden-bag", "butlers", "social", "moderation", "ledger", "grant"} {
		if !strings.Contains(r.body, `id="`+anchor+`"`) || !strings.Contains(r.body, `href="#`+anchor+`"`) {
			t.Errorf("section %s has no anchor or no link", anchor)
		}
	}
	material, ok := economy.GatherMaterialByID(110000)
	if !ok {
		t.Fatal("item 110000 is not a gathering material in the embedded data")
	}
	butler, ok := economy.ButlerByID(rosterButler())
	if !ok {
		t.Fatal("the first Cherry butler is not in the embedded data")
	}
	for _, want := range []string{
		"Race tickets left today", `<th scope="row">Ride points</th><td class="num">42</td>`,
		`<th scope="row">Smiles</th><td class="num">7</td>`, `<th scope="row">Boss Coins</th><td class="num">9</td>`,
		"3 owned item codes", "CUAE0003C", material.Name, butler.Name, "Test circle", "role LEADER",
		`href="/players/`, ">6602<", "Pia",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the player page does not show %q", want)
		}
	}
	if !strings.Contains(r.body, `<option value="110000">`) {
		t.Error("the grant form does not offer the Garden materials")
	}
	if !strings.Contains(r.body, "<option value=\"smiles\">Smiles</option>") {
		t.Error("the grant form does not offer every currency")
	}
	if strings.Contains(r.body, "Online now</dt><dd>yes") {
		t.Error("an account with no live connection shows as online")
	}
}

// TestPlayerLedgerWindowFilters: ?window= keeps only the lines of the window; an unknown window is refused.
func TestPlayerLedgerWindowFilters(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-pp2", "6611", "Win", 0)
	if r := c.post(playerURL(id)+"/grant", url.Values{"csrf": {c.csrf()}, "currency": {"gems"}, "delta": {"25"}, "reason": {"windowed"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("grant: status %d", r.StatusCode)
	}
	if !strings.Contains(c.get(playerURL(id)+"?window=24h").body, "windowed") {
		t.Fatal("a line of the last 24 hours is missing from the 24 hours window")
	}
	if r := c.get(playerURL(id) + "?window=bogus"); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown window: status %d, want 400", r.StatusCode)
	}
	if r := c.get(playerURL(id) + "?window=7d"); r.StatusCode != http.StatusOK {
		t.Fatalf("7 days window: status %d", r.StatusCode)
	}
}

// TestExtendedGrantEachCurrencyAndMaterial: every currency and a material are granted and taken away, each change is audited,
// and a take-away below zero is refused and changes nothing.
func TestExtendedGrantEachCurrencyAndMaterial(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-gx1", "6621", "Gus", 0)
	store.AccountsMu.Lock()
	acc := store.PlayerAccountLocked(id)
	acc.Racing = &store.RacingRecord{Day: economy.AttendDayKey(time.Now()), Used: 3}
	err := store.SaveLocked(nil, acc)
	store.AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	grant := func(kind, item, delta string) reply {
		return c.post(playerURL(id)+"/grant", url.Values{"csrf": {c.csrf()}, "currency": {kind}, "item": {item}, "delta": {delta}, "reason": {"extended"}})
	}
	state := func() (ft, smiles, boss, points, used, bag int64) {
		store.AccountsMu.Lock()
		defer store.AccountsMu.Unlock()
		a := store.PlayerAccountLocked(id)
		for _, s := range a.GardenBag {
			if s.Item == 110000 {
				bag = s.Count
			}
		}
		return a.FaceTickets, a.Smiles, a.BossCoins, a.Racing.Points, a.Racing.Used, bag
	}

	gave := []struct {
		name, kind, item, delta string
	}{
		{"face tickets", "faceTickets", "", "5"},
		{"smiles", "smiles", "", "5"},
		{"boss coins", "bossCoins", "", "5"},
		{"ride points", "ridePoints", "", "5"},
		{"race tickets back", "raceTickets", "", "2"},
		{"a garden material", "material", "110000", "4"},
	}
	for _, g := range gave {
		expect(t, grant(g.kind, g.item, g.delta), http.StatusSeeOther)
	}
	if ft, smiles, boss, points, used, bag := state(); ft != 5 || smiles != 5 || boss != 5 || points != 5 || used != 1 || bag != 4 {
		t.Fatalf("after the grants: tickets %d smiles %d boss %d points %d used %d bag %d", ft, smiles, boss, points, used, bag)
	}

	takes := []struct {
		name, kind, item, delta string
	}{
		{"smiles", "smiles", "", "-2"},
		{"boss coins below zero", "bossCoins", "", "-6"},
		{"ride points below zero", "ridePoints", "", "-9"},
		{"race tickets given back beyond today's", "raceTickets", "", "5"},
		{"a material below zero", "material", "110000", "-9"},
		{"an unknown material", "material", "1", "-1"},
		{"no material chosen", "material", "", "-1"},
		{"an unknown currency", "gold", "", "-1"},
	}
	refused := map[string]bool{"boss coins below zero": true, "ride points below zero": true, "race tickets given back beyond today's": true,
		"a material below zero": true, "an unknown material": true, "no material chosen": true, "an unknown currency": true}
	for _, tk := range takes {
		got := grant(tk.kind, tk.item, tk.delta)
		if refused[tk.name] {
			if got.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: status %d, want 400", tk.name, got.StatusCode)
			}
			continue
		}
		expect(t, got, http.StatusSeeOther)
	}
	if ft, smiles, boss, points, used, bag := state(); smiles != 3 || boss != 5 || points != 5 || used != 1 || bag != 4 || ft != 5 {
		t.Fatalf("after the take-aways: tickets %d smiles %d boss %d points %d used %d bag %d", ft, smiles, boss, points, used, bag)
	}

	lines, err := store.PlayerLedger(id, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		seen[l.Currency] = true
		if !strings.HasPrefix(l.Reason, "staff:dev-owner:extended") {
			t.Errorf("ledger line %+v has no staff reason", l)
		}
	}
	for _, cur := range []string{"faceTickets", "smiles"} {
		if !seen[cur] {
			t.Errorf("no ledger line for %s", cur)
		}
	}
	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "grant", `"currency":"smiles","delta":-2`) {
		t.Error("the smiles take-away has no audit row with its delta")
	}
	if !hasAudit(rows, "dev-owner", "grant", `"raceTickets":2`) {
		t.Error("the race tickets grant has no audit row with its change")
	}
	if !hasAudit(rows, "dev-owner", "grant", `"material:110000":4`) {
		t.Error("the material grant has no audit row with its change")
	}
}
