package lpn

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// squareFirstInteriorNpc returns the n-th NPCEnum.InteriorNpc of an NPCEnumList (0 = the fishing
// spot, 1 = the fish shop). squareNested takes the last repeated occurrence, so it cannot index.
func squareFirstInteriorNpc(t *testing.T, list []byte, n int) []byte {
	t.Helper()
	var entries [][]byte
	if !pbScan(list, func(f, _ uint64, d []byte) {
		if f == 1 && d != nil {
			entries = append(entries, d)
		}
	}) {
		t.Fatalf("NPC list malformed: %x", list)
	}
	if n >= len(entries) {
		t.Fatalf("NPC %d missing from %d entries", n, len(entries))
	}
	return squareNested(t, entries[n], 2)
}

// squareRepeated returns every occurrence of a repeated length-delimited field in data.
func squareRepeated(t *testing.T, data []byte, field uint64) [][]byte {
	t.Helper()
	var out [][]byte
	if !pbScan(data, func(f, _ uint64, d []byte) {
		if f == field && d != nil {
			out = append(out, d)
		}
	}) {
		t.Fatalf("message malformed: %x", data)
	}
	return out
}

func TestSquareFishShopNpcEncoding(t *testing.T) {
	// Tile (76,97) on the Treasure Island map (N=160): Cherry-made, no stock host there (the InteriorID is the stock item).
	if x, y := tilePx(squareIslandSize, squareShopTile); x != 5560 || y != 2920 || squareShopTile != (Tile{76, 97}) {
		t.Fatalf("shop tile = %v at px (%d,%d), want (76,97) at (5560,2920)", squareShopTile, x, y)
	}
	npc := squareFirstInteriorNpc(t, squareTreasureIslandNpcList(), 10)
	interior := squareNested(t, npc, 2)
	npcApp := squareNested(t, interior, 1)
	if got := squareUint(t, npcApp, 1); got != 0x3010001 {
		t.Fatalf("NpcIndex = %#x, want the fish shop 0x3010001", got)
	}
	if got := squareUint(t, interior, 5); got != squareShopInteriorID || squareShopInteriorID != 122005520 {
		t.Fatalf("InteriorID = %d", got)
	}
	px, py := tilePx(squareIslandSize, squareShopTile)
	if squareUint(t, npc, 1) != squareShopSerial || squareUint(t, npc, 10) != px || squareUint(t, npc, 11) != py {
		t.Fatalf("serial/Px/Py = %d/%d/%d", squareUint(t, npc, 1), squareUint(t, npc, 10), squareUint(t, npc, 11))
	}
}

// TestSquareFishShopGemsOnly checks the shop rows: Gem prices, no billing field anywhere,
// repeatable 20-use bait pack purchases with MaxCount 999, equip via msg 90, and balance checks.
func TestSquareFishShopGemsOnly(t *testing.T) {
	const aidText = "9821"
	aid := mustAid(t, aidText)
	c := fishingRoomAccount(t, aidText, 1000)
	start := fishingBalance(t, aidText) // Square entry credits the daily quest (40 Gems)

	c.send(87, nil)
	id, shop := c.read()
	if id != 115 {
		t.Fatalf("shop msgid = %d, want 115", id)
	}
	rows := squareRepeated(t, shop, 3)
	items := economy.FishingShopItems()
	if len(rows) != 3 || len(items) != 3 {
		t.Fatalf("shop rows = %d, items = %d, want 3", len(rows), len(items))
	}
	for i, row := range rows {
		it := items[i]
		if squareUint(t, row, 1) != uint64(it.ID) || squareUint(t, row, 3) != uint64(it.ID) {
			t.Fatalf("row %d ids = %x", i, row)
		}
		if squareUint(t, row, 5) != 1 || squareUint(t, row, 6) != uint64(it.Price) || squareUint(t, row, 7) != 20 {
			t.Fatalf("row %d is not a 20-use Gem price of %d: %x", i, it.Price, row)
		}
		if img, _, _ := pbBytesField(row, 12); !bytes.Equal(img, []byte(economy.FishingItemImageURL(it.ID))) {
			t.Fatalf("row %d ImageUrl = %q", i, img)
		}
		for _, billing := range []uint64{13, 21, 16, 17, 18, 19, 20, 22, 23} { // BillingCode, BillingText, sale, category, cpid
			if _, found, _ := pbBytesField(row, billing); found {
				t.Fatalf("row %d carries billing/sale field %d", i, billing)
			}
		}
		if _, found := protobufUintField(row, 13); found {
			t.Fatalf("row %d has a BillingCode varint", i)
		}
	}
	if squareUint(t, shop, 5) != uint64(start) || squareUint(t, shop, 6) != 0 {
		t.Fatalf("balances in shop reply = gem %d cash %d", squareUint(t, shop, 5), squareUint(t, shop, 6))
	}

	// Buy B Bait (9102, 150 Gems, 20 uses) twice -> 40 uses total.
	c.send(89, pbVar(pbVar(nil, 1, 9102), 2, 1))
	if id, res := c.read(); id != 117 || squareUint(t, res, 2) != 9102 || squareUint(t, res, 3) != 20 || squareUint(t, res, 4) != 20 {
		t.Fatalf("first buy reply = %d/%x", id, res)
	} else if _, found := protobufUintField(res, 1); found {
		t.Fatalf("buy Result set on success: %x", res)
	}
	c.send(89, pbVar(pbVar(nil, 1, 9102), 2, 1))
	if id, res := c.read(); id != 117 || squareUint(t, res, 2) != 9102 || squareUint(t, res, 3) != 20 || squareUint(t, res, 4) != 40 {
		t.Fatalf("second buy reply = %d/%x", id, res)
	}
	if got := fishingBalance(t, aidText); got != start-300 {
		t.Fatalf("gems after 2x B bait = %d, want %d", got, start-300)
	}

	// Buy S Bait (9104, 600 Gems, 20 uses) -> auto-equips S Bait (+35).
	c.send(89, pbVar(pbVar(nil, 1, 9104), 2, 1))
	if id, res := c.read(); id != 117 || squareUint(t, res, 2) != 9104 || squareUint(t, res, 3) != 20 || squareUint(t, res, 4) != 20 {
		t.Fatalf("buy S bait reply = %d/%x", id, res)
	}
	if got := fishingBalance(t, aidText); got != start-900 {
		t.Fatalf("gems after S bait = %d, want %d", got, start-900)
	}

	// Remaining Gems (140) < A Bait (300): refused with 393236.
	c.send(89, pbVar(pbVar(nil, 1, 9103), 2, 1))
	if id, res := c.read(); id != 117 || squareUint(t, res, 1) != 393236 {
		t.Fatalf("short buy = %d/%x, want result 393236", id, res)
	}

	// Bag still returns empty when squareBagShowsOwned is false.
	c.send(88, pbVar(nil, 1, 1))
	_, bag := c.read()
	bagRows := squareRepeated(t, bag, 2)
	if !squareBagShowsOwned && len(bagRows) != 0 {
		t.Fatalf("bait bag rows = %x, want none (switch off)", bag)
	}

	// Equip B Bait (9102) via msg 90 -> rc 118 with EquipItem and Aid.
	c.send(90, pbVar(pbVar(nil, 1, 9102), 2, economy.FishKindBait))
	if id, eq := c.read(); id != 118 || squareUint(t, eq, 1) != squareFishOK || squareUint(t, eq, 3) != aid {
		t.Fatalf("equip 9102 reply = %d/%x", id, eq)
	} else {
		itemMsg := squareNested(t, eq, 2)
		if squareUint(t, itemMsg, 1) != 9102 {
			t.Fatalf("equip item id = %d", squareUint(t, itemMsg, 1))
		}
		if img, _, _ := pbBytesField(itemMsg, 5); !bytes.Equal(img, []byte(economy.FishingItemInvenImageURL(9102))) {
			t.Fatalf("equip ImageUrl = %q", img)
		}
	}
	if bonus := economy.FishingGearBonus(aidText); bonus != 10 {
		t.Fatalf("gear bonus = %d, want 10 (B Bait)", bonus)
	}

	// Equip Basic Bait (9101) -> succeeds, bonus 0.
	c.send(90, pbVar(pbVar(nil, 1, 9101), 2, economy.FishKindBait))
	if id, eq := c.read(); id != 118 || squareUint(t, eq, 1) != squareFishOK {
		t.Fatalf("equip 9101 reply = %d/%x", id, eq)
	}
	if bonus := economy.FishingGearBonus(aidText); bonus != 0 {
		t.Fatalf("gear bonus with basic bait = %d, want 0", bonus)
	}

	// Equip unowned A Bait (9103) -> refused 655376.
	c.send(90, pbVar(pbVar(nil, 1, 9103), 2, economy.FishKindBait))
	if id, eq := c.read(); id != 118 || squareUint(t, eq, 1) != 655376 {
		t.Fatalf("equip unowned reply = %d/%x, want refused 655376", id, eq)
	}

	c.send(91, nil)
	if id, gear := c.read(); id != 119 || len(gear) != 0 {
		t.Fatalf("gear reply = %d/%x, want an empty list", id, gear)
	}

	// Shop InvenItems (4): 9102 (Count 40, MaxCount 999) and 9104 (Count 20, MaxCount 999).
	c.send(87, nil)
	_, shop = c.read()
	inv := squareRepeated(t, shop, 4)
	if len(inv) != 2 ||
		squareUint(t, inv[0], 1) != 9102 || squareUint(t, inv[0], 3) != 40 ||
		squareUint(t, inv[0], 7) != economy.FishKindBait || squareUint(t, inv[0], 9) != uint64(economy.MaxBaitUses) ||
		squareUint(t, inv[1], 1) != 9104 || squareUint(t, inv[1], 3) != 20 ||
		squareUint(t, inv[1], 7) != economy.FishKindBait || squareUint(t, inv[1], 9) != uint64(economy.MaxBaitUses) {
		t.Fatalf("shop owned rows = %x", shop)
	}
}

func mustAid(t *testing.T, aidText string) uint64 {
	t.Helper()
	n, err := strconv.ParseUint(aidText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestSquareFishShopSaveFailureKeepsGems checks that a failed save refunds nothing and grants nothing.
func TestSquareFishShopSaveFailureKeepsGems(t *testing.T) {
	const aidText = "9822"
	testutil.InstallAccounts(t, map[string]*store.Account{"fish-" + aidText: {AccessToken: "fish-" + aidText, SessionKey: "sk", Aid: aidText, Name: "Fisher", Gems: 500}})
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file, not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.FailSaves(t)
	status, gems, total := economy.BuyFishingItem(aidText, 9102)
	if status != economy.FishBuyFailed || gems != 500 || total != 0 {
		t.Fatalf("status=%d gems=%d total=%d, want failed and 500 unchanged", status, gems, total)
	}
	if owned, _ := economy.FishingBag(aidText, economy.FishKindBait); len(owned) != 1 || owned[0].ID != 9101 {
		t.Fatalf("bait kept after a failed save: %+v", owned)
	}
}

// TestSquareFishBagRowLayout pins the bag row Cherry sends while squareBagShowsOwned is on:
// InvenItemClient {Asset (1, message): ItemId 1, Name 2, Description 4, ImageUrl 5 = "", PlayingImageUrl 6 = "",
// MaxCount 7, ItemType 8, ValidStartDateMs 9 = 0, ValidEndDateMs 10 = 0; Count 2}. The parser takes field 1
// only as tag 10 (wire type 2), so the Asset must be a message, never a varint.
func TestSquareFishBagRowLayout(t *testing.T) {
	it, ok := economy.FishingItemByID(9102)
	if !ok {
		t.Fatal("no bait")
	}
	row := squareInvenItemClient(it, 20)
	if _, found := protobufUintField(row, 1); found {
		t.Fatalf("Asset written as a varint: %x", row)
	}
	if squareUint(t, row, 2) != 20 {
		t.Fatalf("Count = %d", squareUint(t, row, 2))
	}
	asset := squareNested(t, row, 1)
	if squareUint(t, asset, 1) != 9102 || squareUint(t, asset, 7) != 1 || squareUint(t, asset, 8) != economy.FishKindBait {
		t.Fatalf("asset ids = %x", asset)
	}
	for _, f := range []uint64{9, 10} {
		if v, found := protobufUintField(asset, f); !found || v != 0 {
			t.Fatalf("period field %d = %d found=%v", f, v, found)
		}
	}
	for _, f := range []uint64{2, 4} { // Name, Description: plain text, no template placeholders
		d, found, _ := pbBytesField(asset, f)
		if !found || len(d) == 0 || bytes.Contains(d, []byte("%s")) || bytes.Contains(d, []byte("%d")) {
			t.Fatalf("text field %d = %q found=%v", f, d, found)
		}
	}
	for _, f := range []uint64{5, 6} { // ImageUrl, PlayingImageUrl: present and empty
		if d, found, _ := pbBytesField(asset, f); !found || len(d) != 0 {
			t.Fatalf("picture field %d = %q found=%v, want empty", f, d, found)
		}
	}
}
