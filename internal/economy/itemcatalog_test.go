package economy

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/itemcode"
	"cherry/internal/store"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, G: 100, B: 150, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestItemNameOverridesLiveAcrossAllKinds(t *testing.T) {
	t.Cleanup(func() { store.SetItemNamesForTest(nil) })

	// Defaults before overrides.
	if got := ItemName("CUHA0066I"); got != "Hair 0066I" {
		t.Fatalf("default hair = %q", got)
	}
	if got := ItemName("GULS0255R"); got != "Classic meadow night" {
		t.Fatalf("default landscape = %q", got)
	}
	if got := ItemName("PUPE0000G"); got != "Mixed Breed" {
		t.Fatalf("default pet = %q", got)
	}
	if got := ItemName("RUPI00007"); got != "Mixed Breed" {
		t.Fatalf("default minipet room item = %q", got)
	}
	if got := ItemName("RULA0001I"); got != "1F Room (12x12)" {
		t.Fatalf("default remodel = %q", got)
	}

	store.SetItemNamesForTest(map[string]string{
		"CUHA0066I": "Berry Ribbon Hair",
		"GULS0255R": "Starry Meadow",
		"PUPE0000G": "Good Boy",
		"RULA0001I": "Cozy Starter Floor",
	})

	if got := ItemName("CUHA0066I"); got != "Berry Ribbon Hair" {
		t.Fatalf("overridden hair = %q", got)
	}
	if got := DefaultItemName("CUHA0066I"); got != "Hair 0066I" {
		t.Fatalf("DefaultItemName changed: %q", got)
	}

	// Built-in default from data/item_names.json sits between live DB override and synthetic fallback.
	restoreBuiltin := store.SetBuiltinItemNamesForTest(map[string]string{
		"CUHA0066I": "Promoted Hair",
		"CUTO00001": "Promoted Top",
	})
	t.Cleanup(restoreBuiltin)
	if got := ItemName("CUHA0066I"); got != "Berry Ribbon Hair" {
		t.Fatalf("DB override should beat built-in: %q", got)
	}
	if got := DefaultItemName("CUHA0066I"); got != "Promoted Hair" {
		t.Fatalf("DefaultItemName should return built-in: %q", got)
	}
	if got := FallbackItemName("CUHA0066I"); got != "Hair 0066I" {
		t.Fatalf("FallbackItemName should stay synthetic: %q", got)
	}
	if got := ItemName("CUTO00001"); got != "Promoted Top" {
		t.Fatalf("ItemName should use built-in when no DB override: %q", got)
	}
	if got := ItemName("GULS0255R"); got != "Starry Meadow" {
		t.Fatalf("overridden landscape = %q", got)
	}
	if l, ok := listedLandscape("GULS0255R"); !ok || l.Name != "Starry Meadow" {
		t.Fatalf("listedLandscape = %+v %t", l, ok)
	}
	if got := PetDisplayName("PUPE0000G"); got != "Good Boy" {
		t.Fatalf("overridden PetDisplayName = %q", got)
	}
	if got := ItemName("RUPI00007"); got != "Good Boy" {
		t.Fatalf("paired minipet item did not follow pet override: %q", got)
	}
	if got, ok := RemodelItemName("RULA0001I"); !ok || got != "Cozy Starter Floor" {
		t.Fatalf("overridden RemodelItemName = %q %t", got, ok)
	}

	// POST /v4/items/dress/some returns the live override immediately.
	req := httptest.NewRequest(http.MethodPost, "/v4/items/dress/some", bytes.NewBufferString(`["CUHA0066I"]`))
	rec := httptest.NewRecorder()
	HandleItemsSome(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HandleItemsSome status %d", rec.Code)
	}
	var resp struct {
		Result []struct {
			CD    string            `json:"cd"`
			Names map[string]string `json:"names"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Result) != 1 || resp.Result[0].Names["en"] != "Berry Ribbon Hair" {
		t.Fatalf("HandleItemsSome body: %s", rec.Body.String())
	}
}

func TestCatalogItemsAndServeItemPreview(t *testing.T) {
	origRoot := t.TempDir()
	genRoot := t.TempDir()
	pngBytes := tinyPNG(t)

	// One original cosmetic item (CUHA00001), one newly donated original human face (CUEY00ZZZ) not in face_codes.txt,
	// one newly donated original animal face (CAEY00ZZZ) not in animal_face_codes.txt, one dp-only broken face (CUMO00ZZZ),
	// and one converted item (CUTO00001).
	hairID, _ := itemcode.ID("CUHA00001")
	donatedHumanFaceID, _ := itemcode.ID("CUEY00ZZZ")
	donatedAnimalFaceID, _ := itemcode.ID("CAEY00ZZZ")
	brokenFaceID, _ := itemcode.ID("CUMO00ZZZ")
	topID, _ := itemcode.ID("CUTO00001")
	writeOrigItem := func(kind string, id int64, withPart bool) {
		dir := filepath.Join(origRoot, kind, strconv.FormatInt(id, 10))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "dp.png"), pngBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		if withPart {
			if err := os.WriteFile(filepath.Join(dir, "1409_iteminfo.artsitem"), []byte("1;0;0;0;0;"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "0_1.png"), pngBytes, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeOrigItem("custom", hairID, true)
	writeOrigItem("custom", donatedHumanFaceID, true)
	writeOrigItem("custom", donatedAnimalFaceID, true)
	writeOrigItem("custom", brokenFaceID, false)

	convDir := filepath.Join(genRoot, "dress", strconv.FormatInt(topID, 10))
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(convDir, "dp.png"), pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	man := &assets.ItemManifest{Entries: []assets.ItemEntry{{
		Kind: "dress", ID: topID, Name: "dp.png", Size: int64(len(pngBytes)),
		MD5: "0123456789abcdef0123456789abcdef", Origin: assets.OriginCherry,
	}}}
	rawMan, err := assets.EncodeItemManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(genRoot, "items.json"), rawMan, 0o644); err != nil {
		t.Fatal(err)
	}

	prevArchive := assets.Archive
	prevGenRoot := assets.GeneratedItemRoot
	assets.Archive = assets.NewLocal(map[string][]string{"static/item/": {origRoot}})
	assets.GeneratedItemRoot = genRoot
	t.Cleanup(func() {
		assets.Archive = prevArchive
		assets.GeneratedItemRoot = prevGenRoot
	})

	items := CatalogItems()
	if len(items) == 0 {
		t.Fatal("CatalogItems returned empty")
	}
	hair, ok := CatalogItemByCode("CUHA00001")
	if !ok || hair.Group != "cosmetics" || hair.Category != "HA" || hair.Converted {
		t.Fatalf("CUHA00001 = %+v %t", hair, ok)
	}
	top, ok := CatalogItemByCode("CUTO00001")
	if !ok || top.Group != "cosmetics" || top.Category != "TO" || !top.Converted {
		t.Fatalf("CUTO00001 = %+v %t", top, ok)
	}
	// Newly donated original faces with part art are auto-discovered in both Face Shop and Item names;
	// dp-only broken face folders without sprites stay out.
	hface, ok := CatalogItemByCode("CUEY00ZZZ")
	if !ok || hface.Group != "faces" || hface.Species != "human" || hface.Converted || !faceForSale("CUEY00ZZZ") {
		t.Fatalf("donated human face CUEY00ZZZ = %+v ok=%t forSale=%t", hface, ok, faceForSale("CUEY00ZZZ"))
	}
	aface, ok := CatalogItemByCode("CAEY00ZZZ")
	if !ok || aface.Group != "faces" || aface.Species != "animal" || aface.Converted || !faceForSale("CAEY00ZZZ") {
		t.Fatalf("donated animal face CAEY00ZZZ = %+v ok=%t forSale=%t", aface, ok, faceForSale("CAEY00ZZZ"))
	}
	if _, ok := CatalogItemByCode("CUMO00ZZZ"); ok || faceForSale("CUMO00ZZZ") {
		t.Fatal("dp-only broken face CUMO00ZZZ should not be discovered or sold")
	}

	for _, code := range []string{"CUHA00001", "CUTO00001", "CUEY00ZZZ", "CAEY00ZZZ"} {
		rec := httptest.NewRecorder()
		if !ServeItemPreview(rec, code) {
			t.Fatalf("ServeItemPreview(%s) returned false", code)
		}
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("ServeItemPreview(%s) = %d %q", code, rec.Code, rec.Header().Get("Content-Type"))
		}
		if !bytes.Equal(rec.Body.Bytes(), pngBytes) {
			t.Fatalf("ServeItemPreview(%s) PNG mismatch", code)
		}
	}
	if ServeItemPreview(httptest.NewRecorder(), "NOTACODE") {
		t.Fatal("ServeItemPreview accepted invalid code")
	}
}
