package economy

import (
	"fmt"
	"log"
	"slices"
	"time"

	"cherry/internal/store"
)

// Circle points, seasons, point steps and missions (spec circle-spec.md 4 rows 24-28, 9.7; LPN msgids 32-35 and 47-53).
// A season is SeasonDays game days (06:00 JST) and the first one starts on SeasonStart. Members earn points from the
// triggers in data/circle.json; the circle's total and each member's record are kept per season in the circle book.
// Every number here is Cherry-made operator data; the meanings of the reply fields are the client's names [I].

// circleJST is Japan Standard Time, the zone of the game day (06:00 JST).
var circleJST = time.FixedZone("JST", 9*3600)

// circleSeasonStartKey returns the game-day key (AttendDayKey) of the day named by s ("2006-01-02"), which starts at
// 06:00 JST.
func circleSeasonStartKey(s string) (int64, error) {
	t, err := time.ParseInLocation("2006-01-02", s, circleJST)
	if err != nil {
		return 0, fmt.Errorf("points.season_start %q: %w", s, err)
	}
	return AttendDayKey(t.Add(6 * time.Hour)), nil
}

// circleSeasonAt returns the season number at t. Times before the first season start are season 1.
func circleSeasonAt(t time.Time) int64 {
	d := ActiveCircleData().Points
	start, _ := circleSeasonStartKey(d.SeasonStart)
	key := AttendDayKey(t)
	if key < start {
		return 1
	}
	return (key-start)/d.SeasonDays + 1
}

// circleSeasonBounds returns season n's first and last instants in epoch milliseconds (06:00 JST boundaries).
func circleSeasonBounds(n int64) (startMs, endMs int64) {
	d := ActiveCircleData().Points
	start, _ := circleSeasonStartKey(d.SeasonStart)
	first := start + (n-1)*d.SeasonDays
	return dayStartMs(first), dayStartMs(first + d.SeasonDays)
}

// dayStartMs is the instant a game day starts (06:00 JST) in epoch milliseconds.
func dayStartMs(key int64) int64 { return (key*86400 - attendBoundaryShift) * 1000 }

// circleSeasonName is the season's title (points.season_name with the number).
func circleSeasonName(n int64) string { return fmt.Sprintf(ActiveCircleData().Points.SeasonName, n) }

// circleEligible reports whether a member may earn in season n: it joined before the season started (arts 5386 [S-str]).
func circleEligible(m store.CircleMember, season int64) bool {
	start, _ := circleSeasonBounds(season)
	return m.Joined < start
}

// circleSeasonRec returns the circle's record of season n. It is made when create is set; otherwise nil stands for a
// season with no points.
func circleSeasonRec(c *store.Circle, n int64, create bool) *store.CircleSeason {
	for i := range c.Seasons {
		if c.Seasons[i].Season == n {
			return &c.Seasons[i]
		}
	}
	if !create {
		return nil
	}
	c.Seasons = append(c.Seasons, store.CircleSeason{Season: n})
	return &c.Seasons[len(c.Seasons)-1]
}

// circleMemberRec returns aid's record in season s, made when create is set (nil when there is none and create is not set).
func circleMemberRec(s *store.CircleSeason, aid string, create bool) *store.CircleMemberSeason {
	if s == nil {
		return nil
	}
	if s.Members == nil {
		if !create {
			return nil
		}
		s.Members = map[string]*store.CircleMemberSeason{}
	}
	ms := s.Members[aid]
	if ms == nil && create {
		ms = &store.CircleMemberSeason{}
		s.Members[aid] = ms
	}
	return ms
}

// circlePointsOf returns the circle's total and aid's record for season n, read-only (zero values when absent).
func circlePointsOf(c *store.Circle, n int64, aid string) (total int64, ms store.CircleMemberSeason) {
	s := circleSeasonRec(c, n, false)
	if s == nil {
		return 0, store.CircleMemberSeason{}
	}
	if m := circleMemberRec(s, aid, false); m != nil {
		ms = *m
	}
	return s.Total, ms
}

// CirclePointEarn credits one event of kind trigger to aid's circle in the current season. The credit is the trigger's
// points, cut to what is left of the member's daily cap (points.daily_cap per game day), and it is refused past the
// trigger's per_day count. A member who joined after the season started, or who is in no circle, gets nothing. It
// returns the points given.
func CirclePointEarn(aid, kind string, now time.Time) int64 {
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	if circleOf(&store.CircleState, aid) < 0 {
		return 0
	}
	// ponytail: the whole book is cloned and saved per credit (members only); per-circle copies if circles multiply.
	next := store.CircleState.Clone()
	got, changed := creditCirclePoints(&next, aid, kind, now)
	if !changed {
		return 0
	}
	if err := store.SaveCirclesLocked(next); err != nil {
		log.Printf("cherry: circle points save failed aid=%s kind=%s err=%v", aid, kind, err)
		return 0
	}
	store.CircleState = next
	return got
}

// creditCirclePoints applies one trigger to book b and reports the points and whether anything changed.
func creditCirclePoints(b *store.CircleBook, aid, kind string, now time.Time) (int64, bool) {
	d := ActiveCircleData().Points
	t, ok := d.Triggers[kind]
	ci := circleOf(b, aid)
	if !ok || ci < 0 {
		return 0, false
	}
	c := &b.Circles[ci]
	season := circleSeasonAt(now)
	if !circleEligible(c.Members[memberIndex(c, aid)], season) {
		return 0, false
	}
	day := AttendDayKey(now)
	s := circleSeasonRec(c, season, true)
	ms := circleMemberRec(s, aid, true)
	if ms.Day != day {
		ms.Day, ms.DayPts, ms.DayKinds = day, 0, nil
	}
	if t.PerDay > 0 && ms.DayKinds[kind] >= t.PerDay {
		return 0, false
	}
	if ms.DayKinds == nil {
		ms.DayKinds = map[string]int64{}
	}
	ms.DayKinds[kind]++
	pts := max(min(t.Points, d.DailyCap-ms.DayPts), 0)
	ms.Points += pts
	ms.DayPts += pts
	s.Total += pts
	return pts, true
}

// CirclePointsForQuest credits the circle triggers of one quest progress key: the Garden visit, the Square visit and
// gathering map to their kinds, and a completed daily quest (done) is the "quest" kind.
func CirclePointsForQuest(aid, key string, done bool, now time.Time) {
	if kind, ok := circleQuestKinds[key]; ok {
		CirclePointEarn(aid, kind, now)
	}
	if done {
		CirclePointEarn(aid, "quest", now)
	}
}

// CirclePointInfoView is the point state the client reads as PointInfo (appendix C of the spec). The names are the
// client's: RewardSeq is the steps the circle reached, LastRewardSeq the steps this member claimed (RewardSeq above it
// draws the claim button for LastRewardSeq+1, InitCirclePoint 0x25ce674 [S]), Max the step count, and the CurrentMax and
// BeforeMax values are the targets around the stage being worked on (LastRewardSeq+1).
type CirclePointInfoView struct {
	RewardSeq, LastRewardSeq, MaxRewardSeq      int64
	LastCirclePoint, CurrentCirclePoint         int64
	CurrentMaxCirclePoint, BeforeMaxCirclePoint int64
	SessionState, AvailSessionAttendYn          int64
}

// CirclePointInfoOf returns aid's circle point state now (all zero for a player in no circle).
func CirclePointInfoOf(aid string, now time.Time) CirclePointInfoView {
	var v CirclePointInfoView
	_ = circleBookTx(false, func(b *store.CircleBook) error {
		ci := circleOf(b, aid)
		if ci < 0 {
			return nil
		}
		c := &b.Circles[ci]
		season := circleSeasonAt(now)
		total, ms := circlePointsOf(c, season, aid)
		v = circlePointInfo(total, ms.Steps, circleEligible(c.Members[memberIndex(c, aid)], season))
		v.SessionState = 1
		return nil
	})
	return v
}

// circlePointInfo derives the point state from the circle total, the steps the viewer claimed (in claim order; the client
// always claims LastRewardSeq+1) and whether the viewer earns this season.
func circlePointInfo(total int64, claimed []int64, eligible bool) CirclePointInfoView {
	steps := ActiveCircleData().Points.Steps
	v := CirclePointInfoView{MaxRewardSeq: int64(len(steps)), LastCirclePoint: total, CurrentCirclePoint: total}
	for i, s := range steps {
		if total >= s.Points {
			v.RewardSeq = int64(i + 1)
		}
	}
	for _, seq := range claimed {
		if seq == v.LastRewardSeq+1 {
			v.LastRewardSeq = seq
		}
	}
	if stage := min(v.LastRewardSeq+1, v.MaxRewardSeq); stage > 0 {
		v.CurrentMaxCirclePoint = steps[stage-1].Points
		if stage >= 2 {
			v.BeforeMaxCirclePoint = steps[stage-2].Points
		}
	}
	if eligible {
		v.AvailSessionAttendYn = 1
	}
	return v
}

// CirclePointClaim pays aid the Gems of point step seq of the current season: the circle total must have reached the
// step, and each member claims each step once per season. A member who joined mid-season may not claim (5386). It
// returns the Gems paid.
func CirclePointClaim(aid string, seq int64, now time.Time) (int64, error) {
	var gems int64
	err := circleTxAid(aid, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleOf(b, aid)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		season := circleSeasonAt(now)
		steps := ActiveCircleData().Points.Steps
		if seq < 1 || seq > int64(len(steps)) {
			return nil, circleRefuse(circleErrBadRequest)
		}
		if !circleEligible(c.Members[memberIndex(c, aid)], season) {
			return nil, circleRefuse(circleErrGeneric)
		}
		s := circleSeasonRec(c, season, false)
		if s == nil || s.Total < steps[seq-1].Points {
			return nil, circleRefuse(circleErrGeneric)
		}
		ms := circleMemberRec(s, aid, true)
		if slices.Contains(ms.Steps, seq) {
			return nil, circleRefuse(circleErrGeneric)
		}
		step := steps[seq-1]
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: step.Gems}, fmt.Sprintf("circle:point:%d:%d", season, seq))
		if err != nil {
			return nil, circleRefuse(circleErrGeneric)
		}
		ms.Steps = append(ms.Steps, seq)
		gems = step.Gems
		return lines, nil
	})
	return gems, err
}

// CircleMissionRow is the current mission of one member as the client reads a QuestInfo: the point step it names (Stage)
// and that step's points (Target), the season's bounds, its Gems and points, whether the circle has reached the step
// (Done) and whether this member may claim it now (Claimable: done, eligible and not claimed yet).
type CircleMissionRow struct {
	Stage, Target  int64
	StartMs, EndMs int64
	Gems, Points   int64
	Done           bool
	Claimable      bool
}

// circleMissionCurrent returns aid's current mission: the first one this member has not claimed this season. ok is false
// for a player in no circle and for a member who has claimed every mission.
func circleMissionCurrent(b *store.CircleBook, aid string, now time.Time) (CircleMissionRow, bool) {
	ci := circleOf(b, aid)
	if ci < 0 {
		return CircleMissionRow{}, false
	}
	c := &b.Circles[ci]
	season := circleSeasonAt(now)
	total, ms := circlePointsOf(c, season, aid)
	missions := ActiveCircleData().Missions
	if ms.Missions >= int64(len(missions)) {
		return CircleMissionRow{}, false
	}
	m := missions[ms.Missions]
	target := ActiveCircleData().Points.Steps[m.Stage-1].Points
	start, end := circleSeasonBounds(season)
	done := total >= target
	return CircleMissionRow{Stage: m.Stage, Target: target, StartMs: start, EndMs: end, Gems: m.Gems, Points: m.Points,
		Done: done, Claimable: done && circleEligible(c.Members[memberIndex(c, aid)], season)}, true
}

// CircleMissionCurrent is circleMissionCurrent for LPN (aid's circle, locked here).
func CircleMissionCurrent(aid string, now time.Time) (row CircleMissionRow, ok bool) {
	_ = circleBookTx(false, func(b *store.CircleBook) error {
		row, ok = circleMissionCurrent(b, aid, now)
		return nil
	})
	return row, ok
}

// CircleMissionClaim pays the Gems of aid's current mission once the circle has reached its point step, then moves the
// member to the next mission. Each mission pays once per member per season, through the ledger as
// circle:mission:<season>:<stage>; the circle total gets the mission's points. A member who joined mid-season gets no
// rewards (arts 5464, 5479). It returns the Gems and points paid.
func CircleMissionClaim(aid string, now time.Time) (gems, points int64, err error) {
	err = circleTxAid(aid, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleOf(b, aid)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		season := circleSeasonAt(now)
		if !circleEligible(c.Members[memberIndex(c, aid)], season) {
			return nil, circleRefuse(circleErrGeneric)
		}
		missions := ActiveCircleData().Missions
		s := circleSeasonRec(c, season, true)
		ms := circleMemberRec(s, aid, true)
		if ms.Missions >= int64(len(missions)) {
			return nil, circleRefuse(circleErrGeneric)
		}
		m := missions[ms.Missions]
		if s.Total < ActiveCircleData().Points.Steps[m.Stage-1].Points {
			return nil, circleRefuse(circleErrGeneric)
		}
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: m.Gems}, fmt.Sprintf("circle:mission:%d:%d", season, m.Stage))
		if err != nil {
			return nil, circleRefuse(circleErrGeneric)
		}
		ms.Missions++
		s.Total += m.Points
		gems, points = m.Gems, m.Points
		return lines, nil
	})
	return gems, points, err
}
