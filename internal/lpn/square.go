package lpn

import (
	"encoding/binary"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"cherry/internal/store"
)

// Minimal two-client Square (SQUARE LPN namespace, gateway agent 7). Message
// layout comes from static research of libgame.so; fields marked [I] in the
// comments below are inferred, not observed on the wire.
const (
	squareAgent      = 7
	squareWriteTTL   = 5 * time.Second
	squareMaxChatLen = 512

	squareLoginResult    = 131073
	squareEnterResult    = 196609
	squareRelayResult    = 196623
	squareMapFileName    = "2401039000"
	squareThemeID        = 1
	squareDefaultCountry = "JP"
)

// Spawn on map 2401039000's own AVATAR_START tile (93,80) -> game px (6920,2920)
// via X=40*(wx-wy+N), Y=20*(2N-1-wx-wy)+20, N=160. Square never reads that table
// itself; each later player steps one tile along x (+40,-20), row y=80 is walkable.
const (
	squareSpawnX     = 6920
	squareSpawnY     = 2920
	squareSpawnStepX = 40
	squareSpawnStepY = 20
	squareMaxPlayers = 100
)

// squareItemPrefix maps an item code's category (code[2:4]) to its numeric
// resource-id prefix, from the restored item/{custom,dress}/<id> directories.
// The numeric id is prefix*100000 + base36(code[4:9]); this reproduces all four
// device-confirmed ids (e.g. CUON004TV -> 225106259).
var squareItemPrefix = map[string]uint64{
	"HE": 2238, "EB": 2240, "EY": 2241, "NO": 2242, "MO": 2243, "HA": 2244,
	"TO": 2250, "ON": 2251, "PA": 2252, "SH": 2253, "AH": 2254, "AE": 2255, "FE": 2256,
}

func SquareNumericItem(code string) (uint64, bool) {
	if len(code) != 9 || code[:2] != "CU" {
		return 0, false
	}
	prefix, ok := squareItemPrefix[code[2:4]]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(code[4:], 36, 64)
	if err != nil || n >= 100000 {
		return 0, false
	}
	return prefix*100000 + n, true
}

// lockedConn serialises whole-frame writes (handler replies and other
// connections' broadcasts) and bounds each write with a deadline so a stalled
// peer cannot block a broadcast forever. SetDeadline takes the same lock so the
// read loop cannot clear a write deadline between set and write.
type lockedConn struct {
	net.Conn
	mu sync.Mutex
}

func (c *lockedConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.Conn.SetWriteDeadline(time.Now().Add(squareWriteTTL))
	return c.Conn.Write(b)
}

func (c *lockedConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.SetDeadline(t)
}

type squarePlayer struct {
	aid     uint64
	conn    *lockedConn
	b1      byte
	sid     [8]byte
	avatar  []byte // encoded _AvatarInfo
	x, y, d uint64 // guarded by squareRoom.mu
}

type squareRoom struct {
	mu         sync.Mutex
	players    []*squarePlayer // join order
	chatSerial uint64
}

var defaultSquare = &squareRoom{}

func squareFrame(b1 byte, sid []byte, msgid uint16, body []byte) []byte {
	f := make([]byte, 16+len(body))
	binary.BigEndian.PutUint32(f, uint32(12+len(body)))
	f[4], f[5] = 'G', b1
	copy(f[6:14], sid)
	binary.BigEndian.PutUint16(f[14:16], msgid)
	copy(f[16:], body)
	return f
}

func pbVar(b []byte, field, v uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(b, field<<3), v)
}

func pbLen(b []byte, field uint64, d []byte) []byte {
	return append(binary.AppendUvarint(binary.AppendUvarint(b, field<<3|2), uint64(len(d))), d...)
}

// pbScan walks a message; v holds varint values, d length-delimited bytes.
// It returns false on malformed input.
func pbScan(data []byte, fn func(field uint64, v uint64, d []byte)) bool {
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 || key>>3 == 0 {
			return false
		}
		data = data[n:]
		switch key & 7 {
		case 0:
			v, n := binary.Uvarint(data)
			if n <= 0 {
				return false
			}
			fn(key>>3, v, nil)
			data = data[n:]
		case 1, 5:
			size := 8
			if key&7 == 5 {
				size = 4
			}
			if len(data) < size {
				return false
			}
			data = data[size:]
		case 2:
			size, n := binary.Uvarint(data)
			if n <= 0 || size > uint64(len(data[n:])) {
				return false
			}
			fn(key>>3, 0, data[n:n+int(size)])
			data = data[n+int(size):]
		default:
			return false
		}
	}
	return true
}

// pbBytesField returns the last length-delimited value of field; ok is false
// when the message is malformed.
func pbBytesField(data []byte, field uint64) (d []byte, found, ok bool) {
	ok = pbScan(data, func(f, _ uint64, b []byte) {
		if f == field && b != nil {
			d, found = b, true
		}
	})
	return d, found, ok
}

// pbSummary renders varint fields as f=v and length-delimited ones as f:len
// (no contents, so chat text or keys never reach the log); fixed-width
// fields show as f/32 or f/64 with their raw value for scale diagnosis.
func pbSummary(data []byte) string {
	out := []byte{}
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 {
			return string(out) + " !malformed"
		}
		data = data[n:]
		field := strconv.FormatUint(key>>3, 10)
		switch key & 7 {
		case 0:
			v, m := binary.Uvarint(data)
			if m <= 0 {
				return string(out) + " !malformed"
			}
			out = append(out, " "+field+"="+strconv.FormatUint(v, 10)...)
			data = data[m:]
		case 1:
			if len(data) < 8 {
				return string(out) + " !malformed"
			}
			out = append(out, " "+field+"/64="+strconv.FormatUint(binary.LittleEndian.Uint64(data), 10)...)
			data = data[8:]
		case 5:
			if len(data) < 4 {
				return string(out) + " !malformed"
			}
			out = append(out, " "+field+"/32="+strconv.FormatUint(uint64(binary.LittleEndian.Uint32(data)), 10)...)
			data = data[4:]
		case 2:
			size, m := binary.Uvarint(data)
			if m <= 0 || size > uint64(len(data[m:])) {
				return string(out) + " !malformed"
			}
			out = append(out, " "+field+":"+strconv.FormatUint(size, 10)...)
			data = data[m+int(size):]
		default:
			return string(out) + " !malformed"
		}
	}
	return string(out)
}

func squarePositionInfo(x, y, dir uint64) []byte {
	b := pbVar(nil, 1, x)
	b = pbVar(b, 2, y)
	b = pbVar(b, 3, x)
	b = pbVar(b, 4, y)
	b = pbVar(b, 5, dir)
	return pbVar(b, 6, 1) // actionType=1 per research
}

// info encodes PlayerInfo; the caller holds the room lock (position fields).
func (p *squarePlayer) info() []byte {
	b := pbVar(nil, 1, p.aid)
	b = pbLen(b, 2, p.avatar)
	return pbLen(b, 3, squarePositionInfo(p.x, p.y, p.d))
}

// squareSex maps Cherry's gender to the client's sex enum. [I] unverified:
// 0=male, 1=female, 2=animal; research only said "1=female?".
func squareSex(gender string) uint64 {
	switch gender {
	case "MALE":
		return 0
	case "ANIMAL":
		return 2
	}
	return 1
}

func squareAvatarInfo(aid uint64) []byte {
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
	b := pbLen(nil, 1, []byte(id))
	b = pbLen(b, 3, []byte(acc.Name))
	b = pbVar(b, 4, squareSex(acc.Gender))
	b = pbLen(b, 6, []byte(acc.Country))
	b = pbLen(b, 10, []byte(acc.Skin))
	for _, code := range acc.ItemCodes {
		if n, ok := SquareNumericItem(code); ok {
			b = pbLen(b, 24, pbVar(nil, 1, n))
		}
	}
	return b
}

func (p *squarePlayer) send(logger *log.Logger, msgid uint16, body []byte) {
	if _, err := p.conn.Write(squareFrame(p.b1, p.sid[:], msgid, body)); err != nil {
		logger.Printf("SQUARE send aid=%d msgid=%d err=%v", p.aid, msgid, err)
	}
}

// members returns a snapshot of the members excluding skip (nil = everyone).
func (r *squareRoom) members(skip *squarePlayer) []*squarePlayer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*squarePlayer, 0, len(r.players))
	for _, p := range r.players {
		if p != skip {
			out = append(out, p)
		}
	}
	return out
}

// broadcast writes outside the room lock; each write is deadline-bounded.
func (r *squareRoom) broadcast(logger *log.Logger, skip *squarePlayer, msgid uint16, body []byte) {
	for _, p := range r.members(skip) {
		p.send(logger, msgid, body)
	}
}

func squareDelUser(aid uint64) []byte { return pbVar(pbVar(nil, 1, aid), 2, 0) } // reason=0

// join registers p and returns the roster body fragment (self first). If the
// aid is already in the room (GO again on the same Square, or a reconnect), p
// takes over that entry and its position silently: other clients still show
// the avatar there, and a deluser+adduser pair would lose it because the
// client deletes departed avatars after a delay (_WaitDeleteWorldAvatar_2_0).
func (r *squareRoom) join(p *squarePlayer) (roster []byte, rejoined bool) {
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
		n := uint64(len(r.players) % 30) // ponytail: wraps after 30 to stay on row y=80
		p.x = squareSpawnX + n*squareSpawnStepX
		p.y = squareSpawnY - n*squareSpawnStepY
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

// leave removes p if still registered and tells the rest. Idempotent.
func (r *squareRoom) leave(logger *log.Logger, p *squarePlayer) {
	r.mu.Lock()
	found := false
	for i, o := range r.players {
		if o == p {
			r.players = append(r.players[:i], r.players[i+1:]...)
			found = true
			break
		}
	}
	r.mu.Unlock()
	if found {
		logger.Printf("SQUARE deluser aid=%d", p.aid)
		r.broadcast(logger, nil, 3, squareDelUser(p.aid))
	}
}

// squareSession is the per-connection agent-7 state, used from the single
// handler goroutine of that connection.
type squareSession struct {
	room     *squareRoom
	conn     *lockedConn
	logger   *log.Logger
	aid      uint64
	loggedIn bool
	entered  bool
	player   *squarePlayer
	moves    int
}

// handleOpen answers an agent-7 tunnel open with the same type-1 control shape
// Cherry sends for Garden. [I] exact bytes.
func (s *squareSession) handleOpen(frame []byte) error {
	control := make([]byte, 22)
	binary.BigEndian.PutUint32(control, 18)
	control[4], control[5] = 'G', frame[1]
	copy(control[6:14], frame[2:10])
	_, err := s.conn.Write(control)
	s.logger.Printf("SQUARE open-control err=%v", err)
	return err
}

func (s *squareSession) reply(frame []byte, msgid uint16, body []byte) error {
	_, err := s.conn.Write(squareFrame(frame[1], frame[2:10], msgid, body))
	return err
}

// close unregisters this connection's membership (exit/EOF/error).
func (s *squareSession) close() {
	if s.player != nil {
		s.logger.Printf("SQUARE leave aid=%d moves=%d", s.aid, s.moves)
		s.room.leave(s.logger, s.player)
		s.player = nil
	}
}

// handleData processes one agent-7 data frame (frame[10:12] msgid, frame[12:]
// protobuf). A non-nil error means a reply write failed and the connection
// should end.
func (s *squareSession) handleData(frame []byte) error {
	msgid := binary.BigEndian.Uint16(frame[10:12])
	data := frame[12:]
	if msgid == 0 { // cr_login_req -> rc_login_res
		aid, _ := protobufUintField(data, 2)
		if !gardenLoginValid(data) || aid == 0 || aid > (1<<63)-1 {
			s.logger.Printf("SQUARE login invalid request")
			return nil
		}
		s.aid, s.loggedIn = aid, true
		s.logger.Printf("SQUARE login aid=%d result=%d", aid, squareLoginResult)
		return s.reply(frame, 0, pbVar(nil, 1, squareLoginResult))
	}
	if !s.loggedIn {
		s.logger.Printf("SQUARE msgid=%d before login ignored", msgid)
		return nil
	}
	switch msgid {
	case 1: // cr_themelist_req -> rc_themelist_res
		s.room.mu.Lock()
		count := uint64(len(s.room.players))
		s.room.mu.Unlock()
		theme := pbVar(nil, 1, squareThemeID)
		theme = pbVar(theme, 2, 0)
		theme = pbLen(theme, 7, nil)
		theme = pbVar(theme, 8, 0)
		theme = pbLen(theme, 9, []byte("Square"))
		theme = pbLen(theme, 10, nil)
		theme = pbVar(theme, 11, 1)
		theme = pbVar(theme, 12, 0)
		theme = pbVar(theme, 14, 1)
		theme = pbVar(theme, 16, count)
		theme = pbLen(theme, 17, []byte("Square"))
		body := pbVar(pbLen(nil, 1, theme), 3, 0) // result=0 [I]
		s.logger.Printf("SQUARE themelist userCount=%d", count)
		return s.reply(frame, 1, body)
	case 2: // cr_room_enter_req -> rc_room_enter_res (idx6)
		body := pbVar(nil, 1, squareEnterResult)
		body = pbVar(body, 3, 1) // squareCode
		body = pbVar(body, 4, squareThemeID)
		body = pbVar(body, 5, 1) // clientMapRevision
		body = pbLen(body, 6, []byte(squareMapFileName))
		body = pbVar(body, 7, 0) // playerType
		s.entered = true
		s.logger.Printf("SQUARE enter aid=%d map=%s", s.aid, squareMapFileName)
		return s.reply(frame, 6, body)
	case 5: // cr_room_relaystart_req -> rc_room_relaystart_res + adduser_all
		if !s.entered {
			s.logger.Printf("SQUARE relaystart before enter ignored")
			return nil
		}
		p := &squarePlayer{aid: s.aid, conn: s.conn, b1: frame[1], avatar: squareAvatarInfo(s.aid)}
		copy(p.sid[:], frame[2:10])
		roster, rejoined := s.room.join(p)
		s.player = p
		s.room.mu.Lock()
		n := len(s.room.players)
		s.room.mu.Unlock()
		s.logger.Printf("SQUARE relaystart aid=%d roster=%d rejoined=%v", s.aid, n, rejoined)
		if err := s.reply(frame, 8, append(pbVar(nil, 1, squareRelayResult), roster...)); err != nil {
			return err
		}
		if rejoined {
			return nil
		}
		s.room.mu.Lock()
		self := p.info()
		s.room.mu.Unlock()
		s.room.broadcast(s.logger, p, 2, pbLen(nil, 1, self))
		return nil
	case 4: // cr_room_exit_req -> rc_room_exit_res
		err := s.reply(frame, 4, pbVar(nil, 1, 0))
		s.logger.Printf("SQUARE exit aid=%d", s.aid)
		s.close()
		s.entered = false
		return err
	}
	if s.player == nil {
		s.logger.Printf("SQUARE msgid=%d outside room ignored", msgid)
		return nil
	}
	switch msgid {
	case 6: // cr_player_move_req -> rc_player_move_all to others
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
			s.logger.Printf("SQUARE move invalid")
			return nil
		}
		s.room.mu.Lock()
		s.player.x, s.player.y, s.player.d = tx, ty, dir
		s.room.mu.Unlock()
		s.moves++
		if s.moves <= 20 || s.moves%50 == 0 {
			s.logger.Printf("SQUARE move aid=%d count=%d fields=%s", s.aid, s.moves, pbSummary(move))
		}
		s.room.broadcast(s.logger, s.player, 9, pbLen(pbVar(nil, 1, s.aid), 2, move))
	case 8: // cr_common_chat -> rc_common_chat_all to everyone
		msg, found, ok := pbBytesField(data, 1)
		if !ok || !found || len(msg) == 0 || len(msg) > squareMaxChatLen || !utf8.Valid(msg) {
			s.logger.Printf("SQUARE chat rejected")
			return nil
		}
		s.room.mu.Lock()
		s.room.chatSerial++
		serial := s.room.chatSerial
		s.room.mu.Unlock()
		body := pbVar(pbVar(nil, 1, s.aid), 2, serial)
		body = pbVar(pbLen(body, 3, msg), 4, 0) // type=0 [I]
		s.logger.Printf("SQUARE chat aid=%d serial=%d bytes=%d", s.aid, serial, len(msg))
		s.room.broadcast(s.logger, nil, 11, body)
	case 9: // cr_player_action -> rc_player_action_all to others [I field mapping]
		action, found, ok := pbBytesField(data, 1)
		if !ok || !found {
			s.logger.Printf("SQUARE action invalid")
			return nil
		}
		s.logger.Printf("SQUARE action aid=%d fields=%s", s.aid, pbSummary(action))
		s.room.broadcast(s.logger, s.player, 12, pbLen(pbVar(nil, 1, s.aid), 2, action))
	case 24: // cr_current_player_list_req -> rc_current_player_list_res
		var body []byte
		members := s.room.members(nil)
		body = pbVar(pbVar(nil, 1, uint64(len(members))), 2, squareMaxPlayers)
		for _, p := range members {
			name := "cherry"
			if acc, ok := store.AccountByAvatarID(strconv.FormatUint(p.aid, 10)); ok {
				name = acc.Name
			}
			body = pbLen(body, 3, pbLen(pbVar(nil, 1, p.aid), 2, []byte(name)))
		}
		s.logger.Printf("SQUARE playerlist aid=%d count=%d", s.aid, len(members))
		return s.reply(frame, 24, body)
	case 10: // cr_trunk_call_req: whisper-state notification, no reply needed
	default:
		s.logger.Printf("SQUARE unknown msgid=%d bytes=%d fields=%s", msgid, len(data), pbSummary(data))
	}
	return nil
}
