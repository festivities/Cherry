package admin

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// monthlyOutboundBudgetBytes is the OCI Always Free monthly outbound allowance (10 TB decimal).
	monthlyOutboundBudgetBytes int64 = 10_000_000_000_000

	bwFlushInterval = 5 * time.Minute
	bwKeepDays      = 62
	bwMaxHistory    = 14
	bwRateSlots     = 13 // 120s of 10s samples; current rate uses the last 1m window
	bwRateWindow    = time.Minute
	bwBucketSec     = 600 // 10-minute bucket size
	bw24hSlots      = 144 // 24h / 10m
	bwMaxRestartGap = 86400
)

type bwPersisted struct {
	Iface  string           `json:"iface,omitempty"`
	LastTx uint64           `json:"last_tx,omitempty"`
	LastAt int64            `json:"last_at,omitempty"` // unix seconds
	Days   map[string]int64 `json:"days,omitempty"`    // "YYYY-MM-DD" (UTC) -> bytes
}

type bwRateSample struct {
	at time.Time
	tx uint64
}

type bwBucket struct {
	slot  int64 // now.Unix() / bwBucketSec
	bytes int64
}

type bandwidthStats struct {
	BWAvailable bool   `json:"bw_available"`
	BWIface     string `json:"bw_iface"`
	BWRate      string `json:"bw_rate"`
	BW24h       string `json:"bw_24h"`
	BWMTD       string `json:"bw_mtd"`
	BWPaceMonth string `json:"bw_pace_month"`
	BWForecast  string `json:"bw_forecast"`
}

// bandwidthTracker samples the host's default-route TX byte counter, maintains a 1-minute rate ring, a 24-hour
// rolling bucket ring, and per-UTC-day totals persisted to cherry.db meta.
type bandwidthTracker struct {
	readTx    func() (uint64, string, bool)
	loadState func() ([]byte, error)
	saveState func([]byte) error

	saveMu sync.Mutex // serializes DB writes outside mu
	mu     sync.Mutex

	loaded    bool
	available bool
	iface     string

	persistedIface string
	persistedTx    uint64
	persistedAt    int64

	hasPrev    bool
	prevIface  string
	prevTx     uint64
	trackStart time.Time

	days        map[string]int64
	dirty       bool
	lastFlushAt time.Time

	rateRing  [bwRateSlots]bwRateSample
	rateNext  int
	rateCount int

	buckets [bw24hSlots]bwBucket
}

func newBandwidthTracker(
	readTx func() (uint64, string, bool),
	loadState func() ([]byte, error),
	saveState func([]byte) error,
) *bandwidthTracker {
	return &bandwidthTracker{
		readTx:    readTx,
		loadState: loadState,
		saveState: saveState,
		days:      map[string]int64{},
	}
}

// parseDefaultRoute returns the interface name of the active default route (Destination == "00000000" and RTF_UP flag
// bit 0x1 set) with the lowest decimal Metric in Linux /proc/net/route format.
func parseDefaultRoute(data []byte) string {
	var bestIface string
	var bestMetric uint64
	found := false

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		if fields[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 64)
		if err != nil || flags&0x1 == 0 {
			continue
		}
		metric, err := strconv.ParseUint(fields[6], 10, 64)
		if err != nil {
			continue
		}
		if !found || metric < bestMetric {
			bestIface = fields[0]
			bestMetric = metric
			found = true
		}
	}
	return bestIface
}

// parseProcNetDevTx parses Linux /proc/net/dev and returns the tx_bytes column (field index 8 after ':') for iface.
func parseProcNetDevTx(data []byte, iface string) (uint64, bool) {
	if iface == "" {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		left, right, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.TrimSpace(left) != iface {
			continue
		}
		fields := strings.Fields(right)
		if len(fields) <= 8 {
			return 0, false
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return 0, false
		}
		return tx, true
	}
	return 0, false
}

func (b *bandwidthTracker) ensureLoadedLocked() {
	if b.loaded {
		return
	}
	b.loaded = true
	if b.days == nil {
		b.days = map[string]int64{}
	}
	if b.loadState == nil {
		return
	}
	raw, err := b.loadState()
	if err != nil || len(raw) == 0 {
		return
	}
	var p bwPersisted
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	b.persistedIface = p.Iface
	b.persistedTx = p.LastTx
	b.persistedAt = p.LastAt
	for k, v := range p.Days {
		if len(k) == 10 && v >= 0 {
			b.days[k] = v
		}
	}
	b.pruneDaysLocked()
}

func (b *bandwidthTracker) pruneDaysLocked() {
	if len(b.days) <= bwKeepDays {
		return
	}
	keys := make([]string, 0, len(b.days))
	for k := range b.days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys[:len(keys)-bwKeepDays] {
		delete(b.days, k)
	}
}

func (b *bandwidthTracker) addRateLocked(at time.Time, tx uint64) {
	b.rateRing[b.rateNext] = bwRateSample{at: at, tx: tx}
	b.rateNext = (b.rateNext + 1) % bwRateSlots
	b.rateCount = min(b.rateCount+1, bwRateSlots)
}

func (b *bandwidthTracker) resetRateLocked() {
	b.rateCount = 0
	b.rateNext = 0
}

func (b *bandwidthTracker) addBucketLocked(at time.Time, delta int64) {
	slot := at.Unix() / bwBucketSec
	idx := int(((slot % bw24hSlots) + bw24hSlots) % bw24hSlots)
	if b.buckets[idx].slot == slot {
		b.buckets[idx].bytes += delta
		return
	}
	b.buckets[idx] = bwBucket{slot: slot, bytes: delta}
}

func (b *bandwidthTracker) marshalLocked(now time.Time) []byte {
	daysCopy := make(map[string]int64, len(b.days))
	for k, v := range b.days {
		daysCopy[k] = v
	}
	p := bwPersisted{
		Iface:  b.prevIface,
		LastTx: b.prevTx,
		LastAt: now.Unix(),
		Days:   daysCopy,
	}
	raw, _ := json.Marshal(p)
	return raw
}

func (b *bandwidthTracker) persist(raw []byte) {
	if len(raw) == 0 || b.saveState == nil {
		return
	}
	b.saveMu.Lock()
	err := b.saveState(raw)
	b.saveMu.Unlock()
	if err != nil {
		b.mu.Lock()
		b.dirty = true
		b.mu.Unlock()
	}
}

func (b *bandwidthTracker) sample(now time.Time) {
	var toPersist []byte

	b.mu.Lock()
	b.ensureLoadedLocked()
	if b.readTx == nil {
		b.available = false
		b.mu.Unlock()
		return
	}
	tx, iface, ok := b.readTx()
	if !ok {
		b.available = false
		b.mu.Unlock()
		return
	}
	b.available = true
	b.iface = iface
	today := now.UTC().Format("2006-01-02")

	if !b.hasPrev {
		b.hasPrev = true
		b.prevIface = iface
		b.prevTx = tx
		b.trackStart = now
		b.lastFlushAt = now
		b.addRateLocked(now, tx)
		if b.persistedIface == iface && tx >= b.persistedTx && b.persistedAt > 0 {
			gapSec := now.Unix() - b.persistedAt
			if gapSec > 0 && gapSec <= bwMaxRestartGap {
				if diff := tx - b.persistedTx; diff > 0 && diff <= math.MaxInt64 {
					b.days[today] += int64(diff)
					b.pruneDaysLocked()
					b.dirty = true
				}
			}
		}
		if _, exists := b.days[today]; !exists {
			b.days[today] = 0
			b.pruneDaysLocked()
			b.dirty = true
		}
	} else if iface != b.prevIface || tx < b.prevTx {
		// Host rebooted or default interface switched: reset baseline without adding a bogus delta.
		b.prevIface = iface
		b.prevTx = tx
		b.resetRateLocked()
		b.addRateLocked(now, tx)
		b.dirty = true
	} else {
		var delta int64
		if diff := tx - b.prevTx; diff <= math.MaxInt64 {
			delta = int64(diff)
		}
		b.prevTx = tx
		if _, exists := b.days[today]; !exists || delta > 0 {
			b.days[today] += delta
			b.pruneDaysLocked()
			b.dirty = true
		}
		b.addRateLocked(now, tx)
		b.addBucketLocked(now, delta)
	}

	if b.dirty && b.saveState != nil && now.Sub(b.lastFlushAt) >= bwFlushInterval {
		toPersist = b.marshalLocked(now)
		b.dirty = false
		b.lastFlushAt = now
	}
	b.mu.Unlock()

	if toPersist != nil {
		b.persist(toPersist)
	}
}

func (b *bandwidthTracker) flush(now time.Time) {
	b.mu.Lock()
	b.ensureLoadedLocked()
	if !b.hasPrev || b.saveState == nil {
		b.mu.Unlock()
		return
	}
	raw := b.marshalLocked(now)
	b.dirty = false
	b.lastFlushAt = now
	b.mu.Unlock()

	b.persist(raw)
}

func (b *bandwidthTracker) currentRateLocked(now time.Time) (float64, bool) {
	var from, to *bwRateSample
	for i := range b.rateCount {
		s := &b.rateRing[i]
		if d := now.Sub(s.at); d >= 0 && d <= bwRateWindow {
			if from == nil || s.at.Before(from.at) {
				from = s
			}
			if to == nil || s.at.After(to.at) {
				to = s
			}
		}
	}
	if from == nil || to == nil {
		return 0, false
	}
	dt := to.at.Sub(from.at)
	if dt < time.Second || to.tx < from.tx {
		return 0, false
	}
	return float64(to.tx-from.tx) / dt.Seconds(), true
}

func (b *bandwidthTracker) rolling24hLocked(now time.Time) (int64, time.Duration) {
	curSlot := now.Unix() / bwBucketSec
	minSlot := curSlot - bw24hSlots + 1
	var total int64
	for _, bk := range b.buckets {
		if bk.slot >= minSlot && bk.slot <= curSlot {
			total += bk.bytes
		}
	}
	if !b.hasPrev {
		return 0, 0
	}
	span := now.Sub(b.trackStart)
	if span < 0 {
		span = 0
	}
	if span > 24*time.Hour {
		span = 24 * time.Hour
	}
	return total, span
}

func (b *bandwidthTracker) snapshot(now time.Time) bandwidthStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureLoadedLocked()

	if !b.available {
		return bandwidthStats{
			BWAvailable: false,
			BWIface:     "unavailable (Linux only)",
			BWRate:      "n/a",
			BW24h:       "n/a",
			BWMTD:       "n/a",
			BWPaceMonth: "n/a",
			BWForecast:  "Host interface counters are only available on Linux.",
		}
	}

	out := bandwidthStats{
		BWAvailable: true,
		BWIface:     b.iface,
	}

	if rateBps, ok := b.currentRateLocked(now); ok {
		out.BWRate = fmt.Sprintf("%s (%s)", formatBytesRate(rateBps), humanBitsRate(rateBps*8))
	} else {
		out.BWRate = "warming up"
	}

	total24h, span := b.rolling24hLocked(now)
	switch {
	case span < time.Second:
		out.BW24h = "0 B (warming up)"
	case span >= 24*time.Hour:
		avg := float64(total24h) / (24 * 3600)
		out.BW24h = fmt.Sprintf("%s (%s avg)", humanBytesDecimal(total24h), formatBytesRate(avg))
	default:
		avg := float64(total24h) / span.Seconds()
		out.BW24h = fmt.Sprintf("%s in %s (%s avg)", humanBytesDecimal(total24h), format24hSpan(span), formatBytesRate(avg))
	}

	nowUTC := now.UTC()
	todayStr := nowUTC.Format("2006-01-02")
	monthPrefix := nowUTC.Format("2006-01-")
	var mtdBytes int64
	var mtdDays int
	for k, v := range b.days {
		if strings.HasPrefix(k, monthPrefix) {
			mtdBytes += v
			mtdDays++
		}
	}
	out.BWMTD = fmt.Sprintf("%s (%d d tracked)", humanBytesDecimal(mtdBytes), mtdDays)

	monthStart := time.Date(nowUTC.Year(), nowUTC.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextMonth := monthStart.AddDate(0, 1, 0)
	daysInMonth := nextMonth.AddDate(0, 0, -1).Day()
	remainingDays := nextMonth.Sub(nowUTC).Hours() / 24.0

	var dailyPace float64
	if span >= 10*time.Minute {
		dailyPace = (float64(total24h) / span.Seconds()) * 86400.0
	} else if mtdDays > 0 {
		dailyPace = float64(mtdBytes) / float64(mtdDays)
	}
	paceMonth := float64(mtdBytes) + dailyPace*remainingDays
	pacePct := paceMonth * 100.0 / float64(monthlyOutboundBudgetBytes)
	out.BWPaceMonth = fmt.Sprintf("%s / %s (%.1f%%)",
		humanBytesDecimalFloat(paceMonth),
		humanBytesDecimal(monthlyOutboundBudgetBytes),
		pacePct,
	)

	var completedKeys []string
	for k, v := range b.days {
		if k < todayStr && v > 0 {
			completedKeys = append(completedKeys, k)
		}
	}
	sort.Strings(completedKeys)
	if len(completedKeys) > bwMaxHistory {
		completedKeys = completedKeys[len(completedKeys)-bwMaxHistory:]
	}

	if len(completedKeys) < 3 {
		if paceMonth > float64(monthlyOutboundBudgetBytes) {
			out.BWForecast = fmt.Sprintf("Collecting daily trend (%d/3 full days); current pace EXCEEDS 10 TB/mo", len(completedKeys))
		} else {
			out.BWForecast = fmt.Sprintf("Collecting daily trend (%d/3 full days); current pace is within 10 TB/mo", len(completedKeys))
		}
		return out
	}

	n := float64(len(completedKeys))
	meanX := (n - 1.0) / 2.0
	var sumY float64
	ys := make([]float64, len(completedKeys))
	for i, k := range completedKeys {
		y := float64(b.days[k])
		ys[i] = y
		sumY += y
	}
	meanY := sumY / n
	var num, den float64
	for i, y := range ys {
		dx := float64(i) - meanX
		num += dx * (y - meanY)
		den += dx * dx
	}
	slope := 0.0
	if den > 0 {
		slope = num / den
	}
	intercept := meanY - slope*meanX
	lastEst := max(0.0, intercept+slope*(n-1.0))

	tomorrow := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day()+1, 0, 0, 0, 0, time.UTC)
	remTodayFrac := tomorrow.Sub(nowUTC).Hours() / 24.0
	remFullDays := daysInMonth - nowUTC.Day()

	budget := float64(monthlyOutboundBudgetBytes)
	cum := float64(mtdBytes)
	exceedDay := 0
	if cum >= budget {
		exceedDay = nowUTC.Day()
	}
	projTodayRem := max(0.0, lastEst+slope) * remTodayFrac
	cum += projTodayRem
	if exceedDay == 0 && cum >= budget {
		exceedDay = nowUTC.Day()
	}
	for d := 1; d <= remFullDays; d++ {
		dayProj := max(0.0, lastEst+slope*float64(d+1))
		cum += dayProj
		if exceedDay == 0 && cum >= budget {
			exceedDay = nowUTC.Day() + d
		}
	}

	var trendPrefix string
	switch {
	case slope > 1:
		trendPrefix = fmt.Sprintf("Growing +%s/day", humanBytesDecimalFloat(slope))
	case slope < -1:
		trendPrefix = fmt.Sprintf("Flat/falling (-%s/day)", humanBytesDecimalFloat(-slope))
	default:
		trendPrefix = "Flat/falling (0 B/day)"
	}

	if cum > budget {
		out.BWForecast = fmt.Sprintf("%s — projected %s this month (EXCEEDS 10 TB around day %d)",
			trendPrefix, humanBytesDecimalFloat(cum), exceedDay)
	} else {
		out.BWForecast = fmt.Sprintf("%s — projected %s this month (within 10 TB)",
			trendPrefix, humanBytesDecimalFloat(cum))
	}
	return out
}

// humanBytesDecimal formats a byte count using decimal SI units (powers of 1000).
func humanBytesDecimal(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	return humanBytesDecimalFloat(float64(n))
}

func humanBytesDecimalFloat(v float64) string {
	if v < 0 {
		v = 0
	}
	if v < 1000 {
		return fmt.Sprintf("%.0f B", v)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	val := v / 1000.0
	idx := 0
	for val >= 1000.0 && idx < len(units)-1 {
		val /= 1000.0
		idx++
	}
	if val < 10.0 {
		return fmt.Sprintf("%.2f %s", val, units[idx])
	}
	return fmt.Sprintf("%.1f %s", val, units[idx])
}

func formatBytesRate(bytesPerSec float64) string {
	return humanBytesDecimalFloat(bytesPerSec) + "/s"
}

func humanBitsRate(bitsPerSec float64) string {
	if bitsPerSec < 0 {
		bitsPerSec = 0
	}
	if bitsPerSec < 1000 {
		return fmt.Sprintf("%.0f bit/s", bitsPerSec)
	}
	units := []string{"kbit/s", "Mbit/s", "Gbit/s", "Tbit/s"}
	val := bitsPerSec / 1000.0
	idx := 0
	for val >= 1000.0 && idx < len(units)-1 {
		val /= 1000.0
		idx++
	}
	if val < 10.0 {
		return fmt.Sprintf("%.2f %s", val, units[idx])
	}
	return fmt.Sprintf("%.1f %s", val, units[idx])
}

func format24hSpan(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%.1fh", d.Hours())
	}
}
