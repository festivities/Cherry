package economy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/store"
	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

// requireArchive skips a test that needs the item archive (the clothing products have art there).
func requireArchive(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(thumbs.DpItemRoot, "dress")); err != nil {
		t.Skip("item archive not present")
	}
}

// defaultAtelier parses a fresh copy of the embedded recipe file, so a test can edit it.
func defaultAtelier(t *testing.T) *Atelier {
	t.Helper()
	a, err := parseAtelier(data.AtelierJSON)
	if err != nil {
		t.Fatalf("default recipes: %v", err)
	}
	return a
}

// TestAtelierDefaultFileLoads: the embedded default validates, and its clothing products are sellable
// with art in the item archive (the startup check).
func TestAtelierDefaultFileLoads(t *testing.T) {
	a := defaultAtelier(t)
	requireArchive(t)
	if err := checkAtelierProducts(a); err != nil {
		t.Fatalf("default recipes: %v", err)
	}
	if got := a.FirstMaterialTheme(); got != 1012 {
		t.Fatalf("first material theme = %d, want 1012", got)
	}
}

// TestAtelierLoaderRejects: each bad entry is refused with an error that names it.
func TestAtelierLoaderRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(a *Atelier)
		want string
	}{
		{"duplicate design", func(a *Atelier) { a.Designs[1].ID = a.Designs[0].ID }, "design 101201: id must be unique"},
		{"unknown theme", func(a *Atelier) { a.Designs[0].Theme = 9999 }, "design 101201: theme 9999 is not in themes"},
		{"unknown input", func(a *Atelier) { a.Designs[0].Inputs[0].Item = 999999 }, "design 101201: input 999999 is neither"},
		{"negative count", func(a *Atelier) { a.Designs[0].Inputs[0].Count = -1 }, "design 101201: input 110000 count -1"},
		{"zero count", func(a *Atelier) { a.Designs[0].Inputs[0].Count = 0 }, "count 0 must be at least 1"},
		{"negative stamina", func(a *Atelier) { a.Designs[0].Stamina = -5 }, "design 101201: stamina must not be negative"},
		{"negative price", func(a *Atelier) { a.Designs[6].Price = -1 }, "design 101301: blueprint_smiles must not be negative"},
		{"material kind on clothes theme", func(a *Atelier) { a.Designs[0].Theme = 1013 }, "design 101201: a material product needs a material theme"},
		{"clothes kind on material theme", func(a *Atelier) { a.Designs[6].Theme = 1012 }, "design 101301: a clothing product needs a clothes theme"},
		{"duplicate theme", func(a *Atelier) { a.Themes[1].ID = 1012 }, "theme 1012: duplicate id"},
		{"theme kind", func(a *Atelier) { a.Themes[0].Kind = "gems" }, `theme 1012: kind must be "material" or "clothes", not "gems"`},
		{"theme without designs", func(a *Atelier) {
			a.Themes = append(a.Themes, AtelierTheme{ID: 1014, Kind: AtelierKindMaterial, Name: "x", Banner: "b", Icon: "i"})
		}, "theme 1014 has no designs"},
		{"no material theme", func(a *Atelier) { a.Themes = a.Themes[1:] }, "no material theme"},
		{"good is a gather drop", func(a *Atelier) { a.Goods = append(a.Goods, AtelierGood{ID: 110000, Name: "x"}) }, "good 110000: id is a gathering drop"},
		{"both products", func(a *Atelier) { a.Designs[0].ProductClothes = "CUAH0066I" }, "design 101201: set exactly one of product_material and product_clothes"},
		{"product not a good", func(a *Atelier) { a.Designs[0].ProductMaterial = 499999 }, "design 101201: product_material 499999 is not in goods"},
		{"no inputs", func(a *Atelier) { a.Designs[0].Inputs = nil }, "design 101201: needs at least one input"},
		{"input twice", func(a *Atelier) {
			a.Designs[0].Inputs = append(a.Designs[0].Inputs, AtelierInput{Item: 110000, Count: 1})
		}, "design 101201: input 110000 is listed twice"},
		{"clothing product made twice", func(a *Atelier) { a.Designs[7].ProductClothes = a.Designs[6].ProductClothes }, "is also made by design 101301"},
		{"stamina max", func(a *Atelier) { a.Stamina.Max = 0 }, "stamina.max must be at least 1"},
		{"regen seconds", func(a *Atelier) { a.Stamina.RegenSeconds = 0 }, "stamina.regen_seconds must be at least 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := defaultAtelier(t)
			c.mut(a)
			err := a.check()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// TestAtelierLoaderRejectsUnknownKeys: a typo in a key is an error, not a silently ignored field.
func TestAtelierLoaderRejectsUnknownKeys(t *testing.T) {
	raw := bytes.Replace(data.AtelierJSON, []byte(`"max": 100`), []byte(`"maxx": 100`), 1)
	if _, err := parseAtelier(raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown field", err)
	}
}

// TestAtelierClothingProductNeedsArt: a clothing code without art (or not clothing) fails the startup check.
func TestAtelierClothingProductNeedsArt(t *testing.T) {
	a := defaultAtelier(t)
	a.Designs[6].ProductClothes = "CUAH0000X" // a hat code with no item folder
	if err := checkAtelierProducts(a); err == nil || !strings.Contains(err.Error(), "design 101301: product CUAH0000X has no art") {
		t.Fatalf("error = %v", err)
	}
	a = defaultAtelier(t)
	a.Designs[6].ProductClothes = "RUTA0001A" // an interior code is not clothing
	if err := checkAtelierProducts(a); err == nil || !strings.Contains(err.Error(), "is not clothing") {
		t.Fatalf("error = %v", err)
	}
}

// TestAtelierOverrideReplacesDefault: an operator file replaces the default; a bad one is refused
// and leaves the active recipes unchanged; a missing file keeps the default.
func TestAtelierOverrideReplacesDefault(t *testing.T) {
	t.Cleanup(func() { atelierFile.Reset() })
	dir := t.TempDir()
	path := filepath.Join(dir, "atelier.json")
	override := `{"origin":"mixed","note":"test","stamina":{"max":3,"regen_seconds":60},
		"themes":[{"id":7,"kind":"material","name":"Test","information":"","banner":"b.png","icon":"i.png"}],
		"goods":[{"id":490100,"name":"Test Good"}],
		"designs":[{"id":101201,"theme":7,"product_material":490100,"inputs":[{"item":210000,"count":2}],"stamina":1,"blueprint_smiles":0}]}`
	if err := os.WriteFile(path, []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atelierFile.LoadFrom(path); err != nil {
		t.Fatalf("override: %v", err)
	}
	if a := ActiveAtelier(); a.Stamina.Max != 3 || len(a.Designs) != 1 || a.Designs[0].ID != 101201 {
		t.Fatalf("override not active: %+v", a)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(strings.Replace(override, `"item":210000`, `"item":5`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atelierFile.LoadFrom(bad); err == nil || !strings.Contains(err.Error(), "design 101201: input 5") || !strings.Contains(err.Error(), bad) {
		t.Fatalf("bad override error = %v", err)
	}
	if a := ActiveAtelier(); len(a.Designs) != 1 || a.Designs[0].ID != 101201 {
		t.Fatal("a refused override changed the active recipes")
	}

	atelierFile.Reset()
	if err := atelierFile.LoadFrom(filepath.Join(dir, "missing.json")); err != nil {
		t.Fatalf("missing override: %v", err)
	}
	if a := ActiveAtelier(); a.Stamina.Max != 100 || len(a.Designs) != 10 {
		t.Fatalf("missing override should keep the default, got max %d designs %d", a.Stamina.Max, len(a.Designs))
	}
}

// atelierAccount installs aid with gems, smiles, a bag and an Atelier record, saved into a temp dir.
func atelierAccount(t *testing.T, aid string, smiles int64, bag []store.GardenBagStack, atelier *store.AtelierState) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"atok-" + aid: {AccessToken: "atok-" + aid, SessionKey: "sk", Aid: aid, Name: "Atelier", Smiles: smiles, GardenBag: bag, Atelier: atelier}})
	testutil.SetLab(t, aid, false)
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Unlock()
}

func atelierBag(t *testing.T, aid string) map[int64]int64 {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	out := map[int64]int64{}
	for _, s := range store.AccountByAidLocked(aid).GardenBag {
		out[s.Item] = s.Count
	}
	return out
}

var atelierNow = time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)

// TestAtelierStaminaRegen: stamina spends, regenerates one point per regen_seconds with the progress
// kept, and caps at max with no timer.
func TestAtelierStaminaRegen(t *testing.T) {
	atelierAccount(t, "9601", 0, []store.GardenBagStack{{Item: 110000, Count: 3}, {Item: 210000, Count: 1}}, nil)
	if s := AtelierStatus("9601", atelierNow); s.Stamina != 100 || s.RemainSec != 0 || s.ChargeSec != 300 {
		t.Fatalf("new account stamina = %+v", s)
	}
	code, s := AtelierMakeMaterial("9601", 101201, 1, atelierNow)
	if code != AtelierOK || s.Stamina != 95 || s.RemainSec != 300 {
		t.Fatalf("make = %d, %+v", code, s)
	}
	if s := AtelierStatus("9601", atelierNow.Add(450*time.Second)); s.Stamina != 96 || s.RemainSec != 150 {
		t.Fatalf("after 450 s = %+v, want stamina 96 with 150 s to the next point", s)
	}
	if s := AtelierStatus("9601", atelierNow.Add(time.Hour)); s.Stamina != 100 || s.RemainSec != 0 {
		t.Fatalf("after an hour = %+v, want full stamina and no timer", s)
	}
}

// TestAtelierBlueprintPurchase: a clothes blueprint costs Smiles (refused when short, spent once);
// a free design is already open.
func TestAtelierBlueprintPurchase(t *testing.T) {
	atelierAccount(t, "9602", 10, nil, nil)
	if code, s := AtelierOpenDesign("9602", 101301, atelierNow); code != AtelierNotSmiles || s.Smiles != 10 || s.Unlocked(mustDesign(t, 101301)) {
		t.Fatalf("short open = %d, %+v", code, s)
	}
	store.AccountsMu.Lock()
	store.AccountByAidLocked("9602").Smiles = 20
	store.AccountsMu.Unlock()
	code, s := AtelierOpenDesign("9602", 101301, atelierNow)
	if code != AtelierOK || s.Smiles != 5 || !s.Unlocked(mustDesign(t, 101301)) {
		t.Fatalf("open = %d, %+v", code, s)
	}
	if code, _ := AtelierOpenDesign("9602", 101301, atelierNow); code != AtelierAlreadyOpen {
		t.Fatalf("second open = %d, want %d", code, AtelierAlreadyOpen)
	}
	if code, _ := AtelierOpenDesign("9602", 101201, atelierNow); code != AtelierAlreadyOpen {
		t.Fatalf("free design open = %d, want %d", code, AtelierAlreadyOpen)
	}
}

func mustDesign(t *testing.T, id int64) AtelierDesign {
	t.Helper()
	d, ok := ActiveAtelier().Design(id)
	if !ok {
		t.Fatalf("design %d missing", id)
	}
	return d
}

// TestAtelierMakeMaterialIsAllOrNothing: a make consumes its inputs (emptied stacks leave the Bag),
// adds the goods and spends the stamina; a short Bag or stamina changes nothing.
func TestAtelierMakeMaterialIsAllOrNothing(t *testing.T) {
	atelierAccount(t, "9603", 0, []store.GardenBagStack{{Item: 110000, Count: 2}, {Item: 210000, Count: 1}}, nil)
	if code, s := AtelierMakeMaterial("9603", 101201, 1, atelierNow); code != AtelierItems || s.Stamina != 100 {
		t.Fatalf("short bag = %d, %+v", code, s)
	}
	if bag := atelierBag(t, "9603"); bag[110000] != 2 || bag[210000] != 1 || len(bag) != 2 {
		t.Fatalf("bag changed on refusal: %v", bag)
	}

	store.AccountsMu.Lock()
	store.AccountByAidLocked("9603").GardenBag = []store.GardenBagStack{{Item: 110000, Count: 4}, {Item: 210000, Count: 1}, {Item: 490001, Count: 2}}
	store.AccountsMu.Unlock()
	code, s := AtelierMakeMaterial("9603", 101201, 1, atelierNow)
	if code != AtelierOK || s.Stamina != 95 {
		t.Fatalf("make = %d, %+v", code, s)
	}
	if bag := atelierBag(t, "9603"); bag[110000] != 1 || bag[210000] != 0 || bag[490000] != 1 || bag[490001] != 2 {
		t.Fatalf("bag after make = %v", bag)
	}
	store.AccountsMu.Lock()
	if _, has := atelierBagIndex(store.AccountByAidLocked("9603").GardenBag, 210000); has {
		t.Fatal("empty stack was kept")
	}
	store.AccountsMu.Unlock()

	store.AccountsMu.Lock()
	store.AccountByAidLocked("9603").Atelier = &store.AtelierState{Stamina: 4, StaminaAt: atelierNow.UnixMilli()}
	store.AccountsMu.Unlock()
	if code, s := AtelierMakeMaterial("9603", 101201, 1, atelierNow); code != AtelierStamina || s.Stamina != 4 {
		t.Fatalf("short stamina = %d, %+v", code, s)
	}
}

func atelierBagIndex(bag []store.GardenBagStack, item int64) (int, bool) {
	i := slices.IndexFunc(bag, func(s store.GardenBagStack) bool { return s.Item == item })
	return i, i >= 0
}

// TestAtelierMakeClothesNeedsBlueprintAndRefusesOwned: clothes need the blueprint, consume their
// inputs and stamina, enter the Closet, and are never made twice (no Gem payout).
func TestAtelierMakeClothesNeedsBlueprintAndRefusesOwned(t *testing.T) {
	requireArchive(t)
	bag := []store.GardenBagStack{{Item: 490000, Count: 4}, {Item: 490001, Count: 2}}
	atelierAccount(t, "9604", 15, bag, nil)
	if code, _ := AtelierMakeClothes("9604", 101301, atelierNow); code != AtelierLockDesign {
		t.Fatalf("make without blueprint = %d, want %d", code, AtelierLockDesign)
	}
	if code, s := AtelierOpenDesign("9604", 101301, atelierNow); code != AtelierOK || s.Smiles != 0 {
		t.Fatalf("open = %d, %+v", code, s)
	}
	code, s := AtelierMakeClothes("9604", 101301, atelierNow)
	if code != AtelierOK || s.Stamina != 90 {
		t.Fatalf("make = %d, %+v", code, s)
	}
	if b := atelierBag(t, "9604"); b[490000] != 2 || b[490001] != 1 {
		t.Fatalf("bag after clothes = %v", b)
	}
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("9604")
	owned := slices.Contains(store.AccountOwnedCodes(acc), "CUAH0066I")
	gems := acc.Gems
	store.AccountsMu.Unlock()
	if !owned || gems != 0 {
		t.Fatalf("owned = %v gems = %d after the make", owned, gems)
	}

	store.AccountsMu.Lock()
	store.AccountByAidLocked("9604").GardenBag = []store.GardenBagStack{{Item: 490000, Count: 2}, {Item: 490001, Count: 1}}
	store.AccountsMu.Unlock()
	if code, s := AtelierMakeClothes("9604", 101301, atelierNow); code != AtelierInvalid || s.Stamina != 90 {
		t.Fatalf("owned re-craft = %d, %+v", code, s)
	}
	if b := atelierBag(t, "9604"); b[490000] != 2 || b[490001] != 1 {
		t.Fatalf("refused re-craft changed the bag: %v", b)
	}

	// Clothing owned from elsewhere (shop, gacha): its blueprint is shown open and never sold.
	store.AccountsMu.Lock()
	acc = store.AccountByAidLocked("9604")
	acc.Smiles = 15
	_, err := itemGrantLocked(acc, "CUAE007IB")
	store.AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if code, s := AtelierOpenDesign("9604", 101302, atelierNow); code != AtelierAlreadyOpen || s.Smiles != 15 || !s.Owned["CUAE007IB"] {
		t.Fatalf("owned blueprint open = %d, %+v", code, s)
	}
}

// TestAtelierClothesChoice: the product code must be the design's, and no Lucky Powder or retry is in v1.
func TestAtelierClothesChoice(t *testing.T) {
	cases := []struct {
		design int64
		code   string
		powder int64
		want   int64
	}{
		{101301, "", 0, AtelierOK},
		{101301, "CUAH0066I", 0, AtelierOK},
		{101301, "CUON0050V", 0, AtelierChoice},
		{101301, "", 1, AtelierPowder},
		{101201, "", 0, AtelierInvalid},
		{999999, "", 0, AtelierInvalid},
	}
	for _, c := range cases {
		if got := AtelierClothesChoice(c.design, c.code, c.powder); got != c.want {
			t.Fatalf("choice(%d, %q, %d) = %d, want %d", c.design, c.code, c.powder, got, c.want)
		}
	}
}

// TestAtelierRecordPersists: the Atelier record round-trips through the saved account form.
func TestAtelierRecordPersists(t *testing.T) {
	acc := &store.Account{Aid: "9605", Atelier: &store.AtelierState{Stamina: 42, StaminaAt: 1_800_000_000_000, Blueprints: []int64{101301}}}
	raw, err := json.Marshal(store.StoredFrom(acc))
	if err != nil {
		t.Fatal(err)
	}
	var back store.StoredAccount
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Atelier == nil || back.Atelier.Stamina != 42 || back.Atelier.StaminaAt != 1_800_000_000_000 || !slices.Equal(back.Atelier.Blueprints, []int64{101301}) {
		t.Fatalf("round trip = %+v", back.Atelier)
	}
	plain, _ := json.Marshal(store.StoredFrom(&store.Account{Aid: "9606"}))
	if strings.Contains(string(plain), "atelier") {
		t.Fatalf("an account without Atelier state saved the key: %s", plain)
	}
}

// TestAtelierGoodIconRoute: a crafted good's icon is served by the Garden item route like a gathered
// material's (the client's craft icon, read in place from the archive).
func TestAtelierGoodIconRoute(t *testing.T) {
	requireArchive(t)
	req := httptest.NewRequest(http.MethodGet, "/cherry/garden/items/item490000.png", nil)
	rec := httptest.NewRecorder()
	HandleGatherImage(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("good icon: status %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	req = httptest.NewRequest(http.MethodGet, "/cherry/garden/items/item499999.png", nil)
	rec = httptest.NewRecorder()
	HandleGatherImage(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown good icon: status %d, want 404", rec.Code)
	}
}

// TestAtelierMakeClothesRefusesHumanOnlyForAnimal: a clothes design whose product is hair is made for a human only. For an
// ANIMAL the make is refused before any stamina or input is spent (the owned-clothing rule), and the human still makes it.
func TestAtelierMakeClothesRefusesHumanOnlyForAnimal(t *testing.T) {
	requireArchive(t)
	t.Cleanup(func() { atelierFile.Reset() })
	hair := strings.Replace(string(data.AtelierJSON), `"product_clothes": "CUAH0066I"`, `"product_clothes": "CUHA00001"`, 1)
	path := filepath.Join(t.TempDir(), "atelier.json")
	if err := os.WriteFile(path, []byte(hair), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atelierFile.LoadFrom(path); err != nil {
		t.Fatalf("hair design: %v", err)
	}
	for _, c := range []struct {
		name, gender string
		code         int64
	}{{"human", "FEMALE", AtelierOK}, {"animal", "ANIMAL", AtelierInvalid}} {
		t.Run(c.name, func(t *testing.T) {
			bag := []store.GardenBagStack{{Item: 490000, Count: 4}, {Item: 490001, Count: 2}}
			atelierAccount(t, "9605", 15, bag, nil)
			if code, _ := AtelierOpenDesign("9605", 101301, atelierNow); code != AtelierOK {
				t.Fatalf("open = %d", code)
			}
			store.AccountsMu.Lock()
			store.AccountByAidLocked("9605").Gender = c.gender
			store.AccountsMu.Unlock()
			code, s := AtelierMakeClothes("9605", 101301, atelierNow)
			if code != c.code {
				t.Fatalf("make = %d, want %d (%+v)", code, c.code, s)
			}
			if c.code == AtelierInvalid && (s.Stamina != 100 || atelierBag(t, "9605")[490000] != 4) {
				t.Fatalf("refused make spent stamina %d or inputs %v", s.Stamina, atelierBag(t, "9605"))
			}
		})
	}
}
