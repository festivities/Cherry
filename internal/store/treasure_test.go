package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestTreasureStoreRoundTrip: a saved board state, its exchange counters, sets and claims come back from disk, and a store
// of another version is refused without changing the book in use.
func TestTreasureStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "treasure.json")
	if err := LoadTreasureFrom(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetTreasure)
	want, err := UpdateTreasure("1001", func(r *TreasureRecord) {
		r.Season, r.Position, r.Tickets, r.Charged, r.Jewels, r.Tutorial = 9, 4, 7, 1234, 3, true
		r.Points, r.ExDay, r.CapSteps, r.RankClaimed = 40, 77, 2, true
		r.Exchanged = map[string]int64{"4": 12}
		r.PointClaimed = []int64{1, 2}
		r.Quests = TreasureList{Set: 1, Posted: 5, Slots: []TreasureSlot{{Task: 3, Count: 2, State: 1}}}
		r.Missions = TreasureList{Set: 2, Posted: 6, Slots: []TreasureSlot{{Task: 1, Count: 0, State: 2}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := LoadTreasureFrom(path); err != nil {
		t.Fatal(err)
	}
	if got, ok := TreasureRecordOf("1001"); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded = %+v (%v), want %+v", got, ok, want)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"accounts":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadTreasureFrom(path); err == nil {
		t.Fatal("store version 2 accepted")
	}
	if got, _ := TreasureRecordOf("1001"); !reflect.DeepEqual(got, want) {
		t.Fatalf("book changed by a refused load: %+v", got)
	}
}

// TestTreasureRecordClone: a clone's maps and slices are its own, so a change to the clone leaves the book alone.
func TestTreasureRecordClone(t *testing.T) {
	ResetTreasure()
	t.Cleanup(ResetTreasure)
	if _, err := UpdateTreasure("1001", func(r *TreasureRecord) {
		r.Exchanged = map[string]int64{"2": 5}
		r.PointClaimed = []int64{1}
		r.Quests.Slots = []TreasureSlot{{Task: 0, Count: 1}}
	}); err != nil {
		t.Fatal(err)
	}
	rec, _ := TreasureRecordOf("1001")
	c := rec.Clone()
	c.Exchanged["2"] = 99
	c.PointClaimed[0] = 7
	c.Quests.Slots[0].Count = 9
	if got, _ := TreasureRecordOf("1001"); got.Exchanged["2"] != 5 || got.PointClaimed[0] != 1 || got.Quests.Slots[0].Count != 1 {
		t.Fatalf("clone changed the book: %+v", got)
	}
}
