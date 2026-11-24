package lpn

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

func expectNoSessionPush(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var buf [1]byte
	if n, err := conn.Read(buf[:]); n > 0 || err == nil {
		t.Fatalf("unexpected session push bytes (%d)", n)
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		if err != io.EOF {
			t.Fatalf("unexpected session read err: %v", err)
		}
	}
}

func assertPushNotiJSON(t *testing.T, payload []byte, wantCatg, wantMsgID, wantSender, wantContent, wantPType string, wantBadge int, wantBadgeInfo map[string]float64) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("push payload is not JSON: %v (%s)", err, payload)
	}
	if got["catg"] != wantCatg {
		t.Fatalf("catg = %#v, want %q", got["catg"], wantCatg)
	}
	if got["msgid"] != wantMsgID {
		t.Fatalf("msgid = %#v, want %q", got["msgid"], wantMsgID)
	}
	if got["sender"] != wantSender {
		t.Fatalf("sender = %#v, want %q", got["sender"], wantSender)
	}
	if got["content"] != wantContent {
		t.Fatalf("content = %#v, want %q", got["content"], wantContent)
	}
	if b, ok := got["badge"].(float64); !ok || int(b) != wantBadge {
		t.Fatalf("badge = %#v, want number %d", got["badge"], wantBadge)
	}
	if sid, ok := got["sid"].(string); !ok || sid != "" {
		t.Fatalf("sid = %#v, want empty string", got["sid"])
	}
	if cc, ok := got["customContent"].(bool); !ok || cc {
		t.Fatalf("customContent = %#v, want false", got["customContent"])
	}
	params, ok := got["params"].(map[string]any)
	if !ok {
		t.Fatalf("params = %#v, want object", got["params"])
	}
	if params["pType"] != wantPType {
		t.Fatalf("params.pType = %#v, want %q", params["pType"], wantPType)
	}
	rawInfo, ok := params["badgeInfo"].(map[string]any)
	if !ok {
		t.Fatalf("params.badgeInfo = %#v, want object", params["badgeInfo"])
	}
	wantFull := map[string]any{
		"NEWS": float64(0), "CHAT": float64(0), "NFRD": float64(0), "IFRD": float64(0),
		"GIFT": float64(0), "CSET": float64(0), "FACE": float64(0), "ROOM": float64(0),
	}
	for k, v := range wantBadgeInfo {
		wantFull[k] = v
	}
	if !reflect.DeepEqual(rawInfo, wantFull) {
		t.Fatalf("params.badgeInfo = %#v, want %#v", rawInfo, wantFull)
	}
}

func TestPushHeartNoti(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"pn-9971": {AccessToken: "pn-9971", SessionKey: "sk-9971", Aid: "9971", Name: "Alice"},
		"pn-9972": {AccessToken: "pn-9972", SessionKey: "sk-9972", Aid: "9972", Name: "Bob"},
	})
	now := time.UnixMilli(1732450000123)
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })

	conn := inviteeSession(t, "sk-9972")

	if !PushHeartNoti("9972", "9971", "Alice") {
		t.Fatal("PushHeartNoti returned false for live session")
	}
	op, txn, payload := readLOPush(t, conn)
	if op != loOpPush || txn != 0 {
		t.Fatalf("op=%#02x txn=%d, want 0x1b txn 0", op, txn)
	}
	assertPushNotiJSON(t, payload, "heart", "1732450000123", "9971", "Alice sent you a Heart!", "HTR", 1, nil)

	if !PushHeartNoti("9972", "9971", "") {
		t.Fatal("PushHeartNoti empty name returned false")
	}
	op, txn, payload = readLOPush(t, conn)
	if op != loOpPush || txn != 0 {
		t.Fatalf("op=%#02x txn=%d, want 0x1b txn 0", op, txn)
	}
	assertPushNotiJSON(t, payload, "heart", "1732450000123", "9971", "Someone sent you a Heart!", "HTR", 1, nil)

	for _, tc := range []struct {
		target, sender string
	}{
		{"", "9971"},
		{"9972", "9972"},
		{"9971", "9971"},
		{"9979", "9971"}, // offline
		{"invalid", "9971"},
		{"0", "9971"},
	} {
		if PushHeartNoti(tc.target, tc.sender, "Alice") {
			t.Fatalf("PushHeartNoti(%q, %q) = true, want false", tc.target, tc.sender)
		}
	}
	expectNoSessionPush(t, conn)
}

func TestPushFriendNoti(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"pn-9973": {AccessToken: "pn-9973", SessionKey: "sk-9973", Aid: "9973", Name: "Alice"},
		"pn-9974": {AccessToken: "pn-9974", SessionKey: "sk-9974", Aid: "9974", Name: "Bob"},
	})
	now := time.UnixMilli(1732450000456)
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })

	conn := inviteeSession(t, "sk-9974")

	if !PushFriendNoti("9974", "9973", "Alice", "FRR", 2, 0) {
		t.Fatal("PushFriendNoti FRR returned false")
	}
	op, txn, payload := readLOPush(t, conn)
	if op != loOpPush || txn != 0 {
		t.Fatalf("op=%#02x txn=%d, want 0x1b txn 0", op, txn)
	}
	assertPushNotiJSON(t, payload, "friend", "1732450000456", "9973", "Alice sent you a friend request.", "FRR", 2, map[string]float64{"IFRD": 2})

	if !PushFriendNoti("9974", "9973", "Alice", "FRA", 0, 1) {
		t.Fatal("PushFriendNoti FRA returned false")
	}
	op, txn, payload = readLOPush(t, conn)
	if op != loOpPush || txn != 0 {
		t.Fatalf("op=%#02x txn=%d, want 0x1b txn 0", op, txn)
	}
	assertPushNotiJSON(t, payload, "friend", "1732450000456", "9973", "Alice accepted your friend request.", "FRA", 1, map[string]float64{"NFRD": 1})

	if !PushFriendNoti("9974", "9973", "", "FRR", 0, 0) {
		t.Fatal("PushFriendNoti empty name returned false")
	}
	_, _, payload = readLOPush(t, conn)
	assertPushNotiJSON(t, payload, "friend", "1732450000456", "9973", "Someone sent you a friend request.", "FRR", 1, nil)

	for _, tc := range []struct {
		target, sender, pType string
	}{
		{"", "9973", "FRR"},
		{"9974", "9974", "FRR"},
		{"9979", "9973", "FRR"}, // offline
		{"9974", "9973", "BAD"}, // unknown pType
	} {
		if PushFriendNoti(tc.target, tc.sender, "Alice", tc.pType, 1, 0) {
			t.Fatalf("PushFriendNoti(%q, %q, %q) = true, want false", tc.target, tc.sender, tc.pType)
		}
	}
	expectNoSessionPush(t, conn)
}

func TestTLUPHeartGatewayPush(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"th-9761": {AccessToken: "th-9761", SessionKey: "sk-9761", Aid: "9761", Name: "Sender"},
		"th-9762": {AccessToken: "th-9762", SessionKey: "sk-9762", Aid: "9762", Name: "Receiver"},
	})
	testutil.SetLab(t, "9761", false)
	testutil.SetLab(t, "9762", false)
	now := time.Date(2026, time.November, 24, 12, 0, 0, 0, time.FixedZone("JST", 9*3600))
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })

	recvConn := inviteeSession(t, "sk-9762")
	senderConn := inviteeSession(t, "sk-9761")
	gwClient, stop := heartGateway(t)
	defer stop()

	sendTLUPHeart := func(pktNo byte, target uint64) {
		t.Helper()
		frame := tlupDFrame(heartTestSession, 1, tlupClientEventKey("sk-9761", 9761, uint64(pktNo), heartEvent(target)))
		if _, err := gwClient.Write(frame); err != nil {
			t.Fatalf("write heart frame: %v", err)
		}
		if got := readFullDeadline(t, gwClient, 24); !bytes.Equal(got, tlupDFrame(heartTestSession, 1, []byte{0x08, pktNo})) {
			t.Fatalf("heart ack = %x", got)
		}
	}

	// Self-heart is rejected by HeartSend and must not push.
	sendTLUPHeart(1, 9761)
	expectNoSessionPush(t, senderConn)
	expectNoSessionPush(t, recvConn)

	// Valid hearts push to receiver until the daily cap is reached.
	for i := byte(1); i <= byte(economy.HeartBase()); i++ {
		sendTLUPHeart(i+1, 9762)
		op, txn, payload := readLOPush(t, recvConn)
		if op != loOpPush || txn != 0 {
			t.Fatalf("heart %d push op=%#02x txn=%d", i, op, txn)
		}
		assertPushNotiJSON(t, payload, "heart", "1795489200000", "9761", "Sender sent you a Heart!", "HTR", 1, nil)
	}

	// Over-cap heart is refused and must not push.
	sendTLUPHeart(99, 9762)
	expectNoSessionPush(t, recvConn)
}
