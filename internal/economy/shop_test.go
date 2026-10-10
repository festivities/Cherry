package economy

import (
	"os"
	"path/filepath"
	"testing"

	"cherry/internal/thumbs"
)

// shopFixture points the catalog at a temp item root: files maps "kind/id" to the file names in it.
func shopFixture(t *testing.T, files map[string][]string) {
	t.Helper()
	root := t.TempDir()
	for dir, names := range files {
		d := filepath.Join(root, filepath.FromSlash(dir))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(d, n), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := thumbs.DpItemRoot
	thumbs.DpItemRoot = root
	t.Cleanup(func() { thumbs.DpItemRoot = old })
}

func TestItemCodeFromID(t *testing.T) {
	for id, want := range map[int64]string{
		225106259: "CUON004TV", 121400639: "RUWA000HR", 326400016: "PUPE0000G", 223800005: "CUHE00005", 235100001: "CAON00001",
	} {
		if got, ok := itemCodeFromID(id); !ok || got != want {
			t.Errorf("itemCodeFromID(%d) = %q %v, want %q", id, got, ok, want)
		}
	}
	for _, id := range []int64{0, 999, 225900001, 999999999} {
		if got, ok := itemCodeFromID(id); ok {
			t.Errorf("itemCodeFromID(%d) = %q, want unclassifiable", id, got)
		}
	}
}

func TestItemPriceTable(t *testing.T) {
	shopFixture(t, map[string][]string{
		"dress/225106259":    {"a.png", "x.aniproj"}, // CUON004TV animated one-piece
		"dress/225000001":    {"a.png"},              // CUTO00001
		"dress/235100001":    {"a.png"},              // CAON00001 animal one-piece
		"custom/224400001":   {"a.png"},              // CUHA00001
		"custom/223800001":   {"a.png"},              // CUHE00001 face: not sold here
		"interior/121400639": {"a.png"},              // RUWA000HR wall
		"interior/121700005": {"a.png"},              // RUTD00005 floor decor
		"interior/122000002": {"a.aniproj"},          // RUGO00002 animated furniture
		"tile/121500101":     {"a.png"},              // RUTI0002T floor
		"pet/326400016":      {"skeleton.xml"},       // PUPE0000G
		"pet/326400230":      {"skeleton.xml", "r.aniproj"},
		"pet/326400300":      {"dp.png"}, // no render assets: not in the catalog
	})
	cases := []struct {
		code  string
		price int64
		cash  bool
		grade string
		ok    bool
	}{
		{"CUTO00001", 150, false, "N", true},
		{"CUHA00001", 200, false, "N", true},
		{"CUON004TV", 625, false, "R", true}, // 250 * 2.5
		{"CAON00001", 250, false, "N", true},
		{"CUSH00001", 120, false, "N", true}, // not in the fixture: plain table price
		{"CUHE00001", 0, false, "N", false},
		{"RUWA000HR", 100, false, "N", true},
		{"RUTI0002T", 100, false, "N", true},
		{"RUTD00005", 150, false, "N", true},
		{"RUGO00002", 375, false, "R", true},
		{"PUPE0000G", 1200, false, "N", true},
		{"PUPE0006E", 25, true, "R", true}, // ride pet (230 = 6E in base36)
		{"BOGUS", 0, false, "N", false},
	}
	for _, c := range cases {
		price, cash, grade, ok := ItemPrice(c.code)
		if price != c.price || cash != c.cash || grade != c.grade || ok != c.ok {
			t.Errorf("ItemPrice(%s) = %d %v %s %v, want %d %v %s %v", c.code, price, cash, grade, ok, c.price, c.cash, c.grade, c.ok)
		}
	}
	cat := loadCatalog()
	if cat.byCode["PUPE0000G"] == nil || cat.byCode["PUPE0008C"] != nil {
		t.Errorf("pet catalog = %v (render-less pet must be absent)", cat.pet)
	}
	if len(cat.fashion) != 4 || len(cat.interior) != 4 || len(cat.pet) != 2 {
		t.Errorf("catalog sizes fashion %d interior %d pet %d, want 4/4/2", len(cat.fashion), len(cat.interior), len(cat.pet))
	}
}

func TestMissingItemRootIsEmpty(t *testing.T) {
	old := thumbs.DpItemRoot
	thumbs.DpItemRoot = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { thumbs.DpItemRoot = old })
	if c := loadCatalog(); len(c.fashion)+len(c.interior)+len(c.pet) != 0 {
		t.Fatalf("catalog = %+v, want empty", c)
	}
	if price, _, _, ok := ItemPrice("CUTO00001"); price != 150 || !ok {
		t.Errorf("price without catalog = %d %v", price, ok)
	}
}
