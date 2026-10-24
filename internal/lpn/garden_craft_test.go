package lpn

import (
	"encoding/binary"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

// craftHc34Example is hc34 craft_info_res from spec 12.5 (the byte example): player_craft with 20
// Smiles, themes 1012 (material) and 1013 (clothes), material_theme_id 1012.
const craftHc34Example = "0800121708011000180a2064280030643864400048ac02501458001a810108f407120c47617264656e2043726166741a0e4d6174657269616c207468656d6520002800300038014000480158006217637261667430312f313031322f62616e6e65722e706e676a15637261667430312f313031322f69636f6e2e706e6770007a008001ffffffffffffffffff018801ffffffffffffffffff01900100a001001a820108f507120e47617264656e20436c6f746865731a0d436c6f74686573207468656d6520002800300038014000480158006217637261667430312f313031332f62616e6e65722e706e676a15637261667430312f313031332f69636f6e2e706e6770007a008001ffffffffffffffffff018801ffffffffffffffffff01900100a0010020f407"

// TestCraftInfoMatchesSpecExample: with the spec's example themes and state, hc 34 is byte-identical
// to the spec's hex (the encoder is the one the Atelier uses).
func TestCraftInfoMatchesSpecExample(t *testing.T) {
	themes := []economy.AtelierTheme{
		{ID: 1012, Kind: economy.AtelierKindMaterial, Name: "Garden Craft", Information: "Material theme", Banner: "craft01/1012/banner.png", Icon: "craft01/1012/icon.png"},
		{ID: 1013, Kind: economy.AtelierKindClothes, Name: "Garden Clothes", Information: "Clothes theme", Banner: "craft01/1013/banner.png", Icon: "craft01/1013/icon.png"},
	}
	s := economy.AtelierSnap{Stamina: 100, StaminaMax: 100, ChargeSec: 300, Smiles: 20}
	got := hex.EncodeToString(craftInfoBody(s, themes, 1012))
	if got != craftHc34Example {
		t.Fatalf("hc34 differs from spec 12.5:\n got %s\nwant %s", got, craftHc34Example)
	}
}

// craftAccount installs aid with Smiles and a Bag (no store path, so nothing is written).
func craftAccount(t *testing.T, aid string, smiles int64, bag []store.GardenBagStack) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"ct-" + aid: {AccessToken: "ct-" + aid, SessionKey: "sk", Aid: aid, Name: "Craft", Smiles: smiles, GardenBag: bag}})
	testutil.SetLab(t, aid, false)
}

// requireCraftArchive skips a test whose clothing products need the item archive.
func requireCraftArchive(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(thumbs.DpItemRoot, "dress")); err != nil {
		t.Skip("item archive not present")
	}
}

// craftNow is the fixed clock of the Atelier tests (stamina regen is measured from it).
var craftNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// craftCall sends one Atelier request and returns its replies.
func craftCall(t *testing.T, viewer uint64, msgid uint16, data []byte) []gardenMsg {
	t.Helper()
	return gardenCraftReply(msgid, viewer, data, craftNow, log.New(io.Discard, "", 0))
}

// craftFirst returns the single reply to msgid and checks its id.
func craftFirst(t *testing.T, msgs []gardenMsg, id uint16) pbMsg {
	t.Helper()
	if len(msgs) != 1 || msgs[0].id != id {
		t.Fatalf("replies = %+v, want one hc %d", msgs, id)
	}
	return pbFieldsOf(t, msgs[0].body)
}

// TestCraftRepliesFieldLayouts decodes each Atelier reply and checks its top-level fields against
// spec 12.1: field numbers, the oneof branch (one per frame), theme id matches and string types.
func TestCraftRepliesFieldLayouts(t *testing.T) {
	requireCraftArchive(t)
	const viewer = 9701
	craftAccount(t, "9701", 15, []store.GardenBagStack{
		{Item: 110000, Count: 5}, {Item: 120000, Count: 3}, {Item: 210000, Count: 1},
		{Item: 490000, Count: 2}, {Item: 490001, Count: 1},
	})

	// hc 34: the craft info (both themes, material_theme_id 1012, Smiles as p_point, no Cash).
	info := craftFirst(t, craftCall(t, viewer, 29, nil), 34)
	player := pbFieldsOf(t, info.msgs[2][0])
	if info.vars[1] != 0 || info.vars[4] != 1012 || len(info.msgs[3]) != 2 || player.vars[10] != 15 || player.vars[11] != 0 || player.vars[6] != 100 {
		t.Fatalf("hc34 = %v player %v", info.vars, player.vars)
	}

	// hc 35: a material theme fills field 2 only; a clothes theme fills field 3 only.
	mat := craftFirst(t, craftCall(t, viewer, 30, pbVar(nil, 1, 1012)), 35)
	if mat.vars[1] != 0 || len(mat.msgs[2]) != 1 || len(mat.msgs[3]) != 0 || len(mat.msgs[4]) == 0 {
		t.Fatalf("hc35 material = vars %v msgs %v", mat.vars, mat.msgs)
	}
	if designs := pbFieldsOf(t, mat.msgs[2][0]); len(designs.msgs[5]) != 6 || designs.vars[1] != 1012 {
		t.Fatalf("material theme designs = %d, want 6", len(designs.msgs[5]))
	}
	clo := craftFirst(t, craftCall(t, viewer, 30, pbVar(nil, 1, 1013)), 35)
	if clo.vars[1] != 0 || len(clo.msgs[2]) != 0 || len(clo.msgs[3]) != 1 {
		t.Fatalf("hc35 clothes = vars %v msgs %v", clo.vars, clo.msgs)
	}
	if theme := pbFieldsOf(t, clo.msgs[3][0]); theme.vars[1] != 1013 || len(theme.msgs[2]) != 4 || len(theme.msgs[3]) != 1 {
		t.Fatalf("clothes theme = %v designs %d", theme.vars, len(theme.msgs[2]))
	}

	// Open condition (spec 19.2): the locked clothes design 101301 (blueprint 15 Smiles, not bought) is
	// type 1 with the price as value; the material design 101201 is free (type 0).
	lockedTheme := pbFieldsOf(t, clo.msgs[3][0])
	if locked := pbFieldsOf(t, lockedTheme.msgs[2][0]); locked.vars[1] != 101301 || len(locked.msgs[4]) != 1 {
		t.Fatalf("first clothes design = %v msgs %v", locked.vars, locked.msgs)
	} else if cond := pbFieldsOf(t, locked.msgs[4][0]); cond.vars[1] != 1 || cond.vars[2] != 15 {
		t.Fatalf("locked clothes open condition = %v, want type 1 value 15", cond.vars)
	}
	if free := pbFieldsOf(t, pbFieldsOf(t, mat.msgs[2][0]).msgs[5][0]); free.vars[1] != 101201 || len(free.msgs[4]) != 1 {
		t.Fatalf("first material design = %v msgs %v", free.vars, free.msgs)
	} else if cond := pbFieldsOf(t, free.msgs[4][0]); cond.vars[1] != 0 {
		t.Fatalf("free material open condition = %v, want type 0", cond.vars)
	}

	// hc 36: a material design fills field 3, a clothes design field 4; field 5 holds the assets.
	md := craftFirst(t, craftCall(t, viewer, 31, pbVar(nil, 1, 101201)), 36)
	if md.vars[2] != 101201 || len(md.msgs[3]) != 1 || len(md.msgs[4]) != 0 || len(md.msgs[5]) == 0 {
		t.Fatalf("hc36 material = %v msgs %v", md.vars, md.msgs)
	}
	cd := craftFirst(t, craftCall(t, viewer, 31, pbVar(nil, 1, 101301)), 36)
	if cd.vars[2] != 101301 || len(cd.msgs[3]) != 0 || len(cd.msgs[4]) != 1 {
		t.Fatalf("hc36 clothes = %v msgs %v", cd.vars, cd.msgs)
	}

	// hc 37: a locked clothes blueprint is bought (Smiles 15 -> 0; f7 carries the new balance, which
	// the client shows as its Smile count); the second open is already open.
	open := craftFirst(t, craftCall(t, viewer, 32, pbVar(nil, 1, 101301)), 37)
	if open.vars[1] != 0 || open.vars[2] != 101301 || len(open.msgs[4]) != 1 || open.vars[5] != 1 || open.vars[7] != 0 || open.vars[9] != 1013 || len(open.msgs[6]) != 1 {
		t.Fatalf("hc37 = %v msgs %v", open.vars, open.msgs)
	}
	if cond := pbFieldsOf(t, pbFieldsOf(t, open.msgs[4][0]).msgs[4][0]); cond.vars[1] != 0 {
		t.Fatalf("bought design open condition = %v, want type 0", cond.vars)
	}
	if again := craftFirst(t, craftCall(t, viewer, 32, pbVar(nil, 1, 101301)), 37); again.vars[1] != 200194 {
		t.Fatalf("second open = %d, want 200194", again.vars[1])
	}
	if poor := craftFirst(t, craftCall(t, viewer, 32, pbVar(nil, 1, 101302)), 37); poor.vars[1] != 200195 || poor.vars[9] != 1013 {
		t.Fatalf("open with no Smiles = %v", poor.vars)
	}

	// hc 38: a material make with one unpacked before_design_id (ignored in v1).
	before := pbVar(pbVar(pbVar(nil, 1, 101201), 2, 1), 3, 101201)
	made := craftFirst(t, craftCall(t, viewer, 33, before), 38)
	if made.vars[1] != 0 || made.vars[2] != 101201 || len(made.msgs[3]) != 1 || len(made.msgs[4]) != 1 || len(made.msgs[5]) != 1 || len(made.msgs[6]) == 0 || len(made.msgs[7]) != 1 {
		t.Fatalf("hc38 = %v msgs %v", made.vars, made.msgs)
	}
	product := pbFieldsOf(t, made.msgs[3][0])
	if product.vars[1] != 490000 || product.vars[2] != 1 {
		t.Fatalf("hc38 product = %v", product.vars)
	}

	// hc 39: the clothes make info names the product code and its catalog id.
	cinfo := craftFirst(t, craftCall(t, viewer, 34, pbLen(pbVar(nil, 1, 101301), 2, []byte("CUAH0066I"))), 39)
	if cinfo.vars[1] != 0 || len(cinfo.msgs[3]) != 1 || string(cinfo.msgs[5][0]) != "CUAH0066I" || cinfo.vars[6] == 0 {
		t.Fatalf("hc39 = %v msgs %v", cinfo.vars, cinfo.msgs)
	}

	// hc 40: a retry is refused (200206); a make carries the matching theme (6, 7) and make_grade 1.
	retry := craftFirst(t, craftCall(t, viewer, 35, pbVar(pbVar(nil, 1, 101301), 2, 1)), 40)
	if retry.vars[1] != 200206 {
		t.Fatalf("retry = %v", retry.vars)
	}
	clothes := craftFirst(t, craftCall(t, viewer, 35, pbVar(nil, 1, 101301)), 40)
	if clothes.vars[1] != 0 || clothes.vars[4] != 1 || len(clothes.msgs[3]) != 1 || len(clothes.msgs[5]) != 1 || len(clothes.msgs[6]) != 1 || len(clothes.msgs[7]) != 1 || len(clothes.msgs[13]) != 1 {
		t.Fatalf("hc40 = %v msgs %v", clothes.vars, clothes.msgs)
	}
	if theme := pbFieldsOf(t, clothes.msgs[6][0]); theme.vars[1] != 1013 {
		t.Fatalf("hc40 f6 theme = %d, want 1013", theme.vars[1])
	}
	if theme := pbFieldsOf(t, clothes.msgs[7][0]); theme.vars[1] != 1013 {
		t.Fatalf("hc40 f7 theme = %d, want 1013", theme.vars[1])
	}
	item := pbFieldsOf(t, clothes.msgs[3][0])
	if string(item.msgs[1][0]) != "CUAH0066I" || string(item.msgs[2][0]) != "1" || len(item.msgs[4][0]) == 0 {
		t.Fatalf("hc40 item code extra = %v", item.msgs)
	}

	// hc 44 (theme rewards) and the craft shop: refused, empty and no-shop replies.
	if reward := craftFirst(t, craftCall(t, viewer, 37, pbVar(pbVar(nil, 1, 1013), 2, 20)), 44); reward.vars[1] != 200213 {
		t.Fatalf("hc44 = %v", reward.vars)
	}
	if shop := craftFirst(t, craftCall(t, viewer, 40, nil), 47); shop.vars[1] != 0 || len(shop.msgs) != 0 {
		t.Fatalf("hc47 = %v", shop)
	}
	if buy := craftFirst(t, craftCall(t, viewer, 41, nil), 48); buy.vars[1] != 200214 {
		t.Fatalf("hc48 = %v", buy.vars)
	}
}

// TestCraftMakeCycle runs the whole material and clothes flow on one account and checks the Bag and
// the Smile balance on the Bag reply (hc 30) match the Atelier's.
func TestCraftMakeCycle(t *testing.T) {
	requireCraftArchive(t)
	const viewer = 9702
	craftAccount(t, "9702", 15, []store.GardenBagStack{{Item: 110000, Count: 3}, {Item: 210000, Count: 1}, {Item: 490000, Count: 2}, {Item: 490001, Count: 1}})

	if m := craftFirst(t, craftCall(t, viewer, 33, pbVar(pbVar(nil, 1, 101201), 2, 1)), 38); pbFieldsOf(t, m.msgs[3][0]).vars[1] != 490000 {
		t.Fatal("material make failed")
	}
	if b := craftFirst(t, craftCall(t, viewer, 35, pbVar(nil, 1, 101301)), 40); b.vars[1] == 0 {
		t.Fatalf("clothes make before the blueprint = %v", b.vars)
	}
	craftFirst(t, craftCall(t, viewer, 32, pbVar(nil, 1, 101301)), 37)
	if c := craftFirst(t, craftCall(t, viewer, 35, pbVar(nil, 1, 101301)), 40); c.vars[1] != 0 {
		t.Fatalf("clothes make = %v", c.vars)
	}

	bag := pbFieldsOf(t, gardenBagReply(26, viewer, pbVar(nil, 1, 0), log.New(io.Discard, "", 0))[0].body)
	if bag.vars[2] != 0 {
		t.Fatalf("hc30 smile = %d, want 0 after the blueprint", bag.vars[2])
	}
	state := economy.AtelierStatus("9702", craftNow)
	if state.Smiles != 0 || state.Stamina != 85 {
		t.Fatalf("state = %+v", state)
	}
	if b := craftFirst(t, craftCall(t, viewer, 29, nil), 34); pbFieldsOf(t, b.msgs[2][0]).vars[10] != 0 {
		t.Fatal("hc34 smile balance differs from hc30")
	}

	// The crafted good shows in the Bag (hc 31) under its own name and icon.
	list := pbFieldsOf(t, gardenBagReply(27, viewer, pbVar(nil, 1, 1), log.New(io.Discard, "", 0))[0].body)
	found := false
	for _, raw := range list.msgs[3] {
		if a := pbFieldsOf(t, raw); a.vars[1] == 490000 {
			found = string(a.msgs[3][0]) == "Pond Charm" && len(a.msgs[6][0]) > 0
		}
	}
	if !found {
		t.Fatal("the Pond Charm row is missing from hc 31 with its name and icon")
	}
	if got := craftFirst(t, gardenBagReply(25, viewer, pbVar(nil, 1, 499999), log.New(io.Discard, "", 0)), 29); len(got.msgs[1]) != 0 {
		t.Fatalf("an unknown good was listed: %v", got.msgs)
	}
}

// TestCraftPackedBeforeDesign: before_design_id is accepted in the packed form too.
func TestCraftPackedBeforeDesign(t *testing.T) {
	const viewer = 9703
	craftAccount(t, "9703", 0, []store.GardenBagStack{{Item: 110000, Count: 3}, {Item: 210000, Count: 1}})
	packed := pbLen(pbVar(pbVar(nil, 1, 101201), 2, 1), 3, binary.AppendUvarint(nil, 101201))
	if m := craftFirst(t, craftCall(t, viewer, 33, packed), 38); m.vars[1] != 0 {
		t.Fatalf("packed before_design_id refused: %v", m.vars)
	}
}

// TestCraftGatherMessages: the Atelier ids are routed as Garden requests; other ids are not.
func TestCraftGatherMessages(t *testing.T) {
	for _, id := range []uint16{29, 30, 31, 32, 33, 34, 35, 37, 40, 41} {
		if !gardenGatherMsg(id) {
			t.Fatalf("craft msgid %d not routed", id)
		}
	}
	for _, id := range []uint16{36, 38, 39, 42, 46, 48} {
		if gardenCraftMsg(id) {
			t.Fatalf("msgid %d is not a craft request", id)
		}
	}
}

// TestCraftAssetImagesAreCraftPaths: every material AssetItem (craft replies and Bag rows) names its
// picture by a path under the client's item/craftitem/ folder, which the client checks on disk first,
// and the archive holds that file (spec 19.1).
func TestCraftAssetImagesAreCraftPaths(t *testing.T) {
	requireCraftArchive(t)
	for _, id := range []int64{110000, 111000, 120000, 130000, 131000, 132000, 210000, 220000, 230000, 310000, 321000, 330000, 331000, 490000, 490001, 490002, 491000, 491001, 491002} {
		drop, ok := economy.GatherMaterialByID(id)
		if !ok {
			t.Fatalf("item %d is neither a gathering drop nor a good", id)
		}
		name := "item" + strconv.FormatInt(id, 10) + ".png"
		if img := pbFieldsOf(t, gardenAssetItem(drop)).msgs[6]; len(img) != 1 || string(img[0]) != "garden01/items/"+name {
			t.Fatalf("item %d img_url = %q, want garden01/items/%s", id, img, name)
		}
		if _, err := os.Stat(filepath.Join(thumbs.DpItemRoot, "craftitem", "garden01", "items", name)); err != nil {
			t.Fatalf("archive icon for %d: %v", id, err)
		}
	}
}
