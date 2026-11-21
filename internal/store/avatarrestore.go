package store

import (
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"cherry/internal/config"
)

// Restoring a deleted avatar (2026-11-21, user: avatar ids are never reused, so a deleted avatar can come back to its identity).
// Identity modes only; the staff panel offers it to the owner only. The record (AvatarRecord, deletedavatars.go) becomes the account of
// the target identity's row: the record's account fields, with that row's identity and ban, a new session key and a new own token
// (the target's earlier logins end). The social rows the record kept go back where their numbers are free and their other side still
// exists; the rest is skipped with the reason. Pictures, Treasure World, circle and chat rows were never removed, so nothing is written
// for them. The whole restore is ONE save: the account row, the social rows, the record's restored_at and the audit row.

// RestoreCounts is how many rows of each kind a restore writes.
type RestoreCounts struct {
	Posts, Entries, Replies, Comments, Likes, Friendships int
	ReadMarker                                            bool
}

// RestoreSkip is a row of the record that a restore leaves out, with the reason the review page shows.
type RestoreSkip struct {
	Kind, Ref, Reason string
}

// RestorePlan is a restore: the record, the identity it goes to, the account row it takes (TargetID 0 = a row is made), what it
// writes (Counts) and what it leaves out (Skipped). Refusals come with the plan (the error of PlanRestore).
type RestorePlan struct {
	Record    DeletedAvatar
	To        string
	TargetID  int64  // the accounts row of To; 0 = none yet
	TargetAid string // the avatar id that row holds ("0" = none)
	Counts    RestoreCounts
	Skipped   []RestoreSkip
}

// restoreWork is a plan with what applying it writes.
type restoreWork struct {
	plan     RestorePlan
	rec      AvatarRecord
	target   *Account // To's row; nil when one is made
	posts    []DiaryPost
	entries  []SavedGuestbookEntry // each Reply holds the replies restored with it
	replies  []restoredReply       // replies of entries that stay, appended to them
	comments []DiaryComment
	likes    []DiaryLike
	friends  []Friendship // the pairs set back to their recorded state
	readSet  bool         // the read marker is written
	readHist uint64
}

type restoredReply struct {
	parent string
	reply  GuestbookReply
}

// holdsAidLocked reports whether a live account holds avatar id aid. Caller holds AccountsMu.
func holdsAidLocked(aid string) bool {
	return slices.ContainsFunc(accountList, func(a *Account) bool { return a.Aid == aid })
}

// liveSocialLocked reports whether an avatar a social row can name still exists: a live account or the synthetic friend.
// Caller holds AccountsMu.
func liveSocialLocked(aid string) bool { return aid == FriendAID || holdsAidLocked(aid) }

// planRestoreLocked checks a restore and works out its rows. Caller holds AccountsMu and SocialMu. The work is returned with
// the refusals (a RefusedError), so the review page can show the plan; nil only when the record is unknown or unreadable.
func planRestoreLocked(recID int64, to string) (*restoreWork, error) {
	det, found, err := DeletedAvatarByID(recID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNoRecord
	}
	w := &restoreWork{rec: det.Record}
	w.plan = RestorePlan{Record: det.DeletedAvatar, To: to}
	aid := det.Aid
	switch {
	case !config.IdentityMode():
		return w, refused("Restores run in discord and dev-fake modes only.")
	case !ValidIdentityForMode(to):
		return w, refused("The target is not an identity of this server's kind.")
	case det.RestoredAt != "":
		return w, refused("This avatar was restored at %s.", det.RestoredAt)
	case holdsAidLocked(aid):
		return w, refused("A live account holds avatar %s, so there is nothing to restore.", aid)
	}
	if tgt := IdentityAccountLocked(to); tgt != nil {
		switch {
		case tgt.Ban != nil:
			return w, refused("The target identity is banned.")
		case tgt.Aid != "0":
			return w, refused("The target identity already has avatar %s.", tgt.Aid)
		}
		w.target = tgt
		w.plan.TargetID, w.plan.TargetAid = accountRows[tgt].id, tgt.Aid
	}
	w.planSocialLocked(aid, det.Record)
	return w, nil
}

// planSocialLocked fills the social rows of the work: a row is restored when its number is free and the avatars it names exist,
// and skipped with its reason otherwise. Posts and guestbook entries go to the front of their lists, the rest to the back, so
// memory and the database keep one order (a restored row keeps the order it had in the record).
func (w *restoreWork) planSocialLocked(aid string, rec AvatarRecord) {
	skip := func(kind, ref, reason string) {
		w.plan.Skipped = append(w.plan.Skipped, RestoreSkip{Kind: kind, Ref: ref, Reason: reason})
	}
	postNos := map[string]bool{}
	for _, p := range DiaryPosts {
		postNos[p.DiaryNo] = true
	}
	droppedPost := map[string]bool{} // numbers of recorded posts whose number a live post holds now
	for _, p := range rec.Posts {
		if postNos[p.DiaryNo] {
			skip("post", p.DiaryNo, "its number is taken")
			droppedPost[p.DiaryNo] = true
			continue
		}
		postNos[p.DiaryNo] = true
		w.posts = append(w.posts, p)
	}

	entryNos, replyNos := map[string]bool{}, map[string]bool{}
	for _, e := range GuestbookEntries {
		entryNos[e.GuestHistNo] = true
		for _, r := range e.Reply {
			replyNos[r.ReplyHistNo] = true
		}
	}
	for _, e := range rec.Guestbook {
		if e.HostAvtNo != aid && !liveSocialLocked(e.HostAvtNo) {
			skip("entry", e.GuestHistNo, "its guestbook's avatar is gone")
			continue
		}
		if entryNos[e.GuestHistNo] {
			skip("entry", e.GuestHistNo, "its number is taken")
			continue
		}
		entryNos[e.GuestHistNo] = true
		kept := e
		kept.Reply = nil
		for _, r := range e.Reply {
			if replyNos[r.ReplyHistNo] {
				skip("reply", r.ReplyHistNo, "its number is taken")
				continue
			}
			replyNos[r.ReplyHistNo] = true
			kept.Reply = append(kept.Reply, r)
		}
		w.entries = append(w.entries, kept)
	}
	for _, r := range rec.Replies {
		if !slices.ContainsFunc(GuestbookEntries, func(e SavedGuestbookEntry) bool { return e.GuestHistNo == r.ParentHistNo }) {
			skip("reply", r.ReplyHistNo, "its guestbook entry is gone")
			continue
		}
		if replyNos[r.ReplyHistNo] {
			skip("reply", r.ReplyHistNo, "its number is taken")
			continue
		}
		replyNos[r.ReplyHistNo] = true
		w.replies = append(w.replies, restoredReply{parent: r.ParentHistNo, reply: r})
	}

	commentNos := map[string]bool{}
	for _, c := range DiaryComments {
		commentNos[c.CommentNo] = true
	}
	for _, c := range rec.Comments {
		switch {
		case droppedPost[c.DiaryNo]:
			skip("comment", c.CommentNo, "its post was not restored")
		case !postNos[c.DiaryNo]:
			skip("comment", c.CommentNo, "its post is gone")
		case commentNos[c.CommentNo]:
			skip("comment", c.CommentNo, "its number is taken")
		default:
			commentNos[c.CommentNo] = true
			w.comments = append(w.comments, c)
		}
	}

	likeNos := map[string]bool{}
	likePairs := map[[2]string]bool{} // one like per post and liker
	for _, l := range DiaryLikes {
		likeNos[l.LikeNo] = true
		likePairs[[2]string{l.DiaryNo, l.LikeAvatarID}] = true
	}
	for _, l := range rec.Likes {
		pair := [2]string{l.DiaryNo, l.LikeAvatarID}
		switch {
		case droppedPost[l.DiaryNo]:
			skip("like", l.LikeNo, "its post was not restored")
		case !postNos[l.DiaryNo]:
			skip("like", l.LikeNo, "its post is gone")
		case likeNos[l.LikeNo] || likePairs[pair]:
			skip("like", l.LikeNo, "its number or its liker is taken")
		default:
			likeNos[l.LikeNo] = true
			likePairs[pair] = true
			w.likes = append(w.likes, l)
		}
	}

	for _, f := range rec.Friendships {
		other := f.A
		if other == aid {
			other = f.B
		}
		if f.State == "removed" {
			continue // the record and the row agree already
		}
		i := FriendshipIndex(aid, other, "")
		switch {
		case i < 0:
			skip("friendship", other, "no friendship row for the pair")
		case Friendships[i].State != "removed":
			skip("friendship", other, "the pair is no longer removed")
		case !liveSocialLocked(other):
			skip("friendship", other, "the other avatar is gone")
		default:
			w.friends = append(w.friends, Friendship{A: f.A, B: f.B, State: f.State})
		}
	}

	if rec.GuestbookRead != 0 {
		w.readSet, w.readHist = true, rec.GuestbookRead
	}
	w.plan.Counts = RestoreCounts{Posts: len(w.posts), Entries: len(w.entries), Replies: len(w.replies),
		Comments: len(w.comments), Likes: len(w.likes), Friendships: len(w.friends), ReadMarker: w.readSet}
	for _, e := range w.entries {
		w.plan.Counts.Replies += len(e.Reply)
	}
}

// PlanRestore returns what restoring the deleted avatar recID to the identity to would do. The error is a RefusedError when the
// restore is refused (the plan is still filled in), ErrNoRecord for an unknown record.
func PlanRestore(recID int64, to string) (RestorePlan, error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	w, err := planRestoreLocked(recID, to)
	if w == nil {
		return RestorePlan{}, err
	}
	return w.plan, err
}

// RestoreAvatar restores the deleted avatar recID to the identity to, in ONE save (see restoreAvatar). Then OnRevoke closes the
// live connections of the target row's earlier logins (its avatar id is "0" before the restore).
func RestoreAvatar(recID int64, to, actor string) (RestorePlan, error) {
	plan, err := restoreAvatar(recID, to, actor)
	if err == nil && OnRevoke != nil {
		OnRevoke("0")
	}
	return plan, err
}

// restoreAvatar is the save of RestoreAvatar. The target row becomes the record's account (restoredAccount), or a row is made
// for a target that has none; the social rows are written and the record is marked restored, in the same transaction as the
// account row and the audit row (avatar-restore). Memory changes first; a failed save undoes them.
func restoreAvatar(recID int64, to, actor string) (RestorePlan, error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	w, err := planRestoreLocked(recID, to)
	if err != nil {
		if w == nil {
			return RestorePlan{}, err
		}
		return w.plan, err
	}
	d := attachedDB()
	if d == nil {
		return w.plan, ErrNoDatabase
	}
	aid := w.plan.Record.Aid

	prevList, prevPending := slices.Clone(accountList), slices.Clone(pendingTokens)
	var tgt *Account
	var rs *rowState
	var prev Account
	var prevRS rowState
	var prevHashes []string
	if w.target == nil {
		tgt = newRecord("")
		tgt.Identity = to
		rs = &rowState{}
		accountList = append(accountList, tgt)
		accountRows[tgt] = rs
	} else {
		tgt, rs = w.target, accountRows[w.target]
		prev, prevRS = *tgt, *rs
		prevHashes = AccountTokenHashesLocked(tgt)
		for _, h := range prevHashes {
			delete(accountsByHash, h)
		}
		pendingTokens = slices.DeleteFunc(pendingTokens, func(p pendingToken) bool { return p.acc == tgt })
		rs.revoke = true // the save deletes the target's token rows (its earlier logins) before the new own token is written
	}
	*tgt = *restoredAccount(w.rec.Account, tgt)
	addTokenLocked(tgt, tgt.AccessToken, true)

	prevPosts, prevBook, prevFriends := DiaryPosts, GuestbookEntries, Friendships
	prevComments, prevLikes, prevRead := DiaryComments, DiaryLikes, GuestbookRead
	book := slices.Clone(GuestbookEntries)
	for _, r := range w.replies {
		i := slices.IndexFunc(book, func(e SavedGuestbookEntry) bool { return e.GuestHistNo == r.parent })
		book[i].Reply = append(slices.Clip(book[i].Reply), r.reply)
	}
	DiaryPosts = slices.Concat(w.posts, prevPosts)
	GuestbookEntries = slices.Concat(w.entries, book)
	DiaryComments = slices.Concat(prevComments, w.comments)
	DiaryLikes = slices.Concat(prevLikes, w.likes)
	Friendships = slices.Clone(prevFriends)
	for _, f := range w.friends {
		Friendships[FriendshipIndex(f.A, f.B, "")].State = f.State
	}
	read := maps.Clone(prevRead)
	if read == nil {
		read = map[string]uint64{}
	}
	if w.readSet {
		read[aid] = w.readHist
	}
	GuestbookRead = read

	now := time.Now().UTC().Format(time.RFC3339)
	c := w.plan.Counts
	audit := staffAudit{actor, "avatar-restore", fmt.Sprintf("deleted-avatar:%d", recID), map[string]any{
		"record": recID, "aid": aid, "to": to, "posts": c.Posts, "entries": c.Entries, "replies": c.Replies,
		"comments": c.Comments, "likes": c.Likes, "friendships": c.Friendships, "readMarker": c.ReadMarker,
		"skipped": len(w.plan.Skipped),
	}}
	extra := func(tx *sql.Tx) error { return restoreRowsTx(tx, w, recID, now, rs.id) }
	if err := commitLocked(nil, []*Account{tgt}, 0, callerSite(), &audit, extra); err != nil {
		dropTokenLocked(CodeHash(tgt.AccessToken)) // the new own token goes with the failed save
		if w.target == nil {
			forgetAccountLocked(tgt)
			accountList = prevList
		} else {
			*tgt, *rs = prev, prevRS
			for _, h := range prevHashes {
				accountsByHash[h] = tgt
			}
		}
		pendingTokens = prevPending
		DiaryPosts, GuestbookEntries, Friendships = prevPosts, prevBook, prevFriends
		DiaryComments, DiaryLikes, GuestbookRead = prevComments, prevLikes, prevRead
		return w.plan, err
	}
	if testing.Testing() {
		netCheckSocial(d, callerSite())
	}
	w.plan.TargetID = rs.id
	return w.plan, nil
}

// restoredAccount is the account a restore gives the target row: the record's account fields, the target's identity, no ban, no
// codes, no legacy key and a new own token. Its session key rotates from the target's salt and epoch, so the target's earlier logins
// end. The once-only flags (welcome gift, butler hire, boss book) are the record's.
func restoredAccount(s StoredAccount, target *Account) *Account {
	acc := accountFromStored(s)
	acc.AccessToken = RandomToken(32)
	acc.Aid = s.Aid
	acc.Identity = target.Identity
	acc.Ban = nil
	acc.GuestCode, acc.GuestCodeHash, acc.CodeHashes, acc.LegacyKeyHash = "", "", nil, ""
	acc.SessionSalt, acc.SessionEpoch, acc.SessionKey = target.SessionSalt, target.SessionEpoch, target.SessionKey
	rotateSessionKey(acc)
	return acc
}

// restoreRowsTx writes the restored rows of w and marks the record restored, inside the caller's transaction. The posts and the
// entries go in front of the rows they have (each takes the lowest id, last first, so the batch keeps its order); the rest is
// appended. A record that was restored already changes nothing and refuses.
func restoreRowsTx(tx *sql.Tx, w *restoreWork, recID int64, now string, accountID int64) error {
	for i := len(w.posts) - 1; i >= 0; i-- {
		p := w.posts[i]
		data, err := rowJSON(p)
		if err != nil {
			return err
		}
		id, err := frontID(tx, "diary_posts")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO diary_posts (id, diary_no, avatar_id, data) VALUES (?, ?, ?, ?)`, id, p.DiaryNo, p.AvatarID, data); err != nil {
			return err
		}
	}
	for i := len(w.entries) - 1; i >= 0; i-- {
		e := w.entries[i]
		body := e
		body.Reply = nil // the replies are rows of their own
		data, err := rowJSON(body)
		if err != nil {
			return err
		}
		id, err := frontID(tx, "guestbook_entries")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO guestbook_entries (id, guest_hist_no, host_avt_no, data) VALUES (?, ?, ?, ?)`, id, e.GuestHistNo, e.HostAvtNo, data); err != nil {
			return err
		}
		for _, r := range e.Reply {
			rd, err := rowJSON(r)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO guestbook_replies (reply_hist_no, entry_id, data) VALUES (?, ?, ?)`, r.ReplyHistNo, id, rd); err != nil {
				return err
			}
		}
	}
	for _, r := range w.replies {
		var entryID int64
		if err := tx.QueryRow(`SELECT id FROM guestbook_entries WHERE guest_hist_no = ?`, r.parent).Scan(&entryID); err != nil {
			return fmt.Errorf("restored reply %s: its entry is not in the database", r.reply.ReplyHistNo)
		}
		rd, err := rowJSON(r.reply)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO guestbook_replies (reply_hist_no, entry_id, data) VALUES (?, ?, ?)`, r.reply.ReplyHistNo, entryID, rd); err != nil {
			return err
		}
	}
	for _, c := range w.comments {
		data, err := rowJSON(c)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO diary_comments (comment_no, diary_no, data) VALUES (?, ?, ?)`, c.CommentNo, c.DiaryNo, data); err != nil {
			return err
		}
	}
	for _, l := range w.likes {
		data, err := rowJSON(l)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO diary_likes (like_no, diary_no, like_avatar_id, data) VALUES (?, ?, ?, ?)`, l.LikeNo, l.DiaryNo, l.LikeAvatarID, data); err != nil {
			return err
		}
	}
	for _, f := range w.friends {
		lo, hi := min(f.A, f.B), max(f.A, f.B)
		if _, err := tx.Exec(`UPDATE friendships SET state = ? WHERE min(a, b) = ? AND max(a, b) = ?`, f.State, lo, hi); err != nil {
			return err
		}
	}
	if w.readSet {
		if err := upsertRow(tx, "guestbook_read", slotBack, []string{"host_aid", "hist_no"},
			[]any{w.plan.Record.Aid, int64(w.readHist)}, "host_aid = ?", w.plan.Record.Aid); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`UPDATE deleted_avatars SET restored_at = ?, restored_account = ? WHERE id = ? AND restored_at = ''`, now, accountID, recID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return refused("This avatar was restored already.")
	}
	return nil
}
