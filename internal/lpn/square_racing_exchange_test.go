package lpn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// rideShopAccount installs a non-lab account with Ride Points, Gems, Cash and an inventory, saving into a
// temp dir.
func rideShopAccount(t *testing.T, aid string, points int64, inventory []string) {
	t.Helper()
	acc := &store.Account{AccessToken: "ride-" + aid, SessionKey: "sk", Aid: aid, Name: "Ride", Gems: 7, Cash: 3,
		InventoryCodes: inventory, Racing: &store.RacingRecord{Points: points}}
	testutil.InstallAccounts(t, map[string]*store.Account{"ride-" + aid: acc})
	testutil.SetLab(t, aid, false)
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Unlock()
}

// rideShopBalances is aid's Ride Points, Gems and Cash.
func rideShopBalances(t *testing.T, aid string) (points, gems, cash int64) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	if acc.Racing != nil {
		points = acc.Racing.Points
	}
	return points, acc.Gems, acc.Cash
}

// rideShopRefused reads an rc81 refusal: Result 1, the echoed shop and item, coin 5, the price and the balance.
func rideShopRefused(t *testing.T, c *sqClient, shop, item, price, balance uint64) {
	t.Helper()
	id, body := c.read()
	if id != 81 || squareUint(t, body, 1) != 1 || squareUint(t, body, 2) != shop || squareUint(t, body, 3) != item ||
		squareUint(t, body, 4) != 5 || squareUint(t, body, 5) != price || squareUint(t, body, 6) != balance {
		t.Fatalf("refusal = %d/%x, want rc81 result 1, shop %d, item %d, coin 5, price %d, balance %d", id, body, shop, item, price, balance)
	}
}

// TestRideShopNpcEncoding: the Ride Point shop is the sixth NPC of the room enter list (after the race
// entrance), a second 0x3010035 on serial 5 at tile (84,85), with AniEventType 2 (popup 117); the race
// entrance is unchanged (serial 4, AniEventType 4).
func TestRideShopNpcEncoding(t *testing.T) {
	if x, y := 40*(84-85+160), 20*(2*160-1-84-85); x != squareRideShopPX || y != squareRideShopPY {
		t.Fatalf("shop px = (%d,%d), tile (84,85) = (%d,%d)", squareRideShopPX, squareRideShopPY, x, y)
	}
	list := squareFishingSpotList()
	npc := squareFirstInteriorNpc(t, list, 4)
	interior := squareNested(t, npc, 2)
	if got := squareUint(t, squareNested(t, interior, 1), 1); got != 0x3010035 {
		t.Fatalf("NpcIndex = %#x, want 0x3010035", got)
	}
	if got := squareUint(t, interior, 5); got != 122003262 {
		t.Fatalf("InteriorID = %d, want RUGO002IM 122003262", got)
	}
	if squareUint(t, npc, 1) != 5 || squareUint(t, npc, 10) != squareRideShopPX || squareUint(t, npc, 11) != squareRideShopPY {
		t.Fatalf("serial/Px/Py = %d/%d/%d", squareUint(t, npc, 1), squareUint(t, npc, 10), squareUint(t, npc, 11))
	}
	if squareUint(t, npc, 63) != 1 || squareUint(t, npc, 75) != 1 || squareUint(t, npc, 76) != 2 {
		t.Fatalf("f63/f75/f76 = %d/%d/%d, want 1/1/2", squareUint(t, npc, 63), squareUint(t, npc, 75), squareUint(t, npc, 76))
	}
	race := squareFirstInteriorNpc(t, list, 3)
	if squareUint(t, race, 1) != 4 || squareUint(t, race, 76) != 4 {
		t.Fatalf("race entrance serial/state = %d/%d, want 4/4", squareUint(t, race, 1), squareUint(t, race, 76))
	}
}

// TestRideShopListShape: cr61 gets the one shop with the five default rows (clothing ItemType 1, room 2),
// a period in epoch ms, and Ride Points (coin 5) as the only balance.
func TestRideShopListShape(t *testing.T) {
	requireExchangeArchive(t)
	rideShopAccount(t, "9861", 50, nil)
	exchangeAt(t, time.Date(2026, time.October, 26, 12, 0, 0, 0, exchangeJST))
	c := newSqClient(t, &squareRoom{}, 5)
	c.join(9861)

	c.send(61, pbVar(nil, 1, 1))
	id, body := c.read()
	if id != 80 {
		t.Fatalf("list msgid = %d, want 80", id)
	}
	shop := squareNested(t, body, 1)
	if squareUint(t, shop, 1) != 1 || squareUint(t, shop, 2) != 1 || squareUint(t, shop, 5) != 5 {
		t.Fatalf("shop ids = %d/%d/%d, want shop 1, type 1, coin 5", squareUint(t, shop, 1), squareUint(t, shop, 2), squareUint(t, shop, 5))
	}
	if begin, end := squareUint(t, shop, 3), squareUint(t, shop, 4); begin == 0 || end <= begin {
		t.Fatalf("period = %d..%d, want epoch ms", begin, end)
	}
	rows := squareRepeated(t, shop, 7)
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	first := rows[0]
	if squareUint(t, first, 1) != 1 || squareUint(t, first, 2) != 1 || squareUint(t, first, 7) != 20 || squareUint(t, first, 4) != 1 {
		t.Fatalf("row 1 = id %d type %d price %d count %d", squareUint(t, first, 1), squareUint(t, first, 2), squareUint(t, first, 7), squareUint(t, first, 4))
	}
	if code, _, _ := pbBytesField(first, 3); string(code) != "CUTO0018W" {
		t.Fatalf("row 1 item_code = %q", code)
	}
	if url, _, _ := pbBytesField(first, 14); string(url) == "" || string(url) != economy.ExchangeItemImageURL("CUTO0018W") {
		t.Fatalf("row 1 img_url = %q", url)
	}
	if squareUint(t, first, 16) != 0 || squareUint(t, first, 17) != 0 {
		t.Fatalf("row 1 remain/status = %d/%d, want 0/0", squareUint(t, first, 16), squareUint(t, first, 17))
	}
	if squareUint(t, rows[2], 2) != 2 { // RUWA000MV, a room item
		t.Fatalf("row 3 item type = %d, want 2 (room)", squareUint(t, rows[2], 2))
	}
	balances := squareRepeated(t, body, 2)
	if len(balances) != 1 || squareUint(t, balances[0], 1) != 5 || squareUint(t, balances[0], 2) != 50 {
		t.Fatalf("balances = %x, want one entry coin 5 = 50", balances)
	}
}

// TestRideShopBuyCharges: a buy takes only Ride Points, grants the item, and replies with the balance and the
// reward. The next list marks the bought row owned. Spending exactly the balance is allowed.
func TestRideShopBuyCharges(t *testing.T) {
	requireExchangeArchive(t)
	rideShopAccount(t, "9862", 50, nil)
	exchangeAt(t, time.Date(2026, time.October, 26, 12, 0, 0, 0, exchangeJST))
	c := newSqClient(t, &squareRoom{}, 6)
	c.join(9862)
	_, gems0, cash0 := rideShopBalances(t, "9862") // the join may pay the day's login gift

	c.send(62, pbVar(pbVar(nil, 1, 1), 2, 1)) // ShopId 1, ItemId 1 (the row id)
	id, body := c.read()
	if id != 81 || squareUint(t, body, 1) != 0 || squareUint(t, body, 2) != 1 || squareUint(t, body, 3) != 1 ||
		squareUint(t, body, 4) != 5 || squareUint(t, body, 5) != 20 || squareUint(t, body, 6) != 30 {
		t.Fatalf("buy = %d/%x, want rc81 result 0, shop 1, item 1, coin 5, price 20, balance 30", id, body)
	}
	reward := squareNested(t, body, 7)
	if code, _, _ := pbBytesField(reward, 3); string(code) != "CUTO0018W" || squareUint(t, reward, 1) != 1 || squareUint(t, reward, 4) != 1 {
		t.Fatalf("reward = %x", reward)
	}
	if points, gems, cash := rideShopBalances(t, "9862"); points != 30 || gems != gems0 || cash != cash0 {
		t.Fatalf("after the buy: points %d gems %d cash %d, want points 30 and the join's gems %d and cash %d", points, gems, cash, gems0, cash0)
	}
	if !exchangeOwns(t, "9862", "CUTO0018W") {
		t.Fatal("the item was not granted")
	}
	if got := economy.BossCoinBalance("9862"); got != 0 {
		t.Fatalf("Boss Coins = %d, want untouched", got)
	}

	c.send(61, pbVar(nil, 1, 1))
	_, list := c.read()
	rows := squareRepeated(t, squareNested(t, list, 1), 7)
	if squareUint(t, rows[0], 17) != 2 || squareUint(t, rows[1], 17) != 0 {
		t.Fatalf("status after the buy = %d/%d, want owned 2 then 0", squareUint(t, rows[0], 17), squareUint(t, rows[1], 17))
	}
	if left := squareUint(t, squareNested(t, list, 2), 2); left != 30 {
		t.Fatalf("list balance = %d, want 30", left)
	}

	// Row 2 costs 30: exactly the balance buys it, and the account is left with none.
	c.send(62, pbVar(pbVar(nil, 1, 1), 2, 2))
	if id, body := c.read(); id != 81 || squareUint(t, body, 1) != 0 || squareUint(t, body, 6) != 0 {
		t.Fatalf("exact buy = %d/%x, want result 0 and balance 0", id, body)
	}
	if points, _, _ := rideShopBalances(t, "9862"); points != 0 {
		t.Fatalf("points = %d, want 0", points)
	}

	raw, err := os.ReadFile(filepath.Join(filepath.Dir(store.AccountStorePath), "ledger.jsonl"))
	if err != nil || !strings.Contains(string(raw), `"currency":"ridePoints"`) || !strings.Contains(string(raw), "racingexchange:1 CUTO0018W") {
		t.Fatalf("ledger %q: %v", raw, err)
	}
}

// TestRideShopRefusals: a short balance, an unknown row and an owned item each get Result 1 with the echo
// fields, and change nothing.
func TestRideShopRefusals(t *testing.T) {
	requireExchangeArchive(t)
	exchangeAt(t, time.Date(2026, time.October, 26, 12, 0, 0, 0, exchangeJST))

	rideShopAccount(t, "9863", 20, nil)
	c := newSqClient(t, &squareRoom{}, 7)
	c.join(9863)
	_, gems0, cash0 := rideShopBalances(t, "9863")
	c.send(62, pbVar(pbVar(nil, 1, 1), 2, 2)) // row 2 costs 30, 20 held
	rideShopRefused(t, c, 1, 2, 30, 20)
	c.send(62, pbVar(pbVar(nil, 1, 1), 2, 99)) // no such row
	rideShopRefused(t, c, 1, 99, 0, 20)
	c.send(62, pbVar(pbVar(nil, 1, 1), 2, 1)) // row 1 costs 20: affordable, so this one is bought
	if id, body := c.read(); id != 81 || squareUint(t, body, 1) != 0 {
		t.Fatalf("affordable buy = %d/%x", id, body)
	}
	if points, gems, cash := rideShopBalances(t, "9863"); points != 0 || gems != gems0 || cash != cash0 {
		t.Fatalf("points %d gems %d cash %d after the refusals, want the join's gems %d and cash %d", points, gems, cash, gems0, cash0)
	}

	rideShopAccount(t, "9864", 50, []string{"CUTO0018W"})
	owned := newSqClient(t, &squareRoom{}, 8)
	owned.join(9864)
	owned.send(62, pbVar(pbVar(nil, 1, 1), 2, 1)) // already owned
	rideShopRefused(t, owned, 1, 1, 20, 50)
	if points, _, _ := rideShopBalances(t, "9864"); points != 50 {
		t.Fatalf("an owned item was charged: points %d", points)
	}
}
