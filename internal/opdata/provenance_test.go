package opdata

import (
	"strings"
	"testing"
)

// TestProvenanceRequiresOriginAndNote: a data file must declare the origin it is checked for and a note.
func TestProvenanceRequiresOriginAndNote(t *testing.T) {
	type marked struct {
		Provenance
		N int `json:"n"`
	}
	ok := marked{Provenance{Origin: OriginMixed, Note: "ids original, names Cherry-made"}, 1}
	if err := ok.CheckOrigin(OriginMixed); err != nil {
		t.Fatalf("valid marking refused: %v", err)
	}
	for name, v := range map[string]marked{
		"wrong origin": {Provenance{Origin: OriginCherry, Note: "x"}, 1},
		"no origin":    {Provenance{Note: "x"}, 1},
		"blank note":   {Provenance{Origin: OriginMixed, Note: " \t"}, 1},
	} {
		if err := v.CheckOrigin(OriginMixed); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if _, err := Decode([]byte(`{"n":1,"origin":"cherry","note":"x"}`), func(m *marked) error { return m.CheckOrigin(OriginCherry) }); err != nil {
		t.Fatalf("decode with marking: %v", err)
	}
	if _, err := Decode([]byte(`{"n":1}`), func(m *marked) error { return m.CheckOrigin(OriginCherry) }); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("unmarked document: err = %v", err)
	}
}
