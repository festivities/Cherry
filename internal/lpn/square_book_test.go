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

// The fishing book (9 pages of 9, per-page reward + RewardRatio), the bag, the fish shop and the
// boss try, end to end through the Square room.
func TestSquareFishingBookAndScreens(t *testing.T) {
	const aidText = "9811"
	c := fishingRoomAccount(t, aidText, 0)
	cat := economy.FishCatalog()
	now := economy.NowFunc()
	if _, ok := economy.FishCatch(aidText, cat[0], now); !ok { // 4101 Mackerel
		t.Fatal("catch failed")
	}
	if _, ok := economy.FishCatch(aidText, cat[6], now); !ok { // 4107 Striped beakfish
		t.Fatal("second catch failed")
	}

	// Book, page 1 of 9: 9 entries, in catalogue order, locked unless caught.
	c.send(25, pbVar(nil, 1, 1))
	id, body := c.read()
	if id != 28 {
		t.Fatalf("book msgid = %d, want 28", id)
	}
	if squareUint(t, body, 1) != squareFishOK || squareUint(t, body, 2) != 9 ||
		squareUint(t, body, 3) != 1 || squareUint(t, body, 4) != 3 ||
		squareUint(t, body, 5) != 4 || squareUint(t, body, 6) != 100 ||
		squareUint(t, body, 9) != 0 || squareUint(t, body, 10) != 0 {
		t.Fatalf("book header = %x", body)
	}
	if prev, _, _ := pbBytesField(body, 12); !bytes.Equal(prev, []byte(economy.FishImageURL(cat[8].ID))) {
		t.Fatalf("preview url = %q, want %q", prev, economy.FishImageURL(cat[8].ID))
	}
	ratios := squareRepeated(t, body, 11)
	if len(ratios) != 2 ||
		squareUint(t, ratios[0], 1) != 1 || squareUint(t, ratios[0], 2) != 900000 ||
		squareUint(t, ratios[1], 1) != 2 || squareUint(t, ratios[1], 2) != 100000 {
		t.Fatalf("RewardRatio = %x", ratios)
	}
	entries := bookEntries(t, body)
	if len(entries) != 9 {
		t.Fatalf("book entries = %d, want 9", len(entries))
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

	// Page 9 returns the last 9 species (4173-4181) with last fish preview URL.
	c.send(25, pbVar(nil, 1, 9))
	if id, body := c.read(); id != 28 || squareUint(t, body, 3) != 9 || len(bookEntries(t, body)) != 9 {
		t.Fatalf("page 9 reply = %d/%x", id, body)
	} else if prev, _, _ := pbBytesField(body, 12); !bytes.Equal(prev, []byte(economy.FishImageURL(cat[80].ID))) {
		t.Fatalf("page 9 preview = %q", prev)
	}

	// Out-of-range page numbers clamp to 1..9.
	c.send(25, pbVar(nil, 1, 10))
	if id, body := c.read(); id != 28 || squareUint(t, body, 3) != 9 {
		t.Fatalf("page 10 reply = %d/%x, want page 9", id, body)
	}
	c.send(25, pbVar(nil, 1, 0))
	if id, body := c.read(); id != 28 || squareUint(t, body, 3) != 1 {
		t.Fatalf("page 0 reply = %d/%x, want page 1", id, body)
	}

	// Incomplete page claim is refused as {Success false}.
	c.send(26, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 29 || !bytes.Equal(body, []byte{0x08, 0}) {
		t.Fatalf("incomplete complete = %d/%x, want 29 Success=false", id, body)
	}

	// Catch remaining 7 species on page 1 -> PossibleComplete=1 -> claim pays 100 Gems once.
	for i := 0; i < 9; i++ {
		if i == 0 || i == 6 {
			continue
		}
		if _, ok := economy.FishCatch(aidText, cat[i], now); !ok {
			t.Fatalf("catch %d failed", i)
		}
	}
	c.send(25, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 28 || squareUint(t, body, 9) != 1 || squareUint(t, body, 10) != 0 {
		t.Fatalf("ready page 1 = %d/%x, want PossibleComplete=1 CompleteAlready=0", id, body)
	}
	beforeClaim := fishingBalance(t, aidText)
	c.send(26, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 29 || !bytes.Equal(body, []byte{0x08, 1}) {
		t.Fatalf("claim page 1 = %d/%x, want 29 Success=true", id, body)
	}
	if got := fishingBalance(t, aidText); got != beforeClaim+100 {
		t.Fatalf("balance after page 1 claim = %d, want %d", got, beforeClaim+100)
	}
	c.send(25, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 28 || squareUint(t, body, 9) != 0 || squareUint(t, body, 10) != 1 {
		t.Fatalf("claimed page 1 = %d/%x, want PossibleComplete=0 CompleteAlready=1", id, body)
	}
	// Duplicate claim is refused without paying again.
	c.send(26, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 29 || !bytes.Equal(body, []byte{0x08, 0}) {
		t.Fatalf("duplicate claim = %d/%x, want 29 Success=false", id, body)
	}
	if got := fishingBalance(t, aidText); got != beforeClaim+100 {
		t.Fatalf("balance changed on duplicate claim: %d", got)
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

	// Fish shop: the 3 bait packs; Gem is the real balance and Cash the real Cash (0 here).
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
