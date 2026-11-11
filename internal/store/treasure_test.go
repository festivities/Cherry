package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestTreasureStoreRoundTrip: a saved board state, its exchange counters, sets and claims come back from the database after
// a restart.
func TestTreasureStoreRoundTrip(t *testing.T) {
	path := swapStore(t)
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
	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseStore() })
	if got, ok := TreasureRecordOf("1001"); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded = %+v (%v), want %+v", got, ok, want)
	}
}

// TestTreasureFileParser: the importer's reader of treasure.json: a store of another version is refused.
func TestTreasureFileParser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "treasure.json")
	if accounts, err := parseTreasureFile(path); err != nil || len(accounts) != 0 {
		t.Fatalf("absent file: accounts %+v err %v", accounts, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"accounts":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseTreasureFile(path); err == nil {
		t.Fatal("store version 2 accepted")
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

// TestTreasureRecordCloneBuffsAndSecret: the worn list and the secret quest are copied too.
func TestTreasureRecordCloneBuffsAndSecret(t *testing.T) {
	ResetTreasure()
	t.Cleanup(ResetTreasure)
	if _, err := UpdateTreasure("1001", func(r *TreasureRecord) {
		r.Worn = []string{"CUAH006FG"}
		r.Secret = &TreasureSecret{Square: 5, Goal: 2, Count: 1}
	}); err != nil {
		t.Fatal(err)
	}
	rec, _ := TreasureRecordOf("1001")
	c := rec.Clone()
	c.Worn[0] = "CUSH003EY"
	c.Secret.Count = 9
	if got, _ := TreasureRecordOf("1001"); got.Worn[0] != "CUAH006FG" || got.Secret.Count != 1 {
		t.Fatalf("clone changed the book: %+v %+v", got.Worn, got.Secret)
	}
}
