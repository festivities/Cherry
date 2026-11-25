package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/store"
)

// balanceForm is the form the balances page posts: the csrf token, the mode, the reason and each balance the test names (the
// others are blank, so untouched).
func balanceForm(csrf, mode, reason string, values map[string]string) url.Values {
	f := url.Values{"csrf": {csrf}, "mode": {mode}, "reason": {reason}}
	for _, fd := range balanceFields {
		f.Set(fd.Name, values[fd.Name])
	}
	return f
}

// setGems sets the Gems of the player with row id and saves it (a balance the test needs as the setup).
func setGems(t *testing.T, id, gems int64) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.PlayerAccountLocked(id)
	if acc == nil {
		t.Fatalf("no account %d", id)
	}
	acc.Gems = gems
	if err := store.SaveLocked(nil, acc); err != nil {
		t.Fatal(err)
	}
}

// balanceGrantAudits counts the audit rows of changes to every account (action grant-all).
func balanceGrantAudits(t *testing.T) int {
	t.Helper()
	n := 0
	for _, r := range auditRows(t) {
		if r.Action == "grant-all" {
			n++
		}
	}
	return n
}

// TestBalancesOwnerOnly: a staff member who is not the owner gets 403 on the page, the review and the apply, and nothing changes;
// the sidebar's entry is the owner's only.
func TestBalancesOwnerOnly(t *testing.T) {
	fx := newFixture(t, devConfigWithStaff(t))
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-bal1", "6101", "Bal", 40)

	st := fx.client()
	st.signInDev("staff")
	denied := st.get("/balances")
	expect(t, denied, http.StatusForbidden)
	if !strings.Contains(denied.body, "Only the owner can change every player&#39;s balances.") {
		t.Fatalf("the refusal does not say so: %.200s", denied.body)
	}
	expect(t, st.post("/balances/review", balanceForm(st.csrf(), "set", "reset", map[string]string{"gems": "0"})), http.StatusForbidden)
	expect(t, st.post("/balances/apply", withSend(balanceForm(st.csrf(), "set", "reset", map[string]string{"gems": "0"}), "abcdefghijklmnopqrstuvwx", 1)),
		http.StatusForbidden)
	if g := playerOf(t, id).Gems; g != 40 {
		t.Fatalf("a refused change moved gems to %d", g)
	}
	if n := balanceGrantAudits(t); n != 0 {
		t.Fatalf("refused changes wrote %d audit rows", n)
	}
	if strings.Contains(st.get("/").body, `href="/balances"`) {
		t.Error("the staff sidebar links to the balances page")
	}

	owner := c.get("/balances")
	expect(t, owner, http.StatusOK)
	checkPage(t, "/balances", owner)
	if !strings.Contains(owner.body, `name="mode"`) || !strings.Contains(c.get("/").body, `href="/balances"`) {
		t.Error("the owner's page or sidebar has no balances entry")
	}
}

// withSend adds the send id and the account count the review showed to a form.
func withSend(f url.Values, send string, expect int) url.Values {
	f.Set("send", send)
	f.Set("expect", strconv.Itoa(expect))
	return f
}

// TestBalancesReviewThenApply: the review shows the change and the accounts that would change and sends nothing; the apply changes
// every account once and writes one audit row; the page says how many players changed.
func TestBalancesReviewThenApply(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	a := seedPlayer(t, "dev-bal2", "6102", "Ann", 100)
	b := seedPlayer(t, "dev-bal3", "6103", "Ben", 0)
	n := totalMailboxes(t)

	review := c.post("/balances/review", balanceForm(c.csrf(), "add", "event", map[string]string{"gems": "50"}))
	expect(t, review, http.StatusOK)
	checkPage(t, "balances review", review)
	for _, want := range []string{"Every player gets 50 Gems", fmt.Sprintf("<strong>%d</strong> of %d", n, n)} {
		if !strings.Contains(review.body, want) {
			t.Fatalf("the review does not show %q: %.300s", want, review.body)
		}
	}
	if playerOf(t, a).Gems != 100 || playerOf(t, b).Gems != 0 {
		t.Fatal("the review changed balances")
	}

	apply := c.post("/balances/apply", withSend(balanceForm(c.csrf(), "add", "event", map[string]string{"gems": "50"}), hiddenValue(t, review.body, "send"), n))
	expect(t, apply, http.StatusSeeOther)
	if loc := apply.Header.Get("Location"); loc != fmt.Sprintf("/balances?notice=balances-changed&n=%d", n) {
		t.Fatalf("apply redirects to %q", loc)
	}
	page := c.follow(apply)
	expect(t, page, http.StatusOK)
	if !strings.Contains(page.body, fmt.Sprintf("Balances changed for %d players.", n)) {
		t.Fatalf("the flash does not name %d players", n)
	}
	if got := playerOf(t, a).Gems; got != 150 {
		t.Errorf("Ann's gems = %d, want 150", got)
	}
	if got := playerOf(t, b).Gems; got != 50 {
		t.Errorf("Ben's gems = %d, want 50", got)
	}
	if got := balanceGrantAudits(t); got != 1 {
		t.Errorf("%d audit rows for the change, want 1", got)
	}
	if !hasAudit(auditRows(t), "dev-owner", "grant-all", "event") {
		t.Error("no audit row with the reason")
	}
}

// TestBalancesTakeStopsAtZero: take away removes the amount from every player and a balance stops at 0; the review says so and the
// audit row names the mode.
func TestBalancesTakeStopsAtZero(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	a := seedPlayer(t, "dev-bal5", "6105", "Kim", 100)
	b := seedPlayer(t, "dev-bal6", "6106", "Lee", 30)
	n := totalMailboxes(t)

	review := c.post("/balances/review", balanceForm(c.csrf(), "take", "clean up", map[string]string{"gems": "50"}))
	expect(t, review, http.StatusOK)
	for _, want := range []string{"Every player loses 50 Gems (a balance stops at 0).", "Take away from every player"} {
		if !strings.Contains(review.body, want) {
			t.Fatalf("the review does not show %q: %.300s", want, review.body)
		}
	}
	if playerOf(t, a).Gems != 100 || playerOf(t, b).Gems != 30 {
		t.Fatal("the review changed balances")
	}

	apply := c.post("/balances/apply", withSend(balanceForm(c.csrf(), "take", "clean up", map[string]string{"gems": "50"}), hiddenValue(t, review.body, "send"), n))
	expect(t, apply, http.StatusSeeOther)
	if got := playerOf(t, a).Gems; got != 50 {
		t.Errorf("Kim's gems = %d, want 50 (100 less the 50 taken away)", got)
	}
	if got := playerOf(t, b).Gems; got != 0 {
		t.Errorf("Lee's gems = %d, want 0 (stopped at 0)", got)
	}
	if !hasAudit(auditRows(t), "dev-owner", "grant-all", `"mode":"take"`) {
		t.Error("no take audit row with the reason")
	}
}

// TestBalancesSetResetsAndReplayChangesNothing: a reset to 0 takes every player's Gems down; a second post of the same send, after
// a player has gems again, changes nothing and says so.
func TestBalancesSetResetsAndReplayChangesNothing(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	a := seedPlayer(t, "dev-bal4", "6104", "Cy", 300)
	n := totalMailboxes(t)
	form := balanceForm(c.csrf(), "set", "season reset", map[string]string{"gems": "0"})

	review := c.post("/balances/review", form)
	expect(t, review, http.StatusOK)
	if !strings.Contains(review.body, "Gems becomes 0.") || !strings.Contains(review.body, "Set takes away") {
		t.Fatalf("the review does not show the reset and its warning: %.300s", review.body)
	}
	send := hiddenValue(t, review.body, "send")

	expect(t, c.post("/balances/apply", withSend(balanceForm(c.csrf(), "set", "season reset", map[string]string{"gems": "0"}), send, n)), http.StatusSeeOther)
	if got := playerOf(t, a).Gems; got != 0 {
		t.Fatalf("Cy's gems after the reset = %d, want 0", got)
	}

	setGems(t, a, 25) // the player holds Gems again before the double post arrives
	replay := c.post("/balances/apply", withSend(balanceForm(c.csrf(), "set", "season reset", map[string]string{"gems": "0"}), send, n))
	expect(t, replay, http.StatusSeeOther)
	if loc := replay.Header.Get("Location"); loc != "/balances?notice=balances-already-applied" {
		t.Fatalf("the repeated apply redirects to %q, want already applied", loc)
	}
	if got := playerOf(t, a).Gems; got != 25 {
		t.Fatalf("the repeated apply changed gems to %d, want 25", got)
	}
	page := c.follow(replay)
	if !strings.Contains(page.body, "This balance change was already made.") {
		t.Error("the page does not say the change was already made")
	}
	if got := balanceGrantAudits(t); got != 1 {
		t.Errorf("%d audit rows, want 1", got)
	}
}

// TestBalancesRefusals: each bad form is refused at the review and at the apply, and no balance moves. A reset that would change
// no player is refused too, and so is an apply whose count is not the one reviewed.
func TestBalancesRefusals(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-bal5", "6105", "Refuse", 40)
	n := totalMailboxes(t)
	csrf := c.csrf()
	cases := []struct {
		name   string
		form   url.Values
		status int
	}{
		{"mode missing", balanceForm(csrf, "", "r", map[string]string{"gems": "5"}), http.StatusBadRequest},
		{"mode neither set nor add", balanceForm(csrf, "both", "r", map[string]string{"gems": "5"}), http.StatusBadRequest},
		{"no balance chosen", balanceForm(csrf, "add", "r", map[string]string{}), http.StatusBadRequest},
		{"add zero", balanceForm(csrf, "add", "r", map[string]string{"gems": "0"}), http.StatusBadRequest},
		{"add above the grant", balanceForm(csrf, "add", "r", map[string]string{"gems": "1000001"}), http.StatusBadRequest},
		{"add not a number", balanceForm(csrf, "add", "r", map[string]string{"cash": "ten"}), http.StatusBadRequest},
		{"set below zero", balanceForm(csrf, "set", "r", map[string]string{"gems": "-1"}), http.StatusBadRequest},
		{"set above the balance cap", balanceForm(csrf, "set", "r", map[string]string{"gems": "1073741825"}), http.StatusBadRequest},
		{"no reason", balanceForm(csrf, "add", "", map[string]string{"gems": "5"}), http.StatusBadRequest},
		{"reason of 201 characters", balanceForm(csrf, "add", strings.Repeat("r", 201), map[string]string{"gems": "5"}), http.StatusBadRequest},
		{"set that changes nobody", balanceForm(csrf, "set", "r", map[string]string{"smiles": "0"}), http.StatusBadRequest},
	}
	for _, tc := range cases {
		if got := c.post("/balances/review", tc.form); got.StatusCode != tc.status {
			t.Errorf("review, %s: status %d, want %d", tc.name, got.StatusCode, tc.status)
		}
	}
	if got := c.post("/balances/review", balanceForm("wrong", "add", "r", map[string]string{"gems": "5"})); got.StatusCode != http.StatusForbidden {
		t.Errorf("review with a wrong csrf token: status %d, want 403", got.StatusCode)
	}

	good := balanceForm(csrf, "add", "r", map[string]string{"gems": "5"})
	applies := []struct {
		name   string
		form   url.Values
		status int
	}{
		{"send id too short", withSend(good, "short", n), http.StatusBadRequest},
		{"count not a number", func() url.Values {
			f := good.Clone()
			f.Set("send", "abcdefghijklmnopqrstuvwx")
			f.Set("expect", "many")
			return f
		}(), http.StatusBadRequest},
		{"count the reviewer did not see", withSend(good, "abcdefghijklmnopqrstuvwx", n+1), http.StatusBadRequest},
	}
	for _, tc := range applies {
		got := c.post("/balances/apply", tc.form)
		if got.StatusCode != tc.status {
			t.Errorf("apply, %s: status %d, want %d", tc.name, got.StatusCode, tc.status)
		}
	}
	if !strings.Contains(c.post("/balances/apply", withSend(good, "abcdefghijklmnopqrstuvwx", n+1)).body, "review it again") {
		t.Error("the count refusal does not say to review again")
	}

	if g := playerOf(t, id).Gems; g != 40 {
		t.Fatalf("refused changes moved gems to %d", g)
	}
	if got := balanceGrantAudits(t); got != 0 {
		t.Fatalf("refused changes wrote %d audit rows", got)
	}
}
