package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"cherry/internal/auth"
	"cherry/internal/store"
)

// putSocial writes one social row of a test into memory and saves it, the way a game request does. kind is the list the row
// goes to: "post", "comment", "like" or "entry" (a guestbook entry on host's book, written by guest).
func putSocial(t *testing.T, kind, no, owner, other string) {
	t.Helper()
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	var c store.SocialChange
	switch kind {
	case "post":
		store.DiaryPosts = append([]store.DiaryPost{{DiaryNo: no, AvatarID: owner, Nickname: "n", RegDate: "1700000000000",
			Content: "post " + no, Images: []store.DiaryImage{}}}, store.DiaryPosts...)
		c.Posts = []string{no}
	case "comment": // writer owner, on the post of other
		store.DiaryComments = append(store.DiaryComments, store.DiaryComment{CommentNo: no, DiaryNo: other, OwnerAvatarID: postOwnerOf(other),
			CommentAvatarID: owner, Content: "comment", Date: "1700000000000"})
		c.Comments = []string{no}
	case "like": // liker owner, on the post of other
		store.DiaryLikes = append(store.DiaryLikes, store.DiaryLike{LikeNo: no, DiaryNo: other, OwnerAvatarID: postOwnerOf(other), LikeAvatarID: owner, LikeType: 1})
		c.Likes = []string{no}
	case "entry":
		store.GuestbookEntries = append([]store.SavedGuestbookEntry{{HostAvtNo: owner, GuestHistNo: no, GuestAvtNo: other,
			GuestNickname: "g", Content: "entry", RegYmdt: "1700000000000"}}, store.GuestbookEntries...)
		c.Entries = []string{no}
	}
	c.Scalars = true
	if err := store.SaveSocialLocked(c); err != nil {
		t.Fatal(err)
	}
}

// putReply writes a reply by replier on the entry parent (reply history number no).
func putReply(t *testing.T, parent, no, replier string) {
	t.Helper()
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	for i := range store.GuestbookEntries {
		if store.GuestbookEntries[i].GuestHistNo == parent {
			store.GuestbookEntries[i].Reply = append(store.GuestbookEntries[i].Reply, store.GuestbookReply{ReplyHistNo: no, ParentHistNo: parent, ReplyAvtNo: replier, Content: "reply"})
		}
	}
	if err := store.SaveSocialLocked(store.SocialChange{Replies: []string{no}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
}

// deleteAvatarOverHTTP runs Delete Avatar the way the game does (removeCode, then the confirm with the key and the code).
func deleteAvatarOverHTTP(t *testing.T, av, aid string) {
	t.Helper()
	rec := socialTestRequestAs(t, av, http.MethodGet, "/v4/avatar/"+aid+"/removeCode", "")
	var del struct {
		Result struct {
			Key  string `json:"key"`
			Code int    `json:"code"`
		} `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &del) != nil || del.Result.Key == "" {
		t.Fatalf("removeCode = %d %s", rec.Code, rec.Body.String())
	}
	if rec := socialTestRequestAs(t, av, http.MethodPost, "/v4/r/avatar/"+aid+"/"+del.Result.Key+"/"+strconv.Itoa(del.Result.Code), "null"); rec.Code != http.StatusOK {
		t.Fatalf("Delete Avatar = %d %s", rec.Code, rec.Body.String())
	}
}

// makeAvatar signs the identity as in up, creates its avatar named name and returns the avatar's id and its AV_AUTH value.
func makeAvatar(t *testing.T, game http.Handler, as, name string) (aid, av string) {
	t.Helper()
	tok := guestGenerateCode(t, devSignUp(t, game, as))
	av = avAuthValue(t, createSession(t, tok))
	aid = createAvatarID(t, av, `{"name":"`+name+`","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON00164"]}`)
	return aid, av
}

// recordOf is the deleted record of aid.
func recordOf(t *testing.T, aid string) store.DeletedAvatar {
	t.Helper()
	d, ok, err := store.DeletedAvatarOfAid(aid)
	if err != nil || !ok {
		t.Fatalf("no record of avatar %s: %v", aid, err)
	}
	return d
}

// TestRestoreBringsTheRecordBack: an avatar with posts, comments, likes, guestbook entries (its own and one it wrote), a reply, a
// friendship, a read marker and a picture is deleted by the game, then restored to its identity: the account, the social rows, the
// friendship and the picture are back, a second restore is refused, and a sign-in reaches the avatar again.
func TestRestoreBringsTheRecordBack(t *testing.T) {
	cfg := devFakeConfig()
	useConfig(t, cfg)
	guestAccountTest(t)
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	svc, err := auth.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	game := NewGameMux(svc)

	aidA, avA := makeAvatar(t, game, "ida", "Ida")
	aidB, _ := makeAvatar(t, game, "idb", "Idb")
	putSocial(t, "post", "5001", aidA, "")
	putSocial(t, "post", "6001", aidB, "")
	putSocial(t, "comment", "7001", aidA, "6001")
	putSocial(t, "like", "7002", aidA, "6001")
	putSocial(t, "entry", "8001", aidB, aidA) // A wrote on B's guestbook
	putReply(t, "8001", "9001", aidB)         // B answered it
	putSocial(t, "entry", "8002", aidB, aidB) // B's own entry, with a reply by A
	putReply(t, "8002", "9002", aidA)
	store.SocialMu.Lock()
	if store.GuestbookRead == nil {
		store.GuestbookRead = map[string]uint64{}
	}
	store.GuestbookRead[aidA] = 3
	store.Friendships = append(store.Friendships, store.Friendship{A: aidA, B: aidB, State: "accepted"})
	store.SocialMu.Unlock()
	if err := store.SaveSocialLocked(store.SocialChange{Reads: []string{aidA}, Friends: [][2]string{{aidA, aidB}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMediaFile(store.MediaFile{Kind: "diary", Owner: aidA, File: "pic-a.img", Size: 10}); err != nil {
		t.Fatal(err)
	}

	deleteAvatarOverHTTP(t, avA, aidA)
	if !store.PictureOfDeletedAvatar("pic-a.img") {
		t.Fatal("the picture of a deleted avatar is served")
	}
	rec := recordOf(t, aidA)

	plan, err := store.RestoreAvatar(rec.ID, "dev-ida", "dev-owner")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	want := store.RestoreCounts{Posts: 1, Entries: 1, Replies: 2, Comments: 1, Likes: 1, Friendships: 1, ReadMarker: true}
	if plan.Counts != want || len(plan.Skipped) != 0 {
		t.Fatalf("restore counts = %+v skipped %v, want %+v and none", plan.Counts, plan.Skipped, want)
	}
	if store.PictureOfDeletedAvatar("pic-a.img") {
		t.Fatal("the restored avatar's picture is still hidden")
	}
	store.SocialMu.Lock()
	postBack := store.DiaryPosts[0].DiaryNo == "5001" && store.DiaryPosts[0].AvatarID == aidA
	var entry store.SavedGuestbookEntry
	for _, e := range store.GuestbookEntries {
		if e.GuestHistNo == "8001" {
			entry = e
		}
	}
	friendOK := store.FriendshipIndex(aidA, aidB, "accepted") >= 0
	readOK := store.GuestbookRead[aidA] == 3
	store.SocialMu.Unlock()
	if !postBack || len(entry.Reply) != 1 || entry.Reply[0].ReplyHistNo != "9001" || !friendOK || !readOK {
		t.Fatalf("social rows after the restore: post %v, entry replies %d, friendship %v, read marker %v", postBack, len(entry.Reply), friendOK, readOK)
	}
	store.AccountsMu.Lock()
	acc := store.IdentityAccountLocked("dev-ida")
	aidOK := acc != nil && acc.Aid == aidA && acc.Name == "Ida"
	store.AccountsMu.Unlock()
	if !aidOK {
		t.Fatal("the identity's account is not the restored avatar")
	}
	if _, err := store.RestoreAvatar(rec.ID, "dev-ida", "dev-owner"); !store.IsRefused(err) {
		t.Fatalf("a second restore = %v; want a refusal", err)
	}

	// The identity signs in again and reaches the restored avatar.
	tok := guestGenerateCode(t, devSignUp(t, game, "ida"))
	if sess := sessionOf(t, tok); sess.Aid != aidA {
		t.Fatalf("sign-in after the restore reached aid %s, want %s", sess.Aid, aidA)
	}
}

// TestRestoreToAnotherIdentitySkipsWhatIsGone: a deleted avatar restored to an identity with no account gets one; the rows whose
// other side is gone (a comment on a deleted post, a friendship with a deleted avatar) are left out and listed with their reasons.
func TestRestoreToAnotherIdentitySkipsWhatIsGone(t *testing.T) {
	cfg := devFakeConfig()
	useConfig(t, cfg)
	guestAccountTest(t)
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	svc, err := auth.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	game := NewGameMux(svc)

	aidB, avB := makeAvatar(t, game, "idb", "Idb")
	aidC, avC := makeAvatar(t, game, "idc", "Idc")
	putSocial(t, "post", "6001", aidB, "")
	putSocial(t, "post", "6101", aidC, "")
	putSocial(t, "comment", "7101", aidC, "6001") // C's comment on B's post
	if err := store.SaveSocialLocked(store.SocialChange{Friends: [][2]string{{aidB, aidC}}}); err != nil {
		t.Fatal(err)
	}
	store.SocialMu.Lock()
	store.Friendships = append(store.Friendships, store.Friendship{A: aidB, B: aidC, State: "accepted"})
	store.SocialMu.Unlock()
	if err := store.SaveSocialLocked(store.SocialChange{Friends: [][2]string{{aidB, aidC}}}); err != nil {
		t.Fatal(err)
	}

	deleteAvatarOverHTTP(t, avC, aidC) // C's record keeps the comment and the accepted friendship
	deleteAvatarOverHTTP(t, avB, aidB) // B's post goes with B; C's friendship with B is marked removed
	recC := recordOf(t, aidC)

	plan, err := store.PlanRestore(recC.ID, "dev-idd")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	reasons := map[string]string{}
	for _, s := range plan.Skipped {
		reasons[s.Kind+" "+s.Ref] = s.Reason
	}
	if reasons["comment 7101"] != "its post is gone" || reasons["friendship "+aidB] != "the other avatar is gone" {
		t.Fatalf("skipped rows = %v", reasons)
	}
	if plan.Counts.Posts != 1 || plan.Counts.Comments != 0 || plan.Counts.Friendships != 0 || plan.TargetID != 0 {
		t.Fatalf("plan = %+v, want the post back, nothing else, and a new row", plan)
	}
	if _, err := store.RestoreAvatar(recC.ID, "dev-idd", "dev-owner"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	store.AccountsMu.Lock()
	acc := store.IdentityAccountLocked("dev-idd")
	ok := acc != nil && acc.Aid == aidC
	store.AccountsMu.Unlock()
	if !ok {
		t.Fatal("the restored avatar did not get a row for dev-idd")
	}
	if tok := guestGenerateCode(t, devSignUp(t, game, "idd")); sessionOf(t, tok).Aid != aidC {
		t.Fatal("a sign-in as dev-idd does not reach the restored avatar")
	}
}

// postOwnerOf is the avatar id of the diary post no. Caller holds SocialMu.
func postOwnerOf(no string) string {
	for _, p := range store.DiaryPosts {
		if p.DiaryNo == no {
			return p.AvatarID
		}
	}
	return ""
}
