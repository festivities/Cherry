package main

import (
	"encoding/binary"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	aid  uint64
	conn *lockedConn
	b1   byte
	sid  [8]byte
	move []byte // last _player_move, guarded by partyHub.mu
	nmov int    // cr_player_move count, guarded by partyHub.mu
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

// rosterLocked lists members host first; caller holds h.mu.
func (r *partyRoom) rosterLocked() []partySnap {
	var out []partySnap
	for _, o := range r.members {
		if o.aid == r.host {
			out = append(out, partySnap{o.aid, o.move})
		}
	}
	for _, o := range r.members {
		if o.aid != r.host {
			out = append(out, partySnap{o.aid, o.move})
		}
	}
	return out
}

// leave removes m (if still registered) and sends deluser to the others.
func (h *partyHub) leave(logger *log.Logger, r *partyRoom, m *partyMember) {
	h.mu.Lock()
	i := r.index(m)
	if i >= 0 {
		r.members = append(r.members[:i], r.members[i+1:]...)
	}
	rest := r.others(nil)
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
			o.send(logger, 31, floorDel)
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
		b = pbVar(b, 5, 1) // ExtendFloors
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
	return s.reply(frame, 5, partyEnterBody(result, host))
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
		s.logger.Printf("ROOMPARTY login aid=%d result=0", aid)
		return s.reply(frame, 0, []byte{0x08, 0x00})
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
	h.mu.Lock()
	r := h.room(s.host)
	delete(r.invited, s.aid) // invite consumed; a later leave must not show "Waiting"
	m := s.pm
	m.move = append([]byte(nil), move...)
	idx := -1
	for i, o := range r.members {
		if o.aid == m.aid {
			idx = i
		}
	}
	if idx >= 0 { // silent rejoin: take over the entry (and its position if none sent)
		if len(m.move) == 0 {
			m.move = r.members[idx].move
		}
		r.members[idx] = m
	} else {
		r.members = append(r.members, m)
	}
	roster := r.rosterLocked()
	hostSession := h.sessions[r.host]
	hostIn := r.has(r.host) && r.host != m.aid
	others := r.others(m)
	h.mu.Unlock()
	s.joined = r
	s.logger.Printf("ROOMPARTY relaystart aid=%d host=%d roster=%d rejoined=%v", s.aid, r.host, len(roster), idx >= 0)
	res := pbVar(nil, 1, 0)
	for _, p := range roster {
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
	floorRes := pbVar(nil, 1, 0)
	for _, p := range roster {
		if p.aid != m.aid {
			m.send(s.logger, 1, pbLen(nil, 1, partyPlayerInfo(p.aid, p.move)))
		}
		floorRes = pbLen(floorRes, 2, partyPlayerInfo(p.aid, p.move))
	}
	m.send(s.logger, 29, floorRes)
	if idx >= 0 {
		return nil
	}
	add := pbLen(nil, 1, partyPlayerInfo(m.aid, m.move))
	for _, o := range others {
		o.send(s.logger, 1, add)
		o.send(s.logger, 30, add)
	}
	if r.host != m.aid && !hostIn && hostSession != nil {
		s.logger.Printf("ROOMPARTY friend-entered aid=%d host=%d", s.aid, r.host)
		hostSession.send(s.logger, 10, pbVar(nil, 1, m.aid))
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
		others := r.others(s.pm)
		h.mu.Unlock()
		if n <= 20 || n%50 == 0 {
			s.logger.Printf("ROOMPARTY move aid=%d count=%d to=%d", s.aid, n, len(others))
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
		others := r.others(s.pm)
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
	var name, level string
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
			name = acc.name
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
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"roomInfo": map[string]any{
			"groundLevel": level,
			"tileSize":    12, // ponytail: unverified; hero spawn tile (8,4) must fit inside
			// The client's default floor (RUTI0005D = 121500193) has no tile_1.png in the
			// full-client dataset and the room renders blank; RUTI000EW (121500536) has one.
			"floor":     map[string]any{"seq": "1", "cd": roomFloorCode, "type": "floorTile"},
			"wall":      map[string]any{"seq": "0", "cd": roomWallCode, "type": "wallTile"}, // default RUWA0006D (121400229) lacks wall_l/r_1.png
			"floorItem": []any{}, "wallItem": []any{}, "item": []any{}, "memberAvatarIdList": []any{},
		},
		"ownMaxGroundLevel": "LEVEL_1", "availMaxGroundLevel": "LEVEL_1",
		"name": name, "vipRewardedList": []any{},
	}})
	writeJSON(w, http.StatusOK, string(body))
}
