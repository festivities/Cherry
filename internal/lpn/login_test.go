package lpn

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// testSessionKey is the session key of the fixture accounts that older tests install with withLoginAccounts.
const testSessionKey = "sk"

// testTunnel is the tunnel (session id) of the gateway frames these tests send.
var testTunnel = [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

// withLoginAccounts returns entries plus one account, holding testSessionKey, for each aid entries does not already hold.
// A test whose login presents testSessionKey needs that account installed.
func withLoginAccounts(entries map[string]*store.Account, aids ...uint64) map[string]*store.Account {
	if entries == nil {
		entries = map[string]*store.Account{}
	}
	held := map[string]bool{}
	for _, acc := range entries {
		held[acc.Aid] = true
	}
	for _, aid := range aids {
		s := strconv.FormatUint(aid, 10)
		if held[s] {
			continue
		}
		entries["lt-"+s] = &store.Account{AccessToken: "lt-" + s, SessionKey: testSessionKey, Aid: s, Name: "Login"}
	}
	return entries
}

// Two fixture accounts with their own keys, for the refusal cases.
const (
	loginAidA = 9101
	loginAidB = 9102
)

func installLoginPair(t *testing.T) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{
		"la-9101": {AccessToken: "la-9101", SessionKey: "sk-9101", Aid: "9101", Name: "A"},
		"la-9102": {AccessToken: "la-9102", SessionKey: "sk-9102", Aid: "9102", Name: "B"},
	})
}

// loginRequest is a gateway login body: sessionKey #1, aid #2.
func loginRequest(key string, aid uint64) []byte { return pbVar(pbLen(nil, 1, []byte(key)), 2, aid) }

// loginFor is the login of aid presenting the session key that aid's installed account holds. With no account for aid
// the key is empty and the observer refuses the login, as it does in the game.
func loginFor(aid uint64) []byte {
	key := ""
	store.AccountsMu.Lock()
	if acc := store.AccountByAidLocked(strconv.FormatUint(aid, 10)); acc != nil {
		key = acc.SessionKey
	}
	store.AccountsMu.Unlock()
	return loginRequest(key, aid)
}

// gwFrame is one length-prefixed data frame of agent (subtype 0) on testTunnel.
func gwFrame(agent int, msgid uint16, body []byte) []byte {
	return squareFrame(byte(agent-1)<<3, testTunnel[:], msgid, body)
}

// gatewayConn runs the real gateway observer on one TCP connection and returns the client end, the observer's log and a
// channel closed when the observer has returned. The client closes the connection to end it.
func gatewayConn(t *testing.T) (net.Conn, *bytes.Buffer, chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	server := <-accepted
	logs := &bytes.Buffer{}
	done := make(chan struct{})
	go func() {
		observeGatewayFrames(&lockedConn{Conn: server}, GatewayAddr, "test", log.New(logs, "", 0))
		_ = server.Close()
		close(done)
	}()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	return client, logs, done
}

// readLengthPrefixed reads one frame (4-byte length, then payload) and returns the payload.
func readLengthPrefixed(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	prefix := readFullDeadline(t, conn, 4)
	return readFullDeadline(t, conn, int(binary.BigEndian.Uint32(prefix)))
}

// gatewayLog sends the frames (length prefixes included) and a ping, reads every reply up to the ping's echo, then ends
// the connection and returns the observer's log.
func gatewayLog(t *testing.T, frames ...[]byte) string {
	t.Helper()
	client, logs, done := gatewayConn(t)
	for _, f := range append(frames, []byte{0, 0, 0, 2, 'M', 'Q'}) {
		if _, err := client.Write(f); err != nil {
			t.Fatalf("gateway write: %v", err)
		}
	}
	for {
		if payload := readLengthPrefixed(t, client); bytes.Equal(payload, []byte("MQ")) {
			break
		}
	}
	client.Close()
	<-done
	return logs.String()
}

// TestGatewayLoginMatrix runs each agent's login through the gateway observer: the right key and aid are accepted; a
// wrong key, another account's key, an unknown aid and an empty key are refused; a verified login as another account
// replaces the first; and a refused login after an accepted one leaves the connection logged in. A probe message that
// needs a login is answered only while the connection is logged in.
func TestGatewayLoginMatrix(t *testing.T) {
	installLoginPair(t)
	agents := []struct {
		name     string
		agent    int
		accepted string // the start of a log line of an accepted login
		refused  string // the start of a log line of a refused login
		probe    uint16 // a message that needs a login (not used when probeLog is empty)
		probeLog string // the observer's line for that message when no login has been accepted
	}{
		{"garden", 10, "garden-login remote=test result=197121", "garden-login remote=test refused", 0, ""},
		{"square", 7, "SQUARE login aid=", "SQUARE login refused", 1, "SQUARE msgid=1 before login ignored"},
		{"room party", 4, "ROOMPARTY login aid=", "ROOMPARTY login refused", 6, "ROOMPARTY msgid=6 before login ignored"},
		{"circle", 8, "CIRCLE login aid=", "CIRCLE login refused", 16, "CIRCLE msgid=16 before login ignored"},
		{"treasure", 9, "TREASURE msgid=0 login aid=", "TREASURE msgid=0 login refused", 1, "TREASURE msgid=1 enter refused: no login"},
	}
	cases := []struct {
		name     string
		logins   [][]byte
		keys     []string // every key the logins present: none may reach the log
		accepted int      // accepted lines expected
		refused  int      // refused lines expected
		loggedIn bool     // whether the probe must be answered
	}{
		{"right key and aid", [][]byte{loginRequest("sk-9101", loginAidA)}, []string{"sk-9101"}, 1, 0, true},
		{"wrong key", [][]byte{loginRequest("sk-9999", loginAidA)}, []string{"sk-9999"}, 0, 1, false},
		{"another account's key", [][]byte{loginRequest("sk-9102", loginAidA)}, []string{"sk-9102"}, 0, 1, false},
		{"unknown aid", [][]byte{loginRequest("sk-9199", 9199)}, []string{"sk-9199"}, 0, 1, false},
		{"empty key", [][]byte{loginRequest("", loginAidA)}, nil, 0, 1, false},
		{"second verified account replaces the first", [][]byte{loginRequest("sk-9101", loginAidA), loginRequest("sk-9102", loginAidB)}, []string{"sk-9101", "sk-9102"}, 2, 0, true},
		{"refused login after an accepted one", [][]byte{loginRequest("sk-9101", loginAidA), loginRequest("sk-9999", loginAidB)}, []string{"sk-9101", "sk-9999"}, 1, 1, true},
		{"another account's key after an accepted login", [][]byte{loginRequest("sk-9101", loginAidA), loginRequest("sk-9102", loginAidA)}, []string{"sk-9101", "sk-9102"}, 1, 1, true},
	}
	for _, a := range agents {
		for _, c := range cases {
			t.Run(a.name+"/"+c.name, func(t *testing.T) {
				var frames [][]byte
				for _, l := range c.logins {
					frames = append(frames, gwFrame(a.agent, 0, l))
				}
				if a.probe != 0 {
					frames = append(frames, gwFrame(a.agent, a.probe, nil))
				}
				logs := gatewayLog(t, frames...)
				if n := strings.Count(logs, a.accepted); n != c.accepted {
					t.Errorf("accepted lines = %d, want %d; log:\n%s", n, c.accepted, logs)
				}
				if n := strings.Count(logs, a.refused); n != c.refused {
					t.Errorf("refused lines = %d, want %d; log:\n%s", n, c.refused, logs)
				}
				for _, key := range c.keys {
					if strings.Contains(logs, key) {
						t.Errorf("log names a session key: %q", key)
					}
				}
				if a.probe == 0 {
					return
				}
				unanswered := strings.Contains(logs, a.probeLog)
				if c.loggedIn && unanswered {
					t.Errorf("probe ignored after an accepted login: %q", a.probeLog)
				}
				if !c.loggedIn && !unanswered {
					t.Errorf("probe answered without a login (no %q)", a.probeLog)
				}
			})
		}
	}
}

// TestTLUPHeartSenderIsVerified: a heart is counted only for the account its ClientSession key verifies, and only when
// the connection has no login or is logged in as that same account.
func TestTLUPHeartSenderIsVerified(t *testing.T) {
	installLoginPair(t)
	heart := heartEvent(loginAidB)
	forged := tlupDFrame(heartTestSession, 1, tlupClientEventKey("sk-9101", loginAidB, 1, heart)) // A's key, B's aid
	own := tlupDFrame(heartTestSession, 1, tlupClientEventKey("sk-9101", loginAidA, 2, heart))
	other := tlupDFrame(heartTestSession, 1, tlupClientEventKey("sk-9102", loginAidB, 3, heart))

	if logs := gatewayLog(t, forged); strings.Contains(logs, "tlup event aid=9102") || !strings.Contains(logs, "invalid or unverified") {
		t.Errorf("heart with another account's key was counted or not refused:\n%s", logs)
	}
	if logs := gatewayLog(t, gwFrame(10, 0, loginRequest("sk-9101", loginAidA)), own, other); !strings.Contains(logs, "tlup event aid=9101") ||
		!strings.Contains(logs, "tlup event refused aid=9102: the connection is logged in as aid=9101") {
		t.Errorf("logged-in connection: heart from its own account not counted, or another account's not refused:\n%s", logs)
	}
	if logs := gatewayLog(t, own); !strings.Contains(logs, "tlup event aid=9101 service=102 event=12") {
		t.Errorf("own verified heart without a login not counted:\n%s", logs)
	}
}

// TestRecordTLUPEventsBound: the sender of a verified request is the bound account, and nothing else counts.
func TestRecordTLUPEventsBound(t *testing.T) {
	installLoginPair(t)
	logger := log.New(io.Discard, "", 0)
	if sender, _ := recordTLUPEvents(logger, tlupClientEventKey("sk-9101", loginAidA, 1, heartEvent(loginAidB)), loginAidA); sender != loginAidA {
		t.Errorf("own heart on its own connection: sender %d, want %d", sender, loginAidA)
	}
	if sender, _ := recordTLUPEvents(logger, tlupClientEventKey("sk-9101", loginAidA, 1, heartEvent(loginAidB)), loginAidB); sender != 0 {
		t.Errorf("heart on a connection logged in as another account: sender %d, want 0", sender)
	}
	if sender, _ := recordTLUPEvents(logger, tlupClientEventKey("sk-9101", loginAidB, 1, heartEvent(loginAidA)), 0); sender != 0 {
		t.Errorf("heart with another account's aid: sender %d, want 0", sender)
	}
}

// TestGatewayPreLoginFrameCap: before any login a frame over maxGatewayPreLoginFrameLen closes the connection; after a
// login a frame of 100 KiB is read and the connection keeps working.
func TestGatewayPreLoginFrameCap(t *testing.T) {
	installLoginPair(t)
	client, logs, done := gatewayConn(t)
	if _, err := client.Write(binary.BigEndian.AppendUint32(nil, maxGatewayPreLoginFrameLen+1)); err != nil {
		t.Fatal(err)
	}
	<-done
	if !strings.Contains(logs.String(), "invalid len=65537 limit=65536") {
		t.Errorf("pre-login oversize frame not refused:\n%s", logs.String())
	}

	big := append([]byte{'X'}, make([]byte, 100<<10-1)...) // an unknown tag: read, logged, ignored
	logs2 := gatewayLog(t, gwFrame(10, 0, loginRequest("sk-9101", loginAidA)), append(binary.BigEndian.AppendUint32(nil, uint32(len(big))), big...))
	if !strings.Contains(logs2, "garden-login remote=test result=197121") || strings.Contains(logs2, "invalid len") {
		t.Errorf("logged-in connection refused a 100 KiB frame:\n%s", logs2)
	}
}

// TestSessionFrameCaps: before a binding a frame over maxSessionPreAuthFrameLen closes the connection; after binding a
// 100 KiB frame is read and the connection keeps working, and a frame over maxSessionFrameLen closes it.
func TestSessionFrameCaps(t *testing.T) {
	installLoginPair(t)
	conn, cleanup := startSessionServer(t)
	defer cleanup()
	readFullDeadline(t, conn, len(sessionBanner))
	if _, err := conn.Write(sessionHello); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(binary.BigEndian.AppendUint32(nil, maxSessionPreAuthFrameLen+1)); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, conn)

	conn, cleanup = startSessionServer(t)
	defer cleanup()
	readFullDeadline(t, conn, len(sessionBanner))
	if _, err := conn.Write(sessionHello); err != nil {
		t.Fatal(err)
	}
	sendInit(t, conn, "sk-9101")
	writeSessionFrame(t, conn, make([]byte, 100<<10)) // read and ignored: not an LO frame
	if _, err := conn.Write([]byte{0, 0, 0, 2, 'M', 'Q'}); err != nil {
		t.Fatal(err)
	}
	if got := readFullDeadline(t, conn, 6); string(got[4:]) != "MQ" {
		t.Fatalf("ping after a 100 KiB frame = %x", got)
	}
	if _, err := conn.Write(binary.BigEndian.AppendUint32(nil, maxSessionFrameLen+1)); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, conn)
}

// expectClosed fails unless the server closes conn without a reply.
func expectClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("read after oversize frame = %v, want EOF", err)
	}
}

// listenSession serves the session socket on a loopback listener and returns its address.
func listenSession(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ServeSession(ln, discardLogger())
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

// dialSession connects to the session socket and completes the banner and hello.
func dialSession(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	readFullDeadline(t, conn, len(sessionBanner))
	if _, err := conn.Write(sessionHello); err != nil {
		t.Fatal(err)
	}
	return conn
}

// sendInit sends a sendInit naming key and reads its configure reply.
func sendInit(t *testing.T, conn net.Conn, key string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"sessionId": "1", "sessionKey": key})
	if err != nil {
		t.Fatal(err)
	}
	writeSessionFrame(t, conn, buildLOFrame(loOpSendInit, 0x42, body)[4:])
	if reply := readSessionFrame(t, conn); !bytes.Equal(reply, buildLOFrame(loOpConfigure, 0, configurePayload)) {
		t.Fatalf("sendInit reply = %x", reply)
	}
}

// TestSessionRebindRules: a connection holds one account, so a sendInit naming another replaces the one it held; an
// account bound by a second connection moves there (newest wins), and closing the old connection leaves it bound.
func TestSessionRebindRules(t *testing.T) {
	installLoginPair(t)
	addr := listenSession(t)
	c1 := dialSession(t, addr)
	c2 := dialSession(t, addr)

	sendInit(t, c1, "sk-9101")
	sendInit(t, c1, "sk-9102") // c1 now holds B only
	if SessionOnline(loginAidA) || !SessionOnline(loginAidB) {
		t.Fatal("the connection's old account stayed bound after its sendInit for another account")
	}

	sendInit(t, c2, "sk-9102") // newest wins: B's pushes now go to c2
	if sent, err := sessionPush(loginAidB, loOpPush, []byte("hello")); err != nil || !sent {
		t.Fatalf("push to B: sent %v err %v", sent, err)
	}
	if got := readSessionFrame(t, c2); !bytes.Equal(got, buildLOFrame(loOpPush, 0, []byte("hello"))) {
		t.Fatalf("push went elsewhere: c2 got %x", got)
	}
	_ = c1.Close()
	time.Sleep(50 * time.Millisecond) // let the old connection's close run its unbind
	if !SessionOnline(loginAidB) {
		t.Fatal("closing the replaced connection unbound the account from the new one")
	}
	_ = c2.Close()
	deadline := time.Now().Add(2 * time.Second)
	for SessionOnline(loginAidB) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if SessionOnline(loginAidB) {
		t.Fatal("account still bound after its connection closed")
	}
}

// TestRemoteConnectionCap: one host may hold maxConnsPerRemote connections; the next is refused until one is released.
// Other hosts are not affected.
func TestRemoteConnectionCap(t *testing.T) {
	const host = "203.0.113.9" // documentation address (TEST-NET-3)
	t.Cleanup(func() {
		for i := 0; i < maxConnsPerRemote; i++ {
			releaseRemote(host + ":1")
		}
		releaseRemote("203.0.114.9:1")
	})
	for i := 0; i < maxConnsPerRemote; i++ {
		if !admitRemote(host + ":" + strconv.Itoa(40000+i)) {
			t.Fatalf("connection %d from one host refused", i+1)
		}
	}
	if admitRemote(host + ":50000") {
		t.Fatal("connection over the per-host cap admitted")
	}
	if !admitRemote("203.0.114.9:1") {
		t.Fatal("another host refused at the first connection")
	}
	releaseRemote(host + ":40000")
	if !admitRemote(host + ":50001") {
		t.Fatal("connection refused after one was released")
	}
}
