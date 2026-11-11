package lpn

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// setAvatarFriends makes each pair accepted friends until the test ends (the record the friend list keeps).
func setAvatarFriends(t *testing.T, pairs ...[2]string) {
	t.Helper()
	store.SocialMu.Lock()
	old := store.Friendships
	for _, p := range pairs {
		store.Friendships = append(store.Friendships, store.Friendship{A: p[0], B: p[1], State: "accepted"})
	}
	store.SocialMu.Unlock()
	t.Cleanup(func() {
		store.SocialMu.Lock()
		store.Friendships = old
		store.SocialMu.Unlock()
	})
}

// resetAvatarChat empties the 1:1 registry in memory: no rooms and the first room id next. Call it before a database is
// attached (avSetup does), so the attached database starts with the same empty registry.
func resetAvatarChat(t *testing.T) {
	t.Helper()
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	avatarChatRooms = map[int64]*avatarChatRoom{}
	avatarChatPairs = map[[2]uint64]int64{}
	avatarChatNext = CherryChatRoomFirst
	avatarChatHere = map[uint64]avatarChatSeenAt{}
	avatarChatUnsaved = map[int64]int64{}
}

// avMembers is the 1001 payload naming the ids, in the order given.
func avMembers(ids ...string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf(`{"userId":%q}`, id)
	}
	return `{"memberList":[` + strings.Join(parts, ",") + `]}`
}

// avSetup makes Ann (9981), Ben (9982) and Cy (9983) with live sessions, Ann and Ben friends, and an empty registry.
func avSetup(t *testing.T) (ann, ben, cy net.Conn) {
	t.Helper()
	inviteAccounts(t, map[string]*store.Account{
		"av-9981": {AccessToken: "av-9981", SessionKey: "sk-av-9981", Aid: "9981", Name: "Ann"},
		"av-9982": {AccessToken: "av-9982", SessionKey: "sk-av-9982", Aid: "9982", Name: "Ben"},
		"av-9983": {AccessToken: "av-9983", SessionKey: "sk-av-9983", Aid: "9983", Name: "Cy"},
	})
	setAvatarFriends(t, [2]string{"9981", "9982"})
	resetAvatarChat(t)
	// The chat tests run with a database attached: every registry save is written and read back by the safety net.
	testutil.UseDatabase(t)
	return inviteeSession(t, "sk-av-9981"), inviteeSession(t, "sk-av-9982"), inviteeSession(t, "sk-av-9983")
}

// avPushOf reads a 13001 chat push: the userId it is addressed to, and its payload object.
func avPushOf(t *testing.T, body []byte) (string, map[string]any) {
	t.Helper()
	var got struct {
		Catg   string `json:"catg"`
		Params struct {
			Command int            `json:"command"`
			UserID  string         `json:"userId"`
			Payload map[string]any `json:"payload"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &got); err != nil || got.Catg != "chat" || got.Params.Command != 13001 {
		t.Fatalf("push %s is not a 13001 chat push (err %v)", body, err)
	}
	return got.Params.UserID, got.Params.Payload
}

// TestAvatarChatOpenOneRoomPerPair: 1001 by either friend makes one room with the same id; a stranger, the synthetic
// friend, an unknown or non-canonical aid, a self pair, a single member and a numeric userId are refused with no room.
func TestAvatarChatOpenOneRoomPerPair(t *testing.T) {
	ann, ben, cy := avSetup(t)

	_, _, body := tcCall(t, ann, 1, acCreate, avMembers("9981", "9982"))
	if code, payload := tcReplyOf(t, body); code != 0 {
		t.Fatalf("Ann open resultCode = %d", code)
	} else {
		wantStr(t, payload, "roomId", "1960000000")
	}
	_, _, body = tcCall(t, ben, 2, acCreate, avMembers("9982", "9981"))
	if code, payload := tcReplyOf(t, body); code != 0 {
		t.Fatalf("Ben open resultCode = %d", code)
	} else {
		wantStr(t, payload, "roomId", "1960000000")
	}

	refused := map[string]string{
		"stranger (Cy by Ann)": avMembers("9981", "9983"),
		"Cy asks for Ann":      avMembers("9983", "9981"),
		"synthetic friend":     avMembers("9981", store.FriendAID),
		"unknown aid":          avMembers("9981", "9999"),
		"non-canonical aid":    avMembers("9981", "09982"),
		"self twice":           avMembers("9981", "9981"),
		"one member":           avMembers("9981"),
		"names nobody":         avMembers("9982", "9983"),
		"numeric userId":       `{"memberList":[{"userId":9981},{"userId":9982}]}`,
	}
	for name, members := range refused {
		conn := ann
		if name == "Cy asks for Ann" {
			conn = cy
		}
		_, _, body := tcCall(t, conn, 3, acCreate, members)
		code, payload := tcReplyOf(t, body)
		if code != acRefused {
			t.Errorf("%s: resultCode = %d, want %d", name, code, acRefused)
		}
		if _, ok := payload["roomId"]; ok {
			t.Errorf("%s: a refused open named a room: %v", name, payload)
		}
	}
	avatarChatMu.Lock()
	rooms := len(avatarChatRooms)
	avatarChatMu.Unlock()
	if rooms != 1 {
		t.Fatalf("%d rooms after the refusals, want 1", rooms)
	}
}

// TestAvatarChatRoomInfoAckAndSync: the 1006 reply is non-empty with the spec's keys (both members, the requester first);
// the 3002 sync, 3004 ack, 1004 delete and 1012 config reply for a member; a stranger is refused on each.
func TestAvatarChatRoomInfoAckAndSync(t *testing.T) {
	ann, _, cy := avSetup(t)
	_, _, body := tcCall(t, ann, 1, acCreate, avMembers("9981", "9982"))
	if code, _ := tcReplyOf(t, body); code != 0 {
		t.Fatalf("open resultCode = %d", code)
	}

	_, _, body = tcCall(t, ann, 2, acSyncRoom, `{"roomId":"1960000000","msgCnt":0}`)
	code, payload := tcReplyOf(t, body)
	if code != 0 || len(payload) == 0 {
		t.Fatalf("1006 resultCode=%d payload=%v, want 0 and a non-empty payload", code, payload)
	}
	wantNum(t, payload, "lastMsgSn", 0)
	wantNum(t, payload, "pushYn", 1)
	wantNum(t, payload, "offsetMsgSn", 0)
	wantList(t, payload, "msgList", 0)
	members := wantList(t, payload, "memberList", 2)
	self, _ := members[0].(map[string]any)
	peer, _ := members[1].(map[string]any)
	wantStr(t, self, "userId", "9981")
	wantStr(t, self, "nickname", "Ann")
	wantStr(t, peer, "userId", "9982")
	wantStr(t, peer, "nickname", "Ben")
	if peer["friend"] != true || peer["deleted"] != false {
		t.Fatalf("peer row friend/deleted = %v/%v, want true/false", peer["friend"], peer["deleted"])
	}

	_, _, body = tcCall(t, ann, 3, acSync, `{"roomId":"1960000000","lastMsgSn":0,"limit":1}`)
	if code, payload = tcReplyOf(t, body); code != 0 {
		t.Fatalf("3002 resultCode = %d", code)
	}
	wantList(t, payload, "msgList", 0)

	for name, req := range map[string]struct {
		cmd     int
		payload string
	}{
		"3004 ack":      {acAck, `{"roomId":"1960000000","msgSn":0}`},
		"1004 delete":   {acDelete, `{"roomId":"1960000000"}`},
		"1012 pushYn":   {acRoomConfig, `{"roomId":"1960000000","pushYn":0}`},
		"3002 sync Ann": {acSync, `{"roomId":"1960000000","lastMsgSn":0,"limit":1}`},
	} {
		_, _, body = tcCall(t, ann, 4, req.cmd, req.payload)
		if code, _ = tcReplyOf(t, body); code != 0 {
			t.Errorf("%s by a member: resultCode = %d", name, code)
		}
	}

	for name, req := range map[string]struct {
		cmd     int
		payload string
	}{
		"1006 by Cy": {acSyncRoom, `{"roomId":"1960000000"}`},
		"3002 by Cy": {acSync, `{"roomId":"1960000000","lastMsgSn":0,"limit":1}`},
		"3004 by Cy": {acAck, `{"roomId":"1960000000","msgSn":0}`},
		"3003 by Cy": {acGetMsgs, `{"roomId":"1960000000","startMsgSn":1,"endMsgSn":1}`},
	} {
		_, _, body = tcCall(t, cy, 5, req.cmd, req.payload)
		if code, _ = tcReplyOf(t, body); code != acRefused {
			t.Errorf("%s: resultCode = %d, want %d", name, code, acRefused)
		}
	}
}

// TestAvatarChatSendPushAndHistory: Ann's send is replied with msgSn 1 and a time; Ben gets the 13001 push and no echo
// goes to Ann. The sender is the session's account even when the request names another senderId. A stranger's send is
// refused and pushes nothing. 3003 returns the range in order.
func TestAvatarChatSendPushAndHistory(t *testing.T) {
	ann, ben, cy := avSetup(t)
	_, _, body := tcCall(t, ann, 1, acCreate, avMembers("9981", "9982"))
	if code, _ := tcReplyOf(t, body); code != 0 {
		t.Fatalf("open resultCode = %d", code)
	}

	_, _, body = tcCall(t, ann, 2, acSend, `{"roomId":"1960000000","msgType":1,"msg":"  hello  ","msgFilter":0,"senderId":"9982"}`)
	code, payload := tcReplyOf(t, body)
	if code != 0 {
		t.Fatalf("Ann send resultCode = %d", code)
	}
	wantNum(t, payload, "msgSn", 1)
	if sec, _ := payload["msgTimeSec"].(float64); sec <= 0 {
		t.Fatalf("msgTimeSec = %v", payload["msgTimeSec"])
	}
	op, _, push := readLOPush(t, ben)
	if op != loOpPush {
		t.Fatalf("Ben got op %#02x, want the 0x1b push", op)
	}
	userID, pushed := avPushOf(t, push)
	if userID != "9982" {
		t.Fatalf("push userId = %q, want 9982", userID)
	}
	wantNum(t, pushed, "msgSn", 1)
	wantStr(t, pushed, "sndr", "9981")
	assertNoReply(t, ann)

	_, _, body = tcCall(t, ben, 3, acSend, `{"roomId":"1960000000","msgType":1,"msg":"hi back","msgFilter":0}`)
	if code, payload = tcReplyOf(t, body); code != 0 || payload["msgSn"] != float64(2) {
		t.Fatalf("Ben send resultCode=%d payload=%v, want 0 and msgSn 2", code, payload)
	}
	op, _, push = readLOPush(t, ann)
	if op != loOpPush {
		t.Fatalf("Ann got op %#02x, want the 0x1b push", op)
	}
	if userID, pushed = avPushOf(t, push); userID != "9981" {
		t.Fatalf("push userId = %q, want 9981", userID)
	}
	wantNum(t, pushed, "msgSn", 2)
	wantStr(t, pushed, "sndr", "9982")

	_, _, body = tcCall(t, cy, 4, acSend, `{"roomId":"1960000000","msgType":1,"msg":"sneak","msgFilter":0}`)
	if code, _ = tcReplyOf(t, body); code != acRefused {
		t.Fatalf("stranger send resultCode = %d, want %d", code, acRefused)
	}
	assertNoReply(t, ann)
	assertNoReply(t, ben)

	_, _, body = tcCall(t, ben, 5, acGetMsgs, `{"roomId":"1960000000","startMsgSn":1,"endMsgSn":2}`)
	_, payload = tcReplyOf(t, body)
	rows := wantList(t, payload, "msgList", 2)
	first, _ := rows[0].(map[string]any)
	second, _ := rows[1].(map[string]any)
	wantStr(t, first, "senderId", "9981")
	wantNum(t, first, "msgSn", 1)
	wantStr(t, first, "msg", "hello")
	wantNum(t, first, "msgType", 1)
	wantNum(t, first, "visible", 1)
	wantStr(t, second, "senderId", "9982")
	wantNum(t, second, "msgSn", 2)
	wantStr(t, second, "msg", "hi back")

	_, _, body = tcCall(t, ann, 6, acGetMsgs, `{"roomId":"1960000000","startMsgSn":3,"endMsgSn":3}`)
	_, payload = tcReplyOf(t, body)
	wantList(t, payload, "msgList", 0)
}

// TestAvatarChatRoomCapPerAccount: an account in avatarChatRoomsMax rooms cannot open another, and the refusal makes no room.
func TestAvatarChatRoomCapPerAccount(t *testing.T) {
	avSetup(t)
	avatarChatMu.Lock()
	for i := 0; i < avatarChatRoomsMax; i++ {
		pair := [2]uint64{9981, uint64(20000 + i)}
		id := avatarChatNext
		avatarChatNext++
		avatarChatRooms[id] = &avatarChatRoom{a: pair[0], b: pair[1]}
		avatarChatPairs[pair] = id
	}
	avatarChatMu.Unlock()
	rep, _ := avatarChatCall(acCreate, json.RawMessage(avMembers("9981", "9982")), 9981, true)
	if rep.code != acRefused {
		t.Fatalf("open past the cap: code %d, want %d", rep.code, acRefused)
	}
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	if _, ok := avatarChatPairs[[2]uint64{9981, 9982}]; ok {
		t.Fatal("the refused open still made a room")
	}
}

// TestAvatarChatSendRefusals: a blank, empty, over-long, non-text or unknown-room send is refused and takes no serial;
// 500 characters (more bytes than that) are accepted.
func TestAvatarChatSendRefusals(t *testing.T) {
	ann, ben, _ := avSetup(t)
	_, _, body := tcCall(t, ann, 1, acCreate, avMembers("9981", "9982"))
	if code, _ := tcReplyOf(t, body); code != 0 {
		t.Fatalf("open resultCode = %d", code)
	}
	refused := map[string]string{
		"blank":        `{"roomId":"1960000000","msgType":1,"msg":"   "}`,
		"empty":        `{"roomId":"1960000000","msgType":1,"msg":""}`,
		"501 chars":    fmt.Sprintf(`{"roomId":"1960000000","msgType":1,"msg":%q}`, strings.Repeat("a", 501)),
		"sticker type": `{"roomId":"1960000000","msgType":205,"msg":"ls_10_1"}`,
		"type 0":       `{"roomId":"1960000000","msgType":0,"msg":"hi"}`,
		"no room":      `{"msgType":1,"msg":"hi"}`,
		"unknown room": `{"roomId":"1960000777","msgType":1,"msg":"hi"}`,
	}
	for name, req := range refused {
		_, _, body := tcCall(t, ann, 2, acSend, req)
		if code, _ := tcReplyOf(t, body); code != acRefused {
			t.Errorf("%s: resultCode = %d, want %d", name, code, acRefused)
		}
	}
	long := strings.Repeat("é", 500)
	_, _, body = tcCall(t, ann, 3, acSend, fmt.Sprintf(`{"roomId":"1960000000","msgType":1,"msg":%q}`, long))
	code, payload := tcReplyOf(t, body)
	if code != 0 {
		t.Fatalf("500-character send resultCode = %d, want 0", code)
	}
	wantNum(t, payload, "msgSn", 1)
	readLOPush(t, ben)
}

// TestAvatarChatHistoryCap: after avatarChatHistoryMax+1 sends only the newest avatarChatHistoryMax remain, the serials keep
// counting from 1 with no reuse, and a range asks only what is kept.
func TestAvatarChatHistoryCap(t *testing.T) {
	avSetup(t)
	rep, _ := avatarChatCall(acCreate, json.RawMessage(avMembers("9981", "9982")), 9981, true)
	if rep.code != 0 {
		t.Fatalf("open code = %d", rep.code)
	}
	send := json.RawMessage(`{"roomId":"1960000000","msgType":1,"msg":"m"}`)
	for i := 1; i <= avatarChatHistoryMax+1; i++ {
		rep, _ := avatarChatCall(acSend, send, 9981, true)
		if rep.code != 0 || rep.result.(map[string]any)["msgSn"] != int64(i) {
			t.Fatalf("send %d: code %d result %v", i, rep.code, rep.result)
		}
	}
	rep, _ = avatarChatCall(acGetMsgs, json.RawMessage(`{"roomId":"1960000000","startMsgSn":1,"endMsgSn":201}`), 9981, true)
	rows := rep.result.(map[string]any)["msgList"].([]avatarChatRow)
	if len(rows) != avatarChatHistoryMax || rows[0].MsgSn != 2 || rows[len(rows)-1].MsgSn != 201 {
		t.Fatalf("kept %d rows; want %d rows from msgSn 2 to 201: %+v", len(rows), avatarChatHistoryMax, rows)
	}
	rep, _ = avatarChatCall(acGetMsgs, json.RawMessage(`{"roomId":"1960000000","startMsgSn":1,"endMsgSn":1}`), 9981, true)
	if n := len(rep.result.(map[string]any)["msgList"].([]avatarChatRow)); n != 0 {
		t.Fatalf("dropped message 1 still returned (%d rows)", n)
	}
}

// TestAvatarChatOwnsSplitsLoungeCommands: 1001, 3001 and 3002 go to Lounge Chat unless their payload is a 1:1 one; the
// 1:1-only numbers always go to the 1:1 chat.
func TestAvatarChatOwnsSplitsLoungeCommands(t *testing.T) {
	for _, tc := range []struct {
		cmd     int64
		payload string
		want    bool
	}{
		{1001, `{"roomId":"1930000001"}`, false},
		{1003, `{"catgId":1930000000}`, false},
		{3001, `{"roomId":"1930000001","startMsgSn":1,"endMsgSn":5}`, false},
		{3002, `{"roomId":"1930000001","msgType":0,"msg":"hi","msgFilter":0}`, false},
		{1001, avMembers("9981", "9982"), true},
		{3001, `{"roomId":"1960000000","msgType":1,"msg":"hi","msgFilter":0}`, true},
		{3002, `{"roomId":"1960000000","lastMsgSn":0,"limit":1}`, true},
		{1004, `{"roomId":"1960000000"}`, true},
		{1006, `{"roomId":"1960000000","msgCnt":0}`, true},
		{1012, `{"roomId":"1960000000","pushYn":1}`, true},
		{3003, `{"roomId":"1960000000","startMsgSn":1,"endMsgSn":1}`, true},
		{3004, `{"roomId":"1960000000","msgSn":1}`, true},
	} {
		if got := avatarChatOwns(tc.cmd, json.RawMessage(tc.payload)); got != tc.want {
			t.Errorf("cmd %d %s: owned = %v, want %v", tc.cmd, tc.payload, got, tc.want)
		}
	}
}

// TestAvatarChatRoomListForMembers: the talk list lists the room for both members with the other member alone in
// memberList, and nothing for a stranger; the HTTP route answers with the same list, and an empty one without a session.
func TestAvatarChatRoomListForMembers(t *testing.T) {
	ann, ben, _ := avSetup(t)
	_, _, body := tcCall(t, ann, 1, acCreate, avMembers("9981", "9982"))
	if code, _ := tcReplyOf(t, body); code != 0 {
		t.Fatalf("open resultCode = %d", code)
	}
	_, _, body = tcCall(t, ben, 2, acSend, `{"roomId":"1960000000","msgType":1,"msg":"hello","msgFilter":0}`)
	if code, _ := tcReplyOf(t, body); code != 0 {
		t.Fatalf("send resultCode = %d", code)
	}
	readLOPush(t, ann)

	list := avatarChatRoomList(9981)
	rows := list["roomList"].([]avatarChatTalk)
	if len(rows) != 1 || rows[0].RoomID != "1960000000" || rows[0].LastMsg != "hello" || rows[0].LastMsgUserID != "9982" {
		t.Fatalf("Ann's talk list = %+v", rows)
	}
	if len(rows[0].MemberList) != 1 || rows[0].MemberList[0].UserID != "9982" {
		t.Fatalf("memberList = %+v, want the friend alone", rows[0].MemberList)
	}
	if list["lastMsgTimeSec"].(int64) <= 0 {
		t.Fatalf("lastMsgTimeSec = %v", list["lastMsgTimeSec"])
	}
	if got := avatarChatRoomList(9983)["roomList"].([]avatarChatTalk); got == nil || len(got) != 0 {
		t.Fatalf("stranger's talk list = %v, want an empty array", got)
	}

	get := func(cookie string) []any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v4/chat/roomlist/sync/0/200", nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: cookie})
		}
		rec := httptest.NewRecorder()
		HandleAvatarChatRoomList(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET with cookie %q: status %d", cookie, rec.Code)
		}
		var out struct {
			Result struct {
				RoomList []map[string]any `json:"roomList"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Result.RoomList == nil {
			t.Fatalf("body %s: %v", rec.Body.String(), err)
		}
		list := make([]any, len(out.Result.RoomList))
		for i, r := range out.Result.RoomList {
			list[i] = r
		}
		return list
	}
	if got := get("av-9981"); len(got) != 1 { // the room with a message, for the AV_AUTH account
		t.Fatalf("Ann's route list has %d rows, want 1", len(got))
	}
	avatarChatListOn = false // the kill switch: the route answers the empty list again
	if got := get("av-9981"); len(got) != 0 {
		t.Fatalf("switched-off route list has %d rows, want 0", len(got))
	}
	avatarChatListOn = true
	if got := get("av-9983"); len(got) != 0 {
		t.Fatalf("stranger's route list has %d rows, want 0", len(got))
	}
	if got := get(""); len(got) != 0 {
		t.Fatalf("no-session route list has %d rows, want 0", len(got))
	}
	req := httptest.NewRequest(http.MethodPost, "/v4/chat/roomlist/sync/0/200", nil)
	rec := httptest.NewRecorder()
	HandleAvatarChatRoomList(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST status %d, want 404", rec.Code)
	}
}

// avRestart simulates a restart: the registry is replaced by the rows the attached database holds (the startup read). With
// no rows it is an empty registry.
func avRestart(t *testing.T) {
	t.Helper()
	book, err := store.LoadChatRooms()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := avatarChatRestore(book); err != nil {
		t.Fatalf("restore: %v", err)
	}
}

// avOpen opens the room of the pair (aid, peer) for aid through the command and returns its room id.
func avOpen(t *testing.T, aid, peer string) string {
	t.Helper()
	id, err := strconv.ParseUint(aid, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	rep, _ := avatarChatCall(acCreate, json.RawMessage(avMembers(aid, peer)), id, true)
	if rep.code != 0 {
		t.Fatalf("open by %s with %s: code %d", aid, peer, rep.code)
	}
	return rep.result.(map[string]any)["roomId"].(string)
}

// avSend sends one text from aid to room through the command and returns its msgSn.
func avSend(t *testing.T, aid, room, text string) int64 {
	t.Helper()
	id, err := strconv.ParseUint(aid, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	rep, _ := avatarChatCall(acSend, json.RawMessage(fmt.Sprintf(`{"roomId":%q,"msgType":1,"msg":%q}`, room, text)), id, true)
	if rep.code != 0 {
		t.Fatalf("send by %s: code %d", aid, rep.code)
	}
	return rep.result.(map[string]any)["msgSn"].(int64)
}

// TestAvatarChatRestartKeepsHistoryAndSerials: after a restart the pair keeps its room id, a new pair gets a new id, the
// history is returned in full (the same rows as before), 1006 reports the newest serial, and the next serial is above every
// serial the old run issued. Each send is one registry write. A room outside the id block is refused at load.
func TestAvatarChatRestartKeepsHistoryAndSerials(t *testing.T) {
	avSetup(t)
	setAvatarFriends(t, [2]string{"9981", "9983"})
	avRestart(t)

	room := avOpen(t, "9981", "9982")
	if room != "1960000000" {
		t.Fatalf("first room = %s", room)
	}
	before := store.ChatRoomWrites()
	for _, text := range []string{"one", "two", "three"} {
		avSend(t, "9981", room, text)
	}
	if n := store.ChatRoomWrites() - before; n != 3 {
		t.Fatalf("%d registry writes for 3 sends, want 3", n)
	}
	history := func() []avatarChatRow {
		t.Helper()
		rep, _ := avatarChatCall(acGetMsgs, json.RawMessage(`{"roomId":"1960000000","startMsgSn":1,"endMsgSn":1000}`), 9981, true)
		return rep.result.(map[string]any)["msgList"].([]avatarChatRow)
	}
	want := history()
	if len(want) != 3 {
		t.Fatalf("history before restart has %d rows, want 3", len(want))
	}

	avRestart(t)
	rep, _ := avatarChatRoomInfo(room, 9981)
	info := rep.result.(map[string]any)
	if info["lastMsgSn"] != int64(3) || info["offsetMsgSn"] != int64(0) {
		t.Fatalf("1006 after restart: lastMsgSn %v offsetMsgSn %v, want 3 and 0", info["lastMsgSn"], info["offsetMsgSn"])
	}
	if got := history(); !reflect.DeepEqual(got, want) {
		t.Fatalf("history after restart = %+v, want %+v", got, want)
	}
	if got := avOpen(t, "9981", "9982"); got != room {
		t.Fatalf("same pair after restart got room %s, want %s", got, room)
	}
	if other := avOpen(t, "9981", "9983"); other == room || other != "1960000001" {
		t.Fatalf("new pair after restart got room %s", other)
	}
	if sn := avSend(t, "9981", room, "four"); sn != 4 {
		t.Fatalf("first send after restart msgSn = %d, want 4 (no gap)", sn)
	}

	// A registry whose room is outside the id block is refused at load (the rows are read by the same check).
	badBook := store.ChatRoomBook{NextID: 1960000005, Rooms: []store.ChatRoom{{ID: 1959999999, Aids: [2]string{"9981", "9982"}}}}
	if err := avatarChatRestore(badBook); err == nil {
		t.Fatal("a room outside the id block was accepted")
	}
}

// TestAvatarChatSendSavesEachMessage: 150 sends cause 150 registry writes, the serials are 1..150 with no gap, and the saved
// file holds the 150 messages with the newest serial as the room's Ceil.
func TestAvatarChatSendSavesEachMessage(t *testing.T) {
	avSetup(t)
	avRestart(t)
	room := avOpen(t, "9981", "9982")
	before := store.ChatRoomWrites()
	for i := int64(1); i <= 150; i++ {
		if sn := avSend(t, "9981", room, "m"); sn != i {
			t.Fatalf("send %d got msgSn %d", i, sn)
		}
	}
	if n := store.ChatRoomWrites() - before; n != 150 {
		t.Fatalf("%d registry writes for 150 sends, want 150", n)
	}
	book, err := store.LoadChatRooms()
	if err != nil || len(book.Rooms) != 1 || book.Rooms[0].Ceil != 150 || len(book.Rooms[0].Msgs) != 150 {
		t.Fatalf("saved registry = %+v (err %v), want one room with newest serial 150 and 150 messages", book, err)
	}
}

// TestAvatarChatLoadOldAndBadFiles: a registry from the chunked file (no messages; the reserved ceiling in ceil) loads: the
// room resumes at the ceiling, its acks are clamped, and its history is empty. A message that is out of order, above the
// newest serial or from a non-member is refused at load. The registries are the books the startup read would get from
// the rows of the old and bad files; the test runs memory-only, so no save is checked against them.
func TestAvatarChatLoadOldAndBadFiles(t *testing.T) {
	avSetup(t)
	store.DetachDB()
	old := store.ChatRoomBook{NextID: 1960000001, Rooms: []store.ChatRoom{{
		ID: 1960000000, Aids: [2]string{"9981", "9982"}, Ceil: 100, Acks: map[string]int64{"9982": 500},
	}}}
	if err := avatarChatRestore(old); err != nil {
		t.Fatalf("restore old registry: %v", err)
	}
	rep, _ := avatarChatRoomInfo("1960000000", 9981)
	if info := rep.result.(map[string]any); info["lastMsgSn"] != int64(100) || info["peerAckMsgSn"] != int64(100) {
		t.Fatalf("old room: lastMsgSn %v peerAckMsgSn %v, want 100 and 100 (the ack clamped)", info["lastMsgSn"], info["peerAckMsgSn"])
	}
	if sn := avSend(t, "9981", "1960000000", "x"); sn != 101 {
		t.Fatalf("first send in an old room = msgSn %d, want 101", sn)
	}

	for name, msgs := range map[string][]store.ChatMsg{
		"out of order": {{Sn: 2, Sec: 1, Sender: "9981", Text: "b"}, {Sn: 1, Sec: 1, Sender: "9981", Text: "a"}},
		"above ceil":   {{Sn: 3, Sec: 1, Sender: "9981", Text: "c"}},
		"non-member":   {{Sn: 1, Sec: 1, Sender: "9983", Text: "x"}},
	} {
		bad := store.ChatRoomBook{NextID: 1960000001, Rooms: []store.ChatRoom{{
			ID: 1960000000, Aids: [2]string{"9981", "9982"}, Ceil: 2, Msgs: msgs,
		}}}
		if err := avatarChatRestore(bad); err == nil {
			t.Errorf("%s: message accepted at load", name)
		}
	}
}

// TestAvatarChatAckClampAndSaves: an ack is clamped to the newest serial and kept in memory without a write; the next send's
// save writes it, and after a restart it is still there.
func TestAvatarChatAckClampAndSaves(t *testing.T) {
	avSetup(t)
	avRestart(t)
	room := avOpen(t, "9981", "9982")
	avSend(t, "9981", room, "hi")
	before := store.ChatRoomWrites()

	if rep, _ := avatarChatCall(acAck, json.RawMessage(`{"roomId":"1960000000","msgSn":999}`), 9982, true); rep.code != 0 {
		t.Fatalf("ack code %d", rep.code)
	}
	if store.ChatRoomWrites() != before+1 {
		t.Fatal("a moved ack did not reach the registry")
	}
	if rep, _ := avatarChatCall(acAck, json.RawMessage(`{"roomId":"1960000000","msgSn":1}`), 9982, true); rep.code != 0 {
		t.Fatalf("repeat ack code %d", rep.code)
	}
	if store.ChatRoomWrites() != before+1 {
		t.Fatal("an unchanged ack wrote the registry")
	}
	before = store.ChatRoomWrites()
	rep, _ := avatarChatRoomInfo(room, 9981)
	if got := rep.result.(map[string]any)["peerAckMsgSn"]; got != int64(1) {
		t.Fatalf("Ann's peerAckMsgSn = %v, want 1 (the ack clamped to lastSn)", got)
	}
	rep, _ = avatarChatRoomInfo(room, 9982)
	if got := rep.result.(map[string]any)["lastAckSn"]; got != int64(1) {
		t.Fatalf("Ben's lastAckSn = %v, want 1", got)
	}

	for i := 2; i <= 101; i++ {
		avSend(t, "9981", room, "m")
	}
	if store.ChatRoomWrites() != before+100 {
		t.Fatalf("writes after 100 sends = %d, want %d", store.ChatRoomWrites(), before+100)
	}
	avRestart(t)
	rep, _ = avatarChatRoomInfo(room, 9982)
	if info := rep.result.(map[string]any); info["lastAckSn"] != int64(1) || info["lastMsgSn"] != int64(101) {
		t.Fatalf("after restart Ben's 1006 lastAckSn %v lastMsgSn %v, want 1 and 101", info["lastAckSn"], info["lastMsgSn"])
	}
}

// TestAvatarChatSaveFailureRefuses: when the registry cannot be written, the open is refused with no room and no id used,
// and a send is refused with its message dropped (its serial stays taken, so it is never handed out again); once the write
// works, the same requests succeed. The failure is a trigger on the attached database that aborts the chat inserts; it is
// dropped for the recovery, so the database and the registry in memory stay the same database throughout.
func TestAvatarChatSaveFailureRefuses(t *testing.T) {
	avSetup(t)
	avRestart(t)
	failWrites := func(on bool) {
		t.Helper()
		stmts := []string{
			`CREATE TRIGGER chat_room_refused BEFORE INSERT ON chat_rooms BEGIN SELECT RAISE(ABORT, 'refused'); END`,
			`CREATE TRIGGER chat_message_refused BEFORE INSERT ON chat_messages BEGIN SELECT RAISE(ABORT, 'refused'); END`,
		}
		if !on {
			stmts = []string{`DROP TRIGGER IF EXISTS chat_room_refused`, `DROP TRIGGER IF EXISTS chat_message_refused`}
		}
		if err := store.AttachedDB().Update(func(tx *sql.Tx) error {
			for _, s := range stmts {
				if _, err := tx.Exec(s); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	open := func() tcReply {
		rep, _ := avatarChatCall(acCreate, json.RawMessage(avMembers("9981", "9982")), 9981, true)
		return rep
	}

	failWrites(true)
	if rep := open(); rep.code != acInternal {
		t.Fatalf("open with a failing save: code %d, want %d", rep.code, acInternal)
	}
	avatarChatMu.Lock()
	if len(avatarChatRooms) != 0 || avatarChatNext != CherryChatRoomFirst {
		t.Fatalf("failed open left %d rooms, next %d", len(avatarChatRooms), avatarChatNext)
	}
	avatarChatMu.Unlock()

	failWrites(false)
	room := avOpen(t, "9981", "9982")
	if room != "1960000000" {
		t.Fatalf("open after the write works: room %s, want 1960000000", room)
	}

	failWrites(true)
	rep, _ := avatarChatCall(acSend, json.RawMessage(`{"roomId":"1960000000","msgType":1,"msg":"x"}`), 9981, true)
	if rep.code != acInternal {
		t.Fatalf("send with a failing save: code %d, want %d", rep.code, acInternal)
	}
	avatarChatMu.Lock()
	if r := avatarChatRooms[1960000000]; r.lastSn != 1 || len(r.msgs) != 0 {
		t.Fatalf("failed send left lastSn %d and %d messages, want 1 (serial kept taken) and 0", r.lastSn, len(r.msgs))
	}
	if _, ok := avatarChatUnsaved[1960000000]; !ok {
		t.Fatal("the room of a failed send is not marked unsaved")
	}
	avatarChatMu.Unlock()

	failWrites(false)
	if sn := avSend(t, "9981", room, "x"); sn != 2 {
		t.Fatalf("send after the write works: msgSn %d, want 2 (serial 1 was never sent)", sn)
	}
}

// TestAvatarChatMemoryOnly: with no database everything works (150 sends) and nothing is written.
func TestAvatarChatMemoryOnly(t *testing.T) {
	avSetup(t)
	store.DetachDB()
	room := avOpen(t, "9981", "9982")
	before := store.ChatRoomWrites()
	for i := 1; i <= 150; i++ {
		avSend(t, "9981", room, "m")
	}
	if store.ChatRoomWrites() != before {
		t.Fatalf("memory-only run wrote the registry %d times", store.ChatRoomWrites()-before)
	}
	rep, _ := avatarChatRoomInfo(room, 9981)
	if got := rep.result.(map[string]any)["lastMsgSn"]; got != int64(150) {
		t.Fatalf("lastMsgSn = %v, want 150", got)
	}
}

// avRowKinds and avUserKinds are the keys and JSON kinds of a talk-list row and of its userList entry (spec 3, A.2 and the
// safe complete row of chatlist-badge-spec.md).
var avRowKinds = map[string]string{
	"roomId": "string", "roomState": "number", "unreadCnt": "number", "lastMsg": "string", "lastMsgType": "number",
	"lastMsgTimeSec": "number", "lastMsgUserId": "string", "memberList": "array",
}

var avUserKinds = map[string]string{
	"avatarId": "string", "name": "string", "gender": "string", "coin": "number", "heartCount": "number",
	"haveHeart": "bool", "haveHeartBalance": "number", "haveExtraHeartBalance": "number", "country": "string",
	"randVisit": "bool", "sType": "string", "deleted": "bool", "headProfile": "string", "profile": "string",
	"wholeProfile": "string", "obsProfileImagePath": "string", "obsWholeBodyProfileImagePath": "string", "bg": "string",
	"badgeIcon": "string", "skin": "string", "clover": "number", "intro": "string", "items": "array", "petProfiles": "array",
}

// avKinds checks that obj has exactly the keys of kinds, each of the JSON kind given.
func avKinds(t *testing.T, what string, obj map[string]any, kinds map[string]string) {
	t.Helper()
	if len(obj) != len(kinds) {
		t.Fatalf("%s has %d keys, want %d: %v", what, len(obj), len(kinds), obj)
	}
	for key, want := range kinds {
		v, ok := obj[key]
		if !ok {
			t.Fatalf("%s lacks %q", what, key)
		}
		got := fmt.Sprintf("%T", v)
		switch v.(type) {
		case string:
			got = "string"
		case float64:
			got = "number"
		case bool:
			got = "bool"
		case []any:
			got = "array"
		}
		if got != want {
			t.Fatalf("%s.%s is %s, want %s", what, key, got, want)
		}
	}
}

// avWireList is a talk-list reply as the client reads it: the JSON of avatarChatRoomList(aid), decoded.
func avWireList(t *testing.T, aid uint64) map[string]any {
	t.Helper()
	raw, err := json.Marshal(avatarChatRoomList(aid))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	return wire
}

// avRows is the number of rows in aid's talk list.
func avRows(aid uint64) int { return len(avatarChatRoomList(aid)["roomList"].([]avatarChatTalk)) }

// TestAvatarChatTalkListShape: the talk list keeps the rooms with a message that the requester has not deleted and whose
// friend is still one. Its row and userList entry have every key of the spec with its JSON kind. The unread count and the
// CHAT badge are the other member's messages above the requester's ack, follow the acks and survive a restart; the switch
// off empties both.
func TestAvatarChatTalkListShape(t *testing.T) {
	avSetup(t)
	setAvatarFriends(t, [2]string{"9981", "9983"})
	avRestart(t)
	room := avOpen(t, "9981", "9982")
	avSend(t, "9982", room, "one")
	avSend(t, "9982", room, "two")
	avOpen(t, "9981", "9983") // no message: left out

	if raw, _ := json.Marshal(avatarChatRoomList(9983)); string(raw) != `{"lastMsgTimeSec":0,"roomList":[],"userList":[]}` {
		t.Fatalf("Cy's list = %s, want the empty shape", raw)
	}
	wire := avWireList(t, 9981)
	rows := wire["roomList"].([]any)
	if len(rows) != 1 {
		t.Fatalf("Ann's list has %d rows, want 1", len(rows))
	}
	row := rows[0].(map[string]any)
	avKinds(t, "row", row, avRowKinds)
	if row["roomId"] != "1960000000" || row["lastMsg"] != "two" || row["lastMsgUserId"] != "9982" ||
		row["lastMsgType"] != float64(1) || row["unreadCnt"] != float64(2) {
		t.Fatalf("row = %v", row)
	}
	if member := row["memberList"].([]any); len(member) != 1 || member[0].(map[string]any)["userId"] != "9982" {
		t.Fatalf("memberList = %v, want the friend alone", member)
	}
	users := wire["userList"].([]any)
	if len(users) != 1 {
		t.Fatalf("userList has %d entries, want 1", len(users))
	}
	user := users[0].(map[string]any)
	avKinds(t, "userList[0]", user, avUserKinds)
	if user["avatarId"] != "9982" || user["name"] != "Ben" {
		t.Fatalf("userList[0] = %v", user)
	}
	if n := AvatarChatUnread(9981); n != 2 {
		t.Fatalf("Ann's CHAT = %d, want 2", n)
	}
	if n := AvatarChatUnread(9982); n != 0 {
		t.Fatalf("Ben's CHAT = %d, want 0", n)
	}

	if rep, _ := avatarChatCall(acAck, json.RawMessage(fmt.Sprintf(`{"roomId":%q,"msgSn":2}`, room)), 9981, true); rep.code != 0 {
		t.Fatalf("ack code %d", rep.code)
	}
	if n := AvatarChatUnread(9981); n != 0 {
		t.Fatalf("CHAT after the ack = %d, want 0", n)
	}
	avSend(t, "9982", room, "three")
	avRestart(t)
	if n := AvatarChatUnread(9981); n != 1 {
		t.Fatalf("CHAT after a restart = %d, want 1", n)
	}
	if got := avWireList(t, 9981)["roomList"].([]any)[0].(map[string]any)["unreadCnt"]; got != float64(1) {
		t.Fatalf("unreadCnt after a restart = %v, want 1", got)
	}

	avatarChatListOn = false
	if n := AvatarChatUnread(9981); n != 0 || avRows(9981) != 1 {
		t.Fatalf("switched off: CHAT %d (want 0)", n)
	}
	avatarChatListOn = true

	// A friend who is no longer one leaves the list and the count.
	store.SocialMu.Lock()
	saved := store.Friendships
	store.Friendships = nil
	store.SocialMu.Unlock()
	if n, rows := AvatarChatUnread(9981), avRows(9981); n != 0 || rows != 0 {
		t.Fatalf("after unfriending: CHAT %d, rows %d, want 0 and 0", n, rows)
	}
	store.SocialMu.Lock()
	store.Friendships = saved
	store.SocialMu.Unlock()
}

// TestAvatarChatDeleteHidesUntilNewMessage: a delete (1004) takes the room off the deleter's talk list and out of their
// CHAT count, leaves the other member's list alone and lasts across a restart until a newer message arrives. A stranger's
// delete is refused.
func TestAvatarChatDeleteHidesUntilNewMessage(t *testing.T) {
	avSetup(t)
	avRestart(t)
	avOpen(t, "9981", "9982")
	avSend(t, "9982", "1960000000", "one")

	if rep, _ := avatarChatCall(acDelete, json.RawMessage(`{"roomId":"1960000000"}`), 9983, true); rep.code != acRefused {
		t.Fatalf("stranger's delete code %d, want %d", rep.code, acRefused)
	}
	if rep, _ := avatarChatCall(acDelete, json.RawMessage(`{"roomId":"1960000000"}`), 9981, true); rep.code != 0 {
		t.Fatalf("delete code %d", rep.code)
	}
	if rows, n := avRows(9981), AvatarChatUnread(9981); rows != 0 || n != 0 {
		t.Fatalf("after the delete: rows %d, CHAT %d, want 0 and 0", rows, n)
	}
	if rows := avRows(9982); rows != 1 {
		t.Fatalf("the other member's rows = %d, want 1", rows)
	}
	avRestart(t)
	if rows := avRows(9981); rows != 0 {
		t.Fatalf("after a restart the deleted room is back: %d rows", rows)
	}
	avSend(t, "9982", "1960000000", "two")
	if rows, n := avRows(9981), AvatarChatUnread(9981); rows != 1 || n != 2 {
		t.Fatalf("a newer message did not bring the room back: rows %d, CHAT %d, want 1 and 2", rows, n)
	}
}

// TestAvatarChatArrivalPush: a message to a member outside the room is a 13001 with the banner text as content; to a member
// inside the room it is the plain 13001. The banner switch off drops the content everywhere.
func TestAvatarChatArrivalPush(t *testing.T) {
	ann, ben, _ := avSetup(t)
	avOpen(t, "9981", "9982")
	_, _, body := tcCall(t, ann, 1, acSyncRoom, `{"roomId":"1960000000","msgCnt":0}`) // Ann is in the room now
	if code, _ := tcReplyOf(t, body); code != 0 {
		t.Fatalf("sync resultCode = %d", code)
	}
	_, _, body = tcCall(t, ben, 2, acSend, `{"roomId":"1960000000","msgType":1,"msg":"inside","msgFilter":0}`)
	if code, _ := tcReplyOf(t, body); code != 0 {
		t.Fatalf("send resultCode = %d", code)
	}
	_, _, push := readLOPush(t, ann)
	if _, has := avPushContent(t, push); has {
		t.Fatalf("the push to a member in the room carries content: %s", push)
	}

	avatarChatMu.Lock()
	delete(avatarChatHere, 9981) // Ann leaves: no leave command exists, so her mark lapses
	avatarChatMu.Unlock()
	_, _, body = tcCall(t, ben, 3, acSend, `{"roomId":"1960000000","msgType":1,"msg":"outside","msgFilter":0}`)
	if code, _ := tcReplyOf(t, body); code != 0 {
		t.Fatalf("send resultCode = %d", code)
	}
	_, _, push = readLOPush(t, ann)
	if content, has := avPushContent(t, push); !has || content != "Ben: outside" {
		t.Fatalf("the push to a member outside the room: content %q (present %v), want %q", content, has, "Ben: outside")
	}
	if to, payload := avPushOf(t, push); to != "9981" || payload["sndr"] != "9982" {
		t.Fatalf("push to %s payload %v", to, payload)
	}

	avatarChatBannerOn = false
	t.Cleanup(func() { avatarChatBannerOn = true })
	_, _, body = tcCall(t, ben, 4, acSend, `{"roomId":"1960000000","msgType":1,"msg":"silent","msgFilter":0}`)
	if code, _ := tcReplyOf(t, body); code != 0 {
		t.Fatalf("send resultCode = %d", code)
	}
	_, _, push = readLOPush(t, ann)
	if _, has := avPushContent(t, push); has {
		t.Fatalf("banner switched off, but the push carries content: %s", push)
	}
	if _, payload := avPushOf(t, push); payload["msgSn"] != float64(3) {
		t.Fatalf("silent push payload %v, want msgSn 3", payload)
	}
}

// avPushContent is the top-level content of a 13001 push body, and whether the key is there.
func avPushContent(t *testing.T, body []byte) (string, bool) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("push %s: %v", body, err)
	}
	c, has := got["content"]
	s, _ := c.(string)
	return s, has
}

// TestAvatarChatBannerText: the banner is "<name>: <text>" with the whitespace collapsed and cut to 40 runes.
func TestAvatarChatBannerText(t *testing.T) {
	if got := avatarChatBanner("Ben", "hi\n  there"); got != "Ben: hi there" {
		t.Fatalf("banner = %q", got)
	}
	long := avatarChatBanner("Ben", strings.Repeat("あ", 60))
	if n := len([]rune(long)); n != avatarChatBannerMax || !strings.HasPrefix(long, "Ben: ") {
		t.Fatalf("long banner = %d runes %q", n, long)
	}
}
