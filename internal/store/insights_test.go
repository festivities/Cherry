package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Insights tests seed ledger, chat and account rows straight into the attached database (the times are the test's), then
// read them back through the functions the staff pages call.

// seedLedger inserts one ledger line per row: time (RFC 3339), aid, currency, delta, reason. The balance is not checked here.
func seedLedger(t *testing.T, d *DB, rows ...[5]any) {
	t.Helper()
	if err := d.Update(func(tx *sql.Tx) error {
		for _, r := range rows {
			if _, err := tx.Exec(`INSERT INTO ledger (time, aid, currency, delta, balance, reason) VALUES (?, ?, ?, ?, 0, ?)`,
				r[0], r[1], r[2], r[3], r[4]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestLedgerFlowAndTopAccountsByWindow: the window cuts the ledger by time (a line older than it is not counted); the flow sums
// minted and spent per currency; the top lists rank by Gems and leave out the accounts with no avatar.
func TestLedgerFlowAndTopAccountsByWindow(t *testing.T) {
	d := attachTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	at := func(h int) string { return now.Add(-time.Duration(h) * time.Hour).Format(time.RFC3339) }
	seedLedger(t, d,
		[5]any{at(1), "1001", "gems", 100, "welcome gift"},
		[5]any{at(2), "1001", "gems", -30, "shop:buy:CUAA,CUAB"},
		[5]any{at(3), "1002", "gems", 50, "butler:order:12:3"},
		[5]any{at(4), "1002", "cash", 7, "butler:order:12:4"},
		[5]any{at(5), "1003", "gems", 20, "staff:discord:111:prize"},
		[5]any{at(6), "1003", "gems", -10, "gift send to 1234 CUEB"},
		[5]any{at(7), "1003", "gems", -15, "gift send to 5555 CUAA"},
		[5]any{at(7), "0", "gems", 500, "staff:discord:111:no avatar"},
		[5]any{at(48), "1002", "gems", 5, "heart:2026-11-15"},
		[5]any{at(40 * 24), "1001", "gems", 999, "welcome gift"},
	)

	day := now.Add(-24 * time.Hour)
	flow, err := LedgerFlow(day)
	if err != nil {
		t.Fatal(err)
	}
	gems := CurrencyFlow{}
	cash := CurrencyFlow{}
	for _, f := range flow {
		switch f.Currency {
		case "gems":
			gems = f
		case "cash":
			cash = f
		}
	}
	// Inside the day: gems minted 100+50+20+500 (the avatar-less grant counts in the flow), spent 30+10+15; cash minted 7.
	if gems.Minted != 670 || gems.Spent != 55 || gems.Lines != 7 {
		t.Errorf("gems flow %+v, want minted 670, spent 55, 7 lines", gems)
	}
	if cash.Minted != 7 || cash.Spent != 0 || cash.Lines != 1 {
		t.Errorf("cash flow %+v, want minted 7, 1 line", cash)
	}
	wider, err := LedgerFlow(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range wider {
		if f.Currency == "gems" && f.Minted != 670+5 {
			t.Errorf("30-day gems minted %d, want the day's 670 plus the 5 from two days back (the line 40 days back is outside)", f.Minted)
		}
	}

	earned, err := TopLedger(day, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(earned) != 3 || earned[0].Aid != "1001" || earned[0].Gems != 100 || earned[1].Aid != "1002" || earned[1].Gems != 50 || earned[1].Cash != 7 || earned[2].Aid != "1003" || earned[2].Gems != 20 {
		t.Errorf("earners %+v, want 1001 (100), 1002 (50 gems, 7 cash), 1003 (20); no account 0", earned)
	}
	spent, err := TopLedger(day, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(spent) != 2 || spent[0].Aid != "1001" || spent[0].Gems != 30 || spent[1].Aid != "1003" || spent[1].Gems != 25 {
		t.Errorf("spenders %+v, want 1001 (30), 1003 (25)", spent)
	}
	if _, err := TopLedger(day, true, 0); err == nil {
		t.Error("a top list of 0 accounts is refused")
	}
}

// TestLedgerReasonGroups: the reason groups follow the rule of ledgerGroupSQL, the most lines first, with the Gems and Cash
// each group moved in and out.
func TestLedgerReasonGroups(t *testing.T) {
	d := attachTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-time.Hour).Format(time.RFC3339)
	seedLedger(t, d,
		[5]any{at, "1001", "gems", 100, "welcome gift"},
		[5]any{at, "1001", "gems", -30, "shop:buy:CUAA,CUAB"},
		[5]any{at, "1002", "gems", 50, "butler:order:12:3"},
		[5]any{at, "1002", "cash", 7, "butler:order:12:4"},
		[5]any{at, "1003", "gems", 20, "staff:discord:111:prize"},
		[5]any{at, "1003", "gems", -10, "gift send to 1234 CUEB"},
		[5]any{at, "1003", "gems", -15, "gift send to 5555 CUAA"},
		[5]any{at, "1004", "gems", 3, "voucher purchase FSV1"},
		[5]any{at, "1004", "gems", 4, "heart:2026-11-16"},
		[5]any{at, "1004", "gems", 6, "atelier:blueprint:5:extra"},
		[5]any{at, "1004", "gems", 8, "one"},
	)
	groups, err := LedgerReasons(now.Add(-24*time.Hour), 15)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ReasonGroup{
		"butler:order":      {Group: "butler:order", Lines: 2, GemsIn: 50, CashIn: 7},
		"gift send":         {Group: "gift send", Lines: 2, GemsOut: 25},
		"shop:buy":          {Group: "shop:buy", Lines: 1, GemsOut: 30},
		"staff grant":       {Group: "staff grant", Lines: 1, GemsIn: 20},
		"welcome gift":      {Group: "welcome gift", Lines: 1, GemsIn: 100},
		"voucher purchase":  {Group: "voucher purchase", Lines: 1, GemsIn: 3},
		"heart:2026-11-16":  {Group: "heart:2026-11-16", Lines: 1, GemsIn: 4},
		"atelier:blueprint": {Group: "atelier:blueprint", Lines: 1, GemsIn: 6},
		"one":               {Group: "one", Lines: 1, GemsIn: 8},
	}
	if len(groups) != len(want) {
		t.Fatalf("%d groups, want %d: %+v", len(groups), len(want), groups)
	}
	for _, g := range groups {
		if w, ok := want[g.Group]; !ok || g != w {
			t.Errorf("group %+v, want %+v", g, w)
		}
	}
	if groups[0].Group != "butler:order" || groups[0].Lines != 2 {
		t.Errorf("the first group is %+v, want butler:order (two lines, the most)", groups[0])
	}
	top, err := LedgerReasons(now.Add(-24*time.Hour), 2)
	if err != nil || len(top) != 2 {
		t.Fatalf("limit 2 gave %d groups (err %v)", len(top), err)
	}
	if _, err := LedgerReasons(now, 0); err == nil {
		t.Error("a list of 0 groups is refused")
	}
}

// TestActivityByDayBucketsUTCDays: each series counts by UTC day from the window's first day; a row at 23:59:59 counts for its
// own day and one at 00:00:00 for the next; days with nothing have no key; avatar "0" is not a daily active player.
func TestActivityByDayBucketsUTCDays(t *testing.T) {
	d := attachTestDB(t)
	day15 := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	seedLedger(t, d,
		[5]any{"2026-11-16T00:00:00Z", "1001", "gems", 1, "a"},
		[5]any{"2026-11-16T23:59:59Z", "1001", "gems", 1, "b"},
		[5]any{"2026-11-17T01:00:00Z", "1003", "gems", 1, "c"},
		[5]any{"2026-11-16T09:00:00Z", "0", "gems", 1, "no avatar"},
		[5]any{"2026-11-14T23:59:59Z", "1009", "gems", 1, "before the window"},
	)
	if err := d.Update(func(tx *sql.Tx) error {
		for _, c := range []struct {
			ms     int64
			sender string
		}{
			{day15.Add(-time.Millisecond).UnixMilli(), "1009"}, // 2026-11-14 23:59:59.999: before the window
			{day15.Add(23*time.Hour + 59*time.Minute + 59*time.Second + 999*time.Millisecond).UnixMilli(), "1001"},
			{day15.Add(24 * time.Hour).UnixMilli(), "1002"},
			{day15.Add(24*time.Hour + time.Second).UnixMilli(), "0"},
		} {
			if _, err := tx.Exec(`INSERT INTO chat_log (time, kind, channel, sender, sender_name, text) VALUES (?, 'lounge', '1960000001', ?, 'n', 'hi')`, c.ms, c.sender); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Accounts: one created on the 16th (a sub-second stamp, as dbNow writes it), one on the 17th, one before the window.
	for i, created := range []string{"2026-11-16T10:00:00.123456789Z", "2026-11-17T09:00:00Z", "2026-11-14T09:00:00Z"} {
		if err := d.Update(func(tx *sql.Tx) error {
			aid := fmt.Sprint(770 + i)
			_, err := tx.Exec(`INSERT INTO accounts (aid, name, identity, guest_code_hash, created, updated, data_hash, data) VALUES (?, 'x', '', '', ?, ?, 'h', '{}')`,
				aid, created, created)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}

	act, err := ActivityByDay(day15)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, got map[string]int64, want map[string]int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s = %v, want %v", name, got, want)
			return
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s[%s] = %d, want %d (all %v)", name, k, got[k], v, got)
			}
		}
	}
	// Chat: 15th 23:59:59 (1001); 16th 00:00:00 (1002) and 00:00:01 (avatar 0: counted here, not in Active). The 14th is out.
	check("chat", act.Chat, map[string]int64{"2026-11-15": 1, "2026-11-16": 2})
	// Ledger lines: 16th two (one of them avatar 0), 17th one; the 14th line is out.
	check("lines", act.Lines, map[string]int64{"2026-11-16": 3, "2026-11-17": 1})
	// Active: the 15th has 1001 (chat); the 16th has 1001 (ledger) and 1002 (chat) — 1001 counts once; the 17th has 1003.
	check("active", act.Active, map[string]int64{"2026-11-15": 1, "2026-11-16": 2, "2026-11-17": 1})
	// Sign-ups: accounts created on the 16th and the 17th; the 14th is out.
	check("signups", act.Signups, map[string]int64{"2026-11-16": 1, "2026-11-17": 1})
}

// TestInsightQueriesUseTheirIndexes: the statements that cut the ledger, the accounts and the chat log by time read one range of
// the index that the schema v6 adds or v5 has, and never scan the table.
func TestInsightQueriesUseTheirIndexes(t *testing.T) {
	d := attachTestDB(t)
	from := "2026-11-01"
	stamp := "2026-11-01T00:00:00Z"
	ms := int64(1793491200000)
	cases := []struct {
		name, query string
		args        []any
		index       string
	}{
		{"flow", qLedgerFlow, []any{stamp}, "ledger_time"},
		{"reasons", qLedgerReasons, []any{stamp, 15}, "ledger_time"},
		{"top", qTopLedger, []any{stamp, 1, 10}, "ledger_time"},
		{"signups", qSignups, []any{from}, "accounts_created"},
		{"chat", qChatDays, []any{ms}, "chat_log_time"},
		{"lines", qLineDays, []any{from}, "ledger_time"},
		{"active", qActiveDays, []any{from, ms}, "ledger_time"},
	}
	for _, c := range cases {
		rows, err := d.read.Query("EXPLAIN QUERY PLAN "+c.query, c.args...)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		text := strings.Join(plan, "; ")
		if !strings.Contains(text, c.index) {
			t.Errorf("%s: plan %q does not use %s", c.name, text, c.index)
		}
		for _, table := range []string{"SCAN ledger", "SCAN accounts", "SCAN chat_log"} {
			if strings.Contains(text, table) {
				t.Errorf("%s: plan %q scans a whole table (%s)", c.name, text, table)
			}
		}
	}
}

// TestMigrationV5ToV6AddsIndexesAndKeepsRows: a database at schema v5 with rows migrates to v6 with its rows in place and the
// two time indexes added.
func TestMigrationV5ToV6AddsIndexesAndKeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), DBFileName)
	raw, err := sql.Open("sqlite", path+"?"+dbPragmas)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(raw, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO ledger (time, aid, currency, delta, balance, reason) VALUES ('2026-11-01T00:00:00Z', '9031', 'gems', 4, 4, 'kept')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err := OpenDB(path)
	if err != nil {
		t.Fatalf("migrate v5 to v6: %v", err)
	}
	defer d.Close()
	var version int
	if err := d.write.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("user_version = %d (err %v), want %d", version, err, len(migrations))
	}
	var reason string
	if err := d.write.QueryRow("SELECT reason FROM ledger").Scan(&reason); err != nil || reason != "kept" {
		t.Fatalf("ledger row after migration = %q (err %v)", reason, err)
	}
	for _, name := range []string{"ledger_time", "accounts_created"} {
		var n int
		if err := d.write.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s: %d found (err %v)", name, n, err)
		}
	}
}

// TestKickPlayerAuditsAndNamesTheConnections: a kick checks its reason, writes one audit row with the avatar id and the reason, names
// the avatar id whose connections the caller closes, and saves nothing on the account (no ban).
func TestKickPlayerAuditsAndNamesTheConnections(t *testing.T) {
	attachTestDB(t)
	acc, id := staffAccount(t, "dev-kick", "1101", "Kicked", 10)
	before := auditCount(t)
	if _, err := KickPlayer(id, "   ", "discord:1"); err == nil {
		t.Fatal("a kick with no reason is accepted")
	}
	if _, err := KickPlayer(id+999, "spam", "discord:1"); err != ErrNoPlayer {
		t.Fatalf("kick of an unknown player: %v, want ErrNoPlayer", err)
	}
	if auditCount(t) != before {
		t.Fatal("a refused kick wrote an audit row")
	}
	aid, err := KickPlayer(id, "  spam  ", "discord:1")
	if err != nil {
		t.Fatal(err)
	}
	if aid != "1101" {
		t.Errorf("kick names aid %q, want 1101", aid)
	}
	rows, err := AuditRows(0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || rows[0].Action != "kick" || rows[0].Actor != "discord:1" || rows[0].Target != playerTarget(id) ||
		!strings.Contains(rows[0].Detail, `"reason":"spam"`) || !strings.Contains(rows[0].Detail, `"aid":"1101"`) {
		t.Errorf("audit row %+v, want one kick by discord:1 on the player with the reason spam", rows)
	}
	if acc.Ban != nil {
		t.Error("a kick banned the account")
	}
	if _, found := PlayerByID(id); !found {
		t.Error("the account is gone after a kick")
	}
}
