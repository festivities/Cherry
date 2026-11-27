package httpx

import (
	"errors"
	"strings"
	"testing"

	"cherry/internal/metrics"
)

// attackText is the shape of the 2026-11-27 crash string: one Thai letter carrying 49 stacked marks.
var attackText = "\u0e01" + strings.Repeat("\u0e39", 25) + strings.Repeat("\u0e47", 24)

// TestSafeText: ordinary text of every kind seen in production passes, and a run of marks longer than MaxMarks fails even
// when format characters sit inside it.
func TestSafeText(t *testing.T) {
	for _, c := range []struct {
		name string
		s    string
		want bool
	}{
		{"ascii", "botak tolol", true},
		{"thai vowel and tone marks", "\u0e43\u0e0a\u0e48\u0e48\u0e48", true},                                              // run 3
		{"thai emphasis", "\u0e2b\u0e27\u0e31\u0e14\u0e14\u0e35\u0e35\u0e35\u0e35\u0e35", true},                            // run 5
		{"kaomoji", "( \u2688\u0325\u0325\u0325\u0325\u0325\u0301\u2322\u2688\u0325\u0325\u0325\u0325\u0325\u0300)", true}, // run 6
		{"emoji zwj family", "\U0001F468\u200d\U0001F469\u200d\U0001F467", true},
		{"tag flag", "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F", true},
		{"exactly the cap", "a" + strings.Repeat("\u0301", MaxMarks), true},
		{"runs split by a letter", "a" + strings.Repeat("\u0301", MaxMarks) + "b" + strings.Repeat("\u0301", MaxMarks), true},
		{"one over the cap", "a" + strings.Repeat("\u0301", MaxMarks+1), false},
		{"attack string", attackText, false},
		{"dotted circle attack", "\u25cc" + strings.Repeat("\u0e48", 47), false},
		{"run split by word joiners", "a" + strings.Repeat("\u0301", 5) + "\u2060" + strings.Repeat("\u0301", 5), false},
		{"not UTF-8", "\xff", false},
	} {
		if got := SafeText(c.s); got != c.want {
			t.Errorf("%s: SafeText = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestUnmarshalNativeJSONRefusesUnsafeText: a body with an unsafe string, written plainly or as \u escapes and as a key or
// a value, is ErrUnsafeText and counted; a safe body decodes as before, with the native terminator.
func TestUnmarshalNativeJSONRefusesUnsafeText(t *testing.T) {
	escaped := `"a` + strings.Repeat(`\u0301`, MaxMarks+1) + `"`
	before := metrics.TextRefused.Load()
	for _, raw := range []string{
		`{"name":"` + attackText + `"}`,
		`{"name":` + escaped + `}`,
		`{"list":["ok",` + escaped + `]}`,
		`{` + escaped + `:"key"}`,
	} {
		var v map[string]any
		if _, err := UnmarshalNativeJSON(append([]byte(raw), 0), &v); !errors.Is(err, ErrUnsafeText) {
			t.Errorf("%s: err = %v, want ErrUnsafeText", raw, err)
		}
	}
	if got := metrics.TextRefused.Load() - before; got != 4 {
		t.Errorf("TextRefused grew by %d, want 4", got)
	}
	var v struct {
		Name string `json:"name"`
	}
	native, err := UnmarshalNativeJSON(append([]byte(`{"name":"\u0e2b\u0e27\u0e31\u0e14\u0e14\u0e35\u0e35\u0e35\u0e35\u0e35"}`), 0), &v)
	if err != nil || !native || v.Name != "\u0e2b\u0e27\u0e31\u0e14\u0e14\u0e35\u0e35\u0e35\u0e35\u0e35" {
		t.Fatalf("safe body: native=%v err=%v name=%q", native, err, v.Name)
	}
}
