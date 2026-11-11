package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type runOpts struct {
	endpoint, session, gateway, admin, serverLog, out string
	serverPID, players, seedAccounts                  int
	gatewayShare                                      float64
	duration, think, ramp, ping                       time.Duration
}

// snapshot is the server's state at one moment: its counters (cherry.* of /debug/vars), its RSS and CPU time, and the
// size of its request log.
type snapshot struct {
	Label     string                     `json:"label"`
	AtSeconds float64                    `json:"at_seconds"`
	Vars      map[string]json.RawMessage `json:"vars,omitempty"`
	ServerRSS int64                      `json:"server_rss_bytes"`
	ServerCPU float64                    `json:"server_cpu_seconds"`
	LogBytes  int64                      `json:"log_bytes"`
	Error     string                     `json:"error,omitempty"`
}

type report struct {
	Config      map[string]any        `json:"config"`
	Started     time.Time             `json:"started"`
	WallSeconds float64               `json:"wall_seconds"`
	Kinds       map[string]kindReport `json:"kinds"`
	Derived     map[string]any        `json:"derived"`
	Snapshots   []snapshot            `json:"snapshots"`
}

func takeSnapshot(label string, o runOpts, t0 time.Time) snapshot {
	s := snapshot{Label: label, AtSeconds: time.Since(t0).Seconds()}
	vars, err := fetchVars(o.admin)
	if err != nil {
		s.Error = "vars: " + err.Error()
	}
	s.Vars = vars
	if o.serverPID > 0 {
		rss, cpu, err := procStats(o.serverPID)
		s.ServerRSS, s.ServerCPU = rss, cpu
		if err != nil {
			s.Error += " proc: " + err.Error()
		}
	}
	s.LogBytes = fileSize(o.serverLog)
	return s
}

// fetchVars reads the admin listener's /debug/vars and keeps the cherry.* counters.
func fetchVars(admin string) (map[string]json.RawMessage, error) {
	resp, err := http.Get(strings.TrimSuffix(admin, "/") + "/debug/vars")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var all map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for k, v := range all {
		if strings.HasPrefix(k, "cherry.") {
			out[k] = v
		}
	}
	return out, nil
}

// num reads an integer counter of a snapshot (0 when missing).
func (s snapshot) num(name string) int64 {
	var n int64
	_ = json.Unmarshal(s.Vars[name], &n)
	return n
}

// hist reads the save-time histogram of a snapshot.
func (s snapshot) hist() map[string]int64 {
	h := map[string]int64{}
	_ = json.Unmarshal(s.Vars["cherry.save_hist"], &h)
	return h
}

// derive turns the start and end snapshots into the numbers the brief asks for.
func derive(start, end snapshot, mid snapshot, wall time.Duration, httpRequests int) map[string]any {
	d := map[string]any{}
	saves := end.num("cherry.saves") - start.num("cherry.saves")
	requests := end.num("cherry.requests") - start.num("cherry.requests")
	d["server_saves"] = saves
	d["server_saves_per_second"] = float64(saves) / wall.Seconds()
	if saves > 0 {
		d["server_save_mean_ms"] = float64(end.num("cherry.save_ns_total")-start.num("cherry.save_ns_total")) / float64(saves) / 1e6
	}
	d["server_save_max_ms_since_boot"] = float64(end.num("cherry.save_ns_max")) / 1e6
	hist := map[string]int64{}
	eh, sh := end.hist(), start.hist()
	for k, v := range eh {
		hist[k] = v - sh[k]
	}
	d["server_save_hist_delta"] = hist
	d["server_requests"] = requests
	d["client_http_requests"] = httpRequests
	d["server_requests_per_second"] = float64(requests) / wall.Seconds()
	d["accounts_end"] = end.num("cherry.accounts")
	d["session_conns_end"] = end.num("cherry.session_conns")
	d["gateway_conns_end"] = end.num("cherry.gateway_conns")
	d["wal_bytes_start"] = start.num("cherry.wal_bytes")
	d["wal_bytes_middle"] = mid.num("cherry.wal_bytes")
	d["wal_bytes_end"] = end.num("cherry.wal_bytes")
	d["server_rss_start"] = start.ServerRSS
	d["server_rss_middle"] = mid.ServerRSS
	d["server_rss_end"] = end.ServerRSS
	cpu := end.ServerCPU - start.ServerCPU
	d["server_cpu_seconds"] = cpu
	d["server_cores_average"] = cpu / wall.Seconds()
	logBytes := end.LogBytes - start.LogBytes
	d["log_bytes"] = logBytes
	if requests > 0 {
		d["log_bytes_per_request"] = float64(logBytes) / float64(requests)
	}
	return d
}

func runLoad(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var o runOpts
	fs.StringVar(&o.endpoint, "endpoint", "https://127.0.0.1:18443", "HTTPS game listener")
	fs.StringVar(&o.session, "session", "127.0.0.1:20123", ":10123 session listener")
	fs.StringVar(&o.gateway, "gateway", "127.0.0.1:20000", ":10000 gateway (Garden login and room enter)")
	fs.Float64Var(&o.gatewayShare, "gateway-share", 0.5, "share of the players that also enter their Garden (0 = none)")
	fs.StringVar(&o.admin, "admin", "http://127.0.0.1:18099", "admin listener (serves /debug/vars)")
	fs.IntVar(&o.players, "players", 300, "concurrent synthetic players (player i uses seeded account i)")
	fs.DurationVar(&o.duration, "duration", 5*time.Minute, "length of the measured run")
	fs.IntVar(&o.seedAccounts, "seed-accounts", 10000, "accounts the data directory was seeded with")
	fs.DurationVar(&o.think, "think", 3*time.Second, "mean think time between a player's actions (uniform, mean = this)")
	fs.DurationVar(&o.ramp, "ramp", 10*time.Second, "players join evenly over this time")
	fs.DurationVar(&o.ping, "ping", 20*time.Second, "session keepalive interval")
	fs.IntVar(&o.serverPID, "server-pid", 0, "PID of the server process (RSS and CPU are read for it)")
	fs.StringVar(&o.serverLog, "server-log", "", "server log file (its growth is the log volume)")
	fs.StringVar(&o.out, "out", "", "JSON result file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.out == "" || o.players < 1 || o.players > o.seedAccounts {
		return fmt.Errorf("run: -out is required and -players must be between 1 and -seed-accounts (%d)", o.seedAccounts)
	}

	st := newStats()
	c := newClient(o.endpoint, o.session, (o.seedAccounts+3)/4, st) // seeded diary posts: one per four accounts
	c.aids = seededAids(o.seedAccounts)
	if o.gatewayShare > 0 {
		c.gateway = o.gateway
	}
	t0 := time.Now()
	_, clientCPU0, _ := procStats(os.Getpid()) // the generator shares the box: its own CPU is reported too
	var snapMu sync.Mutex
	snaps := []snapshot{takeSnapshot("start", o, t0)}
	mid := snapshot{}
	ctx, cancel := context.WithDeadline(context.Background(), t0.Add(o.duration))
	defer cancel()
	midTimer := time.AfterFunc(o.duration/2, func() {
		s := takeSnapshot("middle", o, t0)
		snapMu.Lock()
		mid = s
		snapMu.Unlock()
	})
	var wg sync.WaitGroup
	for i := 0; i < o.players; i++ {
		p := &player{idx: i, code: guestCode(i), garden: i < int(o.gatewayShare*float64(o.players)), rng: rand.New(rand.NewSource(int64(i) + 1))}
		p.mail = []int64{1, 2} // the two mail rows the seed gives every account (seed seedAccount)
		offset := o.ramp * time.Duration(i) / time.Duration(o.players)
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-time.After(offset):
			}
			c.run(ctx, p, o.think, o.ping)
		}()
	}
	wg.Wait()
	midTimer.Stop()
	wall := time.Since(t0)
	time.Sleep(2 * time.Second) // the server notices the closed session sockets a moment after they close
	end := takeSnapshot("end", o, t0)
	snapMu.Lock()
	if mid.Label == "" { // the timer did not fire (a short run): the end state stands in for the middle
		mid = end
	}
	snaps = append(snaps, mid, end)
	snapMu.Unlock()

	kinds := st.report(wall)
	httpReq := 0
	for name, k := range kinds {
		if !strings.HasPrefix(name, "session_") && !strings.HasPrefix(name, "gw_") && name != "login_failed" {
			httpReq += k.Count // HTTP only: the session and gateway kinds are sockets, not requests
		}
	}
	rep := report{
		Config: map[string]any{
			"endpoint": o.endpoint, "session": o.session, "players": o.players, "duration": o.duration.String(),
			"think": o.think.String(), "ramp": o.ramp.String(), "ping": o.ping.String(), "seed_accounts": o.seedAccounts,
			"gateway": o.gateway, "gateway_share": o.gatewayShare,
			"server_pid": o.serverPID, "gomaxprocs": os.Getenv("GOMAXPROCS"), "gomemlimit": os.Getenv("GOMEMLIMIT"),
		},
		Started:     t0,
		WallSeconds: wall.Seconds(),
		Kinds:       kinds,
		Derived:     derive(snaps[0], end, mid, wall, httpReq),
		Snapshots:   snaps,
	}
	rep.Derived["http_requests_per_second"] = float64(httpReq) / wall.Seconds()
	if _, cpu1, err := procStats(os.Getpid()); err == nil {
		rep.Derived["client_cpu_seconds"] = cpu1 - clientCPU0
		rep.Derived["client_cores_average"] = (cpu1 - clientCPU0) / wall.Seconds()
	}
	if err := writeReport(o.out, rep); err != nil {
		return err
	}
	printReport(rep)
	return nil
}

func writeReport(path string, rep report) error {
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func printReport(rep report) {
	names := make([]string, 0, len(rep.Kinds))
	for n := range rep.Kinds {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("%-16s %8s %8s %7s %9s %9s %9s %9s\n", "kind", "count", "per/s", "errors", "p50ms", "p90ms", "p99ms", "maxms")
	for _, n := range names {
		k := rep.Kinds[n]
		fmt.Printf("%-16s %8d %8.2f %7d %9.2f %9.2f %9.2f %9.2f\n", n, k.Count, k.PerSecond, k.Errors, k.P50ms, k.P90ms, k.P99ms, k.MaxMs)
	}
	keys := make([]string, 0, len(rep.Derived))
	for k := range rep.Derived {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s = %v\n", k, rep.Derived[k])
	}
}
