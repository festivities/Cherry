package lpn

import (
	"encoding/binary"
	"log"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Circle room (CIRCLE LPN namespace, gateway agent 8, tunnel "1.1.112.0"; spec circle-spec.md sections 7-8 and
// appendices A-C in %TEMP%\opencode\cherry\circle-20261101). Inbound msgid = dispatch index, outbound msgid = GetIndex;
// the two numberings are separate. Who belongs to which circle and with which role is the economy book
// (store.CircleState); this file keeps who is in the room and where, and the chat log in memory.
const (
	circleAgent = 8

	circleLoginResult = 131073 // sc_login_res: the client then sends cs_room_enter_req; anything else is its error path
	circleEnterResult = 196609 // sc_room_enter_res: any other result is the client's error path (12702) [S]
	circleRelayResult = 196865 // sc_room_relaystart_res: any other result is an error (12704/12705) [S]
	circleRefused     = 1      // ponytail: one refusal result for every member and purchase refusal; the client's text is not traced [I]

	circleMaxChatLen = 512 // bytes, as Square's chat
	circleChatCap    = 200 // messages kept per circle
	circleChatPage   = 50  // messages in one old-chat reply
	circleSpawnTiles = 3   // tiles between spawn slots: one tile drew two avatars on top of each other (device 2026-11-01)
	circleSpawnRow   = 8   // ponytail: slots wrap after 8 (24 tiles) to stay near the spawn; a 9th avatar shares a slot
)

// Outbound (client to server) msgids, cs_* GetIndex (appendix B).
const (
	circleCsLogin             = 0
	circleCsRoomEnter         = 1
	circleCsRoomEnterResume   = 2
	circleCsRoomExit          = 3
	circleCsRelayStart        = 4
	circleCsPlayerMove        = 5
	circleCsPlayerActorStatus = 6
	circleCsPlayerAction      = 7
	circleCsPlayerInteraction = 8
	circleCsPlayerDoInter     = 9
	circleCsPlayerBeginInter  = 10
	circleCsPlayerEndInter    = 11
	circleCsChat              = 12
	circleCsNoticeReq         = 13
	circleCsReportChat        = 14
	circleCsOldChat           = 15
	circleCsNoticeClose       = 16
	circleCsPetAction         = 17
	circleCsPetInteraction    = 18
	circleCsPetMove           = 19
	circleCsMemberList        = 20
	circleCsAuthority         = 21
	circleCsFire              = 22
	circleCsMasterChange      = 23
	circleCsSkinInven         = 24
	circleCsSkinChange        = 25
	circleCsSkinShop          = 26
	circleCsSkinBuy           = 27
	circleCsUpgradeShop       = 28
	circleCsUpgradeBuy        = 29
	circleCsAchievements      = 30
	circleCsBuffCopy          = 31
	circleCsPointSync         = 32
	circleCsPointReward       = 33
	circleCsQuestList         = 34
	circleCsQuestReward       = 35
	circleCsButtonUpdate      = 36
	circleCsPetRide           = 37
)

// Inbound (server to client) msgids, dispatch index (appendix A). Only the ones this file sends are named.
const (
	circleRcLogin        = 0
	circleRcRoomEnter    = 1
	circleRcRelayStart   = 3
	circleRcRoomExit     = 4
	circleRcAddUser      = 5
	circleRcDelUser      = 6
	circleRcPlayerMove   = 8
	circleRcPlayerAction = 10
	circleRcChat         = 12
	circleRcOldChat      = 14
	circleRcNoticeClose  = 15
	circleRcMemberList   = 29
	circleRcAuthority    = 30
	circleRcFire         = 31
	circleRcMasterChange = 32
	circleRcMemberAdd    = 33
	circleRcMemberDelete = 34
	circleRcMemberChange = 35
	circleRcSkinInven    = 36
	circleRcSkinChange   = 37
	circleRcSkinShop     = 38
	circleRcSkinBuy      = 39
	circleRcUpgradeShop  = 40
	circleRcUpgradeBuy   = 41
	circleRcAchievements = 42
	circleRcBuffCopy     = 45
	circleRcPointRefresh = 48
	circleRcPointReward  = 49
	circleRcQuestList    = 52
	circleRcQuestReward  = 53
	circleRcPetRide      = 54
)

// circlePlayer is one avatar in a circle room.
type circlePlayer struct {
	aid     uint64
	conn    *lockedConn
	b1      byte
	sid     [8]byte
	avatar  []byte // AvatarInfo, encoded at relay start
	role    string // economy role (leader, vice, member); guarded by circleRoom.mu
	x, y, d uint64 // guarded by circleRoom.mu
}

// circleRoom is the room of one circle: its players in join order.
type circleRoom struct {
	mu      sync.Mutex
	players []*circlePlayer
}

// circleRooms holds one room per circle. A room is made on first use and kept.
type circleRooms struct {
	mu    sync.Mutex
	rooms map[int64]*circleRoom
}

var defaultCircleRooms = &circleRooms{}

// find returns the room of circle id, nil when nobody has entered it.
func (rs *circleRooms) find(id int64) *circleRoom {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.rooms[id]
}

// room returns the room of circle id, made on first use.
func (rs *circleRooms) room(id int64) *circleRoom {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.rooms == nil {
		rs.rooms = map[int64]*circleRoom{}
	}
	r := rs.rooms[id]
	if r == nil {
		r = &circleRoom{}
		rs.rooms[id] = r
	}
	return r
}

// presence returns the avatars in circle id's room now (Connect on the member list).
func (rs *circleRooms) presence(id int64) map[uint64]bool {
	out := map[uint64]bool{}
	if room := rs.find(id); room != nil {
		for _, p := range room.members(nil) {
			out[p.aid] = true
		}
	}
	return out
}

// members returns a snapshot of the players except skip (nil: everyone).
func (r *circleRoom) members(skip *circlePlayer) []*circlePlayer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*circlePlayer, 0, len(r.players))
	for _, p := range r.players {
		if p != skip {
			out = append(out, p)
		}
	}
	return out
}

// broadcast writes outside the room lock; each write is deadline-bounded by lockedConn.
func (r *circleRoom) broadcast(logger *log.Logger, skip *circlePlayer, msgid uint16, body []byte) {
	for _, p := range r.members(skip) {
		p.send(logger, msgid, body)
	}
}

func (p *circlePlayer) send(logger *log.Logger, msgid uint16, body []byte) {
	if _, err := p.conn.Write(squareFrame(p.b1, p.sid[:], msgid, body)); err != nil {
		logger.Printf("CIRCLE send aid=%d msgid=%d err=%v", p.aid, msgid, err)
	}
}

// info encodes PlayerInfo; the caller holds the room lock (position and role).
func (p *circlePlayer) info() []byte {
	b := pbVar(nil, 1, p.aid)
	b = pbLen(b, 2, p.avatar)
	b = pbLen(b, 3, squarePositionInfo(p.x, p.y, p.d))
	return pbVar(b, 7, economy.CirclePlayerType(p.role))
}

// join registers p and returns the roster fragment (p first, then the others). A player who is already in the room
// (the same aid entering again) takes over that entry and its position silently, as Square does.
func (r *circleRoom) join(p *circlePlayer, spawnX, spawnY uint64) (roster []byte, rejoined bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := -1
	for i, o := range r.players {
		if o.aid == p.aid {
			index = i
			p.x, p.y, p.d = o.x, o.y, o.d
			break
		}
	}
	if index < 0 {
		// The first spawn slot nobody stands on: counting players reused an occupied slot after someone left and came
		// back, and the two avatars stacked exactly (the bear vanished behind the other avatar, device 2026-11-01).
		for n := uint64(0); n <= uint64(len(r.players)); n++ {
			k := (n % circleSpawnRow) * circleSpawnTiles
			x, y := spawnX+k*squareSpawnStepX, spawnY-k*squareSpawnStepY
			taken := false
			for _, o := range r.players {
				if o.x == x && o.y == y {
					taken = true
					break
				}
			}
			if !taken || n == uint64(len(r.players)) {
				p.x, p.y = x, y
				break
			}
		}
	}
	roster = pbLen(nil, 2, p.info())
	for i, o := range r.players {
		if i != index {
			roster = pbLen(roster, 2, o.info())
		}
	}
	if index >= 0 {
		r.players[index] = p
	} else {
		r.players = append(r.players, p)
	}
	return roster, index >= 0
}

// leave removes p if still registered and tells the rest (deluser). Idempotent.
func (r *circleRoom) leave(logger *log.Logger, p *circlePlayer) {
	r.mu.Lock()
	found := false
	for i, o := range r.players {
		if o == p {
			r.players = slices.Delete(r.players, i, i+1)
			found = true
			break
		}
	}
	r.mu.Unlock()
	if found {
		logger.Printf("CIRCLE deluser aid=%d", p.aid)
		r.broadcast(logger, nil, circleRcDelUser, squareDelUser(p.aid))
	}
}

// remove takes the players with these aids out of the room (no message) and returns them.
func (r *circleRoom) remove(aids []uint64) []*circlePlayer {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*circlePlayer
	r.players = slices.DeleteFunc(r.players, func(p *circlePlayer) bool {
		if slices.Contains(aids, p.aid) {
			out = append(out, p)
			return true
		}
		return false
	})
	return out
}

// setRoles copies the members' new roles into the players in the room.
func (r *circleRoom) setRoles(members []store.CircleMember) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range members {
		aid := aidNum(m.Aid)
		for _, p := range r.players {
			if p.aid == aid {
				p.role = m.Role
			}
		}
	}
}

// circleChatMsg is one line of a circle's chat, kept in memory (like Lounge Chat).
type circleChatMsg struct {
	serial   int64
	sendAid  uint64
	sendName string
	kind     uint64
	text     []byte
	ms       int64
}

// circleChats keeps the last circleChatCap messages of each circle.
type circleChats struct {
	mu   sync.Mutex
	logs map[int64][]circleChatMsg
	last map[int64]int64 // the last serial given in each circle
}

var defaultCircleChats = &circleChats{}

// add appends m to circle id's log and returns its serial (1, 2, ... per circle).
func (cc *circleChats) add(id int64, m circleChatMsg) int64 {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.logs == nil {
		cc.logs = map[int64][]circleChatMsg{}
		cc.last = map[int64]int64{}
	}
	cc.last[id]++
	m.serial = cc.last[id]
	msgs := append(cc.logs[id], m)
	if len(msgs) > circleChatCap {
		msgs = slices.Clone(msgs[len(msgs)-circleChatCap:])
	}
	cc.logs[id] = msgs
	return m.serial
}

// before returns up to n messages of circle id older than serial (serial 0: the newest), oldest first.
func (cc *circleChats) before(id, serial int64, n int) []circleChatMsg {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	msgs := cc.logs[id]
	end := len(msgs)
	if serial > 0 {
		end = 0
		for end < len(msgs) && msgs[end].serial < serial {
			end++
		}
	}
	return slices.Clone(msgs[max(0, end-n):end])
}

// circleSession is the per-connection agent-8 state, used from the single handler goroutine of that connection.
type circleSession struct {
	rooms    *circleRooms
	chats    *circleChats
	conn     *lockedConn
	logger   *log.Logger
	aid      uint64
	loggedIn bool
	circleID int64       // the circle entered last (0 before the first enter)
	entered  bool        // cs_room_enter_req was accepted and no exit or drop followed
	room     *circleRoom // the room of the player, nil outside it
	player   *circlePlayer
	moves    int
}

func (s *circleSession) aidText() string { return strconv.FormatUint(s.aid, 10) }

// handleOpen answers an agent-8 tunnel open with the same type-1 control Square sends (the echo of the frame's type
// and session id). [I] exact bytes, as Square's.
func (s *circleSession) handleOpen(frame []byte) error {
	control := make([]byte, 22)
	binary.BigEndian.PutUint32(control, 18)
	control[4], control[5] = 'G', frame[1]
	copy(control[6:14], frame[2:10])
	_, err := s.conn.Write(control)
	s.logger.Printf("CIRCLE open-control err=%v", err)
	return err
}

func (s *circleSession) reply(frame []byte, msgid uint16, body []byte) error {
	_, err := s.conn.Write(squareFrame(frame[1], frame[2:10], msgid, body))
	return err
}

// close ends the room presence of this connection (exit, EOF or error). Idempotent.
func (s *circleSession) close() {
	if s.player != nil {
		s.logger.Printf("CIRCLE leave aid=%d moves=%d", s.aid, s.moves)
		s.room.leave(s.logger, s.player)
	}
	s.player, s.room, s.entered = nil, nil, false
}

// myCircle is the circle this session's avatar is in now, from the book (a member acts from the circle's screens too,
// not only from the room).
func (s *circleSession) myCircle() (int64, bool) {
	id, _, ok := economy.CircleOf(s.aidText())
	return id, ok
}

// isMember reports whether this session's avatar is still in the circle it entered.
func (s *circleSession) isMember() bool {
	id, _, ok := economy.CircleOf(s.aidText())
	return ok && id == s.circleID
}

// inRoom reports whether this session is in its room and still a member. A player who was removed or who left over
// HTTP is dropped from the room here; nothing is pushed to the client (the pushes come only from LPN requests).
func (s *circleSession) inRoom() bool {
	if s.player == nil {
		return false
	}
	if s.isMember() {
		return true
	}
	s.logger.Printf("CIRCLE drop aid=%d circle=%d: no longer a member", s.aid, s.circleID)
	s.close()
	return false
}

// handleData processes one agent-8 data frame (frame[10:12] msgid, frame[12:] protobuf). A non-nil error means a reply
// write failed and the connection should end.
func (s *circleSession) handleData(frame []byte) error {
	msgid := binary.BigEndian.Uint16(frame[10:12])
	data := frame[12:]
	if msgid == circleCsLogin {
		return s.login(frame, data)
	}
	if !s.loggedIn {
		s.logger.Printf("CIRCLE msgid=%d before login ignored", msgid)
		return nil
	}
	switch msgid {
	case circleCsRoomEnter, circleCsRoomEnterResume:
		return s.enter(frame)
	case circleCsRoomExit:
		return s.exit(frame)
	case circleCsRelayStart:
		return s.relayStart(frame)
	case circleCsPlayerMove:
		s.move(data)
	case circleCsPlayerAction:
		s.action(data)
	case circleCsChat:
		s.chat(data)
	case circleCsOldChat:
		return s.oldChat(frame, data)
	case circleCsMemberList:
		return s.memberList(frame)
	case circleCsAuthority:
		return s.authority(frame, data)
	case circleCsFire:
		return s.fire(frame, data)
	case circleCsMasterChange:
		return s.masterChange(frame, data)
	case circleCsUpgradeShop:
		return s.upgradeShop(frame)
	case circleCsUpgradeBuy:
		return s.upgradeBuy(frame, data)
	case circleCsPointSync:
		return s.pointSync(frame)
	case circleCsPointReward:
		return s.pointReward(frame, data)
	case circleCsQuestList:
		return s.questList(frame)
	case circleCsQuestReward:
		return s.questReward(frame)
	default:
		return s.stub(frame, msgid, data)
	}
	return nil
}

// login answers cs_login_req (msgid 0). The request must carry a session key and a positive aid (Square's check) and
// the aid must have an account; anything else gets result 0, which is the client's error path (12698), not a spinner.
func (s *circleSession) login(frame, data []byte) error {
	aid, _ := protobufUintField(data, 2)
	if !gardenLoginValid(data) || aid == 0 || aid > (1<<63)-1 {
		s.logger.Printf("CIRCLE login invalid request")
		return s.reply(frame, circleRcLogin, pbVar(nil, 1, 0))
	}
	if _, ok := store.AccountByAvatarID(strconv.FormatUint(aid, 10)); !ok {
		s.logger.Printf("CIRCLE login unknown aid=%d", aid)
		return s.reply(frame, circleRcLogin, pbVar(nil, 1, 0))
	}
	s.aid, s.loggedIn = aid, true
	s.logger.Printf("CIRCLE login aid=%d result=%d", aid, circleLoginResult)
	return s.reply(frame, circleRcLogin, pbVar(nil, 1, circleLoginResult))
}

// enter answers cs_room_enter_req (msgid 1) with sc_room_enter_res: the circle room's map, the caller's role and the
// members. Only members enter; anyone else gets result 0 (the client's error path).
func (s *circleSession) enter(frame []byte) error {
	id, role, ok := economy.CircleOf(s.aidText())
	if !ok {
		s.logger.Printf("CIRCLE enter aid=%d refused: not a member", s.aid)
		return s.reply(frame, circleRcRoomEnter, pbVar(nil, 1, 0))
	}
	if s.player != nil && id != s.circleID {
		s.close() // another circle's room
	}
	members, _, ok := economy.CircleMembersOf(id)
	if !ok {
		return s.reply(frame, circleRcRoomEnter, pbVar(nil, 1, 0))
	}
	s.circleID, s.entered = id, true
	online := s.rooms.presence(id)
	mapData := economy.ActiveCircleData().Room
	body := pbVar(nil, 1, circleEnterResult)
	body = pbVar(body, 3, 1) // clientMapRevision
	body = pbLen(body, 4, []byte(mapData.Map))
	body = pbVar(body, 5, economy.CirclePlayerType(role))
	for _, m := range members {
		body = pbLen(body, 6, circleMemberInfo(m, online, true))
	}
	s.logger.Printf("CIRCLE enter aid=%d circle=%d members=%d map=%s", s.aid, id, len(members), mapData.Map)
	return s.reply(frame, circleRcRoomEnter, body)
}

// exit answers cs_room_exit_req (msgid 3) with an empty sc_room_exit_res and leaves the room.
//
// The reply alone leaves the client in the room: LP_sc_room_exit_res 0x24b681c only posts 12710 (map teardown), and
// the scene returns to the Garden only on LPN event 13, an inbound agent-8 control of subtype 5 with the same session id
// (CircleService::UpdatePacket 0x2514610 → 12693 → change(25,10,918) → changeToMyGarden; exit-invite.md section A). So
// the tunnel close follows the reply. The client re-opens the agent (auto-login flag, 0x25141ac) on the next entry.
func (s *circleSession) exit(frame []byte) error {
	err := s.reply(frame, circleRcRoomExit, nil)
	s.logger.Printf("CIRCLE exit aid=%d", s.aid)
	s.close()
	if err != nil {
		return err
	}
	closeCtl := make([]byte, 22)
	binary.BigEndian.PutUint32(closeCtl, 18)
	closeCtl[4], closeCtl[5] = 'G', frame[1]&0xf8|5
	copy(closeCtl[6:14], frame[2:10])
	_, err = s.conn.Write(closeCtl)
	s.loggedIn = false
	s.logger.Printf("CIRCLE close-control err=%v", err)
	return err
}

// relayStart answers cs_room_relaystart_req (msgid 4): the roster (this avatar first, then the others) and, for the
// others, adduser. The client's roster loop adds every entry, this avatar included, as Square's does.
func (s *circleSession) relayStart(frame []byte) error {
	if !s.entered {
		s.logger.Printf("CIRCLE relaystart before enter ignored")
		return nil
	}
	if !s.isMember() {
		s.logger.Printf("CIRCLE relaystart aid=%d refused: not a member", s.aid)
		s.entered = false
		return nil
	}
	_, role, _ := economy.CircleOf(s.aidText())
	room := s.rooms.room(s.circleID)
	p := &circlePlayer{aid: s.aid, conn: s.conn, b1: frame[1], avatar: circleAvatarInfo(s.aid), role: role}
	copy(p.sid[:], frame[2:10])
	mapData := economy.ActiveCircleData().Room
	spawnX, spawnY := tilePx(mapData.MapSize, Tile{X: mapData.Spawn.X, Y: mapData.Spawn.Y})
	roster, rejoined := room.join(p, spawnX, spawnY)
	s.room, s.player = room, p
	s.logger.Printf("CIRCLE relaystart aid=%d circle=%d rejoined=%v", s.aid, s.circleID, rejoined)
	if err := s.reply(frame, circleRcRelayStart, append(pbVar(nil, 1, circleRelayResult), roster...)); err != nil {
		return err
	}
	if rejoined {
		return nil
	}
	room.mu.Lock()
	self := p.info()
	room.mu.Unlock()
	room.broadcast(s.logger, p, circleRcAddUser, pbLen(nil, 1, self))
	return nil
}

// move relays cs_player_move_req (msgid 5) as sc_player_move_all (8) to the others.
func (s *circleSession) move(data []byte) {
	if s.player == nil || !s.inRoom() {
		s.logger.Printf("CIRCLE move outside room ignored")
		return
	}
	move, found, ok := pbBytesField(data, 1)
	var tx, ty, dir uint64
	if ok && found {
		ok = pbScan(move, func(f, v uint64, _ []byte) {
			switch f {
			case 3:
				tx = v
			case 4:
				ty = v
			case 5:
				dir = v
			}
		})
	}
	if !ok || !found {
		s.logger.Printf("CIRCLE move invalid")
		return
	}
	s.room.mu.Lock()
	s.player.x, s.player.y, s.player.d = tx, ty, dir
	s.room.mu.Unlock()
	s.moves++
	s.room.broadcast(s.logger, s.player, circleRcPlayerMove, pbLen(pbVar(nil, 1, s.aid), 2, move))
}

// action relays cs_player_action_req (msgid 7) as sc_player_action_all (10) to the others.
func (s *circleSession) action(data []byte) {
	if s.player == nil || !s.inRoom() {
		s.logger.Printf("CIRCLE action outside room ignored")
		return
	}
	action, found, ok := pbBytesField(data, 1)
	if !ok || !found {
		s.logger.Printf("CIRCLE action invalid")
		return
	}
	s.room.broadcast(s.logger, s.player, circleRcPlayerAction, pbLen(pbVar(nil, 1, s.aid), 2, action))
}

// chat stores cs_nomal_chat (msgid 12) and sends it to everyone in the room, the sender included (as Square does).
func (s *circleSession) chat(data []byte) {
	if s.player == nil || !s.inRoom() {
		s.logger.Printf("CIRCLE chat outside room ignored")
		return
	}
	msg, found, ok := pbBytesField(data, 1)
	if !ok || !found || len(msg) == 0 || len(msg) > circleMaxChatLen || !utf8.Valid(msg) {
		s.logger.Printf("CIRCLE chat rejected")
		return
	}
	var kind uint64
	pbScan(data, func(f, v uint64, _ []byte) {
		if f == 2 {
			kind = v
		}
	})
	m := circleChatMsg{sendAid: s.aid, sendName: circleAccountName(s.aid), kind: kind, text: slices.Clone(msg), ms: time.Now().UnixMilli()}
	m.serial = s.chats.add(s.circleID, m)
	s.logger.Printf("CIRCLE chat aid=%d serial=%d bytes=%d", s.aid, m.serial, len(msg))
	s.room.broadcast(s.logger, nil, circleRcChat, pbLen(nil, 1, circleChatInfo(m)))
	economy.CirclePointEarn(s.aidText(), "chat", economy.NowFunc()) // a circle chat message counts toward points and missions
}

// oldChat answers cs_old_chat_req (msgid 15) with up to circleChatPage messages older than the given serial, oldest
// first. [I] the order and the paging rule.
func (s *circleSession) oldChat(frame, data []byte) error {
	if !s.isMember() {
		return s.reply(frame, circleRcOldChat, pbVar(nil, 1, circleRefused))
	}
	serial, _ := protobufUintField(data, 1)
	body := pbVar(nil, 1, 0)
	for _, m := range s.chats.before(s.circleID, int64(serial), circleChatPage) {
		body = pbLen(body, 2, circleChatInfo(m))
	}
	return s.reply(frame, circleRcOldChat, body)
}

// memberList answers cs_circle_member_list_req (msgid 20) with sc_circle_member_list_res.
func (s *circleSession) memberList(frame []byte) error {
	id, _, ok := economy.CircleOf(s.aidText())
	if !ok {
		return s.reply(frame, circleRcMemberList, pbVar(nil, 1, circleRefused))
	}
	members, master, ok := economy.CircleMembersOf(id)
	if !ok {
		return s.reply(frame, circleRcMemberList, pbVar(nil, 1, circleRefused))
	}
	online := s.rooms.presence(id)
	body := pbVar(nil, 1, 0)
	for _, m := range members {
		body = pbLen(body, 2, circleMemberInfo(m, online, true))
	}
	body = pbVar(body, 3, aidNum(master))
	body = pbVar(body, 4, uint64(len(members)))
	body = pbVar(body, 5, uint64(economy.CircleCapOf(id)))
	return s.reply(frame, circleRcMemberList, body)
}

// authority answers cs_authority_change_req (msgid 21): ChangeAuth 1 makes the aids vice-leaders, 0 plain members.
// The room sees the change as sc_circle_memeber_change_push (35).
func (s *circleSession) authority(frame, data []byte) error {
	id, ok := s.myCircle()
	if !ok {
		return s.reply(frame, circleRcAuthority, pbVar(nil, 1, circleRefused))
	}
	change, _ := protobufUintField(data, 2)
	changed, err := economy.CircleChangeAuth(s.aidText(), id, circleAidList(data), change == 1)
	if err != nil {
		s.logger.Printf("CIRCLE authority aid=%d refused: %v", s.aid, err)
		return s.reply(frame, circleRcAuthority, pbVar(nil, 1, circleRefused))
	}
	s.logger.Printf("CIRCLE authority aid=%d circle=%d vice=%v changed=%d", s.aid, id, change == 1, len(changed))
	body := pbVar(nil, 1, 0)
	online := s.rooms.presence(id)
	for _, m := range changed {
		body = pbLen(body, 2, circleMemberInfo(m, online, true))
	}
	if err := s.reply(frame, circleRcAuthority, body); err != nil {
		return err
	}
	pushCircleChanges(s.logger, s.rooms, id, s.player, changed)
	return nil
}

// masterChange answers cs_master_change_req (msgid 23): the leadership goes to the aid, the old leader becomes a
// plain member.
func (s *circleSession) masterChange(frame, data []byte) error {
	id, ok := s.myCircle()
	if !ok {
		return s.reply(frame, circleRcMasterChange, pbVar(nil, 1, circleRefused))
	}
	target, _ := protobufUintField(data, 1)
	changed, err := economy.CircleTransferMaster(s.aidText(), id, strconv.FormatUint(target, 10))
	if err != nil {
		s.logger.Printf("CIRCLE master change aid=%d refused: %v", s.aid, err)
		return s.reply(frame, circleRcMasterChange, pbVar(nil, 1, circleRefused))
	}
	s.logger.Printf("CIRCLE master change aid=%d circle=%d to=%d", s.aid, id, target)
	body := pbVar(nil, 1, 0)
	online := s.rooms.presence(id)
	for _, m := range changed {
		body = pbLen(body, 2, circleMemberInfo(m, online, true))
	}
	if err := s.reply(frame, circleRcMasterChange, body); err != nil {
		return err
	}
	pushCircleChanges(s.logger, s.rooms, id, nil, changed) // the actor too: its MasterAid changed, and the push is idempotent
	return nil
}

// fire answers cs_member_fire_req (msgid 22): sc_member_fire_res lists the removed aids (field 3). Everyone in the room,
// the removed avatars included, gets sc_circle_memeber_delete_push (34); then the removed avatars leave the room and the
// others get deluser; the member count goes out as sc_circle_memeber_change_push (35).
func (s *circleSession) fire(frame, data []byte) error {
	id, ok := s.myCircle()
	if !ok {
		return s.reply(frame, circleRcFire, pbVar(nil, 1, circleRefused))
	}
	removed, err := economy.CircleFire(s.aidText(), id, circleAidList(data))
	if err != nil {
		s.logger.Printf("CIRCLE fire aid=%d refused: %v", s.aid, err)
		return s.reply(frame, circleRcFire, pbVar(nil, 1, circleRefused))
	}
	body := pbVar(nil, 1, 0)
	gone := make([]uint64, 0, len(removed))
	for _, a := range removed {
		n := aidNum(a)
		gone = append(gone, n)
		body = pbVar(body, 3, n)
	}
	s.logger.Printf("CIRCLE fire aid=%d circle=%d removed=%d", s.aid, id, len(gone))
	if err := s.reply(frame, circleRcFire, body); err != nil {
		return err
	}
	if len(gone) == 0 {
		return nil
	}
	if room := s.rooms.find(id); room != nil {
		var list []byte
		for _, n := range gone {
			list = pbVar(list, 1, n)
		}
		room.broadcast(s.logger, nil, circleRcMemberDelete, list)
		for _, p := range room.remove(gone) {
			s.logger.Printf("CIRCLE removed aid=%d from the room", p.aid)
			room.broadcast(s.logger, nil, circleRcDelUser, squareDelUser(p.aid))
		}
	}
	pushCircleChanges(s.logger, s.rooms, id, nil, nil) // the leader's count refreshes too: its reply carries no count
	return nil
}

// upgradeShop answers cs_circle_upgrade_shop_info_req (msgid 28) with sc_circle_upgrade_shop_info_res (40): Result and
// the next cap step as one SapiUpgradeShopInfo (the client reads a single message, not a list). A player in no circle is
// refused.
func (s *circleSession) upgradeShop(frame []byte) error {
	row, err := economy.CircleUpgradeInfo(s.aidText())
	if err != nil {
		return s.reply(frame, circleRcUpgradeShop, pbVar(nil, 1, circleRefused))
	}
	return s.reply(frame, circleRcUpgradeShop, pbLen(pbVar(nil, 1, 0), 2, circleUpgradeShopBytes(row)))
}

// upgradeBuy answers cs_circle_upgrade_buy_req (msgid 29: ItemCode, CoinUnit, Price, Cpid) with sc_circle_upgrade_buy_res
// (41): Result, the Gem balance after the purchase and its unit. A refusal carries only the result. A foreign cpId is
// refused by economy.BuyCircleUpgrade.
func (s *circleSession) upgradeBuy(frame, data []byte) error {
	code, _, _ := pbBytesField(data, 1)
	unit, _, _ := pbBytesField(data, 2)
	price, _ := protobufUintField(data, 3)
	cpID, found, _ := pbBytesField(data, 4)
	if found && len(cpID) > 0 && string(cpID) != economy.CircleCpID {
		s.logger.Printf("CIRCLE upgrade buy foreign cpId refused")
	}
	balance, err := economy.BuyCircleUpgrade(s.aidText(), string(code), string(unit), string(cpID), int64(price), economy.NowFunc())
	if err != nil {
		s.logger.Printf("CIRCLE upgrade buy aid=%d refused: %v", s.aid, err)
		return s.reply(frame, circleRcUpgradeBuy, pbVar(nil, 1, circleRefused))
	}
	s.logger.Printf("CIRCLE upgrade buy aid=%d code=%s gems balance=%d", s.aid, code, balance)
	if id, _, ok := economy.CircleOf(s.aidText()); ok {
		pushCircleChanges(s.logger, s.rooms, id, nil, nil) // the room's count and cap follow the new cap
	}
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, uint64(balance))
	return s.reply(frame, circleRcUpgradeBuy, pbLen(body, 3, []byte("GEM")))
}

// pointSync answers cs_circle_point_sync_req (msgid 32) with sc_circle_point_refresh_push (48): the circle's point state.
// The client's sync reply (47) posts nothing, so the push is the answer.
func (s *circleSession) pointSync(frame []byte) error {
	info := economy.CirclePointInfoOf(s.aidText(), economy.NowFunc())
	return s.reply(frame, circleRcPointRefresh, pbLen(nil, 1, circlePointInfoBytes(info)))
}

// pointReward answers cs_point_reward_req (msgid 33, RewardSeq) with sc_point_reward_res (49): Result 0, the step's Gems
// as RewardInfo and the point state. A refusal carries only the result.
func (s *circleSession) pointReward(frame, data []byte) error {
	seq, _ := protobufUintField(data, 1)
	gems, err := economy.CirclePointClaim(s.aidText(), int64(seq), economy.NowFunc())
	if err != nil {
		s.logger.Printf("CIRCLE point reward aid=%d seq=%d refused: %v", s.aid, seq, err)
		return s.reply(frame, circleRcPointReward, pbVar(nil, 1, circleRefused))
	}
	s.logger.Printf("CIRCLE point reward aid=%d seq=%d gems=%d", s.aid, seq, gems)
	info := economy.CirclePointInfoOf(s.aidText(), economy.NowFunc())
	body := pbLen(pbVar(nil, 1, 0), 2, circleGemRewardInfo(gems))
	return s.reply(frame, circleRcPointReward, pbLen(body, 3, circlePointInfoBytes(info)))
}

// questList answers cs_quest_list_req (msgid 34) with sc_quest_list_res (52): the circle's missions for the season.
// A player in no circle is refused.
func (s *circleSession) questList(frame []byte) error {
	rows, ok := economy.CircleMissionRows(s.aidText(), economy.NowFunc())
	if !ok {
		return s.reply(frame, circleRcQuestList, pbVar(nil, 1, circleRefused))
	}
	body := pbVar(nil, 1, 0)
	for _, q := range rows {
		body = pbLen(body, 2, circleQuestBytes(q))
	}
	return s.reply(frame, circleRcQuestList, body)
}

// questReward answers cs_quest_reward_req (msgid 35, no fields) with sc_quest_reward_res (53): the current mission's
// reward is paid when its goals are met. The request names no mission, so the server claims the current one.
func (s *circleSession) questReward(frame []byte) error {
	gems, points, err := economy.CircleMissionClaim(s.aidText(), economy.NowFunc())
	if err != nil {
		s.logger.Printf("CIRCLE mission claim aid=%d refused: %v", s.aid, err)
		return s.reply(frame, circleRcQuestReward, pbVar(nil, 1, circleRefused))
	}
	s.logger.Printf("CIRCLE mission claim aid=%d gems=%d points=%d", s.aid, gems, points)
	return s.reply(frame, circleRcQuestReward, pbVar(nil, 1, 0))
}

// circlePointInfoBytes encodes PointInfo (appendix C): 1 RewardSeq, 2 LastRewardSeq, 3 MaxRewardSeq, 4 LastCirclePoint,
// 5 CurrentCirclePoint, 6 CurrentMaxCirclePoint, 7 BeforeMaxCirclePoint, 8 SessionState, 9 AvailSessionAttendYn.
func circlePointInfoBytes(v economy.CirclePointInfoView) []byte {
	b := pbVar(nil, 1, uint64(v.RewardSeq))
	b = pbVar(b, 2, uint64(v.LastRewardSeq))
	b = pbVar(b, 3, uint64(v.MaxRewardSeq))
	b = pbVar(b, 4, uint64(v.LastCirclePoint))
	b = pbVar(b, 5, uint64(v.CurrentCirclePoint))
	b = pbVar(b, 6, uint64(v.CurrentMaxCirclePoint))
	b = pbVar(b, 7, uint64(v.BeforeMaxCirclePoint))
	b = pbVar(b, 8, uint64(v.SessionState))
	return pbVar(b, 9, uint64(v.AvailSessionAttendYn))
}

// circleGemRewardInfo encodes RewardInfo for a Gem step: 1 RewardType, 2 ItemList (one RewardItemInfo: ViewMessage
// names the amount, ViewCount is the amount, no item code), 3 TopMessage. The texts are Cherry's [I]: the client's
// reward popup text for point steps was not traced.
func circleGemRewardInfo(gems int64) []byte {
	item := pbVar(nil, 1, 0)
	item = pbLen(item, 2, []byte("+"+strconv.FormatInt(gems, 10)+" Gems"))
	item = pbVar(item, 4, uint64(gems))
	b := pbVar(nil, 1, 1)
	b = pbLen(b, 2, item)
	return pbLen(b, 3, []byte("Circle reward"))
}

// circleUpgradeShopBytes encodes SapiUpgradeShopInfo: 1 ProductCode, 2 ProductName, 3 ProductImagePath, 4 MaxMemberCount
// (the current cap), 5 UpgradeMaxMemberCount (the cap after the step), 6 FinalMaxMemberCount, 7 PaymentType (GEM),
// 8 OriginPrice, 9 DiscountPercentage (0), 12 FinalPrice, 14 Purchasable. The billing id (13) is never sent.
func circleUpgradeShopBytes(row economy.CircleUpgradeRow) []byte {
	b := pbLen(nil, 1, []byte(row.Code))
	b = pbLen(b, 2, []byte(row.Name))
	b = pbLen(b, 3, []byte(row.Image))
	b = pbVar(b, 4, uint64(row.Current))
	b = pbVar(b, 5, uint64(row.To))
	b = pbVar(b, 6, uint64(row.Final))
	b = pbLen(b, 7, []byte("GEM"))
	b = pbVar(b, 8, uint64(row.PriceGems))
	b = pbVar(b, 9, 0)
	b = pbVar(b, 12, uint64(row.PriceGems))
	b = pbLen(b, 13, []byte(economy.CircleCpID)) // CpId: never empty (the create row's empty cpId failed CheckCpid)
	return pbVar(b, 14, boolUint(row.Purchasable))
}

// circleQuestBytes encodes one mission as QuestInfo: 1 Startdate, 2 Enddate (ms), 3 Subquest (one SubQuestInfo per goal:
// 1 Complete = the member's count, 2 Index, 3 Type, 4 TargetValue = the goal count, 6 Msg, 8 Status), 4 Reward (a Gem
// row and, when the mission pays points, a points row), 5 RewardState. [I] the meanings of Complete and Status and the
// reward rows' rendering are not traced.
func circleQuestBytes(q economy.CircleQuestRow) []byte {
	b := pbVar(nil, 1, uint64(q.StartMs))
	b = pbVar(b, 2, uint64(q.EndMs))
	for i, g := range q.Goals {
		sub := pbVar(nil, 1, uint64(g.Progress))
		sub = pbVar(sub, 2, uint64(i))
		sub = pbVar(sub, 3, 0)
		sub = pbLen(sub, 4, []byte(strconv.FormatInt(g.Count, 10)))
		sub = pbLen(sub, 6, []byte(g.Text))
		sub = pbVar(sub, 8, boolUint(g.Done))
		b = pbLen(b, 3, sub)
	}
	b = pbLen(b, 4, circleQuestReward("+"+strconv.FormatInt(q.Gems, 10)+" Gems", q.Gems))
	if q.Points > 0 {
		b = pbLen(b, 4, circleQuestReward("+"+strconv.FormatInt(q.Points, 10)+" Circle points", q.Points))
	}
	return pbVar(b, 5, uint64(q.RewardState))
}

// circleQuestReward encodes a QuestReward row: 6 ItemAmount, 3 ItemCode (empty), 4 ItemName (the text the row shows).
func circleQuestReward(name string, amount int64) []byte {
	return pbVar(pbLen(nil, 4, []byte(name)), 6, uint64(amount))
}

// boolUint is 1 for true and 0 for false, the protobuf varint of a bool.
func boolUint(v bool) uint64 {
	if v {
		return 1
	}
	return 0
}

// pushCircleEvent pushes a membership change made over HTTP into the room of ev.Circle, as the LPN requests push theirs:
// sc_circle_memeber_add_push (33) for each joiner, sc_circle_memeber_delete_push (34) for the leavers (the removed ones
// are taken out of the room, and the rest get deluser), and the member count (35). A closed circle has no members left
// to count. Nothing happens for a circle nobody has entered.
func pushCircleEvent(rooms *circleRooms, logger *log.Logger, ev economy.CircleRoomEvent) {
	room := rooms.find(ev.Circle)
	if room == nil {
		return
	}
	if len(ev.Left) > 0 {
		var gone []uint64
		var list []byte
		for _, a := range ev.Left {
			n := aidNum(a)
			gone = append(gone, n)
			list = pbVar(list, 1, n)
		}
		room.broadcast(logger, nil, circleRcMemberDelete, list)
		for _, p := range room.remove(gone) {
			logger.Printf("CIRCLE removed aid=%d from the room (HTTP)", p.aid)
			room.broadcast(logger, nil, circleRcDelUser, squareDelUser(p.aid))
		}
	}
	if members, _, ok := economy.CircleMembersOf(ev.Circle); ok && len(ev.Joined) > 0 {
		online := rooms.presence(ev.Circle)
		for _, m := range members {
			if slices.Contains(ev.Joined, m.Aid) {
				room.broadcast(logger, nil, circleRcMemberAdd, pbLen(nil, 1, circleMemberInfo(m, online, false)))
			}
		}
	}
	pushCircleChanges(logger, rooms, ev.Circle, nil, nil)
}

func init() {
	economy.CircleRoomHook = func(ev economy.CircleRoomEvent) { pushCircleEvent(defaultCircleRooms, log.Default(), ev) }
}

// stub answers the requests whose features are not built yet (skins, achievements, buffs, pet rides, the button
// update) with the empty or refused shape of their reply, so no screen waits forever. Requests with no reply in the
// client's table are dropped. Nothing here spends money: every purchase is refused, and a real-money id (Cpid) is
// refused with no other check.
func (s *circleSession) stub(frame []byte, msgid uint16, data []byte) error {
	switch msgid {
	case circleCsSkinInven: // empty inventory
		return s.reply(frame, circleRcSkinInven, pbVar(nil, 1, 0))
	case circleCsSkinShop: // empty shop (no skins are made)
		return s.reply(frame, circleRcSkinShop, pbVar(nil, 1, 0))
	case circleCsSkinChange:
		return s.reply(frame, circleRcSkinChange, pbVar(nil, 1, circleRefused))
	case circleCsSkinBuy: // no skins are made, so every purchase is refused (a Cpid is refused with no other check)
		if _, found, _ := pbBytesField(data, 4); found {
			s.logger.Printf("CIRCLE msgid=%d real-money id refused", msgid)
		}
		return s.reply(frame, circleRcSkinBuy, pbVar(nil, 1, circleRefused))
	case circleCsAchievements: // no achievements
		// AchievementList (f2) is sent as an EMPTY message, never omitted: the bell's list (popup 172) received the reply
		// without it and the client died with SIGSEGV at address 0 on both device taps (2026-11-01), the pattern of a
		// missing sub-message read through its pointer. LP_sc_circle_achievements_res 0x24b7f4c hands the message on as is.
		return s.reply(frame, circleRcAchievements, pbLen(pbVar(nil, 1, 0), 2, nil))
	case circleCsBuffCopy:
		target, _ := protobufUintField(data, 1)
		return s.reply(frame, circleRcBuffCopy, pbVar(pbVar(pbVar(nil, 1, circleRefused), 2, s.aid), 3, target))
	case circleCsPetRide:
		return s.reply(frame, circleRcPetRide, pbVar(nil, 1, circleRefused))
	case circleCsNoticeClose:
		return s.reply(frame, circleRcNoticeClose, pbVar(nil, 1, 0))
	case circleCsButtonUpdate, circleCsPlayerActorStatus, circleCsPlayerInteraction,
		circleCsPlayerDoInter, circleCsPlayerBeginInter, circleCsPlayerEndInter, circleCsNoticeReq,
		circleCsReportChat, circleCsPetAction, circleCsPetInteraction, circleCsPetMove:
		s.logger.Printf("CIRCLE msgid=%d not served (no reply in the client's table)", msgid)
		return nil
	}
	s.logger.Printf("CIRCLE unknown msgid=%d bytes=%d", msgid, len(data))
	return nil
}

// pushCircleChanges sends sc_circle_memeber_change_push (35) to the room of circle id except skip: the changed members
// as they now are, the leader's aid and the member counts. It also copies the new roles into the players in the room.
func pushCircleChanges(logger *log.Logger, rooms *circleRooms, id int64, skip *circlePlayer, changed []store.CircleMember) {
	room := rooms.find(id)
	if room == nil {
		return
	}
	members, master, ok := economy.CircleMembersOf(id)
	if !ok {
		return
	}
	room.setRoles(changed)
	online := rooms.presence(id)
	var body []byte
	for _, m := range changed {
		body = pbLen(body, 1, circleMemberInfo(m, online, true))
	}
	body = pbVar(body, 2, aidNum(master))
	body = pbVar(body, 3, uint64(len(members)))
	body = pbVar(body, 4, uint64(economy.CircleCapOf(id)))
	room.broadcast(logger, skip, circleRcMemberChange, body)
}

// circleAvatarInfo encodes the avatar of aid as AvatarInfo (CIRCLE namespace: 1 id, 2 name, 3 sex, 9 skin, 14 items).
// The sex mapping and the item ids are Square's [I].
func circleAvatarInfo(aid uint64) []byte {
	acc, ok := store.AccountByAvatarID(strconv.FormatUint(aid, 10))
	if !ok {
		acc = store.AccountSnapshot{Aid: strconv.FormatUint(aid, 10), Name: "cherry", Gender: "FEMALE"}
	}
	if acc.Skin == "" {
		acc.Skin = "1"
	}
	b := pbVar(nil, 1, aid)
	b = pbLen(b, 2, []byte(acc.Name))
	b = pbVar(b, 3, squareSex(acc.Gender))
	b = pbLen(b, 9, []byte(acc.Skin))
	for _, code := range acc.ItemCodes {
		if n, ok := SquareNumericItem(code); ok {
			b = pbLen(b, 14, pbVar(nil, 1, n))
		}
	}
	return b
}

// circleMemberInfo encodes one member as CircleMemberInfo: 1 aid, 2 name, 5 PlayerType (the role), 8 Connect (in the
// room now). [I] Connect's meaning (online flag or a time) is not traced; the member list sends 1 for an avatar in the
// room.
//
// f3 Profile is the saved profile picture as a path without the leading slash (the client prefixes its static base,
// SetPNGImageWithURL 0x1b816a4; the form Lounge Chat draws). f7 CreateDate is the join time in epoch ms as a decimal
// string (the row's date, TimeToYearMonthDate 0x2b2c7bc; empty drew 1/1/1970); withDate is false for the member-added
// push, whose parser divides f7 by 1000 (LP_sc_circle_memeber_add_push 0x24fd6c8; member-list.md section 1).
func circleMemberInfo(m store.CircleMember, online map[uint64]bool, withDate bool) []byte {
	aid := aidNum(m.Aid)
	b := pbVar(nil, 1, aid)
	b = pbLen(b, 2, []byte(circleAccountName(aid)))
	if pic := loungeProfileURL(aid); pic != "" {
		b = pbLen(b, 3, []byte(pic))
	}
	b = pbVar(b, 5, economy.CirclePlayerType(m.Role))
	if withDate && m.Joined > 0 {
		b = pbLen(b, 7, []byte(strconv.FormatInt(m.Joined, 10)))
	}
	if online[aid] {
		b = pbVar(b, 8, 1)
	}
	return b
}

// circleChatInfo encodes one message as ChatInfo: 1 sender, 2 sender name, 3 serial, 4 type, 5 text, 6 time (ms).
func circleChatInfo(m circleChatMsg) []byte {
	b := pbVar(nil, 1, m.sendAid)
	b = pbLen(b, 2, []byte(m.sendName))
	b = pbVar(b, 3, uint64(m.serial))
	b = pbVar(b, 4, m.kind)
	b = pbLen(b, 5, m.text)
	return pbVar(b, 6, uint64(m.ms))
}

// circleAccountName is the avatar's name ("cherry" when the account is gone).
func circleAccountName(aid uint64) string {
	if acc, ok := store.AccountByAvatarID(strconv.FormatUint(aid, 10)); ok && acc.Name != "" {
		return acc.Name
	}
	return "cherry"
}

// circleAidList reads the repeated aid field 1 (one varint per tag, or packed) as decimal text.
func circleAidList(data []byte) []string {
	var out []string
	pbScan(data, func(f, v uint64, d []byte) {
		if f != 1 {
			return
		}
		if d == nil {
			out = append(out, strconv.FormatUint(v, 10))
			return
		}
		for len(d) > 0 {
			n, m := binary.Uvarint(d)
			if m <= 0 {
				return
			}
			out = append(out, strconv.FormatUint(n, 10))
			d = d[m:]
		}
	})
	return out
}

// aidNum is an aid's number ("0" for text that is not one).
func aidNum(aid string) uint64 {
	n, _ := strconv.ParseUint(aid, 10, 64)
	return n
}
