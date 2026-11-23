package admin

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/metrics"
	"cherry/internal/store"
)

var dataKeyRe = regexp.MustCompile(`data-k="([a-z_]+)"`)

// TestDashboardRendersStats: the cards, the save-time chart, the runtime, the newest audit rows and sign-ups, and the
// refresh hook; every data-k the page carries is a key of /stats.json, so the script updates what it shows.
func TestDashboardRendersStats(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-dash1", "7001", "Newcomer", 0)
	metrics.ObserveSave(2 * time.Millisecond)

	r := c.get("/")
	expect(t, r, http.StatusOK)
	for _, want := range []string{
		"Players online now", "Limits &amp; traffic", "Uptime", "Started", "Saves", "Database", "Build", "Go runtime", "Save time",
		"Newest audit rows", "Newest sign-ups", `data-stats`, `href="/players/` + strconv.FormatInt(id, 10) + `"`,
		"Newcomer", `data-k="build">dev<`, "Dashboard", `aria-current="page"`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	if !strings.Contains(r.body, `href="/audit"`) {
		t.Error("the dashboard does not link the audit log")
	}

	keys := map[string]bool{}
	for _, m := range dataKeyRe.FindAllStringSubmatch(r.body, -1) {
		keys[m[1]] = true
	}
	if len(keys) < 15 {
		t.Fatalf("the dashboard shows %d data keys, want the full set", len(keys))
	}
	var js map[string]any
	if err := json.Unmarshal([]byte(c.get("/stats.json").body), &js); err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		if _, ok := js[k]; !ok {
			t.Errorf("data-k %q is not a key of /stats.json", k)
		}
	}
}

// TestSignupsNewestFirst: the sign-up list runs from the newest account, and each row links to its player page.
func TestSignupsNewestFirst(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	older := seedPlayer(t, "dev-su1", "7201", "Older", 0)
	newer := seedPlayer(t, "dev-su2", "7202", "Newer", 0)
	body := c.get("/").body
	iNew := strings.Index(body, playerURL(newer)+`"`)
	iOld := strings.Index(body, playerURL(older)+`"`)
	if iNew < 0 || iOld < 0 || iNew > iOld {
		t.Fatalf("sign-ups not newest first: newer at %d, older at %d", iNew, iOld)
	}
}

// TestPollingDoesNotKeepASessionAlive: the dashboard's refresh reads /stats.json only, and that is not activity: a tab that
// only polls ends its session at the idle timeout, measured from the last page the staff member loaded.
func TestPollingDoesNotKeepASessionAlive(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	fx.clk.advance(idleTimeout - 5*time.Minute)
	expect(t, c.get("/stats.json"), http.StatusOK)
	fx.clk.advance(6 * time.Minute)
	expect(t, c.get("/stats.json"), http.StatusUnauthorized)

	// A page load is activity: the same timing with a page in between keeps the session.
	d := fx.client()
	d.signInDev("owner")
	fx.clk.advance(idleTimeout - 5*time.Minute)
	expect(t, d.get("/"), http.StatusOK)
	fx.clk.advance(6 * time.Minute)
	expect(t, d.get("/stats.json"), http.StatusOK)
}

// TestStatsJSONNeedsASession: without a session the JSON is 401 with a JSON body; with one it carries the numbers and no
// path, secret or identity.
func TestStatsJSONNeedsASession(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	r := c.get("/stats.json")
	expect(t, r, http.StatusUnauthorized)
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("401 content type %q", ct)
	}
	if strings.Contains(r.body, "<") {
		t.Error("the 401 answer is HTML")
	}

	c.signInDev("owner")
	r = c.get("/stats.json")
	expect(t, r, http.StatusOK)
	if ct := r.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	var js map[string]any
	if err := json.Unmarshal([]byte(r.body), &js); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	for _, k := range []string{"requests", "requests_per_min", "online", "accounts", "saves", "save_hist", "db_bytes", "goroutines", "gomaxprocs", "uptime", "build", "go_version"} {
		if _, ok := js[k]; !ok {
			t.Errorf("no %s in /stats.json", k)
		}
	}
	if n, _ := js["db_bytes"].(float64); n <= 0 {
		t.Errorf("db_bytes %v: the database file is not counted", js["db_bytes"])
	}
	if hist, _ := js["save_hist"].([]any); len(hist) != 5 {
		t.Errorf("save_hist has %d buckets, want 5", len(hist))
	}
	for _, leak := range []string{"cherry.db", "dev-owner", "__Host", "Cookie", "\\"} {
		if strings.Contains(r.body, leak) {
			t.Errorf("/stats.json holds %q", leak)
		}
	}
}

// TestStorageCardCountsPictureRows: the Storage card and /stats.json count the pictures the database holds per kind, against the
// server-wide cap and the per-player limits in effect (the configured values here).
func TestStorageCardCountsPictureRows(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	for _, m := range []store.MediaFile{
		{Kind: store.MediaDiary, Owner: "1001", File: "stats-diary-1.img", Size: 1 << 20},
		{Kind: store.MediaDiary, Owner: "1002", File: "stats-diary-2.img", Size: 512 << 10},
		{Kind: store.MediaProfile, Owner: "1001", File: "stats-profile-1.img", Size: 2 << 10},
	} {
		if err := store.AddMediaFile(m); err != nil {
			t.Fatal(err)
		}
	}
	var js map[string]any
	if err := json.Unmarshal([]byte(c.get("/stats.json").body), &js); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"diary_files": float64(2), "diary_size": "1.5 MiB",
		"room_files": float64(0), "room_size": "0 B",
		"profile_files": float64(1), "profile_size": "2.0 KiB",
		"pictures_files": float64(3), "pictures_size": "1.5 MiB",
		"pictures_cap": "100.0 MiB", "pictures_percent": "1.5 %",
		"per_player_size": "50.0 MiB",
	} {
		if js[k] != want {
			t.Errorf("/stats.json %s = %v, want %v", k, js[k], want)
		}
	}
	page := c.get("/").body
	for _, want := range []string{"<h2>Storage</h2>", "of the server cap", "Per player: ", "50.0 MiB</span> of pictures in all.", "write-ahead log"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Storage card lacks %q", want)
		}
	}
}

// TestRequestRatePerMinute: the rate reads the oldest reading of the last minute, readings older than that do not count, and
// the ring stays rateSlots long however many readings it takes.
func TestRequestRatePerMinute(t *testing.T) {
	var r rateRing
	t0 := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	r.add(t0, 100)
	r.add(t0.Add(30*time.Second), 250)
	if got := r.perMinute(t0.Add(60*time.Second), 400); got != 300 {
		t.Errorf("rate %v, want 300 requests a minute", got)
	}
	if got := r.perMinute(t0.Add(3*time.Minute), 500); got != 0 {
		t.Errorf("rate %v with no reading in the last minute, want 0", got)
	}
	if got := r.perMinute(t0, 100); got != 0 {
		t.Errorf("rate %v one reading after start, want 0", got)
	}
	for i := 0; i < 1000; i++ {
		r.add(t0.Add(time.Duration(i)*time.Second), int64(i))
	}
	if r.count != rateSlots {
		t.Errorf("ring holds %d readings, want %d", r.count, rateSlots)
	}
}

// TestHumanFormats: byte counts and durations read the way the cards show them.
func TestHumanFormats(t *testing.T) {
	for n, want := range map[int64]string{999: "999 B", 1536: "1.5 KiB", 2 << 20: "2.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		5 * time.Second:                               "5s",
		125 * time.Second:                             "2m 5s",
		3*time.Hour + 4*time.Minute:                   "3h 4m",
		26*time.Hour + 61*time.Second + 2*time.Minute: "1d 2h 3m",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// TestSaveBarsAreSharesInFives: each bar is a width class of 0 to 100 in steps of 5, one per bucket.
func TestSaveBarsAreSharesInFives(t *testing.T) {
	metrics.ObserveSave(time.Millisecond / 2)
	metrics.ObserveSave(50 * time.Millisecond)
	bars := saveBars()
	if len(bars) != 5 {
		t.Fatalf("%d buckets, want 5", len(bars))
	}
	for _, b := range bars {
		if b.Bar < 0 || b.Bar > 100 || b.Bar%5 != 0 || b.Label == "" {
			t.Errorf("bucket %+v", b)
		}
	}
}

// TestBuildInfoIsNeverEmpty: the build card always has a value ("dev" when the binary carries no vcs stamp).
func TestBuildInfoIsNeverEmpty(t *testing.T) {
	rev, _ := buildInfo()
	if rev == "" {
		t.Fatal("empty build revision")
	}
}

// TestRefusalAndStaticCountersOnDashboard: the Limits & traffic card and /stats.json report refusal totals since restart,
// last-hour deltas (aging older refusals out past 1 hour), static redirects, static reads, bytes served directly, and
// item lookups; the hour ring stays bounded at hourSlots.
func TestRefusalAndStaticCountersOnDashboard(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	baseConn := metrics.ConnRefused.Load()
	baseRate := metrics.RateLimited.Load()
	baseDrop := metrics.FramesDropped.Load()
	baseFlood := metrics.FrameFloods.Load()
	baseTotal := baseConn + baseRate + baseDrop + baseFlood
	baseReads := metrics.StaticReads.Load()
	baseRedirects, baseBytes := metrics.StaticTotals()
	baseLookups := metrics.ItemLookups.Load()

	// Baseline sample at t0.
	fx.s.sample()

	// Add refusals and static traffic at t0 + 5m.
	fx.clk.advance(5 * time.Minute)
	metrics.ConnRefused.Add(3)
	metrics.RateLimited.Add(2)
	metrics.FramesDropped.Add(5)
	metrics.FrameFloods.Add(1)
	fx.s.sample()

	metrics.StaticReads.Add(12)
	metrics.CountStaticRedirect(metrics.StaticItem)
	metrics.CountStaticRedirect(metrics.StaticItem)
	metrics.CountStaticRedirect(metrics.StaticPack)
	metrics.CountStaticRedirect(metrics.StaticBulk)
	metrics.CountStaticBytes(metrics.StaticItem, 1024)
	metrics.CountStaticBytes(metrics.StaticBulk, 512)
	metrics.ItemLookups.Add(9)

	readStats := func() map[string]any {
		t.Helper()
		r := c.get("/stats.json")
		expect(t, r, http.StatusOK)
		var js map[string]any
		if err := json.Unmarshal([]byte(r.body), &js); err != nil {
			t.Fatal(err)
		}
		return js
	}
	checkNum := func(js map[string]any, key string, want int64) {
		t.Helper()
		if got, ok := js[key].(float64); !ok || int64(got) != want {
			t.Errorf("/stats.json %s = %v, want %d", key, js[key], want)
		}
	}

	js := readStats()
	checkNum(js, "conn_refused", baseConn+3)
	checkNum(js, "conn_refused_hour", 3)
	checkNum(js, "rate_limited", baseRate+2)
	checkNum(js, "rate_limited_hour", 2)
	checkNum(js, "frames_dropped", baseDrop+5)
	checkNum(js, "frames_dropped_hour", 5)
	checkNum(js, "frame_floods", baseFlood+1)
	checkNum(js, "frame_floods_hour", 1)
	checkNum(js, "refusals_total", baseTotal+11)
	checkNum(js, "refusals_hour", 11)
	checkNum(js, "static_reads", baseReads+12)
	checkNum(js, "static_redirects", baseRedirects+4)
	checkNum(js, "item_lookups", baseLookups+9)
	if wantSize := humanBytes(baseBytes + 1536); js["static_served_size"] != wantSize {
		t.Errorf("/stats.json static_served_size = %v, want %q", js["static_served_size"], wantSize)
	}

	page := c.get("/")
	expect(t, page, http.StatusOK)
	for _, want := range []string{
		"<h2>Limits &amp; traffic</h2>",
		`data-k="refusals_total">` + strconv.FormatInt(baseTotal+11, 10) + `<`,
		`data-k="refusals_hour">11<`,
		`data-k="static_redirects">` + strconv.FormatInt(baseRedirects+4, 10) + `<`,
		`data-k="static_served_size">` + humanBytes(baseBytes+1536) + `<`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}

	// Sample at t0 + 30m (keeping the session active) before adding a second wave at t0 + 31m.
	fx.clk.advance(25 * time.Minute)
	expect(t, c.get("/"), http.StatusOK)
	fx.s.sample()
	fx.clk.advance(time.Minute)
	metrics.ConnRefused.Add(4)
	metrics.RateLimited.Add(1)
	fx.s.sample()

	js = readStats()
	checkNum(js, "conn_refused_hour", 7)
	checkNum(js, "rate_limited_hour", 3)
	checkNum(js, "frames_dropped_hour", 5)
	checkNum(js, "frame_floods_hour", 1)
	checkNum(js, "refusals_hour", 16)

	// Advance to t0 + 66m (with a page touch at t0 + 55m so the session stays valid); t0 and t0 + 5m age past 1 hour,
	// leaving t0 + 30m as the oldest in-window sample.
	fx.clk.advance(24 * time.Minute)
	expect(t, c.get("/"), http.StatusOK)
	fx.clk.advance(11 * time.Minute)
	metrics.FrameFloods.Add(2)

	js = readStats()
	checkNum(js, "conn_refused", baseConn+7)
	checkNum(js, "conn_refused_hour", 4)
	checkNum(js, "rate_limited", baseRate+3)
	checkNum(js, "rate_limited_hour", 1)
	checkNum(js, "frames_dropped", baseDrop+5)
	checkNum(js, "frames_dropped_hour", 0)
	checkNum(js, "frame_floods", baseFlood+3)
	checkNum(js, "frame_floods_hour", 2)
	checkNum(js, "refusals_total", baseTotal+18)
	checkNum(js, "refusals_hour", 7)

	page = c.get("/")
	expect(t, page, http.StatusOK)
	if want := `data-k="refusals_hour">7<`; !strings.Contains(page.body, want) {
		t.Errorf("dashboard after 1h rollover lacks %q", want)
	}

	var ring rateRing
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for i := range 1000 {
		ring.addHour(t0.Add(time.Duration(i)*10*time.Second), refusalCounts{connRefused: int64(i)})
	}
	if ring.hourCount != hourSlots {
		t.Errorf("hourRing holds %d readings, want %d", ring.hourCount, hourSlots)
	}
}
