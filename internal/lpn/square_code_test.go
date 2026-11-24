package lpn

import (
	"slices"
	"testing"

	"cherry/internal/testutil"
)

// TestSquareThemeListEnablesCodeSquare verifies cr1 -> rc1 sets field 2 (code_squred_enabled) = 1 on every listed Square.
func TestSquareThemeListEnablesCodeSquare(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8901))
	rooms := &squareRooms{}
	c := newSqClientIn(t, rooms, 1)
	c.send(0, loginBody(8901))
	c.expect(0, []byte{0x08, 0x81, 0x80, 0x08})

	c.send(1, pbVar(nil, 1, 0))
	id, body := c.read()
	if id != 1 {
		t.Fatalf("themelist msgid = %d, want 1", id)
	}
	var count int
	ok := pbScan(body, func(f, _ uint64, d []byte) {
		if f != 1 || d == nil {
			return
		}
		count++
		codeEnabled, found := protobufUintField(d, 2)
		if !found || codeEnabled != 1 {
			themeID, _ := protobufUintField(d, 1)
			t.Fatalf("theme %d code_squred_enabled = %d (found=%v), want 1", themeID, codeEnabled, found)
		}
	})
	if !ok || count == 0 {
		t.Fatalf("themelist ok=%v count=%d body=%x", ok, count, body)
	}
}

// TestSquarePublicAndSecretPoolsSeparate verifies normal GO enters public shard 1 (rc6 omits field 3),
// key-button SquareCode=1 enters secret shard 1 (rc6 sets field 3=1), the two pools are isolated from
// each other, and population sums across both pools.
func TestSquarePublicAndSecretPoolsSeparate(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8901, 8902, 8903, 8904))
	rooms := &squareRooms{}

	pubA := newSqClientIn(t, rooms, 1)
	if no := pubA.enterAt(8901, 3, 0); no != 1 || pubA.sess.roomSecret {
		t.Fatalf("pubA room=(%d, secret=%v), want (1, false)", no, pubA.sess.roomSecret)
	}

	secB := newSqClientIn(t, rooms, 2)
	if no := secB.enterAt(8902, 3, 1); no != 1 || !secB.sess.roomSecret {
		t.Fatalf("secB room=(%d, secret=%v), want (1, true)", no, secB.sess.roomSecret)
	}
	if pubA.sess.room == secB.sess.room {
		t.Fatalf("public room 1 and secret room 1 must be distinct room objects")
	}
	pubA.none()
	secB.none()

	// Chat in public room 1 does not reach secret room 1 and vice versa.
	pubA.send(8, pbLen(nil, 1, []byte("public")))
	if id, _ := pubA.read(); id != 11 {
		t.Fatalf("pubA chat msgid = %d, want 11", id)
	}
	secB.none()

	secB.send(8, pbLen(nil, 1, []byte("secret")))
	if id, _ := secB.read(); id != 11 {
		t.Fatalf("secB chat msgid = %d, want 11", id)
	}
	pubA.none()

	// Second secret-code player joins secB in secret room 1; pubA hears nothing.
	secC := newSqClientIn(t, rooms, 3)
	if no := secC.enterAt(8903, 3, 1); no != 1 || !secC.sess.roomSecret {
		t.Fatalf("secC room=(%d, secret=%v), want (1, true)", no, secC.sess.roomSecret)
	}
	if aid := expectAddUser(t, secB); aid != 8903 {
		t.Fatalf("secB adduser aid = %d, want 8903", aid)
	}
	pubA.none()

	// Second normal-GO player joins pubA in public room 1; secret players hear nothing.
	pubD := newSqClientIn(t, rooms, 4)
	if no := pubD.enterAt(8904, 3, 0); no != 1 || pubD.sess.roomSecret {
		t.Fatalf("pubD room=(%d, secret=%v), want (1, false)", no, pubD.sess.roomSecret)
	}
	if aid := expectAddUser(t, pubA); aid != 8904 {
		t.Fatalf("pubA adduser aid = %d, want 8904", aid)
	}
	secB.none()
	secC.none()

	if pop := rooms.population(3); pop != 4 {
		t.Fatalf("population(3) = %d, want 4 across public + secret", pop)
	}
}

// TestSquareEnterByCodeJoinsFriendRoom verifies entering a valid 4-digit SquareCode (5032) creates secret
// shard 5032, a friend entering 5032 joins the exact same secret shard, and normal-GO players fill public
// shards 1, 2, ... without ever landing in secret shards 0001 or 5032.
func TestSquareEnterByCodeJoinsFriendRoom(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8911, 8912, 8913, 8914, 8915))
	useRoomCap(t, 2)
	rooms := &squareRooms{}

	a := newSqClientIn(t, rooms, 1)
	if no := a.enterAt(8911, 3, 5032); no != 5032 || !a.sess.roomSecret {
		t.Fatalf("player A room=(%d, secret=%v), want (5032, true)", no, a.sess.roomSecret)
	}

	b := newSqClientIn(t, rooms, 2)
	b.send(0, loginBody(8912))
	b.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	b.send(2, pbVar(pbVar(nil, 1, 3), 2, 5032))
	id, enter := b.read()
	if id != 6 {
		t.Fatalf("player B cr2 reply = rc%d, want rc6", id)
	}
	b.checkEnter(enter, 3, squareTestMaps[3])
	if code := enterRoom(enter); code != 5032 {
		t.Fatalf("player B rc6 SquareCode = %d, want 5032", code)
	}
	b.send(5, nil)
	id, roster := b.read()
	if id != 8 {
		t.Fatalf("player B cr5 reply = rc%d, want rc8", id)
	}
	if aids := rosterAids(roster[len(relayResultBytes()):]); !slices.Contains(aids, uint64(8911)) {
		t.Fatalf("player B roster = %v, want 8911", aids)
	}
	if id, _ := b.read(); id != squareRcHighFiveTargetList {
		t.Fatalf("player B got msgid %d, want highfive (%d)", id, squareRcHighFiveTargetList)
	}
	if aid := expectAddUser(t, a); aid != 8912 {
		t.Fatalf("player A adduser aid = %d, want 8912", aid)
	}

	// Normal GO players fill public 1, then public 2, never landing in secret 5032.
	p1 := newSqClientIn(t, rooms, 3)
	p2 := newSqClientIn(t, rooms, 4)
	p3 := newSqClientIn(t, rooms, 5)
	if no := p1.enterAt(8913, 3, 0); no != 1 || p1.sess.roomSecret {
		t.Fatalf("normal GO #1 room=(%d, secret=%v), want (1, false)", no, p1.sess.roomSecret)
	}
	if no := p2.enterAt(8914, 3, 0); no != 1 || p2.sess.roomSecret {
		t.Fatalf("normal GO #2 room=(%d, secret=%v), want (1, false)", no, p2.sess.roomSecret)
	}
	if no := p3.enterAt(8915, 3, 0); no != 2 || p3.sess.roomSecret {
		t.Fatalf("normal GO #3 room=(%d, secret=%v), want (2, false)", no, p3.sess.roomSecret)
	}
	a.none()
	b.none()
	if got := roomNumbers(rooms, 3); !slices.Equal(got, []uint64{1, 2}) {
		t.Fatalf("public rooms = %v, want [1 2]", got)
	}
}

// TestSquareEnterByCodeSwitchesRoomInSameSquare verifies switching public -> secret, secret -> another secret,
// and secret -> public in the same Square cleanly leaves the old room (deluser to remaining member) and enters the new one.
func TestSquareEnterByCodeSwitchesRoomInSameSquare(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8921, 8922))
	rooms := &squareRooms{}

	bystander := newSqClientIn(t, rooms, 1)
	if no := bystander.enterAt(8921, 3, 0); no != 1 {
		t.Fatalf("bystander placed in room %d, want 1", no)
	}
	a := newSqClientIn(t, rooms, 2)
	if no := a.enterAt(8922, 3, 0); no != 1 {
		t.Fatalf("player A placed in room %d, want 1", no)
	}
	if aid := expectAddUser(t, bystander); aid != 8922 {
		t.Fatalf("bystander adduser aid = %d, want 8922", aid)
	}

	// 1) Public 1 -> Secret 1 (must leave public 1 so bystander receives deluser).
	a.send(2, pbVar(pbVar(nil, 1, 3), 2, 1))
	id, enter := a.read()
	if id != 6 {
		t.Fatalf("public->secret cr2 reply = rc%d, want rc6", id)
	}
	a.checkEnter(enter, 3, squareTestMaps[3])
	if code := enterRoom(enter); code != 1 {
		t.Fatalf("public->secret rc6 SquareCode = %d, want 1", code)
	}
	if id, body := bystander.read(); id != 3 || squareUint(t, body, 1) != 8922 {
		t.Fatalf("bystander got msgid=%d body=%x, want deluser (3) for 8922", id, body)
	}
	a.send(5, nil)
	id, roster := a.read()
	if id != 8 {
		t.Fatalf("public->secret cr5 reply = rc%d, want rc8", id)
	}
	if aids := rosterAids(roster[len(relayResultBytes()):]); !slices.Equal(aids, []uint64{8922}) {
		t.Fatalf("secret room 1 roster = %v, want [8922]", aids)
	}
	if !a.sess.roomSecret || a.sess.roomNo != 1 {
		t.Fatalf("after public->secret room=(%d, secret=%v), want (1, true)", a.sess.roomNo, a.sess.roomSecret)
	}

	// 2) Secret 1 -> Secret 5032 (secret 1 is pruned).
	a.send(2, pbVar(pbVar(nil, 1, 3), 2, 5032))
	id, enter = a.read()
	if id != 6 {
		t.Fatalf("secret->secret cr2 reply = rc%d, want rc6", id)
	}
	a.checkEnter(enter, 3, squareTestMaps[3])
	if code := enterRoom(enter); code != 5032 {
		t.Fatalf("secret->secret rc6 SquareCode = %d, want 5032", code)
	}
	a.send(5, nil)
	if id, _ := a.read(); id != 8 {
		t.Fatalf("secret->secret cr5 reply = rc%d, want rc8", id)
	}
	if !a.sess.roomSecret || a.sess.roomNo != 5032 {
		t.Fatalf("after secret->secret room=(%d, secret=%v), want (5032, true)", a.sess.roomNo, a.sess.roomSecret)
	}

	// 3) Secret 5032 -> Public 1 via normal GO (rc6 omits field 3, bystander gets adduser).
	a.send(2, pbVar(nil, 1, 3))
	id, enter = a.read()
	if id != 6 {
		t.Fatalf("secret->public cr2 reply = rc%d, want rc6", id)
	}
	a.checkEnter(enter, 3, squareTestMaps[3])
	if code := enterRoom(enter); code != 0 {
		t.Fatalf("secret->public rc6 SquareCode = %d, want 0 (omitted)", code)
	}
	a.send(5, nil)
	if id, _ := a.read(); id != 8 {
		t.Fatalf("secret->public cr5 reply = rc%d, want rc8", id)
	}
	if id, _ := a.read(); id != squareRcHighFiveTargetList {
		t.Fatalf("secret->public got msgid %d, want highfive (%d)", id, squareRcHighFiveTargetList)
	}
	if a.sess.roomSecret || a.sess.roomNo != 1 {
		t.Fatalf("after secret->public room=(%d, secret=%v), want (1, false)", a.sess.roomNo, a.sess.roomSecret)
	}
	if aid := expectAddUser(t, bystander); aid != 8922 {
		t.Fatalf("bystander adduser after return = %d, want 8922", aid)
	}
}

// TestSquareEnterByCodeRefusals verifies SquareCode 0 and >=10000 return squareEnterUnavailable without disturbing
// an active session, a full secret shard returns squareEnterFull, and a member already in that full shard may re-enter.
func TestSquareEnterByCodeRefusals(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8931, 8932, 8933, 8934, 8935))
	useRoomCap(t, 2)
	rooms := &squareRooms{}

	a := newSqClientIn(t, rooms, 1)
	if no := a.enterAt(8931, squareThemeID, 0); no != 1 {
		t.Fatalf("player A room = %d, want 1", no)
	}
	roommate := newSqClientIn(t, rooms, 2)
	if no := roommate.enterAt(8932, squareThemeID, 0); no != 1 {
		t.Fatalf("roommate room = %d, want 1", no)
	}
	if aid := expectAddUser(t, a); aid != 8932 {
		t.Fatalf("player A adduser aid = %d, want 8932", aid)
	}

	// Secret shard 2 is full (cap 2).
	r2a := newSqClientIn(t, rooms, 3)
	r2b := newSqClientIn(t, rooms, 4)
	if no := r2a.enterAt(8933, squareThemeID, 2); no != 2 || !r2a.sess.roomSecret {
		t.Fatalf("r2a room=(%d, secret=%v), want (2, true)", no, r2a.sess.roomSecret)
	}
	if no := r2b.enterAt(8934, squareThemeID, 2); no != 2 || !r2b.sess.roomSecret {
		t.Fatalf("r2b room=(%d, secret=%v), want (2, true)", no, r2b.sess.roomSecret)
	}
	if aid := expectAddUser(t, r2a); aid != 8934 {
		t.Fatalf("r2a adduser aid = %d, want 8934", aid)
	}

	// SquareCode = 0 ("0000") and 10000 are refused with squareEnterUnavailable and leave A's session intact.
	for _, bad := range []uint64{0, 10000} {
		a.send(2, pbVar(pbVar(nil, 1, squareThemeID), 2, bad))
		id, body := a.read()
		if id != 6 || squareUint(t, body, 1) != squareEnterUnavailable {
			t.Fatalf("bad code %d: cr2 reply = rc%d/%x, want rc6 Result=%d", bad, id, body, squareEnterUnavailable)
		}
		if a.sess.roomNo != 1 || a.sess.roomSecret || a.sess.player == nil {
			t.Fatalf("bad code %d disturbed session: roomNo=%d secret=%v player=%v", bad, a.sess.roomNo, a.sess.roomSecret, a.sess.player)
		}
		roommate.none()
	}
	// Player A is still active in public room 1 (cr24 player list still works).
	a.send(24, nil)
	if id, body := a.read(); id != 24 || squareUint(t, body, 1) != 2 {
		t.Fatalf("playerlist after refused code = rc%d/%x, want rc24 count=2", id, body)
	}

	// Outsider requesting full secret room 2 is refused with squareEnterFull.
	outsider := newSqClientIn(t, rooms, 5)
	outsider.send(0, loginBody(8935))
	outsider.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	outsider.send(2, pbVar(pbVar(nil, 1, squareThemeID), 2, 2))
	if id, body := outsider.read(); id != 6 || squareUint(t, body, 1) != squareEnterFull {
		t.Fatalf("full secret room cr2 reply = rc%d/%x, want rc6 Result=%d", id, body, squareEnterFull)
	}

	// Player already inside full secret room 2 re-entering SquareCode = 2 is allowed.
	r2a.send(2, pbVar(pbVar(nil, 1, squareThemeID), 2, 2))
	id, enter := r2a.read()
	if id != 6 {
		t.Fatalf("member re-enter cr2 reply = rc%d, want rc6", id)
	}
	r2a.checkEnter(enter, squareThemeID, squareTestMaps[squareThemeID])
	if code := enterRoom(enter); code != 2 {
		t.Fatalf("member re-enter SquareCode = %d, want 2", code)
	}
	r2a.send(5, nil)
	id, roster := r2a.read()
	if id != 8 {
		t.Fatalf("member re-enter cr5 reply = rc%d, want rc8", id)
	}
	if aids := rosterAids(roster[len(relayResultBytes()):]); len(aids) != 2 || !slices.Contains(aids, uint64(8933)) || !slices.Contains(aids, uint64(8934)) {
		t.Fatalf("member re-enter roster = %v, want [8933 8934]", aids)
	}
	if id, _ := r2a.read(); id != squareRcHighFiveTargetList {
		t.Fatalf("member re-enter got msgid %d, want highfive (%d)", id, squareRcHighFiveTargetList)
	}
}
