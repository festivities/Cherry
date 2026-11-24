package store

import (
	"encoding/json"
	"slices"
	"testing"
)

// TestAnimalBaseItems: an animal account's appearance fills its missing clothes slots from the animal base (never the
// human base); a human account's appearance is unchanged. The base codes are skipped by save/v2 for their own species
// only: a human save never skips an animal code, and an animal save skips both bases, as before.
func TestAnimalBaseItems(t *testing.T) {
	animal := AppearanceItemCodes("ANIMAL", []string{"CAEY00002", "CANO00001", "CAMO00002"})
	wantAnimal := []string{"CAEY00002", "CANO00001", "CAMO00002", "CATO000L8", "CAPA000BP", "CASH00001", "CAFA0005O"}
	if !slices.Equal(animal, wantAnimal) {
		t.Errorf("animal appearance = %v, want %v", animal, wantAnimal)
	}

	female := AppearanceItemCodes("FEMALE", []string{"CUEY00002"})
	wantFemale := []string{"CUEY00002", "CUTO0011X", "CUPA000LO", "CUSH0009Q", "CUHA00004"}
	if !slices.Equal(female, wantFemale) {
		t.Errorf("female appearance = %v, want %v", female, wantFemale)
	}

	for _, tc := range []struct {
		gender, code string
		want         bool
	}{
		{"FEMALE", "CUTO0011X", true},
		{"FEMALE", "CUHA00001", true},
		{"MALE", "CUHA00004", true},
		{"FEMALE", "CATO000L8", false},
		{"FEMALE", "CAFA0005O", false},
		{"ANIMAL", "CATO000L8", true},
		{"ANIMAL", "CAFA0005O", true},
		{"ANIMAL", "CUHA00004", true},
		{"ANIMAL", "CAEY00002", false},
		{"FEMALE", "CAEY00002", false},
	} {
		if got := IsAvatarBaseItem(tc.gender, tc.code); got != tc.want {
			t.Errorf("IsAvatarBaseItem(%s, %s) = %v, want %v", tc.gender, tc.code, got, tc.want)
		}
	}
}

func TestGardenStatsLevelAndClone(t *testing.T) {
	for _, tc := range []struct {
		exp  int64
		want int
	}{
		{-5, 1},
		{0, 1},
		{99, 1},
		{100, 2},
		{250, 3},
		{9799, 98},
		{9800, 99},
		{50000, 99},
	} {
		if got := (GardenStats{Exp: tc.exp}).Level(); got != tc.want {
			t.Errorf("GardenStats{Exp: %d}.Level() = %d, want %d", tc.exp, got, tc.want)
		}
	}

	orig := &Account{
		Aid: "9101",
		GardenStats: GardenStats{
			Exp:         120,
			Footprints:  5,
			TodayDay:    20500,
			TodayVisits: 2,
			TodaySeen:   []string{"9102", "9103"},
			UserPlanet:  2,
		},
	}
	stored := StoredFrom(orig)
	if stored.GardenStats == nil {
		t.Fatal("StoredFrom dropped non-zero GardenStats")
	}
	stored.GardenStats.TodaySeen[0] = "mutated"
	if orig.GardenStats.TodaySeen[0] != "9102" {
		t.Fatal("StoredFrom aliased TodaySeen slice")
	}
	stored.GardenStats.TodaySeen[0] = "9102"
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var back StoredAccount
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	restored := accountFromStored(back)
	if restored.GardenStats.Exp != 120 || restored.GardenStats.Footprints != 5 || restored.GardenStats.TodayDay != 20500 ||
		restored.GardenStats.TodayVisits != 2 || restored.GardenStats.UserPlanet != 2 ||
		!slices.Equal(restored.GardenStats.TodaySeen, []string{"9102", "9103"}) {
		t.Fatalf("round-trip GardenStats = %+v", restored.GardenStats)
	}
	back.GardenStats.TodaySeen[0] = "mutated2"
	if restored.GardenStats.TodaySeen[0] != "9102" {
		t.Fatal("accountFromStored aliased TodaySeen slice")
	}
	if empty := StoredFrom(&Account{Aid: "9104"}); empty.GardenStats != nil {
		t.Fatalf("StoredFrom zero GardenStats = %+v, want nil", empty.GardenStats)
	}
}
