package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

func TestParseDefaultRoute(t *testing.T) {
	route := strings.Join([]string{
		"Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT",
		"eth1\t00000000\t0100000A\t0002\t0\t0\t10\t00000000\t0\t0\t0",   // down (0x1 not set)
		"enp0s6\t0000FEA9\t00000000\t0001\t0\t0\t50\t0000FFFF\t0\t0\t0", // non-default
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t200\t00000000\t0\t0\t0",
		"enp0s6\t00000000\t0100000A\t0003\t0\t0\t100\t00000000\t0\t0\t0", // up + lowest metric
	}, "\n")

	if got := parseDefaultRoute([]byte(route)); got != "enp0s6" {
		t.Fatalf("parseDefaultRoute = %q, want %q", got, "enp0s6")
	}

	noDefault := strings.Join([]string{
		"Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT",
		"enp0s6\t0000FEA9\t00000000\t0001\t0\t0\t50\t0000FFFF\t0\t0\t0",
		"eth0\t00000000\t0101A8C0\t0002\t0\t0\t100\t00000000\t0\t0\t0",
	}, "\n")
	if got := parseDefaultRoute([]byte(noDefault)); got != "" {
		t.Fatalf("parseDefaultRoute (no active default) = %q, want empty", got)
	}
}

func TestParseProcNetDevTx(t *testing.T) {
	dev := strings.Join([]string{
		"Inter-|   Receive                                                |  Transmit",
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed",
		"    lo: 12345     100    0    0    0     0          0         0    12345     100    0    0    0     0       0          0",
		"enp0s6:9876543210  50000   0    0    0     0          0         0 112233445566  60000   0    0    0     0       0          0",
	}, "\n")

	tx, ok := parseProcNetDevTx([]byte(dev), "enp0s6")
	if !ok || tx != 112233445566 {
		t.Fatalf("parseProcNetDevTx(enp0s6) = (%d, %v), want (112233445566, true)", tx, ok)
	}
	txLo, okLo := parseProcNetDevTx([]byte(dev), "lo")
	if !okLo || txLo != 12345 {
		t.Fatalf("parseProcNetDevTx(lo) = (%d, %v), want (12345, true)", txLo, okLo)
	}
	if _, okMissing := parseProcNetDevTx([]byte(dev), "eth9"); okMissing {
		t.Fatal("parseProcNetDevTx(eth9) = true, want false")
	}
}

func TestBandwidthTrackerSamplingAndRolling24h(t *testing.T) {
	var curTx uint64 = 1_000_000
	curIface := "enp0s6"
	var savedRaw []byte
	saveCount := 0

	bt := newBandwidthTracker(
		func() (uint64, string, bool) { return curTx, curIface, true },
		func() ([]byte, error) { return nil, nil },
		func(raw []byte) error {
			savedRaw = append([]byte(nil), raw...)
			saveCount++
			return nil
		},
	)

	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	bt.sample(t0)
	st := bt.snapshot(t0)
	if !st.BWAvailable || st.BWIface != "enp0s6" {
		t.Fatalf("snapshot after 1st sample = %+v", st)
	}
	if st.BWRate != "warming up" {
		t.Fatalf("rate after 1st sample = %q, want %q", st.BWRate, "warming up")
	}

	// 10s later, 29.1 MB sent -> 2.91 MB/s (23.3 Mbit/s).
	curTx += 29_100_000
	bt.sample(t0.Add(10 * time.Second))
	st = bt.snapshot(t0.Add(10 * time.Second))
	if st.BWRate != "2.91 MB/s (23.3 Mbit/s)" {
		t.Fatalf("rate = %q, want %q", st.BWRate, "2.91 MB/s (23.3 Mbit/s)")
	}

	// Advance 3h30m total and add 42_070_900_000 more -> 42.1 GB total in 3.5h.
	curTx += 42_070_900_000
	t35 := t0.Add(3*time.Hour + 30*time.Minute)
	bt.sample(t35)
	st = bt.snapshot(t35)
	if !strings.HasPrefix(st.BW24h, "42.1 GB in 3.5h (3.34 MB/s avg)") {
		t.Fatalf("bw_24h = %q, want prefix %q", st.BW24h, "42.1 GB in 3.5h (3.34 MB/s avg)")
	}
	if saveCount != 1 || len(savedRaw) == 0 {
		t.Fatalf("saveCount = %d, len(savedRaw) = %d; want 1 flush after 5m", saveCount, len(savedRaw))
	}

	// Counter reset (host reboot) or interface switch must not add a negative/bogus delta.
	curTx = 500
	bt.sample(t35.Add(10 * time.Second))
	st = bt.snapshot(t35.Add(10 * time.Second))
	if st.BWRate != "warming up" {
		t.Fatalf("rate right after counter reset = %q, want %q", st.BWRate, "warming up")
	}
	if bt.days["2026-11-10"] != 42_100_000_000 {
		t.Fatalf("today bytes after counter reset = %d, want 42100000000", bt.days["2026-11-10"])
	}

	// Interface switch + new traffic.
	curIface = "eth0"
	curTx = 10_000
	bt.sample(t35.Add(20 * time.Second))
	curTx = 20_000
	bt.sample(t35.Add(30 * time.Second))
	if bt.days["2026-11-10"] != 42_100_010_000 {
		t.Fatalf("today bytes after iface switch = %d, want 42100010000", bt.days["2026-11-10"])
	}

	// Full 24h rollover drops buckets older than 24h.
	t24 := t0.Add(24 * time.Hour)
	curTx += 1_000_000_000
	bt.sample(t24)
	st = bt.snapshot(t24)
	// Slot at t0 (00:00) was overwritten at t24 (24:00), and buckets from t35 (03:30) remain:
	// 42_070_900_000 + 10_000 + 1_000_000_000 = 43_070_910_000 -> 43.1 GB.
	if !strings.HasPrefix(st.BW24h, "43.1 GB (") {
		t.Fatalf("bw_24h after 24h rollover = %q, want prefix %q", st.BW24h, "43.1 GB (")
	}
}

func TestBandwidthRestartGapAndPruning(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	days := map[string]int64{}
	for i := 1; i <= 70; i++ {
		d := t0.AddDate(0, 0, -i).Format("2006-01-02")
		days[d] = int64(i) * 1_000_000
	}
	persisted, err := json.Marshal(bwPersisted{
		Iface:  "enp0s6",
		LastTx: 50_000_000_000,
		LastAt: t0.Add(-15 * time.Minute).Unix(),
		Days:   days,
	})
	if err != nil {
		t.Fatal(err)
	}

	var flushed []byte
	bt := newBandwidthTracker(
		func() (uint64, string, bool) { return 52_500_000_000, "enp0s6", true },
		func() ([]byte, error) { return persisted, nil },
		func(raw []byte) error {
			flushed = append([]byte(nil), raw...)
			return nil
		},
	)

	bt.sample(t0)
	if got := bt.days["2026-11-15"]; got != 2_500_000_000 {
		t.Fatalf("restart gap today = %d, want 2500000000", got)
	}
	if len(bt.days) != bwKeepDays {
		t.Fatalf("len(days) = %d, want %d after pruning", len(bt.days), bwKeepDays)
	}
	oldestKept := t0.AddDate(0, 0, -(bwKeepDays - 1)).Format("2006-01-02")
	oldestDropped := t0.AddDate(0, 0, -bwKeepDays).Format("2006-01-02")
	if _, ok := bt.days[oldestKept]; !ok {
		t.Errorf("expected %s to be kept", oldestKept)
	}
	if _, ok := bt.days[oldestDropped]; ok {
		t.Errorf("expected %s to be pruned", oldestDropped)
	}

	bt.flush(t0)
	var out bwPersisted
	if err := json.Unmarshal(flushed, &out); err != nil {
		t.Fatal(err)
	}
	if out.Iface != "enp0s6" || out.LastTx != 52_500_000_000 || len(out.Days) != bwKeepDays {
		t.Fatalf("flushed state unexpected: iface=%q lastTx=%d days=%d", out.Iface, out.LastTx, len(out.Days))
	}
}

func TestBandwidthMonthlyProjections(t *testing.T) {
	makeTracker := func(days map[string]int64, curTx uint64) *bandwidthTracker {
		raw, _ := json.Marshal(bwPersisted{
			Iface:  "enp0s6",
			LastTx: curTx,
			LastAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Unix(),
			Days:   days,
		})
		return newBandwidthTracker(
			func() (uint64, string, bool) { return curTx, "enp0s6", true },
			func() ([]byte, error) { return raw, nil },
			func([]byte) error { return nil },
		)
	}

	// 1) <3 full days, within 10 TB pace.
	nowEarly := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	btUnder := makeTracker(map[string]int64{
		"2026-11-01": 100_000_000_000,
		"2026-11-02": 100_000_000_000,
	}, 1000)
	btUnder.sample(nowEarly)
	st := btUnder.snapshot(nowEarly)
	if !strings.Contains(st.BWForecast, "Collecting daily trend (2/3 full days); current pace is within 10 TB/mo") {
		t.Fatalf("<3d under pace forecast = %q", st.BWForecast)
	}

	// 2) <3 full days, exceeding 10 TB pace (500 GB/day -> ~15 TB/mo).
	btOver := makeTracker(map[string]int64{
		"2026-11-01": 500_000_000_000,
		"2026-11-02": 500_000_000_000,
	}, 1000)
	btOver.sample(nowEarly)
	st = btOver.snapshot(nowEarly)
	if !strings.Contains(st.BWForecast, "Collecting daily trend (2/3 full days); current pace EXCEEDS 10 TB/mo") {
		t.Fatalf("<3d over pace forecast = %q", st.BWForecast)
	}

	// 3) >=3 full days, growing and exceeding 10 TB (check exceed-day estimate).
	// Nov 1..10 growing +30 GB/day from 180 GB to 450 GB -> crosses 10 TB late in Nov.
	nowMid := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	growOver := map[string]int64{}
	for d := 1; d <= 10; d++ {
		growOver[time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")] = int64(150+d*30) * 1_000_000_000
	}
	btGrowOver := makeTracker(growOver, 1000)
	btGrowOver.sample(nowMid)
	st = btGrowOver.snapshot(nowMid)
	if !strings.HasPrefix(st.BWForecast, "Growing +30.0 GB/day — projected ") ||
		!strings.Contains(st.BWForecast, "EXCEEDS 10 TB around day ") {
		t.Fatalf(">=3d growing over forecast = %q", st.BWForecast)
	}

	// 4) >=3 full days, growing but staying under 10 TB.
	growUnder := map[string]int64{}
	for d := 1; d <= 10; d++ {
		growUnder[time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")] = int64(50+d*5) * 1_000_000_000
	}
	btGrowUnder := makeTracker(growUnder, 1000)
	btGrowUnder.sample(nowMid)
	st = btGrowUnder.snapshot(nowMid)
	if !strings.HasPrefix(st.BWForecast, "Growing +5.00 GB/day — projected ") ||
		!strings.Contains(st.BWForecast, "(within 10 TB)") {
		t.Fatalf(">=3d growing under forecast = %q", st.BWForecast)
	}

	// 5) >=3 full days, falling.
	falling := map[string]int64{
		"2026-11-01": 300_000_000_000,
		"2026-11-02": 290_000_000_000,
		"2026-11-03": 280_000_000_000,
		"2026-11-04": 270_000_000_000,
	}
	nowFall := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	btFall := makeTracker(falling, 1000)
	btFall.sample(nowFall)
	st = btFall.snapshot(nowFall)
	if !strings.HasPrefix(st.BWForecast, "Flat/falling (-10.0 GB/day) — projected ") ||
		!strings.Contains(st.BWForecast, "(within 10 TB)") {
		t.Fatalf(">=3d falling forecast = %q", st.BWForecast)
	}
}

func TestStoreBandwidthStateRoundTrip(t *testing.T) {
	_ = store.CloseStore()
	if got, err := store.LoadBandwidthState(); err != nil || got != nil {
		t.Fatalf("detached LoadBandwidthState = (%v, %v), want (nil, nil)", got, err)
	}
	if err := store.SaveBandwidthState([]byte(`{"iface":"enp0s6"}`)); err != nil {
		t.Fatalf("detached SaveBandwidthState = %v, want nil", err)
	}

	fx := newFixture(t, devConfig())
	_ = fx
	if got, err := store.LoadBandwidthState(); err != nil || got != nil {
		t.Fatalf("empty LoadBandwidthState = (%v, %v), want (nil, nil)", got, err)
	}
	want := `{"iface":"enp0s6","last_tx":12345,"last_at":1700000000,"days":{"2026-11-25":999}}`
	if err := store.SaveBandwidthState([]byte(want)); err != nil {
		t.Fatalf("SaveBandwidthState = %v", err)
	}
	got, err := store.LoadBandwidthState()
	if err != nil {
		t.Fatalf("LoadBandwidthState = %v", err)
	}
	if string(got) != want {
		t.Fatalf("LoadBandwidthState = %q, want %q", string(got), want)
	}
}

func TestDashboardAndStatsJSONBandwidthCard(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	// Default unavailable state on non-Linux or when reader returns false.
	var tx uint64 = 1_000_000
	ok := false
	fx.s.bw = newBandwidthTracker(
		func() (uint64, string, bool) { return tx, "enp0s6", ok },
		store.LoadBandwidthState,
		store.SaveBandwidthState,
	)
	fx.s.sample()

	var js map[string]any
	if err := json.Unmarshal([]byte(c.get("/stats.json").body), &js); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"bw_available", "bw_iface", "bw_rate", "bw_24h", "bw_mtd", "bw_pace_month", "bw_forecast"} {
		if _, present := js[k]; !present {
			t.Errorf("/stats.json missing %q", k)
		}
	}
	if js["bw_available"] != false || js["bw_iface"] != "unavailable (Linux only)" {
		t.Fatalf("unavailable stats = %v / %v", js["bw_available"], js["bw_iface"])
	}

	// Switch to available and sample two readings 10s apart.
	ok = true
	fx.s.sample()
	fx.clk.advance(10 * time.Second)
	tx += 5_000_000
	fx.s.sample()

	if err := json.Unmarshal([]byte(c.get("/stats.json").body), &js); err != nil {
		t.Fatal(err)
	}
	if js["bw_available"] != true || js["bw_iface"] != "enp0s6" || js["bw_rate"] != "500.0 KB/s (4.00 Mbit/s)" {
		t.Fatalf("available stats unexpected: available=%v iface=%v rate=%v", js["bw_available"], js["bw_iface"], js["bw_rate"])
	}

	page := c.get("/")
	expect(t, page, http.StatusOK)
	for _, want := range []string{
		"<h2>Outbound bandwidth</h2>",
		`data-k="bw_rate">500.0 KB/s (4.00 Mbit/s)<`,
		`data-k="bw_24h"`,
		`data-k="bw_mtd"`,
		`data-k="bw_pace_month"`,
		`data-k="bw_forecast"`,
		`data-k="bw_iface">enp0s6<`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("dashboard HTML lacks %q", want)
		}
	}
	if bad := inlineMarkup(page.body); len(bad) != 0 {
		t.Errorf("dashboard inline markup violation: %v", bad)
	}
}
