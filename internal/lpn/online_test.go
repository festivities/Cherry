package lpn

import (
	"slices"
	"strings"
	"testing"
)

// onlineOf returns the rows of Online for aid.
func onlineOf(aid uint64) []Connection {
	var rows []Connection
	for _, c := range Online() {
		if c.Aid == aid {
			rows = append(rows, c)
		}
	}
	return rows
}

// TestOnlineListsOnlyLoggedInConnections: a session and a gateway connection that hold a login are listed with their listener,
// their masked network and the room the account is in; a connection that has not logged in is not listed.
func TestOnlineListsOnlyLoggedInConnections(t *testing.T) {
	installLoginPair(t)
	addr := listenSession(t)
	sessA := dialSession(t, addr)
	sendInit(t, sessA, "sk-9101")
	dialSession(t, addr) // no login: nothing to list
	gwA, _, doneA := gatewayConn(t)
	sendAndPing(t, gwA, gwFrame(10, 0, loginRequest("sk-9101", loginAidA)))

	// A join makes the room and its member; the test does the same under the lock.
	loungeMu.Lock()
	loungeRoomLocked(1960000001)
	loungeIn[loginAidA] = 1960000001
	loungeMu.Unlock()
	t.Cleanup(func() {
		loungeMu.Lock()
		delete(loungeIn, loginAidA)
		delete(loungeRooms, 1960000001)
		loungeMu.Unlock()
	})

	rows := onlineOf(loginAidA)
	if len(rows) != 2 {
		t.Fatalf("%d rows for aid %d, want the session and the gateway connection", len(rows), loginAidA)
	}
	listeners := map[string]bool{}
	for _, r := range rows {
		listeners[r.Listener] = true
		if r.Since.IsZero() {
			t.Errorf("row %s has no connected-since time", r.Listener)
		}
		if len(r.Places) != 1 || r.Places[0] != (Place{Kind: "lounge", ID: "1960000001"}) {
			t.Errorf("row %s places = %v, want the lounge room", r.Listener, r.Places)
		}
		if r.Listener == "session" && r.Remote != "127.0.0.0/24" {
			t.Errorf("session remote %q: the address is not masked to its /24", r.Remote)
		}
	}
	if !listeners["session"] || !listeners["gateway"] {
		t.Errorf("listeners %v, want session and gateway", listeners)
	}
	for _, c := range Online() {
		if c.Aid == 0 {
			t.Errorf("a connection without a login is listed: %+v", c)
		}
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].Since.Before(rows[i-1].Since) {
			t.Error("rows are not oldest first")
		}
	}

	CloseAid("9101")
	waitDone(t, doneA)
	waitOffline(t, loginAidA)
	if n := len(onlineOf(loginAidA)); n != 0 {
		t.Errorf("%d rows left after the account's connections closed", n)
	}
}

// TestOnlinePlacesFromEachRegistry: a Square room and a circle room of one account are each named as a place, and a room the
// account has left is not.
func TestOnlinePlacesFromEachRegistry(t *testing.T) {
	installLoginPair(t)
	addr := listenSession(t)
	sess := dialSession(t, addr)
	sendInit(t, sess, "sk-9102")

	square := defaultSquares.room(2202250100, 1)
	square.mu.Lock()
	square.players = append(square.players, &squarePlayer{aid: loginAidB})
	square.mu.Unlock()
	circleRoom := defaultCircleRooms.room(1940000001)
	circleRoom.mu.Lock()
	circleRoom.players = append(circleRoom.players, &circlePlayer{aid: loginAidB})
	circleRoom.mu.Unlock()
	t.Cleanup(func() {
		square.mu.Lock()
		square.players = nil
		square.mu.Unlock()
		circleRoom.mu.Lock()
		circleRoom.players = nil
		circleRoom.mu.Unlock()
	})

	rows := onlineOf(loginAidB)
	if len(rows) != 1 {
		t.Fatalf("%d rows for aid %d, want 1", len(rows), loginAidB)
	}
	got := rows[0].Places
	if len(got) != 2 || !slices.Contains(got, Place{Kind: "square", ID: "2202250100", Room: "1"}) || !slices.Contains(got, Place{Kind: "circle", ID: "1940000001"}) {
		t.Errorf("places %v, want the Square and the circle", got)
	}

	square.mu.Lock()
	square.players = nil
	square.mu.Unlock()
	if got := onlineOf(loginAidB)[0].Places; len(got) != 1 || got[0].Kind != "circle" {
		t.Errorf("after leaving the Square, places %v, want the circle only", got)
	}
	CloseAid("9102")
	waitOffline(t, loginAidB)
}

// TestRemoteNetwork: an IPv4 address keeps its /24, an IPv6 address its /48, a port is dropped, and a value that is not an address
// gives "" (the audit's rule, httpx.TruncateAddr).
func TestRemoteNetwork(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.77:5000":     "203.0.113.0/24",
		"[2001:db8:1:2:3::9]:1": "2001:db8:1::/48",
		"test":                  "",
	} {
		if got := remoteNetwork(in); got != want {
			t.Errorf("remoteNetwork(%q) = %q, want %q", in, got, want)
		}
	}
	if strings.Contains(remoteNetwork("203.0.113.77:5000"), "77") {
		t.Error("the last octet is in the network")
	}
}
