package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cherry/internal/store"
)

func TestUploadTotalCapFromSettings(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	c := fx.client()
	c.signInDev("owner")

	page := c.get("/settings").body
	for _, want := range []string{
		"Photo upload limits",
		"From the server's configuration: up to 100 MiB stored in total across diary, room and profile photos.",
		`action="/settings/upload-total-cap"`,
		`data-confirm="Save this total photo storage cap?`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Settings page lacks %q", want)
		}
	}
	if strings.Contains(page, `action="/settings/upload-total-cap/reset"`) {
		t.Fatal("reset offered with no panel value")
	}

	set := c.post("/settings/upload-total-cap", url.Values{"csrf": {c.csrf()}, "mib": {"50"}})
	expect(t, set, http.StatusSeeOther)
	if loc := set.Header.Get("Location"); loc != "/settings?notice=upload-total-cap-saved" {
		t.Fatalf("redirect to %q", loc)
	}
	after := c.follow(set)
	if !strings.Contains(after.body, "Set in this panel: up to 50 MiB stored in total across diary, room and profile photos.") ||
		!strings.Contains(after.body, "Use the server's configured value (100 MiB)") {
		t.Fatalf("Settings page after save missing 50 MiB or reset: %s", after.body)
	}

	for _, bad := range []string{"0", "0.5", "102401", "NaN", "Inf", "-5", "abc", ""} {
		expect(t, c.post("/settings/upload-total-cap", url.Values{"csrf": {c.csrf()}, "mib": {bad}}), http.StatusBadRequest)
	}
	if got := store.UploadMaxTotalBytes(0); got != 50<<20 {
		t.Fatalf("refused value changed cap to %d", got)
	}

	reset := c.post("/settings/upload-total-cap/reset", url.Values{"csrf": {c.csrf()}})
	expect(t, reset, http.StatusSeeOther)
	if loc := reset.Header.Get("Location"); loc != "/settings?notice=upload-total-cap-reset" {
		t.Fatalf("reset redirect to %q", loc)
	}
	if _, set, _ := store.UploadMaxTotalBytesSetting(); set {
		t.Fatal("reset left panel value")
	}

	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "setting", `"new":"52428800"`) || !hasAudit(rows, "dev-owner", "setting", `"new":"default"`) {
		t.Error("upload-total-cap changes lack owner audit rows")
	}
}

func TestUploadFileCapFromSettings(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	c := fx.client()
	c.signInDev("owner")

	page := c.get("/settings").body
	for _, want := range []string{
		"From the server's configuration: up to 1 MiB per uploaded photo.",
		`action="/settings/upload-file-cap"`,
		`data-confirm="Save this maximum upload filesize?`,
		"automatically compressed to fit",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Settings page lacks %q", want)
		}
	}
	if strings.Contains(page, `action="/settings/upload-file-cap/reset"`) {
		t.Fatal("reset offered with no panel value")
	}

	set := c.post("/settings/upload-file-cap", url.Values{"csrf": {c.csrf()}, "mib": {"0.5"}})
	expect(t, set, http.StatusSeeOther)
	if loc := set.Header.Get("Location"); loc != "/settings?notice=upload-file-cap-saved" {
		t.Fatalf("redirect to %q", loc)
	}
	after := c.follow(set)
	if !strings.Contains(after.body, "Set in this panel: up to 0.5 MiB per uploaded photo.") ||
		!strings.Contains(after.body, "Use the server's configured value (1 MiB)") {
		t.Fatal("Settings page after save missing 0.5 MiB or reset")
	}

	for _, bad := range []string{"0", "0.05", "100.1", "NaN", "Inf", "-1", "abc", ""} {
		expect(t, c.post("/settings/upload-file-cap", url.Values{"csrf": {c.csrf()}, "mib": {bad}}), http.StatusBadRequest)
	}
	if got := store.UploadMaxFileBytes(0); got != 512<<10 {
		t.Fatalf("refused value changed cap to %d", got)
	}

	reset := c.post("/settings/upload-file-cap/reset", url.Values{"csrf": {c.csrf()}})
	expect(t, reset, http.StatusSeeOther)
	if loc := reset.Header.Get("Location"); loc != "/settings?notice=upload-file-cap-reset" {
		t.Fatalf("reset redirect to %q", loc)
	}
	if _, set, _ := store.UploadMaxFileBytesSetting(); set {
		t.Fatal("reset left panel value")
	}

	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "setting", `"new":"524288"`) || !hasAudit(rows, "dev-owner", "setting", `"new":"default"`) {
		t.Error("upload-file-cap changes lack owner audit rows")
	}
}

func TestMaxPlayersFromSettings(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	c := fx.client()
	c.signInDev("owner")

	page := c.get("/settings").body
	for _, want := range []string{
		"Concurrent player cap",
		"From the server's configuration: at most 200 concurrent players may be logged in.",
		`action="/settings/max-players"`,
		`data-confirm="Save this concurrent player cap?`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Settings page lacks %q", want)
		}
	}
	if strings.Contains(page, `action="/settings/max-players/reset"`) {
		t.Fatal("reset offered with no panel value")
	}

	set := c.post("/settings/max-players", url.Values{"csrf": {c.csrf()}, "players": {"0"}})
	expect(t, set, http.StatusSeeOther)
	if loc := set.Header.Get("Location"); loc != "/settings?notice=max-players-saved" {
		t.Fatalf("redirect to %q", loc)
	}
	after := c.follow(set)
	if !strings.Contains(after.body, "Set in this panel: concurrent players are unlimited.") ||
		!strings.Contains(after.body, "Use the server's configured value (200 players)") {
		t.Fatal("Settings page after save missing unlimited or reset")
	}

	for _, bad := range []string{"-1", "10001", "abc", ""} {
		expect(t, c.post("/settings/max-players", url.Values{"csrf": {c.csrf()}, "players": {bad}}), http.StatusBadRequest)
	}
	if got := store.MaxPlayers(200); got != 0 {
		t.Fatalf("refused value changed cap to %d", got)
	}

	reset := c.post("/settings/max-players/reset", url.Values{"csrf": {c.csrf()}})
	expect(t, reset, http.StatusSeeOther)
	if loc := reset.Header.Get("Location"); loc != "/settings?notice=max-players-reset" {
		t.Fatalf("reset redirect to %q", loc)
	}
	if _, set, _ := store.MaxPlayersSetting(); set {
		t.Fatal("reset left panel value")
	}

	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "setting", `"new":"0"`) || !hasAudit(rows, "dev-owner", "setting", `"new":"default"`) {
		t.Error("max-players changes lack owner audit rows")
	}
}

func TestLimitsOwnerOnly(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	st := fx.client()
	st.signInDev("staff")
	csrf := st.csrf()

	for _, tc := range []struct {
		path string
		form url.Values
	}{
		{"/settings/upload-total-cap", url.Values{"csrf": {csrf}, "mib": {"50"}}},
		{"/settings/upload-total-cap/reset", url.Values{"csrf": {csrf}}},
		{"/settings/upload-file-cap", url.Values{"csrf": {csrf}, "mib": {"2"}}},
		{"/settings/upload-file-cap/reset", url.Values{"csrf": {csrf}}},
		{"/settings/max-players", url.Values{"csrf": {csrf}, "players": {"10"}}},
		{"/settings/max-players/reset", url.Values{"csrf": {csrf}}},
	} {
		expect(t, st.post(tc.path, tc.form), http.StatusForbidden)
	}
}
