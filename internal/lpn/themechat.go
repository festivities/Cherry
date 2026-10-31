package lpn

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Lounge Chat: the theme chat on the session channel (:10123). The client sends one request per command with op 0x0A
// (NaSessionManager::sendThemeChatMsg 0x1ba7ea8); the reply is op 0x1A with the same txn, and a push is op 0x1B with raw
// JSON. Layouts and reply rules come from the client's static research (lounge-chat-spec.md, sections 3 and 4). Rooms and
// messages live in memory: the client keeps nothing for the theme chat, and a restart empties every room.

const (
	loOpThemeChatReq   = 0x0A // request: BE16(L) || T (L bytes, never parsed) || JSON {version, command, userId, country, lang, payload, deviceType}
	loOpThemeChatReply = 0x1A // reply: BE16(200) || JSON {resultCode, resultMsg, payload}
	themeChatStatusOK  = 200  // parseRpcMessage needs the 2-byte status 200 (spec 3.3)
	themeChatPageMax   = 50   // messages in one 3001 reply and in the join reply (spec 8)
)

// Request numbers (spec 4.1).
const (
	tcJoin     = 1001
	tcLeave    = 1002
	tcRoomList = 1003
	tcCatgList = 1101
	tcMembers  = 2001
	tcAmJoined = 2002
	tcGetMsgs  = 3001
	tcSend     = 3002
)

// Result codes the client has a branch for (spec 4.3). 401 is "room not found" and 502 "room full". Cherry refuses a
// message with 402, which no branch names: a 3002 with it marks the bubble failed, so the user sees it and can retry
// (401 and 501 would leave the bubble pending). 500 is a generic failure: no bound session, or an unknown command.
const (
	tcOK       = 0
	tcNoRoom   = 401
	tcFull     = 502
	tcRefused  = 402
	tcInternal = 500
)

// Message types the scene shows (spec 5.1): 0 is text, 205 a sticker whose msg is the sticker key. Any other type is refused.
const (
	tcTypeText    = 0
	tcTypeSticker = 205
)

// errThemeChatPayload is a payload that does not match its command: the request is dropped without a reply.
var errThemeChatPayload = errors.New("payload malformed")

// loungeMember is one member of a room: the account, and the name and profile picture its messages carry.
type loungeMember struct {
	aid      uint64
	nickname string
	profile  string // profileUrl, from loungeProfileURL when the member joined
}

// loungeMessage is one stored message (sDataChatMsg, spec 4.4).
type loungeMessage struct {
	sn      int64
	sec     int64
	msgType int
	sender  loungeMember
	text    string
}

// loungeRoom is the state of one room: its members in join order, its newest messages (oldest first) and the newest serial.
type loungeRoom struct {
	members []loungeMember
	msgs    []loungeMessage
	lastSn  int64 // msgSn of the newest message, 0 before the first; the next message takes lastSn+1
}

// loungeMu guards every room and loungeIn, which maps a member's aid to the room it joined. Pushes are built under the
// lock and written after it is released (sendLoungePushes), so a slow peer never holds it.
var (
	loungeMu    sync.Mutex
	loungeRooms = map[int64]*loungeRoom{}
	loungeIn    = map[uint64]int64{}
)

// loungePush is one 0x1B push: the JSON for one member's session.
type loungePush struct {
	to   uint64
	body []byte
}

// tcReply is a command's answer: the result code, the payload object and the pushes to send once the reply is written.
type tcReply struct {
	code   int
	result any
	pushes []loungePush
}

// Reply rows. Every list is an array of objects with the key types of spec 4.2 and 4.4; a nil slice would go out as null,
// so the builders always make one.
type tcCatgRow struct {
	CatgID          int64  `json:"catgId"`
	CatgName        string `json:"catgName"`
	CatgIconImg     string `json:"catgIconImg"`
	OnlineMemberCnt int    `json:"onlineMemberCnt"`
}

type tcRoomRow struct {
	RoomID            int64  `json:"roomId"`
	CatgID            int64  `json:"catgId"`
	CatgName          string `json:"catgName"`
	RoomName          string `json:"roomName"`
	CatgIconImg       string `json:"catgIconImg"`
	LimitMemberCnt    int    `json:"limitMemberCnt"`
	OnlineMemberCnt   int    `json:"onlineMemberCnt"`
	LastMsgTimeSec    int64  `json:"lastMsgTimeSec"`
	CreateRoomTimeSec int64  `json:"createRoomTimeSec"`
}

type tcJoinReply struct {
	RoomName        string     `json:"roomName"`
	BgImg           string     `json:"bgImg"`
	FontColor       string     `json:"fontColor"`
	OnlineMemberCnt int        `json:"onlineMemberCnt"`
	LastMsgSn       int64      `json:"lastMsgSn"`
	MsgList         []tcMsgRow `json:"msgList"`
}

type tcMemberRow struct {
	MemberID   string `json:"memberId"`
	Nickname   string `json:"nickname"`
	ProfileURL string `json:"profileUrl"`
	ProfileBg  string `json:"profileBg"`
}

type tcMsgRow struct {
	SenderID   string `json:"senderId"`
	Nickname   string `json:"nickname"`
	ProfileURL string `json:"profileUrl"`
	ProfileBg  string `json:"profileBg"`
	MsgSn      int64  `json:"msgSn"`
	MsgTimeSec int64  `json:"msgTimeSec"`
	MsgType    int    `json:"msgType"`
	Msg        string `json:"msg"`
}

// parseThemeChat splits a request body into its command and payload. The body is BE16(L) || T || JSON: T is skipped
// and never read. Trailing NULs are trimmed, as in the sendInit body. The sender is not read here: it is the session's
// account (sessionAidOf), never the envelope's userId.
func parseThemeChat(body []byte) (int64, json.RawMessage, error) {
	if len(body) < 2 {
		return 0, nil, errors.New("body too short")
	}
	n := 2 + int(binary.BigEndian.Uint16(body))
	if len(body) < n {
		return 0, nil, errors.New("T runs past the body")
	}
	var env struct {
		Command *int64          `json:"command"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(bytes.TrimRight(body[n:], "\x00"), &env); err != nil {
		return 0, nil, errors.New("envelope is not JSON")
	}
	if env.Command == nil || len(env.Payload) == 0 || env.Payload[0] != '{' {
		return 0, nil, errors.New("command or payload missing")
	}
	return *env.Command, env.Payload, nil
}

// handleThemeChat answers one request on conn with the request's txn. A malformed request is logged and dropped: no
// reply, and the link stays open for everyone. It returns false only when the reply cannot be written, and the caller
// then closes the link, as it does for the configure reply.
func handleThemeChat(conn *lockedConn, remote string, txn uint64, body []byte, logger *log.Logger) bool {
	cmd, payload, err := parseThemeChat(body)
	if err != nil {
		logger.Printf("SESSION themechat remote=%s dropped len=%d: %v", remote, len(body), err)
		return true
	}
	aid, bound := sessionAidOf(conn)
	rep, err := themeChatCall(cmd, payload, aid, bound)
	if err != nil {
		logger.Printf("SESSION themechat remote=%s cmd=%d dropped: %v", remote, cmd, err)
		return true
	}
	reply, err := json.Marshal(map[string]any{"resultCode": rep.code, "resultMsg": "", "payload": rep.result})
	if err != nil {
		logger.Printf("SESSION themechat remote=%s cmd=%d reply err=%v", remote, cmd, err)
		return true
	}
	frame := binary.BigEndian.AppendUint16(nil, themeChatStatusOK)
	frame = append(frame, reply...)
	if _, err := conn.Write(buildLOFrame(loOpThemeChatReply, txn, frame)); err != nil {
		logger.Printf("SESSION themechat remote=%s cmd=%d reply err=%v", remote, cmd, err)
		return false
	}
	if rep.code != tcOK {
		logger.Printf("SESSION themechat remote=%s cmd=%d result=%d", remote, cmd, rep.code)
	}
	sendLoungePushes(rep.pushes, logger)
	return true
}

// themeChatCall runs one command for the account aid (bound: whether the session has one). An error means the payload
// does not match the command.
func themeChatCall(cmd int64, payload json.RawMessage, aid uint64, bound bool) (tcReply, error) {
	switch cmd {
	case tcCatgList:
		return loungeCatgList(), nil
	case tcRoomList:
		var p struct {
			CatgID *int64 `json:"catgId"`
		}
		if err := json.Unmarshal(payload, &p); err != nil || p.CatgID == nil {
			return tcReply{}, errThemeChatPayload
		}
		return loungeRoomList(*p.CatgID), nil
	case tcJoin:
		var p struct {
			RoomID *int64 `json:"roomId"`
		}
		if err := json.Unmarshal(payload, &p); err != nil || p.RoomID == nil {
			return tcReply{}, errThemeChatPayload
		}
		return loungeJoin(*p.RoomID, aid, bound), nil
	case tcLeave, tcAmJoined:
		// The client ignores both replies (spec 4.3): leave only needs the room, and am-I-joined is answered empty.
		var p struct {
			RoomID *int64 `json:"roomId"`
		}
		if err := json.Unmarshal(payload, &p); err != nil || p.RoomID == nil {
			return tcReply{}, errThemeChatPayload
		}
		if cmd == tcAmJoined {
			return tcReply{result: map[string]any{}}, nil
		}
		return loungeLeave(*p.RoomID, aid), nil
	case tcMembers:
		var p struct {
			RoomID *int64 `json:"roomId"`
		}
		if err := json.Unmarshal(payload, &p); err != nil || p.RoomID == nil {
			return tcReply{}, errThemeChatPayload
		}
		return loungeMembers(*p.RoomID, aid, bound), nil
	case tcGetMsgs:
		var p struct {
			RoomID  *int64 `json:"roomId"`
			StartSn *int64 `json:"startMsgSn"`
			EndSn   *int64 `json:"endMsgSn"`
		}
		if err := json.Unmarshal(payload, &p); err != nil || p.RoomID == nil || p.StartSn == nil || p.EndSn == nil {
			return tcReply{}, errThemeChatPayload
		}
		return loungeGetMsgs(*p.RoomID, *p.StartSn, *p.EndSn), nil
	case tcSend:
		var p struct {
			RoomID  *int64  `json:"roomId"`
			MsgType *int64  `json:"msgType"`
			Msg     *string `json:"msg"`
		}
		if err := json.Unmarshal(payload, &p); err != nil || p.RoomID == nil || p.MsgType == nil || p.Msg == nil {
			return tcReply{}, errThemeChatPayload
		}
		return loungeSend(*p.RoomID, *p.MsgType, *p.Msg, aid, bound), nil
	}
	return tcReply{code: tcInternal, result: map[string]any{}}, nil
}

// loungeRoomDef is the room roomID in the data file.
func loungeRoomDef(roomID int64) (economy.LoungeRoom, bool) {
	for _, rd := range economy.ActiveLounge().Rooms {
		if rd.RoomID == roomID {
			return rd, true
		}
	}
	return economy.LoungeRoom{}, false
}

// loungeRoomLocked is the state of roomID, created on first use (under loungeMu).
func loungeRoomLocked(roomID int64) *loungeRoom {
	r := loungeRooms[roomID]
	if r == nil {
		r = &loungeRoom{}
		loungeRooms[roomID] = r
	}
	return r
}

// loungeMemberOf is aid's entry in r (under loungeMu).
func loungeMemberOf(r *loungeRoom, aid uint64) (loungeMember, bool) {
	i := slices.IndexFunc(r.members, func(m loungeMember) bool { return m.aid == aid })
	if i < 0 {
		return loungeMember{}, false
	}
	return r.members[i], true
}

// loungeLeaveLocked takes aid out of the room it joined, if any, and returns the pushes for the room's other members
// (under loungeMu).
func loungeLeaveLocked(aid uint64) []loungePush {
	roomID, ok := loungeIn[aid]
	if !ok {
		return nil
	}
	delete(loungeIn, aid)
	r := loungeRooms[roomID]
	r.members = slices.DeleteFunc(r.members, func(m loungeMember) bool { return m.aid == aid })
	return loungePushesLocked(roomID, r, aid, r.lastSn, tcTypeText)
}

// loungePushesLocked builds the 0x1B push for every member of r except actor (under loungeMu). The push names the room,
// the newest serial, the type and sender of that message, and the member count. A member change pushes the newest serial
// with type 0 and the actor as sender: the receiving scene updates its count, and fetches the serial only when it is new.
func loungePushesLocked(roomID int64, r *loungeRoom, actor uint64, sn int64, msgType int) []loungePush {
	body, _ := json.Marshal(map[string]any{ // plain values: cannot fail
		"catg": "tchat",
		"params": map[string]any{"payload": map[string]any{
			"roomId": roomID, "msgSn": sn, "msgType": msgType,
			"senderId": strconv.FormatUint(actor, 10), "onlineMemberCnt": len(r.members),
		}},
	})
	var out []loungePush
	for _, m := range r.members {
		if m.aid != actor {
			out = append(out, loungePush{to: m.aid, body: body})
		}
	}
	return out
}

// sendLoungePushes writes the pushes once the lock is released. A member with no session gets nothing (sessionPush
// reports it unsent), and a failed write is logged while the other members still get theirs.
func sendLoungePushes(pushes []loungePush, logger *log.Logger) {
	for _, p := range pushes {
		if _, err := sessionPush(p.to, loOpPush, p.body); err != nil {
			logger.Printf("SESSION themechat push to=%d err=%v", p.to, err)
		}
	}
}

// themeChatUnbound removes the members whose session ended (the aids unbindSession returned) from their rooms, unless a
// newer session of the account is bound already, and tells the rooms' other members the new count.
func themeChatUnbound(aids []uint64, logger *log.Logger) {
	var pushes []loungePush
	loungeMu.Lock()
	for _, aid := range aids {
		if !SessionOnline(aid) {
			pushes = append(pushes, loungeLeaveLocked(aid)...)
		}
	}
	loungeMu.Unlock()
	sendLoungePushes(pushes, logger)
}

// loungeCatgList is the category list (1101): the one category, with the members of all its rooms.
func loungeCatgList() tcReply {
	l := economy.ActiveLounge()
	loungeMu.Lock()
	cnt := 0
	for _, rd := range l.Rooms {
		if r := loungeRooms[rd.RoomID]; r != nil {
			cnt += len(r.members)
		}
	}
	loungeMu.Unlock()
	return tcReply{result: map[string]any{"catgList": []tcCatgRow{{CatgID: l.CatgID, CatgName: l.CatgName, OnlineMemberCnt: cnt}}}}
}

// loungeRowLocked is the list row of one room (under loungeMu).
func loungeRowLocked(l *economy.Lounge, rd economy.LoungeRoom) tcRoomRow {
	row := tcRoomRow{RoomID: rd.RoomID, CatgID: l.CatgID, CatgName: l.CatgName, RoomName: rd.RoomName, LimitMemberCnt: rd.LimitMemberCnt}
	if r := loungeRooms[rd.RoomID]; r != nil {
		row.OnlineMemberCnt = len(r.members)
		if n := len(r.msgs); n > 0 {
			row.LastMsgTimeSec = r.msgs[n-1].sec
		}
	}
	return row
}

// loungeRoomList is the room list (1003) of a category. catgId 0 is the hot list: the same rooms, busiest first. Any other
// unknown id lists nothing; the client's text for an error there would read as a fault.
func loungeRoomList(catgID int64) tcReply {
	l := economy.ActiveLounge()
	rows := []tcRoomRow{}
	if catgID == l.CatgID || catgID == 0 {
		loungeMu.Lock()
		for _, rd := range l.Rooms {
			rows = append(rows, loungeRowLocked(l, rd))
		}
		loungeMu.Unlock()
		if catgID == 0 {
			slices.SortStableFunc(rows, func(a, b tcRoomRow) int { return b.OnlineMemberCnt - a.OnlineMemberCnt })
		}
	}
	return tcReply{result: map[string]any{"roomList": rows}}
}

// loungeProfileURL is the profile picture path a member's rows carry: the account's saved picture ("" when it has none),
// with the leading slash trimmed. The client prefixes its static base to a path without a scheme, so "/lineplay/pr/x"
// would reach it as "//lineplay/pr/x"; the Go mux answers that with a 307 to "/lineplay/pr/x".
func loungeProfileURL(aid uint64) string {
	acc, ok := store.AccountByAvatarID(strconv.FormatUint(aid, 10))
	if !ok {
		return ""
	}
	return strings.TrimPrefix(acc.ProfileImage.Image, "/")
}

// msgRows converts stored messages to reply rows (never nil). A message carries the picture its sender had when joining
// (loungeMember.profile); the background stays empty.
func msgRows(msgs []loungeMessage) []tcMsgRow {
	rows := make([]tcMsgRow, 0, len(msgs))
	for _, m := range msgs {
		rows = append(rows, tcMsgRow{
			SenderID: strconv.FormatUint(m.sender.aid, 10), Nickname: m.sender.nickname, ProfileURL: m.sender.profile,
			MsgSn: m.sn, MsgTimeSec: m.sec, MsgType: m.msgType, Msg: m.text,
		})
	}
	return rows
}

// loungeJoin enters aid into roomID (1001). A member of another room leaves that room first, and the other members of
// both rooms get the new count. The reply carries the room's name, its count, the newest serial and the newest messages.
func loungeJoin(roomID int64, aid uint64, bound bool) tcReply {
	rd, ok := loungeRoomDef(roomID)
	if !ok {
		return tcReply{code: tcNoRoom, result: map[string]any{}}
	}
	if !bound {
		return tcReply{code: tcInternal, result: map[string]any{}}
	}
	name, pic := squareAccountName(aid), loungeProfileURL(aid)
	loungeMu.Lock()
	defer loungeMu.Unlock()
	r := loungeRoomLocked(roomID)
	var pushes []loungePush
	if loungeIn[aid] != roomID {
		if len(r.members) >= rd.LimitMemberCnt {
			return tcReply{code: tcFull, result: map[string]any{}}
		}
		pushes = loungeLeaveLocked(aid)
		r.members = append(r.members, loungeMember{aid: aid, nickname: name, profile: pic})
		loungeIn[aid] = roomID
		pushes = append(pushes, loungePushesLocked(roomID, r, aid, r.lastSn, tcTypeText)...)
	}
	tail := r.msgs
	if len(tail) > themeChatPageMax {
		tail = tail[len(tail)-themeChatPageMax:]
	}
	return tcReply{result: tcJoinReply{
		RoomName: rd.RoomName, OnlineMemberCnt: len(r.members), LastMsgSn: r.lastSn, MsgList: msgRows(tail),
	}, pushes: pushes}
}

// loungeLeave takes aid out of roomID (1002). A room the member is not in changes nothing.
func loungeLeave(roomID int64, aid uint64) tcReply {
	loungeMu.Lock()
	var pushes []loungePush
	if loungeIn[aid] == roomID {
		pushes = loungeLeaveLocked(aid)
	}
	loungeMu.Unlock()
	return tcReply{result: map[string]any{}, pushes: pushes}
}

// loungeMembers is the member list of roomID (2001). The viewer's own entry is always in it, because the client's send
// path needs it, even when the viewer is not a member.
func loungeMembers(roomID int64, aid uint64, bound bool) tcReply {
	if _, ok := loungeRoomDef(roomID); !ok {
		return tcReply{code: tcNoRoom, result: map[string]any{}}
	}
	if !bound {
		return tcReply{code: tcInternal, result: map[string]any{}}
	}
	name, pic := squareAccountName(aid), loungeProfileURL(aid)
	rows := []tcMemberRow{}
	self := false
	loungeMu.Lock()
	if r := loungeRooms[roomID]; r != nil {
		for _, m := range r.members {
			rows = append(rows, tcMemberRow{MemberID: strconv.FormatUint(m.aid, 10), Nickname: m.nickname, ProfileURL: m.profile})
			self = self || m.aid == aid
		}
	}
	loungeMu.Unlock()
	if !self {
		rows = append(rows, tcMemberRow{MemberID: strconv.FormatUint(aid, 10), Nickname: name, ProfileURL: pic})
	}
	return tcReply{result: map[string]any{"memberList": rows}}
}

// loungeGetMsgs is the messages of roomID with serials in [start, end], oldest first, at most themeChatPageMax. An empty
// range is an empty list: the payload stays non-empty, and an empty 3001 would make the client retry five times.
func loungeGetMsgs(roomID, start, end int64) tcReply {
	if _, ok := loungeRoomDef(roomID); !ok {
		return tcReply{code: tcNoRoom, result: map[string]any{}}
	}
	var msgs []loungeMessage
	loungeMu.Lock()
	if r := loungeRooms[roomID]; r != nil {
		for _, m := range r.msgs {
			if m.sn >= start && m.sn <= end && len(msgs) < themeChatPageMax {
				msgs = append(msgs, m)
			}
		}
	}
	loungeMu.Unlock()
	return tcReply{result: map[string]any{"msgList": msgRows(msgs)}}
}

// stickerSetSize is how many stickers the client has in set t: the sticker key is ls_<t>_<i> with 1 <= i <= size (the
// client's table, lounge-sticker-init.md 1.2). A set the client does not have has size 0.
func stickerSetSize(t int) int {
	switch t {
	case 10:
		return 350
	case 20:
		return 88
	case 30:
		return 96
	case 40, 50:
		return 16
	case 60, 70:
		return 40
	}
	return 0
}

// canonicalNum is the number s spells when s is written the way Cherry writes one ("10", not "+10", "010" or "1x"), else 0.
func canonicalNum(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || strconv.Itoa(n) != s {
		return 0
	}
	return n
}

// validStickerKey reports whether key is ls_<t>_<i> for a set t and an index i the client has.
func validStickerKey(key string) bool {
	rest, ok := strings.CutPrefix(key, "ls_")
	if !ok {
		return false
	}
	setKey, idxKey, ok := strings.Cut(rest, "_")
	if !ok {
		return false
	}
	set, idx := canonicalNum(setKey), canonicalNum(idxKey)
	return idx >= 1 && idx <= stickerSetSize(set)
}

// loungeSend stores one message from aid in roomID (3002) and pushes it to the room's other members, never to the sender.
// It is refused with 402 when the type is neither text nor sticker, the text is blank or longer than maxTextChars
// characters, a sticker's key is not one the client has, or the sender is not in the room. The oldest messages go once
// the room holds historyLimit.
func loungeSend(roomID, msgType int64, text string, aid uint64, bound bool) tcReply {
	if !bound {
		return tcReply{code: tcInternal, result: map[string]any{}}
	}
	l := economy.ActiveLounge()
	refused := tcReply{code: tcRefused, result: map[string]any{}}
	if _, ok := loungeRoomDef(roomID); !ok {
		return refused
	}
	if (msgType != tcTypeText && msgType != tcTypeSticker) || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > l.MaxTextChars {
		return refused
	}
	if msgType == tcTypeSticker && !validStickerKey(text) {
		return refused
	}
	loungeMu.Lock()
	defer loungeMu.Unlock()
	r := loungeRooms[roomID]
	if r == nil || loungeIn[aid] != roomID {
		return refused
	}
	sender, _ := loungeMemberOf(r, aid)
	r.lastSn++
	msg := loungeMessage{sn: r.lastSn, sec: economy.NowFunc().Unix(), msgType: int(msgType), sender: sender, text: text}
	r.msgs = append(r.msgs, msg)
	if over := len(r.msgs) - l.HistoryLimit; over > 0 {
		r.msgs = slices.Delete(r.msgs, 0, over)
	}
	return tcReply{
		result: map[string]any{"msgSn": msg.sn, "msgTimeSec": msg.sec},
		pushes: loungePushesLocked(roomID, r, aid, msg.sn, msg.msgType),
	}
}
