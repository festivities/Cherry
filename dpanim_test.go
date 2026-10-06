package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeAnimFixture(t *testing.T, aniproj string, sprites map[string]image.Image) string {
	t.Helper()
	dir := t.TempDir()
	for name, im := range sprites {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, im); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if err := os.WriteFile(filepath.Join(dir, "1.aniproj"), []byte(aniproj), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(im.Pix); i += 4 {
		im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return im
}

var red = color.NRGBA{255, 0, 0, 255}

func TestAnimFrameScaleKeySkipsEmptyFrame(t *testing.T) {
	// scale 0 at frame 0, 1 from frame 4; sprite 4x2 moved 10 right, 3 up.
	dir := writeAnimFixture(t, `<ANIMATIONS version="1"><SYMBOLS>
<symbol szName="IA_0" dwTotalFrame="10" dwFrameTime="32"><layer dwIndex="0">
<process dwProcessKey="0"><property dwFrame="0" szSymbolKey="a.png" dwKeepFrame="9" dwBlendType="0"/></process>
<process dwProcessKey="3"><property dwFrame="0" fPointX="10" fPointY="3" fBezierX="0" fBezierY="0" eInterpolate="-1"/></process>
<process dwProcessKey="4"><property dwFrame="0" fPointX="0" fPointY="0" eInterpolate="-1"/><property dwFrame="4" fPointX="1" fPointY="1" eInterpolate="-1"/></process>
</layer></symbol></SYMBOLS></ANIMATIONS>`, map[string]image.Image{"a.png": solid(4, 2, red)})
	if got := dpAnimSymbols(dir); !reflect.DeepEqual(got, []string{"IA_0"}) {
		t.Fatalf("symbols %v", got)
	}
	img, org, ok := dpAnimFrame(dir, "IA_0")
	if !ok {
		t.Fatal("not ok")
	}
	// symbol origin is (0,0); sprite spans x 8..12, y(up) 2..4 -> y(down) -4..-2.
	// Canvas must include the origin, so it spans x 0..12, y -4..0.
	if org != image.Pt(0, 4) || img.Bounds().Dx() != 12 || img.Bounds().Dy() != 4 {
		t.Fatalf("origin %v size %v", org, img.Bounds())
	}
	if c := img.RGBAAt(9, 1); c != (color.RGBA{255, 0, 0, 255}) {
		t.Fatalf("sprite pixel %v", c)
	}
	if c := img.RGBAAt(2, 3); c.A != 0 {
		t.Fatalf("expected transparent near origin, got %v", c)
	}
	if _, _, ok := dpAnimFrame(dir, "IA_9"); ok {
		t.Fatal("missing symbol rendered")
	}
}

func TestAnimNestedRotationAlphaAndOldDialect(t *testing.T) {
	// outer IA_1 holds IA_0 (old dialect, with "l" suffixes) rotated 90 degrees cw;
	// the sprite is 6x2 so rotated it becomes 2 wide, 6 tall around the origin.
	dir := writeAnimFixture(t, `<ANIMATIONS version="0.48.72.61"><SYMBOLS>
<symbol szName="IA_0" dwTotalFrame="10l" dwFrameTime="32l"><layer dwIndex="0">
<PROCESS_SYMBOL dwFrame="0" szSymbolKey="a.png" dwKeepFrame="9"/>
<PROCESS_COLOR dwFrame="0" byColorR="255" byColorG="255" byColorB="255" byColorA="0" eInterpolate="-1"/>
<PROCESS_COLOR dwFrame="5" byColorR="255" byColorG="255" byColorB="255" byColorA="255" eInterpolate="-1"/>
</layer></symbol>
<symbol szName="IA_1" dwTotalFrame="10" dwFrameTime="32"><layer dwIndex="0">
<PROCESS_SYMBOL dwFrame="0" szSymbolKey="IA_0" dwKeepFrame="9"/>
<PROCESS_ROTATION dwFrame="0" fRotation="90" eInterpolate="-1"/>
</layer></symbol></SYMBOLS></ANIMATIONS>`, map[string]image.Image{"a.png": solid(6, 2, red)})
	img, org, ok := dpAnimFrame(dir, "IA_1")
	if !ok {
		t.Fatal("not ok")
	}
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 6 || org != image.Pt(1, 3) {
		t.Fatalf("size %v origin %v", img.Bounds(), org)
	}
	if c := img.RGBAAt(1, 0); c.A < 250 || c.R < 250 {
		t.Fatalf("rotated sprite pixel %v", c)
	}
}

func TestAnimInvisibleOrMissing(t *testing.T) {
	dir := writeAnimFixture(t, `<ANIMATIONS version="1"><SYMBOLS>
<symbol szName="IA_0" dwTotalFrame="10" dwFrameTime="32"><layer dwIndex="0">
<process dwProcessKey="0"><property dwFrame="0" szSymbolKey="a.png" dwKeepFrame="9" dwBlendType="0"/></process>
<process dwProcessKey="7"><property dwFrame="0" byColorR="255" byColorG="255" byColorB="255" byColorA="0" eInterpolate="-1"/></process>
</layer></symbol></SYMBOLS></ANIMATIONS>`, map[string]image.Image{"a.png": solid(2, 2, red)})
	if _, _, ok := dpAnimFrame(dir, "IA_0"); ok {
		t.Fatal("alpha-0 symbol should not be visible")
	}
	if _, _, ok := dpAnimFrame(t.TempDir(), "IA_0"); ok {
		t.Fatal("no aniproj should not be ok")
	}
}

func TestItemAniParts(t *testing.T) {
	// custom ver0: one part with one ani record (fields: n, ?, z, angle, scale, alpha, dx, dy)
	custom0 := "0;1;0;1;4;200;0;0;39.5;29.0;0_0.png;1;1;0;5;30;1.000000;255;3.000000;-4.000000;0;0;0;"
	got, err := dpItemAniParts(custom0)
	if err != nil || len(got) != 1 {
		t.Fatalf("custom0 %v %v", got, err)
	}
	want := dpAniPart{Anchor: 4, Z: 200, X: 39.5, Y: 29, DX: 3, DY: -4, Angle: 30, AniZ: 5, Symbol: "IA_1"}
	if got[0] != want {
		t.Fatalf("got %+v want %+v", got[0], want)
	}
	// custom ver1 has two extra ints after the filename
	custom1 := "0;1;1;1;4;209;0;0;48.0;32.0;0_0.png;0;0;1;7;0;0;0;1.000000;255;3.000000;-32.000000;0;0;0;"
	got, err = dpItemAniParts(custom1)
	if err != nil || len(got) != 1 || got[0].Symbol != "IA_7" || got[0].DY != -32 {
		t.Fatalf("custom1 %+v %v", got, err)
	}
	// dress ver0 with a part without ani fields
	dress := "0;0;50;0;0;1;6;112;0;0;12.0;4.5;0_0.png;0;0;0;0;0;"
	got, err = dpItemAniParts(dress)
	if err != nil || len(got) != 0 {
		t.Fatalf("dress %+v %v", got, err)
	}
	if _, err := dpItemAniParts("garbage"); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestAnimRealItemOptional(t *testing.T) {
	dir := filepath.Join(dpItemRoot, "custom", "224100833")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("device item absent")
	}
	img, _, ok := dpAnimFrame(dir, "IA_1")
	if !ok || img.Bounds().Dx() < 10 {
		t.Fatalf("real item not rendered: %v %v", ok, img)
	}
}
