package admin

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"cherry/internal/metrics"
	"cherry/internal/store"
)

// The dashboard's numbers (the staff UI, PLAN P4): the process, the counters of metrics, the database files and the Go
// runtime, as one struct. The dashboard shows it, GET /stats.json returns it as JSON, and static/staff.js refreshes the
// dashboard from that JSON every 15 seconds. No secret and no file path is in it.

const (
	sampleEvery = 10 * time.Second
	rateWindow  = time.Minute
	rateSlots   = 13 // 120 seconds of samples; the rate reads the last minute of them
	recentRows  = 10 // audit rows and sign-ups on the dashboard
	timeFormat  = "2006-01-02 15:04:05 UTC"
)

// histBar is one bucket of the save-time histogram. Bar is its CSS width class in steps of 5 percent (w-0 … w-100) of all
// saves.
type histBar struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
	Bar   int    `json:"bar"`
}

// stats is what the dashboard and /stats.json show. The JSON keys are the data-k names staff.js updates, so a rename is a
// change in both places.
type stats struct {
	Time           string    `json:"time"`
	Started        string    `json:"started"`
	UptimeSeconds  int64     `json:"uptime_seconds"`
	Uptime         string    `json:"uptime"`
	Build          string    `json:"build"`
	BuildTime      string    `json:"build_time"`
	GoVersion      string    `json:"go_version"`
	Accounts       int       `json:"accounts"`
	SessionConns   int64     `json:"session_conns"`
	GatewayConns   int64     `json:"gateway_conns"`
	Online         int64     `json:"online"`
	Requests       int64     `json:"requests"`
	RequestsPerMin float64   `json:"requests_per_min"`
	Saves          int64     `json:"saves"`
	SaveAvg        string    `json:"save_avg"`
	SaveMax        string    `json:"save_max"`
	SaveHist       []histBar `json:"save_hist"`
	DBBytes        int64     `json:"db_bytes"`
	DBSize         string    `json:"db_size"`
	WALBytes       int64     `json:"wal_bytes"`
	WALSize        string    `json:"wal_size"`
	DiaryFiles     int64     `json:"diary_files"`
	DiarySize      string    `json:"diary_size"`
	RoomFiles      int64     `json:"room_files"`
	RoomSize       string    `json:"room_size"`
	ProfileFiles   int64     `json:"profile_files"`
	ProfileSize    string    `json:"profile_size"`
	PicturesFiles  int64     `json:"pictures_files"`
	PicturesSize   string    `json:"pictures_size"`
	PicturesCap    string    `json:"pictures_cap"`
	PicturesPct    string    `json:"pictures_percent"`
	PerPlayerSize  string    `json:"per_player_size"`
	Goroutines     int       `json:"goroutines"`
	HeapBytes      uint64    `json:"heap_inuse_bytes"`
	HeapInUse      string    `json:"heap_inuse"`
	GCCount        uint32    `json:"gc_count"`
	GOMAXPROCS     int       `json:"gomaxprocs"`
	MemoryLimit    string    `json:"memory_limit"`
}

// sample is one reading of the request counter.
type sample struct {
	at    time.Time
	total int64
}

// rateRing keeps the last rateSlots readings of the request counter. A fixed ring: the memory does not grow.
type rateRing struct {
	mu    sync.Mutex
	ring  [rateSlots]sample
	next  int // the slot the next reading takes
	count int // readings held, up to rateSlots; slots 0 … count-1 are the ones in use
}

func (r *rateRing) add(at time.Time, total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ring[r.next] = sample{at, total}
	r.next = (r.next + 1) % rateSlots
	r.count = min(r.count+1, rateSlots)
}

// perMinute is the requests per minute: from the oldest reading in the last minute to total now. Without a reading in that
// minute it is 0.
func (r *rateRing) perMinute(now time.Time, total int64) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var from *sample
	for i := range r.count {
		if s := &r.ring[i]; now.Sub(s.at) <= rateWindow && (from == nil || s.at.Before(from.at)) {
			from = s
		}
	}
	if from == nil || now.Sub(from.at) < time.Second {
		return 0
	}
	return float64(total-from.total) / now.Sub(from.at).Minutes()
}

// sampleLoop reads the request counter every sampleEvery until Shutdown.
func (s *Server) sampleLoop() {
	t := time.NewTicker(sampleEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.rate.add(s.now(), metrics.Requests.Load())
		case <-s.stop:
			return
		}
	}
}

// buildInfo is the revision and time the binary was built from (vcs.revision, vcs.time). "dev" when the binary carries none:
// go run, go test, or a build outside a checkout.
var buildInfo = sync.OnceValues(func() (rev, when string) {
	rev = "dev"
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return rev, ""
	}
	dirty := false
	for _, kv := range bi.Settings {
		switch kv.Key {
		case "vcs.revision":
			if kv.Value != "" {
				rev = kv.Value[:min(len(kv.Value), 12)]
			}
		case "vcs.time":
			when = kv.Value
		case "vcs.modified":
			dirty = kv.Value == "true"
		}
	}
	if rev != "dev" && dirty {
		rev += " (modified)"
	}
	return rev, when
})

// statsAt reads the numbers at now. The pictures are the rows of the database (every kind), against the server-wide cap and the
// per-player limits in effect: the Owner Settings values, the panel's when one is set, else the configuration.
func (s *Server) statsAt(now time.Time) (stats, error) {
	rev, when := buildInfo()
	started := metrics.Started()
	uptime := time.Since(started).Truncate(time.Second)
	requests := metrics.Requests.Load()
	sessions, gateways := metrics.SessionConns.Load(), metrics.GatewayConns.Load()
	saves, held, longest := metrics.SaveStats()
	db, wal := store.DBBytes()
	totals, err := store.MediaTotals()
	if err != nil {
		return stats{}, err
	}
	var pictureFiles, pictureBytes int64
	for _, t := range totals {
		pictureFiles += t.Files
		pictureBytes += t.Bytes
	}
	capBytes := store.UploadMaxTotalBytes(s.cfg.EffectiveUploadMaxTotalBytes())
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	diary, room, profile := totals[store.MediaDiary], totals[store.MediaRoom], totals[store.MediaProfile]
	st := stats{
		Time: now.UTC().Format(timeFormat), Started: started.UTC().Format(timeFormat),
		UptimeSeconds: int64(uptime.Seconds()), Uptime: humanDuration(uptime),
		Build: rev, BuildTime: when, GoVersion: runtime.Version(),
		Accounts: store.AccountCount(), SessionConns: sessions, GatewayConns: gateways, Online: s.onlinePlayers(),
		Requests: requests, RequestsPerMin: math.Round(s.rate.perMinute(now, requests)*10) / 10,
		Saves: saves, SaveAvg: "none yet", SaveMax: "none yet", SaveHist: saveBars(),
		DBBytes: db, DBSize: humanBytes(db), WALBytes: wal, WALSize: humanBytes(wal),
		DiaryFiles: diary.Files, DiarySize: humanBytes(diary.Bytes),
		RoomFiles: room.Files, RoomSize: humanBytes(room.Bytes),
		ProfileFiles: profile.Files, ProfileSize: humanBytes(profile.Bytes),
		PicturesFiles: pictureFiles, PicturesSize: humanBytes(pictureBytes),
		PicturesCap: humanBytes(capBytes), PicturesPct: fmt.Sprintf("%.1f %%", float64(pictureBytes)*100/float64(capBytes)),
		PerPlayerSize: humanBytes(store.UploadQuotaBytes(s.cfg.EffectiveUploadQuotaBytes())),
		Goroutines:    runtime.NumGoroutine(), HeapBytes: mem.HeapInuse, HeapInUse: humanBytes(int64(mem.HeapInuse)),
		GCCount: mem.NumGC, GOMAXPROCS: runtime.GOMAXPROCS(0), MemoryLimit: memoryLimit(),
	}
	if saves > 0 {
		st.SaveAvg = (held / time.Duration(saves)).Round(time.Microsecond).String()
		st.SaveMax = longest.Round(time.Microsecond).String()
	}
	return st, nil
}

// saveBars is the save-time histogram with each bucket's share of all saves as a width class.
func saveBars() []histBar {
	labels, counts := metrics.SaveHistogram()
	var total int64
	for _, c := range counts {
		total += c
	}
	out := make([]histBar, len(labels))
	for i := range labels {
		bar := 0
		if total > 0 {
			bar = int((counts[i]*100/total+2)/5) * 5 // nearest 5 percent
		}
		out[i] = histBar{Label: labels[i], Count: counts[i], Bar: bar}
	}
	return out
}

// memoryLimit is the runtime's soft memory limit, or "none". A negative input reads the limit without changing it.
func memoryLimit() string {
	if l := debug.SetMemoryLimit(-1); l != math.MaxInt64 {
		return humanBytes(l)
	}
	return "none"
}

// humanBytes shows a byte count in IEC units: 1.5 KiB, 2.0 MiB.
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(1024), 0
	for m := n / 1024; m >= 1024; m /= 1024 {
		div *= 1024
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// humanDuration shows a duration as "2d 3h 4m", "3h 4m", "4m 5s" or "5s".
func humanDuration(d time.Duration) string {
	s := int64(d.Seconds())
	days, s := s/86400, s%86400
	h, s := s/3600, s%3600
	m, s := s/60, s%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// handleDashboard: the numbers, the newest audit rows and the newest sign-ups.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request, sess session) {
	recent, err := store.AuditRows(0, recentRows)
	if err != nil {
		s.storeError(w, err)
		return
	}
	signups, err := store.RecentSignups(recentRows)
	if err != nil {
		s.storeError(w, err)
		return
	}
	st, err := s.statsAt(s.now())
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Dashboard")
	v.Stats, v.Recent, v.Signups = st, recent, signups
	v.Summary = s.insightsSummary(s.now())
	if n, err := store.CountOpenReports(); err == nil {
		v.OpenReports = n
	}
	s.render(w, http.StatusOK, "dashboard", v)
}

// handleStats: GET /stats.json, the same numbers as JSON. The session guard is jsonPage.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, _ session) {
	st, err := s.statsAt(s.now())
	if err != nil {
		s.storeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(st)
}

// onlinePlayers is the number of distinct players with a login on a live connection, as the Online now page counts them: a
// player on both the session and the gateway connection is one player (the connection counts stay separate in the stats).
func (s *Server) onlinePlayers() int64 {
	seen := map[uint64]bool{}
	for _, c := range s.online() {
		seen[c.Aid] = true
	}
	return int64(len(seen))
}
