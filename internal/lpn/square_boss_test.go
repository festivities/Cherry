package lpn

import (
	"testing"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// bossStart sends cr45 for a try that is left and reads the try reply and the rc6 boss-room enter.
func bossStart(t *testing.T, c *sqClient) {
	t.Helper()
	c.send(54, nil) // the BOSS! GO banner: an empty cr54
	if id, try := c.read(); id != 72 || squareUint(t, try, 1) != 0 {
		t.Fatalf("try = %d/%x, want a silent rc72", id, try)
	}
	if id, enter := c.read(); id != 6 || squareUint(t, enter, 4) != squareBossThemeID {
		t.Fatalf("boss room enter = %d/%x, want rc6 theme %d", id, enter, squareBossThemeID)
	}
}

// bossBattle starts a try, asks for the lobby and the master start, and returns the rc71 battle body.
func bossBattle(t *testing.T, c *sqClient, aid uint64) []byte {
	t.Helper()
	bossStart(t, c)
	c.send(46, nil)
	if id, lobby := c.read(); id != 59 || squareUint(t, lobby, 1) != squareBossRoomState {
		t.Fatalf("lobby = %d/%x", id, lobby)
	}
	c.send(47, pbVar(nil, 1, squareBossStateStart))
	id, battle := c.read()
	if id != 71 {
		t.Fatalf("battle start = %d/%x, want 71", id, battle)
	}
	return battle
}

// bossTouch sends one cr50 with (hit, note) rows and returns the rc64 reply's TouchResult, HP and damage.
func bossTouch(t *testing.T, c *sqClient, aid uint64, rows ...[3]uint64) (accepted, hp, damage uint64) {
	t.Helper()
	var body []byte
	for _, r := range rows { // {TouchResult, NoteIndex, TouchCount}
		body = pbLen(body, 2, pbVar(pbVar(pbVar(nil, 1, r[0]), 3, r[1]), 4, r[2]))
	}
	c.send(50, body)
	id, reply := c.read()
	if id != 64 {
		t.Fatalf("touch reply = %d/%x, want 64", id, reply)
	}
	play := squareNested(t, reply, 2)
	user := squareNested(t, reply, 3)
	return squareUint(t, reply, 1), squareUint(t, play, 1), squareUint(t, user, 2)
}

// bossRecord returns the account's Boss Coins and boss catch counts.
func bossRecord(t *testing.T, aid string) (int64, map[int64]int64) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return acc.BossCoins, acc.BossCaught
}

func TestSquareBossTryEntersBossWorld(t *testing.T) {
	c := fishingRoomAccount(t, "9821", 0)
	boss := economy.ActiveFishBoss()
	c.send(45, pbVar(nil, 1, 2))
	id, try := c.read()
	if id != 57 || squareUint(t, try, 1) != squareFishOK || squareUint(t, try, 2) != 1 ||
		squareUint(t, try, 3) != uint64(boss.MaxTries) || squareUint(t, try, 4) != uint64(boss.MaxTries-1) ||
		squareUint(t, try, 6) != uint64(boss.Reward.CoinsCatch) {
		t.Fatalf("try = %d/%x", id, try)
	}
	if _, found, _ := pbBytesField(try, 7); found { // AssetBossCoin and billing fields are never served
		t.Fatalf("try reply carries field 7: %x", try)
	}
	id, enter := c.read()
	if id != 6 || squareUint(t, enter, 1) != squareEnterResult || squareUint(t, enter, 4) != squareBossThemeID {
		t.Fatalf("boss room enter = %d/%x", id, enter)
	}
	if name, _, _ := pbBytesField(enter, 6); string(name) != squareBossMapFileName {
		t.Fatalf("boss room map = %q", name)
	}
	if _, found, _ := pbBytesField(enter, 2); found { // no fishing spot NpcList in the boss room
		t.Fatalf("boss room enter carries an NpcList")
	}
	c.none() // no relaystart is needed, and nothing follows the enter
}

func TestSquareBossRefusesWithoutTries(t *testing.T) {
	c := fishingRoomAccount(t, "9822", 0)
	for range economy.ActiveFishBoss().MaxTries {
		bossStart(t, c)
	}
	c.send(45, pbVar(nil, 1, 2))
	id, try := c.read()
	if id != 57 || squareUint(t, try, 1) != squareFishOK || squareUint(t, try, 2) != 0 || squareUint(t, try, 4) != 0 {
		t.Fatalf("refused try = %d/%x, want 57 IsStart false, none left", id, try)
	}
	c.none() // no rc6 for a refused try
}

func TestSquareBossTouchesCountDistinctNotes(t *testing.T) {
	const aidText = "9823"
	aid := uint64(9823)
	c := fishingRoomAccount(t, aidText, 0)
	bossBattle(t, c, aid)
	boss := economy.ActiveFishBoss()
	dmg := uint64(boss.DamagePerHit)
	maxHp := uint64(boss.Boss.MaxHp)

	if n, hp, dealt := bossTouch(t, c, aid, [3]uint64{1, 0, 1}); n != 1 || hp != maxHp-dmg || dealt != dmg {
		t.Fatalf("first hit = accepted %d hp %d damage %d", n, hp, dealt)
	}
	// A replayed count, a miss, a skipped count and a note past the chart all add nothing.
	if n, hp, dealt := bossTouch(t, c, aid, [3]uint64{1, 0, 1}, [3]uint64{0, 2, 0}, [3]uint64{1, 3, 2}, [3]uint64{1, uint64(len(boss.PlayNote)), 1}); n != 0 || hp != maxHp-dmg || dealt != dmg {
		t.Fatalf("replayed, miss, skipped and out-of-chart rows = accepted %d hp %d damage %d", n, hp, dealt)
	}
	// The note's next taps count up to its touch_max; one more adds nothing.
	limit := uint64(boss.TouchMax(boss.PlayNote[0]))
	var rows [][3]uint64
	for n := uint64(2); n <= limit+1; n++ {
		rows = append(rows, [3]uint64{1, 0, n})
	}
	if n, _, dealt := bossTouch(t, c, aid, rows...); n != 1 || dealt != limit*dmg {
		t.Fatalf("taps on one note = accepted %d damage %d, want 1 and %d", n, dealt, limit*dmg)
	}
}

func TestSquareBossCatchPaysOnce(t *testing.T) {
	const aidText = "9824"
	aid := uint64(9824)
	c := fishingRoomAccount(t, aidText, 0)
	base := fishingBalance(t, aidText)
	bossBattle(t, c, aid)
	boss := economy.ActiveFishBoss()
	need := (boss.Boss.MaxHp + boss.DamagePerHit - 1) / boss.DamagePerHit
	if need > int64(len(boss.PlayNote)) {
		t.Fatalf("chart of %d notes cannot catch the boss in %d hits", len(boss.PlayNote), need)
	}
	var rows [][3]uint64
	for i := range need {
		rows = append(rows, [3]uint64{1, uint64(i), 1})
	}
	if _, hp, dealt := bossTouch(t, c, aid, rows...); hp != 0 || dealt != uint64(boss.Boss.MaxHp) {
		t.Fatalf("full hits = hp %d damage %d", hp, dealt)
	}

	c.send(58, nil)
	id, end := c.read()
	gems := boss.Reward.GemsParticipation + boss.Reward.GemsCatch
	coins := boss.Reward.CoinsParticipation + boss.Reward.CoinsCatch
	if id != 65 || squareUint(t, end, 1) != 1 {
		t.Fatalf("finish = %d/%x, want 65 caught", id, end)
	}
	if user := squareNested(t, end, 2); squareUint(t, user, 1) != aid || squareUint(t, user, 3) != uint64(gems) {
		t.Fatalf("user reward = %x, want %d Gems", user, gems)
	}
	if reward := squareNested(t, end, 3); squareUint(t, reward, 1) != uint64(coins) || squareUint(t, reward, 3) != uint64(coins) {
		t.Fatalf("boss coins = %x, want added and balance %d", reward, coins)
	}
	if got := fishingBalance(t, aidText); got != base+gems {
		t.Fatalf("Gems after the catch = %d, want %d", got, base+gems)
	}
	if got, caught := bossRecord(t, aidText); got != coins || caught[boss.Boss.ID] != 1 {
		t.Fatalf("record after the catch = coins %d caught %v", got, caught)
	}

	// A second finish of the same battle pays nothing and answers nothing.
	c.send(58, nil)
	c.none()
	if got := fishingBalance(t, aidText); got != base+gems {
		t.Fatalf("Gems after a repeated finish = %d, want %d", got, base+gems)
	}
}

func TestSquareBossResetRefusedAndCharged(t *testing.T) {
	const aidText = "9825"
	c := fishingRoomAccount(t, aidText, 100)
	base := fishingBalance(t, aidText)
	boss := economy.ActiveFishBoss()
	boss.ResetGems = 50 // the default offers no reset
	t.Cleanup(func() { boss.ResetGems = 0 })

	// Tries left: a paid reset is refused (Result 1 / Remain 0 asks again), nothing is charged.
	c.send(54, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 72 || squareUint(t, body, 1) != 1 || squareUint(t, body, 3) != 0 {
		t.Fatalf("reset with tries left = %d/%x, want refused", id, body)
	}
	c.none()

	for range boss.MaxTries {
		bossStart(t, c)
	}
	// Out of tries: the banner gets the recharge prompt (Result 1, Remain 0, the Gem cost), no rc6.
	c.send(54, nil)
	id, body := c.read()
	cost := squareNested(t, body, 4)
	if id != 72 || squareUint(t, body, 1) != 1 || squareUint(t, body, 3) != 0 ||
		squareUint(t, cost, 1) != squareCoinGems || squareUint(t, cost, 2) != uint64(boss.ResetGems) {
		t.Fatalf("banner out of tries = %d/%x, want the recharge prompt", id, body)
	}
	c.none()
	// The recharge popup's OK: the tries refill for the Gems.
	c.send(54, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 72 || squareUint(t, body, 1) != 0 || squareUint(t, body, 3) != uint64(boss.MaxTries) {
		t.Fatalf("reset = %d/%x, want refilled", id, body)
	}
	c.none()
	if got := fishingBalance(t, aidText); got != base-boss.ResetGems {
		t.Fatalf("Gems after reset = %d, want %d", got, base-boss.ResetGems)
	}
	bossStart(t, c) // the refilled try starts

	// Not enough Gems for the reset: asked again, balance unchanged.
	poor := fishingRoomAccount(t, "9826", 0)
	pbase := fishingBalance(t, "9826")
	if pbase >= boss.ResetGems {
		t.Fatalf("test account has %d Gems, the reset costs %d", pbase, boss.ResetGems)
	}
	for range boss.MaxTries {
		bossStart(t, poor)
	}
	poor.send(54, pbVar(nil, 1, 1))
	if id, body := poor.read(); id != 72 || squareUint(t, body, 1) != 1 || squareUint(t, body, 3) != 0 {
		t.Fatalf("reset without Gems = %d/%x, want refused", id, body)
	}
	if got := fishingBalance(t, "9826"); got != pbase {
		t.Fatalf("Gems after refused reset = %d, want %d", got, pbase)
	}
}

// TestSquareBossBannerWithoutResetSaysRoomsFull: with no reset offered, an out-of-tries banner tap
// gets Result 1 / Remain 1 (box 1834) and no boss world.
func TestSquareBossBannerWithoutResetSaysRoomsFull(t *testing.T) {
	c := fishingRoomAccount(t, "9827", 0)
	for range economy.ActiveFishBoss().MaxTries {
		bossStart(t, c)
	}
	c.send(54, nil)
	if id, body := c.read(); id != 72 || squareUint(t, body, 1) != 1 || squareUint(t, body, 3) != 1 {
		t.Fatalf("banner without reset = %d/%x, want Result 1 Remain 1", id, body)
	}
	c.none()
}

func TestSquareBossLeavingClearsTry(t *testing.T) {
	c := fishingRoomAccount(t, "9827", 0)
	bossStart(t, c)
	c.send(2, pbVar(nil, 1, 1)) // Back to the Square: rc6 theme 6
	if id, enter := c.read(); id != 6 || squareUint(t, enter, 4) != squareThemeID {
		t.Fatalf("leave enter = %d/%x, want theme %d", id, enter, squareThemeID)
	}
	c.send(47, pbVar(nil, 1, squareBossStateStart)) // the battle of the left room cannot start
	c.none()
	c.send(46, nil)
	c.none()
}
