package lpn

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/store"
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

// resetAvatarChat empties the 1:1 registry: no rooms, and the first room id next.
func resetAvatarChat(t *testing.T) {
	t.Helper()
	avatarChatMu.Lock()
	defer avatarChatMu.Unlock()
	avatarChatRooms = map[int64]*avatarChatRoom{}
	avatarChatPairs = map[[2]uint64]int64{}
	avatarChatNext = CherryChatRoomFirst
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
	if got := get("av-9981"); len(got) != 0 { // the route never lists rooms: a listed room crashed the client at login
		t.Fatalf("Ann's route list has %d rows, want 0", len(got))
	}
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
