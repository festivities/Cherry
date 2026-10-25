package economy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// rideShopArchive points the item archive at a fixture with art for every default row.
func rideShopArchive(t *testing.T) {
	t.Helper()
	shopFixture(t, map[string][]string{
		"dress/225001616":    {"dp.png"}, // CUTO0018W
		"dress/225100605":    {"dp.png"}, // CUON000GT
		"interior/121400823": {"dp.png"}, // RUWA000MV
		"dress/225101333":    {"dp.png"}, // CUON00111
		"dress/225106511":    {"dp.png"}, // CUON0050V
	})
}

// rideShopAccount installs a non-lab account with Ride Points, Gems, Cash and an inventory, saving into
// a temp dir. It returns the dir (the ledger is written next to accounts.json).
func rideShopAccount(t *testing.T, aid string, points int64, inventory []string) string {
	t.Helper()
	acc := &store.Account{AccessToken: "rtok-" + aid, SessionKey: "sk", Aid: aid, Name: "Ride", Gems: 7, Cash: 3,
		InventoryCodes: inventory, Racing: &store.RacingRecord{Points: points}}
	testutil.InstallAccounts(t, map[string]*store.Account{"rtok-" + aid: acc})
	testutil.SetLab(t, aid, false)
	dir := t.TempDir()
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(dir, "accounts.json")
	store.AccountsMu.Unlock()
	return dir
}

// rideShopState is aid's Ride Points, Gems and Cash.
func rideShopState(t *testing.T, aid string) (points, gems, cash int64) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return ridePointsLocked(acc), acc.Gems, acc.Cash
}

// TestRideShopFileRefusals: a bad row or period is refused at startup, and the message names the entry.
func TestRideShopFileRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	rideShopArchive(t)
	def := data.RacingExchangeJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, def, `"price": 20`, `"price": 0`), "row 1 (id 1): price must be at least 1 Ride Point"},
		{mutateDefault(t, def, `"id": 2,`, `"id": 1,`), "row 2: id 1 is used twice"},
		{mutateDefault(t, def, `"id": 1, "item_code"`, `"id": 0, "item_code"`), "row 1: id must be at least 1"},
		{mutateDefault(t, def, `"item_code": "CUTO0018W"`, `"item_code": "CUZZ0018W"`), "item CUZZ0018W is not a sellable item code"},
		{mutateDefault(t, def, `"item_code": "CUTO0018W"`, `"item_code": "CUTO00099"`), "item CUTO00099 has no art"},
		{mutateDefault(t, def, `"item_code": "CUTO0018W"`, `"item_code": "PUPE00077"`), "item PUPE00077 is a pet"},
		{mutateDefault(t, def, `"item_code": "CUTO0018W"`, `"item_code": ""`), "item_code must not be empty"},
		{mutateDefault(t, def, `"rows": [`, `"begin": "2026-10-02T00:00:00+09:00", "end": "2026-10-01T00:00:00+09:00", "rows": [`), "end must be after begin"},
		{mutateDefault(t, def, `"rows": [`, `"bogus": 1, "rows": [`), "bogus"},
	}
	for _, c := range cases {
		refuseFile(t, rideShopFile.LoadFrom, "racing_exchange.json", c.text, c.want)
	}
	if err := rideShopFile.LoadFrom(""); err != nil {
		t.Fatalf("default: %v", err)
	}
}

// TestRideShopDefaultsHaveArt checks the default rows against the real item archive (skipped without it).
func TestRideShopDefaultsHaveArt(t *testing.T) {
	t.Cleanup(resetOperatorData)
	requireArchive(t)
	if err := rideShopFile.LoadFrom(""); err != nil {
		t.Fatalf("default rows: %v", err)
	}
}

// TestRideShopWindow: without dates the shop is open around now (one day back, 365 days on); the file's
// period closes it outside the dates, and then it lists nothing and sells nothing.
func TestRideShopWindow(t *testing.T) {
	t.Cleanup(resetOperatorData)
	rideShopArchive(t)
	inside := time.Date(2026, time.October, 26, 12, 0, 0, 0, jst)
	begin, end := RideShopWindowAt(inside)
	if !begin.Equal(inside.AddDate(0, 0, -1)) || !end.Equal(inside.AddDate(0, 0, 365)) {
		t.Fatalf("default window %s..%s", begin, end)
	}

	path := writeOperatorFile(t, "racing_exchange.json", mutateDefault(t, data.RacingExchangeJSON,
		`"rows": [`, `"begin": "2026-10-01T00:00:00+09:00", "end": "2026-10-31T23:59:59+09:00", "rows": [`))
	if err := rideShopFile.LoadFrom(path); err != nil {
		t.Fatal(err)
	}
	rideShopAccount(t, "9860", 100, nil)
	if rows, _ := RideShopFor("9860", inside); len(rows) != 5 {
		t.Fatalf("inside the period: %d rows, want 5", len(rows))
	}
	after := time.Date(2026, time.November, 1, 0, 0, 0, 0, jst)
	if rows, _ := RideShopFor("9860", after); len(rows) != 0 {
		t.Fatalf("after the period: %d rows, want none", len(rows))
	}
	if _, _, ok := RideShopBuy("9860", 1, after); ok {
		t.Fatal("a row was sold after the period")
	}
	if points, _, _ := rideShopState(t, "9860"); points != 100 {
		t.Fatalf("points after a refused buy = %d", points)
	}
}

// TestRideShopBuyChargesPoints: a buy takes only Ride Points, grants the item, writes a ledger line and
// leaves Gems and Cash alone; an owned row then lists as owned.
func TestRideShopBuyChargesPoints(t *testing.T) {
	t.Cleanup(resetOperatorData)
	rideShopArchive(t)
	if err := rideShopFile.LoadFrom(""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 26, 12, 0, 0, 0, jst)
	dir := rideShopAccount(t, "9851", 50, nil)

	row, points, ok := RideShopBuy("9851", 1, now)
	if !ok || row.ItemCode != "CUTO0018W" || row.Price != 20 || points != 30 {
		t.Fatalf("buy 1 = %+v points %d ok %v", row, points, ok)
	}
	if !exchangeOwns(t, "9851", "CUTO0018W") {
		t.Fatal("the clothing item was not granted")
	}
	if p, gems, cash := rideShopState(t, "9851"); p != 30 || gems != 7 || cash != 3 {
		t.Fatalf("after the buy: points %d gems %d cash %d, want 30 7 3", p, gems, cash)
	}
	if rows, pts := RideShopFor("9851", now); pts != 30 || len(rows) != 5 || !rows[0].Owned || rows[1].Owned {
		t.Fatalf("list after the buy: %d rows, points %d, owned flags %v %v", len(rows), pts, rows[0].Owned, rows[1].Owned)
	}

	// A room row grants a room item.
	if _, points, ok := RideShopBuy("9851", 3, now); !ok || points != 5 || !exchangeOwns(t, "9851", "RUWA000MV") {
		t.Fatalf("room row: points %d ok %v", points, ok)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"currency":"ridePoints"`) || strings.Count(string(raw), `"delta":-`) != 2 || !strings.Contains(string(raw), "racingexchange:1 CUTO0018W") {
		t.Fatalf("ledger = %s", raw)
	}
}

// TestRideShopRefusalsChangeNothing: a short balance, an unknown row, an owned item, an unknown account
// and a failed save are refused with the account left as it was.
func TestRideShopRefusalsChangeNothing(t *testing.T) {
	t.Cleanup(resetOperatorData)
	rideShopArchive(t)
	if err := rideShopFile.LoadFrom(""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 26, 12, 0, 0, 0, jst)

	rideShopAccount(t, "9852", 20, nil)
	if row, points, ok := RideShopBuy("9852", 2, now); ok || row.ID != 2 || points != 20 || exchangeOwns(t, "9852", "CUON000GT") {
		t.Fatalf("short points: row %+v points %d ok %v", row, points, ok)
	}
	if _, points, ok := RideShopBuy("9852", 99, now); ok || points != 20 {
		t.Fatalf("unknown row: points %d ok %v", points, ok)
	}
	if _, _, ok := RideShopBuy("nobody", 1, now); ok {
		t.Fatal("an unknown account bought a row")
	}

	rideShopAccount(t, "9853", 50, []string{"CUTO0018W"})
	if row, points, ok := RideShopBuy("9853", 1, now); ok || row.ID != 1 || points != 50 {
		t.Fatalf("owned item: row %+v points %d ok %v", row, points, ok)
	}
	if rows, _ := RideShopFor("9853", now); len(rows) != 5 || !rows[0].Owned {
		t.Fatal("an owned row is not marked owned on the list")
	}

	rideShopAccount(t, "9854", 50, nil)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json") // the parent is a file: the save fails
	store.AccountsMu.Unlock()
	if _, points, ok := RideShopBuy("9854", 1, now); ok || points != 50 || exchangeOwns(t, "9854", "CUTO0018W") {
		t.Fatalf("a failed save changed the account: points %d ok %v", points, ok)
	}
	if p, gems, cash := rideShopState(t, "9854"); p != 50 || gems != 7 || cash != 3 {
		t.Fatalf("failed save: points %d gems %d cash %d", p, gems, cash)
	}
}
