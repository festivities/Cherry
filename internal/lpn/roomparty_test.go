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
	"strconv"
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
	testutil.InstallAccounts(t, withLoginAccounts(nil, 7))
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
	testutil.InstallAccounts(t, map[string]*store.Account{"room-test": {Aid: "9002", Name: "Rin"}})
	for path, name := range map[string]string{
		"/v4/room/enter/9002/LEVEL_2":                "Rin",
		"/v4/room/enter/100000/LEVEL_1":              store.FriendName(),
		"/v4/room/enter/1145205883351836906/LEVEL_1": vipLoungeName,
		"/v4/room/myroom/LEVEL_3":                    "",
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
		"a": {Aid: "11", Name: "Ann", Gender: "MALE", ItemCodes: []string{"CUON004TV"}, SessionKey: testSessionKey},
		"b": {Aid: "12", Name: "Bo", SessionKey: testSessionKey},
		"c": {Aid: "13", Name: "Cy", SessionKey: testSessionKey},
		"d": {Aid: "14", Name: "Di", SessionKey: testSessionKey},
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
	b.enter(11) // no room yet -> ENTER_RESULT=3 (party ended) + HostName
	b.expect(5, pbLen(pbVar(pbVar(nil, 1, partyEnterEnded), 2, 11), 4, []byte("Ann")))
	a.enter(12) // other's room absent -> ENTER_RESULT=3 + HostName
	a.expect(5, pbLen(pbVar(pbVar(nil, 1, partyEnterEnded), 2, 12), 4, []byte("Bo")))
	a.enter(11)
	a.expect(5, []byte{0x08, 0x00, 0x10, 11, 0x28, 2})
	a.expect(23, statusBody) // host enter: status push so My Room rejoins the party
	b.enter(11)              // accepted friend
	b.expect(5, []byte{0x08, 0x00, 0x10, 11, 0x28, 2})
	c.enter(11) // pending only -> not a friend (ENTER_RESULT=1 + HostName)
	c.expect(5, pbLen(pbVar(pbVar(nil, 1, partyEnterInvalid), 2, 11), 4, []byte("Ann")))
	a.send(2, nil) // resume = re-enter current room
	a.expect(5, []byte{0x08, 0x00, 0x10, 11, 0x28, 2})
	a.expect(23, statusBody)
	c.send(2, nil) // resume with no room
	c.expect(5, pbLen(pbVar(pbVar(nil, 1, partyEnterInvalid), 2, 13), 4, []byte("Cy")))
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
	wa, wb := spawnMove(8, 4), spawnMove(8, 4) // everyone spawns on DefaultHeroPosition (8,4)
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
	// PLAN 8.6b part 5: a move or an action over maxRelayPayload goes to nobody
	big := bytes.Repeat([]byte{0x08, 0x01}, maxRelayPayload/2+1)
	b.send(8, pbLen(nil, 1, big))
	b.send(10, pbLen(nil, 1, big))
	a.none()
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
	wa, wb := spawnMove(8, 4), spawnMove(8, 4) // guest and host both on (8,4)
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
	store.AccountsMu.Lock()
	acc12 := store.AccountByAidLocked("12")
	var gotAlert bool
	if acc12 != nil && acc12.News != nil && len(acc12.News.Alerts) > 0 {
		gotAlert = acc12.News.Alerts[0].Type == "ROOM_INVITE_RECEIVE" && acc12.News.Alerts[0].Actor == "11"
	}
	store.AccountsMu.Unlock()
	if !gotAlert {
		t.Fatal("expected ROOM_INVITE_RECEIVE news alert on aid 12 from aid 11")
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
	for _, tl := range append([][2]int{partySpawnTile}, partyPetTiles...) {
		x, y := partyTilePx(tl[0], tl[1])
		if a, b := partyPxTile(x, y); a != tl[0] || b != tl[1] {
			t.Fatalf("tile %v -> (%d,%d) -> (%d,%d)", tl, x, y, a, b)
		}
	}
	// (8,4) for everyone, occupied or not; f5/f6 copied from the client (f6 default 1)
	r := &partyRoom{host: 11}
	got, tx, ty := r.spawnLocked(nil)
	if tx != 8 || ty != 4 || !bytes.Equal(got, []byte{8, 0x80, 5, 0x10, 0xdc, 1, 0x18, 0x80, 5, 0x20, 0xdc, 1, 0x28, 0, 0x30, 1}) {
		t.Fatalf("spawn (%d,%d) %x", tx, ty, got)
	}
	got, _, _ = r.spawnLocked(pbVar(pbVar(pbVar(nil, 1, 1), 5, 3), 6, 7))
	if want := []byte{8, 0x80, 5, 0x10, 0xdc, 1, 0x18, 0x80, 5, 0x20, 0xdc, 1, 0x28, 3, 0x30, 7}; !bytes.Equal(got, want) {
		t.Fatalf("spawn with client move %x want %x", got, want)
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

// TestPartyThirdFloor verifies that a host with 3F unlocked advertises ExtendFloors=3 on rc_room_enter_res and
// permits floorEnter/floorRelayStart on floor 3 (while still rejecting floor 4).
func TestPartyThirdFloor(t *testing.T) {
	h := partySetup(t)
	store.AccountsMu.Lock()
	for _, a := range store.AllAccountsLocked() {
		if a.Aid == "11" {
			a.Remodel = &store.RoomRemodel{MaxFloor: 3}
		}
	}
	store.AccountsMu.Unlock()
	store.SocialMu.Lock()
	store.Friendships = []store.Friendship{{A: "11", B: "12", State: "accepted"}}
	store.SocialMu.Unlock()

	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	a.enter(11)
	a.expect(5, []byte{0x08, 0, 0x10, 11, 0x28, 3}) // ExtendFloors = 3
	a.expect(23, statusBody)
	a.start(nil)
	a.read() // idx29 self

	b.enter(11)
	b.expect(5, []byte{0x08, 0, 0x10, 11, 0x28, 3})
	b.start(nil)
	for _, p := range []*pc{a, b} {
		for p.drain() {
		}
	}

	// Floor 4 is rejected; floor 3 succeeds and moves b onto floor 3.
	b.send(24, pbVar(nil, 1, 4))
	b.expect(28, []byte{0x08, 1, 0x10, 4})
	b.send(24, pbVar(nil, 1, 3))
	b.expect(28, []byte{0x08, 0, 0x10, 3})
	b.send(25, pbLen(pbVar(nil, 1, 3), 2, pbVar(nil, 1, 1)))
	w := spawnMove(8, 4)
	b.expect(29, pbVar(partyFloorRes([]partySnap{{12, w}}, 11, 3), 4, 3))
	a.expect(31, []byte{0x08, 12})
	a.expect(33, append([]byte{0x08, 12, 0x10, 1, 0x1a, 2}, "Bo"...))
	a.expect(32, append([]byte{0x08, 12, 0x10, 3, 0x1a, 2}, "Bo"...))

	// allRosterLocked includes floor 3 members.
	h.mu.Lock()
	all := h.rooms[11].allRosterLocked()
	h.mu.Unlock()
	if len(all) != 2 {
		t.Fatalf("allRosterLocked len = %d, want 2 (1F host + 3F guest)", len(all))
	}
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

// TestPartyExplodeEndParty verifies the "End Party" button (cr_room_banish_req idx4 Explode=true):
// host gets idx4 + idx23(Exist=0) + idx2(self, reason=2); guests get idx2(self, reason=2);
// room is removed; non-host cannot explode; host can start a fresh party afterwards.
func TestPartyExplodeEndParty(t *testing.T) {
	h := partySetup(t)
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	a.enterHost(11)
	a.start(nil)
	a.read() // idx29
	b.enter(11)
	b.read()
	b.start(nil)
	for _, p := range []*pc{a, b} {
		for p.drain() {
		}
	}

	// Non-host attempting to explode gets empty idx4, room stays open, nobody kicked.
	b.send(4, pbVar(nil, 2, 1))
	b.expect(4, nil)
	a.none()
	b.none()
	h.mu.Lock()
	stillOpen := h.rooms[11] != nil && len(h.rooms[11].members) == 2
	h.mu.Unlock()
	if !stillOpen {
		t.Fatal("non-host explode modified host room")
	}

	// Host taps "End Party" (Explode=true).
	a.send(4, pbVar(nil, 2, 1))
	a.expect(4, nil)
	a.expect(23, partyStatusEndedBody())
	a.expect(2, pbVar(pbVar(nil, 1, 11), 2, partyExitEnded))
	b.expect(2, pbVar(pbVar(nil, 1, 12), 2, partyExitEnded))
	a.none()
	b.none()

	h.mu.Lock()
	_, exists := h.rooms[11]
	h.mu.Unlock()
	if exists {
		t.Fatal("host room still exists after explode")
	}

	// Guest trying to enter the ended party gets ENTER_RESULT=3 (ended) + HostName.
	b.enter(11)
	b.expect(5, pbLen(pbVar(pbVar(nil, 1, partyEnterEnded), 2, 11), 4, []byte("Ann")))

	// Host relogin has no MyroomExist.
	a2 := partyLogin(t, h, 11, 5)
	// Host can start a new party and explode it even while outside the room (guest alone inside).
	a2.enterHost(11)
	b.enter(11)
	b.read()
	b.start(nil)
	for _, p := range []*pc{a2, b} {
		for p.drain() {
		}
	}
	a2.s.host = 0 // host navigated back to Garden / solo My Room
	a2.send(4, pbVar(nil, 2, 1))
	a2.expect(4, nil)
	a2.expect(23, partyStatusEndedBody())
	b.expect(2, pbVar(pbVar(nil, 1, 12), 2, partyExitEnded))
	a2.none()
	b.none()
}

// TestPartyBanishKickGuest verifies kicking a specific guest (cr_room_banish_req Explode=false):
// kicked guest gets reason=2, remaining members get reason=0 (+ idx31 on same floor only),
// and the kicked guest is blocked from re-entering until explicitly re-invited by the host.
func TestPartyBanishKickGuest(t *testing.T) {
	h := partySetup(t)
	store.SocialMu.Lock()
	store.Friendships = []store.Friendship{
		{A: "11", B: "12", State: "accepted"},
		{A: "11", B: "13", State: "accepted"},
		{A: "11", B: "14", State: "accepted"},
	}
	store.SocialMu.Unlock()
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	c := partyLogin(t, h, 13, 3)
	d := partyLogin(t, h, 14, 4)
	a.enterHost(11)
	a.start(nil)
	for _, p := range []*pc{b, c, d} {
		p.enter(11)
		p.read()
		p.start(nil)
	}
	// Move d to floor 2.
	d.send(24, pbVar(nil, 1, 2))
	d.read()
	d.send(25, pbLen(pbVar(nil, 1, 2), 2, pbVar(nil, 1, 1)))
	for _, p := range []*pc{a, b, c, d} {
		for p.drain() {
		}
	}

	// Host kicks guest 12 (Explode=false).
	a.send(4, pbVar(pbVar(nil, 1, 12), 2, 0))
	a.expect(4, nil)
	b.expect(2, pbVar(pbVar(nil, 1, 12), 2, partyExitEnded))
	// Host and c (floor 1) get room deluser + floor deluser; d (floor 2) gets room deluser only.
	a.expect(2, pbVar(pbVar(nil, 1, 12), 2, partyExitNormal))
	a.expect(31, pbVar(nil, 1, 12))
	c.expect(2, pbVar(pbVar(nil, 1, 12), 2, partyExitNormal))
	c.expect(31, pbVar(nil, 1, 12))
	d.expect(2, pbVar(pbVar(nil, 1, 12), 2, partyExitNormal))
	d.none()

	// Kicked guest 12 cannot re-enter even though still friends.
	b.enter(11)
	b.expect(5, pbLen(pbVar(pbVar(nil, 1, partyEnterInvalid), 2, 11), 4, []byte("Ann")))

	// Host re-invites 12 -> clears banish -> 12 can enter again.
	a.send(7, pbVar(nil, 1, 12))
	a.expect(7, []byte{0x08, 12})
	a.expect(23, statusBody)
	b.expect(9, []byte{0x08, 11, 0x10, 0xd8, 0x04})
	b.enter(11)
	b.expect(5, []byte{0x08, 0x00, 0x10, 11, 0x28, 2})
}

// TestPartyCapacityAndBlock verifies RoomPartyService::GetMaxVisitor caps (5/7/10/20 by tileSize)
// and bidirectional block enforcement on invite and enter.
func TestPartyCapacityAndBlock(t *testing.T) {
	accs := map[string]*store.Account{
		"h": {Aid: "11", Name: "Ann", SessionKey: testSessionKey},
	}
	var friends []store.Friendship
	for i := 20; i <= 26; i++ {
		id := string(rune('a' + (i - 20)))
		aid := strconv.Itoa(i)
		accs[id] = &store.Account{Aid: aid, Name: "G" + aid, SessionKey: testSessionKey}
		friends = append(friends, store.Friendship{A: "11", B: aid, State: "accepted"})
	}
	accs["blk"] = &store.Account{Aid: "30", Name: "Blk", Blocked: []string{"11"}, SessionKey: testSessionKey}
	friends = append(friends, store.Friendship{A: "11", B: "30", State: "accepted"})
	testutil.InstallAccounts(t, accs)
	store.SocialMu.Lock()
	old := store.Friendships
	store.Friendships = friends
	store.SocialMu.Unlock()
	t.Cleanup(func() { store.SocialMu.Lock(); store.Friendships = old; store.SocialMu.Unlock() })

	if got := partyMaxVisitors(11); got != 5 {
		t.Fatalf("default 12x12 maxVisitors = %d, want 5", got)
	}
	h := &partyHub{}
	host := partyLogin(t, h, 11, 1)
	host.enterHost(11)
	host.start(nil)
	host.read()

	// Blocked user cannot be invited or enter.
	host.send(7, pbVar(nil, 1, 30))
	host.expect(7, pbVar(nil, 2, 30))
	blk := partyLogin(t, h, 30, 9)
	blk.enter(11)
	blk.expect(5, pbLen(pbVar(pbVar(nil, 1, partyEnterInvalid), 2, 11), 4, []byte("Ann")))

	// Fill 5 visitor slots (20..24); 6th visitor (25) is refused.
	for i := uint64(20); i <= 24; i++ {
		g := partyLogin(t, h, i, byte(i))
		g.enter(11)
		g.expect(5, []byte{0x08, 0, 0x10, 11, 0x28, 2})
		g.start(nil)
		for g.drain() {
		}
	}
	g6 := partyLogin(t, h, 25, 25)
	g6.enter(11)
	g6.expect(5, pbLen(pbVar(pbVar(nil, 1, partyEnterInvalid), 2, 11), 4, []byte("Ann")))

	// Expanding LEVEL_1 to 24x24 raises cap to 7 -> g6 can now enter.
	store.AccountsMu.Lock()
	store.AccountByAidLocked("11").Remodel = &store.RoomRemodel{FloorSizes: map[string]int{"LEVEL_1": 24}}
	store.AccountsMu.Unlock()
	if got := partyMaxVisitors(11); got != 7 {
		t.Fatalf("24x24 maxVisitors = %d, want 7", got)
	}
	g6.enter(11)
	g6.expect(5, []byte{0x08, 0, 0x10, 11, 0x28, 2})
}

// TestPartyPlayerInteraction verifies cr_player_interaction_req (14) -> rc_player_interaction_all (17),
// late-joiner PlayerInfo field 4 persistence, and clearing on move/floor change.
func TestPartyPlayerInteraction(t *testing.T) {
	h := partySetup(t)
	store.SocialMu.Lock()
	store.Friendships = []store.Friendship{
		{A: "11", B: "12", State: "accepted"},
		{A: "11", B: "13", State: "accepted"},
	}
	store.SocialMu.Unlock()
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	c := partyLogin(t, h, 13, 3)
	a.enterHost(11)
	a.start(nil)
	b.enter(11)
	b.read()
	b.start(nil)
	for _, p := range []*pc{a, b} {
		for p.drain() {
		}
	}

	inter := pbVar(pbVar(nil, 1, 100), 7, 2) // _player_interaction
	a.send(14, pbLen(nil, 1, inter))
	b.expect(17, pbLen(pbVar(nil, 1, 11), 2, inter))
	a.none()

	// Late joiner c receives a's active interaction inside PlayerInfo field 4 of idx29.
	c.enter(11)
	c.read()
	c.start(nil)
	c.read() // idx1 (a)
	c.read() // idx1 (b)
	id, body := c.read()
	if id != 29 {
		t.Fatalf("late joiner got msgid %d, want 29", id)
	}
	if !bytes.Contains(body, pbLen(nil, 4, inter)) {
		t.Fatalf("late joiner idx29 missing active interaction field 4: %x", body)
	}

	// Normal move clears active interaction.
	a.send(8, pbLen(nil, 1, spawnMove(6, 6)))
	h.mu.Lock()
	cleared := len(a.s.pm.interaction) == 0
	h.mu.Unlock()
	if !cleared {
		t.Fatal("move did not clear active interaction")
	}
}

// TestPartyPetRelaysWakeAndRide verifies pet move/action/interaction relays (21/22/23 -> 25/26/27),
// pet wake (26 -> 34), pet ride (27 -> 35 with Result=0 OK / 1 refused + late-joiner PetBasicInfo f9/f13),
// BI telemetry (19), and RTT echo (20 -> 24).
func TestPartyPetRelaysWakeAndRide(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"a": {
			Aid: "11", Name: "Ann", SessionKey: testSessionKey,
			Pets: []store.PetItem{
				{ID: 1101, Cd: "PUPE0007V", Name: "RidePet", Rep: true},
				{ID: 1102, Cd: "PUPE0000G", Name: "WalkPet", Room: "LEVEL_1"},
			},
		},
		"b": {Aid: "12", Name: "Bo", SessionKey: testSessionKey},
		"c": {Aid: "13", Name: "Cy", SessionKey: testSessionKey},
	})
	squarePetFixture(t, map[string][]string{
		"PUPE0007V": {"skeleton.xml", "dp.png", "ride.aniproj"}, // URIDE
		"PUPE0000G": {"skeleton.xml", "dp.png"},
	})
	store.SocialMu.Lock()
	old := store.Friendships
	store.Friendships = []store.Friendship{
		{A: "11", B: "12", State: "accepted"},
		{A: "11", B: "13", State: "accepted"},
	}
	store.SocialMu.Unlock()
	t.Cleanup(func() { store.SocialMu.Lock(); store.Friendships = old; store.SocialMu.Unlock() })

	h := &partyHub{}
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	c := partyLogin(t, h, 13, 3)
	a.enterHost(11)
	a.start(nil)
	b.enter(11)
	b.read()
	b.start(nil)
	for _, p := range []*pc{a, b} {
		for p.drain() {
		}
	}

	// Pet move (21 -> 25) to others only.
	pmove := pbVar(nil, 1, 1101)
	a.send(21, pbLen(nil, 1, pmove))
	b.expect(25, pbLen(pbVar(nil, 1, 11), 2, pmove))
	a.none()

	// Pet action (22 -> 26) to others only.
	pact := pbVar(pbVar(nil, 1, 3), 2, 1)
	a.send(22, pbLen(pbVar(nil, 1, 1101), 2, pact))
	b.expect(26, pbLen(pbVar(pbVar(nil, 1, 11), 2, 1101), 3, pact))
	a.none()

	// Pet interaction (23 -> 27) to all on floor (incl. sender).
	pinter := pbVar(pbVar(nil, 1, 0), 7, 28)
	a.send(23, pbLen(pbVar(nil, 1, 1101), 2, pinter))
	wantInter := pbLen(pbVar(pbVar(nil, 1, 11), 2, 1101), 3, pinter)
	a.expect(27, wantInter)
	b.expect(27, wantInter)

	// Pet wake (26 -> 34) to sender.
	a.send(26, pbVar(nil, 1, 1102))
	a.expect(34, pbVar(nil, 1, 1102))
	b.none()

	// Pet ride (27 -> 35): non-rideable pet refused (Result=1) to sender only.
	a.send(27, pbVar(pbVar(nil, 1, 1102), 2, 1))
	a.expect(35, pbVar(pbVar(pbVar(pbVar(nil, 1, 1), 2, 11), 3, 1102), 4, 0))
	b.none()

	// Owned URIDE rep pet succeeds (Result=0) to all on floor.
	a.send(27, pbVar(pbVar(nil, 1, 1101), 2, 1))
	wantRide := pbVar(pbVar(pbVar(pbVar(nil, 1, 0), 2, 11), 3, 1101), 4, 1)
	a.expect(35, wantRide)
	b.expect(35, wantRide)

	// Late joiner c receives PetBasicInfo with rideable=1 (f9) and ride=1 (f13).
	c.enter(11)
	c.read()
	c.start(nil)
	c.read() // idx1 (a)
	c.read() // idx1 (b)
	id, body := c.read()
	if id != 29 {
		t.Fatalf("late joiner got msgid %d, want 29", id)
	}
	if err := lpCheck(body, lpRelay, "rc_floor_relaystart_res"); err != nil {
		t.Fatalf("late joiner idx29 failed IsInitialized check: %v", err)
	}
	var gotRideable, gotRide uint64
	pbScan(body, func(f, _ uint64, d []byte) {
		if f != 3 || d == nil {
			return
		}
		var pid, f9, f13 uint64
		pbScan(d, func(pf, pv uint64, _ []byte) {
			switch pf {
			case 2:
				pid = pv
			case 9:
				f9 = pv
			case 13:
				f13 = pv
			}
		})
		if pid == 1101 {
			gotRideable, gotRide = f9, f13
		}
	})
	if gotRideable != 1 || gotRide != 1 {
		t.Fatalf("late joiner ride pet f9=%d f13=%d, want 1, 1", gotRideable, gotRide)
	}
	for _, p := range []*pc{a, b} {
		for p.drain() {
		}
	}

	// Dismount broadcasts RideState=0.
	a.send(27, pbVar(pbVar(nil, 1, 1101), 2, 0))
	wantOff := pbVar(pbVar(pbVar(pbVar(nil, 1, 0), 2, 11), 3, 1101), 4, 0)
	a.expect(35, wantOff)
	b.expect(35, wantOff)
	c.expect(35, wantOff)

	// BI telemetry (19) silently accepted; RTT (20 -> 24) echoed.
	a.send(19, pbVar(nil, 1, 1))
	a.none()
	rtt := []byte{0x09, 1, 2, 3, 4, 5, 6, 7, 8}
	a.send(20, rtt)
	a.expect(24, rtt)
}
