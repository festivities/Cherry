package lpn

import (
	"encoding/json"
	"slices"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// decodeVarintList extracts all uint64 values of repeated field from a protobuf message.
func decodeVarintList(t *testing.T, data []byte, field uint64) []uint64 {
	t.Helper()
	var out []uint64
	if !pbScan(data, func(f, v uint64, _ []byte) {
		if f == field {
			out = append(out, v)
		}
	}) {
		t.Fatalf("malformed protobuf %x", data)
	}
	return out
}

// expectAddUser consumes the adduser (msgid 2) and high-five target list (msgid 26) frames pushed to an existing
// room member when another player relays in, returning the newcomer's AID.
func expectAddUser(t *testing.T, c *sqClient) uint64 {
	t.Helper()
	id, body := c.read()
	if id != 2 {
		t.Fatalf("got msgid %d, want adduser (2)", id)
	}
	info, found, ok := pbBytesField(body, 1)
	if !ok || !found {
		t.Fatalf("adduser missing PlayerInfo: %x", body)
	}
	aid := squareUint(t, info, 1)
	if pid, _ := c.read(); pid != squareRcHighFiveTargetList {
		t.Fatalf("got msgid %d after adduser, want highfive (%d)", pid, squareRcHighFiveTargetList)
	}
	return aid
}

// TestSquareInvitedListAcrossThemes verifies cr37 -> rc46 works across regular Squares (LINE City, LINE Café,
// Disney Square), returns invited AIDs sorted after cr35, clears an invitee when they join, and clears the
// inviter's list when they leave.
func TestSquareInvitedListAcrossThemes(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"sq-8801": {AccessToken: "sq-8801", SessionKey: "sk-8801", Aid: "8801", Name: "Inviter"},
		"sq-8802": {AccessToken: "sq-8802", SessionKey: "sk-8802", Aid: "8802", Name: "FriendA"},
		"sq-8803": {AccessToken: "sq-8803", SessionKey: "sk-8803", Aid: "8803", Name: "FriendB"},
	})
	setAvatarFriends(t, [2]string{"8801", "8802"}, [2]string{"8801", "8803"})

	for _, theme := range []uint64{squareCityThemeID, 3, 10} {
		rooms := &squareRooms{}
		inviter := newSqClientIn(t, rooms, 1)
		inviter.joinTheme(8801, theme)

		inviter.send(37, nil)
		inviter.expect(46, nil)

		inviter.send(35, pbVar(pbVar(nil, 1, 8803), 1, 8802))
		if id, body := inviter.read(); id != 45 {
			t.Fatalf("theme %d: cr35 reply = rc%d, want rc45", theme, id)
		} else if got := decodeVarintList(t, body, 2); !slices.Equal(got, []uint64{8803, 8802}) {
			t.Fatalf("theme %d: rc45 InvitedList = %v, want [8803 8802]", theme, got)
		}

		inviter.send(37, nil)
		inviter.expect(46, pbVar(pbVar(nil, 1, 8802), 1, 8803))

		// Invitee 8802 enters the room -> 8802 is cleared from 8801's invited list.
		friendA := newSqClientIn(t, rooms, 2)
		friendA.joinTheme(8802, theme)
		if aid := expectAddUser(t, inviter); aid != 8802 {
			t.Fatalf("theme %d: adduser aid = %d, want 8802", theme, aid)
		}

		inviter.send(37, nil)
		inviter.expect(46, pbVar(nil, 1, 8803))

		// Inviter leaves -> 8801's remaining invites in the room are cleared.
		inviter.send(4, nil)
		inviter.expect(4, pbVar(nil, 1, 0))
		if id, _ := friendA.read(); id != 3 { // deluser
			t.Fatalf("theme %d: friendA got msgid %d, want deluser (3)", theme, id)
		}

		inviter.send(2, pbVar(nil, 1, theme))
		if id, _ := inviter.read(); id != 6 {
			t.Fatalf("theme %d: re-enter = rc%d, want rc6", theme, id)
		}
		inviter.send(5, nil)
		if id, _ := inviter.read(); id != 8 {
			t.Fatalf("theme %d: relaystart = rc%d, want rc8", theme, id)
		}
		if id, _ := inviter.read(); id != squareRcHighFiveTargetList {
			t.Fatalf("theme %d: highfive = rc%d, want rc%d", theme, id, squareRcHighFiveTargetList)
		}
		inviter.send(37, nil)
		inviter.expect(46, nil)
	}
}

// TestSquareInviteSendAndPush verifies cr35 in LINE Café (theme 3): filters out non-friends, self, zero,
// already-present room members, and duplicates; replies with rc45; and sends :10123 LO push pType "FIR".
func TestSquareInviteSendAndPush(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"sq-8811": {AccessToken: "sq-8811", SessionKey: "sk-8811", Aid: "8811", Name: "Inviter"},
		"sq-8812": {AccessToken: "sq-8812", SessionKey: "sk-8812", Aid: "8812", Name: "Friend"},
		"sq-8813": {AccessToken: "sq-8813", SessionKey: "sk-8813", Aid: "8813", Name: "Stranger"},
		"sq-8814": {AccessToken: "sq-8814", SessionKey: "sk-8814", Aid: "8814", Name: "Roommate"},
	})
	setAvatarFriends(t, [2]string{"8811", "8812"}, [2]string{"8811", "8814"})
	store.SocialMu.Lock()
	store.Friendships = append(store.Friendships, store.Friendship{A: "8811", B: "8813", State: "pending"})
	store.SocialMu.Unlock()

	rooms := &squareRooms{}
	inviter := newSqClientIn(t, rooms, 1)
	inviter.joinTheme(8811, 3)
	roommate := newSqClientIn(t, rooms, 2)
	roommate.joinTheme(8814, 3)
	if aid := expectAddUser(t, inviter); aid != 8814 {
		t.Fatalf("adduser aid = %d, want 8814", aid)
	}

	inviteeConn := inviteeSession(t, "sk-8812")

	var req []byte
	for _, a := range []uint64{8812, 8813, 8811, 8814, 0, 8812} {
		req = pbVar(req, 1, a)
	}
	inviter.send(35, req)

	id, body := inviter.read()
	if id != 45 {
		t.Fatalf("cr35 reply = rc%d, want rc45", id)
	}
	if res := squareUint(t, body, 1); res != 1 {
		t.Fatalf("rc45 Result = %d, want 1", res)
	}
	if got := decodeVarintList(t, body, 2); !slices.Equal(got, []uint64{8812}) {
		t.Fatalf("rc45 InvitedList = %v, want [8812]", got)
	}

	op, txn, payload := readLOPush(t, inviteeConn)
	if op != loOpPush || txn != 0 {
		t.Fatalf("push op=%#02x txn=%d, want 0x1b txn 0", op, txn)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("push payload is not JSON: %v", err)
	}
	if msgID, _ := got["msgid"].(string); msgID == "" {
		t.Fatalf("push msgid empty: %s", payload)
	}
	if got["catg"] != "square" {
		t.Fatalf("push catg = %v, want square", got["catg"])
	}
	if got["sender"] != "8811" {
		t.Fatalf("push sender = %v, want 8811", got["sender"])
	}
	if got["content"] != "Inviter invites you to LINE Café!" {
		t.Fatalf("push content = %v", got["content"])
	}
	params, _ := got["params"].(map[string]any)
	if params["pType"] != "FIR" {
		t.Fatalf("push params.pType = %v, want FIR", params["pType"])
	}
	mapParam, _ := got["mapParam"].(map[string]any)
	if mapParam["catg"] != "square" || mapParam["aid"] != "8811" {
		t.Fatalf("push mapParam = %v, want {catg:square aid:8811}", mapParam)
	}
	store.AccountsMu.Lock()
	acc8812 := store.AccountByAidLocked("8812")
	var gotAlert bool
	if acc8812 != nil && acc8812.News != nil && len(acc8812.News.Alerts) > 0 {
		a := acc8812.News.Alerts[0]
		gotAlert = a.Type == "SQUARE_INVITE_RECEIVE" && a.Actor == "8811" && a.ActorName == "Inviter"
	}
	store.AccountsMu.Unlock()
	if !gotAlert {
		t.Fatal("expected SQUARE_INVITE_RECEIVE news alert on aid 8812 from aid 8811")
	}
}

// TestSquareInviteAcceptFromGardenAndShard verifies cr36 from outside any Square (Garden/logged in only) enters
// the friend's exact theme and shard room (both public room 2 with SquareCode omitted and secret room 5032 with
// SquareCode=5032, plus LINE City with NPCs), and subsequent cr5 places both players in the same room.
func TestSquareInviteAcceptFromGardenAndShard(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8821, 8822, 8823, 8824, 8825, 8826))
	useRoomCap(t, 2)
	setAvatarFriends(t, [2]string{"8823", "8824"}, [2]string{"8825", "8826"})

	rooms := &squareRooms{}
	f1 := newSqClientIn(t, rooms, 1)
	f2 := newSqClientIn(t, rooms, 2)
	c1 := f1.enterAt(8821, 3, 0)
	if no := f2.enterAt(8822, 3, 0); no != c1 { // public room 1 now full (2/2)
		t.Fatalf("f2 placed in room %d, want %d", no, c1)
	}
	if aid := expectAddUser(t, f1); aid != 8822 {
		t.Fatalf("f1 adduser aid = %d, want 8822", aid)
	}

	friend := newSqClientIn(t, rooms, 3)
	friendRoom := friend.enterAt(8823, 3, 0)
	if friendRoom != 2 {
		t.Fatalf("friend placed in room %d, want 2", friendRoom)
	}
	friend.send(35, pbVar(nil, 1, 8824))
	if id, _ := friend.read(); id != 45 {
		t.Fatalf("cr35 reply = rc%d, want rc45", id)
	}

	// Invitee is in Garden (logged in only, no cr2 sent).
	invitee := newSqClientIn(t, rooms, 4)
	invitee.send(0, loginBody(8824))
	invitee.expect(0, []byte{0x08, 0x81, 0x80, 0x08})

	invitee.send(36, pbVar(pbVar(nil, 1, 8823), 2, 0))
	id, enter := invitee.read()
	if id != 6 {
		t.Fatalf("cr36 reply = rc%d, want rc6", id)
	}
	if hasNpc := invitee.checkEnter(enter, 3, squareTestMaps[3]); hasNpc {
		t.Fatalf("LINE Café enter unexpectedly carried NpcList")
	}
	if code := enterRoom(enter); code != 0 {
		t.Fatalf("public cr36 SquareCode = %d, want 0 (omitted)", code)
	}

	invitee.send(5, nil)
	id, roster := invitee.read()
	if id != 8 {
		t.Fatalf("cr5 reply = rc%d, want rc8", id)
	}
	if aids := rosterAids(roster[len(relayResultBytes()):]); !slices.Contains(aids, uint64(8823)) {
		t.Fatalf("invitee roster = %v, want friend 8823", aids)
	}
	if id, _ := invitee.read(); id != squareRcHighFiveTargetList {
		t.Fatalf("invitee got msgid %d, want highfive (%d)", id, squareRcHighFiveTargetList)
	}
	if invitee.sess.roomNo != friendRoom || invitee.sess.roomSecret {
		t.Fatalf("invitee room=(%d, secret=%v), want (%d, false)", invitee.sess.roomNo, invitee.sess.roomSecret, friendRoom)
	}

	// Friend in public room 2 gets adduser for invitee; room 1 players hear nothing.
	if aid := expectAddUser(t, friend); aid != 8824 {
		t.Fatalf("adduser aid = %d, want 8824", aid)
	}
	f1.none()
	f2.none()

	// Inviting into a private secret shard (5032): rc6 carries SquareCode=5032 and cr5 joins secret room 5032.
	secretHost := newSqClientIn(t, rooms, 5)
	if no := secretHost.enterAt(8825, 3, 5032); no != 5032 || !secretHost.sess.roomSecret {
		t.Fatalf("secretHost room=(%d, secret=%v), want (5032, true)", no, secretHost.sess.roomSecret)
	}
	secretHost.send(35, pbVar(nil, 1, 8826))
	if id, _ := secretHost.read(); id != 45 {
		t.Fatalf("secret cr35 reply = rc%d, want rc45", id)
	}
	secretInvitee := newSqClientIn(t, rooms, 6)
	secretInvitee.send(0, loginBody(8826))
	secretInvitee.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	secretInvitee.send(36, pbVar(nil, 1, 8825))
	id, enter = secretInvitee.read()
	if id != 6 {
		t.Fatalf("secret cr36 reply = rc%d, want rc6", id)
	}
	secretInvitee.checkEnter(enter, 3, squareTestMaps[3])
	if code := enterRoom(enter); code != 5032 {
		t.Fatalf("secret cr36 SquareCode = %d, want 5032", code)
	}
	secretInvitee.send(5, nil)
	id, roster = secretInvitee.read()
	if id != 8 {
		t.Fatalf("secret cr5 reply = rc%d, want rc8", id)
	}
	if aids := rosterAids(roster[len(relayResultBytes()):]); !slices.Contains(aids, uint64(8825)) {
		t.Fatalf("secretInvitee roster = %v, want 8825", aids)
	}
	if id, _ := secretInvitee.read(); id != squareRcHighFiveTargetList {
		t.Fatalf("secretInvitee got msgid %d, want highfive (%d)", id, squareRcHighFiveTargetList)
	}
	if !secretInvitee.sess.roomSecret || secretInvitee.sess.roomNo != 5032 {
		t.Fatalf("secretInvitee room=(%d, secret=%v), want (5032, true)", secretInvitee.sess.roomNo, secretInvitee.sess.roomSecret)
	}
	if aid := expectAddUser(t, secretHost); aid != 8826 {
		t.Fatalf("secretHost adduser aid = %d, want 8826", aid)
	}

	// Also verify LINE City (theme 1) cr36 includes its NpcList.
	cityRooms := &squareRooms{}
	cityFriend := newSqClientIn(t, cityRooms, 7)
	cityFriend.enterAt(8821, squareCityThemeID, 0)
	setAvatarFriends(t, [2]string{"8821", "8822"})
	cityInvitee := newSqClientIn(t, cityRooms, 8)
	cityInvitee.send(0, loginBody(8822))
	cityInvitee.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	cityInvitee.send(36, pbVar(nil, 1, 8821))
	id, enter = cityInvitee.read()
	if id != 6 {
		t.Fatalf("city cr36 reply = rc%d, want rc6", id)
	}
	if hasNpc := cityInvitee.checkEnter(enter, squareCityThemeID, squareTestMaps[squareCityThemeID]); !hasNpc {
		t.Fatalf("LINE City cr36 enter missing NpcList")
	}
}

// TestSquareInviteAcceptSwitchesSquare verifies cr36 while already in another Square (Treasure Island -> LINE Café)
// leaves the old room (deluser broadcast to remaining player) and enters the friend's Square and room.
func TestSquareInviteAcceptSwitchesSquare(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"sq-8831": {AccessToken: "sq-8831", SessionKey: "sk-8831", Aid: "8831", Name: "Friend"},
		"sq-8832": {AccessToken: "sq-8832", SessionKey: "sk-8832", Aid: "8832", Name: "Invitee"},
		"sq-8833": {AccessToken: "sq-8833", SessionKey: "sk-8833", Aid: "8833", Name: "Bystander"},
	})
	setAvatarFriends(t, [2]string{"8831", "8832"})

	rooms := &squareRooms{}
	friend := newSqClientIn(t, rooms, 1)
	friend.joinTheme(8831, 3)
	friendCode := friend.sess.roomNo

	bystander := newSqClientIn(t, rooms, 2)
	bystander.joinTheme(8833, squareThemeID)
	invitee := newSqClientIn(t, rooms, 3)
	invitee.joinTheme(8832, squareThemeID)
	if aid := expectAddUser(t, bystander); aid != 8832 {
		t.Fatalf("bystander adduser aid = %d, want 8832", aid)
	}

	friend.send(35, pbVar(nil, 1, 8832))
	if id, _ := friend.read(); id != 45 {
		t.Fatalf("cr35 reply = rc%d, want rc45", id)
	}

	invitee.send(36, pbVar(nil, 1, 8831))
	id, enter := invitee.read()
	if id != 6 {
		t.Fatalf("cr36 reply = rc%d, want rc6", id)
	}
	invitee.checkEnter(enter, 3, squareTestMaps[3])
	if enterRoom(enter) != 0 {
		t.Fatalf("public cr36 SquareCode = %d, want 0 (omitted)", enterRoom(enter))
	}
	_ = friendCode

	// Bystander on Treasure Island received deluser for invitee.
	if id, body := bystander.read(); id != 3 || squareUint(t, body, 1) != 8832 {
		t.Fatalf("bystander got msgid=%d body=%x, want deluser (3) for 8832", id, body)
	}

	// Invitee relays into LINE Café room 1 and appears to friend.
	invitee.send(5, nil)
	if id, _ := invitee.read(); id != 8 {
		t.Fatalf("cr5 reply = rc%d, want rc8", id)
	}
	if id, _ := friend.read(); id != 2 {
		t.Fatalf("friend got msgid %d, want adduser (2)", id)
	}
}

// TestSquareInviteAcceptRefusals verifies cr36 returns rc6 Result=squareEnterNotInSquare (196629) for
// offline/not-in-square friends and Result=0 for strangers, self, and zero AID.
func TestSquareInviteAcceptRefusals(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"sq-8841": {AccessToken: "sq-8841", SessionKey: "sk-8841", Aid: "8841", Name: "InSquare"},
		"sq-8842": {AccessToken: "sq-8842", SessionKey: "sk-8842", Aid: "8842", Name: "Stranger"},
		"sq-8843": {AccessToken: "sq-8843", SessionKey: "sk-8843", Aid: "8843", Name: "OfflineFriend"},
		"sq-8844": {AccessToken: "sq-8844", SessionKey: "sk-8844", Aid: "8844", Name: "Requester"},
	})
	setAvatarFriends(t, [2]string{"8843", "8844"})

	rooms := &squareRooms{}
	inSq := newSqClientIn(t, rooms, 1)
	inSq.joinTheme(8841, 3)

	req := newSqClientIn(t, rooms, 2)
	req.send(0, loginBody(8844))
	req.expect(0, []byte{0x08, 0x81, 0x80, 0x08})

	for _, tc := range []struct {
		name  string
		owner uint64
		want  uint64
	}{
		{"stranger in square", 8841, 0},
		{"offline friend", 8843, squareEnterNotInSquare},
		{"self", 8844, 0},
		{"zero", 0, 0},
	} {
		req.send(36, pbVar(nil, 1, tc.owner))
		id, body := req.read()
		if id != 6 || squareUint(t, body, 1) != tc.want {
			t.Fatalf("%s: cr36 reply = rc%d/%x, want rc6 Result=%d", tc.name, id, body, tc.want)
		}
	}

	// Self while inside a Square is also refused.
	inSq.send(36, pbVar(nil, 1, 8841))
	id, body := inSq.read()
	if id != 6 || squareUint(t, body, 1) != 0 {
		t.Fatalf("self inside square: cr36 reply = rc%d/%x, want rc6 Result=0", id, body)
	}
}

// TestSquareInviteAcceptRoomFullRefused verifies that when a friend's room reaches squareRoomCap before the
// invitee accepts cr36, cr36 returns rc6 Result=squareEnterFull (196617) instead of placing the invitee elsewhere.
func TestSquareInviteAcceptRoomFullRefused(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8851, 8852, 8853))
	useRoomCap(t, 2)
	setAvatarFriends(t, [2]string{"8851", "8853"})

	rooms := &squareRooms{}
	friend := newSqClientIn(t, rooms, 1)
	if no := friend.enterAt(8851, 3, 0); no != 1 { // room 1 (1/2)
		t.Fatalf("friend placed in room %d, want 1", no)
	}

	friend.send(35, pbVar(nil, 1, 8853))
	if id, _ := friend.read(); id != 45 {
		t.Fatalf("cr35 reply = rc%d, want rc45", id)
	}

	// Another player fills room 1 (2/2) before invitee accepts.
	filler := newSqClientIn(t, rooms, 2)
	if no := filler.enterAt(8852, 3, 0); no != 1 {
		t.Fatalf("filler placed in room %d, want 1", no)
	}

	invitee := newSqClientIn(t, rooms, 3)
	invitee.send(0, loginBody(8853))
	invitee.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	invitee.send(36, pbVar(nil, 1, 8851))
	id, body := invitee.read()
	if id != 6 || squareUint(t, body, 1) != squareEnterFull {
		t.Fatalf("cr36 reply = rc%d/%x, want rc6 Result=%d", id, body, squareEnterFull)
	}
	if got := roomNumbers(rooms, 3); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("rooms = %v, want [1] (no fallback room created)", got)
	}
}
