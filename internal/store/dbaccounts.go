package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Account rows of cherry.db (accounts, tokens, the account part of meta, ledger). Access tokens and aliases reach the
// database only as SHA-256 hex (CodeHash); so does the guest code. Every write takes the caller's transaction. Tokens
// are insert-only: an alias is never pruned (AGENTS, "Tokens are stored in clear"), and a row read back has only hashes,
// so a save never rewrites token rows.

// AccountState is the account table as read back: the in-memory accounts (internal id order), the token hash -> account
// index that replaces the clear-token map, the latest account and the next avatar id. The clear secrets are not in the
// database: Account.AccessToken and Account.GuestCode are "" after a read, and Account.GuestCodeHash holds the hash.
// ponytail: every account is read into RAM at start, as the JSON store does; load on demand when the table is too big.
type AccountState struct {
	Accounts     []*Account
	ByToken      map[string]*Account // CodeHash of an access token or alias -> its account
	Latest       *Account
	NextAvatarID uint64
}

// accountRow is what the accounts row of acc holds: the StoredFrom JSON without the clear access token and guest code,
// its SHA-256, and the hash of the guest code (the clear one when set, else the hash read back earlier).
func accountRow(acc *Account) (guest, data, sum string, err error) {
	s := StoredFrom(acc)
	s.AccessToken = ""
	s.GuestCode = ""
	b, err := json.Marshal(s)
	if err != nil {
		return "", "", "", err
	}
	guest = acc.GuestCodeHash
	if acc.GuestCode != "" {
		guest = CodeHash(acc.GuestCode)
	}
	data = string(b)
	return guest, data, CodeHash(data), nil
}

// dbNow is the timestamp of the created, updated and last_used columns.
func dbNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// InsertAccountTx adds acc as a new row whose access token is ownToken, and sets acc.DBID. acc must not have a row yet.
// If the transaction later fails, acc.DBID is stale: the caller drops the account.
func InsertAccountTx(tx *sql.Tx, acc *Account, ownToken string) error {
	if acc.DBID != 0 || ownToken == "" {
		return errors.New("account row: a new account needs an access token and no row yet")
	}
	guest, data, sum, err := accountRow(acc)
	if err != nil {
		return err
	}
	now := dbNow()
	res, err := tx.Exec(`INSERT INTO accounts (aid, name, identity, guest_code_hash, created, updated, data_hash, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, acc.Aid, acc.Name, acc.Identity, guest, now, now, sum, data)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO tokens (hash, account_id, kind, created, last_used) VALUES (?, ?, 'own', ?, '')`,
		CodeHash(ownToken), id, now); err != nil {
		return err
	}
	acc.DBID = id
	return nil
}

// SaveAccountTx rewrites the row of an account that has one (acc.DBID) with its current fields.
// ponytail: one JSON column, so a save writes the whole account; split the column when the WAL numbers ask (PLAN P2).
func SaveAccountTx(tx *sql.Tx, acc *Account) error {
	if acc.DBID == 0 {
		return errors.New("account row: the account has no row yet")
	}
	guest, data, sum, err := accountRow(acc)
	if err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE accounts SET aid = ?, name = ?, identity = ?, guest_code_hash = ?, updated = ?, data_hash = ?, data = ?
		WHERE id = ?`, acc.Aid, acc.Name, acc.Identity, guest, dbNow(), sum, data, acc.DBID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.New("account row: missing")
	}
	return nil
}

// AddTokenTx adds token as an alias of acc (AV_AUTH). The token is stored as its hash only.
func AddTokenTx(tx *sql.Tx, acc *Account, token string) error {
	if acc.DBID == 0 || token == "" {
		return errors.New("account token: needs an account row and a token")
	}
	_, err := tx.Exec(`INSERT INTO tokens (hash, account_id, kind, created, last_used) VALUES (?, ?, 'alias', ?, '')`,
		CodeHash(token), acc.DBID, dbNow())
	return err
}

// DeleteAccountTx removes acc's row; its tokens go with it (ON DELETE CASCADE).
func DeleteAccountTx(tx *sql.Tx, acc *Account) error {
	if acc.DBID == 0 {
		return errors.New("account row: the account has no row yet")
	}
	res, err := tx.Exec(`DELETE FROM accounts WHERE id = ?`, acc.DBID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.New("account row: missing")
	}
	return nil
}

// SetAccountMetaTx stores the account scalars of the JSON envelope: nextAvatarId and the latest account (nil = none).
// latest must already have its row.
func SetAccountMetaTx(tx *sql.Tx, nextAvatarID uint64, latest *Account) error {
	var latestID int64
	if latest != nil {
		latestID = latest.DBID
	}
	if err := setMetaTx(tx, "accounts.nextAvatarId", strconv.FormatUint(nextAvatarID, 10)); err != nil {
		return err
	}
	return setMetaTx(tx, "accounts.latest", strconv.FormatInt(latestID, 10))
}

// ReadAccounts reads every account, its token hashes and the account scalars. A row whose data does not match its hash
// is an error.
func ReadAccounts(q Querier) (AccountState, error) {
	st := AccountState{ByToken: map[string]*Account{}}
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
		if CodeHash(data) != sum {
			return st, fmt.Errorf("account row %d: data does not match its hash", id)
		}
		var saved StoredAccount
		if err := json.Unmarshal([]byte(data), &saved); err != nil {
			return st, fmt.Errorf("account row %d: %w", id, err)
		}
		acc := accountFromStored(saved)
		acc.DBID = id
		acc.GuestCodeHash = guest
		byID[id] = acc
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

// AppendLedgerTx appends the ledger lines (the lines of ledger.jsonl) to the ledger table, in order.
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
