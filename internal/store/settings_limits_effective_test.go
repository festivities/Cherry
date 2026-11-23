package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/config"
)

func limitsTestConfig() config.Config {
	return config.Config{
		ConnsPerAddress: 64, ConnsTotal: 1024, APIRate: 30, APIBurst: 400, AccountRate: 10, AccountBurst: 200,
		GuestPerMinute: 20, StaticRate: 200, StaticBurst: 5000, FrameRate: 100, FrameBurst: 400,
	}
}

// TestEffectiveLimitsConfigOverrideReset: without a database the configured set applies; a stored value replaces only its
// own key; a reset gives the configured value back; each change writes its audit row.
func TestEffectiveLimitsConfigOverrideReset(t *testing.T) {
	cfg := limitsTestConfig()
	if got := EffectiveLimits(cfg); got != ConfiguredLimits(cfg) {
		t.Fatalf("without a database = %+v, want the configured set", got)
	}
	if err := SetLimit(cfg, "limits.api-rate", 5, "dev-owner"); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("SetLimit without a database = %v, want ErrNoDatabase", err)
	}

	docStore(t)
	if got := EffectiveLimits(cfg); got != ConfiguredLimits(cfg) {
		t.Fatalf("unset = %+v, want the configured set", got)
	}
	if err := SetLimit(cfg, "limits.conns-per-address", 2000, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if err := SetLimit(cfg, "limits.frame-burst", 900, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	eff := EffectiveLimits(cfg)
	want := ConfiguredLimits(cfg)
	want.ConnsPerAddress, want.FrameBurst = 2000, 900
	if eff != want {
		t.Fatalf("with two overrides = %+v, want %+v", eff, want)
	}
	if eff.Value("limits.conns-per-address") != 2000 || eff.Value("limits.conns-total") != 1024 || eff.Value("nope") != 0 {
		t.Fatal("Value does not name the fields")
	}
	ov, err := LimitOverrides()
	if err != nil || len(ov) != 2 || ov["limits.conns-per-address"] != 2000 || ov["limits.frame-burst"] != 900 {
		t.Fatalf("LimitOverrides = %v, %v", ov, err)
	}

	if err := ClearLimit(cfg, "limits.conns-per-address", "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := EffectiveLimits(cfg).ConnsPerAddress; got != 64 {
		t.Fatalf("after reset = %d, want the configured 64", got)
	}
	if got := EffectiveLimits(cfg).FrameBurst; got != 900 {
		t.Fatalf("reset of one key changed another: frame burst = %d", got)
	}
	rows, err := AuditRows(0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var set, reset bool
	for _, r := range rows {
		if r.Action != "setting" || r.Target != "limits.conns-per-address" || r.Actor != "dev-owner" {
			continue
		}
		set = set || strings.Contains(r.Detail, `"new":"2000"`)
		reset = reset || strings.Contains(r.Detail, `"new":"default"`) && strings.Contains(r.Detail, `"old":"2000"`)
	}
	if !set || !reset {
		t.Fatalf("audit rows lack the save (%t) or the reset (%t)", set, reset)
	}
}

// TestSetLimitRefusesBadValuesAndBrokenPairs: out-of-range values, unknown keys and a rate above zero whose burst would be
// below one are ErrBadInput and change nothing, not even when the refused change is a reset.
func TestSetLimitRefusesBadValuesAndBrokenPairs(t *testing.T) {
	docStore(t)
	cfg := limitsTestConfig()
	cfg.APIRate, cfg.APIBurst = 0, 0 // configured off: a panel rate would be refused until a burst is set
	for _, tc := range []struct {
		key string
		n   int
	}{
		{"limits.conns-per-address", 0}, {"limits.conns-per-address", 10001}, {"limits.conns-total", 50001},
		{"limits.api-burst", 0}, {"limits.frame-rate", -1}, {"limits.guest-per-minute", 10001}, {"limits.static-rate", 100001},
		{"limits.max-players", 5},
	} {
		if err := SetLimit(cfg, tc.key, tc.n, "dev-owner"); !errors.Is(err, ErrBadInput) {
			t.Fatalf("SetLimit(%s, %d) = %v, want ErrBadInput", tc.key, tc.n, err)
		}
	}
	if err := SetLimit(cfg, "limits.api-rate", 10, "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("a rate with no burst = %v, want ErrBadInput", err)
	}
	if got := EffectiveLimits(cfg).APIRate; got != 0 {
		t.Fatalf("refused rate stored: %d", got)
	}

	if err := SetLimit(cfg, "limits.api-burst", 50, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if err := SetLimit(cfg, "limits.api-rate", 10, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	// Clearing the burst would leave rate 10 with the configured burst 0: refused, and the burst keeps its value.
	if err := ClearLimit(cfg, "limits.api-burst", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("reset that breaks the pair = %v, want ErrBadInput", err)
	}
	if got := EffectiveLimits(cfg).APIBurst; got != 50 {
		t.Fatalf("refused reset changed the burst to %d", got)
	}
	if err := ClearLimit(cfg, "limits.max-players", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("ClearLimit of an unknown key = %v, want ErrBadInput", err)
	}
}

// TestLimitOverrideSurvivesReopen: a stored panel value is read back by a process that opens the database again, which is
// what a restart does before it applies the limits.
func TestLimitOverrideSurvivesReopen(t *testing.T) {
	docStore(t)
	cfg := limitsTestConfig()
	if err := SetLimit(cfg, "limits.static-burst", 7000, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(filepath.Join(os.Getenv("LOCALAPPDATA"), DBFileName)); err != nil {
		t.Fatal(err)
	}
	if got := EffectiveLimits(cfg).StaticBurst; got != 7000 {
		t.Fatalf("after reopen = %d, want 7000", got)
	}
}
