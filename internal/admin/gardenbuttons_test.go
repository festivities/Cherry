package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

// TestGardenButtonsPage: owner only; an unknown picture or action is refused with 400 and nothing stored; a stored button
// is listed and removed with its notice and audit row; the page has no inline markup.
func TestGardenButtonsPage(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))

	st := fx.client()
	st.signInDev("staff")
	expect(t, st.get("/garden-buttons"), http.StatusForbidden)
	expect(t, st.post("/garden-buttons/remove", url.Values{"csrf": {st.csrf()}, "icon": {"x"}}), http.StatusForbidden)

	c := fx.client()
	c.signInDev("owner")
	page := c.get("/garden-buttons")
	expect(t, page, http.StatusOK)
	if bad := inlineMarkup(page.body); len(bad) > 0 {
		t.Fatalf("inline markup: %v", bad)
	}
	for _, want := range []string{"No Garden buttons yet.", "below the player picture"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// No asset store in tests: no picture is known, so every add is refused.
	expect(t, c.post("/garden-buttons/add", url.Values{"csrf": {c.csrf()}, "icon": {"24_event_icon"}, "label": {"x"}, "action": {"goSomewhere(closet)"}}), http.StatusBadRequest)
	if list, _ := store.GardenButtonList(); len(list) != 0 {
		t.Fatalf("refused add stored %v", list)
	}

	if err := store.AddGardenButton(store.GardenButton{Icon: "87_home_free_gift", Label: "Discord", Link: "openWithBrowser", Detail: "https://example.com/d"}, "dev-owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	page = c.get("/garden-buttons")
	for _, want := range []string{"Discord", "Web page: https://example.com/d", `src="/garden-buttons/icon/87_home_free_gift"`, `action="/garden-buttons/remove"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("page with a button lacks %q", want)
		}
	}
	removed := c.post("/garden-buttons/remove", url.Values{"csrf": {c.csrf()}, "icon": {"87_home_free_gift"}})
	expect(t, removed, http.StatusSeeOther)
	if loc := removed.Header.Get("Location"); loc != "/garden-buttons?notice=garden-button-removed" {
		t.Fatalf("remove redirect to %q", loc)
	}
	if !hasAudit(auditRows(t), "dev-owner", "garden-button-remove", `"icon":"87_home_free_gift"`) {
		t.Fatal("no remove audit row")
	}
	expect(t, c.get("/garden-buttons/icon/nope"), http.StatusNotFound)
}
