package store

import (
	"expvar"
	"strconv"
	"testing"
)

// TestSaveMovesCounters: a save of the account store moves cherry.saves, adds to the histogram and shows the WAL size.
func TestSaveMovesCounters(t *testing.T) {
	swapStore(t)
	count := func(name string) string {
		v := expvar.Get(name)
		if v == nil {
			t.Fatalf("%s is not published", name)
		}
		return v.String()
	}
	before, err := strconv.Atoi(count("cherry.saves"))
	if err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	_, err = newAccountLocked("")
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	after, err := strconv.Atoi(count("cherry.saves"))
	if err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("cherry.saves %d -> %d after a save", before, after)
	}
	if hist := count("cherry.save_hist"); hist == "{}" || hist == "null" {
		t.Fatalf("cherry.save_hist is empty: %s", hist)
	}
	if wal, err := strconv.Atoi(count("cherry.wal_bytes")); err != nil || wal <= 0 {
		t.Fatalf("cherry.wal_bytes = %q after a database save", count("cherry.wal_bytes"))
	}
	if n, err := strconv.Atoi(count("cherry.accounts")); err != nil || n != 1 {
		t.Fatalf("cherry.accounts = %q, want 1", count("cherry.accounts"))
	}
}
