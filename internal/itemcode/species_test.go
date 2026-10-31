package itemcode

import "testing"

// TestSpeciesRule: hair and human faces are human-only, animal face, ear and tail parts are animal-only, clothes are
// neither, and a code of the wrong shape is neither.
func TestSpeciesRule(t *testing.T) {
	for _, c := range []struct {
		code          string
		human, animal bool
	}{
		{"CUHA00001", true, false}, {"CUHE0000M", true, false}, {"CUEY0000Q", true, false}, {"CUMO0000C", true, false},
		{"CUNO00006", true, false}, {"CUEB0000A", true, false},
		{"CAFA0000S", false, true}, {"CAEY0000D", false, true}, {"CAMO00002", false, true}, {"CAEA0000E", false, true},
		{"CATL00006", false, true}, {"CANO00001", false, true},
		{"CUTO00001", false, false}, {"CATO000GR", false, false}, {"CUAH004JH", false, false}, {"CAPA000BP", false, false},
		{"CASH00001", false, false}, {"RUCH00001", false, false}, {"", false, false}, {"CUHA0000", false, false},
	} {
		if got := HumanOnly(c.code); got != c.human {
			t.Errorf("HumanOnly(%q) = %v, want %v", c.code, got, c.human)
		}
		if got := AnimalOnly(c.code); got != c.animal {
			t.Errorf("AnimalOnly(%q) = %v, want %v", c.code, got, c.animal)
		}
	}
}
