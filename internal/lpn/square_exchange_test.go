package lpn

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

var exchangeJST = time.FixedZone("JST", 9*3600)

// exchangeAccount installs a non-lab account with Boss Coins and an inventory, saving into a temp dir.
func exchangeAccount(t *testing.T, aid string, coins int64, inventory []string) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"xtok-" + aid: {AccessToken: "xtok-" + aid, SessionKey: "sk", Aid: aid, Name: "Exch", BossCoins: coins, InventoryCodes: inventory}})
	testutil.SetLab(t, aid, false)
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Unlock()
}

// exchangeAt fixes the economy clock (the office's event rows follow it) until the test ends.
func exchangeAt(t *testing.T, when time.Time) {
	t.Helper()
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return when }
	t.Cleanup(func() { economy.NowFunc = old })
}

// exchangeOwns reports whether aid's account holds code as clothing.
func exchangeOwns(t *testing.T, aid, code string) bool {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return slices.Contains(store.AccountOwnedCodes(acc), code)
}

// requireExchangeArchive skips a test that needs the item archive's art folders for the default rows.
func requireExchangeArchive(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(thumbs.DpItemRoot, "dress")); err != nil {
		t.Skip("item archive not present")
	}
}

// TestSquareExchangeNpcEncoding: the office is the third NPC of the room enter list, on the unique
// RUGO002IG host (interior 122003256) at tile (89,90), and the fishing spot and the fish shop are unchanged.
func TestSquareExchangeNpcEncoding(t *testing.T) {
	if x, y := 40*(89-90+160), 20*(2*160-1-89-90); x != squareExchangePX || y != squareExchangePY {
		t.Fatalf("office px = (%d,%d), tile (89,90) = (%d,%d)", squareExchangePX, squareExchangePY, x, y)
	}
	list := squareFishingSpotList()
	npc := squareFirstInteriorNpc(t, list, 2)
	interior := squareNested(t, npc, 2)
	npcApp := squareNested(t, interior, 1)
	if got := squareUint(t, npcApp, 1); got != 0x3010034 {
		t.Fatalf("NpcIndex = %#x, want the exchange office 0x3010034", got)
	}
	if got := squareUint(t, interior, 5); got != 122003256 {
		t.Fatalf("InteriorID = %d, want RUGO002IG 122003256", got)
	}
	if squareUint(t, npc, 1) != squareExchangeSerial || squareUint(t, npc, 10) != squareExchangePX || squareUint(t, npc, 11) != squareExchangePY {
		t.Fatalf("serial/Px/Py = %d/%d/%d", squareUint(t, npc, 1), squareUint(t, npc, 10), squareUint(t, npc, 11))
	}
	if n := len(squareRepeated(t, list, 1)); n != 4 {
		t.Fatalf("NPC list has %d entries, want the spot, the shop, the office and the race entrance", n)
	}
}

// TestSquareExchangeListShape: cr52 gets one group with the standing rows (the event rows only inside
// the Halloween season), the title, and the account's Boss Coins as the only cost entry.
func TestSquareExchangeListShape(t *testing.T) {
	requireExchangeArchive(t)
	exchangeAccount(t, "9841", 50, nil)
	exchangeAt(t, time.Date(2026, time.October, 24, 12, 0, 0, 0, exchangeJST))
	c := newSqClient(t, &squareRoom{}, 1)
	c.join(9841)

	c.send(52, pbVar(nil, 1, 1))
	id, body := c.read()
	if id != 68 {
		t.Fatalf("list msgid = %d, want 68", id)
	}
	group := squareNested(t, body, 1)
	if squareUint(t, group, 1) != 1 {
		t.Fatalf("group_id = %d, want 1", squareUint(t, group, 1))
	}
	if title, _, _ := pbBytesField(group, 6); string(title) != "Boss Coin Exchange" {
		t.Fatalf("title = %q", title)
	}
	rows := squareRepeated(t, group, 5)
	if len(rows) != 6 {
		t.Fatalf("standing rows = %d, want 6 outside the Halloween season", len(rows))
	}
	first := rows[0]
	if squareUint(t, first, 1) != 1001 || squareUint(t, first, 6) != 3 || squareUint(t, first, 7) != 30 {
		t.Fatalf("row 1 = gacha %d coin %d price %d", squareUint(t, first, 1), squareUint(t, first, 6), squareUint(t, first, 7))
	}
	item := squareNested(t, first, 12)
	if code, _, _ := pbBytesField(item, 3); string(code) != "CUON00059" {
		t.Fatalf("item_code = %q", code)
	}
	if url, _, _ := pbBytesField(item, 10); string(url) != "https://play-static.line-scdn.net/arts_item_dress_225100189/dp.png" {
		t.Fatalf("img_url = %q", url)
	}
	costs := squareRepeated(t, body, 2) // Boss Coins, then Cash for the header
	if len(costs) != 2 || squareUint(t, costs[0], 1) != 3 || squareUint(t, costs[0], 2) != 50 || squareUint(t, costs[1], 1) != 2 {
		t.Fatalf("costs = %x, want Boss Coins 50 then Cash", costs)
	}
	if squareUint(t, first, 5) != squareExchangeChest {
		t.Fatalf("gacha_type = %d, want the bronze chest", squareUint(t, first, 5))
	}
	group = squareNested(t, body, 1)
	if begin, end := squareUint(t, group, 3), squareUint(t, group, 4); begin == 0 || end <= begin {
		t.Fatalf("period = %d..%d, want epoch ms", begin, end)
	}
	if squareUint(t, body, 3) != 0 {
		t.Fatalf("result = %d", squareUint(t, body, 3))
	}

	// Inside the Halloween season the same request lists the event rows as well.
	exchangeAt(t, time.Date(2026, time.October, 25, 12, 0, 0, 0, exchangeJST))
	c.send(52, pbVar(nil, 1, 1))
	_, body = c.read()
	if n := len(squareRepeated(t, squareNested(t, body, 1), 5)); n != 10 {
		t.Fatalf("rows in the season = %d, want 10", n)
	}
}

// TestSquareExchangeBuyCharges: a buy of an affordable row takes its Boss Coins, grants the item and
// replies with the balance left and the reward.
func TestSquareExchangeBuyCharges(t *testing.T) {
	requireExchangeArchive(t)
	exchangeAccount(t, "9842", 50, nil)
	exchangeAt(t, time.Date(2026, time.October, 24, 12, 0, 0, 0, exchangeJST))
	c := newSqClient(t, &squareRoom{}, 2)
	c.join(9842)

	c.send(53, pbVar(nil, 1, 1001))
	id, body := c.read()
	if id != 69 {
		t.Fatalf("purchase msgid = %d, want 69", id)
	}
	if squareUint(t, body, 1) != 0 || squareUint(t, body, 2) != 1001 || squareUint(t, body, 3) != 3 || squareUint(t, body, 4) != 30 || squareUint(t, body, 5) != 20 {
		t.Fatalf("reply = %x, want result 0, gacha 1001, coin 3, price 30, balance 20", body)
	}
	reward := squareNested(t, body, 6)
	if code, _, _ := pbBytesField(reward, 3); string(code) != "CUON00059" || squareUint(t, reward, 4) != 1 {
		t.Fatalf("reward = %x", reward)
	}
	if got := economy.BossCoinBalance("9842"); got != 20 {
		t.Fatalf("Boss Coins = %d, want 20", got)
	}
	if !exchangeOwns(t, "9842", "CUON00059") {
		t.Fatal("the item was not granted")
	}
}

// TestSquareExchangeRefusals: a short balance, an unknown row, a row outside its season and an owned
// item each get the failure result (10130, the "Network Error." box) and change nothing.
func TestSquareExchangeRefusals(t *testing.T) {
	requireExchangeArchive(t)
	refused := pbVar(nil, 1, squareExchangeFail)
	exchangeAt(t, time.Date(2026, time.October, 24, 12, 0, 0, 0, exchangeJST))

	exchangeAccount(t, "9843", 20, nil)
	c := newSqClient(t, &squareRoom{}, 3)
	c.join(9843)
	c.send(53, pbVar(nil, 1, 1001)) // 30 needed, 20 held
	c.expect(69, refused)
	c.send(53, pbVar(nil, 1, 9999)) // no such row
	c.expect(69, refused)
	c.send(53, pbVar(nil, 1, 2001)) // a Halloween row before its season
	c.expect(69, refused)
	if got := economy.BossCoinBalance("9843"); got != 20 || exchangeOwns(t, "9843", "CUON00059") {
		t.Fatalf("refused buys changed the account: coins %d", got)
	}

	exchangeAccount(t, "9844", 50, []string{"CUTO0007B"})
	owned := newSqClient(t, &squareRoom{}, 4)
	owned.join(9844)
	owned.send(53, pbVar(nil, 1, 1003)) // already owned
	owned.expect(69, refused)
	if got := economy.BossCoinBalance("9844"); got != 50 {
		t.Fatalf("an owned item was charged: coins %d", got)
	}
}
