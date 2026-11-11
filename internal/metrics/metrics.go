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
