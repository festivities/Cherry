package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestGatewayPingEcho(t *testing.T) {
	server, client := net.Pipe()
	go observeGatewayFrames(&lockedConn{Conn: server}, gatewayAddr, "pipe", log.New(io.Discard, "", 0))
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

func TestPlayDetailLPRmchatBody(t *testing.T) {
	var b struct {
		Result struct {
			GameInfo struct {
				GameID           string `json:"gameId"`
				Executable       bool   `json:"executable"`
				UnderMaintenance bool   `json:"underMaintenance"`
				MinVersion       string `json:"minLinePlayVersion"`
			} `json:"gameInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(playDetailLPRmchatBody), &b); err != nil {
		t.Fatal(err)
	}
	if g := b.Result.GameInfo; g.GameID != "lp_rmchat" || !g.Executable || g.UnderMaintenance || g.MinVersion != "" {
		t.Fatalf("gameInfo = %+v", g)
	}
}

func TestRoomRoutes(t *testing.T) {
	accountsMu.Lock()
	accounts["room-test"] = &account{aid: "9002", name: "Rin"}
	accountsMu.Unlock()
	defer func() { accountsMu.Lock(); delete(accounts, "room-test"); accountsMu.Unlock() }()
	for path, name := range map[string]string{
		"/v4/room/enter/9002/LEVEL_2":   "Rin",
		"/v4/room/enter/100000/LEVEL_1": friendName,
		"/v4/room/myroom/LEVEL_3":       "",
	} {
		rec := serve(t, http.MethodGet, path)
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
		if r.RoomInfo.GroundLevel != path[len(path)-7:] || r.RoomInfo.TileSize != 12 || r.RoomInfo.Floor == nil || r.RoomInfo.Items == nil || r.Max != "LEVEL_1" || (name != "" && r.Name != name) {
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
		if rec := serve(t, c.method, c.path); rec.Code != http.StatusNotFound {
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
	installSocialTestAccounts(t, map[string]*account{
		"a": {aid: "11", name: "Ann", gender: "MALE", itemCodes: []string{"CUON004TV"}},
		"b": {aid: "12", name: "Bo"},
		"c": {aid: "13", name: "Cy"},
	})
	socialMu.Lock()
	old := friendships
	friendships = []friendship{{A: "11", B: "12", State: "accepted"}, {A: "11", B: "13", State: "pending"}}
	socialMu.Unlock()
	t.Cleanup(func() { socialMu.Lock(); friendships = old; socialMu.Unlock() })
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
	a.expect(5, []byte{0x08, 0x00, 0x10, 11, 0x28, 1})
	b.enter(11) // accepted friend
	b.expect(5, []byte{0x08, 0x00, 0x10, 11, 0x28, 1})
	c.enter(11) // pending only -> not a friend
	c.expect(5, []byte{0x08, 1, 0x10, 11})
	a.send(2, nil) // resume = re-enter current room
	a.expect(5, []byte{0x08, 0x00, 0x10, 11, 0x28, 1})
	c.send(2, nil) // resume with no room
	c.expect(5, []byte{0x08, 1, 0x10, 13})
}

func TestPartyRosterRelaysExit(t *testing.T) {
	h := partySetup(t)
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	a.enter(11)
	a.read()
	b.enter(11)
	b.read()
	ma := pbVar(nil, 1, 5)
	mb := pbVar(nil, 1, 9)
	res := a.start(ma)
	if !bytes.Equal(res, append([]byte{0x08, 0}, pbLen(nil, 2, partyPlayerInfo(11, ma))...)) {
		t.Fatalf("host roster = %x", res)
	}
	a.none()
	res = b.start(mb)
	want := append([]byte{0x08, 0}, pbLen(nil, 2, partyPlayerInfo(11, ma))...)
	want = append(want, pbLen(nil, 2, partyPlayerInfo(12, mb))...)
	if !bytes.Equal(res, want) {
		t.Fatalf("guest roster = %x want %x", res, want)
	}
	b.expect(1, pbLen(nil, 1, partyPlayerInfo(11, ma))) // room adduser (chat info) of others to joiner
	// avatars come from the floor messages: roster of the others (not self) to the joiner
	b.expect(29, append([]byte{0x08, 0}, pbLen(nil, 2, partyPlayerInfo(11, ma))...))
	a.expect(1, pbLen(nil, 1, partyPlayerInfo(12, mb))) // joiner to others
	a.expect(30, pbLen(nil, 1, partyPlayerInfo(12, mb)))
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
	a.enter(11)
	a.read()
	b.enter(11)
	b.read()
	b.start(pbVar(nil, 1, 9))
	a.expect(10, []byte{0x08, 12}) // host not in room yet: friend-entered push
	a.start(pbVar(nil, 1, 5))
	b.read()                                                           // room adduser of the host
	b.expect(30, pbLen(nil, 1, partyPlayerInfo(11, pbVar(nil, 1, 5)))) // guest sees the host avatar
	a.expect(1, pbLen(nil, 1, partyPlayerInfo(12, pbVar(nil, 1, 9))))
	a.expect(29, append([]byte{0x08, 0}, pbLen(nil, 2, partyPlayerInfo(12, pbVar(nil, 1, 9)))...)) // host sees the guest avatar
	// guest reconnects: new session takes over silently
	b.s.close() // old connection EOF would deluser; emulate takeover instead
	a.expect(2, []byte{0x08, 12, 0x10, 0})
	a.expect(31, []byte{0x08, 12})
	host := append([]byte{0x08, 0}, pbLen(nil, 2, partyPlayerInfo(11, pbVar(nil, 1, 5)))...)
	b2 := partyLogin(t, h, 12, 4)
	b2.enter(11)
	b2.read()
	b2.start(pbVar(nil, 1, 9))
	b2.read()
	b2.expect(29, host)
	a.expect(1, pbLen(nil, 1, partyPlayerInfo(12, pbVar(nil, 1, 9))))
	a.expect(30, pbLen(nil, 1, partyPlayerInfo(12, pbVar(nil, 1, 9))))
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
	a.enter(11)
	a.read()
	// packed encoding: 12 (friend), 13 (pending), 99 (unknown)
	a.send(7, pbLen(nil, 1, []byte{12, 13, 99}))
	a.expect(7, []byte{0x08, 12, 0x10, 13, 0x10, 99})
	b.expect(9, []byte{0x08, 11, 0x10, 0xd8, 0x04}) // lefttime 600
	c.none()
	// unpacked encoding works too
	a.send(7, append(pbVar(nil, 1, 12), pbVar(nil, 2, 1)...))
	a.expect(7, []byte{0x08, 12})
	b.read()
	// invite list: waiting 12, inroom empty
	a.send(6, nil)
	a.expect(8, []byte{0x08, 12})
	// invited guest may enter (also with friendship removed)
	socialMu.Lock()
	friendships = nil
	socialMu.Unlock()
	b.enter(11)
	b.expect(5, []byte{0x08, 0, 0x10, 11, 0x28, 1})
	b.start(nil)
	a.expect(10, []byte{0x08, 12}) // friend-entered push to absent host
	a.send(6, nil)
	a.expect(8, []byte{0x10, 12})
	// invite creates the inviter's room when none exists
	c.send(7, pbVar(nil, 1, 11))
	c.expect(7, []byte{0x10, 11})
	if h.rooms[13] == nil {
		t.Fatal("inviter room missing")
	}
}
