package lpn

import (
	"bytes"
	"testing"

	"cherry/internal/economy"
)

// bookEntries returns the raw UserFish entries of rc_fish_collection_res.FishCompleteCollection (field 8).
func bookEntries(t *testing.T, body []byte) [][]byte {
	t.Helper()
	coll := squareNested(t, body, 8)
	var out [][]byte
	if !pbScan(coll, func(f, _ uint64, d []byte) {
		if f == 1 && d != nil {
			out = append(out, d)
		}
	}) {
		t.Fatalf("collection malformed: %x", coll)
	}
	return out
}

// The fishing book, the bag, the fish shop, the boss refusal and the collection reward, end to end
// through the Square room. Only the fields the client reads are checked; no billing rows appear.
func TestSquareFishingBookAndScreens(t *testing.T) {
	const aidText = "9811"
	c := fishingRoomAccount(t, aidText, 0)
	cat := economy.FishCatalog()
	now := economy.NowFunc()
	if _, ok := economy.FishCatch(aidText, cat[0], now); !ok { // Square Carp
		t.Fatal("catch failed")
	}
	if _, ok := economy.FishCatch(aidText, cat[6], now); !ok { // Golden Square Koi (rare)
		t.Fatal("rare catch failed")
	}

	// Book, page 1 of 1: one entry per catalogue fish, in catalogue order, locked unless caught.
	c.send(25, pbVar(nil, 1, 1))
	id, body := c.read()
	if id != 28 {
		t.Fatalf("book msgid = %d, want 28", id)
	}
	if squareUint(t, body, 1) != squareFishOK || squareUint(t, body, 2) != squareFishPages ||
		squareUint(t, body, 3) != 1 || squareUint(t, body, 4) != 3 || squareUint(t, body, 6) != 0 {
		t.Fatalf("book header = %x (want result %d, max 1, page 1, version 3, coin 0)", body, squareFishOK)
	}
	entries := bookEntries(t, body)
	if len(entries) != len(cat) {
		t.Fatalf("book entries = %d, want %d", len(entries), len(cat))
	}
	for i, e := range entries {
		f := cat[i]
		if squareUint(t, e, 1) != uint64(i+1) || squareUint(t, e, 2) != uint64(f.ID) {
			t.Fatalf("entry %d index/id = %x", i, e)
		}
		count := squareUint(t, e, 3)
		name, hasName, _ := pbBytesField(e, 4)
		if i != 0 && i != 6 {
			if count != 0 || hasName {
				t.Fatalf("locked entry %d leaks: count=%d name=%q", i, count, name)
			}
			continue
		}
		url := []byte(economy.FishImageURL(f.ID))
		if count != 1 || !hasName || string(name) != f.Name || squareUint(t, e, 6) != uint64(f.Level) {
			t.Fatalf("caught entry %d = count %d name %q level %d", i, count, name, squareUint(t, e, 6))
		}
		if u, _, _ := pbBytesField(e, 5); !bytes.Equal(u, url) {
			t.Fatalf("entry %d FishUrl = %q", i, u)
		}
		if d, _, _ := pbBytesField(e, 7); !bytes.Equal(d, url) {
			t.Fatalf("entry %d DpImageUrl = %q", i, d)
		}
	}

	// A page past MaxPage is served as page 1 and says so.
	c.send(25, pbVar(nil, 1, 2))
	if id, body := c.read(); id != 28 || squareUint(t, body, 3) != 1 {
		t.Fatalf("page 2 reply = %d/%x, want page 1", id, body)
	}

	// Collection reward: refused as {Success false}, no payout.
	c.send(26, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 29 || !bytes.Equal(body, []byte{0x08, 0}) {
		t.Fatalf("complete = %d/%x, want 29 Success=false", id, body)
	}

	// Boss try: started with the day's first try, then rc6 enters the boss world (theme 28).
	c.send(45, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 57 || squareUint(t, body, 2) != 1 {
		t.Fatalf("boss try = %d/%x, want 57 started", id, body)
	}
	if id, body := c.read(); id != 6 || squareUint(t, body, 4) != squareBossThemeID {
		t.Fatalf("boss room enter = %d/%x, want rc6 theme %d", id, body, squareBossThemeID)
	}

	// Bag: empty, BagType echoed.
	c.send(88, pbVar(nil, 1, 3))
	if id, body := c.read(); id != 116 || !bytes.Equal(body, pbVar(pbVar(nil, 1, squareFishOK), 3, 3)) {
		t.Fatalf("bag = %d/%x, want 116 empty BagType 3", id, body)
	}

	// Fish shop: the Gem-priced rods and bait (shop_test checks the rows); Gem is the real balance
	// and Cash the real Cash (0 here).
	c.send(87, nil)
	id, shop := c.read()
	if id != 115 {
		t.Fatalf("shop msgid = %d, want 115", id)
	}
	if squareUint(t, shop, 1) != squareFishOK || squareUint(t, shop, 5) != uint64(fishingBalance(t, aidText)) || squareUint(t, shop, 6) != 0 {
		t.Fatalf("shop = %x", shop)
	}
	if n := len(squareRepeated(t, shop, 3)); n != len(economy.FishingShopItems()) {
		t.Fatalf("shop rows = %d, want %d", n, len(economy.FishingShopItems()))
	}
}
