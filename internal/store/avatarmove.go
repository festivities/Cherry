package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"cherry/internal/config"
	"cherry/internal/httpx"
)

// Moving a live avatar to another Discord identity (2026-11-21, user: a player who comes back on a new account gets the old avatar
// assigned to it). Identity modes only; the staff panel offers it to the owner only. The move is one save: the source row takes the
// target identity and ends every login (its tokens, its aliases and its code hashes; the session key rotates), and an empty row that
// held the target identity goes in the same transaction. The row carries its once-only flags (welcome gift, butler hire, boss book)
// with the avatar; nothing of the target's empty row comes over. The source identity has no row afterwards, so its next Discord
// sign-in creates one through the normal exchange path and the sign-up gate.

// ErrNoRecord is an unknown deleted avatar. RefusedError is a move or restore the store declines: it names the reason, which the
// staff panel shows as is.
var ErrNoRecord = errors.New("no such deleted avatar")

// RefusedError is an action the store declines, with the reason the staff panel shows (409).
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return e.Reason }

func refused(format string, args ...any) error {
	return &RefusedError{Reason: fmt.Sprintf(format, args...)}
}

// IsRefused reports whether err is a RefusedError (the action was declined, nothing changed).
func IsRefused(err error) bool {
	var r *RefusedError
	return errors.As(err, &r)
}

// ValidIdentityForMode reports whether id has the shape of an identity of the running mode: "discord:<digits>" in discord mode,
// "dev-<name>" in dev-fake mode.
func ValidIdentityForMode(id string) bool {
	if config.Current().AuthMode == config.AuthDevFake {
		name, ok := strings.CutPrefix(id, "dev-")
		return ok && config.ValidDevName(name)
	}
	digits, ok := strings.CutPrefix(id, "discord:")
	return ok && digits != "" && httpx.IsAllDigits(digits)
}

// MovePlan is what a move would do, for the review page. Refusals come with it (the error of PlanMove), so the page shows what
// exists for the target next to the reason.
type MovePlan struct {
	Player      Player // the source account (row id, avatar id, name, identity, ban)
	To          string // the target identity
	TargetRowID int64  // the accounts row of To; 0 = none
	TargetAid   string // the avatar id To's row holds ("0" = none); "" when there is no row
	TargetEmpty bool   // To's row holds no avatar: the move removes it
}

// planMoveLocked checks a move and returns the source and the target row (nil when To has none). Caller holds AccountsMu. The
// refusals are RefusedError; ErrNoPlayer when id is no account.
func planMoveLocked(id int64, to string) (src, tgt *Account, err error) {
	if !config.IdentityMode() {
		return nil, nil, refused("Avatar moves run in discord and dev-fake modes only.")
	}
	src = playerLocked(id)
	if src == nil {
		return nil, nil, ErrNoPlayer
	}
	switch {
	case src.Aid == "0":
		return src, nil, refused("The account has no avatar to move.")
	case src.Identity == "":
		return src, nil, refused("The account has no Discord identity to move from.")
	case !ValidIdentityForMode(to):
		return src, nil, refused("The target is not an identity of this server's kind.")
	case to == src.Identity:
		return src, nil, refused("The avatar already belongs to that identity.")
	case src.Ban != nil:
		return src, nil, refused("The account is banned: unban it before the move.")
	}
	tgt = IdentityAccountLocked(to)
	switch {
	case tgt != nil && tgt.Ban != nil:
		return src, tgt, refused("The target identity is banned.")
	case tgt != nil && tgt.Aid != "0":
		return src, tgt, refused("The target identity already has avatar %s.", tgt.Aid)
	}
	return src, tgt, nil
}

// PlanMove returns what moving the account with row id to the identity to would do. The error is a RefusedError when the move is
// refused (the plan is still filled in), ErrNoPlayer for an unknown row.
func PlanMove(id int64, to string) (MovePlan, error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	src, tgt, err := planMoveLocked(id, to)
	if src == nil {
		return MovePlan{}, err
	}
	plan := MovePlan{Player: playerOf(src), To: to}
	if tgt != nil {
		plan.TargetRowID, plan.TargetAid, plan.TargetEmpty = accountRows[tgt].id, tgt.Aid, true
	}
	return plan, err
}

// MoveAvatar moves the live avatar of the account with row id to the identity to, in ONE save with the audit row
// (avatar-move: the avatar id and both identities). Every login of the moved account ends, then OnRevoke closes its live
// connections. Refusals change nothing.
func MoveAvatar(id int64, to, actor string) error {
	aid, err := moveAvatar(id, to, actor)
	if err == nil && OnRevoke != nil {
		OnRevoke(aid)
	}
	return err
}

// moveAvatar is the save of MoveAvatar; it returns the moved avatar id. Memory changes first; on a failed save they are undone.
func moveAvatar(id int64, to, actor string) (string, error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	src, tgt, err := planMoveLocked(id, to)
	if err != nil {
		return "", err
	}
	aid, from := src.Aid, src.Identity
	srcRS := accountRows[src]
	prev, prevRS := *src, *srcRS
	prevPending, prevList, prevLatest := slices.Clone(pendingTokens), slices.Clone(accountList), LatestAcc
	prevHashes := AccountTokenHashesLocked(src)
	for _, h := range prevHashes {
		delete(accountsByHash, h)
	}
	pendingTokens = slices.DeleteFunc(pendingTokens, func(p pendingToken) bool { return p.acc == src })
	rotateSessionKey(src) // every login of the moved avatar ends: tokens, aliases and codes below
	src.CodeHashes = nil
	srcRS.revoke = true // the save deletes the account's token rows before it writes the (none) pending ones
	src.Identity = to

	var removeID int64
	var tgtRS *rowState
	var tgtHashes []string
	if tgt != nil {
		// The empty row goes first in the transaction (commitLocked): the identity is unique in the table.
		tgtRS = accountRows[tgt]
		removeID = tgtRS.id
		tgtHashes = forgetAccountLocked(tgt)
		if LatestAcc == tgt {
			LatestAcc = highestAidLocked()
		}
	}
	audit := staffAudit{actor, "avatar-move", playerTarget(id), map[string]any{"aid": aid, "from": from, "to": to}}
	if err := commitLocked(nil, []*Account{src}, removeID, callerSite(), &audit, nil); err != nil {
		*src, *srcRS = prev, prevRS
		pendingTokens = prevPending
		for _, h := range prevHashes {
			accountsByHash[h] = src
		}
		if tgt != nil {
			accountList = prevList
			accountRows[tgt] = tgtRS
			for _, h := range tgtHashes {
				accountsByHash[h] = tgt
			}
		}
		LatestAcc = prevLatest
		return "", err
	}
	return aid, nil
}
