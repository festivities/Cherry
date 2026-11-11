package store

import (
	"database/sql"
	"errors"
)

// The schema tests write account rows directly, through these helpers. They keep the row ids that the store keeps beside
// each account (accountRows) in a table of their own, so a test writes a row exactly as the save path does.
var testRowIDs = map[*Account]int64{}

// InsertAccountTx writes acc's row with ownToken as its own token; acc must not have a row yet.
func InsertAccountTx(tx *sql.Tx, acc *Account, ownToken string) error {
	if testRowIDs[acc] != 0 || ownToken == "" {
		return errors.New("account row: a new account needs an access token and no row yet")
	}
	id, _, err := insertAccountRowTx(tx, acc)
	if err != nil {
		return err
	}
	testRowIDs[acc] = id
	return addTokenRowTx(tx, id, pendingToken{acc: acc, hash: CodeHash(ownToken), own: true})
}

// AddTokenTx adds token as an alias of acc.
func AddTokenTx(tx *sql.Tx, acc *Account, token string) error {
	return addTokenRowTx(tx, testRowIDs[acc], pendingToken{acc: acc, hash: CodeHash(token)})
}

// SaveAccountTx rewrites the row of an account that has one.
func SaveAccountTx(tx *sql.Tx, acc *Account) error {
	_, err := updateAccountRowTx(tx, testRowIDs[acc], acc)
	return err
}

// DeleteAccountTx removes acc's row.
func DeleteAccountTx(tx *sql.Tx, acc *Account) error {
	return deleteAccountRowTx(tx, testRowIDs[acc])
}

// savedAccounts is the legacy accounts.json that cmd/cherry-import reads (the format the server wrote before slice 8.3b).
type savedAccounts struct {
	Version      int                      `json:"version"`
	Accounts     map[string]StoredAccount `json:"accounts"`
	Aliases      map[string]string        `json:"aliases"`
	Latest       string                   `json:"latest"`
	NextAvatarID uint64                   `json:"nextAvatarId"`
}
