package admin

import (
	"testing"

	"cherry/internal/lpn"
)

// TestPlaceTextNamesTheSquareRoom: a Square is named by its list name and its room number; a theme the operator file does
// not know keeps the theme id.
func TestPlaceTextNamesTheSquareRoom(t *testing.T) {
	if got := placeText(lpn.Place{Kind: "square", ID: "6", Room: "2"}); got != "Treasure Island room 2" {
		t.Fatalf("placeText = %q, want %q", got, "Treasure Island room 2")
	}
	if got := placeText(lpn.Place{Kind: "square", ID: "999", Room: "1"}); got != "Square theme 999 room 1" {
		t.Fatalf("unknown theme = %q, want %q", got, "Square theme 999 room 1")
	}
}
