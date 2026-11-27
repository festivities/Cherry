package lpn

import (
	"encoding/binary"
	"encoding/json"
	"log"
	"maps"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/room"
	"cherry/internal/store"
)

// Room Party (LPNP, gateway agent 4). Handshake per static research: client
// open -> type-1 control (never a type-2 ack: at status 6 an ack re-runs
// OpenTunnel and restarts the retry loop) -> cr_login_req idx0 -> rc_login_res
// idx0 result 0.
const roomPartyAgent = 4

// LPNP::ENTER_RESULT (LP_rc_room_enter_res @0x1f08374, NaRoomPartyExpirePopup::InitUI @0x19f7cec).
const (
	partyEnterOK      = 0
	partyEnterInvalid = 1 // string 1106: invitation invalid / blocked / banished / full
	partyEnterExpired = 2 // string 1133: invitation period ended
	partyEnterEnded   = 3 // string 1134: room party has already ended
)

// LPNP::EXIT_REASON (LP_rc_room_deluser_all @0x1f0810c, NaRoomPartyLayer::MsgProc case 12645 @0x1fc2460).
const (
	partyExitNormal    = 0
	partyExitAdminKick = 1 // string 1139: kicked by administrator
	partyExitEnded     = 2 // string 1100: host ended party / banished guest -> Moving to My Garden
)

// partyMember is one logged-in agent-4 connection. b1/sid come from the login
// frame (constant per tunnel) so other connections can push to it.
type partyMember struct {
	aid         uint64
	conn        *lockedConn
	b1          byte
	sid         [8]byte
	move        []byte // last _player_move, guarded by partyHub.mu
	interaction []byte // active _player_interaction (sit/lie/furniture), cleared on move/floor change; guarded by partyHub.mu
	ridePetID   uint64 // active mounted pet id (0 if not riding); guarded by partyHub.mu
	rideState   uint64 // non-zero while riding ridePetID; guarded by partyHub.mu
	nmov        int    // cr_player_move count, guarded by partyHub.mu
	floor       int    // current floor 1..roomPartyFloors, guarded by partyHub.mu
}

const partyStatusTimeout = 600 // rc_room_status_push Timeout; client keeps MyRoomAlive 2*Timeout s

// partyStatusBody is rc_room_status_push (idx23) {Exist#1, Timeout#2} for an active room.
func partyStatusBody() []byte {
	return pbVar(pbVar(nil, 1, 1), 2, partyStatusTimeout)
}

// partyStatusEndedBody is rc_room_status_push (idx23) {Exist=0, Timeout=0}: sent to the host
// when they end their Room Party (RoomPartyService::onRoomStatus sets MyRoomAlive=false, MyRoomExploded=true).
func partyStatusEndedBody() []byte {
	return pbVar(pbVar(nil, 1, 0), 2, 0)
}

// partyMaxVisitors returns the maximum number of non-host visitors for host's room,
// matching RoomPartyService::GetMaxVisitor (@0x1f08c24): <=23 -> 5, <=29 -> 7, <=35 -> 10, >=36 -> 20.
func partyMaxVisitors(host uint64) int {
	sz := room.HostRoomTileSize(host)
	switch {
	case sz <= 23:
		return 5
	case sz <= 29:
		return 7
	case sz <= 35:
		return 10
	default:
		return 20
	}
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

// partySpawnTile is DefaultHeroPosition: every member starts there and avatars stack until they walk off. Neighbour
// tiles can hold furniture, where the client ignores every tap (the Square spawn bug, 2026-11-25).
var partySpawnTile = [2]int{8, 4}

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

// spawnLocked assigns the spawn _player_move: f1..f4 = partySpawnTile px, f5/f6
// copied from the client's own move (f6 default 1). Caller holds hub.mu.
func (r *partyRoom) spawnLocked(client []byte) (move []byte, tx, ty int) {
	tx, ty = partySpawnTile[0], partySpawnTile[1]
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
// it is deleted when its last member leaves and the host has no session, or
// immediately when the host ends the party via cr_room_banish_req (Explode=true).
// Invites live as long as the room (no 600 s expiry).
type partyRoom struct {
	host       uint64
	members    []*partyMember // join order; roster puts the host first
	invited    map[uint64]bool
	banished   map[uint64]bool // kicked during this party; cleared on re-invite
	chatSerial uint64
}

type partyHub struct {
	mu       sync.Mutex
	sessions map[uint64]*partyMember
	rooms    map[uint64]*partyRoom
	bows     map[uint64]partyBows // ponytail: in memory, resets on restart; no per-day bow cap
	ticking  bool                 // status loop running, guarded by mu
	every    time.Duration        // status refresh period; 0 = partyStatusEvery
}

// partyBows is one source's bow count on game day day (economy.AttendDayKey), guarded by hub.mu.
type partyBows struct{ day, n int64 }

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
		r = &partyRoom{host: host, invited: map[uint64]bool{}, banished: map[uint64]bool{}}
		h.rooms[host] = r
	}
	return r
}

// visitorCountLocked counts non-host members currently in the room.
func (r *partyRoom) visitorCountLocked() int {
	n := 0
	for _, o := range r.members {
		if o.aid != r.host {
			n++
		}
	}
	return n
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
	acc, ok := store.AccountByAvatarID(id)
	if !ok {
		acc = store.AccountSnapshot{Aid: id, Name: "cherry", Gender: "FEMALE"}
	}
	if acc.Skin == "" {
		acc.Skin = "1"
	}
	if acc.Country == "" {
		acc.Country = squareDefaultCountry
	}
	gender := "FEMALE"
	if acc.Gender == "MALE" || acc.Gender == "ANIMAL" {
		gender = acc.Gender
	}
	b := pbLen(nil, 1, []byte(id))
	b = pbLen(b, 3, []byte(acc.Name))
	b = pbLen(b, 4, []byte(gender))
	for _, code := range acc.ItemCodes {
		b = pbLen(b, 5, pbLen(nil, 1, []byte(code)))
	}
	b = pbLen(b, 6, []byte(acc.Country))
	return pbLen(b, 10, []byte(acc.Skin))
}

// partyPlayerInfo encodes PlayerInfo; move is the member's last _player_move.
func partyPlayerInfo(aid uint64, move []byte) []byte {
	return partyPlayerInfoFull(aid, move, nil)
}

// partyPlayerInfoFull encodes PlayerInfo with optional _player_move (f3) and _player_interaction (f4).
func partyPlayerInfoFull(aid uint64, move, interaction []byte) []byte {
	b := pbLen(pbVar(nil, 1, aid), 2, partyAvatarInfo(aid))
	if len(move) > 0 {
		b = pbLen(b, 3, move)
	}
	if len(interaction) > 0 {
		b = pbLen(b, 4, interaction)
	}
	return b
}

type partySnap struct {
	aid  uint64
	move []byte
}

type partyFloorSnap struct {
	aid         uint64
	move        []byte
	interaction []byte
	ridePetID   uint64
	rideState   uint64
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

// floorRosterLocked lists members on floor with interaction and pet-ride state, host first; caller holds h.mu.
func (r *partyRoom) floorRosterLocked(floor int) []partyFloorSnap {
	var out []partyFloorSnap
	for _, o := range r.members {
		if o.aid == r.host && o.floor == floor {
			out = append(out, partyFloorSnap{o.aid, o.move, o.interaction, o.ridePetID, o.rideState})
		}
	}
	for _, o := range r.members {
		if o.aid != r.host && o.floor == floor {
			out = append(out, partyFloorSnap{o.aid, o.move, o.interaction, o.ridePetID, o.rideState})
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
	tlup     *[8]byte  // the connection's latest TLUP session; the observer writes it on this goroutine, nil in tests
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
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	return store.FriendshipIndex(strconv.FormatUint(a, 10), strconv.FormatUint(b, 10), "accepted") >= 0
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
	if result == partyEnterOK {
		b = pbVar(b, 5, uint64(room.HostMaxFloor(host))) // ExtendFloors
	} else {
		// Field 4 (HostName) supplies "%s" to NaShowRoomPartyExpirePopup so the client never
		// falls back to string 10053 ("This avatar has been deleted.").
		b = pbLen(b, 4, partyName(host))
	}
	return b
}

// enter answers cr_room_enter_req for host. Success when the requester is the
// host (room created/kept) or the room exists, the requester is not blocked or
// banished, the room is below visitor capacity, and the requester is the host's
// accepted friend or was invited.
func (s *roomPartySession) enter(frame []byte, host uint64) error {
	h := s.h()
	result := uint64(partyEnterOK)
	if host != s.aid {
		aidStr := strconv.FormatUint(s.aid, 10)
		hostStr := strconv.FormatUint(host, 10)
		blocked := store.BlockedBetween(aidStr, hostStr)
		friend := !blocked && partyAccepted(s.aid, host) // socialMu never nests inside hub.mu
		maxVis := partyMaxVisitors(host)
		h.mu.Lock()
		r := h.rooms[host]
		switch {
		case r == nil:
			result = partyEnterEnded
		case blocked || r.banished[s.aid]:
			result = partyEnterInvalid
		case !friend && !r.invited[s.aid]:
			result = partyEnterInvalid
		case !r.has(s.aid) && r.visitorCountLocked() >= maxVis:
			result = partyEnterInvalid
		}
		h.mu.Unlock()
	}
	if result == partyEnterOK {
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
	if result == partyEnterOK && host == s.aid { // lets the host's My Room menu rejoin this party
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
		aid, ok := verifiedLogin(data)
		if !ok {
			s.logger.Printf("ROOMPARTY login refused: no verified login")
			return nil
		}
		s.close()
		s.aid, s.loggedIn = aid, true
		bindGatewayLogin(s.conn, aid)
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
	case 4: // cr_room_banish_req (AidList#1, Explode#2) -> rc_room_banish_res idx4 + deluser/status
		return s.banish(frame, data)
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
	case 17: // cr_quest_archive_req (TargetAid#1) -> rc_quest_archive_all idx21 (bow)
		return s.bow(frame, data)
	case 13: // cr_report_chat: stored, no reply (report.go)
		s.report(data)
		return nil
	case 19: // cr_player_bi: client action telemetry, no reply
		return nil
	case 20: // RoundTripdelayTimeReq -> RoundTripdelayTimeRes idx24
		return s.reply(frame, 24, data)
	case 26: // cr_pet_wake_req (PetId#1) -> rc_pet_wake_res idx34
		return s.petWake(frame, data)
	case 27: // cr_pet_ride_req (PetId#1, RideState#2) -> rc_pet_ride_res idx35
		return s.petRide(frame, data)
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
	// Only the room's host cancels its invitations: a member who joined another room would otherwise edit its host's
	// Waiting list. The refreshed list still answers, as for any cancel; nothing changes for a non-host.
	if host != s.aid {
		s.logger.Printf("ROOMPARTY invite-cancel refused aid=%d: not the host of room %d", s.aid, host)
	} else if r := h.rooms[host]; r != nil {
		for _, a := range aids {
			delete(r.invited, a)
		}
	}
	h.mu.Unlock()
	body, w, in := s.inviteListBody()
	s.logger.Printf("ROOMPARTY invite-cancel aid=%d host=%d cancelled=%d waiting=%d inroom=%d", s.aid, host, len(aids), w, in)
	return s.reply(frame, 22, body)
}

// partyFloorOK reports whether f is a servable floor of host's room.
func partyFloorOK(host, f uint64) bool { return f >= 1 && f <= uint64(room.HostMaxFloor(host)) }

// partyName is the display name for floor notifications.
func partyName(aid uint64) []byte {
	if acc, ok := store.AccountByAvatarID(strconv.FormatUint(aid, 10)); ok && acc.Name != "" {
		return []byte(acc.Name)
	}
	return []byte("cherry")
}

// allRosterLocked lists every member (all floors), floor by floor; caller holds h.mu.
func (r *partyRoom) allRosterLocked() []partySnap {
	var out []partySnap
	for f := 1; f <= room.MaxRoomFloors; f++ {
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
	if f, found := protobufUintField(data, 2); found && partyFloorOK(s.host, f) {
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
		assigned, tx, ty = r.spawnLocked(move)
	}
	if idx >= 0 && oldFloor != floor {
		m.interaction = nil
	}
	m.move, m.floor = assigned, floor
	if idx >= 0 {
		r.members[idx] = m
	} else {
		r.members = append(r.members, m)
	}
	roster := r.floorRosterLocked(floor)
	selfSnap := partyFloorSnap{m.aid, m.move, m.interaction, m.ridePetID, m.rideState}
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
	// The self row also repositions the hero (setPosition cur f1,f2): it places the hero on the spawn tile.
	for _, p := range all {
		if p.aid != m.aid {
			m.send(s.logger, 1, pbLen(nil, 1, partyPlayerInfo(p.aid, p.move)))
		}
	}
	m.send(s.logger, 29, partyFloorResWithState(roster, s.host, floor))
	if idx >= 0 && oldFloor == floor {
		return nil
	}
	add := pbLen(nil, 1, partyPlayerInfo(m.aid, m.move))
	if idx < 0 {
		for _, o := range others {
			o.send(s.logger, 1, add)
		}
	}
	floorAdd := partyFloorAddWithState(selfSnap, s.host, floor)
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
	r := s.member()
	result := uint64(0)
	if r == nil || !partyFloorOK(r.host, f) {
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
	if r == nil || !partyFloorOK(r.host, f) || !ok {
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
		assigned, tx, ty = r.spawnLocked(move)
	}
	if oldFloor != floor {
		m.interaction = nil
	}
	m.move, m.floor = assigned, floor
	roster := r.floorRosterLocked(floor)
	selfSnap := partyFloorSnap{m.aid, m.move, m.interaction, m.ridePetID, m.rideState}
	newMates := r.onFloor(m, floor)
	var oldMates []*partyMember
	if oldFloor != floor {
		oldMates = r.onFloor(m, oldFloor)
	}
	others := r.others(m)
	h.mu.Unlock()
	s.logger.Printf("ROOMPARTY floor-relaystart aid=%d floor=%d->%d clientCur=(%d,%d) assignedTile=(%d,%d) roster=%d", s.aid, oldFloor, floor, cx, cy, tx, ty, len(roster))
	m.send(s.logger, 29, pbVar(partyFloorResWithState(roster, s.host, floor), 4, f))
	if oldFloor == floor {
		return nil
	}
	add := partyFloorAddWithState(selfSnap, s.host, floor)
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
	actorAidStr := strconv.FormatUint(s.aid, 10)
	for _, a := range aids {
		targetStr := strconv.FormatUint(a, 10)
		_, real := store.AccountByAvatarID(targetStr)
		if real && a != s.aid && !store.BlockedBetween(actorAidStr, targetStr) && partyAccepted(s.aid, a) {
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
		delete(r.banished, a) // explicit re-invite lifts a prior kick from this party
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
		actorAid := actorAidStr
		actorName := ""
		if snap, ok := store.AccountByAvatarID(actorAid); ok {
			actorName = snap.Name
		}
		at := economy.NowFunc().UnixMilli()
		for _, a := range good {
			store.AddNewsAlert(strconv.FormatUint(a, 10), store.NewsAlert{
				Type:      "ROOM_INVITE_RECEIVE",
				At:        at,
				Actor:     actorAid,
				ActorName: actorName,
			})
		}
		h.mu.Lock()
		hs := h.sessions[host]
		h.mu.Unlock()
		if hs != nil {
			hs.send(s.logger, 23, partyStatusBody())
			h.startStatusLoop(s.logger)
		}
	}
	return nil
}

// banish handles cr_room_banish_req idx4 {AidList#1, Explode#2}:
//   - Explode=true ("End Party", RoomPartyService::RoomExplosion @0x1fbd7bc): closes the host's
//     Room Party, sends rc_room_status_push(Exist=0, Timeout=0) to the host (setting MyRoomAlive=false,
//     MyRoomExploded=true), and sends rc_room_deluser_all(self, EXIT_REASON=2) to every member so
//     guests see string 1100 ("The host has ended their room party. Moving to My Garden.") and the
//     host exits scene 303 cleanly.
//   - Explode=false (kick guest, RoomPartyService::SendBanish @0x1fbdaa8): removes the named guest(s)
//     from the party, marks them banished until re-invited, sends EXIT_REASON=2 to the kicked guest,
//     and notifies remaining members with idx2 (deluser) and idx31 (floor deluser).
func (s *roomPartySession) banish(frame, data []byte) error {
	var aids []uint64
	var explode bool
	ok := pbScan(data, func(f, v uint64, d []byte) {
		switch f {
		case 1:
			if len(aids) >= 100 {
				return
			}
			if d == nil {
				aids = append(aids, v)
				return
			}
			for len(d) > 0 && len(aids) < 100 {
				x, n := binary.Uvarint(d)
				if n <= 0 {
					return
				}
				aids = append(aids, x)
				d = d[n:]
			}
		case 2:
			if d == nil {
				explode = v != 0
			}
		}
	})
	if !ok {
		s.logger.Printf("ROOMPARTY banish invalid aid=%d", s.aid)
		return nil
	}
	// Always reply with empty rc_room_banish_res (idx4).
	if err := s.reply(frame, 4, nil); err != nil {
		return err
	}
	host := s.host
	if host == 0 {
		host = s.aid
	}
	if host != s.aid {
		s.logger.Printf("ROOMPARTY banish refused aid=%d: not the host of room %d", s.aid, host)
		return nil
	}
	h := s.h()
	if explode {
		h.mu.Lock()
		r := h.rooms[s.aid]
		var members []*partyMember
		if r != nil {
			members = append([]*partyMember(nil), r.members...)
			r.members = nil
			r.invited = nil
			r.banished = nil
			delete(h.rooms, s.aid)
		}
		h.mu.Unlock()
		if s.joined != nil && s.joined.host == s.aid {
			s.joined = nil
		}
		if s.host == s.aid {
			s.host = 0
		}
		s.logger.Printf("ROOMPARTY explode aid=%d members=%d", s.aid, len(members))
		s.pm.send(s.logger, 23, partyStatusEndedBody())
		for _, o := range members {
			o.send(s.logger, 2, pbVar(pbVar(nil, 1, o.aid), 2, partyExitEnded))
		}
		return nil
	}
	type kickedInfo struct {
		member    *partyMember
		rest      []*partyMember
		sameFloor []*partyMember
	}
	var kicked []kickedInfo
	h.mu.Lock()
	if r := h.rooms[s.aid]; r != nil {
		for _, target := range aids {
			if target == s.aid {
				continue
			}
			delete(r.invited, target)
			if r.banished == nil {
				r.banished = map[uint64]bool{}
			}
			r.banished[target] = true
			for i, o := range r.members {
				if o.aid == target {
					floor := o.floor
					r.members = append(r.members[:i], r.members[i+1:]...)
					kicked = append(kicked, kickedInfo{
						member:    o,
						rest:      r.others(nil),
						sameFloor: r.onFloor(nil, floor),
					})
					break
				}
			}
		}
	}
	h.mu.Unlock()
	s.logger.Printf("ROOMPARTY banish aid=%d targets=%d kicked=%d", s.aid, len(aids), len(kicked))
	for _, k := range kicked {
		k.member.send(s.logger, 2, pbVar(pbVar(nil, 1, k.member.aid), 2, partyExitEnded))
		delRoom := pbVar(pbVar(nil, 1, k.member.aid), 2, partyExitNormal)
		delFloor := pbVar(nil, 1, k.member.aid)
		for _, o := range k.rest {
			o.send(s.logger, 2, delRoom)
		}
		for _, o := range k.sameFloor {
			o.send(s.logger, 31, delFloor)
		}
	}
	return nil
}

// petWake handles cr_pet_wake_req idx26 {PetId#1} -> rc_pet_wake_res idx34 {PetId#1}.
func (s *roomPartySession) petWake(frame, data []byte) error {
	petID, found := protobufUintField(data, 1)
	if !found || petID == 0 {
		return nil
	}
	s.logger.Printf("ROOMPARTY pet-wake aid=%d pet=%d", s.aid, petID)
	return s.reply(frame, 34, pbVar(nil, 1, petID))
}

// petRide handles cr_pet_ride_req idx27 {PetId#1, RideState#2} -> rc_pet_ride_res idx35
// {Result#1 (0=OK, 1=error), Aid#2, PetId#3, RideState#4} broadcast to same floor.
func (s *roomPartySession) petRide(frame, data []byte) error {
	r := s.member()
	if r == nil {
		return nil
	}
	var petID, rideState uint64
	if !pbScan(data, func(f, v uint64, _ []byte) {
		switch f {
		case 1:
			petID = v
		case 2:
			rideState = v
		}
	}) {
		return nil
	}
	if rideState != 0 {
		rideState = 1
		p, ok := squareFollowPet(s.aid)
		if !ok || uint64(p.ID) != petID || economy.PetCategoryCode(p.Cd) != "URIDE" {
			s.logger.Printf("ROOMPARTY pet-ride refused aid=%d pet=%d", s.aid, petID)
			body := pbVar(pbVar(pbVar(pbVar(nil, 1, 1), 2, s.aid), 3, petID), 4, 0)
			return s.reply(frame, 35, body)
		}
	}
	h := s.h()
	h.mu.Lock()
	if rideState != 0 {
		s.pm.ridePetID, s.pm.rideState = petID, rideState
		s.pm.interaction = nil
	} else {
		s.pm.ridePetID, s.pm.rideState = 0, 0
	}
	sameFloor := r.onFloor(nil, s.pm.floor)
	h.mu.Unlock()
	s.logger.Printf("ROOMPARTY pet-ride aid=%d pet=%d state=%d to=%d", s.aid, petID, rideState, len(sameFloor))
	body := pbVar(pbVar(pbVar(pbVar(nil, 1, 0), 2, s.aid), 3, petID), 4, rideState)
	for _, o := range sameFloor {
		o.send(s.logger, 35, body)
	}
	return nil
}

// bowBody is rc_quest_archive_all idx21 {Success#1, SourceId#2, TargetId#3, ArchiveCount#4,
// RewardCoin#5 (0), CompleteQuest#6 (false: true would pop a completion popup)}.
func bowBody(src, dst uint64, count int64) []byte {
	b := pbVar(pbVar(pbVar(pbVar(nil, 1, 1), 2, src), 3, dst), 4, uint64(count))
	return pbVar(pbVar(b, 5, 0), 6, 0)
}

// bow handles cr_quest_archive_req {TargetAid#1}: a room member bows another member. Bows pay
// nothing. Both sides get idx21. A bow to the host also re-sends the heart show, because the client
// creates the host's heart button hidden unless the host is already in its bow-complete list.
func (s *roomPartySession) bow(frame, data []byte) error {
	h := s.h()
	target, _ := protobufUintField(data, 1)
	now := economy.NowFunc()
	h.mu.Lock()
	var dst *partyMember
	r := s.joined
	if r != nil && r.index(s.pm) >= 0 { // not replaced by a newer connection of the same aid
		for _, o := range r.members {
			if o != s.pm && o.aid == target {
				dst = o
			}
		}
	}
	if dst == nil {
		h.mu.Unlock()
		s.logger.Printf("ROOMPARTY bow ignored aid=%d target=%d", s.aid, target)
		return nil
	}
	day := economy.AttendDayKey(now)
	if h.bows == nil {
		h.bows = map[uint64]partyBows{}
	}
	b := h.bows[s.aid]
	if b.day != day {
		b = partyBows{day: day}
	}
	b.n++
	h.bows[s.aid] = b
	host := r.host
	h.mu.Unlock()
	s.logger.Printf("ROOMPARTY bow aid=%d target=%d count=%d", s.aid, target, b.n)
	body := bowBody(s.aid, target, b.n)
	if err := s.reply(frame, 21, body); err != nil {
		return err
	}
	dst.send(s.logger, 21, body)
	if target != host {
		return nil
	}
	if s.tlup == nil || *s.tlup == ([8]byte{}) { // zero = no TLUP session seen on this connection yet
		s.logger.Printf("ROOMPARTY bow heart-show skipped aid=%d no TLUP session", s.aid)
		return nil
	}
	return heartButtonShow(s.conn, *s.tlup, s.aid, host, now, s.logger)
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
		if len(move) > maxRelayPayload {
			s.logger.Printf("ROOMPARTY move over %d bytes ignored", maxRelayPayload)
			return nil
		}
		h.mu.Lock()
		s.pm.move = append([]byte(nil), move...)
		s.pm.interaction = nil // walking clears any active furniture interaction
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
		store.RecordChat(store.ChatEntry{Kind: store.ChatParty, Channel: strconv.FormatUint(r.host, 10), Sender: strconv.FormatUint(s.aid, 10), SenderName: squareAccountName(s.aid), Text: string(msg)})
		for _, o := range all {
			o.send(s.logger, 12, body)
		}
	case 10: // cr_player_action -> idx13 to others
		action, found, ok := pbBytesField(data, 1)
		if !ok || !found {
			s.logger.Printf("ROOMPARTY action invalid")
			return nil
		}
		if len(action) > maxRelayPayload {
			s.logger.Printf("ROOMPARTY action over %d bytes ignored", maxRelayPayload)
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
	case 14: // cr_player_interaction_req (f1 _player_interaction) -> rc_player_interaction_all idx17 to same-floor others
		inter, found, ok := pbBytesField(data, 1)
		if !ok || !found || len(inter) == 0 || len(inter) > maxRelayPayload {
			s.logger.Printf("ROOMPARTY interaction invalid aid=%d", s.aid)
			return nil
		}
		h.mu.Lock()
		s.pm.interaction = append([]byte(nil), inter...)
		others := r.onFloor(s.pm, s.pm.floor)
		h.mu.Unlock()
		body := pbLen(pbVar(nil, 1, s.aid), 2, inter)
		for _, o := range others {
			o.send(s.logger, 17, body)
		}
	case 21: // cr_pet_move_req (f1 repeated PetMoveInfo) -> rc_pet_move_all idx25 (f1 owner, f2 repeated PetMoveInfo)
		if len(data) > maxRelayPayload {
			return nil
		}
		var moves [][]byte
		ok := pbScan(data, func(f, _ uint64, d []byte) {
			if f == 1 && len(d) > 0 {
				moves = append(moves, d)
			}
		})
		if !ok || len(moves) == 0 {
			return nil
		}
		h.mu.Lock()
		others := r.onFloor(s.pm, s.pm.floor)
		h.mu.Unlock()
		body := pbVar(nil, 1, s.aid)
		for _, mv := range moves {
			body = pbLen(body, 2, mv)
		}
		for _, o := range others {
			o.send(s.logger, 25, body)
		}
	case 22: // cr_pet_action_req (f1 petId, f2 PlayerAction) -> rc_pet_action_all idx26 (f1 owner, f2 petId, f3 PlayerAction)
		var pid uint64
		var action []byte
		ok := pbScan(data, func(f, v uint64, d []byte) {
			switch f {
			case 1:
				pid = v
			case 2:
				action = d
			}
		})
		if !ok || pid == 0 || len(action) == 0 || len(action) > maxRelayPayload {
			return nil
		}
		h.mu.Lock()
		others := r.onFloor(s.pm, s.pm.floor)
		h.mu.Unlock()
		body := pbLen(pbVar(pbVar(nil, 1, s.aid), 2, pid), 3, action)
		for _, o := range others {
			o.send(s.logger, 26, body)
		}
	case 23: // cr_pet_interaction_req (f1 petId, f2 _player_interaction) -> rc_pet_interaction_all idx27 to all on floor
		var pid uint64
		var inter []byte
		ok := pbScan(data, func(f, v uint64, d []byte) {
			switch f {
			case 1:
				pid = v
			case 2:
				inter = d
			}
		})
		if !ok || pid == 0 || len(inter) == 0 || len(inter) > maxRelayPayload {
			return nil
		}
		h.mu.Lock()
		sameFloor := r.onFloor(nil, s.pm.floor)
		h.mu.Unlock()
		body := pbLen(pbVar(pbVar(nil, 1, s.aid), 2, pid), 3, inter)
		for _, o := range sameFloor {
			o.send(s.logger, 27, body)
		}
	default:
		s.logger.Printf("ROOMPARTY msgid=%d%s", msgid, pbSummary(data))
	}
	return nil
}

// vipLoungeHostAID is the host of the VIP Lounge (MENU row 50, scene 304): the constant that
// NaBrandManager::NaBrandManager (0x194fa90) stores at this+408 and CSceneChange::DivideRoomScene
// (0x1c8b904) sends as room/enter. It is not an account. The client has no VIP check on that path (the
// non-VIP popup's "Go to the VIP Garden", NaVipLoungeNoneVipPopup::CButtonHandler case 27, uses it too),
// so it is served as a free visit to an empty lounge; VIP membership itself stays unserved.
// ponytail: default empty room, no lounge decor; add a Cherry-made layout when wanted.
const (
	vipLoungeHostAID = "1145205883351836906"
	vipLoungeName    = "Cherry VIP Lounge"
)

// HandleRoom serves GET /v4/room/myroom/<LEVEL> and /v4/room/enter/<aid>/<LEVEL>:
// an empty room with a floor and wall whose assets exist in the full-client dataset.
func HandleRoom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v4/room/"), "/")
	var name, level, decorAID string
	lounge := false
	switch {
	case len(parts) == 2 && parts[0] == "myroom":
		level = parts[1]
		name = "cherry"
		if acc, ok := store.AccountForRequest(r); ok {
			name = acc.Name
		}
	case len(parts) == 3 && parts[0] == "enter":
		level = parts[2]
		if parts[1] == store.FriendAID {
			name = store.FriendName()
		} else if parts[1] == vipLoungeHostAID {
			name, lounge = vipLoungeName, true
		} else if acc, ok := store.AccountByAvatarID(parts[1]); ok {
			name, decorAID = acc.Name, parts[1]
		} else {
			httpx.ServeNotFound(w)
			return
		}
	default:
		httpx.ServeNotFound(w)
		return
	}
	if !room.RoomLevelValid(level) {
		httpx.ServeNotFound(w)
		return
	}
	if viewer, ok := store.AccountForRequest(r); ok {
		if v, err := strconv.ParseUint(viewer.Aid, 10, 64); err == nil {
			if parts[0] == "myroom" || lounge {
				ClearRoomVisit(v)
			} else if owner, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
				NoteRoomVisit(v, owner)
			}
		}
	}
	decor := parts[0] == "myroom" || decorAID != ""
	roomInfo := roomInfoFor(r, level, decorAID, decor)
	maxFloor := room.RoomPartyFloors
	if decor {
		maxFloor = room.AccountMaxFloor(r, decorAID)
	}
	maxLevel := "LEVEL_" + strconv.Itoa(maxFloor)
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"roomInfo":          roomInfo,
		"ownMaxGroundLevel": maxLevel, "availMaxGroundLevel": maxLevel,
		"name": name, "vipRewardedList": []any{},
	}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// roomInfoFor is the roomInfo object of a My Room at level. decor adds the saved layout of decorAID
// ("" = the requester's own room). Visits send it as "roomInfo", the random room as "room".
func roomInfoFor(r *http.Request, level, decorAID string, decor bool) map[string]any {
	roomInfo := map[string]any{
		"groundLevel": level,
		"tileSize":    12, // ponytail: unverified; hero spawn tile (8,4) must fit inside
		// The client's default floor (RUTI0005D = 121500193) has no tile_1.png in the
		// full-client dataset and the room renders blank; RUTI000EW (121500536) has one.
		"floor":     map[string]any{"seq": "1", "cd": room.RoomFloorCode, "type": "floorTile"},
		"wall":      map[string]any{"seq": "0", "cd": room.RoomWallCode, "type": "wallTile"}, // default RUWA0006D (121400229) lacks wall_l/r_1.png
		"floorItem": []any{}, "wallItem": []any{}, "item": []any{}, "memberAvatarIdList": []any{},
	}
	if decor { // saved layout (roomdecor.go); friend 100000 and unsaved levels keep the default
		maps.Copy(roomInfo, room.RoomDecorInfo(r, decorAID, level))
	}
	return roomInfo
}

// randomRoomLevel is the ground level of a random visit (room/random has no level parameter).
const randomRoomLevel = "LEVEL_1"

// noOtherRoomBody answers a random visit when no other account exists. An error body with an errorCode
// other than 10001 (or 140001/140002) gets the client's "unexpected error" popup 10077 and a Garden reload
// (ErrRandomRoomInfoWithCachePolicy @0x1b950d8). The spec's 404 gives the same popup (audit row 12); a 500
// is used here as the explicit error body. [I] for the 500 status: the client only tests status 504.
const noOtherRoomBody = `{"errorCode":"500","errorMessage":"cherry: no other room"}`

// HandleRoomRandom serves GET /v4/room/random (Other Room): the My Room of a random account other
// than the requester. The reply is the room/enter layout under "room" (ResRandomRoomInfoWithCachePolicy
// @0x1bb178c reads result.room, hostAvatarId, ownMaxGroundLevel and availMaxGroundLevel). The client asks
// again when hostAvatarId is its own aid, so the requester is never picked.
func HandleRoomRandom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, _ := store.AccountForRequest(r) // no session: Aid "" excludes nobody
	host, ok := randomRoomHost(viewer.Aid)
	if !ok {
		httpx.WriteJSON(w, http.StatusInternalServerError, noOtherRoomBody)
		return
	}
	if v, err := strconv.ParseUint(viewer.Aid, 10, 64); err == nil {
		if h, err := strconv.ParseUint(host, 10, 64); err == nil {
			NoteRoomVisit(v, h)
		}
	}
	maxLevel := "LEVEL_" + strconv.Itoa(room.AccountMaxFloor(r, host))
	httpx.WriteObj(w, map[string]any{"result": map[string]any{
		"hostAvatarId": host, "room": roomInfoFor(r, randomRoomLevel, host, true),
		"ownMaxGroundLevel": maxLevel, "availMaxGroundLevel": maxLevel,
	}})
}

// HandleRandomGardenHost serves GET /v4/room/random/hostaid (home MENU Random Garden; ReqRandomVisitAid
// 0x1b9564c, ResRandomVisitAid 0x1b11038 reads result.hostAvatarId as a string, message 11072): a random
// other account whose Garden the client then visits. With nobody else the requester's own aid is sent, which
// reloads its own Garden (the client's error path does the same with an empty aid, after a popup).
func HandleRandomGardenHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, _ := store.AccountForRequest(r)
	host, ok := randomRoomHost(viewer.Aid)
	if !ok {
		host = viewer.Aid
	}
	httpx.WriteObj(w, map[string]any{"result": map[string]any{"hostAvatarId": host}})
}

// randomRoomHost picks a random account other than exclude, once per aid (an account may hold several
// token aliases). ok is false when there is no such account.
func randomRoomHost(exclude string) (string, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	seen := map[string]bool{}
	var aids []string
	for _, acc := range store.AllAccountsLocked() {
		if acc == nil || acc.Aid == exclude || acc.Aid == "0" || !store.ValidAvatarID(acc.Aid) || seen[acc.Aid] {
			continue
		}
		seen[acc.Aid] = true
		aids = append(aids, acc.Aid)
	}
	if len(aids) == 0 {
		return "", false
	}
	return aids[rand.IntN(len(aids))], true
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
func partyPetInfo(owner uint64, p store.PetItem, move []byte) []byte {
	return partyPetInfoWithRide(owner, p, move, 0)
}

func partyPetInfoWithRide(owner uint64, p store.PetItem, move []byte, rideState uint64) []byte {
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
	if economy.PetCategoryCode(p.Cd) == "URIDE" {
		b = pbVar(b, 9, 1) // rideable
	}
	b = pbVar(pbVar(b, 11, 1), 12, 1)
	if rideState != 0 {
		b = pbVar(b, 13, rideState)
	}
	return b
}

// partyPets lists the PetBasicInfo of aid on floor: its representative pet, plus, for the
// host, the pets arranged on that floor's My Room level. Caller must not hold h.mu
// (petsOfAid takes accountsMu).
func partyPets(aid, host uint64, floor int) [][]byte {
	return partyPetsWithRide(aid, host, floor, 0, 0)
}

func partyPetsWithRide(aid, host uint64, floor int, ridePetID, rideState uint64) [][]byte {
	level := "LEVEL_" + strconv.Itoa(floor)
	var out [][]byte
	slot := 0
	for _, p := range room.PetsOfAid(strconv.FormatUint(aid, 10)) {
		arranged := aid == host && p.Room == level
		if !p.Rep && !arranged {
			continue
		}
		var move []byte
		if arranged {
			move, slot = partyPetMove(slot), slot+1
		}
		rs := uint64(0)
		if rideState != 0 && uint64(p.ID) == ridePetID {
			rs = rideState
		}
		out = append(out, partyPetInfoWithRide(aid, p, move, rs))
	}
	return out
}

// partyFloorRes is rc_floor_relaystart_res idx29: Result 0, PlayerList#2, PetList#3.
func partyFloorRes(roster []partySnap, host uint64, floor int) []byte {
	snaps := make([]partyFloorSnap, len(roster))
	for i, p := range roster {
		snaps[i] = partyFloorSnap{aid: p.aid, move: p.move}
	}
	return partyFloorResWithState(snaps, host, floor)
}

func partyFloorResWithState(roster []partyFloorSnap, host uint64, floor int) []byte {
	res := pbVar(nil, 1, 0)
	for _, p := range roster {
		res = pbLen(res, 2, partyPlayerInfoFull(p.aid, p.move, p.interaction))
	}
	for _, p := range roster {
		for _, pet := range partyPetsWithRide(p.aid, host, floor, p.ridePetID, p.rideState) {
			res = pbLen(res, 3, pet)
		}
	}
	return res
}

// partyFloorAdd is rc_floor_adduser_all idx30: Player#1, PetList#2.
func partyFloorAdd(p partySnap, host uint64, floor int) []byte {
	return partyFloorAddWithState(partyFloorSnap{aid: p.aid, move: p.move}, host, floor)
}

func partyFloorAddWithState(p partyFloorSnap, host uint64, floor int) []byte {
	b := pbLen(nil, 1, partyPlayerInfoFull(p.aid, p.move, p.interaction))
	for _, pet := range partyPetsWithRide(p.aid, host, floor, p.ridePetID, p.rideState) {
		b = pbLen(b, 2, pet)
	}
	return b
}
