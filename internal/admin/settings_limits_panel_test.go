package admin

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/config"
	"cherry/internal/httpx"
	"cherry/internal/store"
	"cherry/internal/web"
)

// limitsConfig is the owner's configuration for the Server limits table: the lab's identity-mode values.
func limitsConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := settingsConfig(t)
	cfg.ConnsPerAddress, cfg.ConnsTotal = 64, 1024
	cfg.APIRate, cfg.APIBurst, cfg.AccountRate, cfg.AccountBurst = 30, 400, 10, 200
	cfg.GuestPerMinute = 20
	cfg.StaticRate, cfg.StaticBurst, cfg.FrameRate, cfg.FrameBurst = 200, 5000, 100, 400
	return cfg
}

// restoreConns puts the process's connection caps back when the test ends.
func restoreConns(t *testing.T) {
	t.Helper()
	perHost, total := httpx.Conns.Caps()
	t.Cleanup(func() { httpx.Conns.Set(perHost, total) })
}

// TestServerLimitsFromSettings: the table shows the configured, panel and effective values; a save changes the connection cap
// the listeners use at once and is audited; a bad value or a broken pair changes nothing; a reset gives the configured value
// back.
func TestServerLimitsFromSettings(t *testing.T) {
	restoreConns(t)
	cfg := limitsConfig(t)
	fx := newFixture(t, cfg)
	c := fx.client()
	c.signInDev("owner")
	httpx.Conns.Set(cfg.ConnsPerAddress, cfg.ConnsTotal)

	page := c.get("/settings").body
	for _, want := range []string{
		"Server limits",
		"Connections per address",
		"Open game connections now: <strong>0</strong> from <strong>0</strong> addresses",
		`action="/settings/limit"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Settings page lacks %q", want)
		}
	}
	if strings.Contains(page, `action="/settings/limit/reset"`) {
		t.Fatal("reset offered with no panel value")
	}

	set := c.post("/settings/limit", url.Values{"csrf": {c.csrf()}, "key": {"limits.conns-per-address"}, "value": {"200"}})
	expect(t, set, http.StatusSeeOther)
	if loc := set.Header.Get("Location"); loc != "/settings?notice=limit-saved" {
		t.Fatalf("redirect to %q", loc)
	}
	if perHost, total := httpx.Conns.Caps(); perHost != 200 || total != 1024 {
		t.Fatalf("connection caps after the save = %d, %d; want 200, 1024", perHost, total)
	}
	if got := store.EffectiveLimits(cfg).ConnsPerAddress; got != 200 {
		t.Fatalf("effective per-address cap = %d, want 200", got)
	}
	after := c.follow(set)
	if !strings.Contains(after.body, "Set in this panel") || !strings.Contains(after.body, `action="/settings/limit/reset"`) {
		t.Fatal("Settings page after the save lacks the panel value or the reset")
	}

	// A frame rate saves too: its configured burst (400) stays, so the pair check passes.
	expect(t, c.post("/settings/limit", url.Values{"csrf": {c.csrf()}, "key": {"limits.frame-rate"}, "value": {"250"}}), http.StatusSeeOther)
	if got := store.EffectiveLimits(cfg).FrameRate; got != 250 {
		t.Fatalf("effective frame rate = %d, want 250", got)
	}

	for _, tc := range []struct{ key, value string }{
		{"limits.conns-per-address", "0"}, {"limits.conns-per-address", "10001"}, {"limits.conns-per-address", "abc"},
		{"limits.conns-per-address", ""}, {"limits.conns-total", "12001"}, {"limits.api-burst", "0"}, {"limits.static-rate", "100001"},
		{"limits.guest-per-minute", "-1"}, {"limits.not-a-key", "5"},
	} {
		expect(t, c.post("/settings/limit", url.Values{"csrf": {c.csrf()}, "key": {tc.key}, "value": {tc.value}}), http.StatusBadRequest)
	}
	if perHost, _ := httpx.Conns.Caps(); perHost != 200 {
		t.Fatalf("refused values changed the per-address cap to %d", perHost)
	}

	reset := c.post("/settings/limit/reset", url.Values{"csrf": {c.csrf()}, "key": {"limits.conns-per-address"}})
	expect(t, reset, http.StatusSeeOther)
	if loc := reset.Header.Get("Location"); loc != "/settings?notice=limit-reset" {
		t.Fatalf("reset redirect to %q", loc)
	}
	if perHost, _ := httpx.Conns.Caps(); perHost != 64 {
		t.Fatalf("reset left the per-address cap at %d, want the configured 64", perHost)
	}
	if got := store.EffectiveLimits(cfg).ConnsPerAddress; got != 64 {
		t.Fatalf("effective per-address cap after reset = %d, want 64", got)
	}

	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "setting", `"new":"200"`) || !hasAudit(rows, "dev-owner", "setting", `"new":"default"`) {
		t.Error("server limit changes lack owner audit rows")
	}
}

// TestServerLimitsRefuseBrokenPair: a rate above zero whose burst is configured to 0 is refused, the stored value stays out,
// and the table names no value that was not saved.
func TestServerLimitsRefuseBrokenPair(t *testing.T) {
	restoreConns(t)
	cfg := limitsConfig(t)
	cfg.APIRate, cfg.APIBurst = 0, 0
	fx := newFixture(t, cfg)
	c := fx.client()
	c.signInDev("owner")

	refused := c.post("/settings/limit", url.Values{"csrf": {c.csrf()}, "key": {"limits.api-rate"}, "value": {"10"}})
	expect(t, refused, http.StatusBadRequest)
	if got := store.EffectiveLimits(cfg).APIRate; got != 0 {
		t.Fatalf("refused rate is in effect: %d", got)
	}
	expect(t, c.post("/settings/limit", url.Values{"csrf": {c.csrf()}, "key": {"limits.api-burst"}, "value": {"50"}}), http.StatusSeeOther)
	expect(t, c.post("/settings/limit", url.Values{"csrf": {c.csrf()}, "key": {"limits.api-rate"}, "value": {"10"}}), http.StatusSeeOther)
	// Clearing the burst would leave the rate at 10 with no burst: refused, and the burst keeps its panel value.
	expect(t, c.post("/settings/limit/reset", url.Values{"csrf": {c.csrf()}, "key": {"limits.api-burst"}}), http.StatusBadRequest)
	if got := store.EffectiveLimits(cfg).APIBurst; got != 50 {
		t.Fatalf("refused reset changed the burst to %d", got)
	}
}

// TestServerLimitsSurviveRestart: a stored value is applied again by a process that starts with the same configuration: the
// database is opened again, the connection caps start at the configured values and ApplyLimits (what cmd/cherry calls after
// the database opens) brings the stored value back.
func TestServerLimitsSurviveRestart(t *testing.T) {
	restoreConns(t)
	cfg := limitsConfig(t)
	fx := newFixture(t, cfg)
	c := fx.client()
	c.signInDev("owner")
	expect(t, c.post("/settings/limit", url.Values{"csrf": {c.csrf()}, "key": {"limits.conns-total"}, "value": {"3000"}}), http.StatusSeeOther)

	if err := store.CloseStore(); err != nil {
		t.Fatal(err)
	}
	if err := store.OpenStore(filepath.Join(os.Getenv("LOCALAPPDATA"), store.DBFileName)); err != nil {
		t.Fatal(err)
	}
	httpx.Conns.Set(cfg.ConnsPerAddress, cfg.ConnsTotal) // a new process starts from the configuration
	web.ApplyLimits(store.EffectiveLimits(cfg))
	if _, total := httpx.Conns.Caps(); total != 3000 {
		t.Fatalf("total cap after restart = %d, want the stored 3000", total)
	}
}

// TestServerLimitsOwnerOnly: a staff member who is not the owner is refused on the save and the reset, and nothing changes.
func TestServerLimitsOwnerOnly(t *testing.T) {
	restoreConns(t)
	cfg := limitsConfig(t)
	fx := newFixture(t, cfg)
	st := fx.client()
	st.signInDev("staff")
	csrf := st.csrf()
	httpx.Conns.Set(cfg.ConnsPerAddress, cfg.ConnsTotal)

	expect(t, st.post("/settings/limit", url.Values{"csrf": {csrf}, "key": {"limits.conns-per-address"}, "value": {"10"}}), http.StatusForbidden)
	expect(t, st.post("/settings/limit/reset", url.Values{"csrf": {csrf}, "key": {"limits.conns-per-address"}}), http.StatusForbidden)
	if perHost, _ := httpx.Conns.Caps(); perHost != 64 {
		t.Fatalf("a refused change moved the cap to %d", perHost)
	}
}
