package economy

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/assets"
)

// convertedCatalog points the converted index at a temp tree listing dress 225106259 (CUON004TV, an outfit) and an
// entry whose category itemcode cannot decode (999999999). The archive keeps one ordinary dress folder.
func convertedCatalog(t *testing.T) {
	t.Helper()
	shopFixture(t, map[string][]string{"dress/225000001": {"dp.png"}})
	root := t.TempDir()
	dir := filepath.Join(root, "dress", "225106259")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &assets.ItemManifest{}
	for _, f := range []struct {
		id   int64
		name string
		body string
	}{
		{225106259, "1409_iteminfo.artsitem", "0;0;5;6;0;0;"},
		{225106259, "0_0.png", "PART"},
		{225106259, "dp.png", "DP"},
		{999999999, "dp.png", "DP"}, // undecodable: skipped by the catalog without a log line
	} {
		if f.id == 225106259 {
			if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		sum := md5.Sum([]byte(f.body))
		m.Entries = append(m.Entries, assets.ItemEntry{Kind: "dress", ID: f.id, Name: f.name, Size: int64(len(f.body)),
			MD5: strings.ToUpper(hex.EncodeToString(sum[:])), Origin: assets.OriginCherry})
	}
	body, err := assets.EncodeItemManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "items.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	oldRoot := assets.GeneratedItemRoot
	assets.GeneratedItemRoot = root
	t.Cleanup(func() { assets.GeneratedItemRoot = oldRoot })
	catalog = nil
	t.Cleanup(func() { catalog = nil })
}

// TestConvertedItemsListedAndPriced: a converted outfit is in the catalog and priced by the category table; the
// undecodable entry is not listed; the switch (itemconv.json) off hides the converted items again.
func TestConvertedItemsListedAndPriced(t *testing.T) {
	convertedCatalog(t)
	const code = "CUON004TV"
	c := loadCatalog()
	it := c.byCode[code]
	if it == nil || it.ID != 225106259 {
		t.Fatalf("converted outfit not listed: %+v", it)
	}
	listedOn := len(c.byCode)
	price, cash, grade, ok := ItemPrice(code)
	if !ok || cash || grade != "N" || price != ActivePrices().ClothingPrices["ON"] {
		t.Fatalf("price of %s = %d %v %s %v, want the ON category price", code, price, cash, grade, ok)
	}

	// The switch: itemconv.json {"enabled": false}, read from the operator config directory.
	base := t.TempDir()
	cfg := filepath.Join(base, "Cherry", "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "itemconv.json"), []byte(`{"enabled": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = assets.LoadItemConv() }) // registered before Setenv: runs after the environment is restored
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	if err := assets.LoadItemConv(); err != nil {
		t.Fatal(err)
	}
	catalog = nil
	c = loadCatalog()
	if c.byCode[code] != nil {
		t.Fatal("switch off still lists the converted outfit")
	}
	if off := len(c.byCode); listedOn-off != 1 {
		t.Errorf("the switch removed %d listed items, want only the converted outfit (the undecodable entry never lists)", listedOn-off)
	}
}
