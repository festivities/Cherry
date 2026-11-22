package economy

import (
	"regexp"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/opdata"
)

// TestStylingFileValidation: the embedded default is accepted, and each broken variant of it is refused with the field named in
// the error (the origin and note, the slot counts and their order, the price, the currency and the strict decoder).
func TestStylingFileValidation(t *testing.T) {
	if _, err := opdata.Decode(data.StylingJSON, checkStyling); err != nil {
		t.Fatalf("embedded default refused: %v", err)
	}
	base := string(data.StylingJSON)
	noteValue := regexp.MustCompile(`"note": "(?:[^"\\]|\\.)*"`)
	for _, c := range []struct {
		name, old, new, want string
	}{
		{"origin not cherry", `"origin": "cherry"`, `"origin": "mixed"`, "origin"},
		{"blank note", "", "", "note"},
		{"free slots below 0", `"free_slots": 3`, `"free_slots": -1`, "free_slots"},
		{"free slots above 100", `"free_slots": 3`, `"free_slots": 101`, "free_slots"},
		{"max below free", `"max_slots": 20`, `"max_slots": 2`, "max_slots"},
		{"max above 100", `"max_slots": 20`, `"max_slots": 101`, "max_slots"},
		{"negative price", `"slot_price": 300`, `"slot_price": -1`, "slot_price"},
		{"currency not GEM or CASH", `"coin_unit": "GEM"`, `"coin_unit": "USD"`, "coin_unit"},
		{"unknown key", `"slot_price": 300`, `"slot_price": 300, "bogus": 1`, "bogus"},
	} {
		raw := noteValue.ReplaceAllString(base, `"note": " "`) // the blank-note case
		if c.old != "" {
			raw = strings.Replace(base, c.old, c.new, 1)
			if raw == base {
				t.Fatalf("%s: pattern %q not in the default", c.name, c.old)
			}
		}
		if _, err := opdata.Decode([]byte(raw), checkStyling); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one mentioning %q", c.name, err, c.want)
		}
	}
}
