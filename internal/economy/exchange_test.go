package economy

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/store"
	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

// exchangeDefaultArt are the item ids of the default exchange rows (folder names in the item archive).
// exchangeArchive makes each one a fixture item with art, so the verify runs without the archive.
var exchangeDefaultArt = map[string]int64{
	"CUON00059": 225100189, "CUON000DL": 225100489, "CUTO0007B": 225000263, "CUPA0004W": 225200176,
	"RUTA0004S": 120300172, "RUTA00068": 120300224,
	"CUAE00A66": 225513182, "CUAE00AMI": 225513770, "CUAH0075G": 225409268, "CUAE00A8A": 225513258,
}

// exchangeArchive points the item archive at a fixture with art for every default row.
func exchangeArchive(t *testing.T) {
	t.Helper()
	files := map[string][]string{}
	for code, id := range exchangeDefaultArt {
		if got, ok := itemCodeFromID(id); !ok || got != code {
			t.Fatalf("id %d is %q, the table says %s", id, got, code)
		}
		kind := "dress"
		if code[:2] == "RU" {
			kind = "interior"
		}
		files[kind+"/"+strconv.FormatInt(id, 10)] = []string{"dp.png"}
	}
	shopFixture(t, files)
}

// exchangeAccount installs a non-lab account with Boss Coins and an inventory, saving into a temp dir.
func exchangeAccount(t *testing.T, aid string, coins int64, inventory []string) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"etok-" + aid: {AccessToken: "etok-" + aid, SessionKey: "sk", Aid: aid, Name: "Exch", BossCoins: coins, InventoryCodes: inventory}})
	testutil.SetLab(t, aid, false)
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Unlock()
}

// exchangeOwns reports whether aid's account holds code in its clothing or room items.
func exchangeOwns(t *testing.T, aid, code string) bool {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return slices.Contains(store.AccountOwnedCodes(acc), code) || slices.ContainsFunc(acc.RoomItems, func(it store.RoomItem) bool { return it.Cd == code })
}

// TestExchangeFileRefusals: a row that cannot be sold, or a missing standing group, is refused at
// startup and the message names the entry.
func TestExchangeFileRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	exchangeArchive(t)
	def := data.ExchangeJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, def, `"id": 1003, "item_code": "CUTO0007B", "price": 20`, `"id": 1003, "item_code": "CUTO0007B", "price": 0`), "row 3 (id 1003): price must be at least 1"},
		{mutateDefault(t, def, `"id": 1003, "item_code": "CUTO0007B"`, `"id": 1002, "item_code": "CUTO0007B"`), "id 1002 is used twice"},
		{mutateDefault(t, def, `"item_code": "CUTO0007B"`, `"item_code": "CUZZ0007B"`), "item CUZZ0007B is not a sellable item code"},
		{mutateDefault(t, def, `"item_code": "CUTO0007B"`, `"item_code": "CUTO00099"`), "item CUTO00099 has no art in the item archive"},
		{mutateDefault(t, def, `"season": "halloween"`, `"season": "nosuch"`), `season "nosuch" is not a season in calendars.json`},
		{mutateDefault(t, def, `"season": "halloween"`, `"season": ""`), "season must name a season"},
		{`{"title":"T","title_desc":"","rows":[],"events":[]}`, "rows must not be empty"},
		{mutateDefault(t, def, `"title": "Boss Coin Exchange"`, `"title": ""`), "title must not be empty"},
	}
	for _, c := range cases {
		refuseFile(t, exchangeFile.LoadFrom, "exchange.json", c.text, c.want)
	}
	if err := exchangeFile.LoadFrom(""); err != nil { // the embedded default still passes
		t.Fatalf("default: %v", err)
	}
}

// TestExchangeDefaultsHaveArt checks the default rows against the real item archive (skipped without it).
func TestExchangeDefaultsHaveArt(t *testing.T) {
	t.Cleanup(resetOperatorData)
	if _, err := os.Stat(filepath.Join(thumbs.DpItemRoot, "dress")); err != nil {
		t.Skip("item archive not present")
	}
	if err := exchangeFile.LoadFrom(""); err != nil {
		t.Fatalf("default rows: %v", err)
	}
}

// TestExchangeRowsFollowSeason: the standing rows are always on sale, the event rows only while the
// Halloween season is in force (calendars.json, 10-25 to 11-07 by game day, 06:00 JST boundary).
func TestExchangeRowsFollowSeason(t *testing.T) {
	t.Cleanup(resetOperatorData)
	exchangeArchive(t)
	if err := exchangeFile.LoadFrom(""); err != nil {
		t.Fatal(err)
	}
	e := ActiveExchange()
	cases := []struct {
		when   time.Time
		events bool
	}{
		{time.Date(2026, time.October, 24, 12, 0, 0, 0, jst), false},
		{time.Date(2026, time.October, 25, 12, 0, 0, 0, jst), true},
		{time.Date(2026, time.November, 7, 23, 0, 0, 0, jst), true},
		{time.Date(2026, time.November, 8, 7, 0, 0, 0, jst), false},
	}
	for _, c := range cases {
		rows := ExchangeRowsAt(c.when)
		want := len(e.Rows)
		if c.events {
			want += len(e.Events[0].Rows)
		}
		if len(rows) != want {
			t.Fatalf("%s: %d rows, want %d", c.when.Format(time.RFC3339), len(rows), want)
		}
		if has := slices.ContainsFunc(rows, func(r ExchangeRow) bool { return r.ID == 2001 }); has != c.events {
			t.Fatalf("%s: event row 2001 on sale = %v, want %v", c.when.Format(time.RFC3339), has, c.events)
		}
	}
}

// TestExchangeBuyChargesCoinsAndGrants: a buy takes the row's Boss Coins and grants the item, in the
// same save; the reply carries the balance left.
func TestExchangeBuyChargesCoinsAndGrants(t *testing.T) {
	t.Cleanup(resetOperatorData)
	exchangeArchive(t)
	if err := exchangeFile.LoadFrom(""); err != nil {
		t.Fatal(err)
	}
	exchangeAccount(t, "9801", 100, nil)
	now := time.Date(2026, time.October, 24, 12, 0, 0, 0, jst)

	row, balance, ok := ExchangeBuy("9801", 1001, now)
	if !ok || row.ItemCode != "CUON00059" || balance != 70 || BossCoinBalance("9801") != 70 {
		t.Fatalf("buy 1001 = %+v balance %d ok %v, coins %d", row, balance, ok, BossCoinBalance("9801"))
	}
	if !exchangeOwns(t, "9801", "CUON00059") {
		t.Fatal("the clothing item was not granted")
	}

	// A furniture row grants a room item.
	if _, balance, ok := ExchangeBuy("9801", 1005, now); !ok || balance != 45 {
		t.Fatalf("buy 1005 balance %d ok %v", balance, ok)
	}
	if !exchangeOwns(t, "9801", "RUTA0004S") {
		t.Fatal("the room item was not granted")
	}
}

// TestExchangeRowsForHidesOwned: the list leaves out what the account owns, and keeps every row when
// it owns them all (an empty group would read "Boss Raid has finished.").
func TestExchangeRowsForHidesOwned(t *testing.T) {
	t.Cleanup(resetOperatorData)
	exchangeArchive(t)
	if err := exchangeFile.LoadFrom(""); err != nil {
		t.Fatal(err)
	}
	exchangeAccount(t, "9803", 1000, nil)
	now := time.Date(2026, time.October, 24, 12, 0, 0, 0, jst)
	all := ExchangeRowsAt(now)
	if _, _, ok := ExchangeBuy("9803", all[0].ID, now); !ok {
		t.Fatal("first buy refused")
	}
	if rows := ExchangeRowsFor("9803", now); len(rows) != len(all)-1 || rows[0].ID == all[0].ID {
		t.Fatalf("after one buy the list has %d rows, want %d without row %d", len(rows), len(all)-1, all[0].ID)
	}
	for _, r := range all[1:] {
		if _, _, ok := ExchangeBuy("9803", r.ID, now); !ok {
			t.Fatalf("buy %d refused", r.ID)
		}
	}
	if rows := ExchangeRowsFor("9803", now); len(rows) != len(all) {
		t.Fatalf("owning every row the list has %d rows, want all %d", len(rows), len(all))
	}
}

// TestExchangeBuyRefusalsChangeNothing: a short balance, an unknown row, an owned item, a row out of
// its season and a failed save are all refused with the account left as it was.
func TestExchangeBuyRefusalsChangeNothing(t *testing.T) {
	t.Cleanup(resetOperatorData)
	exchangeArchive(t)
	if err := exchangeFile.LoadFrom(""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 24, 12, 0, 0, 0, jst)
	halloween := time.Date(2026, time.October, 25, 12, 0, 0, 0, jst)

	exchangeAccount(t, "9802", 20, nil)
	if _, balance, ok := ExchangeBuy("9802", 1001, now); ok || balance != 20 || exchangeOwns(t, "9802", "CUON00059") {
		t.Fatalf("short balance: balance %d ok %v", balance, ok)
	}

	exchangeAccount(t, "9803", 100, nil)
	if _, _, ok := ExchangeBuy("9803", 9999, now); ok || BossCoinBalance("9803") != 100 {
		t.Fatal("an unknown row was sold")
	}
	if _, _, ok := ExchangeBuy("9803", 2001, now); ok || BossCoinBalance("9803") != 100 {
		t.Fatal("a Halloween row was sold before its season")
	}
	if _, _, ok := ExchangeBuy("9803", 2001, halloween); !ok || BossCoinBalance("9803") != 60 {
		t.Fatalf("a Halloween row in its season: coins %d", BossCoinBalance("9803"))
	}

	exchangeAccount(t, "9804", 100, []string{"CUTO0007B"})
	if _, balance, ok := ExchangeBuy("9804", 1003, now); ok || balance != 100 {
		t.Fatalf("an owned item was sold: balance %d ok %v", balance, ok)
	}

	exchangeAccount(t, "9805", 100, nil)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json") // MkdirAll fails: the parent is a file
	store.AccountsMu.Unlock()
	if _, balance, ok := ExchangeBuy("9805", 1001, now); ok || balance != 100 || exchangeOwns(t, "9805", "CUON00059") {
		t.Fatalf("a failed save changed the account: balance %d ok %v", balance, ok)
	}
}
