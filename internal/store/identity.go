package store

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"slices"
)

// Provider identities (CONTRACT §3, slice 8.6a). An identity ("discord:<id>" or "dev-<name>") is stored on its account
// with the SHA-256 hashes of the uniqueCodes minted for it; a code itself is never stored. The fields are columns of the
// account row (identity, the data JSON).

// maxCodeHashes bounds the codes one identity holds; minting a new one drops the oldest.
const maxCodeHashes = 10

// ErrUnknownCode means no account holds the hash of the presented uniqueCode.
var ErrUnknownCode = errors.New("unknown unique code")

// ErrSignupClosed means the identity has no account and auth.signup does not let it make one (MintIdentityCode).
var ErrSignupClosed = errors.New("sign-up closed for this identity")

// ErrBanned means the account is banned: no uniqueCode is minted for it and no token is given (CONTRACT §3).
var ErrBanned = errors.New("account is banned")

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

// IdentityBanned reports whether a provider identity's account is banned (the callback refuses it before any ticket).
func IdentityBanned(identity string) bool {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := IdentityAccountLocked(identity)
	return acc != nil && acc.Ban != nil
}

// IdentityExists reports whether a provider identity already has an account (the sign-up gate at callback time).
func IdentityExists(identity string) bool {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	return IdentityAccountLocked(identity) != nil
}

// MintIdentityCode is the exchange step: the identity's account, created with aid "0" when it has none, gets the hash of a
// new uniqueCode (the oldest hash is dropped beyond maxCodeHashes). It reports the account's aid and whether the account
// has no avatar yet. An identity with no account is created only when mayCreate says auth.signup lets it (else
// ErrSignupClosed, nothing written); the check runs under the lock, so two exchanges cannot both create one. A failed
// save leaves the account as it was.
func MintIdentityCode(identity, codeHash string, mayCreate bool) (aid string, isNew bool, err error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := IdentityAccountLocked(identity)
	if acc != nil && acc.Ban != nil {
		return "", false, ErrBanned
	}
	if acc == nil {
		if !mayCreate {
			return "", false, ErrSignupClosed
		}
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
// The newest sign-in wins, as on LINE's servers (client string 10066: "The same account has been used on another device"):
// it ends every earlier login of the account and every other code (endLoginsLocked), then OnTakeover tells the older
// device and closes its live connections. Only a fresh Discord sign-in (exchange) brings an older device back.
// ponytail: linear scan over accounts; index the hashes when the account table grows.
func CodeLogin(code string) (string, error) {
	h := CodeHash(code)
	AccountsMu.Lock()
	var acc *Account
	for _, a := range accountList {
		for _, stored := range a.CodeHashes {
			if subtle.ConstantTimeCompare([]byte(stored), []byte(h)) == 1 {
				acc = a
			}
		}
	}
	if acc == nil {
		AccountsMu.Unlock()
		return "", ErrUnknownCode
	}
	token, err := endLoginsLocked(acc, h)
	aid := acc.Aid
	AccountsMu.Unlock()
	if err == nil && OnTakeover != nil {
		OnTakeover(aid)
	}
	return token, err
}

// OnTakeover is called after a sign-in ended the account's earlier logins, with its avatar id ("0" when it has none).
// cmd/cherry sets it to lpn.KickAid: the older device is told and its live connections close. It runs after AccountsMu
// is released. Nil does nothing.
var OnTakeover func(aid string)

// Logout serves PUT sessionout for the account a clear token resolves to: every login and every code of the account ends
// (endLoginsLocked), then OnRevoke closes its live connections, so the player signs in with Discord again. It reports
// whether an account was found. A failed save leaves the account as it was and returns the error.
func Logout(token string) (bool, error) {
	AccountsMu.Lock()
	acc := AccountByTokenLocked(token)
	if acc == nil {
		AccountsMu.Unlock()
		return false, nil
	}
	_, err := endLoginsLocked(acc, "")
	aid := acc.Aid
	AccountsMu.Unlock()
	if err == nil && OnRevoke != nil {
		OnRevoke(aid)
	}
	return true, err
}

// endLoginsLocked ends every login of the account: every token stops resolving (in memory and, in the same save, the
// token rows) and the session key rotates, so an older device's checkSession, API calls, sendInit and gateway logins all
// fail. The stored codes go too, except keepCode (a code hash): a sign-in keeps the code it used, a logout keeps none,
// so a device that kept its code needs a new Discord sign-in. With keepCode the account then gets a new alias (returned)
// and becomes the latest account, in the same save. A banned account gets ErrBanned. On error acc is as it was. Caller
// holds AccountsMu.
func endLoginsLocked(acc *Account, keepCode string) (string, error) {
	if acc.Ban != nil {
		return "", ErrBanned
	}
	rs := accountRows[acc]
	prev, prevRS, prevPending, prevLatest := *acc, *rs, slices.Clone(pendingTokens), LatestAcc
	prevHashes := AccountTokenHashesLocked(acc)
	for _, h := range prevHashes {
		delete(accountsByHash, h)
	}
	pendingTokens = slices.DeleteFunc(pendingTokens, func(p pendingToken) bool { return p.acc == acc })
	rotateSessionKey(acc)
	rs.revoke = true // the save deletes the account's token rows before it writes a new alias (as a ban does)
	acc.CodeHashes = nil
	var token string
	if keepCode != "" {
		acc.CodeHashes = []string{keepCode}
		token = AddAliasLocked(acc)
		LatestAcc = acc
	}
	if err := SaveLocked(nil, acc); err != nil {
		if token != "" {
			dropTokenLocked(CodeHash(token))
		}
		*acc, *rs, pendingTokens, LatestAcc = prev, prevRS, prevPending, prevLatest
		for _, h := range prevHashes {
			accountsByHash[h] = acc
		}
		return "", err
	}
	return token, nil
}
