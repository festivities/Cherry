package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTreasureStoreRoundTrip: a saved board state comes back from disk, and a store of another version is refused
// without changing the book in use.
func TestTreasureStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "treasure.json")
	if err := LoadTreasureFrom(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetTreasure)
	want, err := UpdateTreasure("1001", func(r *TreasureRecord) {
		r.Season, r.Position, r.Tickets, r.Charged, r.Jewels, r.Tutorial = 9, 4, 7, 1234, 3, true
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := LoadTreasureFrom(path); err != nil {
		t.Fatal(err)
	}
	if got, ok := TreasureRecordOf("1001"); !ok || got != want {
		t.Fatalf("reloaded = %+v (%v), want %+v", got, ok, want)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"accounts":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadTreasureFrom(path); err == nil {
		t.Fatal("store version 2 accepted")
	}
	if got, _ := TreasureRecordOf("1001"); got != want {
		t.Fatalf("book changed by a refused load: %+v", got)
	}
}
