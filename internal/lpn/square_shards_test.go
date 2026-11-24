package lpn

import (
	"slices"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// useRoomCap installs a fresh database that holds the staff panel's room size n. The accounts go in first, as the other
// Square tests do.
func useRoomCap(t *testing.T, n int) {
	t.Helper()
	testutil.UseDatabase(t)
	if err := store.SetSquareRoomCap(n, "test"); err != nil {
		t.Fatal(err)
	}
}

// enterRoom is the room number an rc_room_enter_res shows (its SquareCode, field 3).
func enterRoom(enter []byte) uint64 {
	var code uint64
	pbScan(enter, func(f, v uint64, _ []byte) {
		if f == 3 {
			code = v
		}
	})
	return code
}

// enterAt logs c in as aid, enters theme asking for room want (0 for none) and relays in. It returns the room number
// the banner shows; the relaystart reply is consumed.
func (c *sqClient) enterAt(aid, theme, want uint64) uint64 {
	c.t.Helper()
	c.send(0, loginBody(aid))
	c.expect(0, []byte{0x08, 0x81, 0x80, 0x08}) // 131073
	req := pbVar(nil, 1, theme)
	if want != 0 {
		req = pbVar(req, 2, want)
	}
	c.send(2, req)
	id, enter := c.read()
	if id != 6 {
		c.t.Fatalf("room enter msgid = %d", id)
	}
	c.checkEnter(enter, theme, squareTestMaps[theme])
	c.send(5, nil)
	id, body := c.read()
	if id != 8 {
		c.t.Fatalf("relaystart msgid = %d, want 8", id)
	}
	var rosterCount int
	pbScan(body, func(f, _ uint64, _ []byte) {
		if f == 2 {
			rosterCount++
		}
	})
	if rosterCount > 1 && theme != squareBossThemeID && theme != squareRacingTheme && c.sess.boss == nil && c.sess.racing == nil {
		if pid, _ := c.read(); pid != squareRcHighFiveTargetList {
			c.t.Fatalf("highfive push msgid = %d, want %d", pid, squareRcHighFiveTargetList)
		}
	}
	return enterRoom(enter)
}

// roomNumbers is the numbers of the rooms of theme, lowest first.
func roomNumbers(rs *squareRooms, theme uint64) []uint64 {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	var out []uint64
	for _, r := range rs.rooms[theme] {
		out = append(out, r.number)
	}
	return out
}

// A room holds the cap: the third player of a two-player room opens a second shard, and the banner shows the room codes.
func TestSquareRoomFillsThenOpensNext(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	a := newSqClientIn(t, rooms, 1)
	b := newSqClientIn(t, rooms, 2)
	c := newSqClientIn(t, rooms, 3)
	c1 := a.enterAt(11, squareThemeID, 0)
	if c1 == 0 || c1 > 9999 {
		t.Fatalf("first player room %d, want 1..9999", c1)
	}
	if n := b.enterAt(22, squareThemeID, 0); n != c1 {
		t.Fatalf("second player in room %d, want %d", n, c1)
	}
	c2 := c.enterAt(33, squareThemeID, 0)
	if c2 == 0 || c2 > 9999 || c2 == c1 {
		t.Fatalf("third player in room %d, want a new shard != %d", c2, c1)
	}
	want := []uint64{c1, c2}
	slices.Sort(want)
	if got := roomNumbers(rooms, squareThemeID); !slices.Equal(got, want) {
		t.Fatalf("rooms = %v, want %v", got, want)
	}
}

// A player who leaves frees its space for the next entry, and a room with no players is removed.
func TestSquareLeaveFreesRoomAndPrunes(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33, 44, 55, 66))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	a := newSqClientIn(t, rooms, 1)
	b := newSqClientIn(t, rooms, 2)
	c := newSqClientIn(t, rooms, 3)
	d := newSqClientIn(t, rooms, 4)
	e := newSqClientIn(t, rooms, 5)
	f := newSqClientIn(t, rooms, 6)
	c1 := a.enterAt(11, squareThemeID, 0)
	b.enterAt(22, squareThemeID, 0)
	c2 := c.enterAt(33, squareThemeID, 0)
	if c2 == c1 {
		t.Fatalf("second shard reused %d", c1)
	}
	if n := d.enterAt(44, squareThemeID, 0); n != c2 {
		t.Fatalf("fourth player in room %d, want %d", n, c2)
	}
	if aid := expectAddUser(t, c); aid != 44 {
		t.Fatalf("c adduser aid = %d, want 44", aid)
	}
	b.send(4, nil) // c1 has space again while c2 is full
	if id, _ := b.read(); id != 4 {
		t.Fatalf("exit msgid = %d", id)
	}
	if n := e.enterAt(55, squareThemeID, 0); n != c1 {
		t.Fatalf("newcomer in room %d, want the freed room %d", n, c1)
	}
	c.send(4, nil)
	if id, _ := c.read(); id != 4 {
		t.Fatalf("exit msgid = %d", id)
	}
	if id, _ := d.read(); id != 3 { // deluser for c
		t.Fatalf("d deluser msgid = %d, want 3", id)
	}
	d.send(4, nil) // c2 is empty now: it is removed
	if id, _ := d.read(); id != 4 {
		t.Fatalf("exit msgid = %d", id)
	}
	if got := roomNumbers(rooms, squareThemeID); !slices.Equal(got, []uint64{c1}) {
		t.Fatalf("rooms after c2 emptied = %v, want [%d]", got, c1)
	}
	if c3 := f.enterAt(66, squareThemeID, 0); c3 == 0 || c3 > 9999 || c3 == c1 {
		t.Fatalf("c1 is full, so the next player opened %d, want a new shard != %d", c3, c1)
	}
}

// A player who enters again via GO keeps its room, even when another room has space.
func TestSquareRejoinKeepsRoom(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	a := newSqClientIn(t, rooms, 1)
	b := newSqClientIn(t, rooms, 2)
	c := newSqClientIn(t, rooms, 3)
	c1 := a.enterAt(11, squareThemeID, 0)
	b.enterAt(22, squareThemeID, 0)
	c2 := c.enterAt(33, squareThemeID, 0)
	b.send(4, nil) // c1 has space
	b.read()
	if n := c.enterAt(33, squareThemeID, 0); n != c2 {
		t.Fatalf("re-enter in room %d, want its room %d", n, c2)
	}
	want := []uint64{c1, c2}
	slices.Sort(want)
	if got := roomNumbers(rooms, squareThemeID); !slices.Equal(got, want) {
		t.Fatalf("rooms = %v, want %v", got, want)
	}
}

// Explicit SquareCode requests honour existing rooms with space and refuse non-existent or full rooms on cr2;
// chooseLocked/pick still falls back to a room with space or a new random code when a full room is requested directly (e.g. relaystart race or invite accept).
func TestSquareRequestedRoomHonoured(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33, 44, 55))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	var c [5]*sqClient
	for i := range c {
		c[i] = newSqClientIn(t, rooms, byte(i+1))
	}
	c1 := c[0].enterAt(11, squareThemeID, 0)
	if n := c[1].enterAt(22, squareThemeID, 0); n != c1 { // c1 is full
		t.Fatalf("second player in room %d, want %d", n, c1)
	}

	// Asking for full room c1 via cr2 refuses with squareEnterFull; normal GO opens c2.
	c[2].send(0, loginBody(33))
	c[2].expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	c[2].send(2, pbVar(pbVar(nil, 1, squareThemeID), 2, c1))
	if id, body := c[2].read(); id != 6 || squareUint(t, body, 1) != squareEnterFull {
		t.Fatalf("asked for full room %d = rc%d/%x, want rc6 Result=%d", c1, id, body, squareEnterFull)
	}
	c2 := c[2].enterAt(33, squareThemeID, 0)
	if c2 == c1 {
		t.Fatalf("normal GO after refusal reused full room %d", c1)
	}
	// pick fallback while c2 has space returns c2.
	if n := rooms.pick(squareThemeID, 77, c1, 2); n != c2 {
		t.Fatalf("pick fallback while %d has space = %d, want %d", c2, n, c2)
	}
	if n := c[3].enterAt(44, squareThemeID, c2); n != c2 {
		t.Fatalf("asked for room %d with space, got %d", c2, n)
	}

	// Asking for a non-existent 4-digit code via cr2 refuses with squareEnterUnavailable.
	missing := uint64(4242)
	for missing == c1 || missing == c2 {
		missing++
	}
	c[4].send(0, loginBody(55))
	c[4].expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	c[4].send(2, pbVar(pbVar(nil, 1, squareThemeID), 2, missing))
	if id, body := c[4].read(); id != 6 || squareUint(t, body, 1) != squareEnterUnavailable {
		t.Fatalf("asked for missing room %d = rc%d/%x, want rc6 Result=%d", missing, id, body, squareEnterUnavailable)
	}

	// pick fallback when all existing rooms (c1 and c2) are full opens a new random shard code.
	if c3 := rooms.pick(squareThemeID, 77, c1, 2); c3 == 0 || c3 > 9999 || c3 == c1 || c3 == c2 {
		t.Fatalf("pick fallback when all rooms full = %d, want new shard != %d,%d", c3, c1, c2)
	}
}

// The Square list's UserCount is the players in every room of the Square.
func TestSquareCountsSumRooms(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	a := newSqClientIn(t, rooms, 1)
	b := newSqClientIn(t, rooms, 2)
	c := newSqClientIn(t, rooms, 3)
	a.enterAt(11, squareThemeID, 0)
	b.enterAt(22, squareThemeID, 0)
	c.enterAt(33, squareThemeID, 0)
	drainBoss(a)
	a.send(1, pbVar(nil, 1, 0))
	_, body := a.read()
	infos, _ := themeList(t, body)
	for _, ti := range infos {
		if ti.id == squareThemeID && ti.count != 3 {
			t.Fatalf("Treasure Island UserCount = %d, want 3 in two rooms", ti.count)
		}
	}
}

// The cap comes from the staff panel's value, or the default (20) without one or without a database.
func TestSquareRoomCapReadsThePanel(t *testing.T) {
	if got := squareRoomCap(); got != store.SquareRoomCapDefault {
		t.Fatalf("without a database the cap is %d, want %d", got, store.SquareRoomCapDefault)
	}
	useRoomCap(t, 7)
	if got := squareRoomCap(); got != 7 {
		t.Fatalf("with the panel's 7 the cap is %d", got)
	}
}

// Chat, moves and the avatars of a room reach its players only: room 2 never hears room 1.
func TestSquareRoomsKeepChatMovesAndAvatarsApart(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	a := newSqClientIn(t, rooms, 1)
	b := newSqClientIn(t, rooms, 2)
	c := newSqClientIn(t, rooms, 3)
	a.enterAt(11, squareThemeID, 0)
	b.enterAt(22, squareThemeID, 0)
	drainBoss(a) // b's arrival in room 1
	c.enterAt(33, squareThemeID, 0)
	a.none() // c's arrival is announced to room 2 only
	b.none()

	c.send(8, pbLen(nil, 1, []byte("hi")))
	if id, _ := c.read(); id != 11 {
		t.Fatalf("room 2 chat msgid = %d, want 11", id)
	}
	a.none()
	b.none()

	a.send(8, pbLen(nil, 1, []byte("hey")))
	if id, _ := b.read(); id != 11 {
		t.Fatalf("room 1 chat msgid = %d, want 11", id)
	}
	drainBoss(a)
	c.none()

	a.send(6, pbLen(nil, 1, squarePositionInfo(6600, 2900, 1)))
	if id, _ := b.read(); id != 9 {
		t.Fatalf("room 1 move msgid = %d, want 9", id)
	}
	c.none()

	c.send(4, nil) // c leaves room 2: room 1 hears nothing
	if id, _ := c.read(); id != 4 {
		t.Fatalf("exit msgid = %d", id)
	}
	a.none()
	b.none()
}

// A boss invite accepted while the master's room has space puts the invitee in the master's room: its relaystart joins
// that room, the invitee's old room is removed when empty, and the invitee meets the master in the lobby.
func TestSquareBossInviteMovesToMasterRoom(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 9971, 9972, 9973))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	m := newSqClientIn(t, rooms, 1)
	f := newSqClientIn(t, rooms, 2)
	i := newSqClientIn(t, rooms, 3)
	c1 := m.enterAt(9971, squareThemeID, 0)
	f.enterAt(9972, squareThemeID, 0) // c1 is full
	c2 := i.enterAt(9973, squareThemeID, 0)
	f.send(4, nil) // c1 has space again, the master's room
	if id, _ := f.read(); id != 4 {
		t.Fatalf("exit msgid = %d", id)
	}
	want := []uint64{c1, c2}
	slices.Sort(want)
	if got := roomNumbers(rooms, squareThemeID); !slices.Equal(got, want) {
		t.Fatalf("rooms = %v, want %v before the accept", got, want)
	}
	drainBoss(m) // f's arrival and departure
	bossStart(t, m)
	drainBoss(m)
	m.send(35, pbVar(nil, 1, 9973))
	drainBoss(i)
	i.send(36, pbVar(nil, 1, 9971))
	id, body := i.read()
	if id != 6 || squareUint(t, body, 3) != c1 {
		t.Fatalf("accept = %d/%x, want rc6 in the master's room %d", id, body, c1)
	}
	i.send(5, nil)
	id, roster := i.read()
	if id != 8 {
		t.Fatalf("relaystart msgid = %d", id)
	}
	if aids := rosterAids(roster[len(relayResultBytes()):]); !slices.Contains(aids, uint64(9971)) {
		t.Fatalf("invitee's roster = %v, want the master", aids)
	}
	if got := roomNumbers(rooms, squareThemeID); !slices.Equal(got, []uint64{c1}) {
		t.Fatalf("rooms after the accept = %v, want [%d]: the invitee's old room %d is empty", got, c1, c2)
	}
	if _, users := bossLobby(t, i); len(users) != 2 {
		t.Fatalf("lobby after the accept = %+v, want two", users)
	}
}

// A full master's room: the invitee keeps its own room (the normal placement), and the lobby still has both.
func TestSquareBossInviteStaysWhenMasterRoomFull(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 9981, 9982, 9983))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	m := newSqClientIn(t, rooms, 1)
	f := newSqClientIn(t, rooms, 2)
	i := newSqClientIn(t, rooms, 3)
	m.enterAt(9981, squareThemeID, 0)
	f.enterAt(9982, squareThemeID, 0) // master's room is full
	c2 := i.enterAt(9983, squareThemeID, 0)
	drainBoss(m) // f's arrival
	bossStart(t, m)
	drainBoss(m)
	m.send(35, pbVar(nil, 1, 9983))
	drainBoss(i)
	i.send(36, pbVar(nil, 1, 9981))
	id, body := i.read()
	if id != 6 || squareUint(t, body, 3) != c2 {
		t.Fatalf("accept = %d/%x, want rc6 in the invitee's room %d", id, body, c2)
	}
	i.send(5, nil)
	id, roster := i.read()
	if id != 8 {
		t.Fatalf("relaystart msgid = %d", id)
	}
	if aids := rosterAids(roster[len(relayResultBytes()):]); slices.Contains(aids, uint64(9981)) {
		t.Fatalf("invitee's roster = %v: the full master's room must not be joined", aids)
	}
	if _, users := bossLobby(t, i); len(users) != 2 {
		t.Fatalf("lobby after the accept = %+v, want two", users)
	}
}

// The GO join finds a lobby in another room of the Square: lobbies belong to the Square, not to the room they were opened in.
func TestSquareBossJoinFindsLobbyInOtherRoom(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 9991, 9992, 9993))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	m := newSqClientIn(t, rooms, 1)
	f := newSqClientIn(t, rooms, 2)
	i := newSqClientIn(t, rooms, 3)
	c1 := m.enterAt(9991, squareThemeID, 0)
	f.enterAt(9992, squareThemeID, 0) // c1 is full
	c2 := i.enterAt(9993, squareThemeID, 0)
	drainBoss(m) // f's arrival
	bossStart(t, m)
	drainBoss(m)
	drainBoss(i)
	bossStart(t, i) // the BOSS! GO banner from c2
	if _, users := bossLobby(t, i); len(users) != 2 || users[0].aid != 9991 {
		t.Fatalf("GO join lobby = %+v, want the master's lobby with two members", users)
	}
	want := []uint64{c1, c2}
	slices.Sort(want)
	if got := roomNumbers(rooms, squareThemeID); !slices.Equal(got, want) {
		t.Fatalf("rooms = %v, want %v: the GO join does not move the player", got, want)
	}
}
