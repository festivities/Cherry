package web

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/lpn"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

func readSessionLOFrame(t *testing.T, conn net.Conn) (byte, uint64, []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var prefix [4]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		t.Fatalf("read push length: %v", err)
	}
	n := binary.BigEndian.Uint32(prefix[:])
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		t.Fatalf("read push body: %v", err)
	}
	if len(body) < 16 || body[0] != 'L' || body[1] != 'O' || body[2] != 0x10 {
		t.Fatalf("not an LO frame: %x", body)
	}
	return body[3], binary.BigEndian.Uint64(body[8:16]), body[16:]
}

func expectNoWebSessionPush(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var buf [1]byte
	if n, err := conn.Read(buf[:]); n > 0 || err == nil {
		t.Fatalf("unexpected session push (%d bytes)", n)
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("unexpected session read err: %v", err)
	}
}

func startTestSessionConn(t *testing.T, sessionKey string, aid uint64) net.Conn {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go lpn.ServeSession(ln, log.New(io.Discard, "", 0))
	t.Cleanup(func() { _ = ln.Close() })
	conn := bindLPNSession(t, ln.Addr().String(), sessionKey)
	t.Cleanup(func() {
		_ = conn.Close()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && lpn.SessionOnline(aid) {
			time.Sleep(10 * time.Millisecond)
		}
	})
	return conn
}

func buildTLUPHeartFrame(sessionKey string, senderAid, packetNo, targetAid uint64) []byte {
	var sess []byte
	sess = append(sess, 0x0a, byte(len(sessionKey)))
	sess = append(sess, sessionKey...)
	sess = append(sess, 0x10)
	sess = binary.AppendUvarint(sess, senderAid)

	var param []byte
	param = append(param, 0x0a, 7)
	param = append(param, "youraid"...)
	param = append(param, 0x10)
	param = binary.AppendUvarint(param, targetAid)

	var ev []byte
	ev = binary.AppendUvarint(append(ev, 0x08), 102)
	ev = binary.AppendUvarint(append(ev, 0x10), 12)
	ev = append(ev, 0x1a, byte(len(param)))
	ev = append(ev, param...)

	var req []byte
	req = append(req, 0x0a, byte(len(sess)))
	req = append(req, sess...)
	req = binary.AppendUvarint(append(req, 0x10), packetNo)
	req = append(req, 0x1a, byte(len(ev)))
	req = append(req, ev...)

	frame := make([]byte, 22+len(req))
	binary.BigEndian.PutUint32(frame, uint32(18+len(req)))
	frame[4], frame[5] = 'D', 0x00
	copy(frame[6:14], []byte{1, 2, 3, 4, 5, 6, 7, 8})
	frame[14] = 5
	binary.BigEndian.PutUint32(frame[15:19], 1)
	binary.BigEndian.PutUint16(frame[19:21], 1) // msgid 1
	// Note: frame[20:22] is msgid; frame starts at 4, so header is 18 bytes: 4..21
	binary.BigEndian.PutUint16(frame[20:22], 1)
	copy(frame[22:], req)
	return frame
}

func TestWebHeartNotiDelivery(t *testing.T) {
	testutil.UseDatabase(t)
	accts := map[string]*store.Account{
		"hn-9981": {AccessToken: "hn-9981", SessionKey: "sk-hn-9981", Aid: "9981", Name: "Sender"},
		"hn-9982": {AccessToken: "hn-9982", SessionKey: "sk-hn-9982", Aid: "9982", Name: "Receiver"},
	}
	base := int(economy.HeartBase())
	for i := range base {
		aid := strconv.Itoa(9983 + i)
		accts["hn-"+aid] = &store.Account{AccessToken: "hn-" + aid, SessionKey: "sk-hn-" + aid, Aid: aid, Name: "Other" + aid}
	}
	installSocialTestAccounts(t, accts)
	testutil.SetLab(t, "9981", false)
	testutil.SetLab(t, "9982", false)
	for i := range base {
		testutil.SetLab(t, strconv.Itoa(9983+i), false)
	}
	clock := heartTestClock(t, heartTestNow())

	recvConn := startTestSessionConn(t, "sk-hn-9982", 9982)

	oldGW := lpn.GatewayAddr
	lpn.GatewayAddr = "127.0.0.1:0"
	t.Cleanup(func() { lpn.GatewayAddr = oldGW })
	gwLn, err := lpn.ListenObserver("127.0.0.1:0", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gwLn.Close() })
	gwConn, err := net.Dial("tcp", gwLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gwConn.Close() })
	var banner [15]byte
	if _, err := io.ReadFull(gwConn, banner[:]); err != nil {
		t.Fatal(err)
	}

	sendHeart := func(pktNo uint64, target uint64) {
		t.Helper()
		if _, err := gwConn.Write(buildTLUPHeartFrame("sk-hn-9981", 9981, pktNo, target)); err != nil {
			t.Fatal(err)
		}
		var ack [24]byte
		if _, err := io.ReadFull(gwConn, ack[:]); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ack[22:], []byte{0x08, byte(pktNo)}) {
			t.Fatalf("unexpected TLUP ack tail %x", ack)
		}
	}

	// Self-heart must not push.
	sendHeart(1, 9981)
	expectNoWebSessionPush(t, recvConn)

	// Valid heart pushes catg:"heart", params.pType:"HTR".
	assertHeartPush := func() {
		t.Helper()
		op, txn, payload := readSessionLOFrame(t, recvConn)
		if op != 0x1B || txn != 0 {
			t.Fatalf("op=%#02x txn=%d, want 0x1b txn 0", op, txn)
		}
		var got map[string]any
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("unmarshal push: %v", err)
		}
		params, _ := got["params"].(map[string]any)
		if got["catg"] != "heart" || params["pType"] != "HTR" || got["sender"] != "9981" || got["content"] != "Sender sent you a Heart!" {
			t.Fatalf("unexpected heart push: %s", payload)
		}
	}
	sendHeart(2, 9982)
	assertHeartPush()

	// Immediate repeat to the same target during cooldown must not push.
	sendHeart(3, 9982)
	expectNoWebSessionPush(t, recvConn)

	// After cooldown expires, hearting 9982 again pushes HTR.
	*clock = clock.Add(economy.HeartCooldown())
	sendHeart(4, 9982)
	assertHeartPush()

	// Exhaust remaining daily hearts to distinct targets on the current day; over-cap heart to a fresh target must not push.
	for i := 0; i < base-1; i++ {
		if sent, _ := economy.HeartSend("9981", strconv.Itoa(9983+i), *clock); !sent {
			t.Fatalf("heart %d refused", i+2)
		}
	}
	sendHeart(99, uint64(9983+base-1))
	expectNoWebSessionPush(t, recvConn)
}

func badgeInfosFor(t *testing.T, token string) map[string]float64 {
	t.Helper()
	rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/badge/infos/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v4/badge/infos/ = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Result map[string]float64 `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode badge infos: %v (%s)", err, rec.Body.String())
	}
	return out.Result
}

func TestWebFriendRequestAcceptPushAndBadges(t *testing.T) {
	testutil.UseDatabase(t)
	installSocialTestAccounts(t, map[string]*store.Account{
		"fn-9991": {AccessToken: "fn-9991", SessionKey: "sk-fn-9991", Aid: "9991", Name: "Alice"},
		"fn-9992": {AccessToken: "fn-9992", SessionKey: "sk-fn-9992", Aid: "9992", Name: "Bob"},
		"fn-9993": {AccessToken: "fn-9993", SessionKey: "sk-fn-9993", Aid: "9993", Name: "Carol"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	aliceConn := startTestSessionConn(t, "sk-fn-9991", 9991)
	bobConn := startTestSessionConn(t, "sk-fn-9992", 9992)

	if got := badgeInfosFor(t, "fn-9992")["IFRD"]; got != 0 {
		t.Fatalf("initial IFRD = %v, want 0", got)
	}

	// Alice applies to Bob -> FRR push with IFRD=1, badge/infos reports IFRD=1.
	if rec := socialTestRequestAs(t, "fn-9991", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"9992"}`); rec.Code != http.StatusOK {
		t.Fatalf("apply Alice->Bob = %d %s", rec.Code, rec.Body.String())
	}
	op, txn, payload := readSessionLOFrame(t, bobConn)
	if op != 0x1B || txn != 0 {
		t.Fatalf("FRR push op=%#02x txn=%d", op, txn)
	}
	var frr map[string]any
	if err := json.Unmarshal(payload, &frr); err != nil {
		t.Fatal(err)
	}
	frrParams, _ := frr["params"].(map[string]any)
	frrBadges, _ := frrParams["badgeInfo"].(map[string]any)
	if frr["catg"] != "friend" || frrParams["pType"] != "FRR" || frr["sender"] != "9991" || frrBadges["IFRD"] != float64(1) {
		t.Fatalf("unexpected FRR push: %s", payload)
	}
	if got := badgeInfosFor(t, "fn-9992")["IFRD"]; got != 1 {
		t.Fatalf("IFRD after 1st request = %v, want 1", got)
	}

	// Duplicate apply Alice->Bob -> no push.
	if rec := socialTestRequestAs(t, "fn-9991", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"9992"}`); rec.Code != http.StatusOK {
		t.Fatalf("duplicate apply = %d", rec.Code)
	}
	expectNoWebSessionPush(t, bobConn)

	// Carol applies to Bob -> FRR push with IFRD=2, badge/infos reports IFRD=2.
	if rec := socialTestRequestAs(t, "fn-9993", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"9992"}`); rec.Code != http.StatusOK {
		t.Fatalf("apply Carol->Bob = %d", rec.Code)
	}
	_, _, payload = readSessionLOFrame(t, bobConn)
	if err := json.Unmarshal(payload, &frr); err != nil {
		t.Fatal(err)
	}
	frrParams, _ = frr["params"].(map[string]any)
	frrBadges, _ = frrParams["badgeInfo"].(map[string]any)
	if frrParams["pType"] != "FRR" || frr["sender"] != "9993" || frrBadges["IFRD"] != float64(2) {
		t.Fatalf("unexpected 2nd FRR push: %s", payload)
	}
	if got := badgeInfosFor(t, "fn-9992")["IFRD"]; got != 2 {
		t.Fatalf("IFRD after 2nd request = %v, want 2", got)
	}

	// Bob accepts Alice -> Alice gets FRA with NFRD=1, Bob's IFRD drops to 1.
	if rec := socialTestRequestAs(t, "fn-9992", http.MethodPut, "/v4/friend/accept/9991", "null"); rec.Code != http.StatusOK {
		t.Fatalf("accept Alice = %d %s", rec.Code, rec.Body.String())
	}
	op, txn, payload = readSessionLOFrame(t, aliceConn)
	if op != 0x1B || txn != 0 {
		t.Fatalf("FRA push op=%#02x txn=%d", op, txn)
	}
	var fra map[string]any
	if err := json.Unmarshal(payload, &fra); err != nil {
		t.Fatal(err)
	}
	fraParams, _ := fra["params"].(map[string]any)
	fraBadges, _ := fraParams["badgeInfo"].(map[string]any)
	if fra["catg"] != "friend" || fraParams["pType"] != "FRA" || fra["sender"] != "9992" || fraBadges["NFRD"] != float64(1) {
		t.Fatalf("unexpected FRA push: %s", payload)
	}
	if got := badgeInfosFor(t, "fn-9992")["IFRD"]; got != 1 {
		t.Fatalf("IFRD after accepting Alice = %v, want 1", got)
	}

	// Idempotent re-accept does not push again.
	if rec := socialTestRequestAs(t, "fn-9992", http.MethodPut, "/v4/friend/accept/9991", "null"); rec.Code != http.StatusOK {
		t.Fatalf("repeat accept = %d", rec.Code)
	}
	expectNoWebSessionPush(t, aliceConn)

	// Bob accepts Carol -> Bob's IFRD drops to 0.
	if rec := socialTestRequestAs(t, "fn-9992", http.MethodPut, "/v4/friend/accept/9993", "null"); rec.Code != http.StatusOK {
		t.Fatalf("accept Carol = %d", rec.Code)
	}
	if got := badgeInfosFor(t, "fn-9992")["IFRD"]; got != 0 {
		t.Fatalf("IFRD after accepting Carol = %v, want 0", got)
	}

	// Mutual auto-accept: remove Alice<->Bob, Alice applies, then Bob applies -> Alice gets FRA (not FRR).
	if rec := socialTestRequestAs(t, "fn-9991", http.MethodPost, "/v4/r/friend/remove/", `["9992"]`); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d", rec.Code)
	}
	if rec := socialTestRequestAs(t, "fn-9991", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"9992"}`); rec.Code != http.StatusOK {
		t.Fatalf("re-apply Alice->Bob = %d", rec.Code)
	}
	_, _, _ = readSessionLOFrame(t, bobConn) // Alice->Bob FRR
	if rec := socialTestRequestAs(t, "fn-9992", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"9991"}`); rec.Code != http.StatusOK {
		t.Fatalf("mutual apply Bob->Alice = %d", rec.Code)
	}
	_, _, payload = readSessionLOFrame(t, aliceConn)
	if err := json.Unmarshal(payload, &fra); err != nil {
		t.Fatal(err)
	}
	fraParams, _ = fra["params"].(map[string]any)
	fraBadges, _ = fraParams["badgeInfo"].(map[string]any)
	if fra["catg"] != "friend" || fraParams["pType"] != "FRA" || fraBadges["NFRD"] != float64(1) {
		t.Fatalf("mutual auto-accept push = %s, want FRA", payload)
	}
	expectNoWebSessionPush(t, bobConn)

	// Badge set & reset routes return 200.
	for _, target := range []string{
		"/v4/badge/IFRD/0",
		"/v4/r/badge/reset/NFRD",
		"/v4/r/badge/reset/ALERT",
		"/v4/r/badge/reset/NEWS",
		"/v4/r/badge/reset/IFRD",
	} {
		if rec := socialTestRequestAs(t, "fn-9992", http.MethodPost, target, ""); rec.Code != http.StatusOK {
			t.Fatalf("POST %s = %d %s", target, rec.Code, rec.Body.String())
		}
	}
}
