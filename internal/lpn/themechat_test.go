package lpn

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// resetLounge empties every room and the join index, so each test starts with no members and no messages.
func resetLounge(t *testing.T) {
	t.Helper()
	loungeMu.Lock()
	defer loungeMu.Unlock()
	loungeRooms = map[int64]*loungeRoom{}
	loungeIn = map[uint64]int64{}
}

// tcSendRaw writes one LO request frame with the given body (BE16(L) || T || JSON) on conn.
func tcSendRaw(t *testing.T, conn net.Conn, txn uint64, body []byte) {
	t.Helper()
	writeSessionFrame(t, conn, buildLOFrame(loOpThemeChatReq, txn, body)[4:])
}

// tcCall sends one command on conn with a nonzero-length T prefix (which is not JSON, so skipping it is tested), and
// returns the reply frame's op, txn and body.
func tcCall(t *testing.T, conn net.Conn, txn uint64, cmd int, payload string) (byte, uint64, []byte) {
	t.Helper()
	prefix := []byte("\x01\x02{T}\x00") // T of 6 bytes
	body := binary.BigEndian.AppendUint16(nil, uint16(len(prefix)))
	body = append(body, prefix...)
	body = append(body, fmt.Sprintf(`{"version":1,"command":%d,"userId":"someone-else","country":"JP","lang":"ja","payload":%s,"deviceType":2001}`, cmd, payload)...)
	tcSendRaw(t, conn, txn, body)
	return readLOPush(t, conn)
}

// tcReplyOf checks a reply body (BE16 200 || JSON) and returns its resultCode and payload.
func tcReplyOf(t *testing.T, body []byte) (int, map[string]any) {
	t.Helper()
	if len(body) < 2 || binary.BigEndian.Uint16(body) != themeChatStatusOK {
		t.Fatalf("reply body %q has no status 200", body)
	}
	var rep struct {
		ResultCode int            `json:"resultCode"`
		Payload    map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(body[2:], &rep); err != nil {
		t.Fatalf("reply JSON: %v", err)
	}
	if rep.Payload == nil {
		t.Fatalf("reply has no payload object: %s", body[2:])
	}
	return rep.ResultCode, rep.Payload
}

// tcPushOf returns the payload object of a tchat push, and fails when the push is not one.
func tcPushOf(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var got struct {
		Catg   string `json:"catg"`
		Params struct {
			Payload map[string]any `json:"payload"`
		} `json:"params"`
	}
	if err := json.Unmarshal(payload, &got); err != nil || got.Catg != "tchat" || got.Params.Payload == nil {
		t.Fatalf("push %s is not a tchat push (err %v)", payload, err)
	}
	return got.Params.Payload
}

// wantNum is a JSON number in a decoded object (encoding/json gives float64).
func wantNum(t *testing.T, m map[string]any, key string, want float64) {
	t.Helper()
	if got, _ := m[key].(float64); got != want {
		t.Fatalf("%s = %v, want %v (payload %v)", key, m[key], want, m)
	}
}

// wantStr is a JSON string in a decoded object.
func wantStr(t *testing.T, m map[string]any, key, want string) {
	t.Helper()
	if got, _ := m[key].(string); got != want {
		t.Fatalf("%s = %q, want %q (payload %v)", key, m[key], want, m)
	}
}

// wantList is the array under key, with n elements (an array, never null).
func wantList(t *testing.T, m map[string]any, key string, n int) []any {
	t.Helper()
	list, ok := m[key].([]any)
	if !ok || len(list) != n {
		t.Fatalf("%s = %v, want an array of %d", key, m[key], n)
	}
	return list
}

// TestThemeChatParseSkipsPrefix: the body is BE16(L) || T || JSON. T is skipped whatever its bytes are, a trailing NUL
// is trimmed, and a body that is not one of these shapes is refused.
func TestThemeChatParseSkipsPrefix(t *testing.T) {
	envelope := []byte(`{"command":1003,"payload":{"catgId":1930000001,"order":2}}`)
	for _, prefix := range [][]byte{[]byte("T"), []byte("\x00{\"command\":9}\x00"), bytes.Repeat([]byte{0xff}, 40)} {
		body := binary.BigEndian.AppendUint16(nil, uint16(len(prefix)))
		body = append(body, prefix...)
		body = append(body, envelope...)
		body = append(body, 0, 0)
		cmd, payload, err := parseThemeChat(body)
		if err != nil || cmd != 1003 || string(payload) != `{"catgId":1930000001,"order":2}` {
			t.Fatalf("prefix %x: cmd=%d payload=%s err=%v", prefix, cmd, payload, err)
		}
	}
	for name, body := range map[string][]byte{
		"one byte":         {0x00},
		"T past the body":  {0x00, 0x09, 'x'},
		"not JSON":         append(binary.BigEndian.AppendUint16(nil, 0), `{"command":`...),
		"no command":       append(binary.BigEndian.AppendUint16(nil, 0), `{"payload":{}}`...),
		"payload is null":  append(binary.BigEndian.AppendUint16(nil, 0), `{"command":1101,"payload":null}`...),
		"payload is array": append(binary.BigEndian.AppendUint16(nil, 0), `{"command":1101,"payload":[1]}`...),
		"command a string": append(binary.BigEndian.AppendUint16(nil, 0), `{"command":"1101","payload":{}}`...),
	} {
		if _, _, err := parseThemeChat(body); err == nil {
			t.Errorf("%s: accepted %q", name, body)
		}
	}
}

// TestThemeChatCategoryAndRoomLists: the category list (1101) and the room list (1003) reply with arrays of objects, and
// the reply op is 0x1A with the request's txn after a request whose T prefix is nonzero.
func TestThemeChatCategoryAndRoomLists(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"tc-9971": {AccessToken: "tc-9971", SessionKey: "sk-tc-9971", Aid: "9971", Name: "Ann"},
	})
	resetLounge(t)
	conn := inviteeSession(t, "sk-tc-9971")
	cat := economy.ActiveLounge()

	op, txn, body := tcCall(t, conn, 41, tcCatgList, `{"":""}`)
	if op != loOpThemeChatReply || txn != 41 {
		t.Fatalf("1101 reply op=%#02x txn=%d, want op 0x1a txn 41", op, txn)
	}
	code, payload := tcReplyOf(t, body)
	if code != tcOK {
		t.Fatalf("1101 resultCode = %d", code)
	}
	row := wantList(t, payload, "catgList", 1)[0].(map[string]any)
	wantNum(t, row, "catgId", float64(cat.CatgID))
	wantStr(t, row, "catgName", cat.CatgName)
	wantStr(t, row, "catgIconImg", "")
	wantNum(t, row, "onlineMemberCnt", 0)

	_, _, body = tcCall(t, conn, 42, tcRoomList, fmt.Sprintf(`{"catgId":%d,"order":2}`, cat.CatgID))
	code, payload = tcReplyOf(t, body)
	if code != tcOK {
		t.Fatalf("1003 resultCode = %d", code)
	}
	rooms := wantList(t, payload, "roomList", len(cat.Rooms))
	first := rooms[0].(map[string]any)
	wantNum(t, first, "roomId", float64(cat.Rooms[0].RoomID))
	wantNum(t, first, "limitMemberCnt", float64(cat.Rooms[0].LimitMemberCnt))
	wantNum(t, first, "lastMsgTimeSec", 0)
	wantNum(t, first, "createRoomTimeSec", 0)
	wantStr(t, first, "roomName", cat.Rooms[0].RoomName)

	// An unknown category lists nothing (not an error, so no client text shows).
	_, _, body = tcCall(t, conn, 43, tcRoomList, `{"catgId":5,"order":2}`)
	if code, payload = tcReplyOf(t, body); code != tcOK {
		t.Fatalf("1003 unknown catgId resultCode = %d", code)
	}
	wantList(t, payload, "roomList", 0)
}

// TestThemeChatRoomFlow: join (1001) with the member list (2001) including the viewer, a count push to the member already
// in the room, a send (3002) that reaches the other member's session as a push and never the sender's, the fetch (3001),
// and a leave (1002) that tells the rest of the room the new count.
func TestThemeChatRoomFlow(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"tc-9981": {AccessToken: "tc-9981", SessionKey: "sk-tc-9981", Aid: "9981", Name: "Ann"},
		"tc-9982": {AccessToken: "tc-9982", SessionKey: "sk-tc-9982", Aid: "9982", Name: "Ben"},
	})
	resetLounge(t)
	ann := inviteeSession(t, "sk-tc-9981")
	ben := inviteeSession(t, "sk-tc-9982")
	room := economy.ActiveLounge().Rooms[0]
	join := fmt.Sprintf(`{"roomId":%d}`, room.RoomID)

	_, _, body := tcCall(t, ann, 1, tcJoin, join)
	code, payload := tcReplyOf(t, body)
	if code != tcOK {
		t.Fatalf("Ann join resultCode = %d", code)
	}
	wantStr(t, payload, "roomName", room.RoomName)
	wantStr(t, payload, "bgImg", "")
	wantNum(t, payload, "onlineMemberCnt", 1)
	wantNum(t, payload, "lastMsgSn", 0)
	wantList(t, payload, "msgList", 0)

	_, _, body = tcCall(t, ann, 2, tcMembers, join)
	_, payload = tcReplyOf(t, body)
	self := wantList(t, payload, "memberList", 1)[0].(map[string]any)
	wantStr(t, self, "memberId", "9981")
	wantStr(t, self, "nickname", "Ann")

	_, _, body = tcCall(t, ben, 3, tcJoin, join)
	if _, payload = tcReplyOf(t, body); payload["onlineMemberCnt"] != float64(2) {
		t.Fatalf("Ben join onlineMemberCnt = %v, want 2", payload["onlineMemberCnt"])
	}
	// Ann is told the new count (type 0, no new serial, Ben as the actor).
	op, _, push := readLOPush(t, ann)
	if op != loOpPush {
		t.Fatalf("Ann got op %#02x, want the 0x1b push", op)
	}
	pushed := tcPushOf(t, push)
	wantNum(t, pushed, "roomId", float64(room.RoomID))
	wantNum(t, pushed, "onlineMemberCnt", 2)
	wantNum(t, pushed, "msgSn", 0)
	wantStr(t, pushed, "senderId", "9982")

	_, _, body = tcCall(t, ann, 4, tcSend, fmt.Sprintf(`{"roomId":%d,"msgType":0,"msg":"hello","msgFilter":0}`, room.RoomID))
	code, payload = tcReplyOf(t, body)
	if code != tcOK || payload["msgSn"] != float64(1) {
		t.Fatalf("send resultCode=%d payload=%v, want 0 and msgSn 1", code, payload)
	}
	if sec, _ := payload["msgTimeSec"].(float64); sec <= 0 {
		t.Fatalf("msgTimeSec = %v", payload["msgTimeSec"])
	}
	// Ben gets the message push; Ann gets no echo.
	op, _, push = readLOPush(t, ben)
	if op != loOpPush {
		t.Fatalf("Ben got op %#02x, want the 0x1b push", op)
	}
	pushed = tcPushOf(t, push)
	wantNum(t, pushed, "roomId", float64(room.RoomID))
	wantNum(t, pushed, "msgSn", 1)
	wantNum(t, pushed, "msgType", 0)
	wantStr(t, pushed, "senderId", "9981")
	wantNum(t, pushed, "onlineMemberCnt", 2)
	assertNoReply(t, ann)

	// A sticker is relayed as sent: the key goes in msg.
	_, _, body = tcCall(t, ann, 5, tcSend, fmt.Sprintf(`{"roomId":%d,"msgType":205,"msg":"ls_10_5","msgFilter":0}`, room.RoomID))
	if code, payload = tcReplyOf(t, body); code != tcOK || payload["msgSn"] != float64(2) {
		t.Fatalf("sticker send resultCode=%d payload=%v", code, payload)
	}
	readLOPush(t, ben)

	_, _, body = tcCall(t, ben, 6, tcGetMsgs, fmt.Sprintf(`{"roomId":%d,"startMsgSn":1,"endMsgSn":2}`, room.RoomID))
	_, payload = tcReplyOf(t, body)
	msgs := wantList(t, payload, "msgList", 2)
	first2 := msgs[0].(map[string]any)
	wantStr(t, first2, "senderId", "9981")
	wantStr(t, first2, "nickname", "Ann")
	wantStr(t, first2, "msg", "hello")
	wantNum(t, first2, "msgSn", 1)
	wantNum(t, first2, "msgType", 0)
	if second := msgs[1].(map[string]any); second["msgType"] != float64(205) {
		t.Fatalf("second message type = %v, want 205", second["msgType"])
	}

	// An empty range is an empty array in a non-empty payload (an empty 3001 makes the client retry).
	_, _, body = tcCall(t, ben, 7, tcGetMsgs, fmt.Sprintf(`{"roomId":%d,"startMsgSn":3,"endMsgSn":9}`, room.RoomID))
	if _, payload = tcReplyOf(t, body); len(payload) == 0 {
		t.Fatal("3001 empty range gave an empty payload")
	}
	wantList(t, payload, "msgList", 0)

	// Ben leaves: Ann is told the count is 1, and the leave reply is an empty object.
	_, _, body = tcCall(t, ben, 8, tcLeave, join)
	if code, payload = tcReplyOf(t, body); code != tcOK || len(payload) != 0 {
		t.Fatalf("leave resultCode=%d payload=%v", code, payload)
	}
	op, _, push = readLOPush(t, ann)
	if op != loOpPush {
		t.Fatalf("Ann got op %#02x after the leave, want the 0x1b push", op)
	}
	wantNum(t, tcPushOf(t, push), "onlineMemberCnt", 1)
	_, _, body = tcCall(t, ann, 9, tcMembers, join)
	_, payload = tcReplyOf(t, body)
	wantList(t, payload, "memberList", 1)
}

// TestThemeChatDisconnectRemovesMember: a member whose session closes leaves its room, and the others are told the count.
func TestThemeChatDisconnectRemovesMember(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"tc-9971": {AccessToken: "tc-9971", SessionKey: "sk-tc-9971", Aid: "9971", Name: "Cy"},
		"tc-9972": {AccessToken: "tc-9972", SessionKey: "sk-tc-9972", Aid: "9972", Name: "Di"},
	})
	resetLounge(t)
	cy := inviteeSession(t, "sk-tc-9971")
	di := inviteeSession(t, "sk-tc-9972")
	room := economy.ActiveLounge().Rooms[1]
	join := fmt.Sprintf(`{"roomId":%d}`, room.RoomID)
	tcCall(t, cy, 1, tcJoin, join)
	tcCall(t, di, 2, tcJoin, join)
	readLOPush(t, cy)

	if err := di.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	op, _, push := readLOPush(t, cy)
	if op != loOpPush {
		t.Fatalf("op %#02x after the disconnect, want the 0x1b push", op)
	}
	wantNum(t, tcPushOf(t, push), "onlineMemberCnt", 1)
	loungeMu.Lock()
	_, stillIn := loungeIn[9972]
	loungeMu.Unlock()
	if stillIn {
		t.Fatal("the disconnected member is still in its room")
	}
}

// TestThemeChatMalformedRequestsDropped: malformed requests get no reply, log nothing with message text, and leave the
// link open; a valid request after them is answered with its own txn.
func TestThemeChatMalformedRequestsDropped(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"tc-9961": {AccessToken: "tc-9961", SessionKey: "sk-tc-9961", Aid: "9961", Name: "Eve"},
	})
	resetLounge(t)
	conn := inviteeSession(t, "sk-tc-9961")
	room := economy.ActiveLounge().Rooms[0].RoomID
	bad := map[uint64][]byte{
		90: binary.BigEndian.AppendUint16(nil, 50),                                          // T longer than the body
		91: append(binary.BigEndian.AppendUint16(nil, 0), `{"command":1101,"payload":{`...), // JSON cut short
		92: append(binary.BigEndian.AppendUint16(nil, 0), fmt.Sprintf(`{"command":%d,"payload":[1]}`, tcJoin)...),
		93: append(binary.BigEndian.AppendUint16(nil, 0), fmt.Sprintf(`{"command":%d,"payload":{"roomId":"x"}}`, tcJoin)...),
		94: append(binary.BigEndian.AppendUint16(nil, 0), fmt.Sprintf(`{"command":%d,"payload":{"roomId":%d,"msgType":"0","msg":"x"}}`, tcSend, room)...),
	}
	for txn := uint64(90); txn <= 94; txn++ {
		tcSendRaw(t, conn, txn, bad[txn])
	}
	_, txn, body := tcCall(t, conn, 95, tcCatgList, `{"":""}`)
	if txn != 95 {
		t.Fatalf("reply txn = %d, want 95 (a malformed request must not get a reply)", txn)
	}
	if code, _ := tcReplyOf(t, body); code != tcOK {
		t.Fatalf("resultCode = %d", code)
	}
}

// TestThemeChatSendRefusals: a send is refused with 402 (an empty payload, so the bubble is marked failed) for a blank
// text, a text over maxTextChars characters, an unsupported type, a room the sender is not in, and an unknown room; a
// refused send pushes nothing. A send from a connection with no bound session gets the generic failure 500.
func TestThemeChatSendRefusals(t *testing.T) {
	resetLounge(t)
	room := economy.ActiveLounge().Rooms[0].RoomID
	limit := economy.ActiveLounge().MaxTextChars
	const sender uint64 = 9951
	if code, _ := joinAs(t, room, sender); code != tcOK {
		t.Fatalf("join resultCode = %d", code)
	}
	ok := func(text string) int {
		return loungeSend(room, tcTypeText, text, sender, true).code
	}
	if got := ok(strings.Repeat("a", limit)); got != tcOK {
		t.Fatalf("a text of %d characters: code %d, want 0", limit, got)
	}
	if got := ok(strings.Repeat("あ", limit)); got != tcOK {
		t.Fatalf("%d multibyte characters: code %d, want 0 (the cap counts characters)", limit, got)
	}
	if got := ok(strings.Repeat("a", limit+1)); got != tcRefused {
		t.Fatalf("a text one over the cap: code %d, want 402", got)
	}
	if got := ok("   "); got != tcRefused {
		t.Fatalf("a blank text: code %d, want 402", got)
	}
	if got := loungeSend(room, 101, "joined", sender, true).code; got != tcRefused {
		t.Fatalf("msgType 101: code %d, want 402", got)
	}
	if got := loungeSend(room, tcTypeText, "hi", 9952, true).code; got != tcRefused {
		t.Fatalf("a sender not in the room: code %d, want 402", got)
	}
	if got := loungeSend(room+1, tcTypeText, "hi", sender, true).code; got != tcRefused {
		t.Fatalf("an unknown room: code %d, want 402", got)
	}
	if got := loungeSend(room, tcTypeText, "hi", sender, false).code; got != tcInternal {
		t.Fatalf("no bound session: code %d, want 500", got)
	}
	loungeMu.Lock()
	defer loungeMu.Unlock()
	if last := loungeRooms[room].lastSn; last != 2 {
		t.Fatalf("lastSn = %d after two accepted messages, want 2", last)
	}
}

// joinAs enters aid into room through the same path as the client's 1001 and returns the reply's result code and payload.
func joinAs(t *testing.T, room int64, aid uint64) (int, tcJoinReply) {
	t.Helper()
	rep := loungeJoin(room, aid, true)
	joined, ok := rep.result.(tcJoinReply)
	if !ok && rep.code == tcOK {
		t.Fatalf("join reply is %T", rep.result)
	}
	return rep.code, joined
}

// TestThemeChatRoomLimitAndHistory: a full room refuses a join with 502 and changes nothing; a member who joins another
// room leaves the first; the newest 200 messages stay (the history limit) and a join shows the newest 50.
func TestThemeChatRoomLimitAndHistory(t *testing.T) {
	resetLounge(t)
	cat := economy.ActiveLounge()
	full, other := cat.Rooms[0].RoomID, cat.Rooms[1].RoomID

	loungeMu.Lock()
	r := loungeRoomLocked(full)
	for i := 0; i < cat.Rooms[0].LimitMemberCnt; i++ {
		r.members = append(r.members, loungeMember{aid: uint64(8000 + i), nickname: "filler"})
	}
	loungeMu.Unlock()
	if code, _ := joinAs(t, full, 9941); code != tcFull {
		t.Fatalf("join a full room: code %d, want 502", code)
	}
	loungeMu.Lock()
	_, in := loungeIn[9941]
	loungeMu.Unlock()
	if in {
		t.Fatal("a refused join left the member in the room")
	}

	// A member moves: the old room loses it and the new one gets it.
	resetLounge(t)
	if code, _ := joinAs(t, full, 9942); code != tcOK {
		t.Fatalf("join: code %d", code)
	}
	if code, _ := joinAs(t, other, 9942); code != tcOK {
		t.Fatalf("second join: code %d", code)
	}
	loungeMu.Lock()
	got, fullSize, otherSize := loungeIn[9942], len(loungeRooms[full].members), len(loungeRooms[other].members)
	loungeMu.Unlock()
	if got != other || fullSize != 0 || otherSize != 1 {
		t.Fatalf("after moving: room %d, sizes %d/%d", got, fullSize, otherSize)
	}

	// History: 201 messages leave serials 2 to 201; the join shows the newest 50 and the count is the newest serial.
	resetLounge(t)
	if code, _ := joinAs(t, other, 9943); code != tcOK {
		t.Fatalf("join: code %d", code)
	}
	for i := 1; i <= cat.HistoryLimit+1; i++ {
		if code := loungeSend(other, tcTypeText, fmt.Sprint(i), 9943, true).code; code != tcOK {
			t.Fatalf("send %d: code %d", i, code)
		}
	}
	if got := loungeGetMsgs(other, 1, 1).result.(map[string]any)["msgList"].([]tcMsgRow); len(got) != 0 {
		t.Fatalf("serial 1 is still stored: %v", got)
	}
	if got := loungeGetMsgs(other, 2, 2).result.(map[string]any)["msgList"].([]tcMsgRow); len(got) != 1 {
		t.Fatalf("serial 2 is missing")
	}
	code, rep := joinAs(t, other, 9944)
	if code != tcOK || rep.LastMsgSn != int64(cat.HistoryLimit+1) || len(rep.MsgList) != themeChatPageMax {
		t.Fatalf("join: code %d, lastMsgSn %d, %d messages", code, rep.LastMsgSn, len(rep.MsgList))
	}
	if first := rep.MsgList[0].MsgSn; first != int64(cat.HistoryLimit+1-themeChatPageMax+1) {
		t.Fatalf("join starts at serial %d, want the newest %d", first, themeChatPageMax)
	}
}

// TestThemeChatHotListSortsByCount: catgId 0 lists the rooms busiest first; the others keep the data order.
func TestThemeChatHotListSortsByCount(t *testing.T) {
	resetLounge(t)
	cat := economy.ActiveLounge()
	last := cat.Rooms[len(cat.Rooms)-1].RoomID
	loungeMu.Lock()
	loungeRoomLocked(last).members = []loungeMember{{aid: 8100, nickname: "a"}, {aid: 8101, nickname: "b"}}
	loungeMu.Unlock()

	hot := loungeRoomList(0).result.(map[string]any)["roomList"].([]tcRoomRow)
	if hot[0].RoomID != last || hot[0].OnlineMemberCnt != 2 {
		t.Fatalf("hot list starts with room %d (%d online), want room %d with 2", hot[0].RoomID, hot[0].OnlineMemberCnt, last)
	}
	plain := loungeRoomList(cat.CatgID).result.(map[string]any)["roomList"].([]tcRoomRow)
	if plain[0].RoomID != cat.Rooms[0].RoomID {
		t.Fatalf("category list starts with room %d, want the data order", plain[0].RoomID)
	}
}

// TestThemeChatProfilePathRows: a member's and a message's profileUrl is the saved picture path with its leading slash
// trimmed, and "" for an account with no saved picture. The picture rows include the viewer's own fallback row.
func TestThemeChatProfilePathRows(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"tc-9961": {AccessToken: "tc-9961", SessionKey: "sk-tc-9961", Aid: "9961", Name: "Pia", ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9961_1700000001"}},
		"tc-9962": {AccessToken: "tc-9962", SessionKey: "sk-tc-9962", Aid: "9962", Name: "Ned"},
	})
	resetLounge(t)
	pia := inviteeSession(t, "sk-tc-9961")
	ned := inviteeSession(t, "sk-tc-9962")
	cat := economy.ActiveLounge()
	room, other := cat.Rooms[0].RoomID, cat.Rooms[1].RoomID
	join := fmt.Sprintf(`{"roomId":%d}`, room)

	tcCall(t, pia, 1, tcJoin, join)
	tcCall(t, pia, 2, tcSend, fmt.Sprintf(`{"roomId":%d,"msgType":0,"msg":"hi","msgFilter":0}`, room))
	_, _, body := tcCall(t, ned, 3, tcJoin, join)
	_, payload := tcReplyOf(t, body)
	first := wantList(t, payload, "msgList", 1)[0].(map[string]any)
	wantStr(t, first, "senderId", "9961")
	wantStr(t, first, "profileUrl", "lineplay/pr/9961_1700000001")

	_, _, body = tcCall(t, ned, 4, tcSend, fmt.Sprintf(`{"roomId":%d,"msgType":0,"msg":"yo","msgFilter":0}`, room))
	if code, _ := tcReplyOf(t, body); code != tcOK {
		t.Fatalf("Ned send resultCode = %d", code)
	}
	_, _, body = tcCall(t, ned, 5, tcMembers, join)
	_, payload = tcReplyOf(t, body)
	members := wantList(t, payload, "memberList", 2)
	wantStr(t, members[0].(map[string]any), "profileUrl", "lineplay/pr/9961_1700000001")
	wantStr(t, members[1].(map[string]any), "profileUrl", "")
	_, _, body = tcCall(t, ned, 6, tcGetMsgs, fmt.Sprintf(`{"roomId":%d,"startMsgSn":1,"endMsgSn":2}`, room))
	_, payload = tcReplyOf(t, body)
	msgs := wantList(t, payload, "msgList", 2)
	wantStr(t, msgs[0].(map[string]any), "profileUrl", "lineplay/pr/9961_1700000001")
	wantStr(t, msgs[1].(map[string]any), "profileUrl", "")

	// Pia's pending pushes (Ned's join and Ned's message) come before her reply: read them, then the fallback row of a room
	// she is not in.
	readLOPush(t, pia)
	readLOPush(t, pia)
	_, _, body = tcCall(t, pia, 7, tcMembers, fmt.Sprintf(`{"roomId":%d}`, other))
	_, payload = tcReplyOf(t, body)
	self := wantList(t, payload, "memberList", 1)[0].(map[string]any)
	wantStr(t, self, "memberId", "9961")
	wantStr(t, self, "profileUrl", "lineplay/pr/9961_1700000001")
}

// TestThemeChatStickerKeys: a sticker (205) is accepted only when its key is ls_<t>_<i> for a set and an index the client
// has. Both ends of each set are accepted; anything else is refused with 402 and changes nothing.
func TestThemeChatStickerKeys(t *testing.T) {
	resetLounge(t)
	room := economy.ActiveLounge().Rooms[0].RoomID
	const sender uint64 = 9972
	if code, _ := joinAs(t, room, sender); code != tcOK {
		t.Fatalf("join resultCode = %d", code)
	}
	for _, key := range []string{"ls_10_1", "ls_10_350", "ls_20_88", "ls_30_96", "ls_40_16", "ls_50_16", "ls_60_40", "ls_70_40"} {
		if got := loungeSend(room, tcTypeSticker, key, sender, true).code; got != tcOK {
			t.Errorf("sticker %q: code %d, want 0", key, got)
		}
	}
	for _, key := range []string{"ls_10_351", "ls_15_1", "ls_10_0", "ls_10_01", "ls_10_1x", "ls_20_89", "ls_80_1", "ls_+10_1", "ls_10_1_2", "10_1", "ls_10_", "ls_10_1 ", ""} {
		if got := loungeSend(room, tcTypeSticker, key, sender, true).code; got != tcRefused {
			t.Errorf("sticker %q: code %d, want 402", key, got)
		}
	}
	loungeMu.Lock()
	defer loungeMu.Unlock()
	if last := loungeRooms[room].lastSn; last != 8 {
		t.Fatalf("lastSn = %d after 8 accepted stickers, want 8", last)
	}
}
