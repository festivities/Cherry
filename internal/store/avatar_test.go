package store

import (
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
