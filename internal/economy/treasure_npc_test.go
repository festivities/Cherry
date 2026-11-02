package economy

import (
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/opdata"
)

// treasureDefault decodes the embedded treasure file, a copy the test may edit.
func treasureDefault(t *testing.T) *TreasureData {
	t.Helper()
	d, err := opdata.Decode[TreasureData](data.TreasureJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestTreasureNPCItemsDefault: the default portraits pass the shape check and, with the item archive, the art check.
func TestTreasureNPCItemsDefault(t *testing.T) {
	requireArchive(t)
	d := treasureDefault(t)
	if err := checkTreasureData(d); err != nil {
		t.Fatalf("default portraits refused: %v", err)
	}
	if err := verifyTreasure(d); err != nil {
		t.Fatalf("default portraits without art: %v", err)
	}
}

// TestTreasureNPCItemRefusals: a portrait item must be a canonical clothing or face code, not an animal part on a human, and must
// have art (checkTreasureData for the first two, verifyTreasure for the third).
func TestTreasureNPCItemRefusals(t *testing.T) {
	requireArchive(t)
	cases := []struct {
		name, code, want string
		art              bool // refused by the art check (verifyTreasure), not the shape check
	}{
		{"animal face on a human", "CAFA0005O", "animal part", false},
		{"animal ear on a human", "CAEA0000E", "animal part", false},
		{"room item", "RUAB00002", "not a clothing or face item code", false},
		{"unknown category", "CUZZ00001", "not a clothing or face item code", false},
		{"lower case", "cuhe0000m", "not a clothing or face item code", false},
		{"no art anywhere", "CUTO0099Z", "has no art in the item archive", true},
	}
	for _, c := range cases {
		d := treasureDefault(t)
		d.NPCs[1].Items = append(d.NPCs[1].Items, c.code)
		err := checkTreasureData(d)
		if c.art {
			if err != nil {
				t.Fatalf("%s: shape check refused %s: %v", c.name, c.code, err)
			}
			err = verifyTreasure(d)
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %s err = %v, want %q", c.name, c.code, err, c.want)
		}
	}
}

// TestTreasureVerifySkipsWithoutArchive: with no asset store (cloudflare.json absent) the startup art checks are skipped, so Cherry
// still starts, as the other data files do.
func TestTreasureVerifySkipsWithoutArchive(t *testing.T) {
	old := assets.Archive
	assets.Archive = nil
	t.Cleanup(func() { assets.Archive = old })
	d := treasureDefault(t)
	d.NPCs[0].Items = append(d.NPCs[0].Items, "CUTO0099Z")
	d.Buff.Items = []TreasureBuffItem{{Code: "NOSUCHITEM", Percent: 10}}
	if err := verifyTreasure(d); err != nil {
		t.Fatalf("verify without a store refused: %v", err)
	}
}
