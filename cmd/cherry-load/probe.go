package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	_ "modernc.org/sqlite" // the driver the store uses; the probe reads the WAL through its own handle

	"cherry/internal/store"
)

// heavyCodes is the owned-code count from which an account counts as heavy in the probe (seed: heavyOwned).
const heavyCodes = 1000

type probeResult struct {
	Class       string  `json:"class"`
	Saves       int     `json:"saves"`
	RowJSONMean int64   `json:"row_json_bytes_mean"`
	FramesMean  float64 `json:"wal_frames_per_save"`
	BytesMean   float64 `json:"wal_bytes_per_save"`
	LockMean    float64 `json:"save_under_lock_ms_mean"`
	LockP50     float64 `json:"save_under_lock_ms_p50"`
	LockP90     float64 `json:"save_under_lock_ms_p90"`
	LockMax     float64 `json:"save_under_lock_ms_max"`
	MarshalUs   float64 `json:"row_marshal_us_mean"` // json.Marshal of StoredFrom, the row the save writes (under the lock too)
	Mismatches  int     `json:"checkpoint_mismatches"`
}

// runProbe measures what one save writes to the WAL, for ordinary and heavy accounts and two kinds of change: a Gem balance
// (one digit of the row changes) and a new item code at the front of the owned list. It runs the store's own save on a
// COPY of a seeded directory. After each save it runs PRAGMA wal_checkpoint(PASSIVE): the log frame count it reports is
// the frames that save appended (the WAL restarts after every full checkpoint), so frames are counted exactly.
func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	dir := fs.String("data-dir", "", "a COPY of a seeded data directory (the probe writes to it)")
	saves := fs.Int("saves", 200, "saves per class")
	out := fs.String("out", "", "JSON result file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *out == "" || *saves < 1 {
		return fmt.Errorf("probe: -data-dir (a copy), -out and a positive -saves are required")
	}
	path := filepath.Join(*dir, store.DBFileName)
	if err := store.OpenStore(path); err != nil {
		return err
	}
	defer store.CloseStore()
	raw, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	var pageSize int
	if err := raw.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return err
	}

	store.AccountsMu.Lock()
	all := store.AllAccountsLocked()
	store.AccountsMu.Unlock()
	sort.Slice(all, func(i, j int) bool { return aidNum(all[i]) < aidNum(all[j]) })
	var ordinary, heavy []*store.Account
	for _, acc := range all {
		if len(acc.InventoryCodes) >= heavyCodes {
			heavy = append(heavy, acc)
		} else if len(ordinary) < 200 {
			ordinary = append(ordinary, acc)
		}
	}
	if len(ordinary) == 0 || len(heavy) == 0 {
		return fmt.Errorf("probe needs ordinary and heavy accounts (have %d and %d)", len(ordinary), len(heavy))
	}
	classes := []struct {
		name, change string
		accs         []*store.Account
	}{
		{"ordinary gem change", "gem", ordinary},
		{"ordinary item-list change", "list", ordinary},
		{"heavy gem change", "gem", heavy},
		{"heavy item-list change", "list", heavy},
	}
	var results []probeResult
	for _, c := range classes {
		res, err := probeClass(raw, pageSize, c.name, c.change, c.accs, *saves)
		if err != nil {
			return err
		}
		results = append(results, res)
	}
	js, _ := json.MarshalIndent(map[string]any{"page_size": pageSize, "wal_frame_header_bytes": 24, "results": results}, "", "  ")
	if err := os.WriteFile(*out, append(js, '\n'), 0o644); err != nil {
		return err
	}
	for _, r := range results {
		fmt.Printf("%-28s saves=%d frames/save=%.2f wal-bytes/save=%.0f row-json=%d marshal-us=%.0f lock-ms mean=%.3f p50=%.3f p90=%.3f max=%.3f mismatches=%d\n",
			r.Class, r.Saves, r.FramesMean, r.BytesMean, r.RowJSONMean, r.MarshalUs, r.LockMean, r.LockP50, r.LockP90, r.LockMax, r.Mismatches)
	}
	return nil
}

func aidNum(acc *store.Account) uint64 {
	n, _ := strconv.ParseUint(acc.Aid, 10, 64)
	return n
}

// probeClass runs saves of one change over accs (round robin) and sums the WAL frames each one appended.
func probeClass(raw *sql.DB, pageSize int, name, change string, accs []*store.Account, saves int) (probeResult, error) {
	res := probeResult{Class: name, Saves: saves}
	var frames, rowBytes int64
	var locks []time.Duration
	var marshal time.Duration
	for k := 0; k < saves; k++ {
		acc := accs[k%len(accs)]
		store.AccountsMu.Lock()
		switch change {
		case "gem":
			acc.Gems++
		case "list":
			acc.InventoryCodes = append([]string{ownedCode(100000 + k)}, acc.InventoryCodes...)
		}
		t := time.Now()
		err := store.SaveLocked(nil, acc)
		locks = append(locks, time.Since(t))
		m := time.Now()
		b, merr := json.Marshal(store.StoredFrom(acc))
		marshal += time.Since(m)
		if merr == nil {
			rowBytes += int64(len(b))
		}
		store.AccountsMu.Unlock()
		if err != nil {
			return res, err
		}
		var busy, logFrames, checkpointed int64
		if err := raw.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
			return res, err
		}
		if checkpointed != logFrames || busy != 0 {
			res.Mismatches++
		}
		frames += logFrames
	}
	res.RowJSONMean = rowBytes / int64(saves)
	res.MarshalUs = float64(marshal.Microseconds()) / float64(saves)
	res.FramesMean = float64(frames) / float64(saves)
	res.BytesMean = res.FramesMean * float64(pageSize+24)
	sort.Slice(locks, func(i, j int) bool { return locks[i] < locks[j] })
	var sum time.Duration
	for _, d := range locks {
		sum += d
	}
	res.LockMean = ms(sum / time.Duration(len(locks)))
	res.LockP50 = ms(percentile(locks, 0.50))
	res.LockP90 = ms(percentile(locks, 0.90))
	res.LockMax = ms(locks[len(locks)-1])
	return res, nil
}
