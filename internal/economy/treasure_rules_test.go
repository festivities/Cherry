package economy

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

// treasureGems sets aid's Gems for one test (the account must be installed).
func treasureGems(t *testing.T, aid string, gems int64) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	store.AccountByAidLocked(aid).Gems = gems
}

// treasureAccount returns aid's Gems and Mailbox rows.
func treasureAccount(t *testing.T, aid string) (int64, []store.MailRow) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	return acc.Gems, append([]store.MailRow(nil), acc.Mail...)
}

// treasureDayNumber is the game day of the test clock (the same rule as treasureDay, written out).
func treasureDayNumber(t time.Time) int64 { return (t.Unix() + 3*3600) / 86400 }

// TestTreasureItemCodes: the three season dress items convert to their item codes (internal/itemcode) and round-trip. With the
// store index each has dress art (thumbs.HasArt) and is grantable, and every item the data pays is one of them. This test
// fails when a code loses its art, so the reward table is reviewed then.
func TestTreasureItemCodes(t *testing.T) {
	want := map[int64]string{225408332: "CUAH006FG", 225304426: "CUSH003EY", 225410737: "CUAH008A9"}
	for id, code := range want {
		if got, ok := itemcode.Code(id); !ok || got != code {
			t.Fatalf("Code(%d) = %q %v, want %q", id, got, ok, code)
		}
		if n, ok := itemcode.ID(code); !ok || n != id {
			t.Fatalf("ID(%s) = %d %v, want %d", code, n, ok, id)
		}
		if !thumbs.HasArt("dress", id) {
			t.Fatalf("season item %s (%d) has no dress art in the store index", code, id)
		}
		if !itemAvailable(code) {
			t.Fatalf("season item %s has no art to grant", code)
		}
	}
	d := ActiveTreasureData()
	for _, p := range d.PointRewards {
		if p.Item != "" && !itemAvailable(p.Item) {
			t.Fatalf("point reward %d pays item %s, which is not available", p.ID, p.Item)
		}
	}
	if err := verifyTreasure(d); err != nil {
		t.Fatalf("the shipped buff items are refused: %v", err)
	}
	if d.Exchange.Multiple != 10 || len(d.PointRewards) != 4 || len(d.Quests.Pool) != 8 || len(d.Buff.Items) != 3 {
		t.Fatalf("defaults: exchange %+v, %d point rewards, %d quests, %d buff items", d.Exchange, len(d.PointRewards), len(d.Quests.Pool), len(d.Buff.Items))
	}
}

// TestTreasureMissionTypesCount: the type 3 (secret quest) and type 4 (Mystery Card) missions of the shipped pool count the
// board's own events: a Mystery Card use (cs 10) counts the goldkey kind, a secret quest claim (cs 25) the secret kind, and a
// roll counts neither.
func TestTreasureMissionTypesCount(t *testing.T) {
	d := ActiveTreasureData()
	secret, gold := int64(-1), int64(-1)
	for i, task := range d.Missions.Pool {
		if task.Kind == "secret" && task.Type == 3 && secret < 0 {
			secret = int64(i)
		}
		if task.Kind == "goldkey" && task.Type == 4 && gold < 0 {
			gold = int64(i)
		}
	}
	if secret < 0 || gold < 0 {
		t.Fatalf("the shipped pool has no type 3 secret or type 4 goldkey mission (%d, %d)", secret, gold)
	}
	slots := []store.TreasureSlot{{Task: gold}, {Task: secret}}
	treasureRing(t, treasureProbeRing)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 2, Tickets: 10, Charged: treasureNow.UnixMilli(),
		Missions: store.TreasureList{Set: 1, Slots: slots}})
	treasureDie(t, 1) // a roll: nothing for these two missions
	if _, err := TreasureRollDice("1001", treasureNow); err != nil {
		t.Fatal(err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Missions.Slots[0].Count != 0 || rec.Missions.Slots[1].Count != 0 {
		t.Fatalf("a roll counted a goldkey or secret mission: %+v", rec.Missions.Slots)
	}

	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 2, Tickets: 10, Charged: treasureNow.UnixMilli(),
		KeyPending: true, Missions: store.TreasureList{Set: 1, Slots: slots}})
	treasureRandSeq(t, 0, 2, 0) // a Mystery Card: type 1, 3 steps (the same card as TestTreasureGoldKeyCardLandsOnTravel)
	if _, err := TreasureGoldKeyUse("1001", treasureNow); err != nil {
		t.Fatal(err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Missions.Slots[0].Count != 1 || rec.Missions.Slots[1].Count != 0 {
		t.Fatalf("a Mystery Card use (cs 10) counted %+v, want goldkey 1 and secret 0", rec.Missions.Slots)
	}

	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 4, Tickets: 10, Charged: treasureNow.UnixMilli(),
		Secret: &store.TreasureSecret{Square: 4, Goal: 1, Count: 1, Enabled: true, Done: true}, Missions: store.TreasureList{Set: 1, Slots: slots}})
	paid, _, err := TreasureSecretClaim("1001", treasureNow)
	if err != nil || paid != d.Special.RewardTickets {
		t.Fatalf("secret quest claim = %d, %v", paid, err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Missions.Slots[0].Count != 0 || rec.Missions.Slots[1].Count != 1 {
		t.Fatalf("a secret quest claim (cs 25) counted %+v, want goldkey 0 and secret 1", rec.Missions.Slots)
	}
}

// TestTreasureExchangeRules: Jewels become Ranking Points at the rate on an exchange square, up to the square's daily limit;
// a refusal changes nothing; the limit starts again at 06:00 JST.
func TestTreasureExchangeRules(t *testing.T) {
	butlerAccount(t, "1001", 0, 0)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 2, Tickets: 10, Charged: treasureNow.UnixMilli(), Jewels: 70})
	res, err := TreasureExchange("1001", 5, treasureNow)
	if err != nil || res.Added != 50 || res.View.Jewels != 65 || res.View.Points != 50 || res.View.ExchangeLeft != 55 {
		t.Fatalf("exchange 5 = %+v, %v; want 50 points, 65 jewels, 55 left", res, err)
	}
	if _, err := TreasureExchange("1001", 56, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("exchange above the square's 55 left: %v", err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Jewels != 65 || rec.Points != 50 {
		t.Fatalf("refused exchange changed the board: %+v", rec)
	}
	if _, err := TreasureExchange("1001", 55, treasureNow); err != nil {
		t.Fatalf("exchange to the limit: %v", err)
	}
	if _, err := TreasureExchange("1001", 1, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("exchange past the daily limit: %v", err)
	}
	next := time.Date(2026, 11, 11, 6, 0, 0, 0, treasureJST)
	if v, err := TreasureOpen("1001", next); err != nil || v.ExchangeLeft != 60 {
		t.Fatalf("06:00 JST next day: %+v, %v; want the limit back at 60", v, err)
	}
	if treasureDay(time.Date(2026, 11, 11, 5, 59, 0, 0, treasureJST)) == treasureDay(next) {
		t.Fatal("the day did not turn at 06:00 JST")
	}
	if _, err := store.UpdateTreasure("1001", func(r *store.TreasureRecord) { r.Position = 1 }); err != nil {
		t.Fatal(err)
	}
	if _, err := TreasureExchange("1001", 1, next); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("exchange on a coin square: %v", err)
	}
}

// TestTreasureExchangeTiles: squares 2, 8, 15 convert at 1x, 2x, 3x (the popup shows that rate) and square 22 draws
// 1x to 5x per exchange (data/treasure.json tiles).
func TestTreasureExchangeTiles(t *testing.T) {
	butlerAccount(t, "1001", 0, 0)
	draw := TreasureRandIntn
	t.Cleanup(func() { TreasureRandIntn = draw })
	TreasureRandIntn = func(n int) int { return n - 1 } // the top of the gamble
	for _, c := range []struct {
		square     int
		rate, paid int64
	}{{2, 10, 20}, {8, 20, 40}, {15, 30, 60}, {22, 10, 100}} {
		treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: c.square, Tickets: 10, Charged: treasureNow.UnixMilli(), Jewels: 10})
		if v, err := TreasureOpen("1001", treasureNow); err != nil || v.ExchangeRate != c.rate {
			t.Fatalf("square %d rate = %+v, %v; want %d", c.square, v, err, c.rate)
		}
		if res, err := TreasureExchange("1001", 2, treasureNow); err != nil || res.Added != c.paid {
			t.Fatalf("square %d exchange 2 = %+v, %v; want %d points", c.square, res, err, c.paid)
		}
	}
}

// TestTreasureResetRules: a full square resets for earned Gems through the ledger (treasure:reset:*); CASH is refused, a
// short balance is refused, and a square that is not full has nothing to reset.
func TestTreasureResetRules(t *testing.T) {
	dir := butlerAccount(t, "1001", 0, 20)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Position: 2, Tickets: 10, Charged: treasureNow.UnixMilli(),
		ExDay: treasureDayNumber(treasureNow), Exchanged: map[string]int64{"2": 60}})
	info, err := TreasureResetInfo("1001", treasureNow)
	if err != nil || !info.Available || info.Price != 30 || info.Gems != 20 || info.CoinType != "GEM" {
		t.Fatalf("reset info = %+v, %v", info, err)
	}
	if _, err := TreasureResetBuy("1001", "CASH", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("reset paid in CASH: %v", err)
	}
	if _, err := TreasureResetBuy("1001", "GEM", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("reset with 20 Gems for 30: %v", err)
	}
	treasureGems(t, "1001", 50)
	res, err := TreasureResetBuy("1001", "GEM", treasureNow)
	if err != nil || res.Price != 30 || res.Gems != 20 || res.View.ExchangeLeft != 60 || res.View.ExchangeReset {
		t.Fatalf("reset = %+v, %v; want 30 Gems paid, 20 left, the square empty", res, err)
	}
	if gems, _ := treasureAccount(t, "1001"); gems != 20 {
		t.Fatalf("Gems = %d, want 20", gems)
	}
	want := fmt.Sprintf("treasure:reset:%d:%d:2", treasureSeasonID(), treasureDayNumber(treasureNow))
	if reasons := ledgerReasons(t, dir); len(reasons) != 1 || reasons[0] != want {
		t.Fatalf("ledger reasons = %v, want [%s]", reasons, want)
	}
	if _, err := TreasureResetBuy("1001", "GEM", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("reset of a square that is not full: %v", err)
	}
}

// TestTreasureCapRules: each cap raise costs its Gems through the ledger (treasure:cap:*), raises the cap the tickets refill
// to, and a raise the Gems do not cover, or past the last step, is refused.
func TestTreasureCapRules(t *testing.T) {
	dir := butlerAccount(t, "1001", 0, 100)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli()})
	offer, err := TreasureCapInfo("1001", treasureNow)
	if err != nil || offer.Cap != 10 || offer.Top != 25 || offer.Price != 40 || !offer.Available || offer.Gems != 100 {
		t.Fatalf("cap info = %+v, %v", offer, err)
	}
	offer, view, err := TreasureCapBuy("1001", treasureNow)
	if err != nil || offer.Cap != 15 || offer.Price != 40 || view.Max != 15 || offer.Gems != 60 {
		t.Fatalf("first raise = %+v %+v, %v", offer, view, err)
	}
	if _, _, err := TreasureCapBuy("1001", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("second raise for 80 with 60 Gems: %v", err)
	}
	if gems, _ := treasureAccount(t, "1001"); gems != 60 {
		t.Fatalf("Gems after the refused raise = %d, want 60", gems)
	}
	treasureGems(t, "1001", 100)
	offer, _, err = TreasureCapBuy("1001", treasureNow)
	if err != nil || offer.Cap != 20 || offer.Gems != 20 {
		t.Fatalf("second raise = %+v, %v", offer, err)
	}
	if _, _, err := TreasureCapBuy("1001", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("raise for 150 with 20 Gems: %v", err)
	}
	later, err := TreasureOpen("1001", treasureNow.Add(3000*time.Second))
	if err != nil || later.Max != 20 || later.Tickets != 15 {
		t.Fatalf("after 3000 s at the raised cap: %+v, %v; want 15 of 20 tickets", later, err)
	}
	want := []string{fmt.Sprintf("treasure:cap:%d:1", treasureSeasonID()), fmt.Sprintf("treasure:cap:%d:2", treasureSeasonID())}
	if reasons := ledgerReasons(t, dir); len(reasons) != 2 || reasons[0] != want[0] || reasons[1] != want[1] {
		t.Fatalf("ledger reasons = %v, want %v", reasons, want)
	}
}

// TestTreasureQuestRules: the first quest and mission sets are posted on the first list; the board's events count on them;
// a claim pays the tickets or the Jewels once; a refresh waits 24 h; a set whose tasks are all claimed is replaced.
func TestTreasureQuestRules(t *testing.T) {
	butlerAccount(t, "1001", 0, 0)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 40, Charged: treasureNow.UnixMilli()})
	lists, err := TreasureTaskList("1001", treasureNow)
	if err != nil || len(lists.Quests) != 5 || len(lists.Missions) != 4 || lists.FreeRefresh {
		t.Fatalf("first list = %+v, %v", lists, err)
	}
	if q := lists.Quests[0]; q.Seq != 1 || q.Goal != 3 || q.Reward != 2 || q.Text != "Roll the dice 3 times" || q.Item != "CUAH006FG" {
		t.Fatalf("quest 1 = %+v", q)
	}
	if m := lists.Missions[0]; m.Seq != 101 || m.Reward != 3 || m.Goal != 10 {
		t.Fatalf("mission 1 = %+v", m)
	}
	treasureDie(t, 1)
	for i := 0; i < 3; i++ {
		if _, err := TreasureRollDice("1001", treasureNow); err != nil {
			t.Fatal(err)
		}
	}
	lists, _ = TreasureTaskList("1001", treasureNow)
	if lists.Quests[0].State != 1 || lists.Quests[0].Count != 3 || lists.Quests[2].State != 1 || lists.Quests[3].State != 0 {
		t.Fatalf("after three rolls: %+v", lists.Quests)
	}
	if lists.Missions[0].Count != 3 || lists.Missions[0].State != 0 {
		t.Fatalf("roll mission after three rolls: %+v", lists.Missions[0])
	}
	claim, err := TreasureQuestClaim("1001", 1, treasureNow)
	if err != nil || claim.Paid != 2 || claim.View.Tickets != 39 {
		t.Fatalf("claim quest 1 = %+v, %v; want 2 tickets, 39 in all", claim, err)
	}
	if _, err := TreasureQuestClaim("1001", 1, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("second claim of quest 1: %v", err)
	}
	if _, err := TreasureQuestClaim("1001", 2, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("claim of an unfinished quest: %v", err)
	}
	if _, err := TreasureQuestRefresh("1001", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("refresh before 24 h: %v", err)
	}
	if _, err := TreasureQuestRefresh("1001", treasureNow.Add(23*time.Hour)); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("refresh at 23 h: %v", err)
	}
	// Set 2 rotates to task 5 (the pool index), an exchange of 5 Jewels, and is not free to refresh again for 24 h.
	fresh, err := TreasureQuestRefresh("1001", treasureNow.Add(24*time.Hour))
	if err != nil || fresh.Quests[0].Text != "Exchange 5 Jewels for Ranking Points" || fresh.FreeRefresh {
		t.Fatalf("refresh at 24 h = %+v, %v", fresh, err)
	}
	for i := 0; i < 7; i++ { // ten rolls in all: the roll mission completes
		if _, err := TreasureRollDice("1001", treasureNow.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	m, err := TreasureMissionClaim("1001", 101, treasureNow.Add(24*time.Hour))
	if err != nil || m.Paid != 3 || m.Row.State != 2 {
		t.Fatalf("claim mission 101 = %+v, %v; want 3 Jewels", m, err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Jewels < 3 {
		t.Fatalf("mission Jewels not on the board: %+v", rec)
	}
	if _, err := TreasureMissionClaim("1001", 205, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("claim of a mission not in the set: %v", err)
	}
	// Every quest of the posted set claimed: the next list posts a new set.
	if _, err := store.UpdateTreasure("1001", func(r *store.TreasureRecord) {
		for i := range r.Quests.Slots {
			r.Quests.Slots[i].State = 2
		}
	}); err != nil {
		t.Fatal(err)
	}
	if lists, _ := TreasureTaskList("1001", treasureNow); len(lists.Quests) != 5 || lists.Quests[0].Seq != 1 {
		t.Fatalf("list after a finished set: %+v", lists.Quests)
	}
	if rec, _ := store.TreasureRecordOf("1001"); rec.Quests.Set != 3 {
		t.Fatalf("quest set after a finished one = %d, want 3", rec.Quests.Set)
	}
}

// TestTreasureRankingRules: the season's Ranking Points rank the boards (equal points by aid), the grades follow the rank,
// the names come from the accounts, and another season's board is left out.
func TestTreasureRankingRules(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"t-1001": {AccessToken: "t-1001", SessionKey: "sk", Aid: "1001", Name: "Cherry"},
		"t-1002": {AccessToken: "t-1002", SessionKey: "sk", Aid: "1002", Name: "Test"},
		"t-1003": {AccessToken: "t-1003", SessionKey: "sk", Aid: "1003", Name: "Third"},
		"t-1004": {AccessToken: "t-1004", SessionKey: "sk", Aid: "1004", Name: "Old"},
	})
	store.ResetTreasure()
	t.Cleanup(store.ResetTreasure)
	testutil.UseDatabase(t)
	season := treasureSeasonID()
	for aid, pts := range map[string]int64{"1001": 30, "1002": 50, "1003": 50} {
		if _, err := store.UpdateTreasure(aid, func(r *store.TreasureRecord) { r.Season, r.Points = season, pts }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpdateTreasure("1004", func(r *store.TreasureRecord) { r.Season, r.Points = 1, 999 }); err != nil {
		t.Fatal(err)
	}
	rows, pages := TreasureRankingPage(1, 20, treasureNow)
	if pages != 1 || len(rows) != 3 {
		t.Fatalf("page = %d rows, %d pages; want 3 rows, 1 page: %+v", len(rows), pages, rows)
	}
	if rows[0].Aid != "1002" || rows[0].Rank != 1 || rows[0].Name != "Test" || rows[0].Grade.Name != "Gold" || rows[0].Grade.Type != 4 {
		t.Fatalf("rank 1 = %+v", rows[0])
	}
	if rows[1].Aid != "1003" || rows[1].Rank != 2 || rows[1].Grade.Name != "Silver" || rows[2].Aid != "1001" || rows[2].Rank != 3 {
		t.Fatalf("ranks 2 and 3 = %+v %+v", rows[1], rows[2])
	}
	if one, _ := TreasureRankingPage(2, 2, treasureNow); len(one) != 1 || one[0].Aid != "1003" {
		t.Fatalf("page 2..2 = %+v", one)
	}
	if me := TreasureMyRankOf("1001", treasureNow); me.Rank != 3 || me.Points != 30 || me.Grade.Name != "Silver" {
		t.Fatalf("my rank = %+v", me)
	}
	if old := TreasureMyRankOf("1004", treasureNow); old.Rank != 0 || old.Points != 0 {
		t.Fatalf("another season's board ranks: %+v", old)
	}
}

// TestTreasurePointRewardRules: a point reward goes to the Mailbox once the points are there; the Gems row and the item row
// are both mail; a full Mailbox refuses it without changing the points, the claims or the Mailbox; a Mailbox Gem pays the
// ledger ("mail receive" reason) when received.
func TestTreasurePointRewardRules(t *testing.T) {
	butlerAccount(t, "1001", 0, 0)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli(), Points: 100})
	if rows := TreasurePointRows("1001", treasureNow); len(rows) != 4 || rows[0].Claimed || rows[3].Points != 1000 {
		t.Fatalf("point rows = %+v", rows)
	}
	if _, _, err := TreasurePointClaim("1001", 2, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("claim of 300 with 100 points: %v", err)
	}
	row, _, err := TreasurePointClaim("1001", 1, treasureNow)
	if err != nil || !row.Claimed {
		t.Fatalf("claim 1 = %+v, %v", row, err)
	}
	if _, _, err := TreasurePointClaim("1001", 1, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("second claim of reward 1: %v", err)
	}
	if _, mail := treasureAccount(t, "1001"); len(mail) != 1 || mail[0].Type != store.MailTypeGems || mail[0].Amount != 50 {
		t.Fatalf("mail after reward 1 = %+v", mail)
	}
	if _, err := store.UpdateTreasure("1001", func(r *store.TreasureRecord) { r.Points = 300 }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := TreasurePointClaim("1001", 2, treasureNow); err != nil {
		t.Fatalf("claim 2: %v", err)
	}
	_, mail := treasureAccount(t, "1001")
	if len(mail) != 3 || mail[1].Amount != 100 || mail[2].Type != store.MailTypeItem || mail[2].Item != "CUAH006FG" {
		t.Fatalf("mail after reward 2 = %+v", mail)
	}
	// The Mailbox is full: reward 3 (150 Gems and an item, two rows) is refused and nothing changes.
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("1001")
	for len(acc.Mail) < MailMaxRows()-1 {
		acc.Mail = append(acc.Mail, store.MailRow{Type: store.MailTypeGems, Amount: 1, Expires: treasureNow.Add(time.Hour).UnixMilli()})
	}
	store.AccountsMu.Unlock()
	if _, err := store.UpdateTreasure("1001", func(r *store.TreasureRecord) { r.Points = 600 }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := TreasurePointClaim("1001", 3, treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("claim with one Mailbox slot left: %v", err)
	}
	if rec, _ := store.TreasureRecordOf("1001"); len(rec.PointClaimed) != 2 {
		t.Fatalf("a refused claim was recorded: %+v", rec.PointClaimed)
	}
	// A received Gem row pays the ledger.
	store.AccountsMu.Lock()
	acc = store.AccountByAidLocked("1001")
	acc.Mail = acc.Mail[:0]
	lines, err := receiveLocked(acc, store.MailRow{Seq: 9, Type: store.MailTypeGems, Amount: 50})
	store.AccountsMu.Unlock()
	if err != nil || len(lines) != 1 || lines[0].Reason != "mail receive 9" || lines[0].Delta != 50 {
		t.Fatalf("receive = %+v, %v", lines, err)
	}
}

// TestTreasureRankRewardRules: the rank reward waits for the season's end, pays the tier's Gems once inside the claim window,
// and a board with no rank gets nothing.
func TestTreasureRankRewardRules(t *testing.T) {
	butlerAccount(t, "1001", 0, 0)
	treasureState(t, "1001", store.TreasureRecord{Season: treasureSeasonID(), Tickets: 10, Charged: treasureNow.UnixMilli(), Points: 40})
	end := TreasureCurrent(treasureNow).End
	if _, err := TreasureRankClaim("1001", treasureNow); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("rank claim inside the season: %v", err)
	}
	if offer := TreasureRankInfo("1001", treasureNow); offer.Ended || offer.Claimable {
		t.Fatalf("rank offer inside the season: %+v", offer)
	}
	after := end.Add(time.Hour)
	offer := TreasureRankInfo("1001", after)
	if !offer.Ended || !offer.Open || !offer.Claimable || offer.Rank != 1 || offer.Gems != 300 {
		t.Fatalf("rank offer after the season = %+v", offer)
	}
	gems, err := TreasureRankClaim("1001", after)
	if err != nil || gems != 300 {
		t.Fatalf("rank claim = %d, %v; want 300", gems, err)
	}
	if _, err := TreasureRankClaim("1001", after); !errors.Is(err, ErrTreasureRefused) {
		t.Fatalf("second rank claim: %v", err)
	}
	if _, mail := treasureAccount(t, "1001"); len(mail) != 1 || mail[0].Amount != 300 {
		t.Fatalf("mail after the rank reward = %+v", mail)
	}
	if late := TreasureRankInfo("1001", end.AddDate(0, 0, 8)); late.Open || late.Claimable {
		t.Fatalf("rank offer after the claim window: %+v", late)
	}
}

// TestTreasureArtDrawn: the three Cherry-drawn pictures decode at their plist sizes, have a transparent corner and a white
// symbol, and are cached once under the tw1_ prefix.
func TestTreasureArtDrawn(t *testing.T) {
	dir := t.TempDir()
	prev := thumbs.DpCacheDir
	thumbs.DpCacheDir = dir
	t.Cleanup(func() { thumbs.DpCacheDir = prev })
	sizes := map[string][2]int{"quest_button.png": {180, 67}, "help_button.png": {62, 67}, "rank_board.png": {250, 67}}
	for name, size := range sizes {
		b, ok := treasureDrawnPicture(name)
		if !ok {
			t.Fatalf("%s not drawn", name)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != size[0] || img.Bounds().Dy() != size[1] {
			t.Fatalf("%s: %v, bounds %v", name, err, img.Bounds())
		}
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Fatalf("%s: corner is not transparent", name)
		}
		white := 0
		for y := 0; y < size[1]; y++ {
			for x := 0; x < size[0]; x++ {
				r, g, bl, a := img.At(x, y).RGBA()
				if a > 0x8000 && r > 0xF000 && g > 0xE800 && bl > 0xE000 {
					white++
				}
			}
		}
		if white < 20 {
			t.Fatalf("%s: %d white pixels, want a visible symbol", name, white)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 || !strings.HasPrefix(entries[0].Name(), "tw1_") {
		t.Fatalf("cache entries = %v, want three tw1_ files", entries)
	}
	if _, ok := treasureDrawnPicture("nope.png"); ok {
		t.Fatal("an unknown name was drawn")
	}
	if TreasureSeasonPicture(22) != "cherry/treasure/quest_button.png" || TreasureSeasonPicture(1) != "" {
		t.Fatalf("picture paths: 22 = %q, 1 = %q", TreasureSeasonPicture(22), TreasureSeasonPicture(1))
	}
}

// TestTreasureRulesRefusals: each rule the file checks stops the data at startup.
func TestTreasureRulesRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(*TreasureData)
	}{
		{"exchange multiple zero", func(d *TreasureData) { d.Exchange.Multiple = 0 }},
		{"exchange daily limit zero", func(d *TreasureData) { d.Exchange.DailyMax = 0 }},
		{"reset price zero", func(d *TreasureData) { d.Exchange.ResetGems = 0 }},
		{"quest set above five", func(d *TreasureData) { d.Quests.SetSize = 6 }},
		{"quest refresh zero", func(d *TreasureData) { d.Quests.RefreshHours = 0 }},
		{"quest kind unknown", func(d *TreasureData) { d.Quests.Pool[0].Kind = "garden" }},
		{"quest picture 8", func(d *TreasureData) { d.Quests.Pool[0].Type = 8 }},
		{"quest icon not a code", func(d *TreasureData) { d.Quests.Pool[0].Item = "NOPE" }},
		{"quest without text", func(d *TreasureData) { d.Quests.Pool[0].Text = "" }},
		{"mission reward zero", func(d *TreasureData) { d.Missions.Pool[0].Reward = 0 }},
		{"mission picture 6", func(d *TreasureData) { d.Missions.Pool[0].Type = 6 }},
		{"mission kind unknown", func(d *TreasureData) { d.Missions.Pool[8].Kind = "trap" }},
		{"season end message blank", func(d *TreasureData) { d.Season.EndMessage = "  " }},
		{"set larger than the pool", func(d *TreasureData) { d.Missions.SetSize = 6 }},
		{"last grade not the rest", func(d *TreasureData) { d.Ranking.Grades[3].MaxRank = 10 }},
		{"grades out of order", func(d *TreasureData) { d.Ranking.Grades[1].MaxRank = 1 }},
		{"point reward pays nothing", func(d *TreasureData) { d.PointRewards[0].Gems = 0 }},
		{"point reward item not a code", func(d *TreasureData) { d.PointRewards[1].Item = "XX" }},
		{"point thresholds not rising", func(d *TreasureData) { d.PointRewards[2].Points = 200 }},
		{"rank claim window zero", func(d *TreasureData) { d.RankRewards.ClaimDays = 0 }},
		{"rank tiers not rising", func(d *TreasureData) { d.RankRewards.Tiers[1].Cutline = 1 }},
		{"cap step not above the last", func(d *TreasureData) { d.TicketCap.Steps[1].Max = 15 }},
		{"cap step above 99", func(d *TreasureData) { d.TicketCap.Steps[2].Max = 100 }},
		{"ring with a bank square", func(d *TreasureData) { d.Board.Ring[1] = 10 }},
		{"trap high die zero", func(d *TreasureData) { d.Trap.HighDice = 0 }},
		{"trap high die above a die", func(d *TreasureData) { d.Trap.HighDice = 7 }},
		{"trap max tries zero", func(d *TreasureData) { d.Trap.MaxTries = 0 }},
		{"trap max tries above 20", func(d *TreasureData) { d.Trap.MaxTries = 21 }},
		{"broom timeout zero", func(d *TreasureData) { d.Travel.StaleMinutes = 0 }},
		{"broom timeout above a day", func(d *TreasureData) { d.Travel.StaleMinutes = 1441 }},
		{"escape mission picture 6", func(d *TreasureData) { d.Missions.Pool[9].Type = 6 }},
		{"board art Cherry does not draw", func(d *TreasureData) { d.Art.Board = "marble_main_board.png" }},
		{"background art Cherry does not draw", func(d *TreasureData) { d.Art.Background = "board_cal_v1.png" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := opdata.Decode[TreasureData](data.TreasureJSON, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkTreasureData(d); err != nil {
				t.Fatalf("the shipped data is refused: %v", err)
			}
			c.edit(d)
			if err := checkTreasureData(d); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
