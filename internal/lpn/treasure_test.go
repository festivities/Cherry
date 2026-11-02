package lpn

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// Strict shapes of the Treasure World replies this file reads (lpCheck: unknown fields, wrong wire types and missing
// required fields fail). Field numbers are treasure-spec 9.1, transport-money 2.3 and 2.4, cells 6.1 to 6.7.
var (
	treCell   = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}, 4: {wire: 2, req: true}}
	treNPC    = lpMsg{2: {wire: 2}, 3: {wire: 0}, 9: {wire: 2}}
	treSeason = lpMsg{
		1: {wire: 2, req: true}, 2: {wire: 2, req: true}, 3: {wire: 2, req: true}, 4: {wire: 2, req: true}, 5: {wire: 2, req: true},
		6: {wire: 2, req: true}, 7: {wire: 0, req: true}, 8: {wire: 2, req: true}, 9: {wire: 2, req: true}, 10: {wire: 2, req: true},
		11: {wire: 2, req: true}, 12: {wire: 2, req: true, msg: treNPC}, 13: {wire: 2, req: true, msg: treNPC},
		14: {wire: 2, req: true}, 15: {wire: 2, req: true}, 16: {wire: 2, req: true}, 17: {wire: 2, req: true}, 18: {wire: 2, req: true},
		19: {wire: 2, req: true}, 20: {wire: 2, req: true}, 21: {wire: 2, req: true}, 22: {wire: 2, req: true},
		23: {wire: 0, req: true}, 24: {wire: 0, req: true}, 25: {wire: 2, req: true},
	}
	treEnter = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 2, req: true, msg: treSeason}, 4: {wire: 2, rep: true, msg: treCell}, 5: {wire: 0, req: true}}
	// treEnterEnded is sc_room_enter_res with SeasonStatus 2: no ring, the one NPC portrait (AvatarInfo f6).
	treEnterEnded = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 2, req: true, msg: treSeason}, 5: {wire: 0, req: true}, 6: {wire: 2, msg: treNPC}}
	treOwn        = lpMsg{1: {wire: 0}, 2: {wire: 2}, 3: {wire: 0}, 9: {wire: 2}}
	treRelay      = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}, 4: {wire: 0, req: true}, 5: {wire: 0, req: true}, 6: {wire: 0, req: true}, 7: {wire: 0}, 8: {wire: 2, msg: treOwn}, 9: {wire: 2, req: true, msg: treCell}, 10: {wire: 2, msg: treDetail}, 11: {wire: 2, msg: treMyRank}}
	treItem       = lpMsg{1: {wire: 0}, 2: {wire: 2}, 3: {wire: 2}, 4: {wire: 0}}
	treReward     = lpMsg{1: {wire: 0}, 2: {wire: 2, rep: true, msg: treItem}, 5: {wire: 2}}
	treCoin       = lpMsg{1: {wire: 0, req: true}}
	treBonus      = lpMsg{1: {wire: 0, req: true}, 3: {wire: 2}, 4: {wire: 0}}
	treRanking    = lpMsg{1: {wire: 0}, 2: {wire: 0}, 3: {wire: 0}, 4: {wire: 0}, 5: {wire: 0}, 6: {wire: 0}, 7: {wire: 0}, 10: {wire: 0}}
	treGoldKey    = lpMsg{1: {wire: 0, req: true}}
	// The slice 3 probe squares (treasure-spec 3.2 to 3.5): exact wire types, and the repeated fields the client reads as repeated.
	treLink      = lpMsg{1: {wire: 2, req: true}}
	treBuff      = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true}, 3: {wire: 0, req: true}, 4: {wire: 2, req: true, msg: treLink}, 5: {wire: 2, req: true}, 6: {wire: 2, req: true}}
	treGradeGoal = lpMsg{1: {wire: 2, req: true}, 2: {wire: 0, req: true}}
	treGoal      = lpMsg{1: {wire: 2, req: true, msg: treGradeGoal}}
	treSpecQuest = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true, msg: treGoal}, 3: {wire: 0, req: true}, 4: {wire: 0, req: true}, 5: {wire: 0, req: true}, 6: {wire: 2, req: true, msg: treLink}, 7: {wire: 0, req: true}, 8: {wire: 0, req: true}}
	treSpecial   = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true, msg: treSpecQuest}}
	treRankCell  = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}}
	treFly       = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: treRankCell}}
	treMiniGame  = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}, 4: {wire: 0, rep: true}, 5: {wire: 0, req: true}, 6: {wire: 0, rep: true}, 7: {wire: 0, req: true}}
	treDetail    = lpMsg{1: {wire: 2, msg: treReward}, 3: {wire: 2, msg: treCoin}, 4: {wire: 2, msg: treRanking}, 5: {wire: 2, msg: treBuff}, 6: {wire: 2, msg: treBonus}, 7: {wire: 2, msg: treSpecial}, 9: {wire: 2, msg: treGoldKey}, 10: {wire: 2, msg: treFly}, 11: {wire: 2, msg: treMiniGame}}
	// treTravel is sc_fly_travel_res: Result, the square's CellInfo, its CellDetailInfo and the tickets (no RewardInfo).
	treTravel = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true, msg: treCell}, 3: {wire: 2, req: true, msg: treDetail}, 5: {wire: 0, req: true}, 6: {wire: 0, req: true}, 7: {wire: 0, req: true}, 8: {wire: 0, req: true}}
	treRoll   = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}, 4: {wire: 0, req: true}, 5: {wire: 0, req: true}, 6: {wire: 2, req: true, msg: treCell}, 7: {wire: 2, msg: treDetail}, 8: {wire: 0, req: true}, 9: {wire: 2, msg: treReward}}
	// treCard is sc_gold_key_use_res: Result, the card type, its value, the square the token ends on, that square's detail and
	// the lap text (the sc 10 wire of slice 3, treasure-spec section 3.1).
	treCard    = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}, 5: {wire: 2, req: true, msg: treCell}, 6: {wire: 2, msg: treDetail}, 7: {wire: 2, msg: treReward}, 8: {wire: 0, req: true}, 9: {wire: 0, req: true}, 10: {wire: 0, req: true}, 11: {wire: 0, req: true}}
	treRollNo  = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0}, 3: {wire: 0}, 4: {wire: 0}, 5: {wire: 0}}
	treResult  = lpMsg{1: {wire: 0, req: true}}
	treStatus  = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0}}
	treCoinRef = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}}
)

// treLogBuf collects the session's log lines for the tests; the observer writes from its own goroutine.
type treLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *treLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *treLogBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// treAt sets the clock for one test.
func treAt(t *testing.T, ts time.Time) {
	t.Helper()
	prev := economy.NowFunc
	economy.NowFunc = func() time.Time { return ts }
	t.Cleanup(func() { economy.NowFunc = prev })
}

// treTime is 2026-11-10 12:00 JST, inside the default season.
var treTime = time.Date(2026, 11, 10, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))

// treScene installs two lab accounts, an empty board book and the clock inside the season for one test.
func treScene(t *testing.T) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{
		"t-1001": {AccessToken: "t-1001", SessionKey: "sk", Aid: "1001", Name: "Cherry", Gender: "FEMALE"},
		"t-1002": {AccessToken: "t-1002", SessionKey: "sk", Aid: "1002", Name: "Test", Gender: "MALE"},
	})
	store.ResetTreasure()
	t.Cleanup(store.ResetTreasure)
	treAt(t, treTime)
}

// treDice makes the die show the given values in turn; the coin amounts take their minimum. Each roll of the test uses
// one value.
func treDice(t *testing.T, values ...int) {
	t.Helper()
	prev := economy.TreasureRandIntn
	t.Cleanup(func() { economy.TreasureRandIntn = prev })
	next := 0
	economy.TreasureRandIntn = func(n int) int {
		if n != 6 {
			return 0
		}
		v := values[min(next, len(values)-1)]
		next++
		return v - 1
	}
}

// treGateway is one gateway connection of a board player: the real observer loop reads the frames the test writes to a
// pipe, and the test reads every reply.
type treGateway struct {
	t    *testing.T
	peer net.Conn
	sid  [8]byte
	logs *treLogBuf
}

// newTreGateway starts the gateway frame loop on a pipe. The tunnel id is agent 9's, 1.1.114.0 (0x0001000100720000).
func newTreGateway(t *testing.T) *treGateway {
	t.Helper()
	server, client := net.Pipe()
	logs := &treLogBuf{}
	t.Cleanup(func() { client.Close(); server.Close() })
	go observeGatewayFrames(&lockedConn{Conn: server}, GatewayAddr, "pipe", log.New(logs, "", 0))
	return &treGateway{t: t, peer: client, sid: [8]byte{0x00, 0x01, 0x00, 0x01, 0x00, 0x72, 0x00, 0x00}, logs: logs}
}

// write sends one frame (without its length prefix) to the gateway.
func (g *treGateway) write(frame []byte) {
	g.t.Helper()
	_ = g.peer.SetWriteDeadline(time.Now().Add(2 * time.Second))
	out := binary.BigEndian.AppendUint32(nil, uint32(len(frame)))
	if _, err := g.peer.Write(append(out, frame...)); err != nil {
		g.t.Fatalf("write: %v", err)
	}
}

// open sends the client's subtype-1 open control and checks the echo: exactly 22 bytes, type 0x41, the same session id.
func (g *treGateway) open() {
	g.t.Helper()
	g.write(append([]byte{'G', 0x41}, g.sid[:]...))
	want := append(append([]byte{0, 0, 0, 18, 'G', 0x41}, g.sid[:]...), make([]byte, 8)...)
	if got := g.raw(); !bytes.Equal(got, want) {
		g.t.Fatalf("open echo = %x, want %x", got, want)
	}
}

// send sends one data request (type 0x40, the session id, msgid, protobuf).
func (g *treGateway) send(msgid uint16, body []byte) {
	g.t.Helper()
	f := append([]byte{'G', 0x40}, g.sid[:]...)
	f = binary.BigEndian.AppendUint16(f, msgid)
	g.write(append(f, body...))
}

// raw reads one frame from the gateway as it crosses the wire: the 4-byte length prefix and the frame.
func (g *treGateway) raw() []byte {
	g.t.Helper()
	_ = g.peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	var pre [4]byte
	if _, err := io.ReadFull(g.peer, pre[:]); err != nil {
		g.t.Fatalf("read prefix: %v", err)
	}
	f := make([]byte, binary.BigEndian.Uint32(pre[:]))
	if _, err := io.ReadFull(g.peer, f); err != nil {
		g.t.Fatalf("read frame: %v", err)
	}
	return append(pre[:], f...)
}

// read returns the next reply, a data frame of this session: it must be type 0x40 and msgid, and never subtype 2.
func (g *treGateway) read(msgid uint16) []byte {
	g.t.Helper()
	f := g.raw()[4:]
	if len(f) < 12 || f[0] != 'G' || !bytes.Equal(f[2:10], g.sid[:]) {
		g.t.Fatalf("envelope = %x", f)
	}
	if f[1]&7 == 2 {
		g.t.Fatalf("subtype 2 sent to agent 9: %x", f)
	}
	if f[1] != 0x40 {
		g.t.Fatalf("data type = 0x%02x, want 0x40", f[1])
	}
	if got := binary.BigEndian.Uint16(f[10:12]); got != msgid {
		g.t.Fatalf("msgid = %d, want %d", got, msgid)
	}
	return f[12:]
}

// readShape reads one reply and checks its fields against shape.
func (g *treGateway) readShape(msgid uint16, shape lpMsg) []byte {
	g.t.Helper()
	body := g.read(msgid)
	if err := lpCheck(body, shape, "treasure"); err != nil {
		g.t.Fatalf("msgid %d shape: %v (body %x)", msgid, err, body)
	}
	return body
}

// control reads the 22-byte control frame of the given type byte for the session.
func (g *treGateway) control(typ byte) {
	g.t.Helper()
	want := append(append([]byte{0, 0, 0, 18, 'G', typ}, g.sid[:]...), make([]byte, 8)...)
	if got := g.raw(); !bytes.Equal(got, want) {
		g.t.Fatalf("control = %x, want %x", got, want)
	}
}

// login logs aid in and checks Result 0.
func (g *treGateway) login(aid uint64) {
	g.t.Helper()
	g.send(treasureCsLogin, cirLoginBody(aid))
	if body := g.readShape(treasureRcLogin, treResult); cirVar(body, 1) != 0 {
		g.t.Fatalf("login result = %d, want 0", cirVar(body, 1))
	}
}

// exit leaves the board: the empty exit reply, then the subtype-5 close control with the request's session id.
func (g *treGateway) exit() {
	g.t.Helper()
	g.send(treasureCsRoomExit, nil)
	if body := g.read(treasureRcRoomExit); len(body) != 0 {
		g.t.Fatalf("exit reply = %x, want empty", body)
	}
	g.control(0x45)
}

// TestTreasureOpenEchoIsExact: the open control gets exactly one subtype-1 echo, and nothing else follows it.
func TestTreasureOpenEchoIsExact(t *testing.T) {
	treScene(t)
	g := newTreGateway(t)
	g.open()
	assertNoReply(t, g.peer)
}

// TestTreasureBoardOverTheGateway drives one session end to end: login, enter (28 squares, season 1), relay start, six
// rolls (one with a lap), coin count, the exit with its close control, the tutorial flag and the re-entry.
func TestTreasureBoardOverTheGateway(t *testing.T) {
	treScene(t)
	treDice(t, 3, 3, 5, 6, 6, 6) // squares 3, 6, 11, 17, 23, 1: no gold key (5, 12, 19, 26) on the way
	g := newTreGateway(t)
	g.open()
	g.login(1001)

	g.send(treasureCsRoomEnter, nil)
	enter := g.readShape(treasureRcRoomEnter, treEnter)
	if cirVar(enter, 1) != 0 || cirVar(enter, 2) != 1 || cirVar(enter, 5) != 0 {
		t.Fatalf("enter = result %d status %d tutorial %d, want 0, 1, 0", cirVar(enter, 1), cirVar(enter, 2), cirVar(enter, 5))
	}
	season := cirBytes(enter, 3)
	if string(cirBytes(season, 8)) != "Jewel Carriage(s)" || string(cirBytes(season, 9)) != "Jewel(s)" ||
		string(cirBytes(season, 10)) != "Queen's Jewel Box" || cirVar(season, 7) != 1 {
		t.Fatalf("season names = %q %q %q, PlayTicketCount %d", cirBytes(season, 8), cirBytes(season, 9), cirBytes(season, 10), cirVar(season, 7))
	}
	for _, f := range []uint64{1, 2, 3, 4, 5, 6, 14, 15, 16, 17, 18, 19, 20, 21, 22, 25} {
		if got := cirBytes(season, f); got == nil || string(got) != economy.TreasureSeasonPicture(f) {
			t.Fatalf("season picture field %d = %q, want present and %q", f, got, economy.TreasureSeasonPicture(f))
		}
	}
	cells := cirList(enter, 4)
	if len(cells) != 28 || cirVar(cells[0], 3) != 0 || string(cirBytes(cells[0], 4)) != "FR" || string(cirBytes(cells[7], 4)) != "BR" {
		t.Fatalf("ring = %d squares, first %x, sixth %x", len(cells), cells[0], cells[6])
	}

	g.send(treasureCsRelayStart, nil)
	relay := g.readShape(treasureRcRelayStart, treRelay)
	if cirVar(relay, 2) != 10 || cirVar(relay, 3) != 10 || cirVar(relay, 4) != 0 || cirVar(relay, 5) != 600 || cirVar(relay, 6) != 0 {
		t.Fatalf("relay = %x, want 10/10 tickets, no timer, 600 s charge, 0 jewels", relay)
	}
	if own := cirBytes(relay, 8); cirVar(own, 1) != 1001 || string(cirBytes(own, 2)) != "Cherry" {
		t.Fatalf("own avatar = %x", own)
	}
	if cell := cirBytes(relay, 9); cirVar(cell, 1) != 0 || cirVar(cell, 3) != 0 {
		t.Fatalf("start square = %x, want position 0 type 0", cell)
	}

	// Roll 1 (die 3): bonus square 3 pays 5 Jewels and the ticket is spent.
	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	roll := g.readShape(treasureRcRolling, treRoll)
	detail := cirBytes(roll, 7)
	if cirVar(roll, 1) != 0 || cirVar(roll, 2) != 9 || cirVar(roll, 4) != 600 || cirVar(roll, 8) != 3 {
		t.Fatalf("roll 1 = %x, want result 0, 9 tickets, 600 s, die 3", roll)
	}
	if cell := cirBytes(roll, 6); cirVar(cell, 1) != 3 || cirVar(cell, 3) != 4 || string(cirBytes(cell, 4)) != "FR" {
		t.Fatalf("roll 1 square = %x, want position 3 type 4 FR", cell)
	}
	if bonus := cirBytes(detail, 6); cirVar(bonus, 4) != 5 || string(cirBytes(bonus, 3)) != "JEWEL" || string(cirBytes(cirBytes(detail, 1), 5)) != "+5 Jewel(s)" {
		t.Fatalf("bonus detail = %x", detail)
	}

	// The bonus roll is followed by an unsolicited sc 35 with the new count (the HUD has no other writer for it).
	if coins := g.readShape(treasureRcCoinRefresh, treCoinRef); cirVar(coins, 2) != 5 {
		t.Fatalf("pushed coin count = %d, want 5", cirVar(coins, 2))
	}

	// Roll 2 (die 3): coin square 6 pays one Jewel (the minimum of 1 to 3).
	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	roll = g.readShape(treasureRcRolling, treRoll)
	item := cirBytes(cirBytes(cirBytes(roll, 7), 1), 2)
	if cirVar(cirBytes(roll, 6), 1) != 6 || cirVar(item, 4) != 1 || cirVar(item, 1) != 10 || cirBytes(cirBytes(roll, 7), 3) == nil {
		t.Fatalf("roll 2 = %x, want square 6 with a coin view of 1", roll)
	}

	// Roll 3 (die 5): coin square 11, no lap.
	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	roll = g.readShape(treasureRcRolling, treRoll)
	if cirVar(roll, 1) != 0 || cirVar(roll, 8) != 5 || cirVar(cirBytes(roll, 6), 1) != 11 || cirBytes(roll, 9) != nil {
		t.Fatalf("roll 3 = %x, want result 0, die 5, square 11 and no lap", roll)
	}

	// Rolls 4 and 5 (die 6 each): square 17 is a bonus square (the sc 35 push follows), then square 23 is a buff square (pays nothing).
	for i := 4; i <= 5; i++ {
		g.send(treasureCsRolling, pbVar(nil, 1, 0))
		roll = g.readShape(treasureRcRolling, treRoll)
		if cirVar(roll, 1) != 0 || cirVar(roll, 8) != 6 || cirBytes(roll, 9) != nil {
			t.Fatalf("roll %d = %x, want result 0 die 6 and no lap", i, roll)
		}
		if cirVar(cirBytes(roll, 6), 3) == economy.TreasureCellBonus { // a bonus square: the sc 35 push follows
			g.readShape(treasureRcCoinRefresh, treCoinRef)
		}
	}
	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	roll = g.readShape(treasureRcRolling, treRoll)
	start := cirBytes(roll, 9)
	if cirVar(cirBytes(roll, 6), 1) != 1 || cirVar(roll, 2) != 9 || string(cirBytes(start, 5)) != "+5 Jewel Carriage(s)" {
		t.Fatalf("roll 6 = %x, want square 1, 9 tickets after the lap, the lap text", roll)
	}

	g.send(treasureCsCoinRefresh, nil)
	if coins := g.readShape(treasureRcCoinRefresh, treCoinRef); cirVar(coins, 2) != 13 {
		t.Fatalf("coin count after six rolls = %d, want 13 (5 + 1 + 1 + 5 + 0 + 1)", cirVar(coins, 2))
	}

	// Leave the board: the exit reply and the close control. A request after the close is refused.
	g.exit()
	g.send(treasureCsRelayStart, nil)
	if cirVar(g.readShape(treasureRcRelayStart, treResult), 1) != treasureRefused {
		t.Fatal("relay start after the exit was not refused")
	}

	// Back in: the tutorial is still open, the tutorial request answers, and the next enter reports it done with the
	// same tickets, square and jewels.
	g.open()
	g.login(1001)
	g.send(treasureCsRoomEnter, nil)
	if enter = g.readShape(treasureRcRoomEnter, treEnter); cirVar(enter, 5) != 0 {
		t.Fatalf("tutorial flag before the tutorial = %d, want 0", cirVar(enter, 5))
	}
	g.send(treasureCsTutorial, nil)
	if body := g.read(treasureRcTutorial); len(body) != 0 {
		t.Fatalf("tutorial reply = %x, want empty", body)
	}
	g.exit()
	g.open()
	g.login(1001)
	g.send(treasureCsRoomEnter, nil)
	if enter = g.readShape(treasureRcRoomEnter, treEnter); cirVar(enter, 5) != 1 {
		t.Fatalf("tutorial flag after the tutorial = %d, want 1", cirVar(enter, 5))
	}
	g.send(treasureCsRelayStart, nil)
	relay = g.readShape(treasureRcRelayStart, treRelay)
	if cirVar(relay, 2) != 9 || cirVar(relay, 6) != 13 || cirVar(cirBytes(relay, 9), 1) != 1 {
		t.Fatalf("relay after re-entry = %x, want 9 tickets, 13 jewels, square 1", relay)
	}
	if !strings.Contains(g.logs.String(), "TREASURE msgid=4 roll aid=1001 dice=6 from=23 to=1") {
		t.Fatalf("roll log line missing:\n%s", g.logs.String())
	}
}

// TestTreasureRefusals: an unknown account, a closed season and a board that was never entered get Result 1 (the
// client's way back to the Garden), and a roll without tickets moves nothing.
func TestTreasureRefusals(t *testing.T) {
	treScene(t)
	g := newTreGateway(t)
	g.open()
	g.login(9999) // no account: login still answers 0, but the board is refused
	g.send(treasureCsRoomEnter, nil)
	if cirVar(g.readShape(treasureRcRoomEnter, treResult), 1) != treasureRefused {
		t.Fatal("enter for an unknown account was not refused")
	}

	g.login(1001)
	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	if cirVar(g.readShape(treasureRcRolling, treRollNo), 1) != treasureRefused {
		t.Fatal("roll before enter was not refused")
	}

	// 2026-10-31: before the first season (2026-11-01 06:00 JST). The cycle is closed, so the enter is status 2 with no ring
	// (season-end-spec 12 (c)), not a refusal.
	treAt(t, treTime.AddDate(0, 0, -10))
	g.send(treasureCsRoomEnter, nil)
	if body := g.readShape(treasureRcRoomEnter, treEnterEnded); cirVar(body, 2) != treasureSeasonEnded || len(cirList(body, 4)) != 0 {
		t.Fatalf("enter before the season = %x, want status 2 and no ring", body)
	}
}

// TestTreasureNoTicketRoll: with no ticket left the roll is refused with the counts, and the square does not change.
func TestTreasureNoTicketRoll(t *testing.T) {
	treScene(t)
	treDice(t, 1) // one square a roll: no lap, so the ten tickets are the only ones
	g := newTreGateway(t)
	g.open()
	g.login(1001)
	g.send(treasureCsRoomEnter, nil)
	g.readShape(treasureRcRoomEnter, treEnter)
	for i := 0; i < 10; i++ { // the start is ten tickets; the eleventh roll has none
		g.send(treasureCsRolling, pbVar(nil, 1, 0))
		g.readShape(treasureRcRolling, treRoll)
		if i+1 == 3 || i+1 == 10 { // bonus squares: the sc 35 push follows
			g.readShape(treasureRcCoinRefresh, treCoinRef)
		}
	}
	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	refused := g.readShape(treasureRcRolling, treRollNo)
	if cirVar(refused, 1) != treasureRefused || cirVar(refused, 2) != 0 {
		t.Fatalf("roll with no tickets = %x, want Result 1 and 0 tickets", refused)
	}
}

// TestTreasureGoldKeyOverTheGateway: a roll onto square 5 (a gold key) answers with the square's Availability flag (sc 4
// f7.f9.f1 = 1) and no reward. cs 10 (empty body) plays the card: with the stubbed draws the first card, one step forward, so
// the token goes to square 6 (a coin, one Jewel), and the reply has no lap. A second cs 10 on the same key is refused.
func TestTreasureGoldKeyOverTheGateway(t *testing.T) {
	treScene(t)
	treDice(t, 5)
	g := newTreGateway(t)
	g.open()
	g.login(1001)
	g.send(treasureCsRoomEnter, nil)
	g.readShape(treasureRcRoomEnter, treEnter)

	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	roll := g.readShape(treasureRcRolling, treRoll)
	if cell, detail := cirBytes(roll, 6), cirBytes(roll, 7); cirVar(cell, 1) != 5 || cirVar(cell, 3) != economy.TreasureCellGoldKey ||
		cirVar(cirBytes(detail, 9), 1) != 1 || cirBytes(detail, 1) != nil || cirVar(roll, 2) != 9 {
		t.Fatalf("roll onto square 5 = %x, want a gold key with availability 1, no reward and 9 tickets", roll)
	}

	g.send(treasureCsGoldKeyUse, nil)
	card := g.readShape(treasureRcGoldKey, treCard)
	square := cirBytes(card, 5)
	if cirVar(card, 1) != 0 || cirVar(card, 2) != 1 || cirVar(card, 3) != 1 || cirVar(square, 1) != 6 || cirVar(square, 3) != economy.TreasureCellCoin ||
		cirBytes(card, 7) != nil {
		t.Fatalf("gold key use = %x, want result 0, card 1, value 1, square 6 (a coin) and no lap", card)
	}
	if cirVar(card, 8) == 0 || cirVar(card, 9) != 10 || cirVar(card, 11) != 600 { // the HUD tickets come from f8..f11
		t.Fatalf("gold key ticket fields = %x, want the tickets, cap 10, charge 600", card)
	}
	detail := cirBytes(card, 6) // the landing's CellDetail: the coin view with one Jewel and the coin marker
	if item := cirBytes(cirBytes(detail, 1), 2); cirVar(item, 1) != 10 || cirVar(item, 4) != 1 || cirBytes(detail, 3) == nil {
		t.Fatalf("coin detail of the landing = %x, want a coin view of one Jewel", detail)
	}
	if !strings.Contains(g.logs.String(), "TREASURE msgid=10 gold key aid=1001 card=1 value=1 from=5 to=6 coins=1 bonus=0 lap=false jewels=1 result=0") {
		t.Fatalf("gold key log line missing:\n%s", g.logs.String())
	}

	g.send(treasureCsGoldKeyUse, nil)
	if cirVar(g.readShape(treasureRcGoldKey, treResult), 1) != treasureRefused {
		t.Fatal("second card on one key was not refused")
	}
}

// treProbeRing is the board of the probe test: 3 buff, 5 special mission, 8 travel and 9 mini-game squares, with the exchange
// squares 2, 13 and 21 (the travel candidates from square 5).
var treProbeRing = []int{0, 1, 2, 3, 5, 8, 9, 1, 4, 1, 7, 1, 1, 2, 1, 1, 4, 1, 7, 1, 1, 2, 1, 1, 4, 1, 7, 1}

// treLoadRing loads the default treasure file with ring as its board, and square 2 as the only exchange tile (the default
// tiles name 8, 15 and 22, which the probe ring does not keep as exchange squares), for one test.
func treLoadRing(t *testing.T, ring []int) {
	t.Helper()
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(data.TreasureJSON, &doc); err != nil {
		t.Fatal(err)
	}
	doc["board"], _ = json.Marshal(map[string][]int{"ring": ring})
	var ex map[string]json.RawMessage
	if err := json.Unmarshal(doc["exchange"], &ex); err != nil {
		t.Fatal(err)
	}
	ex["tiles"], _ = json.Marshal([]map[string]int{{"square": 2, "times": 1}})
	doc["exchange"], _ = json.Marshal(ex)
	raw, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "treasure.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := economy.TreasureLoadFrom(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { economy.TreasureLoadFrom("") })
}

// treMoveCell is the MoveCellInfo of cs 9 for an exchange square: CellPosition, CellId and CellType.
func treMoveCell(pos uint64) []byte {
	b := pbVar(nil, 1, pos)
	b = pbVar(b, 2, pos)
	return pbVar(b, 3, economy.TreasureCellExchange)
}

// TestTreasureProbeSquaresOverTheGateway: the probe squares answer their sc 4 CellDetailInfo with the exact wire shapes (buff 3,
// special mission 5, travel 8, mini-game 9). cs 9 moves the token from the travel square to an exchange square only: a square
// that is not one, the square the token stands on, and a second travel are refused and change nothing.
func TestTreasureProbeSquaresOverTheGateway(t *testing.T) {
	treScene(t)
	treLoadRing(t, treProbeRing)
	if treasureBuffGacha < economy.CherryTreasureFirst || treasureBuffGacha > economy.CherryTreasureLast {
		t.Fatalf("buff gacha id %d is outside the Cherry Treasure block", treasureBuffGacha)
	}
	// Square 13 is full today (60 Jewels taken on this game day), so its IsReset is 1.
	day := (treTime.Unix() + 3*3600) / 86400
	if _, err := store.UpdateTreasure("1001", func(r *store.TreasureRecord) {
		*r = store.TreasureRecord{Season: economy.ActiveTreasureData().Season.ID, Tickets: 10, Charged: treTime.UnixMilli(), ExDay: day,
			Exchanged: map[string]int64{"13": 60}}
	}); err != nil {
		t.Fatal(err)
	}
	// Dice: 1 to the coin on square 1, 2 to the buff on 3, 1 to the secret quest on 4, 1 to the travel on 5, then (from square 2,
	// after the travel) 4 to the mini-game on 6.
	treDice(t, 1, 2, 1, 1, 4)
	g := newTreGateway(t)
	g.open()
	g.login(1001)
	g.send(treasureCsRoomEnter, nil)
	g.readShape(treasureRcRoomEnter, treEnter)

	g.send(treasureCsRolling, pbVar(nil, 1, 0)) // square 1: a coin, one Jewel
	g.readShape(treasureRcRolling, treRoll)

	g.send(treasureCsRolling, pbVar(nil, 1, 0)) // square 3: the buff gacha
	roll := g.readShape(treasureRcRolling, treRoll)
	cell, buff := cirBytes(roll, 6), cirBytes(cirBytes(roll, 7), 5)
	if cirVar(cell, 1) != 3 || cirVar(cell, 3) != economy.TreasureCellBuff || cirVar(buff, 1) != treasureBuffGacha ||
		string(cirBytes(buff, 2)) != "CUAH006FG" || cirVar(buff, 3) != 10 || string(cirBytes(buff, 5)) != "RARE" || string(cirBytes(buff, 6)) != "NEW" {
		t.Fatalf("buff landing = %x, want square 3 with the buff CUAH006FG at 10%%, grade R, state NEW", roll)
	}

	g.send(treasureCsRolling, pbVar(nil, 1, 0)) // square 4: the secret quest
	roll = g.readShape(treasureRcRolling, treRoll)
	special := cirBytes(cirBytes(roll, 7), 7)
	quest := cirBytes(special, 2)
	if cirVar(cirBytes(roll, 6), 1) != 4 || cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellSpecial || cirVar(special, 1) != 1 ||
		cirVar(quest, 1) != 1 || cirVar(quest, 3) != 1 || cirVar(quest, 4) != 0 || cirVar(quest, 5) != 3 || cirVar(quest, 7) != 0 ||
		cirVar(quest, 8) != 9 {
		t.Fatalf("secret quest landing = %x, want square 4 with availability 1, type 1, goal 1, count 0, 3 tickets, no enable, view 9", roll)
	}

	g.send(treasureCsRolling, pbVar(nil, 1, 0)) // square 5: the travel
	roll = g.readShape(treasureRcRolling, treRoll)
	fly := cirBytes(cirBytes(roll, 7), 10)
	rows := cirList(fly, 2)
	if cirVar(cirBytes(roll, 6), 1) != 5 || cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellTravel || cirVar(fly, 1) != 1 || len(rows) != 3 ||
		cirVar(rows[0], 1) != 2 || cirVar(rows[0], 2) != 0 || cirVar(rows[1], 1) != 13 || cirVar(rows[1], 2) != 1 ||
		cirVar(rows[2], 1) != 21 || cirVar(rows[2], 2) != 0 {
		t.Fatalf("travel landing = %x, want square 5 with availability 1 and exchange squares 2 (free), 13 (full), 21 (free)", roll)
	}

	// Refused, and the token stays on 5: square 6 is a mini-game and 5 is the square the token stands on.
	for _, to := range []uint64{6, 5} {
		g.send(treasureCsFlyTravel, treMoveCell(to))
		if cirVar(g.readShape(treasureRcFlyTravel, treResult), 1) != treasureRefused {
			t.Fatalf("travel to %d was not refused", to)
		}
	}

	// Accepted: square 2, no lap, no payment; 6 tickets left of 10 after four rolls, the timer at 600 s, and no RewardInfo.
	g.send(treasureCsFlyTravel, treMoveCell(2))
	fly2 := g.readShape(treasureRcFlyTravel, treTravel)
	if cirVar(fly2, 1) != 0 || cirVar(cirBytes(fly2, 2), 1) != 2 || cirVar(cirBytes(fly2, 2), 3) != economy.TreasureCellExchange ||
		cirVar(fly2, 5) != 6 || cirVar(fly2, 6) != 10 || cirVar(fly2, 7) != 600 || cirVar(fly2, 8) != 600 {
		t.Fatalf("travel to 2 = %x, want result 0, square 2 (an exchange square), 6 of 10 tickets, 600 s", fly2)
	}
	if rank := cirBytes(cirBytes(fly2, 3), 4); cirVar(rank, 1) != 1 || cirVar(rank, 2) != 1 || cirVar(rank, 3) != 60 || cirVar(rank, 4) != 10 { // 1 Jewel, 60 left, rate 10 (1x)
		t.Fatalf("travel detail = %x, want the exchange rate 10 and 60 left", rank)
	}

	g.send(treasureCsRolling, pbVar(nil, 1, 0)) // square 6: the mini-game, with one Jewel
	roll = g.readShape(treasureRcRolling, treRoll)
	game := cirBytes(cirBytes(roll, 7), 11)
	if cirVar(cirBytes(roll, 6), 3) != economy.TreasureCellMiniGame || cirVar(game, 1) != 1 || cirVar(game, 2) != 1 || cirVar(game, 3) != 20 ||
		cirVar(game, 5) != 20 || cirVar(game, 7) != 1 || !slices.Equal(cirVars(game, 4), []uint64{1, 2, 3}) ||
		!slices.Equal(cirVars(game, 6), []uint64{1, 2, 3}) {
		t.Fatalf("mini-game landing = %x, want availability 1, 1 Jewel, max 20, multiples 1 to 3 unpacked, 1 to use", roll)
	}

	// The travel is spent: a second cs 9 is refused.
	g.send(treasureCsFlyTravel, treMoveCell(13))
	if cirVar(g.readShape(treasureRcFlyTravel, treResult), 1) != treasureRefused {
		t.Fatal("travel after the travel was not refused")
	}
	if !strings.Contains(g.logs.String(), "TREASURE msgid=9 travel aid=1001 from=5 to=2 result=0") {
		t.Fatalf("travel log line missing:\n%s", g.logs.String())
	}
}

// TestTreasureLaterSlicesAnswerSafely: the requests that need a board (mini-game, secret quest enable and claim) are refused
// before the board is entered, the buff list and wear answer for a logged-in account, the reward popup exit is refused in a play window, and
// the client's own close (subtype 2) and the ticket sync (no caller) get no reply. The buff, mini-game and secret quest answers
// are checked over the gateway in treasure_slice3_test.go.
func TestTreasureLaterSlicesAnswerSafely(t *testing.T) {
	treScene(t)
	g := newTreGateway(t)
	g.open()
	g.login(1001)

	g.send(treasureCsBuffList, nil) // buff list: nothing owned, Result 0
	if cirVar(g.readShape(treasureRcBuffList, treBuffList), 1) != 0 {
		t.Fatal("buff list not an empty success")
	}
	g.send(treasureCsBuffWear, nil) // buff wear with no outfit: refused (the client always sends the whole outfit)
	if cirVar(g.readShape(treasureRcBuffWear, treResult), 1) != treasureRefused {
		t.Fatal("buff wear with no outfit not refused")
	}
	g.send(8, nil) // mini-game before the board is entered: refused
	if cirVar(g.readShape(treasureRcMiniGame, treResult), 1) != treasureRefused {
		t.Fatal("mini-game not refused")
	}
	g.send(9, nil) // travel: refused
	if cirVar(g.readShape(treasureRcFlyTravel, treResult), 1) != treasureRefused {
		t.Fatal("travel not refused")
	}
	g.send(25, nil) // special mission reward before the board is entered: refused
	if cirVar(g.readShape(treasureRcSpecialReward, treResult), 1) != treasureRefused {
		t.Fatal("special mission reward not refused")
	}
	g.send(26, nil) // special mission enable before the board is entered: refused
	if cirVar(g.readShape(treasureRcSpecialEnable, treResult), 1) != treasureRefused {
		t.Fatal("special mission enable not refused")
	}
	g.send(28, nil) // reward popup back key in a play window: refused (the popup is opened after the window only)
	if cirVar(g.readShape(treasureRcSeasonState, treStatus), 1) != treasureRefused {
		t.Fatal("reward popup exit in a play window not refused")
	}
	g.send(24, nil) // ticket sync: no caller in the client, no reply
	g.send(0x30, nil)
	g.write(append([]byte{'G', 0x42}, g.sid[:]...)) // the client's own close (subtype 2): never answered
	assertNoReply(t, g.peer)
	for _, msgid := range []string{"msgid=8 minigame refused", "msgid=25 secret reward refused", "msgid=26 secret enable refused", "msgid=24 not built"} {
		if !strings.Contains(g.logs.String(), "TREASURE "+msgid) {
			t.Fatalf("log lacks %q", msgid)
		}
	}
}
