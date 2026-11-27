package lpn

import (
	"cherry/internal/itemcode"
	"cmp"
	"encoding/binary"
	"log"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Minimal two-client Square (SQUARE LPN namespace, gateway agent 7). Message
// layout comes from static research of libgame.so; fields marked [I] in the
// comments below are inferred, not observed on the wire.
const (
	squareAgent      = 7
	squareWriteTTL   = 5 * time.Second
	squareMaxChatLen = 512

	squareLoginResult      = 131073
	squareEnterResult      = 196609
	squareEnterFull        = 196617 // ArtsString 9245: "There are too many avatars in this square."
	squareEnterNotInSquare = 196629 // ArtsString 9267: "The avatar who invited you isn't in the square."
	squareEnterUnavailable = 196630 // ArtsString 9271: "Sorry! This square is not available."
	squareRelayResult      = 196623
	// World type 6 is Treasure Island, the Square with fishing: NaWorldManager::GetConnectedWorldType == 6 shows the
	// book (WorldMain tag 1) and the bait/rod buttons (WorldChatNotificationBg4 tags 5/2), and makes the client ask
	// for rods (59) and bait (91). Only this Square answers the fishing, boss and Boss Coin requests (squareGatedTheme).
	// Map 2108176000 is the beach with the round fish markers (user decision, 2026-11-17).
	squareThemeID     = 6
	squareMapFileName = "2108176000"
	// LINE City (theme 1, the original Square world type: the client builds its Square chat bar) is the Square with the
	// race balloon and the Ride Point shop. Its map is the global one, which holds their stock hosts.
	squareCityThemeID     = 1
	squareCityMapFileName = "2401039000"
	squareDefaultCountry  = "JP"
)

// Each Square's spawn tile (AVATAR_START, data/squares.json) is converted by Square.spawnPx; every Square and
// Circle joiner starts there (squareRoom.join, circleRoom.join).

// squareNpcList returns the minigame NPC list for Squares that host NPCs (Treasure Island and LINE City), or nil for other Squares.
func squareNpcList(theme uint64) []byte {
	switch theme {
	case squareThemeID:
		return squareTreasureIslandNpcList()
	case squareCityThemeID:
		return squareLineCityNpcList()
	}
	return nil
}

// squareRoomEnter is rc_room_enter_res (idx 6): npcs is the NpcList field (nil = none), number the room the banner shows
// ([0001]) when non-zero (omitted for public rooms so the client shows no code suffix), theme the world type the client switches to.
func squareRoomEnter(npcs []byte, number, theme uint64, mapFile string) []byte {
	body := pbVar(nil, 1, squareEnterResult)
	if npcs != nil {
		body = pbLen(body, 2, npcs)
	}
	if number != 0 {
		body = pbVar(body, 3, number) // squareCode: private code rooms and boss/race transfers
	}
	body = pbVar(body, 4, theme)
	body = pbVar(body, 5, 1) // clientMapRevision
	body = pbLen(body, 6, []byte(mapFile))
	return pbVar(body, 7, 0) // playerType
}

// squareThemeInfo is one theme_info of rc_themelist_res: ThemeId, ThemeIndex, ThemeName, ThemeEnabled,
// ThemeGroupId (the world type's own group: GetRepresentGroupType is the identity for these ids),
// ThemeVisibled, UserCount, ThemeListName and ThemeCountryCodeList "ALL", and IconPath (field 7, the list picture: a
// files/-relative path the client loads only when the file exists, square-thumbs-spec 2026-11-16). The invite picker
// lists only friends whose country is in the theme's country list, and "ALL" matches every one (AddFriendCellDatas
// 0x1cd4230; boss-mp-addendum 6).
func squareThemeInfo(sq Square, count uint64) []byte {
	theme := pbVar(nil, 1, sq.Theme)
	theme = pbVar(theme, 2, 1)               // CodeSquredEnabled: shows BTN_SECRET key icon next to GO in NaWorldEnterThemeCell
	theme = pbLen(theme, 7, []byte(sq.Icon)) // IconPath: the list picture (files/-relative; "" = the client default)
	theme = pbVar(theme, 8, sq.Index)
	theme = pbLen(theme, 9, []byte(sq.Name))
	theme = pbLen(theme, 10, nil)
	theme = pbVar(theme, 11, 1)
	theme = pbVar(theme, 12, sq.Theme)
	theme = pbVar(theme, 14, 1)
	theme = pbVar(theme, 16, count)
	theme = pbLen(theme, 17, []byte(sq.ListName))
	return pbLen(theme, 19, []byte("ALL"))
}

// SquareNumericItem is the numeric resource id of a cosmetic item code (attribute C: human CU and animal CA codes, faces
// and animal skin/ears/tail included), the client's id formula in itemcode.ID. The first version mapped only CU codes,
// so an animal avatar arrived with no items and drew as a blank body on other players' screens (Circle room, device
// 2026-11-01); the same helper feeds Square and Garden visit rosters.
func SquareNumericItem(code string) (uint64, bool) {
	if len(code) != 9 || code[0] != 'C' {
		return 0, false
	}
	n, ok := itemcode.ID(code)
	return uint64(n), ok
}

// lockedConn serialises whole-frame writes (handler replies and other
// connections' broadcasts) and bounds each write with a deadline so a stalled
// peer cannot block a broadcast forever. SetDeadline takes the same lock so the
// read loop cannot clear a write deadline between set and write.
type lockedConn struct {
	net.Conn
	mu sync.Mutex
	// listener, remote and since are set when the connection is accepted and never change: the staff Online page reads them
	// (online.go). listener is "session" (:10123) or "gateway" (:10000).
	listener string
	remote   string
	since    time.Time
	broken   error // the first write error; guarded by mu
}

// Write sends b within squareWriteTTL. The first failed write closes the connection and every later write fails at
// once: other players' goroutines broadcast through here, and a peer that stopped reading used to cost each of them
// the full TTL per message, so whole Squares fell minutes behind ("Connection unstable due to heavy traffic", dead
// avatars kept in rooms; production 2026-11-25). Closing also ends the peer's own read loop, which runs its leave. A
// timed-out write may have sent part of a frame, so the stream is unusable anyway.
func (c *lockedConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken != nil {
		return 0, c.broken
	}
	_ = c.Conn.SetWriteDeadline(time.Now().Add(squareWriteTTL))
	n, err := c.Conn.Write(b)
	if err != nil {
		c.broken = err
		_ = c.Conn.Close()
	}
	return n, err
}

func (c *lockedConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.SetDeadline(t)
}

type squarePlayer struct {
	aid            uint64
	conn           *lockedConn
	b1             byte
	sid            [8]byte
	avatar         []byte // encoded _AvatarInfo; guarded by squareRoom.mu once joined
	x, y, d        uint64 // guarded by squareRoom.mu
	seat           []byte // encoded _player_interaction while seated (PlayerInfo.Interaction); guarded by squareRoom.mu
	ridePetID      uint64 // active mounted pet_id (0 if not riding); guarded by squareRoom.mu
	rideState      uint64 // non-zero while riding ridePetID; guarded by squareRoom.mu
	whisperStep    int32  // current eWHISPER_STEP (0..7); guarded by squareRoom.mu
	whisperPartner uint64 // active or pending whisper peer AID (0 if none); guarded by squareRoom.mu
}

// squareRoom is one room of a Square: the players in it see each other and no one else. number is the room's number within
// its pool (public or secret); theme, number and secret never change.
type squareRoom struct {
	mu         sync.Mutex
	theme      uint64
	number     uint64
	secret     bool
	players    []*squarePlayer // join order
	chatSerial uint64
	bosses     []*squareBossRoom          // boss lobbies and battles opened in this room, oldest first (square_boss_room.go); guarded by mu
	invites    map[uint64]map[uint64]bool // inviter AID -> invited AIDs for regular Square invites (square_invite.go); guarded by mu
}

// squareRooms holds the public and private secret/code rooms of each Square (world type), each sorted by number,
// lowest first. Players in different rooms (or in public vs secret rooms with the same number) do not see each other.
// A room is made when a player is placed in it and removed once its last player has left and no boss lobby is open in it,
// so its number is free again.
type squareRooms struct {
	mu          sync.Mutex
	rooms       map[uint64][]*squareRoom
	secretRooms map[uint64][]*squareRoom
}

var defaultSquares = &squareRooms{}

// squareRoomCap is the players one Square room holds: the staff panel's value, else the default.
func squareRoomCap() uint64 { return uint64(store.SquareRoomCap(store.SquareRoomCapDefault)) }

// room is the room of theme with that number, made when absent.
func (rs *squareRooms) room(theme, number uint64) *squareRoom {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.roomLocked(theme, number)
}

func ensureRoomLocked(m *map[uint64][]*squareRoom, theme, number uint64, secret bool) *squareRoom {
	if *m == nil {
		*m = map[uint64][]*squareRoom{}
	}
	list := (*m)[theme]
	i, found := slices.BinarySearchFunc(list, number, func(r *squareRoom, n uint64) int { return cmp.Compare(r.number, n) })
	if found {
		return list[i]
	}
	r := &squareRoom{theme: theme, number: number, secret: secret}
	(*m)[theme] = slices.Insert(list, i, r)
	return r
}

// roomLocked is room for a caller that holds rs.mu (public pool).
func (rs *squareRooms) roomLocked(theme, number uint64) *squareRoom {
	return ensureRoomLocked(&rs.rooms, theme, number, false)
}

// secretRoomLocked is room for a caller that holds rs.mu (private secret/code pool).
func (rs *squareRooms) secretRoomLocked(theme, number uint64) *squareRoom {
	return ensureRoomLocked(&rs.secretRooms, theme, number, true)
}

// firstAvailableLocked returns the lowest room number >= 1 that either does not exist in sorted rooms or has fewer
// than limit players. Custom higher-numbered shards (e.g. 5032) are skipped until all lower numbers are full.
func firstAvailableLocked(rooms []*squareRoom, counts []uint64, limit uint64) uint64 {
	next := uint64(1)
	for i, r := range rooms {
		if r.number > next {
			return next
		}
		if r.number == next {
			if counts[i] < limit {
				return next
			}
			next++
		}
	}
	return next
}

// chooseLocked is the room number aid enters theme in, given want (0 for none, else the number the client asked for or
// the room a lobby master is in):
//   - want == 0: aid's own room when it is in one (a re-enter keeps it); else firstAvailableLocked (1, 2, 3, ...).
//   - want != 0: want when aid is already in room want, room want does not exist yet, or room want has space; if room
//     want exists and is full, fall back to firstAvailableLocked (relaystart race / bossAccept).
//
// aid 0 never matches a room. The caller holds rs.mu.
func (rs *squareRooms) chooseLocked(theme, aid, want, limit uint64) uint64 {
	rooms := rs.rooms[theme]
	counts := make([]uint64, len(rooms))
	for i, r := range rooms {
		r.mu.Lock()
		counts[i] = uint64(len(r.players))
		holds := aid != 0 && slices.ContainsFunc(r.players, func(p *squarePlayer) bool { return p.aid == aid })
		r.mu.Unlock()
		if holds && (want == 0 || r.number == want) {
			return r.number
		}
	}
	if want == 0 {
		return firstAvailableLocked(rooms, counts, limit)
	}
	i, found := slices.BinarySearchFunc(rooms, want, func(r *squareRoom, n uint64) int { return cmp.Compare(r.number, n) })
	if !found || counts[i] < limit {
		return want
	}
	return firstAvailableLocked(rooms, counts, limit)
}

// roomStateFor reports whether room number exists in the public or secret pool of theme, and if so whether it has
// reached limit without holding aid.
func (rs *squareRooms) roomStateFor(theme, aid, number, limit uint64, secret bool) (exists, full bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	list := rs.rooms[theme]
	if secret {
		list = rs.secretRooms[theme]
	}
	i, found := slices.BinarySearchFunc(list, number, func(r *squareRoom, n uint64) int { return cmp.Compare(r.number, n) })
	if !found {
		return false, false
	}
	r := list[i]
	r.mu.Lock()
	defer r.mu.Unlock()
	if uint64(len(r.players)) < limit {
		return true, false
	}
	return true, aid == 0 || !slices.ContainsFunc(r.players, func(p *squarePlayer) bool { return p.aid == aid })
}

// pick is the room number aid enters theme in (chooseLocked under the registry lock). The reply names it; the relaystart
// joins it, or the next room with space when it has filled meanwhile.
func (rs *squareRooms) pick(theme, aid, want, limit uint64) uint64 {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.chooseLocked(theme, aid, want, limit)
}

// join places p in the public room chooseLocked picks and returns that room with the roster and the rejoined and wasSeated
// flags of squareRoom.join.
func (rs *squareRooms) join(theme uint64, p *squarePlayer, want, limit, spawnX, spawnY uint64) (*squareRoom, []byte, bool, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	r := rs.roomLocked(theme, rs.chooseLocked(theme, p.aid, want, limit))
	roster, rejoined, wasSeated := r.join(p, spawnX, spawnY)
	return r, roster, rejoined, wasSeated
}

// joinSecret places p in the private secret/code room number of theme and returns that room with the roster and flags.
func (rs *squareRooms) joinSecret(theme uint64, p *squarePlayer, number, spawnX, spawnY uint64) (*squareRoom, []byte, bool, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	r := rs.secretRoomLocked(theme, number)
	roster, rejoined, wasSeated := r.join(p, spawnX, spawnY)
	return r, roster, rejoined, wasSeated
}

// leave removes p from r if still there and tells the rest of r. Idempotent. An emptied room is removed.
func (rs *squareRooms) leave(logger *log.Logger, r *squareRoom, p *squarePlayer) {
	rs.mu.Lock()
	r.mu.Lock()
	found := false
	for i, o := range r.players {
		if o == p {
			r.players = append(r.players[:i], r.players[i+1:]...)
			found = true
			break
		}
	}
	if found {
		r.clearWhisperLocked(p)
		delete(r.invites, p.aid)
	}
	r.mu.Unlock()
	rs.pruneLocked(r)
	rs.mu.Unlock()
	if found {
		logger.Printf("SQUARE deluser aid=%d room=%d", p.aid, r.number)
		r.broadcast(logger, nil, 3, squareDelUser(p.aid))
	}
}

// prune removes r when it is empty (no player, no boss lobby). A boss lobby that has just left its room calls it.
func (rs *squareRooms) prune(r *squareRoom) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.pruneLocked(r)
}

// pruneLocked is prune for a caller that holds rs.mu.
func (rs *squareRooms) pruneLocked(r *squareRoom) {
	r.mu.Lock()
	empty := len(r.players) == 0 && len(r.bosses) == 0
	r.mu.Unlock()
	if !empty {
		return
	}
	if r.secret {
		rs.secretRooms[r.theme] = slices.DeleteFunc(rs.secretRooms[r.theme], func(o *squareRoom) bool { return o == r })
	} else {
		rs.rooms[r.theme] = slices.DeleteFunc(rs.rooms[r.theme], func(o *squareRoom) bool { return o == r })
	}
}

// population is the players in every room (public and secret) of theme: the Square list's UserCount.
func (rs *squareRooms) population(theme uint64) uint64 {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	var n uint64
	for _, list := range [][]*squareRoom{rs.rooms[theme], rs.secretRooms[theme]} {
		for _, r := range list {
			r.mu.Lock()
			n += uint64(len(r.players))
			r.mu.Unlock()
		}
	}
	return n
}

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

// info encodes PlayerInfo; the caller holds the room lock (position and seat fields). A seated
// player carries its Interaction (field 4): AddAvatarToWorld_2_0 seats the avatar on DestObjectIndex.
func (p *squarePlayer) info() []byte {
	avatar := p.avatar
	if p.whisperStep != 0 {
		avatar = pbVar(slices.Clone(avatar), 19, uint64(p.whisperStep))
	}
	b := pbVar(nil, 1, p.aid)
	b = pbLen(b, 2, avatar)
	b = pbLen(b, 3, squarePositionInfo(p.x, p.y, p.d))
	if p.seat != nil {
		b = pbLen(b, 4, p.seat)
	}
	return b
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
	return squareAvatarInfoWithRide(aid, 0, 0)
}

func squareAvatarInfoWithRide(aid, ridePetID, rideState uint64) []byte {
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
	if pet := squareFollowPetBaseInfo(aid, ridePetID, rideState); pet != nil {
		b = pbLen(b, 23, pet) // SQUARE::_AvatarInfo.pet (repeated PetBaseInfo)
	}
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
// client deletes departed avatars after a delay (_WaitDeleteWorldAvatar_2_0). A re-entered
// client starts standing, so the old seat is dropped (wasSeated) and the caller tells the others.
func (r *squareRoom) join(p *squarePlayer, spawnX, spawnY uint64) (roster []byte, rejoined, wasSeated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := -1
	for i, o := range r.players {
		if o.aid == p.aid {
			index = i
			p.x, p.y, p.d = o.x, o.y, o.d
			wasSeated = o.seat != nil
			break
		}
	}
	if index < 0 {
		// Everyone starts on AVATAR_START, the one tile the map guarantees walkable. Stepping one tile per player put
		// the 7th and 8th joiner of LINE Café and LINE Park on blocked tiles, where the client cannot path out and
		// every tap is ignored (production 2026-11-25: 54-66% of those sessions never moved, 2-5% otherwise).
		p.x, p.y = spawnX, spawnY
	}
	roster = pbLen(nil, 2, p.info())
	for i, o := range r.players {
		if i != index {
			roster = pbLen(roster, 2, o.info())
		}
	}
	if index >= 0 {
		r.clearWhisperLocked(r.players[index])
		r.players[index] = p
	} else {
		r.players = append(r.players, p)
	}
	for inviter, invited := range r.invites {
		delete(invited, p.aid)
		if len(invited) == 0 {
			delete(r.invites, inviter)
		}
	}
	return roster, index >= 0, wasSeated
}

// squareSession is the per-connection agent-7 state, used from the single
// handler goroutine of that connection.
type squareSession struct {
	rooms      *squareRooms
	room       *squareRoom // the room the player is in (set at relaystart; nil before)
	roomNo     uint64      // the room number of the Square entered last: the enter's pick, which the relaystart joins
	roomSecret bool        // true when roomNo is in the private secret/code pool (squareRooms.secretRooms)
	square     Square      // the Square entered last: its spawn and map
	conn       *lockedConn
	logger     *log.Logger
	aid        uint64
	loggedIn   bool
	entered    bool
	player     *squarePlayer
	moves      int
	fish       *squareFish     // catch chosen at the last fishing start, paid once by its finish
	fishing    bool            // busy status 20 was broadcast and no end followed yet
	boss       *squareBossSeat // this session's seat in a boss lobby or battle (square_boss_room.go); nil outside one
	racing     *squareRace     // Riding Pet race in progress (square_racing.go); nil outside the race
	racePet    string          // the Riding Pet code the racer picked last (kept for the next race)
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
	s.bossLeave()
	s.stopRacing()
	if s.player != nil {
		if s.fishing { // left mid-cast: clear the bobber/busy state on the others
			s.room.broadcast(s.logger, s.player, squareRcPlayerStatus, squareBusyStatus(s.aid, squareStatusIdle))
			s.fish, s.fishing = nil, false
		}
		s.logger.Printf("SQUARE leave aid=%d moves=%d", s.aid, s.moves)
		s.rooms.leave(s.logger, s.room, s.player)
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
		aid, ok := verifiedLogin(data)
		if !ok {
			s.logger.Printf("SQUARE login refused: no verified login")
			return nil
		}
		s.aid, s.loggedIn = aid, true
		bindGatewayLogin(s.conn, aid)
		s.logger.Printf("SQUARE login aid=%d result=%d", aid, squareLoginResult)
		return s.reply(frame, 0, pbVar(nil, 1, squareLoginResult))
	}
	if !s.loggedIn {
		s.logger.Printf("SQUARE msgid=%d before login ignored", msgid)
		return nil
	}
	if want, gated := squareGatedTheme(msgid); gated && s.entered && s.square.Theme != want { // never entered: ignored as before
		return s.refuseSquare(frame, msgid)
	}
	switch msgid {
	case 1: // cr_themelist_req -> rc_themelist_res: every visible Square with its user count
		var body []byte
		for _, sq := range squaresFile.Get().listed() {
			body = pbLen(body, 1, squareThemeInfo(sq, s.rooms.population(sq.Theme)))
		}
		body = pbVar(body, 3, 0) // result=0 [I]
		s.logger.Printf("SQUARE themelist squares=%d", len(squaresFile.Get().listed()))
		return s.reply(frame, 1, body)
	case 2: // cr_room_enter_req -> rc_room_enter_res (idx6): explicit SquareCode enters secretRooms; normal GO enters public rooms
		theme, _ := protobufUintField(data, 1)
		want, wantFound := protobufUintField(data, 2)
		sq := squaresFile.Get().entry(theme)
		if wantFound {
			if want == 0 || want > 9999 {
				s.logger.Printf("SQUARE enter code refused aid=%d theme=%d code=%d invalid", s.aid, sq.Theme, want)
				return s.reply(frame, 6, pbVar(nil, 1, squareEnterUnavailable))
			}
			if _, full := s.rooms.roomStateFor(sq.Theme, s.aid, want, squareRoomCap(), true); full {
				s.logger.Printf("SQUARE enter code refused aid=%d theme=%d code=%d full", s.aid, sq.Theme, want)
				return s.reply(frame, 6, pbVar(nil, 1, squareEnterFull))
			}
			s.bossLeave() // back from a boss lobby or battle: the Square is entered again
			s.stopRacing()
			if s.player != nil && (s.square.Theme != sq.Theme || !s.roomSecret || s.roomNo != want) {
				s.close()
			}
			s.roomNo, s.roomSecret, s.square, s.entered = want, true, sq, true
			s.logger.Printf("SQUARE enter aid=%d theme=%d room=%d secret=true map=%s", s.aid, sq.Theme, s.roomNo, sq.Map)
			MarkQuest(s.aid, "square")
			return s.reply(frame, 6, squareRoomEnter(squareNpcList(sq.Theme), want, sq.Theme, sq.Map))
		}
		s.bossLeave() // back from a boss lobby or battle: the Square is entered again
		s.stopRacing()
		if s.player != nil && (s.square.Theme != sq.Theme || s.roomSecret) {
			s.close()
		}
		s.roomNo = s.rooms.pick(sq.Theme, s.aid, 0, squareRoomCap())
		s.roomSecret, s.square, s.entered = false, sq, true
		s.logger.Printf("SQUARE enter aid=%d theme=%d room=%d map=%s", s.aid, sq.Theme, s.roomNo, sq.Map)
		MarkQuest(s.aid, "square")
		return s.reply(frame, 6, squareRoomEnter(squareNpcList(sq.Theme), 0, sq.Theme, sq.Map))
	case 5: // cr_room_relaystart_req -> rc_room_relaystart_res + adduser_all
		if !s.entered {
			s.logger.Printf("SQUARE relaystart before enter ignored")
			return nil
		}
		p := &squarePlayer{aid: s.aid, conn: s.conn, b1: frame[1], avatar: squareAvatarInfo(s.aid)}
		copy(p.sid[:], frame[2:10])
		spawnX, spawnY := s.square.spawnPx()
		var (
			room                *squareRoom
			roster              []byte
			rejoined, wasSeated bool
		)
		if s.roomSecret {
			room, roster, rejoined, wasSeated = s.rooms.joinSecret(s.square.Theme, p, s.roomNo, spawnX, spawnY)
		} else {
			room, roster, rejoined, wasSeated = s.rooms.join(s.square.Theme, p, s.roomNo, squareRoomCap(), spawnX, spawnY)
			if s.roomNo != 0 && s.roomNo != room.number { // the room the enter named filled since: the next one with space
				s.logger.Printf("SQUARE room %d full aid=%d placed in room %d", s.roomNo, s.aid, room.number)
			}
		}
		s.room, s.roomNo, s.player = room, room.number, p
		room.mu.Lock()
		n := len(room.players)
		room.mu.Unlock()
		s.logger.Printf("SQUARE relaystart aid=%d room=%d roster=%d rejoined=%v", s.aid, room.number, n, rejoined)
		if err := s.reply(frame, 8, append(pbVar(nil, 1, squareRelayResult), roster...)); err != nil {
			return err
		}
		s.racingRelayStarted()
		if rejoined {
			if wasSeated { // others still show the old seat: stand the avatar up at its stored position
				s.room.mu.Lock()
				stand := squarePositionInfo(p.x, p.y, p.d)
				s.room.mu.Unlock()
				s.room.broadcast(s.logger, p, 9, pbLen(pbVar(nil, 1, s.aid), 2, stand))
			}
		} else {
			s.room.mu.Lock()
			self := p.info()
			s.room.mu.Unlock()
			s.room.broadcast(s.logger, p, 2, pbLen(nil, 1, self))
		}
		s.pushHighFiveTargets(rejoined)
		return nil
	case 4: // cr_room_exit_req -> rc_room_exit_res
		err := s.reply(frame, 4, pbVar(nil, 1, 0))
		s.logger.Printf("SQUARE exit aid=%d", s.aid)
		s.close()
		s.entered = false
		return err
	case 12: // cr_report_chat: stored, no reply (report.go)
		s.report(data)
		return nil
	}
	switch msgid { // invite requests and boss lobby/battle requests need no Square relay (an invitee accepting from Garden, or a player in a boss lobby)
	case 35, 36, 37:
		return s.handleSquareInvite(frame, msgid, data)
	case 46, 47, 48, 49, 50, 55, 58, 60:
		return s.handleBoss(frame, msgid, data)
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
		if len(move) > maxRelayPayload {
			s.logger.Printf("SQUARE move over %d bytes ignored", maxRelayPayload)
			return nil
		}
		s.room.mu.Lock()
		s.player.x, s.player.y, s.player.d = tx, ty, dir
		s.player.seat = nil // walking stands up: rc_player_move_all removes the seat on the others
		s.room.mu.Unlock()
		s.moves++
		if s.moves <= 20 || s.moves%50 == 0 {
			s.logger.Printf("SQUARE move aid=%d count=%d fields=%s", s.aid, s.moves, pbSummary(move))
		}
		s.room.broadcast(s.logger, s.player, 9, pbLen(pbVar(nil, 1, s.aid), 2, move))
	case 13: // cr_player_interaction_req (sit; Info only) -> rc_player_interaction_all to the others
		info, found, ok := pbBytesField(data, 1)
		var dest uint64
		if ok && found {
			ok = pbScan(info, func(f, v uint64, _ []byte) {
				if f == 6 { // DestObjectIndex: the chair the avatar is seated on
					dest = v
				}
			})
		}
		if !ok || !found {
			s.logger.Printf("SQUARE interaction invalid")
			return nil
		}
		s.room.mu.Lock()
		if dest != 0 {
			s.player.seat = slices.Clone(info)
		} else {
			s.player.seat = nil
		}
		s.room.mu.Unlock()
		s.logger.Printf("SQUARE interaction aid=%d fields=%s", s.aid, pbSummary(info))
		s.room.broadcast(s.logger, s.player, 15, pbLen(pbVar(nil, 1, s.aid), 2, info))
	case 8: // cr_common_chat -> rc_common_chat_all to everyone
		msg, found, ok := pbBytesField(data, 1)
		if !ok || !found || len(msg) == 0 || len(msg) > squareMaxChatLen || !utf8.Valid(msg) || unsafeChat("SQUARE", s.aid, string(msg)) {
			s.logger.Printf("SQUARE chat rejected")
			return nil
		}
		s.room.mu.Lock()
		s.room.chatSerial++
		serial := s.room.chatSerial
		s.room.mu.Unlock()
		body := pbVar(pbVar(nil, 1, s.aid), 2, serial)
		body = pbVar(pbLen(body, 3, []byte(economy.MaskBannedWords(string(msg)))), 4, 0) // type=0 [I]
		s.logger.Printf("SQUARE chat aid=%d serial=%d bytes=%d", s.aid, serial, len(msg))
		store.RecordChat(store.ChatEntry{Kind: store.ChatSquare, Channel: strconv.FormatUint(s.square.Theme, 10), Sender: strconv.FormatUint(s.aid, 10), SenderName: squareAccountName(s.aid), Text: string(msg)})
		s.room.broadcast(s.logger, nil, 11, body)
	case 9: // cr_player_action -> rc_player_action_all to others [I field mapping]
		action, found, ok := pbBytesField(data, 1)
		if !ok || !found {
			s.logger.Printf("SQUARE action invalid")
			return nil
		}
		if len(action) > maxRelayPayload {
			s.logger.Printf("SQUARE action over %d bytes ignored", maxRelayPayload)
			return nil
		}
		s.logger.Printf("SQUARE action aid=%d fields=%s", s.aid, pbSummary(action))
		s.room.broadcast(s.logger, s.player, 12, pbLen(pbVar(nil, 1, s.aid), 2, action))
	case 18: // cr_highfive_achieve_req -> rc_highfive_achieve_res (27) to initiator and target (square_highfive.go)
		return s.handleHighFive(frame, data)
	case 24: // cr_current_player_list_req -> rc_current_player_list_res
		var body []byte
		members := s.room.members(nil)
		body = pbVar(pbVar(nil, 1, uint64(len(members))), 2, squareRoomCap())
		for _, p := range members {
			name, picture, bg := "cherry", "", ""
			if acc, ok := store.AccountByAvatarID(strconv.FormatUint(p.aid, 10)); ok {
				name, picture, bg = acc.Name, acc.ProfileImage.Image, acc.ProfileImage.Bg
			}
			entry := pbLen(pbVar(nil, 1, p.aid), 2, []byte(name))
			if picture != "" { // profile (4) and obs (6) go together: a profile alone takes the client's direct-URL branch
				if bg == "" {
					bg = "profilebg/12.jpg" // the profile reply's fallback (internal/web/social.go)
				}
				entry = pbLen(entry, 4, []byte(picture))
				entry = pbLen(entry, 5, []byte(bg))
				entry = pbLen(entry, 6, []byte(picture))
			}
			body = pbLen(body, 3, entry)
		}
		s.logger.Printf("SQUARE playerlist aid=%d count=%d", s.aid, len(members))
		return s.reply(frame, 24, body)
	case 25, 26, 27, 28, 29, 30, 59, 87, 88, 89, 90, 91: // fishing: cast, book, shop, bag, gear (square_fishing.go)
		return s.handleFishing(frame, msgid, data)
	case 38, 39, 40, 92: // follow/ride pets in Square: action, interaction, move, ride (square_pets.go)
		return s.handleSquarePet(frame, msgid, data)
	case 45, 54, 56, 57: // boss try, banner and book (square_boss.go); the lobby and battle requests are handled above the relay gate
		return s.handleBoss(frame, msgid, data)
	case 52, 53: // Boss Coin exchange office: list, buy (square_exchange.go)
		return s.handleExchange(frame, msgid, data)
	case 61, 62: // Ride Point exchange shop: list, buy (square_racing_exchange.go)
		return s.handleRideShop(frame, msgid, data)
	case 63, 64, 65, 66, 68, 71, 72, 76, 79, 82, 86, 94: // Riding Pet race: entry, create, ready, start, positions (square_racing.go)
		return s.handleRacing(frame, msgid, data)
	case 10: // cr_trunk_call_req -> rc_trunk_call (13) (square_whisper.go)
		return s.handleWhisper(frame, data)
	default:
		s.logger.Printf("SQUARE unknown msgid=%d bytes=%d fields=%s", msgid, len(data), pbSummary(data))
	}
	return nil
}
