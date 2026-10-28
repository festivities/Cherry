package web

import (
	"regexp"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/opdata"
)

// TestWebOperatorFilesNeedOrigin: dressup.json and menu.json refuse a document with no origin or an empty note.
func TestWebOperatorFilesNeedOrigin(t *testing.T) {
	origin := regexp.MustCompile(`"origin":\s*"[^"]*",?`)
	note := regexp.MustCompile(`"note":\s*"[^"]*"`)
	for name, c := range map[string]struct {
		raw   []byte
		check func([]byte) error
	}{
		"dressup.json": {data.DressupJSON, func(b []byte) error { _, err := opdata.Decode(b, checkDressup); return err }},
		"menu.json":    {data.MenuJSON, func(b []byte) error { _, err := opdata.Decode(b, checkMenu); return err }},
	} {
		if err := c.check(c.raw); err != nil {
			t.Fatalf("%s: embedded default refused: %v", name, err)
		}
		if err := c.check([]byte(origin.ReplaceAllString(string(c.raw), ""))); err == nil || !strings.Contains(err.Error(), "origin") {
			t.Errorf("%s without origin: err = %v", name, err)
		}
		if err := c.check([]byte(note.ReplaceAllString(string(c.raw), `"note": ""`))); err == nil || !strings.Contains(err.Error(), "note") {
			t.Errorf("%s with an empty note: err = %v", name, err)
		}
	}
}
