package thumbs

import (
	"image"
	"image/color"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

const petSkeletonXML = `<dragonBones name="minipet" version="2.3"><armature name="minipet">
<skin name="default">
 <slot name="shadow" parent="shadow" z="0"><display name="image/shadow" type="image"><transform x="0" y="0" scX="1" scY="1" pX="5" pY="5"/></display></slot>
 <slot name="body" parent="body" z="2"><display name="image/body" type="image"><transform x="0" y="0" scX="1" scY="1" pX="5" pY="5"/></display></slot>
 <slot name="b_body" parent="b_body" z="1"><display name="image/body" type="image"><transform x="40" y="0" scX="1" scY="1" pX="5" pY="5"/></display></slot>
 <slot name="ef_1" parent="ef_1" z="3"><display name="image/body" type="image"><transform x="-40" y="0" scX="1" scY="1" pX="5" pY="5"/></display></slot>
</skin>
<animation name="idle"><timeline name="shadow"><frame z="0"><transform x="0" y="36"/><colorTransform aM="50"/></frame></timeline>
 <timeline name="body"><frame z="2"><transform x="0" y="0"/></frame></timeline>
 <timeline name="ef_1"><frame z="3"><transform x="0" y="0"/></frame></timeline></animation>
</armature></dragonBones>`

const petTextureXML = `<TextureAtlas name="minipet" imagePath="texture.png">
 <SubTexture name="image/shadow" x="0" y="0" width="10" height="10"/>
 <SubTexture name="image/body" x="10" y="0" width="10" height="10"/></TextureAtlas>`

func writePetFixture(t *testing.T, itemRoot, id string) {
	t.Helper()
	atlas := solidPNG(t, 20, 10, color.RGBA{R: 0xff, A: 0xff}) // red 20x10 atlas
	writeItemFile(t, itemRoot, "pet", id, "texture.png", atlas)
	writeItemFile(t, itemRoot, "pet", id, "texture.xml", []byte(petTextureXML))
	writeItemFile(t, itemRoot, "pet", id, "skeleton.xml", []byte(petSkeletonXML))
}

// petInkTouchesEdge reports whether any drawn (non-transparent) pixel lies on the outer ring of img,
// which is the same as its non-transparent bounding box touching a canvas edge, and whether anything is drawn.
func petInkTouchesEdge(img *image.RGBA) (touches, drawn bool) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.RGBAAt(x, y).A == 0 {
				continue
			}
			drawn = true
			if x == b.Min.X || y == b.Min.Y || x == b.Max.X-1 || y == b.Max.Y-1 {
				touches = true
			}
		}
	}
	return touches, drawn
}

func TestDpPetComposedAndCached(t *testing.T) {
	itemRoot, cacheDir := setDpFixture(t)
	writePetFixture(t, itemRoot, "326400017")
	rec := serveDp(t, http.MethodGet, "/arts_item_pet_326400017/dp.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	img := decodePNG(t, rec.Body.Bytes())
	if b := img.Bounds(); b.Dx() != 260 || b.Dy() != 200 {
		t.Fatalf("pet thumbnail is %v, want 260x200", b)
	}
	at := func(x, y int) color.NRGBA { return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA) }
	if c := at(int(dpPetOX), int(dpPetOY)); c.R < 250 || c.A < 250 {
		t.Fatalf("body missing at origin: %v", c)
	}
	if c := at(int(dpPetOX), int(dpPetOY)+36); c.A < 120 || c.A > 135 { // shadow at 50 percent
		t.Fatalf("shadow alpha = %d, want about 128", c.A)
	}
	if c := at(int(dpPetOX)+40, int(dpPetOY)); c.A != 0 {
		t.Fatalf("back-view slot (no idle timeline) was drawn: %v", c)
	}
	if c := at(int(dpPetOX)-40, int(dpPetOY)); c.A != 0 {
		t.Fatalf("effect slot was drawn: %v", c)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "v2_pet_326400017.png")); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
}

func TestDpPetAuthenticWinsAndBrokenIsNotFound(t *testing.T) {
	itemRoot, _ := setDpFixture(t)
	writePetFixture(t, itemRoot, "326400016")
	writeItemFile(t, itemRoot, "pet", "326400016", "dp.png", []byte("authentic pet dp"))
	if rec := serveDp(t, http.MethodGet, "/arts_item_pet_326400016/dp.png"); rec.Body.String() != "authentic pet dp" {
		t.Fatalf("authentic pet dp not served unchanged: %q", rec.Body.String())
	}
	writeItemFile(t, itemRoot, "pet", "326400018", "skeleton.xml", []byte("<not xml"))
	if rec := serveDp(t, http.MethodGet, "/arts_item_pet_326400018/dp.png"); rec.Code != http.StatusNotFound {
		t.Fatalf("broken pet = %d, want 404", rec.Code)
	}
}

// petFit keeps a fitting pose at the usual origin and scales an oversized one so that its corners
// stay inside the margin. Checked on the transform itself: rendering a 20x upscale would show the
// one-texel bilinear fade of petBlit well beyond the margin.
func TestPetFitScalesOversizedPose(t *testing.T) {
	small := petLayer{img: image.NewNRGBA(image.Rect(0, 0, 10, 10)), m: affine{1, 0, 0, 1, 0, 0}, px: 5, py: 5, alpha: 1}
	if v := petFit([]petLayer{small}); v != (affine{1, 0, 0, 1, dpPetOX, dpPetOY}) {
		t.Fatalf("fitting pose moved: %v", v)
	}
	huge := petLayer{img: image.NewNRGBA(image.Rect(0, 0, 200, 200)), m: affine{1, 0, 0, 1, 0, 0}, px: 100, py: 100, alpha: 1}
	v := petFit([]petLayer{huge})
	const eps = 1e-9
	for _, c := range [4][2]float64{{-100, -100}, {100, -100}, {100, 100}, {-100, 100}} {
		x, y := v[0]*c[0]+v[2]*c[1]+v[4], v[1]*c[0]+v[3]*c[1]+v[5]
		if x < dpPetMargin-eps || x > dpPetW-dpPetMargin+eps || y < dpPetMargin-eps || y > dpPetH-dpPetMargin+eps {
			t.Fatalf("corner %v lands at (%.2f, %.2f), outside the %v px margin", c, x, y, dpPetMargin)
		}
	}
	if v[0] >= 1 {
		t.Fatalf("oversized pose not scaled down: scale %v", v[0])
	}
}

// Ride pets (326400242 is PUPE0006Q) are far larger than the shop canvas at 1:1. Skipped when the archive is absent.
func TestDpPetRideFitsCanvas(t *testing.T) {
	dir := filepath.Join(DpItemRoot, "pet", "326400242")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("ride pet archive absent")
	}
	img, ok := composePetDP(dir)
	if !ok {
		t.Fatal("ride pet 326400242 not composed")
	}
	if touches, drawn := petInkTouchesEdge(img); touches || !drawn {
		t.Fatalf("ride pet cropped or empty: touches edge = %v, drawn = %v", touches, drawn)
	}
}
