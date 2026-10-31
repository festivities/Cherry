package economy

import (
	"regexp"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/opdata"
)

// TestLoungeFileValidation: the embedded default is accepted, and each broken variant of it is refused with the field
// named in the error (the origin and note, the Cherry id block, the limits, the rooms, and the strict decoder).
func TestLoungeFileValidation(t *testing.T) {
	if _, err := opdata.Decode(data.LoungeJSON, checkLounge); err != nil {
		t.Fatalf("embedded default refused: %v", err)
	}
	base := string(data.LoungeJSON)
	noteValue := regexp.MustCompile(`"note": "(?:[^"\\]|\\.)*"`)
	for _, c := range []struct {
		name, old, new, want string
	}{
		{"origin not cherry", `"origin": "cherry"`, `"origin": "mixed"`, "origin"},
		{"blank note", "", "", "note"},
		{"category outside the block", `"catgId": 1930000001`, `"catgId": 5`, "catgId"},
		{"category id 0 (the hot list)", `"catgId": 1930000001`, `"catgId": 0`, "catgId"},
		{"blank category name", `"catgName": "Lounge"`, `"catgName": " "`, "catgName"},
		{"history limit 0", `"historyLimit": 200`, `"historyLimit": 0`, "historyLimit"},
		{"text cap 501", `"maxTextChars": 500`, `"maxTextChars": 501`, "maxTextChars"},
		{"room outside the block", `"roomId": 1930000102`, `"roomId": 1900000102`, "roomId"},
		{"duplicate room", `"roomId": 1930000102`, `"roomId": 1930000101`, "unique"},
		{"room equal to the category", `"roomId": 1930000101`, `"roomId": 1930000001`, "unique"},
		{"blank room name", `"roomName": "Cozy Corner"`, `"roomName": ""`, "roomName"},
		{"room limit 0", `"limitMemberCnt": 20`, `"limitMemberCnt": 0`, "limitMemberCnt"},
		{"unknown key", `"maxTextChars": 500`, `"maxTextChars": 500, "bogus": 1`, "bogus"},
	} {
		raw := noteValue.ReplaceAllString(base, `"note": " "`) // the blank-note case
		if c.old != "" {
			raw = strings.Replace(base, c.old, c.new, 1)
			if raw == base {
				t.Fatalf("%s: pattern %q not in the default", c.name, c.old)
			}
		}
		if _, err := opdata.Decode([]byte(raw), checkLounge); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one mentioning %q", c.name, err, c.want)
		}
	}
}
