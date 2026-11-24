package lpn

import (
	"encoding/binary"
	"fmt"
	"net"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// The chat log keeps what players type through the handlers that take the messages: the 1:1 chat, Lounge Chat and the Square
// chat. A refused message, a sticker and a system line are not kept.

// callWithPushes sends one command on conn and returns the body of its reply. The pushes that reach conn first (a message
// to a member is pushed to the other member's session) are read and skipped.
func callWithPushes(t *testing.T, conn net.Conn, txn uint64, cmd int, payload string) []byte {
	t.Helper()
	prefix := []byte("\x01\x02{T}\x00") // T of 6 bytes, skipped by the server
	body := binary.BigEndian.AppendUint16(nil, uint16(len(prefix)))
	body = append(body, prefix...)
	body = append(body, fmt.Sprintf(`{"version":1,"command":%d,"userId":"someone-else","country":"JP","lang":"ja","payload":%s,"deviceType":2001}`, cmd, payload)...)
	tcSendRaw(t, conn, txn, body)
	for {
		op, got, reply := readLOPush(t, conn)
		if op == loOpThemeChatReply && got == txn {
			return reply
		}
	}
}

// TestChatLogKeepsTypedPrivateAndLoungeText: the 1:1 messages Ann and Ben sent are kept with their channel, sender and name;
// the refused send and the Lounge sticker are not; the Lounge text is kept with its room.
func TestChatLogKeepsTypedPrivateAndLoungeText(t *testing.T) {
	ann, ben, cy := avSetup(t)
	store.StartChatLog()
	t.Cleanup(store.StopChatLog)

	if code, _ := tcReplyOf(t, callWithPushes(t, ann, 1, acCreate, avMembers("9981", "9982"))); code != 0 {
		t.Fatalf("open resultCode = %d", code)
	}
	if code, _ := tcReplyOf(t, callWithPushes(t, ann, 2, acSend, `{"roomId":"1960000000","msgType":1,"msg":"  hello  ","msgFilter":0,"senderId":"9982"}`)); code != 0 {
		t.Fatalf("Ann send resultCode = %d", code)
	}
	if code, _ := tcReplyOf(t, callWithPushes(t, ben, 3, acSend, `{"roomId":"1960000000","msgType":1,"msg":"hi back","msgFilter":0}`)); code != 0 {
		t.Fatalf("Ben send resultCode = %d", code)
	}
	if code, _ := tcReplyOf(t, callWithPushes(t, cy, 4, acSend, `{"roomId":"1960000000","msgType":1,"msg":"sneak","msgFilter":0}`)); code != acRefused {
		t.Fatalf("stranger send resultCode = %d, want %d", code, acRefused)
	}

	resetLounge(t)
	room := economy.ActiveLounge().Rooms[0].RoomID
	join := fmt.Sprintf(`{"roomId":%d}`, room)
	callWithPushes(t, ann, 5, tcJoin, join)
	callWithPushes(t, ben, 6, tcJoin, join)
	if code, _ := tcReplyOf(t, callWithPushes(t, ann, 7, tcSend, fmt.Sprintf(`{"roomId":%d,"msgType":0,"msg":"lounge hi"}`, room))); code != tcOK {
		t.Fatalf("lounge send resultCode = %d", code)
	}
	if code, _ := tcReplyOf(t, callWithPushes(t, ann, 8, tcSend, fmt.Sprintf(`{"roomId":%d,"msgType":205,"msg":"ls_10_1"}`, room))); code != tcOK {
		t.Fatalf("sticker resultCode = %d", code)
	}

	store.FlushChatLog()
	rows, err := store.ChatLogSearch(store.ChatFilter{}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3 (the Lounge text, Ben's and Ann's 1:1 messages)", len(rows))
	}
	want := []struct{ kind, channel, sender, name, target, targetName, text string }{
		{store.ChatLounge, fmt.Sprint(room), "9981", "Ann", "", "", "lounge hi"},
		{store.ChatPrivate, "1960000000", "9982", "Ben", "9981", "Ann", "hi back"},
		{store.ChatPrivate, "1960000000", "9981", "Ann", "9982", "Ben", "hello"},
	}
	for i, w := range want {
		got := rows[i]
		if got.Kind != w.kind || got.Channel != w.channel || got.Sender != w.sender || got.SenderName != w.name ||
			got.Target != w.target || got.TargetName != w.targetName || got.Text != w.text {
			t.Errorf("row %d = %s %s %s %q -> %s %q %q, want %s %s %s %q -> %s %q %q",
				i, got.Kind, got.Channel, got.Sender, got.SenderName, got.Target, got.TargetName, got.Text,
				w.kind, w.channel, w.sender, w.name, w.target, w.targetName, w.text)
		}
		if got.Time <= 0 {
			t.Errorf("row %d has no time", i)
		}
	}
}

// TestChatLogKeepsSquareText: a Square message is kept with the Square's theme as its channel, and a message the Square
// refuses (here one that is not UTF-8) is not.
func TestChatLogKeepsSquareText(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22))
	testutil.UseDatabase(t)
	store.StartChatLog()
	t.Cleanup(store.StopChatLog)

	room := &squareRoom{}
	a, b := newSqClient(t, room, 1), newSqClient(t, room, 2)
	a.join(11)
	b.join(22)
	drainTo := func(c *sqClient, msgid uint16) {
		t.Helper()
		for {
			if id, _ := c.read(); id == msgid {
				return
			}
		}
	}
	drainTo(a, 2)
	b.send(8, pbLen(nil, 1, []byte{0xff, 0xfe}))
	b.send(8, pbLen(nil, 1, []byte("hi square")))
	drainTo(a, 11)
	drainTo(b, 11)

	store.FlushChatLog()
	rows, err := store.ChatLogSearch(store.ChatFilter{}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1 (the refused message is not kept)", len(rows))
	}
	if got := rows[0]; got.Kind != store.ChatSquare || got.Channel != fmt.Sprint(squareThemeID) || got.Sender != "22" || got.Text != "hi square" {
		t.Fatalf("square row = %s %s %s %q", got.Kind, got.Channel, got.Sender, got.Text)
	}
}
