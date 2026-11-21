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

func TestUploadReceiveCapFromSettings(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	c := fx.client()
	c.signInDev("owner")

	page := c.get("/settings").body
	for _, want := range []string{
		"From the server's configuration: up to 20 MiB accepted per upload before compression.",
		`action="/settings/upload-receive-cap"`,
		`data-confirm="Save this maximum upload receive size?`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Settings page lacks %q", want)
		}
	}
	if strings.Contains(page, `action="/settings/upload-receive-cap/reset"`) {
		t.Fatal("reset offered with no panel value")
	}

	set := c.post("/settings/upload-receive-cap", url.Values{"csrf": {c.csrf()}, "mib": {"10"}})
	expect(t, set, http.StatusSeeOther)
	if loc := set.Header.Get("Location"); loc != "/settings?notice=upload-receive-cap-saved" {
		t.Fatalf("redirect to %q", loc)
	}
	after := c.follow(set)
	if !strings.Contains(after.body, "Set in this panel: up to 10 MiB accepted per upload before compression.") ||
		!strings.Contains(after.body, "Use the server's configured value (20 MiB)") {
		t.Fatalf("Settings page after save missing 10 MiB or reset")
	}

	for _, bad := range []string{"0", "0.5", "100.1", "NaN", "Inf", "-1", "abc", ""} {
		expect(t, c.post("/settings/upload-receive-cap", url.Values{"csrf": {c.csrf()}, "mib": {bad}}), http.StatusBadRequest)
	}
	if got := store.UploadMaxReceiveBytes(0); got != 10<<20 {
		t.Fatalf("refused value changed cap to %d", got)
	}

	reset := c.post("/settings/upload-receive-cap/reset", url.Values{"csrf": {c.csrf()}})
	expect(t, reset, http.StatusSeeOther)
	if loc := reset.Header.Get("Location"); loc != "/settings?notice=upload-receive-cap-reset" {
		t.Fatalf("reset redirect to %q", loc)
	}
	if _, set, _ := store.UploadMaxReceiveBytesSetting(); set {
		t.Fatal("reset left panel value")
	}

	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "setting", `"new":"10485760"`) || !hasAudit(rows, "dev-owner", "setting", `"new":"default"`) {
		t.Error("upload-receive-cap changes lack owner audit rows")
	}
}

func TestUploadJPEGAndPNGCapsFromSettings(t *testing.T) {
	fx := newFixture(t, settingsConfig(t))
	c := fx.client()
	c.signInDev("owner")

	page := c.get("/settings").body
	for _, want := range []string{
		"From the server's configuration: up to 150 KiB per stored JPEG photo.",
		`action="/settings/upload-jpeg-cap"`,
		"From the server's configuration: up to 150 KiB per stored transparent PNG photo.",
		`action="/settings/upload-png-cap"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Settings page lacks %q", want)
		}
	}
	if strings.Contains(page, `action="/settings/upload-jpeg-cap/reset"`) || strings.Contains(page, `action="/settings/upload-png-cap/reset"`) {
		t.Fatal("reset offered with no panel value")
	}

	// Save JPEG cap (80 KiB)
	setJPEG := c.post("/settings/upload-jpeg-cap", url.Values{"csrf": {c.csrf()}, "kib": {"80"}})
	expect(t, setJPEG, http.StatusSeeOther)
	if loc := setJPEG.Header.Get("Location"); loc != "/settings?notice=upload-jpeg-cap-saved" {
		t.Fatalf("jpeg redirect to %q", loc)
	}
	afterJPEG := c.follow(setJPEG)
	if !strings.Contains(afterJPEG.body, "Set in this panel: up to 80 KiB per stored JPEG photo.") ||
		!strings.Contains(afterJPEG.body, "Use the server's configured value (150 KiB)") {
		t.Fatalf("Settings page after jpeg save missing 80 KiB or reset")
	}

	// Save PNG cap (250 KiB)
	setPNG := c.post("/settings/upload-png-cap", url.Values{"csrf": {c.csrf()}, "kib": {"250"}})
	expect(t, setPNG, http.StatusSeeOther)
	if loc := setPNG.Header.Get("Location"); loc != "/settings?notice=upload-png-cap-saved" {
		t.Fatalf("png redirect to %q", loc)
	}
	afterPNG := c.follow(setPNG)
	if !strings.Contains(afterPNG.body, "Set in this panel: up to 250 KiB per stored transparent PNG photo.") ||
		!strings.Contains(afterPNG.body, "Use the server's configured value (150 KiB)") {
		t.Fatalf("Settings page after png save missing 250 KiB or reset")
	}

	for _, bad := range []string{"0", "15", "102401", "NaN", "Inf", "-1", "abc", ""} {
		expect(t, c.post("/settings/upload-jpeg-cap", url.Values{"csrf": {c.csrf()}, "kib": {bad}}), http.StatusBadRequest)
		expect(t, c.post("/settings/upload-png-cap", url.Values{"csrf": {c.csrf()}, "kib": {bad}}), http.StatusBadRequest)
	}
	if got := store.UploadMaxJPEGBytes(0); got != 80<<10 {
		t.Fatalf("refused value changed jpeg cap to %d", got)
	}
	if got := store.UploadMaxPNGBytes(0); got != 250<<10 {
		t.Fatalf("refused value changed png cap to %d", got)
	}

	resetJPEG := c.post("/settings/upload-jpeg-cap/reset", url.Values{"csrf": {c.csrf()}})
	expect(t, resetJPEG, http.StatusSeeOther)
	if loc := resetJPEG.Header.Get("Location"); loc != "/settings?notice=upload-jpeg-cap-reset" {
		t.Fatalf("jpeg reset redirect to %q", loc)
	}
	if _, set, _ := store.UploadMaxJPEGBytesSetting(); set {
		t.Fatal("jpeg reset left panel value")
	}

	resetPNG := c.post("/settings/upload-png-cap/reset", url.Values{"csrf": {c.csrf()}})
	expect(t, resetPNG, http.StatusSeeOther)
	if loc := resetPNG.Header.Get("Location"); loc != "/settings?notice=upload-png-cap-reset" {
		t.Fatalf("png reset redirect to %q", loc)
	}
	if _, set, _ := store.UploadMaxPNGBytesSetting(); set {
		t.Fatal("png reset left panel value")
	}

	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "setting", `"new":"81920"`) ||
		!hasAudit(rows, "dev-owner", "setting", `"new":"256000"`) ||
		!hasAudit(rows, "dev-owner", "setting", `"new":"default"`) {
		t.Error("upload-jpeg-cap / upload-png-cap changes lack owner audit rows")
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
		{"/settings/upload-receive-cap", url.Values{"csrf": {csrf}, "mib": {"10"}}},
		{"/settings/upload-receive-cap/reset", url.Values{"csrf": {csrf}}},
		{"/settings/upload-jpeg-cap", url.Values{"csrf": {csrf}, "kib": {"100"}}},
		{"/settings/upload-jpeg-cap/reset", url.Values{"csrf": {csrf}}},
		{"/settings/upload-png-cap", url.Values{"csrf": {csrf}, "kib": {"100"}}},
		{"/settings/upload-png-cap/reset", url.Values{"csrf": {csrf}}},
		{"/settings/max-players", url.Values{"csrf": {csrf}, "players": {"10"}}},
		{"/settings/max-players/reset", url.Values{"csrf": {csrf}}},
	} {
		expect(t, st.post(tc.path, tc.form), http.StatusForbidden)
	}
}
