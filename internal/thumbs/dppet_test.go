package thumbs

import (
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
<animation name="idle"><timeline name="shadow"><frame z="0"><transform x="0" y="40"/><colorTransform aM="50"/></frame></timeline>
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
	if c := at(int(dpPetOX), int(dpPetOY)+40); c.A < 120 || c.A > 135 { // shadow at 50 percent
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
