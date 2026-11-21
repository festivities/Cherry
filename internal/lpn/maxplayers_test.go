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

func TestSessionMaxPlayers(t *testing.T) {
	testutil.UseDatabase(t)
	testutil.InstallAccounts(t, map[string]*store.Account{
		"tok-101": {AccessToken: "tok-101", SessionKey: "key-101", Aid: "101", Name: "One"},
		"tok-102": {AccessToken: "tok-102", SessionKey: "key-102", Aid: "102", Name: "Two"},
		"tok-103": {AccessToken: "tok-103", SessionKey: "key-103", Aid: "103", Name: "Three"},
		"tok-104": {AccessToken: "tok-104", SessionKey: "key-104", Aid: "104", Name: "Four"},
	})
	config.Activate(config.Config{MaxPlayers: 2})
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

	c2, ok := dialSessionAndSendInit(t, ln.Addr().String(), "key-102")
	if !ok {
		t.Fatal("player 102 refused")
	}
	t.Cleanup(func() { _ = c2.Close(); waitOffline(t, 102) })

	if !ServerFullFor(103) {
		t.Fatal("ServerFullFor(103) = false, want true at cap 2")
	}
	if ServerFullFor(101) {
		t.Fatal("ServerFullFor(101) = true for already-online player, want false")
	}

	// Third player is refused and disconnected without binding.
	if _, ok := dialSessionAndSendInit(t, ln.Addr().String(), "key-103"); ok {
		t.Fatal("player 103 was admitted over cap 2")
	}
	if SessionOnline(103) {
		t.Fatal("refused player 103 is marked online")
	}
	if !strings.Contains(logs.String(), "aid=103 reason=max-players") {
		t.Fatalf("logs missing max-players refusal: %s", logs.String())
	}

	// Already-online player can replace its own session even while full, and the other online player stays bound.
	c1New, ok := dialSessionAndSendInit(t, ln.Addr().String(), "key-101")
	if !ok {
		t.Fatal("already-online player 101 reconnect refused while full")
	}
	t.Cleanup(func() { _ = c1New.Close() })
	_ = c1.Close()
	if !SessionOnline(101) || !SessionOnline(102) {
		t.Fatalf("online status after reconnect: 101=%t 102=%t", SessionOnline(101), SessionOnline(102))
	}

	// Live DB override to 0 (unlimited) allows 103 in.
	if err := store.SetMaxPlayers(0, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if ServerFullFor(103) {
		t.Fatal("ServerFullFor(103) = true with unlimited override")
	}
	c3, ok := dialSessionAndSendInit(t, ln.Addr().String(), "key-103")
	if !ok {
		t.Fatal("player 103 refused with unlimited override")
	}
	t.Cleanup(func() { _ = c3.Close(); waitOffline(t, 103) })

	// Lowering cap live to 1 never kicks already-online 101/102/103, and still refuses new player 104.
	if err := store.SetMaxPlayers(1, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if !SessionOnline(101) || !SessionOnline(102) || !SessionOnline(103) {
		t.Fatal("lowering cap disconnected already-online players")
	}
	if _, ok := dialSessionAndSendInit(t, ln.Addr().String(), "key-104"); ok {
		t.Fatal("player 104 admitted over lowered cap")
	}
}
