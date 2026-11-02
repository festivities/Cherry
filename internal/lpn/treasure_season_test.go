package lpn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/economy"
	"cherry/internal/store"
)

// Season cycle over the gateway (season-end-spec 12). The default season 1950000001 plays from 2026-11-01 06:00 JST for 28
// days, its reward period runs 7 more days, and season 1950000002 starts at 2026-12-06 06:00 JST.

// treRewardTime is 2026-12-01 12:00 JST: the reward period of season 1950000001 (claims only, status 2).
var treRewardTime = time.Date(2026, 12, 1, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))

// treBeforeSeason is 2026-10-31 12:00 JST: before the first season, so the cycle is closed.
var treBeforeSeason = time.Date(2026, 10, 31, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))

// The relay and rank claim reply shapes of this file (treEnterEnded is in treasure_test.go).
var (
	treRelayEnded = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}, 4: {wire: 0, req: true},
		5: {wire: 0, req: true}, 6: {wire: 0, req: true}, 7: {wire: 0}, 8: {wire: 2, msg: treOwn}, 11: {wire: 2, msg: treMyRank}}
	treRankClaimRes = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, msg: treRewardInfo}}
)

// treRepeatOff loads the default board with season.repeat off: the board closes once season 1950000001's reward period ends.
func treRepeatOff(t *testing.T) {
	t.Helper()
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(data.TreasureJSON, &doc); err != nil {
		t.Fatal(err)
	}
	var season map[string]json.RawMessage
	if err := json.Unmarshal(doc["season"], &season); err != nil {
		t.Fatal(err)
	}
	season["repeat"] = json.RawMessage("false")
	doc["season"], _ = json.Marshal(season)
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

// treTimers replaces treasureAfter for one test: the season-end pushes are armed and kept, and fire runs one of them.
type treTimers struct {
	mu    sync.Mutex
	after []time.Duration
	fns   []func()
}

// treCaptureTimers installs the recorder and returns it.
func treCaptureTimers(t *testing.T) *treTimers {
	t.Helper()
	tm := &treTimers{}
	prev := treasureAfter
	treasureAfter = func(d time.Duration, f func()) *time.Timer {
		tm.mu.Lock()
		defer tm.mu.Unlock()
		tm.after = append(tm.after, d)
		tm.fns = append(tm.fns, f)
		return time.AfterFunc(time.Hour, func() {}) // a real timer, so the session can stop it
	}
	t.Cleanup(func() { treasureAfter = prev })
	return tm
}

// armed returns the delays the pushes were armed with.
func (tm *treTimers) armed() []time.Duration {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return append([]time.Duration(nil), tm.after...)
}

// fire runs the i-th armed push. The push writes to the connection, so the caller reads the frame.
func (tm *treTimers) fire(i int) {
	tm.mu.Lock()
	f := tm.fns[i]
	tm.mu.Unlock()
	go f()
}

// treEnterFor enters the board as a player does and returns the sc 1 body.
func treEnterFor(t *testing.T, g *treGateway, shape lpMsg) []byte {
	t.Helper()
	g.send(treasureCsRoomEnter, nil)
	return g.readShape(treasureRcRoomEnter, shape)
}

// TestTreasureRewardPeriodOverTheGateway: a player who opens Treasure World in the reward period gets status 2 with no ring, the
// relay with the tickets, the rank reward once (Result 0, paid to the Mailbox), then 786435 for the reward popup, whose point and
// rank lists and point claim answer, a roll is refused, and the back key of the popup answers 0.
func TestTreasureRewardPeriodOverTheGateway(t *testing.T) {
	treScene(t)
	treAt(t, treRewardTime)
	treSeed(t, store.TreasureRecord{Season: 1950000001, Tickets: 10, Charged: treRewardTime.UnixMilli(), Points: 300})
	cyc := economy.TreasureCurrent(treRewardTime)
	g := newTreGateway(t)
	g.open()
	g.login(1001)

	enter := treEnterFor(t, g, treEnterEnded)
	if cirVar(enter, 2) != treasureSeasonEnded || len(cirList(enter, 4)) != 0 || cirVar(enter, 5) != 0 {
		t.Fatalf("reward period enter = %x, want status 2, no ring", enter)
	}
	if msg := string(cirBytes(enter, 7)); msg == "" || msg != economy.ActiveTreasureData().Season.EndMessage {
		t.Fatalf("reward period Message (f7) = %q, want the season end text", msg)
	}
	if season := cirBytes(enter, 3); cirVar(season, 24) != uint64(cyc.End.Unix()) {
		t.Fatalf("reward period SeasonEndDate = %d, want %d (seconds)", cirVar(season, 24), cyc.End.Unix())
	}

	g.send(treasureCsRelayStart, nil)
	relay := g.readShape(treasureRcRelayStart, treRelayEnded)
	if cirVar(relay, 1) != 0 || cirVar(relay, 2) != 10 || cirVar(relay, 3) != 10 {
		t.Fatalf("reward period relay = %x, want Result 0 and 10/10 tickets", relay)
	}

	g.send(treasureCsClassRewardClaim, nil) // rank 1 with 300 points: 300 Gems to the Mailbox
	claim := g.readShape(treasureRcClassReward, treRankClaimRes)
	if cirVar(claim, 1) != 0 || len(cirBytes(claim, 2)) == 0 {
		t.Fatalf("rank reward claim = %x, want Result 0 with a reward", claim)
	}
	if _, mail := treAccount(t); len(mail) != 1 || mail[0].Amount != 300 {
		t.Fatalf("mail after the rank reward = %+v", mail)
	}

	g.send(treasureCsClassRewardClaim, nil) // paid once: the reward popup opens (786435)
	if cirVar(g.readShape(treasureRcClassReward, treResult), 1) != treasureRewardPopup {
		t.Fatal("second rank claim did not open the reward popup")
	}

	g.send(treasureCsRankingInfo, pbVar(pbVar(nil, 2, 1), 3, 20)) // the ranking board of the season
	if rank := g.readShape(treasureRcRankingInfo, treRankingRes); cirVar(rank, 1) != 0 || len(cirList(rank, 3)) != 1 {
		t.Fatalf("ranking in the reward period = %x", rank)
	}

	g.send(treasureCsPointRewardInfo, nil) // the point rewards list: 100 and 300 are reached, 1000 is not
	rows := cirList(g.readShape(treasureRcPointRewardInfo, treRewardList), 2)
	if len(rows) != 4 || cirVar(rows[0], 11) != 100 {
		t.Fatalf("point rewards in the reward period = %x", rows)
	}
	g.send(treasureCsPointRewardClaim, pbVar(nil, 1, 2)) // 300 points: claimable in the reward period
	if claimed := g.readShape(treasureRcPointReward, treClaimRes); cirVar(claimed, 4) != 2 {
		t.Fatalf("point reward 2 in the reward period = %x", claimed)
	}

	g.send(treasureCsRolling, pbVar(nil, 1, 0)) // the board is closed to rolls
	if cirVar(g.readShape(treasureRcRolling, treRollNo), 1) != treasureRefused {
		t.Fatal("roll in the reward period was not refused")
	}

	g.send(treasureCsRewardExit, nil) // the back key of the reward popup: the season-end box (4593), Result 0
	if cirVar(g.readShape(treasureRcSeasonState, treStatus), 1) != 0 {
		t.Fatal("reward popup exit in the reward period did not answer 0")
	}
	if !strings.Contains(g.logs.String(), "TREASURE msgid=28 reward exit aid=1001 phase=reward result=0") {
		t.Fatal("reward popup exit not logged")
	}
}

// TestTreasureClosedOverTheGateway: before the first season (and after the claim window when season.repeat is off) the enter is
// status 2, the relay answers Result 0, the rank claim answers 65541, the back key answers 65541 and no push is armed.
func TestTreasureClosedOverTheGateway(t *testing.T) {
	treScene(t)
	treAt(t, treBeforeSeason)
	tm := treCaptureTimers(t)
	g := newTreGateway(t)
	g.open()
	g.login(1001)

	enter := treEnterFor(t, g, treEnterEnded)
	if cirVar(enter, 2) != treasureSeasonEnded || len(cirList(enter, 4)) != 0 {
		t.Fatalf("closed enter = %x, want status 2, no ring", enter)
	}
	g.send(treasureCsRelayStart, nil)
	if cirVar(g.readShape(treasureRcRelayStart, treRelayEnded), 1) != 0 {
		t.Fatal("closed relay start not Result 0")
	}
	g.send(treasureCsClassRewardClaim, nil)
	if cirVar(g.readShape(treasureRcClassReward, treResult), 1) != treasureSeasonNotActive {
		t.Fatal("closed rank claim not 65541")
	}
	g.send(treasureCsRewardExit, nil)
	if cirVar(g.readShape(treasureRcSeasonState, treStatus), 1) != treasureSeasonNotActive {
		t.Fatal("closed reward popup exit not 65541")
	}
	if n := len(tm.armed()); n != 0 {
		t.Fatalf("a closed enter armed %d season-end pushes", n)
	}
	g.send(treasureCsRolling, pbVar(nil, 1, 0))
	if cirVar(g.readShape(treasureRcRolling, treRollNo), 1) != treasureRefused {
		t.Fatal("roll in a closed cycle was not refused")
	}
	g.exit()
}

// TestTreasureRepeatOffClosesAfterTheClaimWindow: with season.repeat off the reward period still answers the claims, and the
// closed cycle after it answers 65541 (the box text 4594).
func TestTreasureRepeatOffClosesAfterTheClaimWindow(t *testing.T) {
	treScene(t)
	treRepeatOff(t)
	treAt(t, time.Date(2026, 12, 7, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))) // after season 0's claim window
	g := newTreGateway(t)
	g.open()
	g.login(1001)
	enter := treEnterFor(t, g, treEnterEnded)
	if cirVar(enter, 2) != treasureSeasonEnded {
		t.Fatalf("closed enter with repeat off = %x", enter)
	}
	g.send(treasureCsClassRewardClaim, nil)
	if cirVar(g.readShape(treasureRcClassReward, treResult), 1) != treasureSeasonNotActive {
		t.Fatal("rank claim after the claim window with repeat off not 65541")
	}
}

// TestTreasureSeasonEndPush: a board open when its play window ends gets sc 25 once. The push is armed at the enter for the
// window's end, a stale push (after a re-enter or an exit) writes nothing, and the board's own push goes out on its connection.
func TestTreasureSeasonEndPush(t *testing.T) {
	treScene(t)
	tm := treCaptureTimers(t)
	g := newTreGateway(t)
	g.open()
	g.login(1001)
	treEnterFor(t, g, treEnter) // play window: status 1 with the ring

	end := economy.TreasureCurrent(treTime).End
	if got := tm.armed(); len(got) != 1 || got[0] != end.Sub(treTime) {
		t.Fatalf("armed pushes = %v, want one for the window's end (%s)", got, end.Sub(treTime))
	}
	tm.fire(0)
	if body := g.read(treasureRcSeasonExit); len(body) != 0 {
		t.Fatalf("season-end push body = %x, want empty", body)
	}
	tm.fire(0) // a push that already went out is not sent again
	assertNoReply(t, g.peer)

	treEnterFor(t, g, treEnter) // re-enter: the new push replaces the armed one
	g.exit()                    // the exit stops it
	tm.fire(1)
	assertNoReply(t, g.peer)

	g.login(1001)
	treEnterFor(t, g, treEnter)
	tm.fire(1) // the push armed before the exit: stopped, so nothing
	assertNoReply(t, g.peer)
	tm.fire(2)
	if body := g.read(treasureRcSeasonExit); len(body) != 0 {
		t.Fatalf("second season-end push body = %x, want empty", body)
	}
}

// TestTreasureSeasonInfoIsInSeconds: SeasonInfo carries the play window's start and end as Unix seconds (f23 and f24), so the
// client's date text (it multiplies by 1000) is right.
func TestTreasureSeasonInfoIsInSeconds(t *testing.T) {
	treScene(t)
	g := newTreGateway(t)
	g.open()
	g.login(1001)
	season := cirBytes(treEnterFor(t, g, treEnter), 3)
	cyc := economy.TreasureCurrent(treTime)
	if cirVar(season, 23) != uint64(cyc.Start.Unix()) || cirVar(season, 24) != uint64(cyc.End.Unix()) {
		t.Fatalf("SeasonInfo dates = %d..%d, want %d..%d", cirVar(season, 23), cirVar(season, 24), cyc.Start.Unix(), cyc.End.Unix())
	}
	if end := cirVar(season, 24); end > 1<<32 {
		t.Fatalf("SeasonEndDate %d is not seconds", end)
	}
}
