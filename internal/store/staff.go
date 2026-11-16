package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
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
	ErrNoPlayer       = errors.New("no such player")
	ErrAlreadyBanned  = errors.New("the player is already banned")
	ErrNotBanned      = errors.New("the player is not banned")
	ErrNoItem         = errors.New("the player has no such diary post, guestbook entry or comment")
	ErrNicknameCopies = errors.New("the nickname is saved, but its diary and guestbook copies are not; repeat the rename to write them")
)

// OnBan is called once a ban is saved, with the banned account's avatar id ("0" when it has none). cmd/cherry sets it to
// lpn.CloseBannedAid, which ends the account's live connections. It runs after AccountsMu is released. Nil does nothing.
var OnBan func(aid string)

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

// Signup is an account as the dashboard's newest-accounts list shows it: the row id (the player page's key), the avatar id
// ("0" until the avatar is made), the nickname and the creation time (RFC 3339, UTC).
type Signup struct {
	ID      int64
	Aid     string
	Name    string
	Created string
}

// RecentSignups returns the newest limit accounts, newest first. The creation time is kept in the database, so this reads it
// there. ErrNoDatabase without one.
func RecentSignups(limit int) ([]Signup, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	out := []Signup{}
	err = d.View(func(q Querier) error {
		rows, err := q.Query("SELECT id, aid, name, created FROM accounts ORDER BY id DESC LIMIT ?", limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s Signup
			if err := rows.Scan(&s.ID, &s.Aid, &s.Name, &s.Created); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
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
// empty one would be a broken login. Then OnBan ends the account's live connections. On error nothing changes.
func BanPlayer(id int64, reason, actor string) error {
	aid, err := banPlayer(id, reason, actor)
	if err == nil && OnBan != nil {
		OnBan(aid)
	}
	return err
}

// banPlayer is the save of BanPlayer; it returns the banned avatar id.
func banPlayer(id int64, reason, actor string) (string, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return "", err
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := playerLocked(id)
	if acc == nil {
		return "", ErrNoPlayer
	}
	if acc.Ban != nil {
		return "", ErrAlreadyBanned
	}
	rs := accountRows[acc]
	prev, prevRS, prevPending := *acc, *rs, slices.Clone(pendingTokens)
	prevHashes := AccountTokenHashesLocked(acc)
	for _, h := range prevHashes {
		delete(accountsByHash, h)
	}
	pendingTokens = slices.DeleteFunc(pendingTokens, func(p pendingToken) bool { return p.acc == acc })
	acc.Ban = &Ban{Reason: reason, By: actor, At: time.Now().UTC().Format(time.RFC3339)}
	rotateSessionKey(acc) // the session key ends with the ban (epoch bump; a legacy key stops too)
	rs.revoke = true      // the save deletes every token row of the account first (ResetIdentityLocked does the same)
	audit := staffAudit{actor, "ban", playerTarget(id), map[string]any{"aid": acc.Aid, "identity": acc.Identity, "reason": reason}}
	if err := saveAuditedLocked(audit, nil, acc); err != nil {
		*acc, *rs, pendingTokens = prev, prevRS, prevPending
		for _, h := range prevHashes {
			accountsByHash[h] = acc
		}
		return "", err
	}
	return acc.Aid, nil
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

// PlayerWriting is what the player page lists of the player's own writing, newest first, at most a page of each: the diary
// posts the player wrote, the guestbook entries the player wrote in other players' guestbooks, and the diary comments the
// player wrote.
type PlayerWriting struct {
	Posts     []DiaryPost
	Guestbook []SavedGuestbookEntry
	Comments  []DiaryComment
}

// PlayerWritingOf returns the newest limit posts, entries and comments the player with row id wrote. An account with aid
// "0" has none. ErrNoPlayer for an unknown id.
func PlayerWritingOf(id int64, limit int) (PlayerWriting, error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := playerLocked(id)
	if acc == nil {
		return PlayerWriting{}, ErrNoPlayer
	}
	w := PlayerWriting{Posts: []DiaryPost{}, Guestbook: []SavedGuestbookEntry{}, Comments: []DiaryComment{}}
	if acc.Aid == "0" {
		return w, nil
	}
	SocialMu.Lock()
	defer SocialMu.Unlock()
	// Posts and entries are kept newest first (a new one goes to the front), comments oldest first (to the back).
	for _, p := range DiaryPosts {
		if len(w.Posts) < limit && p.AvatarID == acc.Aid {
			w.Posts = append(w.Posts, p)
		}
	}
	for _, e := range GuestbookEntries {
		if len(w.Guestbook) < limit && e.GuestAvtNo == acc.Aid {
			w.Guestbook = append(w.Guestbook, e)
		}
	}
	for i := len(DiaryComments) - 1; i >= 0 && len(w.Comments) < limit; i-- {
		if c := DiaryComments[i]; c.CommentAvatarID == acc.Aid {
			w.Comments = append(w.Comments, c)
		}
	}
	return w, nil
}

// nicknameMaxRunes is the creation limit (NaCreateLayer::CheckValidNickName 0x1828498 rejects more than 20 characters,
// error 48). ValidNickname is the one nickname rule, for the in-game edit (web/profileedit.go) and the staff rename: not
// empty and not only spaces (IsEmptyNickName 0x18282d8 looks at spaces only), at most 20 characters, valid UTF-8 with no
// control characters.
const nicknameMaxRunes = 20

func ValidNickname(s string) bool {
	if strings.Trim(s, " ") == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > nicknameMaxRunes {
		return false
	}
	return !slices.ContainsFunc([]rune(s), unicode.IsControl)
}

// RenamePlayer sets the nickname of the player with row id to name, which must pass the in-game nickname rule. The account
// row and its audit row (old and new name, reason) are one save. Then the diary posts and guestbook entries the player wrote
// take the new name, as after an in-game rename; that second save can fail alone, and then the error is ErrNicknameCopies
// (repeating the rename writes the copies). Any other error changes nothing.
func RenamePlayer(id int64, name, reason, actor string) error {
	reason, err := cleanReason(reason)
	if err != nil {
		return err
	}
	if !ValidNickname(name) {
		return fmt.Errorf("%w: the nickname is 1 to %d characters, not only spaces, with no control characters", ErrBadInput, nicknameMaxRunes)
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := playerLocked(id)
	if acc == nil {
		return ErrNoPlayer
	}
	prev, old := *acc, acc.Name
	acc.Name = name
	audit := staffAudit{actor, "rename", playerTarget(id), map[string]any{"aid": acc.Aid, "old": old, "new": name, "reason": reason}}
	if err := saveAuditedLocked(audit, nil, acc); err != nil {
		*acc = prev
		return err
	}
	if err := renameCopiesLocked(acc.Aid, name); err != nil {
		return ErrNicknameCopies
	}
	return nil
}

// renameCopiesLocked writes name into the diary posts and guestbook entries aid wrote: the copies an in-game rename rewrites
// (web/profileedit.go renameSocialCopies). Caller holds AccountsMu. A failed save puts the rows back.
func renameCopiesLocked(aid, name string) error {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	var change SocialChange
	prevPosts, prevEntries := slices.Clone(DiaryPosts), slices.Clone(GuestbookEntries)
	for i := range DiaryPosts {
		if DiaryPosts[i].AvatarID == aid && DiaryPosts[i].Nickname != name {
			DiaryPosts[i].Nickname = name
			change.Posts = append(change.Posts, DiaryPosts[i].DiaryNo)
		}
	}
	for i := range GuestbookEntries {
		if GuestbookEntries[i].GuestAvtNo == aid && GuestbookEntries[i].GuestNickname != name {
			GuestbookEntries[i].GuestNickname = name
			change.Entries = append(change.Entries, GuestbookEntries[i].GuestHistNo)
		}
	}
	if len(change.Posts)+len(change.Entries) == 0 {
		return nil
	}
	if err := SaveSocialLocked(change); err != nil {
		DiaryPosts, GuestbookEntries = prevPosts, prevEntries
		return err
	}
	return nil
}

// RemovePlayerPost removes diary post number of the player with row id, with the comments and likes on it: the owner's in-game
// delete (erasePostLocked, the one cascade), which also deletes the pictures only the post named. The post, its cascade, those
// file rows and the audit row (number, reason) are one save. ErrNoItem when the post is not the player's.
func RemovePlayerPost(id int64, no, reason, actor string) error {
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
	SocialMu.Lock()
	defer SocialMu.Unlock()
	audit := &staffAudit{actor, "remove-post", playerTarget(id), map[string]any{"aid": acc.Aid, "number": no, "reason": reason}}
	found, err := erasePostLocked(acc.Aid, no, audit)
	if err != nil {
		return err
	}
	if !found {
		return ErrNoItem
	}
	return nil
}

// RemovePlayerGuestbookEntry removes guestbook entry number that the player with row id wrote, with its replies (the in-game
// erase, web/guestbook.go handleGuestbookErase; the replies go with the entry's row). One save with the audit row (number,
// host, reason). ErrNoItem when the entry is not the player's.
func RemovePlayerGuestbookEntry(id int64, no, reason, actor string) error {
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
	SocialMu.Lock()
	defer SocialMu.Unlock()
	i := slices.IndexFunc(GuestbookEntries, func(e SavedGuestbookEntry) bool { return e.GuestHistNo == no && e.GuestAvtNo == acc.Aid })
	if i < 0 {
		return ErrNoItem
	}
	host := GuestbookEntries[i].HostAvtNo
	prev := GuestbookEntries
	GuestbookEntries = slices.Delete(slices.Clone(prev), i, i+1)
	audit := staffAudit{actor, "remove-guestbook", playerTarget(id), map[string]any{"aid": acc.Aid, "number": no, "host": host, "reason": reason}}
	if err := saveSocialAuditedLocked(SocialChange{Entries: []string{no}}, audit); err != nil {
		GuestbookEntries = prev
		return err
	}
	return nil
}

// RemovePlayerComment removes diary comment number that the player with row id wrote. Nothing hangs off a comment. One save
// with the audit row (number, host = the post's owner, reason). ErrNoItem when the comment is not the player's.
func RemovePlayerComment(id int64, no, reason, actor string) error {
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
	SocialMu.Lock()
	defer SocialMu.Unlock()
	i := slices.IndexFunc(DiaryComments, func(c DiaryComment) bool { return c.CommentNo == no && c.CommentAvatarID == acc.Aid })
	if i < 0 {
		return ErrNoItem
	}
	host := DiaryComments[i].OwnerAvatarID
	prev := DiaryComments
	DiaryComments = slices.Delete(slices.Clone(prev), i, i+1)
	audit := staffAudit{actor, "remove-comment", playerTarget(id), map[string]any{"aid": acc.Aid, "number": no, "host": host, "reason": reason}}
	if err := saveSocialAuditedLocked(SocialChange{Comments: []string{no}}, audit); err != nil {
		DiaryComments = prev
		return err
	}
	return nil
}

// saveSocialAuditedLocked is SaveSocialLocked with one audit row in the same transaction, so a staff removal that cannot be
// recorded does not happen. Without an attached database it fails with ErrNoDatabase. Caller holds SocialMu.
func saveSocialAuditedLocked(c SocialChange, audit staffAudit) error {
	d := attachedDB()
	if d == nil {
		return ErrNoDatabase
	}
	site := callerSite()
	if err := d.Update(func(tx *sql.Tx) error {
		if err := saveSocialRowsTx(tx, c); err != nil {
			return err
		}
		return auditTx(tx, audit.actor, audit.action, audit.target, audit.detail)
	}); err != nil {
		return err
	}
	if testing.Testing() {
		netCheckSocial(d, site)
	}
	return nil
}
