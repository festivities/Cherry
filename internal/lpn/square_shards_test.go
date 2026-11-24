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

// enterAt logs c in as aid, enters theme asking for room want (0 for normal GO, else secret SquareCode) and relays in.
// It verifies rc6 omits field 3 for normal GO and sets field 3 == want for an explicit SquareCode, and returns the
// actual joined room number from c.sess.roomNo.
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
	if gotCode := enterRoom(enter); gotCode != want {
		c.t.Fatalf("rc6 SquareCode = %d, want %d", gotCode, want)
	}
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
	return c.sess.roomNo
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

// A room holds the cap: the third player of a two-player room opens room 2, and the banner shows the room numbers.
func TestSquareRoomFillsThenOpensNext(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	a := newSqClientIn(t, rooms, 1)
	b := newSqClientIn(t, rooms, 2)
	c := newSqClientIn(t, rooms, 3)
	if n := a.enterAt(11, squareThemeID, 0); n != 1 {
		t.Fatalf("first player in room %d, want 1", n)
	}
	if n := b.enterAt(22, squareThemeID, 0); n != 1 {
		t.Fatalf("second player in room %d, want 1", n)
	}
	if n := c.enterAt(33, squareThemeID, 0); n != 2 {
		t.Fatalf("third player in room %d, want 2 (room 1 holds 2)", n)
	}
	if got := roomNumbers(rooms, squareThemeID); !slices.Equal(got, []uint64{1, 2}) {
		t.Fatalf("rooms = %v, want [1 2]", got)
	}
}

// A player who leaves frees its space for the next entry, and a room with no players is removed, so its number is free.
func TestSquareLeaveFreesRoomAndPrunes(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33, 44, 55))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	a := newSqClientIn(t, rooms, 1)
	b := newSqClientIn(t, rooms, 2)
	c := newSqClientIn(t, rooms, 3)
	d := newSqClientIn(t, rooms, 4)
	e := newSqClientIn(t, rooms, 5)
	a.enterAt(11, squareThemeID, 0)
	b.enterAt(22, squareThemeID, 0)
	c.enterAt(33, squareThemeID, 0) // room 2
	b.send(4, nil)                  // room 1 has space again
	if id, _ := b.read(); id != 4 {
		t.Fatalf("exit msgid = %d", id)
	}
	if n := d.enterAt(44, squareThemeID, 0); n != 1 {
		t.Fatalf("newcomer in room %d, want the freed room 1", n)
	}
	c.send(4, nil) // room 2 is empty now: it is removed
	if id, _ := c.read(); id != 4 {
		t.Fatalf("exit msgid = %d", id)
	}
	if got := roomNumbers(rooms, squareThemeID); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("rooms after room 2 emptied = %v, want [1]", got)
	}
	if n := e.enterAt(55, squareThemeID, 0); n != 2 {
		t.Fatalf("room 1 is full, so the next player opens room %d, want the freed 2", n)
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
	a.enterAt(11, squareThemeID, 0)
	b.enterAt(22, squareThemeID, 0)
	c.enterAt(33, squareThemeID, 0) // room 2
	b.send(4, nil)                  // room 1 has space
	b.read()
	if n := c.enterAt(33, squareThemeID, 0); n != 2 {
		t.Fatalf("re-enter in room %d, want its room 2", n)
	}
	if got := roomNumbers(rooms, squareThemeID); !slices.Equal(got, []uint64{1, 2}) {
		t.Fatalf("rooms = %v, want [1 2]", got)
	}
}

// Explicit SquareCode requests enter secretRooms (isolated from public shards) and refuse full secret rooms on cr2;
// chooseLocked/pick on the public pool still falls back to firstAvailableLocked when a full public room is requested directly (e.g. relaystart race or bossAccept).
func TestSquareRequestedRoomHonoured(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33, 44, 55, 66))
	useRoomCap(t, 2)
	rooms := &squareRooms{}
	var c [6]*sqClient
	for i := range c {
		c[i] = newSqClientIn(t, rooms, byte(i+1))
	}
	if n := c[0].enterAt(11, squareThemeID, 0); n != 1 {
		t.Fatalf("first player in room %d, want 1", n)
	}
	c[1].enterAt(22, squareThemeID, 0) // public room 1 is full
	if n := c[2].enterAt(33, squareThemeID, 0); n != 2 {
		t.Fatalf("third normal-GO player in room %d, want 2", n)
	}

	// Asking for SquareCode = 2 enters secret room 2 (separate from public room 2).
	if n := c[3].enterAt(44, squareThemeID, 2); n != 2 || !c[3].sess.roomSecret {
		t.Fatalf("asked for secret room 2, got %d secret=%v", n, c[3].sess.roomSecret)
	}
	if n := c[4].enterAt(55, squareThemeID, 2); n != 2 || !c[4].sess.roomSecret {
		t.Fatalf("second player in secret room 2, got %d secret=%v", n, c[4].sess.roomSecret)
	}
	// Secret room 2 is now full: a third player asking for SquareCode = 2 is refused with squareEnterFull.
	c[5].send(0, loginBody(66))
	c[5].expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	c[5].send(2, pbVar(pbVar(nil, 1, squareThemeID), 2, 2))
	if id, body := c[5].read(); id != 6 || squareUint(t, body, 1) != squareEnterFull {
		t.Fatalf("asked for full secret room 2 = rc%d/%x, want rc6 Result=%d", id, body, squareEnterFull)
	}
	// Public pick fallback for full public room 1 returns public room 2 (1/2 players).
	if n := rooms.pick(squareThemeID, 77, 1, 2); n != 2 {
		t.Fatalf("pick fallback while public room 2 has space = %d, want 2", n)
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
