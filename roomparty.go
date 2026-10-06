package main

import (
	"encoding/binary"
	"encoding/json"
	"log"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Room Party (LPNP, gateway agent 4). Handshake per static research: client
// open -> type-1 control (never a type-2 ack: at status 6 an ack re-runs
// OpenTunnel and restarts the retry loop) -> cr_login_req idx0 -> rc_login_res
// idx0 result 0. Rooms and relays are not implemented yet.
const roomPartyAgent = 4

const (
	roomFloorCode = "RUTI000EW"
	roomWallCode  = "RUWA000HR" // 121400639: has wall_l_1.png and wall_r_1.png
)

// partyMember is one logged-in agent-4 connection. b1/sid come from the login
// frame (constant per tunnel) so other connections can push to it.
type partyMember struct {
	aid   uint64
	conn  *lockedConn
	b1    byte
	sid   [8]byte
	move  []byte // last _player_move, guarded by partyHub.mu
	nmov  int    // cr_player_move count, guarded by partyHub.mu
	floor int    // current floor 1..roomPartyFloors, guarded by partyHub.mu
}

// roomPartyFloors is sent as rc_room_enter_res ExtendFloors (floor count; the
// client hides the floor selector when it is 1).
// ponytail: every room has 2 floors until a remodeling shop / ownership exists.
const roomPartyFloors = 2

const partyStatusTimeout = 600 // rc_room_status_push Timeout; client keeps MyRoomAlive 2*Timeout s

// partyStatusBody is rc_room_status_push (idx23) {Exist#1, Timeout#2}. Exist=0 is never
// sent: it would mark the room exploded on the client.
func partyStatusBody() []byte {
	return pbVar(pbVar(nil, 1, 1), 2, partyStatusTimeout)
}

// Room grid: tileSize 12 -> one 12x12 sector of 80x40 px tiles (static research,
// MpCoord::ConvertWorldTileCCToGame). DefaultHeroPosition picks tile (8,4).
// ponytail: Y offset uses 20*(23-tx-ty) (tile centre, ~220 for (8,4)); Square's formula
// would give 20*(24-tx-ty) -> 240. Switch the constant below if runtime shows the avatar offset.
const partyYBase = 23

func partyTilePx(tx, ty int) (x, y uint64) {
	return uint64(40 * (tx - ty + 12)), uint64(20 * (partyYBase - tx - ty))
}

// partyPxTile is the inverse (truncating for off-centre positions).
func partyPxTile(x, y uint64) (tx, ty int) {
	a := int(x/40) - 12
	b := partyYBase - int(y/20)
	return (a + b) / 2, (b - a) / 2
}

// partySpawnTiles is the preference order (first free tile wins).
var partySpawnTiles = [][2]int{{8, 4}, {8, 5}, {7, 4}, {9, 4}, {8, 3}, {7, 5}, {9, 3}, {7, 3}, {9, 5}}

// partyMoveFields returns cur x,y plus f5/f6 of a _player_move.
func partyMoveFields(move []byte) (x, y, f5, f6 uint64, ok bool) {
	var got uint8
	pbScan(move, func(f, v uint64, d []byte) {
		if d != nil {
			return
		}
		switch f {
		case 1:
			x, got = v, got|1
		case 2:
			y, got = v, got|2
		case 5:
			f5 = v
		case 6:
			f6 = v
		}
	})
	return x, y, f5, f6, got == 3
}

// spawnLocked assigns a spawn _player_move for m on floor: f1..f4 = tile px, f5/f6
// copied from the client's own move (f6 default 1). Caller holds hub.mu.
func (r *partyRoom) spawnLocked(m *partyMember, floor int, client []byte) (move []byte, tx, ty int) {
	taken := map[[2]int]bool{}
	for _, o := range r.members {
		if o.aid == m.aid || o.floor != floor || len(o.move) == 0 {
			continue
		}
		if x, y, _, _, ok := partyMoveFields(o.move); ok {
			a, b := partyPxTile(x, y)
			taken[[2]int{a, b}] = true
		}
	}
	tiles := partySpawnTiles
	for a := 0; a < 12; a++ { // fallback: any tile of the grid
		for b := 0; b < 12; b++ {
			tiles = append(tiles[:len(tiles):len(tiles)], [2]int{a, b})
		}
	}
	tx, ty = tiles[0][0], tiles[0][1]
	for _, t := range tiles { // first free tile; the host (normally first) therefore keeps (8,4)
		if !taken[t] {
			tx, ty = t[0], t[1]
			break
		}
	}
	_, _, f5, f6, _ := partyMoveFields(client)
	if f6 == 0 {
		f6 = 1
	}
	x, y := partyTilePx(tx, ty)
	b := pbVar(pbVar(pbVar(pbVar(nil, 1, x), 2, y), 3, x), 4, y)
	return pbVar(pbVar(b, 5, f5), 6, f6), tx, ty
}

// onFloor lists members on floor, excluding skip (nil = everyone).
func (r *partyRoom) onFloor(skip *partyMember, floor int) []*partyMember {
	var out []*partyMember
	for _, o := range r.members {
		if o != skip && o.floor == floor {
			out = append(out, o)
		}
	}
	return out
}

func (m *partyMember) send(logger *log.Logger, msgid uint16, body []byte) {
	if _, err := m.conn.Write(squareFrame(m.b1, m.sid[:], msgid, body)); err != nil {
		logger.Printf("ROOMPARTY send aid=%d msgid=%d err=%v", m.aid, msgid, err)
	}
}

// partyRoom is keyed by host aid. Host-leave policy: the room stays alive
// while any member remains (the host just gets a deluser like anyone else);
// it is deleted when its last member leaves and the host has no session.
// Invites live as long as the room (no 600 s expiry).
type partyRoom struct {
	host       uint64
	members    []*partyMember // join order; roster puts the host first
	invited    map[uint64]bool
	chatSerial uint64
}

type partyHub struct {
	mu       sync.Mutex
	sessions map[uint64]*partyMember
	rooms    map[uint64]*partyRoom
	ticking  bool          // status loop running, guarded by mu
	every    time.Duration // status refresh period; 0 = partyStatusEvery
}

// partyStatusEvery is how often an open room's host gets rc_room_status_push again
// (the client keeps MyRoomAlive for 2*Timeout s, so this must stay below 1200 s).
const partyStatusEvery = 5 * time.Minute

// startStatusLoop refreshes idx23 for every open room (members present or invites
// outstanding) whose host is connected. It exits once no room is left and is restarted
// by the next host enter.
func (h *partyHub) startStatusLoop(logger *log.Logger) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ticking {
		return
	}
	h.ticking = true
	d := h.every
	if d <= 0 {
		d = partyStatusEvery
	}
	go func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for range t.C {
			h.mu.Lock()
			if len(h.rooms) == 0 {
				h.ticking = false
				h.mu.Unlock()
				return
			}
			var hosts []*partyMember
			for aid, r := range h.rooms {
				if m := h.sessions[aid]; m != nil && (len(r.members) > 0 || len(r.invited) > 0) {
					hosts = append(hosts, m)
				}
			}
			h.mu.Unlock()
			for _, m := range hosts {
				m.send(logger, 23, partyStatusBody())
			}
		}
	}()
}

var defaultParty = &partyHub{}

const partyInviteLefttime = 600

func (h *partyHub) room(host uint64) *partyRoom { // caller holds mu
	if h.rooms == nil {
		h.rooms = map[uint64]*partyRoom{}
	}
	r := h.rooms[host]
	if r == nil {
		r = &partyRoom{host: host, invited: map[uint64]bool{}}
		h.rooms[host] = r
	}
	return r
}

func (r *partyRoom) index(m *partyMember) int {
	for i, o := range r.members {
		if o == m {
			return i
		}
	}
	return -1
}

func (r *partyRoom) has(aid uint64) bool {
	for _, o := range r.members {
		if o.aid == aid {
			return true
		}
	}
	return false
}

// others returns members excluding skip (nil = everyone).
func (r *partyRoom) others(skip *partyMember) []*partyMember {
	out := make([]*partyMember, 0, len(r.members))
	for _, o := range r.members {
		if o != skip {
			out = append(out, o)
		}
	}
	return out
}

func partyAvatarInfo(aid uint64) []byte {
	id := strconv.FormatUint(aid, 10)
	acc, ok := accountByAvatarID(id)
	if !ok {
		acc = accountSnapshot{aid: id, name: "cherry", gender: "FEMALE"}
	}
	if acc.skin == "" {
		acc.skin = "1"
	}
	if acc.country == "" {
		acc.country = squareDefaultCountry
	}
	gender := "FEMALE"
	if acc.gender == "MALE" || acc.gender == "ANIMAL" {
		gender = acc.gender
	}
	b := pbLen(nil, 1, []byte(id))
	b = pbLen(b, 3, []byte(acc.name))
	b = pbLen(b, 4, []byte(gender))
	for _, code := range acc.itemCodes {
		b = pbLen(b, 5, pbLen(nil, 1, []byte(code)))
	}
	b = pbLen(b, 6, []byte(acc.country))
	return pbLen(b, 10, []byte(acc.skin))
}

// partyPlayerInfo encodes PlayerInfo; move is the member's last _player_move.
func partyPlayerInfo(aid uint64, move []byte) []byte {
	b := pbLen(pbVar(nil, 1, aid), 2, partyAvatarInfo(aid))
	if len(move) > 0 {
		b = pbLen(b, 3, move)
	}
	return b
}

type partySnap struct {
	aid  uint64
	move []byte
}

// rosterLocked lists members on floor, host first; caller holds h.mu.
func (r *partyRoom) rosterLocked(floor int) []partySnap {
	var out []partySnap
	for _, o := range r.members {
		if o.aid == r.host && o.floor == floor {
			out = append(out, partySnap{o.aid, o.move})
		}
	}
	for _, o := range r.members {
		if o.aid != r.host && o.floor == floor {
			out = append(out, partySnap{o.aid, o.move})
		}
	}
	return out
}

// leave removes m (if still registered) and sends deluser to the others.
func (h *partyHub) leave(logger *log.Logger, r *partyRoom, m *partyMember) {
	h.mu.Lock()
	i := r.index(m)
	floor := m.floor
	if i >= 0 {
		r.members = append(r.members[:i], r.members[i+1:]...)
	}
	rest := r.others(nil)
	sameFloor := map[*partyMember]bool{}
	for _, o := range r.onFloor(nil, floor) {
		sameFloor[o] = true
	}
	if len(rest) == 0 && h.sessions[r.host] == nil && h.rooms[r.host] == r {
		delete(h.rooms, r.host)
	}
	h.mu.Unlock()
	if i >= 0 {
		logger.Printf("ROOMPARTY deluser aid=%d host=%d to=%d", m.aid, r.host, len(rest))
		body := pbVar(pbVar(nil, 1, m.aid), 2, 0)
		// idx2 only drops chat info; avatars are removed by idx31 rc_floor_deluser_all.
		floorDel := pbVar(nil, 1, m.aid)
		for _, o := range rest {
			o.send(logger, 2, body)
			if sameFloor[o] {
				o.send(logger, 31, floorDel)
			}
		}
	}
}

// roomPartySession is the per-connection agent-4 state, used from the single
// handler goroutine of that connection.
type roomPartySession struct {
	conn     *lockedConn
	logger   *log.Logger
	hub      *partyHub // nil = defaultParty
	aid      uint64
	loggedIn bool
	pm       *partyMember
	host     uint64     // room last entered (0 = none)
	joined   *partyRoom // room this session is a member of
}

func (s *roomPartySession) h() *partyHub {
	if s.hub == nil {
		return defaultParty
	}
	return s.hub
}

// close unregisters the session and leaves its room (exit/EOF/error).
func (s *roomPartySession) close() {
	if s.pm == nil {
		return
	}
	h := s.h()
	s.leaveRoom()
	h.mu.Lock()
	if h.sessions[s.aid] == s.pm {
		delete(h.sessions, s.aid)
		if r := h.rooms[s.aid]; r != nil && len(r.members) == 0 {
			delete(h.rooms, s.aid)
		}
	}
	h.mu.Unlock()
	s.logger.Printf("ROOMPARTY close aid=%d", s.aid)
	s.pm, s.host = nil, 0
}

func (s *roomPartySession) leaveRoom() {
	if s.joined != nil {
		s.h().leave(s.logger, s.joined, s.pm)
		s.joined = nil
	}
}

// handleOpen answers a tunnel open with the Square/Garden-shaped control.
func (s *roomPartySession) handleOpen(frame []byte) error {
	control := make([]byte, 22)
	binary.BigEndian.PutUint32(control, 18)
	control[4], control[5] = 'G', frame[1]
	copy(control[6:14], frame[2:10])
	_, err := s.conn.Write(control)
	s.logger.Printf("ROOMPARTY open-control err=%v", err)
	return err
}

func (s *roomPartySession) reply(frame []byte, msgid uint16, body []byte) error {
	_, err := s.conn.Write(squareFrame(frame[1], frame[2:10], msgid, body))
	return err
}

func partyAccepted(a, b uint64) bool {
	socialMu.Lock()
	defer socialMu.Unlock()
	return friendshipIndex(strconv.FormatUint(a, 10), strconv.FormatUint(b, 10), "accepted") >= 0
}

// partyAidList reads repeated int64 field 1, packed or not.
func partyAidList(data []byte) ([]uint64, bool) {
	var out []uint64
	ok := pbScan(data, func(f, v uint64, d []byte) {
		if f != 1 || len(out) >= 100 {
			return
		}
		if d == nil {
			out = append(out, v)
			return
		}
		for len(d) > 0 {
			x, n := binary.Uvarint(d)
			if n <= 0 {
				return
			}
			out = append(out, x)
			d = d[n:]
		}
	})
	return out, ok
}

func partyEnterBody(result, host uint64) []byte {
	b := pbVar(pbVar(nil, 1, result), 2, host)
	if result == 0 {
		b = pbVar(b, 5, roomPartyFloors) // ExtendFloors
	}
	return b
}

// enter answers cr_room_enter_req for host. Success when the requester is the
// host (room created/kept) or the room exists and the requester is the host's
// accepted friend or was invited.
func (s *roomPartySession) enter(frame []byte, host uint64) error {
	h := s.h()
	ok := host == s.aid
	if !ok {
		friend := partyAccepted(s.aid, host) // socialMu never nests inside hub.mu
		h.mu.Lock()
		r := h.rooms[host]
		ok = r != nil && (friend || r.invited[s.aid])
		h.mu.Unlock()
	}
	result := uint64(1)
	if ok {
		result = 0
		if s.joined != nil && s.joined.host != host {
			s.leaveRoom()
		}
		h.mu.Lock()
		h.room(host)
		h.mu.Unlock()
		s.host = host
	}
	s.logger.Printf("ROOMPARTY enter aid=%d host=%d result=%d", s.aid, host, result)
	if err := s.reply(frame, 5, partyEnterBody(result, host)); err != nil {
		return err
	}
	if result == 0 && host == s.aid { // lets the host's My Room menu rejoin this party
		s.pm.send(s.logger, 23, partyStatusBody())
		h.startStatusLoop(s.logger)
	}
	return nil
}

// handleData processes one agent-4 data frame (frame[10:12] msgid, frame[12:]
// protobuf). A non-nil error means a reply write failed.
func (s *roomPartySession) handleData(frame []byte) error {
	msgid := binary.BigEndian.Uint16(frame[10:12])
	data := frame[12:]
	if msgid == 0 {
		aid, _ := protobufUintField(data, 2)
		if !gardenLoginValid(data) || aid == 0 || aid > (1<<63)-1 {
			s.logger.Printf("ROOMPARTY login invalid request")
			return nil
		}
		s.close()
		s.aid, s.loggedIn = aid, true
		s.pm = &partyMember{aid: aid, conn: s.conn, b1: frame[1]}
		copy(s.pm.sid[:], frame[2:10])
		h := s.h()
		h.mu.Lock()
		if h.sessions == nil {
			h.sessions = map[uint64]*partyMember{}
		}
		h.sessions[aid] = s.pm
		h.mu.Unlock()
		body := []byte{0x08, 0x00}
		h.mu.Lock()
		if r := h.rooms[aid]; r != nil && (len(r.members) > 0 || len(r.invited) > 0) {
			body = pbVar(pbVar(body, 2, 1), 3, partyStatusTimeout) // MyroomExist, MyroomTimeout
		}
		h.mu.Unlock()
		s.logger.Printf("ROOMPARTY login aid=%d result=0 myroom=%v", aid, len(body) > 2)
		return s.reply(frame, 0, body)
	}
	if !s.loggedIn {
		s.logger.Printf("ROOMPARTY msgid=%d before login ignored", msgid)
		return nil
	}
	switch msgid {
	case 1: // cr_room_enter_req -> rc_room_enter_res idx5
		host, _ := protobufUintField(data, 1)
		return s.enter(frame, host)
	case 2: // cr_room_enter_resume_req: same as re-entering the current room
		if s.host == 0 {
			s.logger.Printf("ROOMPARTY resume aid=%d no room", s.aid)
			return s.reply(frame, 5, partyEnterBody(1, s.aid))
		}
		return s.enter(frame, s.host)
	case 3: // cr_room_exit_req -> rc_room_exit_res + deluser to others
		err := s.reply(frame, 3, nil)
		s.logger.Printf("ROOMPARTY exit aid=%d", s.aid)
		s.leaveRoom()
		s.host = 0
		return err
	case 5: // cr_room_relaystart_req
		return s.relayStart(frame, data)
	case 6: // cr_friend_invite_list_req -> idx8
		return s.inviteList(frame)
	case 7:
		return s.invite(frame, data)
	case 24: // cr_floor_enter_req -> rc_floor_enter_res idx28
		return s.floorEnter(frame, data)
	case 25: // cr_floor_relaystart_req (Floor#1, MoveInfo#2) -> idx29 + floor messages
		return s.floorRelayStart(data)
	case 18: // cr_friend_invite_cancel_req -> idx22
		return s.inviteCancel(frame, data)
	}
	return s.relay(msgid, data)
}

// inviteListBody encodes Waiting#1 (invited, not in room) and Inroom#2 (others in
// the room). rc_friend_invite_list_res (idx8) and rc_friend_invite_cancel_res
// (idx22) share this shape: the client clears its invitee set and rebuilds it.
func (s *roomPartySession) inviteListBody() (body []byte, w, in int) {
	h := s.h()
	host := s.host
	if host == 0 {
		host = s.aid
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[host]; r != nil {
		for a := range r.invited {
			if !r.has(a) {
				body = pbVar(body, 1, a)
				w++
			}
		}
		for _, o := range r.members {
			if o != s.pm {
				body = pbVar(body, 2, o.aid)
				in++
			}
		}
	}
	return
}

func (s *roomPartySession) inviteList(frame []byte) error {
	body, w, in := s.inviteListBody()
	s.logger.Printf("ROOMPARTY invite-list aid=%d waiting=%d inroom=%d", s.aid, w, in)
	return s.reply(frame, 8, body)
}

// inviteCancel handles cr_friend_invite_cancel_req idx18 (AidList#1): drop the
// aids from the room's invited set, reply idx22 with the refreshed lists.
func (s *roomPartySession) inviteCancel(frame, data []byte) error {
	aids, ok := partyAidList(data)
	if !ok {
		s.logger.Printf("ROOMPARTY invite-cancel invalid aid=%d", s.aid)
		return nil
	}
	host := s.host
	if host == 0 {
		host = s.aid
	}
	h := s.h()
	h.mu.Lock()
	if r := h.rooms[host]; r != nil {
		for _, a := range aids {
			delete(r.invited, a)
		}
	}
	h.mu.Unlock()
	body, w, in := s.inviteListBody()
	s.logger.Printf("ROOMPARTY invite-cancel aid=%d host=%d cancelled=%d waiting=%d inroom=%d", s.aid, host, len(aids), w, in)
	return s.reply(frame, 22, body)
}

// partyFloorOK reports whether f is a servable floor.
func partyFloorOK(f uint64) bool { return f >= 1 && f <= roomPartyFloors }

// partyName is the display name for floor notifications.
func partyName(aid uint64) []byte {
	if acc, ok := accountByAvatarID(strconv.FormatUint(aid, 10)); ok && acc.name != "" {
		return []byte(acc.name)
	}
	return []byte("cherry")
}

// allRosterLocked lists every member (all floors), floor by floor; caller holds h.mu.
func (r *partyRoom) allRosterLocked() []partySnap {
	var out []partySnap
	for f := 1; f <= roomPartyFloors; f++ {
		out = append(out, r.rosterLocked(f)...)
	}
	return out
}

func (s *roomPartySession) relayStart(frame, data []byte) error {
	h := s.h()
	if s.host == 0 {
		s.logger.Printf("ROOMPARTY relaystart before enter ignored aid=%d", s.aid)
		return nil
	}
	move, _, ok := pbBytesField(data, 1)
	if !ok {
		s.logger.Printf("ROOMPARTY relaystart invalid aid=%d", s.aid)
		return nil
	}
	floor := 1
	if f, found := protobufUintField(data, 2); found && partyFloorOK(f) {
		floor = int(f)
	}
	cx, cy, _, _, _ := partyMoveFields(move)
	h.mu.Lock()
	r := h.room(s.host)
	delete(r.invited, s.aid) // invite consumed; a later leave must not show "Waiting"
	m := s.pm
	idx := -1
	for i, o := range r.members {
		if o.aid == m.aid {
			idx = i
		}
	}
	oldFloor := 0
	var assigned []byte
	if idx >= 0 { // silent rejoin keeps the stored position when the floor is unchanged
		oldFloor = r.members[idx].floor
		if oldFloor == floor && len(r.members[idx].move) > 0 {
			assigned = r.members[idx].move
		}
	}
	tx, ty := -1, -1
	if assigned == nil {
		assigned, tx, ty = r.spawnLocked(m, floor, move)
	}
	m.move, m.floor = assigned, floor
	if idx >= 0 {
		r.members[idx] = m
	} else {
		r.members = append(r.members, m)
	}
	roster := r.rosterLocked(floor)
	all := r.allRosterLocked()
	hostSession := h.sessions[r.host]
	hostIn := r.has(r.host) && r.host != m.aid
	others := r.others(m)
	sameFloor := r.onFloor(m, floor)
	var oldMates []*partyMember
	if idx >= 0 && oldFloor != floor {
		oldMates = r.onFloor(m, oldFloor)
	}
	h.mu.Unlock()
	s.joined = r
	s.logger.Printf("ROOMPARTY relaystart aid=%d host=%d floor=%d roster=%d rejoined=%v clientCur=(%d,%d) assignedTile=(%d,%d)", s.aid, r.host, floor, len(roster), idx >= 0, cx, cy, tx, ty)
	// idx6/idx1 are room-wide (chat info only); avatars come from the per-floor messages.
	res := pbVar(nil, 1, 0)
	for _, p := range all {
		res = pbLen(res, 2, partyPlayerInfo(p.aid, p.move))
	}
	if err := s.reply(frame, 6, res); err != nil {
		return err
	}
	// Avatars are spawned only by the floor messages (idx29 roster, idx30 adduser ->
	// msg 12630/12633 -> AddInviteUser); room idx6/idx1 (12629/12631) only feed chat
	// info and the joined toast. The floor roster goes to the joiner even on a silent
	// rejoin (a fresh scene has no avatars); it MUST include the joiner itself: AddInviteUser's self branch (aid == hero) is the only
	// place that calls SetJoinRoomParty(true) on the hero, and AvActor::MoveNextActor posts the
	// move message 12638 -> RoomPartyService::SendMove only for such a hero (else walking is local).
	// The self row also repositions the hero (setPosition cur f1,f2): that is the spawn separation.
	for _, p := range all {
		if p.aid != m.aid {
			m.send(s.logger, 1, pbLen(nil, 1, partyPlayerInfo(p.aid, p.move)))
		}
	}
	m.send(s.logger, 29, partyFloorRes(roster, s.host, floor))
	if idx >= 0 && oldFloor == floor {
		return nil
	}
	add := pbLen(nil, 1, partyPlayerInfo(m.aid, m.move))
	if idx < 0 {
		for _, o := range others {
			o.send(s.logger, 1, add)
		}
	}
	floorAdd := partyFloorAdd(partySnap{m.aid, m.move}, s.host, floor)
	for _, o := range sameFloor {
		o.send(s.logger, 30, floorAdd)
	}
	for _, o := range oldMates {
		o.send(s.logger, 31, pbVar(nil, 1, m.aid))
	}
	if idx < 0 && r.host != m.aid && !hostIn && hostSession != nil {
		s.logger.Printf("ROOMPARTY friend-entered aid=%d host=%d", s.aid, r.host)
		hostSession.send(s.logger, 10, pbVar(nil, 1, m.aid))
	}
	return nil
}

// member returns the session's room while it is still a member (not replaced).
func (s *roomPartySession) member() *partyRoom {
	r := s.joined
	if r == nil {
		return nil
	}
	h := s.h()
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.index(s.pm) < 0 {
		return nil
	}
	return r
}

// floorEnter answers cr_floor_enter_req (Floor#1) with rc_floor_enter_res idx28
// {Result#1, Floor#2}. Result 0 makes the client reload that floor over HTTP and send
// cr_floor_relaystart_req; the move itself is applied there. Nonzero: client does nothing.
func (s *roomPartySession) floorEnter(frame, data []byte) error {
	f, _ := protobufUintField(data, 1)
	result := uint64(0)
	if s.member() == nil || !partyFloorOK(f) {
		result = 1
	}
	s.logger.Printf("ROOMPARTY floor-enter aid=%d floor=%d result=%d", s.aid, f, result)
	return s.reply(frame, 28, pbVar(pbVar(nil, 1, result), 2, f))
}

// floorRelayStart handles cr_floor_relaystart_req {Floor#1, MoveInfo#2}: set floor and
// spawn, idx29 roster (incl. self, Floor#4) to the mover, idx30 to the new floor,
// idx31 to the old floor, idx32/idx33 notifications to every other member.
func (s *roomPartySession) floorRelayStart(data []byte) error {
	r := s.member()
	f, _ := protobufUintField(data, 1)
	move, _, ok := pbBytesField(data, 2)
	if r == nil || !partyFloorOK(f) || !ok {
		s.logger.Printf("ROOMPARTY floor-relaystart ignored aid=%d floor=%d", s.aid, f)
		return nil
	}
	floor := int(f)
	h := s.h()
	m := s.pm
	cx, cy, _, _, _ := partyMoveFields(move)
	h.mu.Lock()
	oldFloor := m.floor
	var assigned []byte
	tx, ty := -1, -1
	if oldFloor == floor && len(m.move) > 0 {
		assigned = m.move
	} else {
		assigned, tx, ty = r.spawnLocked(m, floor, move)
	}
	m.move, m.floor = assigned, floor
	roster := r.rosterLocked(floor)
	newMates := r.onFloor(m, floor)
	var oldMates []*partyMember
	if oldFloor != floor {
		oldMates = r.onFloor(m, oldFloor)
	}
	others := r.others(m)
	h.mu.Unlock()
	s.logger.Printf("ROOMPARTY floor-relaystart aid=%d floor=%d->%d clientCur=(%d,%d) assignedTile=(%d,%d) roster=%d", s.aid, oldFloor, floor, cx, cy, tx, ty, len(roster))
	m.send(s.logger, 29, pbVar(partyFloorRes(roster, s.host, floor), 4, f))
	if oldFloor == floor {
		return nil
	}
	add := partyFloorAdd(partySnap{m.aid, m.move}, s.host, floor)
	for _, o := range newMates {
		o.send(s.logger, 30, add)
	}
	del := pbVar(nil, 1, m.aid)
	for _, o := range oldMates {
		o.send(s.logger, 31, del)
	}
	name := partyName(m.aid)
	leave := pbLen(pbVar(pbVar(nil, 1, m.aid), 2, uint64(oldFloor)), 3, name)
	enter := pbLen(pbVar(pbVar(nil, 1, m.aid), 2, f), 3, name)
	for _, o := range others {
		o.send(s.logger, 33, leave)
		o.send(s.logger, 32, enter)
	}
	return nil
}

func (s *roomPartySession) invite(frame, data []byte) error {
	h := s.h()
	aids, ok := partyAidList(data)
	if !ok {
		s.logger.Printf("ROOMPARTY invite invalid aid=%d", s.aid)
		return nil
	}
	host := s.host
	if host == 0 {
		host = s.aid
	}
	var good []uint64
	var body, failBody []byte
	for _, a := range aids {
		_, real := accountByAvatarID(strconv.FormatUint(a, 10))
		if real && a != s.aid && partyAccepted(s.aid, a) {
			good = append(good, a)
			body = pbVar(body, 1, a)
		} else {
			failBody = pbVar(failBody, 2, a)
		}
	}
	h.mu.Lock()
	r := h.room(host)
	var pushes []*partyMember
	for _, a := range good {
		r.invited[a] = true
		if t := h.sessions[a]; t != nil {
			pushes = append(pushes, t)
		}
	}
	h.mu.Unlock()
	s.logger.Printf("ROOMPARTY invite aid=%d host=%d invited=%d failed=%d pushed=%d", s.aid, host, len(good), len(aids)-len(good), len(pushes))
	if err := s.reply(frame, 7, append(body, failBody...)); err != nil {
		return err
	}
	push := pbVar(pbVar(nil, 1, s.aid), 2, partyInviteLefttime)
	for _, t := range pushes {
		t.send(s.logger, 9, push)
	}
	if len(good) > 0 {
		h.mu.Lock()
		hs := h.sessions[host]
		h.mu.Unlock()
		if hs != nil {
			hs.send(s.logger, 23, partyStatusBody())
		}
	}
	return nil
}

// relay handles move/chat/action from a room member.
func (s *roomPartySession) relay(msgid uint16, data []byte) error {
	h := s.h()
	r := s.joined
	if r == nil {
		s.logger.Printf("ROOMPARTY msgid=%d outside room ignored", msgid)
		return nil
	}
	h.mu.Lock()
	member := r.index(s.pm) >= 0
	h.mu.Unlock()
	if !member { // replaced by a newer connection of the same aid
		return nil
	}
	switch msgid {
	case 8: // cr_player_move -> idx11 to others
		move, found, ok := pbBytesField(data, 1)
		if !ok || !found {
			s.logger.Printf("ROOMPARTY move invalid")
			return nil
		}
		h.mu.Lock()
		s.pm.move = append([]byte(nil), move...)
		s.pm.nmov++
		n := s.pm.nmov
		others := r.onFloor(s.pm, s.pm.floor)
		h.mu.Unlock()
		if n <= 20 || n%50 == 0 {
			s.logger.Printf("ROOMPARTY move aid=%d count=%d to=%d", s.aid, n, len(others))
		}
		if n <= 3 {
			x, y, _, _, _ := partyMoveFields(move)
			tx, ty := partyPxTile(x, y)
			s.logger.Printf("ROOMPARTY move-cur aid=%d n=%d cur=(%d,%d) tile=(%d,%d)", s.aid, n, x, y, tx, ty)
		}
		body := pbLen(pbVar(nil, 1, s.aid), 2, move)
		for _, o := range others {
			o.send(s.logger, 11, body)
		}
	case 9: // cr_common_chat -> idx12 to all
		msg, found, ok := pbBytesField(data, 1)
		if !ok || !found || len(msg) == 0 || len(msg) > squareMaxChatLen || !utf8.Valid(msg) {
			s.logger.Printf("ROOMPARTY chat rejected")
			return nil
		}
		h.mu.Lock()
		r.chatSerial++
		serial := r.chatSerial
		all := r.others(nil)
		h.mu.Unlock()
		body := pbVar(pbVar(nil, 1, s.aid), 2, serial)
		body = pbVar(pbLen(body, 3, msg), 4, 0)
		s.logger.Printf("ROOMPARTY chat aid=%d serial=%d bytes=%d to=%d", s.aid, serial, len(msg), len(all))
		for _, o := range all {
			o.send(s.logger, 12, body)
		}
	case 10: // cr_player_action -> idx13 to others
		action, found, ok := pbBytesField(data, 1)
		if !ok || !found {
			s.logger.Printf("ROOMPARTY action invalid")
			return nil
		}
		h.mu.Lock()
		others := r.onFloor(s.pm, s.pm.floor)
		h.mu.Unlock()
		s.logger.Printf("ROOMPARTY action aid=%d fields=%s", s.aid, pbSummary(action))
		body := pbLen(pbVar(nil, 1, s.aid), 2, action)
		for _, o := range others {
			o.send(s.logger, 13, body)
		}
	default:
		s.logger.Printf("ROOMPARTY msgid=%d%s", msgid, pbSummary(data))
	}
	return nil
}

// roomLevelValid accepts LEVEL_<n>.
func roomLevelValid(s string) bool {
	n, ok := strings.CutPrefix(s, "LEVEL_")
	if !ok || n == "" {
		return false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// handleRoom serves GET /v4/room/myroom/<LEVEL> and /v4/room/enter/<aid>/<LEVEL>:
// an empty room with a floor and wall whose assets exist in the full-client dataset.
func handleRoom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v4/room/"), "/")
	var name, level, decorAID string
	switch {
	case len(parts) == 2 && parts[0] == "myroom":
		level = parts[1]
		name = "cherry"
		if acc, ok := accountForRequest(r); ok {
			name = acc.name
		}
	case len(parts) == 3 && parts[0] == "enter":
		level = parts[2]
		if parts[1] == friendAID {
			name = friendName
		} else if acc, ok := accountByAvatarID(parts[1]); ok {
			name, decorAID = acc.name, parts[1]
		} else {
			serveNotFound(w)
			return
		}
	default:
		serveNotFound(w)
		return
	}
	if !roomLevelValid(level) {
		serveNotFound(w)
		return
	}
	roomInfo := map[string]any{
		"groundLevel": level,
		"tileSize":    12, // ponytail: unverified; hero spawn tile (8,4) must fit inside
		// The client's default floor (RUTI0005D = 121500193) has no tile_1.png in the
		// full-client dataset and the room renders blank; RUTI000EW (121500536) has one.
		"floor":     map[string]any{"seq": "1", "cd": roomFloorCode, "type": "floorTile"},
		"wall":      map[string]any{"seq": "0", "cd": roomWallCode, "type": "wallTile"}, // default RUWA0006D (121400229) lacks wall_l/r_1.png
		"floorItem": []any{}, "wallItem": []any{}, "item": []any{}, "memberAvatarIdList": []any{},
	}
	if parts[0] == "myroom" || decorAID != "" { // saved layout (roomdecor.go); friend 100000 and unsaved levels keep the default
		maps.Copy(roomInfo, roomDecorInfo(r, decorAID, level))
	}
	maxLevel := "LEVEL_" + strconv.Itoa(roomPartyFloors)
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"roomInfo":          roomInfo,
		"ownMaxGroundLevel": maxLevel, "availMaxGroundLevel": maxLevel,
		"name": name, "vipRewardedList": []any{},
	}})
	writeJSON(w, http.StatusOK, string(body))
}

// Pets in a Room Party. Scene 303 skips the HTTP pet loads (NaMyRoomBaseScene::
// GetCurrentGroundDepthLayer @0x22c8458 case 303: ClearPet, SetReservePetData), so pets
// arrive only inside the floor messages: rc_floor_relaystart_res idx29 field 3 and
// rc_floor_adduser_all idx30 field 2, both repeated LPNP::PetBasicInfo. The frame is
// parsed with ParsePartialFromArray then IsInitialized and dropped entirely on failure
// (LPNP::roomclient_dispatch @0x252c2cc, "initialize check failed"); AllAreInitialized
// covers the pet lists (@0x25430b4/@0x25430fc), so ONE incomplete pet removes every avatar.
//
// PetBasicInfo (IsInitialized @0x2635340; MergePartial @0x26335e0), req = required:
//
//	1 u64 owner aid (req; queue key of UpdateDelayInOutPet, == hero/host checks)
//	2 u64 pet id (req; RefreashPetDataInRoomparty's FindActorByUniqueID key)
//	3 PetFeatureInfo (req)
//	4 _player_move (opt; all six fields req when present): pet position for the HOST's pets,
//	  target f3/f4 px (RefreashPetDataInRoomparty @0x21b0d7c); other owners' pets spawn at the owner
//	5 _player_interaction (opt, 1 req), 6 PlayerAction (opt, 2 req): unused here
//	7 bool unused, 8 bool (client-side spawn position / sleeping), 9 bool rideable, 10 bool unused
//	11 i32 level (blackboard "level", sDataPetArrangeInfo.level), 12 bool MUST be nonzero:
//	_AddPetsToThisRoom @0x1fbfb5c returns without spawning when byte +80 is 0, 13 i32 ride (nonzero = ride)
//
// PetFeatureInfo (IsInitialized @0x277e0c4, mask 0x37; MergePartial @0x2785b2c):
//
//	1 u64 pet id (req; actor unique id; an existing actor with it is skipped by idx29)
//	2 u64 owner aid (req; actor spawns at this avatar's position: it must be on the floor)
//	3 string pet name (req) 4 string skin code (req; GetIntIdx -> actor resource)
//	5 string category (req; HTTP catgCd, "PET") 6 string item code (opt; GetIntIdx -> arrange info)
//
// The earlier attempt omitted PetFeatureInfo field 5, a required field: the whole frame was
// rejected. Pets are spawned by UpdateDelayInOutPet once the owner's avatar exists.
// Nothing deletes a pet when its owner leaves (idx31 only removes the avatar) and the
// client never sends pet moves (cr_pet_move_req is never built).

// partyPetTiles are where the host's arranged pets are placed (near the centre of the 12x12
// grid, none on a hero spawn tile); ponytail: no furniture/walkability check.
var partyPetTiles = [][2]int{{5, 5}, {6, 5}, {5, 6}, {6, 6}, {4, 5}, {5, 4}, {4, 6}, {6, 4}}

// partyPetMove is a full _player_move (f1..f4 px of the tile, f5 dir 0, f6 action 1).
func partyPetMove(slot int) []byte {
	t := partyPetTiles[slot%len(partyPetTiles)]
	x, y := partyTilePx(t[0], t[1])
	return pbVar(pbVar(pbVar(pbVar(pbVar(pbVar(nil, 1, x), 2, y), 3, x), 4, y), 5, 0), 6, 1)
}

// partyPetInfo encodes one fully initialized LPNP::PetBasicInfo; move (may be nil) is field 4.
func partyPetInfo(owner uint64, p petItem, move []byte) []byte {
	id := uint64(p.ID)
	f := pbVar(pbVar(nil, 1, id), 2, owner)
	f = pbLen(f, 3, []byte(p.Name))
	f = pbLen(f, 4, []byte(p.Cd))
	f = pbLen(f, 5, []byte("PET"))
	f = pbLen(f, 6, []byte(p.Cd))
	b := pbLen(pbVar(pbVar(nil, 1, owner), 2, id), 3, f)
	if move != nil {
		b = pbLen(b, 4, move)
	}
	return pbVar(pbVar(b, 11, 1), 12, 1)
}

// partyPets lists the PetBasicInfo of aid on floor: its representative pet, plus, for the
// host, the pets arranged on that floor's My Room level. Caller must not hold h.mu
// (petsOfAid takes accountsMu).
func partyPets(aid, host uint64, floor int) [][]byte {
	level := "LEVEL_" + strconv.Itoa(floor)
	var out [][]byte
	slot := 0
	for _, p := range petsOfAid(strconv.FormatUint(aid, 10)) {
		arranged := aid == host && p.Room == level
		if !p.Rep && !arranged {
			continue
		}
		var move []byte
		if arranged {
			move, slot = partyPetMove(slot), slot+1
		}
		out = append(out, partyPetInfo(aid, p, move))
	}
	return out
}

// partyFloorRes is rc_floor_relaystart_res idx29: Result 0, PlayerList#2, PetList#3.
func partyFloorRes(roster []partySnap, host uint64, floor int) []byte {
	res := pbVar(nil, 1, 0)
	for _, p := range roster {
		res = pbLen(res, 2, partyPlayerInfo(p.aid, p.move))
	}
	for _, p := range roster {
		for _, pet := range partyPets(p.aid, host, floor) {
			res = pbLen(res, 3, pet)
		}
	}
	return res
}

// partyFloorAdd is rc_floor_adduser_all idx30: Player#1, PetList#2.
func partyFloorAdd(p partySnap, host uint64, floor int) []byte {
	b := pbLen(nil, 1, partyPlayerInfo(p.aid, p.move))
	for _, pet := range partyPets(p.aid, host, floor) {
		b = pbLen(b, 2, pet)
	}
	return b
}
