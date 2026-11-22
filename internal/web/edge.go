package web

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"cherry/internal/assets"
	"cherry/internal/config"
	"cherry/internal/httpx"
	"cherry/internal/metrics"
	"cherry/internal/store"
)

// What the listeners let through before a handler runs (PLAN 8.6b parts 4 and 5): the plain HTTP port serves a short list
// of paths, and game requests spend tokens from their address's and account's buckets. Static GETs (asset files, which
// can cost an R2 read or a generated picture) have their own bucket per address, sized for a fresh install (see
// newLimited); they are exempt from the game buckets, since the first download is thousands of them.

// plainPathAllowed reports whether the plain HTTP listener (client port 80) answers path. The client asks for these over
// plain HTTP and nothing else: item art under http://play-static.line-scdn.net/img/read/ (IDA host table, live index 15),
// the notice requests of the Java and native notice stacks (notice2.line.me, index 22/23, and /img/read/notice/, which
// answer 404 as before). The lab logs of 2026-10 to 2026-11 (17 files, 482 plain requests: sni empty) show only these
// shapes: notice2 /v1/lineplay/android and /img/read/notice/notice.json2 (all 404), and /img/read/arts_item_* (2026-10-03).
//
// The iOS 10.1.0 client's static base is http://play-static.line-scdn.net/img/read/ (Raspberry CONTRACT §4, 2026-11-20,
// static: InitFunc_634), so it asks for its manifest, update lists, category files, chat icons, diary skin, map packs and
// craft art on the plain port under /img/read/: the /arts_ routes, the map pack folders and the craft folders. The
// profile pictures (lineplay/pr/<oid>, the home menu's and the Lounge Chat rows' relative paths) are downloads too; the
// upload (lineplay/pr/upload.nhn, the owner's POST) is not allowed here, and any other method on a picture path reaches
// the download handler, which answers 404.
func plainPathAllowed(path string) bool {
	if path == "/v1/lineplay/android" || path == "/v1/lineplay/ios" {
		return true
	}
	rest, ok := strings.CutPrefix(path, "/img/read/")
	if !ok {
		return false
	}
	if strings.HasPrefix(rest, "arts_") || strings.HasPrefix(rest, "notice/") {
		return true
	}
	if pic, ok := strings.CutPrefix(rest, "lineplay/pr/"); ok && pic != "upload.nhn" {
		return true
	}
	for _, dir := range slices.Concat(assets.PackDirs, assets.CraftDirs) {
		if strings.HasPrefix(rest, dir+"/") {
			return true
		}
	}
	return false
}

// NewPlainMux is the handler of the plain HTTP listener: the allowed paths of the game mux, nothing else (404). A path
// under /img/read/ reaches the game mux without that prefix, at the route the HTTPS static base uses; item files keep
// it (their routes take both forms, as before).
func NewPlainMux() http.Handler {
	mux := NewMux()
	strip := http.StripPrefix("/img/read", mux)
	game := newLimited(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/img/read/") && !strings.HasPrefix(r.URL.Path, "/img/read/arts_item_") {
			strip.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}), config.Current())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !plainPathAllowed(r.URL.Path) {
			httpx.ServeNotFound(w)
			return
		}
		game.ServeHTTP(w, r)
	})
}

// staticPrefixes are the GET paths that serve files: the asset store, the Cherry pictures, the butler voice packs and the
// profile and diary pictures. Everything else is a game request.
var staticPrefixes = []string{"/arts_", "/img/read/", "/notice/", "/cherry/", "/web/", "/lineplay/", "/r/lineplay/", "/v4/resource/splash/", "/butler_voice_master/"}

// isStaticGET reports whether r is a file read the limits exempt: a GET or HEAD of a static prefix, or a map pack ("/../").
func isStaticGET(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if strings.Contains(p, "/../") {
		return true
	}
	for _, pre := range staticPrefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// requestBuckets are the game request buckets of one newLimited wrapper (see newLimited for what each one counts).
type requestBuckets struct{ address, account, guest, static *httpx.Bucket }

// limitSets records the buckets of every wrapper newLimited built: ApplyLimits changes them all while Cherry runs. A wrapper
// keeps its own buckets, so the two listeners and the tests do not share counts.
var limitSets struct {
	sync.Mutex
	list []requestBuckets
}

// ApplyLimits makes l live: the connection caps (httpx.Conns, checked at accept and at each admit) and the request buckets
// of every listener. Connections already open keep their counts; the new rates and bursts apply to the next request.
func ApplyLimits(l store.Limits) {
	httpx.Conns.Set(l.ConnsPerAddress, l.ConnsTotal)
	limitSets.Lock()
	defer limitSets.Unlock()
	for _, b := range limitSets.list {
		b.address.Set(float64(l.APIRate), l.APIBurst)
		b.account.Set(float64(l.AccountRate), l.AccountBurst)
		b.guest.Set(float64(l.GuestPerMinute)/60, l.GuestPerMinute)
		b.static.Set(float64(l.StaticRate), l.StaticBurst)
	}
}

// newLimited wraps next with the request limits in effect for cfg (store.EffectiveLimits: the panel's value or the configured
// one): a token bucket per remote address (limits.api-*), one per account that the AV_AUTH token names (limits.account-*),
// guest/generate per address a minute (limits.guest-per-minute), and static GETs per address (limits.static-*). A rate of
// zero is off, which is the lab's setting. Over a limit the answer is 429 with a body the client reads as an error.
//
// Static sizing (PLAN 8.6b part 5, measured on the lab's request logs, 2026-11-16): a fresh install made 13,255 static GETs
// in one log, with peaks of 195 in a second, 1,791 in ten seconds and 8,761 in a minute; 150 a second with a burst of 836
// covers it, and 200 a second needs no burst at all in the three fresh-install logs measured. The burst of 5,000 covers
// the ten-second peak with room. The bucket is per address, so a carrier NAT shares one bucket among its users; watch the
// 429 counts before tightening it.
func newLimited(next http.Handler, cfg config.Config) http.Handler {
	l := store.EffectiveLimits(cfg)
	b := requestBuckets{
		address: httpx.NewBucket(float64(l.APIRate), l.APIBurst),
		account: httpx.NewBucket(float64(l.AccountRate), l.AccountBurst),
		guest:   httpx.NewBucket(float64(l.GuestPerMinute)/60, l.GuestPerMinute),
		static:  httpx.NewBucket(float64(l.StaticRate), l.StaticBurst),
	}
	limitSets.Lock()
	limitSets.list = append(limitSets.list, b)
	limitSets.Unlock()
	address, account, guest, static := b.address, b.account, b.guest, b.static
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		addr := httpx.RemoteHost(r.RemoteAddr)
		if isStaticGET(r) || httpx.IsItemNameLookup(r) {
			if !static.Allow(addr, now) {
				refuseRateLimited(w)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !address.Allow(addr, now) {
			refuseRateLimited(w)
			return
		}
		if r.URL.Path == "/v4/account/guest/generate" && !guest.Allow(addr, now) {
			refuseRateLimited(w)
			return
		}
		if account.On() { // the lookup takes the account lock: only when the account limit is on (the lab's is off)
			if aid := store.AidForToken(httpx.CookieValue(r, "AV_AUTH")); aid != "" && !account.Allow("aid:"+aid, now) {
				refuseRateLimited(w)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// refuseRateLimited answers 429. [I] The stock client is assumed to treat any non-200 answer as a failed call (its error
// callbacks; not checked in the native code, and not device-tested). A burst past the limit is not expected in play: the
// sizing is in the slice 8.6b part 4 report, measured on the lab logs.
func refuseRateLimited(w http.ResponseWriter) {
	metrics.RateLimited.Add(1)
	w.Header().Set("Retry-After", "1")
	httpx.WriteJSON(w, http.StatusTooManyRequests, httpx.RateLimitedBody)
}
