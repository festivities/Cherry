package economy

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/itemcode"
)

// faceIndex points the converted index at a temp tree listing the given custom items. The face catalog reads only
// the index, so no item files are written.
func faceIndex(t *testing.T, codes ...string) {
	t.Helper()
	m := &assets.ItemManifest{}
	for _, c := range codes {
		id, ok := itemcode.ID(c)
		if !ok {
			t.Fatalf("no item id for %s", c)
		}
		m.Entries = append(m.Entries, assets.ItemEntry{Kind: "custom", ID: id, Name: "dp.png", Size: 1,
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
// converted hair, animal faces and faces the index does not list stay out.
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
	for _, bad := range []string{"CUHA00001", "CAEY0000A", "CUNO0000A"} {
		if slices.Contains(got, bad) || faceForSale(bad) {
			t.Errorf("%s listed or for sale, want out", bad)
		}
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
