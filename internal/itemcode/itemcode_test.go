package itemcode

import "testing"

// TestIDRoundTrip: a code maps to the numeric id the client derives (attr*1e8 + type*1e7 + cat*1e5 + base36
// tail), and back. Non-canonical codes and tails of 1e5 or more do not parse.
func TestIDRoundTrip(t *testing.T) {
	for code, want := range map[string]int64{
		"RUGO007DR": 122009567, // room (R, U), GO (20): tail 007DR = 9567
		"RUTD003K2": 121704610, // room, TD (17): tail 003K2 = 4610
		"GULS0255R": 428499999, // landscape (G, U), LS (84): tail 0255R = 99999, the top of Cherry's block
		"CAFA0000S": 233900028, // animal face (cat 39): a 10.1 folder of the santi backup
		"CAEA0000E": 235700014, // animal ear (cat 57): a 10.1 folder of the santi backup
		"CATL00006": 235800006, // animal tail (cat 58): a 10.1 folder of the santi backup
	} {
		id, ok := ID(code)
		if !ok || id != want {
			t.Errorf("ID(%q) = %d %v, want %d", code, id, ok, want)
		}
		if back, ok := Code(want); !ok || back != code {
			t.Errorf("Code(%d) = %q %v, want %q", want, back, ok, code)
		}
	}
	for _, code := range []string{"RUGO007dr", "RUXX00001", "RUGO0ZZZZZ", "RUGO007D", "XUGO007DR"} {
		if id, ok := ID(code); ok {
			t.Errorf("ID(%q) = %d, want rejected", code, id)
		}
	}
}
