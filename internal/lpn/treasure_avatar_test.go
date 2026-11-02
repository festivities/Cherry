package lpn

import (
	"slices"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/itemcode"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// treAvItem is MONOPOLY::ItemInfo, the element of AvatarInfo f15 (avatar-items-spec 1.2 and 1.3): f1 the numeric item id, f3 the
// dye type and f5 the colour layers {f1 colorCode, f2 transparency}.
var (
	treLayer  = lpMsg{1: {wire: 2, req: true}, 2: {wire: 0}}
	treAvItem = lpMsg{1: {wire: 0, req: true}, 3: {wire: 0}, 5: {wire: 2, rep: true, msg: treLayer}}
)

// treAvItemRead is one decoded f15 element: its id, its dye type (0 undyed) and its colour layers.
type treAvItemRead struct {
	id, dyeType uint64
	layers      []treLayerRead
}

type treLayerRead struct {
	color  string
	transp uint64
}

// treAvItems decodes the f15 elements of an AvatarInfo body, in order.
func treAvItems(t *testing.T, body []byte) []treAvItemRead {
	t.Helper()
	var out []treAvItemRead
	ok := pbScan(body, func(f, _ uint64, d []byte) {
		if f != 15 {
			return
		}
		var it treAvItemRead
		if !pbScan(d, func(f, v uint64, d []byte) {
			switch f {
			case 1:
				it.id = v
			case 3:
				it.dyeType = v
			case 5:
				var layer treLayerRead
				pbScan(d, func(f, v uint64, d []byte) {
					if f == 1 {
						layer.color = string(d)
					} else if f == 2 {
						layer.transp = v
					}
				})
				it.layers = append(it.layers, layer)
			}
		}) {
			t.Fatal("malformed ItemInfo")
		}
		out = append(out, it)
	})
	if !ok {
		t.Fatal("malformed AvatarInfo")
	}
	return out
}

// TestTreasureNPCPortraitItems: a season portrait carries its outfit as AvatarInfo f15, one undyed ItemInfo per data code with
// the code's numeric id (the spec's values for the base items: CUTO0011X 225001365, CUHA00004 224400004).
func TestTreasureNPCPortraitItems(t *testing.T) {
	npc := economy.ActiveTreasureData().NPCs[0]
	body := treasureAvatar(npc)
	if err := lpCheck(body, treNPC, "npc"); err != nil {
		t.Fatal(err)
	}
	items := treAvItems(t, body)
	if len(items) != len(npc.Items) {
		t.Fatalf("portrait items = %d, want %d", len(items), len(npc.Items))
	}
	for i, code := range npc.Items {
		want, ok := itemcode.ID(code)
		if !ok || items[i].id != uint64(want) || items[i].dyeType != 0 || len(items[i].layers) != 0 {
			t.Fatalf("item %d = %+v, want %s as %d undyed", i, items[i], code, want)
		}
	}
	if !slices.ContainsFunc(items, func(it treAvItemRead) bool { return it.id == 225001365 }) ||
		!slices.ContainsFunc(items, func(it treAvItemRead) bool { return it.id == 224400004 }) {
		t.Fatalf("portrait items %v lack the base top or hair ids", items)
	}
}

// TestTreasureOwnAvatarItems: the relay start's own avatar lists the account's appearance (equipped items, the base items of the
// empty slots, SKN skin codes left out) as f15, and a dyed garment carries f3 = 2 and two f5 layers, layer 0 first.
func TestTreasureOwnAvatarItems(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"tw-own-9701": {AccessToken: "tw-own-9701", SessionKey: "sk", Aid: "9701",
		Name: "Dyed", Gender: "FEMALE", Skin: "002", ItemCodes: []string{"CUSH003EY", "SKN002", "CUTO0011X"},
		DyeColors: map[string][]store.DyeColor{"CUSH003EY": {{ColorCode: "#E53935"}, {ColorCode: "#43A047", Transparency: 20}}}}})
	body := treasureOwnAvatar(9701)
	if err := lpCheck(body, treOwn, "own"); err != nil {
		t.Fatal(err)
	}
	items := treAvItems(t, body)
	// Equipped: CUSH003EY (SH), SKN002 (not clothing, left out), CUTO0011X (TO). The appearance adds the FEMALE base PA and HA
	// (CUSH0009Q is skipped: SH is taken). The order is the equipped codes first, then the base items.
	want := []uint64{225304426, 225001365, 225200780, 224400004}
	var ids []uint64
	for _, it := range items {
		ids = append(ids, it.id)
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("own item ids = %v, want %v", ids, want)
	}
	dyed := items[0]
	if dyed.dyeType != store.DyeTypeTwoLayers || len(dyed.layers) != 2 {
		t.Fatalf("dyed garment = %+v, want dye type 2 with two layers", dyed)
	}
	if dyed.layers[0] != (treLayerRead{color: "#E53935"}) || dyed.layers[1] != (treLayerRead{color: "#43A047", transp: 20}) {
		t.Fatalf("dye layers = %+v, want the stored colours in order", dyed.layers)
	}
}
