package admin

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"cherry/internal/opdata"
	"cherry/internal/store"
)

// TestNoticeCodesAreFixedTexts: a known ?notice= code shows its fixed text; an unknown code, and any text that is not a
// code, shows nothing and is not echoed; every fixed text is plain (no markup to escape).
func TestNoticeCodesAreFixedTexts(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	r := c.get("/docs?notice=draft-saved")
	expect(t, r, http.StatusOK)
	if !strings.Contains(r.body, notices["draft-saved"]) || !strings.Contains(r.body, `class="flash ok"`) {
		t.Error("a known notice code does not show its text")
	}
	for _, query := range []string{"notice=" + url.QueryEscape("<script>x</script>"), "notice=" + url.QueryEscape("draft-saved<b>"), "notice=unknown-code", "notice="} {
		r := c.get("/docs?" + query)
		if strings.Contains(r.body, "<script>x") || strings.Contains(r.body, "unknown-code") || strings.Contains(r.body, `class="flash ok"`) {
			t.Errorf("?%s: a bad notice is shown", query)
		}
	}
	for code, text := range notices {
		if strings.ContainsAny(text, "<>&\"") || text == "" {
			t.Errorf("notice %s has markup or no text: %q", code, text)
		}
	}
}

// TestRemovalsAndBansRedirectWithANotice: each change of the player page redirects back to the page with the code of the
// change, and the code's text shows there.
func TestRemovalsAndBansRedirectWithANotice(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-fl1", "8801", "Flash", 0)
	base := playerURL(id)
	post := seedPost(t, "8801", "a post to remove", false)

	steps := []struct {
		target, code string
		form         url.Values
	}{
		{base + "/posts/" + post + "/remove", "post-removed", url.Values{"reason": {"spam"}}},
		{base + "/ban", "banned", url.Values{"reason": {"abuse"}}},
		{base + "/unban", "unbanned", nil},
	}
	for _, s := range steps {
		form := url.Values{"csrf": {c.csrf()}}
		for k, v := range s.form {
			form[k] = v
		}
		r := c.post(s.target, form)
		expect(t, r, http.StatusSeeOther)
		if loc := r.Header.Get("Location"); loc != base+"?notice="+s.code {
			t.Errorf("%s redirects to %q, want the %s notice", s.target, loc, s.code)
		}
		page := c.get(base + "?notice=" + s.code)
		if !strings.Contains(page.body, notices[s.code]) {
			t.Errorf("the %s notice does not show on the player page", s.code)
		}
	}
	if !hasAudit(auditRows(t), "dev-owner", "remove-post", "spam") {
		t.Error("the removal is not in the audit trail with its reason")
	}
}

// TestDestructiveFormsAskFirst: every removal, ban and roll-back form carries a data-confirm; the server still checks the
// POST, the attribute only asks.
func TestDestructiveFormsAskFirst(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-cf1", "8901", "Confirm", 0)
	seedPost(t, "8901", "to remove", false)
	page := c.get(playerURL(id)).body
	if n := strings.Count(page, `data-confirm="`); n < 2 {
		t.Errorf("the player page has %d confirmed forms, want the ban and the removal", n)
	}
	def, _ := opdata.Embedded("cherrytips.json")
	expect(t, c.post("/docs/cherrytips.json/draft", url.Values{"csrf": {c.csrf()}, "body": {string(def)}, "note": {"n"}}), http.StatusSeeOther)
	if !strings.Contains(c.get("/docs/cherrytips.json/1").body, `data-confirm="Discard cherrytips.json v1?`) {
		t.Error("the draft page does not confirm the discard")
	}
	expect(t, c.post("/docs/cherrytips.json/1/publish", url.Values{"csrf": {c.csrf()}}), http.StatusOK)
	if !strings.Contains(c.get("/docs/cherrytips.json/1").body, `data-confirm="Roll cherrytips.json back`) {
		t.Error("the version page does not confirm the roll-back")
	}
}

// topbarRe is the header of a signed-in page.
var topbarRe = regexp.MustCompile(`(?s)<header class="topbar">.*?</header>`)

// TestRoleBadgeShowsOwnerOrStaff: the owner is the owner; an allowlisted identity is staff; the header shows the identity
// masked as the logs mask it, never the full Discord id.
func TestRoleBadgeShowsOwnerOrStaff(t *testing.T) {
	fakeDiscord(t, new(int))
	fx := newFixture(t, discordConfig(t, friendID+"\n"))

	owner := fx.client()
	expect(t, owner.discordLogin("c|"+ownerID+"|1"), http.StatusOK)
	r := owner.get("/")
	if !strings.Contains(r.body, `<span class="role owner">Owner</span>`) {
		t.Error("the owner's header lacks the Owner badge")
	}
	header := topbarRe.FindString(r.body)
	if strings.Contains(header, ownerID) || !strings.Contains(header, "discord:…"+ownerID[len(ownerID)-4:]) {
		t.Errorf("the owner's identity is not masked in the header: %q", header)
	}

	staff := fx.client()
	expect(t, staff.discordLogin("c|"+friendID+"|1"), http.StatusOK)
	r = staff.get("/")
	if !strings.Contains(topbarRe.FindString(r.body), `<span class="role moderator">Moderator</span>`) {
		t.Errorf("the allowlisted header lacks the Moderator badge: %s", topbarRe.FindString(r.body))
	}
}

// TestNavMarksTheArea: the sidebar marks the entry of the area the path is in, and the sidebar holds only pages that exist.
func TestNavMarksTheArea(t *testing.T) {
	for path, want := range map[string]string{"/": "/", "/players/5": "/players", "/items": "/items", "/docs/x.json/3": "/docs", "/audit": "/audit"} {
		marked := ""
		for _, g := range navFor(path, roleOwner) {
			for _, l := range g.Links {
				if l.Current {
					marked = l.Href
				}
			}
		}
		if marked != want {
			t.Errorf("%s marks %q, want %q", path, marked, want)
		}
	}
	// Every sidebar link is a page this server serves: the link's own path answers the signed-in page, not a 404 (the
	// fixture's session is the dev owner, see the moderation tests for the pages' bodies).
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	for _, g := range navGroups {
		for _, l := range g.Links {
			if r := c.get(l.Href); r.StatusCode != http.StatusOK {
				t.Errorf("sidebar link %q (group %s) answers %d", l.Href, g.Title, r.StatusCode)
			}
		}
	}
	if !slices.ContainsFunc(navGroups, func(g navGroup) bool { return g.Title == "Moderation" }) {
		t.Error("the sidebar has no Moderation group")
	}
}
