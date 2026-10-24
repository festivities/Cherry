package lpn

import (
	"bytes"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// heartTestSession is the client's TLUP D session in these tests.
var heartTestSession = [8]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

// recordConn keeps what a lockedConn writes; deadlines are no-ops.
type recordConn struct {
	net.Conn
	out bytes.Buffer
}

func (c *recordConn) Write(b []byte) (int, error)      { return c.out.Write(b) }
func (c *recordConn) SetWriteDeadline(time.Time) error { return nil }

// TestHeartButtonFrames pins the 43-byte show frame and the hide frame (Id 102) from heart-button.md,
// and the one credential-free log line per push.
func TestHeartButtonFrames(t *testing.T) {
	show := mustHex(t, `00 00 00 27 44 00 11 22 33 44 55 66 77 88 05 00 00 00 00 00 00 05
		0a 13 0a 11 10 65 1a 0d 0a 04 69 74 65 6d 22 05 48 45 41 52 54`)
	hide := mustHex(t, `00 00 00 27 44 00 11 22 33 44 55 66 77 88 05 00 00 00 00 00 00 05
		0a 13 0a 11 10 66 1a 0d 0a 04 69 74 65 6d 22 05 48 45 41 52 54`)
	if len(show) != 43 {
		t.Fatalf("show golden is %d bytes, want 43", len(show))
	}
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	for _, c := range []struct {
		id   uint64
		verb string
		want []byte
	}{{heartButtonShowID, "show", show}, {heartButtonHideID, "hide", hide}} {
		rc := &recordConn{}
		if err := heartButtonPush(&lockedConn{Conn: rc}, heartTestSession, c.id, 9721, 9722, logger); err != nil {
			t.Fatalf("%s push: %v", c.verb, err)
		}
		if !bytes.Equal(rc.out.Bytes(), c.want) {
			t.Fatalf("%s frame = %x, want %x", c.verb, rc.out.Bytes(), c.want)
		}
		if line := "cherry: heart button " + c.verb + " aid=9721 owner=9722\n"; !strings.HasSuffix(logs.String(), line) {
			t.Fatalf("log = %q, want suffix %q", logs.String(), line)
		}
	}
}

// TestHeartButtonShowConditions: the show push needs hearts left and a Garden that is not the
// viewer's own. A capped viewer and an own Garden push nothing.
func TestHeartButtonShowConditions(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"hbv": {AccessToken: "hbv", SessionKey: "sk", Aid: "9721", Name: "V"},
		"hbo": {AccessToken: "hbo", SessionKey: "sk", Aid: "9722", Name: "O"},
		"hbt": {AccessToken: "hbt", SessionKey: "sk", Aid: "9723", Name: "T"},
	})
	for _, aid := range []string{"9721", "9722", "9723"} {
		testutil.SetLab(t, aid, false)
	}
	now := time.Date(2026, time.October, 22, 12, 0, 0, 0, time.UTC)
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	show := func(viewer, owner uint64) []byte {
		rc := &recordConn{}
		if err := heartButtonShow(&lockedConn{Conn: rc}, heartTestSession, viewer, owner, now, logger); err != nil {
			t.Fatalf("show %d/%d: %v", viewer, owner, err)
		}
		return rc.out.Bytes()
	}
	wantShow := tlupDFrame(heartTestSession, tlupQuestEventMsg, tlupQuestActionBody(heartButtonShowID))

	if got := show(9721, 9721); len(got) != 0 {
		t.Fatalf("own Garden pushed %x", got)
	}
	if got := show(9721, 9722); !bytes.Equal(got, wantShow) {
		t.Fatalf("friend Garden push = %x, want %x", got, wantShow)
	}
	for range economy.HeartBase() {
		economy.HeartSend("9721", "9723", now)
	}
	if got := show(9721, 9722); len(got) != 0 {
		t.Fatalf("capped viewer pushed %x", got)
	}
	if n := strings.Count(logs.String(), "cherry: heart button show aid=9721 owner=9722\n"); n != 1 {
		t.Fatalf("show lines = %d, want 1 in %q", n, logs.String())
	}
}

// heartGateway opens one gateway connection on a pipe and reads the banner. stop closes it and
// waits for the observer to finish (its defers clear the Garden registry).
func heartGateway(t *testing.T) (client net.Conn, stop func()) {
	t.Helper()
	server, client := net.Pipe()
	cleared := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		observeConn(deadlineRecordingConn{server, cleared}, GatewayAddr, log.New(&bytes.Buffer{}, "", 0))
		close(done)
	}()
	if banner := readFullDeadline(t, client, len(sessionBanner)); !bytes.Equal(banner, sessionBanner) {
		t.Fatalf("gateway banner = %x", banner)
	}
	return client, func() {
		client.Close()
		<-done
	}
}

// heartEnterGarden sends the Garden login, room-enter (owner) and relay-start for viewer on tunnel,
// reading each reply and the visit-quest notice that the room-enter queues (push 50, then sync 28,
// both before any heart push). Relay-start sends no show: the button belongs to a My Room visit.
func heartEnterGarden(t *testing.T, client net.Conn, tunnel [8]byte, viewer, owner uint64) {
	t.Helper()
	login := pbVar(pbLen(nil, 1, []byte("sk")), 2, viewer)
	write := func(f []byte) {
		if _, err := client.Write(f); err != nil {
			t.Fatalf("garden write: %v", err)
		}
	}
	write(squareFrame(0x48, tunnel[:], 0, login))
	readFullDeadline(t, client, 20) // hc_login_res
	write(squareFrame(0x48, tunnel[:], 1, pbVar(nil, 1, owner)))
	readSessionFrame(t, client) // hc_room_enter_res
	write(squareFrame(0x48, tunnel[:], 3, nil))
	readSessionFrame(t, client) // relay-start reply
	readSessionFrame(t, client) // hc_quest_complete_push 50 (visit quest)
	readSessionFrame(t, client) // hc_quest_call_npc_res 28
}

// expectTLUPReply sends a session-only TLUP request and checks the next bytes are its reply. Any
// push that the server wrote before it would show up here instead.
func expectTLUPReply(t *testing.T, client net.Conn) {
	t.Helper()
	req := tlupDFrame(heartTestSession, 0, nil)
	if _, err := client.Write(req); err != nil {
		t.Fatalf("tlup write: %v", err)
	}
	if got := readFullDeadline(t, client, len(req)); !bytes.Equal(got, req) {
		t.Fatalf("next bytes = %x, want the TLUP reply %x (no push expected)", got, req)
	}
}

// expectNoPush sends a gateway ping and checks its echo is the next bytes. A push that the server
// wrote before the echo would show up first.
func expectNoPush(t *testing.T, client net.Conn) {
	t.Helper()
	ping := []byte{0, 0, 0, 2, 'M', 'P'}
	if _, err := client.Write(ping); err != nil {
		t.Fatalf("ping write: %v", err)
	}
	if got := readFullDeadline(t, client, len(ping)); !bytes.Equal(got, ping) {
		t.Fatalf("next bytes = %x, want the ping echo %x (no push expected)", got, ping)
	}
}

// rpLogin logs aid in on the gateway connection the way the client does at boot: a Room Party
// (agent 4) login, answered before the test goes on. The TLUP quest-list request carries no aid, so
// this login names the connection's viewer.
func rpLogin(t *testing.T, client net.Conn, aid uint64) {
	t.Helper()
	sid := [8]byte{0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68}
	if _, err := client.Write(squareFrame(0x18, sid[:], 0, loginBody(aid))); err != nil {
		t.Fatalf("room party login write: %v", err)
	}
	readSessionFrame(t, client) // login reply
}

// roomLoad serves a My Room request (room/enter/<owner> or room/myroom) as the account behind token.
func roomLoad(t *testing.T, token, path string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/room/", HandleRoom)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: token})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
}

// TestRoomHeartButtonFlow runs the gateway path for the stock heart button. A My Room visit over
// room/enter shows it on the client's next quest-list request, and not before. A counted heart to
// another player leaves it up; a counted heart to the visited owner hides it. Own rooms, capped
// viewers and Garden loads get no show.
func TestRoomHeartButtonFlow(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"hv-1": {AccessToken: "hv-1", SessionKey: "sk", Aid: "9741", Name: "V"},
		"hv-2": {AccessToken: "hv-2", SessionKey: "sk", Aid: "9742", Name: "O"},
		"hv-3": {AccessToken: "hv-3", SessionKey: "sk", Aid: "9743", Name: "T"},
		"hv-4": {AccessToken: "hv-4", SessionKey: "sk", Aid: "9744", Name: "R"},
		"hv-5": {AccessToken: "hv-5", SessionKey: "sk", Aid: "9745", Name: "C"},
		"hv-6": {AccessToken: "hv-6", SessionKey: "sk", Aid: "9746", Name: "G"},
	})
	for _, aid := range []string{"9741", "9742", "9743", "9744", "9745", "9746"} {
		testutil.SetLab(t, aid, false)
	}
	now := time.Date(2026, time.October, 22, 12, 0, 0, 0, time.UTC)
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })
	gardenReset(t)
	tunnel := [8]byte{0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58}
	wantShow := tlupDFrame(heartTestSession, tlupQuestEventMsg, tlupQuestActionBody(heartButtonShowID))
	wantHide := tlupDFrame(heartTestSession, tlupQuestEventMsg, tlupQuestActionBody(heartButtonHideID))
	// The client's TLUP quest-list request (agent 5, msgid 0); its reply is the same bytes.
	sessionReq := tlupDFrame(heartTestSession, 0, nil)

	t.Run("visit shows after the quest-list request, a heart to another player keeps it, to the owner hides it", func(t *testing.T) {
		client, stop := heartGateway(t)
		defer stop()
		rpLogin(t, client, 9741)
		roomLoad(t, "hv-1", "/v4/room/enter/9742/LEVEL_1")
		expectNoPush(t, client) // the room load alone pushes nothing
		if _, err := client.Write(sessionReq); err != nil {
			t.Fatalf("session write: %v", err)
		}
		if got := readFullDeadline(t, client, len(sessionReq)); !bytes.Equal(got, sessionReq) {
			t.Fatalf("request reply = %x, want %x", got, sessionReq)
		}
		if got := readFullDeadline(t, client, len(wantShow)); !bytes.Equal(got, wantShow) {
			t.Fatalf("show push = %x, want %x", got, wantShow)
		}
		expectTLUPReply(t, client) // the show is sent once per visit
		other := tlupDFrame(heartTestSession, 1, tlupClientEvent(9741, 0x2a, heartEvent(9743)))
		if _, err := client.Write(other); err != nil {
			t.Fatalf("heart write: %v", err)
		}
		if got := readFullDeadline(t, client, 24); !bytes.Equal(got, tlupDFrame(heartTestSession, 1, []byte{0x08, 0x2a})) {
			t.Fatalf("ack to other = %x", got)
		}
		expectNoPush(t, client) // a heart to someone else does not hide the button
		host := tlupDFrame(heartTestSession, 1, tlupClientEvent(9741, 0x2b, heartEvent(9742)))
		if _, err := client.Write(host); err != nil {
			t.Fatalf("heart write: %v", err)
		}
		if got := readFullDeadline(t, client, 24); !bytes.Equal(got, tlupDFrame(heartTestSession, 1, []byte{0x08, 0x2b})) {
			t.Fatalf("ack to owner = %x", got)
		}
		if got := readFullDeadline(t, client, len(wantHide)); !bytes.Equal(got, wantHide) {
			t.Fatalf("hide push = %x, want %x", got, wantHide)
		}
		expectTLUPReply(t, client) // the visit ended with the hide
		if got := economy.HeartsReceived("9742"); got != 1 {
			t.Fatalf("owner received = %d, want 1", got)
		}
	})

	t.Run("own room pushes nothing", func(t *testing.T) {
		client, stop := heartGateway(t)
		defer stop()
		rpLogin(t, client, 9744)
		roomLoad(t, "hv-4", "/v4/room/enter/9742/LEVEL_1")
		roomLoad(t, "hv-4", "/v4/room/myroom/LEVEL_1")
		expectTLUPReply(t, client)
	})

	t.Run("capped viewer pushes nothing", func(t *testing.T) {
		for range economy.HeartBase() {
			economy.HeartSend("9745", "9743", now)
		}
		client, stop := heartGateway(t)
		defer stop()
		rpLogin(t, client, 9745)
		roomLoad(t, "hv-5", "/v4/room/enter/9742/LEVEL_1")
		expectTLUPReply(t, client)
	})

	t.Run("garden load drops a pending show", func(t *testing.T) {
		client, stop := heartGateway(t)
		defer stop()
		roomLoad(t, "hv-6", "/v4/room/enter/9742/LEVEL_1")
		heartEnterGarden(t, client, tunnel, 9746, 9742) // the Garden login names the connection's viewer
		expectTLUPReply(t, client)
	})

	t.Run("garden relay-start pushes nothing", func(t *testing.T) {
		client, stop := heartGateway(t)
		defer stop()
		heartEnterGarden(t, client, tunnel, 9743, 9742)
		expectTLUPReply(t, client)
	})
}
