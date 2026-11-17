package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cherry/internal/store"
)

// TestSquareRoomCapFromSettings: the owner sets 5 players and the page shows it, with the reset; bad values are 400 and
// change nothing; the reset brings the default 20 back. Each change is audited with the owner as the actor.
func TestSquareRoomCapFromSettings(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	c := fx.client()
	c.signInDev("owner")
	page := c.get("/settings").body
	for _, want := range []string{
		"Square room size", "The default (20): each Square room holds 20 players.",
		`action="/settings/square-room-cap"`, `data-confirm="Save this room size?`,
		"New players go to the first room with space; lowering the size does not move anyone already inside.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the Settings page lacks %q", want)
		}
	}
	if strings.Contains(page, `action="/settings/square-room-cap/reset"`) {
		t.Fatal("the reset is offered with no panel value")
	}

	set := c.post("/settings/square-room-cap", url.Values{"csrf": {c.csrf()}, "players": {"5"}})
	expect(t, set, http.StatusSeeOther)
	if loc := set.Header.Get("Location"); loc != "/settings?notice=square-room-cap-saved" {
		t.Fatalf("redirect to %q", loc)
	}
	after := c.follow(set)
	if !strings.Contains(after.body, "Set in this panel: each Square room holds 5 players.") ||
		!strings.Contains(after.body, "Reset to default (20 players)") {
		t.Fatal("the Settings page after the save does not show the panel's 5 players and the reset")
	}
	for _, bad := range []string{"1", "101", "abc", ""} {
		expect(t, c.post("/settings/square-room-cap", url.Values{"csrf": {c.csrf()}, "players": {bad}}), http.StatusBadRequest)
	}
	if n := store.SquareRoomCap(store.SquareRoomCapDefault); n != 5 {
		t.Fatalf("a refused value changed the room size to %d", n)
	}

	reset := c.post("/settings/square-room-cap/reset", url.Values{"csrf": {c.csrf()}})
	expect(t, reset, http.StatusSeeOther)
	if loc := reset.Header.Get("Location"); loc != "/settings?notice=square-room-cap-reset" {
		t.Fatalf("reset redirect to %q", loc)
	}
	if n := store.SquareRoomCap(store.SquareRoomCapDefault); n != store.SquareRoomCapDefault {
		t.Fatalf("after the reset the room size is %d, want the default", n)
	}
	if _, set, _ := store.SquareRoomCapSetting(); set {
		t.Fatal("the reset left a panel value")
	}

	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "setting", `"new":"5"`) || !hasAudit(rows, "dev-owner", "setting", `"new":"default"`) {
		t.Error("the room size changes have no audit rows by the owner")
	}
}

// TestSquareRoomCapOwnerOnly: a staff member gets 403 on the save and on the reset, and nothing changes.
func TestSquareRoomCapOwnerOnly(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	st := fx.client()
	st.signInDev("staff")
	csrf := st.csrf()
	expect(t, st.post("/settings/square-room-cap", url.Values{"csrf": {csrf}, "players": {"5"}}), http.StatusForbidden)
	expect(t, st.post("/settings/square-room-cap/reset", url.Values{"csrf": {csrf}}), http.StatusForbidden)
	if _, set, _ := store.SquareRoomCapSetting(); set {
		t.Fatal("a refused change set the room size")
	}
}
