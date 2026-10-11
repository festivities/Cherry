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
	// Tile (115,94) on the N=160 Square map: the unique RUGO0049C host of the shop's InteriorID.
	if x, y := 40*(115-94+160), 20*(2*160-1-115-94); x != squareShopPX || y != squareShopPY {
		t.Fatalf("shop px = (%d,%d), tile (115,94) = (%d,%d)", squareShopPX, squareShopPY, x, y)
	}
	npc := squareFirstInteriorNpc(t, squareFishingSpotList(), 1)
	interior := squareNested(t, npc, 2)
	npcApp := squareNested(t, interior, 1)
	if got := squareUint(t, npcApp, 1); got != 0x3010001 {
		t.Fatalf("NpcIndex = %#x, want the fish shop 0x3010001", got)
	}
	if got := squareUint(t, interior, 5); got != squareShopInteriorID || squareShopInteriorID != 122005520 {
		t.Fatalf("InteriorID = %d", got)
	}
	if squareUint(t, npc, 1) != squareShopSerial || squareUint(t, npc, 10) != squareShopPX || squareUint(t, npc, 11) != squareShopPY {
		t.Fatalf("serial/Px/Py = %d/%d/%d", squareUint(t, npc, 1), squareUint(t, npc, 10), squareUint(t, npc, 11))
	}
}

// TestSquareFishShopGemsOnly checks the shop rows: Gem prices, no billing field anywhere, and
// bag, equip and buy with the balance moving by exactly the price.
func TestSquareFishShopGemsOnly(t *testing.T) {
	const aidText = "9821"
	c := fishingRoomAccount(t, aidText, 100)
	start := fishingBalance(t, aidText) // Square entry credits the daily quest (40 Gems)

	c.send(87, nil)
	id, shop := c.read()
	if id != 115 {
		t.Fatalf("shop msgid = %d, want 115", id)
	}
	rows := squareRepeated(t, shop, 3)
	items := economy.FishingShopItems()
	if len(rows) != len(items) {
		t.Fatalf("shop rows = %d, want %d", len(rows), len(items))
	}
	for i, row := range rows {
		it := items[i]
		if squareUint(t, row, 1) != uint64(it.ID) || squareUint(t, row, 3) != uint64(it.ID) {
			t.Fatalf("row %d ids = %x", i, row)
		}
		if squareUint(t, row, 5) != 1 || squareUint(t, row, 6) != uint64(it.Price) {
			t.Fatalf("row %d is not a Gem price of %d: %x", i, it.Price, row)
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

	// Empty bag, then buy the cheap bait: 40 Gems leave the balance.
	c.send(88, pbVar(nil, 1, 1))
	if id, bag := c.read(); id != 116 || len(squareRepeated(t, bag, 2)) != 0 {
		t.Fatalf("bag = %d/%x, want empty", id, bag)
	}
	c.send(89, pbVar(pbVar(nil, 1, 9101), 2, 1))
	if id, res := c.read(); id != 117 || squareUint(t, res, 2) != 9101 {
		t.Fatalf("buy reply = %d/%x", id, res)
	} else if _, found := protobufUintField(res, 1); found {
		t.Fatalf("buy Result set on success: %x", res)
	}
	if got := fishingBalance(t, aidText); got != start-40 {
		t.Fatalf("gems after bait = %d, want %d", got, start-40)
	}

	// Owned items cannot be bought twice (retention limit message 1878 is result 393237).
	c.send(89, pbVar(pbVar(nil, 1, 9101), 2, 1))
	if id, res := c.read(); id != 117 || squareUint(t, res, 1) != 393237 {
		t.Fatalf("second buy = %d/%x, want result 393237", id, res)
	}
	if got := fishingBalance(t, aidText); got != start-40 {
		t.Fatalf("gems after refused buy = %d, want %d", got, start-40)
	}

	// A rod costs 150 Gems and the account has less than that: refused, nothing owned.
	c.send(89, pbVar(pbVar(nil, 1, 9001), 2, 1))
	if id, res := c.read(); id != 117 || squareUint(t, res, 1) != 393236 {
		t.Fatalf("rod buy = %d/%x, want result 393236", id, res)
	}

	// Buying the bait equips it (the bag is not shown): the bonus applies, the bag and the in-use
	// list stay empty, and an equip request is refused.
	c.send(88, pbVar(nil, 1, 1))
	_, bag := c.read()
	bagRows := squareRepeated(t, bag, 2)
	if !squareBagShowsOwned {
		if len(bagRows) != 0 {
			t.Fatalf("bait bag rows = %x, want none (switch off)", bag)
		}
	} else {
		if len(bagRows) != 1 || squareUint(t, bagRows[0], 2) != 1 {
			t.Fatalf("bait bag rows = %x, want one row with Count 1", bag)
		}
		asset := squareNested(t, bagRows[0], 1)
		if squareUint(t, asset, 1) != 9101 || squareUint(t, asset, 8) != economy.FishKindBait {
			t.Fatalf("bait row asset = %x", asset)
		}
		for _, f := range []uint64{5, 6} { // ImageUrl, PlayingImageUrl: present and empty
			if d, found, _ := pbBytesField(asset, f); !found || len(d) != 0 {
				t.Fatalf("picture field %d = %q found=%v, want empty", f, d, found)
			}
		}
	}
	c.send(90, pbVar(nil, 1, 9101))
	if id, eq := c.read(); id != 118 || squareUint(t, eq, 1) != 655376 {
		t.Fatalf("equip reply = %d/%x, want refused 655376", id, eq)
	}
	if owned, bait := economy.FishingBag(aidText, economy.FishKindBait); len(owned) != 1 || bait != 9101 {
		t.Fatalf("bait in use = %d owned %d", bait, len(owned))
	}
	if bonus := economy.FishingGearBonus(aidText); bonus != 1 {
		t.Fatalf("gear bonus = %d, want 1 (Worm Bait)", bonus)
	}
	c.send(91, nil)
	if id, gear := c.read(); id != 119 || len(gear) != 0 {
		t.Fatalf("gear reply = %d/%x, want an empty list", id, gear)
	}

	// The shop lists the owned bait as InvenItems (4): ItemId 1, Count 3, ItemType 7, MaxCount 9.
	c.send(87, nil)
	_, shop = c.read()
	inv := squareRepeated(t, shop, 4)
	if len(inv) != 1 || squareUint(t, inv[0], 1) != 9101 || squareUint(t, inv[0], 3) != 1 ||
		squareUint(t, inv[0], 7) != economy.FishKindBait || squareUint(t, inv[0], 9) != 1 {
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
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json") // MkdirAll fails: the parent is a file
	store.AccountsMu.Unlock()
	status, gems := economy.BuyFishingItem(aidText, 9101)
	if status != economy.FishBuyFailed || gems != 500 {
		t.Fatalf("status=%d gems=%d, want failed and 500 unchanged", status, gems)
	}
	if owned, _ := economy.FishingBag(aidText, economy.FishKindBait); len(owned) != 0 {
		t.Fatalf("bait kept after a failed save: %d", len(owned))
	}
}

// TestSquareFishBagRowLayout pins the bag row Cherry sends while squareBagShowsOwned is on:
// InvenItemClient {Asset (1, message): ItemId 1, Name 2, Description 4, ImageUrl 5 = "", PlayingImageUrl 6 = "",
// MaxCount 7, ItemType 8, ValidStartDateMs 9 = 0, ValidEndDateMs 10 = 0; Count 2}. The parser takes field 1
// only as tag 10 (wire type 2), so the Asset must be a message, never a varint.
func TestSquareFishBagRowLayout(t *testing.T) {
	it, ok := economy.FishingItemByID(9101)
	if !ok {
		t.Fatal("no bait")
	}
	row := squareInvenItemClient(it, 1)
	if _, found := protobufUintField(row, 1); found {
		t.Fatalf("Asset written as a varint: %x", row)
	}
	if squareUint(t, row, 2) != 1 {
		t.Fatalf("Count = %d", squareUint(t, row, 2))
	}
	asset := squareNested(t, row, 1)
	if squareUint(t, asset, 1) != 9101 || squareUint(t, asset, 7) != 1 || squareUint(t, asset, 8) != economy.FishKindBait {
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
