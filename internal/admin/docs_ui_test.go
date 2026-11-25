package admin

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cherry/internal/opdata"
	"cherry/internal/store"
)

// TestDraftPageHasEditor: the draft page loads the editor from its own origin (src only) and keeps the raw textarea the
// server reads, with a "save and check" button beside the plain save; the other pages load no editor.
func TestDraftPageHasEditor(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	r := c.get("/docs/cherrytips.json")
	expect(t, r, http.StatusOK)
	if !strings.Contains(r.body, `<script src="/static/editor.js?v=`) {
		t.Error("the draft page does not load the editor from /static")
	}
	for _, want := range []string{`<textarea id="body-text" name="body"`, `data-json-editor`, `name="then" value="check"`, `id="draft"`, `id="history"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("draft page: no %s", want)
		}
	}
	checkPage(t, "draft page", r)
	if strings.Contains(c.get("/docs").body, "editor.js") || strings.Contains(c.get("/docs/cherrytips.json/1").body, "editor.js") {
		t.Error("a page without a draft form loads the editor")
	}
}

// TestFaviconRoutes: /favicon.ico redirects to the icon in the allowlist, which is an SVG; the sign-in page links it too.
func TestFaviconRoutes(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	r := c.get("/favicon.ico")
	expect(t, r, http.StatusFound)
	loc := r.Header.Get("Location")
	if loc != assetURL("favicon.svg") || !strings.HasPrefix(loc, "/static/favicon.svg?v=") {
		t.Fatalf("favicon redirects to %q", loc)
	}
	icon := c.get(loc)
	expect(t, icon, http.StatusOK)
	if ct := icon.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("icon content type %q", ct)
	}
	if !strings.Contains(icon.body, "<svg") || strings.Contains(strings.ToLower(icon.body), "<script") {
		t.Error("the icon is not a plain SVG")
	}
	if page := c.get("/auth/login?as=bad%20name").body; !strings.Contains(page, `<link rel="icon" href="`+assetURL("favicon.svg")+`"`) {
		t.Error("the pages do not link the icon")
	}
}

// TestSaveAndCheck: "save and check" on a refused draft keeps the draft and shows the validator's message verbatim in an
// error box beside the editor; a valid draft goes to its version page with the check notice.
func TestSaveAndCheck(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	bad := c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {"{}"}, "note": {"broken"}, "then": {"check"}})
	expect(t, bad, http.StatusUnprocessableEntity)
	verr := opdata.Validate("cherrytips.json", []byte("{}"))
	if verr == nil {
		t.Fatal("the empty document validates: the test needs a refused body")
	}
	want := template.HTMLEscapeString("document is invalid: " + verr.Error())
	if !strings.Contains(bad.body, `<div class="error-box" role="alert">`) || !strings.Contains(bad.body, `<pre class="error-msg">`+want+`</pre>`) {
		t.Errorf("the error box does not hold the message %q verbatim", want)
	}
	if !strings.Contains(bad.body, "Saved as draft v1, but it is not valid.") || !strings.Contains(bad.body, `<textarea id="body-text" name="body" rows="30" spellcheck="false" required data-json-editor>{}</textarea>`) {
		t.Error("the refused draft is not shown saved, with its text in the editor")
	}
	checkPage(t, "refused draft", bad)
	if versions, _ := store.History("cherrytips.json"); len(versions) != 1 {
		t.Fatalf("a refused draft: %d stored versions, want the draft kept", len(versions))
	}

	def, _ := opdata.Embedded("cherrytips.json")
	good := c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {string(def)}, "note": {"fine"}, "then": {"check"}})
	expect(t, good, http.StatusSeeOther)
	if loc := good.Header.Get("Location"); loc != "/docs/cherrytips.json/2?notice=draft-checked" {
		t.Fatalf("valid draft redirects to %q", loc)
	}
	if page := c.get("/docs/cherrytips.json/2?notice=draft-checked").body; !strings.Contains(page, notices["draft-checked"]) {
		t.Error("the version page does not show the check notice")
	}
}

// TestVersionPageShowsTheDiff: a version is compared with the live document (the built-in default here): the added and
// removed lines carry their classes and the summary counts them; an identical version says so; validate shows it too.
func TestVersionPageShowsTheDiff(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	def, _ := opdata.Embedded("cherrytips.json")
	changed := strings.Replace(string(def), `"title": "Daily quests"`, `"title": "Daily quest list"`, 1)
	if changed == string(def) {
		t.Fatal("the fixture title is not in the default: update the test")
	}
	expect(t, c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {changed}}), http.StatusSeeOther)

	page := c.get("/docs/cherrytips.json/1")
	expect(t, page, http.StatusOK)
	for _, want := range []string{
		`<tr class="add">`, `<tr class="del">`, template.HTMLEscapeString(`"title": "Daily quest list"`),
		`<span class="diff-add">+1</span> <span class="diff-del">&minus;1</span> lines against the built-in default (nothing is published)`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("version page: no %s", want)
		}
	}
	checkPage(t, "version page", page)

	validated := c.post("/docs/cherrytips.json/1/validate", url.Values{"csrf": {c.csrf()}})
	expect(t, validated, http.StatusOK)
	if !strings.Contains(validated.body, "Valid.") || !strings.Contains(validated.body, `<tr class="add">`) {
		t.Error("after validate: no check result or no diff")
	}

	expect(t, c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {string(def)}}), http.StatusSeeOther)
	same := c.get("/docs/cherrytips.json/2").body
	if !strings.Contains(same, "This version reads the same as the built-in default (nothing is published).") {
		t.Error("an identical version does not say so")
	}
}

// TestVersionPageButtonsByState: a version shows only the actions its state allows. A draft has Publish and Discard, no
// Roll back; a published version has Roll back only; a retired one has Publish, no Roll back. Discard deletes the draft,
// and the store refuses it for the published and retired versions.
func TestVersionPageButtonsByState(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	def, _ := opdata.Embedded("cherrytips.json")
	const doc = "/docs/cherrytips.json"
	form := func(extra url.Values) url.Values {
		if extra == nil {
			extra = url.Values{}
		}
		extra.Set("csrf", c.csrf())
		return extra
	}
	has := func(body, action string) bool { return strings.Contains(body, `action="`+action+`"`) }

	expect(t, c.post(doc+"/draft", form(url.Values{"body": {string(def)}})), http.StatusSeeOther) // v1
	expect(t, c.post(doc+"/draft", form(url.Values{"body": {string(def)}})), http.StatusSeeOther) // v2
	draft := c.get(doc + "/1").body
	if !has(draft, doc+"/1/discard") || !has(draft, doc+"/1/publish") || has(draft, doc+"/rollback") {
		t.Error("draft page: want Publish and Discard, no Roll back")
	}
	expect(t, c.post(doc+"/1/publish", form(nil)), http.StatusOK)
	expect(t, c.post(doc+"/2/publish", form(nil)), http.StatusOK) // v1 becomes retired

	published := c.get(doc + "/2").body
	if !has(published, doc+"/rollback") || has(published, doc+"/2/publish") || has(published, doc+"/2/discard") {
		t.Error("published page: want Roll back only")
	}
	retired := c.get(doc + "/1").body
	if !has(retired, doc+"/1/publish") || has(retired, doc+"/rollback") || has(retired, doc+"/1/discard") {
		t.Error("retired page: want Publish, no Roll back, no Discard")
	}

	expect(t, c.post(doc+"/1/discard", form(nil)), http.StatusConflict)
	expect(t, c.post(doc+"/2/discard", form(nil)), http.StatusConflict)
	draftNew := c.post(doc+"/draft", form(url.Values{"body": {string(def)}}))
	expect(t, draftNew, http.StatusSeeOther) // v3
	discarded := c.post(doc+"/3/discard", form(nil))
	expect(t, discarded, http.StatusSeeOther)
	if loc := discarded.Header.Get("Location"); loc != doc+"?notice=draft-discarded" {
		t.Fatalf("discard redirects to %q", loc)
	}
	expect(t, c.get(doc+"/3"), http.StatusNotFound)
	if page := c.get(doc + "?notice=draft-discarded").body; !strings.Contains(page, notices["draft-discarded"]) {
		t.Error("the history page does not show the discard notice")
	}
	if !hasAudit(auditRows(t), "dev-owner", "doc-discard", "") {
		t.Error("the discard is not in the audit trail with its actor")
	}
}

// TestDocsListFields: each document shows its live version, when and by whom it was published, a newer draft, whether it
// has art checks, its origin and the first line of its note, and links to Edit and History.
func TestDocsListFields(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	before := c.get("/docs").body
	for _, want := range []string{
		`href="/docs/cherrytips.json#draft">Edit</a>`, `href="/docs/cherrytips.json#history">History</a>`,
		`<span class="pill">cherry</span>`, "Cherry-made tips about Cherry", `<span class="muted">none</span>`,
	} {
		if !strings.Contains(before, want) {
			t.Errorf("docs list before any draft: no %s", want)
		}
	}
	if strings.Contains(before, `class="pill warn">v1</span>`) {
		t.Error("docs list: a newer draft is shown with none saved")
	}

	def, _ := opdata.Embedded("cherrytips.json")
	expect(t, c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {string(def)}}), http.StatusSeeOther)
	if !strings.Contains(c.get("/docs").body, `<span class="pill warn">v1</span>`) {
		t.Error("docs list: the newer draft v1 is not shown")
	}
	expect(t, c.post("/docs/cherrytips.json/1/publish", url.Values{"csrf": {c.csrf()}}), http.StatusOK)

	after := c.get("/docs").body
	if !strings.Contains(after, "<td>v1</td>") {
		t.Error("docs list: the live version v1 is not shown")
	}
	if !strings.Contains(after, "<td>dev-owner</td>") {
		t.Error("docs list: the publisher is not shown")
	}
	if strings.Contains(after, `<span class="pill warn">v1</span>`) {
		t.Error("docs list: the published version is still a newer draft")
	}
	live := liveOf(mustLive(t), "cherrytips.json")
	if live.PublishedAt == "" || live.PublishedBy != "dev-owner" || live.Draft != 0 || live.ArtChecks != opdata.HasArtChecks("cherrytips.json") {
		t.Errorf("live row: at %q by %q draft %d artChecks %t", live.PublishedAt, live.PublishedBy, live.Draft, live.ArtChecks)
	}
}

// mustLive is store.Live for a test, failing on error.
func mustLive(t *testing.T) []store.DocLive {
	t.Helper()
	docs, err := store.Live()
	if err != nil {
		t.Fatal(err)
	}
	return docs
}
