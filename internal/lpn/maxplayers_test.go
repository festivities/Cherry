package lpn

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"cherry/internal/config"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func dialSessionAndSendInit(t *testing.T, addr, sessionKey string) (net.Conn, bool) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	readFullDeadline(t, conn, len(sessionBanner))
	if _, err := conn.Write(sessionHello); err != nil {
		_ = conn.Close()
		t.Fatalf("write hello: %v", err)
	}
	body, _ := json.Marshal(struct {
		SessionKey string `json:"sessionKey"`
	}{SessionKey: sessionKey})
	writeSessionFrame(t, conn, buildLOFrame(loOpSendInit, 1, body)[4:])

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	prefix := make([]byte, 4)
	if _, err := io.ReadFull(conn, prefix); err != nil {
		_ = conn.Close()
		return nil, false
	}
	frameLen := int(prefix[0])<<24 | int(prefix[1])<<16 | int(prefix[2])<<8 | int(prefix[3])
	payload := make([]byte, frameLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		_ = conn.Close()
		return nil, false
	}
	op, _, _, ok := parseLOFrame(payload)
	if !ok || op != loOpConfigure {
		_ = conn.Close()
		t.Fatalf("unexpected reply op=%#02x ok=%t", op, ok)
	}
	return conn, true
}

// The cap counts distinct players over both sockets (a gateway-only login counts, a player on both counts once), a full
// server never refuses a sendInit (no player is left on the gateway without a session), and the live override applies.
// Other tests of the package may leave players online, so this checks membership and caps whose answer cannot depend on them.
func TestServerFullCountsBothSocketsAndNeverRefusesSendInit(t *testing.T) {
	testutil.UseDatabase(t)
	testutil.InstallAccounts(t, map[string]*store.Account{
		"tok-101": {AccessToken: "tok-101", SessionKey: "key-101", Aid: "101", Name: "One"},
		"tok-102": {AccessToken: "tok-102", SessionKey: "key-102", Aid: "102", Name: "Two"},
	})
	config.Activate(config.Config{MaxPlayers: 1})
	t.Cleanup(func() { config.Activate(config.Config{}) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	go ServeSession(ln, log.New(&logs, "", 0))
	t.Cleanup(func() { _ = ln.Close() })

	c1, ok := dialSessionAndSendInit(t, ln.Addr().String(), "key-101")
	if !ok {
		t.Fatal("player 101 refused")
	}
	t.Cleanup(func() { _ = c1.Close(); waitOffline(t, 101) })
	if !ServerFull() {
		t.Fatal("ServerFull = false at cap 1 with 101 online")
	}

	// A full server still binds a sendInit.
	c2, ok := dialSessionAndSendInit(t, ln.Addr().String(), "key-102")
	if !ok || !SessionOnline(102) {
		t.Fatal("full server refused the sendInit of 102")
	}
	t.Cleanup(func() { _ = c2.Close(); waitOffline(t, 102) })
	if strings.Contains(logs.String(), "refused") {
		t.Fatalf("session refused a player: %s", logs.String())
	}

	// A gateway-only login counts (onlinePlayers is a set, so 101 on both sockets is one player).
	gw101, gw103 := &lockedConn{}, &lockedConn{}
	t.Cleanup(func() { unbindGatewayLogins(gw101); unbindGatewayLogins(gw103) })
	bindGatewayLogin(gw101, 101)
	bindGatewayLogin(gw103, 103)
	online := onlinePlayers()
	if !online[101] || !online[102] || !online[103] {
		t.Fatalf("online set misses a player: %v", online)
	}

	// The live override wins: 0 = unlimited, 10000 = far from full, 1 = full again.
	for _, c := range []struct {
		cap  int
		full bool
	}{{0, false}, {10000, false}, {1, true}} {
		if err := store.SetMaxPlayers(c.cap, "dev-owner"); err != nil {
			t.Fatal(err)
		}
		if ServerFull() != c.full {
			t.Fatalf("ServerFull at override %d = %t, want %t", c.cap, !c.full, c.full)
		}
	}
}
