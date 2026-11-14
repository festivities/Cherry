package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Staff actions on accounts (PLAN P4, P11; slice 8.7 part 2a): player lookup, currency grants and bans. Each change is one
// save, and its audit row is written in the same transaction (saveAuditedLocked): a change that cannot be recorded does not
// happen. Players are addressed by the store's row id, which stays with the account; the avatar id is "0" until the avatar
// is made, so it cannot be the key.

// Ban is a staff ban on an account. The record holds it, so it survives Delete Avatar (ResetIdentityLocked keeps it). By is
// the staff identity, At the time in UTC (RFC 3339).
type Ban struct {
	Reason string `json:"reason"`
	By     string `json:"by"`
	At     string `json:"at"`
}

var (
	ErrNoPlayer      = errors.New("no such player")
	ErrAlreadyBanned = errors.New("the player is already banned")
	ErrNotBanned     = errors.New("the player is not banned")
)

const (
	playerSearchMax = 50      // rows of a search; a search that has more says so
	maxGrant        = 1000000 // the largest grant or take-away in one action
	maxReason       = 200     // characters of a reason (grant, ban)
)

// Player is one account as the staff pages show it: ID is the row id (the key of /players/{id}), the rest is what the
// lookup and the page show. The Ban pointer is never changed in place, only replaced.
type Player struct {
	ID       int64
	Aid      string
	Name     string
	Identity string
	Gems     int64
	Cash     int64
	Ban      *Ban
}

// staffAudit is the audit row of one staff change, written with its save.
type staffAudit struct {
	actor, action, target string
	detail                map[string]any
}

// saveAuditedLocked is SaveLocked with one audit row in the same transaction. Without an attached database it fails with
// ErrNoDatabase before anything is written. Caller holds AccountsMu and restores what it changed on error.
func saveAuditedLocked(audit staffAudit, lines []LedgerLine, accs ...*Account) error {
	return commitLocked(lines, accs, 0, callerSite(), &audit)
}

// playerLocked returns the account with row id, or nil. Caller holds AccountsMu.
// ponytail: linear scan; index the row id when the account table grows.
func playerLocked(id int64) *Account {
	if id < 1 {
		return nil
	}
	for _, acc := range accountList {
		if accountRows[acc].id == id {
			return acc
		}
	}
	return nil
}

// playerOf is the staff view of acc. Caller holds AccountsMu.
func playerOf(acc *Account) Player {
	return Player{ID: accountRows[acc].id, Aid: acc.Aid, Name: acc.Name, Identity: acc.Identity, Gems: acc.Gems, Cash: acc.Cash, Ban: acc.Ban}
}

// playerTarget is the audit target of an account.
func playerTarget(id int64) string { return fmt.Sprintf("player:%d", id) }

// cleanReason trims a staff reason and checks it: 1 to 200 characters.
func cleanReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > maxReason {
		return "", fmt.Errorf("%w: the reason is required, 1 to %d characters", ErrBadInput, maxReason)
	}
	return reason, nil
}

// FindPlayers returns the accounts whose avatar id is q exactly, whose Discord or dev identity is q exactly, or whose name
// contains q (any case). At most 50 rows; more reports that the search has more. An empty q finds nothing.
func FindPlayers(q string) (rows []Player, more bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, false
	}
	needle := strings.ToLower(q)
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	rows = []Player{}
	for _, acc := range accountList {
		if acc.Aid != q && acc.Identity != q && !strings.Contains(strings.ToLower(acc.Name), needle) {
			continue
		}
		if len(rows) == playerSearchMax {
			return rows, true
		}
		rows = append(rows, playerOf(acc))
	}
	return rows, false
}

// PlayerByID returns the account with row id.
func PlayerByID(id int64) (Player, bool) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := playerLocked(id)
	if acc == nil {
		return Player{}, false
	}
	return playerOf(acc), true
}

// PlayerLedger returns the newest limit ledger lines of the account with row id, newest first. The ledger is keyed by the
// avatar id, so an account with aid "0" has none of its own: it gets an empty list. ErrNoPlayer for an unknown id.
func PlayerLedger(id int64, limit int) ([]LedgerLine, error) {
	AccountsMu.Lock()
	acc := playerLocked(id)
	var aid string
	if acc != nil {
		aid = acc.Aid
	}
	d := database.Load()
	AccountsMu.Unlock()
	if acc == nil {
		return nil, ErrNoPlayer
	}
	lines := []LedgerLine{}
	if d == nil || aid == "0" {
		return lines, nil
	}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(`SELECT time, aid, currency, delta, balance, reason FROM ledger WHERE aid = ? ORDER BY id DESC LIMIT ?`, aid, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l LedgerLine
			if err := rows.Scan(&l.Time, &l.Aid, &l.Currency, &l.Delta, &l.Balance, &l.Reason); err != nil {
				return err
			}
			lines = append(lines, l)
		}
		return rows.Err()
	})
	return lines, err
}

// GrantPlayer changes the gems or cash balance of the account with row id by delta (a take-away is negative). delta is
// not zero and within ±maxGrant; the balance may not go below 0 or above MaxBalance; reason is 1 to 200 characters. The
// ledger line's reason is "staff:<actor>:<reason>". The balance, the line and the audit row are one save. On error nothing
// changes.
func GrantPlayer(id int64, currency string, delta int64, reason, actor string) error {
	reason, err := cleanReason(reason)
	if err != nil {
		return err
	}
	var d LedgerDeltas
	switch currency {
	case "gems":
		d.Gems = delta
	case "cash":
		d.Cash = delta
	default:
		return fmt.Errorf("%w: the currency is gems or cash", ErrBadInput)
	}
	if delta == 0 || delta > maxGrant || delta < -maxGrant {
		return fmt.Errorf("%w: the amount is a whole number from -%d to %d, not zero", ErrBadInput, maxGrant, maxGrant)
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := playerLocked(id)
	if acc == nil {
		return ErrNoPlayer
	}
	prev := *acc
	lines, err := SpendLocked(acc, d, "staff:"+actor+":"+reason)
	if err != nil {
		var be *BalanceError
		if errors.As(err, &be) {
			return fmt.Errorf("%w: the balance cannot go below 0 or above %d", ErrBadInput, MaxBalance)
		}
		return err
	}
	balance := acc.Gems
	if currency == "cash" {
		balance = acc.Cash
	}
	audit := staffAudit{actor, "grant", playerTarget(id), map[string]any{
		"aid": acc.Aid, "currency": currency, "delta": delta, "balance": balance, "reason": reason,
	}}
	if err := saveAuditedLocked(audit, lines, acc); err != nil {
		*acc = prev
		return err
	}
	return nil
}

// BanPlayer bans the account with row id. Its record gets the ban; every token of the account stops resolving (its own and
// its aliases, in memory and, in the same save, in the token rows); and its session key is replaced, so the gateway logins
// that check it fail. A session key is replaced, not emptied: after an unban the sign-in hands out the key again, and an
// empty one would be a broken login. On error nothing changes.
func BanPlayer(id int64, reason, actor string) error {
	reason, err := cleanReason(reason)
	if err != nil {
		return err
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := playerLocked(id)
	if acc == nil {
		return ErrNoPlayer
	}
	if acc.Ban != nil {
		return ErrAlreadyBanned
	}
	rs := accountRows[acc]
	prev, prevRS, prevPending := *acc, *rs, slices.Clone(pendingTokens)
	prevHashes := AccountTokenHashesLocked(acc)
	for _, h := range prevHashes {
		delete(accountsByHash, h)
	}
	pendingTokens = slices.DeleteFunc(pendingTokens, func(p pendingToken) bool { return p.acc == acc })
	acc.Ban = &Ban{Reason: reason, By: actor, At: time.Now().UTC().Format(time.RFC3339)}
	acc.SessionKey = RandomToken(32)
	rs.revoke = true // the save deletes every token row of the account first (ResetIdentityLocked does the same)
	audit := staffAudit{actor, "ban", playerTarget(id), map[string]any{"aid": acc.Aid, "identity": acc.Identity, "reason": reason}}
	if err := saveAuditedLocked(audit, nil, acc); err != nil {
		*acc, *rs, pendingTokens = prev, prevRS, prevPending
		for _, h := range prevHashes {
			accountsByHash[h] = acc
		}
		return err
	}
	return nil
}

// UnbanPlayer lifts the ban of the account with row id. The account's tokens stay revoked: the player signs in again
// (exchange, guest/generate), which mints a new alias as it does for any login.
func UnbanPlayer(id int64, actor string) error {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := playerLocked(id)
	if acc == nil {
		return ErrNoPlayer
	}
	if acc.Ban == nil {
		return ErrNotBanned
	}
	prev := *acc
	acc.Ban = nil
	audit := staffAudit{actor, "unban", playerTarget(id), map[string]any{"aid": acc.Aid, "identity": acc.Identity}}
	if err := saveAuditedLocked(audit, nil, acc); err != nil {
		*acc = prev
		return err
	}
	return nil
}
