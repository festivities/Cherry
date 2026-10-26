package thumbs

import (
	"bytes"
	"image/png"
	"testing"
)

// TestLandscapeDPIsTheHexagon: the placeholder is a 130x100 PNG, opaque inside the hexagon and clear
// outside it, filled with one colour.
func TestLandscapeDPIsTheHexagon(t *testing.T) {
	b, err := LandscapeDP("1908231804")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Size(); got.X != 130 || got.Y != 100 {
		t.Fatalf("size = %v, want 130x100", got)
	}
	_, _, _, inside := img.At(65, 50).RGBA()
	_, _, _, corner := img.At(0, 0).RGBA()
	_, _, _, leftEdge := img.At(13, 50).RGBA() // centre x 13.5 is left of the hexagon's side at x 14
	if inside == 0 || corner != 0 || leftEdge != 0 {
		t.Errorf("alpha inside %d corner %d outside the left side %d", inside, corner, leftEdge)
	}
	if r, g, bl, _ := img.At(65, 50).RGBA(); r == 0 && g == 0 && bl == 0 {
		t.Error("the hexagon is black")
	}
}
