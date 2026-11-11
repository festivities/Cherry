package main

import (
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
)

// stats collects every request of a run by action kind. Status 0 is a transport failure; d is 0 for kinds that carry no
// latency (session frames).
type stats struct {
	mu    sync.Mutex
	kinds map[string]*kindStats
}

type kindStats struct {
	n         int
	transport int
	status    map[int]int
	lat       []time.Duration
}

func newStats() *stats { return &stats{kinds: map[string]*kindStats{}} }

func (s *stats) add(kind string, status int, d time.Duration, err error) {
	_ = err // a transport error is counted by its status 0; the text is not kept (it can carry request details)
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.kinds[kind]
	if k == nil {
		k = &kindStats{status: map[int]int{}}
		s.kinds[kind] = k
	}
	k.n++
	if status == 0 {
		k.transport++
	} else {
		k.status[status]++
	}
	if d > 0 {
		k.lat = append(k.lat, d)
	}
}

// kindReport is one action kind in the result file. Errors are transport failures plus every 4xx and 5xx.
type kindReport struct {
	Count           int            `json:"count"`
	PerSecond       float64        `json:"per_second"`
	Errors          int            `json:"errors"`
	TransportErrors int            `json:"transport_errors"`
	Status          map[int]int    `json:"status"`
	P50ms           float64        `json:"p50_ms"`
	P90ms           float64        `json:"p90_ms"`
	P99ms           float64        `json:"p99_ms"`
	MaxMs           float64        `json:"max_ms"`
	Latencies       int            `json:"latency_samples"`
	ErrorsByStatus  map[string]int `json:"errors_by_status,omitempty"`
}

func (s *stats) report(wall time.Duration) map[string]kindReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]kindReport, len(s.kinds))
	for name, k := range s.kinds {
		r := kindReport{Count: k.n, Errors: k.transport, TransportErrors: k.transport, Status: k.status, Latencies: len(k.lat), ErrorsByStatus: map[string]int{}}
		if wall > 0 {
			r.PerSecond = float64(k.n) / wall.Seconds()
		}
		for code, n := range k.status {
			if code >= 400 {
				r.Errors += n
				r.ErrorsByStatus[strconv.Itoa(code)] = n
			}
		}
		if len(r.ErrorsByStatus) == 0 {
			r.ErrorsByStatus = nil
		}
		lat := append([]time.Duration(nil), k.lat...)
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		r.P50ms, r.P90ms, r.P99ms = ms(percentile(lat, 0.50)), ms(percentile(lat, 0.90)), ms(percentile(lat, 0.99))
		if len(lat) > 0 {
			r.MaxMs = ms(lat[len(lat)-1])
		}
		out[name] = r
	}
	return out
}

// percentile of an ascending slice (nearest rank); 0 for an empty slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

func ms(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Millisecond)*1000) / 1000 }
