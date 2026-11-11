package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Account rows of cherry.db: accounts, tokens, the account scalars of meta and the ledger. Access tokens and the guest
// code reach the database only as SHA-256 hex (CodeHash). The writers are called by commitLocked (accounts.go) inside its
// transaction; the readers take a Querier. Token rows are insert-only: an alias is never pruned.

// AccountState is the account table as read back: the accounts in row order, the token hash -> account index, the latest
// account and the next avatar id. The clear secrets are not in the database: Account.AccessToken and GuestCode are ""
// after a read, and GuestCodeHash holds the hash.
// ponytail: every account is read into RAM at start, as the JSON store did; load on demand when the table is too big.
type AccountState struct {
	Accounts     []*Account
	ByToken      map[string]*Account // CodeHash of an access token or alias -> its account
	Latest       *Account
	NextAvatarID uint64
	rowIDs       map[*Account]int64
}

// accountRow is the row of acc: the guest code hash, the data (the StoredFrom JSON) and the checksum of both. The
// checksum is what the database keeps as data_hash and what the safety net compares.
func accountRow(acc *Account) (guest, data, sum string, err error) {
	b, err := json.Marshal(StoredFrom(acc))
	if err != nil {
		return "", "", "", err
	}
	guest = acc.GuestCodeHash
	if acc.GuestCode != "" {
		guest = CodeHash(acc.GuestCode)
	}
	data = string(b)
	return guest, data, CodeHash(guest + "\n" + data), nil
}

// dbNow is the timestamp of the created, updated and last_used columns.
func dbNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// insertAccountRowTx writes the row of an account that has none and returns its id and checksum.
func insertAccountRowTx(tx *sql.Tx, acc *Account) (int64, string, error) {
	guest, data, sum, err := accountRow(acc)
	if err != nil {
		return 0, "", err
	}
	now := dbNow()
	res, err := tx.Exec(`INSERT INTO accounts (aid, name, identity, guest_code_hash, created, updated, data_hash, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, acc.Aid, acc.Name, acc.Identity, guest, now, now, sum, data)
	if err != nil {
		return 0, "", err
	}
	id, err := res.LastInsertId()
	return id, sum, err
}

// updateAccountRowTx rewrites the row id with the current fields of acc and returns the checksum.
// ponytail: one JSON column, so a save writes the whole account; split the column when the WAL numbers ask (PLAN P2).
func updateAccountRowTx(tx *sql.Tx, id int64, acc *Account) (string, error) {
	guest, data, sum, err := accountRow(acc)
	if err != nil {
		return "", err
	}
	res, err := tx.Exec(`UPDATE accounts SET aid = ?, name = ?, identity = ?, guest_code_hash = ?, updated = ?, data_hash = ?, data = ?
		WHERE id = ?`, acc.Aid, acc.Name, acc.Identity, guest, dbNow(), sum, data, id)
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return "", errors.New("account row: missing")
	}
	return sum, nil
}

// addTokenRowTx writes the token row of a pending token of the account whose row is id. Only the hash is stored.
func addTokenRowTx(tx *sql.Tx, id int64, p pendingToken) error {
	kind := "alias"
	if p.own {
		kind = "own"
	}
	_, err := tx.Exec(`INSERT INTO tokens (hash, account_id, kind, created, last_used) VALUES (?, ?, ?, ?, '')`,
		p.hash, id, kind, dbNow())
	return err
}

// deleteAccountRowTx removes the row id; its tokens go with it (ON DELETE CASCADE).
func deleteAccountRowTx(tx *sql.Tx, id int64) error {
	res, err := tx.Exec(`DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.New("account row: missing")
	}
	return nil
}

// SetAccountMetaTx stores the account scalars of the store: nextAvatarId and the latest account's row id (0 = none).
func SetAccountMetaTx(tx *sql.Tx, nextAvatarID uint64, latestID int64) error {
	if err := setMetaTx(tx, "accounts.nextAvatarId", strconv.FormatUint(nextAvatarID, 10)); err != nil {
		return err
	}
	return setMetaTx(tx, "accounts.latest", strconv.FormatInt(latestID, 10))
}

// ReadAccounts reads every account, its token hashes and the account scalars. A row whose data does not match its
// checksum is an error.
func ReadAccounts(q Querier) (AccountState, error) {
	st := AccountState{ByToken: map[string]*Account{}, rowIDs: map[*Account]int64{}}
	byID := map[int64]*Account{}
	rows, err := q.Query(`SELECT id, guest_code_hash, data, data_hash FROM accounts ORDER BY id`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var guest, data, sum string
		if err := rows.Scan(&id, &guest, &data, &sum); err != nil {
			return st, err
		}
		if CodeHash(guest+"\n"+data) != sum {
			return st, fmt.Errorf("account row %d: data does not match its checksum", id)
		}
		var saved StoredAccount
		if err := json.Unmarshal([]byte(data), &saved); err != nil {
			return st, fmt.Errorf("account row %d: %w", id, err)
		}
		acc := accountFromStored(saved)
		acc.GuestCodeHash = guest
		byID[id] = acc
		st.rowIDs[acc] = id
		st.Accounts = append(st.Accounts, acc)
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	tokens, err := q.Query(`SELECT hash, account_id FROM tokens`)
	if err != nil {
		return st, err
	}
	defer tokens.Close()
	for tokens.Next() {
		var hash string
		var id int64
		if err := tokens.Scan(&hash, &id); err != nil {
			return st, err
		}
		acc := byID[id]
		if acc == nil {
			return st, errors.New("token row without its account")
		}
		st.ByToken[hash] = acc
	}
	if err := tokens.Err(); err != nil {
		return st, err
	}
	latest, err := metaInt(q, "accounts.latest")
	if err != nil {
		return st, err
	}
	if latest != 0 {
		if st.Latest = byID[latest]; st.Latest == nil {
			return st, errors.New("latest account row missing")
		}
	}
	st.NextAvatarID, err = metaUint(q, "accounts.nextAvatarId")
	return st, err
}

// AppendLedgerTx appends the ledger lines to the ledger table, in order.
func AppendLedgerTx(tx *sql.Tx, lines []LedgerLine) error {
	if len(lines) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`INSERT INTO ledger (time, aid, currency, delta, balance, reason) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, l := range lines {
		if _, err := stmt.Exec(l.Time, l.Aid, l.Currency, l.Delta, l.Balance, l.Reason); err != nil {
			return err
		}
	}
	return nil
}

// ReadLedger returns every ledger row in append order.
func ReadLedger(q Querier) ([]LedgerLine, error) {
	rows, err := q.Query(`SELECT time, aid, currency, delta, balance, reason FROM ledger ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []LedgerLine{}
	for rows.Next() {
		var l LedgerLine
		if err := rows.Scan(&l.Time, &l.Aid, &l.Currency, &l.Delta, &l.Balance, &l.Reason); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

// setMetaTx writes one scalar of the meta table.
func setMetaTx(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// metaValue is the text of a meta key; "" when the key is absent.
func metaValue(q Querier, key string) (string, error) {
	var v string
	err := q.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// metaUint and metaInt read a meta key as a number; an absent key is 0.
func metaUint(q Querier, key string) (uint64, error) {
	v, err := metaValue(q, key)
	if err != nil || v == "" {
		return 0, err
	}
	return strconv.ParseUint(v, 10, 64)
}

func metaInt(q Querier, key string) (int64, error) {
	v, err := metaValue(q, key)
	if err != nil || v == "" {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// metaBool reads a meta key written by setMetaBoolTx; an absent key is false.
func metaBool(q Querier, key string) (bool, error) {
	v, err := metaValue(q, key)
	return v == "1", err
}

func setMetaBoolTx(tx *sql.Tx, key string, b bool) error {
	if b {
		return setMetaTx(tx, key, "1")
	}
	return setMetaTx(tx, key, "0")
}
