package web

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"testing"
	"time"

	"cherry/internal/config"
	"cherry/internal/lpn"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

func bindLPNSession(t *testing.T, addr, sessionKey string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial session: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	banner := make([]byte, 15)
	if _, err := io.ReadFull(conn, banner); err != nil {
		_ = conn.Close()
		t.Fatalf("read banner: %v", err)
	}
	hello := []byte{0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x01, 0x0A, 0x35, 0xED, 0x6A}
	if _, err := conn.Write(hello); err != nil {
		_ = conn.Close()
		t.Fatalf("write hello: %v", err)
	}
	body, _ := json.Marshal(struct {
		SessionKey string `json:"sessionKey"`
	}{SessionKey: sessionKey})
	loHeader := make([]byte, 16)
	copy(loHeader[0:2], "LO")
	loHeader[2] = 0x10
	loHeader[3] = 0x00
	binary.BigEndian.PutUint32(loHeader[4:8], uint32(len(body)))
	binary.BigEndian.PutUint64(loHeader[8:16], 1)
	payload := append(loHeader, body...)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	frame = append(frame, payload...)
	if _, err := conn.Write(frame); err != nil {
		_ = conn.Close()
		t.Fatalf("write sendInit: %v", err)
	}
	prefix := make([]byte, 4)
	if _, err := io.ReadFull(conn, prefix); err != nil {
		_ = conn.Close()
		t.Fatalf("read configure prefix: %v", err)
	}
	reply := make([]byte, binary.BigEndian.Uint32(prefix))
	if _, err := io.ReadFull(conn, reply); err != nil {
		_ = conn.Close()
		t.Fatalf("read configure body: %v", err)
	}
	return conn
}

func TestCheckSessionMaxPlayersRefusal(t *testing.T) {
	testutil.UseDatabase(t)
	installSocialTestAccounts(t, map[string]*store.Account{
		"tok1": {AccessToken: "tok1", SessionKey: "skey1", Aid: "1001", Name: "One"},
		"tok2": {AccessToken: "tok2", SessionKey: "skey2", Aid: "1002", Name: "Two"},
	})
	config.Activate(config.Config{MaxPlayers: 1})
	t.Cleanup(func() { config.Activate(config.Config{}) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go lpn.ServeSession(ln, log.New(io.Discard, "", 0))
	t.Cleanup(func() { _ = ln.Close() })

	conn := bindLPNSession(t, ln.Addr().String(), "skey1")
	t.Cleanup(func() {
		_ = conn.Close()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && lpn.SessionOnline(1001) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	// Offline player 1002 is refused with 503 (never 404), keeping its token usable.
	refused := checkSession(t, "tok2")
	if refused.Code != http.StatusServiceUnavailable {
		t.Fatalf("checkSession when full = %d %s, want 503", refused.Code, refused.Body.String())
	}
	wantBody := `{"errorCode":"503","errorMessage":"cherry: server full"}`
	if !bytes.Equal(refused.Body.Bytes(), []byte(wantBody)) {
		t.Fatalf("body = %q, want %q", refused.Body.String(), wantBody)
	}

	// Already-online player 1001 is still admitted at cap.
	online := checkSession(t, "tok1")
	if online.Code != http.StatusOK {
		t.Fatalf("already-online checkSession = %d %s, want 200", online.Code, online.Body.String())
	}

	// Live DB override to 0 (unlimited) allows 1002's saved token to succeed.
	if err := store.SetMaxPlayers(0, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	admitted := checkSession(t, "tok2")
	if admitted.Code != http.StatusOK {
		t.Fatalf("checkSession after unlimited override = %d %s, want 200", admitted.Code, admitted.Body.String())
	}
}
