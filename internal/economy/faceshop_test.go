package economy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/itemcode"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// faceIndex points the converted index at a temp tree listing the given custom items. The face catalog reads only
// the index, so no item files are written. Animal ears and tails are listed under dress, as the converter writes them.
func faceIndex(t *testing.T, codes ...string) {
	t.Helper()
	m := &assets.ItemManifest{}
	for _, c := range codes {
		id, ok := itemcode.ID(c)
		if !ok {
			t.Fatalf("no item id for %s", c)
		}
		kind := "custom"
		if c[2:4] == "EA" || c[2:4] == "TL" {
			kind = "dress"
		}
		m.Entries = append(m.Entries, assets.ItemEntry{Kind: kind, ID: id, Name: "dp.png", Size: 1,
			MD5: strings.Repeat("A", 32), Origin: assets.OriginCherry})
	}
	body, err := assets.EncodeItemManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "items.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	old := assets.GeneratedItemRoot
	assets.GeneratedItemRoot = root
	t.Cleanup(func() { assets.GeneratedItemRoot = old })
}

// itemconvSwitch sets itemconv.json in a temp operator config directory (on or off) and reloads it. Cleanup runs
// after the environment is restored, so the embedded default (on) comes back.
func itemconvSwitch(t *testing.T, on bool) {
	t.Helper()
	base := t.TempDir()
	cfg := filepath.Join(base, "Cherry", "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "itemconv.json"), []byte(fmt.Sprintf(`{"enabled": %v}`, on)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = assets.LoadItemConv() })
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	if err := assets.LoadItemConv(); err != nil {
		t.Fatal(err)
	}
}

// TestFaceCatalogConvertedFaces: the converted human faces the index lists join the Face Shop catalog and are sold;
// converted hair, faces the index does not list, and animal faces (TestFaceShopAnimalFaces) stay out of the catalog.
func TestFaceCatalogConvertedFaces(t *testing.T) {
	faceIndex(t, "CUEY0000A", "CUMO0000A", "CUHE0000A", "CUHA00001", "CAEY0000A")
	got := faceCatalog()
	if len(got) != len(FaceCatalog)+3 {
		t.Fatalf("catalog = %d codes, want the %d static faces plus the 3 converted ones", len(got), len(FaceCatalog))
	}
	for _, want := range []string{"CUEY0000A", "CUMO0000A", "CUHE0000A"} {
		if !slices.Contains(got, want) || !faceForSale(want) {
			t.Errorf("converted face %s not listed or not for sale", want)
		}
	}
	for _, bad := range []string{"CUHA00001", "CUNO0000A"} {
		if slices.Contains(got, bad) || faceForSale(bad) {
			t.Errorf("%s listed or for sale, want out", bad)
		}
	}
	if slices.Contains(got, "CAEY0000A") {
		t.Error("animal face CAEY0000A in the human catalog")
	}
	if body := string(faceShopBody("FEMALE", 1)); !strings.Contains(body, `"cd":"CUEY0000A"`) {
		t.Errorf("shop body lacks the converted eye face")
	}
}

// TestFaceCatalogConvertedSwitch: itemconv.json off hides every converted face, from the catalog and from purchase.
func TestFaceCatalogConvertedSwitch(t *testing.T) {
	faceIndex(t, "CUEY0000A")
	itemconvSwitch(t, false)
	if len(faceCatalog()) != len(FaceCatalog) || faceForSale("CUEY0000A") {
		t.Fatal("switch off still lists or sells the converted face")
	}
	if body := string(faceShopBody("FEMALE", 1)); strings.Contains(body, "CUEY0000A") {
		t.Errorf("shop body lists a converted face with the switch off")
	}
	itemconvSwitch(t, true)
	if !faceForSale("CUEY0000A") {
		t.Fatal("switch back on does not sell the converted face")
	}
}

// shopCategories maps each shop key holding a category ({"items":[{"cd"}]}) of a face shop body to its codes.
func shopCategories(t *testing.T, body []byte) map[string][]string {
	t.Helper()
	var top struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for key, raw := range top.Result {
		var cat struct {
			Items []struct {
				CD string `json:"cd"`
			} `json:"items"`
		}
		if json.Unmarshal(raw, &cat) != nil || cat.Items == nil {
			continue
		}
		codes := []string{}
		for _, item := range cat.Items {
			codes = append(codes, item.CD)
		}
		out[key] = codes
	}
	return out
}

// faceShopAccount installs test account ftok (gender, 10 Face Shop tickets, not a lab account) with a ledger dir.
func faceShopAccount(t *testing.T, gender string) *store.Account {
	t.Helper()
	acc := &store.Account{AccessToken: "ftok", SessionKey: "sk", Aid: "9311", Name: "Face", Gender: gender,
		Skin: "2", Country: "JP", Gems: 500, FaceTickets: 10}
	testutil.InstallAccounts(t, map[string]*store.Account{"ftok": acc})
	testutil.SetLab(t, "9311", false)
	gachaLedgerDir(t)
	return acc
}

// faceShopBuy posts a saveAndPurchase/v2 body as account ftok.
func faceShopBuy(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v4/faceshop/saveAndPurchase/v2", strings.NewReader(body))
	req.Header.Set("Cookie", `AV_AUTH="ftok"`)
	rec := httptest.NewRecorder()
	HandleFaceShopPurchase(rec, req)
	return rec
}

// slotCodes returns the codes whose category (code[2:4]) is slot, in order.
func slotCodes(codes []string, slot string) []string {
	var out []string
	for _, c := range codes {
		if c[2:4] == slot {
			out = append(out, c)
		}
	}
	return out
}

// TestFaceShopAnimalFaces: an animal account's shop body lists the animal parts: the originals first (114 EY/NO/MO
// faces, 24 FA, 19 EA and 15 TL from data/animal_face_codes.txt), then the converted parts the index lists, under
// AEYE/ANOSE/AMOUTH/AFACE/AEAR/ATAIL. A human body has none, a human account cannot buy an animal part (original or
// converted), an animal account cannot buy a human face, and it buys a converted face for one ticket.
func TestFaceShopAnimalFaces(t *testing.T) {
	if len(AnimalFaceCatalog) != 172 {
		t.Fatalf("AnimalFaceCatalog = %d codes, want 172 (114 EY/NO/MO faces, 24 FA, 19 EA, 15 TL)", len(AnimalFaceCatalog))
	}
	for slot, want := range map[string]int{"EY": 48, "NO": 23, "MO": 43, "FA": 24, "EA": 19, "TL": 15} {
		if got := len(slotCodes(AnimalFaceCatalog, slot)); got != want {
			t.Errorf("%s originals = %d, want %d", slot, got, want)
		}
	}
	faceIndex(t, "CAEY0000A", "CANO0000A", "CAMO0000A", "CAFA0000A", "CAEA0000A", "CATL0000A", "CUEY0000A")
	animal := shopCategories(t, faceShopBody("ANIMAL", 1))
	for key, want := range map[string][2]string{"AEYE": {"EY", "CAEY0000A"}, "ANOSE": {"NO", "CANO0000A"},
		"AMOUTH": {"MO", "CAMO0000A"}, "AFACE": {"FA", "CAFA0000A"}, "AEAR": {"EA", "CAEA0000A"}, "ATAIL": {"TL", "CATL0000A"}} {
		exp := append(slotCodes(AnimalFaceCatalog, want[0]), want[1])
		if !slices.Equal(animal[key], exp) {
			t.Errorf("%s has %d codes, want the %d originals then %s", key, len(animal[key]), len(exp)-1, want[1])
		}
	}
	if !slices.Contains(animal["AFACE"], "CAFA0005O") || !slices.Contains(animal["AEAR"], "CAEA00001") ||
		!slices.Contains(animal["ATAIL"], "CATL00001") {
		t.Error("the default skin or the starter ear and tail is not listed")
	}

	human := shopCategories(t, faceShopBody("FEMALE", 1))
	if _, ok := human["AEYE"]; ok {
		t.Error("human body has an AEYE key")
	}
	for key, codes := range human {
		for _, code := range codes {
			if strings.HasPrefix(code, "CA") {
				t.Errorf("human body lists animal face %s under %s", code, key)
			}
		}
	}
	if !slices.Contains(human["FEYE"], "CUEY0000A") {
		t.Error("human body lacks the converted CUEY0000A under FEYE")
	}

	hacc := faceShopAccount(t, "FEMALE")
	for _, code := range []string{"CAEY0000A", slotCodes(AnimalFaceCatalog, "EY")[0]} {
		if rec := faceShopBuy(`{"saveItemList":[{"itemCode":"` + code + `","invenSeq":1}],"invenItemList":[]}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("human bought animal face %s: status %d %s", code, rec.Code, rec.Body.String())
		}
	}
	if hacc.FaceTickets != 10 || slices.Contains(store.AccountOwnedCodes(hacc), "CAEY0000A") {
		t.Errorf("refused human purchase spent tickets (now %d) or granted a face", hacc.FaceTickets)
	}

	aacc := faceShopAccount(t, "ANIMAL")
	if rec := faceShopBuy(`{"saveItemList":[{"itemCode":"CUEY0000A","invenSeq":1}],"invenItemList":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("animal bought human face CUEY0000A: status %d %s", rec.Code, rec.Body.String())
	}
	if rec := faceShopBuy(`{"saveItemList":[{"itemCode":"CAEY0000A","invenSeq":1}],"invenItemList":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("animal could not buy CAEY0000A: status %d %s", rec.Code, rec.Body.String())
	}
	if aacc.FaceTickets != 9 || !slices.Contains(store.AccountOwnedCodes(aacc), "CAEY0000A") {
		t.Errorf("animal purchase: tickets %d owned %v, want 9 and CAEY0000A", aacc.FaceTickets, store.AccountOwnedCodes(aacc))
	}
}

// TestFaceShopAnimalStarterKept: the client sends the preview's whole face list, so an animal account that keeps its
// starter faces (CAEY00002, CANO00001, CAMO00002) and changes its mouth buys the new mouth for one ticket.
func TestFaceShopAnimalStarterKept(t *testing.T) {
	aacc := faceShopAccount(t, "ANIMAL")
	starters := []string{"CAEY00002", "CANO00001", "CAMO00002"}
	aacc.ItemCodes = slices.Clone(starters)
	aacc.InventoryCodes = slices.Clone(starters)
	rec := faceShopBuy(`{"saveItemList":[{"itemCode":"CAEY00002","invenSeq":1},{"itemCode":"CANO00001","invenSeq":2},{"itemCode":"CAMO00009","invenSeq":3}],"invenItemList":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("starter-kept purchase: status %d %s", rec.Code, rec.Body.String())
	}
	if aacc.FaceTickets != 9 {
		t.Errorf("tickets = %d, want 9 (one for the changed mouth)", aacc.FaceTickets)
	}
	for _, want := range []string{"CAEY00002", "CANO00001", "CAMO00009"} {
		if !slices.Contains(aacc.ItemCodes, want) {
			t.Errorf("worn = %v, want %s", aacc.ItemCodes, want)
		}
	}
	if slices.Contains(aacc.ItemCodes, "CAMO00002") {
		t.Errorf("worn = %v, still has the replaced mouth CAMO00002", aacc.ItemCodes)
	}
}

// TestAnimalWearPurchase: a tail changed through invenItemList (the client's animal branch, as a string) spends one
// ticket, is worn and owned, and the other starters stay worn. The converted dress tail is sold the same way.
func TestAnimalWearPurchase(t *testing.T) {
	faceIndex(t, "CATL0000A")
	aacc := faceShopAccount(t, "ANIMAL")
	starters := []string{"CAEY00002", "CANO00001", "CAMO00002", "CAEA00001", "CATL00001"}
	aacc.ItemCodes = slices.Clone(starters)
	aacc.InventoryCodes = slices.Clone(starters)
	rec := faceShopBuy(`{"saveItemList":[{"itemCode":"CAEY00002","invenSeq":1},{"itemCode":"CANO00001","invenSeq":2},` +
		`{"itemCode":"CAMO00002","invenSeq":3},{"itemCode":"CAEA00001","invenSeq":4},{"itemCode":"CATL00006","invenSeq":5}],` +
		`"invenItemList":["CATL00006"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tail purchase: status %d %s", rec.Code, rec.Body.String())
	}
	if aacc.FaceTickets != 9 {
		t.Errorf("tickets = %d, want 9 (one for the tail)", aacc.FaceTickets)
	}
	for _, want := range []string{"CAEY00002", "CANO00001", "CAMO00002", "CAEA00001", "CATL00006"} {
		if !slices.Contains(aacc.ItemCodes, want) {
			t.Errorf("worn = %v, want %s", aacc.ItemCodes, want)
		}
	}
	if slices.Contains(aacc.ItemCodes, "CATL00001") || !slices.Contains(store.AccountOwnedCodes(aacc), "CATL00006") {
		t.Errorf("worn = %v owned = %v: want the starter tail replaced and CATL00006 owned", aacc.ItemCodes, store.AccountOwnedCodes(aacc))
	}

	rec = faceShopBuy(`{"saveItemList":[{"itemCode":"CATL0000A"}],"invenItemList":["CATL0000A"]}`)
	if rec.Code != http.StatusOK || aacc.FaceTickets != 8 || !slices.Contains(aacc.ItemCodes, "CATL0000A") ||
		slices.Contains(aacc.ItemCodes, "CATL00006") {
		t.Fatalf("converted tail: status %d tickets %d worn %v, want 200, 8 and CATL0000A replacing CATL00006",
			rec.Code, aacc.FaceTickets, aacc.ItemCodes)
	}
}

// TestAnimalSkinReturnsToDefault: the default skin CAFA0005O comes back through invenItemList (the client's
// saveItemList drops base items). It is free: the worn skin is dropped, the base shows, nothing is bought, no ticket.
func TestAnimalSkinReturnsToDefault(t *testing.T) {
	aacc := faceShopAccount(t, "ANIMAL")
	aacc.ItemCodes = []string{"CAEY00002", "CAFA0000Q"}
	aacc.InventoryCodes = []string{"CAEY00002", "CAFA0000Q"}
	rec := faceShopBuy(`{"saveItemList":[{"itemCode":"CAEY00002","invenSeq":1}],"invenItemList":["CAFA0005O"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("return to default skin: status %d %s", rec.Code, rec.Body.String())
	}
	if aacc.FaceTickets != 10 || !slices.Equal(aacc.ItemCodes, []string{"CAEY00002"}) {
		t.Errorf("tickets %d worn %v, want 10 and only the eyes worn", aacc.FaceTickets, aacc.ItemCodes)
	}
	if slices.Contains(store.AccountInventoryCodes(aacc), "CAFA0005O") {
		t.Error("the default skin was granted as a purchase")
	}
	if !strings.Contains(rec.Body.String(), `"cd":"CAFA0005O"`) {
		t.Errorf("response does not show the base skin: %s", rec.Body.String())
	}
}

// TestAnimalWearRefusals: an animal part that is not a listed skin, ear or tail, an owned-only part, two codes for one
// slot, and a human's animal part are refused (400, or 61009 for owned-only), with nothing equipped or spent.
func TestAnimalWearRefusals(t *testing.T) {
	faceIndex(t, "CATL0000A")
	for name, c := range map[string]struct {
		gender, body string
		owned        bool // 61009 "Item already purchased." (owned-only change)
	}{
		"owned tail only":       {"ANIMAL", `{"saveItemList":[{"itemCode":"CAEY00002"}],"invenItemList":["CATL00006"]}`, true},
		"two tails":             {"ANIMAL", `{"saveItemList":[],"invenItemList":["CATL00006","CATL0000A"]}`, false},
		"unlisted tail":         {"ANIMAL", `{"saveItemList":[],"invenItemList":["CATL0FFFF"]}`, false},
		"face code as wear":     {"ANIMAL", `{"saveItemList":[],"invenItemList":["CAEY00002"]}`, false},
		"human face as wear":    {"ANIMAL", `{"saveItemList":[],"invenItemList":["CUTL00006"]}`, false},
		"human bought CA tail":  {"FEMALE", `{"saveItemList":[{"itemCode":"CUEY0030Q"}],"invenItemList":["CATL00006"]}`, false},
		"human CA skin in list": {"FEMALE", `{"saveItemList":[{"itemCode":"CAFA0000Q"}],"invenItemList":[]}`, false},
		"human CA ear in list":  {"FEMALE", `{"saveItemList":[{"itemCode":"CAEA00001"}],"invenItemList":[]}`, false},
	} {
		acc := faceShopAccount(t, c.gender)
		if c.gender == "ANIMAL" {
			acc.ItemCodes = []string{"CAEY00002", "CAEA00001", "CATL00001"}
			acc.InventoryCodes = []string{"CAEY00002", "CAEA00001", "CATL00001", "CATL00006"}
		} else {
			acc.ItemCodes = []string{"CUEY00002"}
			acc.InventoryCodes = []string{"CUEY00002"}
		}
		worn, owned := slices.Clone(acc.ItemCodes), slices.Clone(acc.InventoryCodes)
		rec := faceShopBuy(c.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d %s, want 400", name, rec.Code, rec.Body.String())
		}
		if c.owned && !strings.Contains(rec.Body.String(), "61009") {
			t.Errorf("%s: body %s, want 61009", name, rec.Body.String())
		}
		if acc.FaceTickets != 10 || !slices.Equal(acc.ItemCodes, worn) || !slices.Equal(acc.InventoryCodes, owned) {
			t.Errorf("%s: tickets %d worn %v owned %v, want 10 and no change", name, acc.FaceTickets, acc.ItemCodes, acc.InventoryCodes)
		}
	}
}
