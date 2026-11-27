package admin

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/assets"
	"cherry/internal/economy"
	"cherry/internal/itemcode"
	"cherry/internal/store"
)

func TestRolePermissionsHelperModeratorOwner(t *testing.T) {
	fx := newFixture(t, devConfig())
	owner := fx.client()
	owner.signInDev("owner")

	// Owner adds one Helper and one Moderator via Settings.
	expect(t, owner.post("/settings/staff/add", url.Values{
		"csrf": {owner.csrf()}, "identity": {"dev-vol"}, "role": {"helper"}, "note": {"Volunteer"},
	}), http.StatusSeeOther)
	expect(t, owner.post("/settings/staff/add", url.Values{
		"csrf": {owner.csrf()}, "identity": {"dev-mod"}, "role": {"moderator"}, "note": {"Mod"},
	}), http.StatusSeeOther)

	// --- Helper ---
	helper := fx.client()
	helper.signInDev("vol")

	// Visiting / redirects Helpers straight to /items.
	root := helper.get("/")
	expect(t, root, http.StatusSeeOther)
	if loc := root.Header.Get("Location"); loc != "/items" {
		t.Fatalf("helper GET / redirected to %q, want /items", loc)
	}

	itemsPage := helper.get("/items")
	expect(t, itemsPage, http.StatusOK)
	if !strings.Contains(itemsPage.body, `<span class="role helper">Helper</span>`) {
		t.Fatal("helper header lacks Helper role badge")
	}
	if strings.Contains(itemsPage.body, `action="/search"`) {
		t.Fatal("helper header should not show global player/chat search")
	}
	for _, forbiddenNav := range []string{`href="/players"`, `href="/chat"`, `href="/docs"`, `href="/settings"`, `href="/audit"`} {
		if strings.Contains(itemsPage.body, forbiddenNav) {
			t.Errorf("helper sidebar contains %s", forbiddenNav)
		}
	}
	if !strings.Contains(itemsPage.body, `href="/items" aria-current="page"`) {
		t.Fatal("helper sidebar lacks Item names entry")
	}

	// Every non-item route is 403 for Helper.
	for _, p := range []string{
		"/stats.json", "/players", "/players/deleted", "/mail", "/mail/sent",
		"/balances", "/broadcast", "/reports", "/chat", "/chat.json", "/diary", "/diary.json",
		"/circles", "/photos", "/online", "/economy", "/activity", "/geography",
		"/docs", "/docs/cherrytips.json", "/audit", "/settings", "/search", "/items/export.json",
	} {
		expect(t, helper.get(p), http.StatusForbidden)
	}
	// Helper POSTs to moderator/owner actions are also 403.
	hcsrf := helper.csrf()
	for _, p := range []string{"/docs/cherrytips.json/draft", "/settings/staff/add", "/broadcast/send"} {
		expect(t, helper.post(p, url.Values{"csrf": {hcsrf}}), http.StatusForbidden)
	}

	// --- Moderator ---
	mod := fx.client()
	mod.signInDev("mod")
	dash := mod.get("/")
	expect(t, dash, http.StatusOK)
	if !strings.Contains(dash.body, `<span class="role moderator">Moderator</span>`) {
		t.Fatal("moderator header lacks Moderator role badge")
	}
	if !strings.Contains(dash.body, `action="/search"`) {
		t.Fatal("moderator header lacks global search")
	}
	for _, allowed := range []string{"/players", "/reports", "/chat", "/diary", "/circles", "/photos", "/online", "/items", "/items/export.json", "/audit", "/search?q=x"} {
		expect(t, mod.get(allowed), http.StatusOK)
	}
	// Moderators cannot access Owner-only routes (incl. Documents, Settings, Balances, Broadcast).
	for _, denied := range []string{"/docs", "/docs/cherrytips.json", "/settings", "/balances", "/broadcast"} {
		expect(t, mod.get(denied), http.StatusForbidden)
	}
	if strings.Contains(dash.body, `href="/docs"`) || strings.Contains(dash.body, `href="/settings"`) {
		t.Fatal("moderator sidebar exposes owner-only links")
	}
	// Moderator global search hides Documents section.
	if sr := mod.get("/search?q=cherrytips"); strings.Contains(sr.body, "<h2>Documents") {
		t.Fatal("moderator search shows Documents section")
	}

	// --- Role change via Owner Settings ends open sessions immediately ---
	expect(t, owner.post("/settings/staff/role", url.Values{
		"csrf": {owner.csrf()}, "identity": {"dev-mod"}, "role": {"helper"},
	}), http.StatusSeeOther)
	// Mod's existing session is terminated immediately.
	expect(t, mod.get("/"), http.StatusUnauthorized)
	// Signing back in gives Helper access only.
	mod.signInDev("mod")
	expect(t, mod.get("/"), http.StatusSeeOther)
	expect(t, mod.get("/players"), http.StatusForbidden)
	expect(t, mod.get("/items"), http.StatusOK)

	// Helper can still log out cleanly.
	expect(t, helper.post("/auth/logout", url.Values{"csrf": {helper.csrf()}}), http.StatusSeeOther)
	expect(t, helper.get("/items"), http.StatusUnauthorized)
}

func TestHelperItemNamingInlineAndNoJSAndPreview(t *testing.T) {
	// Mount one real PNG preview so GET /items/{code}/preview returns 200 image/png.
	origRoot := t.TempDir()
	hairID, _ := itemcode.ID("CUHA00001")
	dir := filepath.Join(origRoot, "custom", strconv.FormatInt(hairID, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 200, G: 80, B: 120, A: 255})
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dp.png"), pngBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1409_iteminfo.artsitem"), []byte("1;0;0;0;0;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0_1.png"), pngBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	prevArchive := assets.Archive
	assets.Archive = assets.NewLocal(map[string][]string{"static/item/": {origRoot}})
	t.Cleanup(func() { assets.Archive = prevArchive })

	fx := newFixture(t, devConfig())
	owner := fx.client()
	owner.signInDev("owner")
	expect(t, owner.post("/settings/staff/add", url.Values{
		"csrf": {owner.csrf()}, "identity": {"dev-vol"}, "role": {"helper"}, "note": {"Namer"},
	}), http.StatusSeeOther)

	helper := fx.client()
	helper.signInDev("vol")

	// Advance past the 5-minute fresh window: Helpers must still be able to rename items without re-authing every 5m.
	fx.clk.advance(10 * time.Minute)

	// Preview endpoint serves PNG directly under staff origin.
	prev := helper.get("/items/CUHA00001/preview")
	expect(t, prev, http.StatusOK)
	if ct := prev.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("preview content-type = %q, want image/png", ct)
	}
	if !strings.Contains(prev.Header.Get("Cache-Control"), "private") {
		t.Fatalf("preview cache-control = %q", prev.Header.Get("Cache-Control"))
	}
	expect(t, helper.get("/items/NOTACODE/preview"), http.StatusNotFound)

	// Inline JSON save (items.js fetch path).
	r := helper.req(http.MethodPost, "/items/CUHA00001/name", url.Values{"csrf": {helper.csrf()}, "name": {"Cherry Blossom Bob"}})
	r.Header.Set("Accept", "application/json")
	resp := helper.send(r)
	expect(t, resp, http.StatusOK)
	var out itemNameResponse
	if err := json.Unmarshal([]byte(resp.body), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Custom || out.Name != "Cherry Blossom Bob" || out.DefaultName != "Hair 00001" || out.UpdatedBy != "" {
		t.Fatalf("helper JSON save response: %+v", out)
	}
	// Live game ItemName reflects it immediately!
	if got := economy.ItemName("CUHA00001"); got != "Cherry Blossom Bob" {
		t.Fatalf("live economy.ItemName = %q, want Cherry Blossom Bob", got)
	}

	// Owner sees actor in JSON export and on /items page; Helper does not see other staff identities on cards.
	exp := owner.get("/items/export.json")
	expect(t, exp, http.StatusOK)
	if !strings.Contains(exp.body, `"CUHA00001": "Cherry Blossom Bob"`) {
		t.Fatalf("export missing item: %s", exp.body)
	}
	if ownerPage := owner.get("/items?group=cosmetics&status=named"); !strings.Contains(ownerPage.body, "Cherry Blossom Bob") || !strings.Contains(ownerPage.body, "dev-vol") {
		t.Fatalf("owner filtered page should show name and actor identity")
	}
	// Owner edits an item; Helper must see the new name without seeing dev-owner anywhere on the page.
	expect(t, owner.post("/items/CUHA00001/name", url.Values{
		"csrf": {owner.csrf()}, "name": {"Owner Blossom Bob"},
	}), http.StatusSeeOther)
	if helperPage := helper.get("/items?group=cosmetics&status=named"); !strings.Contains(helperPage.body, "Owner Blossom Bob") || strings.Contains(helperPage.body, "dev-owner") {
		t.Fatalf("helper filtered page should show name without leaking other staff identities")
	}

	// No-JS form POST + redirect back with filter preserved.
	noJS := helper.post("/items/CUHA00001/name", url.Values{
		"csrf": {helper.csrf()}, "name": {"Sakura Bob"}, "return": {"/items?group=cosmetics"},
	})
	expect(t, noJS, http.StatusSeeOther)
	if loc := noJS.Header.Get("Location"); !strings.Contains(loc, "/items?") || !strings.Contains(loc, "group=cosmetics") || !strings.Contains(loc, "notice=item-name-saved") {
		t.Fatalf("no-JS redirect = %q", loc)
	}
	if got := economy.ItemName("CUHA00001"); got != "Sakura Bob" {
		t.Fatalf("after no-JS save economy.ItemName = %q", got)
	}

	// Invalid name returns JSON 400 when Accept: application/json.
	bad := helper.req(http.MethodPost, "/items/CUHA00001/name", url.Values{"csrf": {helper.csrf()}, "name": {"   "}})
	bad.Header.Set("Accept", "application/json")
	expect(t, helper.send(bad), http.StatusBadRequest)

	// Inline JSON reset restores default name.
	resReq := helper.req(http.MethodPost, "/items/CUHA00001/reset", url.Values{"csrf": {helper.csrf()}})
	resReq.Header.Set("Accept", "application/json")
	resResp := helper.send(resReq)
	expect(t, resResp, http.StatusOK)
	if got := economy.ItemName("CUHA00001"); got != "Hair 00001" {
		t.Fatalf("after reset economy.ItemName = %q, want Hair 00001", got)
	}

	// Audit trail captured the helper's renames and reset.
	if !hasAudit(auditRows(t), "dev-vol", "item-name", "Sakura Bob") {
		t.Error("missing item-name audit row")
	}
	if !hasAudit(auditRows(t), "dev-vol", "item-name-reset", "Sakura Bob") {
		t.Error("missing item-name-reset audit row")
	}

	// Built-in promoted item (data/item_names.json): counts as Named, excluded from status=unnamed & status=custom,
	// and export produces a valid strict item_names.json file with built-ins + overrides merged.
	restoreBuiltin := store.SetBuiltinItemNamesForTest(map[string]string{"CUHA00001": "Promoted Bob"})
	t.Cleanup(restoreBuiltin)

	if unnamed := helper.get("/items?group=cosmetics&status=unnamed"); strings.Contains(unnamed.body, "CUHA00001") {
		t.Fatal("built-in named item should not appear under status=unnamed")
	}
	if named := helper.get("/items?group=cosmetics&status=named"); !strings.Contains(named.body, "Promoted Bob") || !strings.Contains(named.body, "Built-in") {
		t.Fatal("built-in named item missing Built-in badge under status=named")
	}
	if customOnly := helper.get("/items?group=cosmetics&status=custom"); strings.Contains(customOnly.body, "CUHA00001") {
		t.Fatal("built-in item without override should not appear under status=custom")
	}

	// Export parses cleanly as a drop-in data/item_names.json file.
	exp2 := owner.get("/items/export.json")
	expect(t, exp2, http.StatusOK)
	parsedExport, err := store.ParseItemNamesFile([]byte(exp2.body))
	if err != nil {
		t.Fatalf("export.json is not valid data/item_names.json: %v", err)
	}
	if parsedExport.Names["CUHA00001"] != "Promoted Bob" {
		t.Fatalf("export missing built-in name: %+v", parsedExport.Names)
	}
}
