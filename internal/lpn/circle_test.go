package lpn

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// Strict shapes of the circle messages this file reads (lpCheck: unknown fields and wrong wire types fail). req marks
// the fields a success reply always carries; a refusal carries only the result, so the reply-level tables leave the
// rest optional. Field numbers are appendix A and C of circle-spec.md.
var (
	cirItem       = lpMsg{1: {wire: 0, req: true}}
	cirAvatar     = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true}, 3: {wire: 0}, 9: {wire: 2}, 14: {wire: 2, rep: true, msg: cirItem}}
	cirMove       = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}, 4: {wire: 0, req: true}, 5: {wire: 0, req: true}, 6: {wire: 0, req: true}}
	cirPlayer     = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true, msg: cirAvatar}, 3: {wire: 2, req: true, msg: cirMove}, 7: {wire: 0, req: true}}
	cirMemberInfo = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2}, 3: {wire: 2}, 5: {wire: 0, req: true}, 7: {wire: 2}, 8: {wire: 0}}
	cirChatInfo   = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2}, 3: {wire: 0, req: true}, 4: {wire: 0}, 5: {wire: 2, req: true}, 6: {wire: 0}}
	cirResult     = lpMsg{1: {wire: 0, req: true}}
	cirLogin      = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0}}
	cirEnter      = lpMsg{1: {wire: 0, req: true}, 3: {wire: 0}, 4: {wire: 2}, 5: {wire: 0}, 6: {wire: 2, rep: true, msg: cirMemberInfo}}
	cirRelay      = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: cirPlayer}}
	cirAdd        = lpMsg{1: {wire: 2, req: true, msg: cirPlayer}}
	cirDel        = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0}}
	cirMoveAll    = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true, msg: cirMove}}
	cirActionAll  = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true}}
	cirChat       = lpMsg{1: {wire: 2, req: true, msg: cirChatInfo}}
	cirOldChat    = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: cirChatInfo}}
	cirMemberList = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: cirMemberInfo}, 3: {wire: 0}, 4: {wire: 0}, 5: {wire: 0}}
	cirChanged    = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: cirMemberInfo}}
	cirFire       = lpMsg{1: {wire: 0, req: true}, 3: {wire: 0, rep: true}}
	cirMemDelete  = lpMsg{1: {wire: 0, rep: true}}
	cirMemChange  = lpMsg{1: {wire: 2, rep: true, msg: cirMemberInfo}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}, 4: {wire: 0, req: true}}
	cirBuff       = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}}
	cirMemAdd     = lpMsg{1: {wire: 2, req: true, msg: cirMemberInfo}}
	cirPoint      = lpMsg{1: {wire: 0}, 2: {wire: 0}, 3: {wire: 0}, 4: {wire: 0}, 5: {wire: 0}, 6: {wire: 0}, 7: {wire: 0}, 8: {wire: 0}, 9: {wire: 0}}
	cirRefresh    = lpMsg{1: {wire: 2, req: true, msg: cirPoint}}
	cirRewardRow  = lpMsg{1: {wire: 0}, 2: {wire: 2}, 3: {wire: 2}, 4: {wire: 0}, 5: {wire: 2}, 6: {wire: 2}, 7: {wire: 2}, 8: {wire: 0}, 9: {wire: 0}}
	cirReward     = lpMsg{1: {wire: 0}, 2: {wire: 2, rep: true, msg: cirRewardRow}, 3: {wire: 2}, 4: {wire: 2}, 5: {wire: 2}}
	cirPointRwd   = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, msg: cirReward}, 3: {wire: 2, msg: cirPoint}}
	cirUpgRow     = lpMsg{1: {wire: 2}, 2: {wire: 2}, 3: {wire: 2}, 4: {wire: 0}, 5: {wire: 0}, 6: {wire: 0}, 7: {wire: 2}, 8: {wire: 0}, 9: {wire: 0}, 10: {wire: 2}, 11: {wire: 2}, 12: {wire: 0}, 13: {wire: 2}, 14: {wire: 0}}
	cirUpgShop    = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, msg: cirUpgRow}}
	cirUpgBuy     = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0}, 3: {wire: 2}}
	cirQuestSub   = lpMsg{1: {wire: 0}, 2: {wire: 0}, 3: {wire: 0}, 4: {wire: 2}, 5: {wire: 0}, 6: {wire: 2}, 7: {wire: 2}, 8: {wire: 0}}
	cirQuestRwd   = lpMsg{1: {wire: 2}, 2: {wire: 0}, 3: {wire: 2}, 4: {wire: 2}, 5: {wire: 2}, 6: {wire: 0}, 7: {wire: 2}, 8: {wire: 0}, 9: {wire: 0}, 10: {wire: 2}}
	cirQuest      = lpMsg{1: {wire: 0}, 2: {wire: 0}, 3: {wire: 2, rep: true, msg: cirQuestSub}, 4: {wire: 2, rep: true, msg: cirQuestRwd}, 5: {wire: 0}}
	cirQuestList  = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: cirQuest}}
)

// cirShapes maps each inbound msgid this file reads to its strict shape. The exit reply is empty and is checked by its test.
var cirShapes = map[uint16]lpMsg{
	circleRcLogin: cirLogin, circleRcRoomEnter: cirEnter, circleRcRelayStart: cirRelay, circleRcAddUser: cirAdd,
	circleRcDelUser: cirDel, circleRcPlayerMove: cirMoveAll, circleRcPlayerAction: cirActionAll, circleRcChat: cirChat,
	circleRcOldChat: cirOldChat, circleRcNoticeClose: cirResult, circleRcMemberList: cirMemberList,
	circleRcAuthority: cirChanged, circleRcMasterChange: cirChanged, circleRcFire: cirFire,
	circleRcMemberDelete: cirMemDelete, circleRcMemberChange: cirMemChange, circleRcSkinInven: cirResult,
	circleRcSkinChange: cirResult, circleRcSkinShop: cirResult, circleRcSkinBuy: cirResult, circleRcUpgradeShop: cirUpgShop,
	circleRcUpgradeBuy: cirUpgBuy, circleRcAchievements: cirResult, circleRcBuffCopy: cirBuff, circleRcPointReward: cirPointRwd,
	circleRcQuestList: cirQuestList, circleRcQuestReward: cirResult, circleRcPetRide: cirResult,
	circleRcMemberAdd: cirMemAdd, circleRcPointRefresh: cirRefresh,
}

// cirClient is one gateway connection of a circle player: the session under test and the peer that reads its frames.
type cirClient struct {
	t    *testing.T
	sess *circleSession
	peer net.Conn
	sid  [8]byte
	aid  uint64
}

func newCirClient(t *testing.T, rooms *circleRooms, chats *circleChats, aid uint64, sid byte) *cirClient {
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
	c := &cirClient{t: t, peer: peer, aid: aid}
	c.sid[7] = sid
	c.sess = &circleSession{rooms: rooms, chats: chats, conn: &lockedConn{Conn: r.c}, logger: log.New(io.Discard, "", 0)}
	return c
}

// req is the frame body the client sends (without the length prefix): type 0x38 (data), session id, msgid, protobuf.
func (c *cirClient) req(msgid uint16, body []byte) []byte {
	f := append([]byte{'G', 0x38}, c.sid[:]...)
	f = binary.BigEndian.AppendUint16(f, msgid)
	return append(f, body...)
}

func (c *cirClient) send(msgid uint16, body []byte) {
	c.t.Helper()
	if err := c.sess.handleData(c.req(msgid, body)); err != nil {
		c.t.Fatalf("handleData(%d): %v", msgid, err)
	}
}

// read returns one server frame as (msgid, body), after checking its envelope and, when the msgid has a table, its shape.
func (c *cirClient) read() (uint16, []byte) {
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
	if f[0] != 'G' || f[1] != 0x38 || !bytes.Equal(f[2:10], c.sid[:]) {
		c.t.Fatalf("envelope = %x, want G 38 sid", f[:10])
	}
	id, body := binary.BigEndian.Uint16(f[10:12]), f[12:]
	if shape, ok := cirShapes[id]; ok {
		if err := lpCheck(body, shape, fmt.Sprintf("msgid%d", id)); err != nil {
			c.t.Fatalf("shape of msgid %d: %v (body %x)", id, err, body)
		}
	}
	return id, body
}

// expect reads one frame and fails unless it is msgid with exactly body.
func (c *cirClient) expect(msgid uint16, body []byte) {
	c.t.Helper()
	id, got := c.read()
	if id != msgid || !bytes.Equal(got, body) {
		c.t.Fatalf("frame = %d/%x, want %d/%x", id, got, msgid, body)
	}
}

// expectID reads one frame, fails unless it has msgid, and returns its body.
func (c *cirClient) expectID(msgid uint16) []byte {
	c.t.Helper()
	id, body := c.read()
	if id != msgid {
		c.t.Fatalf("msgid = %d, want %d (body %x)", id, msgid, body)
	}
	return body
}

// none fails when a frame is waiting on the peer.
func (c *cirClient) none() {
	c.t.Helper()
	_ = c.peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var b [1]byte
	if n, err := c.peer.Read(b[:]); n != 0 || err == nil {
		c.t.Fatalf("unexpected frame data n=%d err=%v", n, err)
	}
}

func cirLoginBody(aid uint64) []byte { return pbVar(pbLen(nil, 1, []byte("sk")), 2, aid) }

// login logs the client in and checks the success result (131073).
func (c *cirClient) login() {
	c.t.Helper()
	c.send(circleCsLogin, cirLoginBody(c.aid))
	c.expect(circleRcLogin, pbVar(nil, 1, circleLoginResult))
}

// enter sends cs_room_enter_req with both fields, as the client does, and returns the reply body.
func (c *cirClient) enter() []byte {
	c.t.Helper()
	c.send(circleCsRoomEnter, pbVar(pbVar(nil, 1, 0), 2, 0))
	return c.expectID(circleRcRoomEnter)
}

// relay sends cs_room_relaystart_req for the circle room's map and returns the roster body.
func (c *cirClient) relay() []byte {
	c.t.Helper()
	c.send(circleCsRelayStart, pbLen(nil, 1, []byte("2202250100")))
	return c.expectID(circleRcRelayStart)
}

// joined logs in, enters and relays (the enter must succeed); it returns the roster.
func (c *cirClient) joined() []byte {
	c.t.Helper()
	c.login()
	if got := cirVar(c.enter(), 1); got != circleEnterResult {
		c.t.Fatalf("aid %d enter result = %d, want %d", c.aid, got, circleEnterResult)
	}
	return c.relay()
}

// cirVar is the last varint of field in data (0 when absent).
func cirVar(data []byte, field uint64) uint64 {
	var v uint64
	pbScan(data, func(f, x uint64, d []byte) {
		if f == field && d == nil {
			v = x
		}
	})
	return v
}

// cirBytes is the last length-delimited value of field in data (nil when absent).
func cirBytes(data []byte, field uint64) []byte {
	var out []byte
	pbScan(data, func(f, _ uint64, d []byte) {
		if f == field && d != nil {
			out = d
		}
	})
	return out
}

// cirList is every length-delimited value of field in data, in order.
func cirList(data []byte, field uint64) [][]byte {
	var out [][]byte
	pbScan(data, func(f, _ uint64, d []byte) {
		if f == field && d != nil {
			out = append(out, d)
		}
	})
	return out
}

// cirVars is every varint of field in data, in order.
func cirVars(data []byte, field uint64) []uint64 {
	var out []uint64
	pbScan(data, func(f, v uint64, d []byte) {
		if f == field && d == nil {
			out = append(out, v)
		}
	})
	return out
}

// cirMember is one CircleMemberInfo as the test reads it: aid, PlayerType and whether Connect is present.
type cirMember struct {
	aid, role uint64
	online    bool
}

func cirMembers(list [][]byte) []cirMember {
	var out []cirMember
	for _, m := range list {
		online := false
		pbScan(m, func(f, _ uint64, _ []byte) {
			if f == 8 {
				online = true
			}
		})
		out = append(out, cirMember{aid: cirVar(m, 1), role: cirVar(m, 5), online: online})
	}
	return out
}

// cirPlayerView is a PlayerInfo as the test reads it: aid, PlayerType, position, name and the avatar's item ids.
type cirPlayerView struct {
	aid, role, x, y uint64
	name            string
	items           []uint64
}

func cirPlayerInfo(p []byte) cirPlayerView {
	mv, av := cirBytes(p, 3), cirBytes(p, 2)
	var items []uint64
	for _, it := range cirList(av, 14) {
		items = append(items, cirVar(it, 1))
	}
	return cirPlayerView{aid: cirVar(p, 1), role: cirVar(p, 7), x: cirVar(mv, 1), y: cirVar(mv, 2), name: string(cirBytes(av, 2)), items: items}
}

// cirScene is the test book: circle A (the first Cherry id) led by 1101, with vice 1102 and members 1103 and 1104;
// circle B (the next id) led by 1105. 1106 is in no circle. Each account has a name, and 1101 owns one item code.
func cirScene(t *testing.T) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{
		"t-1101": {AccessToken: "t-1101", SessionKey: "sk", Aid: "1101", Name: "Leader", Gender: "FEMALE", ItemCodes: []string{"CUON004TV"}},
		"t-1102": {AccessToken: "t-1102", SessionKey: "sk", Aid: "1102", Name: "Vice", Gender: "MALE"},
		"t-1103": {AccessToken: "t-1103", SessionKey: "sk", Aid: "1103", Name: "Member", Gender: "FEMALE"},
		"t-1104": {AccessToken: "t-1104", SessionKey: "sk", Aid: "1104", Name: "Kicked", Gender: "FEMALE"},
		"t-1105": {AccessToken: "t-1105", SessionKey: "sk", Aid: "1105", Name: "Other", Gender: "FEMALE"},
		"t-1106": {AccessToken: "t-1106", SessionKey: "sk", Aid: "1106", Name: "Stranger", Gender: "FEMALE"},
	})
	store.ResetCircles()
	a := economy.CherryCircleFirst + 1
	b := economy.CherryCircleFirst + 2
	store.CirclesMu.Lock()
	store.CircleState = store.CircleBook{NextID: b, Circles: []store.Circle{
		{ID: a, Code: "000001", Name: "Lab", JoinType: "OPEN", Created: 1, Modified: 1, Members: []store.CircleMember{
			{Aid: "1101", Role: "leader", Joined: 1}, {Aid: "1102", Role: "vice", Joined: 2},
			{Aid: "1103", Role: "member", Joined: 3}, {Aid: "1104", Role: "member", Joined: 4},
		}},
		{ID: b, Code: "000002", Name: "Other", JoinType: "OPEN", Created: 1, Modified: 1, Members: []store.CircleMember{
			{Aid: "1105", Role: "leader", Joined: 1},
		}},
	}}
	store.CirclesMu.Unlock()
	t.Cleanup(store.ResetCircles)
}

// cirRegistry is a fresh room and chat registry for one test.
func cirRegistry() (*circleRooms, *circleChats) { return &circleRooms{}, &circleChats{} }

func TestCircleOpenControlEchoesTheTunnel(t *testing.T) {
	rooms, chats := cirRegistry()
	c := newCirClient(t, rooms, chats, 1101, 9)
	open := append([]byte{'G', 0x39}, c.sid[:]...) // the client's open control: type 0x39 (agent 8, subtype 1)
	if err := c.sess.handleOpen(open); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 22)
	binary.BigEndian.PutUint32(want, 18)
	want[4], want[5] = 'G', 0x39
	copy(want[6:14], c.sid[:])
	got := make([]byte, 22)
	_ = c.peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c.peer, got); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("control = %x (err %v), want %x", got, err, want)
	}
}

// TestCircleLoginAndEnterRules: login needs a key, a positive aid and an account. The room is for members only, and a
// member's enter reply names the circle map, the caller's role and the members; the roster is the caller's avatar at the
// spawn tile with its items.
func TestCircleLoginAndEnterRules(t *testing.T) {
	cirScene(t)
	rooms, chats := cirRegistry()
	bad := newCirClient(t, rooms, chats, 9999, 1)
	bad.send(circleCsLogin, cirLoginBody(9999))
	bad.expect(circleRcLogin, pbVar(nil, 1, 0)) // no account: the client's error path, not a spinner
	noKey := newCirClient(t, rooms, chats, 1101, 2)
	noKey.send(circleCsLogin, pbVar(nil, 2, 1101))
	noKey.expect(circleRcLogin, pbVar(nil, 1, 0))

	stranger := newCirClient(t, rooms, chats, 1106, 3)
	stranger.login()
	if got := cirVar(stranger.enter(), 1); got == circleEnterResult {
		t.Fatalf("a player in no circle entered a room (result %d)", got)
	}
	if stranger.sess.player != nil || stranger.sess.entered {
		t.Fatal("refused enter left the session entered")
	}

	leader := newCirClient(t, rooms, chats, 1101, 4)
	leader.login()
	body := leader.enter()
	if cirVar(body, 1) != circleEnterResult || string(cirBytes(body, 4)) != "../world/2202250100" || cirVar(body, 5) != 2 {
		t.Fatalf("enter = %x, want result 196609, map ../world/2202250100, PlayerType 2", body)
	}
	members := cirMembers(cirList(body, 6))
	if len(members) != 4 || members[0].aid != 1101 || members[0].role != 2 || members[1].role != 1 || members[2].role != 0 {
		t.Fatalf("enter members = %+v", members)
	}
	list := cirList(leader.relay(), 2)
	if len(list) != 1 {
		t.Fatalf("roster of one = %d entries", len(list))
	}
	p := cirPlayerInfo(list[0])
	spawnX, spawnY := tilePx(160, Tile{X: 87, Y: 85})
	if p.aid != 1101 || p.role != 2 || p.x != spawnX || p.y != spawnY || p.name != "Leader" {
		t.Fatalf("self in roster = %+v, want the spawn %d,%d", p, spawnX, spawnY)
	}
	if !slices.Contains(p.items, 225106259) { // CUON004TV, owned; the appearance defaults come with it (AppearanceItemCodes)
		t.Fatalf("avatar items = %v, want 225106259 (CUON004TV) among them", p.items)
	}
	leader.none()
}

// TestCircleRelayBeforeEnterIgnored: a relay start with no accepted enter gets no roster.
func TestCircleRelayBeforeEnterIgnored(t *testing.T) {
	cirScene(t)
	rooms, chats := cirRegistry()
	c := newCirClient(t, rooms, chats, 1101, 5)
	c.login()
	c.send(circleCsRelayStart, pbLen(nil, 1, []byte("2202250100")))
	c.none()
}

// TestCircleRoomRelayAndLeave: three members enter and relay (each roster starts with its own avatar, and the others get
// adduser), then move, action and chat are relayed, old chat is served, and an exit and a disconnect send deluser to the
// rest. Another circle's member, in its own room, hears none of it.
func TestCircleRoomRelayAndLeave(t *testing.T) {
	cirScene(t)
	rooms, chats := cirRegistry()
	a := newCirClient(t, rooms, chats, 1101, 10)
	b := newCirClient(t, rooms, chats, 1102, 11)
	c := newCirClient(t, rooms, chats, 1103, 12)
	other := newCirClient(t, rooms, chats, 1105, 13)
	other.joined()

	a.joined()
	rosterB := b.joined()
	if list := cirList(rosterB, 2); len(list) != 2 || cirPlayerInfo(list[0]).aid != 1102 || cirPlayerInfo(list[1]).aid != 1101 {
		t.Fatalf("B's roster is not self first: %d entries", len(list))
	}
	a.expectID(circleRcAddUser) // B
	rosterC := c.joined()
	if list := cirList(rosterC, 2); len(list) != 3 || cirPlayerInfo(list[0]).aid != 1103 {
		t.Fatalf("C's roster = %d entries", len(list))
	}
	a.expectID(circleRcAddUser) // C
	b.expectID(circleRcAddUser) // C
	other.none()

	// move: the others see it, the mover does not
	move := pbVar(pbVar(pbVar(pbVar(pbVar(pbVar(nil, 1, 6480), 2, 2940), 3, 6500), 4, 2900), 5, 2), 6, 1)
	a.send(circleCsPlayerMove, pbLen(nil, 1, move))
	bodyB := b.expectID(circleRcPlayerMove)
	c.expectID(circleRcPlayerMove)
	a.none()
	if cirVar(bodyB, 1) != 1101 || !bytes.Equal(cirBytes(bodyB, 2), move) {
		t.Fatalf("move relay = %x", bodyB)
	}

	// action
	action := pbVar(pbVar(nil, 1, 3), 2, 1)
	a.send(circleCsPlayerAction, pbLen(nil, 1, action))
	bodyB = b.expectID(circleRcPlayerAction)
	c.expectID(circleRcPlayerAction)
	if cirVar(bodyB, 1) != 1101 || !bytes.Equal(cirBytes(bodyB, 2), action) {
		t.Fatalf("action relay = %x", bodyB)
	}

	// chat: everyone in the room, the sender included; a text that is not UTF-8 is dropped
	a.send(circleCsChat, pbLen(pbVar(nil, 2, 0), 1, []byte("hello")))
	for _, cl := range []*cirClient{a, b, c} {
		info := cirBytes(cl.expectID(circleRcChat), 1)
		if cirVar(info, 1) != 1101 || string(cirBytes(info, 2)) != "Leader" || string(cirBytes(info, 5)) != "hello" || cirVar(info, 3) != 1 {
			t.Fatalf("chat info = %x", info)
		}
	}
	a.send(circleCsChat, pbLen(nil, 1, []byte{0xff, 0xfe}))
	a.none()
	b.send(circleCsChat, pbLen(pbVar(nil, 2, 3), 1, []byte("second")))
	for _, cl := range []*cirClient{a, b, c} {
		info := cirBytes(cl.expectID(circleRcChat), 1)
		if cirVar(info, 3) != 2 || cirVar(info, 4) != 3 {
			t.Fatalf("second chat info = %x", info)
		}
	}
	other.none()

	// old chat: the newest page, oldest first; then the page older than serial 2
	c.send(circleCsOldChat, pbVar(nil, 1, 0))
	old := c.expectID(circleRcOldChat)
	msgs := cirList(old, 2)
	if cirVar(old, 1) != 0 || len(msgs) != 2 || cirVar(msgs[0], 3) != 1 || cirVar(msgs[1], 3) != 2 {
		t.Fatalf("old chat = %x", old)
	}
	c.send(circleCsOldChat, pbVar(nil, 1, 2))
	if older := cirList(c.expectID(circleRcOldChat), 2); len(older) != 1 || cirVar(older[0], 3) != 1 {
		t.Fatalf("old chat before serial 2 has %d messages", len(older))
	}

	// exit: an empty reply, and the rest get deluser
	c.send(circleCsRoomExit, nil)
	c.expect(circleRcRoomExit, nil)
	// then the tunnel close (subtype 5, same session id), which takes the client back to its Garden
	var pre [4]byte
	_ = c.peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c.peer, pre[:]); err != nil || binary.BigEndian.Uint32(pre[:]) != 18 {
		t.Fatalf("close control prefix %x err %v", pre, err)
	}
	ctl := make([]byte, 18)
	if _, err := io.ReadFull(c.peer, ctl); err != nil || ctl[0] != 'G' || ctl[1] != 0x3D || !bytes.Equal(ctl[2:10], c.sid[:]) {
		t.Fatalf("close control = %x (err %v), want G 3D sid", ctl, err)
	}
	a.expect(circleRcDelUser, pbVar(pbVar(nil, 1, 1103), 2, 0))
	b.expect(circleRcDelUser, pbVar(pbVar(nil, 1, 1103), 2, 0))

	// disconnect: the connection's presence ends and the rest get deluser
	b.sess.close()
	a.expect(circleRcDelUser, pbVar(pbVar(nil, 1, 1102), 2, 0))
	a.none()
	other.none()
}

// TestCircleMembersAndRoles covers the member list, the rights of a member, a vice and the leader, the pushes to the
// room, a kick and what the kicked avatar gets, a transfer, and a leave over HTTP that drops the leaver from the room.
func TestCircleMembersAndRoles(t *testing.T) {
	cirScene(t)
	rooms, chats := cirRegistry()
	a := newCirClient(t, rooms, chats, 1101, 20)
	b := newCirClient(t, rooms, chats, 1102, 21)
	c := newCirClient(t, rooms, chats, 1103, 22)
	d := newCirClient(t, rooms, chats, 1104, 23)
	a.joined()
	b.joined()
	c.joined()
	d.joined()
	for range 3 {
		a.expectID(circleRcAddUser)
	}
	for range 2 {
		b.expectID(circleRcAddUser)
	}
	c.expectID(circleRcAddUser)

	// member list: four members with roles 2/1/0/0, all four in the room
	c.send(circleCsMemberList, nil)
	list := c.expectID(circleRcMemberList)
	got := cirMembers(cirList(list, 2))
	want := []cirMember{{aid: 1101, role: 2, online: true}, {aid: 1102, role: 1, online: true}, {aid: 1103, role: 0, online: true}, {aid: 1104, role: 0, online: true}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("member list = %+v, want %+v", got, want)
	}
	if cirVar(list, 1) != 0 || cirVar(list, 3) != 1101 || cirVar(list, 4) != 4 || cirVar(list, 5) != 30 {
		t.Fatalf("result/master/count/max = %d/%d/%d/%d, want 0/1101/4/30", cirVar(list, 1), cirVar(list, 3), cirVar(list, 4), cirVar(list, 5))
	}

	// a plain member cannot promote; the leader can, and the room hears it (the leader is the actor, so it is skipped)
	c.send(circleCsAuthority, pbVar(nil, 2, 1))
	c.expect(circleRcAuthority, pbVar(nil, 1, circleRefused))
	a.send(circleCsAuthority, pbVar(pbVar(nil, 1, 1103), 2, 1))
	res := a.expectID(circleRcAuthority)
	if cirVar(res, 1) != 0 || len(cirList(res, 2)) != 1 || cirMembers(cirList(res, 2))[0] != (cirMember{aid: 1103, role: 1, online: true}) {
		t.Fatalf("promotion reply = %x", res)
	}
	for _, cl := range []*cirClient{b, c, d} {
		push := cl.expectID(circleRcMemberChange)
		if mm := cirMembers(cirList(push, 1)); len(mm) != 1 || mm[0].aid != 1103 || mm[0].role != 1 || cirVar(push, 2) != 1101 || cirVar(push, 3) != 4 {
			t.Fatalf("change push = %x", push)
		}
	}
	a.none()

	// the new vice cannot fire a vice; a vice fires a plain member: the reply, then the room gets the delete list, the
	// deluser and the counts; the removed avatar gets the delete list only
	b.send(circleCsFire, pbVar(nil, 1, 1103))
	b.expect(circleRcFire, pbVar(nil, 1, circleRefused))
	b.send(circleCsFire, pbVar(nil, 1, 1104))
	fire := b.expectID(circleRcFire)
	if cirVar(fire, 1) != 0 || fmt.Sprint(cirVars(fire, 3)) != "[1104]" {
		t.Fatalf("fire reply = %x", fire)
	}
	for _, cl := range []*cirClient{a, b, c} {
		if gone := cirVars(cl.expectID(circleRcMemberDelete), 1); fmt.Sprint(gone) != "[1104]" {
			t.Fatalf("delete list = %v, want [1104]", gone)
		}
		cl.expect(circleRcDelUser, pbVar(pbVar(nil, 1, 1104), 2, 0))
		if cnt := cirVar(cl.expectID(circleRcMemberChange), 3); cnt != 3 {
			t.Fatalf("member count push = %d, want 3", cnt)
		}
	}
	if gone := cirVars(d.expectID(circleRcMemberDelete), 1); fmt.Sprint(gone) != "[1104]" {
		t.Fatalf("removed avatar's delete list = %v", gone)
	}
	d.none()

	// the removed avatar's next room request is dropped and nothing is relayed
	d.send(circleCsPlayerMove, pbLen(nil, 1, pbVar(pbVar(pbVar(nil, 1, 1), 3, 2), 4, 2)))
	d.none()
	if d.sess.player != nil || d.sess.entered {
		t.Fatal("removed avatar still in the room")
	}
	a.none()
	b.none()
	c.none()

	// transfer: the vice becomes the leader and the old leader a plain member; the room hears it, the actor included
	a.send(circleCsMasterChange, pbVar(nil, 1, 1102))
	master := a.expectID(circleRcMasterChange)
	mm := cirMembers(cirList(master, 2))
	if cirVar(master, 1) != 0 || len(mm) != 2 || mm[0] != (cirMember{aid: 1102, role: 2, online: true}) || mm[1] != (cirMember{aid: 1101, role: 0, online: true}) {
		t.Fatalf("master reply = %x (%+v)", master, mm)
	}
	if pushed := a.expectID(circleRcMemberChange); cirVar(pushed, 2) != 1102 { // the actor's own push: its MasterAid moved
		t.Fatalf("actor's master push names %d, want 1102", cirVar(pushed, 2))
	}
	for _, cl := range []*cirClient{b, c} {
		if pushed := cl.expectID(circleRcMemberChange); cirVar(pushed, 2) != 1102 {
			t.Fatalf("master push names %d, want 1102", cirVar(pushed, 2))
		}
	}
	a.send(circleCsAuthority, pbVar(pbVar(nil, 1, 1103), 2, 0)) // the old leader has no rights now
	a.expect(circleRcAuthority, pbVar(nil, 1, circleRefused))

	// the old leader, now a plain member, leaves over HTTP: its next room request drops it, and the rest get deluser
	if err := economy.LeaveCircle("t-1101", economy.CherryCircleFirst+1, time.Now()); err != nil {
		t.Fatalf("leave: %v", err)
	}
	a.send(circleCsChat, pbLen(nil, 1, []byte("bye")))
	b.expect(circleRcDelUser, pbVar(pbVar(nil, 1, 1101), 2, 0))
	c.expect(circleRcDelUser, pbVar(pbVar(nil, 1, 1101), 2, 0))
	a.none()
	b.none()
	c.none()
	if a.sess.player != nil || a.sess.entered {
		t.Fatal("left avatar still in the room")
	}
}

// TestCircleChatCapAndPaging: the log keeps the last circleChatCap messages per circle; a page is at most
// circleChatPage messages, oldest first.
func TestCircleChatCapAndPaging(t *testing.T) {
	chats := &circleChats{}
	const id = 1940000001
	for range 205 {
		chats.add(id, circleChatMsg{sendAid: 1101, sendName: "Leader", text: []byte("x")})
	}
	page := chats.before(id, 0, circleChatPage)
	if len(page) != circleChatPage || page[0].serial != 156 || page[len(page)-1].serial != 205 {
		t.Fatalf("newest page serials %d..%d, want 156..205", page[0].serial, page[len(page)-1].serial)
	}
	if n := len(chats.before(id, 6, circleChatPage)); n != 0 {
		t.Fatalf("serial 6 is the oldest kept; the page before it has %d", n)
	}
	if got := chats.before(id, 7, circleChatPage); len(got) != 1 || got[0].serial != 6 {
		t.Fatalf("page before serial 7 = %+v", got)
	}
	if n := len(chats.logs[id]); n != circleChatCap {
		t.Fatalf("kept %d messages, want %d", n, circleChatCap)
	}
}

// TestCircleStubsAreSafe: the features not built yet answer with the empty or refused shape of their reply. Every
// purchase is refused, a real-money id included. Requests with no reply are dropped, and nothing is answered before login.
func TestCircleStubsAreSafe(t *testing.T) {
	cirScene(t)
	rooms, chats := cirRegistry()
	a := newCirClient(t, rooms, chats, 1101, 40)
	a.login()
	a.send(circleCsSkinInven, nil)
	a.expect(circleRcSkinInven, pbVar(nil, 1, 0))
	a.send(circleCsSkinShop, nil)
	a.expect(circleRcSkinShop, pbVar(nil, 1, 0))
	a.send(circleCsSkinChange, pbLen(nil, 1, []byte("CSX")))
	a.expect(circleRcSkinChange, pbVar(nil, 1, circleRefused))
	a.send(circleCsSkinBuy, pbLen(pbLen(pbLen(nil, 1, []byte("CSX")), 2, []byte("GEM")), 4, []byte("cp-real")))
	a.expect(circleRcSkinBuy, pbVar(nil, 1, circleRefused))
	a.send(circleCsUpgradeBuy, pbLen(nil, 4, []byte("cp-real")))
	a.expect(circleRcUpgradeBuy, pbVar(nil, 1, circleRefused))
	a.send(circleCsAchievements, pbVar(nil, 1, 7))
	a.expect(circleRcAchievements, pbVar(nil, 1, 0))
	a.send(circleCsBuffCopy, pbVar(nil, 1, 1103))
	a.expect(circleRcBuffCopy, pbVar(pbVar(pbVar(nil, 1, circleRefused), 2, 1101), 3, 1103))
	a.send(circleCsPointReward, pbVar(nil, 1, 1))
	a.expect(circleRcPointReward, pbVar(nil, 1, circleRefused))
	a.send(circleCsQuestReward, nil) // no mission is claimable yet
	a.expect(circleRcQuestReward, pbVar(nil, 1, circleRefused))
	a.send(circleCsPetRide, pbVar(nil, 1, 5))
	a.expect(circleRcPetRide, pbVar(nil, 1, circleRefused))
	a.send(circleCsNoticeClose, pbVar(nil, 1, 3))
	a.expect(circleRcNoticeClose, pbVar(nil, 1, 0))
	for _, msgid := range []uint16{circleCsButtonUpdate, circleCsPlayerActorStatus, circleCsPlayerInteraction,
		circleCsPlayerDoInter, circleCsPlayerBeginInter, circleCsPlayerEndInter, circleCsNoticeReq, circleCsReportChat,
		circleCsPetAction, circleCsPetInteraction, circleCsPetMove, 99} {
		a.send(msgid, nil)
		a.none()
	}
	pre := newCirClient(t, rooms, chats, 1101, 41)
	pre.send(circleCsSkinInven, nil)
	pre.none()
}

// TestCircleRefusalsForNonMembers: a stranger's rights changes, fire, transfer and member list are refused with the
// refusal result, and its room requests are not answered.
func TestCircleRefusalsForNonMembers(t *testing.T) {
	cirScene(t)
	rooms, chats := cirRegistry()
	s := newCirClient(t, rooms, chats, 1106, 50)
	s.login()
	s.send(circleCsAuthority, pbVar(pbVar(nil, 1, 1103), 2, 1))
	s.expect(circleRcAuthority, pbVar(nil, 1, circleRefused))
	s.send(circleCsFire, pbVar(nil, 1, 1103))
	s.expect(circleRcFire, pbVar(nil, 1, circleRefused))
	s.send(circleCsMasterChange, pbVar(nil, 1, 1103))
	s.expect(circleRcMasterChange, pbVar(nil, 1, circleRefused))
	s.send(circleCsMemberList, nil)
	s.expect(circleRcMemberList, pbVar(nil, 1, circleRefused))
	s.send(circleCsOldChat, pbVar(nil, 1, 0))
	s.expect(circleRcOldChat, pbVar(nil, 1, circleRefused))
}

// TestCircleShapeCheckerRejectsDrift: the strict table refuses a reply with a missing required field, an extra field or
// a wrong wire type, so an edit that breaks the client's parser shows up here.
func TestCircleShapeCheckerRejectsDrift(t *testing.T) {
	missing := pbVar(pbVar(nil, 1, circleEnterResult), 3, 1)
	if err := lpCheck(missing, cirPlayer, "player"); err == nil {
		t.Fatal("a PlayerInfo without its aid, avatar and position was accepted")
	}
	if err := lpCheck(pbVar(pbVar(nil, 1, circleEnterResult), 3, 1), cirEnter, "enter"); err != nil {
		t.Fatalf("a refusal-shaped enter with only the result is refused: %v", err)
	}
	extra := pbVar(pbLen(pbVar(pbVar(nil, 1, circleEnterResult), 3, 1), 4, []byte("2202250100")), 9, 1)
	if err := lpCheck(extra, cirEnter, "enter"); err == nil {
		t.Fatal("unknown field 9 was accepted")
	}
	wrong := pbLen(pbVar(nil, 1, circleEnterResult), 3, []byte("1"))
	if err := lpCheck(wrong, cirEnter, "enter"); err == nil {
		t.Fatal("ClientMapRevision sent as a string was accepted")
	}
	badMember := pbVar(pbVar(nil, 1, 0), 6, 0)
	if err := lpCheck(badMember, cirEnter, "enter"); err == nil {
		t.Fatal("a member entry sent as a varint was accepted")
	}
}

// cirJST is Japan Standard Time for the test clock (the game day starts at 06:00 JST).
var cirJST = time.FixedZone("JST", 9*3600)

// cirAt fixes the economy clock for one test: 2026-11-02 is in the first season (it starts 2026-11-01 06:00 JST).
func cirAt(t *testing.T, ts time.Time) {
	t.Helper()
	prev := economy.NowFunc
	economy.NowFunc = func() time.Time { return ts }
	t.Cleanup(func() { economy.NowFunc = prev })
}

// cirSetGems sets aid's Gem balance for a test.
func cirSetGems(t *testing.T, aid string, gems int64) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	store.AccountByAidLocked(aid).Gems = gems
}

// cirGems is aid's Gem balance.
func cirGems(t *testing.T, aid string) int64 {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return store.AccountByAidLocked(aid).Gems
}

// cirUpgradeReq is cs_circle_upgrade_buy_req: ItemCode, CoinUnit, Price and (when set) Cpid.
func cirUpgradeReq(code, unit string, price uint64, cpid string) []byte {
	b := pbLen(nil, 1, []byte(code))
	b = pbLen(b, 2, []byte(unit))
	b = pbVar(b, 3, price)
	if cpid != "" {
		b = pbLen(b, 4, []byte(cpid))
	}
	return b
}

// TestCirclePointsOverTheWire: a circle's members earn points from their triggers; the sync answers with the point
// state (048), a point step is claimed once per member (049), and an outsider or a repeat claim is refused.
func TestCirclePointsOverTheWire(t *testing.T) {
	cirScene(t)
	cirAt(t, time.Date(2026, 11, 2, 12, 0, 0, 0, cirJST))
	rooms, chats := cirRegistry()
	leader := newCirClient(t, rooms, chats, 1101, 60)
	leader.login()
	member := newCirClient(t, rooms, chats, 1103, 61)
	member.login()
	outsider := newCirClient(t, rooms, chats, 1106, 62)
	outsider.login()
	// Per member: garden 10, square 10, quest 5, gather 10 x 2 and chat 10 x 1 = 55 (under the daily cap of 60).
	for _, aid := range []string{"1101", "1103"} {
		for _, kind := range []string{"garden", "square", "quest"} {
			economy.CirclePointEarn(aid, kind, economy.NowFunc())
		}
		for i := 0; i < 10; i++ {
			economy.CirclePointEarn(aid, "gather", economy.NowFunc())
			economy.CirclePointEarn(aid, "chat", economy.NowFunc())
		}
	}
	leader.send(circleCsPointSync, nil)
	info := cirBytes(leader.expectID(circleRcPointRefresh), 1)
	if cirVar(info, 5) != 110 || cirVar(info, 1) != 2 || cirVar(info, 9) != 1 {
		t.Fatalf("point state after 110 points = seq %d total %d avail %d", cirVar(info, 1), cirVar(info, 5), cirVar(info, 9))
	}
	leader.send(circleCsPointReward, pbVar(nil, 1, 1))
	body := leader.expectID(circleRcPointReward)
	if cirVar(body, 1) != 0 {
		t.Fatalf("step 1 result = %d, want 0 (body %x)", cirVar(body, 1), body)
	}
	if items := cirList(cirBytes(body, 2), 2); len(items) != 1 || cirVar(items[0], 4) != 50 {
		t.Fatalf("step 1 reward rows = %x, want one row of 50", items)
	}
	if g := cirGems(t, "1101"); g != 50 {
		t.Fatalf("leader gems after step 1 = %d, want 50", g)
	}
	leader.send(circleCsPointReward, pbVar(nil, 1, 1))
	if got := cirVar(leader.expectID(circleRcPointReward), 1); got != circleRefused {
		t.Fatalf("second claim by the leader result = %d, want refused", got)
	}
	member.send(circleCsPointReward, pbVar(nil, 1, 1)) // each member claims a step once per season
	if got := cirVar(member.expectID(circleRcPointReward), 1); got != 0 {
		t.Fatalf("member claim result = %d, want 0", got)
	}
	if g := cirGems(t, "1103"); g != 50 {
		t.Fatalf("member gems = %d, want 50", g)
	}
	outsider.send(circleCsPointReward, pbVar(nil, 1, 1))
	if got := cirVar(outsider.expectID(circleRcPointReward), 1); got != circleRefused {
		t.Fatalf("outsider claim result = %d, want refused", got)
	}
	leader.send(circleCsPointReward, pbVar(nil, 1, 9)) // not a step of the season
	if got := cirVar(leader.expectID(circleRcPointReward), 1); got != circleRefused {
		t.Fatalf("unknown step result = %d, want refused", got)
	}
}

// TestCircleMissionsAndUpgradesOverTheWire: the mission list has the season's missions and none is claimable at first;
// the leader buys the next member cap with Gems (a real-money id, a wrong price and a non-GEM unit are refused); the
// shop then offers the step after it, and a member may not buy.
func TestCircleMissionsAndUpgradesOverTheWire(t *testing.T) {
	cirScene(t)
	cirAt(t, time.Date(2026, 11, 2, 12, 0, 0, 0, cirJST))
	rooms, chats := cirRegistry()
	leader := newCirClient(t, rooms, chats, 1101, 70)
	leader.login()
	leader.send(circleCsQuestList, nil)
	if n := len(cirList(leader.expectID(circleRcQuestList), 2)); n != 3 {
		t.Fatalf("missions = %d, want 3", n)
	}
	leader.send(circleCsQuestReward, nil)
	if got := cirVar(leader.expectID(circleRcQuestReward), 1); got != circleRefused {
		t.Fatalf("claim with no goal met = %d, want refused", got)
	}
	leader.send(circleCsUpgradeShop, nil)
	row := cirBytes(leader.expectID(circleRcUpgradeShop), 2)
	if string(cirBytes(row, 1)) != "cherry_circle_cap_40" || cirVar(row, 4) != 30 || cirVar(row, 5) != 40 ||
		cirVar(row, 12) != 1000 || cirVar(row, 14) != 1 {
		t.Fatalf("upgrade row = %x", row)
	}
	cirSetGems(t, "1101", 5000)
	for _, req := range [][]byte{
		cirUpgradeReq("cherry_circle_cap_40", "GEM", 1000, "cp-real"), // a real-money id
		cirUpgradeReq("cherry_circle_cap_40", "GEM", 999, ""),         // not the step's price
		cirUpgradeReq("cherry_circle_cap_40", "CASH", 1000, ""),       // not Gems
	} {
		leader.send(circleCsUpgradeBuy, req)
		if got := cirVar(leader.expectID(circleRcUpgradeBuy), 1); got != circleRefused {
			t.Fatalf("refused upgrade result = %d", got)
		}
	}
	if g := cirGems(t, "1101"); g != 5000 {
		t.Fatalf("refused upgrades spent Gems: %d", g)
	}
	leader.send(circleCsUpgradeBuy, cirUpgradeReq("cherry_circle_cap_40", "GEM", 1000, ""))
	buy := leader.expectID(circleRcUpgradeBuy)
	if cirVar(buy, 1) != 0 || cirVar(buy, 2) != 4000 || string(cirBytes(buy, 3)) != "GEM" {
		t.Fatalf("upgrade reply = %x, want result 0, balance 4000, GEM", buy)
	}
	if g := cirGems(t, "1101"); g != 4000 {
		t.Fatalf("leader gems after the upgrade = %d, want 4000", g)
	}
	leader.send(circleCsUpgradeShop, nil)
	if row := cirBytes(leader.expectID(circleRcUpgradeShop), 2); string(cirBytes(row, 1)) != "cherry_circle_cap_50" ||
		cirVar(row, 4) != 40 || cirVar(row, 12) != 2000 {
		t.Fatalf("next upgrade row = %x", row)
	}
	member := newCirClient(t, rooms, chats, 1103, 71)
	member.login()
	member.send(circleCsUpgradeBuy, cirUpgradeReq("cherry_circle_cap_50", "GEM", 2000, ""))
	if got := cirVar(member.expectID(circleRcUpgradeBuy), 1); got != circleRefused {
		t.Fatalf("a member bought an upgrade: result %d", got)
	}
}

// TestCircleHTTPChangesReachTheRoom: join, leave and close made over HTTP reach the circle room as the LPN requests do:
// the joiner is added to the members of the room (033) with the new count (035); a leaver is taken out of the room
// (034, deluser for the rest); a closed circle empties its room.
func TestCircleHTTPChangesReachTheRoom(t *testing.T) {
	cirScene(t)
	rooms, chats := cirRegistry()
	prev := economy.CircleRoomHook
	economy.CircleRoomHook = func(ev economy.CircleRoomEvent) { pushCircleEvent(rooms, log.New(io.Discard, "", 0), ev) }
	t.Cleanup(func() { economy.CircleRoomHook = prev })
	a := economy.CherryCircleFirst + 1
	leader := newCirClient(t, rooms, chats, 1101, 80)
	leader.joined()
	member := newCirClient(t, rooms, chats, 1103, 81)
	member.joined()
	leader.expectID(circleRcAddUser)

	if err := economy.JoinCircle("t-1106", a, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := economy.ApproveApply("t-1101", a, "1106", time.Now()); err != nil { // an OPEN circle's join needs approval
		t.Fatal(err)
	}
	for _, c := range []*cirClient{leader, member} {
		c.expectID(circleRcMemberAdd)
		c.expectID(circleRcMemberChange)
	}
	if err := economy.LeaveCircle("t-1103", a, time.Now()); err != nil {
		t.Fatal(err)
	}
	member.expectID(circleRcMemberDelete) // the leaver sees its own removal
	leader.expectID(circleRcMemberDelete)
	leader.expectID(circleRcDelUser)
	leader.expectID(circleRcMemberChange)
	member.none()

	if err := economy.LeaveCircle("t-1104", a, time.Now()); err != nil { // a member who is not in the room
		t.Fatal(err)
	}
	leader.expectID(circleRcMemberDelete)
	leader.expectID(circleRcMemberChange)

	if err := economy.CloseCircle("t-1101", a); err != nil {
		t.Fatal(err)
	}
	leader.expectID(circleRcMemberDelete)
	leader.none()
}

// TestCircleMemberInfoDate: the member row's date is f7 CreateDate, the join time in epoch ms as a decimal string, and
// the member-added push leaves it out (its parser divides f7 by 1000).
func TestCircleMemberInfoDate(t *testing.T) {
	m := store.CircleMember{Aid: "1101", Role: "member", Joined: 1793480000000}
	if got := string(cirBytes(circleMemberInfo(m, nil, true), 7)); got != "1793480000000" {
		t.Fatalf("list row f7 = %q, want the join time in ms", got)
	}
	if got := cirBytes(circleMemberInfo(m, nil, false), 7); got != nil {
		t.Fatalf("add push carries f7 %q", got)
	}
}
