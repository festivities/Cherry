package admin

import (
	"bytes"
	"io"
	"mime/multipart"
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

// uploadPicture posts the picture form as multipart, the way the browser sends it.
func uploadPicture(t *testing.T, c *client, csrf, name string, file []byte) reply {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range map[string]string{"csrf": csrf, "name": name} {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("png", "pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(fw, bytes.NewReader(file)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := c.req(http.MethodPost, "/garden-buttons/pictures/add", nil)
	r.Body = io.NopCloser(&body)
	r.ContentLength = int64(body.Len())
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return c.send(r)
}

// TestGardenPicturesPage: the multipart upload passes the CSRF check (a wrong token is refused) and is owner only;
// without the patcher lists (tests have no asset store) the page says custom pictures are off and an upload is
// refused with 409, storing nothing; a stored picture is listed and served to the owner only.
func TestGardenPicturesPage(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	st := fx.client()
	st.signInDev("staff")
	expect(t, uploadPicture(t, st, st.csrf(), "x", []byte("x")), http.StatusForbidden)

	c := fx.client()
	c.signInDev("owner")
	expect(t, uploadPicture(t, c, "wrong", "x", []byte("x")), http.StatusForbidden)
	r := uploadPicture(t, c, c.csrf(), "Mine", []byte("\x89PNG\r\n\x1a\n"))
	expect(t, r, http.StatusConflict)
	if !strings.Contains(r.body, "Custom pictures are off") {
		t.Fatalf("refusal does not say why: %s", r.body)
	}
	if list, _ := store.GardenIconList(); len(list) != 0 {
		t.Fatalf("refused upload stored %v", list)
	}
	page := c.get("/garden-buttons")
	if bad := inlineMarkup(page.body); len(bad) > 0 {
		t.Fatalf("inline markup: %v", bad)
	}
	for _, want := range []string{"Custom pictures (Cherry-made)", "Custom pictures are off:", "No custom pictures yet."} {
		if !strings.Contains(page.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page.body, `action="/garden-buttons/pictures/add"`) {
		t.Error("the upload form is offered while custom pictures are off")
	}
	if err := store.AddGardenIcon(store.GardenIcon{ID: "0123456789ab", Name: "Stored", Width: 1, Height: 1}, []byte("png"), "dev-owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	page = c.get("/garden-buttons")
	for _, want := range []string{"Stored", `src="/garden-buttons/custom/0123456789ab"`, `action="/garden-buttons/pictures/remove"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("page with a picture lacks %q", want)
		}
	}
	if got := c.get("/garden-buttons/custom/0123456789ab"); got.StatusCode != http.StatusOK || got.body != "png" {
		t.Fatalf("owner picture = %d %q", got.StatusCode, got.body)
	}
	expect(t, st.get("/garden-buttons/custom/0123456789ab"), http.StatusForbidden)
	removed := c.post("/garden-buttons/pictures/remove", url.Values{"csrf": {c.csrf()}, "id": {"0123456789ab"}})
	if removed.StatusCode != http.StatusServiceUnavailable && removed.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove = %d %s", removed.StatusCode, removed.body)
	}
	if list, _ := store.GardenIconList(); len(list) != 0 {
		t.Fatalf("picture not removed: %v", list)
	}
}
