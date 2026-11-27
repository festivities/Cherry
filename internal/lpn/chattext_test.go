package lpn

import (
	"fmt"
	"strings"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/metrics"
	"cherry/internal/testutil"
)

// crashText is the shape of the 2026-11-27 crash string: one Thai letter carrying 49 stacked marks.
var crashText = "\u0e01" + strings.Repeat("\u0e39", 25) + strings.Repeat("\u0e47", 24)

// TestSquareChatRefusesStackedMarks: the crash string never reaches the room (the next message the others see is the
// ordinary one sent after it) and the refusal is counted.
func TestSquareChatRefusesStackedMarks(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22))
	room := &squareRoom{}
	a, b := newSqClient(t, room, 1), newSqClient(t, room, 2)
	a.join(11)
	b.join(22)
	for {
		if id, _ := a.read(); id == 2 { // b's arrival
			break
		}
	}
	before := metrics.TextRefused.Load()
	b.send(8, pbLen(nil, 1, []byte(crashText)))
	b.send(8, pbLen(nil, 1, []byte("\u0e43\u0e0a\u0e48\u0e48\u0e48 ok"))) // ordinary Thai with three stacked marks
	for {
		id, body := a.read()
		if id != 11 {
			continue
		}
		if msg, _, _ := pbBytesField(body, 3); string(msg) != "\u0e43\u0e0a\u0e48\u0e48\u0e48 ok" {
			t.Fatalf("first chat in the room = %q, want the ordinary message", msg)
		}
		break
	}
	if got := metrics.TextRefused.Load() - before; got != 1 {
		t.Fatalf("TextRefused grew by %d, want 1", got)
	}
}

// TestSessionChatRefusesStackedMarks: Lounge Chat and the 1:1 chat answer the crash string with their refusal codes and
// still take an ordinary message.
func TestSessionChatRefusesStackedMarks(t *testing.T) {
	ann, ben, _ := avSetup(t)
	if code, _ := tcReplyOf(t, callWithPushes(t, ann, 1, acCreate, avMembers("9981", "9982"))); code != 0 {
		t.Fatalf("open resultCode = %d", code)
	}
	send := fmt.Sprintf(`{"roomId":"1960000000","msgType":1,"msg":%q,"msgFilter":0}`, crashText)
	if code, _ := tcReplyOf(t, callWithPushes(t, ann, 2, acSend, send)); code != acRefused {
		t.Fatalf("1:1 crash text resultCode = %d, want %d", code, acRefused)
	}
	if code, _ := tcReplyOf(t, callWithPushes(t, ann, 3, acSend, `{"roomId":"1960000000","msgType":1,"msg":"hello","msgFilter":0}`)); code != 0 {
		t.Fatalf("1:1 ordinary resultCode = %d", code)
	}

	resetLounge(t)
	room := economy.ActiveLounge().Rooms[0].RoomID
	callWithPushes(t, ann, 4, tcJoin, fmt.Sprintf(`{"roomId":%d}`, room))
	callWithPushes(t, ben, 5, tcJoin, fmt.Sprintf(`{"roomId":%d}`, room))
	if code, _ := tcReplyOf(t, callWithPushes(t, ann, 6, tcSend, fmt.Sprintf(`{"roomId":%d,"msgType":0,"msg":%q}`, room, crashText))); code != tcRefused {
		t.Fatalf("lounge crash text resultCode = %d, want %d", code, tcRefused)
	}
	if code, _ := tcReplyOf(t, callWithPushes(t, ann, 7, tcSend, fmt.Sprintf(`{"roomId":%d,"msgType":0,"msg":"lounge hi"}`, room))); code != tcOK {
		t.Fatalf("lounge ordinary resultCode = %d", code)
	}
}
