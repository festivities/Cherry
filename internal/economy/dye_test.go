package economy

import (
	"bytes"
	"image/png"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
)

// TestDyeCatalogRefusesBadFiles: each mistake in data/dyes.json stops Cherry at startup with the entry named.
func TestDyeCatalogRefusesBadFiles(t *testing.T) {
	good := string(data.DyesJSON)
	for _, c := range []struct{ name, old, new, want string }{
		{"no origin", `"origin": "cherry",`, ``, "origin"},
		{"garment the client does not flag", `224103060`, `224103061`, "not one of the garments the client flags"},
		{"dye outside the Cherry block", `"item_no": 9510`, `"item_no": 9600`, "Cherry block"},
		{"free dye", `"price": 200`, `"price": 0`, "price must be at least 1 Gem"},
		{"random odds not summing to 1", `{"color": "#212121", "prob": 0.125}]`, `{"color": "#212121", "prob": 0.5}]`, "sum to"},
		{"colour not #rrggbb", `{"color": "#E53935", "prob": 1}`, `{"color": "red", "prob": 1}`, "must be #rrggbb"},
		{"palette colour not #rrggbb", `"#90A4AE"`, `"#90A4AEZ"`, "palette"},
		{"custom dye with its own colours", `"kind": "CUSTOM", "price": 200, "colors": []`, `"kind": "CUSTOM", "price": 200, "colors": [{"color": "#E53935", "prob": 1}]`, "CUSTOM"},
	} {
		if !strings.Contains(good, c.old) {
			t.Fatalf("%s: the edit target %q is not in dyes.json", c.name, c.old)
		}
		_, err := opdata.Decode([]byte(strings.Replace(good, c.old, c.new, 1)), checkDyes)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// TestDyeableGarmentsDecode: every flagged garment decodes to an item code and back to the same numeric id, and the
// client flags exactly the fifteen garments of the colorant spec (12 custom, 3 dress).
func TestDyeableGarmentsDecode(t *testing.T) {
	if len(dyeFlagged) != 15 {
		t.Fatalf("flagged garments = %d, want 15", len(dyeFlagged))
	}
	for id := range dyeFlagged {
		code, ok := itemcode.Code(id)
		if !ok {
			t.Errorf("garment %d has no item code", id)
			continue
		}
		if back, ok := itemcode.ID(code); !ok || back != id {
			t.Errorf("garment %d: code %s decodes to %d (ok %v)", id, code, back, ok)
		}
	}
}

// TestDyeIconIsAPNGInTheDyeColour: the drawn drop is a 96-pixel PNG whose bulb takes the dye colour.
func TestDyeIconIsAPNGInTheDyeColour(t *testing.T) {
	b, err := dyeIconPNG([]string{"#E53935"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != dyeIconSize || img.Bounds().Dy() != dyeIconSize {
		t.Fatalf("bounds = %v", img.Bounds())
	}
	r, g, bl, a := img.At(dyeIconSize/2, dyeIconCY).RGBA()
	if r>>8 != 0xE5 || g>>8 != 0x39 || bl>>8 != 0x35 || a>>8 != 0xFF {
		t.Fatalf("bulb colour = %02x%02x%02x alpha %02x", r>>8, g>>8, bl>>8, a>>8)
	}
}
