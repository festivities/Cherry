// Package metrics holds Cherry's server counters (PLAN P8/P9). They are published through the standard expvar package
// and served only by the admin listener (economy.NewAdminMux, at /debug/vars); no game listener mounts them. The
// package is a leaf: it imports only the standard library, so store, httpx and lpn can count without a cycle.
package metrics

import (
	"expvar"
	"sync/atomic"
	"time"
)

// Requests counts the requests answered by the game and plain HTTP servers (httpx.WithLogging).
var Requests atomic.Int64

// SessionConns and GatewayConns are the open :10123 session and :10000 gateway connections.
var SessionConns, GatewayConns atomic.Int64

// ChatLogDropped counts the player messages the chat log could not keep: a full queue or a failed write (store/chatlog.go).
var ChatLogDropped atomic.Int64

// ItemLookups counts the item-name lookups (httpx.IsItemNameLookup), logged or not. StaticReads counts the static GETs
// (httpx.IsStaticGET), logged or not. ConnRefused counts the connections a cap turned away; RateLimited the 429 answers
// of the rate limits; FramesDropped the frames over limits.frame-rate and FrameFloods the connections closed for a
// flood (lpn framelimit.go).
var ItemLookups, StaticReads, ConnRefused, RateLimited, FramesDropped, FrameFloods atomic.Int64

var (
	saves      atomic.Int64 // every store save (accounts, social, circles, Treasure World, chat)
	saveNs     atomic.Int64 // total time the saves held their lock
	saveMaxNs  atomic.Int64
	saveBucket [saveBuckets]atomic.Int64
)

const saveBuckets = 5

// saveLimits are the upper bounds of the first four save-time buckets; the last bucket takes the rest.
var saveLimits = [saveBuckets - 1]time.Duration{time.Millisecond, 5 * time.Millisecond, 20 * time.Millisecond, 100 * time.Millisecond}

var saveNames = [saveBuckets]string{"le_1ms", "le_5ms", "le_20ms", "le_100ms", "gt_100ms"}

// saveLabels are the buckets' names as the staff dashboard shows them.
var saveLabels = [saveBuckets]string{"up to 1 ms", "1 to 5 ms", "5 to 20 ms", "20 to 100 ms", "over 100 ms"}

// started is when the process started: this package initialises before main, which is near enough.
var started = time.Now()

// ObserveSave records one save that held its lock for d.
func ObserveSave(d time.Duration) {
	ns := int64(d)
	saves.Add(1)
	saveNs.Add(ns)
	for {
		cur := saveMaxNs.Load()
		if ns <= cur || saveMaxNs.CompareAndSwap(cur, ns) {
			break
		}
	}
	i := 0
	for i < len(saveLimits) && d > saveLimits[i] {
		i++
	}
	saveBucket[i].Add(1)
}

// SaveSince is ObserveSave of the time since start; a save function starts with `defer metrics.SaveSince(time.Now())`.
func SaveSince(start time.Time) { ObserveSave(time.Since(start)) }

// Gauge publishes f as cherry.<name>. The store publishes the values only it can read (accounts, WAL size) this way.
func Gauge(name string, f func() int64) {
	expvar.Publish("cherry."+name, expvar.Func(func() any { return f() }))
}

func init() {
	publish("requests", &Requests)
	publish("session_conns", &SessionConns)
	publish("gateway_conns", &GatewayConns)
	publish("chat_log_dropped", &ChatLogDropped)
	publish("item_lookups", &ItemLookups)
	publish("static_reads", &StaticReads)
	publish("conn_refused", &ConnRefused)
	publish("rate_limited", &RateLimited)
	publish("frames_dropped", &FramesDropped)
	publish("frame_floods", &FrameFloods)
	publish("saves", &saves)
	publish("save_ns_total", &saveNs)
	publish("save_ns_max", &saveMaxNs)
	expvar.Publish("cherry.save_hist", expvar.Func(func() any {
		h := make(map[string]int64, saveBuckets)
		for i, name := range saveNames {
			h[name] = saveBucket[i].Load()
		}
		return h
	}))
}

func publish(name string, v *atomic.Int64) {
	expvar.Publish("cherry."+name, expvar.Func(func() any { return v.Load() }))
}

// Started is when the process started.
func Started() time.Time { return started }

// SaveStats is the number of store saves so far, the time they held the store's lock in total and the longest hold.
func SaveStats() (n int64, total, longest time.Duration) {
	return saves.Load(), time.Duration(saveNs.Load()), time.Duration(saveMaxNs.Load())
}

// SaveHistogram is the save-time buckets' labels and counts, in order (the staff dashboard reads them).
func SaveHistogram() (labels []string, counts []int64) {
	for i := range saveBuckets {
		labels = append(labels, saveLabels[i])
		counts = append(counts, saveBucket[i].Load())
	}
	return labels, counts
}

// StaticKind splits the streamed original files (assets) for the counters: item files (static/item/ and the converted
// cherry/item/), map packs, and every other bulk file.
type StaticKind int

const (
	StaticItem StaticKind = iota
	StaticPack
	StaticBulk
	numStaticKinds
)

var staticKindNames = [numStaticKinds]string{"item", "pack", "bulk"}

// staticRedirects counts the streamed GETs answered with a redirect to the public copy (assets/egress.go); staticBytes
// the bytes Cherry wrote for the streamed files it served itself.
var staticRedirects, staticBytes [numStaticKinds]atomic.Int64

// CountStaticRedirect counts one streamed GET answered with a redirect.
func CountStaticRedirect(k StaticKind) { staticRedirects[k].Add(1) }

// CountStaticBytes adds n bytes Cherry wrote for a streamed file of kind k.
func CountStaticBytes(k StaticKind, n int64) { staticBytes[k].Add(n) }

// publishStatic publishes cherry.<name> (the total over the kinds) and cherry.<name>_<kind> for each kind.
func publishStatic(name string, c *[numStaticKinds]atomic.Int64) {
	expvar.Publish("cherry."+name, expvar.Func(func() any {
		var n int64
		for i := range c {
			n += c[i].Load()
		}
		return n
	}))
	for k := range numStaticKinds {
		expvar.Publish("cherry."+name+"_"+staticKindNames[k], expvar.Func(func() any { return c[k].Load() }))
	}
}

func init() {
	publishStatic("static_redirects", &staticRedirects)
	publishStatic("static_bytes_served", &staticBytes)
}
