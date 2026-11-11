package lpn

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/store"
)

// 1:1 friend chat: the CHAT button on a friend's profile (client scene 9, NaAvatarChatScene). Requests are op 0x0A on the
// session channel, like Lounge Chat, and get the same 0x1A reply and 0x1B push frames. The layouts and reply rules are the
// slice-1 minimum in avatarchat-spec.md, section 7.
//
// The room registry and its messages survive a restart (chatrooms.json, store/chatrooms.go): each room's id, members, newest
// serial, each member's last ack and delete mark and its newest messages, and the next room id. A send, a room creation, an
// ack that moves and a delete are saved before their reply and push, so a restart resumes at the last serial, keeps the
// unread counts and never hands out a room id or a serial again.
//
// ponytail: whole-file rewrite per message, ack and delete. Every such change rewrites chatrooms.json with all rooms' history
// (up to avatarChatHistoryMax messages each), so the write cost grows with the total. Becomes rows in the DB with Roadmap 8
// storage (8.3b).

// CherryChatRoomFirst and CherryChatRoomLast bound the room ids Cherry makes. The client takes a room id as a JSON string
// (spec 1.1), so the ids go out as decimal strings.
const (
	CherryChatRoomFirst int64 = 1960000000
	CherryChatRoomLast  int64 = 1969999999
)

// Command numbers (spec 1.1). 1001, 3001 and 3002 are shared with Lounge Chat: avatarChatOwns tells the two apart.
const (
	acCreate     = 1001
	acDelete     = 1004
	acSyncRoom   = 1006
	acRoomConfig = 1012
	acSend       = 3001
	acSync       = 3002
	acGetMsgs    = 3003
	acAck        = 3004
)

// Result codes. The client branches on 201, 202, 604, 50000 and 50001 (spec 1.2), so a refusal is 402 and an internal
// fault 500. Neither has a branch of its own: 402 shows the generic error and a send is marked failed.
const (
	acRefused  = 402
	acInternal = 500
)

const (
	avatarChatTypeText    = 1   // the only message type this chat shows (spec 4.2)
	avatarChatTextMax     = 500 // characters after trimming, as Lounge Chat counts them
	avatarChatHistoryMax  = 200 // messages kept per room; the oldest go first, the serials keep counting
	avatarChatRoomsMax    = 200 // rooms per account (a room exists per friend pair)
	avatarChatBannerMax   = 40  // runes of the arrival banner text
	avatarChatPushCommand = 13001
)

// avatarChatListOn: kill switch for the talk list and CHAT count; false serves the empty list again (flip here on a device crash).
var avatarChatListOn = true

// avatarChatBannerOn: kill switch for the arrival banner text; false sends the silent 13001 refresh push without content.
var avatarChatBannerOn = true

// avatarChatHereTTL is how long a member counts as in a room after their last command naming it. The client sends no
// leave command, so a member who left is out of the room this long after their last one. ponytail: a member who leaves
// and is sent a message within that time gets no banner; the client routes a push for an open room to the chat anyway.
const avatarChatHereTTL = 30 * time.Second

// avatarChatMsg is one stored message: the serial, the unix seconds, the sender and the text.
type avatarChatMsg struct {
	sn     int64
	sec    int64
	sender uint64
	text   string
}

// avatarChatRoom is a room between two friends: a < b, the newest messages (oldest first), the newest serial lastSn (saved
// with every message, so a restart resumes exactly there) and each member's last acked serial, never above lastSn. hid
// holds the members who deleted the room from their talk list, each with the serial it had then.
type avatarChatRoom struct {
	a, b       uint64
	msgs       []avatarChatMsg
	lastSn     int64
	ackA, ackB int64
	hid        map[uint64]int64
}

// avatarChatSeenAt is where a member was last seen: the room and the time of their last command naming it.
type avatarChatSeenAt struct {
	room int64
	at   time.Time
}

// other is the member of r that is not aid.
func (r *avatarChatRoom) other(aid uint64) uint64 {
	if r.a == aid {
		return r.b
	}
	return r.a
}

// ackOf is the ack slot of aid, a member of r.
func (r *avatarChatRoom) ackOf(aid uint64) *int64 {
	if r.a == aid {
		return &r.ackA
	}
	return &r.ackB
}

// offset is offsetMsgSn: the serial just below the oldest message r still holds, so the client asks for no serial the
// server has dropped. With no message held (a fresh room, or a room restored from a file with none) it is lastSn. Caller
// holds avatarChatMu.
func (r *avatarChatRoom) offset() int64 {
	if len(r.msgs) == 0 {
		return r.lastSn
	}
	return r.msgs[0].sn - 1
}

// avatarChatMu guards every room, the pair index and the next id. Pushes are built under the lock and written after it
// is released (sendLoungePushes), and no account or friend lookup runs under it. A registry save runs under it
// (avatarChatSaveLocked); store.SaveChatRooms takes only its own leaf lock, so the order is avatarChatMu, then that one.
var (
	avatarChatMu    sync.Mutex
	avatarChatRooms = map[int64]*avatarChatRoom{}
	avatarChatPairs = map[[2]uint64]int64{} // members (low, high) -> room id
	avatarChatNext  = CherryChatRoomFirst
	avatarChatHere  = map[uint64]avatarChatSeenAt{} // member -> where they were last seen (memory only)
)

// avatarChatUser is one entry of a memberList or of a talk-list memberList: a userId string.
type avatarChatUser struct {
	UserID string `json:"userId"`
}

// avatarChatReq is the payload keys the 1:1 commands read (spec 1.1). A key a command does not name stays zero. A key of
// the wrong JSON type fails the whole request, which is then refused.
type avatarChatReq struct {
	MemberList []avatarChatUser `json:"memberList"`
	RoomID     string           `json:"roomId"`
	MsgType    int64            `json:"msgType"`
	Msg        string           `json:"msg"`
	StartMsgSn int64            `json:"startMsgSn"`
	EndMsgSn   int64            `json:"endMsgSn"`
	MsgSn      int64            `json:"msgSn"`
}

// avatarChatMember is one row of the 1006 memberList (spec 5.2 and section 7.1).
type avatarChatMember struct {
	UserID     string `json:"userId"`
	Nickname   string `json:"nickname"`
	ProfileURL string `json:"profileUrl"`
	Deleted    bool   `json:"deleted"`
	Friend     bool   `json:"friend"`
}

// avatarChatRow is one message in a 3003 reply (spec 4.1).
type avatarChatRow struct {
	SenderID   string `json:"senderId"`
	MsgSn      int64  `json:"msgSn"`
	MsgTimeSec int64  `json:"msgTimeSec"`
	Visible    int    `json:"visible"`
	UnreadCnt  int    `json:"unreadCnt"`
	MsgType    int    `json:"msgType"`
	Msg        string `json:"msg"`
}

// avatarChatTalk is one room of the talk list (spec 3, chat/roomlist/sync). memberList holds the other member only: the
// client caches the friend's room id when the list has exactly one member.
type avatarChatTalk struct {
	RoomID         string           `json:"roomId"`
	RoomState      int              `json:"roomState"`
	UnreadCnt      int              `json:"unreadCnt"`
	LastMsg        string           `json:"lastMsg"`
	LastMsgType    int              `json:"lastMsgType"`
	LastMsgTimeSec int64            `json:"lastMsgTimeSec"`
	LastMsgUserID  string           `json:"lastMsgUserId"`
	MemberList     []avatarChatUser `json:"memberList"`
}

// avatarChatAvatar is one userList entry of the talk list: the sDataAvatar keys with the types of spec 3, every key present.
// Only the id, name and sex are filled; the rest is empty (the list cell draws a name and a last message, not the figure).
type avatarChatAvatar struct {
	AvatarID                     string `json:"avatarId"`
	Name                         string `json:"name"`
	Gender                       string `json:"gender"`
	Coin                         int    `json:"coin"`
	HeartCount                   int64  `json:"heartCount"`
	HaveHeart                    bool   `json:"haveHeart"`
	HaveHeartBalance             int    `json:"haveHeartBalance"`
	HaveExtraHeartBalance        int    `json:"haveExtraHeartBalance"`
	Country                      string `json:"country"`
	RandVisit                    bool   `json:"randVisit"`
	SType                        string `json:"sType"`
	Deleted                      bool   `json:"deleted"`
	HeadProfile                  string `json:"headProfile"`
	Profile                      string `json:"profile"`
	WholeProfile                 string `json:"wholeProfile"`
	ObsProfileImagePath          string `json:"obsProfileImagePath"`
	ObsWholeBodyProfileImagePath string `json:"obsWholeBodyProfileImagePath"`
	Bg                           string `json:"bg"`
	BadgeIcon                    string `json:"badgeIcon"`
	Skin                         string `json:"skin"`
	Clover                       int    `json:"clover"`
	Intro                        string `json:"intro"`
	Items                        []any  `json:"items"`
	PetProfiles                  []any  `json:"petProfiles"`
}

// avatarChatListed is one room of a talk list before the friend check: the row, the other member and the unread count.
type avatarChatListed struct {
	talk   avatarChatTalk
	friend uint64
	unread int
}

// avatarChatOwns reports whether a 0x0A request is a 1:1 chat command. Lounge Chat sends 1001 (join), 3001 (messages in a
// range) and 3002 (send) as well, so for those the payload decides: a 1:1 create names memberList, a 1:1 send names msg
// and a 1:1 sync names lastMsgSn. 1004, 1006, 1012, 3003 and 3004 are 1:1 only.
func avatarChatOwns(cmd int64, payload json.RawMessage) bool {
	var keys map[string]json.RawMessage
	if json.Unmarshal(payload, &keys) != nil {
		return false
	}
	_, memberList := keys["memberList"]
	_, msg := keys["msg"]
	_, lastSn := keys["lastMsgSn"]
	switch cmd {
	case acCreate:
		return memberList
	case acSend:
		return msg
	case acSync:
		return lastSn
	case acDelete, acSyncRoom, acRoomConfig, acGetMsgs, acAck:
		return true
	}
	return false
}

// handleAvatarChat answers one 1:1 request with the request's txn. Every well-formed request gets a reply, a refused one
// an error envelope, because the scene has no timeout and a missing reply leaves its spinner up. It returns false only
// when the reply cannot be written.
func handleAvatarChat(conn *lockedConn, remote string, txn uint64, cmd int64, payload json.RawMessage, logger *log.Logger) bool {
	aid, bound := sessionAidOf(conn)
	rep, roomID := avatarChatCall(cmd, payload, aid, bound)
	if rep.code == 0 && roomID != 0 && cmd != acDelete {
		avatarChatSeen(aid, roomID)
	}
	frame, err := chatReplyFrame(txn, rep)
	if err != nil {
		logger.Printf("SESSION avchat remote=%s cmd=%d reply err=%v", remote, cmd, err)
		return true
	}
	if _, err := conn.Write(frame); err != nil {
		logger.Printf("SESSION avchat remote=%s cmd=%d reply err=%v", remote, cmd, err)
		return false
	}
	logger.Printf("AVCHAT cmd=%d aid=%d room=%d result=%d", cmd, aid, roomID, rep.code)
	sendLoungePushes(rep.pushes, logger)
	return true
}

// avatarChatCall runs one 1:1 command for the session's account aid (bound: whether the session has one). It returns the
// reply and the room id the request named (0 when none), for the log line.
func avatarChatCall(cmd int64, payload json.RawMessage, aid uint64, bound bool) (tcReply, int64) {
	if !bound {
		return avatarChatErr(acInternal), 0
	}
	var req avatarChatReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return avatarChatErr(acRefused), 0
	}
	switch cmd {
	case acCreate:
		return avatarChatOpen(req.MemberList, aid)
	case acSyncRoom:
		return avatarChatRoomInfo(req.RoomID, aid)
	case acGetMsgs:
		return avatarChatRange(req.RoomID, req.StartMsgSn, req.EndMsgSn, aid)
	case acSend:
		return avatarChatSend(req.RoomID, req.MsgType, req.Msg, aid)
	case acSync:
		// The sync is answered empty: the history comes from 3003, and the client's gap fetch needs nothing else (spec 7.1).
		return avatarChatMemberReply(req.RoomID, aid, map[string]any{"msgList": []avatarChatRow{}})
	case acAck:
		return avatarChatAck(req.RoomID, req.MsgSn, aid)
	case acDelete:
		return avatarChatDelete(req.RoomID, aid)
	case acRoomConfig:
		// Accepted, nothing stored: the push-on flag is not kept (spec 7.2).
		return avatarChatMemberReply(req.RoomID, aid, map[string]any{})
	}
	return avatarChatErr(acInternal), 0
}

func avatarChatErr(code int) tcReply {
	return tcReply{code: code, result: map[string]any{}}
}

// avatarChatAccount reports whether id is a real account's aid. The synthetic friend (store.FriendAID) has none.
func avatarChatAccount(id string) bool {
	if id == store.FriendAID {
		return false
	}
	_, ok := store.AccountByAvatarID(id)
	return ok
}

// avatarChatOpen opens the room of the pair in memberList (1001) for aid, creating it on first use. The pair is the
// requester and the other entry; both must be real accounts and accepted friends. The same pair gets the same id in
// either order.
func avatarChatOpen(members []avatarChatUser, aid uint64) (tcReply, int64) {
	self := strconv.FormatUint(aid, 10)
	if len(members) != 2 {
		return avatarChatErr(acRefused), 0
	}
	var friend string
	switch self {
	case members[0].UserID:
		friend = members[1].UserID
	case members[1].UserID:
		friend = members[0].UserID
	}
	fa, err := strconv.ParseUint(friend, 10, 64)
	if err != nil || fa == 0 || strconv.FormatUint(fa, 10) != friend || fa == aid ||
		!avatarChatAccount(self) || !avatarChatAccount(friend) || !store.AcceptedFriends(self, friend) {
		return avatarChatErr(acRefused), 0
	}
	pair := [2]uint64{min(aid, fa), max(aid, fa)}
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	id, ok := avatarChatPairs[pair]
	if !ok {
		if avatarChatRoomCount(aid) >= avatarChatRoomsMax || avatarChatRoomCount(fa) >= avatarChatRoomsMax {
			return avatarChatErr(acRefused), 0
		}
		if avatarChatNext > CherryChatRoomLast {
			return avatarChatErr(acInternal), 0
		}
		id = avatarChatNext
		avatarChatNext++
		avatarChatRooms[id] = &avatarChatRoom{a: pair[0], b: pair[1]}
		avatarChatPairs[pair] = id
		if err := avatarChatSaveLocked(); err != nil {
			// Not persisted, so the id must not be handed out: take the room back and refuse the open.
			avatarChatNext--
			delete(avatarChatRooms, id)
			delete(avatarChatPairs, pair)
			return avatarChatErr(acInternal), 0
		}
	}
	return tcReply{result: map[string]any{"roomId": strconv.FormatInt(id, 10)}}, id
}

// avatarChatSaveLocked writes the registry: every room's id, members, newest serial, acks and messages, and the next room
// id. Caller holds avatarChatMu.
func avatarChatSaveLocked() error {
	book := store.ChatRoomBook{NextID: avatarChatNext}
	for _, id := range slices.Sorted(maps.Keys(avatarChatRooms)) {
		r := avatarChatRooms[id]
		a, b := strconv.FormatUint(r.a, 10), strconv.FormatUint(r.b, 10)
		cr := store.ChatRoom{ID: id, Aids: [2]string{a, b}, Ceil: r.lastSn, Acks: map[string]int64{a: r.ackA, b: r.ackB}}
		if len(r.hid) > 0 {
			cr.Hid = map[string]int64{}
			for aid, sn := range r.hid {
				cr.Hid[strconv.FormatUint(aid, 10)] = sn
			}
		}
		for _, m := range r.msgs {
			cr.Msgs = append(cr.Msgs, store.ChatMsg{Sn: m.sn, Sec: m.sec, Sender: strconv.FormatUint(m.sender, 10), Text: m.text})
		}
		book.Rooms = append(book.Rooms, cr)
	}
	return store.SaveChatRooms(book)
}

// LoadAvatarChat loads the room registry at startup (chatrooms.json; none is an empty registry). A corrupt file, or a record
// outside the room block, is an error and Cherry does not start.
func LoadAvatarChat() error {
	book, err := store.LoadChatRooms()
	if err != nil {
		return err
	}
	return avatarChatRestore(book)
}

// avatarChatRestore replaces the in-memory registry with book. A room's serials resume at its saved newest serial (Ceil),
// and its acks are clamped to that. A file from the chunked registry has no messages and its Ceil is a reserved ceiling,
// so such a room resumes at that ceiling once. It refuses a room outside the id block or below the next id, a repeated
// room or pair, a member that is not an aid in ascending order, an ack for a non-member, and a message that is out of
// order, above the newest serial or from a non-member.
func avatarChatRestore(book store.ChatRoomBook) error {
	next := book.NextID
	if next == 0 {
		next = CherryChatRoomFirst
	}
	if next < CherryChatRoomFirst || next > CherryChatRoomLast+1 {
		return fmt.Errorf("chat room store: next id %d outside the room block", next)
	}
	rooms := map[int64]*avatarChatRoom{}
	pairs := map[[2]uint64]int64{}
	for _, cr := range book.Rooms {
		var m [2]uint64
		for i, s := range cr.Aids {
			v, err := strconv.ParseUint(s, 10, 64)
			if err != nil || v == 0 || strconv.FormatUint(v, 10) != s {
				return fmt.Errorf("chat room %d: bad member %q", cr.ID, s)
			}
			m[i] = v
		}
		if m[0] >= m[1] || cr.ID < CherryChatRoomFirst || cr.ID >= next || cr.Ceil < 0 ||
			rooms[cr.ID] != nil || pairs[m] != 0 {
			return fmt.Errorf("chat room %d: invalid record", cr.ID)
		}
		r := &avatarChatRoom{a: m[0], b: m[1], lastSn: cr.Ceil}
		for _, cm := range cr.Msgs {
			sender, err := strconv.ParseUint(cm.Sender, 10, 64)
			if err != nil || (sender != r.a && sender != r.b) || cm.Sn < 1 || cm.Sn > r.lastSn ||
				(len(r.msgs) > 0 && cm.Sn <= r.msgs[len(r.msgs)-1].sn) {
				return fmt.Errorf("chat room %d: invalid message %d", cr.ID, cm.Sn)
			}
			r.msgs = append(r.msgs, avatarChatMsg{sn: cm.Sn, sec: cm.Sec, sender: sender, text: cm.Text})
		}
		for s, ack := range cr.Acks {
			v, err := strconv.ParseUint(s, 10, 64)
			if err != nil || (v != r.a && v != r.b) {
				return fmt.Errorf("chat room %d: ack for a non-member %q", cr.ID, s)
			}
			*r.ackOf(v) = min(max(ack, 0), r.lastSn)
		}
		for s, sn := range cr.Hid {
			v, err := strconv.ParseUint(s, 10, 64)
			if err != nil || (v != r.a && v != r.b) || sn < 0 || sn > r.lastSn {
				return fmt.Errorf("chat room %d: invalid delete mark for %q", cr.ID, s)
			}
			if r.hid == nil {
				r.hid = map[uint64]int64{}
			}
			r.hid[v] = sn
		}
		rooms[cr.ID] = r
		pairs[m] = cr.ID
	}
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	avatarChatRooms, avatarChatPairs, avatarChatNext = rooms, pairs, next
	return nil
}

// avatarChatRoomCount is how many rooms aid is in (under avatarChatMu). It runs only when a room is created.
func avatarChatRoomCount(aid uint64) int {
	n := 0
	for _, r := range avatarChatRooms {
		if r.a == aid || r.b == aid {
			n++
		}
	}
	return n
}

// avatarChatRoomOf is the room roomID names when aid is one of its members, and the id parsed from roomID (0 when it is
// not a number). Caller holds avatarChatMu.
func avatarChatRoomOf(roomID string, aid uint64) (*avatarChatRoom, int64) {
	id, err := strconv.ParseInt(roomID, 10, 64)
	if err != nil {
		return nil, 0
	}
	r := avatarChatRooms[id]
	if r == nil || (r.a != aid && r.b != aid) {
		return nil, id
	}
	return r, id
}

// avatarChatMemberOf is the 1006 row of aid: its name, and no picture (the spec allows an empty profileUrl).
func avatarChatMemberOf(aid uint64) avatarChatMember {
	return avatarChatMember{UserID: strconv.FormatUint(aid, 10), Nickname: squareAccountName(aid), Friend: true}
}

// avatarChatRoomInfo is the room's sync reply (1006). The payload is never empty (an empty one leaves the spinner up), and
// it names the newest serial and both members, the requester first.
func avatarChatRoomInfo(roomID string, aid uint64) (tcReply, int64) {
	avatarChatMu.Lock()
	r, id := avatarChatRoomOf(roomID, aid)
	var lastSn, offset, lastAck, peerAck int64
	var friend uint64
	if r != nil {
		lastSn, offset, friend = r.lastSn, r.offset(), r.other(aid)
		lastAck, peerAck = *r.ackOf(aid), *r.ackOf(friend)
	}
	avatarChatMu.Unlock()
	if r == nil {
		return avatarChatErr(acRefused), id
	}
	return tcReply{result: map[string]any{
		"groupType": 0, "bgUrl": "", "offsetMsgSn": offset, "lastMsgSn": lastSn, "lastAckSn": lastAck, "unreadCnt": 0,
		"peerAckMsgSn": peerAck, "attribute": "", "pushYn": 1, "msgList": []avatarChatRow{},
		"memberList": []avatarChatMember{avatarChatMemberOf(aid), avatarChatMemberOf(friend)},
	}}, id
}

// avatarChatMemberReply answers a command that only needs the room: result when aid is a member, else a refusal.
func avatarChatMemberReply(roomID string, aid uint64, result map[string]any) (tcReply, int64) {
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	r, id := avatarChatRoomOf(roomID, aid)
	if r == nil {
		return avatarChatErr(acRefused), id
	}
	return tcReply{result: result}, id
}

// avatarChatRowOf is the 3003 row of one stored message.
func avatarChatRowOf(m avatarChatMsg) avatarChatRow {
	return avatarChatRow{
		SenderID: strconv.FormatUint(m.sender, 10), MsgSn: m.sn, MsgTimeSec: m.sec, Visible: 1,
		MsgType: avatarChatTypeText, Msg: m.text,
	}
}

// avatarChatRange is the room's messages with serials in [start, end] (inclusive, spec 1.3), oldest first (3003). An empty
// range is an empty list.
func avatarChatRange(roomID string, start, end int64, aid uint64) (tcReply, int64) {
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	r, id := avatarChatRoomOf(roomID, aid)
	if r == nil {
		return avatarChatErr(acRefused), id
	}
	rows := []avatarChatRow{}
	for _, m := range r.msgs {
		if m.sn >= start && m.sn <= end {
			rows = append(rows, avatarChatRowOf(m))
		}
	}
	return tcReply{result: map[string]any{"msgList": rows}}, id
}

// avatarChatSend stores one text message from aid in the room (3001) and pushes it to the other member. The text is trimmed
// and refused when it is blank, longer than avatarChatTextMax characters or not a text message. The sender is aid, the
// session's account, whatever the request names.
func avatarChatSend(roomID string, msgType int64, msg string, aid uint64) (tcReply, int64) {
	text := strings.TrimSpace(msg)
	name := squareAccountName(aid) // read before the lock: no account lookup runs under avatarChatMu
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	r, id := avatarChatRoomOf(roomID, aid)
	if r == nil || msgType != avatarChatTypeText || text == "" || utf8.RuneCountInString(text) > avatarChatTextMax {
		return avatarChatErr(acRefused), id
	}
	m := avatarChatMsg{sn: r.lastSn + 1, sec: economy.NowFunc().Unix(), sender: aid, text: text}
	// The message is saved before its serial is returned. A failed save keeps the serial taken (memory never goes back, and
	// nothing was sent with it, so the next message skips it) and drops the message again. Its text is never logged.
	kept := r.msgs
	r.lastSn = m.sn
	r.msgs = append(r.msgs, m)
	if over := len(r.msgs) - avatarChatHistoryMax; over > 0 {
		r.msgs = r.msgs[over:]
	}
	if err := avatarChatSaveLocked(); err != nil {
		r.msgs = kept
		return avatarChatErr(acInternal), id
	}
	// A member outside the room gets the banner text with the push; one inside gets the plain 13001 as before.
	to := r.other(aid)
	banner := ""
	if avatarChatBannerOn && !avatarChatInRoomLocked(to, id) {
		banner = avatarChatBanner(name, text)
	}
	return tcReply{
		result: map[string]any{"msgSn": m.sn, "msgTimeSec": m.sec},
		pushes: []loungePush{avatarChatPush(id, to, aid, m.sn, banner)},
	}, id
}

// avatarChatBanner is the arrival banner text, "<sender name>: <text>" cut to avatarChatBannerMax runes. It is Cherry's own
// text (not a client string); the name is the sender's account name, or "cherry" when unknown.
func avatarChatBanner(name, text string) string {
	s := []rune(strings.Join(strings.Fields(name+": "+text), " "))
	if len(s) > avatarChatBannerMax {
		s = s[:avatarChatBannerMax]
	}
	return string(s)
}

// avatarChatSeen marks aid as in room id now (any command of theirs that names the room, except the delete).
func avatarChatSeen(aid uint64, id int64) {
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	avatarChatHere[aid] = avatarChatSeenAt{room: id, at: time.Now()}
}

// avatarChatInRoomLocked reports whether aid was seen in room id within avatarChatHereTTL. Caller holds avatarChatMu.
func avatarChatInRoomLocked(aid uint64, id int64) bool {
	s, ok := avatarChatHere[aid]
	return ok && s.room == id && time.Since(s.at) < avatarChatHereTTL
}

// avatarChatAck records aid's read mark (3004): the room's ack for aid becomes the larger of the old one and msgSn, never
// past lastSn. The unread counts depend on it, so a changed ack is saved at once; an unchanged one writes nothing.
func avatarChatAck(roomID string, msgSn int64, aid uint64) (tcReply, int64) {
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	r, id := avatarChatRoomOf(roomID, aid)
	if r == nil {
		return avatarChatErr(acRefused), id
	}
	p := r.ackOf(aid)
	old := *p
	*p = max(old, min(msgSn, r.lastSn))
	if *p != old {
		if err := avatarChatSaveLocked(); err != nil {
			*p = old
			return avatarChatErr(acInternal), id
		}
	}
	return tcReply{result: map[string]any{}}, id
}

// avatarChatDelete is the edit-mode delete of the talk list (1004): the room leaves aid's list until a message newer than
// now arrives. The other member's list and the room's messages are untouched. Saved like an ack.
func avatarChatDelete(roomID string, aid uint64) (tcReply, int64) {
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	r, id := avatarChatRoomOf(roomID, aid)
	if r == nil {
		return avatarChatErr(acRefused), id
	}
	if r.hid == nil {
		r.hid = map[uint64]int64{}
	}
	old, had := r.hid[aid]
	r.hid[aid] = r.lastSn
	if err := avatarChatSaveLocked(); err != nil {
		if had {
			r.hid[aid] = old
		} else {
			delete(r.hid, aid)
		}
		return avatarChatErr(acInternal), id
	}
	return tcReply{result: map[string]any{}}, id
}

// avatarChatPush is the 13001 push that tells the other member a message is in the room (spec 2.1). banner, when not
// empty, is the top-level content that lets the client show a banner (spec B.4a); the client's chat-scene routing ignores
// it for an open chat with this friend, and an open chat fetches the message with 3003.
func avatarChatPush(roomID int64, to, sender uint64, sn int64, banner string) loungePush {
	push := map[string]any{
		"catg":  "chat",
		"msgid": strconv.FormatInt(roomID, 10) + "-" + strconv.FormatInt(sn, 10),
		"params": map[string]any{
			"command": avatarChatPushCommand,
			"userId":  strconv.FormatUint(to, 10),
			"payload": map[string]any{"msgSn": sn, "sndr": strconv.FormatUint(sender, 10)},
		},
	}
	if banner != "" {
		push["content"] = banner
	}
	body, _ := json.Marshal(push) // plain values: cannot fail
	return loungePush{to: to, body: body}
}

// avatarChatListRows is aid's talk list before the response is built: the rooms aid is in that have a message, that aid
// has not deleted since their newest message, whose other member is still an account and an accepted friend, by room id.
// The rooms are read under avatarChatMu and the account and friend checks run after it.
func avatarChatListRows(aid uint64) []avatarChatListed {
	var cands []avatarChatListed
	avatarChatMu.Lock()
	for _, id := range slices.Sorted(maps.Keys(avatarChatRooms)) {
		r := avatarChatRooms[id]
		if (r.a != aid && r.b != aid) || len(r.msgs) == 0 {
			continue
		}
		if sn, deleted := r.hid[aid]; deleted && r.lastSn <= sn {
			continue
		}
		friend, ack := r.other(aid), *r.ackOf(aid)
		unread := 0
		for _, m := range r.msgs {
			if m.sender == friend && m.sn > ack {
				unread++
			}
		}
		last := r.msgs[len(r.msgs)-1]
		cands = append(cands, avatarChatListed{talk: avatarChatTalk{
			RoomID: strconv.FormatInt(id, 10), UnreadCnt: unread, LastMsg: last.text, LastMsgType: avatarChatTypeText,
			LastMsgTimeSec: last.sec, LastMsgUserID: strconv.FormatUint(last.sender, 10),
			MemberList: []avatarChatUser{{UserID: strconv.FormatUint(friend, 10)}},
		}, friend: friend, unread: unread})
	}
	avatarChatMu.Unlock()
	self := strconv.FormatUint(aid, 10)
	var rows []avatarChatListed
	for _, c := range cands {
		if friend := strconv.FormatUint(c.friend, 10); avatarChatAccount(friend) && store.AcceptedFriends(self, friend) {
			rows = append(rows, c)
		}
	}
	return rows
}

// avatarChatAvatarOf is the userList entry of a friend (see avatarChatAvatar). The friend's account is read by aid.
func avatarChatAvatarOf(friend uint64) avatarChatAvatar {
	acc, _ := store.AccountByAvatarID(strconv.FormatUint(friend, 10))
	return avatarChatAvatar{
		AvatarID: strconv.FormatUint(friend, 10), Name: acc.Name, Gender: acc.Gender, Items: []any{}, PetProfiles: []any{},
	}
}

// avatarChatRoomList is the talk-list reply for aid (avatarChatListRows): one row per room, the friend alone in memberList,
// and one userList entry per row. A room with no rows is an empty list, never null.
func avatarChatRoomList(aid uint64) map[string]any {
	rows := []avatarChatTalk{}
	users := []avatarChatAvatar{}
	var newest int64
	for _, c := range avatarChatListRows(aid) {
		rows = append(rows, c.talk)
		users = append(users, avatarChatAvatarOf(c.friend))
		newest = max(newest, c.talk.LastMsgTimeSec)
	}
	return map[string]any{"roomList": rows, "userList": users, "lastMsgTimeSec": newest}
}

// AvatarChatUnread is the CHAT badge of aid (badge/infos): the unread messages over the rooms its talk list shows. Zero
// while the talk list is switched off, so the badge never counts a room the list does not show. Call it with no account
// lock held.
func AvatarChatUnread(aid uint64) int {
	if !avatarChatListOn {
		return 0
	}
	n := 0
	for _, c := range avatarChatListRows(aid) {
		n += c.unread
	}
	return n
}

// HandleAvatarChatRoomList serves GET /v4/chat/roomlist/sync/<ts>/<n>: the talk list of the AV_AUTH account (never an aid from
// the path). The client caches each friend's room id from it, so CHAT opens the room without 1001. Without a session, or
// with avatarChatListOn off, the list is empty.
func HandleAvatarChatRoomList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	list := map[string]any{"roomList": []avatarChatTalk{}, "userList": []avatarChatAvatar{}, "lastMsgTimeSec": 0}
	if snap, ok := store.AccountForRequest(r); ok && avatarChatListOn {
		if aid, err := strconv.ParseUint(snap.Aid, 10, 64); err == nil {
			list = avatarChatRoomList(aid)
		}
	}
	httpx.WriteObj(w, map[string]any{"result": list})
}
