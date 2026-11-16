package economy

import (
	"fmt"
	"time"

	"cherry/internal/store"
)

// Staff mail (PLAN Roadmap 8, staff panel phase 5). The loopback tool (/admin/mail) and the staff panel send the same mail: one
// attachment (gems, cash, face tickets or one sellable item: the postbox has no other kind) and the text. A send to one player
// goes to that account's mailbox; a send to every account is one save, and the staff panel's send is kept under its send id
// (store.MailSend), so the same send id cannot send twice.

// MailOrder is one staff mail. Amount is for the currency types, Item for item; Message is at most 500 bytes; Days is the
// expiry (0 = rewards.json's default, at most 3650).
type MailOrder struct {
	Type    string
	Amount  int64
	Item    string
	Message string
	Days    int
}

// Check validates o the way the loopback tool always has: a known attachment, an amount within rewards.json's maximum or an item
// code the catalog sells, a message of at most 500 bytes and an expiry of 0 to 3650 days. A refusal is a store.ErrBadInput.
func (o MailOrder) Check() error {
	switch {
	case !validMailType(o.Type):
		return fmt.Errorf("%w: the attachment is gems, cash, faceTickets or item", store.ErrBadInput)
	case o.Type == store.MailTypeItem && !validMailPayload(o.Type, o.Amount, o.Item):
		return fmt.Errorf("%w: the item code is not a sellable item in the catalog", store.ErrBadInput)
	case o.Type != store.MailTypeItem && !validMailPayload(o.Type, o.Amount, o.Item):
		return fmt.Errorf("%w: the amount is a whole number from 1 to %d", store.ErrBadInput, ActiveRewards().Mail.MaxAmount)
	case len(o.Message) > 500:
		return fmt.Errorf("%w: the message is at most 500 bytes", store.ErrBadInput)
	case o.Days < 0 || o.Days > 3650:
		return fmt.Errorf("%w: the expiry is 0 (the default) to 3650 days", store.ErrBadInput)
	}
	return nil
}

// MailExpiry is the expiry in days a mail of days gets: days, or rewards.json's default for 0.
func MailExpiry(days int) int {
	if days == 0 {
		return ActiveRewards().Mail.DefaultDays
	}
	return days
}

// days is the expiry in days a send of o gets.
func (o MailOrder) days() int { return MailExpiry(o.Days) }

// amount and item are what the attachment carries: an item order has no amount, a currency order no item.
func (o MailOrder) amount() int64 {
	if o.Type == store.MailTypeItem {
		return 0
	}
	return o.Amount
}

func (o MailOrder) item() string {
	if o.Type == store.MailTypeItem {
		return o.Item
	}
	return ""
}

// row is the mailbox row of o (addMailLocked gives it its sequence number, its times and its expiry).
func (o MailOrder) row() store.MailRow {
	return store.MailRow{Type: o.Type, Amount: o.amount(), Item: o.item(), Message: o.Message, Sender: MailSender()}
}

// DeliverMailLocked appends row to the mailbox of each account in targets, expiring in days from now. A full mailbox gets nothing.
// It returns how many got it. The caller holds store.AccountsMu, keeps a copy of the accounts (snapshotAccounts) and restores
// them when its save fails.
func DeliverMailLocked(targets []*store.Account, row store.MailRow, days int, now time.Time) int {
	sent := 0
	for _, a := range targets {
		if addMailLocked(a, row, days, now) {
			sent++
		}
	}
	return sent
}

// snapshotAccounts copies each account, for a restore after a failed save.
func snapshotAccounts(targets []*store.Account) []store.Account {
	prevs := make([]store.Account, len(targets))
	for i, a := range targets {
		prevs[i] = *a
	}
	return prevs
}

// restoreAccounts puts back the copies snapshotAccounts made.
func restoreAccounts(targets []*store.Account, prevs []store.Account) {
	for i, a := range targets {
		*a = prevs[i]
	}
}

// MailRecipients is how many accounts a send to player (a row id) or, with player 0, to every account reaches. ok is false for an
// unknown player.
func MailRecipients(player int64) (n int, ok bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	if player > 0 {
		return 1, store.PlayerAccountLocked(player) != nil
	}
	return len(store.AllAccountsLocked()), true
}

// StaffMail is one staff send: its order, its target (the player's row id, or 0 for every account), the send id the form carried,
// the reason and the actor, and Expect, the recipients the reviewer was shown.
type StaffMail struct {
	SendID        string
	Player        int64
	Order         MailOrder
	Expect        int
	Actor, Reason string
}

// SendStaffMail sends m once. A send id already kept returns that send with sent false and changes nothing. Otherwise the mail
// goes to the target's mailboxes (a full mailbox gets none), and the mailboxes, the send row and its audit row are one save: a
// save that fails leaves every account as it was. A target that no longer has m.Expect accounts is refused, so the send is the
// one the reviewer saw. The reason is trimmed and checked (store.CleanStaffReason); the audit action is "mail".
func SendStaffMail(m StaffMail, now time.Time) (rec store.MailSend, sent bool, err error) {
	if err := m.Order.Check(); err != nil {
		return store.MailSend{}, false, err
	}
	reason, err := store.CleanStaffReason(m.Reason)
	if err != nil {
		return store.MailSend{}, false, err
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	if kept, found, err := store.MailSendLocked(m.SendID); err != nil || found {
		return kept, false, err
	}
	var targets []*store.Account
	target := store.AllPlayersTarget
	if m.Player > 0 {
		acc := store.PlayerAccountLocked(m.Player)
		if acc == nil {
			return store.MailSend{}, false, store.ErrNoPlayer
		}
		targets = []*store.Account{acc}
		target = store.PlayerTarget(m.Player)
	} else {
		targets = store.AllAccountsLocked()
	}
	if len(targets) != m.Expect {
		return store.MailSend{}, false, fmt.Errorf("%w: %d accounts now, %d when the mail was reviewed: review it again",
			store.ErrBadInput, len(targets), m.Expect)
	}
	prevs := snapshotAccounts(targets)
	sentTo := DeliverMailLocked(targets, m.Order.row(), m.Order.days(), now)
	if sentTo == 0 {
		return store.MailSend{}, false, fmt.Errorf("%w: every mailbox is full", store.ErrBadInput)
	}
	rec = store.MailSend{
		SendID: m.SendID, Created: now.UTC().Format(time.RFC3339), Actor: m.Actor, Target: target,
		Recipients: int64(sentTo), Skipped: int64(len(targets) - sentTo), Kind: m.Order.Type, Amount: m.Order.amount(),
		Item: m.Order.item(), Message: m.Order.Message, ExpiresDays: int64(m.Order.days()), Reason: reason,
	}
	if err := store.SaveMailSendLocked(rec, targets...); err != nil {
		restoreAccounts(targets, prevs)
		return store.MailSend{}, false, err
	}
	return rec, true, nil
}
