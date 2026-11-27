package economy

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"testing/fstest"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

const iconFixture = `<?xml version="1.0" encoding="UTF-8" ?>
<ANIMATIONS name="NHNArts Animation" version="1"><PNGS><png path="./icon_effect/bg.png" /><png path="./icon_effect/fg.png" /></PNGS>
<SYMBOLS>
<symbol szName="btn" dwTotalFrame="30" dwFrameTime="33">
 <layer dwIndex="0" szName="L0"><process dwProcessKey="0" numChild="1"><property dwFrame="0" szSymbolKey="bg.png" dwKeepFrame="29" /></process></layer>
 <layer dwIndex="1" szName="L1"><process dwProcessKey="0" numChild="1"><property dwFrame="0" szSymbolKey="stars" dwKeepFrame="29" /></process></layer>
 <layer dwIndex="2" szName="L2"><process dwProcessKey="0" numChild="1"><property dwFrame="0" szSymbolKey="fg.png" dwKeepFrame="29" /></process>
  <process dwProcessKey="3" numChild="2"><property dwFrame="0" fPointX="2" fPointY="1" /><property dwFrame="14" fPointX="9" fPointY="9" /></process></layer>
</symbol>
<symbol szName="stars" dwTotalFrame="30" dwFrameTime="33"><layer dwIndex="0" szName="L0"><process dwProcessKey="0" numChild="1"><property dwFrame="5" szSymbolKey="bg.png" /></process></layer></symbol>
</SYMBOLS></ANIMATIONS>`

func pngOf(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func useIconFixture(t *testing.T) {
	t.Helper()
	old := iconEffectFS
	iconEffectFS = fstest.MapFS{
		"icon_effect.aniproj": {Data: []byte(iconFixture)},
		"icon_effect/bg.png":  {Data: pngOf(t, 10, 10, color.NRGBA{255, 0, 0, 255})},
		"icon_effect/fg.png":  {Data: pngOf(t, 2, 2, color.NRGBA{0, 0, 255, 255})},
	}
	reset := func() {
		iconEffect.Lock()
		iconEffect.symbols, iconEffect.previews = nil, nil
		iconEffect.Unlock()
	}
	reset()
	t.Cleanup(func() { iconEffectFS = old; reset() })
}

// TestIconEffect: only symbols with a frame-0 picture are offered; the preview stacks the layers at their frame-0 positions
// (y up), the nested symbol and later frames ignored.
func TestIconEffect(t *testing.T) {
	useIconFixture(t)
	names, err := IconEffectSymbols()
	if err != nil || len(names) != 1 || names[0] != "btn" {
		t.Fatalf("symbols = %v %v", names, err)
	}
	if !IsIconEffectSymbol("btn") || IsIconEffectSymbol("stars") || IsIconEffectSymbol("nope") {
		t.Fatal("IsIconEffectSymbol wrong")
	}
	b, ok := IconEffectPreview("btn")
	if !ok {
		t.Fatal("no preview")
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 10 || img.Bounds().Dy() != 10 {
		t.Fatalf("preview size %v", img.Bounds())
	}
	// fg (2x2) centred at (+2, -1 in y-down) over bg centred at 0: canvas origin is (-5,-5), so fg covers x 6..7, y 3..4.
	if r, _, bl, _ := img.At(6, 3).RGBA(); bl == 0 || r != 0 {
		t.Fatalf("fg not at its position: %v", img.At(6, 3))
	}
	if r, _, _, _ := img.At(0, 0).RGBA(); r == 0 {
		t.Fatal("bg missing")
	}
}

// TestHomeIconGardenButtons: the owner's buttons come first, the season icon after them.
func TestHomeIconGardenButtons(t *testing.T) {
	testutil.UseDatabase(t)
	pinPopupClock(t, jstAt(26, 12, 0))
	for _, b := range []store.GardenButton{
		{Icon: "72_home_l_my_garden", Label: "Closet", Link: "goSomewhere(closet)"},
		{Icon: "87_home_free_gift", Label: "Discord", Link: LinkBrowser, Detail: "https://example.com/x"},
	} {
		if err := store.AddGardenButton(b, "dev-owner", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	_, res := homeIconReply(t)
	rows, _ := res["home_icon"].([]any)
	if len(rows) != 3 {
		t.Fatalf("home_icon = %#v", res["home_icon"])
	}
	want := []struct{ id, link, detail string }{
		{"72_home_l_my_garden", "goSomewhere(closet)", ""},
		{"87_home_free_gift", "openWithBrowser", "https://example.com/x"},
		{"halloween_17s", "goSomewhere(gachaShop)", ""},
	}
	for i, w := range want {
		row := rows[i].(map[string]any)
		if row["iconSeq"] != float64(i+1) || row["iconId"] != w.id || row["linkTypeCode"] != w.link || row["linkDetail"] != w.detail {
			t.Fatalf("row %d = %v", i, row)
		}
	}
}
