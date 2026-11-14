package lpn

import (
	"bytes"
	"net"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// playerID is the store row id of the account with avatar id aid.
func playerID(t *testing.T, aid string) int64 {
	t.Helper()
	rows, _ := store.FindPlayers(aid)
	if len(rows) != 1 {
		t.Fatalf("%d rows for aid %s, want 1", len(rows), aid)
	}
	return rows[0].ID
}

// sendAndPing writes frames to a gateway connection and a ping, then reads the replies up to the ping's echo: every frame
// before the ping has been handled when it returns.
func sendAndPing(t *testing.T, conn net.Conn, frames ...[]byte) {
	t.Helper()
	for _, f := range append(frames, []byte{0, 0, 0, 2, 'M', 'Q'}) {
		if _, err := conn.Write(f); err != nil {
			t.Fatalf("gateway write: %v", err)
		}
	}
	for {
		if bytes.Equal(readLengthPrefixed(t, conn), []byte("MQ")) {
			return
		}
	}
}

// sessionPing sends a ping on a session connection and reads its echo.
func sessionPing(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte{0, 0, 0, 2, 'M', 'Q'}); err != nil {
		t.Fatal(err)
	}
	if got := readFullDeadline(t, conn, 6); string(got[4:]) != "MQ" {
		t.Fatalf("ping reply = %x", got)
	}
}

// waitDone fails unless the gateway observer behind done has returned (its connection was closed and torn down).
func waitDone(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was not closed")
	}
}

// waitOffline waits until aid has no live :10123 session (the closed connection's own teardown unbinds it).
func waitOffline(t *testing.T, aid uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for SessionOnline(aid) {
		if time.Now().After(deadline) {
			t.Fatalf("aid %d still has a live session", aid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// gatewayLoginCount is the number of gateway connections that have a verified login recorded.
func gatewayLoginCount() int {
	gatewayLoginsMu.Lock()
	defer gatewayLoginsMu.Unlock()
	return len(gatewayLogins)
}

// TestBanClosesTheBannedAccountsConnections: a ban (through the store's OnBan hook) ends the banned account's :10123 session
// and its gateway connection, each through its own teardown; another account's connections stay open and answer.
func TestBanClosesTheBannedAccountsConnections(t *testing.T) {
	testutil.UseDatabase(t)
	installLoginPair(t)
	prev := store.OnBan
	store.OnBan = CloseBannedAid
	t.Cleanup(func() { store.OnBan = prev })

	addr := listenSession(t)
	sessA := dialSession(t, addr)
	sendInit(t, sessA, "sk-9101")
	sessB := dialSession(t, addr)
	sendInit(t, sessB, "sk-9102")
	gwA, _, doneA := gatewayConn(t)
	sendAndPing(t, gwA, gwFrame(10, 0, loginRequest("sk-9101", loginAidA)))
	gwB, _, doneB := gatewayConn(t)
	sendAndPing(t, gwB, gwFrame(7, 0, loginRequest("sk-9102", loginAidB)))

	if err := store.BanPlayer(playerID(t, "9101"), "spam", "discord:1"); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, sessA)
	waitOffline(t, loginAidA)
	waitDone(t, doneA)
	expectClosed(t, gwA)

	sessionPing(t, sessB)
	sendAndPing(t, gwB)
	select {
	case <-doneB:
		t.Fatal("another account's gateway connection was closed by the ban")
	default:
	}
	if !SessionOnline(loginAidB) {
		t.Fatal("another account's session was unbound by the ban")
	}

	CloseBannedAid("0") // an account with no avatar has no connections: nothing happens
	CloseBannedAid("not-an-aid")
	sessionPing(t, sessB)
}

// TestCloseBannedAidEndsEachAgentsLogin: a gateway connection that verified a login for the aid is closed, whichever agent
// verified it (Garden, Square, Room Party, Circle, Treasure World); the other account's connection stays, and no connection
// record is left behind when the connections end.
func TestCloseBannedAidEndsEachAgentsLogin(t *testing.T) {
	installLoginPair(t)
	for _, agent := range []struct {
		name string
		id   int
	}{{"garden", 10}, {"square", 7}, {"room party", 4}, {"circle", 8}, {"treasure", 9}} {
		t.Run(agent.name, func(t *testing.T) {
			before := gatewayLoginCount()
			gwA, _, doneA := gatewayConn(t)
			sendAndPing(t, gwA, gwFrame(agent.id, 0, loginRequest("sk-9101", loginAidA)))
			gwB, _, doneB := gatewayConn(t)
			sendAndPing(t, gwB, gwFrame(agent.id, 0, loginRequest("sk-9102", loginAidB)))

			CloseBannedAid("9101")
			waitDone(t, doneA)
			expectClosed(t, gwA)
			sendAndPing(t, gwB)
			select {
			case <-doneB:
				t.Fatal("the other account's connection was closed")
			default:
			}

			_ = gwB.Close()
			waitDone(t, doneB)
			if n := gatewayLoginCount(); n != before {
				t.Fatalf("%d connection records left after both connections ended, want %d", n, before)
			}
		})
	}
}
