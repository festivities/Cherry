package economy

import (
	"fmt"
	"time"

	"cherry/internal/store"
)

// Every player's balances (owner only, staff panel): one change sets, adds to or takes away chosen currencies on every account in one
// save. Set makes each chosen balance the value (0 is a reset); add gives each chosen balance the value, stopping at store.MaxBalance;
// take removes the value from each chosen balance, stopping at 0. Each account's change goes through GrantLocked, so Gems, Cash, face
// tickets and Smiles get ledger lines that carry the staff reason, as a single grant does. Boss Coins, ride points and extra hearts
// have no ledger in a single grant either: they show in the audit row.

// balancesAction is the audit action of a change to every account; its target is store.AllPlayersTarget, as the mail to every
// account has.
const balancesAction = "grant-all"

// balanceCurrency is one currency a change can set or add to: its name (the key of StaffBalances.Values, the form's and the audit
// row's), its balance and the grant field that moves it.
type balanceCurrency struct {
	name    string
	balance func(*store.Account) int64
	delta   func(*StaffGrant) *int64
}

// balanceCurrencies are the currencies in the order the form and the grant use.
var balanceCurrencies = []balanceCurrency{
	{"gems", func(a *store.Account) int64 { return a.Gems }, func(g *StaffGrant) *int64 { return &g.Gems }},
	{"cash", func(a *store.Account) int64 { return a.Cash }, func(g *StaffGrant) *int64 { return &g.Cash }},
	{"faceTickets", func(a *store.Account) int64 { return a.FaceTickets }, func(g *StaffGrant) *int64 { return &g.FaceTickets }},
	{"smiles", func(a *store.Account) int64 { return a.Smiles }, func(g *StaffGrant) *int64 { return &g.Smiles }},
	{"bossCoins", func(a *store.Account) int64 { return a.BossCoins }, func(g *StaffGrant) *int64 { return &g.BossCoins }},
	{"ridePoints", ridePointsLocked, func(g *StaffGrant) *int64 { return &g.RidePoints }},
	{"extraHearts", heartExtra, func(g *StaffGrant) *int64 { return &g.ExtraHearts }},
}

// StaffBalances is one change to every account. Set: each chosen balance becomes its value (0 to store.MaxBalance). Add: each chosen
// balance gains its value (1 to store.MaxGrant), up to store.MaxBalance. Take: each chosen balance loses its value (1 to
// store.MaxGrant), down to 0. Set, Take and neither (add) are exclusive. Values is keyed by currency name ("gems", "cash",
// "faceTickets", "smiles", "bossCoins", "ridePoints", "extraHearts"); a currency not in it is untouched, and at least one must be.
// Expect is the accounts the reviewer was shown; SendID is the send the change is kept under.
type StaffBalances struct {
	SendID        string
	Set, Take     bool
	Values        map[string]int64
	Expect        int
	Actor, Reason string
}

// check validates b and returns the reason as it is kept (trimmed, at most 200 characters, may be empty). Refusals are store.ErrBadInput.
func (b StaffBalances) check() (string, error) {
	if b.Set && b.Take {
		return "", fmt.Errorf("%w: the change is set, take or add", store.ErrBadInput)
	}
	if len(b.Values) == 0 {
		return "", fmt.Errorf("%w: choose at least one balance", store.ErrBadInput)
	}
	known := 0
	for _, c := range balanceCurrencies {
		v, ok := b.Values[c.name]
		if !ok {
			continue
		}
		known++
		if b.Set && (v < 0 || v > store.MaxBalance) {
			return "", fmt.Errorf("%w: a balance is set to a whole number from 0 to %d", store.ErrBadInput, store.MaxBalance)
		}
		if !b.Set && (v < 1 || v > store.MaxGrant) {
			return "", fmt.Errorf("%w: the amount is a whole number from 1 to %d", store.ErrBadInput, store.MaxGrant)
		}
	}
	if known != len(b.Values) {
		return "", fmt.Errorf("%w: unknown balance", store.ErrBadInput)
	}
	return store.CleanStaffReason(b.Reason)
}

// balanceGrant is the change acc takes under b: each chosen currency moves to its value (set), or gains its value up to
// store.MaxBalance (add; a balance already above the cap gains nothing), or loses its value down to 0 (take).
func balanceGrant(acc *store.Account, b StaffBalances) StaffGrant {
	var g StaffGrant
	for _, c := range balanceCurrencies {
		v, ok := b.Values[c.name]
		if !ok {
			continue
		}
		cur := c.balance(acc)
		d := v - cur
		switch {
		case b.Take:
			d = -min(v, cur)
		case !b.Set:
			d = max(0, min(v, store.MaxBalance-cur))
		}
		*c.delta(&g) = d
	}
	return g
}

// changesAnything reports whether g moves any currency.
func changesAnything(g StaffGrant) bool {
	for _, c := range balanceCurrencies {
		if *c.delta(&g) != 0 {
			return true
		}
	}
	return false
}

// PreviewStaffBalances is what a change under b would do, without making it: the accounts it reaches (every account, as the mail to
// every account does) and how many of them would change. A change that would change no account is refused (store.ErrBadInput).
func PreviewStaffBalances(b StaffBalances) (accounts, changing int, err error) {
	if _, err := b.check(); err != nil {
		return 0, 0, err
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	targets := store.AllAccountsLocked()
	for _, acc := range targets {
		if changesAnything(balanceGrant(acc, b)) {
			changing++
		}
	}
	if changing == 0 {
		return 0, 0, fmt.Errorf("%w: no player's balance would change", store.ErrBadInput)
	}
	return len(targets), changing, nil
}

// StaffBalancesAll applies b to every account once. A send id already kept returns applied false and changes nothing. Otherwise each
// account gets its change (balanceGrant through GrantLocked, with the reason "staff:<actor>:<reason>"), and the changed accounts,
// their ledger lines and the audit row ("grant-all", all-players, with the send id, the mode, the values, the accounts reached and
// the accounts changed, and the reason) are one save. A failure restores every account. A count that is not b.Expect is refused,
// so the change is the one the reviewer saw. It returns how many accounts changed.
func StaffBalancesAll(b StaffBalances, now time.Time) (changed int, applied bool, err error) {
	reason, err := b.check()
	if err != nil {
		return 0, false, err
	}
	if b.SendID == "" {
		return 0, false, fmt.Errorf("%w: the send id is required", store.ErrBadInput)
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	if done, err := store.AuditSendLocked(balancesAction, store.AllPlayersTarget, b.SendID); err != nil || done {
		return 0, false, err
	}
	targets := store.AllAccountsLocked()
	if len(targets) != b.Expect {
		return 0, false, fmt.Errorf("%w: %d accounts now, %d when the change was reviewed: review it again",
			store.ErrBadInput, len(targets), b.Expect)
	}
	prevs := snapshotAccounts(targets)
	var lines []store.LedgerLine
	var moved []*store.Account
	for _, acc := range targets {
		g := balanceGrant(acc, b)
		if !changesAnything(g) {
			continue
		}
		l, err := GrantLocked(acc, g, "staff:"+b.Actor+":"+reason, now)
		if err != nil {
			restoreAccounts(targets, prevs)
			return 0, false, err
		}
		lines = append(lines, l...)
		moved = append(moved, acc)
	}
	if len(moved) == 0 {
		return 0, false, fmt.Errorf("%w: no player's balance would change", store.ErrBadInput)
	}
	mode := "add"
	switch {
	case b.Set:
		mode = "set"
	case b.Take:
		mode = "take"
	}
	detail := map[string]any{"send": b.SendID, "mode": mode, "values": b.Values, "accounts": len(targets), "changed": len(moved),
		"reason": reason}
	if err := store.SaveAuditedLocked(b.Actor, balancesAction, store.AllPlayersTarget, detail, lines, moved...); err != nil {
		restoreAccounts(targets, prevs)
		return 0, false, err
	}
	return len(moved), true, nil
}
