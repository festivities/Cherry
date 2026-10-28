package store

import (
	"regexp"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/opdata"
)

// TestProfilesFileNeedsOrigin: profiles.json refuses a document with no origin or an empty note (the marking).
func TestProfilesFileNeedsOrigin(t *testing.T) {
	origin := regexp.MustCompile(`"origin":\s*"[^"]*",?`)
	note := regexp.MustCompile(`"note":\s*"[^"]*"`)
	if _, err := opdata.Decode(data.ProfilesJSON, checkProfiles); err != nil {
		t.Fatalf("embedded default refused: %v", err)
	}
	if _, err := opdata.Decode([]byte(origin.ReplaceAllString(string(data.ProfilesJSON), "")), checkProfiles); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Errorf("without origin: err = %v", err)
	}
	if _, err := opdata.Decode([]byte(note.ReplaceAllString(string(data.ProfilesJSON), `"note": ""`)), checkProfiles); err == nil || !strings.Contains(err.Error(), "note") {
		t.Errorf("with an empty note: err = %v", err)
	}
}
