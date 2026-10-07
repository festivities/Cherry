package lpn

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"net"
	"testing"
	"time"

	"cherry/internal/store"
)

type sqClient struct {
	t    *testing.T
	sess *squareSession
	peer net.Conn // what the gateway client would read
	sid  [8]byte
}

func newSqClient(t *testing.T, room *squareRoom, sid byte) *sqClient {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() { c, err := ln.Accept(); ch <- res{c, err} }()
	peer, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { peer.Close(); r.c.Close() })
	c := &sqClient{t: t, peer: peer}
	c.sid[7] = sid
	c.sess = &squareSession{room: room, conn: &lockedConn{Conn: r.c}, logger: log.New(io.Discard, "", 0)}
	return c
}

// req builds the frame body (without length prefix) the client would send.
func (c *sqClient) req(msgid uint16, body []byte) []byte {
	f := append([]byte{'G', 0x30}, c.sid[:]...)
	f = binary.BigEndian.AppendUint16(f, msgid)
	return append(f, body...)
}

func (c *sqClient) send(msgid uint16, body []byte) {
	c.t.Helper()
	if err := c.sess.handleData(c.req(msgid, body)); err != nil {
		c.t.Fatalf("handleData(%d): %v", msgid, err)
	}
}

// read returns one server frame as (msgid, body) after checking its envelope.
func (c *sqClient) read() (uint16, []byte) {
	c.t.Helper()
	_ = c.peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	var pre [4]byte
	if _, err := io.ReadFull(c.peer, pre[:]); err != nil {
		c.t.Fatalf("read prefix: %v", err)
	}
	f := make([]byte, binary.BigEndian.Uint32(pre[:]))
	if _, err := io.ReadFull(c.peer, f); err != nil {
		c.t.Fatalf("read frame: %v", err)
	}
	if f[0] != 'G' || f[1] != 0x30 || !bytes.Equal(f[2:10], c.sid[:]) {
		c.t.Fatalf("envelope = %x, want G 30 sid", f[:10])
	}
	return binary.BigEndian.Uint16(f[10:12]), f[12:]
}

func (c *sqClient) expect(msgid uint16, body []byte) {
	c.t.Helper()
	id, got := c.read()
	if id != msgid || !bytes.Equal(got, body) {
		c.t.Fatalf("frame = %d/%x, want %d/%x", id, got, msgid, body)
	}
}

func (c *sqClient) none() {
	c.t.Helper()
	_ = c.peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var b [1]byte
	if n, err := c.peer.Read(b[:]); n != 0 || err == nil {
		c.t.Fatalf("unexpected frame data n=%d err=%v", n, err)
	}
}

func loginBody(aid uint64) []byte { return pbVar(pbLen(nil, 1, []byte("sk")), 2, aid) }

func (c *sqClient) join(aid uint64) []byte {
	c.t.Helper()
	c.send(0, loginBody(aid))
	c.expect(0, []byte{0x08, 0x81, 0x80, 0x08}) // 131073
	c.send(2, pbVar(nil, 1, 1))
	c.expect(6, append(append([]byte{0x08, 0x81, 0x80, 0x0c, 0x18, 1, 0x20, 1, 0x28, 1, 0x32, 10}, squareMapFileName...), 0x38, 0))
	c.send(5, nil)
	id, body := c.read()
	if id != 8 {
		c.t.Fatalf("relaystart msgid = %d, want 8", id)
	}
	return body
}

func relayResultBytes() []byte { return pbVar(nil, 1, squareRelayResult) }

func TestSquareOpenControl(t *testing.T) {
	c := newSqClient(t, &squareRoom{}, 1)
	open := append([]byte{'G', 0x31}, c.sid[:]...)
	if err := c.sess.handleOpen(open); err != nil {
		t.Fatal(err)
	}
	_ = c.peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, 22)
	if _, err := io.ReadFull(c.peer, got); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0, 0, 0, 18}, open...)
	want = append(want, make([]byte, 8)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("control = %x, want %x", got, want)
	}
}

func TestSquareThemeList(t *testing.T) {
	room := &squareRoom{}
	a := newSqClient(t, room, 1)
	a.send(0, loginBody(11))
	a.read()
	a.send(1, pbVar(nil, 1, 0))
	theme := []byte{0x08, 1, 0x10, 0, 0x3a, 0, 0x40, 0, 0x4a, 6}
	theme = append(theme, "Square"...)
	theme = append(theme, 0x52, 0, 0x58, 1, 0x60, 0, 0x70, 1, 0x80, 1, 0, 0x8a, 1, 6)
	theme = append(theme, "Square"...)
	// field 16 (userCount) is a two-byte key: 0x80 0x01.
	want := append([]byte{0x0a, byte(len(theme))}, theme...)
	want = append(want, 0x18, 0)
	a.expect(1, want)
}

func TestSquareRosterAndBroadcasts(t *testing.T) {
	room := &squareRoom{}
	a, b := newSqClient(t, room, 1), newSqClient(t, room, 2)

	rosterA := a.join(11)
	if !bytes.HasPrefix(rosterA, relayResultBytes()) {
		t.Fatalf("relaystart A = %x", rosterA)
	}
	rest := rosterA[len(relayResultBytes()):]
	if rest[0] != 0x12 || bytes.Count(rest, []byte{0x12}) < 1 {
		t.Fatalf("roster A = %x", rest)
	}
	aidA, _ := protobufUintField(rest[2:], 1)
	if aidA != 11 {
		t.Fatalf("A roster first aid = %d, want 11", aidA)
	}
	a.none()

	rosterB := b.join(22)
	var aids []uint64
	body := rosterB[len(relayResultBytes()):]
	for len(body) > 0 {
		if body[0] != 0x12 {
			t.Fatalf("roster field = %x", body[0])
		}
		n := int(body[1])
		aid, _ := protobufUintField(body[2:2+n], 1)
		aids = append(aids, aid)
		body = body[2+n:]
	}
	if len(aids) != 2 || aids[0] != 22 || aids[1] != 11 {
		t.Fatalf("roster B aids = %v, want [22 11] (self first)", aids)
	}
	// A is told about B via adduser_all (msgid 2, field 1 = PlayerInfo).
	id, add := a.read()
	if id != 2 || add[0] != 0x0a {
		t.Fatalf("adduser = %d/%x", id, add)
	}
	if aid, _ := protobufUintField(add[2:], 1); aid != 22 {
		t.Fatalf("adduser aid = %d, want 22", aid)
	}
	b.none()

	// Move: others only, verbatim moveInfo.
	move := pbVar(pbVar(pbVar(pbVar(nil, 1, 5), 2, 6), 3, 700), 4, 800)
	a.send(6, pbLen(nil, 1, move))
	b.expect(9, pbLen(pbVar(nil, 1, 11), 2, move))
	a.none()

	// Chat: everyone including sender, incrementing serial.
	b.send(8, pbLen(nil, 1, []byte("hi")))
	want := pbVar(pbLen(pbVar(pbVar(nil, 1, 22), 2, 1), 3, []byte("hi")), 4, 0)
	a.expect(11, want)
	b.expect(11, want)

	// Invalid UTF-8 and oversized chat are dropped.
	b.send(8, pbLen(nil, 1, []byte{0xff, 0xfe}))
	b.send(8, pbLen(nil, 1, bytes.Repeat([]byte("x"), squareMaxChatLen+1)))
	a.none()
	b.none()

	// Action: others only.
	act := pbVar(nil, 1, 3)
	a.send(9, pbLen(nil, 1, act))
	b.expect(12, pbLen(pbVar(nil, 1, 11), 2, act))
	a.none()

	// Late joiner sees A at its last move target.
	c := newSqClient(t, room, 3)
	rc := c.join(33)
	if !bytes.Contains(rc, squarePositionInfo(700, 800, 0)) {
		t.Fatalf("roster C lacks A's updated position: %x", rc)
	}
	a.read() // adduser C
	b.read()

	// Exit: reply, then deluser to the rest.
	a.send(4, nil)
	a.expect(4, []byte{0x08, 0})
	b.expect(3, []byte{0x08, 11, 0x10, 0})
	c.expect(3, []byte{0x08, 11, 0x10, 0})
	a.none()
	if n := len(room.members(nil)); n != 2 {
		t.Fatalf("members after exit = %d, want 2", n)
	}

	// EOF/error path: close() unregisters and broadcasts deluser once.
	b.sess.close()
	b.sess.close()
	c.expect(3, []byte{0x08, 22, 0x10, 0})
	c.none()
}

func TestSquareInvalidLogin(t *testing.T) {
	a := newSqClient(t, &squareRoom{}, 1)
	for _, body := range [][]byte{
		nil,
		pbVar(nil, 2, 5),                         // no session key
		pbLen(nil, 1, []byte("sk")),              // no aid
		pbVar(pbLen(nil, 1, []byte("sk")), 2, 0), // aid 0
		{0x0a, 0x05, 'a'},                        // truncated
	} {
		a.send(0, body)
		a.none()
		if a.sess.loggedIn {
			t.Fatalf("login accepted for %x", body)
		}
	}
	// Requests before login are ignored.
	a.send(2, nil)
	a.none()
}

func TestSquareSelfAvatarInfo(t *testing.T) {
	store.AccountsMu.Lock()
	store.Accounts["sq-test"] = &store.Account{Aid: "9001", Name: "Mia", Gender: "MALE", ItemCodes: []string{"CUON004TV", "UNKNOWN01"}}
	store.AccountsMu.Unlock()
	defer func() { store.AccountsMu.Lock(); delete(store.Accounts, "sq-test"); store.AccountsMu.Unlock() }()

	want := pbLen(nil, 1, []byte("9001"))
	want = pbLen(want, 3, []byte("Mia"))
	want = pbVar(want, 4, 0)
	want = pbLen(want, 6, []byte("JP"))
	want = pbLen(want, 10, []byte("1"))
	want = pbLen(want, 24, pbVar(nil, 1, 225106259))
	want = pbLen(want, 24, pbVar(nil, 1, 225300350))
	want = pbLen(want, 24, pbVar(nil, 1, 224400001))
	if got := squareAvatarInfo(9001); !bytes.Equal(got, want) {
		t.Fatalf("avatar = %x, want %x", got, want)
	}
}

func TestSquarePositionInfo(t *testing.T) {
	want := []byte{0x08, 1, 0x10, 2, 0x18, 1, 0x20, 2, 0x28, 3, 0x30, 1}
	if got := squarePositionInfo(1, 2, 3); !bytes.Equal(got, want) {
		t.Fatalf("position = %x, want %x", got, want)
	}
}

func TestSquareNumericItem(t *testing.T) {
	for code, want := range map[string]uint64{
		"CUHA0036Z": 224404139, "CUON004TV": 225106259, "CUSH00267": 225302815, "CUAH004JH": 225405885,
	} {
		if got, ok := SquareNumericItem(code); !ok || got != want {
			t.Fatalf("%s = %d %v, want %d", code, got, ok, want)
		}
	}
	for _, bad := range []string{"", "CUXX00001", "XXON004TV", "CUON004T", "CUON004T!"} {
		if _, ok := SquareNumericItem(bad); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestPBSummaryHidesContents(t *testing.T) {
	got := pbSummary(pbLen(pbVar(nil, 1, 1500), 3, []byte("secret")))
	if got != " 1=1500 3:6" {
		t.Fatalf("pbSummary = %q", got)
	}
}

func TestSquarePlayerListAndSpawn(t *testing.T) {
	room := &squareRoom{}
	a, b := newSqClient(t, room, 1), newSqClient(t, room, 2)
	a.join(11)
	b.join(22)
	a.read() // adduser for 22
	room.mu.Lock()
	if p := room.players; p[0].x != 6920 || p[0].y != 2920 || p[1].x != 6960 || p[1].y != 2900 {
		t.Fatalf("spawns = (%d,%d) (%d,%d)", p[0].x, p[0].y, p[1].x, p[1].y)
	}
	room.mu.Unlock()
	a.send(24, nil)
	id, body := a.read()
	count, _ := protobufUintField(body, 1)
	if id != 24 || count != 2 || bytes.Count(body, []byte{0x1a}) < 2 {
		t.Fatalf("player list = %d/%x", id, body)
	}
	a.send(10, pbVar(nil, 1, 11)) // whisper-state notification: no reply
	a.none()
}

func TestSquareReenterKeepsAvatarForOthers(t *testing.T) {
	room := &squareRoom{}
	a, b := newSqClient(t, room, 1), newSqClient(t, room, 2)
	a.join(11)
	b.join(22)
	a.read() // adduser 22
	a.send(6, pbLen(nil, 1, squarePositionInfo(7000, 2800, 2)))
	b.read() // move_all

	// Back -> GO on the same Square: enter + relaystart again on the same tunnel.
	a.send(2, pbVar(nil, 1, 1))
	a.read() // room enter reply
	a.send(5, nil)
	id, roster := a.read()
	if id != 8 || !bytes.Contains(roster, squarePositionInfo(7000, 2800, 2)) {
		t.Fatalf("re-enter roster = %d/%x, want own last position", id, roster)
	}
	aid, _ := protobufUintField(roster[len(relayResultBytes())+2:], 1)
	if aid != 11 {
		t.Fatalf("re-enter roster first aid = %d, want 11", aid)
	}
	b.none() // no deluser/adduser: B keeps showing A
	if n := len(room.members(nil)); n != 2 {
		t.Fatalf("members = %d, want 2", n)
	}
	// Moves after re-entry still reach B.
	a.send(6, pbLen(nil, 1, squarePositionInfo(7040, 2780, 1)))
	b.expect(9, pbLen(pbVar(nil, 1, 11), 2, squarePositionInfo(7040, 2780, 1)))
}
