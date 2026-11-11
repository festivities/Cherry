package store

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
)

// Provider identities (CONTRACT §3, slice 8.6a). An identity ("discord:<id>" or "dev-<name>") is stored on its account
// with the SHA-256 hashes of the uniqueCodes minted for it; a code itself is never stored. The fields are columns of the
// account row (identity, the data JSON).

// maxCodeHashes bounds the codes one identity holds; minting a new one drops the oldest.
const maxCodeHashes = 10

// ErrUnknownCode means no account holds the hash of the presented uniqueCode.
var ErrUnknownCode = errors.New("unknown unique code")

// CodeHash is the stored form of a uniqueCode: lowercase hex SHA-256.
func CodeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// IdentityAccountLocked returns the account of a provider identity, or nil. Caller holds AccountsMu.
// ponytail: linear scan; index by identity when the account table grows.
func IdentityAccountLocked(identity string) *Account {
	if identity == "" {
		return nil
	}
	for _, acc := range accountList {
		if acc.Identity == identity {
			return acc
		}
	}
	return nil
}

// MintIdentityCode is the exchange step: the identity's account, created with aid "0" when it has none, gets the hash of a
// new uniqueCode (the oldest hash is dropped beyond maxCodeHashes). It reports the account's aid and whether the account
// has no avatar yet. A failed save leaves the account as it was.
func MintIdentityCode(identity, codeHash string) (aid string, isNew bool, err error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := IdentityAccountLocked(identity)
	if acc == nil {
		rec := newRecord("")
		rec.Identity = identity
		rec.CodeHashes = []string{codeHash}
		created, err := insertAccountLocked(rec)
		if err != nil {
			return "", false, err
		}
		return created.Aid, true, nil
	}
	prev := *acc
	hashes := append(append([]string(nil), acc.CodeHashes...), codeHash)
	if len(hashes) > maxCodeHashes {
		hashes = hashes[len(hashes)-maxCodeHashes:]
	}
	acc.CodeHashes = hashes
	if err := SaveLocked(nil, acc); err != nil {
		*acc = prev
		return "", false, err
	}
	return acc.Aid, acc.Aid == "0", nil
}

// CodeLogin serves guest/generate in discord and dev-fake modes: the account whose stored hashes include the hash of code
// gets an access token as GuestLogin gives one. ErrUnknownCode when no account holds it (a deleted account takes its codes).
// ponytail: linear scan over accounts; index the hashes when the account table grows.
func CodeLogin(code string) (string, error) {
	h := []byte(CodeHash(code))
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	for _, acc := range accountList {
		for _, stored := range acc.CodeHashes {
			if subtle.ConstantTimeCompare([]byte(stored), h) == 1 {
				return loginAliasLocked(acc)
			}
		}
	}
	return "", ErrUnknownCode
}
