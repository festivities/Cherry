package lpn

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

func TestGatewayPingEcho(t *testing.T) {
	server, client := net.Pipe()
	go observeGatewayFrames(&lockedConn{Conn: server}, GatewayAddr, "pipe", log.New(io.Discard, "", 0))
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write([]byte{0, 0, 0, 2, 'M', 83}); err != nil {
		t.Fatal(err)
	}
	if got := readFullDeadline(t, client, 6); !bytes.Equal(got, []byte{0, 0, 0, 2, 'M', 83}) {
		t.Fatalf("echo = %x", got)
	}
	if _, err := client.Write([]byte{0, 0, 0, 2, 'M', 'U'}); err != nil {
		t.Fatal(err)
	}
	assertNoReply(t, client)
}

func newRPClient(t *testing.T) (*roomPartySession, *sqClient) {
	c := newSqClient(t, &squareRoom{}, 1)
	return &roomPartySession{conn: c.sess.conn, logger: log.New(io.Discard, "", 0)}, c
}

func TestRoomPartyOpenControlNoAck(t *testing.T) {
	s, c := newRPClient(t)
	open := append([]byte{'G', 0x19}, c.sid[:]...)
	if err := s.handleOpen(open); err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{0, 0, 0, 18}, open...), make([]byte, 8)...)
	if got := readFullDeadline(t, c.peer, 22); !bytes.Equal(got, want) {
		t.Fatalf("control = %x, want %x", got, want)
	}
	c.none()
}

func TestRoomPartyLogin(t *testing.T) {
	s, c := newRPClient(t)
	if err := s.handleData(c.req(0, pbVar(nil, 2, 5))); err != nil { // no session key
		t.Fatal(err)
	}
	c.none()
	if s.loggedIn {
		t.Fatal("invalid login accepted")
	}
	if err := s.handleData(c.req(3, []byte{0x08, 1})); err != nil {
		t.Fatal(err)
	}
	c.none()
	if err := s.handleData(c.req(0, loginBody(7))); err != nil {
		t.Fatal(err)
	}
	c.expect(0, []byte{0x08, 0x00})
	if !s.loggedIn || s.aid != 7 {
		t.Fatalf("session = %+v", s)
	}
	if err := s.handleData(c.req(6, nil)); err != nil { // invite list -> empty idx8
		t.Fatal(err)
	}
	c.expect(8, nil)
}

func TestRoomRoutes(t *testing.T) {
	store.AccountsMu.Lock()
	store.Accounts["room-test"] = &store.Account{Aid: "9002", Name: "Rin"}
	store.AccountsMu.Unlock()
	defer func() { store.AccountsMu.Lock(); delete(store.Accounts, "room-test"); store.AccountsMu.Unlock() }()
	for path, name := range map[string]string{
		"/v4/room/enter/9002/LEVEL_2":   "Rin",
		"/v4/room/enter/100000/LEVEL_1": store.FriendName,
		"/v4/room/myroom/LEVEL_3":       "",
	} {
		rec := serveRoom(t, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		var b struct {
			Result struct {
				RoomInfo struct {
					GroundLevel string         `json:"groundLevel"`
					TileSize    int            `json:"tileSize"`
					Floor       map[string]any `json:"floor"`
					Items       []any          `json:"item"`
				} `json:"roomInfo"`
				Name string `json:"name"`
				Max  string `json:"ownMaxGroundLevel"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		r := b.Result
		if r.RoomInfo.GroundLevel != path[len(path)-7:] || r.RoomInfo.TileSize != 12 || r.RoomInfo.Floor == nil || r.RoomInfo.Items == nil || r.Max != "LEVEL_2" || (name != "" && r.Name != name) {
			t.Fatalf("GET %s body = %s", path, rec.Body.String())
		}
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v4/room/myroom/BAD"},
		{http.MethodGet, "/v4/room/myroom/LEVEL_"},
		{http.MethodGet, "/v4/room/enter/9999/LEVEL_1"},
		{http.MethodGet, "/v4/room/enter/9002/x"},
		{http.MethodGet, "/v4/room/other/LEVEL_1"},
		{http.MethodPost, "/v4/room/myroom/LEVEL_1"},
	} {
		if rec := serveRoom(t, c.method, c.path); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c.method, c.path, rec.Code)
		}
	}
}

// ---- Room Party LPNP rooms ----

type pc struct {
	*sqClient
	s *roomPartySession
}

func partySetup(t *testing.T) *partyHub {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{
		"a": {Aid: "11", Name: "Ann", Gender: "MALE", ItemCodes: []string{"CUON004TV"}},
		"b": {Aid: "12", Name: "Bo"},
		"c": {Aid: "13", Name: "Cy"},
	})
	store.SocialMu.Lock()
	old := store.Friendships
	store.Friendships = []store.Friendship{{A: "11", B: "12", State: "accepted"}, {A: "11", B: "13", State: "pending"}}
	store.SocialMu.Unlock()
	t.Cleanup(func() { store.SocialMu.Lock(); store.Friendships = old; store.SocialMu.Unlock() })
	return &partyHub{}
}

func partyLogin(t *testing.T, h *partyHub, aid uint64, sid byte) *pc {
	t.Helper()
	c := newSqClient(t, &squareRoom{}, sid)
	p := &pc{c, &roomPartySession{conn: c.sess.conn, logger: log.New(io.Discard, "", 0), hub: h}}
	p.send(0, loginBody(aid))
	c.expect(0, []byte{0x08, 0x00})
	t.Cleanup(p.s.close)
	return p
}

func (p *pc) send(msgid uint16, body []byte) {
	p.t.Helper()
	if err := p.s.handleData(p.req(msgid, body)); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pc) enter(host uint64) {
	p.t.Helper()
	p.send(1, pbVar(nil, 1, host))
}

// spawnMove is the _player_move Cherry assigns for tile (tx,ty): f1..f4 px, f5=0, f6=1.
func spawnMove(tx, ty int) []byte {
	x, y := partyTilePx(tx, ty)
	return pbVar(pbVar(pbVar(pbVar(pbVar(pbVar(nil, 1, x), 2, y), 3, x), 4, y), 5, 0), 6, 1)
}

var statusBody = []byte{0x08, 1, 0x10, 0xd8, 0x04} // Exist 1, Timeout 600

// enterHost enters the host's own room and consumes the reply and the status push.
// drain consumes one pending frame, reporting whether there was one.
func (p *pc) drain() bool {
	_ = p.peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var pre [4]byte
	if _, err := io.ReadFull(p.peer, pre[:]); err != nil {
		return false
	}
	f := make([]byte, binary.BigEndian.Uint32(pre[:]))
	_, _ = io.ReadFull(p.peer, f)
	return true
}

func (p *pc) enterHost(aid uint64) {
	p.t.Helper()
	p.enter(aid)
	p.expect(5, []byte{0x08, 0x00, 0x10, byte(aid), 0x28, 2})
	p.expect(23, statusBody)
}

func (p *pc) start(move []byte) []byte {
	p.t.Helper()
	p.send(5, pbLen(pbVar(nil, 2, 1), 1, move))
	id, body := p.read()
	if id != 6 {
		p.t.Fatalf("relaystart msgid = %d", id)
	}
	return body
}

func TestPartyEnterRules(t *testing.T) {
	h := partySetup(t)
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	c := partyLogin(t, h, 13, 3)
	b.enter(11) // no room yet
	b.expect(5, []byte{0x08, 1, 0x10, 11})
	a.enter(12) // other's room absent -> failure with Aid = host
	a.expect(5, []byte{0x08, 1, 0x10, 12})
	a.enter(11)
	a.expect(5, []byte{0x08, 0x00, 0x10, 11, 0x28, 2})
	a.expect(23, statusBody) // host enter: status push so My Room rejoins the party
	b.enter(11)              // accepted friend
	b.expect(5, []byte{0x08, 0x00, 0x10, 11, 0x28, 2})
	c.enter(11) // pending only -> not a friend
	c.expect(5, []byte{0x08, 1, 0x10, 11})
	a.send(2, nil) // resume = re-enter current room
	a.expect(5, []byte{0x08, 0x00, 0x10, 11, 0x28, 2})
	a.expect(23, statusBody)
	c.send(2, nil) // resume with no room
	c.expect(5, []byte{0x08, 1, 0x10, 13})
}

func TestPartyRosterRelaysExit(t *testing.T) {
	h := partySetup(t)
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	a.enterHost(11)
	b.enter(11)
	b.read()
	ma := pbVar(nil, 1, 5) // client's own move; the server assigns spawn tiles
	mb := pbVar(nil, 1, 9)
	wa, wb := spawnMove(8, 4), spawnMove(8, 5) // host keeps (8,4), joiner next free tile
	res := a.start(ma)
	if !bytes.Equal(res, append([]byte{0x08, 0}, pbLen(nil, 2, partyPlayerInfo(11, wa))...)) {
		t.Fatalf("host roster = %x", res)
	}
	a.expect(29, partyFloorRes([]partySnap{{11, wa}}, 11, 1)) // host alone: self roster sets JoinRoomParty on its hero
	a.none()
	res = b.start(mb)
	want := append([]byte{0x08, 0}, pbLen(nil, 2, partyPlayerInfo(11, wa))...)
	want = append(want, pbLen(nil, 2, partyPlayerInfo(12, wb))...)
	if !bytes.Equal(res, want) {
		t.Fatalf("guest roster = %x want %x", res, want)
	}
	b.expect(1, pbLen(nil, 1, partyPlayerInfo(11, wa))) // room adduser (chat info) of others to joiner
	// avatars come from the floor messages: full roster INCLUDING self to the joiner
	b.expect(29, partyFloorRes([]partySnap{{11, wa}, {12, wb}}, 11, 1))
	a.expect(1, pbLen(nil, 1, partyPlayerInfo(12, wb))) // joiner to others
	a.expect(30, partyFloorAdd(partySnap{12, wb}, 11, 1))
	b.none()
	a.none()
	// avatar content
	info := partyAvatarInfo(11)
	if !bytes.Contains(info, []byte("MALE")) || !bytes.Contains(info, []byte("CUON004TV")) || !bytes.Contains(info, []byte("Ann")) {
		t.Fatalf("avatar = %q", info)
	}
	// move: others only, verbatim, stored for later joiners
	b.send(8, pbLen(nil, 1, mb))
	a.expect(11, pbLen(pbVar(nil, 1, 12), 2, mb))
	b.none()
	// chat: everyone incl. sender, serial counts
	b.send(9, pbLen(nil, 1, []byte("hi")))
	want = pbVar(pbLen(pbVar(pbVar(nil, 1, 12), 2, 1), 3, []byte("hi")), 4, 0)
	a.expect(12, want)
	b.expect(12, want)
	b.send(9, pbLen(nil, 1, []byte{0xff}))
	b.send(9, pbLen(nil, 1, nil))
	a.none()
	b.none()
	// action: others only
	b.send(10, pbLen(nil, 1, []byte{8, 2, 16, 3}))
	a.expect(13, pbLen(pbVar(nil, 1, 12), 2, []byte{8, 2, 16, 3}))
	b.none()
	// exit: reply to leaver, deluser to others only
	b.send(3, nil)
	b.expect(3, nil)
	a.expect(2, []byte{0x08, 12, 0x10, 0})
	a.expect(31, []byte{0x08, 12}) // floor deluser removes the avatar
	a.none()
	b.none()
	// EOF of the host leaves nothing to tell and drops its registration
	a.s.close()
	if len(h.sessions) != 1 {
		t.Fatalf("sessions = %d", len(h.sessions))
	}
}

func TestPartySilentRejoinAndFriendEntered(t *testing.T) {
	h := partySetup(t)
	b := partyLogin(t, h, 12, 2)
	a := partyLogin(t, h, 11, 1)
	wa, wb := spawnMove(8, 5), spawnMove(8, 4) // guest got there first; host takes the next free tile
	a.enterHost(11)
	b.enter(11)
	b.read()
	b.start(pbVar(nil, 1, 9))
	b.expect(29, partyFloorRes([]partySnap{{12, wb}}, 11, 1)) // self only
	a.expect(10, []byte{0x08, 12})                            // host not in room yet: friend-entered push
	a.start(pbVar(nil, 1, 5))
	b.read()                                              // room adduser of the host
	b.expect(30, partyFloorAdd(partySnap{11, wa}, 11, 1)) // guest sees the host avatar and its pets
	a.expect(1, pbLen(nil, 1, partyPlayerInfo(12, wb)))
	a.expect(29, partyFloorRes([]partySnap{{11, wa}, {12, wb}}, 11, 1)) // host roster: self + guest
	// guest reconnects: new session takes over silently
	b.s.close() // old connection EOF would deluser; emulate takeover instead
	a.expect(2, []byte{0x08, 12, 0x10, 0})
	a.expect(31, []byte{0x08, 12})
	host := partyFloorRes([]partySnap{{11, wa}, {12, wb}}, 11, 1) // roster incl. the joiner itself
	b2 := partyLogin(t, h, 12, 4)
	b2.enter(11)
	b2.read()
	b2.start(pbVar(nil, 1, 9))
	b2.read()
	b2.expect(29, host)
	a.expect(1, pbLen(nil, 1, partyPlayerInfo(12, wb)))
	a.expect(30, partyFloorAdd(partySnap{12, wb}, 11, 1))
	b2.start(pbVar(nil, 1, 9)) // rejoin: roster only (fresh scene needs avatars), no adduser to anyone
	b2.read()
	b2.expect(29, host)
	a.none()
	b2.none()
}

func TestPartyInvite(t *testing.T) {
	h := partySetup(t)
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	c := partyLogin(t, h, 13, 3)
	a.enterHost(11)
	// packed encoding: 12 (friend), 13 (pending), 99 (unknown)
	a.send(7, pbLen(nil, 1, []byte{12, 13, 99}))
	a.expect(7, []byte{0x08, 12, 0x10, 13, 0x10, 99})
	a.expect(23, statusBody)                        // nonempty Invited -> host status push
	b.expect(9, []byte{0x08, 11, 0x10, 0xd8, 0x04}) // lefttime 600
	c.none()
	// unpacked encoding works too
	a.send(7, append(pbVar(nil, 1, 12), pbVar(nil, 2, 1)...))
	a.expect(7, []byte{0x08, 12})
	a.expect(23, statusBody)
	b.read()
	// invite list: waiting 12, inroom empty
	a.send(6, nil)
	a.expect(8, []byte{0x08, 12})
	// invited guest may enter (also with friendship removed)
	store.SocialMu.Lock()
	store.Friendships = nil
	store.SocialMu.Unlock()
	b.enter(11)
	b.expect(5, []byte{0x08, 0, 0x10, 11, 0x28, 2})
	b.start(nil)
	b.read()                       // floor roster (self)
	a.expect(10, []byte{0x08, 12}) // friend-entered push to absent host
	a.send(6, nil)
	a.expect(8, []byte{0x10, 12})
	// invite creates the inviter's room when none exists
	c.send(7, pbVar(nil, 1, 11))
	c.expect(7, []byte{0x10, 11})
	c.none() // nothing invited: no status push
	if h.rooms[13] == nil {
		t.Fatal("inviter room missing")
	}
}

// Cancel drops the aid from Waiting (reply idx22 = refreshed list); joining
// consumes the invite so a later leave is not "Waiting" and re-invite works.
func TestPartyInviteCancelAndConsume(t *testing.T) {
	h := partySetup(t)
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	a.enterHost(11)
	a.send(7, pbVar(nil, 1, 12))
	a.expect(7, []byte{0x08, 12})
	a.expect(23, statusBody)
	b.read()
	a.send(18, pbVar(nil, 1, 12))
	a.expect(22, nil)
	a.send(6, nil)
	a.expect(8, nil)
	a.send(18, pbLen(nil, 1, []byte{12, 99})) // unknown/not-invited aids are harmless
	a.expect(22, nil)
	a.send(7, pbVar(nil, 1, 12)) // re-invite after cancel
	a.expect(7, []byte{0x08, 12})
	a.expect(23, statusBody)
	b.read()
	b.enter(11)
	b.read()
	b.start(nil)
	b.read()                       // floor roster
	a.expect(10, []byte{0x08, 12}) // host not started: friend-entered push
	a.send(6, nil)
	a.expect(8, []byte{0x10, 12}) // in room, not waiting
	b.send(3, nil)
	b.expect(3, nil)
	a.send(6, nil)
	a.expect(8, nil) // consumed: no stale Waiting after leaving
	a.send(7, pbVar(nil, 1, 12))
	a.expect(7, []byte{0x08, 12})
	a.expect(23, statusBody)
	b.read()
}

func TestPartyTileHelpers(t *testing.T) {
	if x, y := partyTilePx(8, 4); x != 640 || y != 220 {
		t.Fatalf("(8,4) -> %d,%d", x, y)
	}
	for _, tl := range partySpawnTiles {
		x, y := partyTilePx(tl[0], tl[1])
		if a, b := partyPxTile(x, y); a != tl[0] || b != tl[1] {
			t.Fatalf("tile %v -> (%d,%d) -> (%d,%d)", tl, x, y, a, b)
		}
	}
	// (8,5): 08 d8 04 10 c8 01 18 d8 04 20 c8 01 28 dir 30 01, f5/f6 copied from the client
	r := &partyRoom{host: 11}
	m := &partyMember{aid: 12}
	got, tx, ty := r.spawnLocked(m, 1, nil)
	if tx != 8 || ty != 4 || !bytes.Equal(got, []byte{8, 0x80, 5, 0x10, 0xdc, 1, 0x18, 0x80, 5, 0x20, 0xdc, 1, 0x28, 0, 0x30, 1}) {
		t.Fatalf("empty room spawn (%d,%d) %x", tx, ty, got)
	}
	r.members = []*partyMember{{aid: 11, floor: 1, move: got}, {aid: 13, floor: 2, move: got}}
	client := pbVar(pbVar(pbVar(nil, 1, 1), 5, 3), 6, 7)
	got, tx, ty = r.spawnLocked(m, 1, client)
	want := []byte{8, 0xd8, 4, 0x10, 0xc8, 1, 0x18, 0xd8, 4, 0x20, 0xc8, 1, 0x28, 3, 0x30, 7}
	if tx != 8 || ty != 5 || !bytes.Equal(got, want) {
		t.Fatalf("occupied spawn (%d,%d) %x want %x", tx, ty, got, want)
	}
	if _, tx, ty = r.spawnLocked(m, 2, nil); tx != 8 || ty != 5 { // aid 13 holds (8,4) on floor 2
		t.Fatalf("floor 2 spawn (%d,%d)", tx, ty)
	}
}

// Floor change: idx24 -> idx28; idx25 -> idx29 (+Floor#4) to mover, idx30 new floor,
// idx31 old floor, idx32/33 to every other member; relays are floor-filtered.
func TestPartyFloors(t *testing.T) {
	h := partySetup(t)
	store.SocialMu.Lock()
	store.Friendships = []store.Friendship{{A: "11", B: "12", State: "accepted"}, {A: "11", B: "13", State: "accepted"}}
	store.SocialMu.Unlock()
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	c := partyLogin(t, h, 13, 3)
	a.enterHost(11)
	a.start(nil)
	a.read() // idx29 self
	for _, p := range []*pc{b, c} {
		p.enter(11)
		p.read()
		p.start(nil)
	}
	for _, p := range []*pc{a, b, c} {
		for p.drain() {
		}
	}
	// invalid / not in room
	b.send(24, pbVar(nil, 1, 3))
	b.expect(28, []byte{0x08, 1, 0x10, 3})
	b.send(24, pbVar(nil, 1, 0))
	b.expect(28, []byte{0x08, 1, 0x10, 0})
	d := partyLogin(t, h, 14, 4)
	d.send(24, pbVar(nil, 1, 2))
	d.expect(28, []byte{0x08, 1, 0x10, 2})
	// b -> floor 2
	b.send(24, pbVar(nil, 1, 2))
	b.expect(28, []byte{0x08, 0, 0x10, 2})
	a.none() // nothing is announced before cr_floor_relaystart_req
	b.send(25, pbLen(pbVar(nil, 1, 2), 2, pbVar(nil, 1, 1)))
	w := spawnMove(8, 4) // alone on floor 2
	b.expect(29, pbVar(partyFloorRes([]partySnap{{12, w}}, 11, 2), 4, 2))
	b.none()
	for _, p := range []*pc{a, c} {
		p.expect(31, []byte{0x08, 12})
		p.expect(33, append([]byte{0x08, 12, 0x10, 1, 0x1a, 2}, "Bo"...))
		p.expect(32, append([]byte{0x08, 12, 0x10, 2, 0x1a, 2}, "Bo"...))
		p.none()
	}
	// relays: b on floor 2 is isolated from a/c for move/action, chat stays room-wide
	b.send(8, pbLen(nil, 1, pbVar(nil, 1, 1)))
	a.send(8, pbLen(nil, 1, pbVar(nil, 1, 2)))
	c.expect(11, pbLen(pbVar(nil, 1, 11), 2, pbVar(nil, 1, 2)))
	b.none()
	a.none()
	c.none()
	b.send(10, pbLen(nil, 1, []byte{8, 1}))
	a.none()
	b.send(9, pbLen(nil, 1, []byte("yo")))
	for _, p := range []*pc{a, b, c} {
		p.read()
	}
	// a joins floor 2: gets roster with b, b gets idx30, c (floor 1) gets idx31
	a.send(24, pbVar(nil, 1, 2))
	a.expect(28, []byte{0x08, 0, 0x10, 2})
	a.send(25, pbLen(pbVar(nil, 1, 2), 2, nil))
	id, body := a.read()
	if id != 29 || !bytes.Contains(body, []byte("Bo")) || !bytes.Contains(body, []byte("Ann")) || !bytes.HasSuffix(body, []byte{0x20, 2}) {
		t.Fatalf("floor roster = %d/%x", id, body)
	}
	id, body = b.read()
	if id != 30 {
		t.Fatalf("b got %d, want 30 (%x)", id, body)
	}
	b.expect(33, append([]byte{0x08, 11, 0x10, 1, 0x1a, 3}, "Ann"...))
	b.expect(32, append([]byte{0x08, 11, 0x10, 2, 0x1a, 3}, "Ann"...))
	c.expect(31, []byte{0x08, 11})
	c.expect(33, append([]byte{0x08, 11, 0x10, 1, 0x1a, 3}, "Ann"...))
	c.expect(32, append([]byte{0x08, 11, 0x10, 2, 0x1a, 3}, "Ann"...))
	// leaving: idx31 only to same-floor members, idx2 to all
	b.send(3, nil)
	b.expect(3, nil)
	if id, _ := a.read(); id != 2 {
		t.Fatalf("a first = %d", id)
	}
	a.expect(31, []byte{0x08, 12})
	if id, _ := c.read(); id != 2 {
		t.Fatalf("c first = %d", id)
	}
	c.none() // c is on floor 1: no floor deluser
}

func TestPartyLoginStatus(t *testing.T) {
	h := partySetup(t)
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	a.enterHost(11)
	a.start(nil)
	a.read()
	b.enter(11)
	b.read()
	// host relogs while a guest keeps the room open: login reply carries MyroomExist/Timeout
	a2 := newSqClient(t, &squareRoom{}, 5)
	s2 := &roomPartySession{conn: a2.sess.conn, logger: log.New(io.Discard, "", 0), hub: h}
	t.Cleanup(s2.close)
	if err := s2.handleData(a2.req(0, loginBody(11))); err != nil {
		t.Fatal(err)
	}
	a2.expect(0, []byte{0x08, 0x00, 0x10, 1, 0x18, 0xd8, 0x04})
	partyLogin(t, h, 13, 3) // no open room: plain result (checked by partyLogin)
}

func TestPartyStatusRefresh(t *testing.T) {
	h := partySetup(t)
	h.every = 20 * time.Millisecond
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	a.enterHost(11) // room exists but empty: no refresh yet
	time.Sleep(60 * time.Millisecond)
	a.none()
	a.start(nil)
	a.read()
	b.enter(11)
	b.read()
	b.start(nil)
	for b.drain() {
	}
	for { // the host gets other pushes (friend-entered, ...) too; ticks keep coming
		if id, body := a.read(); id == 23 {
			if !bytes.Equal(body, statusBody) {
				t.Fatalf("status body = %x", body)
			}
			break
		}
	}
	b.none()
}

// serveRoom mirrors the web mux's /v4/room/ catch-all route to HandleRoom.
func serveRoom(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/room/", HandleRoom)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}
