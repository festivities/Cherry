package economy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

// Circle points, seasons, point steps, missions and cap upgrades (circle_points.go, circle_upgrade.go). The times are
// taken from the season bounds, so the tests hold whatever season_start the data names.

// cptSeasonStart is the first instant of season n as a time.
func cptSeasonStart(n int64) time.Time {
	start, _ := circleSeasonBounds(n)
	return time.UnixMilli(start)
}

// cptFounded is a time before season 1 starts: circles and members formed then are eligible for season 1.
func cptFounded() time.Time { return cptSeasonStart(1).Add(-time.Hour) }

// cptDay is the moment (18:00 JST) of game day d of season 1 (d = 0 is the season's first day).
func cptDay(d int) time.Time {
	return cptSeasonStart(1).Add(12*time.Hour + time.Duration(d)*24*time.Hour)
}

// cptTotal is the circle's points in season n.
func cptTotal(t *testing.T, id, n int64) int64 {
	t.Helper()
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	for i := range store.CircleState.Circles {
		c := &store.CircleState.Circles[i]
		if c.ID == id {
			if s := circleSeasonRec(c, n, false); s != nil {
				return s.Total
			}
		}
	}
	return 0
}

// cptMember is aid's record in season n of circle id (zero when absent).
func cptMember(t *testing.T, id, n int64, aid string) store.CircleMemberSeason {
	t.Helper()
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	for i := range store.CircleState.Circles {
		c := &store.CircleState.Circles[i]
		if c.ID == id {
			_, ms := circlePointsOf(c, n, aid)
			return ms
		}
	}
	return store.CircleMemberSeason{}
}

// cptEarnDay gives aid the day's garden (10), square (10), quest (5), gather (10 x 2) and chat (10 x 1): 55 points,
// under the daily cap of 60.
func cptEarnDay(aid string, now time.Time) {
	for _, kind := range []string{"garden", "square", "quest"} {
		CirclePointEarn(aid, kind, now)
	}
	for i := 0; i < 10; i++ {
		CirclePointEarn(aid, "gather", now)
		CirclePointEarn(aid, "chat", now)
	}
}

// cptCircle is a fixture: a circle led by 3001 (Gems 8000) with members 3002 and 3003, all formed before season 1.
func cptCircle(t *testing.T) int64 {
	t.Helper()
	id, _ := cptCircleDir(t)
	return id
}

// cptCircleDir is cptCircle, and also the directory its ledger and circles.json are written to.
func cptCircleDir(t *testing.T) (int64, string) {
	t.Helper()
	dir := circFixture(t, map[string]int64{"3001": 8000, "3002": 0, "3003": 0, "3004": 0})
	c, err := CreateCircle("ctok-3001", circValidCreate("Points"), cptFounded())
	if err != nil {
		t.Fatal(err)
	}
	for _, aid := range []string{"3002", "3003"} {
		if err := circleJoin("ctok-"+aid, c.ID, cptFounded()); err != nil {
			t.Fatal(err)
		}
	}
	return c.ID, dir
}

// TestCirclePointsDailyCap: a member's day is capped at points.daily_cap, each once-a-day trigger counts once, and a
// new game day starts again. The cap cuts the last credit to what is left.
func TestCirclePointsDailyCap(t *testing.T) {
	id := cptCircle(t)
	d := cptDay(0)
	var got int64
	for _, kind := range []string{"garden", "square", "quest", "quest", "quest", "quest", "quest"} {
		got += CirclePointEarn("3002", kind, d)
	}
	if got != 10+10+5*5 { // garden, square and five quests (quest's per_day is 5)
		t.Fatalf("points before the gathers = %d, want 45", got)
	}
	for i := 0; i < 10; i++ {
		got += CirclePointEarn("3002", "gather", d)
	}
	if got != 60 || cptTotal(t, id, 1) != 60 {
		t.Fatalf("day total = %d (circle %d), want the cap of 60", got, cptTotal(t, id, 1))
	}
	if n := CirclePointEarn("3002", "chat", d); n != 0 {
		t.Fatalf("a credit past the cap gave %d points", n)
	}
	if ms := cptMember(t, id, 1, "3002"); ms.Points != 60 {
		t.Fatalf("member after the cap = %+v", ms)
	}
	if n := CirclePointEarn("3002", "garden", d.Add(time.Hour)); n != 0 {
		t.Fatalf("a second garden credit on the same day gave %d", n)
	}
	if n := CirclePointEarn("3002", "garden", d.Add(24*time.Hour)); n != 10 {
		t.Fatalf("the next game day's garden credit gave %d, want 10", n)
	}
	if n := CirclePointEarn("3004", "garden", d); n != 0 {
		t.Fatalf("a player in no circle earned %d", n)
	}
}

// TestCirclePointsSeasonRollover: a new season starts at its first 06:00 JST boundary with zero points, the old
// season keeps its total, and the season number is read from the clock.
func TestCirclePointsSeasonRollover(t *testing.T) {
	id := cptCircle(t)
	cptEarnDay("3002", cptDay(0))
	if got := cptTotal(t, id, 1); got != 55 {
		t.Fatalf("season 1 total = %d, want 55", got)
	}
	start2 := cptSeasonStart(2)
	if circleSeasonAt(start2.Add(-time.Second)) != 1 || circleSeasonAt(start2) != 2 {
		t.Fatalf("season at the boundary = %d before, %d at", circleSeasonAt(start2.Add(-time.Second)), circleSeasonAt(start2))
	}
	if n := CirclePointEarn("3002", "garden", start2.Add(time.Hour)); n != 10 {
		t.Fatalf("first credit of season 2 gave %d", n)
	}
	if cptTotal(t, id, 2) != 10 || cptTotal(t, id, 1) != 55 {
		t.Fatalf("season totals = %d (season 2) and %d (season 1)", cptTotal(t, id, 2), cptTotal(t, id, 1))
	}
	if ms := cptMember(t, id, 2, "3002"); ms.Points != 10 {
		t.Fatalf("member season 2 = %+v", ms)
	}
}

// TestCirclePointsMidSeasonJoinerEarnsNothing: a member who joined after the season started gets no points that season
// (arts 5386), and can claim no step; the next season counts it.
func TestCirclePointsMidSeasonJoinerEarnsNothing(t *testing.T) {
	circFixture(t, map[string]int64{"3001": 8000, "3002": 0})
	c, err := CreateCircle("ctok-3001", circValidCreate("Late"), cptFounded())
	if err != nil {
		t.Fatal(err)
	}
	if err := circleJoin("ctok-3002", c.ID, cptDay(1)); err != nil {
		t.Fatal(err)
	}
	if n := CirclePointEarn("3002", "garden", cptDay(2)); n != 0 {
		t.Fatalf("mid-season joiner earned %d", n)
	}
	if info := circlePointInfo(0, nil, false); info.AvailSessionAttendYn != 0 {
		t.Fatalf("mid-season joiner shows the season as available: %+v", info)
	}
	// The client draws the claim button when RewardSeq > LastRewardSeq, so a step not reached must not show one.
	if info := circlePointInfo(26, nil, true); info.RewardSeq != 0 || info.LastRewardSeq != 0 || info.CurrentMaxCirclePoint != 100 {
		t.Fatalf("26 points: %+v", info)
	}
	if info := circlePointInfo(350, []int64{1}, true); info.RewardSeq != 2 || info.LastRewardSeq != 1 || info.CurrentMaxCirclePoint != 300 {
		t.Fatalf("350 points, step 1 claimed: %+v", info)
	}
	if info := circlePointInfo(5000, []int64{1, 2, 3, 4}, true); info.RewardSeq != 4 || info.LastRewardSeq != 4 || info.CurrentMaxCirclePoint != 1000 {
		t.Fatalf("all claimed: %+v", info)
	}
	if n := CirclePointEarn("3001", "garden", cptDay(2)); n != 10 {
		t.Fatalf("founder earned %d", n)
	}
	if _, err := CirclePointClaim("3002", 1, cptDay(2)); !isRefusal(err, circleErrGeneric) {
		t.Fatalf("mid-season joiner claimed a step: %v", err)
	}
	if n := CirclePointEarn("3002", "garden", cptSeasonStart(2).Add(time.Hour)); n != 10 {
		t.Fatalf("next season the joiner earned %d, want 10", n)
	}
}

// TestCirclePointStepClaimOnce: a step needs the circle total, pays its Gems once to each member per season, and the
// next season asks for the total again.
func TestCirclePointStepClaimOnce(t *testing.T) {
	cptCircle(t)
	cptEarnDay("3001", cptDay(0))
	cptEarnDay("3002", cptDay(0))
	if _, err := CirclePointClaim("3003", 1, cptDay(0)); err != nil {
		t.Fatalf("member 3003 claims step 1 (total 110): %v", err)
	}
	if _, err := CirclePointClaim("3003", 1, cptDay(0)); !isRefusal(err, circleErrGeneric) {
		t.Fatalf("second claim of step 1: %v", err)
	}
	if gems, err := CirclePointClaim("3001", 1, cptDay(0)); err != nil || gems != 50 {
		t.Fatalf("leader claims step 1: gems %d err %v", gems, err)
	}
	if got := gemsOf(t, "3003"); got != 50 {
		t.Fatalf("member gems = %d, want 50", got)
	}
	if _, err := CirclePointClaim("3001", 2, cptDay(0)); !isRefusal(err, circleErrGeneric) {
		t.Fatalf("step 2 (300 points) claimed at 110: %v", err)
	}
	if _, err := CirclePointClaim("3001", 99, cptDay(0)); !isRefusal(err, circleErrBadRequest) {
		t.Fatalf("unknown step: %v", err)
	}
	if _, err := CirclePointClaim("3004", 1, cptDay(0)); err == nil {
		t.Fatal("a player in no circle claimed a step")
	}
	if _, err := CirclePointClaim("3001", 1, cptSeasonStart(2).Add(time.Hour)); !isRefusal(err, circleErrGeneric) {
		t.Fatalf("season 2 claimed step 1 with no season-2 points: %v", err)
	}
}

// cptSetTotal sets the circle's total in season n (the mission tests need more points than a few days of play give).
func cptSetTotal(t *testing.T, id, n, total int64) {
	t.Helper()
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	for i := range store.CircleState.Circles {
		if c := &store.CircleState.Circles[i]; c.ID == id {
			circleSeasonRec(c, n, true).Total = total
			return
		}
	}
	t.Fatalf("circle %d is missing", id)
}

// cptJoinLate moves aid's join time to an hour after season 1 starts: aid may earn and claim nothing this season.
func cptJoinLate(t *testing.T, aid string) {
	t.Helper()
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	for i := range store.CircleState.Circles {
		for j := range store.CircleState.Circles[i].Members {
			if m := &store.CircleState.Circles[i].Members[j]; m.Aid == aid {
				m.Joined = cptSeasonStart(1).Add(time.Hour).UnixMilli()
			}
		}
	}
}

// TestCircleMissionClaimOnce: the current mission is stage 1 until the circle reaches its step (100 points). Its Gems are
// paid once through the ledger as circle:mission:1:1, the mission's 20 points join the circle, and stage 2 (300 points)
// becomes the current mission, not done.
func TestCircleMissionClaimOnce(t *testing.T) {
	id, dir := cptCircleDir(t)
	if row, ok := CircleMissionCurrent("3002", cptDay(0)); !ok || row.Stage != 1 || row.Target != 100 || row.Done || row.Claimable {
		t.Fatalf("mission before any points = %+v", row)
	}
	if _, _, err := CircleMissionClaim("3002", cptDay(0)); !isRefusal(err, circleErrGeneric) {
		t.Fatalf("claim before the step was reached: %v", err)
	}
	for d := 0; d < 10; d++ { // the garden once a day: 10 points a day, 100 in all
		CirclePointEarn("3002", "garden", cptDay(d))
	}
	if row, _ := CircleMissionCurrent("3002", cptDay(9)); !row.Done || !row.Claimable || row.Stage != 1 {
		t.Fatalf("mission at the step = %+v", row)
	}
	gems, points, err := CircleMissionClaim("3002", cptDay(9))
	if err != nil || gems != 100 || points != 20 {
		t.Fatalf("mission 1 claim: gems %d points %d err %v", gems, points, err)
	}
	if got := gemsOf(t, "3002"); got != 100 {
		t.Fatalf("mission 1 Gems = %d, want 100", got)
	}
	// the fixture's circle creation is the first line; the mission's +100 Gems is the second
	if lines := ledgerLines(t, dir); len(lines) != 2 || !strings.Contains(lines[1], `"delta":100`) || !strings.Contains(lines[1], `"reason":"circle:mission:1:1"`) {
		t.Fatalf("ledger after mission 1 = %q", lines)
	}
	if got := cptTotal(t, id, 1); got != 120 { // 100 from the garden days and the mission's 20 points
		t.Fatalf("circle total after mission 1 = %d, want 120", got)
	}
	if _, _, err := CircleMissionClaim("3002", cptDay(9)); !isRefusal(err, circleErrGeneric) {
		t.Fatalf("mission 1 claimed twice, or stage 2 claimed unmet: %v", err)
	}
	if got := gemsOf(t, "3002"); got != 100 {
		t.Fatalf("a refused claim paid Gems: %d", got)
	}
	if row, ok := CircleMissionCurrent("3002", cptDay(9)); !ok || row.Stage != 2 || row.Target != 300 || row.Done || row.Claimable {
		t.Fatalf("next mission = %+v, want stage 2 not done", row)
	}
}

// TestCircleMissionsAllClaimed: with the circle past every step, the three missions pay in order (450 Gems), and then no
// mission is current and a further claim is refused.
func TestCircleMissionsAllClaimed(t *testing.T) {
	id := cptCircle(t)
	cptSetTotal(t, id, 1, 1000)
	for i, want := range []int64{100, 150, 200} {
		if gems, _, err := CircleMissionClaim("3002", cptDay(0)); err != nil || gems != want {
			t.Fatalf("mission %d claim: gems %d err %v, want %d", i+1, gems, err, want)
		}
	}
	if _, ok := CircleMissionCurrent("3002", cptDay(0)); ok {
		t.Fatal("a mission is still current after all three were claimed")
	}
	if _, _, err := CircleMissionClaim("3002", cptDay(0)); !isRefusal(err, circleErrGeneric) {
		t.Fatalf("claim after all missions: %v", err)
	}
	if got := gemsOf(t, "3002"); got != 450 {
		t.Fatalf("Gems after all missions = %d, want 450", got)
	}
}

// TestCircleMissionLateJoinerGetsNoRewards: a member who joined after the season started sees the same mission, done once
// the circle reaches the step, but cannot claim it (arts 5464 and 5479), and a refused claim leaves the total as it is.
func TestCircleMissionLateJoinerGetsNoRewards(t *testing.T) {
	id := cptCircle(t)
	cptJoinLate(t, "3003")
	cptSetTotal(t, id, 1, 300)
	if row, ok := CircleMissionCurrent("3003", cptDay(0)); !ok || row.Stage != 1 || !row.Done || row.Claimable {
		t.Fatalf("late member's mission = %+v, want stage 1 done and not claimable", row)
	}
	if _, _, err := CircleMissionClaim("3003", cptDay(0)); !isRefusal(err, circleErrGeneric) {
		t.Fatalf("late member claimed: %v", err)
	}
	if got := gemsOf(t, "3003"); got != 0 {
		t.Fatalf("late member Gems = %d, want 0", got)
	}
	if got := cptTotal(t, id, 1); got != 300 {
		t.Fatalf("circle total after a refused claim = %d, want 300", got)
	}
}

// TestCircleUpgradeSpendAndCap: the leader buys each cap step with Gems (ledger and cap move together); a member, a
// billing id, another unit, a wrong step or a short balance is refused with nothing spent; the cap decides who joins.
func TestCircleUpgradeSpendAndCap(t *testing.T) {
	circFixture(t, map[string]int64{"3001": 8000, "3002": 0})
	c, err := CreateCircle("ctok-3001", circValidCreate("Caps"), cptFounded())
	if err != nil {
		t.Fatal(err)
	}
	if err := circleJoin("ctok-3002", c.ID, cptFounded()); err != nil {
		t.Fatal(err)
	}
	now := cptDay(1)
	if CircleCapOf(c.ID) != 30 {
		t.Fatalf("base cap = %d, want 30", CircleCapOf(c.ID))
	}
	row, err := CircleUpgradeInfo("3001")
	if err != nil || row.Code != "cherry_circle_cap_40" || row.To != 40 || row.PriceGems != 1000 || !row.Purchasable || row.Final != 50 {
		t.Fatalf("upgrade row = %+v, %v", row, err)
	}
	if row, _ := CircleUpgradeInfo("3002"); row.Purchasable {
		t.Fatal("a member's upgrade row says purchasable")
	}
	if _, err := BuyCircleUpgrade("3002", "cherry_circle_cap_40", "GEM", "", 1000, now); !isRefusal(err, circleErrRights) {
		t.Fatalf("member bought an upgrade: %v", err)
	}
	for _, c := range []struct {
		code, unit, cp string
		price          int64
	}{
		{"cherry_circle_cap_40", "GEM", "cp-real", 1000},
		{"cherry_circle_cap_40", "CASH", "", 1000},
		{"cherry_circle_cap_50", "GEM", "", 2000},
		{"cherry_circle_cap_40", "GEM", "", 999},
	} {
		if _, err := BuyCircleUpgrade("3001", c.code, c.unit, c.cp, c.price, now); !isRefusal(err, circleErrBadRequest) {
			t.Fatalf("upgrade %+v: %v", c, err)
		}
	}
	if got := gemsOf(t, "3001"); got != 3000 || CircleCapOf(c.ID) != 30 {
		t.Fatalf("refused upgrades changed the balance (%d) or the cap (%d)", got, CircleCapOf(c.ID))
	}
	if bal, err := BuyCircleUpgrade("3001", "cherry_circle_cap_40", "GEM", "", 1000, now); err != nil || bal != 2000 {
		t.Fatalf("buy 40: balance %d err %v", bal, err)
	}
	if CircleCapOf(c.ID) != 40 {
		t.Fatalf("cap after step 1 = %d", CircleCapOf(c.ID))
	}
	if _, err := BuyCircleUpgrade("3001", "cherry_circle_cap_50", "GEM", "", 2000, now); err != nil {
		t.Fatalf("buy 50: %v", err)
	}
	if got, cap := gemsOf(t, "3001"), CircleCapOf(c.ID); got != 0 || cap != 50 {
		t.Fatalf("after both steps: gems %d cap %d", got, cap)
	}
	if _, err := BuyCircleUpgrade("3001", "cherry_circle_cap_50", "GEM", "", 2000, now); !isRefusal(err, circleErrGeneric) {
		t.Fatalf("a step past the final cap: %v", err)
	}
}

// TestCircleCapFillsAndUpgrades: the base cap of 30 refuses the 31st join; the first upgrade lets it in.
func TestCircleCapFillsAndUpgrades(t *testing.T) {
	entries := map[string]int64{"3001": 8000}
	for i := 0; i < 30; i++ {
		entries[fmt.Sprintf("31%02d", i)] = 0
	}
	circFixture(t, entries)
	c, err := CreateCircle("ctok-3001", circValidCreate("Full"), cptFounded())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 29; i++ { // 29 members and the leader: 30
		if err := circleJoin(fmt.Sprintf("ctok-31%02d", i), c.ID, cptFounded()); err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
	}
	if err := circleJoin("ctok-3129", c.ID, cptFounded()); !isRefusal(err, circleErrGeneric) {
		t.Fatalf("31st join at the base cap: %v", err)
	}
	if _, err := BuyCircleUpgrade("3001", "cherry_circle_cap_40", "GEM", "", 1000, cptDay(1)); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if err := circleJoin("ctok-3129", c.ID, cptDay(1)); err != nil {
		t.Fatalf("31st join after the upgrade: %v", err)
	}
	if n := len(circleMembersForTest(t, c.ID)); n != 31 {
		t.Fatalf("members = %d, want 31", n)
	}
}

// circleMembersForTest is the member list of circle id.
func circleMembersForTest(t *testing.T, id int64) []store.CircleMember {
	t.Helper()
	members, _, ok := CircleMembersOf(id)
	if !ok {
		t.Fatalf("circle %d is gone", id)
	}
	return members
}

// TestCircleSeasonStartRules: the data is checked at startup: a bad season start, steps that do not rise, an upgrade
// that does not rise, and a mission that names no point step (or out of order, or negative Gems) are refused.
func TestCircleSeasonStartRules(t *testing.T) {
	base := *ActiveCircleData()
	if err := checkCircleRules(&base); err != nil {
		t.Fatalf("the shipped data fails its own check: %v", err)
	}
	bad := base
	bad.Points.SeasonStart = "2026-13-40"
	if checkCircleRules(&bad) == nil {
		t.Fatal("a bad season start passed")
	}
	bad = base
	bad.Points.Steps = []CircleStep{{Points: 300, Gems: 1}, {Points: 100, Gems: 1}}
	if checkCircleRules(&bad) == nil {
		t.Fatal("steps that fall passed")
	}
	bad = base
	bad.Upgrades = slices.Clone(base.Upgrades)
	bad.Upgrades[1].To = 40
	if checkCircleRules(&bad) == nil {
		t.Fatal("an upgrade that does not rise passed")
	}
	bad = base
	bad.Missions = []CircleMission{{Stage: 9, Gems: 1, Points: 1}}
	if checkCircleRules(&bad) == nil {
		t.Fatal("a stage that is not a point step passed")
	}
	bad.Missions = []CircleMission{{Stage: 2, Gems: 1}, {Stage: 2, Gems: 1}}
	if checkCircleRules(&bad) == nil {
		t.Fatal("stages that do not rise passed")
	}
	bad.Missions = []CircleMission{{Stage: 1, Gems: -1}}
	if checkCircleRules(&bad) == nil {
		t.Fatal("negative Gems passed")
	}
	if err := checkCircleRules(&base); err != nil {
		t.Fatalf("the shipped missions fail the check: %v", err)
	}
}

// TestCircleHTTPHooksReportSavedChanges: the membership changes made over HTTP are reported after they are saved, and a
// refused change reports nothing.
func TestCircleHTTPHooksReportSavedChanges(t *testing.T) {
	circFixture(t, map[string]int64{"3001": 8000, "3002": 0, "3003": 0})
	var events []CircleRoomEvent
	prev := CircleRoomHook
	CircleRoomHook = func(ev CircleRoomEvent) { events = append(events, ev) }
	t.Cleanup(func() { CircleRoomHook = prev })
	c, err := CreateCircle("ctok-3001", circValidCreate("Hooks"), cptFounded())
	if err != nil {
		t.Fatal(err)
	}
	if err := circleJoin("ctok-3002", c.ID, cptDay(0)); err != nil {
		t.Fatal(err)
	}
	if err := circleJoin("ctok-3002", c.ID, cptDay(0)); err == nil || len(events) != 1 {
		t.Fatalf("a refused join reported: events %+v err %v", events, err)
	}
	if err := LeaveCircle("ctok-3002", c.ID, cptDay(0)); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !slices.Equal(events[1].Left, []string{"3002"}) || events[1].Closed {
		t.Fatalf("leave event = %+v", events)
	}
	if err := circleJoin("ctok-3003", c.ID, cptDay(0)); err != nil {
		t.Fatal(err)
	}
	if err := CloseCircle("ctok-3001", c.ID); err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if !last.Closed || !slices.Equal(last.Left, []string{"3001", "3003"}) {
		t.Fatalf("close event = %+v, want the leader and 3003 out", last)
	}
}

// TestCircleRankingAndHallOfFame: the season's ranking orders circles by points (then id); the hall of fame of the next
// season lists the previous season's top three.
func TestCircleRankingAndHallOfFame(t *testing.T) {
	circFixture(t, map[string]int64{"3001": 8000, "3002": 5000, "3003": 0})
	a, err := CreateCircle("ctok-3001", circValidCreate("Alpha"), cptFounded())
	if err != nil {
		t.Fatal(err)
	}
	b, err := CreateCircle("ctok-3002", circValidCreate("Beta"), cptFounded())
	if err != nil {
		t.Fatal(err)
	}
	cptEarnDay("3001", cptDay(0))
	CirclePointEarn("3002", "garden", cptDay(0))
	store.CirclesMu.Lock()
	ranked := circleRanking(&store.CircleState, 1, false)
	store.CirclesMu.Unlock()
	if len(ranked) != 2 || ranked[0].c.ID != a.ID || ranked[1].c.ID != b.ID || ranked[0].pts != 55 {
		t.Fatalf("season 1 ranking = %+v", ranked)
	}
	store.CirclesMu.Lock()
	hall := circleHallView(&store.CircleState, cptSeasonStart(2).Add(time.Hour))
	store.CirclesMu.Unlock()
	if hall.SeasonID != 1 || len(hall.EntryList) != 2 || hall.EntryList[0].CircleID != circleIDText(a.ID) || hall.EntryList[0].TotalGainPoint != 55 {
		t.Fatalf("hall of fame = %+v", hall)
	}
	if h := circleHallView(&store.CircleState, cptDay(0)); h.SeasonID != 0 || len(h.EntryList) != 0 {
		t.Fatalf("season 1 has a previous season: %+v", h)
	}
}

// TestCircleRankRowsCarryPointsAndRank: each row of a rank reply carries its circle's points and rank (the rows follow
// circleRanking; a circle with no points is in no row), the viewer's circle is a row of the same shape ({} outside a
// circle), and neither point key is at the top level. The season reply adds the banner keys, with the season's end as
// gainEndDate; the gacha name is blank while the circle page offers no gacha.
func TestCircleRankRowsCarryPointsAndRank(t *testing.T) {
	circFixture(t, map[string]int64{"3001": 8000, "3002": 0, "3004": 8000, "3005": 0, "3006": 8000, "3007": 0})
	alpha, err := CreateCircle("ctok-3001", circValidCreate("Alpha"), cptFounded())
	if err != nil {
		t.Fatal(err)
	}
	beta, err := CreateCircle("ctok-3004", circValidCreate("Beta"), cptFounded())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateCircle("ctok-3006", circValidCreate("Gamma"), cptFounded()); err != nil {
		t.Fatal(err)
	}
	if err := circleJoin("ctok-3002", alpha.ID, cptFounded()); err != nil {
		t.Fatal(err)
	}
	if err := circleJoin("ctok-3005", beta.ID, cptFounded()); err != nil {
		t.Fatal(err)
	}
	cptEarnDay("3001", cptDay(0))                // Alpha: 55 points
	CirclePointEarn("3004", "garden", cptDay(0)) // Beta: 10 points; Gamma has none

	store.CirclesMu.Lock()
	book := &store.CircleState
	total := circleRankView("3002", book, 1, true)
	season := circleRankView("3002", book, 1, false)
	gammaSeason := circleRankView("3006", book, 1, false)
	outsiderSeason := circleRankView("3007", book, 1, false)
	banner := circleRankSeasonView("3002", book, cptDay(0))
	store.CirclesMu.Unlock()

	decode := func(v any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	top := decode(season)
	for _, k := range []string{"myCirclePoint", "myCircleRank"} {
		if _, ok := top[k]; ok {
			t.Fatalf("top level carries %s: %v", k, top)
		}
	}
	rows, ok := top["topRankCircleList"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("rows = %v, want Alpha and Beta only", top["topRankCircleList"])
	}
	for i, w := range []struct {
		id, name string
		pts      float64
		rank     string
	}{{circleIDText(alpha.ID), "Alpha", 55, "1"}, {circleIDText(beta.ID), "Beta", 10, "2"}} {
		row := rows[i].(map[string]any)
		if row["circleId"] != w.id || row["circleName"] != w.name || row["myCirclePoint"] != w.pts || row["myCircleRank"] != w.rank {
			t.Fatalf("row %d = %v, want %+v", i, row, w)
		}
		for _, k := range []string{"logoImgPath", "memberCount", "maxMemberCount", "masterAid"} {
			if _, ok := row[k]; !ok {
				t.Fatalf("row %d lacks the circle key %s: %v", i, k, row)
			}
		}
	}
	if first := decode(total)["topRankCircleList"].([]any)[0].(map[string]any); first["myCirclePoint"] != float64(55) {
		t.Fatalf("total row = %v, want Alpha's 55 points", first)
	}

	if mine := decode(season)["myCircle"].(map[string]any); mine["circleName"] != "Alpha" || mine["myCirclePoint"] != float64(55) || mine["myCircleRank"] != "1" {
		t.Fatalf("member of Alpha: myCircle = %v", mine)
	}
	if mine := decode(gammaSeason)["myCircle"].(map[string]any); mine["circleName"] != "Gamma" || mine["myCirclePoint"] != float64(0) || mine["myCircleRank"] != "" {
		t.Fatalf("member of Gamma (no points): myCircle = %v, want 0 points and rank \"\"", mine)
	}
	if mine, ok := decode(outsiderSeason)["myCircle"].(map[string]any); !ok || len(mine) != 0 {
		t.Fatalf("outsider's myCircle = %v, want {}", mine)
	}

	start, end := circleSeasonBounds(1)
	sea := decode(banner)
	wantGacha := ""
	if ActiveCircleData().Gacha.Enabled {
		wantGacha = ActiveGacha().Name
	}
	if sea["seasonId"] != float64(1) || sea["seasonName"] != circleSeasonName(1) || sea["seasonStartDate"] != circleMS(start) ||
		sea["gainEndDate"] != circleMS(end) || sea["seasonEndDate"] != circleMS(end) || sea["gachaName"] != wantGacha {
		t.Fatalf("season banner = %v, want season 1 ending at %d", sea, end)
	}
	if sea["bannerImagePath"] != "" || sea["gachaTicket"] != float64(0) || sea["gachaId"] != float64(0) || sea["gachaImagePath"] != "" {
		t.Fatalf("season banner blanks = %v", sea)
	}

	gacha := &ActiveCircleData().Gacha.Enabled
	prev := *gacha
	t.Cleanup(func() { *gacha = prev })
	*gacha = false
	store.CirclesMu.Lock()
	off := circleRankSeasonView("3002", &store.CircleState, cptDay(0))
	store.CirclesMu.Unlock()
	if off.GachaName != "" {
		t.Fatalf("gacha name with the gacha off = %q, want blank", off.GachaName)
	}
}
