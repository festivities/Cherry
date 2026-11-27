package lpn

import (
	"bytes"
	"cherry/internal/testutil"
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

// newSqClient is a client in a registry where room is room 1 of Treasure Island (other rooms are new ones).
func newSqClient(t *testing.T, room *squareRoom, sid byte) *sqClient {
	t.Helper()
	room.theme, room.number = squareThemeID, 1
	return newSqClientIn(t, &squareRooms{rooms: map[uint64][]*squareRoom{squareThemeID: {room}}}, sid)
}

// newSqClientIn is a client of the shared registry rooms (several Squares in one test).
func newSqClientIn(t *testing.T, rooms *squareRooms, sid byte) *sqClient {
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
	c.sess = &squareSession{rooms: rooms, conn: &lockedConn{Conn: r.c}, logger: log.New(io.Discard, "", 0)}
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

func loginBody(aid uint64) []byte { return loginFor(aid) } // presents the key the installed account holds

// squareTestMaps is the map each listed Square loads (data/squares.json).
var squareTestMaps = map[uint64]string{
	6: "2108176000", 3: "2202250100", 4: "2310272600", 5: "2201202900",
	16: "2202253000", squareCityThemeID: "2401039000", 8: "2202254000", 10: "2212231000",
	27: "2208162700", 17: "2210312600", 15: "2304052600",
}

func (c *sqClient) join(aid uint64) []byte { return c.joinTheme(aid, squareThemeID) }

// joinTheme logs in, enters the Square theme and relays in. Only Treasure Island gets the fishing UI's rod
// list request, and the boss icon (rc58) follows its reply. Treasure Island and LINE City carry NPCs.
func (c *sqClient) joinTheme(aid, theme uint64) []byte {
	c.t.Helper()
	c.send(0, loginBody(aid))
	c.expect(0, []byte{0x08, 0x81, 0x80, 0x08}) // 131073
	c.send(2, pbVar(nil, 1, theme))
	id, enter := c.read()
	if id != 6 {
		c.t.Fatalf("room enter msgid = %d", id)
	}
	if npc := c.checkEnter(enter, theme, squareTestMaps[theme]); npc != (theme == squareThemeID || theme == squareCityThemeID) {
		c.t.Fatalf("theme %d NpcList present = %v", theme, npc)
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
	if theme == squareThemeID {
		c.send(59, nil)
		c.expect(78, nil)
		c.expect(58, pbVar(nil, 1, 1))
	}
	return body
}

// checkEnter decodes rc_room_enter_res (result, NpcList, SquareCode, ThemeId4, MapFileName6,
// PlayerType) and fails unless it names theme and map and carries NpcList (always present, empty when the Square has no
// NPCs, so the client drops the previous Square's); it reports whether the list holds NPCs. Callers that expect a
// specific SquareCode check enterRoom(enter) directly (public rooms omit field 3).
func (c *sqClient) checkEnter(enter []byte, theme uint64, mapName string) (hasNpc bool) {
	c.t.Helper()
	var result, themeID, playerType uint64
	var mapFile string
	var npcList bool
	ok := pbScan(enter, func(f, v uint64, d []byte) {
		switch f {
		case 1:
			result = v
		case 2:
			npcList, hasNpc = true, len(d) > 0
		case 4:
			themeID = v
		case 6:
			mapFile = string(d)
		case 7:
			playerType = v
		}
	})
	if !ok || !npcList || result != squareEnterResult || themeID != theme || mapFile != mapName || playerType != 0 {
		c.t.Fatalf("room enter = %x, want theme %d map %s", enter, theme, mapName)
	}
	return hasNpc
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

// themeInfo is the decoded theme_info fields the list test checks.
type themeInfo struct {
	id, index, group, count, visible, enabled uint64
	name, listName                            string
}

// themeList decodes rc_themelist_res: the theme_info entries (field 1) and the result (field 3).
func themeList(t *testing.T, body []byte) ([]themeInfo, uint64) {
	t.Helper()
	var infos []themeInfo
	var result uint64
	ok := pbScan(body, func(f, v uint64, d []byte) {
		switch {
		case f == 3:
			result = v
		case f == 1 && d != nil:
			var ti themeInfo
			pbScan(d, func(f, v uint64, d []byte) {
				switch f {
				case 1:
					ti.id = v
				case 8:
					ti.index = v
				case 9:
					ti.name = string(d)
				case 11:
					ti.enabled = v
				case 12:
					ti.group = v
				case 14:
					ti.visible = v
				case 16:
					ti.count = v
				case 17:
					ti.listName = string(d)
				}
			})
			infos = append(infos, ti)
		}
	})
	if !ok {
		t.Fatalf("themelist malformed: %x", body)
	}
	return infos, result
}

func TestSquareThemeList(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11))
	a := newSqClient(t, &squareRoom{}, 1)
	a.send(0, loginBody(11))
	a.read()
	a.send(1, pbVar(nil, 1, 0))
	id, body := a.read()
	infos, result := themeList(t, body)
	if id != 1 || result != 0 || len(infos) != 11 {
		t.Fatalf("themelist msgid=%d result=%d entries=%d", id, result, len(infos))
	}
	want := []themeInfo{
		{id: squareCityThemeID, index: 0, group: squareCityThemeID, count: 0, visible: 1, enabled: 1, name: "LINE City", listName: "LINE City"},
		{id: 3, index: 1, group: 3, count: 0, visible: 1, enabled: 1, name: "LINE Café", listName: "LINE Café"},
		{id: 4, index: 2, group: 4, count: 0, visible: 1, enabled: 1, name: "Runway Square", listName: "Runway Square"},
		{id: 5, index: 3, group: 5, count: 0, visible: 1, enabled: 1, name: "Pet Theme Park", listName: "Pet Theme Park"},
		{id: 16, index: 4, group: 16, count: 0, visible: 1, enabled: 1, name: "LINE Park", listName: "LINE Park"},
		{id: 6, index: 5, group: 6, count: 0, visible: 1, enabled: 1, name: "Treasure Island", listName: "Treasure Island"},
		{id: 8, index: 6, group: 8, count: 0, visible: 1, enabled: 1, name: "LINE City Shibuya", listName: "LINE City Shibuya"},
		{id: 10, index: 7, group: 10, count: 0, visible: 1, enabled: 1, name: "Disney Square", listName: "Disney Square"},
		{id: 27, index: 8, group: 27, count: 0, visible: 1, enabled: 1, name: "Disney Disco", listName: "Disney Disco"},
		{id: 17, index: 9, group: 17, count: 0, visible: 1, enabled: 1, name: "Hatsune Miku Square", listName: "Hatsune Miku Square"},
		{id: 15, index: 10, group: 15, count: 0, visible: 1, enabled: 1, name: "SANRIO CHARACTERS Square", listName: "SANRIO CHARACTERS Square"},
	}
	for i := range want {
		if infos[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, infos[i], want[i])
		}
	}
}

func TestSquareRosterAndBroadcasts(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33))
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
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{22}))
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

	// PLAN 8.6b part 5: a move or an action over maxRelayPayload goes to nobody.
	big := bytes.Repeat([]byte{0x08, 0x01}, maxRelayPayload/2+1)
	a.send(6, pbLen(nil, 1, big))
	a.send(9, pbLen(nil, 1, big))
	b.none()

	// Late joiner sees A at its last move target.
	c := newSqClient(t, room, 3)
	rc := c.join(33)
	if !bytes.Contains(rc, squarePositionInfo(700, 800, 0)) {
		t.Fatalf("roster C lacks A's updated position: %x", rc)
	}
	a.read() // adduser C
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{33}))
	b.read()
	b.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{33}))

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
	testutil.InstallAccounts(t, map[string]*store.Account{"sq-test": {Aid: "9001", Name: "Mia", Gender: "MALE", ItemCodes: []string{"CUON004TV", "UNKNOWN01"}}})

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
		"CAFA0005O": 233900204, "CAEY0000G": 234100016, // animal skin and eyes: 2e8 + 3e7 (type A) + category*1e5 + base36
	} {
		if got, ok := SquareNumericItem(code); !ok || got != want {
			t.Fatalf("%s = %d %v, want %d", code, got, ok, want)
		}
	}
	for _, bad := range []string{"", "CUXX00001", "XXON004TV", "CUON004T", "CUON004T!", "RUWA000HR"} {
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
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22))
	room := &squareRoom{}
	a, b := newSqClient(t, room, 1), newSqClient(t, room, 2)
	a.join(11)
	b.join(22)
	a.read() // adduser for 22
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{22}))
	room.mu.Lock()
	if p := room.players; p[0].x != 5400 || p[0].y != 2960 || p[1].x != 5400 || p[1].y != 2960 { // both on Treasure Island's spawn (73,98): a stepped spawn can be a blocked tile
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
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22))
	room := &squareRoom{}
	a, b := newSqClient(t, room, 1), newSqClient(t, room, 2)
	a.join(11)
	b.join(22)
	a.read() // adduser 22
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{22}))
	a.send(6, pbLen(nil, 1, squarePositionInfo(7000, 2800, 2)))
	b.read() // move_all

	// Back -> GO on the same Square: enter + relaystart again on the same tunnel.
	a.send(2, pbVar(nil, 1, squareThemeID))
	a.read() // room enter reply
	a.send(5, nil)
	id, roster := a.read()
	if id != 8 || !bytes.Contains(roster, squarePositionInfo(7000, 2800, 2)) {
		t.Fatalf("re-enter roster = %d/%x, want own last position", id, roster)
	}
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{22}))
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

// sitInfo is the cr_player_interaction_req Info (_player_interaction) for a sit on chair object 77.
func sitInfo() []byte {
	b := pbVar(pbVar(pbVar(pbVar(nil, 1, 6900), 2, 2940), 3, 6960), 4, 2420)
	return pbVar(pbVar(pbVar(b, 5, 2), 6, 77), 7, 1)
}

// Sit: the request is relayed to the others as rc_player_interaction_all{Aid, Info}, never echoed.
// A late joiner sees the seat in PlayerInfo.Interaction; walking stands the sitter up for everyone.
func TestSquareSitRelayAndStandUp(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33, 44, 55))
	room := &squareRoom{}
	a, b := newSqClient(t, room, 1), newSqClient(t, room, 2)
	a.join(11)
	b.join(22)
	a.read() // adduser 22
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{22}))
	info := sitInfo()

	a.send(13, pbLen(nil, 1, info))
	b.expect(15, pbLen(pbVar(nil, 1, 11), 2, info))
	a.none()
	a.send(13, pbLen(nil, 1, []byte{0x08})) // malformed Info is dropped, not relayed
	b.none()

	c := newSqClient(t, room, 3)
	if rc := c.join(33); !bytes.Contains(rc, pbLen(nil, 4, info)) {
		t.Fatalf("roster C lacks A's seat: %x", rc)
	}
	a.read() // adduser 33
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{33}))
	b.read()
	b.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{33}))

	// Walking stands A up: move_all reaches B and C, and a later joiner no longer sees the seat.
	walk := squarePositionInfo(7000, 2800, 2)
	a.send(6, pbLen(nil, 1, walk))
	b.expect(9, pbLen(pbVar(nil, 1, 11), 2, walk))
	c.expect(9, pbLen(pbVar(nil, 1, 11), 2, walk))
	d := newSqClient(t, room, 4)
	if rd := d.join(44); bytes.Contains(rd, pbLen(nil, 4, info)) {
		t.Fatalf("roster D still shows A seated after walking: %x", rd)
	}
	a.read()
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{44}))
	b.read()
	b.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{44}))
	c.read()
	c.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{44}))

	// Leave while seated: the deluser removes A and its seat for everyone; a later joiner sees neither.
	a.send(13, pbLen(nil, 1, info))
	b.expect(15, pbLen(pbVar(nil, 1, 11), 2, info))
	c.expect(15, pbLen(pbVar(nil, 1, 11), 2, info))
	d.expect(15, pbLen(pbVar(nil, 1, 11), 2, info))
	a.sess.close()
	for _, p := range []*sqClient{b, c, d} {
		p.expect(3, []byte{0x08, 11, 0x10, 0})
	}
	e := newSqClient(t, room, 5)
	if re := e.join(55); bytes.Contains(re, pbLen(nil, 4, info)) || bytes.Contains(re, pbVar(nil, 1, 11)) {
		t.Fatalf("roster E shows departed A: %x", re)
	}
}

// Re-entry while seated (Back -> GO on the same Square): the client starts standing, so the others
// get a move to its stored position (not a deluser/adduser) and the roster carries no seat for it.
func TestSquareReenterStandsUpSeat(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22))
	room := &squareRoom{}
	a, b := newSqClient(t, room, 1), newSqClient(t, room, 2)
	a.join(11)
	b.join(22)
	a.read() // adduser 22
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{22}))
	a.send(13, pbLen(nil, 1, sitInfo()))
	b.expect(15, pbLen(pbVar(nil, 1, 11), 2, sitInfo()))

	a.send(2, pbVar(nil, 1, squareThemeID))
	a.read() // room enter reply
	a.send(5, nil)
	id, roster := a.read()
	if id != 8 || bytes.Contains(roster, pbLen(nil, 4, sitInfo())) {
		t.Fatalf("re-enter roster = %d/%x, want no own seat", id, roster)
	}
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{22}))
	b.expect(9, pbLen(pbVar(nil, 1, 11), 2, squarePositionInfo(5400, 2960, 0))) // Treasure Island's spawn
	b.none()
}

// TestLockedConnClosesOnWriteError: after one failed write the connection is closed (its read loop ends and runs the
// leave) and later writes fail at once instead of waiting squareWriteTTL each.
func TestLockedConnClosesOnWriteError(t *testing.T) {
	ours, peer := net.Pipe()
	peer.Close()
	c := &lockedConn{Conn: ours}
	if _, err := c.Write([]byte("x")); err == nil {
		t.Fatal("write to a closed peer succeeded")
	}
	if c.broken == nil {
		t.Fatal("failed write not remembered")
	}
	if _, err := c.Write([]byte("y")); err != c.broken {
		t.Fatalf("second write err=%v, want the first error %v", err, c.broken)
	}
	// A closed local end reads io.ErrClosedPipe; a merely closed peer would read io.EOF.
	if _, err := ours.Read(make([]byte, 1)); err != io.ErrClosedPipe {
		t.Fatalf("read after a failed write: %v, want our end closed", err)
	}
}
