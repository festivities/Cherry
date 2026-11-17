package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cherry/internal/config"
	"cherry/internal/store"
)

// settingsConfig is the dev-fake staff UI with dev-staff on the allowlist and the server's chat retention at 30 days.
func settingsConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := devConfigWithStaff(t)
	cfg.ChatDays = 30
	return cfg
}

// settingsAdd posts the add form of the Settings page as c.
func settingsAdd(c *client, identity, note string) reply {
	return c.post("/settings/staff/add", url.Values{"csrf": {c.csrf()}, "identity": {identity}, "note": {note}})
}

// TestSettingsOwnerOnly: a staff member who is not the owner gets 403 on the page and on each change, and nothing changes. The
// owner sees the page, and the navigation shows Settings to the owner only.
func TestSettingsOwnerOnly(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	owner := fx.client()
	owner.signInDev("owner")
	if !strings.Contains(owner.get("/").body, `href="/settings"`) {
		t.Fatal("the owner's navigation has no Settings entry")
	}
	if r := owner.get("/settings"); r.StatusCode != http.StatusOK || !strings.Contains(r.body, `href="/settings" aria-current="page"`) {
		t.Fatalf("the owner's Settings page: status %d", r.StatusCode)
	}

	st := fx.client()
	st.signInDev("staff")
	if strings.Contains(st.get("/").body, `href="/settings"`) {
		t.Fatal("a staff member's navigation shows Settings")
	}
	expect(t, st.get("/settings"), http.StatusForbidden)
	csrf := st.csrf()
	for _, p := range []struct {
		path string
		form url.Values
	}{
		{"/settings/chat-days", url.Values{"csrf": {csrf}, "days": {"7"}}},
		{"/settings/chat-days/reset", url.Values{"csrf": {csrf}}},
		{"/settings/staff/add", url.Values{"csrf": {csrf}, "identity": {"dev-helper"}}},
		{"/settings/staff/remove", url.Values{"csrf": {csrf}, "identity": {"dev-helper"}}},
	} {
		r := st.post(p.path, p.form)
		expect(t, r, http.StatusForbidden)
		if !strings.Contains(r.body, "Only the owner can change the settings.") {
			t.Errorf("%s: the refusal does not name the owner", p.path)
		}
	}

	if days, set, _ := store.ChatRetentionSetting(); set || days != 0 {
		t.Fatalf("a refused change set the retention: %d %t", days, set)
	}
	if list, _ := store.StaffMembers(); len(list) != 0 {
		t.Fatalf("a refused change added %d members", len(list))
	}
	for _, row := range auditRows(t) {
		if row.Action == "setting" || row.Action == "staff-add" || row.Action == "staff-remove" {
			t.Fatalf("a refused change left an audit row: %+v", row)
		}
	}
}

// TestSettingsNeedSessionAndFreshSignIn: without a session the page is the sign-in page and each change is 401. With a session
// older than the fresh window a change is refused and nothing changes.
func TestSettingsNeedSessionAndFreshSignIn(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	anon := fx.client()
	expect(t, anon.get("/settings"), http.StatusUnauthorized)
	expect(t, anon.post("/settings/chat-days", url.Values{"csrf": {"x"}, "days": {"7"}}), http.StatusUnauthorized)
	expect(t, anon.post("/settings/chat-days/reset", url.Values{"csrf": {"x"}}), http.StatusUnauthorized)
	expect(t, anon.post("/settings/staff/add", url.Values{"csrf": {"x"}, "identity": {"dev-helper"}}), http.StatusUnauthorized)
	expect(t, anon.post("/settings/staff/remove", url.Values{"csrf": {"x"}, "identity": {"dev-helper"}}), http.StatusUnauthorized)

	c := fx.client()
	c.signInDev("owner")
	fx.clk.advance(6 * time.Minute)
	stale := c.post("/settings/chat-days", url.Values{"csrf": {c.csrf()}, "days": {"7"}})
	expect(t, stale, http.StatusForbidden)
	if !strings.Contains(stale.body, "needs a Discord sign-in") {
		t.Fatal("the stale change does not ask for a fresh sign-in")
	}
	if _, set, _ := store.ChatRetentionSetting(); set {
		t.Fatal("a stale change set the retention")
	}
}

// TestChatRetentionFromSettings: the owner sets 7 days and the chat page says 7; the reset brings the configured 30 back. Bad
// values are 400 and change nothing.
func TestChatRetentionFromSettings(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	c := fx.client()
	c.signInDev("owner")
	chat := c.get("/chat").body
	if !strings.Contains(chat, "Kept for 30 days, then deleted.") || !strings.Contains(chat, `href="/settings">Change in Settings</a>`) {
		t.Fatal("the chat page does not show the configured 30 days and the owner's link")
	}

	set := c.post("/settings/chat-days", url.Values{"csrf": {c.csrf()}, "days": {"7"}})
	expect(t, set, http.StatusSeeOther)
	if loc := set.Header.Get("Location"); loc != "/settings?notice=chat-days-saved" {
		t.Fatalf("redirect to %q", loc)
	}
	after := c.follow(set)
	if !strings.Contains(after.body, "The chat log keeps messages for the new number of days.") ||
		!strings.Contains(after.body, "Set in this panel: the chat log keeps messages for 7 days") {
		t.Fatal("the Settings page after the save does not show the panel's 7 days")
	}
	if !strings.Contains(c.get("/chat").body, "Kept for 7 days, then deleted.") {
		t.Fatal("the chat page does not show 7 days after the change")
	}

	for _, bad := range []string{"-1", "3651", "abc", ""} {
		expect(t, c.post("/settings/chat-days", url.Values{"csrf": {c.csrf()}, "days": {bad}}), http.StatusBadRequest)
	}
	if !strings.Contains(c.get("/chat").body, "Kept for 7 days, then deleted.") {
		t.Fatal("a refused value changed the retention")
	}

	reset := c.post("/settings/chat-days/reset", url.Values{"csrf": {c.csrf()}})
	expect(t, reset, http.StatusSeeOther)
	if loc := reset.Header.Get("Location"); loc != "/settings?notice=chat-days-reset" {
		t.Fatalf("reset redirect to %q", loc)
	}
	if !strings.Contains(c.get("/chat").body, "Kept for 30 days, then deleted.") {
		t.Fatal("the chat page does not show the configured 30 days after the reset")
	}
	if _, set, _ := store.ChatRetentionSetting(); set {
		t.Fatal("the reset left a panel value")
	}
}

// TestStaffMembersSignIn: a member added from the panel can sign in and reach the dashboard; removing them ends their open
// session at once (401), and a new sign-in is refused.
func TestStaffMembersSignIn(t *testing.T) {
	fx := newFixture(t, devConfig())
	owner := fx.client()
	owner.signInDev("owner")
	helper := fx.client()

	refused := helper.get("/auth/login?as=helper")
	expect(t, refused, http.StatusFound)
	expect(t, helper.follow(refused), http.StatusForbidden)

	added := settingsAdd(owner, "dev-helper", "Helper")
	expect(t, added, http.StatusSeeOther)
	if loc := added.Header.Get("Location"); loc != "/settings?notice=staff-added" {
		t.Fatalf("add redirect to %q", loc)
	}
	helper.signInDev("helper")
	expect(t, helper.get("/"), http.StatusOK)

	removed := owner.post("/settings/staff/remove", url.Values{"csrf": {owner.csrf()}, "identity": {"dev-helper"}})
	expect(t, removed, http.StatusSeeOther)
	expect(t, helper.get("/"), http.StatusUnauthorized)

	again := helper.get("/auth/login?as=helper")
	expect(t, again, http.StatusFound)
	expect(t, helper.follow(again), http.StatusForbidden)
}

// TestStaffMemberRefusals: a duplicate, the owner, a malformed identity and an unknown removal are 400 and add or remove
// nothing. A bare Discord id is stored with its prefix.
func TestStaffMemberRefusals(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	expect(t, settingsAdd(c, "dev-helper", "Helper"), http.StatusSeeOther)

	for _, id := range []string{"dev-helper", "dev-owner", "dev bad", "dev-", "12345", "", "dev-" + strings.Repeat("a", 25)} {
		expect(t, settingsAdd(c, id, ""), http.StatusBadRequest)
	}
	owner := settingsAdd(c, "dev-owner", "")
	if !strings.Contains(owner.body, "The owner already has access.") {
		t.Fatal("the owner refusal does not say so")
	}
	expect(t, c.post("/settings/staff/remove", url.Values{"csrf": {c.csrf()}, "identity": {"dev-nobody"}}), http.StatusBadRequest)

	expect(t, settingsAdd(c, "222222222222222222", ""), http.StatusSeeOther)
	list, _ := store.StaffMembers()
	if len(list) != 2 || list[0].Identity != "dev-helper" || list[1].Identity != "discord:222222222222222222" {
		t.Fatalf("members after the refusals: %+v", list)
	}
}

// TestSettingsPageListsStaff: the table shows the owner (not removable), the members with their notes and a Remove form, the
// allowlist line, and the add form with the dev-fake hint.
func TestSettingsPageListsStaff(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	c := fx.client()
	c.signInDev("owner")
	expect(t, settingsAdd(c, "dev-helper", "Helper for the game"), http.StatusSeeOther)
	expect(t, c.post("/settings/chat-days", url.Values{"csrf": {c.csrf()}, "days": {"7"}}), http.StatusSeeOther)
	page := c.get("/settings").body
	for _, want := range []string{
		"dev-owner", "not removable", "dev-helper", "Helper for the game", "Copy User ID",
		`action="/settings/staff/remove"`, `data-confirm="Remove this staff member?`,
		"A staff allowlist file is also configured", "dev-&lt;name&gt;",
		"Use the server's configured value (30 days)",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the Settings page lacks %q", want)
		}
	}
	if strings.Contains(page, "/staff.txt") || strings.Contains(page, "dev-staff") {
		t.Error("the Settings page shows the allowlist's path or its names")
	}
}

// TestSettingsAudit: each change writes its audit row with the owner as the actor.
func TestSettingsAudit(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	c := fx.client()
	c.signInDev("owner")
	expect(t, c.post("/settings/chat-days", url.Values{"csrf": {c.csrf()}, "days": {"7"}}), http.StatusSeeOther)
	expect(t, settingsAdd(c, "dev-helper", ""), http.StatusSeeOther)
	expect(t, c.post("/settings/staff/remove", url.Values{"csrf": {c.csrf()}, "identity": {"dev-helper"}}), http.StatusSeeOther)

	rows := auditRows(t)
	for _, want := range []struct{ action, reason string }{
		{"setting", `"new":"7"`}, {"staff-add", `"identity":"dev-helper"`}, {"staff-remove", `"identity":"dev-helper"`},
	} {
		if !hasAudit(rows, "dev-owner", want.action, want.reason) {
			t.Errorf("no %s audit row with %s by the owner", want.action, want.reason)
		}
	}
}
