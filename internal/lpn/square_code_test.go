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

// TestSquareEnterByCodeJoinsFriendRoom verifies cr2 with an explicit SquareCode joins a friend's active shard
// (both the first shard and a later overflow shard) and cr5 places both players together with adduser broadcast.
func TestSquareEnterByCodeJoinsFriendRoom(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8911, 8912, 8913, 8914, 8915, 8916))
	useRoomCap(t, 2)
	rooms := &squareRooms{}

	// Case 1: Player A opens the first shard c1 via normal GO; Player B joins c1 by code.
	a := newSqClientIn(t, rooms, 1)
	c1 := a.enterAt(8911, 3, 0)
	if c1 == 0 || c1 > 9999 {
		t.Fatalf("player A room = %d, want 1..9999", c1)
	}

	b := newSqClientIn(t, rooms, 2)
	b.send(0, loginBody(8912))
	b.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	b.send(2, pbVar(pbVar(nil, 1, 3), 2, c1))
	id, enter := b.read()
	if id != 6 {
		t.Fatalf("player B cr2 reply = rc%d, want rc6", id)
	}
	b.checkEnter(enter, 3, squareTestMaps[3])
	if code := enterRoom(enter); code != c1 {
		t.Fatalf("player B rc6 SquareCode = %d, want %d", code, c1)
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

	// Case 2: c1 is now full (2/2), so Player C opens a second shard c2; Player D joins c2 by code.
	c := newSqClientIn(t, rooms, 3)
	c2 := c.enterAt(8915, 3, 0)
	if c2 == 0 || c2 > 9999 || c2 == c1 {
		t.Fatalf("player C room = %d, want new shard != %d", c2, c1)
	}

	d := newSqClientIn(t, rooms, 4)
	d.send(0, loginBody(8916))
	d.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	d.send(2, pbVar(pbVar(nil, 1, 3), 2, c2))
	id, enter = d.read()
	if id != 6 {
		t.Fatalf("player D cr2 reply = rc%d, want rc6", id)
	}
	d.checkEnter(enter, 3, squareTestMaps[3])
	if code := enterRoom(enter); code != c2 {
		t.Fatalf("player D rc6 SquareCode = %d, want %d", code, c2)
	}
	d.send(5, nil)
	id, roster = d.read()
	if id != 8 {
		t.Fatalf("player D cr5 reply = rc%d, want rc8", id)
	}
	if aids := rosterAids(roster[len(relayResultBytes()):]); !slices.Contains(aids, uint64(8915)) {
		t.Fatalf("player D roster = %v, want 8915", aids)
	}
	if id, _ := d.read(); id != squareRcHighFiveTargetList {
		t.Fatalf("player D got msgid %d, want highfive (%d)", id, squareRcHighFiveTargetList)
	}
	if aid := expectAddUser(t, c); aid != 8916 {
		t.Fatalf("player C adduser aid = %d, want 8916", aid)
	}
	a.none()
	b.none()
}

// TestSquareEnterByCodeSwitchesRoomInSameSquare verifies sending cr2 with a different existing SquareCode in the same
// Square leaves the old room (deluser to remaining member) and relays into the target shard.
func TestSquareEnterByCodeSwitchesRoomInSameSquare(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8921, 8922, 8923))
	useRoomCap(t, 2)
	rooms := &squareRooms{}

	bystander := newSqClientIn(t, rooms, 1)
	c1 := bystander.enterAt(8921, 3, 0)
	a := newSqClientIn(t, rooms, 2)
	if no := a.enterAt(8922, 3, 0); no != c1 {
		t.Fatalf("player A placed in room %d, want %d", no, c1)
	}
	if aid := expectAddUser(t, bystander); aid != 8922 {
		t.Fatalf("bystander adduser aid = %d, want 8922", aid)
	}

	// Open a second shard c2 with space.
	host2 := newSqClientIn(t, rooms, 3)
	c2 := host2.enterAt(8923, 3, 0)
	if c2 == c1 {
		t.Fatalf("host2 reused full room %d", c1)
	}

	a.send(2, pbVar(pbVar(nil, 1, 3), 2, c2))
	id, enter := a.read()
	if id != 6 {
		t.Fatalf("switch cr2 reply = rc%d, want rc6", id)
	}
	a.checkEnter(enter, 3, squareTestMaps[3])
	if code := enterRoom(enter); code != c2 {
		t.Fatalf("switch rc6 SquareCode = %d, want %d", code, c2)
	}

	// Bystander in c1 receives deluser for player A immediately.
	if id, body := bystander.read(); id != 3 || squareUint(t, body, 1) != 8922 {
		t.Fatalf("bystander got msgid=%d body=%x, want deluser (3) for 8922", id, body)
	}

	a.send(5, nil)
	id, roster := a.read()
	if id != 8 {
		t.Fatalf("switch cr5 reply = rc%d, want rc8", id)
	}
	aids := rosterAids(roster[len(relayResultBytes()):])
	if len(aids) != 2 || !slices.Contains(aids, uint64(8922)) || !slices.Contains(aids, uint64(8923)) {
		t.Fatalf("c2 roster = %v, want [8922 8923]", aids)
	}
	if id, _ := a.read(); id != squareRcHighFiveTargetList {
		t.Fatalf("player A got msgid %d, want highfive (%d)", id, squareRcHighFiveTargetList)
	}
	if aid := expectAddUser(t, host2); aid != 8922 {
		t.Fatalf("host2 adduser aid = %d, want 8922", aid)
	}
	want := []uint64{c1, c2}
	slices.Sort(want)
	if got := roomNumbers(rooms, 3); !slices.Equal(got, want) {
		t.Fatalf("rooms = %v, want %v", got, want)
	}
}

// TestSquareEnterByCodeRefusals verifies SquareCode 0, >=10000, and non-existent 4-digit codes return
// squareEnterUnavailable without disturbing an active session or creating a room, a full target room returns
// squareEnterFull, and a member already in that full room may re-enter.
func TestSquareEnterByCodeRefusals(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8931, 8932, 8933, 8934, 8935))
	useRoomCap(t, 2)
	rooms := &squareRooms{}

	a := newSqClientIn(t, rooms, 1)
	c1 := a.enterAt(8931, squareThemeID, 0)
	roommate := newSqClientIn(t, rooms, 2)
	if no := roommate.enterAt(8932, squareThemeID, 0); no != c1 {
		t.Fatalf("roommate room = %d, want %d", no, c1)
	}
	if aid := expectAddUser(t, a); aid != 8932 {
		t.Fatalf("player A adduser aid = %d, want 8932", aid)
	}

	// Second shard c2 is full (cap 2).
	r2a := newSqClientIn(t, rooms, 3)
	r2b := newSqClientIn(t, rooms, 4)
	c2 := r2a.enterAt(8933, squareThemeID, 0)
	if no := r2b.enterAt(8934, squareThemeID, 0); no != c2 {
		t.Fatalf("r2b room = %d, want %d", no, c2)
	}
	if aid := expectAddUser(t, r2a); aid != 8934 {
		t.Fatalf("r2a adduser aid = %d, want 8934", aid)
	}

	missing := uint64(4242)
	for missing == c1 || missing == c2 {
		missing++
	}

	// SquareCode = 0 ("0000"), 10000, and non-existent code 4242 are refused with squareEnterUnavailable and leave A intact.
	for _, bad := range []uint64{0, 10000, missing} {
		a.send(2, pbVar(pbVar(nil, 1, squareThemeID), 2, bad))
		id, body := a.read()
		if id != 6 || squareUint(t, body, 1) != squareEnterUnavailable {
			t.Fatalf("bad code %d: cr2 reply = rc%d/%x, want rc6 Result=%d", bad, id, body, squareEnterUnavailable)
		}
		if a.sess.roomNo != c1 || a.sess.player == nil {
			t.Fatalf("bad code %d disturbed session: roomNo=%d player=%v", bad, a.sess.roomNo, a.sess.player)
		}
		roommate.none()
	}
	if slices.Contains(roomNumbers(rooms, squareThemeID), missing) {
		t.Fatalf("non-existent code %d must not create a room", missing)
	}
	// Player A is still active in c1 (cr24 player list still works).
	a.send(24, nil)
	if id, body := a.read(); id != 24 || squareUint(t, body, 1) != 2 {
		t.Fatalf("playerlist after refused code = rc%d/%x, want rc24 count=2", id, body)
	}

	// Outsider requesting full room c2 is refused with squareEnterFull.
	outsider := newSqClientIn(t, rooms, 5)
	outsider.send(0, loginBody(8935))
	outsider.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	outsider.send(2, pbVar(pbVar(nil, 1, squareThemeID), 2, c2))
	if id, body := outsider.read(); id != 6 || squareUint(t, body, 1) != squareEnterFull {
		t.Fatalf("full room cr2 reply = rc%d/%x, want rc6 Result=%d", id, body, squareEnterFull)
	}

	// Player already inside full room c2 re-entering SquareCode = c2 is allowed.
	r2a.send(2, pbVar(pbVar(nil, 1, squareThemeID), 2, c2))
	id, enter := r2a.read()
	if id != 6 {
		t.Fatalf("member re-enter cr2 reply = rc%d, want rc6", id)
	}
	r2a.checkEnter(enter, squareThemeID, squareTestMaps[squareThemeID])
	if code := enterRoom(enter); code != c2 {
		t.Fatalf("member re-enter SquareCode = %d, want %d", code, c2)
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

// TestSquareShardCodesRandomized verifies newly opened shards get valid 4-digit codes in 1..9999, distinct across
// open shards, not hardcoded to 1, 2, 3..., and shared via pending when two players enter an empty Square concurrently.
func TestSquareShardCodesRandomized(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 8941, 8942, 8943, 8944, 8945, 8946, 8947, 8948))

	firstCodes := make([]uint64, 0, 8)
	allOnes := true
	for i := range 8 {
		rooms := &squareRooms{}
		c := newSqClientIn(t, rooms, byte(i+1))
		code := c.enterAt(8941, 3, 0)
		if code < 1 || code > 9999 {
			t.Fatalf("fresh registry %d gave code %d, want 1..9999", i, code)
		}
		if code != 1 {
			allOnes = false
		}
		firstCodes = append(firstCodes, code)
	}
	if allOnes {
		t.Fatalf("all 8 fresh registries returned hardcoded code 1: %v", firstCodes)
	}

	// Multiple shards in one registry (cap 2, 8 players -> 4 shards) get distinct 4-digit codes not fixed to 1..4.
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	seen := map[uint64]bool{}
	opened := make([]uint64, 0, 4)
	for i := 0; i < 8; i += 2 {
		p1 := newSqClientIn(t, rooms, byte(i+1))
		p2 := newSqClientIn(t, rooms, byte(i+2))
		code := p1.enterAt(uint64(8941+i), 3, 0)
		if n := p2.enterAt(uint64(8942+i), 3, 0); n != code {
			t.Fatalf("pair %d second player room = %d, want %d", i/2, n, code)
		}
		if code < 1 || code > 9999 {
			t.Fatalf("shard %d code = %d, want 1..9999", i/2, code)
		}
		if seen[code] {
			t.Fatalf("duplicate shard code %d across open shards %v", code, opened)
		}
		seen[code] = true
		opened = append(opened, code)
	}
	if slices.Equal(opened, []uint64{1, 2, 3, 4}) {
		t.Fatalf("opened shard codes = %v, want randomized codes", opened)
	}

	// Two players tapping normal GO on an empty Square before either relays in land in the same random shard.
	useRoomCap(t, 2)
	emptyRooms := &squareRooms{}
	p1 := newSqClientIn(t, emptyRooms, 10)
	p2 := newSqClientIn(t, emptyRooms, 11)
	p1.send(0, loginBody(8941))
	p1.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	p2.send(0, loginBody(8942))
	p2.expect(0, []byte{0x08, 0x81, 0x80, 0x08})

	p1.send(2, pbVar(nil, 1, 3))
	id1, enter1 := p1.read()
	p2.send(2, pbVar(nil, 1, 3))
	id2, enter2 := p2.read()
	if id1 != 6 || id2 != 6 {
		t.Fatalf("concurrent cr2 replies = rc%d / rc%d, want rc6", id1, id2)
	}
	code1, code2 := enterRoom(enter1), enterRoom(enter2)
	if code1 < 1 || code1 > 9999 || code1 != code2 {
		t.Fatalf("concurrent normal-GO codes = %d and %d, want matching 1..9999", code1, code2)
	}
	p1.send(5, nil)
	if id, _ := p1.read(); id != 8 {
		t.Fatalf("p1 cr5 reply = rc%d, want rc8", id)
	}
	p2.send(5, nil)
	id, roster := p2.read()
	if id != 8 {
		t.Fatalf("p2 cr5 reply = rc%d, want rc8", id)
	}
	if aids := rosterAids(roster[len(relayResultBytes()):]); !slices.Contains(aids, uint64(8941)) {
		t.Fatalf("p2 roster = %v, want 8941 in same shard", aids)
	}
	if id, _ := p2.read(); id != squareRcHighFiveTargetList {
		t.Fatalf("p2 got msgid %d, want highfive (%d)", id, squareRcHighFiveTargetList)
	}
	if aid := expectAddUser(t, p1); aid != 8942 {
		t.Fatalf("p1 adduser aid = %d, want 8942", aid)
	}
}
