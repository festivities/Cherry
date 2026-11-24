package lpn

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

type trunkCallMsg struct {
	sendAid    uint64
	targetAid  uint64
	chatSerial uint64
	msg        string
	step       int32
	result     int32
}

func trunkCallReq(targetAid uint64, msg string, step int32) []byte {
	b := pbVar(nil, 1, targetAid)
	b = pbLen(b, 2, []byte(msg))
	return pbVar(b, 3, uint64(step))
}

func decodeTrunkCall(t *testing.T, body []byte) trunkCallMsg {
	t.Helper()
	var m trunkCallMsg
	ok := pbScan(body, func(f, v uint64, d []byte) {
		switch f {
		case 1:
			m.sendAid = v
		case 2:
			m.targetAid = v
		case 3:
			m.chatSerial = v
		case 4:
			m.msg = string(d)
		case 5:
			m.step = int32(v)
		case 6:
			m.result = int32(v)
		}
	})
	if !ok {
		t.Fatalf("malformed rc_trunk_call: %x", body)
	}
	return m
}

func expectTrunkCall(t *testing.T, c *sqClient, want trunkCallMsg) trunkCallMsg {
	t.Helper()
	id, body := c.read()
	if id != squareRcTrunkCall {
		t.Fatalf("msgid = %d, want %d", id, squareRcTrunkCall)
	}
	got := decodeTrunkCall(t, body)
	if got.sendAid != want.sendAid || got.targetAid != want.targetAid ||
		got.msg != want.msg || got.step != want.step || got.result != want.result {
		t.Fatalf("rc_trunk_call = %+v, want %+v", got, want)
	}
	if want.chatSerial != 0 && got.chatSerial != want.chatSerial {
		t.Fatalf("rc_trunk_call chatSerial = %d, want %d", got.chatSerial, want.chatSerial)
	}
	return got
}

// setupWhisperRoom creates a Square room with the given AIDs joined and drains
// adduser / high-five frames so each client channel is empty.
func setupWhisperRoom(t *testing.T, aids ...uint64) (*squareRoom, []*sqClient) {
	t.Helper()
	testutil.InstallAccounts(t, withLoginAccounts(nil, aids...))
	room := &squareRoom{theme: squareThemeID, number: 1}
	rooms := &squareRooms{rooms: map[uint64][]*squareRoom{squareThemeID: {room}}}
	clients := make([]*sqClient, len(aids))
	for i, aid := range aids {
		c := newSqClientIn(t, rooms, byte(i+1))
		c.join(aid)
		for j := 0; j < i; j++ {
			if id, _ := clients[j].read(); id != 2 {
				t.Fatalf("client %d expected adduser(2), got %d", j, id)
			}
			if id, _ := clients[j].read(); id != squareRcHighFiveTargetList {
				t.Fatalf("client %d expected highfive target list, got %d", j, id)
			}
		}
		clients[i] = c
	}
	return room, clients
}

func rosterWhisperStep(t *testing.T, roster []byte, targetAid uint64) (step uint64, found bool) {
	t.Helper()
	ok := pbScan(roster, func(f, _ uint64, d []byte) {
		if f != 2 {
			return
		}
		aid, _ := protobufUintField(d, 1)
		if aid != targetAid {
			return
		}
		av, avFound, _ := pbBytesField(d, 2)
		if !avFound {
			return
		}
		step, found = protobufUintField(av, 19)
	})
	if !ok {
		t.Fatalf("malformed roster: %x", roster)
	}
	return step, found
}

func TestSquareWhisperFullFlowAndIsolation(t *testing.T) {
	testutil.UseDatabase(t)
	store.StartChatLog()
	t.Cleanup(store.StopChatLog)

	room, cs := setupWhisperRoom(t, 11, 22, 33)
	a, b, obs := cs[0], cs[1], cs[2]

	var logBuf bytes.Buffer
	a.sess.logger = log.New(&logBuf, "", 0)
	b.sess.logger = log.New(&logBuf, "", 0)

	// 1. A sends self-step 1 (RequestState) -> B and obs get it, A does not.
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 1))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 1})
	expectTrunkCall(t, obs, trunkCallMsg{sendAid: 11, targetAid: 11, step: 1})
	a.none()

	// 2. A sends peer request step 3 to B -> B receives it; A and obs do not.
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	req3 := expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, step: 3})
	if req3.chatSerial != 1 {
		t.Fatalf("first serial = %d, want 1", req3.chatSerial)
	}
	a.none()
	obs.none()

	// B sends self-step 3 (ReceiveState).
	b.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 22, step: 3})
	expectTrunkCall(t, obs, trunkCallMsg{sendAid: 22, targetAid: 22, step: 3})
	b.none()

	// 3. B accepts (self-step 5 + peer step 5 to A).
	b.send(squareCrTrunkCall, trunkCallReq(22, "", 5))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 22, step: 5})
	expectTrunkCall(t, obs, trunkCallMsg{sendAid: 22, targetAid: 22, step: 5})
	b.none()

	b.send(squareCrTrunkCall, trunkCallReq(11, "", 5))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 11, chatSerial: 2, step: 5})
	b.none()
	obs.none()

	// A enters MettingState (self-step 5).
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 5))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 5})
	expectTrunkCall(t, obs, trunkCallMsg{sendAid: 11, targetAid: 11, step: 5})
	a.none()

	// 4. A arrives and sends self-step 6 + peer step 6 (msg="") ->
	//    B AND obs receive peer step 6 (for 06_chat_noti), A does not get echo.
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 6))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 6})
	expectTrunkCall(t, obs, trunkCallMsg{sendAid: 11, targetAid: 11, step: 6})
	a.none()

	a.send(squareCrTrunkCall, trunkCallReq(22, "", 6))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 3, step: 6})
	expectTrunkCall(t, obs, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 3, step: 6})
	a.none()

	b.send(squareCrTrunkCall, trunkCallReq(22, "", 6))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 22, step: 6})
	expectTrunkCall(t, obs, trunkCallMsg{sendAid: 22, targetAid: 22, step: 6})
	b.none()

	// 5. Private chat messages: echoed to sender and delivered to target ONLY.
	a.send(squareCrTrunkCall, trunkCallReq(22, "  secret-one\x00 ", 6))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 4, msg: "secret-one", step: 6})
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 4, msg: "secret-one", step: 6})
	obs.none()

	b.send(squareCrTrunkCall, trunkCallReq(11, "secret-two", 6))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 22, targetAid: 11, chatSerial: 5, msg: "secret-two", step: 6})
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 11, chatSerial: 5, msg: "secret-two", step: 6})
	obs.none()

	// 6. Verify chat log recorded both messages and stdout/logger never saw message text.
	store.FlushChatLog()
	rows, err := store.ChatLogSearch(store.ChatFilter{Kind: store.ChatSquare}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Text != "secret-two" || rows[1].Text != "secret-one" {
		t.Fatalf("chat_log rows = %+v", rows)
	}
	if strings.Contains(logBuf.String(), "secret-one") || strings.Contains(logBuf.String(), "secret-two") {
		t.Fatalf("logger leaked private whisper text: %s", logBuf.String())
	}

	// 7. Latecomer sees _AvatarInfo field 19 = 6 for A and B, and 0/omitted for obs.
	d := newSqClientIn(t, &squareRooms{rooms: map[uint64][]*squareRoom{squareThemeID: {room}}}, 4)
	testutil.InstallAccounts(t, withLoginAccounts(nil, 11, 22, 33, 44))
	rosterD := d.join(44)
	if step, ok := rosterWhisperStep(t, rosterD, 11); !ok || step != 6 {
		t.Fatalf("latecomer A whisperStep = (%d, %v), want (6, true)", step, ok)
	}
	if step, ok := rosterWhisperStep(t, rosterD, 22); !ok || step != 6 {
		t.Fatalf("latecomer B whisperStep = (%d, %v), want (6, true)", step, ok)
	}
	if step, ok := rosterWhisperStep(t, rosterD, 33); ok {
		t.Fatalf("latecomer obs whisperStep = (%d, %v), want omitted", step, ok)
	}
}

func TestSquareWhisperDeclineCancelDisconnect(t *testing.T) {
	room, cs := setupWhisperRoom(t, 11, 22)
	a, b := cs[0], cs[1]

	// Decline (step 4): A requests B, B declines.
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 1))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 1})
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 1, step: 3})

	b.send(squareCrTrunkCall, trunkCallReq(11, "", 4))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 11, chatSerial: 2, step: 4})
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 0))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 0})

	room.mu.Lock()
	if a.sess.player.whisperPartner != 0 || b.sess.player.whisperPartner != 0 {
		t.Fatalf("partners after decline = (%d, %d), want (0, 0)", a.sess.player.whisperPartner, b.sess.player.whisperPartner)
	}
	room.mu.Unlock()

	// Cancel (step 2): A requests B, A cancels.
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 1))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 1})
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 3, step: 3})
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 2))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 4, step: 2})
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 0))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 0})

	room.mu.Lock()
	if a.sess.player.whisperPartner != 0 || b.sess.player.whisperPartner != 0 {
		t.Fatalf("partners after cancel = (%d, %d), want (0, 0)", a.sess.player.whisperPartner, b.sess.player.whisperPartner)
	}
	room.mu.Unlock()

	// Disconnect (step 7): A and B accept & connect, then A disconnects.
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 5, step: 3})
	b.send(squareCrTrunkCall, trunkCallReq(11, "", 5))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 11, chatSerial: 6, step: 5})
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 6))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 7, step: 6})

	a.send(squareCrTrunkCall, trunkCallReq(22, "", 7))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 8, step: 7})

	room.mu.Lock()
	if a.sess.player.whisperPartner != 0 || b.sess.player.whisperPartner != 0 {
		t.Fatalf("partners after disconnect = (%d, %d), want (0, 0)", a.sess.player.whisperPartner, b.sess.player.whisperPartner)
	}
	room.mu.Unlock()

	// Private chat after disconnect is refused with 458756 (not paired).
	a.send(squareCrTrunkCall, trunkCallReq(22, "after disconnect", 6))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 22, step: 6, result: squareWhisperErrNotPaired})
	b.none()
}

func TestSquareWhisperBusyAndMutualTieBreaker(t *testing.T) {
	_, cs := setupWhisperRoom(t, 11, 22, 33)
	a, b, c := cs[0], cs[1], cs[2]

	// A and B enter whisper step 6.
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 1))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 1})
	expectTrunkCall(t, c, trunkCallMsg{sendAid: 11, targetAid: 11, step: 1})

	a.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 1, step: 3})

	b.send(squareCrTrunkCall, trunkCallReq(22, "", 6))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 22, step: 6})
	expectTrunkCall(t, c, trunkCallMsg{sendAid: 22, targetAid: 22, step: 6})

	b.send(squareCrTrunkCall, trunkCallReq(11, "", 5))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 11, chatSerial: 2, step: 5})

	// C tries to request B while B is busy with A -> 458755 to C only.
	c.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	expectTrunkCall(t, c, trunkCallMsg{sendAid: 22, targetAid: 22, step: 4, result: squareWhisperErrBusy})
	a.none()
	b.none()

	// Reset A and B back to idle.
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 7))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 3, step: 7})
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 0))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 0})
	expectTrunkCall(t, c, trunkCallMsg{sendAid: 11, targetAid: 11, step: 0})
	b.send(squareCrTrunkCall, trunkCallReq(22, "", 0))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 22, step: 0})
	expectTrunkCall(t, c, trunkCallMsg{sendAid: 22, targetAid: 22, step: 0})

	// Simultaneous mutual request: A requests B (self 1 + peer 3), then B simultaneously
	// requests A (self 1 + peer 3) before processing A's request. Must NOT bounce with 458755.
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 1))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 1})
	expectTrunkCall(t, c, trunkCallMsg{sendAid: 11, targetAid: 11, step: 1})
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 4, step: 3})

	b.send(squareCrTrunkCall, trunkCallReq(22, "", 1))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 22, step: 1})
	expectTrunkCall(t, c, trunkCallMsg{sendAid: 22, targetAid: 22, step: 1})
	b.send(squareCrTrunkCall, trunkCallReq(11, "", 3))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 11, chatSerial: 5, step: 3})
	b.none()
}

func TestSquareWhisperErrorsAndValidation(t *testing.T) {
	_, cs := setupWhisperRoom(t, 11, 22)
	a, b := cs[0], cs[1]

	// Target not in room -> 458754.
	a.send(squareCrTrunkCall, trunkCallReq(9999, "", 3))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 9999, targetAid: 9999, step: 3, result: squareWhisperErrNotFound})

	// Missing / zero target -> 458754.
	a.send(squareCrTrunkCall, trunkCallReq(0, "", 3))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 0, targetAid: 0, step: 3, result: squareWhisperErrNotFound})

	// Out-of-range step -> ignored.
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 8))
	a.none()
	b.none()

	// Chat to missing target -> 458754.
	a.send(squareCrTrunkCall, trunkCallReq(9999, "hello", 6))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 9999, targetAid: 9999, step: 6, result: squareWhisperErrNotFound})

	// Chat when not paired -> 458756.
	a.send(squareCrTrunkCall, trunkCallReq(22, "hello", 6))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 22, step: 6, result: squareWhisperErrNotPaired})

	// Pair A and B, then test rejected message payloads.
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 1, step: 3})
	b.send(squareCrTrunkCall, trunkCallReq(11, "", 5))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 11, chatSerial: 2, step: 5})
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 6))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 3, step: 6})

	// Blank/NUL-only, invalid UTF-8, > 200 runes, > 500 bytes are all dropped.
	a.send(squareCrTrunkCall, trunkCallReq(22, "  \x00  ", 6))
	a.send(squareCrTrunkCall, trunkCallReq(22, string([]byte{0xff, 0xfe}), 6))
	a.send(squareCrTrunkCall, trunkCallReq(22, strings.Repeat("a", squareWhisperMaxRunes+1), 6))
	a.send(squareCrTrunkCall, trunkCallReq(22, strings.Repeat("あ", 167), 6)) // 167 runes, 501 bytes
	a.none()
	b.none()
}

func TestSquareWhisperLeaveCleanup(t *testing.T) {
	room, cs := setupWhisperRoom(t, 11, 22)
	a, b := cs[0], cs[1]

	// Put A (step 1) and B (step 3) in pending whisper.
	a.send(squareCrTrunkCall, trunkCallReq(11, "", 1))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 11, step: 1})
	a.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	expectTrunkCall(t, b, trunkCallMsg{sendAid: 11, targetAid: 22, chatSerial: 1, step: 3})
	b.send(squareCrTrunkCall, trunkCallReq(22, "", 3))
	expectTrunkCall(t, a, trunkCallMsg{sendAid: 22, targetAid: 22, step: 3})

	// A leaves the room; B gets rc_room_deluser and B's whisperPartner + pending step 3 are cleared.
	a.sess.close()
	b.expect(3, []byte{0x08, 11, 0x10, 0})

	room.mu.Lock()
	gotPartner := b.sess.player.whisperPartner
	gotStep := b.sess.player.whisperStep
	room.mu.Unlock()
	if gotPartner != 0 || gotStep != 0 {
		t.Fatalf("B after partner leave: partner=%d step=%d, want (0, 0)", gotPartner, gotStep)
	}
}
