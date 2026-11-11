package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestPragmasOnEveryConnection: every connection of both handles has the settings of dbPragmas; the read pool connections
// are query_only and the write connection is not.
func TestPragmasOnEveryConnection(t *testing.T) {
	d, _ := openTestDB(t)
	ctx := context.Background()
	want := []struct{ name, value string }{
		{"journal_mode", "wal"}, {"synchronous", "1"}, {"busy_timeout", "5000"}, {"foreign_keys", "1"},
	}
	check := func(role string, c *sql.Conn, queryOnly string) {
		t.Helper()
		for _, p := range want {
			var got string
			if err := c.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(&got); err != nil {
				t.Fatalf("%s: PRAGMA %s: %v", role, p.name, err)
			}
			if got != p.value {
				t.Errorf("%s: PRAGMA %s = %s, want %s", role, p.name, got, p.value)
			}
		}
		var got string
		if err := c.QueryRowContext(ctx, "PRAGMA query_only").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != queryOnly {
			t.Errorf("%s: PRAGMA query_only = %s, want %s", role, got, queryOnly)
		}
	}
	// Hold the four read connections at once, so the pool opens each of them.
	var reads []*sql.Conn
	for i := 0; i < 4; i++ {
		c, err := d.read.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reads = append(reads, c)
	}
	for i, c := range reads {
		check(fmt.Sprintf("read connection %d", i), c, "1")
		c.Close()
	}
	w, err := d.write.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check("write connection", w, "0")
	w.Close()
}

// TestWriteTransactionsBeginImmediate: a write transaction takes the write lock when it begins (_txlock=immediate), so a
// second writer cannot begin while it is open.
func TestWriteTransactionsBeginImmediate(t *testing.T) {
	d, path := openTestDB(t)
	tx, err := d.write.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	other, err := sql.Open("sqlite", path) // default busy timeout 0: a busy lock fails at once
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	c, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err == nil {
		c.ExecContext(context.Background(), "ROLLBACK")
		t.Fatal("a second writer began while a write transaction was open: the transactions are not IMMEDIATE")
	}
}

// TestMigrationRunner: a fresh file reaches the current version, a reopen changes nothing, and a database from a newer
// build is refused.
func TestMigrationRunner(t *testing.T) {
	path := filepath.Join(t.TempDir(), DBFileName)
	d, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := d.write.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d (err %v), want %d", v, err, len(migrations))
	}
	d.Close()
	if d, err = OpenDB(path); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	d.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if _, err := OpenDB(path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("a newer schema opened: %v", err)
	}
}

// TestAccountIndexes: aid "0" is shared by accounts that have no avatar yet; a real aid, an identity and a guest code
// are each held by one account only. An empty identity or guest code is not indexed.
func TestAccountIndexes(t *testing.T) {
	d, _ := openTestDB(t)
	add := func(aid, identity, guest string) error {
		acc := newRecord("")
		acc.Aid, acc.Name, acc.Identity, acc.GuestCode = aid, "n", identity, guest
		return d.Update(func(tx *sql.Tx) error { return InsertAccountTx(tx, acc, RandomToken(32)) })
	}
	mustAccept := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s refused: %v", what, err)
		}
	}
	mustRefuse := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s accepted", what)
		}
	}
	mustAccept("first account with aid 0", add("0", "", ""))
	mustAccept("second account with aid 0", add("0", "", ""))
	mustAccept("account 1001", add("1001", "dev-a", "guest-a"))
	mustRefuse("second account 1001", add("1001", "", ""))
	mustRefuse("second identity dev-a", add("1002", "dev-a", ""))
	mustRefuse("second guest code guest-a", add("1003", "", "guest-a"))
	mustAccept("empty identity and guest code again", add("1004", "", ""))
}

// TestTokensAreNeverStoredInClear: the database holds SHA-256 hex of the access tokens, the aliases and the guest code,
// never the clear values; the scan covers the main file and the WAL.
func TestTokensAreNeverStoredInClear(t *testing.T) {
	d, path := openTestDB(t)
	var secrets []string
	err := d.Update(func(tx *sql.Tx) error {
		for i := 0; i < 2; i++ {
			acc, own, alias, guest := syntheticAccount(i)
			if err := InsertAccountTx(tx, acc, own); err != nil {
				return err
			}
			if err := AddTokenTx(tx, acc, alias); err != nil {
				return err
			}
			secrets = append(secrets, own, alias, guest)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scan := func(when string) {
		t.Helper()
		hashFound := false
		for _, name := range []string{path, path + "-wal"} {
			data, err := os.ReadFile(name)
			if err != nil {
				continue // no WAL after a clean close
			}
			for _, s := range secrets {
				if bytes.Contains(data, []byte(s)) {
					t.Fatalf("%s: a clear token or guest code is in %s", when, filepath.Base(name))
				}
			}
			hashFound = hashFound || bytes.Contains(data, []byte(CodeHash(secrets[0])))
		}
		if !hashFound {
			t.Fatalf("%s: no token hash in the database files: the scan would be blind", when)
		}
	}
	scan("written")
	d.Close()
	scan("closed")
}

// TestAccountAndLedgerAreOneTransaction: a failed account write rolls back the ledger lines written before it in the
// same transaction, and the account is not there either.
func TestAccountAndLedgerAreOneTransaction(t *testing.T) {
	d, _ := openTestDB(t)
	first := newRecord("")
	first.Aid = "1001"
	if err := d.Update(func(tx *sql.Tx) error { return InsertAccountTx(tx, first, RandomToken(32)) }); err != nil {
		t.Fatal(err)
	}
	lines := []LedgerLine{{Time: "2026-11-11T00:00:00Z", Aid: "1001", Currency: "gems", Delta: -5, Balance: 10, Reason: "test"}}
	second := newRecord("")
	second.Aid = "1001" // duplicate real aid: the insert fails after the ledger lines are written
	err := d.Update(func(tx *sql.Tx) error {
		if err := AppendLedgerTx(tx, lines); err != nil {
			return err
		}
		return InsertAccountTx(tx, second, RandomToken(32))
	})
	if err == nil {
		t.Fatal("the duplicate account was accepted")
	}
	if err := d.View(func(q Querier) error {
		got, err := ReadLedger(q)
		if err != nil {
			return err
		}
		if len(got) != 0 {
			return fmt.Errorf("%d ledger lines survived the failed transaction", len(got))
		}
		st, err := ReadAccounts(q)
		if err != nil {
			return err
		}
		if len(st.Accounts) != 1 || len(st.ByToken) != 1 {
			return fmt.Errorf("%d accounts and %d tokens after the failed transaction, want 1 and 1", len(st.Accounts), len(st.ByToken))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestDeleteAccountDropsItsTokens: deleting an account removes its token rows (ON DELETE CASCADE), and another
// account's tokens stay.
func TestDeleteAccountDropsItsTokens(t *testing.T) {
	d, _ := openTestDB(t)
	a, b := newRecord(""), newRecord("")
	a.Aid, b.Aid = "1001", "1002"
	var aliasOfA string
	err := d.Update(func(tx *sql.Tx) error {
		if err := InsertAccountTx(tx, a, RandomToken(32)); err != nil {
			return err
		}
		aliasOfA = RandomToken(32)
		if err := AddTokenTx(tx, a, aliasOfA); err != nil {
			return err
		}
		return InsertAccountTx(tx, b, RandomToken(32))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Update(func(tx *sql.Tx) error { return DeleteAccountTx(tx, a) }); err != nil {
		t.Fatal(err)
	}
	if err := d.View(func(q Querier) error {
		st, err := ReadAccounts(q)
		if err != nil {
			return err
		}
		if len(st.Accounts) != 1 || st.Accounts[0].Aid != "1002" || len(st.ByToken) != 1 {
			return fmt.Errorf("after the delete: %d accounts, %d tokens", len(st.Accounts), len(st.ByToken))
		}
		if st.ByToken[CodeHash(aliasOfA)] != nil {
			return fmt.Errorf("the alias of the deleted account still resolves")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAccountRowsRoundTrip: fully populated accounts go into the database and come back with the same stored JSON, the
// same guest code hash, every token resolving to its account, the latest pointer and the next avatar id.
func TestAccountRowsRoundTrip(t *testing.T) {
	d, _ := openTestDB(t)
	var accs []*Account
	var owns, aliases []string
	err := d.Update(func(tx *sql.Tx) error {
		for i := 0; i < 3; i++ {
			acc, own, alias, _ := syntheticAccount(i)
			if err := InsertAccountTx(tx, acc, own); err != nil {
				return err
			}
			if err := AddTokenTx(tx, acc, alias); err != nil {
				return err
			}
			accs = append(accs, acc)
			owns = append(owns, own)
			aliases = append(aliases, alias)
		}
		return SetAccountMetaTx(tx, 1100, testRowIDs[accs[2]])
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.View(func(q Querier) error {
		st, err := ReadAccounts(q)
		if err != nil {
			return err
		}
		if len(st.Accounts) != 3 || len(st.ByToken) != 6 || st.NextAvatarID != 1100 {
			return fmt.Errorf("read %d accounts, %d tokens, next id %d", len(st.Accounts), len(st.ByToken), st.NextAvatarID)
		}
		if st.Latest != st.ByToken[CodeHash(owns[2])] {
			return fmt.Errorf("the latest account is not the one written last")
		}
		for i, acc := range accs {
			got := st.ByToken[CodeHash(owns[i])]
			if got == nil || st.ByToken[CodeHash(aliases[i])] != got {
				return fmt.Errorf("account %d: its tokens do not resolve to one account", i)
			}
			wantGuest, wantData, _, err := accountRow(acc)
			if err != nil {
				return err
			}
			gotGuest, gotData, _, err := accountRow(got)
			if err != nil {
				return err
			}
			if wantGuest != gotGuest || wantData != gotData {
				return fmt.Errorf("account %d differs after the round trip", i)
			}
			if got.AccessToken != "" || got.GuestCode != "" || got.GuestCodeHash != wantGuest {
				return fmt.Errorf("account %d: a clear secret came back or the guest hash is wrong", i)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSocialRowsRoundTrip, TestCirclesRowsRoundTrip, TestTreasureRowsRoundTrip and TestChatRowsRoundTrip: every field of
// each stored type (filled by reflection) comes back unchanged.
func TestSocialRowsRoundTrip(t *testing.T) {
	d, _ := openTestDB(t)
	var want savedSocial
	fillSynthetic(reflect.ValueOf(&want).Elem())
	want.Version = 1
	checkRoundTrip(t, d, "social", want, writeSocialTx, readSocial)
}

func TestCirclesRowsRoundTrip(t *testing.T) {
	d, _ := openTestDB(t)
	var want CircleBook
	fillSynthetic(reflect.ValueOf(&want).Elem())
	checkRoundTrip(t, d, "circles", want, writeCirclesTx, readCircles)
}

func TestTreasureRowsRoundTrip(t *testing.T) {
	d, _ := openTestDB(t)
	var rec TreasureRecord
	fillSynthetic(reflect.ValueOf(&rec).Elem())
	want := map[string]TreasureRecord{"1001": rec}
	checkRoundTrip(t, d, "treasure", want, writeTreasureTx, readTreasure)
}

func TestChatRowsRoundTrip(t *testing.T) {
	d, _ := openTestDB(t)
	var want ChatRoomBook
	fillSynthetic(reflect.ValueOf(&want).Elem())
	checkRoundTrip(t, d, "chat rooms", want, writeChatRoomsTx, readChatRooms)
}

// bigCodes is a list of n item codes of the length a long-playing account keeps (about 13 bytes of JSON each).
func bigCodes(n int) []string {
	codes := make([]string, n)
	for i := range codes {
		codes[i] = fmt.Sprintf("CU%08d", i)
	}
	return codes
}

// TestWALBytesPerAccountWrite logs what one account write costs in the WAL (PLAN P2, "Write volume"): a small account
// (about 2 KB of JSON) and a large one (about 300 KB, as a long-playing account such as 1001 is), each inserted and then
// saved after one change, every write alone after a checkpoint.
func TestWALBytesPerAccountWrite(t *testing.T) {
	d, path := openTestDB(t)
	small, own, _, _ := syntheticAccount(1)
	large, ownLarge, _, _ := syntheticAccount(2)
	large.ItemCodes = bigCodes(12000)
	large.InventoryCodes = bigCodes(12000)
	measure := func(name string, acc *Account, write func(*sql.Tx) error) {
		t.Helper()
		// The checkpoint reports busy (not an error) when it could not reset the WAL, which would mix frames of two writes.
		var busy, logFrames, moved int
		if err := d.write.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &moved); err != nil || busy != 0 {
			t.Fatalf("%s: checkpoint busy %d (err %v): the WAL was not reset", name, busy, err)
		}
		t.Logf("%s: before the write the WAL held %d frames", name, logFrames)
		if err := d.Update(write); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		_, data, _, err := accountRow(acc)
		if err != nil {
			t.Fatal(err)
		}
		// The measured write must be the real one: the row holds the account as it is now.
		var stored string
		if err := d.View(func(q Querier) error {
			return q.QueryRow(`SELECT data FROM accounts WHERE id = ?`, testRowIDs[acc]).Scan(&stored)
		}); err != nil || stored != data {
			t.Fatalf("%s: the row does not hold the written account (err %v)", name, err)
		}
		t.Logf("%s: account JSON %d B, WAL %d B for this write", name, len(data), fi.Size())
	}
	measure("small account, insert", small, func(tx *sql.Tx) error { return InsertAccountTx(tx, small, own) })
	measure("small account, save after one change", small, func(tx *sql.Tx) error {
		small.Gems++
		return SaveAccountTx(tx, small)
	})
	measure("large account, insert", large, func(tx *sql.Tx) error { return InsertAccountTx(tx, large, ownLarge) })
	measure("large account, save after one change", large, func(tx *sql.Tx) error {
		large.Gems++
		return SaveAccountTx(tx, large)
	})
	// A code added at the front of a list moves every byte after it: the realistic worst case for overflow pages.
	measure("large account, save after one code added at the front of a list", large, func(tx *sql.Tx) error {
		large.InventoryCodes = append([]string{"CU99999999"}, large.InventoryCodes...)
		return SaveAccountTx(tx, large)
	})
}
