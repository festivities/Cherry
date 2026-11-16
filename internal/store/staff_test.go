package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// staffAccount makes the account of identity in the attached database, with its avatar id, name and gems, and returns it with
// its row id. Its uniqueCode is identity + " code".
func staffAccount(t *testing.T, identity, aid, name string, gems int64) (*Account, int64) {
	t.Helper()
	if _, _, err := MintIdentityCode(identity, CodeHash(identity+" code"), true); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := IdentityAccountLocked(identity)
	acc.Aid, acc.Name, acc.Gems = aid, name, gems
	if err := SaveLocked(nil, acc); err != nil {
		t.Fatal(err)
	}
	return acc, accountRows[acc].id
}

// balances reads the gems and cash of acc under the lock.
func balances(acc *Account) (gems, cash int64) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	return acc.Gems, acc.Cash
}

// auditCount is the number of audit rows.
func auditCount(t *testing.T) int {
	t.Helper()
	rows, err := AuditRows(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// must returns the value of a call that cannot fail in a test.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TestFailedSaveLeavesTheAccountAsItWas: with the database closed every save fails; the grant and the ban change nothing in
// memory, and the tokens still resolve.
func TestFailedSaveLeavesTheAccountAsItWas(t *testing.T) {
	d := attachTestDB(t)
	acc, id := staffAccount(t, "dev-fs", "1003", "Failed", 10)
	AccountsMu.Lock()
	own := acc.AccessToken
	AccountsMu.Unlock()
	if err := d.Close(); err != nil { // every save from now on fails
		t.Fatal(err)
	}
	AccountsMu.Lock()
	acc.Gems += 5
	err := SaveAuditedLocked("discord:1", "grant", PlayerTarget(id), map[string]any{"delta": 5}, nil, acc)
	acc.Gems -= 5
	AccountsMu.Unlock()
	if err == nil {
		t.Fatal("a grant with a failing save succeeded")
	}
	if err := BanPlayer(id, "x", "discord:1"); err == nil {
		t.Fatal("a ban with a failing save succeeded")
	}
	if gems, _ := balances(acc); gems != 10 {
		t.Fatalf("a failed grant left gems %d, want 10", gems)
	}
	AccountsMu.Lock()
	banned := acc.Ban != nil
	AccountsMu.Unlock()
	if banned || resolves(own) != acc {
		t.Fatal("a failed ban left the account banned or its token unresolved")
	}
}

// TestBanRevokesTokensAndSessionKey: a ban ends every token of the account (own and alias), replaces its session key, refuses
// the next uniqueCode and the next login, and survives a reload; a second ban is refused, and the audit row names the reason.
func TestBanRevokesTokensAndSessionKey(t *testing.T) {
	d := attachTestDB(t)
	acc, id := staffAccount(t, "dev-bn", "1004", "Banned", 0)
	AccountsMu.Lock()
	own, session := acc.AccessToken, acc.SessionKey
	alias, err := MintAliasLocked(acc)
	AccountsMu.Unlock()
	if err != nil || resolves(alias) != acc || resolves(own) != acc {
		t.Fatalf("before the ban: alias %v, own resolves %v", err, resolves(own) == acc)
	}
	if err := BanPlayer(id, "  spam  ", "discord:9"); err != nil {
		t.Fatal(err)
	}
	if resolves(own) != nil || resolves(alias) != nil {
		t.Fatal("a token of the banned account still resolves")
	}
	if rows := tokenRows(t, d, acc); len(rows) != 0 {
		t.Fatalf("the ban left %d token rows", len(rows))
	}
	AccountsMu.Lock()
	accountsByHash[CodeHash(alias)] = acc // a token that survived the ban in memory still does not resolve
	AccountsMu.Unlock()
	if resolves(alias) != nil {
		t.Fatal("a surviving token of a banned account resolves")
	}
	AccountsMu.Lock()
	delete(accountsByHash, CodeHash(alias))
	AccountsMu.Unlock()
	AccountsMu.Lock()
	rotated := acc.SessionKey != session && acc.SessionKey != ""
	ban := acc.Ban
	AccountsMu.Unlock()
	if !rotated || ban == nil || ban.Reason != "spam" || ban.By != "discord:9" || ban.At == "" {
		t.Fatalf("after the ban: session rotated %v, ban %+v", rotated, ban)
	}
	if _, _, err := MintIdentityCode("dev-bn", CodeHash("fresh"), true); !errors.Is(err, ErrBanned) {
		t.Fatalf("mint for a banned identity: err %v, want ErrBanned", err)
	}
	if _, err := CodeLogin("dev-bn code"); !errors.Is(err, ErrBanned) {
		t.Fatalf("code login for a banned identity: err %v, want ErrBanned", err)
	}
	if err := BanPlayer(id, "again", "discord:9"); !errors.Is(err, ErrAlreadyBanned) {
		t.Fatalf("second ban: err %v, want ErrAlreadyBanned", err)
	}

	if err := ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	if !IdentityBanned("dev-bn") || resolves(alias) != nil {
		t.Fatal("the ban or the revoked token did not survive a reload")
	}
	rows, err := AuditRows(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Action == "ban" && r.Actor == "discord:9" && r.Target == fmt.Sprintf("player:%d", id) && strings.Contains(r.Detail, `"reason":"spam"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ban audit row among %+v", rows)
	}
}

// TestBanSurvivesResetIdentity: Delete Avatar in identity mode (ResetIdentityLocked) keeps the ban and still refuses the
// identity's next code.
func TestBanSurvivesResetIdentity(t *testing.T) {
	attachTestDB(t)
	acc, id := staffAccount(t, "dev-rs", "1005", "Reset", 0)
	if err := BanPlayer(id, "x", "discord:9"); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	_, err := ResetIdentityLocked(acc, []LedgerLine{{Aid: "1005", Reason: "account deleted"}})
	kept := acc.Ban != nil && acc.Aid == "0"
	fresh := acc.AccessToken
	AccountsMu.Unlock()
	if err != nil || !kept {
		t.Fatalf("reset: err %v, ban kept and aid 0 %v", err, kept)
	}
	if resolves(fresh) != nil {
		t.Fatal("the fresh own token of a banned identity resolves")
	}
	if !IdentityBanned("dev-rs") {
		t.Fatal("the reset dropped the ban")
	}
	if _, err := CodeLogin("dev-rs code"); !errors.Is(err, ErrBanned) {
		t.Fatalf("code login after the reset: err %v, want ErrBanned", err)
	}
}

// TestUnbanRestoresSignIn: an unban lifts the refusal; the identity signs in again through a new code, and the account is the
// same record (its aid is kept).
func TestUnbanRestoresSignIn(t *testing.T) {
	attachTestDB(t)
	acc, id := staffAccount(t, "dev-ub", "1006", "Unban", 0)
	if err := BanPlayer(id, "x", "discord:9"); err != nil {
		t.Fatal(err)
	}
	if err := UnbanPlayer(id, "discord:9"); err != nil {
		t.Fatal(err)
	}
	if err := UnbanPlayer(id, "discord:9"); !errors.Is(err, ErrNotBanned) {
		t.Fatalf("second unban: err %v, want ErrNotBanned", err)
	}
	aid, _, err := MintIdentityCode("dev-ub", CodeHash("after unban"), true)
	if err != nil || aid != "1006" {
		t.Fatalf("mint after unban: aid %q err %v", aid, err)
	}
	token, err := CodeLogin("after unban")
	if err != nil || token == "" || resolves(token) != acc {
		t.Fatalf("code login after unban: err %v", err)
	}
	AccountsMu.Lock()
	key := acc.SessionKey
	AccountsMu.Unlock()
	if key == "" {
		t.Fatal("the session key is empty after an unban")
	}
}

// TestGuestLoginRefusesBannedAccount: in lab-guest a banned device account gets no token and no new account for its code.
func TestGuestLoginRefusesBannedAccount(t *testing.T) {
	attachTestDB(t)
	AccountsMu.Lock()
	acc, err := newAccountLocked("device-1234")
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	id := accountRows[acc].id
	AccountsMu.Unlock()
	if err := BanPlayer(id, "x", "discord:9"); err != nil {
		t.Fatal(err)
	}
	AccountsMu.Lock()
	before := AccountCountLocked()
	AccountsMu.Unlock()
	if _, err := GuestLogin("device-1234"); !errors.Is(err, ErrBanned) {
		t.Fatalf("guest login of a banned device: err %v, want ErrBanned", err)
	}
	AccountsMu.Lock()
	after := AccountCountLocked()
	AccountsMu.Unlock()
	if after != before {
		t.Fatalf("the refused guest login changed the account count %d -> %d", before, after)
	}
}

// TestFindPlayersAndCap: a search matches the avatar id exactly, the identity exactly and the name in any case, returns at
// most 50 rows and says when there are more; an empty search finds nothing.
func TestFindPlayersAndCap(t *testing.T) {
	attachTestDB(t)
	staffAccount(t, "dev-f1", "2001", "Alice Ocean", 0)
	staffAccount(t, "dev-f2", "2002", "bob", 0)
	if rows, more := FindPlayers("2001"); len(rows) != 1 || rows[0].Aid != "2001" || more {
		t.Fatalf("by aid: %+v more %v", rows, more)
	}
	if rows, _ := FindPlayers("ALICE"); len(rows) != 1 || rows[0].Name != "Alice Ocean" || rows[0].ID == 0 {
		t.Fatalf("by name: %+v", rows)
	}
	if rows, _ := FindPlayers("dev-f2"); len(rows) != 1 || rows[0].Aid != "2002" {
		t.Fatalf("by identity: %+v", rows)
	}
	if rows, _ := FindPlayers("dev-f"); len(rows) != 0 {
		t.Fatalf("a part of an identity matched: %+v", rows)
	}
	if rows, more := FindPlayers("  "); rows != nil || more {
		t.Fatalf("an empty search: %+v", rows)
	}
	for i := 0; i < 60; i++ {
		staffAccount(t, fmt.Sprintf("dev-c%d", i), fmt.Sprintf("%d", 3000+i), "Crowd", 0)
	}
	rows, more := FindPlayers("crowd")
	if len(rows) != 50 || !more {
		t.Fatalf("60 matches: %d rows, more %v; want 50 and more", len(rows), more)
	}
}

// TestStaffActionsRefuseUnknownPlayers: an id with no account (or 0) is ErrNoPlayer for every staff call.
func TestStaffActionsRefuseUnknownPlayers(t *testing.T) {
	attachTestDB(t)
	for _, id := range []int64{0, 99999} {
		if _, ok := PlayerByID(id); ok {
			t.Errorf("PlayerByID(%d) found an account", id)
		}
		if _, err := PlayerLedger(id, 20); !errors.Is(err, ErrNoPlayer) {
			t.Errorf("PlayerLedger(%d): err %v", id, err)
		}
		if err := BanPlayer(id, "x", "discord:1"); !errors.Is(err, ErrNoPlayer) {
			t.Errorf("BanPlayer(%d): err %v", id, err)
		}
		if err := UnbanPlayer(id, "discord:1"); !errors.Is(err, ErrNoPlayer) {
			t.Errorf("UnbanPlayer(%d): err %v", id, err)
		}
	}
}

// socialDB is attachTestDB with the social memory emptied first: AttachDB writes what memory holds into the new database, so
// the social rows of an earlier test would otherwise be written into this one.
func socialDB(t *testing.T) *DB {
	t.Helper()
	ResetSocial()
	return attachTestDB(t)
}

// nameOf reads the nickname of acc under the lock.
func nameOf(acc *Account) string {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	return acc.Name
}

// auditRowsOf is the audit rows with action, newest first.
func auditRowsOf(t *testing.T, action string) []AuditRow {
	t.Helper()
	rows, err := AuditRows(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []AuditRow
	for _, r := range rows {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

// seedPost appends a diary post of aid (newest first, as the game stores it) and saves it. It returns the post's number.
func seedPost(t *testing.T, aid, nick, content string) string {
	t.Helper()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	NextDiaryNo++
	no := strconv.FormatUint(NextDiaryNo, 10)
	DiaryPosts = append([]DiaryPost{{DiaryNo: no, AvatarID: aid, Nickname: nick, RegDate: "1700000000000", Content: content, Images: []DiaryImage{}}}, DiaryPosts...)
	if err := SaveSocialLocked(SocialChange{Posts: []string{no}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
	return no
}

// seedEntry appends a guestbook entry written by guest in host's guestbook and returns its number.
func seedEntry(t *testing.T, host, guest, nick, content string) string {
	t.Helper()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	NextGuestHistNo++
	no := strconv.FormatUint(NextGuestHistNo, 10)
	GuestbookEntries = append([]SavedGuestbookEntry{{HostAvtNo: host, GuestHistNo: no, GuestAvtNo: guest, GuestNickname: nick, Content: content, RegYmdt: "1700000000000"}}, GuestbookEntries...)
	if err := SaveSocialLocked(SocialChange{Entries: []string{no}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
	return no
}

// seedReply appends a reply by replier to the guestbook entry parent and returns its reply history number.
func seedReply(t *testing.T, parent, replier string) string {
	t.Helper()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	NextReplyHistNo++
	no := strconv.FormatUint(NextReplyHistNo, 10)
	for i := range GuestbookEntries {
		if GuestbookEntries[i].GuestHistNo == parent {
			GuestbookEntries[i].Reply = append(GuestbookEntries[i].Reply, GuestbookReply{ReplyHistNo: no, ParentHistNo: parent, ReplyAvtNo: replier, Content: "reply"})
		}
	}
	if err := SaveSocialLocked(SocialChange{Replies: []string{no}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
	return no
}

// seedComment appends a comment by writer on the post of owner (number no) and returns its comment number.
func seedComment(t *testing.T, owner, no, writer, content string) string {
	t.Helper()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	NextCommentNo++
	cno := strconv.FormatUint(NextCommentNo, 10)
	DiaryComments = append(DiaryComments, DiaryComment{CommentNo: cno, DiaryNo: no, OwnerAvatarID: owner, CommentAvatarID: writer, Content: content, Date: "1700000000000"})
	if err := SaveSocialLocked(SocialChange{Comments: []string{cno}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
	return cno
}

// seedLike appends a like by liker on the post of owner (number no) and returns its like number.
func seedLike(t *testing.T, owner, no, liker string) string {
	t.Helper()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	NextLikeNo++
	lno := strconv.FormatUint(NextLikeNo, 10)
	DiaryLikes = append(DiaryLikes, DiaryLike{LikeNo: lno, DiaryNo: no, OwnerAvatarID: owner, LikeAvatarID: liker, LikeType: 1})
	if err := SaveSocialLocked(SocialChange{Likes: []string{lno}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
	return lno
}

// socialCounts is the number of diary posts, comments, likes and guestbook entries in memory.
func socialCounts() (posts, comments, likes, entries int) {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	return len(DiaryPosts), len(DiaryComments), len(DiaryLikes), len(GuestbookEntries)
}

// TestRenameNicknameRule: the rename applies the game's nickname rule (1 to 20 characters, not only spaces, no control
// characters, valid UTF-8; 20 characters is the limit). A refused name, or a refused reason, changes nothing and writes no
// audit row.
func TestRenameNicknameRule(t *testing.T) {
	socialDB(t)
	acc, id := staffAccount(t, "dev-rn", "1101", "Old", 0)
	before := auditCount(t)
	for _, name := range []string{"", "   ", strings.Repeat("n", 21), strings.Repeat("あ", 21), "a\tb", "a\x7fb", "\xff"} {
		if err := RenamePlayer(id, name, "fix", "discord:1"); !errors.Is(err, ErrBadInput) {
			t.Errorf("name %q: err %v, want ErrBadInput", name, err)
		}
	}
	if err := RenamePlayer(id, "Fine", "  ", "discord:1"); !errors.Is(err, ErrBadInput) {
		t.Errorf("empty reason: err %v, want ErrBadInput", err)
	}
	if nameOf(acc) != "Old" || auditCount(t) != before {
		t.Fatalf("a refused rename changed the name to %q or wrote %d audit rows", nameOf(acc), auditCount(t)-before)
	}
	for _, name := range []string{strings.Repeat("n", 20), strings.Repeat("あ", 20), "Mika Chan"} {
		if err := RenamePlayer(id, name, "fine", "discord:1"); err != nil {
			t.Errorf("name %q refused: %v", name, err)
		}
	}
}

// TestRenameWritesAccountAuditAndCopies: a rename sets the name, writes its audit row (old and new name, reason), and rewrites
// the nickname in the diary posts and guestbook entries the player wrote, and only those.
func TestRenameWritesAccountAuditAndCopies(t *testing.T) {
	socialDB(t)
	acc, id := staffAccount(t, "dev-rc", "1102", "Old", 0)
	mine := seedPost(t, "1102", "Old", "my post")
	other := seedPost(t, "1199", "Other", "their post")
	entry := seedEntry(t, "1199", "1102", "Old", "my entry")
	theirs := seedEntry(t, "1102", "1198", "Them", "their entry")
	if err := RenamePlayer(id, "New", "  wrong name  ", "discord:1"); err != nil {
		t.Fatal(err)
	}
	if nameOf(acc) != "New" {
		t.Fatalf("name %q, want New", nameOf(acc))
	}
	wantPosts := map[string]string{mine: "New", other: "Other"}
	wantEntries := map[string]string{entry: "New", theirs: "Them"}
	SocialMu.Lock()
	for _, p := range DiaryPosts {
		if want, ok := wantPosts[p.DiaryNo]; ok && p.Nickname != want {
			t.Errorf("post %s nickname %q, want %q", p.DiaryNo, p.Nickname, want)
		}
	}
	for _, e := range GuestbookEntries {
		if want, ok := wantEntries[e.GuestHistNo]; ok && e.GuestNickname != want {
			t.Errorf("entry %s nickname %q, want %q", e.GuestHistNo, e.GuestNickname, want)
		}
	}
	SocialMu.Unlock()
	rows := auditRowsOf(t, "rename")
	if len(rows) != 1 || rows[0].Actor != "discord:1" || rows[0].Target != fmt.Sprintf("player:%d", id) {
		t.Fatalf("rename audit rows = %+v", rows)
	}
	if d := rows[0].Detail; !strings.Contains(d, `"old":"Old"`) || !strings.Contains(d, `"new":"New"`) || !strings.Contains(d, `"reason":"wrong name"`) {
		t.Fatalf("rename audit detail = %s", d)
	}
}

// TestRemovePostCascade: removing the player's own post takes its comments and likes and nothing else; the audit row names
// the number and the reason, never the text. A post that is not the player's, or a wrong number, changes nothing.
func TestRemovePostCascade(t *testing.T) {
	socialDB(t)
	_, idA := staffAccount(t, "dev-rp1", "1103", "Poster", 0)
	_, idB := staffAccount(t, "dev-rp2", "1104", "Reader", 0)
	p1 := seedPost(t, "1103", "Poster", "secret words in post one")
	p2 := seedPost(t, "1103", "Poster", "post two")
	pB := seedPost(t, "1104", "Reader", "reader's post")
	c1 := seedComment(t, "1103", p1, "1104", "on post one")
	c2 := seedComment(t, "1103", p2, "1104", "on post two")
	c3 := seedComment(t, "1104", pB, "1103", "poster on reader's post")
	seedLike(t, "1103", p1, "1104")
	seedLike(t, "1103", p2, "1104")
	if posts, comments, likes, _ := socialCounts(); posts != 3 || comments != 3 || likes != 2 {
		t.Fatalf("seeded posts %d comments %d likes %d", posts, comments, likes)
	}

	if err := RemovePlayerPost(idB, p1, "not theirs", "discord:1"); !errors.Is(err, ErrNoItem) {
		t.Fatalf("another player's post: err %v, want ErrNoItem", err)
	}
	if err := RemovePlayerPost(idA, "999999", "no such", "discord:1"); !errors.Is(err, ErrNoItem) {
		t.Fatalf("wrong number: err %v, want ErrNoItem", err)
	}
	if posts, comments, likes, _ := socialCounts(); posts != 3 || comments != 3 || likes != 2 {
		t.Fatal("a refused removal changed the social rows")
	}

	if err := RemovePlayerPost(idA, p1, "spam", "discord:1"); err != nil {
		t.Fatal(err)
	}
	if posts, comments, likes, _ := socialCounts(); posts != 2 || comments != 2 || likes != 1 {
		t.Fatalf("after the removal: posts %d comments %d likes %d, want 2 2 1", posts, comments, likes)
	}
	SocialMu.Lock()
	kept := map[string]bool{}
	for _, c := range DiaryComments {
		kept[c.CommentNo] = true
	}
	SocialMu.Unlock()
	if kept[c1] || !kept[c2] || !kept[c3] {
		t.Fatalf("comments kept %v: the post one comment must go, the others stay (c1 %s c2 %s c3 %s)", kept, c1, c2, c3)
	}
	rows := auditRowsOf(t, "remove-post")
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, `"number":"`+p1+`"`) || !strings.Contains(rows[0].Detail, `"reason":"spam"`) {
		t.Fatalf("remove-post audit = %+v", rows)
	}
	if strings.Contains(rows[0].Detail, "secret words") {
		t.Fatal("the audit row names the removed text")
	}
}

// TestRemoveGuestbookEntryTakesItsReplies: the player's entry in another guestbook goes with its replies (the replies are
// rows of their own, cascaded by the database). The host cannot remove it through the player's route, and a wrong number
// changes nothing.
func TestRemoveGuestbookEntryTakesItsReplies(t *testing.T) {
	d := socialDB(t)
	_, idW := staffAccount(t, "dev-gw", "1105", "Writer", 0)
	_, idH := staffAccount(t, "dev-gh", "1106", "Host", 0)
	entry := seedEntry(t, "1106", "1105", "Writer", "hello host")
	seedReply(t, entry, "1106")
	other := seedEntry(t, "1106", "1107", "Other", "not from the writer")
	countReplies := func() int {
		var n int
		if err := d.View(func(q Querier) error { return q.QueryRow("SELECT COUNT(*) FROM guestbook_replies").Scan(&n) }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countReplies(); n != 1 {
		t.Fatalf("seeded replies %d, want 1", n)
	}

	if err := RemovePlayerGuestbookEntry(idH, entry, "x", "discord:1"); !errors.Is(err, ErrNoItem) {
		t.Fatalf("the host's route: err %v, want ErrNoItem", err)
	}
	if err := RemovePlayerGuestbookEntry(idW, other, "x", "discord:1"); !errors.Is(err, ErrNoItem) {
		t.Fatalf("another writer's entry: err %v, want ErrNoItem", err)
	}
	if _, _, _, entries := socialCounts(); entries != 2 {
		t.Fatalf("refused removals changed the entries to %d", entries)
	}

	if err := RemovePlayerGuestbookEntry(idW, entry, "wrong guestbook", "discord:1"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, entries := socialCounts(); entries != 1 {
		t.Fatalf("entries after the removal: %d, want 1", entries)
	}
	if n := countReplies(); n != 0 {
		t.Fatalf("replies after the removal: %d, want 0", n)
	}
	rows := auditRowsOf(t, "remove-guestbook")
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, `"host":"1106"`) || !strings.Contains(rows[0].Detail, `"number":"`+entry+`"`) {
		t.Fatalf("remove-guestbook audit = %+v", rows)
	}
}

// TestRemoveCommentOnlyThatComment: removing the player's comment leaves the post's other comments; the player's route
// refuses a comment another player wrote.
func TestRemoveCommentOnlyThatComment(t *testing.T) {
	socialDB(t)
	_, idW := staffAccount(t, "dev-cm", "1108", "Writer", 0)
	post := seedPost(t, "1109", "Owner", "owner's post")
	mine := seedComment(t, "1109", post, "1108", "my comment")
	theirs := seedComment(t, "1109", post, "1110", "their comment")
	if err := RemovePlayerComment(idW, theirs, "x", "discord:1"); !errors.Is(err, ErrNoItem) {
		t.Fatalf("another writer's comment: err %v, want ErrNoItem", err)
	}
	if err := RemovePlayerComment(idW, mine, "rude", "discord:1"); err != nil {
		t.Fatal(err)
	}
	if _, comments, _, _ := socialCounts(); comments != 1 {
		t.Fatalf("comments after the removal: %d, want 1", comments)
	}
	if err := RemovePlayerComment(idW, mine, "again", "discord:1"); !errors.Is(err, ErrNoItem) {
		t.Fatalf("a second removal: err %v, want ErrNoItem", err)
	}
	rows := auditRowsOf(t, "remove-comment")
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, `"host":"1109"`) || !strings.Contains(rows[0].Detail, `"reason":"rude"`) {
		t.Fatalf("remove-comment audit = %+v", rows)
	}
}

// TestPlayerWritingOfListsNewestFirst: the player's own posts, entries and comments, newest first and at most the limit;
// an account with no avatar has none.
func TestPlayerWritingOfListsNewestFirst(t *testing.T) {
	socialDB(t)
	_, id := staffAccount(t, "dev-pw", "1111", "Lister", 0)
	_, zero := staffAccount(t, "dev-pz", "0", "", 0)
	first := seedPost(t, "1111", "Lister", "first")
	second := seedPost(t, "1111", "Lister", "second")
	seedPost(t, "1112", "Else", "not mine")
	c1 := seedComment(t, "1112", second, "1111", "one")
	c2 := seedComment(t, "1112", second, "1111", "two")
	w, err := PlayerWritingOf(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Posts) != 1 || w.Posts[0].DiaryNo != second {
		t.Fatalf("posts at limit 1 = %+v, want only post %s", w.Posts, second)
	}
	if w, _ = PlayerWritingOf(id, 20); len(w.Comments) != 2 || w.Comments[0].CommentNo != c2 || w.Comments[1].CommentNo != c1 || len(w.Posts) != 2 || w.Posts[1].DiaryNo != first {
		t.Fatalf("writing = %+v", w)
	}
	if w, err := PlayerWritingOf(zero, 20); err != nil || len(w.Posts)+len(w.Guestbook)+len(w.Comments) != 0 {
		t.Fatalf("an account with no avatar: %+v, err %v", w, err)
	}
}

// TestRemovalFailureChangesNothing: with the database closed every removal fails and leaves the rows as they were.
func TestRemovalFailureChangesNothing(t *testing.T) {
	d := socialDB(t)
	_, id := staffAccount(t, "dev-rf", "1113", "Fails", 0)
	post := seedPost(t, "1113", "Fails", "keep me")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RemovePlayerPost(id, post, "x", "discord:1"); err == nil {
		t.Fatal("a removal with a failing save succeeded")
	}
	if posts, _, _, _ := socialCounts(); posts != 1 {
		t.Fatalf("a failed removal left %d posts, want 1", posts)
	}
}

// TestBanCallsOnBanOnlyOnASavedBan: OnBan gets the banned avatar id after a ban is saved, and is not called for a refused one.
func TestBanCallsOnBanOnlyOnASavedBan(t *testing.T) {
	socialDB(t)
	_, id := staffAccount(t, "dev-hk", "1114", "Hooked", 0)
	var got []string
	prev := OnBan
	OnBan = func(aid string) { got = append(got, aid) }
	t.Cleanup(func() { OnBan = prev })
	if err := BanPlayer(id, "spam", "discord:1"); err != nil {
		t.Fatal(err)
	}
	if err := BanPlayer(id, "again", "discord:1"); !errors.Is(err, ErrAlreadyBanned) {
		t.Fatalf("second ban: err %v", err)
	}
	if len(got) != 1 || got[0] != "1114" {
		t.Fatalf("OnBan calls = %v, want [1114]", got)
	}
}
