package lpn

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/store"
)

// 1:1 friend chat: the CHAT button on a friend's profile (client scene 9, NaAvatarChatScene). Requests are op 0x0A on the
// session channel, like Lounge Chat, and get the same 0x1A reply and 0x1B push frames. The layouts and reply rules are the
// slice-1 minimum in avatarchat-spec.md, section 7.
//
// ponytail: memory only. A restart empties every room and message, and the room ids start again, so a room id a client
// kept may name another pair. Every command checks membership, so that id reaches nobody new. Persist rooms and messages
// in the DB with Roadmap 8 storage.

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
	avatarChatPushCommand = 13001
)

// avatarChatMsg is one stored message: the serial, the unix seconds, the sender and the text.
type avatarChatMsg struct {
	sn     int64
	sec    int64
	sender uint64
	text   string
}

// avatarChatRoom is a room between two friends: a < b, the newest messages (oldest first) and the newest serial.
type avatarChatRoom struct {
	a, b   uint64
	msgs   []avatarChatMsg
	lastSn int64
}

// other is the member of r that is not aid.
func (r *avatarChatRoom) other(aid uint64) uint64 {
	if r.a == aid {
		return r.b
	}
	return r.a
}

// avatarChatMu guards every room, the pair index and the next id. Pushes are built under the lock and written after it
// is released (sendLoungePushes), and no store lookup runs under it.
var (
	avatarChatMu    sync.Mutex
	avatarChatRooms = map[int64]*avatarChatRoom{}
	avatarChatPairs = map[[2]uint64]int64{} // members (low, high) -> room id
	avatarChatNext  = CherryChatRoomFirst
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
	case acDelete, acRoomConfig, acAck:
		// Accepted, nothing stored: the client's delete is local, and the push-on flag and read marks are not kept (spec 7.2).
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
	}
	return tcReply{result: map[string]any{"roomId": strconv.FormatInt(id, 10)}}, id
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
	var lastSn int64
	var friend uint64
	if r != nil {
		lastSn, friend = r.lastSn, r.other(aid)
	}
	avatarChatMu.Unlock()
	if r == nil {
		return avatarChatErr(acRefused), id
	}
	return tcReply{result: map[string]any{
		"groupType": 0, "bgUrl": "", "offsetMsgSn": 0, "lastMsgSn": lastSn, "lastAckSn": 0, "unreadCnt": 0,
		"peerAckMsgSn": 0, "attribute": "", "pushYn": 1, "msgList": []avatarChatRow{},
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
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	r, id := avatarChatRoomOf(roomID, aid)
	if r == nil || msgType != avatarChatTypeText || text == "" || utf8.RuneCountInString(text) > avatarChatTextMax {
		return avatarChatErr(acRefused), id
	}
	r.lastSn++
	m := avatarChatMsg{sn: r.lastSn, sec: economy.NowFunc().Unix(), sender: aid, text: text}
	r.msgs = append(r.msgs, m)
	if over := len(r.msgs) - avatarChatHistoryMax; over > 0 {
		r.msgs = slices.Delete(r.msgs, 0, over)
	}
	return tcReply{
		result: map[string]any{"msgSn": m.sn, "msgTimeSec": m.sec},
		pushes: []loungePush{avatarChatPush(id, r.other(aid), aid, m.sn)},
	}, id
}

// avatarChatPush is the 13001 push that tells the other member a message is in the room (spec 2.1). An open chat with this
// friend fetches it with 3003; otherwise the client's banner path takes it.
func avatarChatPush(roomID int64, to, sender uint64, sn int64) loungePush {
	body, _ := json.Marshal(map[string]any{ // plain values: cannot fail
		"catg":  "chat",
		"msgid": strconv.FormatInt(roomID, 10) + "-" + strconv.FormatInt(sn, 10),
		"params": map[string]any{
			"command": avatarChatPushCommand,
			"userId":  strconv.FormatUint(to, 10),
			"payload": map[string]any{"msgSn": sn, "sndr": strconv.FormatUint(sender, 10)},
		},
	})
	return loungePush{to: to, body: body}
}

// avatarChatRoomList is the requester's rooms for the talk list, by room id. A room is listed for its two members only.
func avatarChatRoomList(aid uint64) map[string]any {
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	var ids []int64
	for id, r := range avatarChatRooms {
		if r.a == aid || r.b == aid {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	rows := []avatarChatTalk{}
	var newest int64
	for _, id := range ids {
		r := avatarChatRooms[id]
		row := avatarChatTalk{
			RoomID:     strconv.FormatInt(id, 10),
			MemberList: []avatarChatUser{{UserID: strconv.FormatUint(r.other(aid), 10)}},
		}
		if n := len(r.msgs); n > 0 {
			last := r.msgs[n-1]
			row.LastMsg, row.LastMsgType = last.text, avatarChatTypeText
			row.LastMsgTimeSec, row.LastMsgUserID = last.sec, strconv.FormatUint(last.sender, 10)
			newest = max(newest, last.sec)
		}
		rows = append(rows, row)
	}
	return map[string]any{"roomList": rows, "userList": []any{}, "lastMsgTimeSec": newest}
}

// HandleAvatarChatRoomList serves GET /v4/chat/roomlist/sync/<ts>/<n>: the rooms of the requester, in the talk-list shape.
// The client caches each friend's room id from it, so CHAT opens the room without 1001. Without a session the list is empty.
func HandleAvatarChatRoomList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	// The list is served EMPTY for everyone (aid 0 is in no room). Device 2026-11-11: an app start that got one room in
	// this list crashed within a second (SIGSEGV/SIGABRT, GLThread), so some part of avatarChatTalk is wrong. With an
	// empty list CHAT sends 1001 first, which returns the pair's room anyway. Serve avatarChatRoomList(viewer) again only
	// after the row shape is proven on a device.
	httpx.WriteObj(w, map[string]any{"result": avatarChatRoomList(0)})
}
