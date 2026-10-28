package lpn

import (
	"regexp"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/opdata"
)

// TestSquaresFileNeedsOrigin: squares.json refuses a document with no origin or an empty note (the marking).
func TestSquaresFileNeedsOrigin(t *testing.T) {
	origin := regexp.MustCompile(`"origin":\s*"[^"]*",?`)
	note := regexp.MustCompile(`"note":\s*"[^"]*"`)
	if _, err := opdata.Decode(data.SquaresJSON, checkSquares); err != nil {
		t.Fatalf("embedded default refused: %v", err)
	}
	if _, err := opdata.Decode([]byte(origin.ReplaceAllString(string(data.SquaresJSON), "")), checkSquares); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Errorf("without origin: err = %v", err)
	}
	if _, err := opdata.Decode([]byte(note.ReplaceAllString(string(data.SquaresJSON), `"note": ""`)), checkSquares); err == nil || !strings.Contains(err.Error(), "note") {
		t.Errorf("with an empty note: err = %v", err)
	}
}
